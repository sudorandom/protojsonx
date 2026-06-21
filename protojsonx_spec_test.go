package protojsonx

import (
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/sudorandom/protojsonx/internal/testpb"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestProtojsonPrimitiveTypes(t *testing.T) {
	tests := []struct {
		name string
		msg  proto.Message
	}{
		{
			name: "Double",
			msg:  &testpb.ComplexMessage{DoubleField: 3.14159},
		},
		{
			name: "Float",
			msg:  &testpb.ComplexMessage{FloatField: 1.5},
		},
		{
			name: "Int32",
			msg:  &testpb.ComplexMessage{Int32Field: -100},
		},
		{
			name: "Int64",
			msg:  &testpb.ComplexMessage{Int64Field: -1234567890},
		},
		{
			name: "Uint32",
			msg:  &testpb.ComplexMessage{Uint32Field: 100},
		},
		{
			name: "Uint64",
			msg:  &testpb.ComplexMessage{Uint64Field: 1234567890},
		},
		{
			name: "Sint32",
			msg:  &testpb.ComplexMessage{Sint32Field: -50},
		},
		{
			name: "Sint64",
			msg:  &testpb.ComplexMessage{Sint64Field: -5000},
		},
		{
			name: "Fixed32",
			msg:  &testpb.ComplexMessage{Fixed32Field: 400},
		},
		{
			name: "Fixed64",
			msg:  &testpb.ComplexMessage{Fixed64Field: 4000},
		},
		{
			name: "Sfixed32",
			msg:  &testpb.ComplexMessage{Sfixed32Field: -400},
		},
		{
			name: "Sfixed64",
			msg:  &testpb.ComplexMessage{Sfixed64Field: -4000},
		},
		{
			name: "Bool",
			msg:  &testpb.ComplexMessage{BoolField: true},
		},
		{
			name: "String",
			msg:  &testpb.ComplexMessage{StringField: "hello world"},
		},
		{
			name: "Bytes",
			msg:  &testpb.ComplexMessage{BytesField: []byte("protojsonx")},
		},
		{
			name: "Enum",
			msg:  &testpb.ComplexMessage{EnumField: testpb.TestEnum_TEST_ENUM_FIRST},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Test Marshal cross-compatibility
			xData, err := Marshal(tc.msg)
			require.NoError(t, err, "Marshal failed")

			stdData, err := protojson.Marshal(tc.msg)
			require.NoError(t, err, "standard protojson.Marshal failed")

			assert.Equal(t, stdData, xData, "JSON output mismatch")

			// Test Unmarshal roundtrip
			newMsg := reflect.New(reflect.TypeOf(tc.msg).Elem()).Interface().(proto.Message)
			err = Unmarshal(xData, newMsg)
			require.NoError(t, err, "Unmarshal failed")

			assert.True(t, proto.Equal(tc.msg, newMsg), "roundtripped message mismatch")
		})
	}
}

