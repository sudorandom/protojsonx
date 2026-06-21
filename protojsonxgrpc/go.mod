module github.com/sudorandom/protojsonx/protojsonxgrpc

go 1.25.0

require (
	github.com/sudorandom/protojsonx v0.0.0
	google.golang.org/grpc v1.81.1
	google.golang.org/protobuf v1.36.11
)

require golang.org/x/sys v0.42.0 // indirect

replace github.com/sudorandom/protojsonx => ../
