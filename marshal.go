package protojsonx

// Encoding strategy:
//
// Marshal uses a compiled MessageTable instead of protobuf reflection at
// runtime. Each fieldInstruction stores the generated Go struct offset and a
// small field-kind enum. The encoder walks that instruction slice in proto
// field order, reads values directly with unsafe.Add, and appends JSON into a
// pooled byte buffer. This keeps the hot path predictable: one switch per
// supported field, no reflective field lookups, and a single copy into the
// returned []byte.
//
// The implementation intentionally supports a focused protojson subset. Table
// compilation rejects unsupported schemas up front, which is safer than
// silently dropping fields during marshal.

import (
	"encoding/base64"
	"errors"
	"math"
	"reflect"
	"strconv"
	"sync"
	"time"
	"unsafe"

	"fmt"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"slices"
	"strings"
)

// encBufPool reuses the scratch buffer used while building JSON. Marshal still
// returns an owned copy, so callers can safely retain or mutate the result.
var encBufPool = sync.Pool{
	New: func() any {
		return &encBuffer{buf: make([]byte, 0, 1024)}
	},
}

type encBuffer struct {
	buf []byte
}

func (b *encBuffer) writeByte(c byte) {
	b.buf = append(b.buf, c)
}

const hex = "0123456789abcdef"

func (b *encBuffer) writeEscapedString(s string) {
	b.buf = append(b.buf, '"')
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c == '\\' || c == '"' {
			if start < i {
				b.buf = append(b.buf, s[start:i]...)
			}
			switch c {
			case '\\', '"':
				b.buf = append(b.buf, '\\', c)
			case '\n':
				b.buf = append(b.buf, '\\', 'n')
			case '\r':
				b.buf = append(b.buf, '\\', 'r')
			case '\t':
				b.buf = append(b.buf, '\\', 't')
			default:
				b.buf = append(b.buf, '\\', 'u', '0', '0', hex[c>>4], hex[c&0xF])
			}
			start = i + 1
		}
	}
	if start < len(s) {
		b.buf = append(b.buf, s[start:]...)
	}
	b.buf = append(b.buf, '"')
}

func (b *encBuffer) writeInt64String(v int64) {
	b.buf = append(b.buf, '"')
	b.buf = strconv.AppendInt(b.buf, v, 10)
	b.buf = append(b.buf, '"')
}

func (b *encBuffer) writeUint64String(v uint64) {
	b.buf = append(b.buf, '"')
	b.buf = strconv.AppendUint(b.buf, v, 10)
	b.buf = append(b.buf, '"')
}

func (b *encBuffer) writeInt64(v int64) {
	b.buf = strconv.AppendInt(b.buf, v, 10)
}

func (b *encBuffer) writeUint64(v uint64) {
	b.buf = strconv.AppendUint(b.buf, v, 10)
}

func (b *encBuffer) writeFloat64(v float64, bitSize int) {
	switch {
	case math.IsNaN(v):
		b.buf = append(b.buf, `"NaN"`...)
	case math.IsInf(v, +1):
		b.buf = append(b.buf, `"Infinity"`...)
	case math.IsInf(v, -1):
		b.buf = append(b.buf, `"-Infinity"`...)
	default:
		b.buf = strconv.AppendFloat(b.buf, v, 'g', -1, bitSize)
	}
}

func (b *encBuffer) writeBool(v bool) {
	if v {
		b.buf = append(b.buf, "true"...)
	} else {
		b.buf = append(b.buf, "false"...)
	}
}

// stringSlicePool avoids allocating a key slice on every map encode. Map keys
// are sorted to keep output deterministic.
var stringSlicePool = sync.Pool{
	New: func() interface{} {
		s := make([]string, 0, 16)
		return &s
	},
}

type MarshalOptions struct {
	EmitUnpopulated bool
	UseProtoNames   bool
}

// Marshal message with default options
func Marshal(msg proto.Message) ([]byte, error) {
	return MarshalOptions{}.Marshal(msg)
}

func (o MarshalOptions) Marshal(msg proto.Message) ([]byte, error) {
	val := reflect.ValueOf(msg)
	if !val.IsValid() || val.Kind() != reflect.Pointer || val.IsNil() {
		return nil, errors.New("marshal target must be non-nil pointer")
	}

	table, err := getTable(msg)
	if err != nil {
		return nil, err
	}

	if isCustomWellKnown(table.fullName) {
		eb := encBufPool.Get().(*encBuffer)
		eb.buf = eb.buf[:0]
		err := marshalCustomWellKnown(msg, eb, o)
		if err != nil {
			encBufPool.Put(eb)
			return nil, err
		}
		data := make([]byte, len(eb.buf))
		copy(data, eb.buf)
		encBufPool.Put(eb)
		return data, nil
	}

	eb := encBufPool.Get().(*encBuffer)
	eb.buf = eb.buf[:0]

	ptr := val.UnsafePointer()

	err = table.marshalTo(ptr, eb, o)
	if err != nil {
		encBufPool.Put(eb)
		return nil, err
	}

	out := make([]byte, len(eb.buf))
	copy(out, eb.buf)
	encBufPool.Put(eb)
	return out, nil
}

