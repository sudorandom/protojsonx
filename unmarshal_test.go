package protojsonx

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/sudorandom/protojsonx/internal/testpb"
	conformance "github.com/sudorandom/protojsonx/internal/conformancepb"
	"google.golang.org/protobuf/types/known/durationpb"
)

func TestUnmarshalRejectsTrailingData(t *testing.T) {
	var out testpb.Address
	err := Unmarshal([]byte(`{"street":"123"} trailing`), &out)
	require.Error(t, err, "expected trailing data error")
}

func TestDiscardUnknownRejectsInvalidJSONValue(t *testing.T) {
	var out testpb.UserProfile
	err := (UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(`{"unknown":,"id":"123"}`), &out)
	require.Error(t, err, "expected invalid unknown value to be rejected")
}

func TestUnmarshalUnicodeEscapes(t *testing.T) {
	var out testpb.Address
	err := Unmarshal([]byte(`{"street":"\u20ac \ud83d\ude00"}`), &out)
	require.NoError(t, err)
	assert.Equal(t, "€ 😀", out.Street, "unicode escapes decoded incorrectly")
}

func TestUnmarshalRejectsMalformedStringEscapes(t *testing.T) {
	tests := [][]byte{
		[]byte(`{"street":"bad \u12"}`),
		[]byte("{\"street\":\"bad \n raw\"}"),
		[]byte(`{"street":"bad \q"}`),
	}

	for _, data := range tests {
		var out testpb.Address
		err := Unmarshal(data, &out)
		require.Error(t, err, "expected malformed string error for payload %s", data)
	}
}

func TestUnmarshalRejectsUnknownEnumName(t *testing.T) {
	var out testpb.ComplexMessage
	err := Unmarshal([]byte(`{"enumField":"NOT_A_REAL_ENUM"}`), &out)
	require.Error(t, err, "expected unknown enum error")
}

func TestUnmarshalAcceptsQuotedRegularFloat(t *testing.T) {
	var out testpb.ComplexMessage
	err := Unmarshal([]byte(`{"doubleField":"1.25"}`), &out)
	require.NoError(t, err)
	assert.Equal(t, 1.25, out.DoubleField)
}

func TestUnmarshalRejectsInvalidKnownFieldNumbers(t *testing.T) {
	tests := [][]byte{
		[]byte(`{"int32Field":01}`),
		[]byte(`{"uint32Field":+1}`),
		[]byte(`{"doubleField":1e}`),
		[]byte(`{"floatField":--1}`),
	}

	for _, data := range tests {
		var out testpb.ComplexMessage
		err := Unmarshal(data, &out)
		require.Error(t, err, "expected invalid number error for payload %s", data)
	}
}

func TestNilMessageInputsReturnErrors(t *testing.T) {
	var msg *testpb.ComplexMessage

	_, err := Marshal(msg)
	require.Error(t, err)

	err = Unmarshal([]byte(`{}`), msg)
	require.Error(t, err)

	assert.Nil(t, GetTable(msg))
}

func TestUnmarshalRejectsDuplicateFieldNames(t *testing.T) {
	userPayloads := [][]byte{
		[]byte(`{"id":"first","id":"second"}`),
		[]byte(`{"isActive":true,"is_active":false}`),
	}
	for _, data := range userPayloads {
		var out testpb.UserProfile
		err := Unmarshal(data, &out)
		require.Error(t, err, "expected duplicate field error for user payload %s", data)
	}

	var complex testpb.ComplexMessage
	err := Unmarshal([]byte(`{"doubleField":1,"double_field":2}`), &complex)
	require.Error(t, err, "expected duplicate field error for complex payload")
}

func TestUnmarshalRejectsIntegerOverflow(t *testing.T) {
	payloads := [][]byte{
		[]byte(`{"int64Field": 20496382304121724020}`),
		[]byte(`{"uint64Field": 20496382304121724020}`),
	}
	for _, data := range payloads {
		var out testpb.ComplexMessage
		err := Unmarshal(data, &out)
		require.Error(t, err)

		var outPlugin testpb.ComplexMessage
		err = outPlugin.UnmarshalProtoJSONX(data)
		require.Error(t, err)
	}
}

func TestUnmarshalRejectsDuplicateMapKeys(t *testing.T) {
	data := []byte(`{"metadata":{"key":"first","key":"second"}}`)
	var out testpb.UserProfile
	err := Unmarshal(data, &out)
	require.Error(t, err, "expected duplicate map key error")

	// Also verify using the generated fast-path plugin unmarshaler
	var outPlugin testpb.UserProfile
	err = outPlugin.UnmarshalProtoJSONX(data)
	require.Error(t, err, "expected duplicate map key error in generated fast-path")
}

