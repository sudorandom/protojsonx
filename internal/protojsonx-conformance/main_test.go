package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	conformance "github.com/sudorandom/protojsonx/internal/conformancepb"
	"google.golang.org/protobuf/proto"
)

func TestHandleRejectsPartiallyNumericJSONStrings(t *testing.T) {
	tests := []struct {
		name    string
		message string
		json    string
	}{
		{
			name:    "Proto2Int32Comma",
			message: "protobuf_test_messages.proto2.TestAllTypesProto2",
			json:    `{"optionalInt32":"123,456"}`,
		},
		{
			name:    "Proto2Int32Space",
			message: "protobuf_test_messages.proto2.TestAllTypesProto2",
			json:    `{"optionalInt32":"123 456"}`,
		},
		{
			name:    "Proto2Int32UnicodeSpace",
			message: "protobuf_test_messages.proto2.TestAllTypesProto2",
			json:    `{"optionalInt32":"123\u00a0456"}`,
		},
		{
			name:    "Proto3FloatComma",
			message: "protobuf_test_messages.proto3.TestAllTypesProto3",
			json:    `{"optionalFloat":"1.5,2"}`,
		},
		{
			name:    "Proto3FloatSpace",
			message: "protobuf_test_messages.proto3.TestAllTypesProto3",
			json:    `{"optionalFloat":"1.5 2"}`,
		},
		{
			name:    "Proto3FloatUnicodeSpace",
			message: "protobuf_test_messages.proto3.TestAllTypesProto3",
			json:    `{"optionalFloat":"1.5\u00a02"}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := handle(&conformance.ConformanceRequest{
				MessageType: tc.message,
				Payload: &conformance.ConformanceRequest_JsonPayload{
					JsonPayload: tc.json,
				},
				RequestedOutputFormat: conformance.WireFormat_JSON,
			})
			_, ok := res.GetResult().(*conformance.ConformanceResponse_ParseError)
			assert.True(t, ok, "got %T", res.GetResult())
		})
	}
}

func TestHandleAcceptsQuotedIntegerExponentJSON(t *testing.T) {
	for _, message := range []string{
		"protobuf_test_messages.proto2.TestAllTypesProto2",
		"protobuf_test_messages.proto3.TestAllTypesProto3",
	} {
		t.Run(message, func(t *testing.T) {
			res := handle(&conformance.ConformanceRequest{
				MessageType: message,
				Payload: &conformance.ConformanceRequest_JsonPayload{
					JsonPayload: `{"optionalInt32":"1e5"}`,
				},
				RequestedOutputFormat: conformance.WireFormat_PROTOBUF,
			})
			_, ok := res.GetResult().(*conformance.ConformanceResponse_ProtobufPayload)
			assert.True(t, ok, "got %T", res.GetResult())
		})
	}
}

func TestHandleAllowsEmptyAnyJSON(t *testing.T) {
	req := &conformance.ConformanceRequest{
		MessageType: "protobuf_test_messages.proto3.TestAllTypesProto3",
		Payload: &conformance.ConformanceRequest_JsonPayload{
			JsonPayload: `{"optionalAny":{"@type":"type.googleapis.com/google.protobuf.Empty"}}`,
		},
		RequestedOutputFormat: conformance.WireFormat_PROTOBUF,
	}

	res := handle(req)
	payload, ok := res.GetResult().(*conformance.ConformanceResponse_ProtobufPayload)
	require.True(t, ok, "got %T", res.GetResult())

	var msg conformance.TestAllTypesProto3
	require.NoError(t, proto.Unmarshal(payload.ProtobufPayload, &msg))
	require.NotNil(t, msg.OptionalAny)
	assert.Equal(t, "type.googleapis.com/google.protobuf.Empty", msg.OptionalAny.TypeUrl)
	assert.Empty(t, msg.OptionalAny.Value)
}

func TestHandleEmitsConformanceEmptyAnyJSON(t *testing.T) {
	res := handle(&conformance.ConformanceRequest{
		MessageType: "protobuf_test_messages.proto3.TestAllTypesProto3",
		Payload: &conformance.ConformanceRequest_JsonPayload{
			JsonPayload: `{"optionalAny":{"@type":"type.googleapis.com/google.protobuf.Empty"}}`,
		},
		RequestedOutputFormat: conformance.WireFormat_JSON,
	})
	payload, ok := res.GetResult().(*conformance.ConformanceResponse_JsonPayload)
	require.True(t, ok, "got %T", res.GetResult())

	var out map[string]map[string]any
	require.NoError(t, json.Unmarshal([]byte(payload.JsonPayload), &out))
	assert.Equal(t, "type.googleapis.com/google.protobuf.Empty", out["optionalAny"]["@type"])
	assert.NotContains(t, out["optionalAny"], "value")
}

func TestHandleRejectsOverlongWireTag(t *testing.T) {
	res := handle(&conformance.ConformanceRequest{
		MessageType: "protobuf_test_messages.proto3.TestAllTypesProto3",
		Payload: &conformance.ConformanceRequest_ProtobufPayload{
			ProtobufPayload: []byte{0x88, 0x00, 0x00},
		},
		RequestedOutputFormat: conformance.WireFormat_JSON,
	})

	_, ok := res.GetResult().(*conformance.ConformanceResponse_ParseError)
	assert.True(t, ok, "got %T", res.GetResult())
}

func TestHandleSkipsTextFormat(t *testing.T) {
	res := handle(&conformance.ConformanceRequest{
		MessageType: "protobuf_test_messages.proto3.TestAllTypesProto3",
		Payload: &conformance.ConformanceRequest_TextPayload{
			TextPayload: `optional_int32: 1`,
		},
		RequestedOutputFormat: conformance.WireFormat_JSON,
	})

	_, ok := res.GetResult().(*conformance.ConformanceResponse_Skipped)
	assert.True(t, ok, "got %T", res.GetResult())
}
