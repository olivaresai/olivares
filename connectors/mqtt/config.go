// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mqtt

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/tlsx"
	"github.com/olivaresai/olivares/sdk"
)

// config is the resolved MQTT source configuration. The connector is SOURCE-ONLY:
// it dials a broker, subscribes to observation topic filters, and observes the
// PUBLISH flow (Sparkplug B topology + generic MQTT traffic) as minimal-data edges.
// Secrets (the broker password) live here in memory only while the connector runs
// and are never logged or emitted (docs/SECURITY-HARDENING.md).
type config struct {
	// broker is the MQTT broker endpoint: tcp://host:1883 (plain) or tls://host:8883
	// (TLS). The host:port is dialed directly; the scheme only selects TLS.
	broker string
	// brokerRef labels the broker in emitted edges; defaults to the broker host:port
	// with any credentials stripped (redact.SanitizeURL), never the raw endpoint.
	brokerRef string
	// host is the dial target host:port parsed from broker.
	host string
	// useTLS is set when the broker scheme is tls:// or the tls flag is on.
	useTLS bool

	// clientID is the MQTT client identifier the connector connects with.
	clientID string
	// topics are the topic filters the connector SUBSCRIBEs to as a passive observer.
	// Default 'spBv1.0/#' (the Sparkplug B namespace). A pub/sub SUBSCRIBE is a
	// non-destructive fan-out copy — it never disturbs other subscribers (doc.go).
	topics []string
	// keepalive is the MQTT keepalive interval; the connector sends a PINGREQ within
	// it to keep the connection live (the engine owns scheduling, not the connector).
	keepalive time.Duration

	// username / password are the optional MQTT 5 CONNECT credentials. password is a
	// Secret reference held in memory only (docs/SECURITY-HARDENING.md).
	username string
	password string

	// tls is the TLS/mTLS client config (nil unless TLS configured). mTLS is the
	// secure default for OT/IoT brokers (doc.go).
	tls *tls.Config

	// otelMessaging is the instrumentation gate (default off).
	otelMessaging bool

	timeout time.Duration
}

const (
	defaultTimeout   = 30 * time.Second
	defaultKeepalive = 60 * time.Second
	defaultClientID  = "olivares-observer"
	defaultTopic     = "spBv1.0/#"
)

// descriptorFields declares every setting the source accepts, so the engine can
// validate config and a UI can render a form without running the connector.
func descriptorFields() []sdk.ConfigField {
	return []sdk.ConfigField{
		{Key: "broker", Type: sdk.FieldString, Required: true, Description: "MQTT broker endpoint: tcp://host:1883 or tls://host:8883."},
		{Key: "broker_ref", Type: sdk.FieldString, Description: "Label for the broker in emitted edges (defaults to the broker host:port)."},
		{Key: "client_id", Type: sdk.FieldString, Default: defaultClientID, Description: "MQTT client identifier to connect with."},
		{Key: "topics", Type: sdk.FieldString, Default: defaultTopic, Description: "Comma-separated topic filters to observe (default the Sparkplug B namespace spBv1.0/#)."},
		{Key: "keepalive", Type: sdk.FieldDuration, Default: "60s", Description: "MQTT keepalive interval (a PINGREQ is sent within it)."},
		{Key: "username", Type: sdk.FieldString, Description: "MQTT username (optional)."},
		{Key: "password", Type: sdk.FieldString, Secret: true, Description: "MQTT password (reference; held in memory only)."},
		{Key: "tls", Type: sdk.FieldBool, Default: "false", Description: "Enable TLS to the broker (also implied by a tls:// broker scheme)."},
		{Key: "tls_ca_file", Type: sdk.FieldString, Description: "PEM CA bundle to verify the broker."},
		{Key: "tls_cert_file", Type: sdk.FieldString, Description: "Client certificate for mTLS (the secure default for OT/IoT)."},
		{Key: "tls_key_file", Type: sdk.FieldString, Description: "Client private key for mTLS."},
		{Key: "tls_insecure_skip_verify", Type: sdk.FieldBool, Default: "false", Description: "Skip broker certificate verification (NOT for production)."},
		{Key: "otel_messaging", Type: sdk.FieldBool, Default: "false", Description: "Enable gated OTel messaging-semconv instrumentation (default off; semconv in Development)."},
		{Key: "timeout", Type: sdk.FieldDuration, Default: "30s", Description: "Per-operation (dial/connect) timeout."},
	}
}