// writeDurationString emits protobuf Duration JSON without constructing an
// intermediate string. Duration precision is integer-based so large durations
// do not lose nanoseconds through float64 rounding.
func (b *encBuffer) writeDurationString(secs int64, nanos int32) {
	b.writeByte('"')
	if nanos == 0 {
		b.buf = strconv.AppendInt(b.buf, secs, 10)
		b.buf = append(b.buf, 's', '"')
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
		b.writeByte('-')
	}
	b.buf = strconv.AppendInt(b.buf, secs, 10)
	b.writeByte('.')
	b.appendPaddedInt32(nanos, fracWidth)
	b.buf = append(b.buf, 's', '"')
}

// appendPaddedInt32 appends a fractional seconds component already scaled to
// milliseconds, microseconds, or nanoseconds.
func (b *encBuffer) appendPaddedInt32(v int32, width int) {
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
		b.writeByte('0')
		width--
	}
	b.buf = append(b.buf, tmp[i:]...)
}

// marshalTo is the recursive encoder used for both root and nested messages.
// ptr must point at the generated message struct matching table.goType.
func (table *MessageTable) marshalTo(ptr unsafe.Pointer, b *encBuffer, opts MarshalOptions) error {
	b.writeByte('{')
	wroteAny := false
	var pref protoreflect.Message

	for i := range table.fields {
		inst := &table.fields[i]
		fieldPtr := unsafe.Add(ptr, inst.offset)
		fieldName := inst.jsonName
		if opts.UseProtoNames {
			fieldName = inst.protoName
		}

		switch inst.ftype {
		case TypeString:
			var val string
			present := true
			if inst.isOptional {
				ptrVal := *(*unsafe.Pointer)(fieldPtr)
				if ptrVal == nil {
					present = false
				} else {
					val = *(*string)(ptrVal)
				}
			} else {
				val = *(*string)(fieldPtr)
			}
			if (inst.isOptional && present) || (!inst.isOptional && val != "") || opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":`...)
				if present {
					b.writeEscapedString(val)
				} else {
					b.buf = append(b.buf, "null"...)
				}
				wroteAny = true
			}
		case TypeInt32:
			var val int32
			present := true
			if inst.isOptional {
				ptrVal := *(*unsafe.Pointer)(fieldPtr)
				if ptrVal == nil {
					present = false
				} else {
					val = *(*int32)(ptrVal)
				}
			} else {
				val = *(*int32)(fieldPtr)
			}
			if (inst.isOptional && present) || (!inst.isOptional && val != 0) || opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":`...)
				if present {
					b.writeInt64(int64(val))
				} else {
					b.buf = append(b.buf, "null"...)
				}
				wroteAny = true
			}
		case TypeInt64:
			var val int64
			present := true
			if inst.isOptional {
				ptrVal := *(*unsafe.Pointer)(fieldPtr)
				if ptrVal == nil {
					present = false
				} else {
					val = *(*int64)(ptrVal)
				}
			} else {
				val = *(*int64)(fieldPtr)
			}
			if (inst.isOptional && present) || (!inst.isOptional && val != 0) || opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":`...)
				if present {
					b.writeInt64String(val)
				} else {
					b.buf = append(b.buf, "null"...)
				}
				wroteAny = true
			}
		case TypeUint32:
			var val uint32
			present := true
			if inst.isOptional {
				ptrVal := *(*unsafe.Pointer)(fieldPtr)
				if ptrVal == nil {
					present = false
				} else {
					val = *(*uint32)(ptrVal)
				}
			} else {
				val = *(*uint32)(fieldPtr)
			}
			if (inst.isOptional && present) || (!inst.isOptional && val != 0) || opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":`...)
				if present {
					b.writeUint64(uint64(val))
				} else {
					b.buf = append(b.buf, "null"...)
				}
				wroteAny = true
			}
		case TypeUint64:
			var val uint64
			present := true
			if inst.isOptional {
				ptrVal := *(*unsafe.Pointer)(fieldPtr)
				if ptrVal == nil {
					present = false
				} else {
					val = *(*uint64)(ptrVal)
				}
			} else {
				val = *(*uint64)(fieldPtr)
			}
			if (inst.isOptional && present) || (!inst.isOptional && val != 0) || opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":`...)
				if present {
					b.writeUint64String(val)
				} else {
					b.buf = append(b.buf, "null"...)
				}
				wroteAny = true
			}
		case TypeFloat32:
			var val float32
			present := true
			if inst.isOptional {
				ptrVal := *(*unsafe.Pointer)(fieldPtr)
				if ptrVal == nil {
					present = false
				} else {
					val = *(*float32)(ptrVal)
				}
			} else {
				val = *(*float32)(fieldPtr)
			}
			if (inst.isOptional && present) || (!inst.isOptional && val != 0) || opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":`...)
				if present {
					b.writeFloat64(float64(val), 32)
				} else {
					b.buf = append(b.buf, "null"...)
				}
				wroteAny = true
			}
		case TypeFloat64:
			var val float64
			present := true
			if inst.isOptional {
				ptrVal := *(*unsafe.Pointer)(fieldPtr)
				if ptrVal == nil {
					present = false
				} else {
					val = *(*float64)(ptrVal)
				}
			} else {
				val = *(*float64)(fieldPtr)
			}
			if (inst.isOptional && present) || (!inst.isOptional && val != 0) || opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":`...)
				if present {
					b.writeFloat64(val, 64)
				} else {
					b.buf = append(b.buf, "null"...)
				}
				wroteAny = true
			}
		case TypeBool:
			var val bool
			present := true
			if inst.isOptional {
				ptrVal := *(*unsafe.Pointer)(fieldPtr)
				if ptrVal == nil {
					present = false
				} else {
					val = *(*bool)(ptrVal)
				}
			} else {
				val = *(*bool)(fieldPtr)
			}
			if (inst.isOptional && present) || (!inst.isOptional && val) || opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":`...)
				if present {
					b.writeBool(val)
				} else {
					b.buf = append(b.buf, "null"...)
				}
				wroteAny = true
			}
		case TypeBytes:
			val := *(*[]byte)(fieldPtr)
			present := val != nil
			if (inst.isOptional && present) || (!inst.isOptional && len(val) > 0) || opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":`...)
				if present {
					b.writeEscapedString(base64.StdEncoding.EncodeToString(val))
				} else {
					b.buf = append(b.buf, "null"...)
				}
				wroteAny = true
			}
		case TypeEnum:
			var val int32
			present := true
			if inst.isOptional {
				ptrVal := *(*unsafe.Pointer)(fieldPtr)
				if ptrVal == nil {
					present = false
				} else {
					val = *(*int32)(ptrVal)
				}
			} else {
				val = *(*int32)(fieldPtr)
			}
			if (inst.isOptional && present) || (!inst.isOptional && val != 0) || opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":`...)
				if present {
					enumStr, ok := inst.enumNameMap[val]
					if ok {
						b.writeEscapedString(enumStr)
					} else {
						b.writeInt64(int64(val))
					}
				} else {
					b.buf = append(b.buf, "null"...)
				}
				wroteAny = true
			}
		case TypeRepeatedString:
			val := *(*[]string)(fieldPtr)
			if len(val) > 0 || opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":[`...)
				for j, s := range val {
					if j > 0 {
						b.writeByte(',')
					}
					b.writeEscapedString(s)
				}
				b.writeByte(']')
				wroteAny = true
			}
		case TypeRepeatedInt32:
			val := *(*[]int32)(fieldPtr)
			if len(val) > 0 || opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":[`...)
				for j, v := range val {
					if j > 0 {
						b.writeByte(',')
					}
					b.writeInt64(int64(v))
				}
				b.writeByte(']')
				wroteAny = true
			}
		case TypeRepeatedInt64:
			val := *(*[]int64)(fieldPtr)
			if len(val) > 0 || opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":[`...)
				for j, v := range val {
					if j > 0 {
						b.writeByte(',')
					}
					b.writeInt64String(v)
				}
				b.writeByte(']')
				wroteAny = true
			}
		case TypeRepeatedUint32:
			val := *(*[]uint32)(fieldPtr)
			if len(val) > 0 || opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":[`...)
				for j, v := range val {
					if j > 0 {
						b.writeByte(',')
					}
					b.writeUint64(uint64(v))
				}
				b.writeByte(']')
				wroteAny = true
			}
		case TypeRepeatedUint64:
			val := *(*[]uint64)(fieldPtr)
			if len(val) > 0 || opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":[`...)
				for j, v := range val {
					if j > 0 {
						b.writeByte(',')
					}
					b.writeUint64String(v)
				}
				b.writeByte(']')
				wroteAny = true
			}
		case TypeRepeatedFloat32:
			val := *(*[]float32)(fieldPtr)
			if len(val) > 0 || opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":[`...)
				for j, v := range val {
					if j > 0 {
						b.writeByte(',')
					}
					b.writeFloat64(float64(v), 32)
				}
				b.writeByte(']')
				wroteAny = true
			}
		case TypeRepeatedFloat64:
			val := *(*[]float64)(fieldPtr)
			if len(val) > 0 || opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":[`...)
				for j, v := range val {
					if j > 0 {
						b.writeByte(',')
					}
					b.writeFloat64(v, 64)
				}
				b.writeByte(']')
				wroteAny = true
			}
		case TypeRepeatedBool:
			val := *(*[]bool)(fieldPtr)
			if len(val) > 0 || opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":[`...)
				for j, v := range val {
					if j > 0 {
						b.writeByte(',')
					}
					b.writeBool(v)
				}
				b.writeByte(']')
				wroteAny = true
			}
		case TypeRepeatedBytes:
			val := *(*[][]byte)(fieldPtr)
			if len(val) > 0 || opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":[`...)
				for j, v := range val {
					if j > 0 {
						b.writeByte(',')
					}
					b.writeEscapedString(base64.StdEncoding.EncodeToString(v))
				}
				b.writeByte(']')
				wroteAny = true
			}
		case TypeRepeatedEnum:
			val := *(*[]int32)(fieldPtr)
			if len(val) > 0 || opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":[`...)
				for j, v := range val {
					if j > 0 {
						b.writeByte(',')
					}
					enumStr, ok := inst.enumNameMap[v]
					if ok {
						b.writeEscapedString(enumStr)
					} else {
						b.writeInt64(int64(v))
					}
				}
				b.writeByte(']')
				wroteAny = true
			}
		case TypeMapStringString:
			val := *(*map[string]string)(fieldPtr)
			if len(val) > 0 || opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":{`...)

				var arr [16]string
				var keys []string
				if len(val) <= 16 {
					keys = arr[:0]
				} else {
					keys = make([]string, 0, len(val))
				}
				for k := range val {
					keys = append(keys, k)
				}
				slices.Sort(keys)

				for j, k := range keys {
					if j > 0 {
						b.writeByte(',')
					}
					b.writeEscapedString(k)
					b.writeByte(':')
					b.writeEscapedString(val[k])
				}

				b.writeByte('}')
				wroteAny = true
			}
		case TypeMessage:
			subMsgPtr := *(*unsafe.Pointer)(fieldPtr)
			if subMsgPtr != nil {
				if inst.msgNeedsWait {
					if err := inst.msgTable.wait(); err != nil {
						return err
					}
				} else if inst.msgTable.err != nil {
					return inst.msgTable.err
				}
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":`...)
				err := inst.msgTable.marshalTo(subMsgPtr, b, opts)
				if err != nil {
					return err
				}
				wroteAny = true
			} else if opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":null`...)
				wroteAny = true
			}
		case TypeTimestamp:
			subMsgPtr := *(*unsafe.Pointer)(fieldPtr)
			if subMsgPtr != nil {
				secs := *(*int64)(unsafe.Add(subMsgPtr, inst.secondsOffset))
				nanos := *(*int32)(unsafe.Add(subMsgPtr, inst.nanosOffset))
				if err := validateTimestamp(secs, nanos); err != nil {
					return err
				}
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":`...)
				b.writeEscapedString(formatTimestamp(secs, nanos))
				wroteAny = true
			} else if opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":null`...)
				wroteAny = true
			}
		case TypeDuration:
			subMsgPtr := *(*unsafe.Pointer)(fieldPtr)
			if subMsgPtr != nil {
				secs := *(*int64)(unsafe.Add(subMsgPtr, inst.secondsOffset))
				nanos := *(*int32)(unsafe.Add(subMsgPtr, inst.nanosOffset))
				if err := validateDuration(secs, nanos); err != nil {
					return err
				}
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":`...)

				b.writeDurationString(secs, nanos)
				wroteAny = true
			} else if opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":null`...)
				wroteAny = true
			}
		case TypeDoubleValue:
			subMsgPtr := *(*unsafe.Pointer)(fieldPtr)
			if subMsgPtr != nil {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":`...)
				val := *(*float64)(unsafe.Add(subMsgPtr, inst.valueOffset))
				b.writeFloat64(val, 64)
				wroteAny = true
			} else if opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":null`...)
				wroteAny = true
			}
		case TypeFloatValue:
			subMsgPtr := *(*unsafe.Pointer)(fieldPtr)
			if subMsgPtr != nil {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":`...)
				val := *(*float32)(unsafe.Add(subMsgPtr, inst.valueOffset))
				b.writeFloat64(float64(val), 32)
				wroteAny = true
			} else if opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":null`...)
				wroteAny = true
			}
		case TypeInt64Value:
			subMsgPtr := *(*unsafe.Pointer)(fieldPtr)
			if subMsgPtr != nil {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":`...)
				val := *(*int64)(unsafe.Add(subMsgPtr, inst.valueOffset))
				b.writeInt64String(val)
				wroteAny = true
			} else if opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":null`...)
				wroteAny = true
			}
		case TypeUint64Value:
			subMsgPtr := *(*unsafe.Pointer)(fieldPtr)
			if subMsgPtr != nil {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":`...)
				val := *(*uint64)(unsafe.Add(subMsgPtr, inst.valueOffset))
				b.writeUint64String(val)
				wroteAny = true
			} else if opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":null`...)
				wroteAny = true
			}
		case TypeInt32Value:
			subMsgPtr := *(*unsafe.Pointer)(fieldPtr)
			if subMsgPtr != nil {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":`...)
				val := *(*int32)(unsafe.Add(subMsgPtr, inst.valueOffset))
				b.writeInt64(int64(val))
				wroteAny = true
			} else if opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":null`...)
				wroteAny = true
			}
		case TypeUint32Value:
			subMsgPtr := *(*unsafe.Pointer)(fieldPtr)
			if subMsgPtr != nil {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":`...)
				val := *(*uint32)(unsafe.Add(subMsgPtr, inst.valueOffset))
				b.writeUint64(uint64(val))
				wroteAny = true
			} else if opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":null`...)
				wroteAny = true
			}
		case TypeBoolValue:
			subMsgPtr := *(*unsafe.Pointer)(fieldPtr)
			if subMsgPtr != nil {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":`...)
				val := *(*bool)(unsafe.Add(subMsgPtr, inst.valueOffset))
				b.writeBool(val)
				wroteAny = true
			} else if opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":null`...)
				wroteAny = true
			}
		case TypeStringValue:
			subMsgPtr := *(*unsafe.Pointer)(fieldPtr)
			if subMsgPtr != nil {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":`...)
				val := *(*string)(unsafe.Add(subMsgPtr, inst.valueOffset))
				b.writeEscapedString(val)
				wroteAny = true
			} else if opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":null`...)
				wroteAny = true
			}
		case TypeBytesValue:
			subMsgPtr := *(*unsafe.Pointer)(fieldPtr)
			if subMsgPtr != nil {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":`...)
				val := *(*[]byte)(unsafe.Add(subMsgPtr, inst.valueOffset))
				b.writeEscapedString(base64.StdEncoding.EncodeToString(val))
				wroteAny = true
			} else if opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":null`...)
				wroteAny = true
			}
		case TypeEmpty:
			subMsgPtr := *(*unsafe.Pointer)(fieldPtr)
			if subMsgPtr != nil {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":{}`...)
				wroteAny = true
			} else if opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":null`...)
				wroteAny = true
			}
		case TypeFieldMask, TypeStruct, TypeValue, TypeListValue, TypeAny:
			subMsgPtr := *(*unsafe.Pointer)(fieldPtr)
			if subMsgPtr != nil {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":`...)
				msg := reflect.NewAt(inst.elemType, subMsgPtr).Interface().(proto.Message)
				if err := marshalCustomWellKnown(msg, b, opts); err != nil {
					return err
				}
				wroteAny = true
			} else if opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":null`...)
				wroteAny = true
			}
		case TypeOneofField:
			if pref == nil {
				pref = reflect.NewAt(table.goType, ptr).Interface().(proto.Message).ProtoReflect()
			}
			if pref.Has(inst.fd) {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":`...)
				val := pref.Get(inst.fd)
				if err := marshalProtoreflectValue(val, inst.fd, b, opts); err != nil {
					return err
				}
				wroteAny = true
			}
		case TypeMapField:
			if pref == nil {
				pref = reflect.NewAt(table.goType, ptr).Interface().(proto.Message).ProtoReflect()
			}
			m := pref.Get(inst.fd).Map()
			if m.Len() > 0 || opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":`...)
				if m.Len() == 0 {
					b.buf = append(b.buf, "{}"...)
				} else {
					if err := marshalMap(pref, inst, b, opts); err != nil {
						return err
					}
				}
				wroteAny = true
			}
		case TypeRepeatedMessage:
			slice := *(*[]unsafe.Pointer)(fieldPtr)
			if len(slice) > 0 || opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":[`...)
				for j, itemPtr := range slice {
					if j > 0 {
						b.writeByte(',')
					}
					if itemPtr != nil {
						var err error
						if isCustomWellKnown(inst.msgTable.fullName) {
							msg := reflect.NewAt(inst.elemType, itemPtr).Interface().(proto.Message)
							err = marshalCustomWellKnown(msg, b, opts)
						} else {
							if inst.msgNeedsWait {
								if err = inst.msgTable.wait(); err != nil {
									return err
								}
							} else if inst.msgTable.err != nil {
								return inst.msgTable.err
							}
							err = inst.msgTable.marshalTo(itemPtr, b, opts)
						}
						if err != nil {
							return err
						}
					} else {
						b.buf = append(b.buf, "null"...)
					}
				}
				b.writeByte(']')
				wroteAny = true
			}
		}
	}
	// Extensions
	if table.hasExtensionRanges {
		var err error
		wroteAny, err = table.marshalExtensions(ptr, pref, b, opts, wroteAny)
		if err != nil {
			return err
		}
	}
	b.writeByte('}')
	return nil
}

