package protojsonx

import (
	"fmt"
	"math"
	"reflect"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/sudorandom/protojsonx/internal/testpb"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestComprehensiveShapes(t *testing.T) {
	msg := createComplexMessage()

	t.Run("Standard Marshal/Unmarshal roundtrip", func(t *testing.T) {
		// Marshal using protojsonx
		data, err := Marshal(msg)
		require.NoError(t, err)

		// Unmarshal using protojsonx
		var out testpb.ComplexMessage
		err = Unmarshal(data, &out)
		require.NoError(t, err)

		// Compare semantically
		assert.True(t, proto.Equal(msg, &out), "Roundtrip result not equal to original")
	})

	t.Run("Cross-compatibility with standard protojson", func(t *testing.T) {
		// Marshal using protojsonx
		data, err := Marshal(msg)
		require.NoError(t, err)

		// Unmarshal using standard protojson
		var out testpb.ComplexMessage
		err = protojson.Unmarshal(data, &out)
		require.NoError(t, err, "protojson failed to parse protojsonx output")

		assert.True(t, proto.Equal(msg, &out), "protojson unmarshal of protojsonx output not equal to original")

		// Marshal using standard protojson
		stdData, err := protojson.Marshal(msg)
		require.NoError(t, err)

		// Unmarshal using protojsonx
		var tableOut testpb.ComplexMessage
		err = Unmarshal(stdData, &tableOut)
		require.NoError(t, err, "protojsonx failed to parse protojson output")

		assert.True(t, proto.Equal(msg, &tableOut), "protojsonx unmarshal of protojson output not equal to original")
	})

	t.Run("EmitUnpopulated Option", func(t *testing.T) {
		emptyMsg := &testpb.ComplexMessage{}

		// Default: false
		dataDefault, err := Marshal(emptyMsg)
		require.NoError(t, err)
		assert.Equal(t, "{}", string(dataDefault))

		// EmitUnpopulated: true
		dataEmit, err := MarshalOptions{EmitUnpopulated: true}.Marshal(emptyMsg)
		require.NoError(t, err)

		// Verify fields are present
		assert.Contains(t, string(dataEmit), `"doubleField"`)
		assert.Contains(t, string(dataEmit), `"int32Field"`)
		assert.Contains(t, string(dataEmit), `"boolField"`)
	})

	t.Run("UseProtoNames Option", func(t *testing.T) {
		data, err := MarshalOptions{UseProtoNames: true}.Marshal(msg)
		require.NoError(t, err)

		// Should use snake_case proto name "double_field" instead of json camelCase "doubleField"
		assert.Contains(t, string(data), `"double_field"`, "UseProtoNames did not produce proto snake_case name")
	})

	t.Run("Float extreme values", func(t *testing.T) {
		extremeMsg := &testpb.ComplexMessage{
			DoubleField: math.NaN(),
			FloatField:  float32(math.Inf(1)),
		}

		data, err := Marshal(extremeMsg)
		require.NoError(t, err)

		assert.Contains(t, string(data), `"doubleField":"NaN"`)
		assert.Contains(t, string(data), `"floatField":"Infinity"`)

		var out testpb.ComplexMessage
		err = Unmarshal(data, &out)
		require.NoError(t, err, "failed to unmarshal special floats")

		assert.True(t, math.IsNaN(out.DoubleField))
		assert.True(t, math.IsInf(float64(out.FloatField), 1))
	})
}

func TestRepeatedMessageNullElementRejects(t *testing.T) {
	data := []byte(`{"repeatedMessage":[null,{"name":"after nil","value":7}]}`)
	var out testpb.ComplexMessage
	err := Unmarshal(data, &out)
	require.Error(t, err)
}

