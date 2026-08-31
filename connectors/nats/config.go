// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package nats

import (
	"crypto/tls"
	"fmt"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/tlsx"
	"github.com/olivaresai/olivares/sdk"
)

// config is the resolved NATS source connector configuration. Secrets (password,
// token) live here in memory only while the connector runs and are never logged or
// emitted (docs/SECURITY-HARDENING.md).
type config struct {
	// servers is the bootstrap server list (nats[s]://host:4222). One connector wire
	// reaches a standalone NATS server or a clustered/supercluster deployment.
	servers []string
	// streamRef labels the JetStream stream in emitted edges; defaults to stream.
	streamRef string

	// stream is the JetStream stream to observe (required).
	stream string
	// consumer is the durable PULL consumer name the connector attaches to. It is a
	// DEDICATED observation consumer, independent of the app's consumers (doc.go).
	consumer string
	// batch is the number of messages requested per pull (default 64).
	batch int
	// expires bounds one pull request server-side (default 5s); the server returns a
	// 408-status message when the window elapses with no traffic, and the connector
	// re-requests, so Gather blocks honoring ctx without a client-side ticker.
	expires time.Duration

	// --- auth (in memory only) ---
	user     string
	password string
	token    string

	// --- security ---
	tls *tls.Config // nil unless tls/mTLS configured

	// otelMessaging is the instrumentation gate (default off).
	otelMessaging bool

	// timeout bounds the connect handshake and a single pull read.
	timeout time.Duration
}

const (
	defaultBatch   = 64
	defaultExpires = 5 * time.Second
	defaultTimeout = 30 * time.Second
)

// descriptorFields declares every setting the source accepts, so the engine can
// validate config and a UI can render a form without running the connector.
func descriptorFields() []sdk.ConfigField {
	return []sdk.ConfigField{
		{Key: "servers", Type: sdk.FieldString, Required: true, Description: "Comma-separated NATS servers (nats[s]://host:4222)."},
		{Key: "stream", Type: sdk.FieldString, Required: true, Description: "JetStream stream to observe."},
		{Key: "consumer", Type: sdk.FieldString, Required: true, Description: "Durable PULL consumer name (a dedicated observation consumer, not the app's)."},
		{Key: "stream_ref", Type: sdk.FieldString, Description: "Label for the stream in emitted edges (defaults to the stream name)."},
		{Key: "batch", Type: sdk.FieldInt, Default: "64", Description: "Messages requested per JetStream pull."},
		{Key: "expires", Type: sdk.FieldDuration, Default: "5s", Description: "Server-side expiry of one pull request (re-requested on expiry)."},
		{Key: "user", Type: sdk.FieldString, Description: "NATS username (user/password auth)."},
		{Key: "password", Type: sdk.FieldString, Secret: true, Description: "NATS password (reference)."},
		{Key: "token", Type: sdk.FieldString, Secret: true, Description: "NATS auth token (reference; alternative to user/password)."},
		{Key: "tls", Type: sdk.FieldBool, Default: "false", Description: "Enable TLS to the servers (forced when a server uses natss://)."},
		{Key: "tls_ca_file", Type: sdk.FieldString, Description: "PEM CA bundle to verify the servers."},
		{Key: "tls_cert_file", Type: sdk.FieldString, Description: "Client certificate for mTLS."},
		{Key: "tls_key_file", Type: sdk.FieldString, Description: "Client private key for mTLS."},
		{Key: "tls_insecure_skip_verify", Type: sdk.FieldBool, Default: "false", Description: "Skip server certificate verification (NOT for production)."},
		{Key: "otel_messaging", Type: sdk.FieldBool, Default: "false", Description: "Enable gated OTel messaging-semconv instrumentation (default off; semconv in Development)."},
		{Key: "timeout", Type: sdk.FieldDuration, Default: "30s", Description: "Connect handshake / per-pull read timeout."},
	}
}

// loadConfig resolves and validates configuration. The required checks (servers,
// stream, consumer) surface here, before Gather, per the SDK contract. TLS material
// is loaded into a *tls.Config; auth secrets are kept in memory only.
func loadConfig(cfg sdk.Config) (config, error) {
	c := config{
		servers:       splitCSV(cfg.Get("servers")),
		stream:        strings.TrimSpace(cfg.Get("stream")),
		consumer:      strings.TrimSpace(cfg.Get("consumer")),
		streamRef:     strings.TrimSpace(cfg.Get("stream_ref")),
		batch:         cfg.GetInt("batch", defaultBatch),
		expires:       cfg.GetDuration("expires", defaultExpires),
		user:          cfg.Get("user"),
		password:      cfg.Get("password"),
		token:         cfg.Get("token"),
		otelMessaging: cfg.GetBool("otel_messaging", false),
		timeout:       cfg.GetDuration("timeout", defaultTimeout),
	}
	if len(c.servers) == 0 {
		return config{}, fmt.Errorf("nats: 'servers' is required")
	}
	if c.stream == "" {
		return config{}, fmt.Errorf("nats: 'stream' is required")
	}
	if c.consumer == "" {
		return config{}, fmt.Errorf("nats: 'consumer' is required")
	}
	if c.batch <= 0 {
		c.batch = defaultBatch
	}
	if c.expires <= 0 {
		c.expires = defaultExpires
	}
	if c.streamRef == "" {
		c.streamRef = c.stream
	}
	// A natss:// scheme on any server forces TLS even if tls=false was left default.
	forceTLS := cfg.GetBool("tls", false)
	for _, s := range c.servers {
		if strings.HasPrefix(strings.ToLower(s), "natss://") || strings.HasPrefix(strings.ToLower(s), "tls://") {
			forceTLS = true
		}
	}
	tlsCfg, err := tlsx.Build(tlsx.Options{
		Enable:             forceTLS,
		CAFile:             cfg.Get("tls_ca_file"),
		CertFile:           cfg.Get("tls_cert_file"),
		KeyFile:            cfg.Get("tls_key_file"),
		InsecureSkipVerify: cfg.GetBool("tls_insecure_skip_verify", false),
	})
	if err != nil {
		return config{}, err
	}
	c.tls = tlsCfg
	return c, nil
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
