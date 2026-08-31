// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Login-enforcement seam. This is the RESERVED ENTERPRISE login-enforcement
// line declared in LICENSING.md: require-SSO (block password login when an IdP is
// active) and a network/IP allow-list over the login surface. Like the seat cap
// (seatcap.go) and multi-IdP (federation_config.go), it is the inverted seam — the
// open (AGPL) build wires NO policy (nil), so login behaves EXACTLY as it does today
// (the open build never fakes the control); the enterprise build injects a closed
// engine (enterprise/ssoenforce, build-tag gated, absent from the public repo after
// the split) that reads the stored posture (FederationConfig.RequireSSO /
// NetworkAllowCIDRs) and decides. The open binary literally has no code to enforce —
// the GitLab ee/ seam, not a flag that disables linked code.
//
// HONEST LIMITS (docs/SECURITY-HARDENING.md, never overstated). The enforcement is VERIFIED-DEPLOYED
// over the engine's own login routes — Authenticator.Login (password) and CompleteSSO
// (federated completion) — NEVER "impossible to bypass". It covers the two ways a
// human establishes a first-party session; it does NOT cover an already-issued token
// presented to the API (that credential was minted by an earlier, allowed login), nor
// any non-login access path, and the IP allow-list trusts the real transport peer
// (clientIP = RemoteAddr, not a spoofable X-Forwarded-For). It is a login-surface
// defense, not a network firewall. See enterprise/ssoenforce/doc.go for the full
// scope statement.

// ErrSSORequired is returned when a PASSWORD login is refused because the deployment
// requires single sign-on. It is a deny-closed SECURITY control the operator turns on
// (NOT a product cut like ErrMultiIDPRequiresEnterprise / ErrUserCapRequiresEnterprise):
// the message tells the human to sign in with SSO, never to upgrade an edition. It is
// declared here in the open core but INERT in the open build — only the enterprise
// LoginPolicy ever produces it (a nil policy never blocks a password login).
var ErrSSORequired = errors.New("auth: sso_required: password login is disabled for this deployment; sign in with single sign-on (SSO)")

// ErrNetworkNotAllowed is returned when a login (password OR SSO completion) arrives
// from a peer IP outside the deployment's configured network allow-list. It is a
// deny-closed perimeter control over the login surface; like ErrSSORequired it is
// declared here but INERT without the enterprise LoginPolicy (an unconfigured or empty
// allow-list permits every IP — the control is opt-in, deny-closed only once a list is
// set). A malformed stored CIDR, or a peer address the engine cannot parse, fails
// CLOSED to this error (never allow-all on a parse failure).
var ErrNetworkNotAllowed = errors.New("auth: network_not_allowed: login is not permitted from your network")

// LoginPolicy is the RESERVED ENTERPRISE login-enforcement capability. The default
// (AGPL) binary wires nil (WithLoginPolicy is never called, or called with nil), which
// means NO enforcement — Login and CompleteSSO behave byte-identically to today. The
// enterprise build wires a closed engine (enterprise/ssoenforce) that resolves the
// stored posture and decides. The two methods are consulted at the two login decision
// points; both are deny-closed (any non-nil error blocks the login).
type LoginPolicy interface {
	// AllowNetwork reports whether a login from peer IP ip is permitted by the
	// scope's network allow-list. It is consulted on EVERY login path (password and
	// SSO completion) BEFORE any credential work, so a network-blocked peer never
	// reaches password verification or JIT provisioning. nil = permitted (the common
	// case: no allow-list configured ⇒ permit all). A non-nil error blocks the login:
	// ErrNetworkNotAllowed for an IP outside a configured list, or a wrapped
	// store/parse error (fail-closed — a posture the engine cannot read or a malformed
	// stored CIDR denies the login rather than silently allowing all).
	AllowNetwork(ctx context.Context, ip string) error

	// RequireSSO reports whether a PASSWORD login for user must be refused because the
	// scope requires SSO. It is consulted in Authenticator.Login ONLY, after the
	// password is verified and just before the session is minted; the SSO completion
	// path (CompleteSSO) is NEVER routed through here — require-SSO blocks password
	// logins, and permitting SSO is its whole point. nil = permitted. A non-nil error
	// blocks: ErrSSORequired when SSO is required, or a wrapped fail-closed error. The
	// enterprise engine refuses a password login only when an IdP is actually ACTIVE
	// (so a broken/absent IdP can never lock everyone out — anti-lockout); user lets
	// the engine apply any per-account exemption.
	RequireSSO(ctx context.Context, user model.User) error
}

