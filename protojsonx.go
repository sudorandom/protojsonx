package protojsonx

// Table compilation strategy:
//
// protojsonx pays reflection cost once per generated message type. A
// MessageTable maps protobuf descriptors to generated Go struct offsets by
// reading the generated `protobuf` tags, then records the minimal per-field
// metadata needed by marshal/unmarshal. The hot paths only use this table plus
// unsafe pointer arithmetic.
//
// The cache publishes placeholders while compiling so recursive message graphs
// can be represented. Each table has a readiness channel and error slot; public
// entry points wait for readiness before using a table, while compiled nested
// fields skip that wait unless they observed an unresolved placeholder.

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type FieldType int

const (
	TypeInvalid FieldType = iota
	TypeString
	TypeInt32
	TypeInt64
	TypeUint32
	TypeUint64
	TypeFloat32
	TypeFloat64
	TypeBool
	TypeBytes
	TypeEnum
	TypeMessage
	TypeTimestamp
	TypeDuration
	TypeProtojsonWellKnown
	TypeDoubleValue
	TypeFloatValue
	TypeInt64Value
	TypeUint64Value
	TypeInt32Value
	TypeUint32Value
	TypeBoolValue
	TypeStringValue
	TypeBytesValue
	TypeEmpty
	TypeFieldMask
	TypeStruct
	TypeValue
	TypeListValue
	TypeAny
	TypeOneofField
	TypeMapField
	TypeRepeatedString
	TypeRepeatedMessage
	TypeMapStringString
	TypeRepeatedInt32
	TypeRepeatedInt64
	TypeRepeatedUint32
	TypeRepeatedUint64
	TypeRepeatedFloat32
	TypeRepeatedFloat64
	TypeRepeatedBool
	TypeRepeatedBytes
	TypeRepeatedEnum
)

type fieldInstruction struct {
	offset    uintptr
	ftype     FieldType
	jsonName  string
	protoName string
	index     int

	// Enum helpers
	enumNameMap  map[int32]string
	enumValueMap map[string]int32

	// Message helper
	msgTable     *MessageTable
	msgNeedsWait bool
	// For repeated messages
	elemType reflect.Type

	// Offset helpers for Timestamp/Duration/Wrappers
	secondsOffset uintptr
	nanosOffset   uintptr
	valueOffset   uintptr

	// Oneof helper
	fd protoreflect.FieldDescriptor

	// Optional primitive helper
	isOptional bool
	goPointer  bool
}

type MessageTable struct {
	goType             reflect.Type
	fullName           protoreflect.FullName
	fields             []fieldInstruction
	fieldMap           map[string]*fieldInstruction
	useProtojson       bool
	hasExtensionRanges bool
	ready              chan struct{}
	done               atomic.Bool
	err                error
}

func (table *MessageTable) wait() error {
	if table.done.Load() {
		return table.err
	}
	<-table.ready
	return table.err
}

// readyNow lets table compilation detect whether a nested table is already
// complete. Completed nested tables can avoid a channel wait in the hot path.
func (table *MessageTable) readyNow() bool {
	select {
	case <-table.ready:
		return true
	default:
		return false
	}
}

var tableCache = make(map[reflect.Type]*MessageTable)
var cacheMutex sync.RWMutex

