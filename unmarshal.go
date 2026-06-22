package protojsonx

// Decoding strategy:
//
// Unmarshal is a small JSON parser specialized for the field shapes accepted by
// table compilation. It uses MessageTable metadata to map a JSON key directly
// to a generated struct offset, then writes the decoded value with unsafe
// pointer arithmetic. This avoids protobuf reflection in the hot path while
// still preserving protojson-like behavior for important edge cases:
// unknown-field discarding validates the skipped JSON value, duplicate known
// fields are rejected, null field values clear to the protobuf default, and a
// successful decode clears fields that were omitted from reused target structs.
//
// ZeroCopy only applies to unescaped JSON strings. Escaped strings are decoded
// into new byte slices because their in-memory bytes differ from the input.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"time"
	"unsafe"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type Allocator interface {
	New(t reflect.Type) reflect.Value
}

type UnmarshalOptions struct {
	DiscardUnknown bool
	ZeroCopy       bool
	Allocator      Allocator
}

func allocate(t reflect.Type, opts UnmarshalOptions) reflect.Value {
	if opts.Allocator != nil {
		return opts.Allocator.New(t)
	}
	return reflect.New(t)
}

// decBuffer is a cursor over the input JSON. The parser is intentionally
// minimal: callers choose the expected token based on the compiled field type.
type decBuffer struct {
	data  []byte
	off   int
	depth int
}

// skipWhitespace advances over JSON whitespace without allocation.
func (d *decBuffer) skipWhitespace() {
	for d.off < len(d.data) {
		c := d.data[d.off]
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			d.off++
		} else {
			break
		}
	}
}

// readStringBytes returns the decoded bytes for a JSON string. When the string
// has no escapes, the returned slice aliases d.data; when escapes are present,
// it returns newly allocated decoded bytes.
func (d *decBuffer) readStringBytes() ([]byte, error) {
	d.skipWhitespace()
	if d.off >= len(d.data) || d.data[d.off] != '"' {
		return nil, errors.New("expected string")
	}
	d.off++
	start := d.off
	hasEscapes := false
	for d.off < len(d.data) {
		c := d.data[d.off]
		if c == '"' {
			s := d.data[start:d.off]
			d.off++
			if hasEscapes {
				return unescapeString(s)
			}
			return s, nil
		}
		if c == '\\' {
			hasEscapes = true
			if d.off+1 >= len(d.data) {
				return nil, errors.New("unterminated escape sequence")
			}
			d.off += 2
		} else {
			if c < 0x20 {
				return nil, errors.New("invalid control character in string")
			}
			d.off++
		}
	}
	return nil, errors.New("unterminated string")
}

// unescapeString handles common JSON escapes inline and falls back to the
// standard JSON decoder only when \u handling is required.
func unescapeString(s []byte) ([]byte, error) {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			out = append(out, s[i])
			continue
		}
		i++
		if i >= len(s) {
			return nil, errors.New("unterminated escape sequence")
		}
		switch s[i] {
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case 'b':
			out = append(out, '\b')
		case 'f':
			out = append(out, '\f')
		case '\\':
			out = append(out, '\\')
		case '/':
			out = append(out, '/')
		case '"':
			out = append(out, '"')
		case 'u':
			return unescapeUnicodeString(s)
		default:
			return nil, errors.New("invalid escape sequence")
		}
	}
	return out, nil
}

// unescapeUnicodeString delegates Unicode escape and surrogate-pair handling
// to encoding/json. This is colder than simple escapes but keeps semantics
// correct for non-ASCII escaped input.
func unescapeUnicodeString(s []byte) ([]byte, error) {
	quoted := make([]byte, 0, len(s)+2)
	quoted = append(quoted, '"')
	quoted = append(quoted, s...)
	quoted = append(quoted, '"')
	var out string
	if err := json.Unmarshal(quoted, &out); err != nil {
		return nil, err
	}
	return []byte(out), nil
}

func (d *decBuffer) readInt32() (int32, error) {
	token, err := d.readJSONNumberToken()
	if err != nil {
		return 0, err
	}
	v, err := strconv.ParseInt(string(token), 10, 32)
	return int32(v), err
}

func (d *decBuffer) readInt64() (int64, error) {
	d.skipWhitespace()
	if d.off < len(d.data) && d.data[d.off] == '"' {
		s, err := d.readStringBytes()
		if err != nil {
			return 0, err
		}
		return strconv.ParseInt(string(s), 10, 64)
	}
	token, err := d.readJSONNumberToken()
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(string(token), 10, 64)
}

func (d *decBuffer) readUint32() (uint32, error) {
	token, err := d.readJSONNumberToken()
	if err != nil {
		return 0, err
	}
	v, err := strconv.ParseUint(string(token), 10, 32)
	return uint32(v), err
}

func (d *decBuffer) readUint64() (uint64, error) {
	d.skipWhitespace()
	if d.off < len(d.data) && d.data[d.off] == '"' {
		s, err := d.readStringBytes()
		if err != nil {
			return 0, err
		}
		return strconv.ParseUint(string(s), 10, 64)
	}
	token, err := d.readJSONNumberToken()
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(string(token), 10, 64)
}

func (d *decBuffer) readFloat32() (float32, error) {
	d.skipWhitespace()
	if d.off < len(d.data) && d.data[d.off] == '"' {
		s, err := d.readStringBytes()
		if err != nil {
			return 0, err
		}
		v, err := parseFloatLiteral(unsafeString(s), 32)
		return float32(v), err
	}
	token, err := d.readJSONNumberToken()
	if err != nil {
		return 0, err
	}
	v, err := strconv.ParseFloat(string(token), 32)
	return float32(v), err
}

