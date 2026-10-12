// The public composition root: the single `olivares` binary. It wires the engine
// (/core) and the product modules (/modules); dependencies point inward, so /core
// does not import /modules or /connectors.
// It builds the store via the public core/engine seam, never the internal store.
module github.com/olivaresai/olivares/cmd/olivares

go 1.26.6

require (
	github.com/ProtonMail/go-crypto v1.5.2
	github.com/go-chi/chi/v5 v5.3.2
	github.com/go-jose/go-jose/v4 v4.1.5
	github.com/google/uuid v1.6.0
	github.com/hashicorp/go-plugin v1.8.0
	github.com/jackc/pgx/v5 v5.11.0
	github.com/mattn/go-isatty v0.0.24
	github.com/modelcontextprotocol/go-sdk v1.8.0
	github.com/olivaresai/olivares/clients/go v0.0.0
	github.com/olivaresai/olivares/connectors v0.0.0
	github.com/olivaresai/olivares/core v0.0.0
	github.com/olivaresai/olivares/modules v0.0.0
	github.com/olivaresai/olivares/sdk v0.0.0
	github.com/olivaresai/olivares/sdk/plugin v0.0.0
	github.com/olivaresai/olivares/sdk/scaffold v0.0.0
	github.com/spf13/cobra v1.10.2
	github.com/spf13/pflag v1.0.10
	golang.org/x/sys v0.49.0
	google.golang.org/grpc v1.84.0
	gopkg.in/yaml.v3 v3.0.1
	modernc.org/sqlite v1.60.1
)

require (
	github.com/DataDog/datadog-go v4.5.1+incompatible // indirect
	github.com/armon/go-proxyproto v0.0.0-20170620220930-48572f11356f // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/boombuler/barcode v1.1.0 // indirect
	github.com/dlclark/regexp2 v1.11.5 // indirect
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/hashicorp/golang-lru v1.0.2 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/patrickmn/go-cache v2.1.0+incompatible // indirect
	github.com/prometheus/client_golang v1.24.1 // indirect
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/common v0.70.1 // indirect
	github.com/prometheus/procfs v0.21.1 // indirect
	github.com/pydantic/genai-prices/packages/go v0.1.9 // indirect
	github.com/rs/xid v1.2.1 // indirect
	github.com/russellhaering/gosaml2 v0.12.0 // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/sirupsen/logrus v1.9.4 // indirect
	github.com/stripe/goproxy v0.0.0-20260826103935-0cd07c409a76 // indirect
	github.com/stripe/smokescreen v0.1.0 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	go.opentelemetry.io/otel/log v1.47.0 // indirect
	go.opentelemetry.io/proto/otlp v1.11.1 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
	gopkg.in/yaml.v2 v2.4.0 // indirect
)

require (
	github.com/Azure/go-ntlmssp v0.1.1 // indirect
	github.com/BurntSushi/toml v1.6.0 // indirect
	github.com/Microsoft/go-winio v0.6.2 // indirect
	github.com/beevik/etree v1.8.1 // indirect
	github.com/cedar-policy/cedar-go v1.8.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/cloudflare/circl v1.6.3 // indirect
	github.com/coreos/go-oidc/v3 v3.21.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/fatih/color v1.19.0 // indirect
	github.com/fxamacker/cbor/v2 v2.9.6 // indirect
	github.com/go-asn1-ber/asn1-ber v1.5.8 // indirect
	github.com/go-ldap/ldap/v3 v3.4.15 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/go-webauthn/webauthn v0.18.2 // indirect
	github.com/go-webauthn/x v0.3.1 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/google/go-tpm v0.9.8 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.31.0 // indirect
	github.com/hashicorp/go-hclog v1.6.3 // indirect
	github.com/hashicorp/yamux v0.1.2 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/jonboulle/clockwork v0.5.0 // indirect
	github.com/klauspost/compress v1.20.1
	github.com/mattermost/xml-roundtrip-validator v0.1.0 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/oklog/run v1.1.0 // indirect
	github.com/philhofer/fwd v1.2.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/russellhaering/goxmldsig v1.6.1 // indirect
	github.com/spiffe/go-spiffe/v2 v2.9.0 // indirect
	github.com/tinylib/msgp v1.6.4 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel v1.47.0
	go.opentelemetry.io/otel/metric v1.47.0 // indirect
	go.opentelemetry.io/otel/sdk v1.47.0 // indirect
	go.opentelemetry.io/otel/sdk/metric v1.47.0 // indirect
	go.opentelemetry.io/otel/trace v1.47.0
	golang.org/x/crypto v0.57.0
	golang.org/x/exp v0.0.0-20260908205506-85c1c2202aba // indirect
	golang.org/x/mod v0.41.0 // indirect
	golang.org/x/net v0.60.0 // indirect
	golang.org/x/oauth2 v0.37.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	golang.org/x/time v0.16.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260928230214-8a89bd6388cc // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260928230214-8a89bd6388cc // indirect
	modernc.org/libc v1.77.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)

replace github.com/olivaresai/olivares/core => ../../core

replace github.com/olivaresai/olivares/modules => ../../modules

replace github.com/olivaresai/olivares/connectors => ../../connectors

replace github.com/olivaresai/olivares/sdk => ../../sdk

replace github.com/olivaresai/olivares/sdk/plugin => ../../sdk/plugin

replace github.com/olivaresai/olivares/sdk/scaffold => ../../sdk/scaffold

replace github.com/olivaresai/olivares/clients/go => ../../clients/go
