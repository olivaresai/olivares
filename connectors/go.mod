// Capture/output connectors (Apache-2.0). A connector imports only from ./sdk
// (and, to ship as a plugin, ./sdk/plugin), NEVER from ./core — enforced by
// scripts/check-boundary.sh.
module github.com/olivaresai/olivares/connectors

go 1.26.5

require (
	github.com/Azure/go-amqp v1.7.0
	github.com/BurntSushi/toml v1.6.0
	github.com/beevik/etree v1.7.0
	github.com/cilium/cilium v1.19.6
	github.com/envoyproxy/go-control-plane/envoy v1.37.0
	github.com/go-jose/go-jose/v4 v4.1.4
	github.com/go-ldap/ldap/v3 v3.4.14
	github.com/gosnmp/gosnmp v1.44.0
	github.com/jackc/pgx/v5 v5.10.0
	github.com/olivaresai/olivares/sdk v0.0.0
	github.com/olivaresai/olivares/sdk/plugin v0.0.0
	github.com/russellhaering/goxmldsig v1.6.0
	github.com/spiffe/go-spiffe/v2 v2.8.1
	github.com/stretchr/testify v1.11.1
	github.com/twmb/franz-go v1.21.5
	github.com/twmb/franz-go/pkg/kmsg v1.13.1
	go.opentelemetry.io/proto/otlp v1.10.0
	golang.org/x/sys v0.47.0
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa
	google.golang.org/grpc v1.82.1
	google.golang.org/protobuf v1.36.12-0.20260120151049-f2248ac996af
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/Azure/go-ntlmssp v0.1.1 // indirect
	github.com/Microsoft/go-winio v0.6.2 // indirect
	github.com/cncf/xds/go v0.0.0-20260202195803-dba9d589def2 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/envoyproxy/protoc-gen-validate v1.3.3 // indirect
	github.com/fatih/color v1.18.0 // indirect
	github.com/go-asn1-ber/asn1-ber v1.5.8 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.29.0 // indirect
	github.com/hashicorp/go-hclog v1.6.3 // indirect
	github.com/hashicorp/go-plugin v1.8.0 // indirect
	github.com/hashicorp/yamux v0.1.2 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/jonboulle/clockwork v0.5.0 // indirect
	github.com/klauspost/compress v1.18.6 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/oklog/run v1.1.0 // indirect
	github.com/pierrec/lz4/v4 v4.1.26 // indirect
	github.com/planetscale/vtprotobuf v0.6.1-0.20240319094008-0393e58bdf10 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/rogpeppe/go-internal v1.15.0 // indirect
	go.opentelemetry.io/otel/metric v1.44.0 // indirect
	go.opentelemetry.io/otel/sdk v1.44.0 // indirect
	go.opentelemetry.io/otel/trace v1.44.0 // indirect
	golang.org/x/crypto v0.54.0 // indirect
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260526163538-3dc84a4a5aaa // indirect
)

replace github.com/olivaresai/olivares/sdk => ../sdk

replace github.com/olivaresai/olivares/sdk/plugin => ../sdk/plugin