func (d *decBuffer) readFloat64() (float64, error) {
	d.skipWhitespace()
	if d.off < len(d.data) && d.data[d.off] == '"' {
		s, err := d.readStringBytes()
		if err != nil {
			return 0, err
		}
		return parseFloatLiteral(unsafeString(s), 64)
	}
	token, err := d.readJSONNumberToken()
	if err != nil {
		return 0, err
	}
	return strconv.ParseFloat(string(token), 64)
}

func (d *decBuffer) readJSONNumberToken() ([]byte, error) {
	d.skipWhitespace()
	start := d.off

	if d.off < len(d.data) && d.data[d.off] == '-' {
		d.off++
	}
	if d.off >= len(d.data) {
		return nil, errors.New("invalid JSON number")
	}
	if d.data[d.off] == '0' {
		d.off++
	} else if d.data[d.off] >= '1' && d.data[d.off] <= '9' {
		d.off++
		for d.off < len(d.data) && d.data[d.off] >= '0' && d.data[d.off] <= '9' {
			d.off++
		}
	} else {
		return nil, errors.New("expected number")
	}

	if d.off < len(d.data) && d.data[d.off] == '.' {
		d.off++
		if d.off >= len(d.data) || d.data[d.off] < '0' || d.data[d.off] > '9' {
			return nil, errors.New("invalid JSON number")
		}
		for d.off < len(d.data) && d.data[d.off] >= '0' && d.data[d.off] <= '9' {
			d.off++
		}
	}

	if d.off < len(d.data) && (d.data[d.off] == 'e' || d.data[d.off] == 'E') {
		d.off++
		if d.off < len(d.data) && (d.data[d.off] == '+' || d.data[d.off] == '-') {
			d.off++
		}
		if d.off >= len(d.data) || d.data[d.off] < '0' || d.data[d.off] > '9' {
			return nil, errors.New("invalid JSON number")
		}
		for d.off < len(d.data) && d.data[d.off] >= '0' && d.data[d.off] <= '9' {
			d.off++
		}
	}

	if d.off < len(d.data) && !isJSONValueTerminator(d.data[d.off]) {
		d.off++
		for d.off < len(d.data) && !isJSONValueTerminator(d.data[d.off]) {
			d.off++
		}
		return nil, errors.New("invalid JSON number")
	}
	if d.off == start {
		return nil, errors.New("expected number")
	}
	return d.data[start:d.off], nil
}

func isJSONValueTerminator(c byte) bool {
	return c == ',' || c == '}' || c == ']' || c == ' ' || c == '\t' || c == '\r' || c == '\n'
}

func parseFloatLiteral(s string, bitSize int) (float64, error) {
	switch s {
	case "NaN", "Infinity", "-Infinity":
		return strconv.ParseFloat(s, bitSize)
	default:
		return 0, errors.New("expected special floating-point string")
	}
}

// readBool accepts only JSON boolean literals and leaves token boundary checks
// to the enclosing object/array parser.
func (d *decBuffer) readBool() (bool, error) {
	d.skipWhitespace()
	if d.off+4 <= len(d.data) && string(d.data[d.off:d.off+4]) == "true" {
		d.off += 4
		return true, nil
	}
	if d.off+5 <= len(d.data) && string(d.data[d.off:d.off+5]) == "false" {
		d.off += 5
		return false, nil
	}
	return false, errors.New("expected boolean")
}

// readNull consumes a JSON null literal when present. Field decoders use this
// to implement protojson's "null means unset/default" behavior.
func (d *decBuffer) readNull() bool {
	d.skipWhitespace()
	if d.off+4 <= len(d.data) && string(d.data[d.off:d.off+4]) == "null" {
		d.off += 4
		return true
	}
	return false
}

func (d *decBuffer) isObject() bool {
	d.skipWhitespace()
	return d.off < len(d.data) && d.data[d.off] == '{'
}

func (d *decBuffer) unmarshalWrapper(opts UnmarshalOptions, inst *fieldInstruction, fieldPtr unsafe.Pointer, readVal func() error) error {
	subMsgPtrPtr := (*unsafe.Pointer)(fieldPtr)
	if *subMsgPtrPtr == nil {
		newVal := allocate(inst.elemType, opts)
		*subMsgPtrPtr = unsafe.Pointer(newVal.Pointer())
	}
	if d.isObject() {
		return d.parseObject(func(key []byte) error {
			if string(key) == "value" {
				return readVal()
			}
			if opts.DiscardUnknown {
				return d.skipValue()
			}
			return errors.New("unknown field in wrapper: " + string(key))
		})
	}
	return readVal()
}

