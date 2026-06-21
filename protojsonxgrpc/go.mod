module github.com/sudorandom/protojsonx/protojsonxgrpc

go 1.24

require (
	github.com/sudorandom/protojsonx v0.0.0
	google.golang.org/grpc v1.64.0
	google.golang.org/protobuf v1.36.11
)

require golang.org/x/sys v0.18.0 // indirect

replace github.com/sudorandom/protojsonx => ../