// loadConfig resolves and validates the source configuration. The required check
// (broker) and the broker URL parse surface here, before Gather, per the SDK
// contract. TLS material is loaded into a *tls.Config; the password secret is kept
// in memory only.
func loadConfig(cfg sdk.Config) (config, error) {
	c := config{
		broker:        strings.TrimSpace(cfg.Get("broker")),
		brokerRef:     strings.TrimSpace(cfg.Get("broker_ref")),
		clientID:      cfg.Get("client_id"),
		topics:        splitCSV(cfg.Get("topics")),
		keepalive:     cfg.GetDuration("keepalive", defaultKeepalive),
		username:      cfg.Get("username"),
		password:      cfg.Get("password"),
		otelMessaging: cfg.GetBool("otel_messaging", false),
		timeout:       cfg.GetDuration("timeout", defaultTimeout),
	}
	if c.broker == "" {
		return config{}, fmt.Errorf("mqtt: 'broker' is required")
	}
	host, scheme, err := parseBroker(c.broker)
	if err != nil {
		return config{}, err
	}
	c.host = host

	tlsRequested := cfg.GetBool("tls", false) || scheme == "tls" || scheme == "ssl" || scheme == "mqtts"
	tlsCfg, err := buildTLSConfig(cfg, tlsRequested)
	if err != nil {
		return config{}, err
	}
	c.tls = tlsCfg
	c.useTLS = tlsCfg != nil

	if c.clientID == "" {
		c.clientID = defaultClientID
	}
	if len(c.topics) == 0 {
		c.topics = []string{defaultTopic}
	}
	if c.keepalive <= 0 {
		c.keepalive = defaultKeepalive
	}
	if c.brokerRef == "" {
		c.brokerRef = c.host
	}
	return c, nil
}

// parseBroker extracts the dial target host:port and the scheme from a broker URL.
// A bare "host:port" with no scheme is accepted (treated as tcp). A tls:// scheme
// (or ssl/mqtts) selects TLS; tcp:// (or mqtt) is plaintext.
func parseBroker(raw string) (host, scheme string, err error) {
	if !strings.Contains(raw, "://") {
		// bare host:port; default scheme tcp.
		return raw, "tcp", nil
	}
	u, perr := url.Parse(raw)
	if perr != nil {
		return "", "", fmt.Errorf("mqtt: invalid broker URL %q: %w", raw, perr)
	}
	if u.Host == "" {
		return "", "", fmt.Errorf("mqtt: broker URL %q has no host", raw)
	}
	scheme = strings.ToLower(u.Scheme)
	switch scheme {
	case "tcp", "mqtt", "tls", "ssl", "mqtts", "":
	default:
		return "", "", fmt.Errorf("mqtt: unsupported broker scheme %q (use tcp:// or tls://)", scheme)
	}
	host = u.Host
	if _, _, err := net.SplitHostPort(host); err != nil {
		port := "1883"
		if scheme == "tls" || scheme == "ssl" || scheme == "mqtts" {
			port = "8883"
		}
		host = net.JoinHostPort(u.Hostname(), port)
	}
	return host, scheme, nil
}

// buildTLSConfig assembles a *tls.Config from the tls_* settings via the shared
// tlsx builder (secure default: verification on, TLS 1.2 floor; mTLS needs both
// cert and key), forcing TLS on when the broker scheme requested it. Returns nil
// when no TLS material is requested.
func buildTLSConfig(cfg sdk.Config, enable bool) (*tls.Config, error) {
	return tlsx.Build(tlsx.Options{
		Enable:             enable,
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
