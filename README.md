# protojsonx

`protojsonx` is a high-performance alternative to the standard Go protobuf JSON library (`google.golang.org/protobuf/encoding/protojson`).

It uses a dynamic table-driven parser and unsafe pointer offset arithmetic to avoid the runtime protobuf reflection overhead in hot marshal/unmarshal paths.

## ⚡ Performance

Benchmarks run on an Apple M1 Pro (8 cores, Go 1.26.4), comparing standard `protojson`, standard binary protobuf wire format (`proto`), and `protojsonx`.

| Case | protojson | protojsonx | protojsonx ZeroCopy | proto binary |
|---|---:|---:|---:|---:|
| Simple marshal | 5305 ns/op, 63 allocs | 853 ns/op, 1 alloc | n/a | 1462 ns/op, 13 allocs |
| Simple unmarshal | 9236 ns/op, 129 allocs | 2486 ns/op, 35 allocs | 2282 ns/op, 13 allocs | 1988 ns/op, 45 allocs |
| Complex marshal | 7378 ns/op, 69 allocs | 1195 ns/op, 3 allocs | n/a | 1326 ns/op, 9 allocs |
| Complex unmarshal | 12149 ns/op, 153 allocs | 3470 ns/op, 28 allocs | 3285 ns/op, 17 allocs | 1713 ns/op, 33 allocs |

### 🚀 Summary

- **Marshal is about 6x faster than `protojson`** with dramatically fewer allocations.
- **Unmarshal is about 3.5-4x faster than `protojson`**, depending on whether `ZeroCopy` is enabled.
- **Marshal is competitive with binary protobuf**, and is faster than binary marshal for the complex benchmark shape.
- **Allocations drop sharply**: complex unmarshal falls from **153 allocs/op** with `protojson` to **28 allocs/op**, or **17 allocs/op** with `ZeroCopy`.

## Install

```sh
go get github.com/sudorandom/protojsonx
```

Optional integration modules:

```sh
go get github.com/sudorandom/protojsonx/protojsonxconnect
go get github.com/sudorandom/protojsonx/protojsonxgrpc
```

## Configuration

### MarshalOptions

- `EmitUnpopulated bool`: render fields with zero/default values.
- `UseProtoNames bool`: use proto snake_case names instead of JSON camelCase names.

### UnmarshalOptions

- `DiscardUnknown bool`: ignore unknown keys after validating their JSON value.
- `ZeroCopy bool`: alias unescaped input string bytes directly as Go strings.

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

Run benchmarks:

```sh
just bench
```
