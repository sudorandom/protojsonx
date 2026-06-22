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

Benchmarks run on an Apple M1 Pro (8 cores, Go 1.26.4), comparing standard `protojson`, standard binary protobuf wire format (`proto`), and `protojsonx`.

### Marshalling (Serialization)

| Implementation | Simple (ns/op) | Simple (allocs) | Complex (ns/op) | Complex (allocs) |
|---|---:|---:|---:|---:|
| `protojson` (Standard Lib) | 6,177 ns | 63 | 8,508 ns | 69 |
| `protojsonx` | **941 ns** | **1** | **1,564 ns** | **5** |
| `proto` (Binary Wire) | 1,532 ns | 13 | 1,354 ns | 9 |

### Unmarshalling (Deserialization)

| Implementation | Simple (ns/op) | Simple (allocs) | Complex (ns/op) | Complex (allocs) |
|---|---:|---:|---:|---:|
| `protojson` (Standard Lib) | 9,867 ns | 129 | 12,832 ns | 153 |
| `protojsonx` (Standard) | 2,817 ns | 38 | 4,109 ns | 41 |
| `protojsonx` (ZeroCopy) | 2,559 ns | 16 | 3,810 ns | 30 |
| `protojsonx` (Allocator) | 2,688 ns | 35 | 3,839 ns | 36 |
| `protojsonx` (ZeroCopy + Allocator) | 2,393 ns | **13** | 3,623 ns | **25** |
| `proto` (Binary Wire) | **2,249 ns** | 45 | **1,938 ns** | 33 |

### 🚀 Summary

- **Marshal is about 5.4-6.5x faster than `protojson`** with dramatically fewer allocations.
- **Unmarshal is about 3.3-4.1x faster than `protojson`**, depending on message shape and configured options.
- **Marshal is competitive with binary protobuf**, faster in the simple benchmark and roughly tied in the complex benchmark.
- **Allocations drop sharply**: complex unmarshal falls from **153 allocs/op** with `protojson` to **41 allocs/op** (Standard), **30 allocs/op** (ZeroCopy), **36 allocs/op** (Allocator), or **25 allocs/op** (ZeroCopy + Allocator).
- **No extra generated code or protoc plugin required**: `protojsonx` works with ordinary Go protobuf generated types.

The binary marshal comparison is message-shape dependent. In these benchmarks, `protojsonx` can beat binary protobuf marshal because the JSON encoder writes directly into a pooled byte buffer from precomputed field offsets, while binary protobuf still pays its own per-field encoding and allocation costs for these generated message shapes.

## How It Works

`protojsonx` keeps the same generated message structs you already have, but replaces protobuf reflection in the hot path with a runtime-compiled table.

- **Runtime table compilation**: the first use of a message type reads its protobuf descriptor and generated Go struct tags, then builds a `MessageTable` containing field offsets, JSON/proto names, field kinds, enum maps, and nested message tables.
- **Unsafe field access**: marshal and unmarshal read/write generated struct fields with precomputed `unsafe` offsets instead of reflective field lookup.
- **Specialized JSON parser**: unmarshal uses a small parser tailored to the supported protojson field shapes. It validates skipped unknown JSON values, rejects duplicate known fields, handles `null` as the protobuf default, and parses known numeric tokens without routing every field through `encoding/json`.
- **Low-allocation marshal path**: JSON is appended directly into a pooled byte buffer, with deterministic map-key sorting and one owned copy returned to the caller.
- **Optional zero-copy strings**: `UnmarshalOptions{ZeroCopy: true}` can alias unescaped input string bytes, avoiding string allocation when the input buffer lifetime is request-scoped.
- **Optional bump allocator**: nested messages can be allocated from a reusable monotonic allocator to reduce heap allocation and GC pressure in high-throughput decode paths.
- **Full protojson compatibility**: all standard features and Well-Known Types are supported natively. There are no runtime fallbacks to the standard `protojson` library.

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

## Compatibility

`protojsonx` is fully self-contained and does not import or fall back to the standard `protojson` library. All common request/response message shapes, enums, Well-Known Types, map configurations, and oneof constraints are natively optimized.

Optimized field and schema shapes:

- **Scalars**: `string`, numeric types, `bool`, `bytes`, and enums.
- **Nested messages** and recursive structures.
- **Repeated fields**: repeated strings, numbers, booleans, bytes, enums, and nested messages.
- **Map fields**: maps with keys and values of any scalar types, string-to-string maps, and string-to-message maps.
- **Oneof fields**: support for oneof selection, type validation, and conflicting/duplicate oneof key checks (excluding null values).
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
- `ZeroCopy bool`: alias unescaped input string bytes directly as Go strings.
- `Allocator Allocator`: a custom allocator to optimize allocation of nested submessage structures.

### Allocator Configuration

By default, Go's reflection API allocates nested submessages individually on the Go heap via `reflect.New`. In high-throughput pathways, this can lead to memory fragmentation and garbage collection pressure.

`protojsonx` provides a built-in pointer-stable, thread-local monotonic `BumpAllocator` out of the box.

#### Using the Built-in BumpAllocator

To use the built-in allocator, instantiate it, pass it to `UnmarshalOptions`, and call `Reset()` on the allocator to reuse its underlying buffers across requests/cycles:

```go
// Create or reuse an allocator (not thread-safe; reuse per-goroutine or via a pool)
alloc := protojsonx.NewBumpAllocator()

// Reset allocator buffers from any previous runs
alloc.Reset()

var out MyMessage
err := protojsonx.UnmarshalOptions{
	Allocator: alloc,
}.Unmarshal(data, &out)
```

> [!NOTE]
> Ensure that the lifetime of the `BumpAllocator` matches or outlives the decoded message. Only call `Reset()` when you are completely finished using the decoded structure.

#### Implementing a Custom Allocator

If you want to plug in your own memory management strategy (such as integrating with Go's experimental `arena` package or CGO-based allocators), you can implement the `Allocator` interface:

```go
type Allocator interface {
	New(t reflect.Type) reflect.Value
}
```


### ZeroCopy Caveats

When `ZeroCopy` is enabled, decoded string fields can point directly at the input JSON byte slice.

1. The input byte slice stays live as long as any decoded string references it.
2. Mutating or reusing the input buffer can mutate decoded strings.
3. Use it only for short-lived request-scoped data where the input buffer lifetime is clear.

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
			ZeroCopy:       true,
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

The generated binary at `.bin/protojsonx-conformance` speaks the official protobuf conformance runner protocol. Run it with the upstream `conformance_test_runner` from the Protocol Buffers source tree. The harness exercises JSON and protobuf input/output; text-format cases are reported as skipped because text format is outside `protojsonx`'s scope.

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

Generated Protocol Buffers conformance files retain their upstream BSD-style notices; see [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
