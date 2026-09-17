// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// Provider AUTHENTICATION source and readiness (CORRECTED-CONTRACT §5).
//
// Two things are kept apart here that are easy to conflate, and conflating them
// is what the ratified clauses exist to stop:
//
//   - the SOURCE is an authorization: which of two explicitly authorized ways
//     this profile's child obtains its provider identity. It is non-secret, it is
//     resolved and persisted BEFORE the launch intent, the gates and the
//     reservation, and it travels in the K4 dispatch digest so the same dispatch
//     key with another source is a conflict rather than a replay.
//   - the READINESS is an observation: what the provider itself answered when
//     asked. A home path and an injected value are not proof of an authenticated
//     account, and a later provider authentication error invalidates it.
//
// They are NOT a fallback chain. A profile authorized for its own account home is
// never required to receive an injected key, and a managed mint failure DENIES
// that launch instead of quietly opening the account home. And the existing
// Claude WIF bearer is never reinterpreted as another provider's token: a managed
// launch resolves a PROVIDER-COMPATIBLE credential through that driver's own
// governed adapter, or it is refused with the adapter named.

// The two authorized authentication sources. Empty is the LEGACY unprofiled /
// pre-source state and is only honored on the historical Claude path, whose
// behavior it preserves exactly.
const (
	// AuthSourceAccountHome uses the saved account login inside the profile's own
	// homes. Only the owned official child ever opens them; Olivares does not
	// parse, copy, log or persist the contents of an auth file.
	AuthSourceAccountHome = "provider_account_home"
	// AuthSourceManagedInjection resolves a provider-compatible credential through
	// that driver's governed adapter. Mint, expiry or adapter failure denies the
	// launch.
	AuthSourceManagedInjection = "managed_injection"
)

// Authentication readiness, reported separately from process, conversation and
// turn state.
const (
	AuthStateUnknown  = "unknown"
	AuthStateRequired = "required"
	AuthStateReady    = "ready"
)

// validAuthSource reports whether s is one of the two authorized sources.
func validAuthSource(s string) bool {
	return s == AuthSourceAccountHome || s == AuthSourceManagedInjection
}

// normalizeAuthSource validates an operator-supplied authentication source.
// Empty stays empty: it is the absence of an authorization, not a default.
func normalizeAuthSource(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || validAuthSource(s) {
		return s, nil
	}
	return "", badRequest("auth_source must be " + AuthSourceAccountHome + " or " + AuthSourceManagedInjection)
}

// ProviderCredentialRequest scopes a managed mint to one launch. References only.
type ProviderCredentialRequest struct {
	Driver     string
	Tenant     model.TenantID
	RunRef     string
	ProfileRef string
}

// ProviderCredential is a SHORT-LIVED, provider-compatible credential produced by
// a governed adapter. Env is the exact environment the provider's own CLI reads;
// it is held in memory for the launch and never persisted. ID and Scheme are the
// only non-sensitive parts and the only ones that reach a row.
type ProviderCredential struct {
	ID       string
	Scheme   string
	NotAfter time.Time
	Env      []EnvVar
}

// Expired mirrors Credential.Expired: a zero NotAfter is treated as expired.
func (c ProviderCredential) Expired(now time.Time) bool {
	return c.NotAfter.IsZero() || !now.Before(c.NotAfter)
}

// ProviderCredentialSource is the governed managed-injection adapter for ONE
// driver. There is no default: an unconfigured driver refuses its managed
// launches by name rather than borrowing another provider's issuer.
type ProviderCredentialSource interface {
	Mint(context.Context, ProviderCredentialRequest) (ProviderCredential, error)
}

// ProviderCredentialSourceFunc adapts a function to a ProviderCredentialSource.
type ProviderCredentialSourceFunc func(context.Context, ProviderCredentialRequest) (ProviderCredential, error)

// Mint calls the wrapped function.
func (f ProviderCredentialSourceFunc) Mint(ctx context.Context, req ProviderCredentialRequest) (ProviderCredential, error) {
	return f(ctx, req)
}

// errNoProviderAdapter is the truthful refusal for a managed launch of a driver
// whose governed adapter nobody configured.
var errNoProviderAdapter = errors.New("sessions: no managed provider credential adapter is configured for this driver")

// ---------------------------------------------------------------------------
// Provider approval authority.
// ---------------------------------------------------------------------------

// ProviderApprovalRequest is the references-only view of one approval an owned
// provider child asked for. It carries no command, no patch and no file content:
// the driver's codec decides the wire shape, this seam decides authority.
type ProviderApprovalRequest struct {
	Driver         string
	RunRef         string
	ProfileRef     string
	ConversationID string
	TurnID         string
	// Method is the exact provider method, so an authority can refuse a surface it
	// does not understand instead of answering a generic "approval".
	Method string
	// Kind is the codec family the answer will be encoded with.
	Kind string
	// Requested names the permission entries the provider asked for. Grants are
	// intersected with it: an authority cannot widen a request it was shown.
	Requested []string
}

// ProviderApprovalDecision is an authority's verdict. Granted is intersected with
// the request; SessionScope is honored only when the authority set it explicitly.
type ProviderApprovalDecision struct {
	Allow        bool
	Granted      []string
	SessionScope bool
	Reason       string
}

// ProviderApprovalGate authorizes one provider approval request. The default is
// DENY-CLOSED: with no authority wired, every approval is refused with the
// method's own refusal codec — a refusal the provider understands, never a hang
// and never a grant.
type ProviderApprovalGate interface {
	Approve(context.Context, model.TenantID, ProviderApprovalRequest) (ProviderApprovalDecision, error)
}

