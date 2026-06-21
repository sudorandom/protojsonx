package protojsonxgrpc_test

import (
	"fmt"

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
