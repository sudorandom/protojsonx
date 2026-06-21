# protojsonx

[![CI Status](https://github.com/sudorandom/protojsonx/actions/workflows/ci.yml/badge.svg)](https://github.com/sudorandom/protojsonx/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/sudorandom/protojsonx.svg)](https://pkg.go.dev/github.com/sudorandom/protojsonx)
[![GitHub Release](https://img.shields.io/github/v/release/sudorandom/protojsonx)](https://github.com/sudorandom/protojsonx/releases)

`protojsonx` is a high-performance alternative to the standard Go protobuf JSON library (`google.golang.org/protobuf/encoding/protojson`).

It uses a dynamic table-driven parser and unsafe pointer offset arithmetic to avoid the runtime protobuf reflection overhead in hot marshal/unmarshal paths.

> [!WARNING]
> `protojsonx` is a super experimental project and is not intended for production use.

Requires Go 1.24 or newer.

## ⚡ Performance

Benchmarks run on an Apple M1 Pro (8 cores, Go 1.26.4), comparing standard `protojson`, standard binary protobuf wire format (`proto`), and `protojsonx`.

### Marshalling (Serialization)

| Implementation | Simple (ns/op) | Simple (allocs) | Complex (ns/op) | Complex (allocs) |
|---|---:|---:|---:|---:|
| `protojson` (Standard Lib) | 4,090 ns | 63 | 5,675 ns | 69 |
| `protojsonx` | **662 ns** | **1** | **917 ns** | **3** |
| `proto` (Binary Wire) | 1,101 ns | 13 | 980 ns | 9 |

### Unmarshalling (Deserialization)

| Implementation | Simple (ns/op) | Simple (allocs) | Complex (ns/op) | Complex (allocs) |
|---|---:|---:|---:|---:|
| `protojson` (Standard Lib) | 7,203 ns | 129 | 9,339 ns | 153 |
| `protojsonx` (Standard) | 1,989 ns | 35 | 2,641 ns | 28 |
| `protojsonx` (ZeroCopy) | 1,804 ns | 13 | 2,722 ns | 17 |
| `protojsonx` (Allocator) | 1,914 ns | 32 | 2,509 ns | 23 |
| `protojsonx` (ZeroCopy + Allocator) | 1,731 ns | **10** | 2,427 ns | **12** |
| `proto` (Binary Wire) | **1,516 ns** | 45 | **1,324 ns** | 33 |

### 🚀 Summary

- **Marshal is about 6x faster than `protojson`** with dramatically fewer allocations.
- **Unmarshal is about 3.4-4.2x faster than `protojson`**, depending on message shape and configured options.
- **Marshal is competitive with binary protobuf**, and is faster than binary marshal for both benchmark shapes.
- **Allocations drop sharply**: complex unmarshal falls from **153 allocs/op** with `protojson` to **28 allocs/op** (Standard), **17 allocs/op** (ZeroCopy), **23 allocs/op** (Allocator), or **12 allocs/op** (ZeroCopy + Allocator).
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
- **Full protojson compatibility**: schemas outside the optimized fast path fall back to the standard `protojson` implementation instead of producing lossy JSON.

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

`protojsonx` supports full protojson compatibility. Common request/response message shapes use the optimized runtime table path; schemas outside that fast path automatically fall back to the standard `protojson` implementation.

Optimized fast-path field shapes:

- Scalar fields: `string`, numeric types, `bool`, `bytes`, and enums.
- Nested message fields.
- Repeated `string` fields.
- Repeated message fields.
- `map<string, string>` fields.
- Both JSON camelCase names and proto snake_case names during unmarshal.
- Well-Known Types with protojson-compatible JSON: `google.protobuf.Timestamp`, `google.protobuf.Duration`, `google.protobuf.Any`, `google.protobuf.FieldMask`, and wrapper types such as `google.protobuf.StringValue`.
- `google.protobuf.Empty`.

Fallback-compatible field shapes:

- `oneof` fields.
- Repeated scalar fields other than `repeated string`.
- Maps other than `map<string, string>`.
- `google.protobuf.Struct`, `google.protobuf.Value`, and `google.protobuf.ListValue`.
- Message schemas that rely on protojson special cases outside the Well-Known Types listed above.

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
