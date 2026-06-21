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
	"sort"
	"strconv"
	"sync"
	"time"
	"unsafe"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
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
	if isProtojsonCustomWellKnown(msg.ProtoReflect().Descriptor().FullName()) {
		return protojson.MarshalOptions{
			EmitUnpopulated: o.EmitUnpopulated,
			UseProtoNames:   o.UseProtoNames,
		}.Marshal(msg)
	}

	table, err := getTable(msg)
	if err != nil {
		return nil, err
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

	for _, inst := range table.fields {
		fieldPtr := unsafe.Add(ptr, inst.offset)
		fieldName := inst.jsonName
		if opts.UseProtoNames {
			fieldName = inst.protoName
		}

		switch inst.ftype {
		case TypeString:
			val := *(*string)(fieldPtr)
			if val != "" || opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":`...)
				b.writeEscapedString(val)
				wroteAny = true
			}
		case TypeInt32:
			val := *(*int32)(fieldPtr)
			if val != 0 || opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":`...)
				b.writeInt64(int64(val))
				wroteAny = true
			}
		case TypeInt64:
			val := *(*int64)(fieldPtr)
			if val != 0 || opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":`...)
				b.writeInt64String(val)
				wroteAny = true
			}
		case TypeUint32:
			val := *(*uint32)(fieldPtr)
			if val != 0 || opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":`...)
				b.writeUint64(uint64(val))
				wroteAny = true
			}
		case TypeUint64:
			val := *(*uint64)(fieldPtr)
			if val != 0 || opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":`...)
				b.writeUint64String(val)
				wroteAny = true
			}
		case TypeFloat32:
			val := *(*float32)(fieldPtr)
			if val != 0 || opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":`...)
				b.writeFloat64(float64(val), 32)
				wroteAny = true
			}
		case TypeFloat64:
			val := *(*float64)(fieldPtr)
			if val != 0 || opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":`...)
				b.writeFloat64(val, 64)
				wroteAny = true
			}
		case TypeBool:
			val := *(*bool)(fieldPtr)
			if val || opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":`...)
				b.writeBool(val)
				wroteAny = true
			}
		case TypeBytes:
			val := *(*[]byte)(fieldPtr)
			if len(val) > 0 || opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":`...)
				b.writeEscapedString(base64.StdEncoding.EncodeToString(val))
				wroteAny = true
			}
		case TypeEnum:
			val := *(*int32)(fieldPtr)
			if val != 0 || opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":`...)
				enumStr, ok := inst.enumNameMap[val]
				if ok {
					b.writeEscapedString(enumStr)
				} else {
					b.writeInt64(int64(val))
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
		case TypeMapStringString:
			val := *(*map[string]string)(fieldPtr)
			if len(val) > 0 || opts.EmitUnpopulated {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":{`...)

				keysPtr := stringSlicePool.Get().(*[]string)
				keys := (*keysPtr)[:0]
				for k := range val {
					keys = append(keys, k)
				}
				sort.Strings(keys)

				for j, k := range keys {
					if j > 0 {
						b.writeByte(',')
					}
					b.writeEscapedString(k)
					b.writeByte(':')
					b.writeEscapedString(val[k])
				}
				*keysPtr = keys
				stringSlicePool.Put(keysPtr)

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
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":`...)
				t := time.Unix(secs, int64(nanos)).UTC()
				b.writeEscapedString(t.Format(time.RFC3339Nano))
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
		case TypeProtojsonWellKnown:
			subMsgPtr := *(*unsafe.Pointer)(fieldPtr)
			if subMsgPtr != nil {
				if wroteAny {
					b.writeByte(',')
				}
				b.buf = append(b.buf, '"')
				b.buf = append(b.buf, fieldName...)
				b.buf = append(b.buf, `":`...)
				msg := reflect.NewAt(inst.elemType, subMsgPtr).Interface().(proto.Message)
				data, err := protojson.MarshalOptions{
					EmitUnpopulated: opts.EmitUnpopulated,
					UseProtoNames:   opts.UseProtoNames,
				}.Marshal(msg)
				if err != nil {
					return err
				}
				b.buf = append(b.buf, data...)
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
						if inst.msgNeedsWait {
							if err := inst.msgTable.wait(); err != nil {
								return err
							}
						} else if inst.msgTable.err != nil {
							return inst.msgTable.err
						}
						err := inst.msgTable.marshalTo(itemPtr, b, opts)
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
	b.writeByte('}')
	return nil
}
