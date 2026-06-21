package protojsonxgrpc_test

import (
	"fmt"

	"github.com/sudorandom/protojsonx"
	"github.com/sudorandom/protojsonx/pb"
	"github.com/sudorandom/protojsonx/protojsonxgrpc"
	"google.golang.org/grpc/encoding"
)

func ExampleCodec() {
	var codec encoding.Codec = protojsonxgrpc.Codec{
		UnmarshalOptions: protojsonx.UnmarshalOptions{
			DiscardUnknown: true,
		},
	}

	data, err := codec.Marshal(&pb.UserProfile{
		Id:       "usr-123",
		Name:     "Bob",
		IsActive: true,
	})
	if err != nil {
		panic(err)
	}

	var out pb.UserProfile
	if err := codec.Unmarshal(data, &out); err != nil {
		panic(err)
	}

	fmt.Println(codec.Name(), out.Id, out.Name, out.IsActive)
	// Output: json usr-123 Bob true
}
