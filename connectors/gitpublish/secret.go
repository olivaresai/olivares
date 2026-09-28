// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitpublish

import (
	"fmt"
	"log/slog"
	"time"
)

const redacted = "[redacted]"

// secretBox holds the value behind a pointer, so even reflection-based
// printing of a struct that embeds a Secret in an unexported field shows an
// address, never the value.
type secretBox struct{ v string }

// Secret is a credential value: an App private key, an installation token, a
// bot token or the git authorization header built from one. It renders as
// "[redacted]" everywhere; Reveal is the one way out.
type Secret struct{ p *secretBox }

// NewSecret wraps v.
func NewSecret(v string) Secret {
	if v == "" {
		return Secret{}
	}
	return Secret{p: &secretBox{v: v}}
}

// Reveal returns the value. Call it only where the value must be sent.
func (s Secret) Reveal() string {
	if s.p == nil {
		return ""
	}
	return s.p.v
}

// IsZero reports whether no value is held.
func (s Secret) IsZero() bool { return s.p == nil || s.p.v == "" }

func (Secret) String() string   { return redacted }
func (Secret) GoString() string { return redacted }

// Format implements fmt.Formatter for every verb.
func (Secret) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(redacted)) }

// MarshalJSON never emits the value.
func (Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }

// MarshalText never emits the value.
func (Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }

// LogValue implements slog.LogValuer.
func (Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// Token is a host capability minted for ONE effect. Release it on every exit
// path; it is never persisted.
type Token struct {
	secret Secret
	// ExpiresAt is the host-declared expiry (zero for a GitLab bot token).
	ExpiresAt time.Time
	// Repositories is the host-declared repository list of the token.
	Repositories []string
}

// Value returns the token's secret.
func (t Token) Value() Secret { return t.secret }

// IsZero reports whether the token holds nothing.
func (t Token) IsZero() bool { return t.secret.IsZero() }

func (t Token) String() string   { return "token" + redacted }
func (t Token) GoString() string { return t.String() }

// Format implements fmt.Formatter so a Token never prints its secret.
func (t Token) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(t.String())) }

// LogValue implements slog.LogValuer.
func (t Token) LogValue() slog.Value { return slog.StringValue(t.String()) }

// MarshalJSON never emits the value.
func (t Token) MarshalJSON() ([]byte, error) { return []byte(`"` + t.String() + `"`), nil }
