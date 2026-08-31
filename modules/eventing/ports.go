// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package eventing

import (
	"context"
	"errors"
	"net/http"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// This module's seams. Like notify's Dispatcher, each port is defined in the
// module's OWN terms and wired by the composition root; every default is
// fail-closed and loudly visible at Start — a deployment that cannot authorize
// or cannot keep secrets must never silently deliver or silently store.

// Authz is the authorization seam: the engine's *auth.Authorizer satisfies it
// directly. The dispatcher evaluates every delivery attempt through it with a
// ScopedPrincipal carrying the subscription's (tenant, role) grant, so the ABAC
// policy layer restricts outbound events exactly as it restricts live requests.
type Authz interface {
	// Allowed reports whether p holds perm in tenant (RBAC ∩ ABAC, deny-closed).
	Allowed(ctx context.Context, p auth.Principal, perm auth.Permission, tenant model.TenantID) bool
}

// SecretSealer is the secret-at-rest seam: it seals a subscription's HMAC
// signing secret for persistence and opens it at dispatch time. The sealed form
// is bound to the tenant so a ciphertext cannot be replayed across tenants. The
// composition root implements it over an engine-held key (cmd/olivares);
// the module never sees key material.
type SecretSealer interface {
	// Seal encrypts plaintext for tenant and returns an opaque storable string.
	Seal(ctx context.Context, tenant model.TenantID, plaintext []byte) (string, error)
	// Open decrypts a sealed value for tenant.
	Open(ctx context.Context, tenant model.TenantID, sealed string) ([]byte, error)
}

// Doer is the outbound HTTP seam (tests inject a fake or a permissive client;
// production uses the module's SSRF-guarded, timeout-bounded client).
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// errNoSealer is returned when no SecretSealer is wired: subscriptions cannot
// be created or rotated, because the platform refuses to hold a cleartext
// secret (fail-closed; docs/SECURITY-HARDENING.md).
var errNoSealer = errors.New("eventing: no secret sealer wired; cannot store a subscription secret")

// nopSealer is the deny-closed default SecretSealer.
type nopSealer struct{}

func (nopSealer) Seal(context.Context, model.TenantID, []byte) (string, error) {
	return "", errNoSealer
}

func (nopSealer) Open(context.Context, model.TenantID, string) ([]byte, error) {
	return nil, errNoSealer
}
