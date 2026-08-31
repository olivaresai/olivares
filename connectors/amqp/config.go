// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package amqp

import (
	"crypto/tls"
	"fmt"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/connectors/internal/tlsx"
	"github.com/olivaresai/olivares/sdk"
)

// config is the resolved AMQP 1.0 connector configuration. The same struct serves
// the Source (observation receiver) and the Output (egress sender); a field only one
// of them uses is documented as such. Secrets (the SASL password) live here in
// memory only while the connector runs and are never logged or emitted (docs/SECURITY-HARDENING.md).
type config struct {
	// addr is the AMQP 1.0 endpoint URL, amqp[s]://host:port. amqps selects TLS and
	// the 5671 default port; amqp the 5672 default. One wire reaches both RabbitMQ 4.0
	// (native AMQP 1.0) and Azure Service Bus (see doc.go).
	addr string
	// namespaceRef labels the broker/namespace in emitted edges; defaults to the
	// host:port parsed from addr (never the credentials in a userinfo).
	namespaceRef string

	// --- Source (observation) ---
	// observationAddress is the DEDICATED observation queue/subscription the receiver
	// attaches to — a tee/mirror of the production traffic, NEVER the app's own queue
	// (settling a message here must not drain the application; see doc.go).
	observationAddress string

	// --- Output (egress) ---
	egressAddress string // queue/topic findings/evidence are sent to
	egressSource  string // CloudEvents source URI for produced events
	binaryEgress  bool   // send CloudEvents in binary content mode (default structured)

	// --- security (both) ---
	tls           *tls.Config // nil unless tls/mTLS configured (passed to the dialer)
	saslUser      string
	saslPass      string
	saslAnonymous bool // SASL ANONYMOUS (no credentials); otherwise PLAIN when a user is set

	// otelMessaging is the instrumentation gate (default off).
	otelMessaging bool

	timeout time.Duration
}

const defaultTimeout = 30 * time.Second

// descriptorFields declares every setting both connectors accept, so the engine can
// validate config and a UI can render a form without running the connector.
func descriptorFields() []sdk.ConfigField {
	return []sdk.ConfigField{
		{Key: "addr", Type: sdk.FieldString, Required: true, Description: "AMQP 1.0 endpoint URL amqp[s]://host:port. One wire reaches RabbitMQ 4.0 and Azure Service Bus."},
		{Key: "namespace_ref", Type: sdk.FieldString, Description: "Label for the broker/namespace in emitted edges (defaults to host:port from addr)."},
		{Key: "observation_address", Type: sdk.FieldString, Description: "DEDICATED observation queue/subscription to attach to (Source). MUST be a tee/mirror, never the app's production queue."},
		{Key: "egress_address", Type: sdk.FieldString, Description: "Queue/topic findings/evidence are sent to (Output)."},
		{Key: "egress_source", Type: sdk.FieldString, Default: "/olivares/olivares", Description: "CloudEvents source URI for produced events (Output)."},
		{Key: "binary_egress", Type: sdk.FieldBool, Default: "false", Description: "Send CloudEvents in binary content mode (Output); default structured."},
		{Key: "tls", Type: sdk.FieldBool, Default: "false", Description: "Force TLS even when addr is amqp:// (amqps:// already implies TLS)."},
		{Key: "tls_ca_file", Type: sdk.FieldString, Description: "PEM CA bundle to verify the broker."},
		{Key: "tls_cert_file", Type: sdk.FieldString, Description: "Client certificate for mTLS."},
		{Key: "tls_key_file", Type: sdk.FieldString, Description: "Client private key for mTLS."},
		{Key: "tls_insecure_skip_verify", Type: sdk.FieldBool, Default: "false", Description: "Skip broker certificate verification (NOT for production)."},
		{Key: "sasl_user", Type: sdk.FieldString, Description: "SASL PLAIN username (for Azure Service Bus use the SAS key name)."},
		{Key: "sasl_password", Type: sdk.FieldString, Secret: true, Description: "SASL PLAIN password / SAS key (reference)."},
		{Key: "sasl_anonymous", Type: sdk.FieldBool, Default: "false", Description: "Use SASL ANONYMOUS (no credentials)."},
		{Key: "otel_messaging", Type: sdk.FieldBool, Default: "false", Description: "Enable gated OTel messaging-semconv instrumentation (default off; semconv in Development)."},
		{Key: "timeout", Type: sdk.FieldDuration, Default: "30s", Description: "Per-operation timeout (dial, receive, send)."},
	}
}

// loadConfig resolves and validates configuration shared by both connectors. The
// required check (addr) surfaces here, before Gather/Notify, per the SDK contract.
// TLS material is loaded into a *tls.Config; the SASL secret is kept in memory only.
func loadConfig(cfg sdk.Config) (config, error) {
	c := config{
		addr:               strings.TrimSpace(cfg.Get("addr")),
		namespaceRef:       cfg.Get("namespace_ref"),
		observationAddress: cfg.Get("observation_address"),
		egressAddress:      cfg.Get("egress_address"),
		egressSource:       cfg.Get("egress_source"),
		binaryEgress:       cfg.GetBool("binary_egress", false),
		saslUser:           cfg.Get("sasl_user"),
		saslPass:           cfg.Get("sasl_password"),
		saslAnonymous:      cfg.GetBool("sasl_anonymous", false),
		otelMessaging:      cfg.GetBool("otel_messaging", false),
		timeout:            cfg.GetDuration("timeout", defaultTimeout),
	}
	if c.addr == "" {
		return config{}, fmt.Errorf("amqp: 'addr' is required")
	}
	if !strings.HasPrefix(c.addr, "amqp://") && !strings.HasPrefix(c.addr, "amqps://") && !strings.HasPrefix(c.addr, "amqp+ssl://") {
		return config{}, fmt.Errorf("amqp: 'addr' must be an amqp://, amqps:// or amqp+ssl:// URL")
	}
	if c.namespaceRef == "" {
		// Derive the namespace label from the endpoint, scrubbed of any userinfo
		// credentials (redact.SanitizeURL strips user:pass@ and query secrets).
		c.namespaceRef = redact.SanitizeURL(c.addr)
	}
	if c.egressSource == "" {
		c.egressSource = "/olivares/olivares"
	}
	tlsCfg, err := buildTLSConfig(cfg)
	if err != nil {
		return config{}, err
	}
	c.tls = tlsCfg
	return c, nil
}

// buildTLSConfig assembles a *tls.Config from the tls_* settings via the shared tlsx
// builder (secure default: verification on, TLS 1.2 floor; mTLS needs both cert and
// key), or returns nil when no TLS material is requested. amqps:// in addr still
// negotiates TLS with go-amqp's default config when this returns nil; the operator
// only needs tls_* to pin a CA or present a client certificate.
func buildTLSConfig(cfg sdk.Config) (*tls.Config, error) {
	return tlsx.Build(tlsx.Options{
		Enable:             cfg.GetBool("tls", false),
		CAFile:             cfg.Get("tls_ca_file"),
		CertFile:           cfg.Get("tls_cert_file"),
		KeyFile:            cfg.Get("tls_key_file"),
		InsecureSkipVerify: cfg.GetBool("tls_insecure_skip_verify", false),
	})
}