// skipValue validates and skips one complete JSON value. It is used only for
// DiscardUnknown, so it favors correctness over micro-optimizing the hot path.
func (d *decBuffer) skipValue() error {
	d.skipWhitespace()
	if d.off >= len(d.data) {
		return errors.New("unexpected EOF")
	}
	switch d.data[d.off] {
	case '{':
		d.depth++
		if d.depth > 100 {
			return errors.New("exceeded maximum recursion depth")
		}
		d.off++
		first := true
		for {
			d.skipWhitespace()
			if d.off >= len(d.data) {
				d.depth--
				return errors.New("unexpected EOF")
			}
			if d.data[d.off] == '}' {
				d.off++
				d.depth--
				return nil
			}
			if !first {
				if d.data[d.off] != ',' {
					d.depth--
					return errors.New("expected ','")
				}
				d.off++
				d.skipWhitespace()
			}
			first = false
			if _, err := d.readStringBytes(); err != nil {
				d.depth--
				return err
			}
			d.skipWhitespace()
			if d.off >= len(d.data) || d.data[d.off] != ':' {
				d.depth--
				return errors.New("expected ':'")
			}
			d.off++
			if err := d.skipValue(); err != nil {
				d.depth--
				return err
			}
		}
	case '[':
		d.depth++
		if d.depth > 100 {
			return errors.New("exceeded maximum recursion depth")
		}
		d.off++
		first := true
		for {
			d.skipWhitespace()
			if d.off >= len(d.data) {
				d.depth--
				return errors.New("unexpected EOF")
			}
			if d.data[d.off] == ']' {
				d.off++
				d.depth--
				return nil
			}
			if !first {
				if d.data[d.off] != ',' {
					d.depth--
					return errors.New("expected ','")
				}
				d.off++
				d.skipWhitespace()
			}
			first = false
			if err := d.skipValue(); err != nil {
				d.depth--
				return err
			}
		}
	case '"':
		_, err := d.readStringBytes()
		return err
	default:
		start := d.off
		for d.off < len(d.data) {
			c := d.data[d.off]
			if c == ',' || c == '}' || c == ']' || c == ' ' || c == '\t' || c == '\r' || c == '\n' {
				break
			}
			d.off++
		}
		if d.off == start {
			return errors.New("expected value")
		}
		if !json.Valid(d.data[start:d.off]) {
			return errors.New("invalid JSON value")
		}
		return nil
	}
}

// parseObject parses a JSON object and delegates each key's value to fn. fn is
// responsible for consuming exactly one value.
func (d *decBuffer) parseObject(fn func(key []byte) error) error {
	d.skipWhitespace()
	if d.off >= len(d.data) || d.data[d.off] != '{' {
		return errors.New("expected '{'")
	}
	d.depth++
	if d.depth > 100 {
		return errors.New("exceeded maximum recursion depth")
	}
	d.off++

	first := true
	for {
		d.skipWhitespace()
		if d.off >= len(d.data) {
			d.depth--
			return errors.New("unexpected EOF")
		}
		if d.data[d.off] == '}' {
			d.off++
			d.depth--
			return nil
		}
		if !first {
			if d.data[d.off] != ',' {
				d.depth--
				return errors.New("expected ','")
			}
			d.off++
			d.skipWhitespace()
		}
		first = false

		key, err := d.readStringBytes()
		if err != nil {
			d.depth--
			return err
		}
		d.skipWhitespace()
		if d.off >= len(d.data) || d.data[d.off] != ':' {
			d.depth--
			return errors.New("expected ':'")
		}
		d.off++

		err = fn(key)
		if err != nil {
			d.depth--
			return err
		}
	}
}

// parseArray parses a JSON array and delegates each element to fn. fn is
// responsible for consuming exactly one element value.
func (d *decBuffer) parseArray(fn func() error) error {
	d.skipWhitespace()
	if d.off >= len(d.data) || d.data[d.off] != '[' {
		return errors.New("expected '['")
	}
	d.depth++
	if d.depth > 100 {
		return errors.New("exceeded maximum recursion depth")
	}
	d.off++

	first := true
	for {
		d.skipWhitespace()
		if d.off >= len(d.data) {
			d.depth--
			return errors.New("unexpected EOF")
		}
		if d.data[d.off] == ']' {
			d.off++
			d.depth--
			return nil
		}
		if !first {
			if d.data[d.off] != ',' {
				d.depth--
				return errors.New("expected ','")
			}
			d.off++
			d.skipWhitespace()
		}
		first = false

		err := fn()
		if err != nil {
			d.depth--
			return err
		}
	}
}

func Unmarshal(data []byte, msg proto.Message) error {
	return UnmarshalOptions{}.Unmarshal(data, msg)
}

// Unmarshal decodes JSON into msg using the compiled table for msg's generated
// type. A successful decode consumes the entire input, including trailing
// whitespace, and clears omitted fields on reused target messages.
func (o UnmarshalOptions) Unmarshal(data []byte, msg proto.Message) error {
	val := reflect.ValueOf(msg)
	if !val.IsValid() || val.Kind() != reflect.Pointer || val.IsNil() {
		return errors.New("unmarshal target must be non-nil pointer")
	}
	if isProtojsonCustomWellKnown(msg.ProtoReflect().Descriptor().FullName()) {
		d := &decBuffer{data: data}
		if err := d.skipValue(); err != nil {
			return err
		}
		d.skipWhitespace()
		if d.off != len(d.data) {
			return errors.New("unexpected trailing data")
		}
		return protojson.UnmarshalOptions{
			DiscardUnknown: o.DiscardUnknown,
		}.Unmarshal(data, msg)
	}

	table, err := getTable(msg)
	if err != nil {
		return err
	}
	if table.useProtojson {
		d := &decBuffer{data: data}
		if err := d.skipValue(); err != nil {
			return err
		}
		d.skipWhitespace()
		if d.off != len(d.data) {
			return errors.New("unexpected trailing data")
		}
		return protojson.UnmarshalOptions{
			DiscardUnknown: o.DiscardUnknown,
		}.Unmarshal(data, msg)
	}
	ptr := val.UnsafePointer()

	d := &decBuffer{data: data}
	if err := table.unmarshalFrom(ptr, d, o); err != nil {
		return err
	}
	d.skipWhitespace()
	if d.off != len(d.data) {
		return errors.New("unexpected trailing data")
	}
	return nil
}

