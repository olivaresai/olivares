// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// rsconfig.go builds the inline MCP Resource Server (PEP) from operator config. The RS
// is an OAuth 2.1 Resource Server (RFC 9728 / 8707 / 9068) mounted IN FRONT OF an MCP
// dispatcher: it serves Protected Resource Metadata, validates every bearer
// audience-bound and fail-closed, gates tools/call deny-by-default, and forwards
// admitted/gated calls to the upstream WITHOUT passing the inbound token through. Every
// governance/actuation seam defaults to its deny-closed value, so a misconfigured RS
// serves metadata and rejects tokens but can NEVER actuate.

// wellKnownProtectedResource is the RFC 9728 Protected Resource Metadata path.
const wellKnownProtectedResource = "/.well-known/oauth-protected-resource"

// ResourceServerConfig is the operator configuration for one inline MCP Resource
// Server. Resource is THIS server's URL (canonicalized to the RFC 8707 resource URI
// that every accepted token MUST name in its audience). Token trust is ISSUER-KEYED
// (RFC 9068 §4): every accepted token MUST name a configured trusted issuer,
// and each issuer brings its OWN anchors — Issuers carries the full multi-issuer
// form; the legacy single-issuer fields (Issuer + IssuerJWKS/JWKSURL/
// IntrospectionURL/IntrospectionAuth) are folded in as one more entry. An RS without
// at least one trusted ISSUER (not merely a trust anchor) no longer constructs —
// there is no skip-the-iss-check mode. Toolset is the server-owned tool→scope policy
// (nil ⇒ deny ALL tools/call). The Gate / Upstream / Auditor are governance seams
// (nil ⇒ deny-closed / no-op). Secret-bearing fields (IntrospectionAuth) are operator
// config, never logged.
type ResourceServerConfig struct {
	Resource             string
	AuthorizationServers []string
	ScopesSupported      []string
	// Issuers is the issuer-keyed trust configuration (preferred form). Each entry
	// names the EXACT iss value it trusts plus that issuer's own anchors.
	Issuers []IssuerTrust
	// Legacy single-issuer fields (config shape): folded into Issuers as
	// the first entry. Issuer is now REQUIRED when any of the other three is set.
	Issuer            string
	IssuerJWKS        []byte
	JWKSURL           string
	IntrospectionURL  string
	IntrospectionAuth string
	Toolset           *Toolset
	AllowedOrigins    []string
	Tenant            string
	// UpstreamDescriptor is the STABLE identity of the wired upstream backend +
	// credential profile (round-2) — e.g. the upstream base URL plus the
	// credential-provider identity, or "in-process:governed-retrieval". It is
	// bound into every tools/call EffectDigest so a re-pointed backend changes
	// the effect identity (a keyed retry rebinds instead of replaying). Identity
	// fields only, NEVER secrets. Empty = no descriptor (explicit stable absence).
	UpstreamDescriptor string
	Gate               ApprovalGate
	TaskGate           TaskGate
	// DurableTaskStore is the source of truth for the optional MCP Tasks
	// extension. Nil keeps ordinary standalone MCP forwarding available but
	// disables task creation and task methods; task capability advertisements
	// are removed from initialize/server-discover responses.
	DurableTaskStore DurableTaskStore
	Upstream         Upstream
	// SubscriptionUpstream is the streaming, no-token-passthrough counterpart
	// used exclusively by subscriptions/listen. When nil, NewResourceServer also
	// accepts cfg.Upstream if it implements SubscriptionUpstream. A listen request
	// still requires SubscriptionLedger; either missing seam returns 503.
	SubscriptionUpstream SubscriptionUpstream
	// SubscriptionLedger durably commits each relayed notification and its opaque
	// cursor before the byte is emitted downstream. Nil disables the relay
	// deny-closed while leaving ordinary request/response MCP forwarding intact.
	SubscriptionLedger SubscriptionLedger
	Auditor            GateAuditor
	Clock              func() time.Time
	Doer               httpDoer
	// MaxActiveTasksPerSubject bounds active durable task handles tracked for one
	// subject. 0 means the default (32). The ledger also carries a bounded global cap.
	MaxActiveTasksPerSubject int
	// --- RFC 9728 Protected Resource Metadata completeness + RFC 9068 strictness.
	// ResourceName / ResourceDocumentation are optional human-readable PRM fields
	// (RFC 9728 §3.2) advertised when set. RequireATJWT turns on strict RFC 9068
	// validation: an inbound JWT MUST carry typ=at+jwt (defense-in-depth on top of the
	// always-on audience binding — rejects an id_token/other-typ token replayed as an
	// access token). It defaults OFF because not every compliant AS stamps the typ.
	ResourceName          string
	ResourceDocumentation string
	RequireATJWT          bool
	// RequireDPoP demands a DPoP-bound token on every authenticated request: an
	// unbound bearer, or a DPoP-bound token without a matching proof, is refused.
	RequireDPoP bool
	// RequireDPoPNonce additionally requires each DPoP proof to carry the current
	// RS nonce. Clients self-heal by retrying after the use_dpop_nonce challenge.
	RequireDPoPNonce bool
	// AcceptMTLSBoundTokens enables RFC 8705 certificate-bound access-token
	// verification against r.TLS.PeerCertificates[0]. It only works when this
	// process terminates TLS with client-cert negotiation; behind a TLS-terminating
	// proxy the peer certificate is not visible and bound tokens are refused
	// fail-closed.
	AcceptMTLSBoundTokens bool
	// RoleClaim is the token claim the RS reads roles from for the E1 PER-ROLE tool
	// allowlist (a `roles` array/string claim). Empty ⇒ "roles". The Claude MCP API has no
	// native role abstraction; this drives the control plane's PEP-side role gate.
	RoleClaim string
	// UpstreamRevision is the MCP protocol revision the WIRED UPSTREAM speaks, as
	// declared by the OPERATOR. The reconciliation read synthesizes a
	// Tasks-extension request, and the Tasks extension (SEP-2663) belongs to the
	// 2026-07-28 revision — an upstream configured as an older revision gets the
	// read REFUSED rather than a fabricated legacy request (round-5 R5-05).
	//
	// ROUND-6 R6-06, said accurately: this is CONFIGURATION, not negotiation. The
	// gateway does not discover what the upstream speaks, and an empty value ASSUMES
	// the connector's baseline (2026-07-28) rather than asking. The safety direction
	// still holds — a declared legacy revision is refused, and an upstream that
	// answers a different revision gets no fallback request, so the read fails and
	// the record stays retained — but an operator who mis-declares the revision gets
	// a refusal. Correcting it normally rebuilds the RS; a wired DurableTaskStore
	// rehydrates registered inventory, while process-only reconciliation state is
	// intentionally not reconstructed. An unknown dated revision fails
	// construction deny-closed.
	UpstreamRevision string
	// RevisionMode controls MCP revision handling per request: "dual" (default)
	// accepts the 2026-07-28 frozen-RC header path and known older dated
	// revisions with legacy body validation, "rc-strict" enforces the RC headers
	// exactly, and "legacy" preserves 2025-11-25 behavior. Unknown values fail
	// construction deny-closed.
	RevisionMode string
	// DisableNextRevisionHeaders is the legacy compatibility knob. When
	// RevisionMode is empty, true maps to "legacy" and false maps to "dual".
	// Prefer RevisionMode for new config.
	DisableNextRevisionHeaders bool
	// UITemplates is the server-owned MCP App template policy (SEP-1865):
	// the PRE-DECLARED ui:// inventory the RS serves, deny-by-default — a
	// resources/read of an undeclared ui:// URI is refused, exactly like a tool
	// absent from the Toolset. nil/empty ⇒ deny ALL ui:// reads.
	UITemplates []UITemplatePolicy
	// Consent is the consent-tracking seam for consent-gated template renders
	// (nil ⇒ deny-closed: a require_consent template never renders un-wired).
	Consent ConsentStore
	// PinVerifier verifies that a tool's current fingerprint matches its
	// approved pin before a tools/call is forwarded. Nil = no pin verification
	// (community build; the gate is additive — tools/call proceeds exactly as
	// before when unset). The enterprise implementation stores pins, compares
	// fingerprints, and re-pins on operator approval.
	PinVerifier ToolPinVerifier
	// RenderInspector inspects the HTML body of a rendered MCP App
	// template before it is forwarded to the client. Nil = no deep content
	// inspection (the render-gate + consent + inventory deny-closed all keep
	// working — no rug-pull). The enterprise implementation runs the
	// prompt-injection / exfiltration / unsafe-action detectors on the HTML.
	RenderInspector RenderInspector
	// ElicitationMediator governs runtime elicitation and sampling
	// messages (the "future seam" surface.go named). Nil = no mediation (the
	// surface.go detective still inventories the capability — no rug-pull).
	ElicitationMediator ElicitationMediator
	// COAZEvaluator evaluates tools/call requests against the AuthZEN
	// PDP using the COAZ profile (MCP tool authorization). When wired, the RS
	// queries the PDP after toolset/scope/role checks and before HITL. Nil = no
	// COAZ evaluation (the community build; existing gates keep working).
	COAZEvaluator COAZEvaluator
}