func (table *MessageTable) marshalExtensions(ptr unsafe.Pointer, pref protoreflect.Message, b *encBuffer, opts MarshalOptions, wroteAny bool) (bool, error) {
	if pref == nil {
		pref = reflect.NewAt(table.goType, ptr).Interface().(proto.Message).ProtoReflect()
	}
	var extErr error
	pref.Range(func(fd protoreflect.FieldDescriptor, val protoreflect.Value) bool {
		if !fd.IsExtension() {
			return true
		}
		if wroteAny {
			b.writeByte(',')
		}
		b.buf = append(b.buf, `"[`...)
		b.buf = append(b.buf, string(fd.FullName())...)
		b.buf = append(b.buf, `]":`...)
		if fd.IsList() {
			b.writeByte('[')
			list := val.List()
			for j := 0; j < list.Len(); j++ {
				if j > 0 {
					b.writeByte(',')
				}
				if err := marshalProtoreflectValue(list.Get(j), fd, b, opts); err != nil {
					extErr = err
					return false
				}
			}
			b.writeByte(']')
		} else {
			if err := marshalProtoreflectValue(val, fd, b, opts); err != nil {
				extErr = err
				return false
			}
		}
		wroteAny = true
		return true
	})
	return wroteAny, extErr
}

func isCustomWellKnown(fullName protoreflect.FullName) bool {
	switch fullName {
	case "google.protobuf.Any",
		"google.protobuf.Empty",
		"google.protobuf.Struct",
		"google.protobuf.Value",
		"google.protobuf.ListValue",
		"google.protobuf.FieldMask",
		"google.protobuf.Timestamp",
		"google.protobuf.Duration",
		"google.protobuf.DoubleValue",
		"google.protobuf.FloatValue",
		"google.protobuf.Int64Value",
		"google.protobuf.UInt64Value",
		"google.protobuf.Int32Value",
		"google.protobuf.UInt32Value",
		"google.protobuf.BoolValue",
		"google.protobuf.StringValue",
		"google.protobuf.BytesValue":
		return true
	default:
		return false
	}
}