// resetIfNeeded is the fallback for very wide messages where the compact seen
// bitmask cannot track every field. It clears only when the target is not
// already zero to avoid unnecessary writes for fresh structs.
func (table *MessageTable) resetIfNeeded(ptr unsafe.Pointer) {
	if table.isZero(ptr) {
		return
	}
	for _, inst := range table.fields {
		fieldPtr := unsafe.Add(ptr, inst.offset)
		switch inst.ftype {
		case TypeString:
			*(*string)(fieldPtr) = ""
		case TypeInt32, TypeEnum:
			*(*int32)(fieldPtr) = 0
		case TypeInt64:
			*(*int64)(fieldPtr) = 0
		case TypeUint32:
			*(*uint32)(fieldPtr) = 0
		case TypeUint64:
			*(*uint64)(fieldPtr) = 0
		case TypeFloat32:
			*(*float32)(fieldPtr) = 0
		case TypeFloat64:
			*(*float64)(fieldPtr) = 0
		case TypeBool:
			*(*bool)(fieldPtr) = false
		case TypeBytes:
			*(*[]byte)(fieldPtr) = nil
		case TypeRepeatedString:
			*(*[]string)(fieldPtr) = nil
		case TypeRepeatedInt32, TypeRepeatedEnum:
			*(*[]int32)(fieldPtr) = nil
		case TypeRepeatedInt64:
			*(*[]int64)(fieldPtr) = nil
		case TypeRepeatedUint32:
			*(*[]uint32)(fieldPtr) = nil
		case TypeRepeatedUint64:
			*(*[]uint64)(fieldPtr) = nil
		case TypeRepeatedFloat32:
			*(*[]float32)(fieldPtr) = nil
		case TypeRepeatedFloat64:
			*(*[]float64)(fieldPtr) = nil
		case TypeRepeatedBool:
			*(*[]bool)(fieldPtr) = nil
		case TypeRepeatedBytes:
			*(*[][]byte)(fieldPtr) = nil
		case TypeMapStringString:
			*(*map[string]string)(fieldPtr) = nil
		case TypeMessage, TypeTimestamp, TypeDuration, TypeProtojsonWellKnown, TypeDoubleValue, TypeFloatValue, TypeInt64Value, TypeUint64Value, TypeInt32Value, TypeUint32Value, TypeBoolValue, TypeStringValue, TypeBytesValue, TypeEmpty:
			*(*unsafe.Pointer)(fieldPtr) = nil
		case TypeRepeatedMessage:
			*(*[]unsafe.Pointer)(fieldPtr) = nil
		}
	}
}

// clearMissing applies protobuf default semantics for fields that were absent
// from a successful object decode.
func (table *MessageTable) clearMissing(ptr unsafe.Pointer, seen uint64) {
	for i := range table.fields {
		if seen&(uint64(1)<<uint(i)) == 0 {
			table.clearField(ptr, &table.fields[i])
		}
	}
}

// clearField writes the Go zero value for one supported field shape.
func (table *MessageTable) clearField(ptr unsafe.Pointer, inst *fieldInstruction) {
	fieldPtr := unsafe.Add(ptr, inst.offset)
	switch inst.ftype {
	case TypeString:
		*(*string)(fieldPtr) = ""
	case TypeInt32, TypeEnum:
		*(*int32)(fieldPtr) = 0
	case TypeInt64:
		*(*int64)(fieldPtr) = 0
	case TypeUint32:
		*(*uint32)(fieldPtr) = 0
	case TypeUint64:
		*(*uint64)(fieldPtr) = 0
	case TypeFloat32:
		*(*float32)(fieldPtr) = 0
	case TypeFloat64:
		*(*float64)(fieldPtr) = 0
	case TypeBool:
		*(*bool)(fieldPtr) = false
	case TypeBytes:
		*(*[]byte)(fieldPtr) = nil
	case TypeRepeatedString:
		*(*[]string)(fieldPtr) = nil
	case TypeRepeatedInt32, TypeRepeatedEnum:
		*(*[]int32)(fieldPtr) = nil
	case TypeRepeatedInt64:
		*(*[]int64)(fieldPtr) = nil
	case TypeRepeatedUint32:
		*(*[]uint32)(fieldPtr) = nil
	case TypeRepeatedUint64:
		*(*[]uint64)(fieldPtr) = nil
	case TypeRepeatedFloat32:
		*(*[]float32)(fieldPtr) = nil
	case TypeRepeatedFloat64:
		*(*[]float64)(fieldPtr) = nil
	case TypeRepeatedBool:
		*(*[]bool)(fieldPtr) = nil
	case TypeRepeatedBytes:
		*(*[][]byte)(fieldPtr) = nil
	case TypeMapStringString:
		*(*map[string]string)(fieldPtr) = nil
	case TypeMessage, TypeTimestamp, TypeDuration, TypeProtojsonWellKnown, TypeDoubleValue, TypeFloatValue, TypeInt64Value, TypeUint64Value, TypeInt32Value, TypeUint32Value, TypeBoolValue, TypeStringValue, TypeBytesValue, TypeEmpty:
		*(*unsafe.Pointer)(fieldPtr) = nil
	case TypeRepeatedMessage:
		*(*[]unsafe.Pointer)(fieldPtr) = nil
	}
}

