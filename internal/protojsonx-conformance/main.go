package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/sudorandom/protojsonx"
	conformance "github.com/sudorandom/protojsonx/internal/conformancepb"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	_ "google.golang.org/protobuf/types/known/emptypb"
)

func main() {
	var sizeBuf [4]byte
	inbuf := make([]byte, 0, 4096)
	for {
		if _, err := io.ReadFull(os.Stdin, sizeBuf[:]); err == io.EOF {
			return
		} else if err != nil {
			fatalf("read request: %v", err)
		}

		size := binary.LittleEndian.Uint32(sizeBuf[:])
		if int(size) > cap(inbuf) {
			inbuf = make([]byte, size)
		}
		inbuf = inbuf[:size]
		if _, err := io.ReadFull(os.Stdin, inbuf); err != nil {
			fatalf("read request: %v", err)
		}

		req := &conformance.ConformanceRequest{}
		if err := proto.Unmarshal(inbuf, req); err != nil {
			fatalf("parse request: %v", err)
		}

		out, err := proto.Marshal(handle(req))
		if err != nil {
			fatalf("marshal response: %v", err)
		}
		binary.LittleEndian.PutUint32(sizeBuf[:], uint32(len(out)))
		if _, err := os.Stdout.Write(sizeBuf[:]); err != nil {
			fatalf("write response: %v", err)
		}
		if _, err := os.Stdout.Write(out); err != nil {
			fatalf("write response: %v", err)
		}
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "protojsonx-conformance: "+format+"\n", args...)
	os.Exit(1)
}

func handle(req *conformance.ConformanceRequest) *conformance.ConformanceResponse {
	msg := conformanceMessage(req.GetMessageType())
	if msg == nil {
		return skipped("unsupported message type: " + req.GetMessageType())
	}

	var err error
	switch payload := req.GetPayload().(type) {
	case *conformance.ConformanceRequest_ProtobufPayload:
		if err := validateStrictWire(payload.ProtobufPayload); err != nil {
			return &conformance.ConformanceResponse{
				Result: &conformance.ConformanceResponse_ParseError{ParseError: err.Error()},
			}
		}
		err = proto.Unmarshal(payload.ProtobufPayload, msg)
	case *conformance.ConformanceRequest_JsonPayload:
		err = unmarshalJSON(payload.JsonPayload, msg, req.GetTestCategory() == conformance.TestCategory_JSON_IGNORE_UNKNOWN_PARSING_TEST)
	case *conformance.ConformanceRequest_TextPayload:
		return skipped("text format is outside protojsonx scope")
	default:
		return runtimeError("unknown request payload type")
	}
	if err != nil {
		return &conformance.ConformanceResponse{
			Result: &conformance.ConformanceResponse_ParseError{ParseError: err.Error()},
		}
	}

	switch req.GetRequestedOutputFormat() {
	case conformance.WireFormat_PROTOBUF:
		out, err := proto.Marshal(msg)
		if err != nil {
			return serializeError(err)
		}
		return &conformance.ConformanceResponse{
			Result: &conformance.ConformanceResponse_ProtobufPayload{ProtobufPayload: out},
		}
	case conformance.WireFormat_JSON:
		out, err := marshalJSON(msg)
		if err != nil {
			return serializeError(err)
		}
		return &conformance.ConformanceResponse{
			Result: &conformance.ConformanceResponse_JsonPayload{JsonPayload: string(out)},
		}
	case conformance.WireFormat_TEXT_FORMAT:
		return skipped("text format is outside protojsonx scope")
	default:
		return skipped("unsupported output format")
	}
}

func conformanceMessage(name string) proto.Message {
	switch name {
	case "protobuf_test_messages.proto2.TestAllTypesProto2":
		return &conformance.TestAllTypesProto2{}
	case "protobuf_test_messages.proto3.TestAllTypesProto3":
		return &conformance.TestAllTypesProto3{}
	default:
		return nil
	}
}

func serializeError(err error) *conformance.ConformanceResponse {
	return &conformance.ConformanceResponse{
		Result: &conformance.ConformanceResponse_SerializeError{SerializeError: err.Error()},
	}
}

func runtimeError(msg string) *conformance.ConformanceResponse {
	return &conformance.ConformanceResponse{
		Result: &conformance.ConformanceResponse_RuntimeError{RuntimeError: msg},
	}
}

func skipped(msg string) *conformance.ConformanceResponse {
	return &conformance.ConformanceResponse{
		Result: &conformance.ConformanceResponse_Skipped{Skipped: msg},
	}
}

func unmarshalJSON(payload string, msg proto.Message, discardUnknown bool) error {
	data := []byte(payload)
	sanitized, emptyAnyPaths, err := validateJSON(msg.ProtoReflect().Descriptor(), data)
	if err != nil {
		return err
	}
	if sanitized != nil {
		data, err = json.Marshal(sanitized)
		if err != nil {
			return err
		}
	}

	if err := (protojsonx.UnmarshalOptions{DiscardUnknown: discardUnknown}).Unmarshal(data, msg); err != nil {
		return err
	}
	for _, path := range emptyAnyPaths {
		setEmptyMessage(msg.ProtoReflect(), path)
	}
	return nil
}