// LoginEnforcementPosture is the RESOLVED, NON-SECRET login-enforcement posture for a
// scope: whether SSO is required, whether an ACTIVE IdP backs that requirement, and
// the network allow-list CIDRs. The open core computes it from the stored
// FederationConfig (FederationService.Posture) for two consumers: the console read
// (so the operator sees the posture and whether THIS build enforces it) and the
// enterprise LoginPolicy (which decides over it). The open build computes and reports
// it but NEVER enforces it — that is the closed engine's job.
type LoginEnforcementPosture struct {
	// RequireSSO is the operator's stored intent to block password login. It is honored
	// only by the enterprise engine and only when HasActiveIdP is true.
	RequireSSO bool
	// HasActiveIdP reports whether an active IdP is configured for the scope (an
	// ACTIVE, non-empty FederationConfig). require-SSO is meaningless — and the engine
	// refuses to enforce it — without one (anti-lockout).
	HasActiveIdP bool
	// NetworkAllowCIDRs is the operator's IP allow-list (CIDR strings). Empty ⇒ no
	// network restriction. Non-secret (public verifier material), so it is safe to
	// return to the console verbatim.
	NetworkAllowCIDRs []string
}

// Configured reports whether the operator has set ANY enforcement intent (require-SSO
// or a non-empty allow-list). An unconfigured posture is the default no-op — the
// console renders it as "no enforcement configured" rather than implying a control is
// off when none was ever set (docs/SECURITY-HARDENING.md: unknown ≠ disabled).
func (p LoginEnforcementPosture) Configured() bool {
	return p.RequireSSO || len(p.NetworkAllowCIDRs) > 0
}

// WithLoginPolicy sets the login-enforcement policy and returns the Authenticator for
// chaining. Like WithSeatPolicy it is called once at boot, before serving, so it is
// race-free. A nil policy (the default) leaves login UNENFORCED — exactly today's
// behavior (no rug-pull).
func (a *Authenticator) WithLoginPolicy(p LoginPolicy) *Authenticator {
	a.loginPolicy = p
	return a
}

// EnforcesLogin reports whether a login-enforcement policy is wired RIGHT NOW, for the
// console posture display (the honest enforced_by signal). false in the open binary
// (no policy linked) and in any embedder/test that did not opt in; true only in the
// enterprise build. It says nothing about whether a posture is configured — only
// whether THIS build is capable of enforcing one.
func (a *Authenticator) EnforcesLogin() bool { return a.loginPolicy != nil }

// enforceNetwork consults the wired login policy's network allow-list, nil-safe. A nil
// policy (the open build / a test embedder) is a no-op (login proceeds), so the cap is
// a binary packaging decision and the test suite is unenforced. Any policy error is
// returned verbatim (deny-closed) for the caller to surface and abort the login.
func (a *Authenticator) enforceNetwork(ctx context.Context, ip string) error {
	if a.loginPolicy == nil {
		return nil
	}
	return a.loginPolicy.AllowNetwork(ctx, ip)
}

// enforceRequireSSO consults the wired login policy's require-SSO rule for a PASSWORD
// login, nil-safe. A nil policy is a no-op. It is the caller's responsibility to invoke
// this ONLY on the password path (never for CompleteSSO).
func (a *Authenticator) enforceRequireSSO(ctx context.Context, user model.User) error {
	if a.loginPolicy == nil {
		return nil
	}
	return a.loginPolicy.RequireSSO(ctx, user)
}

// auditLoginBlocked records a login refused by the enterprise enforcement policy
// (network allow-list or require-SSO) on the security ledger, so a governed estate can
// see WHO was blocked and WHY — the deny-closed counterpart to appendLoginFail. It is
// best-effort (a ledger write failure must not change the already-decided refusal) and
// only ever runs on the enterprise block path (a nil policy never reaches it, so the
// open build is byte-identical). reason is a stable, non-secret code (network_not_allowed
// / sso_required). actor is "anonymous" for an IP-only network block (no user resolved)
// or "user:<id>" once a password was verified.
func (a *Authenticator) auditLoginBlocked(ctx context.Context, actor, ip, reason string) {
	if aerr := a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		_, err := as.Audit().Append(ctx, model.AuditDraft{
			Actor: actor, ActorKind: model.ActorUser, Action: "auth.login.blocked",
			Meta: map[string]any{"ip": ip, "reason": reason},
		})
		return err
	}); aerr != nil {
		a.log.Error("auth: recording blocked login", "err", aerr, "reason", reason)
	}
}