func TestUnmarshalRepeatedEnumDiscardUnknown(t *testing.T) {
	dataFast := []byte(`{"repeatedNestedEnum": ["FOO", "UNKNOWN_ENUM_VALUE", "BAR"]}`)
	dataSlow := []byte(`{"repeatedNestedEnum": ["FOO", "UNKNOWN_ENUM_VALUE", "BAR"], "id": 123}`)

	opts := UnmarshalOptions{DiscardUnknown: true}

	// 1. Table-driven test
	var msg1 conformance.TestAllTypesProto3
	err := opts.Unmarshal(dataSlow, &msg1)
	require.NoError(t, err)
	assert.Equal(t, []conformance.TestAllTypesProto3_NestedEnum{
		conformance.TestAllTypesProto3_FOO,
		conformance.TestAllTypesProto3_BAR,
	}, msg1.RepeatedNestedEnum)

	// 2. Fast-path generated code tests
	var msgFast conformance.TestAllTypesProto3
	err = msgFast.UnmarshalProtoJSONXWithOptions(dataFast, true)
	require.NoError(t, err)
	assert.Equal(t, []conformance.TestAllTypesProto3_NestedEnum{
		conformance.TestAllTypesProto3_FOO,
		conformance.TestAllTypesProto3_BAR,
	}, msgFast.RepeatedNestedEnum)

	var msgSlow conformance.TestAllTypesProto3
	err = msgSlow.UnmarshalProtoJSONXWithOptions(dataSlow, true)
	require.NoError(t, err)
	assert.Equal(t, []conformance.TestAllTypesProto3_NestedEnum{
		conformance.TestAllTypesProto3_FOO,
		conformance.TestAllTypesProto3_BAR,
	}, msgSlow.RepeatedNestedEnum)
}

func TestUnmarshalResetsExistingMessage(t *testing.T) {
	out := &testpb.UserProfile{
		Id:   "old",
		Name: "stale",
		Metadata: map[string]string{
			"stale": "value",
		},
		Tags: []string{"old"},
	}

	err := Unmarshal([]byte(`{"id":"new","metadata":{"fresh":"value"}}`), out)
	require.NoError(t, err)

	assert.Equal(t, "new", out.Id)
	assert.Equal(t, "", out.Name)
	assert.NotContains(t, out.Metadata, "stale")
	assert.Empty(t, out.Tags)
}

func TestUnmarshalNullScalarsAndContainers(t *testing.T) {
	out := &testpb.UserProfile{
		Id:       "old",
		Age:      42,
		IsActive: true,
		Tags:     []string{"stale"},
		Metadata: map[string]string{"stale": "value"},
	}

	data := []byte(`{"id":null,"age":null,"isActive":null,"tags":null,"metadata":null}`)
	err := Unmarshal(data, out)
	require.NoError(t, err)

	assert.Equal(t, "", out.Id)
	assert.Equal(t, int32(0), out.Age)
	assert.False(t, out.IsActive)
	assert.Nil(t, out.Tags)
	assert.Nil(t, out.Metadata)
}

func TestUnmarshalNullMessages(t *testing.T) {
	data := []byte(`{"childField":null,"timestampField":null,"durationField":null}`)
	var out testpb.ComplexMessage
	err := Unmarshal(data, &out)
	require.NoError(t, err)

	assert.Nil(t, out.ChildField)
	assert.Nil(t, out.TimestampField)
	assert.Nil(t, out.DurationField)
}

func TestDurationPrecision(t *testing.T) {
	msg := &testpb.ComplexMessage{
		DurationField: &durationpb.Duration{
			Seconds: 315575999999,
			Nanos:   123456789,
		},
	}

	data, err := Marshal(msg)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"315575999999.123456789s"`)

	var out testpb.ComplexMessage
	err = Unmarshal(data, &out)
	require.NoError(t, err)

	require.NotNil(t, out.DurationField)
	assert.Equal(t, msg.DurationField.GetSeconds(), out.DurationField.GetSeconds())
	assert.Equal(t, msg.DurationField.GetNanos(), out.DurationField.GetNanos())
}

func TestNegativeDurationRoundTrip(t *testing.T) {
	msg := &testpb.ComplexMessage{
		DurationField: &durationpb.Duration{
			Seconds: 0,
			Nanos:   -500000000,
		},
	}

	data, err := Marshal(msg)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"-0.500s"`)

	var out testpb.ComplexMessage
	err = Unmarshal(data, &out)
	require.NoError(t, err)

	require.NotNil(t, out.DurationField)
	assert.Equal(t, int64(0), out.DurationField.GetSeconds())
	assert.Equal(t, int32(-500000000), out.DurationField.GetNanos())
}