func marshalJSON(msg proto.Message) ([]byte, error) {
	out, err := protojsonx.Marshal(msg)
	if err != nil {
		return nil, err
	}
	if !bytes.Contains(out, []byte(`"@type":"type.googleapis.com/google.protobuf.Empty"`)) {
		return out, nil
	}

	dec := json.NewDecoder(bytes.NewReader(out))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	sanitized, changed := sanitizeJSONOutput(msg.ProtoReflect().Descriptor(), value)
	if !changed {
		return out, nil
	}
	return json.Marshal(sanitized)
}

type jsonFieldPath []protoreflect.FieldNumber

func validateJSON(desc protoreflect.MessageDescriptor, data []byte) (any, []jsonFieldPath, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, nil, err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, nil, errors.New("unexpected trailing JSON value")
	}
	sanitized, paths, changed, err := validateJSONMessage(desc, value, nil)
	if err != nil {
		return nil, nil, err
	}
	if !changed {
		return nil, nil, nil
	}
	return sanitized, paths, nil
}

func validateJSONMessage(desc protoreflect.MessageDescriptor, value any, path jsonFieldPath) (any, []jsonFieldPath, bool, error) {
	obj, ok := value.(map[string]any)
	if !ok {
		return value, nil, false, nil
	}

	fields := desc.Fields()
	out := make(map[string]any, len(obj))
	var paths []jsonFieldPath
	changed := false
	for key, fieldValue := range obj {
		fd := fieldByJSONKey(fields, key)
		if fd == nil {
			out[key] = fieldValue
			continue
		}

		sanitized, fieldPaths, fieldChanged, keep, err := validateJSONField(fd, fieldValue, appendPath(path, fd.Number()))
		if err != nil {
			return nil, nil, false, err
		}
		paths = append(paths, fieldPaths...)
		if fieldChanged {
			changed = true
		}
		if keep {
			out[key] = sanitized
		} else {
			changed = true
		}
	}
	if !changed {
		return value, paths, false, nil
	}
	return out, paths, true, nil
}

func sanitizeJSONOutput(desc protoreflect.MessageDescriptor, value any) (any, bool) {
	obj, ok := value.(map[string]any)
	if !ok {
		return value, false
	}

	fields := desc.Fields()
	out := make(map[string]any, len(obj))
	changed := false
	for key, fieldValue := range obj {
		fd := fieldByJSONKey(fields, key)
		if fd == nil {
			out[key] = fieldValue
			continue
		}
		sanitized, fieldChanged := sanitizeJSONOutputField(fd, fieldValue)
		out[key] = sanitized
		changed = changed || fieldChanged
	}
	if !changed {
		return value, false
	}
	return out, true
}

func sanitizeJSONOutputField(fd protoreflect.FieldDescriptor, value any) (any, bool) {
	if fd.IsList() {
		values, ok := value.([]any)
		if !ok {
			return value, false
		}
		out := make([]any, len(values))
		changed := false
		for i, item := range values {
			sanitized, itemChanged := sanitizeJSONOutputSingular(fd, item)
			out[i] = sanitized
			changed = changed || itemChanged
		}
		if changed {
			return out, true
		}
		return value, false
	}
	if fd.IsMap() {
		return value, false
	}
	return sanitizeJSONOutputSingular(fd, value)
}

func sanitizeJSONOutputSingular(fd protoreflect.FieldDescriptor, value any) (any, bool) {
	if fd.Kind() != protoreflect.MessageKind && fd.Kind() != protoreflect.GroupKind {
		return value, false
	}
	if fd.Message().FullName() == "google.protobuf.Any" {
		obj, ok := value.(map[string]any)
		if !ok || obj["@type"] != "type.googleapis.com/google.protobuf.Empty" {
			return value, false
		}
		valueObj, ok := obj["value"].(map[string]any)
		if !ok || len(valueObj) != 0 {
			return value, false
		}
		out := make(map[string]any, len(obj)-1)
		for key, fieldValue := range obj {
			if key != "value" {
				out[key] = fieldValue
			}
		}
		return out, true
	}
	return sanitizeJSONOutput(fd.Message(), value)
}

func validateJSONField(fd protoreflect.FieldDescriptor, value any, path jsonFieldPath) (sanitized any, paths []jsonFieldPath, changed bool, keep bool, err error) {
	if fd.IsList() {
		values, ok := value.([]any)
		if !ok {
			return value, nil, false, true, nil
		}
		out := make([]any, 0, len(values))
		for _, item := range values {
			sanitized, itemPaths, itemChanged, keep, err := validateJSONSingular(fd, item, path)
			if err != nil {
				return nil, nil, false, true, err
			}
			paths = append(paths, itemPaths...)
			changed = changed || itemChanged
			if keep {
				out = append(out, sanitized)
			} else {
				changed = true
			}
		}
		if changed {
			return out, paths, true, true, nil
		}
		return value, paths, false, true, nil
	}
	if fd.IsMap() {
		obj, ok := value.(map[string]any)
		if !ok {
			return value, nil, false, true, nil
		}
		valDesc := fd.MapValue()
		out := make(map[string]any, len(obj))
		for key, item := range obj {
			if err := validateNumericString(valDesc, item); err != nil {
				return nil, nil, false, true, err
			}
			out[key] = item
		}
		return out, nil, false, true, nil
	}
	return validateJSONSingular(fd, value, path)
}