// isZero checks the supported protobuf field storage without touching runtime
// message state. It is used only by the wide-message fallback.
func (table *MessageTable) isZero(ptr unsafe.Pointer) bool {
	for _, inst := range table.fields {
		fieldPtr := unsafe.Add(ptr, inst.offset)
		switch inst.ftype {
		case TypeString:
			if *(*string)(fieldPtr) != "" {
				return false
			}
		case TypeInt32, TypeEnum:
			if *(*int32)(fieldPtr) != 0 {
				return false
			}
		case TypeInt64:
			if *(*int64)(fieldPtr) != 0 {
				return false
			}
		case TypeUint32:
			if *(*uint32)(fieldPtr) != 0 {
				return false
			}
		case TypeUint64:
			if *(*uint64)(fieldPtr) != 0 {
				return false
			}
		case TypeFloat32:
			if *(*float32)(fieldPtr) != 0 {
				return false
			}
		case TypeFloat64:
			if *(*float64)(fieldPtr) != 0 {
				return false
			}
		case TypeBool:
			if *(*bool)(fieldPtr) {
				return false
			}
		case TypeBytes:
			if len(*(*[]byte)(fieldPtr)) != 0 {
				return false
			}
		case TypeRepeatedString:
			if len(*(*[]string)(fieldPtr)) != 0 {
				return false
			}
		case TypeRepeatedInt32, TypeRepeatedEnum:
			if len(*(*[]int32)(fieldPtr)) != 0 {
				return false
			}
		case TypeRepeatedInt64:
			if len(*(*[]int64)(fieldPtr)) != 0 {
				return false
			}
		case TypeRepeatedUint32:
			if len(*(*[]uint32)(fieldPtr)) != 0 {
				return false
			}
		case TypeRepeatedUint64:
			if len(*(*[]uint64)(fieldPtr)) != 0 {
				return false
			}
		case TypeRepeatedFloat32:
			if len(*(*[]float32)(fieldPtr)) != 0 {
				return false
			}
		case TypeRepeatedFloat64:
			if len(*(*[]float64)(fieldPtr)) != 0 {
				return false
			}
		case TypeRepeatedBool:
			if len(*(*[]bool)(fieldPtr)) != 0 {
				return false
			}
		case TypeRepeatedBytes:
			if len(*(*[][]byte)(fieldPtr)) != 0 {
				return false
			}
		case TypeMapStringString:
			if len(*(*map[string]string)(fieldPtr)) != 0 {
				return false
			}
		case TypeMessage, TypeTimestamp, TypeDuration, TypeProtojsonWellKnown, TypeDoubleValue, TypeFloatValue, TypeInt64Value, TypeUint64Value, TypeInt32Value, TypeUint32Value, TypeBoolValue, TypeStringValue, TypeBytesValue, TypeEmpty:
			if *(*unsafe.Pointer)(fieldPtr) != nil {
				return false
			}
		case TypeRepeatedMessage:
			if len(*(*[]unsafe.Pointer)(fieldPtr)) != 0 {
				return false
			}
		}
	}
	return true
}

// unsafeString aliases b as a string. Callers must only use it when b's backing
// storage outlives the resulting string or when the string is consumed
// immediately.
func unsafeString(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return unsafe.String(unsafe.SliceData(b), len(b))
}

// parseDuration parses protobuf Duration JSON using integer arithmetic so
// large second values keep exact nanosecond precision.
func parseDuration(s string) (int64, int32, error) {
	if len(s) < 2 || s[len(s)-1] != 's' {
		return 0, 0, errors.New("duration must end in s")
	}
	s = s[:len(s)-1]
	if s == "" {
		return 0, 0, errors.New("empty duration")
	}

	neg := false
	if s[0] == '-' || s[0] == '+' {
		neg = s[0] == '-'
		s = s[1:]
		if s == "" {
			return 0, 0, errors.New("empty duration")
		}
	}

	dot := -1
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			if dot >= 0 {
				return 0, 0, errors.New("invalid duration")
			}
			dot = i
		}
	}

	intPart := s
	if dot >= 0 {
		intPart = s[:dot]
	}
	if intPart == "" {
		return 0, 0, errors.New("invalid duration")
	}
	secs, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil {
		return 0, 0, err
	}

	var nanos int64
	if dot >= 0 {
		frac := s[dot+1:]
		if frac == "" || len(frac) > 9 {
			return 0, 0, errors.New("invalid duration fractional seconds")
		}
		for i := 0; i < len(frac); i++ {
			c := frac[i]
			if c < '0' || c > '9' {
				return 0, 0, errors.New("invalid duration fractional seconds")
			}
			nanos = nanos*10 + int64(c-'0')
		}
		for i := len(frac); i < 9; i++ {
			nanos *= 10
		}
	}

	if neg {
		secs = -secs
		nanos = -nanos
	}
	return secs, int32(nanos), nil
}

func (table *MessageTable) unmarshalFrom(ptr unsafe.Pointer, d *decBuffer, opts UnmarshalOptions) error {
	if table.useProtojson {
		msg := reflect.NewAt(table.goType, ptr).Interface().(proto.Message)
		start := d.off
		if err := d.skipValue(); err != nil {
			return err
		}
		return protojson.UnmarshalOptions{
			DiscardUnknown: opts.DiscardUnknown,
		}.Unmarshal(d.data[start:d.off], msg)
	}

	if len(table.fields) > 64 {
		table.resetIfNeeded(ptr)
		seen := make(map[*fieldInstruction]struct{}, len(table.fields))
		return d.parseObject(func(key []byte) error {
			return table.unmarshalField(ptr, d, opts, key, seen)
		})
	}

	var seen uint64
	err := d.parseObject(func(key []byte) error {
		inst, err := table.unmarshalFieldInstruction(key, opts)
		if err != nil {
			if opts.DiscardUnknown && err == errUnknownField {
				return d.skipValue()
			}
			return err
		}
		bit := uint64(1) << uint(inst.index)
		if seen&bit != 0 {
			return errors.New("duplicate field: " + unsafeString(key))
		}
		seen |= bit
		return table.unmarshalKnownField(ptr, d, opts, inst)
	})
	if err != nil {
		return err
	}
	allSeen := ^uint64(0)
	if len(table.fields) < 64 {
		allSeen = (uint64(1) << uint(len(table.fields))) - 1
	}
	if seen != allSeen {
		table.clearMissing(ptr, seen)
	}
	return nil
}

var errUnknownField = errors.New("unknown field")

