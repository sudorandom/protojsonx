package protojsonx_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/sudorandom/protojsonx"
	"github.com/sudorandom/protojsonx/internal/testpb"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"
)

// Issue 1: Any marshalling data loss when subtable uses fallback
func TestBug1_AnyMarshallingDataLoss(t *testing.T) {
	// A dynamic message or custom type can force useProtojson=true
	// Or we can verify with structpb.Struct / dynamicpb if supported, or check Any wrapping
	msg := &testpb.ComplexMessage{
		StringField: "hello inside any",
	}
	anyVal, err := anypb.New(msg)
	require.NoError(t, err)

	outerMsg := &testpb.SpecMessage{
		AnyField: anyVal,
	}

	b, err := protojsonx.Marshal(outerMsg)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"stringField":"hello inside any"`)
}


// Issue 6: TypeMapStringString duplicate keys
func TestBug6_DuplicateMapKeys(t *testing.T) {
	jsonInput := []byte(`{"mapStringString":{"key1":"val1","key1":"val2"}}`)
	msg := &testpb.ComplexMessage{}
	err := protojsonx.Unmarshal(jsonInput, msg)
	assert.Error(t, err, "should reject duplicate map keys")
}


// Issue 8: Duration formatting consistency between marshalTo and marshalCustomWellKnown
func TestBug8_DurationFormattingInconsistency(t *testing.T) {
	dur := durationpb.New(500 * time.Millisecond)
	anyVal, err := anypb.New(dur)
	require.NoError(t, err)

	msgAny := &testpb.SpecMessage{AnyField: anyVal}
	bAny, err := protojsonx.Marshal(msgAny)
	require.NoError(t, err)

	msgDirect := &testpb.ComplexMessage{DurationField: dur}
	bDirect, err := protojsonx.Marshal(msgDirect)
	require.NoError(t, err)

	assert.Contains(t, string(bAny), `"0.500s"`)
	assert.Contains(t, string(bDirect), `"0.500s"`)
}



// Issue 10: Potential Nil Pointer Dereference in writeValue
func TestBug10_NilValueStructList(t *testing.T) {
	val := &structpb.Value{
		Kind: &structpb.Value_StructValue{StructValue: nil},
	}
	msg := &testpb.ValueMessage{
		ValueField: val,
	}
	assert.NotPanics(t, func() {
		_, _ = protojsonx.Marshal(msg)
	})
}
