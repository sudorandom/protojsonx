// Package protojsonxgrpc provides a gRPC-Go codec backed by protojsonx.
package protojsonxgrpc

import (
	"fmt"

	"github.com/sudorandom/protojsonx"
	"google.golang.org/grpc/encoding"
	"google.golang.org/protobuf/proto"
)

const Name = "json"

// Codec implements google.golang.org/grpc/encoding.Codec using protojsonx.
type Codec struct {
	MarshalOptions   protojsonx.MarshalOptions
	UnmarshalOptions protojsonx.UnmarshalOptions
}

var _ encoding.Codec = Codec{}

func (Codec) Name() string {
	return Name
}

func (c Codec) Marshal(v any) ([]byte, error) {
	msg, ok := v.(proto.Message)
	if !ok {
		return nil, fmt.Errorf("protojsonxgrpc: expected proto.Message, got %T", v)
	}
	return c.MarshalOptions.Marshal(msg)
}

func (c Codec) Unmarshal(data []byte, v any) error {
	msg, ok := v.(proto.Message)
	if !ok {
		return fmt.Errorf("protojsonxgrpc: expected proto.Message, got %T", v)
	}
	return c.UnmarshalOptions.Unmarshal(data, msg)
}

// Register registers a protojsonx JSON codec with gRPC-Go's global codec
// registry. Call it during process initialization before making JSON calls.
func Register(opts ...Option) {
	codec := Codec{}
	for _, opt := range opts {
		opt(&codec)
	}
	encoding.RegisterCodec(codec)
}

type Option func(*Codec)

func WithMarshalOptions(opts protojsonx.MarshalOptions) Option {
	return func(c *Codec) {
		c.MarshalOptions = opts
	}
}

func WithUnmarshalOptions(opts protojsonx.UnmarshalOptions) Option {
	return func(c *Codec) {
		c.UnmarshalOptions = opts
	}
}
