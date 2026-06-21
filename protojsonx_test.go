package protojsonx

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/sudorandom/protojsonx/internal/testpb"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func createComplexMessage() *testpb.ComplexMessage {
	return &testpb.ComplexMessage{
		DoubleField:    123.456,
		FloatField:     78.9,
		Int32Field:     -42,
		Int64Field:     -9000000000,
		Uint32Field:    4200,
		Uint64Field:    90000000000,
		Sint32Field:    -55,
		Sint64Field:    -8800000,
		Fixed32Field:   999,
		Fixed64Field:   888888888,
		Sfixed32Field:  -111,
		Sfixed64Field:  -222222222,
		BoolField:      true,
		StringField:    "hello world \n \t \" \\",
		BytesField:     []byte{1, 2, 3, 4, 5},
		EnumField:      testpb.TestEnum_TEST_ENUM_FIRST,
		TimestampField: timestamppb.New(time.Date(2026, 6, 21, 8, 30, 0, 123000000, time.UTC)),
		DurationField:  durationpb.New(123*time.Second + 456*time.Millisecond),
		ChildField: &testpb.ChildMessage{
			Name:  "nested child",
			Value: 99,
		},
		RepeatedString: []string{"apple", "banana", "cherry"},
		RepeatedMessage: []*testpb.ChildMessage{
			{Name: "item1", Value: 10},
			{Name: "item2", Value: 20},
		},
		MapStringString: map[string]string{
			"key1": "value1",
			"key2": "value2",
		},
	}
}

func TestReflectCompiles(t *testing.T) {
	tbl := GetTable(&testpb.ComplexMessage{})
	require.NotNil(t, tbl, "table compilation returned nil")

	assert.NotNil(t, tbl.fieldMap["doubleField"])
	assert.NotNil(t, tbl.fieldMap["double_field"])
}