// ResourceServer is the inline MCP PEP: an http.Handler serving RFC 9728 metadata and
// gating the JSON-RPC endpoint. Build it with NewResourceServer; it is safe for
// concurrent use.
type ResourceServer struct {
	resource       string // canonical resource URI (the mandatory token audience)
	metadataURL    string // absolute URL of the served PRM document (for WWW-Authenticate)
	authServers    []string
	scopes         []string
	resourceName   string // optional human-readable PRM resource_name (RFC 9728 §3.2)
	resourceDocs   string // optional PRM resource_documentation URL (RFC 9728 §3.2)
	validator      *tokenValidator
	toolset        *Toolset
	allowedOrigins map[string]struct{}
	tenant         string
	// upstreamDescriptor: the stable upstream/credential identity bound into the
	// tools/call EffectDigest (round-2); "" = explicit absence.
	upstreamDescriptor   string
	gate                 ApprovalGate
	taskGate             TaskGate
	upstream             Upstream
	subscriptionUpstream SubscriptionUpstream
	subscriptionLedger   SubscriptionLedger
	auditor              GateAuditor
	now                  func() time.Time
	requireDPoP          bool
	requireDPoPNonce     bool
	acceptMTLSBound      bool
	dpopReplay           *dpopReplayCache
	dpopNonces           *dpopNonceManager
	taskLedger           *taskLedger
	// durableTasks is the authority behind the optional Tasks extension. When it
	// is non-nil taskLedger is a restart-rehydratable cache, never the authority.
	durableTasks DurableTaskStore
	// revisionMode: "dual", "rc-strict" or "legacy" (frozen-RC alignment).
	revisionMode string
	// upstreamRevision: the RECORDED protocol revision of the wired upstream
	// (round-5 R5-05) — what the gateway synthesizes Tasks requests for.
	upstreamRevision string
	// instanceID identifies THIS gateway process in the operator reconciliation
	// surface. Durable task rows can be rehydrated, but the cache snapshot,
	// cancellation bars and process-only artifacts belong to one instance. A
	// continuation cursor issued by one instance must never be honored by another.
	instanceID string
	// cursorKey is the per-process secret that AUTHENTICATES the reconciliation
	// pagination cursor (round-6 R6-03). Round-5 issued a bare base64 JSON token
	// carrying a SELF-REPORTED instance, so any caller that had seen one list
	// response could hand-build a cursor for an arbitrary position and start a
	// traversal wherever it liked. The token is now versioned and MAC'd with this
	// key: it is only ever a position THIS process issued. The key never leaves the
	// process and is never persisted — a restart invalidates outstanding cursors
	// because it builds a new cache snapshot, even when durable rows rehydrate.
	cursorKey []byte
	// apps: the SEP-1865 ui:// template policy; nil ⇒ deny all ui:// reads.
	apps *appSet
	// consent: the consent-tracking seam for consent-gated renders (deny-closed).
	consent ConsentStore
	// pinVerifier: the tool-pin seam; nil = no verification (community build).
	pinVerifier ToolPinVerifier
	// renderInspector: the render content-inspection seam; nil = no inspection.
	renderInspector RenderInspector
	// elicitationMediator: the elicitation/sampling runtime PEP; nil = no mediation.
	elicitationMediator ElicitationMediator
	// coazEvaluator: the COAZ evaluator; nil = no AuthZEN evaluation (community build).
	coazEvaluator COAZEvaluator
}