func marshalCustomWellKnown(msg proto.Message, b *encBuffer, opts MarshalOptions) error {
	pref := msg.ProtoReflect()
	fullName := pref.Descriptor().FullName()

	switch fullName {
	case "google.protobuf.Empty":
		b.buf = append(b.buf, "{}"...)
		return nil
	case "google.protobuf.Timestamp":
		fdSec := pref.Descriptor().Fields().ByNumber(1)
		fdNano := pref.Descriptor().Fields().ByNumber(2)
		secs := pref.Get(fdSec).Int()
		nanos := pref.Get(fdNano).Int()
		if err := validateTimestamp(secs, int32(nanos)); err != nil {
			return err
		}
		b.writeEscapedString(formatTimestamp(secs, int32(nanos)))
		return nil
	case "google.protobuf.Duration":
		fdSec := pref.Descriptor().Fields().ByNumber(1)
		fdNano := pref.Descriptor().Fields().ByNumber(2)
		secs := pref.Get(fdSec).Int()
		nanos := pref.Get(fdNano).Int()
		if err := validateDuration(secs, int32(nanos)); err != nil {
			return err
		}
		var s string
		if secs == 0 && nanos < 0 {
			s = fmt.Sprintf("-0.%09ds", -nanos)
		} else if secs < 0 {
			s = fmt.Sprintf("-%d.%09ds", -secs, -nanos)
		} else {
			s = fmt.Sprintf("%d.%09ds", secs, nanos)
		}
		idx := strings.IndexByte(s, '.')
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
		b.writeEscapedString(s)
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
		val := pref.Get(fd)
		switch fullName {
		case "google.protobuf.DoubleValue":
			b.writeFloat64(val.Float(), 64)
		case "google.protobuf.FloatValue":
			b.writeFloat64(val.Float(), 32)
		case "google.protobuf.Int64Value":
			b.writeInt64String(val.Int())
		case "google.protobuf.UInt64Value":
			b.writeUint64String(val.Uint())
		case "google.protobuf.Int32Value":
			b.writeInt64(val.Int())
		case "google.protobuf.UInt32Value":
			b.writeUint64(val.Uint())
		case "google.protobuf.BoolValue":
			b.writeBool(val.Bool())
		case "google.protobuf.StringValue":
			b.writeEscapedString(val.String())
		case "google.protobuf.BytesValue":
			b.writeEscapedString(base64.StdEncoding.EncodeToString(val.Bytes()))
		}
		return nil
	case "google.protobuf.FieldMask":
		return marshalFieldMask(pref, b)
	case "google.protobuf.Struct":
		return writeStruct(pref, b, opts)
	case "google.protobuf.Value":
		return writeValue(pref, b, opts)
	case "google.protobuf.ListValue":
		return writeListValue(pref, b, opts)
	case "google.protobuf.Any":
		return marshalAny(pref, b, opts)
	}
	return fmt.Errorf("unknown custom well-known type: %s", fullName)
}

