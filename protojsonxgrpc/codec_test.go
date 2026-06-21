package protojsonxgrpc_test

import (
	"fmt"
	"testing"

	"github.com/sudorandom/protojsonx"
	"github.com/sudorandom/protojsonx/protojsonxgrpc"
	"google.golang.org/grpc/encoding"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func ExampleCodec() {
	var codec encoding.Codec = protojsonxgrpc.Codec{
		UnmarshalOptions: protojsonx.UnmarshalOptions{
			DiscardUnknown: true,
		},
	}

	data, err := codec.Marshal(wrapperspb.String("hello"))
	if err != nil {
		panic(err)
	}

	var out wrapperspb.StringValue
	if err := codec.Unmarshal(data, &out); err != nil {
		panic(err)
	}

	fmt.Println(codec.Name(), out.Value)
	// Output: json hello
}

func TestCodecTypedNilMessageReturnsError(t *testing.T) {
	codec := protojsonxgrpc.Codec{}
	var msg *wrapperspb.StringValue

	if _, err := codec.Marshal(msg); err == nil {
		t.Fatal("expected typed nil marshal error")
	}
	if err := codec.Unmarshal([]byte(`{}`), msg); err == nil {
		t.Fatal("expected typed nil unmarshal error")
	}
}