// NewResourceServer builds the PEP, validating the config and defaulting every seam to
// its safe value. It errors when Resource is missing/unparseable, when no trusted
// ISSUER is configured (the iss check is mandatory and fail-closed — a bare
// trust anchor with no issuer identifier is a configuration error, not a skip), or
// when an offline_access scope leaks into the advertised/enforced scopes (SEP-2207:
// refresh tokens are a client↔AS concern, never a resource requirement).
func NewResourceServer(cfg ResourceServerConfig) (*ResourceServer, error) {
	resource, err := canonicalResourceURI(cfg.Resource)
	if err != nil {
		return nil, fmt.Errorf("mcp: rs: %w", err)
	}
	if len(cfg.AuthorizationServers) == 0 {
		return nil, fmt.Errorf("mcp: rs: at least one authorization_servers entry is required (RFC 9728)")
	}

	// Fold the legacy single-issuer fields into the issuer-keyed form. A legacy
	// config that sets anchors but NO issuer fails in newIssuerKeyring with the
	// explicit RFC 9068 message — the fail-closed change of behavior. A JSON
	// `null` passed through a RawMessage is NOT a configured anchor (it must not
	// activate the fold and demand a legacy issuer when only issuers[] is meant).
	legacyJWKS := jwksConfigured(cfg.IssuerJWKS)
	issuers := make([]IssuerTrust, 0, len(cfg.Issuers)+1)
	if strings.TrimSpace(cfg.Issuer) != "" || legacyJWKS ||
		strings.TrimSpace(cfg.JWKSURL) != "" || strings.TrimSpace(cfg.IntrospectionURL) != "" {
		entry := IssuerTrust{
			Issuer:            strings.TrimSpace(cfg.Issuer),
			JWKSURL:           cfg.JWKSURL,
			IntrospectionURL:  cfg.IntrospectionURL,
			IntrospectionAuth: cfg.IntrospectionAuth,
		}
		if legacyJWKS {
			entry.JWKS = cfg.IssuerJWKS
		}
		issuers = append(issuers, entry)
	}
	issuers = append(issuers, cfg.Issuers...)
	keyring, err := newIssuerKeyring(issuers)
	if err != nil {
		return nil, err
	}

	// SEP-2207: offline_access is the refresh-token scope a CLIENT may negotiate with
	// its AS; a resource server advertising it in scopes_supported (or demanding it
	// for a tool) would push clients to request refresh tokens as if they were a
	// resource permission. Refuse the config loudly rather than serve it.
	if err := rejectOfflineAccess(cfg.ScopesSupported, cfg.Toolset.requiredScopes()); err != nil {
		return nil, err
	}
	revisionMode, err := resolveResourceServerRevisionMode(cfg)
	if err != nil {
		return nil, err
	}
	upstreamRevision, err := resolveUpstreamRevision(cfg)
	if err != nil {
		return nil, err
	}
	instanceID, err := newInstanceID()
	if err != nil {
		return nil, fmt.Errorf("mcp: rs: initialize instance identity: %w", err)
	}
	cursorKey := make([]byte, 32)
	if _, err := rand.Read(cursorKey); err != nil {
		return nil, fmt.Errorf("mcp: rs: initialize reconciliation cursor key: %w", err)
	}

	doer := cfg.Doer
	if doer == nil {
		doer = ssrfSafeClient()
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	dpopNonces, err := newDPoPNonceManager()
	if err != nil {
		return nil, fmt.Errorf("mcp: rs: initialize dpop nonce key: %w", err)
	}

	tv := &tokenValidator{
		resource:    resource,
		keyring:     keyring,
		requireType: cfg.RequireATJWT,
		roleClaim:   strings.TrimSpace(cfg.RoleClaim),
		doer:        doer,
		now:         clock,
	}

	apps, err := newAppSet(cfg.UITemplates)
	if err != nil {
		return nil, err
	}

	rs := &ResourceServer{
		resource:    resource,
		metadataURL: metadataURLFor(cfg.Resource),
		authServers: append([]string(nil), cfg.AuthorizationServers...),
		// RFC 9728 scopes_supported is the UNION of the operator's explicit list and
		// the scopes the toolset actually enforces, so ADVERTISED scopes are never less
		// than ENFORCED scopes (a client can discover every scope it may be challenged
		// for at step-up; advertised==enforced is a governance-correctness invariant).
		//
		// Round-5 R5-03: that invariant covers the OPERATOR reconciliation scope
		// too. `tasks:reconcile` is enforced by this RS on every tasks/reconcile/*
		// request, but it is not a tool scope and an operator had no way to discover
		// it — the recovery surface was unreachable by discovery even for someone
		// entitled to it. Advertising it grants nothing (the token still has to carry
		// it, minted by the AS).
		scopes:               unionScopes(cfg.ScopesSupported, cfg.Toolset.requiredScopes(), []string{scopeTasksReconcile}),
		resourceName:         strings.TrimSpace(cfg.ResourceName),
		resourceDocs:         strings.TrimSpace(cfg.ResourceDocumentation),
		validator:            tv,
		toolset:              cfg.Toolset,
		allowedOrigins:       originSet(cfg.AllowedOrigins),
		tenant:               cfg.Tenant,
		upstreamDescriptor:   strings.TrimSpace(cfg.UpstreamDescriptor),
		gate:                 cfg.Gate,
		taskGate:             cfg.TaskGate, // nil OK: durable task creation proceeds (additive)
		upstream:             cfg.Upstream,
		subscriptionUpstream: cfg.SubscriptionUpstream,
		subscriptionLedger:   cfg.SubscriptionLedger,
		auditor:              cfg.Auditor,
		now:                  clock,
		requireDPoP:          cfg.RequireDPoP,
		requireDPoPNonce:     cfg.RequireDPoPNonce,
		acceptMTLSBound:      cfg.AcceptMTLSBoundTokens,
		dpopReplay:           newDPoPReplayCache(dpopReplayMaxJTI),
		dpopNonces:           dpopNonces,
		taskLedger:           newTaskLedger(cfg.MaxActiveTasksPerSubject, clock),
		durableTasks:         cfg.DurableTaskStore,
		revisionMode:         revisionMode,
		upstreamRevision:     upstreamRevision,
		instanceID:           instanceID,
		cursorKey:            cursorKey,
		apps:                 apps,
		consent:              cfg.Consent,
		pinVerifier:          cfg.PinVerifier,         // nil OK: no verification (additive)
		renderInspector:      cfg.RenderInspector,     // nil OK: no deep inspection
		elicitationMediator:  cfg.ElicitationMediator, // nil OK: no mediation
		coazEvaluator:        cfg.COAZEvaluator,       // nil OK: no COAZ evaluation
	}
	if rs.gate == nil {
		rs.gate = denyApprovalGate{}
	}
	if rs.upstream == nil {
		rs.upstream = denyUpstream{}
	}
	if rs.subscriptionUpstream == nil {
		if streaming, ok := cfg.Upstream.(SubscriptionUpstream); ok {
			rs.subscriptionUpstream = streaming
		}
	}
	if rs.auditor == nil {
		rs.auditor = nopGateAuditor{}
	}
	if rs.consent == nil {
		rs.consent = denyConsentStore{}
	}
	if rs.durableTasks != nil {
		if err := rs.rehydrateDurableTasks(context.Background()); err != nil {
			return nil, fmt.Errorf("mcp: rs: rehydrate durable tasks: %w", err)
		}
	}
	return rs, nil
}

const (
	revisionModeDual     = "dual"
	revisionModeRCStrict = "rc-strict"
	revisionModeLegacy   = "legacy"
)

// resolveUpstreamRevision validates the OPERATOR-DECLARED upstream protocol
// revision. An unknown string fails construction (round-5 R5-05: the
// reconciliation read must know which wire shape it is synthesizing for).
//
// ROUND-6 R6-06: an EMPTY value is an ASSUMPTION of the connector's baseline, not
// a discovery — nothing here asks the upstream what it speaks. The assumption is
// safe in the sense that a mismatch fails the read and retains the record, but it
// is an assumption, and the operator owns it.
func resolveUpstreamRevision(cfg ResourceServerConfig) (string, error) {
	rev := strings.TrimSpace(cfg.UpstreamRevision)
	if rev == "" {
		return currentRevision, nil
	}
	if revisionIndex(rev) < 0 {
		return "", fmt.Errorf("mcp: rs: unknown upstream_revision %q (accepted: %s)", rev, strings.Join(revisionTimeline, ", "))
	}
	return rev, nil
}

// newInstanceID mints the per-process identity of this RS. It is an opaque
// random label, never a host or tenant identifier: its only job is to let an
// operator (and the pagination cursor) tell one process-local cache snapshot
// from another.
func newInstanceID() (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

func resolveResourceServerRevisionMode(cfg ResourceServerConfig) (string, error) {
	mode := strings.TrimSpace(cfg.RevisionMode)
	if mode == "" {
		if cfg.DisableNextRevisionHeaders {
			return revisionModeLegacy, nil
		}
		return revisionModeDual, nil
	}
	switch mode {
	case revisionModeDual, revisionModeRCStrict, revisionModeLegacy:
		return mode, nil
	default:
		return "", fmt.Errorf("mcp: rs: unknown revision_mode %q (accepted: dual, rc-strict, legacy)", mode)
	}
}

// metadataURLFor derives the absolute URL where this RS serves its PRM document
// (scheme://host + the well-known path), the value advertised in WWW-Authenticate's
// resource_metadata parameter. It tolerates an unparseable input by returning the
// path alone (still a valid relative reference).
func metadataURLFor(serverURL string) string {
	u, err := url.Parse(strings.TrimSpace(serverURL))
	if err != nil || u.Host == "" {
		return wellKnownProtectedResource
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host) + wellKnownProtectedResource
}

// unionScopes merges the scope sets the RS advertises, trimmed/de-duplicated and
// sorted (deterministic PRM document). An empty union yields nil so
// scopes_supported is omitted rather than served empty.
func unionScopes(sets ...[]string) []string {
	seen := map[string]struct{}{}
	for _, set := range sets {
		for _, s := range set {
			if s = strings.TrimSpace(s); s != "" {
				seen[s] = struct{}{}
			}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// jwksConfigured reports whether raw inline-JWKS bytes actually configure an anchor:
// empty, whitespace, or a JSON null (a json.RawMessage artifact) do not.
func jwksConfigured(raw []byte) bool {
	t := strings.TrimSpace(string(raw))
	return t != "" && t != "null"
}

// scopeOfflineAccess is the OIDC scope a client uses to request a refresh token.
// SEP-2207 (2026-07-28 RC): MCP servers SHOULD NOT include it in WWW-Authenticate
// scope or in Protected Resource Metadata scopes_supported — refresh tokens are not
// a resource requirement. This RS enforces the SHOULD as a constructor error: an
// auditable RS never advertises or demands it.
const scopeOfflineAccess = "offline_access"

// rejectOfflineAccess refuses a config whose advertised or toolset-enforced scopes
// include offline_access (SEP-2207).
func rejectOfflineAccess(declared, enforced []string) error {
	for _, s := range declared {
		if strings.TrimSpace(s) == scopeOfflineAccess {
			return fmt.Errorf("mcp: rs: scopes_supported must not include %q (SEP-2207: refresh tokens are not a resource requirement)", scopeOfflineAccess)
		}
	}
	for _, s := range enforced {
		if s == scopeOfflineAccess {
			return fmt.Errorf("mcp: rs: a tool policy requires scope %q (SEP-2207: refresh tokens are not a resource requirement)", scopeOfflineAccess)
		}
	}
	return nil
}

// originSet normalizes the Origin allowlist into a set for O(1) checks. An empty input
// yields an empty set, which the Origin check treats as "browser clients not enabled"
// (any request carrying an Origin header is then refused — DNS-rebinding defense).
func originSet(origins []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, o := range origins {
		if o = strings.TrimSpace(o); o != "" {
			out[strings.ToLower(o)] = struct{}{}
		}
	}
	return out
}