func TestDiscardUnknownSkipsNestedMixedValues(t *testing.T) {
	data := []byte(`{"unknown":{"arr":[{"nested":true},["x",{"y":1}]],"keep":null},"id":"123"}`)
	var out testpb.UserProfile
	err := (UnmarshalOptions{DiscardUnknown: true}).Unmarshal(data, &out)
	require.NoError(t, err)
	assert.Equal(t, "123", out.Id)
}

func TestAddressAndSessions(t *testing.T) {
	addr := &testpb.Address{
		Street:  "123 Main St",
		City:    "Seattle",
		State:   "WA",
		Zip:     "98101",
		Country: "USA",
	}

	data, err := Marshal(addr)
	require.NoError(t, err)

	var out testpb.Address
	err = Unmarshal(data, &out)
	require.NoError(t, err)

	assert.Equal(t, addr.Street, out.Street)
	assert.Equal(t, addr.City, out.City)
}

func TestUnmarshalWrappersObjectAndPrimitive(t *testing.T) {
	t.Run("Primitive wrapper values", func(t *testing.T) {
		jsonData := []byte(`{
			"doubleValueField": 1.23,
			"floatValueField": 4.56,
			"int64ValueField": "1234567890",
			"uint64ValueField": "9876543210",
			"int32ValueField": 123,
			"uint32ValueField": 456,
			"boolValueField": true,
			"stringValueField": "hello",
			"bytesValueField": "Ynl0ZXM=",
			"emptyField": {}
		}`)
		var out testpb.SpecMessage
		err := Unmarshal(jsonData, &out)
		require.NoError(t, err)

		assert.Equal(t, 1.23, out.DoubleValueField.Value)
		assert.Equal(t, float32(4.56), out.FloatValueField.Value)
		assert.Equal(t, int64(1234567890), out.Int64ValueField.Value)
		assert.Equal(t, uint64(9876543210), out.Uint64ValueField.Value)
		assert.Equal(t, int32(123), out.Int32ValueField.Value)
		assert.Equal(t, uint32(456), out.Uint32ValueField.Value)
		assert.Equal(t, true, out.BoolValueField.Value)
		assert.Equal(t, "hello", out.StringValueField.Value)
		assert.Equal(t, []byte("bytes"), out.BytesValueField.Value)
		assert.NotNil(t, out.EmptyField)
	})

	t.Run("Object wrapper values", func(t *testing.T) {
		jsonData := []byte(`{
			"doubleValueField": {"value": 1.23},
			"floatValueField": {"value": 4.56},
			"int64ValueField": {"value": "1234567890"},
			"uint64ValueField": {"value": "9876543210"},
			"int32ValueField": {"value": 123},
			"uint32ValueField": {"value": 456},
			"boolValueField": {"value": true},
			"stringValueField": {"value": "hello"},
			"bytesValueField": {"value": "Ynl0ZXM="}
		}`)
		var out testpb.SpecMessage
		err := Unmarshal(jsonData, &out)
		require.NoError(t, err)

		assert.Equal(t, 1.23, out.DoubleValueField.Value)
		assert.Equal(t, float32(4.56), out.FloatValueField.Value)
		assert.Equal(t, int64(1234567890), out.Int64ValueField.Value)
		assert.Equal(t, uint64(9876543210), out.Uint64ValueField.Value)
		assert.Equal(t, int32(123), out.Int32ValueField.Value)
		assert.Equal(t, uint32(456), out.Uint32ValueField.Value)
		assert.Equal(t, true, out.BoolValueField.Value)
		assert.Equal(t, "hello", out.StringValueField.Value)
		assert.Equal(t, []byte("bytes"), out.BytesValueField.Value)
	})

	t.Run("Object wrappers with DiscardUnknown true", func(t *testing.T) {
		jsonData := []byte(`{
			"stringValueField": {"value": "hello", "unknown_field": 123},
			"emptyField": {"unknown_field": 456}
		}`)
		var out testpb.SpecMessage
		err := (UnmarshalOptions{DiscardUnknown: true}).Unmarshal(jsonData, &out)
		require.NoError(t, err)
		assert.Equal(t, "hello", out.StringValueField.Value)
		assert.NotNil(t, out.EmptyField)

		// DiscardUnknown false should reject it
		var outReject testpb.SpecMessage
		err = (UnmarshalOptions{DiscardUnknown: false}).Unmarshal(jsonData, &outReject)
		require.Error(t, err)
	})
}