func marshalFieldMask(pref protoreflect.Message, b *encBuffer) error {
	fd := pref.Descriptor().Fields().ByNumber(1)
	list := pref.Get(fd).List()
	paths := make([]string, 0, list.Len())
	for i := 0; i < list.Len(); i++ {
		s := list.Get(i).String()
		if !protoreflect.FullName(s).IsValid() {
			return fmt.Errorf("paths contains invalid path: %q", s)
		}
		cc := jsonCamelCase(s)
		if s != jsonSnakeCase(cc) {
			return fmt.Errorf("paths contains irreversible value: %q", s)
		}
		paths = append(paths, cc)
	}
	b.writeEscapedString(strings.Join(paths, ","))
	return nil
}

func writeStruct(pref protoreflect.Message, b *encBuffer, opts MarshalOptions) error {
	fd := pref.Descriptor().Fields().ByNumber(1)
	m := pref.Get(fd).Map()
	var arr [16]string
	var keys []string
	if m.Len() <= 16 {
		keys = arr[:0]
	} else {
		keys = make([]string, 0, m.Len())
	}
	m.Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
		keys = append(keys, k.String())
		return true
	})
	slices.Sort(keys)

	b.writeByte('{')
	for i, k := range keys {
		if i > 0 {
			b.writeByte(',')
		}
		b.writeEscapedString(k)
		b.writeByte(':')
		val := m.Get(protoreflect.ValueOfString(k).MapKey())
		if err := writeValue(val.Message(), b, opts); err != nil {
			return err
		}
	}
	b.writeByte('}')
	return nil
}

