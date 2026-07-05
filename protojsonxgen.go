package protojsonx

import (
	"github.com/sudorandom/protojsonx/protojsonxgen"
	"google.golang.org/protobuf/proto"
)

func init() {
	protojsonxgen.RegisterFallbacks(
		func(m proto.Message) ([]byte, error) {
			return Marshal(m)
		},
		func(data []byte, m proto.Message) error {
			return Unmarshal(data, m)
		},
		func(data []byte, m proto.Message, discardUnknown bool) error {
			return UnmarshalOptions{DiscardUnknown: discardUnknown}.Unmarshal(data, m)
		},
	)
}
