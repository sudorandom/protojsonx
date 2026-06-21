package protojsonxconnect_test

import (
	"fmt"

	"connectrpc.com/connect"
	"github.com/sudorandom/protojsonx"
	"github.com/sudorandom/protojsonx/pb"
	"github.com/sudorandom/protojsonx/protojsonxconnect"
)

func ExampleCodec() {
	var codec connect.Codec = &protojsonxconnect.Codec{
		UnmarshalOptions: protojsonx.UnmarshalOptions{
			DiscardUnknown: true,
		},
	}

	data, err := codec.Marshal(&pb.UserProfile{
		Id:       "usr-789",
		Name:     "Alice",
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
	// Output: json usr-789 Alice true
}