// unmarshalField is the wide-message fallback. It uses a map for duplicate
// detection because the fast 64-bit seen mask cannot represent every field.
func (table *MessageTable) unmarshalField(ptr unsafe.Pointer, d *decBuffer, opts UnmarshalOptions, key []byte, seen map[*fieldInstruction]struct{}) error {
	inst, err := table.unmarshalFieldInstruction(key, opts)
	if err != nil {
		if opts.DiscardUnknown && err == errUnknownField {
			return d.skipValue()
		}
		return err
	}
	if _, ok := seen[inst]; ok {
		return errors.New("duplicate field: " + unsafeString(key))
	}
	seen[inst] = struct{}{}
	return table.unmarshalKnownField(ptr, d, opts, inst)
}

// unmarshalFieldInstruction resolves a JSON key to the compiled instruction.
// Both camelCase JSON names and proto snake_case names map to the same
// instruction, which lets duplicate detection catch mixed-name duplicates.
func (table *MessageTable) unmarshalFieldInstruction(key []byte, opts UnmarshalOptions) (*fieldInstruction, error) {
	k := unsafeString(key)
	inst, ok := table.fieldMap[k]
	if !ok {
		if opts.DiscardUnknown {
			return nil, errUnknownField
		}
		return nil, errors.New("unknown field: " + k)
	}
	return inst, nil
}