func TestUnmarshalRecursionLimit(t *testing.T) {
	// Construct a deeply nested JSON payload (101 levels of objects)
	// {"childField": {"childField": ... }}
	var buf bytes.Buffer
	for i := 0; i < 101; i++ {
		buf.WriteString(`{"childField":`)
	}
	buf.WriteString(`{}`)
	for i := 0; i < 101; i++ {
		buf.WriteString(`}`)
	}

	var msg testpb.ComplexMessage
	err := UnmarshalOptions{DiscardUnknown: true}.Unmarshal(buf.Bytes(), &msg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeded maximum recursion depth")
}

func TestFallbackOptimizations(t *testing.T) {
	t.Run("synthetic oneof (optional fields)", func(t *testing.T) {
		jsonData := []byte(`{
			"optionalString": "hello",
			"optionalInt32": null
		}`)
		var out testpb.CompatibilityMessage
		err := Unmarshal(jsonData, &out)
		require.NoError(t, err)
		assert.Equal(t, "hello", *out.OptionalString)
		assert.Nil(t, out.OptionalInt32)
	})

	t.Run("discard unknown on fast path", func(t *testing.T) {
		jsonData := []byte(`{
			"optionalString": "hello",
			"unknownField": "skip me"
		}`)
		var out testpb.CompatibilityMessage
		// Without DiscardUnknown, should fail
		err := Unmarshal(jsonData, &out)
		require.Error(t, err)

		// With DiscardUnknown, should pass
		err = UnmarshalOptions{DiscardUnknown: true}.Unmarshal(jsonData, &out)
		require.NoError(t, err)
		assert.Equal(t, "hello", *out.OptionalString)
	})

	t.Run("discard unknown on slow path (shuffled keys)", func(t *testing.T) {
		jsonData := []byte(`{
			"unknownField": "skip me",
			"optionalString": "hello"
		}`)
		var out testpb.CompatibilityMessage
		// Without DiscardUnknown, should fail
		err := Unmarshal(jsonData, &out)
		require.Error(t, err)

		// With DiscardUnknown, should pass
		err = UnmarshalOptions{DiscardUnknown: true}.Unmarshal(jsonData, &out)
		require.NoError(t, err)
		assert.Equal(t, "hello", *out.OptionalString)
	})
}

func TestUnmarshalEdgeCases(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		wantErr bool
	}{
		{
			name:    "deeply nested arrays in skipValue",
			payload: `{"unknown":[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]}`,
			wantErr: true, // exceeds recursion limit
		},
		{
			name:    "invalid surrogate pairs",
			payload: `{"stringField":"\ud800\ud800"}`,
			wantErr: true,
		},
		{
			name:    "invalid low surrogate",
			payload: `{"stringField":"\udc00"}`,
			wantErr: true,
		},
		{
			name:    "unpaired high surrogate",
			payload: `{"stringField":"\ud83d"}`,
			wantErr: true,
		},
		{
			name:    "unpaired high surrogate followed by low invalid",
			payload: `{"stringField":"\ud83d\u0000"}`,
			wantErr: true,
		},
		{
			name:    "extreme large float",
			payload: `{"doubleField":1e9999999}`,
			wantErr: true, // invalid JSON number or Infinity
		},
		{
			name:    "extreme large int32",
			payload: `{"int32Field":99999999999999999999999999999999999999999999999999999}`,
			wantErr: true, // out of range
		},
		{
			name:    "bad base64",
			payload: `{"bytesField":"!!!!"}`,
			wantErr: true, // fails to decode base64
		},
		{
			name:    "control characters in string",
			payload: "{\"stringField\":\"\x00\x1f\"}",
			wantErr: true, // invalid control character
		},
		{
			name:    "unterminated string",
			payload: `{"stringField":"abc`,
			wantErr: true,
		},
		{
			name:    "whitespace exhaustion",
			payload: `{  "stringField"  :  "hello"  ,  "int32Field"  :  123  }`,
			wantErr: false,
		},
		{
			name:    "incomplete escape sequence",
			payload: `{"stringField":"\u123"}`,
			wantErr: true,
		},
		{
			name:    "duration with leading zero",
			payload: `{"durationField":"01s"}`,
			wantErr: true,
		},
		{
			name:    "duration with leading zero negative",
			payload: `{"durationField":"-01.500s"}`,
			wantErr: true,
		},
		{
			name:    "duration valid single zero seconds",
			payload: `{"durationField":"0.5s"}`,
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var msg testpb.ComplexMessage
			err := UnmarshalOptions{DiscardUnknown: true}.Unmarshal([]byte(tc.payload), &msg)
			if tc.wantErr {
				require.Error(t, err, "expected error for %s", tc.name)
			} else {
				require.NoError(t, err, "unexpected error for %s", tc.name)
			}
		})
	}
}
