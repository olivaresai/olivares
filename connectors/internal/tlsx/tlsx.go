// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package tlsx builds the TLS/mTLS client configuration shared by the Olivares AI
// messaging connectors (Kafka, AMQP, NATS, MQTT —). Every broker connector
// connects securely the same way: a CA bundle to pin broker trust, an optional
// client certificate+key for mutual TLS, and a secure default (verification ON,
// TLS 1.2 floor) the operator can only weaken with an explicit, documented opt-in
// (docs/SECURITY-HARDENING.md — a guardrail is never silently relaxed). Centralizing it keeps that
// secure default identical across connectors and impossible to forget.
//
// It is stdlib-only and imports no engine package.
package tlsx

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// Options selects the TLS material. Enable forces TLS on even when no CA/cert is
// supplied (system roots are used). CAFile pins broker trust. CertFile+KeyFile turn
// on mutual TLS. InsecureSkipVerify disables verification — an explicit operator
// opt-in only, never a default.
type Options struct {
	Enable             bool
	CAFile             string
	CertFile           string
	KeyFile            string
	InsecureSkipVerify bool
}

// Configured reports whether any TLS material was requested, so a caller can leave
// TLS off entirely (returning a nil *tls.Config) when nothing is set.
func (o Options) Configured() bool {
	return o.Enable || o.CAFile != "" || o.CertFile != "" || o.KeyFile != ""
}

// Build returns a *tls.Config for o, or (nil, nil) when no TLS material was
// requested. The config uses a TLS 1.2 minimum and keeps certificate verification
// on unless InsecureSkipVerify is explicitly set. mTLS requires BOTH a cert and a
// key; supplying one without the other is a configuration error, not a silent
// downgrade to one-way TLS.
func Build(o Options) (*tls.Config, error) {
	if !o.Configured() {
		return nil, nil
	}
	t := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: o.InsecureSkipVerify, // #nosec G402 -- explicit operator opt-in only (default verifies), documented
	}
	if o.CAFile != "" {
		pool, err := CAPool(o.CAFile)
		if err != nil {
			return nil, err
		}
		t.RootCAs = pool
	}
	switch {
	case o.CertFile != "" && o.KeyFile != "":
		cert, err := tls.LoadX509KeyPair(o.CertFile, o.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("tlsx: load client keypair: %w", err)
		}
		t.Certificates = []tls.Certificate{cert}
	case o.CertFile != "" || o.KeyFile != "":
		return nil, fmt.Errorf("tlsx: cert_file and key_file must both be set for mTLS")
	}
	return t, nil
}

// CAPool reads a PEM CA bundle into an x509.CertPool. An unreadable file or a file
// with no valid certificate is an error (a CA that pins nothing would silently fall
// back to system roots — surfacing it is safer).
func CAPool(pemFile string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(pemFile)
	if err != nil {
		return nil, fmt.Errorf("tlsx: read CA file: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("tlsx: no valid certificate in %s", pemFile)
	}
	return pool, nil
}
