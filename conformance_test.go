package protojsonx

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	conformance "github.com/sudorandom/protojsonx/internal/conformancepb"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestConformanceMessageParity(t *testing.T) {
	tests := []struct {
		name string
		msg  proto.Message
	}{
		{
			name: "Proto3KitchenSink",
			msg: &conformance.TestAllTypesProto3{
				OptionalInt32:         123,
				OptionalInt64:         -9007199254740991,
				OptionalUint32:        456,
				OptionalUint64:        9007199254740991,
				OptionalFloat:         1.5,
				OptionalDouble:        -2.25,
				OptionalBool:          true,
				OptionalString:        "hello",
				OptionalBytes:         []byte("bytes"),
				OptionalNestedMessage: &conformance.TestAllTypesProto3_NestedMessage{A: 7},
				OptionalNestedEnum:    conformance.TestAllTypesProto3_BAZ,
				RepeatedInt32:         []int32{1, -2, 3},
				RepeatedString:        []string{"a", "b"},
				RepeatedBytes:         [][]byte{[]byte("x"), []byte("y")},
				RepeatedNestedEnum: []conformance.TestAllTypesProto3_NestedEnum{
					conformance.TestAllTypesProto3_FOO,
					conformance.TestAllTypesProto3_BAR,
				},
				MapInt32Int32: map[int32]int32{1: 2, -3: 4},
				MapStringString: map[string]string{
					"a": "one",
					"b": "two",
				},
				MapStringNestedMessage: map[string]*conformance.TestAllTypesProto3_NestedMessage{
					"nested": {A: 11},
				},
				OneofField: &conformance.TestAllTypesProto3_OneofString{
					OneofString: "choice",
				},
			},
		},
		{
			name: "Proto2KitchenSink",
			msg: &conformance.TestAllTypesProto2{
				OptionalInt32:         proto.Int32(123),
				OptionalInt64:         proto.Int64(-9007199254740991),
				OptionalUint32:        proto.Uint32(456),
				OptionalUint64:        proto.Uint64(9007199254740991),
				OptionalFloat:         proto.Float32(1.5),
				OptionalDouble:        proto.Float64(-2.25),
				OptionalBool:          proto.Bool(true),
				OptionalString:        proto.String("hello"),
				OptionalBytes:         []byte("bytes"),
				OptionalNestedMessage: &conformance.TestAllTypesProto2_NestedMessage{A: proto.Int32(7)},
				OptionalNestedEnum:    conformance.TestAllTypesProto2_BAZ.Enum(),
				RepeatedInt32:         []int32{1, -2, 3},
				RepeatedString:        []string{"a", "b"},
				RepeatedBytes:         [][]byte{[]byte("x"), []byte("y")},
				RepeatedNestedEnum: []conformance.TestAllTypesProto2_NestedEnum{
					conformance.TestAllTypesProto2_FOO,
					conformance.TestAllTypesProto2_BAR,
				},
				MapInt32Int32: map[int32]int32{1: 2, -3: 4},
				MapStringString: map[string]string{
					"a": "one",
					"b": "two",
				},
				MapStringNestedMessage: map[string]*conformance.TestAllTypesProto2_NestedMessage{
					"nested": {A: proto.Int32(11)},
				},
				OneofField: &conformance.TestAllTypesProto2_OneofString{
					OneofString: "choice",
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertConformanceParity(t, tc.msg)
		})
	}
}

func assertConformanceParity(t *testing.T, msg proto.Message) {
	t.Helper()

	xData, err := Marshal(msg)
	require.NoError(t, err)

	stdData, err := protojson.Marshal(msg)
	require.NoError(t, err)
	assert.JSONEq(t, string(stdData), string(xData))

	out := reflect.New(reflect.TypeOf(msg).Elem()).Interface().(proto.Message)
	err = Unmarshal(stdData, out)
	require.NoError(t, err)
	assert.True(t, proto.Equal(msg, out), "unmarshal roundtrip mismatch")
}