func TestGeneratedMarshalMatchesRuntime(t *testing.T) {
	tests := []struct {
		name      string
		runtime   func() ([]byte, error)
		generated func() ([]byte, error)
	}{
		{
			name: "user profile",
			runtime: func() ([]byte, error) {
				return Marshal(createUserProfile())
			},
			generated: func() ([]byte, error) {
				return createUserProfile().MarshalProtoJSONX()
			},
		},
		{
			name: "complex message",
			runtime: func() ([]byte, error) {
				return Marshal(createBenchComplexMessage())
			},
			generated: func() ([]byte, error) {
				return createBenchComplexMessage().MarshalProtoJSONX()
			},
		},
		{
			name: "compatibility message",
			runtime: func() ([]byte, error) {
				return Marshal(createCompatibilityMessage())
			},
			generated: func() ([]byte, error) {
				return createCompatibilityMessage().MarshalProtoJSONX()
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want, err := tt.runtime()
			require.NoError(t, err)
			got, err := tt.generated()
			require.NoError(t, err)
			assert.JSONEq(t, string(want), string(got))
		})
	}
}

func TestGeneratedUnmarshalMatchesRuntime(t *testing.T) {
	tests := []struct {
		name string
		msg  proto.Message
		new  func() interface {
			UnmarshalProtoJSONX([]byte) error
			proto.Message
		}
	}{
		{
			name: "user profile",
			msg:  createUserProfile(),
			new: func() interface {
				UnmarshalProtoJSONX([]byte) error
				proto.Message
			} {
				return &testpb.UserProfile{}
			},
		},
		{
			name: "complex message",
			msg:  createBenchComplexMessage(),
			new: func() interface {
				UnmarshalProtoJSONX([]byte) error
				proto.Message
			} {
				return &testpb.ComplexMessage{}
			},
		},
		{
			name: "compatibility message",
			msg:  createCompatibilityMessage(),
			new: func() interface {
				UnmarshalProtoJSONX([]byte) error
				proto.Message
			} {
				return &testpb.CompatibilityMessage{}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := Marshal(tt.msg)
			require.NoError(t, err)

			var runtimeOut proto.Message
			switch tt.msg.(type) {
			case *testpb.UserProfile:
				runtimeOut = &testpb.UserProfile{}
			case *testpb.ComplexMessage:
				runtimeOut = &testpb.ComplexMessage{}
			case *testpb.CompatibilityMessage:
				runtimeOut = &testpb.CompatibilityMessage{}
			default:
				t.Fatalf("unsupported test message %T", tt.msg)
			}
			require.NoError(t, Unmarshal(data, runtimeOut))

			generatedOut := tt.new()
			require.NoError(t, generatedOut.UnmarshalProtoJSONX(data))
			assert.True(t, proto.Equal(runtimeOut, generatedOut), "generated unmarshal result differs from runtime")
		})
	}
}

func TestConcurrentColdTableUse(t *testing.T) {
	cacheMutex.Lock()
	delete(tableCache, reflect.TypeOf(testpb.ComplexMessage{}))
	delete(tableCache, reflect.TypeOf(testpb.ChildMessage{}))
	cacheMutex.Unlock()

	const workers = 128
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			<-start
			data, err := Marshal(createComplexMessage())
			if err != nil {
				errs <- err
				return
			}
			if string(data) == "{}" {
				errs <- fmt.Errorf("marshal used an empty table")
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}
}

func createCompatibilityMessage() *testpb.CompatibilityMessage {
	return &testpb.CompatibilityMessage{
		Choice: &testpb.CompatibilityMessage_NameChoice{NameChoice: "my oneof choice"},
		MapStringInt32: map[string]int32{
			"key1": 100,
			"key2": 200,
		},
		MapInt32String: map[int32]string{
			1: "one",
			2: "two",
		},
		MapStringMessage: map[string]*testpb.ChildMessage{
			"nested": {
				Name:  "child",
				Value: 123,
			},
		},
		RepeatedInt32: []int32{1, 2, 3},
		OptionalString: proto.String("pointer string"),
		OptionalInt32:  proto.Int32(42),
	}
}
