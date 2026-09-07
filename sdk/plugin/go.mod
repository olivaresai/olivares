// Connector/Module plugin transport (Apache-2.0): the versioned gRPC/protobuf
// wire contract plus the hashicorp/go-plugin glue that lets a connector or
// module ship as a separate process. It is a SEPARATE module from ./sdk on
// purpose: the gRPC dependency tree is opt-in and reaches an author's go.sum
// only if they build a plugin. Like ./sdk it imports only ./sdk, never ./core.
module github.com/olivaresai/olivares/sdk/plugin

go 1.26.5

require (
	github.com/hashicorp/go-plugin v1.8.0
	github.com/olivaresai/olivares/sdk v0.0.0-00010101000000-000000000000
	google.golang.org/grpc v1.83.2
	google.golang.org/protobuf v1.36.12
)

require (
	github.com/fatih/color v1.19.0 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/hashicorp/go-hclog v1.6.3 // indirect
	github.com/hashicorp/yamux v0.1.2 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/oklog/run v1.1.0 // indirect
	github.com/stretchr/testify v1.12.1 // indirect
	go.opentelemetry.io/otel v1.46.0 // indirect
	go.opentelemetry.io/otel/sdk/metric v1.46.0 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260819154853-08b0e4226688 // indirect
)

replace github.com/olivaresai/olivares/sdk => ../
