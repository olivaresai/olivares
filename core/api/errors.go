// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/license"
	"github.com/olivaresai/olivares/core/store"
)

// Sentinel API errors that handlers/middleware raise and writeError maps.
var (
	errForbidden                      = errors.New("forbidden")
	errBadRequest                     = errors.New("bad request")
	errRequestBodyTooLarge            = errors.New("request body too large")
	errTenantRequired                 = errors.New("tenant required")
	errSetupRequired                  = errors.New("setup required")
	errEntityAuthorizationUnavailable = errors.New(
		"entity authorization evidence unavailable",
	)
	// errRateLimited is raised by the inbound rate-limit middleware (OPS-5).
	// It maps to 429 here so REST and gRPC report it identically (gRPC →
	// codes.ResourceExhausted via grpcError); the middleware additionally sets the
	// Retry-After / RateLimit-* headers, which the envelope cannot carry.
	errRateLimited = errors.New("rate limited")
)

// errorBody is the single JSON error envelope for the whole API.
type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// statusFor maps an error to an HTTP status and a stable, non-leaking code. It is
// the ONE place the mapping lives so REST and gRPC cannot diverge. Critically,
// store.ErrNotFound and an other-tenant row are indistinguishable here (both 404)
// so the API is never a cross-tenant existence oracle (errors.go).
func statusFor(err error) (int, string) {
	switch {
	case errors.Is(err, auth.ErrUnauthenticated), errors.Is(err, auth.ErrInvalidCredentials):
		return http.StatusUnauthorized, "unauthenticated"
	case errors.Is(err, auth.ErrSSONotConfigured):
		return http.StatusNotImplemented, "sso_not_configured"
	case errors.Is(err, auth.ErrFederationBuilderUnavailable):
		// this build (default AGPL) can store SSO config but has no provider
		// builder to activate it. 501 honest-seam, like the env path.
		return http.StatusNotImplemented, "sso_builder_unavailable"
	case errors.Is(err, auth.ErrNoFederationSealer):
		// deny-closed — a secret cannot be sealed/opened with no sealer wired.
		return http.StatusServiceUnavailable, "sso_unavailable"
	case errors.Is(err, errSecretStoreUnavailable):
		// the runtime secret store is not wired on this deployment (an
		// embedder/test that did not opt in). 501 honest-seam, like SSO.
		return http.StatusNotImplemented, "secret_store_unavailable"
	case errors.Is(err, auth.ErrNoSecretSealer):
		// deny-closed — a secret cannot be sealed/opened with no sealer wired.
		return http.StatusServiceUnavailable, "secret_store_unavailable"
	case errors.Is(err, auth.ErrSecretNotFound):
		return http.StatusNotFound, "not_found"
	case errors.Is(err, auth.ErrBadSecretName), errors.Is(err, auth.ErrEmptySecretValue):
		return http.StatusBadRequest, "bad_request"
	case errors.Is(err, errSourceRosterUnavailable):
		// the live source-reconfiguration surface is not wired on this
		// deployment (an embedder/test that did not opt in). 501 honest-seam.
		return http.StatusNotImplemented, "source_roster_unavailable"
	case errors.Is(err, errConnectorOnboardingUnavailable):
		// the console connector-onboarding surface is not wired on this
		// deployment (an embedder/test that did not opt in). 501 honest-seam.
		return http.StatusNotImplemented, "connector_onboarding_unavailable"
	case errors.Is(err, errDRUnavailable):
		// the DR console surface is not wired on this deployment. 501.
		return http.StatusNotImplemented, "dr_unavailable"
	case errors.Is(err, errLogBrokerUnavailable):
		// the log broker is not wired on this deployment. 501.
		return http.StatusNotImplemented, "log_broker_unavailable"
	case errors.Is(err, ErrLicenseUnavailable):
		// the live edition/license surface is not wired on this deployment (an
		// embedder/test that did not opt in). 501 honest-seam, like the source roster.
		return http.StatusNotImplemented, "license_unavailable"
	case errors.Is(err, ErrActivationUnavailable):
		// the enterprise activation surface is not wired (community build / not
		// opted in). 501 honest-seam, like the license surface.
		return http.StatusNotImplemented, "activation_unavailable"
	case errors.Is(err, ErrActivationInvalidRequest):
		// a malformed enable/disable/promote (unknown preset or add-on). 400.
		return http.StatusBadRequest, "activation_invalid_request"
	case errors.Is(err, ErrLicenseDowngrade):
		// the explicit acknowledge dialog (Elastic's acknowledge=true) rather
		// than a generic denial. UNREACHABLE since B10 — no license entitles fewer
		// user accounts — but the mapping is retained for wire compatibility.
		return http.StatusConflict, "license_downgrade_requires_acknowledge"
	case errors.Is(err, ErrLicenseManagedExternally):
		// a boot override (--license / OLIVARES_LICENSE[_PATH]) outranks the
		// data-dir file, so a console/CLI install would be shadowed. 409 + distinct
		// code so the console explains the license is managed out-of-band.
		return http.StatusConflict, "license_managed_externally"
	case errors.Is(err, ErrLicenseInvalid):
		// the blob does not verify against this build's embedded key — a paste/
		// format error, refused before persisting. 400.
		return http.StatusBadRequest, "license_invalid"
	case errors.Is(err, ErrConnectorTestFailed):
		// a connectivity test failed — the candidate connector could not be
		// opened with the supplied configuration. 422 (understood, did not succeed);
		// the message is generic by construction (never echoes a resolved secret).
		return http.StatusUnprocessableEntity, "connector_test_failed"
	case errors.Is(err, auth.ErrSourceDefNotFound):
		return http.StatusNotFound, "not_found"
	case errors.Is(err, auth.ErrBadSourceName), errors.Is(err, auth.ErrBadSourceDef):
		return http.StatusBadRequest, "bad_request"
	case errors.Is(err, auth.ErrInviteInvalid):
		// coarse (unknown/expired/used) so the accept leg is no oracle.
		return http.StatusBadRequest, "invite_invalid"
	case errors.Is(err, auth.ErrBadFederationConfig):
		return http.StatusBadRequest, "bad_request"
	case errors.Is(err, auth.ErrBadFederationAlias):
		// U4: a malformed IdP alias slug (per-IdP admin routes). 400 before any
		// store round-trip so a bad path segment never becomes a row or a 404.
		return http.StatusBadRequest, "bad_request"
	case errors.Is(err, auth.ErrScopeActiveIdPExists):
		// U4: a scope already has an active IdP (one active per scope until U5's
		// home-realm-by-domain). Distinct 409 so the console can prompt "deactivate the
		// current IdP first" rather than show a generic conflict.
		return http.StatusConflict, "scope_active_idp_exists"
	case errors.Is(err, auth.ErrGlobalIdPMustBeDefault):
		// U4: the deployment-wide login IdP must use the "default" alias (domain-bearing
		// IdPs live in tenant scopes; the global login resolves the single "default" IdP).
		return http.StatusConflict, "global_idp_must_be_default"
	case errors.Is(err, auth.ErrDomainClaimed):
		// U5: a home-realm email domain is globally unique — already claimed by another
		// IdP. Distinct 409 so the console can point at the conflicting domain.
		return http.StatusConflict, "domain_claimed"
	case errors.Is(err, auth.ErrLockedOut):
		return http.StatusTooManyRequests, "locked_out"
	case errors.Is(err, errRateLimited):
		return http.StatusTooManyRequests, "rate_limited"
	case errors.Is(err, auth.ErrRoleCeiling):
		// Distinct from a plain denial: the actor MAY administer this thing, but
		// not grant a rank above their own. Collapsing it into "forbidden" told
		// an admin mapping OWNER onto a group that they lacked permission
		// entirely, so they would chase a permission they already have instead
		// of asking someone senior. Same reasoning as domain_claimed and the
		// edition codes below.
		return http.StatusForbidden, "role_ceiling"
	case errors.Is(err, errForbidden), errors.Is(err, auth.ErrWorkspaceConfined):
		return http.StatusForbidden, "forbidden"
	case errors.Is(err, store.ErrWorkspaceConfinement), errors.Is(err, store.ErrWorkspaceLineageRequired):
		// B-03: the store refused a row-level operation to a workspace-confined
		// caller. It is a 403 and not a 500 because nothing failed — the answer IS
		// "you may not", and a 500 would send an operator hunting a server fault.
		// The two sentinels share a code: which one fired is an internal distinction
		// (the entity has no lineage vs the row is in another workspace), and telling
		// them apart would let a caller probe the shape of what it cannot see.
		return http.StatusForbidden, "workspace_confined"
	case errors.Is(err, auth.ErrUserCapRequiresEnterprise):
		// UNREACHABLE since B10 (2026-07-27): self-hosted user accounts are
		// unlimited in every tier, so core/auth never returns this error any more
		// (seatcap.go). The mapping is retained — never deleted — so the wire code
		// keeps its meaning for any client that still switches on it.
		return http.StatusForbidden, "user_cap_requires_enterprise"
	case errors.Is(err, license.ErrAddonRequiresLicense):
		// A commercial ADD-ON operation refused for want of an entitlement (the
		// AddonGate). 403 + a distinct code, exactly like multi_idp above and for the
		// same reason: without it the refusal fell through to `default:` and reached
		// the caller as a generic 500 "internal", which tells an operator their server
		// is broken when in fact their license lapsed.
		//
		// THIS DOES NOT MAKE THE OPEN BINARY A GATE. Nothing in the open build ever
		// constructs this error — the add-ons it refuses do not exist here — so the arm
		// is inert in the default artifact, in the same way user_cap_requires_enterprise
		// stayed mapped after B10 made it unreachable. Only the closed enterprise build
		// decides (LICENSING.md §ADR-0010); this is where its decision becomes one status
		// code instead of many. And /v1/console/license is untouched: it still answers
		// 200 in every commercial state (handlers_license_polarity_test.go).
		return http.StatusForbidden, "addon_requires_license"
	case errors.Is(err, auth.ErrMultiIDPRequiresEnterprise):
		// A second active SSO IdP is the reserved enterprise line. Distinct
		// code so the console shows an edition prompt (it previously fell through
		// to a generic 500).
		return http.StatusForbidden, "multi_idp_requires_enterprise"
	case errors.Is(err, auth.ErrSSORequired):
		// password login refused because the deployment requires SSO. This is a
		// deny-closed SECURITY control the operator turned on (NOT an edition prompt
		// like multi_idp/user_cap): the body tells the human to sign in with SSO, never
		// to upgrade. 403 + distinct code so the login UI routes to the SSO button.
		return http.StatusForbidden, "sso_required"
	case errors.Is(err, auth.ErrNetworkNotAllowed):
		// login refused because the peer IP is outside the deployment's network
		// allow-list (a deny-closed perimeter control over the login surface). 403 +
		// distinct code; the body never reveals the allow-list (no recon oracle).
		return http.StatusForbidden, "network_not_allowed"
	case errors.Is(err, auth.ErrLastSuperadmin):
		// deny-closed against total lockout — disabling the last ACTIVE
		// superadmin is refused. 409 (conflicts with the keep-one-active-superadmin
		// invariant); distinct code so the console explains it must keep/provision
		// another active superadmin first, not a generic authz denial.
		return http.StatusConflict, "last_superadmin"
	case errors.Is(err, auth.ErrNotSuperadmin):
		// the superadmin enable/disable surface was targeted at a non-superadmin
		// account (tenant members use the SCIM/onboarding lifecycle). 409, distinct
		// code so the console routes to the right surface.
		return http.StatusConflict, "not_superadmin"
	case errors.Is(err, auth.ErrGroupCycle):
		// S256: nesting a group under another would make it its own ancestor. 409
		// (conflicts with the acyclic-forest invariant); distinct code so the console
		// explains the chosen parent is already a descendant.
		return http.StatusConflict, "group_cycle"
	case errors.Is(err, ErrRecordingConsentRequired):
		// the operator has not acknowledged the recording notice for this
		// privileged surface. Distinct code so the console can show the consent
		// dialog instead of a generic authz denial.
		return http.StatusForbidden, "recording_consent_required"
	case errors.Is(err, errRecordingUnavailable):
		// the session recorder could not gate/persist evidence. Privileged
		// surfaces are deny-closed without recording — 503, retryable.
		return http.StatusServiceUnavailable, "recording_unavailable"
	case errors.Is(err, errEntityAuthorizationUnavailable):
		// A concealed entity route could not establish its stored scope/existence.
		// Returning 404 would turn UNKNOWN into a false negative; returning 403 would
		// confuse evidence loss with a clean authorization denial. Retry deny-closed.
		return http.StatusServiceUnavailable, "entity_authorization_unavailable"
	case errors.Is(err, auth.ErrStepUpRequired):
		// the action demands a higher session assurance (AAL3). Distinct
		// code so the console can route the operator to the step-up ceremony
		// instead of a generic authz denial.
		return http.StatusForbidden, "step_up_required"
	case errors.Is(err, auth.ErrWebAuthnVerification):
		// a WebAuthn/PIV ceremony failed verification. 403 (not 401: the
		// SESSION is still valid — only the elevation was refused).
		return http.StatusForbidden, "webauthn_verification_failed"
	case errors.Is(err, auth.ErrPIVVerification):
		return http.StatusForbidden, "piv_verification_failed"
	case errors.Is(err, auth.ErrPIVNotConfigured):
		// the PIV/CAC route is not configured on this deployment. 501 is
		// the honest-seam signal (the panel renders "backend pending").
		return http.StatusNotImplemented, "piv_not_configured"
	case errors.Is(err, auth.ErrNoWebAuthnCredential):
		return http.StatusBadRequest, "no_webauthn_credential"
	case errors.Is(err, auth.ErrLastWebAuthnCredential):
		return http.StatusConflict, "last_webauthn_credential"
	case errors.Is(err, store.ErrResidencyViolation):
		// a tenant pinned to another region was reached on this region-scoped
		// instance. Deny-closed, distinct code so a client/operator sees it is a
		// residency decision, not a generic authz denial.
		return http.StatusForbidden, "residency_violation"
	case errors.Is(err, store.ErrTenantNotInService):
		// The tenant's org row carries a status that is neither active nor suspended.
		// Locked like a suspension — the work must not run — but under its OWN code:
		// no commercial decision was recorded, and rendering it as one sends an
		// operator after a billing problem that does not exist.
		return http.StatusLocked, "tenant_not_in_service"
	case errors.Is(err, store.ErrTenantSuspended):
		// the tenant's service has been withdrawn (orgs.status). 423 Locked,
		// matching the kill-switch posture already used in this codebase for "denied
		// because a stop is engaged" — NOT 403 (this is not an authorization
		// decision about the caller, whose credentials are perfectly valid) and NOT
		// 402 (the engine holds no commercial policy: WHY a tenant was suspended is
		// the control plane's business). The distinct code lets a console render
		// "your service is suspended" instead of a generic denial.
		return http.StatusLocked, "tenant_suspended"
	case errors.Is(err, errSetupRequired):
		return http.StatusConflict, "setup_required"
	case errors.Is(err, auth.ErrSetupComplete):
		return http.StatusConflict, "setup_complete"
	case errors.Is(err, auth.ErrCoordinationUnavailable):
		// A decision that must serialize across the cluster refused because it
		// could not take its lock. NOTHING was written, so this is transient and
		// retryable — 503, not the 409 "setup_complete" next to it. Telling an
		// operator the work was already done when it was not would send them
		// hunting for state that does not exist.
		return http.StatusServiceUnavailable, "coordination_unavailable"
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrUnknownEntity):
		return http.StatusNotFound, "not_found"
	case errors.Is(err, store.ErrConflict):
		return http.StatusConflict, "conflict"
	case errors.Is(err, store.ErrNotLeader):
		// HA standby: a write reached a non-leader node (it should have been
		// drained by /readyz). 503 so the caller retries against the current leader —
		// a transient, retryable condition, not a client error.
		return http.StatusServiceUnavailable, "not_leader"
	case errors.Is(err, store.ErrAuditSpoolFull):
		// ADR-0024 Q2: evidence integrity outranks write availability. Reads remain
		// serviceable, but governed actions fail deny-closed until the operator
		// restores spool capacity, so 503 describes an actionable transient state.
		return http.StatusServiceUnavailable, "audit_spool_full"
	case errors.Is(err, auth.ErrWeakPassword):
		return http.StatusBadRequest, "weak_password"
	case errors.Is(err, errRequestBodyTooLarge):
		return http.StatusRequestEntityTooLarge, "request_body_too_large"
	case errors.Is(err, auth.ErrInvalidRole), errors.Is(err, auth.ErrInvalidToken),
		errors.Is(err, store.ErrCursorWithSort), errors.Is(err, errBadRequest), errors.Is(err, errTenantRequired):
		return http.StatusBadRequest, "bad_request"
	case errors.Is(err, store.ErrEnumerationNotAuthoritative):
		//. NOT an internal invariant violation, which is why it does not belong
		// in the arm below. The store answered CORRECTLY and ON PURPOSE: Made it
		// refuse to report an RLS-limited read as the whole estate, and `authoritative`
		// is derived from this deployment's CONFIGURATION, not from broken state
		// (sqlstore/system.go ListOrgsVisible). What is missing is an operator step:
		// a NOSUPERUSER BYPASSRLS role behind --admin-dsn.
		//
		// Falling through to `default:` made the FIRST BOOT fail mute. Measured against
		// Postgres 16 on 2026-08-08: POST /v1/setup answered 500
		// {"error":{"code":"internal","message":"internal error"}} while the sentence
		// naming the role file AND the flag — which the engine already writes
		// (sqlstore/system.go ListOrgs) and already warns about at boot (boot.go) —
		// stayed in the server log. That is precisely the failure the
		// addon_requires_license arm above exists to prevent: an operator told their
		// server is broken when in fact their deployment is incomplete.
		//
		// 501 rather than 503, because 503 promises a retry will help (RFC 9110
		// §15.6.4: a TEMPORARY inability that will likely be alleviated after a
		// delay) and this condition never self-heals — it takes a provisioned role
		// and a restart.
		//
		// The nearest 501 neighbors are sso_not_configured and piv_not_configured:
		// the capability is absent from this deployment. The 503 band is where an
		// OPERATION is refused deny-closed right now (not_leader, audit_spool_full).
		// That line is not perfectly clean and this comment should not pretend it is
		// — sso_unavailable and secret_store_unavailable are 503 for a missing
		// sealer, which is also a wiring gap that no retry fixes. The distinction
		// applied here is capability-absent (501) vs operation-refused (503), and
		// under it an unmakeable authoritative read is the former.
		return http.StatusNotImplemented, "cross_tenant_admin_pool_not_configured"
	case errors.Is(err, store.ErrNoTenant), errors.Is(err, store.ErrReadOnly),
		errors.Is(err, store.ErrAppendOnly), errors.Is(err, store.ErrTenantViolation):
		// These are internal invariant violations, never a client's fault.
		return http.StatusInternalServerError, "internal"
	default:
		return http.StatusInternalServerError, "internal"
	}
}

