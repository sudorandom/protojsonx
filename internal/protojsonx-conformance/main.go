package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/sudorandom/protojsonx"
	conformance "github.com/sudorandom/protojsonx/internal/conformancepb"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	_ "google.golang.org/protobuf/types/known/emptypb"
)

var conformanceMode = "runtime"

type generatedJSONMarshaler interface {
	MarshalProtoJSONX() ([]byte, error)
}

type generatedJSONUnmarshaler interface {
	UnmarshalProtoJSONX([]byte) error
}

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
	return unmarshalJSONData([]byte(payload), msg, discardUnknown)
}

func unmarshalJSONData(data []byte, msg proto.Message, discardUnknown bool) error {
	if conformanceMode == "plugin" && !discardUnknown {
		if generated, ok := msg.(generatedJSONUnmarshaler); ok {
			return generated.UnmarshalProtoJSONX(data)
		}
	}
	return (protojsonx.UnmarshalOptions{DiscardUnknown: discardUnknown}).Unmarshal(data, msg)
}

func marshalJSON(msg proto.Message) ([]byte, error) {
	return marshalJSONData(msg)
}

func marshalJSONData(msg proto.Message) ([]byte, error) {
	if conformanceMode == "plugin" {
		if generated, ok := msg.(generatedJSONMarshaler); ok {
			return generated.MarshalProtoJSONX()
		}
	}
	return protojsonx.Marshal(msg)
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