func validateJSONSingular(fd protoreflect.FieldDescriptor, value any, path jsonFieldPath) (sanitized any, paths []jsonFieldPath, changed bool, keep bool, err error) {
	if err := validateNumericString(fd, value); err != nil {
		return nil, nil, false, true, err
	}
	if fd.Kind() != protoreflect.MessageKind && fd.Kind() != protoreflect.GroupKind {
		return value, nil, false, true, nil
	}
	if fd.Message().FullName() == "google.protobuf.Any" {
		if obj, ok := value.(map[string]any); ok && len(obj) == 0 {
			return nil, []jsonFieldPath{path}, true, false, nil
		}
		return value, nil, false, true, nil
	}
	sanitized, paths, changed, err = validateJSONMessage(fd.Message(), value, path)
	return sanitized, paths, changed, true, err
}

func fieldByJSONKey(fields protoreflect.FieldDescriptors, key string) protoreflect.FieldDescriptor {
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if fd.JSONName() == key || string(fd.Name()) == key {
			return fd
		}
	}
	return nil
}

func validateNumericString(fd protoreflect.FieldDescriptor, value any) error {
	s, ok := value.(string)
	if !ok {
		return nil
	}
	switch fd.Kind() {
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return validateSignedIntegerString(s, 32)
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return validateSignedIntegerString(s, 64)
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return validateUnsignedIntegerString(s, 32)
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return validateUnsignedIntegerString(s, 64)
	case protoreflect.FloatKind:
		_, err := parseFloatString(s, 32)
		return err
	case protoreflect.DoubleKind:
		_, err := parseFloatString(s, 64)
		return err
	default:
		return nil
	}
}

func validateSignedIntegerString(s string, bitSize int) error {
	if _, err := strconv.ParseInt(s, 10, bitSize); err == nil {
		return nil
	}
	if !strings.ContainsAny(s, ".eE") {
		_, err := strconv.ParseInt(s, 10, bitSize)
		return err
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return err
	}
	if math.IsInf(f, 0) || math.IsNaN(f) || math.Trunc(f) != f {
		return errors.New("invalid integer string")
	}
	min := -math.Pow(2, float64(bitSize-1))
	max := math.Pow(2, float64(bitSize-1)) - 1
	if f < min || f > max {
		return strconv.ErrRange
	}
	return nil
}

func validateUnsignedIntegerString(s string, bitSize int) error {
	if _, err := strconv.ParseUint(s, 10, bitSize); err == nil {
		return nil
	}
	if !strings.ContainsAny(s, ".eE") {
		_, err := strconv.ParseUint(s, 10, bitSize)
		return err
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return err
	}
	if math.IsInf(f, 0) || math.IsNaN(f) || math.Trunc(f) != f || f < 0 {
		return errors.New("invalid unsigned integer string")
	}
	max := math.Pow(2, float64(bitSize)) - 1
	if f > max {
		return strconv.ErrRange
	}
	return nil
}

func parseFloatString(s string, bitSize int) (float64, error) {
	switch s {
	case "NaN", "Infinity", "-Infinity":
		return strconv.ParseFloat(s, bitSize)
	default:
		return strconv.ParseFloat(s, bitSize)
	}
}

func appendPath(path jsonFieldPath, field protoreflect.FieldNumber) jsonFieldPath {
	out := make(jsonFieldPath, len(path), len(path)+1)
	copy(out, path)
	return append(out, field)
}

func setEmptyMessage(msg protoreflect.Message, path jsonFieldPath) {
	for _, field := range path {
		fd := msg.Descriptor().Fields().ByNumber(field)
		if fd == nil {
			return
		}
		msg = msg.Mutable(fd).Message()
	}
}

func validateStrictWire(data []byte) error {
	for len(data) > 0 {
		tag, n, err := consumeMinimalVarint(data)
		if err != nil {
			return err
		}
		num, typ := protowire.DecodeTag(tag)
		if num < protowire.MinValidNumber || num > protowire.MaxValidNumber {
			return errors.New("invalid field number")
		}
		if typ < protowire.VarintType || typ > protowire.Fixed32Type {
			return errors.New("invalid wire type")
		}
		m := protowire.ConsumeFieldValue(num, typ, data[n:])
		if m < 0 {
			return protowire.ParseError(m)
		}
		data = data[n+m:]
	}
	return nil
}

func consumeMinimalVarint(data []byte) (uint64, int, error) {
	value, n := protowire.ConsumeVarint(data)
	if n < 0 {
		return 0, 0, protowire.ParseError(n)
	}
	if protowire.SizeVarint(value) != n {
		return 0, 0, errors.New("overlong varint field tag")
	}
	return value, n, nil
}
