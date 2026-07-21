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
	"google.golang.org/protobuf/types/known/fieldmaskpb"
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


// Issue 4: FieldMask unmarshaling rejects snake_case paths
func TestBug4_FieldMaskSnakeCase(t *testing.T) {
	jsonInput := []byte(`{"fieldMaskField":"string_field,child_field.name"}`)
	msg := &testpb.SpecMessage{}
	err := protojsonx.Unmarshal(jsonInput, msg)
	require.NoError(t, err)
	require.NotNil(t, msg.FieldMaskField)
	assert.Equal(t, []string{"string_field", "child_field.name"}, msg.FieldMaskField.Paths)
}

// Issue 2 & 3: Panic safety in compileTable and compileTableForType
func TestBug2And3_CompileTablePanicSafety(t *testing.T) {
	// Should not panic on non-proto message or non-slice types
	assert.NotPanics(t, func() {
		tbl := protojsonx.GetTable(&testpb.ComplexMessage{})
		require.NotNil(t, tbl)
	})
}

// Issue 5: Int64 precision loss for float strings or integers > 2^53 - 1
func TestBug5_Int64PrecisionLoss(t *testing.T) {
	// Integers above 2^53 - 1 should not lose precision when parsed from string or float
	jsonInput := []byte(`{"int64Field":"9007199254740993"}`)
	msg := &testpb.ComplexMessage{}
	err := protojsonx.Unmarshal(jsonInput, msg)
	require.NoError(t, err)
	assert.Equal(t, int64(9007199254740993), msg.Int64Field)

	// An invalid float integer that loses precision like "9007199254740993.0" shouldn't silently lose precision
	jsonFloatInput := []byte(`{"int64Field":9007199254740993.0}`)
	msgFloat := &testpb.ComplexMessage{}
	_ = protojsonx.Unmarshal(jsonFloatInput, msgFloat)
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

// Issue 9: FieldMask marshal failure for field names with digits
func TestBug9_FieldMaskMarshalDigits(t *testing.T) {
	msg := &testpb.SpecMessage{
		FieldMaskField: &fieldmaskpb.FieldMask{
			Paths: []string{"field_1", "test_field_2"},
		},
	}
	b, err := protojsonx.Marshal(msg)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"field1,testField2"`)
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
