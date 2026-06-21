default:
    @just --list

# Run all Go tests in the repository
test:
    go test -v ./...
    cd protojsonxconnect && go test -v ./...
    cd protojsonxgrpc && go test -v ./...

# Run golangci-lint on the codebase
lint:
    golangci-lint run ./...

# Run the performance benchmark suite
bench:
    go test -bench=. -benchmem

# Build the subprocess used by the official protobuf conformance runner.
conformance-binary:
    mkdir -p ./.bin
    GOCACHE="{{justfile_directory()}}/.bin/go-build-cache" go build -o ./.bin/protojsonx-conformance ./internal/protojsonx-conformance

# Regenerate internal protobuf fixtures used by tests and benchmarks.
generate-protos:
    buf generate --template buf.generate.yaml

# Keep go.mod/go.sum files synchronized for the root module and nested modules.
mod-tidy:
    go mod tidy
    cd protojsonxconnect && go mod tidy
    cd protojsonxgrpc && go mod tidy

# Run the unmarshal fuzz test (default fuzzing duration: 10s)
fuzz duration="10s":
    go test -fuzz=FuzzUnmarshal -fuzztime={{duration}} .