func writeValue(pref protoreflect.Message, b *encBuffer, opts MarshalOptions) error {
	od := pref.Descriptor().Oneofs().ByName("kind")
	if od == nil {
		return fmt.Errorf("google.protobuf.Value is missing 'kind' oneof")
	}
	fd := pref.WhichOneof(od)
	if fd == nil {
		return fmt.Errorf("google.protobuf.Value: none of the oneof fields is set")
	}
	val := pref.Get(fd)
	switch fd.Number() {
	case 1: // null_value
		b.buf = append(b.buf, "null"...)
	case 2: // number_value
		f := val.Float()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return fmt.Errorf("google.protobuf.Value: invalid number_value %v", f)
		}
		b.writeFloat64(f, 64)
	case 3: // string_value
		b.writeEscapedString(val.String())
	case 4: // bool_value
		b.writeBool(val.Bool())
	case 5: // struct_value
		return writeStruct(val.Message(), b, opts)
	case 6: // list_value
		return writeListValue(val.Message(), b, opts)
	default:
		return fmt.Errorf("google.protobuf.Value: unknown field number %d", fd.Number())
	}
	return nil
}

func writeListValue(pref protoreflect.Message, b *encBuffer, opts MarshalOptions) error {
	fd := pref.Descriptor().Fields().ByNumber(1)
	list := pref.Get(fd).List()
	b.writeByte('[')
	for i := 0; i < list.Len(); i++ {
		if i > 0 {
			b.writeByte(',')
		}
		val := list.Get(i)
		if err := writeValue(val.Message(), b, opts); err != nil {
			return err
		}
	}
	b.writeByte(']')
	return nil
}

