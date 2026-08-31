// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package natsbus

import (
	"errors"
	"fmt"
	"strings"
)

// Config is the operator's distributed-bus backend selection
// (OLIVARES_BUS_CONFIG, a JSON file read by the composition root). Invalid
// config is a BOOT FAILURE, never a silent in-proc fallback: a node that
// silently fell back would partition the bus across an HA cluster — the exact
// failure the backend exists to fix (docs/SECURITY-HARDENING.md: never a silent gap).
type Config struct {
	// Backend selects the transport. The only supported value is "nats".
	Backend string `json:"backend"`
	// URL is the NATS server URL (nats://host:4222, comma-separated for a
	// cluster, tls:// for implicit TLS). Required.
	URL string `json:"url"`
	// Name is the NATS connection name (shows in server monitoring). Default
	// "olivares".
	Name string `json:"name"`
	// SubjectPrefix roots every bridged event's subject
	// ("<prefix>.<event type>"). Default "olivares.bus". Deployments sharing one
	// NATS cluster across environments isolate them by prefix (or by account).
	SubjectPrefix string `json:"subject_prefix"`
	// CredentialsFile is an optional NATS .creds file (JWT+NKey operator auth).
	CredentialsFile string `json:"credentials_file"`
	// TLSCAFile optionally pins the CA bundle that signed the server cert.
	TLSCAFile string `json:"tls_ca_file"`
	// TLSCertFile/TLSKeyFile optionally present a client certificate (NATS mTLS).
	// Both or neither.
	TLSCertFile string `json:"tls_cert_file"`
	TLSKeyFile  string `json:"tls_key_file"`
	// Buffer is the per-subscriber queue depth of the local fan-out (0 = the
	// in-proc default, 256).
	Buffer int `json:"buffer"`
}

// DefaultName and DefaultSubjectPrefix are applied by Validate when unset.
const (
	DefaultName          = "olivares"
	DefaultSubjectPrefix = "olivares.bus"
)

// Validate checks the config and fills defaults in place. Any error means the
// operator's distributed-bus intent cannot be honored — the caller must refuse
// to boot (deny-closed), not degrade to in-proc.
func (c *Config) Validate() error {
	if c.Backend != "nats" {
		return fmt.Errorf("bus config: unknown backend %q (the only supported backend is \"nats\")", c.Backend)
	}
	if strings.TrimSpace(c.URL) == "" {
		return errors.New("bus config: url is required")
	}
	if c.Name == "" {
		c.Name = DefaultName
	}
	if c.SubjectPrefix == "" {
		c.SubjectPrefix = DefaultSubjectPrefix
	}
	if err := ValidSubjectTokens(c.SubjectPrefix); err != nil {
		return fmt.Errorf("bus config: subject_prefix %q: %w", c.SubjectPrefix, err)
	}
	if (c.TLSCertFile == "") != (c.TLSKeyFile == "") {
		return errors.New("bus config: tls_cert_file and tls_key_file must be set together")
	}
	return nil
}

// ValidSubjectTokens rejects a string that cannot be (part of) a published NATS
// subject: empty, whitespace, NUL, the wildcard characters, or empty tokens
// (leading/trailing/consecutive dots) — all of which the server would reject or
// misroute (docs.nats.io/nats-concepts/subjects). Exported so the enterprise
// durable backend validates its own subject prefix the same way.
func ValidSubjectTokens(s string) error {
	if s == "" {
		return errors.New("empty")
	}
	for _, tok := range strings.Split(s, ".") {
		if tok == "" {
			return errors.New("empty subject token (leading/trailing/consecutive dots)")
		}
		if strings.ContainsAny(tok, " \t\r\n*>\x00") {
			return fmt.Errorf("token %q contains whitespace, a wildcard or NUL", tok)
		}
	}
	return nil
}
