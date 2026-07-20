package protojsonx

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/sudorandom/protojsonx/internal/testpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

func TestDynamicpbSupport(t *testing.T) {
	// 1. Get the descriptor for UserProfile
	desc := (&testpb.UserProfile{}).ProtoReflect().Descriptor()

	// 2. Create a dynamic message
	dynMsg := dynamicpb.NewMessage(desc)

	// Unmarshal JSON into it (triggers the fallback path to protojson)
	payload := []byte(`{"id":"12345","name":"Dynamic User","age":42}`)
	err := Unmarshal(payload, dynMsg)
	require.NoError(t, err)

	// Verify values on the dynamic message
	assert.Equal(t, "12345", dynMsg.Get(desc.Fields().ByName("id")).String())
	assert.Equal(t, "Dynamic User", dynMsg.Get(desc.Fields().ByName("name")).String())
	assert.Equal(t, int64(42), dynMsg.Get(desc.Fields().ByName("age")).Int())

	// 3. Marshal the dynamic message back (triggers the fallback path to protojson)
	out, err := Marshal(dynMsg)
	require.NoError(t, err)
	assert.JSONEq(t, string(payload), string(out))
}