// honestSeamMessage is the client-facing sentence for a DELIBERATE 5xx refusal —
// a capability this build does not carry (501) or one that is wired deny-closed
// (503). It is keyed on the CODE, which statusFor already curates, so it can never
// echo a wrapped error's text and therefore can never leak an internal detail.
//
// A code with no entry falls back to a humanized form of the code itself. That
// fallback is the point: the failure mode being closed here is "the operator is
// told `internal error` and has nothing to act on", and a new sentinel added
// tomorrow must not silently reintroduce it.
var honestSeamMessage = map[string]string{
	"sso_not_configured":               "SSO is not configured on this deployment.",
	"sso_builder_unavailable":          "This build can store SSO configuration but has no provider builder to activate it.",
	"sso_unavailable":                  "SSO is unavailable: no secret sealer is wired, so an SSO secret cannot be sealed or opened.",
	"secret_store_unavailable":         "The runtime secret store is not wired on this deployment.",
	"source_roster_unavailable":        "The durable source roster is not wired on this deployment.",
	"connector_onboarding_unavailable": "Connector onboarding is not wired on this deployment.",
	"dr_unavailable":                   "Disaster-recovery operations are not wired on this deployment.",
	"log_broker_unavailable":           "The log broker is not wired on this deployment.",
	"license_unavailable":              "License operations are not wired on this deployment.",
	"activation_unavailable":           "Activation is not wired on this deployment.",
	"recording_unavailable":            "Session recording is not wired on this deployment.",
	"entity_authorization_unavailable": "Entity authorization evidence is temporarily unavailable.",
	"piv_not_configured":               "Privileged login (PIV) is not configured on this deployment.",
	"not_leader":                       "This node is not the leader; retry against the leader.",
	"audit_spool_full":                 "The audit spool is full; the ledger is refusing writes deny-closed rather than dropping evidence.",
	// the ONE sentence a first-boot operator needs. It names the remedy in
	// full — which role, which command, which flag — because the alternative
	// measured on 2026-08-08 was "internal error" on POST /v1/setup with the remedy
	// sitting in a server log the person configuring the install is not reading.
	//
	// The command is quoted in a form that RUNS AS WRITTEN, and that is the second
	// lesson here rather than a detail. The first draft of this sentence offered
	// "deploy/postgres/01-app-role.sql, or `olivares db init --admin-role`", and the
	// external contrast showed that NEITHER provisions the role: that .sql file's
	// admin-role block is commented out end to end, and a bare --admin-role exits
	// with "flag needs an argument". A remedy that does not run is the same defect
	// as no remedy, one step further along — and the tests did not see it because
	// they asserted substrings, not executability. Measured working before landing.
	// THE REMEDY MUST NOT UNDO THE DEPLOYMENT IT IS REPAIRING, which is the third lesson on
	// this one line. The command above ran, and it was still wrong on the deployments that
	// took our own least-privilege advice: `db init` defaults --owner-role to EMPTY, meaning
	// "the app role owns the schema", and --database to "olivares", and it executes
	// `ALTER DATABASE <db> OWNER TO <owner>` (sqlstore/dbsetup.go:608). So on an estate
	// provisioned with the documented owner/app split, pasting the short form REASSIGNS THE
	// DATABASE to the application role and dissolves the split — from inside the error
	// message the operator reads while already in trouble, which is the worst place to put a
	// destructive default. The flags are named explicitly now, with the caution that they
	// must match what is deployed; a remedy that runs is necessary and is not sufficient.
	"cross_tenant_admin_pool_not_configured": "This deployment has no cross-tenant admin database pool, so a read across all tenants cannot be made authoritatively. Provision a NOSUPERUSER BYPASSRLS role — `olivares db init --superuser-dsn <dsn> --database <your db> --app-role <your app role> --owner-role <your owner role, omit only if the app role owns the schema> --admin-role olivares_admin --admin-password-file <file>` (see deploy/postgres/README.md) — and restart the server with --admin-dsn pointing at that role. The --database/--app-role/--owner-role values must match what is already deployed: db init runs ALTER DATABASE ... OWNER TO, so a short form that omits them can hand ownership to the wrong role.",
}