func isProtojsonCustomWellKnown(fullName protoreflect.FullName) bool {
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

func GetTable(msg proto.Message) *MessageTable {
	table, _ := getTable(msg)
	return table
}

// getTable returns the cached table for msg's concrete generated type. It is
// the error-returning internal form used by Marshal and Unmarshal.
func getTable(msg proto.Message) (*MessageTable, error) {
	t := reflect.TypeOf(msg)
	if t == nil {
		return nil, fmt.Errorf("protojsonx: nil message")
	}
	v := reflect.ValueOf(msg)
	if v.Kind() == reflect.Pointer && v.IsNil() {
		return nil, fmt.Errorf("protojsonx: nil message")
	}
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	cacheMutex.RLock()
	table, ok := tableCache[t]
	cacheMutex.RUnlock()
	if ok {
		return table, table.wait()
	}

	cacheMutex.Lock()
	table, ok = tableCache[t]
	if ok {
		cacheMutex.Unlock()
		return table, table.wait()
	}

	table = &MessageTable{
		goType:   t,
		fieldMap: make(map[string]*fieldInstruction),
		ready:    make(chan struct{}),
	}
	tableCache[t] = table
	cacheMutex.Unlock()

	fullTable, err := compileTable(msg)

	cacheMutex.Lock()
	if fullTable != nil {
		table.fields = fullTable.fields
		table.fieldMap = fullTable.fieldMap
		table.useProtojson = fullTable.useProtojson
		table.hasExtensionRanges = fullTable.hasExtensionRanges
		table.fullName = fullTable.fullName
	}
	table.err = err
	table.done.Store(true)
	close(table.ready)
	cacheMutex.Unlock()

	return table, err
}

// compileTable translates protobuf descriptors into fieldInstructions for the
// generated Go struct. Shapes not covered by the optimized table compiler fall
// back to standard protojson for full compatibility.
func compileTable(msg proto.Message) (*MessageTable, error) {
	pref := msg.ProtoReflect()
	desc := pref.Descriptor()

	goType := reflect.TypeOf(msg)
	if goType.Kind() == reflect.Pointer {
		goType = goType.Elem()
	}

	table := &MessageTable{
		goType:             goType,
		fieldMap:           make(map[string]*fieldInstruction),
		fullName:           desc.FullName(),
		hasExtensionRanges: desc.ExtensionRanges().Len() > 0,
		ready:              make(chan struct{}),
	}
	close(table.ready)

	fieldNumToStructField := make(map[protoreflect.FieldNumber]reflect.StructField)
	for i := 0; i < goType.NumField(); i++ {
		f := goType.Field(i)
		tag := f.Tag.Get("protobuf")
		if tag == "" {
			continue
		}
		parts := strings.Split(tag, ",")
		if len(parts) >= 2 {
			numVal, err := strconv.Atoi(parts[1])
			if err == nil {
				fieldNumToStructField[protoreflect.FieldNumber(numVal)] = f
			}
		}
	}

	fieldsDesc := desc.Fields()
	for i := 0; i < fieldsDesc.Len(); i++ {
		fd := fieldsDesc.Get(i)
		var inst fieldInstruction
		sf, ok := fieldNumToStructField[fd.Number()]
		if !ok {
			if fd.ContainingOneof() != nil {
				inst = fieldInstruction{
					ftype:     TypeOneofField,
					jsonName:  fd.JSONName(),
					protoName: string(fd.Name()),
					index:     len(table.fields),
					fd:        fd,
				}
				table.fields = append(table.fields, inst)
				continue
			}
			table.useProtojson = true
			return table, nil
		}

		structType := sf.Type
		isOptional := false
		goPointer := false
		if structType.Kind() == reflect.Pointer && fd.Kind() != protoreflect.MessageKind {
			structType = structType.Elem()
			isOptional = true
			goPointer = true
		} else if fd.Kind() == protoreflect.BytesKind {
			isOptional = fd.HasPresence()
		}

		inst = fieldInstruction{
			offset:     sf.Offset,
			jsonName:   fd.JSONName(),
			protoName:  string(fd.Name()),
			index:      len(table.fields),
			isOptional: isOptional,
			goPointer:  goPointer,
			elemType:   structType,
			fd:         fd,
		}

		if fd.IsMap() {
			keyKind := fd.MapKey().Kind()
			valKind := fd.MapValue().Kind()
			if keyKind == protoreflect.StringKind && valKind == protoreflect.StringKind {
				inst.ftype = TypeMapStringString
			} else {
				inst.ftype = TypeMapField
				inst.fd = fd
			}
		} else if fd.IsList() {
			if fd.Kind() == protoreflect.StringKind {
				inst.ftype = TypeRepeatedString
			} else if fd.Kind() == protoreflect.MessageKind {
				inst.ftype = TypeRepeatedMessage
				sliceType := sf.Type
				elemType := sliceType.Elem()
				if elemType.Kind() == reflect.Pointer {
					elemType = elemType.Elem()
				}
				inst.elemType = elemType
				inst.msgTable = compileTableForType(elemType)
				inst.msgNeedsWait = !inst.msgTable.readyNow()
				if inst.msgTable.readyNow() && inst.msgTable.err != nil {
					return nil, inst.msgTable.err
				}
			} else {
				if sf.Type.Kind() != reflect.Slice {
					table.useProtojson = true
					return table, nil
				}
				switch fd.Kind() {
				case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
					inst.ftype = TypeRepeatedInt32
				case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
					inst.ftype = TypeRepeatedInt64
				case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
					inst.ftype = TypeRepeatedUint32
				case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
					inst.ftype = TypeRepeatedUint64
				case protoreflect.FloatKind:
					inst.ftype = TypeRepeatedFloat32
				case protoreflect.DoubleKind:
					inst.ftype = TypeRepeatedFloat64
				case protoreflect.BoolKind:
					inst.ftype = TypeRepeatedBool
				case protoreflect.BytesKind:
					inst.ftype = TypeRepeatedBytes
				case protoreflect.EnumKind:
					inst.ftype = TypeRepeatedEnum
					inst.enumNameMap = make(map[int32]string)
					inst.enumValueMap = make(map[string]int32)
					enumDesc := fd.Enum()
					vals := enumDesc.Values()
					for j := 0; j < vals.Len(); j++ {
						v := vals.Get(j)
						name := string(v.Name())
						num := int32(v.Number())
						inst.enumNameMap[num] = name
						inst.enumValueMap[name] = num
					}
				default:
					table.useProtojson = true
					return table, nil
				}
			}
		} else {
			switch fd.Kind() {
			case protoreflect.StringKind:
				if structType.Kind() != reflect.String {
					table.useProtojson = true
					return table, nil
				}
				inst.ftype = TypeString
			case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
				if structType.Kind() != reflect.Int32 {
					table.useProtojson = true
					return table, nil
				}
				inst.ftype = TypeInt32
			case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
				if structType.Kind() != reflect.Int64 {
					table.useProtojson = true
					return table, nil
				}
				inst.ftype = TypeInt64
			case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
				if structType.Kind() != reflect.Uint32 {
					table.useProtojson = true
					return table, nil
				}
				inst.ftype = TypeUint32
			case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
				if structType.Kind() != reflect.Uint64 {
					table.useProtojson = true
					return table, nil
				}
				inst.ftype = TypeUint64
			case protoreflect.FloatKind:
				if structType.Kind() != reflect.Float32 {
					table.useProtojson = true
					return table, nil
				}
				inst.ftype = TypeFloat32
			case protoreflect.DoubleKind:
				if structType.Kind() != reflect.Float64 {
					table.useProtojson = true
					return table, nil
				}
				inst.ftype = TypeFloat64
			case protoreflect.BoolKind:
				if structType.Kind() != reflect.Bool {
					table.useProtojson = true
					return table, nil
				}
				inst.ftype = TypeBool
			case protoreflect.BytesKind:
				if structType.Kind() != reflect.Slice {
					table.useProtojson = true
					return table, nil
				}
				inst.ftype = TypeBytes
			case protoreflect.EnumKind:
				if structType.Kind() != reflect.Int32 {
					table.useProtojson = true
					return table, nil
				}
				inst.ftype = TypeEnum
				inst.enumNameMap = make(map[int32]string)
				inst.enumValueMap = make(map[string]int32)
				enumDesc := fd.Enum()
				vals := enumDesc.Values()
				for j := 0; j < vals.Len(); j++ {
					v := vals.Get(j)
					name := string(v.Name())
					num := int32(v.Number())
					inst.enumNameMap[num] = name
					inst.enumValueMap[name] = num
				}
			case protoreflect.MessageKind, protoreflect.GroupKind:
				fullName := fd.Message().FullName()
				structType := sf.Type
				if structType.Kind() == reflect.Pointer {
					structType = structType.Elem()
				}
				inst.elemType = structType
				switch fullName {
				case "google.protobuf.Timestamp":
					inst.ftype = TypeTimestamp
					fSec, okSec := structType.FieldByName("Seconds")
					fNano, okNano := structType.FieldByName("Nanos")
					if okSec && okNano {
						inst.secondsOffset = fSec.Offset
						inst.nanosOffset = fNano.Offset
					}
				case "google.protobuf.Duration":
					inst.ftype = TypeDuration
					fSec, okSec := structType.FieldByName("Seconds")
					fNano, okNano := structType.FieldByName("Nanos")
					if okSec && okNano {
						inst.secondsOffset = fSec.Offset
						inst.nanosOffset = fNano.Offset
					}
				case "google.protobuf.DoubleValue":
					inst.ftype = TypeDoubleValue
					if fVal, okVal := structType.FieldByName("Value"); okVal {
						inst.valueOffset = fVal.Offset
					}
				case "google.protobuf.FloatValue":
					inst.ftype = TypeFloatValue
					if fVal, okVal := structType.FieldByName("Value"); okVal {
						inst.valueOffset = fVal.Offset
					}
				case "google.protobuf.Int64Value":
					inst.ftype = TypeInt64Value
					if fVal, okVal := structType.FieldByName("Value"); okVal {
						inst.valueOffset = fVal.Offset
					}
				case "google.protobuf.UInt64Value":
					inst.ftype = TypeUint64Value
					if fVal, okVal := structType.FieldByName("Value"); okVal {
						inst.valueOffset = fVal.Offset
					}
				case "google.protobuf.Int32Value":
					inst.ftype = TypeInt32Value
					if fVal, okVal := structType.FieldByName("Value"); okVal {
						inst.valueOffset = fVal.Offset
					}
				case "google.protobuf.UInt32Value":
					inst.ftype = TypeUint32Value
					if fVal, okVal := structType.FieldByName("Value"); okVal {
						inst.valueOffset = fVal.Offset
					}
				case "google.protobuf.BoolValue":
					inst.ftype = TypeBoolValue
					if fVal, okVal := structType.FieldByName("Value"); okVal {
						inst.valueOffset = fVal.Offset
					}
				case "google.protobuf.StringValue":
					inst.ftype = TypeStringValue
					if fVal, okVal := structType.FieldByName("Value"); okVal {
						inst.valueOffset = fVal.Offset
					}
				case "google.protobuf.BytesValue":
					inst.ftype = TypeBytesValue
					if fVal, okVal := structType.FieldByName("Value"); okVal {
						inst.valueOffset = fVal.Offset
					}
				case "google.protobuf.Empty":
					inst.ftype = TypeEmpty
				case "google.protobuf.FieldMask":
					inst.ftype = TypeFieldMask
				case "google.protobuf.Struct":
					inst.ftype = TypeStruct
				case "google.protobuf.Value":
					inst.ftype = TypeValue
				case "google.protobuf.ListValue":
					inst.ftype = TypeListValue
				case "google.protobuf.Any":
					inst.ftype = TypeAny
				default:
					if isProtojsonCustomWellKnown(fullName) {
						inst.ftype = TypeProtojsonWellKnown
						break
					}
					inst.ftype = TypeMessage
					inst.msgTable = compileTableForType(structType)
					inst.msgNeedsWait = !inst.msgTable.readyNow()
					if inst.msgTable.readyNow() && inst.msgTable.err != nil {
						return nil, inst.msgTable.err
					}
				}
			default:
				table.useProtojson = true
				return table, nil
			}
		}

		table.fields = append(table.fields, inst)
	}

	for i := range table.fields {
		inst := &table.fields[i]
		table.fieldMap[inst.jsonName] = inst
		table.fieldMap[inst.protoName] = inst
	}

	return table, nil
}

// compileTableForType compiles a nested message type and stores a placeholder
// before descending, which lets recursive message references terminate.
func compileTableForType(structType reflect.Type) *MessageTable {
	cacheMutex.Lock()
	if table, ok := tableCache[structType]; ok {
		cacheMutex.Unlock()
		return table
	}

	table := &MessageTable{
		goType:   structType,
		fieldMap: make(map[string]*fieldInstruction),
		ready:    make(chan struct{}),
	}
	tableCache[structType] = table
	cacheMutex.Unlock()

	zeroPtr := reflect.New(structType).Interface().(proto.Message)
	fullTable, err := compileTable(zeroPtr)

	cacheMutex.Lock()
	if fullTable != nil {
		table.fields = fullTable.fields
		table.useProtojson = fullTable.useProtojson
		table.hasExtensionRanges = fullTable.hasExtensionRanges
		table.fullName = fullTable.fullName
		for i := range table.fields {
			inst := &table.fields[i]
			table.fieldMap[inst.jsonName] = inst
			table.fieldMap[inst.protoName] = inst
		}
	}
	table.err = err
	table.done.Store(true)
	close(table.ready)
	cacheMutex.Unlock()

	return table
}

func validateTimestamp(secs int64, nanos int32) error {
	if secs < -62135596800 || secs > 253402300799 {
		return fmt.Errorf("timestamp: seconds out of range %d", secs)
	}
	if nanos < 0 || nanos > 999999999 {
		return fmt.Errorf("timestamp: nanos out of range %d", nanos)
	}
	return nil
}

func validateDuration(secs int64, nanos int32) error {
	if secs < -315576000000 || secs > 315576000000 {
		return fmt.Errorf("duration: seconds out of range %d", secs)
	}
	if nanos < -999999999 || nanos > 999999999 {
		return fmt.Errorf("duration: nanos out of range %d", nanos)
	}
	if (secs < 0 && nanos > 0) || (secs > 0 && nanos < 0) {
		return fmt.Errorf("duration: seconds and nanos must have the same sign")
	}
	return nil
}
