default:
    @just --list

# Run all Go tests in the repository
test:
    mise exec -- go test -v ./...
    cd protojsonxconnect && mise exec -- go test -v ./...
    cd protojsonxgrpc && mise exec -- go test -v ./...

# Run golangci-lint on the codebase
lint:
    mise exec -- golangci-lint run ./...

# Run the performance benchmark suite
bench:
    mise exec -- go test -bench=. -benchmem

# Regenerate internal protobuf fixtures used by tests and benchmarks.
generate-protos:
    mise exec -- buf generate --template buf.generate.yaml

# Keep go.mod/go.sum files synchronized for the root module and nested modules.
mod-tidy:
    mise exec -- go mod tidy
    cd protojsonxconnect && mise exec -- go mod tidy
    cd protojsonxgrpc && mise exec -- go mod tidy

# Run the unmarshal fuzz test (default fuzzing duration: 10s)
fuzz duration="10s":
    mise exec -- go test -fuzz=FuzzUnmarshal -fuzztime={{duration}} .
