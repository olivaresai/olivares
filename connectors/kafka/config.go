// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package kafka

import (
	"crypto/tls"
	"fmt"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/kafkawire"
	"github.com/olivaresai/olivares/connectors/internal/tlsx"
	"github.com/olivaresai/olivares/sdk"
)

// wireConfig projects the connector config onto the shared kafkawire connection
// config (brokers, group, topics, TLS, SASL).
func (c config) wireConfig() kafkawire.Config {
	return kafkawire.Config{
		Brokers:    c.brokers,
		ClusterRef: c.clusterRef,
		Group:      c.group,
		Topics:     c.topics,
		TLS:        c.tls,
		SASLMech:   c.saslMech,
		SASLUser:   c.saslUser,
		SASLPass:   c.saslPass,
	}
}

// config is the resolved Kafka connector configuration. The same struct serves the
// Source (consumer-group observation) and the Output (egress producer); a field that
// only one of them uses is documented as such. Secrets (SASL password, registry
// credential) live here in memory only while the connector runs and are never
// logged or emitted (docs/SECURITY-HARDENING.md).
type config struct {
	// brokers is the bootstrap broker list (one connector wire reaches Apache Kafka,
	// Confluent, Redpanda, MSK and Azure Event Hubs' Kafka endpoint — see doc.go).
	brokers []string
	// clusterRef labels the cluster in emitted edges; defaults to the first broker.
	clusterRef string

	// --- Source (consumer-group) ---
	topics []string // topics to observe
	group  string   // consumer group id
	// topologyScan additionally emits a one-shot cluster/topic/consumer-group
	// topology snapshot at Gather start (admin metadata, read-only). Default true.
	topologyScan bool

	// --- Output (egress) ---
	egressTopic  string // topic findings/evidence are produced to
	egressSource string // CloudEvents source URI for produced events
	binaryEgress bool   // produce CloudEvents in binary content mode (default structured)

	// --- security (both) ---
	tls      *tls.Config // nil unless tls/mTLS configured
	saslMech string      // "", "plain", "scram-sha-256", "scram-sha-512"
	saslUser string
	saslPass string

	// --- schema registry (Source) ---
	registryURL        string
	registryUser       string
	registryPass       string
	registryGUIDHeader string // operator-known header key carrying a schema GUID (optional)

	// otelMessaging is the instrumentation gate (default off).
	otelMessaging bool

	timeout time.Duration
}

const (
	defaultTimeout = 30 * time.Second
)

// descriptorFields declares every setting both connectors accept, so the engine can
// validate config and a UI can render a form without running the connector.
func descriptorFields() []sdk.ConfigField {
	return []sdk.ConfigField{
		{Key: "brokers", Type: sdk.FieldString, Required: true, Description: "Comma-separated bootstrap brokers (host:port). One wire reaches Kafka 4.0/Confluent/Redpanda/MSK/Event Hubs."},
		{Key: "cluster_ref", Type: sdk.FieldString, Description: "Label for the cluster in emitted edges (defaults to the first broker)."},
		{Key: "topics", Type: sdk.FieldString, Description: "Comma-separated topics to observe (Source)."},
		{Key: "group", Type: sdk.FieldString, Description: "Consumer group id (Source). Defaults to olivares-observer."},
		{Key: "topology_scan", Type: sdk.FieldBool, Default: "true", Description: "Emit a one-shot cluster/topic/consumer-group topology snapshot at start (Source)."},
		{Key: "egress_topic", Type: sdk.FieldString, Description: "Topic findings/evidence are produced to (Output)."},
		{Key: "egress_source", Type: sdk.FieldString, Default: "/olivares/olivares", Description: "CloudEvents source URI for produced events (Output)."},
		{Key: "binary_egress", Type: sdk.FieldBool, Default: "false", Description: "Produce CloudEvents in binary content mode (Output); default structured."},
		{Key: "tls", Type: sdk.FieldBool, Default: "false", Description: "Enable TLS to the brokers."},
		{Key: "tls_ca_file", Type: sdk.FieldString, Description: "PEM CA bundle to verify the brokers."},
		{Key: "tls_cert_file", Type: sdk.FieldString, Description: "Client certificate for mTLS."},
		{Key: "tls_key_file", Type: sdk.FieldString, Description: "Client private key for mTLS."},
		{Key: "tls_insecure_skip_verify", Type: sdk.FieldBool, Default: "false", Description: "Skip broker certificate verification (NOT for production)."},
		{Key: "sasl_mechanism", Type: sdk.FieldString, Description: "SASL mechanism: plain, scram-sha-256 or scram-sha-512."},
		{Key: "sasl_user", Type: sdk.FieldString, Description: "SASL username (for Event Hubs use $ConnectionString)."},
		{Key: "sasl_password", Type: sdk.FieldString, Secret: true, Description: "SASL password (reference)."},
		{Key: "schema_registry_url", Type: sdk.FieldString, Description: "Schema Registry base URL (Confluent/Redpanda; read-only)."},
		{Key: "schema_registry_user", Type: sdk.FieldString, Description: "Schema Registry basic-auth user."},
		{Key: "schema_registry_password", Type: sdk.FieldString, Secret: true, Description: "Schema Registry basic-auth password (reference)."},
		{Key: "schema_registry_guid_header", Type: sdk.FieldString, Description: "Record-header key carrying a 16-byte schema GUID (header-GUID wire format; optional, see contract)."},
		{Key: "otel_messaging", Type: sdk.FieldBool, Default: "false", Description: "Enable gated OTel messaging-semconv instrumentation (default off; semconv in Development)."},
		{Key: "timeout", Type: sdk.FieldDuration, Default: "30s", Description: "Per-operation timeout."},
	}
}

