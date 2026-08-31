// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package executor

import (
	"context"
	"errors"
	"time"
)

// nowFunc is the package clock, overridable in tests. The executor never uses
// wall-clock time except to reject an already-expired credential.
var nowFunc = time.Now

// ErrNoCredentialSource is the deny-closed sentinel: no credential source is
// wired, so no actuation may proceed. This is the strongest least-privilege
// posture (docs/SECURITY-HARDENING.md,§4): the executor NEVER falls back to a default or
// long-lived key — absent a source, every backend fails closed.
var ErrNoCredentialSource = errors.New("executor: no credential source wired; actuation denied (no default/long-lived key)")

// Mode is the access a credential is scoped to: read (plan/observe) or write
// (apply/destroy). Minting the narrowest mode for each operation is least
// privilege in action.
type Mode int

const (
	// ModeRead scopes a credential to read-only operations (plan, observe).
	ModeRead Mode = iota
	// ModeWrite scopes a credential to mutating operations (apply, destroy).
	ModeWrite
)

// String renders a Mode for audit meta and credential-source policy.
func (m Mode) String() string {
	if m == ModeWrite {
		return "write"
	}
	return "read"
}

// MintRequest asks a CredentialSource for a short-lived credential scoped to one
// operation: a target environment, the runtime/target being acted on, and the
// access mode. The source attests and scopes accordingly (per-environment,
// least-privilege).
type MintRequest struct {
	Environment string
	Target      string
	Runtime     string
	Mode        Mode
}

// Credential is a SHORT-LIVED, environment-attested credential minted at the
// moment of an operation. It is used in the call and discarded:
//
//   - ID is a NON-SENSITIVE identifier (a SPIFFE SVID serial, a WIF exchange id, a
//     hash) suitable for the audit ledger. It is the ONLY part that may be recorded.
//   - Token / ClientCert / ClientKey is the credential MATERIAL. It is NEVER logged,
//     NEVER persisted, NEVER placed in a Change/Result/audit/ledger entry.
//   - NotAfter is the expiry; an expired credential is unusable (fail-closed).
type Credential struct {
	ID         string
	Token      string // bearer / JWT-SVID material (opaque) — never recorded
	ClientCert []byte // X.509-SVID cert chain (PEM) — never recorded
	ClientKey  []byte // X.509-SVID private key (PEM) — never recorded
	NotAfter   time.Time
	// Scheme names the attestation mechanism (e.g. "spiffe", "wif", "mock") for
	// non-sensitive audit context only.
	Scheme string
}

// Expired reports whether the credential is past its NotAfter. A zero NotAfter is
// treated as expired (fail-closed: a credential with no stated lifetime is not a
// short-lived attested credential).
func (c Credential) Expired(now time.Time) bool {
	return c.NotAfter.IsZero() || !now.Before(c.NotAfter)
}

// Bearer reports whether the credential carries bearer/JWT material.
func (c Credential) Bearer() bool { return c.Token != "" }

// CredentialSource mints short-lived, environment-attested credentials at the
// moment of a call. The production implementation is an OIDC workload-identity
// federation exchange (RFC 7523/8693) or a SPIFFE Workload API client (X.509 /
// JWT-SVID) — the LIVE in-process WIF exchange (SPIRE JWT-SVID → claude-wif sk-ant-oat)
// is wired behind this seam by the broker in cmd/olivares (credential kind "wif") leaves this seam functional and deny-closed with the rotated-file source. A source
// that cannot attest MUST return an error (never a default credential).
type CredentialSource interface {
	Mint(ctx context.Context, req MintRequest) (Credential, error)
}

// DenyCredentialSource is the deny-closed default: it never mints. Without a real
// source wired, every actuation fails closed — there is no default or long-lived
// key anywhere in the executor.
type DenyCredentialSource struct{}

// Mint always denies.
func (DenyCredentialSource) Mint(context.Context, MintRequest) (Credential, error) {
	return Credential{}, ErrNoCredentialSource
}

// MintFunc adapts a function to a CredentialSource (used by the composition root
// to wrap a WIF/SPIFFE client, and by tests to inject a short-lived mock source).
type MintFunc func(ctx context.Context, req MintRequest) (Credential, error)

// Mint calls the wrapped function.
func (f MintFunc) Mint(ctx context.Context, req MintRequest) (Credential, error) { return f(ctx, req) }
