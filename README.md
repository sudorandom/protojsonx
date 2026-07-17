# protojsonx

[![CI Status](https://github.com/sudorandom/protojsonx/actions/workflows/ci.yml/badge.svg)](https://github.com/sudorandom/protojsonx/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/sudorandom/protojsonx.svg)](https://pkg.go.dev/github.com/sudorandom/protojsonx)
[![GitHub Release](https://img.shields.io/github/v/release/sudorandom/protojsonx)](https://github.com/sudorandom/protojsonx/releases)

`protojsonx` is a high-performance alternative to the standard Go protobuf JSON library (`google.golang.org/protobuf/encoding/protojson`).

It uses a dynamic table-driven parser and unsafe pointer offset arithmetic to avoid the runtime protobuf reflection overhead in hot marshal/unmarshal paths. The library is fully self-contained and passes 100% of the official protobuf conformance tests for JSON.

> [!WARNING]
> `protojsonx` is a super experimental project and is not intended for production use.

Requires Go 1.24 or newer.

## ⚡ Performance

Benchmarks run on an Apple M1 Pro (8 cores, Go 1.26.4), comparing standard `protojson`, standard binary protobuf wire format (`proto`), and `protojsonx` (using the generated plugin delegate).

### Marshalling (Serialization)

| Implementation | Simple (ns/op) | Simple (allocs) | Complex (ns/op) | Complex (allocs) |
|---|---:|---:|---:|---:|
| `protojson` (Standard Lib) | 4,746 ns | 63 | 6,225 ns | 69 |
| `protojsonx` | **689 ns** | **1** | **1,060 ns** | **5** |
| `proto` (Binary Wire) | 1,100 ns | 13 | 1,017 ns | 9 |

### Unmarshalling (Deserialization)

| Implementation | Simple (ns/op) | Simple (allocs) | Complex (ns/op) | Complex (allocs) |
|---|---:|---:|---:|---:|
| `protojson` (Standard Lib) | 7,629 ns | 129 | 9,758 ns | 153 |
| `protojsonx` | **1,353 ns** | **28** | **1,562 ns** | **25** |
| `proto` (Binary Wire) | 1,705 ns | 45 | 1,481 ns | 33 |

### 🚀 Summary

- **Marshal is about 5.8-6.9x faster than `protojson`** with dramatically fewer allocations.
- **Unmarshal is about 5.6-6.2x faster than `protojson`**, depending on message shape.
- **Marshal and Unmarshal are fully competitive with binary protobuf**, outperforming it in simple scenarios and matching it closely in complex scenarios.
- **Allocations drop sharply**: complex unmarshal falls from **153 allocs/op** with `protojson` to **25 allocs/op** with `protojsonx`.
- **Automatic plugin delegation**: `protojsonx` works out-of-the-box using reflection-free code-generated paths if generated with our `protoc` plugin, falling back gracefully to the table-driven reflection engine otherwise.

## How It Works

`protojsonx` keeps the same generated message structs you already have, but replaces protobuf reflection in the hot path with a runtime-compiled table or generated serialization code.

- **Code Generation Plugin (`protoc-gen-go-protojsonx`)**: Generates type-specific code for the fastest possible path, completely avoiding struct-tag parsing, table lookup, and type assertions.
- **Runtime table compilation**: The first use of a message type reads its protobuf descriptor and generated Go struct tags, then builds a `MessageTable` containing field offsets, JSON/proto names, field kinds, enum maps, and nested message tables.
- **Unsafe field access**: Both generated code and the reflection runtime read/write generated struct fields with precomputed `unsafe` offsets instead of reflective field lookup.
- **Specialized JSON parser**: Unmarshal uses a small parser tailored to the supported protojson field shapes. It validates skipped unknown JSON values, rejects duplicate known fields, handles `null` as the protobuf default, and parses known numeric tokens without routing every field through `encoding/json`.
- **Low-allocation marshal path**: JSON is appended directly into a pooled byte buffer, with deterministic map-key sorting and one owned copy returned to the caller.
- **Full protojson compatibility**: All standard features and Well-Known Types are supported natively. If generated code is not found for a type, the library automatically falls back to the table-driven reflection engine at runtime.

## Install

```sh
go get github.com/sudorandom/protojsonx
```

## Quick Start

```go
data, err := protojsonx.Marshal(msg)

var out mypb.MyMessage
err = protojsonx.Unmarshal(data, &out)
```

Optional integration modules:

```sh
go get github.com/sudorandom/protojsonx/protojsonxconnect
go get github.com/sudorandom/protojsonx/protojsonxgrpc
```

## 🛠️ Code Generation Plugin

`protojsonx` provides a `protoc` plugin that generates type-specific serialization and deserialization methods. Generating code completely avoids runtime reflection, table lookups, and dynamic type assertions, achieving the absolute maximum speed.

### Installation

Install the plugin via `go install`:

```sh
go install github.com/sudorandom/protojsonx/cmd/protoc-gen-go-protojsonx@latest
```

### Usage with `buf`

Add the plugin to your `buf.gen.yaml` config:

```yaml
version: v2
plugins:
  - local: protoc-gen-go
    out: .
    opt:
      - module=your-go-module-name
  - local: protoc-gen-go-protojsonx
    out: .
    opt:
      - module=your-go-module-name
```

### Usage with `protoc`

Run the plugin alongside `protoc-gen-go`:

```sh
protoc --go_out=. --go-protojsonx_out=. path/to/file.proto
```

### Generated Methods

The plugin generates a `.protojsonx.pb.go` file next to each `.pb.go` file. It exposes the following methods:

- `func (x *MyMessage) MarshalProtoJSONX() ([]byte, error)`
- `func (x *MyMessage) UnmarshalProtoJSONX(data []byte) error`
- `func (x *MyMessage) UnmarshalProtoJSONXWithOptions(data []byte, discardUnknown bool) error`

The main `protojsonx.Marshal` and `protojsonx.Unmarshal` methods automatically detect these generated methods and delegate to them directly, meaning no code changes are required in your application!

## Compatibility

`protojsonx` is fully self-contained and does not import or fall back to the standard `protojson` library. All common request/response message shapes, enums, Well-Known Types, map configurations, and oneof constraints are natively optimized.

Optimized field and schema shapes:

- **Scalars**: `string`, numeric types, `bool`, `bytes`, and enums.
- **Nested messages** and recursive structures.
- **Repeated fields**: repeated strings, numbers, booleans, bytes, enums, and nested messages.
- **Map fields**: maps with keys and values of any scalar types, string-to-string maps, and string-to-message maps.
- **Oneof fields**: full support for both standard `oneof` choice selections and synthetic `oneof` fields (proto3 `optional` pointer-scalars).
- **Extensions**: dynamic protobuf extensions registered via `protoregistry.GlobalTypes` are supported and serialized/deserialized natively.
- **Well-Known Types**: `google.protobuf.Timestamp`, `google.protobuf.Duration`, `google.protobuf.Any`, `google.protobuf.FieldMask`, `google.protobuf.Struct`, `google.protobuf.Value`, `google.protobuf.ListValue`, `google.protobuf.Empty`, and all wrapper types (e.g. `google.protobuf.StringValue`).
- **Naming**: Supports both JSON `camelCase` names and protobuf `snake_case` names during unmarshalling.

Non-optimized cases:

- **Dynamic Messages**: Messages that do not map to generated concrete Go struct types are not supported.

## Configuration

### MarshalOptions

- `EmitUnpopulated bool`: render fields with zero/default values.
- `UseProtoNames bool`: use proto snake_case names instead of JSON camelCase names.

### UnmarshalOptions

- `DiscardUnknown bool`: ignore unknown keys after validating their JSON value.

## ConnectRPC

Use `github.com/sudorandom/protojsonx/protojsonxconnect` with `connect.WithCodec`.

```go
package server

import (
	"net/http"

	"connectrpc.com/connect"
	"github.com/sudorandom/protojsonx"
	"github.com/sudorandom/protojsonx/protojsonxconnect"
)

func Handler() http.Handler {
	codec := &protojsonxconnect.Codec{
		UnmarshalOptions: protojsonx.UnmarshalOptions{
			DiscardUnknown: true,
		},
	}

	path, handler := userv1connect.NewUserServiceHandler(
		&UserServiceServer{},
		connect.WithCodec(codec),
	)

	mux := http.NewServeMux()
	mux.Handle(path, handler)
	return mux
}
```

See `protojsonxconnect/codec_test.go` for a complete runnable example test.

## gRPC-Go

Use `github.com/sudorandom/protojsonx/protojsonxgrpc` to register a `json` content subtype codec.

```go
package server

import (
	"github.com/sudorandom/protojsonx"
	"github.com/sudorandom/protojsonx/protojsonxgrpc"
)

func init() {
	protojsonxgrpc.Register(
		protojsonxgrpc.WithUnmarshalOptions(protojsonx.UnmarshalOptions{
			DiscardUnknown: true,
		}),
	)
}
```

Then request the JSON subtype on calls that should use the codec:

```go
err := conn.Invoke(
	ctx,
	"/user.UserService/GetUserProfile",
	req,
	resp,
	grpc.CallContentSubtype(protojsonxgrpc.Name),
)
```

See `protojsonxgrpc/codec_test.go` for a complete runnable example test.

## Development

Run tests across the root and codec modules:

```sh
just test
```

Keep all module files up to date:

```sh
just mod-tidy
```

Regenerate internal protobuf fixtures used by tests and benchmarks:

```sh
just generate-protos
```

Run benchmarks:

```sh
just bench
```

Build the protobuf conformance subprocess:

```sh
just conformance-binary
```

Build the generated-plugin dispatch variant of the conformance subprocess:

```sh
just plugin-conformance-binary
```

The generated binary at `.bin/protojsonx-conformance` speaks the official protobuf conformance runner protocol. Run it with the upstream `conformance_test_runner` from the Protocol Buffers source tree. The harness exercises JSON and protobuf input/output; text-format cases are reported as skipped because text format is outside `protojsonx`'s scope.

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

Generated Protocol Buffers conformance files retain their upstream BSD-style notices; see [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