// humanisedCode turns a curated snake_case code into a sentence. It exists so a
// sentinel nobody added to honestSeamMessage still produces something an operator
// can act on, instead of falling back to "internal error".
func humanisedCode(code string) string {
	if code == "" {
		return "This capability is unavailable on this deployment."
	}
	words := strings.ReplaceAll(code, "_", " ")
	return strings.ToUpper(words[:1]) + words[1:] + "."
}

// writeError writes the mapped error as JSON.
//
// A GENUINE 500 logs the underlying error and returns a generic message, because
// an internal invariant violation has nothing safe or useful to say to a client.
//
// A 501 or a 503 is the opposite kind of event and used to be treated the same
// (2026-08-05): the condition was `status >= 500`, so all fifteen honest-seam and
// deny-closed branches of statusFor had their message replaced with "internal
// error" AND were logged as `api: request failed`. Measured — `auth: SSO not
// configured` reached the client as 501 `{"code":"sso_not_configured","message":
// "internal error"}` with an ERROR line behind it.
//
// Two consequences, both bad in a product whose whole posture is saying out loud
// what it will not do: the operator was told nothing actionable about a refusal
// the engine had made ON PURPOSE, and a deliberate deny-closed answer was logged
// at ERROR next to real faults, which is how operators learn to ignore the log.
func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code := statusFor(err)
	msg := err.Error()
	switch {
	case status == http.StatusInternalServerError:
		s.log.Error("api: request failed", "err", err, "path", r.URL.Path, "request_id", requestID(r.Context()))
		msg = "internal error"
	case status > http.StatusInternalServerError:
		// Deliberate: not implemented here, or wired deny-closed. Never echo the
		// error's own text — it may be a wrapper carrying internals — but never
		// say "internal error" either.
		if m, ok := honestSeamMessage[code]; ok {
			msg = m
		} else {
			msg = humanisedCode(code)
		}
		// raised by the external contrast: 501 is HEURISTICALLY CACHEABLE
		// (RFC 9110 §15.6.2), and writeJSON sets no cache directives (render.go), so
		// nothing stopped a private cache from storing one of these. That is actively
		// harmful for this whole band and not merely untidy: every message here
		// describes a DEPLOYMENT STATE the operator is being told to go and change,
		// so the one response that must never be replayed is the one saying "not
		// configured" to someone who has just configured it. A shared cache is
		// already held off by Authorization (RFC 9111 §3.5); a private one was not.
		w.Header().Set("Cache-Control", "no-store")
		s.log.Info("api: refused by design", "code", code, "status", status,
			"path", r.URL.Path, "request_id", requestID(r.Context()))
	}
	var body errorBody
	body.Error.Code = code
	body.Error.Message = msg
	writeJSON(w, status, body)
}

// badRequest is a small helper for malformed-input responses.
func (s *Server) badRequest(w http.ResponseWriter, r *http.Request, msg string) {
	var body errorBody
	body.Error.Code = "bad_request"
	body.Error.Message = msg
	_ = r
	writeJSON(w, http.StatusBadRequest, body)
}

// forbidden writes a 403 with a caller-supplied message (unlike errForbidden, which
// carries the generic "forbidden" text).
func (s *Server) forbidden(w http.ResponseWriter, r *http.Request, msg string) {
	var body errorBody
	body.Error.Code = "forbidden"
	body.Error.Message = msg
	_ = r
	writeJSON(w, http.StatusForbidden, body)
}
