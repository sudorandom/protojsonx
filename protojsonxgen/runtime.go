package protojsonxgen

import (
	"cmp"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"

	"google.golang.org/protobuf/proto"
)

var (
	fallbackMarshal      func(proto.Message) ([]byte, error)
	fallbackUnmarshal    func([]byte, proto.Message) error
	fallbackUnmarshalOpt func([]byte, proto.Message, bool) error
)

// RegisterFallbacks connects generated code to the protojsonx runtime fallback.
func RegisterFallbacks(
	marshal func(proto.Message) ([]byte, error),
	unmarshal func([]byte, proto.Message) error,
	unmarshalOpt func([]byte, proto.Message, bool) error,
) {
	fallbackMarshal = marshal
	fallbackUnmarshal = unmarshal
	fallbackUnmarshalOpt = unmarshalOpt
}

func Marshal(m proto.Message) ([]byte, error) {
	if fallbackMarshal == nil {
		return nil, errors.New("protojsonx generated fallback is not registered")
	}
	return fallbackMarshal(m)
}

func Unmarshal(data []byte, m proto.Message) error {
	if fallbackUnmarshal == nil {
		return errors.New("protojsonx generated fallback is not registered")
	}
	return fallbackUnmarshal(data, m)
}

func UnmarshalWithOptions(data []byte, m proto.Message, discardUnknown bool) error {
	if fallbackUnmarshalOpt == nil {
		return errors.New("protojsonx generated fallback is not registered")
	}
	return fallbackUnmarshalOpt(data, m, discardUnknown)
}

func MarshalField(e *Encoder, m proto.Message) error {
	bytes, err := Marshal(m)
	if err != nil {
		return err
	}
	e.buf = append(e.buf, bytes...)
	return nil
}

func UnmarshalField(d *Decoder, m proto.Message, discardUnknown bool) error {
	raw, err := d.ReadRawValue()
	if err != nil {
		return err
	}
	return UnmarshalWithOptions(raw, m, discardUnknown)
}

var encPool = sync.Pool{
	New: func() any {
		return &Encoder{buf: make([]byte, 0, 1024)}
	},
}

type Encoder struct {
	buf []byte
}

func NewEncoder() *Encoder {
	e := encPool.Get().(*Encoder)
	e.buf = e.buf[:0]
	return e
}

func (e *Encoder) Bytes() []byte {
	out := make([]byte, len(e.buf))
	copy(out, e.buf)
	encPool.Put(e)
	return out
}

func (e *Encoder) Byte(c byte) {
	e.buf = append(e.buf, c)
}

func (e *Encoder) Raw(s string) {
	e.buf = append(e.buf, s...)
}

func (e *Encoder) FieldPrefix(wrote *bool, name string) {
	if *wrote {
		e.buf = append(e.buf, ',')
	}
	e.buf = append(e.buf, '"')
	e.buf = append(e.buf, name...)
	e.buf = append(e.buf, `":`...)
	*wrote = true
}

const hex = "0123456789abcdef"

func (e *Encoder) String(s string) {
	e.buf = append(e.buf, '"')
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c == '\\' || c == '"' {
			if start < i {
				e.buf = append(e.buf, s[start:i]...)
			}
			switch c {
			case '\\', '"':
				e.buf = append(e.buf, '\\', c)
			case '\n':
				e.buf = append(e.buf, '\\', 'n')
			case '\r':
				e.buf = append(e.buf, '\\', 'r')
			case '\t':
				e.buf = append(e.buf, '\\', 't')
			default:
				e.buf = append(e.buf, '\\', 'u', '0', '0', hex[c>>4], hex[c&0xF])
			}
			start = i + 1
		}
	}
	if start < len(s) {
		e.buf = append(e.buf, s[start:]...)
	}
	e.buf = append(e.buf, '"')
}

func (e *Encoder) BytesField(v []byte) {
	e.String(base64.StdEncoding.EncodeToString(v))
}

func (e *Encoder) Int32(v int32) {
	e.buf = strconv.AppendInt(e.buf, int64(v), 10)
}

func (e *Encoder) Int64String(v int64) {
	e.buf = append(e.buf, '"')
	e.buf = strconv.AppendInt(e.buf, v, 10)
	e.buf = append(e.buf, '"')
}

func (e *Encoder) Uint32(v uint32) {
	e.buf = strconv.AppendUint(e.buf, uint64(v), 10)
}

func (e *Encoder) Uint64String(v uint64) {
	e.buf = append(e.buf, '"')
	e.buf = strconv.AppendUint(e.buf, v, 10)
	e.buf = append(e.buf, '"')
}

func (e *Encoder) Float32(v float32) {
	e.float(float64(v), 32)
}

func (e *Encoder) Float64(v float64) {
	e.float(v, 64)
}

func (e *Encoder) float(v float64, bitSize int) {
	switch {
	case math.IsNaN(v):
		e.buf = append(e.buf, `"NaN"`...)
	case math.IsInf(v, +1):
		e.buf = append(e.buf, `"Infinity"`...)
	case math.IsInf(v, -1):
		e.buf = append(e.buf, `"-Infinity"`...)
	default:
		e.buf = strconv.AppendFloat(e.buf, v, 'g', -1, bitSize)
	}
}

