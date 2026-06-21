// Package protojsonxconnect provides a Connect codec backed by protojsonx.
package protojsonxconnect

import (
	"fmt"

	"connectrpc.com/connect"
	"github.com/sudorandom/protojsonx"
	"google.golang.org/protobuf/proto"
)

const Name = "json"

// Codec implements connectrpc.com/connect.Codec using protojsonx.
type Codec struct {
	MarshalOptions   protojsonx.MarshalOptions
	UnmarshalOptions protojsonx.UnmarshalOptions
}

var _ connect.Codec = (*Codec)(nil)

func (c *Codec) Name() string {
	return Name
}

func (c *Codec) Marshal(v any) ([]byte, error) {
	msg, ok := v.(proto.Message)
	if !ok {
		return nil, fmt.Errorf("protojsonxconnect: expected proto.Message, got %T", v)
	}
	return c.MarshalOptions.Marshal(msg)
}

func (c *Codec) Unmarshal(data []byte, v any) error {
	msg, ok := v.(proto.Message)
	if !ok {
		return fmt.Errorf("protojsonxconnect: expected proto.Message, got %T", v)
	}
	return c.UnmarshalOptions.Unmarshal(data, msg)
}