func marshalAny(pref protoreflect.Message, b *encBuffer, opts MarshalOptions) error {
	fdType := pref.Descriptor().Fields().ByNumber(1)
	fdValue := pref.Descriptor().Fields().ByNumber(2)

	if !pref.Has(fdType) {
		if !pref.Has(fdValue) {
			b.buf = append(b.buf, "{}"...)
			return nil
		}
		return errors.New("google.protobuf.Any: type_url is not set")
	}

	typeURL := pref.Get(fdType).String()
	valueBytes := pref.Get(fdValue).Bytes()

	mt, err := protoregistry.GlobalTypes.FindMessageByURL(typeURL)
	if err != nil {
		return fmt.Errorf("google.protobuf.Any: unable to resolve %q: %v", typeURL, err)
	}

	em := mt.New()
	err = proto.UnmarshalOptions{
		AllowPartial: true,
	}.Unmarshal(valueBytes, em.Interface())
	if err != nil {
		return fmt.Errorf("google.protobuf.Any: unable to unmarshal %q: %v", typeURL, err)
	}

	if isCustomWellKnown(mt.Descriptor().FullName()) {
		b.writeByte('{')
		b.buf = append(b.buf, `"@type":`...)
		b.writeEscapedString(typeURL)
		b.buf = append(b.buf, `,"value":`...)
		if err := marshalCustomWellKnown(em.Interface(), b, opts); err != nil {
			return err
		}
		b.writeByte('}')
		return nil
	}

	b.writeByte('{')
	b.buf = append(b.buf, `"@type":`...)
	b.writeEscapedString(typeURL)

	subTable, err := getTable(em.Interface())
	if err != nil {
		return err
	}
	subMsgPtr := unsafe.Pointer(reflect.ValueOf(em.Interface()).Pointer())

	tempBuf := encBufPool.Get().(*encBuffer)
	tempBuf.buf = tempBuf.buf[:0]
	err = subTable.marshalTo(subMsgPtr, tempBuf, opts)
	if err != nil {
		encBufPool.Put(tempBuf)
		return err
	}
	if len(tempBuf.buf) >= 2 && tempBuf.buf[0] == '{' && tempBuf.buf[len(tempBuf.buf)-1] == '}' {
		stripped := tempBuf.buf[1 : len(tempBuf.buf)-1]
		if len(stripped) > 0 {
			b.writeByte(',')
			b.buf = append(b.buf, stripped...)
		}
	}
	encBufPool.Put(tempBuf)
	b.writeByte('}')
	return nil
}

