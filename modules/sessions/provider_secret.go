// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// The two ports a provider record needs and the module is not allowed to be.
//
// WHY THESE ARE PORTS AND NOT CODE IN THIS PACKAGE. A module holds no store.Store,
// so it cannot reach the auth partition where the engine's sealed secrets live, and
// it holds no key material of its own. That is not an inconvenience to route around
// — it is the property that makes "a module can never read a secret" true. A module
// that sealed its own credentials would be a module holding a sealer, and the
// sentence would stop being true for every other secret too.
//
// So the module declares what it NEEDS and the composition root supplies it:
// seal, open, destroy — and, separately, ask a provider what it serves. Both are
// DENY-CLOSED when unwired, and both say which wiring is missing rather than
// answering "internal error" about a deployment choice nobody made.

// ProviderSecretVault seals a provider credential outside this module's partition
// and hands back a NON-SECRET locator.
//
// `name` is a stable, caller-chosen handle derived from the record's own ref, so a
// rotation can reseal in place: Seal with an existing name REPLACES the value and
// returns the same locator. That is what makes a rotation invisible to every
// binding that already names the record.
type ProviderSecretVault interface {
	// Seal stores value under name for this tenant and returns the locator to Open
	// it with. Calling it twice with the same name replaces the value.
	//
	// The ACTOR is a parameter and not a context value because the engine's secret
	// store fails closed on an unattributable principal: it refuses to append an
	// audit event whose subject reads as a real user and is not one. A privileged
	// write this plane cannot attribute is one it does not perform, so the actor
	// travels explicitly and a synthetic engine principal is not an option.
	Seal(ctx context.Context, actor auth.Principal, tenant model.TenantID, name string, value []byte) (string, error)
	// Open returns the sealed value. The caller holds it for one operation and
	// never persists, logs or returns it.
	//
	// It takes NO actor: its caller is the launch path, whose evidence is the run
	// row, its ledger and its K4 digest. A second audit subject for the same act
	// would be a second story about it.
	Open(ctx context.Context, tenant model.TenantID, locator string) ([]byte, error)
	// Revoke destroys the sealed value. It is idempotent: destroying what is
	// already gone is success, because the postcondition the caller wanted holds.
	Revoke(ctx context.Context, actor auth.Principal, tenant model.TenantID, locator string) error
}

// providerVaultName derives the vault handle of one record. It is derived from the
// record ref and from nothing an operator types, so two records can never collide
// on a name and a rename can never move a credential.
func providerVaultName(ref string) string { return "sessions/provider/" + ref }

// ErrProviderRefused marks a probe answer that the PROVIDER produced: it was
// reached, it understood the request, and it rejected the credential.
//
// It is separate from every transport failure on purpose. "Your key is not
// accepted" and "I could not reach the endpoint" have different remedies, and an
// operator handed the first for the second regenerates a key that was never the
// problem — which is a real cost, because regenerating a key invalidates it
// everywhere else it was in use.
var ErrProviderRefused = errors.New("sessions: the provider rejected this credential")

// ProviderProbeRequest is one connection test. APIKey travels in memory for the
// duration of the call. An implementation must not log it, retain it, or put it in
// a URL.
type ProviderProbeRequest struct {
	Kind    string
	BaseURL string
	APIKey  string
}

// ProviderProbeResult is what the provider answered: which models it serves, and a
// bounded non-secret sentence for the operator.
type ProviderProbeResult struct {
	Models []string
	Detail string
}

// ProviderProbe asks a provider which models it serves, with a supplied credential.
//
// THE CONTRACT THAT MATTERS IS THE NEGATIVE ONE: an implementation must not send a
// completion, must not create anything on the provider's side, and must not spend.
// A connection test that spent would be a diagnostic with a bill attached, run on a
// model the operator did not choose.
type ProviderProbe interface {
	Probe(ctx context.Context, req ProviderProbeRequest) (ProviderProbeResult, error)
}

// sealFailure is the fixed public answer when a credential could not be sealed. The
// cause is preserved for internal inspection and never concatenated into the body:
// a sealer's own error text can name a key file, a path or a scope.
func sealFailure(err error) error {
	return errors.Join(&runErr{
		http.StatusServiceUnavailable,
		"the provider credential could not be stored: the sealed credential vault refused (nothing was written, and no credential was stored in the clear)",
	}, err)
}

// openFailure is the fixed public answer when a sealed credential could not be
// opened. It says the operation is denied and does NOT say what to try instead,
// because the only honest remedy — rotate or re-register — depends on whether the
// vault or the record is the broken one, and this layer cannot tell.
func openFailure(err error) error {
	return errors.Join(&runErr{
		http.StatusServiceUnavailable,
		"the registered provider credential could not be opened on this node; the operation is denied and does not fall back to another credential",
	}, err)
}