// unmarshalKnownField consumes and stores one field value. The generic null
// check at the top intentionally applies to all supported field shapes:
// protojson treats null as "field not present", so we clear the destination and
// leave the field marked as seen for duplicate detection.
func (table *MessageTable) unmarshalKnownField(ptr unsafe.Pointer, d *decBuffer, opts UnmarshalOptions, inst *fieldInstruction) error {
	fieldPtr := unsafe.Add(ptr, inst.offset)
	if d.readNull() {
		table.clearField(ptr, inst)
		return nil
	}

	switch inst.ftype {
	case TypeString:
		val, err := d.readStringBytes()
		if err != nil {
			return err
		}
		if opts.ZeroCopy {
			*(*string)(fieldPtr) = unsafeString(val)
		} else {
			*(*string)(fieldPtr) = string(val)
		}
	case TypeInt32:
		val, err := d.readInt32()
		if err != nil {
			return err
		}
		*(*int32)(fieldPtr) = val
	case TypeInt64:
		val, err := d.readInt64()
		if err != nil {
			return err
		}
		*(*int64)(fieldPtr) = val
	case TypeUint32:
		val, err := d.readUint32()
		if err != nil {
			return err
		}
		*(*uint32)(fieldPtr) = val
	case TypeUint64:
		val, err := d.readUint64()
		if err != nil {
			return err
		}
		*(*uint64)(fieldPtr) = val
	case TypeFloat32:
		val, err := d.readFloat32()
		if err != nil {
			return err
		}
		*(*float32)(fieldPtr) = val
	case TypeFloat64:
		val, err := d.readFloat64()
		if err != nil {
			return err
		}
		*(*float64)(fieldPtr) = val
	case TypeBool:
		val, err := d.readBool()
		if err != nil {
			return err
		}
		*(*bool)(fieldPtr) = val
	case TypeBytes:
		val, err := d.readStringBytes()
		if err != nil {
			return err
		}
		// Base64 decode
		decoded, err := base64.StdEncoding.DecodeString(unsafeString(val))
		if err != nil {
			return err
		}
		*(*[]byte)(fieldPtr) = decoded
	case TypeEnum:
		d.skipWhitespace()
		var ev int32
		if d.off < len(d.data) && d.data[d.off] == '"' {
			s, err := d.readStringBytes()
			if err != nil {
				return err
			}
			var ok bool
			ev, ok = inst.enumValueMap[unsafeString(s)]
			if !ok {
				return errors.New("unknown enum value: " + unsafeString(s))
			}
		} else {
			val, err := d.readInt32()
			if err != nil {
				return err
			}
			ev = val
		}
		*(*int32)(fieldPtr) = ev
	case TypeRepeatedString:
		slicePtr := (*[]string)(fieldPtr)
		*slicePtr = (*slicePtr)[:0]
		return d.parseArray(func() error {
			val, err := d.readStringBytes()
			if err != nil {
				return err
			}
			if opts.ZeroCopy {
				*slicePtr = append(*slicePtr, unsafeString(val))
			} else {
				*slicePtr = append(*slicePtr, string(val))
			}
			return nil
		})
	case TypeRepeatedInt32:
		slicePtr := (*[]int32)(fieldPtr)
		*slicePtr = (*slicePtr)[:0]
		return d.parseArray(func() error {
			val, err := d.readInt32()
			if err != nil {
				return err
			}
			*slicePtr = append(*slicePtr, val)
			return nil
		})
	case TypeRepeatedInt64:
		slicePtr := (*[]int64)(fieldPtr)
		*slicePtr = (*slicePtr)[:0]
		return d.parseArray(func() error {
			val, err := d.readInt64()
			if err != nil {
				return err
			}
			*slicePtr = append(*slicePtr, val)
			return nil
		})
	case TypeRepeatedUint32:
		slicePtr := (*[]uint32)(fieldPtr)
		*slicePtr = (*slicePtr)[:0]
		return d.parseArray(func() error {
			val, err := d.readUint32()
			if err != nil {
				return err
			}
			*slicePtr = append(*slicePtr, val)
			return nil
		})
	case TypeRepeatedUint64:
		slicePtr := (*[]uint64)(fieldPtr)
		*slicePtr = (*slicePtr)[:0]
		return d.parseArray(func() error {
			val, err := d.readUint64()
			if err != nil {
				return err
			}
			*slicePtr = append(*slicePtr, val)
			return nil
		})
	case TypeRepeatedFloat32:
		slicePtr := (*[]float32)(fieldPtr)
		*slicePtr = (*slicePtr)[:0]
		return d.parseArray(func() error {
			val, err := d.readFloat32()
			if err != nil {
				return err
			}
			*slicePtr = append(*slicePtr, val)
			return nil
		})
	case TypeRepeatedFloat64:
		slicePtr := (*[]float64)(fieldPtr)
		*slicePtr = (*slicePtr)[:0]
		return d.parseArray(func() error {
			val, err := d.readFloat64()
			if err != nil {
				return err
			}
			*slicePtr = append(*slicePtr, val)
			return nil
		})
	case TypeRepeatedBool:
		slicePtr := (*[]bool)(fieldPtr)
		*slicePtr = (*slicePtr)[:0]
		return d.parseArray(func() error {
			val, err := d.readBool()
			if err != nil {
				return err
			}
			*slicePtr = append(*slicePtr, val)
			return nil
		})
	case TypeRepeatedBytes:
		slicePtr := (*[][]byte)(fieldPtr)
		*slicePtr = (*slicePtr)[:0]
		return d.parseArray(func() error {
			val, err := d.readStringBytes()
			if err != nil {
				return err
			}
			decoded, err := base64.StdEncoding.DecodeString(unsafeString(val))
			if err != nil {
				return err
			}
			*slicePtr = append(*slicePtr, decoded)
			return nil
		})
	case TypeRepeatedEnum:
		slicePtr := (*[]int32)(fieldPtr)
		*slicePtr = (*slicePtr)[:0]
		return d.parseArray(func() error {
			d.skipWhitespace()
			var ev int32
			if d.off < len(d.data) && d.data[d.off] == '"' {
				s, err := d.readStringBytes()
				if err != nil {
					return err
				}
				var ok bool
				ev, ok = inst.enumValueMap[unsafeString(s)]
				if !ok {
					return errors.New("unknown enum value: " + unsafeString(s))
				}
			} else {
				val, err := d.readInt32()
				if err != nil {
					return err
				}
				ev = val
			}
			*slicePtr = append(*slicePtr, ev)
			return nil
		})
	case TypeMapStringString:
		mapPtr := (*map[string]string)(fieldPtr)
		if *mapPtr == nil {
			*mapPtr = make(map[string]string)
		} else {
			clear(*mapPtr)
		}
		m := *mapPtr
		return d.parseObject(func(mkey []byte) error {
			val, err := d.readStringBytes()
			if err != nil {
				return err
			}
			var mk, mv string
			if opts.ZeroCopy {
				mk = unsafeString(mkey)
				mv = unsafeString(val)
			} else {
				mk = string(mkey)
				mv = string(val)
			}
			m[mk] = mv
			return nil
		})
	case TypeMessage:
		subMsgPtrPtr := (*unsafe.Pointer)(fieldPtr)
		if d.readNull() {
			*subMsgPtrPtr = nil
			return nil
		}
		if inst.msgNeedsWait {
			if err := inst.msgTable.wait(); err != nil {
				return err
			}
		} else if inst.msgTable.err != nil {
			return inst.msgTable.err
		}
		if *subMsgPtrPtr == nil {
			newVal := allocate(inst.msgTable.goType, opts)
			*subMsgPtrPtr = unsafe.Pointer(newVal.Pointer())
		}
		return inst.msgTable.unmarshalFrom(*subMsgPtrPtr, d, opts)
	case TypeTimestamp:
		subMsgPtrPtr := (*unsafe.Pointer)(fieldPtr)
		if d.readNull() {
			*subMsgPtrPtr = nil
			return nil
		}
		if *subMsgPtrPtr == nil {
			newVal := allocate(inst.elemType, opts)
			*subMsgPtrPtr = unsafe.Pointer(newVal.Pointer())
		}
		val, err := d.readStringBytes()
		if err != nil {
			return err
		}
		t, err := time.Parse(time.RFC3339Nano, unsafeString(val))
		if err != nil {
			return err
		}
		*(*int64)(unsafe.Add(*subMsgPtrPtr, inst.secondsOffset)) = t.Unix()
		*(*int32)(unsafe.Add(*subMsgPtrPtr, inst.nanosOffset)) = int32(t.Nanosecond())
	case TypeDuration:
		subMsgPtrPtr := (*unsafe.Pointer)(fieldPtr)
		if d.readNull() {
			*subMsgPtrPtr = nil
			return nil
		}
		if *subMsgPtrPtr == nil {
			newVal := allocate(inst.elemType, opts)
			*subMsgPtrPtr = unsafe.Pointer(newVal.Pointer())
		}
		val, err := d.readStringBytes()
		if err != nil {
			return err
		}
		secs, nanos, err := parseDuration(unsafeString(val))
		if err != nil {
			return err
		}
		*(*int64)(unsafe.Add(*subMsgPtrPtr, inst.secondsOffset)) = secs
		*(*int32)(unsafe.Add(*subMsgPtrPtr, inst.nanosOffset)) = int32(nanos)
	case TypeDoubleValue:
		subMsgPtrPtr := (*unsafe.Pointer)(fieldPtr)
		err := d.unmarshalWrapper(opts, inst, fieldPtr, func() error {
			val, err := d.readFloat64()
			if err != nil {
				return err
			}
			*(*float64)(unsafe.Add(*subMsgPtrPtr, inst.valueOffset)) = val
			return nil
		})
		if err != nil {
			return err
		}
	case TypeFloatValue:
		subMsgPtrPtr := (*unsafe.Pointer)(fieldPtr)
		err := d.unmarshalWrapper(opts, inst, fieldPtr, func() error {
			val, err := d.readFloat32()
			if err != nil {
				return err
			}
			*(*float32)(unsafe.Add(*subMsgPtrPtr, inst.valueOffset)) = val
			return nil
		})
		if err != nil {
			return err
		}
	case TypeInt64Value:
		subMsgPtrPtr := (*unsafe.Pointer)(fieldPtr)
		err := d.unmarshalWrapper(opts, inst, fieldPtr, func() error {
			val, err := d.readInt64()
			if err != nil {
				return err
			}
			*(*int64)(unsafe.Add(*subMsgPtrPtr, inst.valueOffset)) = val
			return nil
		})
		if err != nil {
			return err
		}
	case TypeUint64Value:
		subMsgPtrPtr := (*unsafe.Pointer)(fieldPtr)
		err := d.unmarshalWrapper(opts, inst, fieldPtr, func() error {
			val, err := d.readUint64()
			if err != nil {
				return err
			}
			*(*uint64)(unsafe.Add(*subMsgPtrPtr, inst.valueOffset)) = val
			return nil
		})
		if err != nil {
			return err
		}
	case TypeInt32Value:
		subMsgPtrPtr := (*unsafe.Pointer)(fieldPtr)
		err := d.unmarshalWrapper(opts, inst, fieldPtr, func() error {
			val, err := d.readInt32()
			if err != nil {
				return err
			}
			*(*int32)(unsafe.Add(*subMsgPtrPtr, inst.valueOffset)) = val
			return nil
		})
		if err != nil {
			return err
		}
	case TypeUint32Value:
		subMsgPtrPtr := (*unsafe.Pointer)(fieldPtr)
		err := d.unmarshalWrapper(opts, inst, fieldPtr, func() error {
			val, err := d.readUint32()
			if err != nil {
				return err
			}
			*(*uint32)(unsafe.Add(*subMsgPtrPtr, inst.valueOffset)) = val
			return nil
		})
		if err != nil {
			return err
		}
	case TypeBoolValue:
		subMsgPtrPtr := (*unsafe.Pointer)(fieldPtr)
		err := d.unmarshalWrapper(opts, inst, fieldPtr, func() error {
			val, err := d.readBool()
			if err != nil {
				return err
			}
			*(*bool)(unsafe.Add(*subMsgPtrPtr, inst.valueOffset)) = val
			return nil
		})
		if err != nil {
			return err
		}
	case TypeStringValue:
		subMsgPtrPtr := (*unsafe.Pointer)(fieldPtr)
		err := d.unmarshalWrapper(opts, inst, fieldPtr, func() error {
			s, err := d.readStringBytes()
			if err != nil {
				return err
			}
			var val string
			if opts.ZeroCopy {
				val = unsafeString(s)
			} else {
				val = string(s)
			}
			*(*string)(unsafe.Add(*subMsgPtrPtr, inst.valueOffset)) = val
			return nil
		})
		if err != nil {
			return err
		}
	case TypeBytesValue:
		subMsgPtrPtr := (*unsafe.Pointer)(fieldPtr)
		err := d.unmarshalWrapper(opts, inst, fieldPtr, func() error {
			s, err := d.readStringBytes()
			if err != nil {
				return err
			}
			decoded, err := base64.StdEncoding.DecodeString(unsafeString(s))
			if err != nil {
				return err
			}
			*(*[]byte)(unsafe.Add(*subMsgPtrPtr, inst.valueOffset)) = decoded
			return nil
		})
		if err != nil {
			return err
		}
	case TypeEmpty:
		subMsgPtrPtr := (*unsafe.Pointer)(fieldPtr)
		if *subMsgPtrPtr == nil {
			newVal := allocate(inst.elemType, opts)
			*subMsgPtrPtr = unsafe.Pointer(newVal.Pointer())
		}
		if d.isObject() {
			err := d.parseObject(func(key []byte) error {
				if opts.DiscardUnknown {
					return d.skipValue()
				}
				return errors.New("unknown field in Empty: " + string(key))
			})
			if err != nil {
				return err
			}
		} else {
			return errors.New("expected empty object for Empty")
		}
	case TypeProtojsonWellKnown:
		subMsgPtrPtr := (*unsafe.Pointer)(fieldPtr)
		if *subMsgPtrPtr == nil {
			newVal := allocate(inst.elemType, opts)
			*subMsgPtrPtr = unsafe.Pointer(newVal.Pointer())
		}
		msg := reflect.NewAt(inst.elemType, *subMsgPtrPtr).Interface().(proto.Message)
		start := d.off
		if err := d.skipValue(); err != nil {
			return err
		}
		return protojson.UnmarshalOptions{
			DiscardUnknown: opts.DiscardUnknown,
		}.Unmarshal(d.data[start:d.off], msg)
	case TypeRepeatedMessage:
		if inst.msgNeedsWait {
			if err := inst.msgTable.wait(); err != nil {
				return err
			}
		} else if inst.msgTable.err != nil {
			return inst.msgTable.err
		}
		sliceVal := reflect.NewAt(reflect.SliceOf(reflect.PointerTo(inst.elemType)), fieldPtr).Elem()
		sliceVal.SetLen(0)

		return d.parseArray(func() error {
			if d.readNull() {
				sliceVal.Set(reflect.Append(sliceVal, reflect.Zero(sliceVal.Type().Elem())))
				return nil
			}
			newElem := allocate(inst.elemType, opts)
			err := inst.msgTable.unmarshalFrom(unsafe.Pointer(newElem.Pointer()), d, opts)
			if err != nil {
				return err
			}
			sliceVal.Set(reflect.Append(sliceVal, newElem))
			return nil
		})
	}
	return nil
}
