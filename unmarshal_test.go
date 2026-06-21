package protojsonx

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/sudorandom/protojsonx/internal/testpb"
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

func TestUnmarshalRejectsQuotedRegularFloat(t *testing.T) {
	var out testpb.ComplexMessage
	err := Unmarshal([]byte(`{"doubleField":"1.25"}`), &out)
	require.Error(t, err, "expected quoted non-special float error")
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
			Seconds: 9007199254740991,
			Nanos:   123456789,
		},
	}

	data, err := Marshal(msg)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"9007199254740991.123456789s"`)

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