func TestProtojsonWellKnownTypes(t *testing.T) {
	// First let's test the fully supported well-known types: Timestamp and Duration.
	t.Run("Timestamp", func(t *testing.T) {
		msg := &testpb.ComplexMessage{
			TimestampField: timestamppb.New(time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)),
		}
		xData, err := Marshal(msg)
		require.NoError(t, err)

		stdData, err := protojson.Marshal(msg)
		require.NoError(t, err)

		assert.Equal(t, stdData, xData, "Timestamp json mismatch")

		var out testpb.ComplexMessage
		err = Unmarshal(xData, &out)
		require.NoError(t, err)
		assert.True(t, proto.Equal(msg, &out), "roundtrip mismatch")
	})

	t.Run("Duration", func(t *testing.T) {
		msg := &testpb.ComplexMessage{
			DurationField: durationpb.New(5 * time.Minute),
		}
		xData, err := Marshal(msg)
		require.NoError(t, err)

		stdData, err := protojson.Marshal(msg)
		require.NoError(t, err)

		assert.Equal(t, stdData, xData, "Duration json mismatch")

		var out testpb.ComplexMessage
		err = Unmarshal(xData, &out)
		require.NoError(t, err)
		assert.True(t, proto.Equal(msg, &out), "roundtrip mismatch")
	})

	t.Run("Empty", func(t *testing.T) {
		msg := &testpb.SpecMessage{
			EmptyField: &emptypb.Empty{},
		}
		xData, err := Marshal(msg)
		require.NoError(t, err)

		stdData, err := protojson.Marshal(msg)
		require.NoError(t, err)

		assert.Equal(t, stdData, xData, "Empty json mismatch")

		var out testpb.SpecMessage
		err = Unmarshal(xData, &out)
		require.NoError(t, err)
		assert.True(t, proto.Equal(msg, &out), "roundtrip mismatch")
	})

	// Now let's test wrappers and WKTs that compile but serialize/deserialize as custom structs
	t.Run("StringValue Compilation and Marshal", func(t *testing.T) {
		msg := &testpb.SpecMessage{
			StringValueField: wrapperspb.String("test"),
		}
		xData, err := Marshal(msg)
		require.NoError(t, err, "Marshal failed")

		stdData, err := protojson.Marshal(msg)
		require.NoError(t, err)
		assert.Equal(t, stdData, xData, "StringValue json mismatch")

		var out testpb.SpecMessage
		err = Unmarshal(stdData, &out)
		require.NoError(t, err, "Unmarshal failed")

		require.NotNil(t, out.StringValueField)
		assert.Equal(t, "test", out.StringValueField.Value)
	})

	t.Run("DoubleValue Compilation and Marshal", func(t *testing.T) {
		msg := &testpb.SpecMessage{
			DoubleValueField: wrapperspb.Double(1.23),
		}
		xData, err := Marshal(msg)
		require.NoError(t, err)

		stdData, err := protojson.Marshal(msg)
		require.NoError(t, err)
		assert.Equal(t, stdData, xData, "DoubleValue json mismatch")

		var out testpb.SpecMessage
		err = Unmarshal(stdData, &out)
		require.NoError(t, err)

		require.NotNil(t, out.DoubleValueField)
		assert.Equal(t, 1.23, out.DoubleValueField.Value)
	})

	t.Run("BoolValue Compilation and Marshal", func(t *testing.T) {
		msg := &testpb.SpecMessage{
			BoolValueField: wrapperspb.Bool(true),
		}
		xData, err := Marshal(msg)
		require.NoError(t, err)

		stdData, err := protojson.Marshal(msg)
		require.NoError(t, err)
		assert.Equal(t, stdData, xData, "BoolValue json mismatch")

		var out testpb.SpecMessage
		err = Unmarshal(stdData, &out)
		require.NoError(t, err)

		require.NotNil(t, out.BoolValueField)
		assert.True(t, out.BoolValueField.Value)
	})

	t.Run("FieldMask", func(t *testing.T) {
		mask, err := fieldmaskpb.New(&testpb.ComplexMessage{}, "double_field", "float_field")
		require.NoError(t, err)

		msg := &testpb.SpecMessage{
			FieldMaskField: mask,
		}
		xData, err := Marshal(msg)
		require.NoError(t, err)

		stdData, err := protojson.Marshal(msg)
		require.NoError(t, err)
		assert.Equal(t, stdData, xData, "FieldMask json mismatch")

		var out testpb.SpecMessage
		err = Unmarshal(stdData, &out)
		require.NoError(t, err)

		require.NotNil(t, out.FieldMaskField)
		assert.Len(t, out.FieldMaskField.Paths, 2)
	})

	t.Run("Any", func(t *testing.T) {
		child := &testpb.ChildMessage{Name: "nested", Value: 42}
		anyVal, err := anypb.New(child)
		require.NoError(t, err)

		msg := &testpb.SpecMessage{
			AnyField: anyVal,
		}
		xData, err := Marshal(msg)
		require.NoError(t, err)

		stdData, err := protojson.Marshal(msg)
		require.NoError(t, err)
		assert.Equal(t, stdData, xData, "Any json mismatch")

		var out testpb.SpecMessage
		err = Unmarshal(stdData, &out)
		require.NoError(t, err)

		require.NotNil(t, out.AnyField)
		assert.Equal(t, anyVal.TypeUrl, out.AnyField.TypeUrl)
	})

	// Lastly, verify that unsupported complex WKT schemas (which contain oneof or message-valued maps)
	// fail table compilation explicitly.
	t.Run("Unsupported WKT Struct", func(t *testing.T) {
		st, err := structpb.NewStruct(map[string]any{"key": "value"})
		require.NoError(t, err)

		msg := &testpb.StructMessage{
			StructField: st,
		}
		_, err = Marshal(msg)
		require.Error(t, err, "expected error compiling Struct field")
	})

	t.Run("Unsupported WKT Value", func(t *testing.T) {
		val, err := structpb.NewValue("string-value")
		require.NoError(t, err)

		msg := &testpb.ValueMessage{
			ValueField: val,
		}
		_, err = Marshal(msg)
		require.Error(t, err, "expected error compiling Value field")
	})

	t.Run("Unsupported WKT ListValue", func(t *testing.T) {
		list, err := structpb.NewList([]any{"a", 1})
		require.NoError(t, err)

		msg := &testpb.ListValueMessage{
			ListValueField: list,
		}
		_, err = Marshal(msg)
		require.Error(t, err, "expected error compiling ListValue field")
	})
}

func TestProtojsonRootWellKnownWrapper(t *testing.T) {
	msg := wrapperspb.String("hello")

	xData, err := Marshal(msg)
	require.NoError(t, err)

	stdData, err := protojson.Marshal(msg)
	require.NoError(t, err)
	assert.Equal(t, stdData, xData)

	var out wrapperspb.StringValue
	err = Unmarshal(stdData, &out)
	require.NoError(t, err)
	assert.Equal(t, "hello", out.Value)
}
