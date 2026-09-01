// Engine / control plane core (AGPL-3.0-only). The single `olivares`
// binary builds from core/cmd/olivares; cobra powers the growing CLI.
module github.com/olivaresai/olivares/core

go 1.26.5

require (
	github.com/go-chi/chi/v5 v5.3.2
	github.com/go-jose/go-jose/v4 v4.1.4
	github.com/jackc/pgx/v5 v5.10.0
	golang.org/x/crypto v0.55.0
	golang.org/x/net v0.58.0
	google.golang.org/grpc v1.83.2
	google.golang.org/protobuf v1.36.12
	modernc.org/sqlite v1.57.0
)

require (
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/fatih/color v1.19.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/go-webauthn/x v0.3.0 // indirect
	github.com/golang-jwt/jwt/v4 v4.5.2 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/google/go-tpm v0.9.8 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.30.0 // indirect
	github.com/hashicorp/go-hclog v1.6.3 // indirect
	github.com/hashicorp/yamux v0.1.2 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/jonboulle/clockwork v0.5.0 // indirect
	github.com/klauspost/compress v1.19.2 // indirect
	github.com/mattermost/xml-roundtrip-validator v0.1.0 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/nats-io/nkeys v0.4.16 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/oklog/run v1.1.0 // indirect
	github.com/philhofer/fwd v1.2.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/tinylib/msgp v1.6.4 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/proto/otlp v1.11.0
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0
	golang.org/x/text v0.41.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260819154853-08b0e4226688 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260819154853-08b0e4226688 // indirect
	modernc.org/libc v1.74.4 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)

require (
	github.com/beevik/etree v1.7.1
	github.com/coreos/go-oidc/v3 v3.20.0
	github.com/crewjam/saml v0.5.1
	github.com/fxamacker/cbor/v2 v2.9.3
	github.com/go-webauthn/webauthn v0.18.0
	github.com/google/uuid v1.6.0
	github.com/hashicorp/go-plugin v1.8.0
	github.com/nats-io/nats.go v1.53.1
	github.com/olivaresai/olivares/modules v0.0.0-20260901063010-63780b2680dd
	github.com/olivaresai/olivares/sdk v0.0.0
	github.com/olivaresai/olivares/sdk/plugin v0.0.0
	github.com/russellhaering/goxmldsig v1.6.1
	go.opentelemetry.io/otel v1.46.0
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc v1.46.0
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp v1.46.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.46.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.46.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp v1.46.0
	go.opentelemetry.io/otel/metric v1.46.0
	go.opentelemetry.io/otel/sdk v1.46.0
	go.opentelemetry.io/otel/sdk/metric v1.46.0
	go.opentelemetry.io/otel/trace v1.46.0
	golang.org/x/oauth2 v0.36.0
)

replace github.com/olivaresai/olivares/sdk => ../sdk

// sdk/plugin is a separate module (the gRPC/go-plugin transport); the engine is
// the plugin host, so core depends on it directly.
replace github.com/olivaresai/olivares/sdk/plugin => ../sdk/plugin
