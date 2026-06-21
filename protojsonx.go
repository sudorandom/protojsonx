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
	TypeRepeatedString
	TypeRepeatedMessage
	TypeMapStringString
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

	// Offset helpers for Timestamp/Duration
	secondsOffset uintptr
	nanosOffset   uintptr
}

type MessageTable struct {
	goType   reflect.Type
	fields   []fieldInstruction
	fieldMap map[string]*fieldInstruction
	ready    chan struct{}
	done     atomic.Bool
	err      error
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
	}
	table.err = err
	table.done.Store(true)
	close(table.ready)
	cacheMutex.Unlock()

	return table, err
}

// compileTable translates protobuf descriptors into fieldInstructions for the
// generated Go struct. Unsupported field shapes fail compilation explicitly so
// callers do not get lossy JSON.
func compileTable(msg proto.Message) (*MessageTable, error) {
	pref := msg.ProtoReflect()
	desc := pref.Descriptor()

	goType := reflect.TypeOf(msg)
	if goType.Kind() == reflect.Pointer {
		goType = goType.Elem()
	}

	table := &MessageTable{
		goType:   goType,
		fieldMap: make(map[string]*fieldInstruction),
		ready:    make(chan struct{}),
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
		sf, ok := fieldNumToStructField[fd.Number()]
		if !ok {
			return nil, fmt.Errorf("protojsonx: unsupported field %s: no compatible generated struct field", fd.FullName())
		}

		inst := fieldInstruction{
			offset:    sf.Offset,
			jsonName:  fd.JSONName(),
			protoName: string(fd.Name()),
			index:     len(table.fields),
		}

		if fd.IsMap() {
			keyKind := fd.MapKey().Kind()
			valKind := fd.MapValue().Kind()
			if keyKind == protoreflect.StringKind && valKind == protoreflect.StringKind {
				inst.ftype = TypeMapStringString
			} else {
				return nil, fmt.Errorf("protojsonx: unsupported map field %s with key kind %s and value kind %s", fd.FullName(), keyKind, valKind)
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
				return nil, fmt.Errorf("protojsonx: unsupported repeated field %s with kind %s", fd.FullName(), fd.Kind())
			}
		} else {
			switch fd.Kind() {
			case protoreflect.StringKind:
				inst.ftype = TypeString
			case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
				inst.ftype = TypeInt32
			case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
				inst.ftype = TypeInt64
			case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
				inst.ftype = TypeUint32
			case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
				inst.ftype = TypeUint64
			case protoreflect.FloatKind:
				inst.ftype = TypeFloat32
			case protoreflect.DoubleKind:
				inst.ftype = TypeFloat64
			case protoreflect.BoolKind:
				inst.ftype = TypeBool
			case protoreflect.BytesKind:
				inst.ftype = TypeBytes
			case protoreflect.EnumKind:
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
			case protoreflect.MessageKind:
				fullName := fd.Message().FullName()
				structType := sf.Type
				if structType.Kind() == reflect.Ptr {
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
				default:
					inst.ftype = TypeMessage
					inst.msgTable = compileTableForType(structType)
					inst.msgNeedsWait = !inst.msgTable.readyNow()
					if inst.msgTable.readyNow() && inst.msgTable.err != nil {
						return nil, inst.msgTable.err
					}
				}
			default:
				return nil, fmt.Errorf("protojsonx: unsupported field %s with kind %s", fd.FullName(), fd.Kind())
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
