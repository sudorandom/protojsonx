package protojsonx

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/sudorandom/protojsonx/internal/conformancepb"
	"github.com/sudorandom/protojsonx/internal/testpb"
	"google.golang.org/protobuf/proto"
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