// denyProviderApprovalGate is the unwired default.
type denyProviderApprovalGate struct{}

func (denyProviderApprovalGate) Approve(context.Context, model.TenantID, ProviderApprovalRequest) (ProviderApprovalDecision, error) {
	return ProviderApprovalDecision{Reason: "no provider approval authority is wired"}, nil
}

// ---------------------------------------------------------------------------
// Launch-time resolution.
// ---------------------------------------------------------------------------

// launchAuthSource is the authorization this launch runs under ("" for a legacy
// unprofiled Claude launch).
func launchAuthSource(p CreateRunParams) string {
	if p.ProviderHome == nil {
		return ""
	}
	return p.ProviderHome.AuthSource
}

// requireAuthSourceForDriver refuses, deny-closed, a launch of a driver that has
// no explicitly authorized authentication source. The historical Claude path is
// exempt BY NAME and only when the profile names no source: that exemption is
// what preserves the shipped behavior, and it must not be reachable by a driver
// that never had it.
func requireAuthSourceForDriver(driver, source string) error {
	if validAuthSource(source) {
		return nil
	}
	if driver == providerDriverClaude && source == "" {
		return nil
	}
	return &runErr{
		http.StatusUnprocessableEntity,
		"this provider profile has no authorized authentication source; set auth_source to " +
			AuthSourceAccountHome + " or " + AuthSourceManagedInjection + " before launching driver " + driver,
	}
}

// mintLaunchAuthority resolves the SHORT-LIVED material this launch's authorized
// source produces. It returns the (non-secret) credential stamp for the run row
// and the provider-compatible environment the child receives.
//
// The four cases are deliberately explicit rather than a chain:
//
//   - Claude, no source: exactly the historical path (the WIF/file bearer).
//   - Claude, managed_injection: the SAME governed Claude issuer — the existing
//     WIF exchange IS Claude's managed adapter, so this is a name for what already
//     happens, not a second issuer.
//   - any driver, provider_account_home: NOTHING is injected. The child opens the
//     home it was explicitly authorized for, and Olivares never reads it.
//   - any other driver, managed_injection: that driver's OWN governed adapter, or
//     a refusal that names the missing adapter. The Claude bearer is never reused.
func (m *Module) mintLaunchAuthority(
	ctx context.Context,
	tenant model.TenantID,
	runRef string,
	p CreateRunParams,
) (Credential, []EnvVar, error) {
	driver := launchDriverKey(p)
	source := launchAuthSource(p)
	if err := requireAuthSourceForDriver(driver, source); err != nil {
		return Credential{}, nil, err
	}
	if source == AuthSourceAccountHome {
		// The authorized account home is the credential. Requiring an injected key
		// on top of it is exactly the conflation §5.1 forbids.
		return Credential{}, nil, nil
	}
	if driver == providerDriverClaude {
		cred, err := m.maybeMint(ctx, tenant, runRef, p.Transport)
		return cred, nil, err
	}
	profileRef := ""
	if p.ProviderHome != nil {
		profileRef = p.ProviderHome.ProfileID
	}
	src, ok := m.rt.providerCreds[driver]
	if !ok || src == nil {
		return Credential{}, nil, &runErr{
			http.StatusServiceUnavailable,
			"managed credential injection is not available for driver " + driver +
				": no governed provider credential adapter is configured (the launch is denied; it does not fall back to the profile's account home)",
		}
	}
	pc, err := src.Mint(ctx, ProviderCredentialRequest{
		Driver: driver, Tenant: tenant, RunRef: runRef, ProfileRef: profileRef,
	})
	if err != nil {
		return Credential{}, nil, &runErr{
			http.StatusForbidden,
			"the governed credential adapter for driver " + driver + " refused this launch (no fallback to the profile's account home)",
		}
	}
	if pc.Expired(m.now()) {
		return Credential{}, nil, &runErr{
			http.StatusForbidden,
			"the governed credential adapter for driver " + driver + " returned an expired credential",
		}
	}
	if err := validateProviderCredentialEnv(pc.Env); err != nil {
		return Credential{}, nil, err
	}
	if err := validateOpenCodeReservedInjection(driver, pc.Env); err != nil {
		return Credential{}, nil, err
	}
	// Only the non-sensitive stamp lands on the row; Env stays in LaunchSpec.
	return Credential{ID: pc.ID, Scheme: pc.Scheme, NotAfter: pc.NotAfter}, pc.Env, nil
}

// validateProviderCredentialEnv refuses an adapter that tries to name a variable
// it does not own. The adapter's licence is to supply CREDENTIAL variables; a
// home or a routing override from it would be the same accident §6 refuses from a
// caller and from a gate, arriving through a third door.
func validateProviderCredentialEnv(env []EnvVar) error {
	for _, item := range env {
		if providerHomeEnvName(item.Name) {
			return forbiddenErr("launch denied: the provider credential adapter named " + item.Name + ", which the provider profile owns")
		}
		// Nor may it name the control plane's own variables or another provider's:
		// the adapter's licence is its OWN driver's credential, and OLIVARES_* is
		// where this runtime's work and communication bearers live.
		if strings.HasPrefix(item.Name, "OLIVARES_") ||
			strings.HasPrefix(item.Name, "ANTHROPIC_") ||
			strings.HasPrefix(item.Name, "CLAUDE_") ||
			strings.HasPrefix(item.Name, "OPENCODE_") {
			return forbiddenErr("launch denied: the provider credential adapter named " + item.Name + ", which it does not own")
		}
	}
	return validateExplicitEnv(env)
}