func (e *Encoder) Bool(v bool) {
	if v {
		e.buf = append(e.buf, "true"...)
	} else {
		e.buf = append(e.buf, "false"...)
	}
}

func (e *Encoder) StringMap(v map[string]string) {
	e.buf = append(e.buf, '{')
	var arr [16]string
	var keys []string
	if len(v) <= len(arr) {
		keys = arr[:0]
	} else {
		keys = make([]string, 0, len(v))
	}
	for k := range v {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for i, k := range keys {
		if i > 0 {
			e.buf = append(e.buf, ',')
		}
		e.String(k)
		e.buf = append(e.buf, ':')
		e.String(v[k])
	}
	e.buf = append(e.buf, '}')
}

func (e *Encoder) Timestamp(secs int64, nanos int32) error {
	if err := ValidateTimestamp(secs, nanos); err != nil {
		return err
	}
	e.String(FormatTimestamp(secs, nanos))
	return nil
}

func (e *Encoder) Duration(secs int64, nanos int32) error {
	if err := ValidateDuration(secs, nanos); err != nil {
		return err
	}
	e.durationString(secs, nanos)
	return nil
}

func (e *Encoder) durationString(secs int64, nanos int32) {
	e.Byte('"')
	if nanos == 0 {
		e.buf = strconv.AppendInt(e.buf, secs, 10)
		e.buf = append(e.buf, 's', '"')
		return
	}

	neg := secs < 0 || nanos < 0
	if secs < 0 {
		secs = -secs
	}
	if nanos < 0 {
		nanos = -nanos
	}

	fracWidth := 9
	if nanos%1_000_000 == 0 {
		fracWidth = 3
		nanos /= 1_000_000
	} else if nanos%1_000 == 0 {
		fracWidth = 6
		nanos /= 1_000
	}

	if neg {
		e.Byte('-')
	}
	e.buf = strconv.AppendInt(e.buf, secs, 10)
	e.Byte('.')
	e.appendPaddedInt32(nanos, fracWidth)
	e.buf = append(e.buf, 's', '"')
}

func (e *Encoder) appendPaddedInt32(v int32, width int) {
	var tmp [10]byte
	i := len(tmp)
	for {
		i--
		tmp[i] = byte(v%10) + '0'
		v /= 10
		width--
		if v == 0 {
			break
		}
	}
	for width > 0 {
		e.Byte('0')
		width--
	}
	e.buf = append(e.buf, tmp[i:]...)
}

func FormatTimestamp(secs int64, nanos int32) string {
	t := time.Unix(secs, int64(nanos)).UTC()
	base := t.Format("2006-01-02T15:04:05")
	if nanos == 0 {
		return base + "Z"
	}
	if nanos%1000000 == 0 {
		return fmt.Sprintf("%s.%03dZ", base, nanos/1000000)
	}
	if nanos%1000 == 0 {
		return fmt.Sprintf("%s.%06dZ", base, nanos/1000)
	}
	return fmt.Sprintf("%s.%09dZ", base, nanos)
}

func FormatDuration(secs int64, nanos int32) string {
	var s string
	if secs == 0 && nanos < 0 {
		s = fmt.Sprintf("-0.%09ds", -nanos)
	} else if secs < 0 {
		s = fmt.Sprintf("-%d.%09ds", -secs, -nanos)
	} else {
		s = fmt.Sprintf("%d.%09ds", secs, nanos)
	}
	idx := -1
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			idx = i
			break
		}
	}
	if idx != -1 {
		end := len(s) - 1
		for end > idx && s[end-1] == '0' {
			end--
		}
		if s[end-1] == '.' {
			end--
		}
		s = s[:end] + "s"
	}
	return s
}

func ValidateTimestamp(secs int64, nanos int32) error {
	if secs < -62135596800 || secs > 253402300799 {
		return errors.New("timestamp out of range")
	}
	if nanos < 0 || nanos >= 1e9 {
		return errors.New("timestamp nanos out of range")
	}
	return nil
}

func ValidateDuration(secs int64, nanos int32) error {
	if secs < -315576000000 || secs > 315576000000 {
		return errors.New("duration seconds out of range")
	}
	if nanos <= -1e9 || nanos >= 1e9 {
		return errors.New("duration nanos out of range")
	}
	if (secs < 0 && nanos > 0) || (secs > 0 && nanos < 0) {
		return errors.New("duration seconds and nanos have different signs")
	}
	return nil
}

type Decoder struct {
	data  []byte
	off   int
	depth int
}

func NewDecoder(data []byte) *Decoder {
	return &Decoder{data: data}
}

func (d *Decoder) Mark() (int, int) {
	return d.off, d.depth
}

func (d *Decoder) Reset(off, depth int) {
	d.off = off
	d.depth = depth
}

func (d *Decoder) Finish() error {
	d.skipWhitespace()
	if d.off != len(d.data) {
		return errors.New("unexpected trailing data")
	}
	return nil
}

