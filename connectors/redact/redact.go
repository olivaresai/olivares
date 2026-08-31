// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package redact is the public re-export of the connectors' shared secret scrubber
// (connectors/internal/redact), so a module (modules/* — which the Go internal rule
// bars from reaching connectors/internal/*) can apply the SAME vetted redaction the
// connectors use at ingest, rather than reimplementing security-sensitive detection.
// It is a defensive second pass: data read from the access graph / inventory /
// findings is already redacted at connector ingest, so this catches only a free-form
// field that slipped through (docs/SECURITY-HARDENING.md, minimal-data).
package redact

import "github.com/olivaresai/olivares/connectors/internal/redact"

// Clean returns s with any detected secret token replaced by a redaction marker,
// and URL/DSN userinfo and query stripped — safe to emit.
func Clean(s string) string { return redact.Clean(s) }

// Scrub returns the cleaned string and whether anything was redacted.
func Scrub(s string) (string, bool) { return redact.Scrub(s) }

// ContainsSecret reports whether s appears to contain a credential/secret token.
func ContainsSecret(s string) bool { return redact.ContainsSecret(s) }
