# protojsonx Code Generation Plugin Design

This document sketches a `protoc` plugin that generates schema-specific protojson
marshal and unmarshal code for Go protobuf messages.

The current runtime remains the default path: it works with ordinary generated Go
protobuf types and requires no extra build step. The plugin is an optional fast
path for users who already own a `protoc` or `buf` pipeline and want lower decode
overhead than the dynamic table compiler can provide.

## Goals

- Preserve protojson semantics and the existing `protojsonx` option types.
- Generate code that avoids descriptor lookup, table compilation, reflective
  allocation, and repeated dynamic kind dispatch.
- Keep generated code memory-safe Go where practical. The runtime can continue
  to use `unsafe` internally, but generated access should prefer direct fields.
- Allow adoption per package without changing message definitions.
- Keep Well-Known Type and extension behavior compatible with the runtime.

## Non-Goals

- Replacing the dynamic runtime.
- Generating a full replacement for `protoc-gen-go`.
- Supporting dynamic messages that do not have generated Go struct types.
- Introducing custom lifetime-sensitive allocation or zero-copy APIs.

## Package Shape

The plugin binary should be named:

```sh
protoc-gen-go-protojsonx
```

With `buf`, users would add it beside `protoc-gen-go`:

```yaml
plugins:
  - local: protoc-gen-go
    out: .
    opt:
      - module=github.com/sudorandom/protojsonx
  - local: ["go", "run", "./cmd/protoc-gen-go-protojsonx"]
    out: .
    opt:
      - module=github.com/sudorandom/protojsonx
```

For `foo.proto`, the plugin writes:

```text
foo.protojsonx.pb.go
```

Generated files live in the same Go package as `foo.pb.go`.

## Generated API

For each supported message, generate methods:

```go
func (x *MyMessage) MarshalProtoJSONX() ([]byte, error)
func (x *MyMessage) UnmarshalProtoJSONX(data []byte) error
```

The initial generated methods call `github.com/sudorandom/protojsonx/protojsonxgen`.
That package is intentionally separate from the root `protojsonx` package so
generated test fixtures can be imported by the root package tests without an
import cycle.

A follow-up integration layer can probe for generated helpers through a registry:

```go
type GeneratedCodec struct {
	Marshal   func(proto.Message, protojsonx.MarshalOptions) ([]byte, error)
	Unmarshal func([]byte, proto.Message, protojsonx.UnmarshalOptions) error
}
```

Generated `init` functions can register handlers keyed by `protoreflect.FullName`
or concrete Go type. Then the public `protojsonx.MarshalOptions.Marshal` and
`UnmarshalOptions.Unmarshal` methods can optionally dispatch to generated code
when available.

## Marshal Strategy

Generated marshal code should mirror the current runtime encoder:

- Use a pooled byte buffer from the runtime.
- Emit fields in protobuf field-number order.
- Respect `EmitUnpopulated`.
- Respect `UseProtoNames`.
- Sort map keys deterministically.
- Encode enums as protojson names.
- Reuse runtime helpers for Well-Known Types and edge cases that are already
  hard to get right.

The first implementation should generate direct scalar, enum, repeated, map, and
nested-message encoding, while delegating Well-Known Types and extensions to
shared runtime helpers.

## Unmarshal Strategy

Generated unmarshal code should still use a small JSON cursor, but replace table
lookup and dynamic field assignment with direct switch cases:

```go
for dec.moreObjectFields() {
	name, err := dec.readObjectKey()
	if err != nil {
		return err
	}
	switch name {
	case "displayName", "display_name":
		v, err := dec.readString()
		if err != nil {
			return err
		}
		m.DisplayName = v
	case "status":
		v, err := readMyEnum(dec)
		if err != nil {
			return err
		}
		m.Status = v
	default:
		if !opts.DiscardUnknown {
			return protojsonx.UnknownFieldError(name)
		}
		if err := dec.skipValue(); err != nil {
			return err
		}
	}
}
```

For correctness, generated unmarshal must keep the same validation behavior as
the dynamic parser:

- Reject duplicate known fields.
- Treat `null` as clearing to the protobuf default for ordinary fields.
- Allow unknown fields only when `DiscardUnknown` is set, after validating the
  skipped JSON value.
- Accept both JSON names and proto names.
- Enforce oneof conflicts.
- Enforce protojson number parsing rules.

Duplicate detection can start with a generated bitset when field counts are low,
falling back to a small `map[int]struct{}` for very large messages.

## Runtime Support Needed

The plugin should not copy every parser and encoder helper into generated files.
Expose a small support surface from `protojsonxgen` instead:

- JSON cursor operations used by generated unmarshal.
- Scalar parse helpers with protojson-compatible errors.
- Scalar append helpers for marshal.
- Map key sorting helpers.
- Well-Known Type marshal/unmarshal helpers.
- Registration hooks for generated codecs, if generated dispatch from the root
  package is added later.

The current `protojsonxgen` implementation is only a fallback through standard
`protojson`. It exists to establish the generated API, Buf integration, and
benchmark split before moving optimized parser and encoder helpers behind that
package boundary.

## Suggested Milestones

1. Add `protojsonxgen` runtime helpers by moving existing parser and append
   helpers behind a narrow generated-code API.
2. Build `cmd/protoc-gen-go-protojsonx` with `protogen.Options`.
3. Generate marshal-only code for scalar fields and nested messages.
4. Add unmarshal for scalar fields with duplicate detection and unknown-field
   handling.
5. Add repeated fields, maps, enums, oneofs, and bytes.
6. Delegate Well-Known Types and extensions to shared runtime helpers.
7. Add registration-based dispatch from the public runtime.
8. Extend conformance tests to run both dynamic and generated paths.

## Initial Recommendation

Start with generated unmarshal for ordinary messages. That is where the dynamic
runtime still pays the most overhead: object-key dispatch, field-kind branching,
and reflective nested message allocation. Marshal is already very fast, so it can
follow once the plugin and conformance scaffolding are proven.
