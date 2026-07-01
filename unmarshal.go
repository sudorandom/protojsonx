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
import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"time"
	"unsafe"

	"fmt"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"math"
	"strings"
)

type UnmarshalOptions struct {
	DiscardUnknown bool
}

func allocate(t reflect.Type, _ UnmarshalOptions) reflect.Value {
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
	if err := validateSurrogates(s); err != nil {
		return nil, err
	}
	if len(s) > math.MaxInt-2 {
		return nil, errors.New("string length exceeds maximum capacity")
	}
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

func validateSurrogates(s []byte) error {
	for i := 0; i < len(s); {
		if s[i] == '\\' {
			if i+1 >= len(s) {
				return errors.New("incomplete escape")
			}
			if s[i+1] == 'u' {
				if i+5 >= len(s) {
					return errors.New("incomplete unicode escape")
				}
				r, err := parseHex4(s[i+2 : i+6])
				if err != nil {
					return err
				}
				if r >= 0xD800 && r <= 0xDBFF {
					// High surrogate, must be followed by a low surrogate
					i += 6
					if i+5 >= len(s) || s[i] != '\\' || s[i+1] != 'u' {
						return errors.New("unpaired high surrogate")
					}
					r2, err2 := parseHex4(s[i+2 : i+6])
					if err2 != nil {
						return err2
					}
					if r2 < 0xDC00 || r2 > 0xDFFF {
						return errors.New("unpaired high surrogate")
					}
					i += 6
				} else if r >= 0xDC00 && r <= 0xDFFF {
					return errors.New("unpaired low surrogate")
				} else {
					i += 6
				}
			} else {
				i += 2
			}
		} else {
			i++
		}
	}
	return nil
}

func parseHex4(b []byte) (rune, error) {
	var r rune
	for _, c := range b {
		r <<= 4
		switch {
		case c >= '0' && c <= '9':
			r |= rune(c - '0')
		case c >= 'a' && c <= 'f':
			r |= rune(c - 'a' + 10)
		case c >= 'A' && c <= 'F':
			r |= rune(c - 'A' + 10)
		default:
			return 0, errors.New("invalid hex character")
		}
	}
	return r, nil
}

func parseStringOrNumberToInt64(s string) (int64, error) {
	v, err := strconv.ParseInt(s, 10, 64)
	if err == nil {
		return v, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	if f != math.Round(f) || math.IsNaN(f) || math.IsInf(f, 0) || f < float64(math.MinInt64) || f > float64(math.MaxInt64) {
		return 0, fmt.Errorf("invalid integer: %s", s)
	}
	return int64(f), nil
}

func parseStringOrNumberToUint64(s string) (uint64, error) {
	v, err := strconv.ParseUint(s, 10, 64)
	if err == nil {
		return v, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	if f < 0 || f != math.Round(f) || math.IsNaN(f) || math.IsInf(f, 0) || f > float64(math.MaxUint64) {
		return 0, fmt.Errorf("invalid integer: %s", s)
	}
	return uint64(f), nil
}

func (d *decBuffer) readInt32() (int32, error) {
	d.skipWhitespace()
	var s string
	if d.off < len(d.data) && d.data[d.off] == '"' {
		val, err := d.readStringBytes()
		if err != nil {
			return 0, err
		}
		s = string(val)
	} else {
		token, err := d.readJSONNumberToken()
		if err != nil {
			return 0, err
		}
		s = string(token)
	}
	v, err := parseStringOrNumberToInt64(s)
	if err != nil {
		return 0, err
	}
	if v < math.MinInt32 || v > math.MaxInt32 {
		return 0, fmt.Errorf("integer out of range for int32: %s", s)
	}
	return int32(v), nil
}

func (d *decBuffer) readInt64() (int64, error) {
	d.skipWhitespace()
	var s string
	if d.off < len(d.data) && d.data[d.off] == '"' {
		val, err := d.readStringBytes()
		if err != nil {
			return 0, err
		}
		s = string(val)
	} else {
		token, err := d.readJSONNumberToken()
		if err != nil {
			return 0, err
		}
		s = string(token)
	}
	return parseStringOrNumberToInt64(s)
}

func (d *decBuffer) readUint32() (uint32, error) {
	d.skipWhitespace()
	var s string
	if d.off < len(d.data) && d.data[d.off] == '"' {
		val, err := d.readStringBytes()
		if err != nil {
			return 0, err
		}
		s = string(val)
	} else {
		token, err := d.readJSONNumberToken()
		if err != nil {
			return 0, err
		}
		s = string(token)
	}
	v, err := parseStringOrNumberToUint64(s)
	if err != nil {
		return 0, err
	}
	if v > math.MaxUint32 {
		return 0, fmt.Errorf("integer out of range for uint32: %s", s)
	}
	return uint32(v), nil
}

func (d *decBuffer) readUint64() (uint64, error) {
	d.skipWhitespace()
	var s string
	if d.off < len(d.data) && d.data[d.off] == '"' {
		val, err := d.readStringBytes()
		if err != nil {
			return 0, err
		}
		s = string(val)
	} else {
		token, err := d.readJSONNumberToken()
		if err != nil {
			return 0, err
		}
		s = string(token)
	}
	return parseStringOrNumberToUint64(s)
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
	return strconv.ParseFloat(s, bitSize)
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

func (d *decBuffer) peekNull() bool {
	d.skipWhitespace()
	if d.off+4 <= len(d.data) && string(d.data[d.off:d.off+4]) == "null" {
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

	table, err := getTable(msg)
	if err != nil {
		return err
	}

	if isCustomWellKnown(table.fullName) {
		d := &decBuffer{data: data}
		if err := unmarshalCustomWellKnown(msg, d, o); err != nil {
			return err
		}
		d.skipWhitespace()
		if d.off != len(d.data) {
			return errors.New("unexpected trailing data")
		}
		return nil
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
	case TypeMessage, TypeTimestamp, TypeDuration, TypeProtojsonWellKnown, TypeDoubleValue, TypeFloatValue, TypeInt64Value, TypeUint64Value, TypeInt32Value, TypeUint32Value, TypeBoolValue, TypeStringValue, TypeBytesValue, TypeEmpty, TypeFieldMask, TypeStruct, TypeValue, TypeListValue, TypeAny:
		*(*unsafe.Pointer)(fieldPtr) = nil
	case TypeOneofField, TypeMapField:
		pref := reflect.NewAt(table.goType, ptr).Interface().(proto.Message).ProtoReflect()
		pref.Clear(inst.fd)
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
	var seenOneofs uint64
	if len(table.fields) > 64 {
		table.resetIfNeeded(ptr)
		seen := make(map[*fieldInstruction]struct{}, len(table.fields))
		seenExts := make(map[string]struct{})
		return d.parseObject(func(key []byte) error {
			return table.unmarshalField(ptr, d, opts, key, seen, seenExts, &seenOneofs)
		})
	}

	var seen uint64
	var seenExts map[string]struct{}
	err := d.parseObject(func(key []byte) error {
		if len(key) > 2 && key[0] == '[' && key[len(key)-1] == ']' {
			extName := string(key[1 : len(key)-1])
			xt, errExt := protoregistry.GlobalTypes.FindExtensionByName(protoreflect.FullName(extName))
			if errExt == nil {
				if seenExts == nil {
					seenExts = make(map[string]struct{})
				}
				if _, ok := seenExts[extName]; ok {
					return fmt.Errorf("duplicate field: %q", string(key))
				}
				seenExts[extName] = struct{}{}
				pref := reflect.NewAt(table.goType, ptr).Interface().(proto.Message).ProtoReflect()
				return unmarshalExtensionField(pref, xt, d, opts)
			}
		}
		inst, err := table.unmarshalFieldInstruction(key, opts)
		if err != nil {
			if opts.DiscardUnknown && err == errUnknownField {
				return d.skipValue()
			}
			return err
		}
		if inst.ftype == TypeOneofField && !d.peekNull() {
			od := inst.fd.ContainingOneof()
			if od != nil {
				bit := uint64(1) << uint(od.Index())
				if seenOneofs&bit != 0 {
					return fmt.Errorf("duplicate oneof field: %s", key)
				}
				seenOneofs |= bit
			}
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
var errUnknownEnum = errors.New("unknown enum value name")

// unmarshalField is the wide-message fallback. It uses a map for duplicate
// detection because the fast 64-bit seen mask cannot represent every field.
func (table *MessageTable) unmarshalField(ptr unsafe.Pointer, d *decBuffer, opts UnmarshalOptions, key []byte, seen map[*fieldInstruction]struct{}, seenExts map[string]struct{}, seenOneofs *uint64) error {
	if len(key) > 2 && key[0] == '[' && key[len(key)-1] == ']' {
		extName := string(key[1 : len(key)-1])
		xt, errExt := protoregistry.GlobalTypes.FindExtensionByName(protoreflect.FullName(extName))
		if errExt == nil {
			if _, ok := seenExts[extName]; ok {
				return fmt.Errorf("duplicate field: %q", string(key))
			}
			seenExts[extName] = struct{}{}
			pref := reflect.NewAt(table.goType, ptr).Interface().(proto.Message).ProtoReflect()
			return unmarshalExtensionField(pref, xt, d, opts)
		}
	}
	inst, err := table.unmarshalFieldInstruction(key, opts)
	if err != nil {
		if opts.DiscardUnknown && err == errUnknownField {
			return d.skipValue()
		}
		return err
	}
	if inst.ftype == TypeOneofField && !d.peekNull() {
		od := inst.fd.ContainingOneof()
		if od != nil {
			bit := uint64(1) << uint(od.Index())
			if *seenOneofs&bit != 0 {
				return fmt.Errorf("duplicate oneof field: %s", key)
			}
			*seenOneofs |= bit
		}
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
		if inst.ftype == TypeOneofField {
			if inst.fd.Kind() == protoreflect.EnumKind && inst.fd.Enum().FullName() == "google.protobuf.NullValue" {
				pref := reflect.NewAt(table.goType, ptr).Interface().(proto.Message).ProtoReflect()
				pref.Set(inst.fd, protoreflect.ValueOfEnum(0))
				return nil
			}
			if inst.fd.Kind() == protoreflect.MessageKind && inst.fd.Message().FullName() == "google.protobuf.Value" {
				pref := reflect.NewAt(table.goType, ptr).Interface().(proto.Message).ProtoReflect()
				val := pref.NewField(inst.fd).Message()
				fd := val.Descriptor().Fields().ByNumber(1)
				val.Set(fd, protoreflect.ValueOfEnum(0))
				pref.Set(inst.fd, protoreflect.ValueOfMessage(val))
				return nil
			}
			pref := reflect.NewAt(table.goType, ptr).Interface().(proto.Message).ProtoReflect()
			pref.Clear(inst.fd)
			return nil
		}
		if inst.ftype == TypeMapField {
			pref := reflect.NewAt(table.goType, ptr).Interface().(proto.Message).ProtoReflect()
			pref.Clear(inst.fd)
			return nil
		}
		if inst.ftype == TypeValue {
			subMsgPtrPtr := (*unsafe.Pointer)(fieldPtr)
			if *subMsgPtrPtr == nil {
				newVal := allocate(inst.elemType, opts)
				*subMsgPtrPtr = unsafe.Pointer(newVal.Pointer())
			}
			msg := reflect.NewAt(inst.elemType, *subMsgPtrPtr).Interface().(proto.Message)
			pref := msg.ProtoReflect()
			fd := pref.Descriptor().Fields().ByNumber(1)
			pref.Set(fd, protoreflect.ValueOfEnum(0))
			return nil
		}
		table.clearField(ptr, inst)
		return nil
	}

	targetPtr := fieldPtr
	if inst.goPointer {
		newVal := allocate(inst.elemType, opts)
		targetPtr = unsafe.Pointer(newVal.Pointer())
		*(*unsafe.Pointer)(fieldPtr) = targetPtr
	}

	switch inst.ftype {
	case TypeString:
		val, err := d.readStringBytes()
		if err != nil {
			return err
		}
		*(*string)(targetPtr) = string(val)
	case TypeInt32:
		val, err := d.readInt32()
		if err != nil {
			return err
		}
		*(*int32)(targetPtr) = val
	case TypeInt64:
		val, err := d.readInt64()
		if err != nil {
			return err
		}
		*(*int64)(targetPtr) = val
	case TypeUint32:
		val, err := d.readUint32()
		if err != nil {
			return err
		}
		*(*uint32)(targetPtr) = val
	case TypeUint64:
		val, err := d.readUint64()
		if err != nil {
			return err
		}
		*(*uint64)(targetPtr) = val
	case TypeFloat32:
		val, err := d.readFloat32()
		if err != nil {
			return err
		}
		*(*float32)(targetPtr) = val
	case TypeFloat64:
		val, err := d.readFloat64()
		if err != nil {
			return err
		}
		*(*float64)(targetPtr) = val
	case TypeBool:
		val, err := d.readBool()
		if err != nil {
			return err
		}
		*(*bool)(targetPtr) = val
	case TypeBytes:
		val, err := d.readStringBytes()
		if err != nil {
			return err
		}
		// Base64 decode
		decoded, err := decodeBase64(unsafeString(val))
		if err != nil {
			return err
		}
		*(*[]byte)(targetPtr) = decoded
	case TypeEnum:
		d.skipWhitespace()
		var ev int32
		if d.off < len(d.data) && d.data[d.off] == '"' {
			s, err := d.readStringBytes()
			if err != nil {
				if inst.goPointer {
					*(*unsafe.Pointer)(fieldPtr) = nil
				}
				return err
			}
			var ok bool
			ev, ok = inst.enumValueMap[unsafeString(s)]
			if !ok {
				if opts.DiscardUnknown {
					if inst.goPointer {
						*(*unsafe.Pointer)(fieldPtr) = nil
					}
					return nil
				}
				if inst.goPointer {
					*(*unsafe.Pointer)(fieldPtr) = nil
				}
				return errors.New("unknown enum value: " + unsafeString(s))
			}
		} else {
			val, err := d.readInt32()
			if err != nil {
				if inst.goPointer {
					*(*unsafe.Pointer)(fieldPtr) = nil
				}
				return err
			}
			ev = val
		}
		*(*int32)(targetPtr) = ev
	case TypeRepeatedString:
		slicePtr := (*[]string)(fieldPtr)
		*slicePtr = (*slicePtr)[:0]
		return d.parseArray(func() error {
			val, err := d.readStringBytes()
			if err != nil {
				return err
			}
			*slicePtr = append(*slicePtr, string(val))
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
			decoded, err := decodeBase64(unsafeString(val))
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
					if opts.DiscardUnknown {
						return nil
					}
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
			m[string(mkey)] = string(val)
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
		secs := t.Unix()
		nanos := int32(t.Nanosecond())
		if err := validateTimestamp(secs, nanos); err != nil {
			return err
		}
		*(*int64)(unsafe.Add(*subMsgPtrPtr, inst.secondsOffset)) = secs
		*(*int32)(unsafe.Add(*subMsgPtrPtr, inst.nanosOffset)) = nanos
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
		if err := validateDuration(secs, int32(nanos)); err != nil {
			return err
		}
		*(*int64)(unsafe.Add(*subMsgPtrPtr, inst.secondsOffset)) = secs
		*(*int32)(unsafe.Add(*subMsgPtrPtr, inst.nanosOffset)) = int32(nanos)
	case TypeDoubleValue:
		subMsgPtrPtr := (*unsafe.Pointer)(fieldPtr)
		err := d.unmarshalWrapper(opts, inst, fieldPtr, func() error {
			var val float64
			var err error
			d.skipWhitespace()
			if d.off < len(d.data) && d.data[d.off] == '"' {
				s, err := d.readStringBytes()
				if err != nil {
					return err
				}
				val, err = strconv.ParseFloat(string(s), 64)
				if err != nil {
					return err
				}
			} else {
				val, err = d.readFloat64()
				if err != nil {
					return err
				}
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
			var val float32
			d.skipWhitespace()
			if d.off < len(d.data) && d.data[d.off] == '"' {
				s, err := d.readStringBytes()
				if err != nil {
					return err
				}
				v, err := strconv.ParseFloat(string(s), 32)
				if err != nil {
					return err
				}
				val = float32(v)
			} else {
				v, err := d.readFloat32()
				if err != nil {
					return err
				}
				val = float32(v)
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
			*(*string)(unsafe.Add(*subMsgPtrPtr, inst.valueOffset)) = string(s)
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
			decoded, err := decodeBase64(unsafeString(s))
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
	case TypeFieldMask, TypeStruct, TypeValue, TypeListValue, TypeAny, TypeProtojsonWellKnown:
		subMsgPtrPtr := (*unsafe.Pointer)(fieldPtr)
		if *subMsgPtrPtr == nil {
			newVal := allocate(inst.elemType, opts)
			*subMsgPtrPtr = unsafe.Pointer(newVal.Pointer())
		}
		msg := reflect.NewAt(inst.elemType, *subMsgPtrPtr).Interface().(proto.Message)
		if err := unmarshalCustomWellKnown(msg, d, opts); err != nil {
			return err
		}
	case TypeOneofField:
		pref := reflect.NewAt(table.goType, ptr).Interface().(proto.Message).ProtoReflect()
		var target protoreflect.Message
		if inst.fd.Kind() == protoreflect.MessageKind || inst.fd.Kind() == protoreflect.GroupKind {
			target = pref.NewField(inst.fd).Message()
		}
		val, err := unmarshalProtoreflectValue(inst.fd, target, d, opts)
		if err != nil {
			if opts.DiscardUnknown && err == errUnknownEnum {
				return nil
			}
			return err
		}
		pref.Set(inst.fd, val)
	case TypeMapField:
		pref := reflect.NewAt(table.goType, ptr).Interface().(proto.Message).ProtoReflect()
		return unmarshalMap(pref, inst, d, opts)
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
			if d.peekNull() {
				return errors.New("repeated field elements cannot be null")
			}
			newElem := allocate(inst.elemType, opts)
			var err error
			if isCustomWellKnown(inst.msgTable.fullName) {
				msg := newElem.Interface().(proto.Message)
				err = unmarshalCustomWellKnown(msg, d, opts)
			} else {
				err = inst.msgTable.unmarshalFrom(unsafe.Pointer(newElem.Pointer()), d, opts)
			}
			if err != nil {
				return err
			}
			sliceVal.Set(reflect.Append(sliceVal, newElem))
			return nil
		})
	}
	return nil
}

func unmarshalCustomWellKnown(msg proto.Message, d *decBuffer, opts UnmarshalOptions) error {
	pref := msg.ProtoReflect()
	fullName := pref.Descriptor().FullName()

	switch fullName {
	case "google.protobuf.Empty":
		if d.readNull() {
			return nil
		}
		d.skipWhitespace()
		if d.off >= len(d.data) || d.data[d.off] != '{' {
			return errors.New("expected '{' for Empty")
		}
		return d.parseObject(func(key []byte) error {
			if opts.DiscardUnknown {
				return d.skipValue()
			}
			return fmt.Errorf("unknown field in Empty: %s", string(key))
		})
	case "google.protobuf.Timestamp":
		if d.readNull() {
			fdSec := pref.Descriptor().Fields().ByNumber(1)
			fdNano := pref.Descriptor().Fields().ByNumber(2)
			pref.Set(fdSec, protoreflect.ValueOfInt64(0))
			pref.Set(fdNano, protoreflect.ValueOfInt32(0))
			return nil
		}
		bytes, err := d.readStringBytes()
		if err != nil {
			return err
		}
		t, err := time.Parse(time.RFC3339Nano, string(bytes))
		if err != nil {
			return err
		}
		secs := t.Unix()
		nanos := int32(t.Nanosecond())
		if err := validateTimestamp(secs, nanos); err != nil {
			return err
		}
		fdSec := pref.Descriptor().Fields().ByNumber(1)
		fdNano := pref.Descriptor().Fields().ByNumber(2)
		pref.Set(fdSec, protoreflect.ValueOfInt64(secs))
		pref.Set(fdNano, protoreflect.ValueOfInt32(nanos))
		return nil
	case "google.protobuf.Duration":
		if d.readNull() {
			fdSec := pref.Descriptor().Fields().ByNumber(1)
			fdNano := pref.Descriptor().Fields().ByNumber(2)
			pref.Set(fdSec, protoreflect.ValueOfInt64(0))
			pref.Set(fdNano, protoreflect.ValueOfInt32(0))
			return nil
		}
		bytes, err := d.readStringBytes()
		if err != nil {
			return err
		}
		s := string(bytes)
		if !strings.HasSuffix(s, "s") {
			return fmt.Errorf("invalid duration format: missing suffix 's'")
		}
		s = s[:len(s)-1]
		neg := false
		if strings.HasPrefix(s, "-") {
			neg = true
			s = s[1:]
		}
		var secs int64
		var nanos int64
		idx := strings.IndexByte(s, '.')
		if idx == -1 {
			var err error
			secs, err = strconv.ParseInt(s, 10, 64)
			if err != nil {
				return fmt.Errorf("invalid duration: %v", err)
			}
		} else {
			var err error
			secs, err = strconv.ParseInt(s[:idx], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid duration: %v", err)
			}
			frac := s[idx+1:]
			if len(frac) > 9 {
				return fmt.Errorf("duration fraction has too many digits")
			}
			nanos, err = strconv.ParseInt(frac, 10, 64)
			if err != nil {
				return fmt.Errorf("invalid duration fraction: %v", err)
			}
			for len(frac) < 9 {
				nanos *= 10
				frac += "0"
			}
		}
		if neg {
			secs = -secs
			nanos = -nanos
		}
		if nanos < math.MinInt32 || nanos > math.MaxInt32 {
			return fmt.Errorf("duration: nanos out of range %d", nanos)
		}
		if err := validateDuration(secs, int32(nanos)); err != nil {
			return err
		}
		fdSec := pref.Descriptor().Fields().ByNumber(1)
		fdNano := pref.Descriptor().Fields().ByNumber(2)
		pref.Set(fdSec, protoreflect.ValueOfInt64(secs))
		pref.Set(fdNano, protoreflect.ValueOfInt32(int32(nanos)))
		return nil
	case "google.protobuf.DoubleValue",
		"google.protobuf.FloatValue",
		"google.protobuf.Int64Value",
		"google.protobuf.UInt64Value",
		"google.protobuf.Int32Value",
		"google.protobuf.UInt32Value",
		"google.protobuf.BoolValue",
		"google.protobuf.StringValue",
		"google.protobuf.BytesValue":
		fd := pref.Descriptor().Fields().ByNumber(1)
		if d.readNull() {
			pref.Clear(fd)
			return nil
		}
		switch fullName {
		case "google.protobuf.DoubleValue":
			var val float64
			var err error
			d.skipWhitespace()
			if d.off < len(d.data) && d.data[d.off] == '"' {
				s, err := d.readStringBytes()
				if err != nil {
					return err
				}
				val, err = strconv.ParseFloat(string(s), 64)
				if err != nil {
					return err
				}
			} else {
				val, err = d.readFloat64()
				if err != nil {
					return err
				}
			}
			pref.Set(fd, protoreflect.ValueOfFloat64(val))
		case "google.protobuf.FloatValue":
			var val float32
			var err error
			d.skipWhitespace()
			if d.off < len(d.data) && d.data[d.off] == '"' {
				s, err := d.readStringBytes()
				if err != nil {
					return err
				}
				v, err := strconv.ParseFloat(string(s), 32)
				if err != nil {
					return err
				}
				val = float32(v)
			} else {
				val, err = d.readFloat32()
				if err != nil {
					return err
				}
			}
			pref.Set(fd, protoreflect.ValueOfFloat64(float64(val)))
		case "google.protobuf.Int64Value":
			val, err := d.readInt64()
			if err != nil {
				return err
			}
			pref.Set(fd, protoreflect.ValueOfInt64(val))
		case "google.protobuf.UInt64Value":
			val, err := d.readUint64()
			if err != nil {
				return err
			}
			pref.Set(fd, protoreflect.ValueOfUint64(val))
		case "google.protobuf.Int32Value":
			val, err := d.readInt32()
			if err != nil {
				return err
			}
			pref.Set(fd, protoreflect.ValueOfInt32(val))
		case "google.protobuf.UInt32Value":
			val, err := d.readUint32()
			if err != nil {
				return err
			}
			pref.Set(fd, protoreflect.ValueOfUint32(val))
		case "google.protobuf.BoolValue":
			val, err := d.readBool()
			if err != nil {
				return err
			}
			pref.Set(fd, protoreflect.ValueOfBool(val))
		case "google.protobuf.StringValue":
			val, err := d.readStringBytes()
			if err != nil {
				return err
			}
			pref.Set(fd, protoreflect.ValueOfString(string(val)))
		case "google.protobuf.BytesValue":
			val, err := d.readStringBytes()
			if err != nil {
				return err
			}
			b, err := decodeBase64(string(val))
			if err != nil {
				return err
			}
			pref.Set(fd, protoreflect.ValueOfBytes(b))
		}
		return nil
	case "google.protobuf.FieldMask":
		return unmarshalFieldMask(pref, d)
	case "google.protobuf.Struct":
		return unmarshalStruct(pref, d, opts)
	case "google.protobuf.Value":
		return unmarshalValue(pref, d, opts)
	case "google.protobuf.ListValue":
		return unmarshalListValue(pref, d, opts)
	case "google.protobuf.Any":
		return unmarshalAny(pref, d, opts)
	}
	return fmt.Errorf("unknown custom well-known type: %s", fullName)
}

func unmarshalFieldMask(pref protoreflect.Message, d *decBuffer) error {
	bytes, err := d.readStringBytes()
	if err != nil {
		return err
	}
	str := strings.TrimSpace(string(bytes))
	if str == "" {
		return nil
	}
	parts := strings.Split(str, ",")
	fd := pref.Descriptor().Fields().ByNumber(1)
	list := pref.Mutable(fd).List()
	for list.Len() > 0 {
		list.Truncate(0)
	}
	for _, s0 := range parts {
		s := jsonSnakeCase(s0)
		if strings.Contains(s0, "_") || !protoreflect.FullName(s).IsValid() {
			return fmt.Errorf("paths contains invalid path: %q", s0)
		}
		list.Append(protoreflect.ValueOfString(s))
	}
	return nil
}

func unmarshalStruct(pref protoreflect.Message, d *decBuffer, opts UnmarshalOptions) error {
	fd := pref.Descriptor().Fields().ByNumber(1)
	m := pref.Mutable(fd).Map()
	m.Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
		m.Clear(k)
		return true
	})

	return d.parseObject(func(key []byte) error {
		val := m.NewValue()
		if err := unmarshalValue(val.Message(), d, opts); err != nil {
			return err
		}
		m.Set(protoreflect.ValueOfString(string(key)).MapKey(), val)
		return nil
	})
}

func unmarshalValue(pref protoreflect.Message, d *decBuffer, opts UnmarshalOptions) error {
	d.skipWhitespace()
	if d.off >= len(d.data) {
		return errors.New("unexpected end of JSON input")
	}
	c := d.data[d.off]
	switch c {
	case 'n':
		if d.readNull() {
			fd := pref.Descriptor().Fields().ByNumber(1)
			pref.Set(fd, protoreflect.ValueOfEnum(0))
			return nil
		}
		return errors.New("expected null")
	case 't', 'f':
		val, err := d.readBool()
		if err != nil {
			return err
		}
		fd := pref.Descriptor().Fields().ByNumber(4)
		pref.Set(fd, protoreflect.ValueOfBool(val))
		return nil
	case '"':
		bytes, err := d.readStringBytes()
		if err != nil {
			return err
		}
		s := string(bytes)
		fd := pref.Descriptor().Fields().ByNumber(3)
		pref.Set(fd, protoreflect.ValueOfString(s))
		return nil
	case '{':
		fd := pref.Descriptor().Fields().ByNumber(5)
		val := pref.NewField(fd)
		if err := unmarshalStruct(val.Message(), d, opts); err != nil {
			return err
		}
		pref.Set(fd, val)
		return nil
	case '[':
		fd := pref.Descriptor().Fields().ByNumber(6)
		val := pref.NewField(fd)
		if err := unmarshalListValue(val.Message(), d, opts); err != nil {
			return err
		}
		pref.Set(fd, val)
		return nil
	default:
		val, err := d.readFloat64()
		if err != nil {
			return err
		}
		fd := pref.Descriptor().Fields().ByNumber(2)
		pref.Set(fd, protoreflect.ValueOfFloat64(val))
		return nil
	}
}

func unmarshalListValue(pref protoreflect.Message, d *decBuffer, opts UnmarshalOptions) error {
	fd := pref.Descriptor().Fields().ByNumber(1)
	list := pref.Mutable(fd).List()
	for list.Len() > 0 {
		list.Truncate(0)
	}

	return d.parseArray(func() error {
		val := list.NewElement()
		if err := unmarshalValue(val.Message(), d, opts); err != nil {
			return err
		}
		list.Append(val)
		return nil
	})
}

var errMissingType = errors.New(`missing "@type" field`)
var errEmptyObject = errors.New(`empty object`)

func findTypeURL(d *decBuffer) (string, error) {
	dCopy := *d
	dCopy.skipWhitespace()
	if dCopy.off >= len(dCopy.data) || dCopy.data[dCopy.off] != '{' {
		return "", errors.New("expected '{'")
	}
	dCopy.off++

	typeURL := ""
	first := true
	numFields := 0
	for {
		dCopy.skipWhitespace()
		if dCopy.off >= len(dCopy.data) {
			return "", errors.New("unexpected EOF")
		}
		if dCopy.data[dCopy.off] == '}' {
			if typeURL == "" {
				if numFields > 0 {
					return "", errMissingType
				}
				return "", errEmptyObject
			}
			break
		}
		if !first {
			if dCopy.data[dCopy.off] != ',' {
				return "", errors.New("expected ','")
			}
			dCopy.off++
			dCopy.skipWhitespace()
		}
		first = false

		key, err := dCopy.readStringBytes()
		if err != nil {
			return "", err
		}
		dCopy.skipWhitespace()
		if dCopy.off >= len(dCopy.data) || dCopy.data[dCopy.off] != ':' {
			return "", errors.New("expected ':'")
		}
		dCopy.off++

		numFields++
		if string(key) == "@type" {
			if typeURL != "" {
				return "", errors.New(`duplicate "@type" field`)
			}
			valBytes, err := dCopy.readStringBytes()
			if err != nil {
				return "", err
			}
			typeURL = string(valBytes)
			if typeURL == "" {
				return "", errors.New("@type field contains empty value")
			}
		} else {
			if err := dCopy.skipValue(); err != nil {
				return "", err
			}
		}
	}
	return typeURL, nil
}

func unmarshalAny(pref protoreflect.Message, d *decBuffer, opts UnmarshalOptions) error {
	if d.readNull() {
		fdType := pref.Descriptor().Fields().ByNumber(1)
		fdValue := pref.Descriptor().Fields().ByNumber(2)
		pref.Clear(fdType)
		pref.Clear(fdValue)
		return nil
	}

	typeURL, err := findTypeURL(d)
	if err != nil {
		if err == errMissingType && opts.DiscardUnknown {
			return d.skipValue()
		}
		return err
	}

	if typeURL == "" {
		d.skipWhitespace()
		if d.off >= len(d.data) || d.data[d.off] != '{' {
			return errors.New("expected '{' for Any")
		}
		d.off++
		d.skipWhitespace()
		if d.off >= len(d.data) || d.data[d.off] != '}' {
			return errors.New("expected empty object for Any")
		}
		d.off++
		return nil
	}

	mt, err := protoregistry.GlobalTypes.FindMessageByURL(typeURL)
	if err != nil {
		return fmt.Errorf("unable to resolve type %q: %v", typeURL, err)
	}

	em := mt.New()

	if isCustomWellKnown(mt.Descriptor().FullName()) {
		d.skipWhitespace()
		if d.off >= len(d.data) || d.data[d.off] != '{' {
			return errors.New("expected '{' for Any")
		}
		d.off++

		first := true
		for {
			d.skipWhitespace()
			if d.off >= len(d.data) {
				return errors.New("unexpected EOF")
			}
			if d.data[d.off] == '}' {
				d.off++
				break
			}
			if !first {
				if d.data[d.off] != ',' {
					return errors.New("expected ','")
				}
				d.off++
				d.skipWhitespace()
			}
			first = false

			key, err := d.readStringBytes()
			if err != nil {
				return err
			}
			d.skipWhitespace()
			if d.off >= len(d.data) || d.data[d.off] != ':' {
				return errors.New("expected ':'")
			}
			d.off++

			if string(key) == "@type" {
				_, err := d.readStringBytes()
				if err != nil {
					return err
				}
			} else if string(key) == "value" {
				if err := unmarshalCustomWellKnown(em.Interface(), d, opts); err != nil {
					return err
				}
			} else {
				if opts.DiscardUnknown {
					if err := d.skipValue(); err != nil {
						return err
					}
				} else {
					return fmt.Errorf("unknown field in Any: %s", string(key))
				}
			}
		}
	} else {
		table, err := getTable(em.Interface())
		if err != nil {
			return err
		}
		ptr := unsafe.Pointer(reflect.ValueOf(em.Interface()).Pointer())

		var seenOneofs uint64
		seen := make(map[*fieldInstruction]struct{})
		seenExts := make(map[string]struct{})
		err = d.parseObject(func(key []byte) error {
			if string(key) == "@type" {
				_, err := d.readStringBytes()
				return err
			}
			return table.unmarshalField(ptr, d, opts, key, seen, seenExts, &seenOneofs)
		})
		if err != nil {
			return err
		}
	}

	binaryBytes, err := proto.MarshalOptions{
		AllowPartial: true,
	}.Marshal(em.Interface())
	if err != nil {
		return fmt.Errorf("error in marshaling Any.value field: %v", err)
	}

	fdType := pref.Descriptor().Fields().ByNumber(1)
	fdValue := pref.Descriptor().Fields().ByNumber(2)
	pref.Set(fdType, protoreflect.ValueOfString(typeURL))
	pref.Set(fdValue, protoreflect.ValueOfBytes(binaryBytes))
	return nil
}

func unmarshalProtoreflectValue(fd protoreflect.FieldDescriptor, target protoreflect.Message, d *decBuffer, opts UnmarshalOptions) (protoreflect.Value, error) {
	switch fd.Kind() {
	case protoreflect.StringKind:
		val, err := d.readStringBytes()
		if err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfString(string(val)), nil
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		val, err := d.readInt32()
		if err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfInt32(val), nil
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		val, err := d.readInt64()
		if err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfInt64(val), nil
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		val, err := d.readUint32()
		if err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfUint32(val), nil
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		val, err := d.readUint64()
		if err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfUint64(val), nil
	case protoreflect.FloatKind:
		val, err := d.readFloat32()
		if err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfFloat32(val), nil
	case protoreflect.DoubleKind:
		val, err := d.readFloat64()
		if err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfFloat64(val), nil
	case protoreflect.BoolKind:
		val, err := d.readBool()
		if err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfBool(val), nil
	case protoreflect.BytesKind:
		val, err := d.readStringBytes()
		if err != nil {
			return protoreflect.Value{}, err
		}
		b, err := decodeBase64(string(val))
		if err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfBytes(b), nil
	case protoreflect.EnumKind:
		d.skipWhitespace()
		if d.off < len(d.data) && d.data[d.off] == '"' {
			nameBytes, err := d.readStringBytes()
			if err != nil {
				return protoreflect.Value{}, err
			}
			name := string(nameBytes)
			enumDesc := fd.Enum()
			enumVal := enumDesc.Values().ByName(protoreflect.Name(name))
			if enumVal == nil {
				if opts.DiscardUnknown {
					return protoreflect.Value{}, errUnknownEnum
				}
				return protoreflect.Value{}, fmt.Errorf("unknown enum value name: %q", name)
			}
			return protoreflect.ValueOfEnum(enumVal.Number()), nil
		}
		val, err := d.readInt32()
		if err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfEnum(protoreflect.EnumNumber(val)), nil
	case protoreflect.MessageKind, protoreflect.GroupKind:
		msg := target.Interface()
		fullName := fd.Message().FullName()
		if isCustomWellKnown(fullName) {
			if err := unmarshalCustomWellKnown(msg, d, opts); err != nil {
				return protoreflect.Value{}, err
			}
			return protoreflect.ValueOfMessage(target), nil
		}
		subTable, err := getTable(msg)
		if err != nil {
			return protoreflect.Value{}, err
		}
		subMsgPtr := unsafe.Pointer(reflect.ValueOf(msg).Pointer())
		if len(subTable.fields) > 64 {
			subTable.resetIfNeeded(subMsgPtr)
			seen := make(map[*fieldInstruction]struct{}, len(subTable.fields))
			seenExts := make(map[string]struct{})
			var seenOneofs uint64
			err = d.parseObject(func(key []byte) error {
				return subTable.unmarshalField(subMsgPtr, d, opts, key, seen, seenExts, &seenOneofs)
			})
		} else {
			var seen uint64
			var seenExts map[string]struct{}
			var seenOneofs uint64
			err = d.parseObject(func(key []byte) error {
				if len(key) > 2 && key[0] == '[' && key[len(key)-1] == ']' {
					extName := string(key[1 : len(key)-1])
					xt, errExt := protoregistry.GlobalTypes.FindExtensionByName(protoreflect.FullName(extName))
					if errExt == nil {
						if seenExts == nil {
							seenExts = make(map[string]struct{})
						}
						if _, ok := seenExts[extName]; ok {
							return fmt.Errorf("duplicate field: %q", string(key))
						}
						seenExts[extName] = struct{}{}
						return unmarshalExtensionField(target, xt, d, opts)
					}
				}
				inst, err := subTable.unmarshalFieldInstruction(key, opts)
				if err != nil {
					if opts.DiscardUnknown && err == errUnknownField {
						return d.skipValue()
					}
					return err
				}
				if inst.ftype == TypeOneofField && !d.peekNull() {
					od := inst.fd.ContainingOneof()
					if od != nil {
						bit := uint64(1) << uint(od.Index())
						if seenOneofs&bit != 0 {
							return fmt.Errorf("duplicate oneof field: %s", key)
						}
						seenOneofs |= bit
					}
				}
				idx := uint(inst.index)
				if (seen & (1 << idx)) != 0 {
					return errors.New("duplicate field: " + unsafeString(key))
				}
				seen |= 1 << idx
				return subTable.unmarshalKnownField(subMsgPtr, d, opts, inst)
			})
		}
		if err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfMessage(target), nil
	default:
		return protoreflect.Value{}, fmt.Errorf("unsupported oneof field kind: %v", fd.Kind())
	}
}

func unmarshalMap(pref protoreflect.Message, inst *fieldInstruction, d *decBuffer, opts UnmarshalOptions) error {
	m := pref.Mutable(inst.fd).Map()
	m.Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
		m.Clear(k)
		return true
	})

	return d.parseObject(func(keyBytes []byte) error {
		keyStr := string(keyBytes)
		var mapKey protoreflect.MapKey

		keyDesc := inst.fd.MapKey()
		switch keyDesc.Kind() {
		case protoreflect.StringKind:
			mapKey = protoreflect.ValueOfString(keyStr).MapKey()
		case protoreflect.BoolKind:
			switch keyStr {
			case "true":
				mapKey = protoreflect.ValueOfBool(true).MapKey()
			case "false":
				mapKey = protoreflect.ValueOfBool(false).MapKey()
			default:
				return fmt.Errorf("invalid map bool key: %q", keyStr)
			}
		case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
			val, err := strconv.ParseInt(keyStr, 10, 32)
			if err != nil {
				return fmt.Errorf("invalid map int32 key: %v", err)
			}
			mapKey = protoreflect.ValueOfInt32(int32(val)).MapKey()
		case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
			val, err := strconv.ParseInt(keyStr, 10, 64)
			if err != nil {
				return fmt.Errorf("invalid map int64 key: %v", err)
			}
			mapKey = protoreflect.ValueOfInt64(val).MapKey()
		case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
			val, err := strconv.ParseUint(keyStr, 10, 32)
			if err != nil {
				return fmt.Errorf("invalid map uint32 key: %v", err)
			}
			mapKey = protoreflect.ValueOfUint32(uint32(val)).MapKey()
		case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
			val, err := strconv.ParseUint(keyStr, 10, 64)
			if err != nil {
				return fmt.Errorf("invalid map uint64 key: %v", err)
			}
			mapKey = protoreflect.ValueOfUint64(val).MapKey()
		default:
			return fmt.Errorf("unsupported map key kind: %v", keyDesc.Kind())
		}

		if m.Has(mapKey) {
			return fmt.Errorf("duplicate map key: %q", keyStr)
		}

		var target protoreflect.Message
		val := m.NewValue()
		if inst.fd.MapValue().Kind() == protoreflect.MessageKind {
			target = val.Message()
		}
		parsedVal, err := unmarshalProtoreflectValue(inst.fd.MapValue(), target, d, opts)
		if err != nil {
			if opts.DiscardUnknown && err == errUnknownEnum {
				return nil
			}
			return err
		}

		m.Set(mapKey, parsedVal)
		return nil
	})
}

func decodeBase64(s string) ([]byte, error) {
	if strings.ContainsAny(s, "-_") {
		s = strings.ReplaceAll(s, "-", "+")
		s = strings.ReplaceAll(s, "_", "/")
	}
	if len(s)%4 != 0 {
		s += strings.Repeat("=", 4-(len(s)%4))
	}
	return base64.StdEncoding.DecodeString(s)
}

func unmarshalExtensionField(pref protoreflect.Message, xt protoreflect.ExtensionType, d *decBuffer, opts UnmarshalOptions) error {
	fd := xt.TypeDescriptor()
	if fd.IsList() {
		pref.Clear(fd)
		list := pref.Mutable(fd).List()
		return d.parseArray(func() error {
			var target protoreflect.Message
			if fd.Kind() == protoreflect.MessageKind || fd.Kind() == protoreflect.GroupKind {
				target = list.NewElement().Message()
			}
			val, err := unmarshalProtoreflectValue(fd, target, d, opts)
			if err != nil {
				if opts.DiscardUnknown && err == errUnknownEnum {
					return nil
				}
				return err
			}
			list.Append(val)
			return nil
		})
	}
	if d.readNull() {
		pref.Clear(fd)
		return nil
	}
	var target protoreflect.Message
	if fd.Kind() == protoreflect.MessageKind || fd.Kind() == protoreflect.GroupKind {
		target = pref.Mutable(fd).Message()
	}
	val, err := unmarshalProtoreflectValue(fd, target, d, opts)
	if err != nil {
		if opts.DiscardUnknown && err == errUnknownEnum {
			return nil
		}
		return err
	}
	pref.Set(fd, val)
	return nil
}
