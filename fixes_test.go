package protojsonx

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/sudorandom/protojsonx/internal/conformancepb"
	"github.com/sudorandom/protojsonx/internal/testpb"
	"github.com/sudorandom/protojsonx/protojsonxgen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

// TestOptionalFieldClearSafety verifies that clearField and resetIfNeeded correctly
// zero pointer fields without clobbering adjacent memory.
func TestOptionalFieldClearSafety(t *testing.T) {
	// Re-unmarshaling onto a populated struct triggers clearField / resetIfNeeded.
	msg := &testpb.CompatibilityMessage{
		OptionalString: proto.String("original-string"),
		OptionalInt32:  proto.Int32(9999),
	}

	// Unmarshal JSON with empty object via runtime engine (DisableFastPath)
	err := (UnmarshalOptions{DisableFastPath: true}).Unmarshal([]byte("{}"), msg)
	require.NoError(t, err)
	assert.Nil(t, msg.OptionalString)
	assert.Nil(t, msg.OptionalInt32)

	// Now unmarshal only OptionalInt32; OptionalString must remain nil and not corrupted
	err = (UnmarshalOptions{DisableFastPath: true}).Unmarshal([]byte(`{"optionalInt32": 42}`), msg)
	require.NoError(t, err)
	assert.Nil(t, msg.OptionalString)
	require.NotNil(t, msg.OptionalInt32)
	assert.Equal(t, int32(42), *msg.OptionalInt32)
}

// TestEmitUnpopulatedBytes verifies that unpopulated non-optional bytes fields
// serialize as `""` (empty string) instead of `null`.
func TestEmitUnpopulatedBytes(t *testing.T) {
	msg := &conformance.TestAllTypesProto3{}
	opts := MarshalOptions{
		EmitUnpopulated: true,
		DisableFastPath: true,
	}
	data, err := opts.Marshal(msg)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"optionalBytes":""`)
	assert.NotContains(t, string(data), `"optionalBytes":null`)
}

// TestMapNullMessageValueRejected verifies that null message map values are rejected
// in both runtime and generated unmarshaler.
func TestMapNullMessageValueRejected(t *testing.T) {
	jsonInput := []byte(`{"mapStringMessage": {"child": null}}`)

	// 1. Runtime unmarshaler
	var runtimeMsg testpb.CompatibilityMessage
	err := (UnmarshalOptions{DisableFastPath: true}).Unmarshal(jsonInput, &runtimeMsg)
	require.Error(t, err, "runtime unmarshaler must reject null message in map")

	// 2. Generated unmarshaler
	var genMsg testpb.CompatibilityMessage
	err = genMsg.UnmarshalProtoJSONX(jsonInput)
	require.Error(t, err, "generated unmarshaler must reject null message in map")
}

// TestNilReceiverSafety verifies that calling MarshalProtoJSONX or UnmarshalProtoJSONX
// on a nil pointer returns an error rather than panicking.
func TestNilReceiverSafety(t *testing.T) {
	var nilProfile *testpb.UserProfile
	_, err := nilProfile.MarshalProtoJSONX()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-nil pointer")

	err = nilProfile.UnmarshalProtoJSONX([]byte("{}"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-nil pointer")
}

// TestFastPathWithOmittedAndShuffledNestedFields verifies that the generated
// ordered fast path succeeds when intermediate fields are omitted and when a
// nested message has out-of-order fields.
func TestFastPathWithOmittedAndShuffledNestedFields(t *testing.T) {
	// Email and Age are omitted, and Address has keys out of proto definition order.
	jsonInput := []byte(`{"id":"u1","name":"Alice","isActive":true,"address":{"country":"USA","street":"123 Main St"}}`)
	var msg testpb.UserProfile
	err := msg.UnmarshalProtoJSONX(jsonInput)
	require.NoError(t, err)
	assert.Equal(t, "u1", msg.Id)
	assert.Equal(t, "Alice", msg.Name)
	assert.True(t, msg.IsActive)
	require.NotNil(t, msg.Address)
	assert.Equal(t, "USA", msg.Address.Country)
	assert.Equal(t, "123 Main St", msg.Address.Street)
}

// TestFloatPrecisionMatchesParseFloat verifies that generated float32/float64
// decoding has 0-ULP drift relative to strconv.ParseFloat.
func TestFloatPrecisionMatchesParseFloat(t *testing.T) {
	jsonInput := []byte(`{"doubleField":0.12345678901234567,"floatField":1.0000001788139343}`)
	var genMsg testpb.ComplexMessage
	require.NoError(t, genMsg.UnmarshalProtoJSONX(jsonInput))

	var runtimeMsg testpb.ComplexMessage
	require.NoError(t, (UnmarshalOptions{DisableFastPath: true}).Unmarshal(jsonInput, &runtimeMsg))

	assert.Equal(t, runtimeMsg.DoubleField, genMsg.DoubleField)
	assert.Equal(t, runtimeMsg.FloatField, genMsg.FloatField)
}

// TestEscapedStringWithControlCharRejected verifies that unescaped control
// characters inside strings containing valid escape sequences are rejected.
func TestEscapedStringWithControlCharRejected(t *testing.T) {
	jsonInput := []byte("{\"stringField\":\"hello\\n\x01world\"}")
	var msg testpb.ComplexMessage
	require.Error(t, (UnmarshalOptions{DisableFastPath: true}).Unmarshal(jsonInput, &msg))
	require.Error(t, msg.UnmarshalProtoJSONX(jsonInput))
}

// TestFallbackUnmarshalWithOptionsDiscardUnknown verifies that calling
// UnmarshalProtoJSONXWithOptions(data, true) on a message with extensions
// forwards discardUnknown to the runtime fallback.
func TestFallbackUnmarshalWithOptionsDiscardUnknown(t *testing.T) {
	jsonInput := []byte(`{"optionalInt32": 7, "unknownField": "ignored"}`)
	var msg conformance.TestAllTypesProto2
	require.NoError(t, msg.UnmarshalProtoJSONXWithOptions(jsonInput, true))
	assert.Equal(t, int32(7), msg.GetOptionalInt32())
	require.Error(t, msg.UnmarshalProtoJSONXWithOptions(jsonInput, false))
}

// TestOneofDuplicateWithNullValueRejected verifies that setting a google.protobuf.NullValue
// oneof variant to null counts as populating the oneof and rejects a second variant.
func TestOneofDuplicateWithNullValueRejected(t *testing.T) {
	for _, payload := range []string{
		`{"oneofNullValue": null, "oneofUint32": 42}`,
		`{"oneofUint32": 42, "oneofNullValue": null}`,
	} {
		var genMsg conformance.TestAllTypesProto3
		require.Error(t, genMsg.UnmarshalProtoJSONX([]byte(payload)), payload)

		var runtimeMsg conformance.TestAllTypesProto3
		require.Error(t, (UnmarshalOptions{DisableFastPath: true}).Unmarshal([]byte(payload), &runtimeMsg), payload)
	}
}

// TestMapValueNullStructpbValueAllowed verifies that UnmarshalMessageMapValue
// permits JSON null when the value type is google.protobuf.Value.
func TestMapValueNullStructpbValueAllowed(t *testing.T) {
	d := protojsonxgen.NewDecoder([]byte("null"))
	val, err := protojsonxgen.UnmarshalMessageMapValue[structpb.Value](d, false)
	require.NoError(t, err)
	require.NotNil(t, val)
	_, ok := val.Kind.(*structpb.Value_NullValue)
	assert.True(t, ok)
}