// loadConfig resolves and validates configuration shared by both connectors. The
// required check (brokers) surfaces here, before Gather/Notify, per the SDK
// contract. TLS material is loaded into a *tls.Config; SASL/registry secrets are
// kept in memory only.
func loadConfig(cfg sdk.Config) (config, error) {
	c := config{
		brokers:            splitCSV(cfg.Get("brokers")),
		clusterRef:         cfg.Get("cluster_ref"),
		topics:             splitCSV(cfg.Get("topics")),
		group:              cfg.Get("group"),
		topologyScan:       cfg.GetBool("topology_scan", true),
		egressTopic:        cfg.Get("egress_topic"),
		egressSource:       cfg.Get("egress_source"),
		binaryEgress:       cfg.GetBool("binary_egress", false),
		saslMech:           strings.ToLower(strings.TrimSpace(cfg.Get("sasl_mechanism"))),
		saslUser:           cfg.Get("sasl_user"),
		saslPass:           cfg.Get("sasl_password"),
		registryURL:        strings.TrimRight(cfg.Get("schema_registry_url"), "/"),
		registryUser:       cfg.Get("schema_registry_user"),
		registryPass:       cfg.Get("schema_registry_password"),
		registryGUIDHeader: cfg.Get("schema_registry_guid_header"),
		otelMessaging:      cfg.GetBool("otel_messaging", false),
		timeout:            cfg.GetDuration("timeout", defaultTimeout),
	}
	if len(c.brokers) == 0 {
		return config{}, fmt.Errorf("kafka: 'brokers' is required")
	}
	if c.clusterRef == "" {
		c.clusterRef = c.brokers[0]
	}
	if c.group == "" {
		c.group = "olivares-observer"
	}
	if c.egressSource == "" {
		c.egressSource = "/olivares/olivares"
	}
	switch c.saslMech {
	case "", "plain", "scram-sha-256", "scram-sha-512":
	default:
		return config{}, fmt.Errorf("kafka: unsupported sasl_mechanism %q", c.saslMech)
	}
	tlsCfg, err := buildTLSConfig(cfg)
	if err != nil {
		return config{}, err
	}
	c.tls = tlsCfg
	return c, nil
}

// buildTLSConfig assembles a *tls.Config from the tls_* settings via the shared
// tlsx builder (secure default: verification on, TLS 1.2 floor; mTLS needs both
// cert and key), or returns nil when TLS is not enabled.
func buildTLSConfig(cfg sdk.Config) (*tls.Config, error) {
	return tlsx.Build(tlsx.Options{
		Enable:             cfg.GetBool("tls", false),
		CAFile:             cfg.Get("tls_ca_file"),
		CertFile:           cfg.Get("tls_cert_file"),
		KeyFile:            cfg.Get("tls_key_file"),
		InsecureSkipVerify: cfg.GetBool("tls_insecure_skip_verify", false),
	})
}

// splitCSV splits a comma list, trimming spaces and dropping empties.
func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