func marshalProtoreflectValue(val protoreflect.Value, fd protoreflect.FieldDescriptor, b *encBuffer, opts MarshalOptions) error {
	switch fd.Kind() {
	case protoreflect.StringKind:
		b.writeEscapedString(val.String())
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		b.writeInt64(val.Int())
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		b.writeInt64String(val.Int())
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		b.writeUint64(val.Uint())
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		b.writeUint64String(val.Uint())
	case protoreflect.FloatKind:
		b.writeFloat64(val.Float(), 32)
	case protoreflect.DoubleKind:
		b.writeFloat64(val.Float(), 64)
	case protoreflect.BoolKind:
		b.writeBool(val.Bool())
	case protoreflect.BytesKind:
		b.writeEscapedString(base64.StdEncoding.EncodeToString(val.Bytes()))
	case protoreflect.EnumKind:
		num := int32(val.Enum())
		enumDesc := fd.Enum()
		enumVal := enumDesc.Values().ByNumber(protoreflect.EnumNumber(num))
		if enumVal != nil {
			b.writeEscapedString(string(enumVal.Name()))
		} else {
			b.writeInt64(int64(num))
		}
	case protoreflect.MessageKind, protoreflect.GroupKind:
		msg := val.Message().Interface()
		fullName := fd.Message().FullName()
		if isCustomWellKnown(fullName) {
			return marshalCustomWellKnown(msg, b, opts)
		}
		subTable, err := getTable(msg)
		if err != nil {
			return err
		}
		subMsgPtr := unsafe.Pointer(reflect.ValueOf(msg).Pointer())
		return subTable.marshalTo(subMsgPtr, b, opts)
	default:
		return fmt.Errorf("unsupported oneof field kind: %v", fd.Kind())
	}
	return nil
}

func jsonCamelCase(s string) string {
	var b []byte
	var wasUnderscore bool
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '_' {
			if wasUnderscore && isASCIILower(c) {
				c -= 'a' - 'A'
			}
			b = append(b, c)
		}
		wasUnderscore = c == '_'
	}
	return string(b)
}

func jsonSnakeCase(s string) string {
	var b []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isASCIIUpper(c) {
			b = append(b, '_')
			c += 'a' - 'A'
		}
		b = append(b, c)
	}
	return string(b)
}

func isASCIILower(c byte) bool {
	return 'a' <= c && c <= 'z'
}

func isASCIIUpper(c byte) bool {
	return 'A' <= c && c <= 'Z'
}

type mapKeyVal struct {
	key protoreflect.MapKey
	val protoreflect.Value
}

func marshalMap(pref protoreflect.Message, inst *fieldInstruction, b *encBuffer, opts MarshalOptions) error {
	m := pref.Get(inst.fd).Map()
	var arr [16]mapKeyVal
	var keys []mapKeyVal
	if m.Len() <= 16 {
		keys = arr[:0]
	} else {
		keys = make([]mapKeyVal, 0, m.Len())
	}
	m.Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
		keys = append(keys, mapKeyVal{key: k, val: v})
		return true
	})

	slices.SortFunc(keys, func(a, b mapKeyVal) int {
		ki := a.key
		kj := b.key
		switch ki.Interface().(type) {
		case string:
			if ki.String() < kj.String() {
				return -1
			} else if ki.String() > kj.String() {
				return 1
			}
			return 0
		case bool:
			if !ki.Bool() && kj.Bool() {
				return -1
			} else if ki.Bool() && !kj.Bool() {
				return 1
			}
			return 0
		case int32, int64:
			if ki.Int() < kj.Int() {
				return -1
			} else if ki.Int() > kj.Int() {
				return 1
			}
			return 0
		case uint32, uint64:
			if ki.Uint() < kj.Uint() {
				return -1
			} else if ki.Uint() > kj.Uint() {
				return 1
			}
			return 0
		default:
			return 0
		}
	})

	b.writeByte('{')
	for idx, kv := range keys {
		if idx > 0 {
			b.writeByte(',')
		}
		switch k := kv.key.Interface().(type) {
		case string:
			b.writeEscapedString(k)
		case bool:
			if k {
				b.buf = append(b.buf, `"true"`...)
			} else {
				b.buf = append(b.buf, `"false"`...)
			}
		case int32, int64:
			b.buf = append(b.buf, '"')
			b.writeInt64(kv.key.Int())
			b.buf = append(b.buf, '"')
		case uint32, uint64:
			b.buf = append(b.buf, '"')
			b.writeUint64(kv.key.Uint())
			b.buf = append(b.buf, '"')
		}
		b.writeByte(':')

		if err := marshalProtoreflectValue(kv.val, inst.fd.MapValue(), b, opts); err != nil {
			return err
		}
	}
	b.writeByte('}')
	return nil
}

func formatTimestamp(secs int64, nanos int32) string {
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