func (d *Decoder) skipWhitespace() {
	for d.off < len(d.data) {
		switch d.data[d.off] {
		case ' ', '\t', '\r', '\n':
			d.off++
		default:
			return
		}
	}
}

func (d *Decoder) ReadNull() bool {
	d.skipWhitespace()
	if d.off+4 <= len(d.data) && d.data[d.off] == 'n' && d.data[d.off+1] == 'u' && d.data[d.off+2] == 'l' && d.data[d.off+3] == 'l' {
		d.off += 4
		return true
	}
	return false
}

func (d *Decoder) IsString() bool {
	d.skipWhitespace()
	return d.off < len(d.data) && d.data[d.off] == '"'
}

func (d *Decoder) ReadString() (string, error) {
	b, err := d.readStringBytes()
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (d *Decoder) ReadStringBytes() ([]byte, error) {
	return d.readStringBytes()
}

func MatchStringBytes(b []byte, s string) bool {
	if len(b) != len(s) {
		return false
	}
	for i := range b {
		if b[i] != s[i] {
			return false
		}
	}
	return true
}

func (d *Decoder) ReadBytes() ([]byte, error) {
	b, err := d.readStringBytes()
	if err != nil {
		return nil, err
	}
	s := string(b)
	if strings.ContainsAny(s, "-_") {
		s = strings.ReplaceAll(s, "-", "+")
		s = strings.ReplaceAll(s, "_", "/")
	}
	if len(s)%4 != 0 {
		s += strings.Repeat("=", 4-(len(s)%4))
	}
	out := make([]byte, base64.StdEncoding.DecodedLen(len(s)))
	n, err := base64.StdEncoding.Decode(out, []byte(s))
	if err != nil {
		return nil, err
	}
	return out[:n], nil
}

func (d *Decoder) readStringBytes() ([]byte, error) {
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
			continue
		}
		if c < 0x20 {
			return nil, errors.New("invalid control character in string")
		}
		d.off++
	}
	return nil, errors.New("unterminated string")
}

func unescapeString(s []byte) ([]byte, error) {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' {
			if c < 0x20 {
				return nil, errors.New("invalid control character in string")
			}
			out = append(out, c)
			continue
		}
		i++
		if i >= len(s) {
			return nil, errors.New("unterminated escape sequence")
		}
		switch s[i] {
		case '"', '\\', '/':
			out = append(out, s[i])
		case 'b':
			out = append(out, '\b')
		case 'f':
			out = append(out, '\f')
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case 'u':
			if i+4 >= len(s) {
				return nil, errors.New("invalid unicode escape")
			}
			r, ok := parseHexRune(s[i+1 : i+5])
			if !ok {
				return nil, errors.New("invalid unicode escape")
			}
			i += 4
			if utf16.IsSurrogate(r) {
				if r < 0xD800 || r > 0xDBFF {
					return nil, errors.New("invalid unicode surrogate")
				}
				if i+6 >= len(s) || s[i+1] != '\\' || s[i+2] != 'u' {
					return nil, errors.New("invalid unicode surrogate pair")
				}
				r2, ok := parseHexRune(s[i+3 : i+7])
				if !ok || r2 < 0xDC00 || r2 > 0xDFFF {
					return nil, errors.New("invalid unicode surrogate pair")
				}
				i += 6
				r = utf16.DecodeRune(r, r2)
			}
			out = utf8.AppendRune(out, r)
		default:
			return nil, errors.New("invalid escape sequence")
		}
	}
	return out, nil
}

func parseHexRune(s []byte) (rune, bool) {
	if len(s) != 4 {
		return 0, false
	}
	var v rune
	for _, c := range s {
		v <<= 4
		switch {
		case c >= '0' && c <= '9':
			v |= rune(c - '0')
		case c >= 'a' && c <= 'f':
			v |= rune(c-'a') + 10
		case c >= 'A' && c <= 'F':
			v |= rune(c-'A') + 10
		default:
			return 0, false
		}
	}
	return v, true
}

func (d *Decoder) ReadInt32() (int32, error) {
	v, err := d.readInt64()
	if err != nil {
		return 0, err
	}
	if v < math.MinInt32 || v > math.MaxInt32 {
		return 0, fmt.Errorf("integer out of range for int32: %d", v)
	}
	return int32(v), nil
}

func (d *Decoder) ReadInt64() (int64, error) {
	return d.readInt64()
}

func (d *Decoder) readInt64() (int64, error) {
	d.skipWhitespace()
	if d.off < len(d.data) && d.data[d.off] != '"' {
		if v, ok, err := d.readSimpleInt64Token(); ok || err != nil {
			return v, err
		}
	}
	s, err := d.readNumberLike()
	if err != nil {
		return 0, err
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err == nil {
		return v, nil
	}
	if errors.Is(err, strconv.ErrRange) {
		return 0, err
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	if f < -9223372036854775808.0 || f >= 9223372036854775808.0 || f != math.Round(f) || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("invalid integer: %s", s)
	}
	return int64(f), nil
}

func (d *Decoder) ReadUint32() (uint32, error) {
	v, err := d.readUint64()
	if err != nil {
		return 0, err
	}
	if v > math.MaxUint32 {
		return 0, fmt.Errorf("integer out of range for uint32: %d", v)
	}
	return uint32(v), nil
}

func (d *Decoder) ReadUint64() (uint64, error) {
	return d.readUint64()
}

func (d *Decoder) readUint64() (uint64, error) {
	d.skipWhitespace()
	if d.off < len(d.data) && d.data[d.off] != '"' {
		if v, ok, err := d.readSimpleUint64Token(); ok || err != nil {
			return v, err
		}
	}
	s, err := d.readNumberLike()
	if err != nil {
		return 0, err
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err == nil {
		return v, nil
	}
	if errors.Is(err, strconv.ErrRange) {
		return 0, err
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	if f < 0 || f >= 18446744073709551616.0 || f != math.Round(f) || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("invalid integer: %s", s)
	}
	return uint64(f), nil
}

func (d *Decoder) readSimpleInt64Token() (int64, bool, error) {
	start := d.off
	neg := false
	if d.off < len(d.data) && d.data[d.off] == '-' {
		neg = true
		d.off++
	}
	if d.off >= len(d.data) || d.data[d.off] < '0' || d.data[d.off] > '9' {
		d.off = start
		return 0, false, nil
	}
	if d.data[d.off] == '0' && d.off+1 < len(d.data) && d.data[d.off+1] >= '0' && d.data[d.off+1] <= '9' {
		d.off = start
		return 0, false, nil
	}
	var u uint64
	for d.off < len(d.data) {
		c := d.data[d.off]
		if c < '0' || c > '9' {
			if c == '.' || c == 'e' || c == 'E' {
				d.off = start
				return 0, false, nil
			}
			if !isJSONValueTerminator(c) {
				d.off = start
				return 0, false, nil
			}
			break
		}
		u = u*10 + uint64(c-'0')
		if (!neg && u > math.MaxInt64) || (neg && u > uint64(math.MaxInt64)+1) {
			return 0, true, errors.New("integer out of range for int64")
		}
		d.off++
	}
	if neg {
		if u == uint64(math.MaxInt64)+1 {
			return math.MinInt64, true, nil
		}
		return -int64(u), true, nil
	}
	return int64(u), true, nil
}

func (d *Decoder) readSimpleUint64Token() (uint64, bool, error) {
	start := d.off
	if d.off >= len(d.data) || d.data[d.off] < '0' || d.data[d.off] > '9' {
		return 0, false, nil
	}
	if d.data[d.off] == '0' && d.off+1 < len(d.data) && d.data[d.off+1] >= '0' && d.data[d.off+1] <= '9' {
		return 0, false, nil
	}
	var u uint64
	for d.off < len(d.data) {
		c := d.data[d.off]
		if c < '0' || c > '9' {
			if c == '.' || c == 'e' || c == 'E' {
				d.off = start
				return 0, false, nil
			}
			if !isJSONValueTerminator(c) {
				d.off = start
				return 0, false, nil
			}
			break
		}
		prev := u
		u = u*10 + uint64(c-'0')
		if u < prev {
			return 0, true, errors.New("integer out of range for uint64")
		}
		d.off++
	}
	return u, true, nil
}

func (d *Decoder) ReadFloat32() (float32, error) {
	d.skipWhitespace()
	if d.off < len(d.data) && d.data[d.off] != '"' {
		if v, ok, err := d.readSimpleFloatToken(); ok || err != nil {
			return float32(v), err
		}
	}
	s, err := d.readNumberLike()
	if err != nil {
		return 0, err
	}
	v, err := strconv.ParseFloat(s, 32)
	return float32(v), err
}

func (d *Decoder) ReadFloat64() (float64, error) {
	d.skipWhitespace()
	if d.off < len(d.data) && d.data[d.off] != '"' {
		if v, ok, err := d.readSimpleFloatToken(); ok || err != nil {
			return v, err
		}
	}
	s, err := d.readNumberLike()
	if err != nil {
		return 0, err
	}
	return strconv.ParseFloat(s, 64)
}

func (d *Decoder) readSimpleFloatToken() (float64, bool, error) {
	start := d.off
	neg := false
	if d.off < len(d.data) && d.data[d.off] == '-' {
		neg = true
		d.off++
	}
	if d.off >= len(d.data) || d.data[d.off] < '0' || d.data[d.off] > '9' {
		d.off = start
		return 0, false, nil
	}
	var intPart uint64
	for d.off < len(d.data) && d.data[d.off] >= '0' && d.data[d.off] <= '9' {
		intPart = intPart*10 + uint64(d.data[d.off]-'0')
		d.off++
	}
	var frac float64
	if d.off < len(d.data) && d.data[d.off] == '.' {
		d.off++
		if d.off >= len(d.data) || d.data[d.off] < '0' || d.data[d.off] > '9' {
			d.off = start
			return 0, false, nil
		}
		scale := 0.1
		for d.off < len(d.data) && d.data[d.off] >= '0' && d.data[d.off] <= '9' {
			frac += float64(d.data[d.off]-'0') * scale
			scale *= 0.1
			d.off++
		}
	}
	if d.off < len(d.data) {
		c := d.data[d.off]
		if c == 'e' || c == 'E' {
			d.off = start
			return 0, false, nil
		}
		if !isJSONValueTerminator(c) {
			d.off = start
			return 0, false, nil
		}
	}
	v := float64(intPart) + frac
	if neg {
		v = -v
	}
	return v, true, nil
}

func (d *Decoder) readNumberLike() (string, error) {
	d.skipWhitespace()
	if d.off < len(d.data) && d.data[d.off] == '"' {
		return d.ReadString()
	}
	token, err := d.readJSONNumberToken()
	if err != nil {
		return "", err
	}
	return unsafeString(token), nil
}

func (d *Decoder) readJSONNumberToken() ([]byte, error) {
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

func (d *Decoder) ReadBool() (bool, error) {
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

func (d *Decoder) ParseObject(fn func(key string) error) error {
	if err := d.BeginObject(); err != nil {
		return err
	}
	first := true
	for {
		key, ok, err := d.NextObjectKey(&first)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if err := fn(key); err != nil {
			return err
		}
	}
}

func (d *Decoder) BeginObject() error {
	d.skipWhitespace()
	if d.off >= len(d.data) || d.data[d.off] != '{' {
		return errors.New("expected '{'")
	}
	d.depth++
	if d.depth > 100 {
		return errors.New("exceeded maximum recursion depth")
	}
	d.off++
	return nil
}

func (d *Decoder) TryObjectField(first *bool, name string) (matched bool, done bool, err error) {
	off, depth := d.Mark()
	d.skipWhitespace()
	if d.off >= len(d.data) {
		d.Reset(off, depth)
		return false, false, errors.New("unexpected EOF")
	}
	if d.data[d.off] == '}' {
		d.off++
		d.depth--
		return false, true, nil
	}
	if !*first {
		if d.data[d.off] != ',' {
			d.Reset(off, depth)
			return false, false, errors.New("expected ','")
		}
		d.off++
		d.skipWhitespace()
	}
	if d.off >= len(d.data) || d.data[d.off] != '"' {
		d.Reset(off, depth)
		return false, false, nil
	}
	d.off++
	if len(d.data)-d.off < len(name)+1 || string(d.data[d.off:d.off+len(name)]) != name || d.data[d.off+len(name)] != '"' {
		d.Reset(off, depth)
		return false, false, nil
	}
	d.off += len(name) + 1
	d.skipWhitespace()
	if d.off >= len(d.data) || d.data[d.off] != ':' {
		d.Reset(off, depth)
		return false, false, errors.New("expected ':'")
	}
	d.off++
	*first = false
	return true, false, nil
}

func (d *Decoder) TryObjectFieldFast(first *bool, name string) (matched bool, done bool, err error) {
	d.skipWhitespace()
	if d.off >= len(d.data) {
		return false, false, errors.New("unexpected EOF")
	}
	if d.data[d.off] == '}' {
		d.off++
		d.depth--
		return false, true, nil
	}
	if !*first {
		if d.data[d.off] != ',' {
			return false, false, errors.New("expected ','")
		}
		d.off++
		d.skipWhitespace()
	}
	if d.off >= len(d.data) || d.data[d.off] != '"' {
		return false, false, nil
	}
	d.off++
	if len(d.data)-d.off < len(name)+1 || string(d.data[d.off:d.off+len(name)]) != name || d.data[d.off+len(name)] != '"' {
		return false, false, nil
	}
	d.off += len(name) + 1
	d.skipWhitespace()
	if d.off >= len(d.data) || d.data[d.off] != ':' {
		return false, false, errors.New("expected ':'")
	}
	d.off++
	*first = false
	return true, false, nil
}

func (d *Decoder) MatchFast(first *bool, name string) (status int, err error) {
	matched, done, err := d.TryObjectFieldFast(first, name)
	if err != nil {
		return 0, err
	}
	if done {
		return 0, nil
	}
	if !matched {
		return -1, nil
	}
	return 1, nil
}

func (d *Decoder) TryEndObject() (bool, error) {
	d.skipWhitespace()
	if d.off >= len(d.data) {
		return false, errors.New("unexpected EOF")
	}
	if d.data[d.off] != '}' {
		return false, nil
	}
	d.off++
	d.depth--
	return true, nil
}

func (d *Decoder) NextObjectKey(first *bool) (string, bool, error) {
	d.skipWhitespace()
	if d.off >= len(d.data) {
		d.depth--
		return "", false, errors.New("unexpected EOF")
	}
	if d.data[d.off] == '}' {
		d.off++
		d.depth--
		return "", false, nil
	}
	if !*first {
		if d.data[d.off] != ',' {
			d.depth--
			return "", false, errors.New("expected ','")
		}
		d.off++
		d.skipWhitespace()
	}
	*first = false
	keyBytes, err := d.readObjectKeyFast()
	if err != nil {
		d.depth--
		return "", false, err
	}
	d.skipWhitespace()
	if d.off >= len(d.data) || d.data[d.off] != ':' {
		d.depth--
		return "", false, errors.New("expected ':'")
	}
	d.off++
	return unsafeString(keyBytes), true, nil
}

// readObjectKeyFast parses a JSON string that is expected to be a plain ASCII
// object key (e.g. a proto field name). It scans forward looking for the
// closing '"', bailing to the full escape-aware readStringBytes as soon as it
// encounters a backslash or a non-ASCII byte. This avoids the hasEscapes flag
// overhead for the common all-ASCII case.
func (d *Decoder) readObjectKeyFast() ([]byte, error) {
	if d.off >= len(d.data) || d.data[d.off] != '"' {
		return nil, errors.New("expected string")
	}
	d.off++ // consume opening '"'
	start := d.off
	for d.off < len(d.data) {
		c := d.data[d.off]
		if c == '"' {
			// Plain ASCII key — return a zero-copy slice.
			s := d.data[start:d.off]
			d.off++
			return s, nil
		}
		if c == '\\' || c >= 0x80 {
			// Escape or multibyte: rewind and let readStringBytes handle it.
			d.off = start - 1 // back to the opening '"'
			return d.readStringBytes()
		}
		if c < 0x20 {
			return nil, errors.New("invalid control character in string")
		}
		d.off++
	}
	return nil, errors.New("unterminated string")
}

// PeekObjectFieldName reads past optional leading whitespace and the '{' that
// opens a JSON object, then peeks the first key string without advancing past
// the ':' separator or the value. It returns the key name (as a string slice
// of the underlying buffer) and the decoder offset/depth to reset to if the
// caller wants to re-parse from the start of the object.
//
// The decoder is left positioned just after the ':' of the first key, ready
// for the caller to consume the value — or reset to (savedOff, savedDepth)
// to start over.
//
// Returns ("", 0, 0, false, nil) when the object is empty.
func (d *Decoder) PeekObjectFieldName() (name string, savedOff int, savedDepth int, ok bool, err error) {
	savedOff, savedDepth = d.Mark()
	d.skipWhitespace()
	if d.off >= len(d.data) || d.data[d.off] != '{' {
		return "", savedOff, savedDepth, false, errors.New("expected '{'")
	}
	d.depth++
	if d.depth > 100 {
		return "", savedOff, savedDepth, false, errors.New("exceeded maximum recursion depth")
	}
	d.off++ // consume '{'
	d.skipWhitespace()
	if d.off >= len(d.data) {
		return "", savedOff, savedDepth, false, errors.New("unexpected EOF")
	}
	if d.data[d.off] == '}' {
		// Empty object — reset so BeginObject/NextObjectKey sees it fresh.
		d.Reset(savedOff, savedDepth)
		return "", savedOff, savedDepth, false, nil
	}
	keyBytes, kerr := d.readObjectKeyFast()
	if kerr != nil {
		return "", savedOff, savedDepth, false, kerr
	}
	d.skipWhitespace()
	if d.off >= len(d.data) || d.data[d.off] != ':' {
		return "", savedOff, savedDepth, false, errors.New("expected ':'")
	}
	d.off++ // consume ':'
	return unsafeString(keyBytes), savedOff, savedDepth, true, nil
}

func (d *Decoder) ParseArray(fn func() error) error {
	if err := d.BeginArray(); err != nil {
		return err
	}
	first := true
	for {
		ok, err := d.NextArrayElement(&first)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if err := fn(); err != nil {
			return err
		}
	}
}

func (d *Decoder) BeginArray() error {
	d.skipWhitespace()
	if d.off >= len(d.data) || d.data[d.off] != '[' {
		return errors.New("expected '['")
	}
	d.depth++
	if d.depth > 100 {
		return errors.New("exceeded maximum recursion depth")
	}
	d.off++
	return nil
}

func (d *Decoder) NextArrayElement(first *bool) (bool, error) {
	d.skipWhitespace()
	if d.off >= len(d.data) {
		d.depth--
		return false, errors.New("unexpected EOF")
	}
	if d.data[d.off] == ']' {
		d.off++
		d.depth--
		return false, nil
	}
	if !*first {
		if d.data[d.off] != ',' {
			d.depth--
			return false, errors.New("expected ','")
		}
		d.off++
		d.skipWhitespace()
	}
	*first = false
	return true, nil
}

func (d *Decoder) SkipValue() error {
	d.skipWhitespace()
	if d.off >= len(d.data) {
		return errors.New("unexpected EOF")
	}
	switch d.data[d.off] {
	case '{':
		return d.ParseObject(func(string) error {
			return d.SkipValue()
		})
	case '[':
		return d.ParseArray(func() error {
			return d.SkipValue()
		})
	case '"':
		_, err := d.ReadString()
		return err
	default:
		start := d.off
		for d.off < len(d.data) && !isJSONValueTerminator(d.data[d.off]) {
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

func (d *Decoder) ReadRawValue() ([]byte, error) {
	d.skipWhitespace()
	start := d.off
	if err := d.SkipValue(); err != nil {
		return nil, err
	}
	return d.data[start:d.off], nil
}

func (d *Decoder) ReadTimestamp() (int64, int32, error) {
	s, err := d.ReadStringBytes()
	if err != nil {
		return 0, 0, err
	}
	if secs, nanos, ok, err := parseUTCTimestampBytes(s); ok || err != nil {
		if err != nil {
			return 0, 0, err
		}
		return secs, nanos, nil
	}
	t, err := time.Parse(time.RFC3339Nano, string(s))
	if err != nil {
		return 0, 0, err
	}
	secs := t.Unix()
	nanos := int32(t.Nanosecond())
	if err := ValidateTimestamp(secs, nanos); err != nil {
		return 0, 0, err
	}
	return secs, nanos, nil
}

func parseUTCTimestampBytes(s []byte) (int64, int32, bool, error) {
	if len(s) < len("2006-01-02T15:04:05Z") || s[4] != '-' || s[7] != '-' || s[10] != 'T' || s[13] != ':' || s[16] != ':' {
		return 0, 0, false, nil
	}
	year, ok := parseNDigitsBytes(s, 0, 4)
	if !ok {
		return 0, 0, false, nil
	}
	month, ok := parseNDigitsBytes(s, 5, 2)
	if !ok {
		return 0, 0, false, nil
	}
	day, ok := parseNDigitsBytes(s, 8, 2)
	if !ok {
		return 0, 0, false, nil
	}
	hour, ok := parseNDigitsBytes(s, 11, 2)
	if !ok {
		return 0, 0, false, nil
	}
	minute, ok := parseNDigitsBytes(s, 14, 2)
	if !ok {
		return 0, 0, false, nil
	}
	second, ok := parseNDigitsBytes(s, 17, 2)
	if !ok {
		return 0, 0, false, nil
	}
	pos := 19
	var nanos int32
	if pos < len(s) && s[pos] == '.' {
		pos++
		start := pos
		var n int32
		for pos < len(s) && s[pos] >= '0' && s[pos] <= '9' {
			if pos-start >= 9 {
				return 0, 0, true, errors.New("timestamp fractional seconds out of range")
			}
			n = n*10 + int32(s[pos]-'0')
			pos++
		}
		if pos == start {
			return 0, 0, true, errors.New("timestamp missing fractional seconds")
		}
		for i := pos - start; i < 9; i++ {
			n *= 10
		}
		nanos = n
	}
	if pos != len(s)-1 || s[pos] != 'Z' {
		return 0, 0, false, nil
	}
	if month < 1 || month > 12 || day < 1 || day > daysInMonth(year, month) || hour > 23 || minute > 59 || second > 59 {
		return 0, 0, true, errors.New("invalid timestamp")
	}
	secs := daysSinceUnixEpoch(year, month, day)*86400 + int64(hour*3600+minute*60+second)
	if err := ValidateTimestamp(secs, nanos); err != nil {
		return 0, 0, true, err
	}
	return secs, nanos, true, nil
}

func daysInMonth(year, month int) int {
	switch month {
	case 1, 3, 5, 7, 8, 10, 12:
		return 31
	case 4, 6, 9, 11:
		return 30
	case 2:
		if isLeapYear(year) {
			return 29
		}
		return 28
	default:
		return 0
	}
}

func isLeapYear(year int) bool {
	return year%4 == 0 && (year%100 != 0 || year%400 == 0)
}

func daysSinceUnixEpoch(year, month, day int) int64 {
	y := year
	m := month
	if m <= 2 {
		y--
	}
	era := divFloor(y, 400)
	yoe := y - era*400
	mp := m
	if m > 2 {
		mp -= 3
	} else {
		mp += 9
	}
	doy := (153*mp+2)/5 + day - 1
	doe := yoe*365 + yoe/4 - yoe/100 + doy
	return int64(era*146097+doe) - 719468
}

func divFloor(a, b int) int {
	q := a / b
	r := a % b
	if r != 0 && ((r < 0) != (b < 0)) {
		q--
	}
	return q
}

func parseNDigitsBytes(s []byte, off, n int) (int, bool) {
	if off+n > len(s) {
		return 0, false
	}
	var v int
	for i := 0; i < n; i++ {
		c := s[off+i]
		if c < '0' || c > '9' {
			return 0, false
		}
		v = v*10 + int(c-'0')
	}
	return v, true
}

func (d *Decoder) ReadDuration() (int64, int32, error) {
	s, err := d.ReadStringBytes()
	if err != nil {
		return 0, 0, err
	}
	return parseDurationBytes(s)
}

func parseDurationBytes(s []byte) (int64, int32, error) {
	if len(s) < 2 || s[len(s)-1] != 's' {
		return 0, 0, errors.New("duration must end in s")
	}
	s = s[:len(s)-1]
	if len(s) == 0 {
		return 0, 0, errors.New("empty duration")
	}
	neg := false
	if s[0] == '-' || s[0] == '+' {
		neg = s[0] == '-'
		s = s[1:]
		if len(s) == 0 {
			return 0, 0, errors.New("empty duration")
		}
	}
	dot := -1
	for i, c := range s {
		if c == '.' {
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
	if len(intPart) == 0 {
		return 0, 0, errors.New("invalid duration")
	}
	var secs int64
	for _, c := range intPart {
		if c < '0' || c > '9' {
			return 0, 0, errors.New("invalid duration")
		}
		if secs > (math.MaxInt64-int64(c-'0'))/10 {
			return 0, 0, errors.New("duration seconds out of range")
		}
		secs = secs*10 + int64(c-'0')
	}
	var nanos int64
	if dot >= 0 {
		frac := s[dot+1:]
		if len(frac) == 0 || len(frac) > 9 {
			return 0, 0, errors.New("invalid duration fractional seconds")
		}
		for _, c := range frac {
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
	if err := ValidateDuration(secs, int32(nanos)); err != nil {
		return 0, 0, err
	}
	return secs, int32(nanos), nil
}

func ParseDuration(s string) (int64, int32, error) {
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
	if err := ValidateDuration(secs, int32(nanos)); err != nil {
		return 0, 0, err
	}
	return secs, int32(nanos), nil
}

func DuplicateField(name string) error {
	return errors.New("duplicate field: " + name)
}

func UnknownField(name string) error {
	return errors.New("unknown field: " + name)
}

var ErrUnknownEnum = errors.New("unknown enum value name")
var ErrIgnoredMapEntry = errors.New("ignored map entry")

func UnknownEnumValue(name string) error {
	return errors.New("unknown enum value: " + name)
}

func NullRepeatedMessage() error {
	return errors.New("repeated message field contains null element")
}

func unsafeString(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return unsafe.String(unsafe.SliceData(b), len(b))
}

func WriteMap[K comparable, V any](e *Encoder, m map[K]V, keyToString func(K) string, valEnc func(*Encoder, V) error) error {
	if m == nil {
		return nil
	}
	e.Byte('{')
	keys := make([]K, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	switch ts := any(keys).(type) {
	case []int32:
		slices.SortFunc(ts, func(a, b int32) int {
			return cmp.Compare(a, b)
		})
	case []int64:
		slices.SortFunc(ts, func(a, b int64) int {
			return cmp.Compare(a, b)
		})
	case []uint32:
		slices.SortFunc(ts, func(a, b uint32) int {
			return cmp.Compare(a, b)
		})
	case []uint64:
		slices.SortFunc(ts, func(a, b uint64) int {
			return cmp.Compare(a, b)
		})
	case []bool:
		slices.SortFunc(ts, func(a, b bool) int {
			if !a && b {
				return -1
			} else if a && !b {
				return 1
			}
			return 0
		})
	default:
		keyStrings := make(map[K]string, len(m))
		for _, k := range keys {
			keyStrings[k] = keyToString(k)
		}
		slices.SortFunc(keys, func(a, b K) int {
			return cmp.Compare(keyStrings[a], keyStrings[b])
		})
	}
	keyStrings := make(map[K]string, len(m))
	for _, k := range keys {
		keyStrings[k] = keyToString(k)
	}
	for i, k := range keys {
		if i > 0 {
			e.Byte(',')
		}
		e.String(keyStrings[k])
		e.Byte(':')
		if err := valEnc(e, m[k]); err != nil {
			return err
		}
	}
	e.Byte('}')
	return nil
}

func ReadMap[K comparable, V any](d *Decoder, m map[K]V, keyParse func(string) (K, error), valDec func(*Decoder) (V, error)) error {
	if err := d.BeginObject(); err != nil {
		return err
	}
	first := true
	for {
		keyStr, ok, err := d.NextObjectKey(&first)
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		key, err := keyParse(keyStr)
		if err != nil {
			return err
		}
		if _, exists := m[key]; exists {
			return DuplicateField(keyStr)
		}
		val, err := valDec(d)
		if err != nil {
			if errors.Is(err, ErrIgnoredMapEntry) {
				continue
			}
			return err
		}
		m[key] = val
	}
	return nil
}

func MarshalMessageMapValue[V proto.Message](e *Encoder, v V) error {
	if any(v) == nil || reflect.ValueOf(v).IsNil() {
		e.Raw("null")
		return nil
	}
	if fast, ok := any(v).(interface{ marshalProtoJSONXTo(*Encoder) error }); ok {
		return fast.marshalProtoJSONXTo(e)
	}
	return MarshalField(e, v)
}

func UnmarshalMessageMapValue[V any, PT interface {
	*V
	proto.Message
}](d *Decoder, discardUnknown bool) (PT, error) {
	if d.ReadNull() {
		return nil, nil
	}
	var v V
	var pt PT = &v
	if fast, ok := any(pt).(interface {
		unmarshalProtoJSONXFast(*Decoder, bool) (bool, error)
	}); ok {
		if ok, err := fast.unmarshalProtoJSONXFast(d, discardUnknown); err != nil {
			return nil, err
		} else if !ok {
			if err := any(pt).(interface {
				unmarshalProtoJSONXFrom(*Decoder, bool) error
			}).unmarshalProtoJSONXFrom(d, discardUnknown); err != nil {
				return nil, err
			}
		}
	} else {
		if err := UnmarshalField(d, pt, discardUnknown); err != nil {
			return nil, err
		}
	}
	return pt, nil
}
