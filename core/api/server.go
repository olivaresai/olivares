// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api/ratelimit"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/core/license"
	"github.com/olivaresai/olivares/core/metrics"
	"github.com/olivaresai/olivares/core/model"
	obstrace "github.com/olivaresai/olivares/core/observability/trace"
	"github.com/olivaresai/olivares/core/residency"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/core/updatecheck"
)

// Options configures a Server. Store, Authenticator, Authorizer, Signer and
// SetupToken are required.
type Options struct {
	Store         store.Store
	Authenticator *auth.Authenticator
	Authorizer    *auth.Authorizer
	Signer        *audit.Signer
	SetupToken    *secure.SetupToken
	Logger        *slog.Logger
	Clock         model.Clock
	Version       string
	// LicensePublicKey and LicenseBlob are informational only; the API never
	// gates on them (LICENSING.md). They populate the license status in server-info.
	LicensePublicKey ed25519.PublicKey
	LicenseBlob      string
	// Modules are the API modules whose routes are mounted under /v1/m/<ns>/.
	Modules []Module
	// UnconditionalGrants reports the permissions a principal holds by an authored
	// grant that no resource condition gates, so /v1/auth/whoami can report authority
	// the ROLE does not carry. It is the reporting counterpart of the
	// ScopedAuthorizer wired into the Authorizer: that one DECIDES a request, this one
	// only describes what the console may offer, and neither derives the other's answer.
	// nil leaves whoami reporting the role-derived set exactly as before.
	UnconditionalGrants auth.UnconditionalGrantReporter
	// Federation is the SSO provider backing the /v1/auth/federation endpoints. nil
	// defaults to auth.NoFederation (every SSO request returns 501
	// sso_not_configured). Since the single-IdP OIDC/SAML provider is open-core
	// (core/auth/federation), so the base AGPL build DOES link go-oidc/crewjam and
	// does real single-IdP login; the reserved multi-IdP line is wired separately.
	Federation auth.Federation
	// Ingest, when set, enables the collector→core IngestService on the gRPC
	// server (CB-1 option C): pushed observations are authorized (ingest:write)
	// and lifted onto the event bus through it. nil leaves ingest unavailable
	// (single-node engines need no inbound ingest — their sources run in-process).
	Ingest ObservationPublisher
	// Metrics is the Prometheus exposition registry backing /metrics (OBS-06). nil
	// makes New build a default registry, so the composition root needs no wiring;
	// a caller (or a test) may inject one to assert on the exposed series.
	Metrics *metrics.Registry
	// Tracing is the engine-side W3C Trace Context provider (OBS-03). When set, an
	// ingress middleware extracts the inbound traceparent (continuing the caller/mesh
	// trace) so audit events and the engine→Claude hop share one trace. nil disables
	// the trace middleware (the engine runs untraced) — a missing collector never
	// breaks a request (the provider itself degrades to no-op).
	Tracing *obstrace.Provider
	// RateLimit configures the inbound per-tenant/per-endpoint-class rate limiter
	// (OPS-5). nil leaves the limiter OUT of the chain — so an embedder or a
	// test that does not opt in is unmetered (and existing harnesses are unchanged).
	// The PRODUCTION fail-closed guarantee lives in the composition root, which always
	// supplies a config (default: enforce, built-in tiers). The limiter binds the
	// shared metrics registry, so it is built here (not by the caller).
	RateLimit *ratelimit.Config
	// Residency is this instance's multi-region residency registry. When it
	// enforces (a home region is configured) the org-provisioning handler validates
	// a tenant's region pin against it. nil/non-enforcing = single-region mode: a
	// pin request is rejected (you cannot promise residency without --region).
	Residency *residency.Registry
	// KeyCustody is the non-secret boot-time custody inventory exposed to
	// /v1/console/keys. The composition root supplies public keys, fingerprints,
	// custody provenance, and sealer presence only; private/symmetric material
	// must never enter this value. The zero value yields an honest empty list.
	KeyCustody KeyCustodyInfo
	// BusStats is the optional event-bus introspection seam used by the privileged
	// console snapshot and the coarse public ingest status. nil preserves the
	// historical store-derived ingest status for embedders that do not expose bus
	// statistics.
	BusStats eventbus.StatsProvider
	// TLSCertNotAfter returns the expiry of the certificate currently served by
	// the listener. It is an accessor (rather than a boot-time value) so certificate
	// reloads are reflected without rebuilding the API server. nil/ok=false omits
	// TLS expiry from the health summary.
	TLSCertNotAfter func() (time.Time, bool)
	// CoreEntityResolver reads the authorization facts of a CORE row for a module
	// route that declares EntityRef.CoreKind (V269 / COCKPIT-02 §3). nil is fine for
	// every deployment whose modules declare none; a route that DOES declare one and
	// finds this nil refuses to mount, rather than authorizing without its lineage.
	CoreEntityResolver CoreEntityAuthorizationResolver

	// PrincipalEvidenceProducer installs the sealed, windowed directory-epoch fact the evidence
	// path needs before it can say anything but "unknown" about a principal.
	//
	// ⛔ ITS ABSENCE IS A REFUSAL TO START WHEN ANY GOVERNED ROUTE IS REGISTERED, and that is not
	// caution. Without it AuthorizeRoute can never mint a witness, so every governed route answers
	// 503 to everyone, forever — and 503 reads like an outage, not like a missing dependency. A
	// refusal at boot names the precondition instead of leaving it to be diagnosed in production.
	//
	// ⛔ AND IT IS AN INTERFACE, NOT A BOOL. A bool would let anyone declare the dependency
	// satisfied by editing configuration; a producer cannot be brought into existence that way.
	// The guard relaxes by PRESENCE, and its mutant — remove the producer, the server refuses to
	// start — is what keeps that honest.
	PrincipalEvidenceProducer PrincipalEvidenceProducer
	// Recorder is the privileged-session recording seam (SEC-G5). When set,
	// every module route is gated and captured through it (recording.go). nil
	// leaves module routes unrecorded — existing embedders and tests unchanged;
	// the production composition root always wires the recording module.
	Recorder SessionRecorder
	// WebAuthn pins the relying party for the privileged-login ceremonies
	//. Zero value = derive per request from the proxy-aware external URL
	// (single-node default); pinning it makes registered credentials survive a
	// rename of the panel host only if the RP ID stays constant.
	WebAuthn auth.WebAuthnRP
	// WebAuthnUnusable states that this deployment HAS a declared console address
	// and that address cannot be a relying party (an IP, or a name the verifier
	// refuses). It is an explicit unavailable outcome and not a fallback: without
	// it, an operator who declared "https://10.0.0.7:8443" would silently get a
	// relying party derived from whatever Host header arrived, which is an
	// authentication authority nobody chose. Ceremonies answer the typed 503.
	//
	// It is mutually exclusive with a pinned WebAuthn: a pin that validated is a
	// usable relying party by construction.
	WebAuthnUnusable bool
	// PIV configures the PIV/CAC client-certificate route.
	// nil = not configured: the status endpoint reports the honest 501 seam and
	// elevation is refused (fail-closed).
	PIV *auth.PIVConfig
	// InviteSender optionally delivers onboarding invitation links by email.
	// nil leaves invites show-once only (the token is returned to the admin to
	// relay out-of-band) — the console still works without a mailer wired.
	InviteSender InviteSender
	// FederationService is the MANAGED SSO config service: it backs the
	// console SSO config endpoints AND resolves the live provider for login (so
	// SSO is store-driven, not env-only). When set it supersedes Federation for the
	// login round-trip. nil leaves login on the static Federation provider and the
	// console SSO config endpoints answer 501 (managed config unavailable) — the
	// behavior for an embedder/test that has not opted in.
	FederationService *auth.FederationService
	// SecretStore is the runtime secret store: it backs the console/CLI
	// secret CRUD endpoints (/v1/console/secrets). Superadmin + AAL3 gated; secrets
	// are sealed at rest and never returned (only a non-secret hint). nil leaves the
	// secret endpoints answering 501 (an embedder/test that has not opted in).
	SecretStore *auth.SecretStore
	// SourceRoster is the live connector/source reconfiguration surface: it
	// backs the console/CLI source CRUD and the /v1/console/runtime/reload trigger
	// that adds/removes/rotates connectors in the running engine without a restart.
	// Superadmin + AAL3 gated. nil leaves those endpoints answering 501.
	SourceRoster SourceRoster
	// ConnectorOnboarding is the console connector-onboarding surface: the
	// descriptor catalog + sealed-credential CRUD + connectivity test that lets an
	// operator add a connector AND its credentials from the console (sealing inline
	// secrets into the store and persisting reference-only source rows via).
	// Superadmin-gated; writes/test need AAL3. nil leaves those endpoints 501.
	ConnectorOnboarding ConnectorOnboarding
	// KnowledgeStatus exposes the composition-root knowledge-plane posture (embedder
	// kind and whether retrieval is semantic) to /status and the console summary.
	// nil reports an unwired local-hash posture so the degraded state stays visible.
	KnowledgeStatus KnowledgeStatusProvider
	// License is the live edition/license surface: install/observe/HOT-APPLY a
	// commercial license without a restart, backing /v1/console/license and the live
	// server-info status. Superadmin-gated; writes need AAL3. nil leaves those console
	// endpoints 501 and server-info falls back to the static LicenseBlob below. It is
	// pure edition plumbing — it NEVER gates a feature on the license (LICENSING.md).
	License LicenseService
	// Activation is the enterprise activation surface: the console reads
	// per-add-on state and enables/disables a preset. nil (community build) = the
	// /v1/console/activation endpoints answer 501, the honest not-wired seam.
	Activation ActivationService
	// UpdateStatus is the OTA update-availability probe: a nil-safe accessor
	// returning the latest cached update check for the console indicator. nil (or an
	// air-gapped deployment with no update endpoint) leaves the health summary's
	// `update` field absent — silence, never an error (docs/UPGRADE-AND-ROLLBACK.md).
	UpdateStatus func() updatecheck.Status
	// UpdateRefresh runs one OTA check immediately and returns its fresh status.
	// nil is the honest air-gapped/not-configured seam: the console check-now
	// endpoint answers 501 and performs no outbound request.
	UpdateRefresh func(context.Context) updatecheck.Status
	// EffectiveConfig returns a fresh, already-redacted projection of the
	// production config registry. It is evaluated per request so enterprise
	// activation overlays remain live. nil yields an honest empty projection.
	EffectiveConfig func() []EffectiveConfigEntry
	// EffectiveConfigViolations returns the current sorted unknown OLIVARES_* env
	// keys. Values never cross this seam. nil yields an empty violation list.
	EffectiveConfigViolations func() []string
	// SupportBundleRedact and SupportBundleContainsSensitive are the canonical
	// security-module policy functions supplied by the composition root. Keeping
	// them as function seams avoids a core/api -> modules/security import cycle.
	// If absent, the server omits free-text log/secret metadata and uses a
	// conservative final guard rather than emitting content it cannot scrub.
	SupportBundleRedact            func(string) (string, int)
	SupportBundleContainsSensitive func(string) bool
	// MetricsAuth configures application-level access control on /metrics:
	// optional bearer token and/or CIDR allowlist layered on top of the existing
	// network-level controls (bind address, NetworkPolicy). nil ⇒ unauthenticated
	// (the historical default). A malformed CIDR makes New fail fast.
	MetricsAuth *MetricsConfig
	// AuthZen configures the AuthZEN/access-review surface EXPOSURE: disable the
	// whole surface, just the reverse-query searches, or the export; and/or confine it
	// to an intra-cluster network (AuthZenConfig). nil ⇒ fully enabled with no network
	// restriction — the per-call bearer + authz:read/authz:admin + AAL3 gates still
	// apply. A malformed CIDR makes New fail fast.
	AuthZen *AuthZenConfig
	// EMAGrant is the EMA jwt-bearer grant handler: validates an ID-JAG
	// assertion and mints an audience-bound at+jwt for the target MCP server.
	// nil leaves the /v1/auth/token endpoint responding unsupported_grant_type
	// for jwt-bearer requests (deny-closed — EMA is an opt-in enterprise feature).
	EMAGrant *auth.EMAGrant
	// AuthZenPrincipals overrides the candidate-principal SOURCE for the reverse
	// queries / access-review export. nil ⇒ the Authenticator (LIVE enumeration — the
	// no-divergence default). An enterprise build may inject a materialized population
	// source (PrincipalEnumerator); decisions still go live through Authorize.
	AuthZenPrincipals PrincipalEnumerator
	// DR configures the disaster-recovery console surface: backup trigger,
	// list, download, restore, and schedule endpoints under /v1/console/dr/. When
	// set the console can manage DR bundles directly; nil leaves those endpoints
	// answering 501 (an embedder/test that did not opt in).
	DR *DRConfig
	// LeaderRouteGate arms the HA leader-routing backstop (stage-2): every
	// application route re-checks Leader().IsLeader() and answers the retryable 503
	// not_leader on a standby, while the operational endpoints stay reachable
	// (middleware.go leaderGate). It exists because the leader-routing layout makes
	// standbys Pod-Ready — and therefore reachable — so the Pod label that selects
	// the leader Service can be briefly stale. false (the default) leaves behavior
	// exactly as it was: an embedder, a single-node engine and a legacy HA
	// deployment (standbys drained by /readyz) are unaffected.
	LeaderRouteGate bool
	// LogBroker is the engine-log viewer surface: a ring-buffer slog.Handler
	// that captures log entries and broadcasts them via SSE to the console log
	// viewer. When set the console can stream and query engine logs in real time;
	// nil leaves /v1/console/logs/* answering 501.
	LogBroker *LogBroker
}

// Server is the engine's HTTP API. Build it with New, mount it with Handler or
// run it with a hardened http.Server from NewHTTPServer.
type Server struct {
	st          store.Store
	authr       *auth.Authenticator
	authz       *auth.Authorizer
	signer      *audit.Signer
	setupTok    *secure.SetupToken
	log         *slog.Logger
	clock       model.Clock
	version     string
	licensePub  ed25519.PublicKey
	licenseBlob string

	ingest ObservationPublisher
	fed    auth.Federation
	sso    *ssoFlowStore

	// Metrics (OBS-06): the registry plus the handles the hot paths increment.
	metrics    *metrics.Registry
	mReqTotal  *metrics.Counter
	mReqDur    *metrics.Histogram
	mInflight  *metrics.Gauge
	mIngestObs *metrics.Counter
	mIngestDur *metrics.Histogram
	mIngestRej *metrics.Counter
	mGRPCTotal *metrics.Counter
	mGRPCDur   *metrics.Histogram
	mLogin     *metrics.Counter

	// trace (OBS-03): the W3C Trace Context provider whose ingress middleware
	// continues the caller's trace; nil leaves the engine untraced.
	trace *obstrace.Provider

	// rl: the inbound rate limiter; nil leaves the limiter middleware and the
	// gRPC rate-limit hook disabled (no metering).
	rl *ratelimit.Limiter
	// routeResponseHeaders is the ROUTE-SPECIFIC response metadata declared at
	// registration, keyed by "METHOD <full router pattern>". It is resolved by
	// routeResponseMetadata BEFORE the authenticating and rate-limiting middlewares
	// can answer, which is the only way a route-level declaration reaches a refusal
	// written before chi routes the request. Empty for every route that declares
	// nothing, which is all of them by default.
	routeResponseHeaders map[string]map[string]string
	// routeDescriptors is the ENGINE's private record of every module route it
	// mounted, keyed by "METHOD <full spec path>" (capabilities_registry.go). It is
	// written ONLY during mountModules, inside New, and read only afterwards from the
	// self capability projection — so a served request can never give a route a
	// description it did not declare.
	routeDescriptors map[string]routeDescriptor
	// residency: the multi-region residency registry; nil/non-enforcing in
	// single-region mode. Used to validate a tenant's region pin at provisioning.
	residency *residency.Registry
	// keyCustody is a detached copy of the non-secret boot inventory.
	keyCustody KeyCustodyInfo
	// busStats is the optional event-bus snapshot provider. The previous public
	// status observation is serialized with busStatusMu so cumulative-counter
	// DELTAS are computed exactly once per observation.
	busStats          eventbus.StatsProvider
	busStatusMu       sync.Mutex
	busLastStats      eventbus.Stats
	busLastObservedAt time.Time
	busHasLastStats   bool
	// tlsCertNotAfter reads the live listener certificate expiry (including
	// reloads); nil means the API does not know of a TLS certificate.
	tlsCertNotAfter func() (time.Time, bool)
	// recorder: the privileged-session recording seam; nil leaves module
	// routes unrecorded (recording.go).
	recorder SessionRecorder
	// coreEntityResolver (V269): reads the authorization facts of a core row for a
	// module route that declares EntityRef.CoreKind; nil = no such route may mount.
	coreEntityResolver CoreEntityAuthorizationResolver

	// principalEvidence is the producer the governed door needs; nil is a refusal to mount a
	// governed route (checkGovernedRoutesHaveEvidence), not a degraded mode.
	principalEvidence PrincipalEvidenceProducer
	// webauthn: the pinned relying party; zero = per-request derivation.
	webauthn auth.WebAuthnRP
	// webauthnUnusable: the declared console address cannot be a relying party, so
	// ceremonies refuse deny-closed instead of deriving one from the request.
	webauthnUnusable bool
	// piv: the PIV/CAC client-cert route config; nil = not configured.
	piv *auth.PIVConfig
	// inviteSender: optional onboarding-invite email delivery; nil = invites
	// are show-once only (token returned to the admin to relay out-of-band).
	inviteSender InviteSender
	// fedSvc: the managed SSO config service; when set, login resolves its
	// provider through it (store-driven) and the console SSO config endpoints use
	// it. nil = login on the static fed and config endpoints report unavailable.
	fedSvc *auth.FederationService
	// secretStore: the runtime secret store backing /v1/console/secrets.
	// nil = the secret endpoints answer 501 (not opted in).
	secretStore *auth.SecretStore
	// sourceRoster: the live source-reconfiguration surface backing the
	// source CRUD + /v1/console/runtime/reload. nil = those endpoints answer 501.
	sourceRoster SourceRoster
	// connectorOnboarding: the console connector-onboarding surface backing
	// the descriptor catalog + sealed-credential CRUD + test under
	// /v1/console/connectors. nil = those endpoints answer 501.
	connectorOnboarding ConnectorOnboarding
	// knowledgeStatus is the no-secret, process-level knowledge-plane posture.
	knowledgeStatus KnowledgeStatusProvider
	// updateStatus returns the latest cached OTA update check; nil when
	// update checking is not configured (air-gap) — the console shows no indicator.
	updateStatus func() updatecheck.Status
	// updateRefresh performs a fresh OTA check for the explicit console check-now
	// route; nil is the honest unconfigured/air-gap seam.
	updateRefresh func(context.Context) updatecheck.Status
	// effectiveConfig/effectiveConfigViolations are live composition-root
	// projections of the cmd-owned config registry and activation overlay.
	effectiveConfig           func() []EffectiveConfigEntry
	effectiveConfigViolations func() []string
	// supportBundleRedact/supportBundleContainsSensitive are the canonical text
	// scrubber and final fail-closed guard supplied by the composition root.
	supportBundleRedact            func(string) (string, int)
	supportBundleContainsSensitive func(string) bool
	// license: the live edition/license surface backing /v1/console/license and
	// the live server-info status. nil = those endpoints answer 501 and server-info
	// falls back to the static licenseBlob. Pure edition plumbing (LICENSING.md).
	license LicenseService
	// activation: the enterprise activation surface; nil = the console
	// activation endpoints answer 501 (community build / not opted in).
	activation ActivationService
	// emaGrant: the EMA jwt-bearer grant handler; nil = EMA not configured
	// (deny-closed: the token endpoint refuses jwt-bearer requests).
	emaGrant *auth.EMAGrant
	// metricsGate: application-level access control on /metrics; nil ⇒
	// unauthenticated (the historical default, safe behind a trusted network).
	metricsGate *metricsGate
	// authzen: the parsed AuthZEN/access-review exposure gate; nil ⇒ fully
	// enabled, no network restriction (auth + permission gates still apply).
	authzen *authzenGate
	// authzenPrincipals: the candidate-principal source for reverse queries;
	// defaults to authr (live enumeration — the no-divergence default).
	authzenPrincipals PrincipalEnumerator
	// drSvc: the disaster-recovery console surface; nil = the DR endpoints
	// answer 501 (not opted in).
	drSvc *drService
	// logBroker: the engine-log viewer surface; nil = the log endpoints
	// answer 501 (not opted in).
	logBroker *LogBroker
	// leaderRouteGate (stage-2): when true, application HTTP routes and gRPC
	// methods re-check leadership and answer 503 not_leader on a standby. Off by
	// default — only the leader-routing HA layout arms it.
	leaderRouteGate bool
	// deprecations: the route-deprecation table indexed for the request
	// hot path; empty keeps the deprecationHeaders middleware out of the chain.
	// mux is the built router, kept so the deprecation writer can resolve the
	// route pattern of a response written BEFORE routing (auth/rate-limit/setup
	// gate). openapiDoc is this server's published contract, built once from
	// the table New indexed (per-Server, so a test-swapped table is honored).
	deprecations map[string]RouteDeprecation
	mux          *chi.Mux
	openapiDoc   map[string]any
	// openapiBetaDoc is the BETA module-route contract, distinct from the
	// stable openapiDoc. It is built LAZILY on the first GET /openapi.beta.json
	// (betaOnce) rather than in New: it is a large document (the whole module
	// surface) and most server lifetimes — boot, and the many test harnesses —
	// never request it, so eager construction was pure overhead on every startup.
	modules        []Module
	betaOnce       sync.Once
	openapiBetaDoc map[string]any
	// searchKinds is the federated console-search registry: the core
	// kinds plus every kind contributed by a Searcher module, collected once at
	// mount and gated per request in handleSearch (search.go).
	searchKinds []SearchKind
	// rolePerms is the per-role EFFECTIVE permission set /v1/auth/whoami hands the
	// console, computed once at construction from the permission catalog this
	// binary actually serves (buildPermCatalog). Built once because it is a pure
	// function of the mounted modules, which never change after New; whoami copies
	// out of it and applies the per-grant workspace-confinement filter.
	rolePerms map[rolePermKey][]auth.Permission
	// unconditionalGrants reports the permissions a principal holds by an authored
	// grant that no resource condition gates. rolePerms above answers from the
	// ROLE alone, so without this a tenant-scoped grant is invisible to the console and
	// — since can() became set membership — silently HIDES the action. nil keeps the
	// role-only set, which is the behavior every embedder had before the seam existed.
	unconditionalGrants auth.UnconditionalGrantReporter

	setupComplete atomic.Bool
	handler       http.Handler
}

// New builds a Server and its router.
func New(opts Options) (*Server, error) {
	if opts.Store == nil || opts.Authenticator == nil || opts.Authorizer == nil || opts.Signer == nil || opts.SetupToken == nil {
		return nil, errors.New("api: Store, Authenticator, Authorizer, Signer and SetupToken are required")
	}
	// A pinned relying party must be whole: an ID with no origins would make
	// webauthn.New reject EVERY ceremony at request time (500s) — surface the
	// misconfiguration at build time instead. (The env loader already guards
	// this; the Options seam is for embedders, who deserve the same guard.)
	if (opts.WebAuthn.ID == "") != (len(opts.WebAuthn.Origins) == 0) {
		return nil, errors.New("api: Options.WebAuthn must set both ID and Origins, or neither (per-request derivation)")
	}
	// A pinned relying party and "no usable relying party" are contradictory
	// states, and an embedder who set both would get one of them silently.
	if opts.WebAuthnUnusable && opts.WebAuthn.ID != "" {
		return nil, errors.New("api: Options.WebAuthnUnusable cannot be set together with a pinned Options.WebAuthn")
	}
	s := &Server{
		st: opts.Store, authr: opts.Authenticator, authz: opts.Authorizer, signer: opts.Signer,
		setupTok: opts.SetupToken, log: opts.Logger, clock: opts.Clock, version: opts.Version,
		licensePub: opts.LicensePublicKey, licenseBlob: opts.LicenseBlob, ingest: opts.Ingest,
		fed: opts.Federation, sso: newSSOFlowStore(), trace: opts.Tracing, residency: opts.Residency,
		keyCustody:      KeyCustodyInfo{Keys: append([]KeyInfo(nil), opts.KeyCustody.Keys...)},
		busStats:        opts.BusStats,
		tlsCertNotAfter: opts.TLSCertNotAfter,
		recorder:        opts.Recorder, webauthn: opts.WebAuthn, webauthnUnusable: opts.WebAuthnUnusable, piv: opts.PIV,
		coreEntityResolver: opts.CoreEntityResolver,
		principalEvidence:  opts.PrincipalEvidenceProducer,
		inviteSender:       opts.InviteSender, fedSvc: opts.FederationService, secretStore: opts.SecretStore,
		sourceRoster: opts.SourceRoster, connectorOnboarding: opts.ConnectorOnboarding,
		knowledgeStatus:                opts.KnowledgeStatus,
		updateStatus:                   opts.UpdateStatus,
		updateRefresh:                  opts.UpdateRefresh,
		effectiveConfig:                opts.EffectiveConfig,
		effectiveConfigViolations:      opts.EffectiveConfigViolations,
		supportBundleRedact:            opts.SupportBundleRedact,
		supportBundleContainsSensitive: opts.SupportBundleContainsSensitive,
		license:                        opts.License, emaGrant: opts.EMAGrant,
		activation:      opts.Activation,
		logBroker:       opts.LogBroker,
		leaderRouteGate: opts.LeaderRouteGate,
	}
	if opts.DR != nil {
		s.drSvc = newDRService(*opts.DR)
		// reload the persisted backup schedule — the RequireDualControl
		// restore gate included — so a process restart never silently resets it.
		// Bounded: a wedged store must not hang construction forever.
		loadCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := s.loadDRSchedule(loadCtx)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("api: load persisted DR schedule: %w", err)
		}
	}
	if s.fed == nil {
		s.fed = auth.NoFederation{}
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.clock == nil {
		s.clock = model.SystemClock{}
	}
	if s.version == "" {
		s.version = "dev"
	}
	s.deprecations = indexDeprecations(routeDeprecations)
	s.openapiDoc = buildOpenAPI()
	// keep the SAME modules mounted below, so the beta document (built lazily
	// on first serve, from collectModuleRoutes over these) never drifts from what
	// the engine mounts.
	s.modules = opts.Modules
	s.initMetrics(opts.Metrics)
	if opts.RateLimit != nil {
		// Built here (not by the caller) so the limiter registers its metrics into the
		// SAME registry initMetrics just bound, and so a nil config cleanly leaves the
		// middleware out of the chain below.
		s.rl = ratelimit.New(*opts.RateLimit, s.metrics)
	}
	// compile the metrics access-control gate (fail fast on a bad CIDR).
	mg, merr := buildMetricsGate(opts.MetricsAuth)
	if merr != nil {
		return nil, merr
	}
	s.metricsGate = mg
	// compile the AuthZEN exposure gate (fail fast on a bad CIDR) and pick the
	// candidate-principal source (default: the Authenticator's live enumeration).
	gate, gerr := buildAuthzenGate(opts.AuthZen)
	if gerr != nil {
		return nil, gerr
	}
	s.authzen = gate
	s.authzenPrincipals = opts.AuthZenPrincipals
	if s.authzenPrincipals == nil {
		s.authzenPrincipals = s.authr
	}
	h, err := s.buildRouter(opts.Modules)
	if err != nil {
		return nil, err
	}
	s.handler = h
	// AFTER buildRouter, which is what mounts the modules and fills searchKinds: the
	// catalog must describe what this binary SERVES, so it is read from the mount, not
	// from opts.
	s.rolePerms = buildRolePerms(buildPermCatalog(opts.Modules, s.searchKinds))
	s.unconditionalGrants = opts.UnconditionalGrants
	return s, nil
}

// Handler returns the built HTTP handler (for embedding or testing).
func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) buildRouter(modules []Module) (http.Handler, error) {
	r := chi.NewRouter()
	// EVERY response carries the API's error shape, including the two chi answers
	// for itself (2026-08-05). Measured against a live engine before this existed:
	//
	//   POST /v1/console/setup-status  -> 405, ZERO bytes, no Content-Type at all
	//   GET  /v1/no-such-route         -> 404, text/plain "404 page not found"
	//
	// Neither is the `{"error":{"code","message"}}` every handler emits, and the 405
	// gave a client literally nothing to act on. That matters most exactly where a
	// generated client goes wrong: three published operations pointed at URLs the
	// router never matched, and what those calls got back was this empty 405.
	//
	// The status codes are unchanged — only the body, so a client can parse ONE
	// shape for every failure this API produces.
	r.MethodNotAllowed(func(w http.ResponseWriter, req *http.Request) {
		var body errorBody
		body.Error.Code = "method_not_allowed"
		body.Error.Message = "That method is not allowed on this path. Check the OpenAPI document for the methods this route registers."
		writeJSON(w, http.StatusMethodNotAllowed, body)
	})
	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		var body errorBody
		body.Error.Code = "not_found"
		body.Error.Message = "No route is registered at that path."
		writeJSON(w, http.StatusNotFound, body)
	})
	// The middleware chain. The OBS-03 trace extractor sits AFTER requestIDMW (so the
	// request id is already assigned) and BEFORE the rest, so the access log, auth and
	// every handler — and the audit events they write — run inside the continued
	// trace span. It is inserted only when a provider is wired (untraced otherwise).
	mw := []func(http.Handler) http.Handler{s.recoverer, s.requestIDMW}
	if s.trace != nil {
		mw = append(mw, s.trace.HTTPMiddleware)
	}
	// The deprecation signal sits with the other unconditional header
	// middlewares: it must wrap the writer before accessLog's statusRecorder so a
	// deprecated route's headers commit ahead of the first body byte.
	// ⛔ routeResponseMetadata GOES BEFORE authenticate, AND THAT ORDER IS THE FIX.
	// A route that declares response metadata used to receive it inside its own
	// mounted closure, which is downstream of every middleware that can answer
	// first: an invalid bearer is refused by authenticate and a throttled caller by
	// rateLimit, both before chi ever routes the request, so both answered WITHOUT
	// the declaration. Resolving the pattern here — with chi's own matcher, for the
	// routes that declared something and no others — is what lets a per-route
	// declaration reach those refusals without giving any sibling a policy it never
	// asked for. It is inert when nothing is declared.
	mw = append(mw, s.deprecationHeaders, s.secureHeaders, s.accessLog,
		s.routeResponseMetadata, s.authenticate)
	// Stage-2: the HA leader-routing backstop runs right after authentication —
	// BEFORE the rate limiter and the setup gate. A standby is dialable in that
	// layout, so a request that lands on one is going to be refused no matter what;
	// letting it consume a token of the tenant's SHARED (HA-wide) rate-limit bucket
	// first would let stale routing burn quota on requests that never ran, and would
	// pay for a setup-gate store lookup for the same nothing. Authentication stays
	// outside so a bad credential still reports 401 rather than 503, and accessLog
	// still records the refusal. Inserted only when the composition root deploys the
	// leader-routing layout.
	if s.leaderRouteGate {
		mw = append(mw, s.leaderGate)
	}
	// The rate limiter runs after authenticate (it needs the principal) and
	// before setupGate (it also shields the setup-gate store query); inserted only
	// when configured (nil => unmetered, e.g. tests/embedders). It sits inside
	// accessLog, so a 429 is logged and counted like any other response.
	if s.rl != nil {
		mw = append(mw, s.rateLimit)
	}
	mw = append(mw, s.setupGate)
	r.Use(mw...)

	r.Get("/healthz", s.handleHealth)
	r.Get("/openapi.json", s.handleOpenAPI)
	// the beta module-route document, alongside the stable one and exempt
	// the same way (RootEnginePaths) so SDK codegen can fetch it pre-setup.
	r.Get("/openapi.beta.json", s.handleOpenAPIBeta)
	// Operational probes + Prometheus scrape target (OBS-06). Root-level and
	// auth/setup-exempt like /healthz: a Prometheus scraper and a k8s probe present
	// no bearer and must work before first setup.
	r.Get("/metrics", s.handleMetrics)
	r.Get("/livez", s.handleLivez)
	r.Get("/readyz", s.handleReadyz)
	// Stage-2: pod health WITHOUT the leader check — the HA readinessProbe, so
	// a hot standby is Ready to the kubelet and a rolling update can progress
	// (metrics.go handlePodReadyz). /readyz keeps its leader-only drain meaning.
	r.Get("/pod-readyz", s.handlePodReadyz)
	r.Get("/status", s.handlePublicStatus)
	r.Get(OAuthAuthorizationServerMetadataPath, s.handleAuthorizationServerMetadata)

	// OpenID AuthZEN Authorization API 1.0: a conformant wire adapter over the
	// existing PDP (auth.Authorizer). Mounted at the spec's conventional /access/v1
	// paths (top-level, NOT /v1 — they are not part of the SDK/openapi surface; an
	// external PEP consumes them, and the SPA router routes the /access/ prefix to the
	// API). The discovery doc is public + setup-exempt (RootEnginePaths); the decision,
	// search and export endpoints authenticate the caller and gate on authz:read /
	// authz:admin. The Search endpoints ARE the access-review reverse queries
	// ("who can access R" / "what can X access"); the export seals its result in the
	// audit ledger (handlers_authzen.go, handlers_accessreview.go).
	r.Get(AuthZenConfigPath, s.handleAuthzenConfig)
	r.Post(authzenEvalPath, s.handleAuthzenEvaluation)
	r.Post(authzenEvalsPath, s.handleAuthzenEvaluations)
	r.Post(authzenSearchSubjPath, s.handleAuthzenSearchSubject)
	r.Post(authzenSearchResPath, s.handleAuthzenSearchResource)
	r.Post(authzenSearchActPath, s.handleAuthzenSearchAction)
	r.Post(authzenExportPath, s.handleAccessReviewExport)

	// Build /v1 as an explicit subrouter so a module-mount error can propagate.
	v1 := chi.NewRouter()
	v1.Get("/server-info", s.handleServerInfo)
	v1.Post("/setup", s.handleSetup)

	v1.Route("/auth", func(r chi.Router) {
		r.Post("/login", s.handleLogin)
		r.Post("/logout", s.handleLogout)
		// Renew the calling session credential without a full re-login (rotates the
		// token, extends expiry). Deny-closed for non-session principals.
		r.Post("/refresh", s.handleRefresh)
		r.Get("/whoami", s.handleWhoami)
		// G1-A: the SELF capability projection. Whoami keeps its contract — identity,
		// membership and the tenant-wide role floor — and this answers the different
		// question it was never able to: is THIS registered operation authorized for
		// the calling credential right now, and may THIS collection be loaded. It is
		// additive; nothing consults it to decide a route, and the handler remains the
		// authority for every real act (handlers_capabilities.go).
		r.Post("/capabilities", s.handleAuthCapabilities)
		// RFC 8693 token exchange: mint a down-scoped, audience-bound delegated
		// token from a subject token. OAuth wire contract (form in, OAuth JSON out).
		r.Post("/token-exchange", s.handleTokenExchange)
		// EMA jwt-bearer grant (RFC 7523): validate an ID-JAG assertion
		// and mint an audience-bound at+jwt for the target MCP server. Same OAuth
		// wire contract (form in, OAuth JSON out). Deny-closed when unconfigured.
		r.Post("/token", s.handleJWTBearerGrant)
		// SSO login federation (OIDC/SAML) behind the auth.Federation seam. start
		// redirects to the IdP; callback validates the assertion, find/provisions
		// the local user, and mints an opaque session. With NoFederation -> 501.
		r.Get("/federation/start", s.handleSSOStart)
		r.Get("/federation/callback", s.handleSSOCallback)
		r.Post("/federation/callback", s.handleSSOCallback)
		// Privileged login: WebAuthn ceremonies elevate the
		// calling session to AAL3; PIV/CAC is the second hardware route. The
		// paths are the declared seam — the panel already calls them.
		r.Post("/webauthn/register/options", s.handleWebAuthnRegisterOptions)
		r.Post("/webauthn/register", s.handleWebAuthnRegister)
		r.Post("/webauthn/authenticate/options", s.handleWebAuthnAuthOptions)
		r.Post("/webauthn/authenticate", s.handleWebAuthnAuthenticate)
		// Credential lifecycle: list own authenticators (metadata only) and
		// unregister one (lost/stolen-key remediation; AAL3-required, ledgered).
		r.Get("/webauthn/credentials", s.handleWebAuthnList)
		r.Patch("/webauthn/credentials/{id}", s.handleWebAuthnRename)
		r.Delete("/webauthn/credentials/{id}", s.handleWebAuthnDelete)
		r.Get("/piv/status", s.handlePIVStatus)
		r.Post("/piv/elevate", s.handlePIVElevate)
	})

	v1.Route("/agents", func(r chi.Router) {
		r.Get("/", s.handleListAgents)
		r.Post("/", s.handleCreateAgent)
		r.Get("/{id}", s.handleGetAgent)
		r.Patch("/{id}", s.handleUpdateAgent)
		r.Delete("/{id}", s.handleDeleteAgent)
	})

	// FASE X /: the scoping-plane console CRUD. Shipped the workspace and
	// agent-group entities + store CRUD but no HTTP surface; these routes let the
	// console manage them. Workspace has no DELETE (its lifecycle is Status:
	// archiving, never delete, so scoped entities are not orphaned); creating one is
	// owner-only + AAL3 (handlers_scoping.go).
	v1.Route("/workspaces", func(r chi.Router) {
		r.Get("/", s.handleListWorkspaces)
		r.Post("/", s.handleCreateWorkspace)
		r.Get("/{id}", s.handleGetWorkspace)
		r.Patch("/{id}", s.handleUpdateWorkspace)
		r.Get("/{id}/summary", s.handleWorkspaceSummary)
	})
	v1.Route("/agent-groups", func(r chi.Router) {
		r.Get("/", s.handleListAgentGroups)
		r.Post("/", s.handleCreateAgentGroup)
		r.Get("/{id}", s.handleGetAgentGroup)
		r.Patch("/{id}", s.handleUpdateAgentGroup)
		r.Delete("/{id}", s.handleDeleteAgentGroup)
		r.Get("/{id}/members", s.handleListAgentGroupMembers)
		r.Put("/{id}/members/{agentID}", s.handleAddAgentGroupMember)
		r.Delete("/{id}/members/{agentID}", s.handleRemoveAgentGroupMember)
	})
	// FASE X /: managed SSO/IdP configuration (moves SSO off env-only). V1
	// governs the global config, so these are superadmin-gated; writes need AAL3.
	// Secrets are sealed at rest and never returned (handlers_sso_config.go).
	v1.Route("/console/sso", func(r chi.Router) {
		// One scope's SSO admin surface. The base routes operate on the scope's PRIMARY
		// ("default") IdP; the /idps subtree (U4) lists and CRUDs ADDITIONAL IdPs by
		// alias, so a scope can federate more than one IdP (the first-class IdP entity).
		// Every route is superadmin-gated + AAL3 on writes (ratified D8); ssoScope reads
		// {tenant} and ssoAlias reads {alias} (a malformed one 400s before the store).
		ssoScopeRoutes := func(r chi.Router) {
			r.Get("/", s.handleGetSSOConfig)
			r.Put("/", s.handlePutSSOConfig)
			r.Delete("/", s.handleDeleteSSOConfig)
			r.Post("/test", s.handleTestSSOConfig)
			r.Route("/idps", func(r chi.Router) {
				r.Get("/", s.handleListSSOIdPs)
				r.Route("/{alias}", func(r chi.Router) {
					r.Get("/", s.handleGetSSOConfig)
					r.Put("/", s.handlePutSSOConfig)
					r.Delete("/", s.handleDeleteSSOConfig)
					r.Post("/test", s.handleTestSSOConfig)
				})
			})
		}
		// Deployment-wide global scope (no {tenant} param).
		ssoScopeRoutes(r)
		// U6: the SAME surface scoped to a specific tenant's IdP via {tenant}. This
		// makes the enterprise multi-IdP cap-lift REACHABLE through the product — before
		// U6 the only surface was the hardcoded global scope.
		r.Route("/tenants/{tenant}", ssoScopeRoutes)
	})
	// the runtime secret store (the sealed secrets an operator references from
	// connector configs as `store:<name>`). Deployment-wide, so superadmin-gated;
	// writes need AAL3. The value is never returned — only a non-secret hint. The
	// name is carried in the body (not the path) so it may contain '/'
	// (handlers_secrets.go).
	v1.Route("/console/secrets", func(r chi.Router) {
		r.Get("/", s.handleListSecrets)
		r.Put("/", s.handlePutSecret)
		r.Delete("/", s.handleDeleteSecret)
	})
	// the durable source roster + live reconfiguration. The source CRUD
	// authors the connectors the engine ingests from; a write persists then applies
	// the change to the running engine (add/rotate/remove) without a restart, and
	// /runtime/reload reconciles the whole roster. Deployment-wide ingestion, so
	// superadmin-gated; writes/reload need AAL3. Config carries secret references,
	// never values (handlers_sources.go).
	v1.Route("/console/sources", func(r chi.Router) {
		r.Get("/", s.handleListSources)
		r.Put("/", s.handlePutSource)
		r.Delete("/", s.handleDeleteSource)
	})
	// console connector ONBOARDING — the descriptor catalog the console renders
	// a form from, plus a sealed-credential CRUD and a connectivity test that compose
	// the secret store (seal an inline credential, store only a reference) and
	// the source roster (persist + apply live). Same superadmin gate as the
	// source roster; the catalog read carries no secret (no AAL3), writes and the test
	// do. The name is carried in the body on DELETE (it may contain '/').
	v1.Route("/console/connectors", func(r chi.Router) {
		r.Get("/", s.handleListConnectors)
		r.Put("/", s.handlePutConnector)
		r.Delete("/", s.handleDeleteConnector)
		r.Post("/test", s.handleTestConnector)
	})
	v1.Post("/console/runtime/reload", s.handleReloadRuntime)

	// the live edition/license surface — install/observe/HOT-APPLY a commercial
	// license without a restart (the Grafana/Elastic in-place model). Superadmin-gated;
	// the writes (install/uninstall) additionally require an AAL3 step-up. A license
	// blob is a signed public attestation (not a credential), so the status read needs
	// no step-up. Pure edition plumbing — never a feature gate (LICENSING.md).
	v1.Route("/console/license", func(r chi.Router) {
		r.Get("/", s.handleLicenseStatus)
		r.Post("/", s.handleInstallLicense)
		r.Delete("/", s.handleUninstallLicense)
	})
	// enterprise activation surface — read per-add-on state, preview a preset
	// diff, and enable/disable/promote. Superadmin-gated; the apply write requires an
	// AAL3 step-up. nil service (community) ⇒ every route 501s (honest not-wired seam).
	v1.Route("/console/activation", func(r chi.Router) {
		r.Get("/", s.handleActivationStatus)
		r.Post("/preview", s.handleActivationPreview)
		r.Post("/apply", s.handleActivationApply)
	})
	// Operational console endpoints. Effective config is already redacted and the
	// remaining reads expose no secrets, so they need no AAL3. The support bundle
	// aggregates config and logs and therefore adds an explicit AAL3 gate.
	v1.Get("/console/setup-status", s.handleSetupStatus)
	v1.Get("/console/health-summary", s.handleHealthSummary)
	v1.Get("/console/keys", s.handleKeyCustody)
	v1.Get("/console/bus", s.handleBusSnapshot)
	v1.Get("/console/config/effective", s.handleEffectiveConfig)
	v1.Post("/console/support-bundle", s.handleSupportBundle)
	v1.Post("/console/update-check", s.handleUpdateCheck)

	// disaster-recovery console surface — backup trigger, list, download,
	// restore, schedule, and job stream. Superadmin-gated (system:admin). The DR
	// service is optional: nil ⇒ each handler answers 501 honestly.
	v1.Route("/console/dr", func(r chi.Router) {
		r.Post("/backup", s.handleTriggerBackup)
		r.Get("/backups", s.handleListBackups)
		r.Get("/backups/{id}", s.handleGetBackup)
		r.Get("/backups/{id}/download", s.handleDownloadBackup)
		r.Delete("/backups/{id}", s.handleDeleteBackup)
		r.Post("/restore/upload", s.handleRestoreUpload)
		r.Post("/restore/{id}/apply", s.handleRestoreApply)
		r.Post("/restore/{id}/approve", s.handleRestoreApprove)
		r.Get("/restore/pending", s.handleListPendingRestores)
		r.Get("/jobs", s.handleListDRJobs)
		r.Get("/jobs/{id}/stream", s.handleDRJobStream)
		r.Get("/schedule", s.handleGetDRSchedule)
		r.Put("/schedule", s.handlePutDRSchedule)
	})
	// engine-log viewer — real-time SSE stream and ring-buffer snapshot.
	// Superadmin-gated. The log broker is optional: nil ⇒ 501.
	v1.Get("/console/logs/stream", s.handleLogStream)
	v1.Get("/console/logs/buffer", s.handleLogBuffer)

	// per-connector health metrics — the connector-health dashboard
	// surface. Gated on health:status:read (any admin/viewer, not superadmin-only)
	// so the console health view can show per-connector operational state.
	v1.Get("/connectors/health", s.handleConnectorHealth)

	v1.Get("/access-edges", s.handleListAccessEdges)
	// GET /access-edges/drift was REMOVED in (C2): it served raw,
	// UNRECONCILED drift (cross-origin false positives). The reconciled drift lives
	// at module III's GET /v1/m/accessmap/drift, the single source consumed by the
	// Terraform provider and compliance. The store-level Drift accessor stays.

	v1.Route("/audit", func(r chi.Router) {
		r.Get("/", s.handleAuditList)
		// Superadmin-only read of the system-tenant ledger (cross-tenant ops); the
		// tenant-scoped list above cannot reach it (resolveTenant rejects system).
		r.Get("/system", s.handleSystemAuditList)
		r.Get("/verify", s.handleAuditVerify)
		r.Get("/export", s.handleAuditExport)
		r.Get("/pubkey", s.handleAuditPubkey)
	})

	v1.Route("/users", func(r chi.Router) {
		r.Get("/", s.handleListUsers)
		r.Post("/", s.handleCreateUser)
		// internal-superadmin lifecycle. List superadmins (read), and
		// enable/disable an internal superadmin account (write, AAL3-gated, deny-
		// closed against total lockout). Disabling is non-destructive and reversible
		// — the global-principal counterpart to the tenant-scoped SCIM deactivate.
		r.Get("/superadmins", s.handleListSuperadmins)
		r.Post("/{id}/disable", s.handleDisableSuperadmin)
		r.Post("/{id}/enable", s.handleEnableSuperadmin)
	})
	v1.Route("/tokens", func(r chi.Router) {
		r.Get("/", s.handleListTokens)
		r.Post("/", s.handleIssueToken)
		r.Delete("/{id}", s.handleRevokeToken)
		r.Post("/{id}/rotate", s.handleRotateToken)
	})
	v1.Post("/memberships", s.handleGrantMembership)
	// the federated console search behind ⌘K. Any authenticated tenant
	// principal may call it; every kind is authorization-gated on its own read
	// permission inside the handler (deny-closed per kind, search.go).
	v1.Get("/search", s.handleSearch)
	// the tenant member roster for the console members grid — every user with
	// a membership in the resolved tenant, enriched with effective role, workspace
	// scoping and directory groups. Tenant-scoped read (user:read); the enable/
	// disable actions the grid drives are the existing tenant-scoped SCIM deactivate
	// (PATCH /scim/v2/Users/{id} active), never a new write here.
	v1.Get("/members", s.handleListMembers)
	// FASE X /: tenant-scoped console onboarding (distinct from the
	// superadmin-only POST /v1/users). Onboard a non-federated person into the
	// resolved tenant (membership:write + AAL3), invite by email or admin-set
	// password (handlers_onboarding.go). The accept leg is unauthenticated — the
	// single-use token is the gate (the invitee has no session yet).
	v1.Post("/onboard", s.handleOnboardMember)
	v1.Route("/invites", func(r chi.Router) {
		r.Get("/", s.handleListInvites)
		r.Post("/accept", s.handleAcceptInvite)
		r.Delete("/{id}", s.handleRevokeInvite)
	})
	// the operator's view of SCIM-provisioned groups and the group→role
	// mapping (a mapped group elevates its members' effective role in its tenant
	// — see core/auth ConfigureGroupRole/loadGrants). Deliberately NOT a SCIM
	// surface: the IdP pushes rosters, never roles.
	v1.Route("/groups", func(r chi.Router) {
		r.Get("/", s.handleListGroups)
		r.Put("/{id}/role", s.handleSetGroupRole)
		// S256: nest a group under another (the group hierarchy). Owner/superadmin
		// authority, acyclic, operator-only — the IdP pushes membership, never shape.
		r.Put("/{id}/parent", s.handleSetGroupParent)
	})

	v1.Route("/system", func(r chi.Router) {
		// The configured registry is a secretless operational read used by the
		// residency selector. It is deployment-wide, so it remains superadmin-only.
		r.Get("/residency", s.handleResidencyRegistry)
		r.Post("/orgs", s.handleCreateOrg)
		r.Get("/orgs", s.handleListOrgs)
		r.Delete("/orgs/{tenant}", s.handleDropOrg)
		// set/clear a tenant's data-residency pin (region). Deny-closed against
		// the residency registry (known region; the home region on a region-scoped
		// instance). Adopting residency for an existing local tenant is safe; moving a
		// tenant between regions is a data migration, out of this endpoint's scope.
		r.Put("/orgs/{tenant}/region", s.handleSetOrgRegion)
		// withdraw or restore a tenant's SERVICE without deleting its data —
		// the intermediate door the cloud grace period needs, between serving a
		// tenant and hard-deleting it. Enforcement is the store guard
		// (core/suspension), so every path is covered, not just this API.
		r.Put("/orgs/{tenant}/status", s.handleSetOrgStatus)
	})

	// SCIM 2.0 inbound service provider (RFC 7643/7644), mounted as a first-party
	// surface under /v1/scim/v2 (NOT a module — it reaches the auth partition that
	// modules cannot). Bearer-authed with a tenant-bound admin token.
	v1.Route("/scim/v2", func(r chi.Router) {
		r.Get("/Users", s.scimListUsers)
		r.Post("/Users", s.scimCreateUser)
		r.Get("/Users/{id}", s.scimGetUser)
		r.Put("/Users/{id}", s.scimReplaceUser)
		r.Patch("/Users/{id}", s.scimPatchUser)
		r.Delete("/Users/{id}", s.scimDeleteUser)
		// Groups inbound is REAL since (was honest-501): provisioning +
		// memberships, with the group→role mapping kept operator-only (/v1/groups).
		r.Get("/Groups", s.scimListGroups)
		r.Post("/Groups", s.scimCreateGroup)
		r.Get("/Groups/{id}", s.scimGetGroup)
		r.Put("/Groups/{id}", s.scimReplaceGroup)
		r.Patch("/Groups/{id}", s.scimPatchGroup)
		r.Delete("/Groups/{id}", s.scimDeleteGroup)
		r.Get("/ServiceProviderConfig", s.scimSPConfig)
		r.Get("/ResourceTypes", s.scimResourceTypes)
		r.Get("/ResourceTypes/{type}", s.scimResourceType)
		r.Get("/Schemas", s.scimSchemas)
		r.Get("/Schemas/{urn}", s.scimSchema)
		// SCIM Security Event Token receiver (RFC 9967 over RFC 8935 push
		// delivery): async deprovisioning of agents/NHIs on event, not on next
		// poll (IDN-11). Bearer-authed like the rest of the provider.
		r.Post("/Events", s.scimReceiveEvents)
	})

	// SSF 1.0 / CAEP 1.0 / RISC 1.0 receiver: continuous access
	// evaluation via SET push delivery (RFC 8935). Bearer-authed with a
	// tenant-bound token, same auth surface as the SCIM provider.
	v1.Route("/ssf", func(r chi.Router) {
		r.Post("/events", s.caepReceiveEvents)
	})

	if err := s.mountModules(v1, modules); err != nil {
		return nil, err
	}
	r.Mount("/v1", v1)
	s.mux = r
	return r, nil
}

// mountModules mounts each API module under /v1/m/<namespace>/, wrapping its
// routes with tenant resolution + authorization. It also collects the federated
// search registry: the core kinds first, then every kind a Searcher
// module contributes — rejecting duplicates and permissionless kinds at mount
// so a misdeclared kind can never reach the deny-closed per-request gate.
func (s *Server) mountModules(r chi.Router, modules []Module) error {
	s.searchKinds = s.coreSearchKinds()
	seenKinds := map[string]bool{}
	for _, k := range s.searchKinds {
		seenKinds[k.Kind] = true
	}
	// the scope-grantable permission catalog is defined BY the mounted module set,
	// so mounting rebuilds it from scratch rather than adding to whatever a previous
	// server left behind — otherwise a second server in the same process would leave the
	// first server's namespaces grantable and make the catalog depend on mount order.
	//
	// The honest cost of that choice, since a global has no owner: if a SECOND server is
	// constructed while a first is still serving, the first server's next
	// reprojectManaged sees the second's catalog, and a permission it can no longer
	// resolve is dropped from the projection — persisting a silently NARROWED
	// cedar-managed revision that stands until the next RBAC edit. Narrowing fails safe
	// (it removes authority, never adds it) but it is silent, which is the part worth
	// naming. The shipped binary constructs exactly one server (cmd/olivares boot.go) and
	// DR restore requires a restart, so this is out of reach in a deployment; it is
	// reachable for an embedder that runs two engines in one process, and for tests. The
	// fix, if that ever becomes a supported shape, is to hang the catalog off the Server
	// rather than the process — not to make the reset conditional.
	auth.ResetModuleCatalog()
	seen := map[string]bool{}
	for _, m := range modules {
		ns := m.APINamespace()
		if err := validateNamespace(ns); err != nil {
			return err
		}
		if seen[ns] {
			return errors.New("api: duplicate module namespace " + ns)
		}
		seen[ns] = true
		// Declaring a permission is what makes it grantable by a custom role or a scoped
		// grant. A malformed declaration fails the mount instead of silently
		// leaving the module's surface undelegable. NOTE: the permission's namespace is
		// deliberately NOT required to equal ns — the route-only console modules
		// (claude-policy, claude-agents, identity) reuse another module's namespace on
		// purpose, which is documented on the Module interface and measured: 3 of 33
		// modules do it.
		if err := auth.RegisterModulePermissions(m.Permissions()); err != nil {
			return fmt.Errorf("api: module %s: %w", ns, err)
		}
		if err := checkRoutePermsDeclared(m); err != nil {
			return err
		}
		// ⛔ REGISTRAR ANTES DE COMPROBAR, Y COMPROBAR ANTES DE MONTAR. Si el registro fuera
		// despues, la primera ruta gobernada quedaria rechazada por el ORDEN y no por la regla —
		// la confusion que este catalogo existe para cerrar.
		if d, ok := m.(ActionDeclarer); ok {
			if err := auth.RegisterModuleActions(ns, d.Actions()); err != nil {
				return fmt.Errorf("api: module %s: %w", ns, err)
			}
		}
		if err := checkGovernedRoutes(m, ns, s.principalEvidence); err != nil {
			return err
		}
		sub := chi.NewRouter()
		// The module's OWN capability adapter travels with its registrar, so every
		// route it registers is filed with the adapter of the module that registered
		// it. A module implementing none files nil, and its operations project as
		// not_supported rather than as the outer boolean.
		projector, _ := m.(ModuleCapabilityProjector)
		m.APIRoutes(chiRegistrar{s: s, r: sub, ns: ns, projector: projector})
		r.Mount("/m/"+ns, sub)
		if sr, ok := m.(Searcher); ok {
			for _, k := range sr.SearchKinds() {
				if k.Kind == "" || k.Permission == "" || k.Search == nil {
					return errors.New("api: module " + ns + " declared an incomplete search kind")
				}
				if seenKinds[k.Kind] {
					return errors.New("api: duplicate search kind " + k.Kind)
				}
				seenKinds[k.Kind] = true
				s.searchKinds = append(s.searchKinds, k)
			}
		}
	}
	return nil
}

// checkRoutePermsDeclared enforces the Module contract that Permissions() declares the
// permissions the module's routes require (modules.go) — "so roles can grant them". It is
// a boot invariant, not a style rule: an undeclared route permission never reaches the
// scope-grantable catalog, so its route rides the module verb tier alone and can be
// neither delegated to a scoped admin nor excluded by a custom role. governance shipped
// three such permissions across five agent-risk routes until measured it.
//
// It replays APIRoutes against a recording registrar — the same double-call the OpenAPI
// document already relies on (collectModuleRoutes), so a module that cannot tolerate it
// is already broken. No handler runs and no route is mounted by the replay.
// PrincipalEvidenceProducer reconstructs a principal's authority inside one tenant, carrying the
// private provenance AuthorizeEvidence consumes. core/auth's *Authenticator satisfies it.
type PrincipalEvidenceProducer interface {
	ResolvePrincipalScope(context.Context, auth.PrincipalRef, model.TenantID) (auth.Principal, error)
}

// checkGovernedRoutes refuses to start a server whose governed routes cannot work, and it asks
// BOTH questions in ONE replay of the module's routes.
//
// ⛔ UN SOLO REPLAY Y UNA SOLA PUERTA DE ERROR, porque son la misma pregunta en dos mitades: «¿esta
// ruta gobernada puede llegar a decidir?». Sin productor de evidencia no puede acuñar testigo
// nunca; con una acción que su módulo no declaró, la acción llega al motor de política como una
// entidad que nadie registró. Las dos hacen inútil la ruta, y las dos se ven aquí, que es el único
// sitio del árbol que ve TODAS las rutas de un módulo ANTES de montarlas.
//
// ⛔ Y DEVUELVE ERROR, NO PANIC, Y ESA DIFERENCIA ERA UNA AFIRMACIÓN MÍA FALSA. La validación de
// acción vivía en HandlePolicy y hacía `panic`; yo declaré por escrito que «api.New devuelve error
// nombrando la regla», y no era verdad — el caso que lo comprobaba tenía que convertir el panic en
// dato con `recover`, que es la huella de una afirmación que no encaja con su código. Aquí sí lo
// es: `mountModules` propaga, `api.New` devuelve, y el caso lo lee sin recuperar nada.
//
// TODAY THE EVIDENCE HALF CAN ONLY REFUSE, deliberately: nothing in the tree supplies a producer
// yet, and the real composition root boots because it registers no governed route.
//
// ⚠ Y PRESENCIA NO ES CABLEADO, dicho aquí porque el tercer contraste lo levantó y tiene razón: un
// productor guardado satisface esta guarda y NO hace que ningún camino de petición lo invoque.
// Esto refuta «nadie lo inyectó», no «nadie lo llama». El carril que cablee la evidencia sustituye
// esta comprobación por un testigo del CAMINO DE PETICIÓN — ruta gobernada, principal reconstruido
// de verdad, 200 con testigo sólido; y quitar sólo la llamada ⇒ 503 con el handler en cero.
func checkGovernedRoutes(m Module, ns string, producer PrincipalEvidenceProducer) error {
	var routes []moduleRoute
	m.APIRoutes(recordingRegistrar{ns: ns, out: &routes})
	var governed []string
	for _, r := range routes {
		if !r.governed {
			continue
		}
		governed = append(governed, r.method+" "+r.pattern)
		// ⛔ CONTRA LO QUE ESTE MÓDULO DECLARÓ, no contra un catálogo global: una ruta que cita la
		// acción de otro monta hoy y deja de montar el día que ese módulo no se cargue, o según el
		// orden en que los dos se monten. El mensaje dice la REGLA, porque un «no declarada» a
		// secas no dice dónde declararla.
		if r.cedarAction != "" && !auth.ModuleDeclaresAction(ns, auth.CedarAction(r.cedarAction)) {
			return fmt.Errorf(
				"api: route %s %s declares Cedar action %q, which module %q does not declare; a "+
					"module may only name actions it declares itself (add it to Actions())",
				r.method, r.pattern, r.cedarAction, ns)
		}
	}
	if len(governed) == 0 || producer != nil {
		return nil
	}
	return fmt.Errorf(
		"api: module %q registers %d governed route(s) (%s) but no PrincipalEvidenceProducer is "+
			"wired, so AuthorizeRoute can never mint a witness and every one of them would answer "+
			"503: install one in the composition root before mounting them",
		ns, len(governed), strings.Join(governed, ", "))
}

func checkRoutePermsDeclared(m Module) error {
	declared := map[auth.Permission]bool{}
	for _, p := range m.Permissions() {
		declared[p] = true
	}
	var routes []moduleRoute
	m.APIRoutes(recordingRegistrar{ns: m.APINamespace(), out: &routes})
	missing := map[auth.Permission]bool{}
	var names []string
	for _, r := range routes {
		if !declared[r.perm] && !missing[r.perm] {
			missing[r.perm] = true
			names = append(names, string(r.perm))
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	return fmt.Errorf("api: module %s mounts routes requiring undeclared permissions %v; add them to Permissions() so a role can grant them",
		m.APINamespace(), names)
}

// chiRegistrar adapts chi to the RouteRegistrar seam, wrapping every module route
// with the standard authorize-then-handle flow bound to a single tenant.
type chiRegistrar struct {
	s  *Server
	r  chi.Router
	ns string
	// responseHeaders are set on the ResponseWriter BEFORE anything in the mounted
	// closure can write, so they reach the engine's own refusals — the entity
	// lineage denial, the authorization denial, the concealed not-found and the
	// deferred locator error — and not only the module handler's own responses.
	// It is nil for every route that does not ask, which is all of them by default.
	responseHeaders map[string]string
	// collectionScope is the workspace selector declared for the COLLECTION routes
	// mounted through this registrar value. Like responseHeaders it travels on a COPY
	// (withCollectionScope), so one route can never observe another's declaration, and
	// it is nil for every route that did not ask — which is all of them by default.
	collectionScope *CollectionScopeRef
	// projector is the module's own capability adapter, or nil when the module
	// implements none. It is set once by mountModules from the module being mounted.
	projector ModuleCapabilityProjector
}

// WithCollectionScope returns a COPY of the registrar carrying scope. The registrar is
// a value, so the declaration cannot leak onto any other route mounted through the
// same seam — the identical argument withResponseHeaders makes.
func (cr chiRegistrar) WithCollectionScope(scope CollectionScopeRef) RouteRegistrar {
	if scope.WorkspaceQueryParam == "" {
		// A declaration with no selector names nothing to corroborate. Returning the
		// registrar unchanged keeps the route mounting exactly as it always did, which
		// is the direction that authorizes LESS.
		return cr
	}
	copied := scope
	cr.collectionScope = &copied
	return cr
}

func (cr chiRegistrar) Handle(method, pattern string, perm auth.Permission, h ModuleHandler) {
	cr.handle(method, pattern, perm, nil, auth.RouteMetadata{}, ungovernedRoute, h)
}

// NoStoreRouteRegistrar is the OPTIONAL capability a module asserts when EVERY
// response of a route — success and refusal alike — must carry
// `Cache-Control: no-store`.
//
// ⛔ IT IS AN OPTIONAL CAPABILITY ON THE CONCRETE REGISTRARS, this repository's
// documented idiom (HandlePolicy above, core/store's EpochFencer). Adding it to
// RouteRegistrar would break every implementer and test double for something no
// other module wants today.
//
// ⛔ AND, AS recordingRegistrar.HandlePolicy PUT IT, AN OPTIONAL CAPABILITY FOUND
// BY ASSERTION PUTS THE BURDEN ON EVERY REGISTRAR THAT ANSWERS: both in-tree
// registrars implement it, so the published document and the mounted server
// describe the same routes.
//
// ⛔ IT REACHES THE PRE-ROUTING REFUSALS TOO, AND THAT WAS A CORRECTION.
// An earlier version of this comment declared the opposite as a settled limit:
// that a refusal written by a global middleware before chi routes the request —
// the 401 from `authenticate`, the 429 from `rateLimit` — could not carry the
// header without changing global transport semantics for every route. An
// independent HTTP witness measured both leaving `Cache-Control` absent, and the
// stated reason did not hold: applying a route's OWN declared response metadata
// before an early writer opts no sibling into anything. `routeResponseMetadata`
// does exactly that, and a plain sibling mounted through this same registrar
// still sends no cache directive.
//
// So the declaration now covers every response of the route, including the two
// written before routing, and the published contract says so for all of them.
type NoStoreRouteRegistrar interface {
	// HandleNoStore mounts a collection route whose every response carries no-store.
	HandleNoStore(method, pattern string, perm auth.Permission, handler ModuleHandler)
	// HandleEntityNoStore mounts an entity route whose every response carries no-store.
	HandleEntityNoStore(method, pattern string, perm auth.Permission, ref EntityRef, handler ModuleHandler)
}

// NoStoreResponseHeaders is the exact header set the capability applies. It is a
// function, not a literal, so the runtime and the published contract cite one
// source instead of two spellings of the same intent.
func NoStoreResponseHeaders() map[string]string {
	return map[string]string{"Cache-Control": "no-store"}
}

// declareRouteResponseHeaders records a route's response metadata under the FULL
// pattern the router reports for it, so routeResponseMetadata can find it before
// the request is routed.
//
// The key is built from the mount layout (`/v1/m/<namespace>` + the module's own
// pattern) rather than guessed: mountModules mounts every module there and
// nowhere else. A key that did not match what chi reports would apply nothing —
// silently — so core/api's capability test drives a REAL invalid-credential 401
// and a REAL limiter 429 through a mounted server rather than inspecting this map.
//
// ⛔ THE DECLARATION IS PER ROUTE AND IMMUTABLE ONCE FILED. Each entry is a fresh
// COPY of the registrar's map, so one route can never observe or alter another's,
// and the table is written ONLY here — inside mountModules, during New — and read
// only afterwards, from the request path. Nothing at request time writes to it,
// so a served request cannot give a route a directive it did not declare.
func (cr chiRegistrar) declareRouteResponseHeaders(method, pattern string) {
	if len(cr.responseHeaders) == 0 || cr.s == nil {
		return
	}
	if cr.s.routeResponseHeaders == nil {
		cr.s.routeResponseHeaders = map[string]map[string]string{}
	}
	copied := make(map[string]string, len(cr.responseHeaders))
	for name, value := range cr.responseHeaders {
		copied[name] = value
	}
	cr.s.routeResponseHeaders[method+" "+canonicalRoutePattern("/v1/m/"+cr.ns+pattern)] = copied
}

// routeResponseMetadata applies a matched route's DECLARED response headers
// before any middleware downstream of it can answer.
//
// ⛔ WHY IT EXISTS AT ALL, since a route already sets its own headers. Those two
// are not the same coverage. The route's own loop runs inside the mounted
// closure, so it reaches every answer the ROUTE produces — including the engine's
// entity-lineage denial and concealed not-found — but nothing that answers
// earlier. `authenticate` refuses an invalid supplied credential and `rateLimit`
// refuses a throttled caller BEFORE chi routes the request at all, and an
// independent HTTP witness measured both leaving `Cache-Control` absent. This
// middleware closes exactly those, and the route's own loop stays: it is the one
// that still applies when a registrar is exercised without the full chain.
//
// ⛔ AND IT IS NOT A GLOBAL CACHE POLICY. It applies only what a route DECLARED,
// to that route. A sibling mounted through the same registrar without a
// declaration matches nothing here and still sends no cache directive, which the
// capability test asserts in the same run as the two refusals above.
//
// The pattern is resolved with chi's own matcher over the built router, AND with
// chi's own choice of routing path and method, so this cannot drift from how the
// request will actually be routed. Matching costs one radix-tree lookup and
// happens only while at least one route has declared something.
func (s *Server) routeResponseMetadata(next http.Handler) http.Handler {
	// ⚠ THE EMPTY-TABLE CHECK IS INSIDE THE CLOSURE, NOT HERE. The middleware chain
	// is assembled before mountModules runs, so at build time NO route has declared
	// anything yet; short-circuiting on that would remove this middleware from
	// every server permanently — including the one whose routes declare something a
	// moment later. deprecationHeaders may short-circuit because its table is set
	// in New; this one is filled during mounting.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(s.routeResponseHeaders) > 0 && s.mux != nil {
			// A FRESH route context — the live one must not be mutated — matched
			// against the built router with the EFFECTIVE method and path chi will
			// itself dispatch on, then canonicalised the same way the pattern was
			// filed. Nothing about the request is decoded or rewritten.
			method, routePath := effectiveChiRouteRequest(r)
			rctx := chi.NewRouteContext()
			if s.mux.Match(rctx, method, routePath) {
				key := method + " " + canonicalRoutePattern(rctx.RoutePattern())
				for name, value := range s.routeResponseHeaders[key] {
					w.Header().Set(name, value)
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// effectiveChiRouteRequest reports the method and path the mounted router will
// actually dispatch this request on, chosen exactly as chi chooses them.
//
// ⛔ IT EXISTS BECAUSE "THE SAME MATCHER" IS NOT "THE SAME RESOLUTION".
// routeResponseMetadata used to call Match with `r.URL.Path`, and an independent
// HTTP witness measured the consequence on the real mounted K3 router: chi's
// routeHTTP prefers a non-empty `URL.RawPath`, so for an ESCAPED URL the two
// disagreed about which route was being served, in both directions —
//
//   - GET /v1/m/sessions/channels/bad%2Fid/grants with an invalid bearer.
//     chi routes it to the declared grant sheet ({id}/grants, id="bad%2Fid");
//     the decoded Path is .../channels/bad/id/grants, which matches nothing. The
//     pre-routing 401 of a route that DID declare no-store went out without it.
//   - GET /v1/m/sessions/channels/%61dministration. chi routes the raw literal to
//     the ordinary {id} detail route, which declares nothing; the decoded Path is
//     .../channels/administration, the catalog. A route that declared NOTHING was
//     given the catalog's directive, on its 401 and on its authenticated 404.
//
// The second is the one that matters most: applying a declaration to a route that
// never made it is exactly the "global cache policy" this middleware is not
// allowed to become, arriving through the back door.
//
// ⚠ THE CURE IS TO MATCH WHAT CHI MATCHES, NEVER TO DECODE THE REQUEST. Rewriting
// `r.URL` so the decoded path routes "correctly" would change what the ROUTER
// sees, i.e. change dispatch to fit the metadata guess — the opposite of the
// requirement. This function only reads.
//
// The order below is chi's own (mux.go routeHTTP): a route context already
// carrying a RoutePath wins (a parent router that has consumed a prefix), then a
// non-empty RawPath, then Path, then "/". The live route context is READ and
// never written; chi installs it in Mux.ServeHTTP before the middleware chain
// runs, so at this depth RoutePath and RouteMethod are empty and the RawPath term
// is the material one — but reading them keeps this correct if the engine is ever
// mounted under a parent router.
//
// ⚠ `deprecationHeaders` (stability.go) performs the same class of pre-routing
// resolution and still selects `URL.Path`. That is NOT changed here: it is a
// different subsystem with its own table, contract and tests, and this task is
// bounded to the response-metadata resolver. It is named so that the next reader
// does not conclude from the earlier version of the comment above — which said
// this reused the deprecation idiom — that the two are still identical. They are
// not: this one follows dispatch, that one does not.
func effectiveChiRouteRequest(r *http.Request) (method, routePath string) {
	method = r.Method
	if rctx := chi.RouteContext(r.Context()); rctx != nil {
		routePath = rctx.RoutePath
		if rctx.RouteMethod != "" {
			method = rctx.RouteMethod
		}
	}
	if routePath == "" {
		if r.URL.RawPath != "" {
			routePath = r.URL.RawPath
		} else {
			routePath = r.URL.Path
		}
		if routePath == "" {
			routePath = "/"
		}
	}
	return method, routePath
}

func (cr chiRegistrar) HandleNoStore(
	method, pattern string, perm auth.Permission, h ModuleHandler,
) {
	cr.withResponseHeaders(NoStoreResponseHeaders()).Handle(method, pattern, perm, h)
}

func (cr chiRegistrar) HandleEntityNoStore(
	method, pattern string, perm auth.Permission, ref EntityRef, h ModuleHandler,
) {
	cr.withResponseHeaders(NoStoreResponseHeaders()).HandleEntity(method, pattern, perm, ref, h)
}

// withResponseHeaders returns a COPY of the registrar carrying the headers. The
// registrar is a value, so the copy cannot leak the declaration onto any other
// route mounted through the same seam.
func (cr chiRegistrar) withResponseHeaders(headers map[string]string) chiRegistrar {
	if len(headers) == 0 {
		return cr
	}
	copied := make(map[string]string, len(headers))
	for name, value := range headers {
		copied[name] = value
	}
	cr.responseHeaders = copied
	return cr
}

// entityRefFor is the mapping from a route's declared metadata to the reference the engine
// uses to find the row.
//
// ⛔ IT IS A FUNCTION SO THAT THE MAPPING CAN BE OBSERVED WITHOUT A TEST-ONLY FIELD ON THE
// REGISTRAR. The defect it fixes (B01) was not that the route declared nothing - it was that
// the declaration did not REACH the engine, so the test that matters reads what is handed
// over rather than what was declared. Inlined, that step had nowhere to be looked at except
// through a mounted server, and a test that mounts one is measuring five things at once.
//
// ⛔ AND THE LOCATOR TRAVELS WITH THE KIND. Without it the engine has an entity kind and no
// way to find the row, so it denies BEFORE the handler - the route is mounted, reachable and
// unusable, and the failure looks like an authorization decision. RouteMetadata.Validate
// refuses that combination at boot, so a ref built here always carries exactly one locator.
func entityRefFor(meta RouteMetadata) *EntityRef {
	if meta.CoreKind == CoreKindNone {
		// A route that resolves its own resource gets no ref: handing it one would send it
		// through a row lookup it does not want and cannot satisfy.
		return nil
	}
	// ⛔ ALL FIVE, NOT THREE. This used to carry CoreKind and the two locators and drop
	// ResourceKind and ConcealDeniedAsNotFound on the floor, so the policy door could describe
	// WHERE the row is and not WHAT it is nor how a denial should look. The kind is not
	// presentation: with it empty the server derives the kind from the permission, and the
	// scope resolver only adds a session's agent groups when the kind is `session`.
	return &EntityRef{
		CoreKind:                meta.CoreKind,
		IDParam:                 meta.IDParam,
		BodyIDField:             meta.BodyIDField,
		ResourceKind:            meta.ResourceKind,
		ConcealDeniedAsNotFound: meta.ConcealDeniedAsNotFound,
	}
}

// HandlePolicy mounts a route WITH its sealed metadata (V269 / architecture §7.1).
//
// ⛔ IT IS A METHOD ON THE CONCRETE REGISTRAR AND NOT A METHOD ON RouteRegistrar, which is
// this repository's own idiom for an optional capability — core/store documents EpochFencer
// the same way ("asserted like AuditSpoolStatuser / CanonicalWalker"). Adding it to the
// interface would break every implementer, test doubles included, for a capability most
// modules do not want; a type assertion lets the module that needs it ask, and lets one that
// finds it absent decide what to do rather than be told at compile time.
//
// ⛔ AND A MODULE THAT FINDS IT ABSENT MUST NOT FALL BACK TO Handle FOR A GOVERNED ROUTE.
// Handle carries method, pattern and permission and drops the AAL floor, the Cedar action,
// the scoped-grant requirement and the row lineage, so the route would serve under
// substantially weaker authorization than its table declares. That is finding B01 of the
// 2026-09-03 contrast, and the fix is this method existing rather than a comment asking
// callers to be careful.
func (cr chiRegistrar) HandlePolicy(method, pattern string, perm auth.Permission, meta RouteMetadata, h ModuleHandler) {
	if err := meta.Validate(); err != nil {
		// AT MOUNT, AND IT PANICS, exactly as HandleEntity does for an impossible EntityRef:
		// registration runs at boot inside the composition root, so a panic is a REFUSAL TO
		// START with the reason in the message. Deferring to the first request would serve
		// every other route while this one waited to be discovered.
		panic(fmt.Sprintf("%v (namespace %q, %s %s)", err, cr.ns, method, pattern))
	}
	// La accion NO se valida aqui: vive en checkGovernedRoutes, que corre ANTES de montar y
	// devuelve error en vez de panic. Dos sitios comprobando lo mismo son dos productores, y el
	// que hace panic es el que convierte una afirmacion sobre `api.New` en algo que un test tiene
	// que `recover` para leer.
	ref := entityRefFor(meta)

	// ⛔ AND THE REF IS VALIDATED TOO, WHICH THIS DOOR DID NOT DO. meta.Validate answers
	// questions about the METADATA; validateCoreEntityRef answers the ones about the ROW and
	// its resolver, and only the second can see that a CoreKind route was mounted with no
	// CoreEntityAuthorizationResolver wired. Options promises to refuse such a mount; without
	// this call the promise was kept by HandleEntity and broken here, and the nil surfaced on
	// the first request instead of at boot - as a denial, which is the one failure a reader
	// cannot tell from working correctly.
	if ref != nil {
		if err := validateCoreEntityRef(*ref, cr.s.coreEntityResolver); err != nil {
			panic(fmt.Sprintf("%v (namespace %q, %s %s)", err, cr.ns, method, pattern))
		}
	}
	cr.handle(method, pattern, perm, ref, meta.RouteMetadata, governedRoute, h)
}

// HandleSealed is the governed door that accepts ONLY metadata which went through SealRoute.
//
// ⛔ IT EXISTS BECAUSE HandlePolicy CANNOT REFUSE A LITERAL, and a seal nobody has to use is a
// convention rather than a mechanism. api.RouteMetadata embeds auth.RouteMetadata, which
// PROMOTES its exported fields, so any caller could write MinimumAAL back to zero or
// RequireScopedGrant to false between declaring a route and registering it — and the route would
// mount looking exactly like one that had asked for less. SealedRoute keeps that value
// unexported and offers only tightenings, so the loosening is not something a caller must
// remember not to write: it is something they cannot spell.
//
// ⚠ AND THIS DOES NOT CLOSE R-629 ON ITS OWN, which is why the fila stays open. While
// HandlePolicy still accepts a literal, this door is the safe one rather than the only one. The
// switchover — HandlePolicy retiring or requiring the seal — moves six call sites that live in a
// module whose package does not compile against the current pin, so it lands with that pin and
// not before. Delivering it today would mean shipping a change whose composition root never
// compiled.
func (cr chiRegistrar) HandleSealed(method, pattern string, perm auth.Permission, s SealedRoute, h ModuleHandler) {
	if !s.IsSealed() {
		// AT MOUNT AND IT PANICS, like its neighbours: a zero SealedRoute carries a zero
		// RouteMetadata, which is perfectly legal metadata, so without this the door would
		// silently register the historical unrestricted route in place of whatever the caller
		// meant to seal.
		panic(fmt.Sprintf(
			"api: HandleSealed was given metadata that never went through SealRoute "+
				"(namespace %q, %s %s)", cr.ns, method, pattern))
	}
	cr.HandlePolicy(method, pattern, perm, s.Metadata(), h)
}

// HandleEntity mounts an entity route: the row is read BEFORE authorization so the
// scoped engine sees the entity's stored lineage rather than a collection-level
// blank. See EntityRef.
func (cr chiRegistrar) HandleEntity(method, pattern string, perm auth.Permission, ref EntityRef, h ModuleHandler) {
	// ⛔ AT MOUNT, AND IT PANICS. Registration runs at boot inside the composition
	// root, so a panic here is a REFUSAL TO START with the reason in the message —
	// the loud end of the same choice entityResource already makes for its own
	// mutually-exclusive fields. The alternative, deferring to the first request,
	// would serve every other route while this one waited to be discovered.
	if err := validateCoreEntityRef(ref, cr.s.coreEntityResolver); err != nil {
		panic(fmt.Sprintf("%v (namespace %q, %s %s)", err, cr.ns, method, pattern))
	}
	cr.handle(method, pattern, perm, &ref, auth.RouteMetadata{}, ungovernedRoute, h)
}

// restoredRequestBody replays the bytes consumed by the body entity locator and
// then continues with the unread tail. Retaining the original Closer preserves
// the request body's lifecycle even when the bounded probe stopped at max+1.
type restoredRequestBody struct {
	io.Reader
	io.Closer
}

// bodyEntityID locates one exact top-level JSON string without interpreting the
// rest of the module payload. It deliberately does not reject unknown fields:
// semantic closure belongs to the module's decoder after authorization.
//
// The body is restored before returning on every path. The max+1 probe both
// proves the limit and avoids buffering an unbounded unauthenticated request.
func bodyEntityID(r *http.Request, field string) (string, error) {
	if r.Body == nil {
		return "", fmt.Errorf("%w: entity body is absent", errBadRequest)
	}
	original := r.Body
	raw, readErr := io.ReadAll(io.LimitReader(original, maxBodyBytes+1))
	r.Body = restoredRequestBody{
		Reader: io.MultiReader(bytes.NewReader(raw), original),
		Closer: original,
	}
	if readErr != nil {
		return "", fmt.Errorf("%w: read entity body", errBadRequest)
	}
	if len(raw) > maxBodyBytes {
		return "", errRequestBodyTooLarge
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	first, err := dec.Token()
	if err != nil {
		return "", fmt.Errorf("%w: invalid entity body", errBadRequest)
	}
	open, ok := first.(json.Delim)
	if !ok || open != '{' {
		return "", fmt.Errorf("%w: entity body must be an object", errBadRequest)
	}

	var (
		id    string
		found bool
	)
	for dec.More() {
		nameToken, tokenErr := dec.Token()
		if tokenErr != nil {
			return "", fmt.Errorf("%w: invalid entity body field", errBadRequest)
		}
		name, ok := nameToken.(string)
		if !ok {
			return "", fmt.Errorf("%w: invalid entity body field name", errBadRequest)
		}
		var value json.RawMessage
		if decodeErr := dec.Decode(&value); decodeErr != nil {
			return "", fmt.Errorf("%w: invalid entity body field value", errBadRequest)
		}
		if name != field {
			continue
		}
		if found {
			return "", fmt.Errorf("%w: duplicate entity body field", errBadRequest)
		}
		found = true
		if decodeErr := json.Unmarshal(value, &id); decodeErr != nil {
			return "", fmt.Errorf("%w: entity body field must be a string", errBadRequest)
		}
	}
	last, err := dec.Token()
	if err != nil {
		return "", fmt.Errorf("%w: invalid entity body object", errBadRequest)
	}
	close, ok := last.(json.Delim)
	if !ok || close != '}' {
		return "", fmt.Errorf("%w: invalid entity body object", errBadRequest)
	}
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("%w: entity body has trailing data", errBadRequest)
	}
	if !found || id == "" {
		return "", fmt.Errorf("%w: entity body field is absent", errBadRequest)
	}
	return id, nil
}

func entityBaseResource(perm auth.Permission, ref EntityRef) auth.ResourceAttrs {
	res := auth.ResourceFor(perm)
	if ref.ResourceKind != "" {
		res.Kind = ref.ResourceKind
	}
	return res
}

// entityResource resolves the authorization resource for an entity route by reading the
// STORED row: the id from the declared path parameter or body field, and the workspace
// from the column the module declared.
//
// Everything authoritative comes from the store, never from the request — a caller cannot
// name a workspace it does not own its way into a grant, which is the property
// declaredScope was written to protect and this strengthens rather than relaxes.
//
// It fails CLOSED on a store error. Ordinary routes degrade to collection level — id
// set, no workspace — when the row is missing and leave the handler to write 404.
// A ConcealDeniedAsNotFound route additionally receives the found witness so its wrapper
// can unify absence and denial without confusing an unavailable lookup with either. A
// blank workspace is an entity with no lineage, and no tree grant may match it.
func (cr chiRegistrar) entityResource(
	r *http.Request,
	perm auth.Permission,
	ref EntityRef,
) (auth.ResourceAttrs, model.TenantID, bool, error, error) {
	res := entityBaseResource(perm, ref)
	if (ref.IDParam == "") == (ref.BodyIDField == "") {
		return res, "", false, nil, errors.New(
			"api: entity route must declare exactly one of IDParam and BodyIDField",
		)
	}
	// A concealing route must be able to establish whether the row exists, so it needs
	// a kind — either the external Kind or, since V269, a CoreKind. Accepting only the
	// first made CoreKind and conceal-404 MUTUALLY UNREACHABLE: coreentity.go refuses a
	// route that declares both kinds, and this refused a concealing route that declared
	// only the core one, so the exact combination COCKPIT-02 requires could not exist.
	// An adversarial contrast found it by reading the two guards together; neither test
	// saw it alone, because the mount-time validator never runs entityResource.
	if ref.ConcealDeniedAsNotFound && ref.Kind == "" && ref.CoreKind == CoreKindNone {
		return res, "", false, nil, errors.New(
			"api: a concealed entity route must declare Kind or CoreKind",
		)
	}
	id := ""
	if ref.IDParam != "" {
		id = chi.URLParam(r, ref.IDParam)
	} else {
		var locatorErr error
		id, locatorErr = bodyEntityID(r, ref.BodyIDField)
		if locatorErr != nil {
			return res, "", false, locatorErr, nil
		}
	}
	if id == "" {
		return res, "", false, nil, nil
	}
	res.ID = id
	if ref.CoreKind != CoreKindNone {
		return cr.coreEntityResource(r, res, ref, model.ID(id))
	}
	if ref.Kind == "" || (ref.WorkspaceColumn == "" && !ref.ConcealDeniedAsNotFound) {
		return res, "", true, nil, nil
	}
	p, ok := principalFrom(r.Context())
	if !ok {
		return res, "", false, nil, nil // the shared authz path writes the 401
	}
	tenant, err := cr.s.resolveTenant(r, p)
	if err != nil {
		return res, "", false, nil, nil // likewise: let the shared path map the error
	}
	res, found, verr := cr.s.storedEntityLineage(r.Context(), tenant, res, ref, id)
	if verr != nil {
		return res, tenant, false, nil, verr
	}
	return res, tenant, found, nil, nil
}

// storedEntityLineage reads the declared row and stamps its STORED workspace onto res.
//
// ⛔ IT IS A METHOD ON THE SERVER, NOT INLINE, BECAUSE IT HAS A SECOND CONSUMER. The
// self capability projection must decide an entity operation against the SAME lineage
// the route would use, and a projection that resolved the row its own way would be
// answering a different question from the one the handler will be authorized for. Two
// copies of a lineage rule derive, and the one that derives is the one fewer people read.
//
// It fails CLOSED on a store error and reports absence separately: a missing row leaves
// the resource at collection level — id set, no workspace — exactly as before.
func (s *Server) storedEntityLineage(
	ctx context.Context,
	tenant model.TenantID,
	res auth.ResourceAttrs,
	ref EntityRef,
	id string,
) (auth.ResourceAttrs, bool, error) {
	found := false
	verr := s.st.View(ctx, tenant, func(sc store.Scope) error {
		repo, e := sc.Ext(ref.Kind)
		if e != nil {
			return e
		}
		rec, e := repo.Get(ctx, model.ID(id))
		if errors.Is(e, store.ErrNotFound) {
			return nil // no row: stay at collection level, the handler answers 404
		}
		if e != nil {
			return e
		}
		found = true
		if ref.WorkspaceColumn != "" {
			if ws := rec.String(ref.WorkspaceColumn); ws != "" {
				res.WorkspaceID = model.ID(ws)
			}
		}
		return nil
	})
	if verr != nil {
		return res, false, verr
	}
	return res, found, nil
}

// authzEntityResource is the ordinary authorize-then-handle gate with one narrow
// wire choice: an authenticated denial on an existence-concealing point route is
// written as 404. Authentication and tenant resolution retain their shared
// responses, notably 401; only the final authorization denial is conceal-able.
// authzEntityResourcePolicy is authzEntityResource with the route's metadata carried in.
// The old signature stays and delegates with a zero, so every existing route decides
// bit-for-bit as before.
func (cr chiRegistrar) authzEntityResourcePolicy(
	w http.ResponseWriter,
	r *http.Request,
	perm auth.Permission,
	res auth.ResourceAttrs,
	meta auth.RouteMetadata,
	concealDenied bool,
	governed routeGovernance,
) (auth.Principal, model.TenantID, auth.RouteAuthorizationWitness, bool) {
	// ⛔ AQUI VIVE EL CONCEAL, Y POR ESO LA REGLA DE ERRORES NO CAMBIA. La ruta elige su denial:
	// con conceal, `store.ErrNotFound` -> 404. Si el middleware pasara a escribir el error tipado
	// de la autorizacion, esa 404 se volveria 403 y CONFIRMARIA QUE LA FILA EXISTE, que es lo
	// unico que el conceal existe para impedir.
	denial := errForbidden
	if concealDenied {
		denial = store.ErrNotFound
	}
	return cr.s.authzTenantResourcePolicy(w, r, perm, res, meta, denial, governed)
}

func (cr chiRegistrar) authzEntityResource(
	w http.ResponseWriter,
	r *http.Request,
	perm auth.Permission,
	res auth.ResourceAttrs,
	concealDenied bool,
) (auth.Principal, model.TenantID, bool) {
	if concealDenied {
		return cr.s.authzTenantResourceWithDenial(
			w, r, perm, res, store.ErrNotFound,
		)
	}
	return cr.s.authzTenantResource(w, r, perm, res)
}

// ⛔ handle SIRVE A LAS TRES GRAMATICAS DE REGISTRO, asi que la gobernanza tiene que ENTRAR:
// deducirla aqui es imposible sin volver a mirar la metadata, que es justo lo que no vale
// (una ruta gobernada con metadata vacia se degradaria sola). Handle y HandleEntity pasan
// ungovernedRoute; HandlePolicy —y por tanto HandleSealed— pasa governedRoute.
func (cr chiRegistrar) handle(method, pattern string, perm auth.Permission, ref *EntityRef, meta auth.RouteMetadata, governed routeGovernance, h ModuleHandler) {
	// File the declaration where a PRE-ROUTING writer can find it. Registration
	// time is the only moment that knows both the namespace and the pattern.
	cr.declareRouteResponseHeaders(method, pattern)
	// And file the route's OWN description — door, metadata, locator, concealment,
	// declared collection scope — for the self capability projection. Registration is
	// likewise the only moment that knows all of them together.
	cr.declareRouteDescriptor(method, pattern, perm, ref, meta, governed)
	cr.r.MethodFunc(method, pattern, func(w http.ResponseWriter, r *http.Request) {
		// FIRST, before any branch can write: a route that declared response
		// headers gets them on every answer it produces, including the refusals
		// below that return before the module handler is ever called. Nil for
		// every route that did not declare any, so this is inert by default.
		for name, value := range cr.responseHeaders {
			w.Header().Set(name, value)
		}
		var (
			p      auth.Principal
			tenant model.TenantID
			ok     bool
			// witness is the record of the decision that let this request through. For an
			// ungoverned door it stays the zero value, which authorizes nothing by construction.
			witness  auth.RouteAuthorizationWitness
			resource = auth.ResourceFor(perm)
		)
		// ⛔ LA EVIDENCIA SE INSTALA AQUÍ, ANTES DE DIVIDIR COLECCIÓN Y ENTIDAD, Y SÓLO EN RUTA
		// GOBERNADA. Antes esta pregunta vivía en la rama de entidad, y eso tenía dos defectos: la
		// colección no la hacía nunca, y la de entidad comprobaba el principal CRUDO —el que aún no
		// tiene evidencia— así que su respuesta era 503 pase lo que pase. Aquí cubre las dos ramas y
		// valida el principal ya reconstruido.
		//
		// Sigue valiendo por qué va ANTES del cargador de entidad: una petición que no puede
		// decidirse provocaría igualmente una lectura no autorizada —coste, latencia, bloqueos y
		// trazas del resolvedor—, que es un oráculo por otra vía aunque el cuerpo salga idéntico.
		//
		// Una ruta NO gobernada no pasa por aquí: su camino booleano no pide evidencia, y hacerle
		// esta pregunta le impondría un requisito que nunca declaró.
		if governed {
			var cont bool
			if r, cont = cr.s.prepareGovernedPrincipal(w, r); !cont {
				return
			}
		}
		if ref == nil && cr.collectionScope != nil {
			// A DECLARED collection scope is corroborated BEFORE the decision, and the
			// corroborated workspace becomes the authorization resource. Everything
			// else about this route — its door, its denial, its handler — is unchanged.
			p, tenant, resource, witness, ok = cr.s.authzScopedCollectionPolicy(
				w, r, perm, meta, *cr.collectionScope, governed,
			)
		} else if ref == nil {
			p, tenant, witness, ok = cr.s.authzTenantResourcePolicy(
				w, r, perm, resource, meta, errForbidden, governed,
			)
		} else {
			res, _, found, locatorErr, err := cr.entityResource(r, perm, *ref)
			if err != nil {
				// Deny-closed: a lineage we could not read is not a lineage we may
				// ignore — authorizing at collection level here would silently widen
				// the route back to what it was before it opted in.
				cr.s.log.Error("api: could not resolve the entity lineage for an authorization decision (deny-closed)",
					"err", err, "namespace", cr.ns, "pattern", pattern, "request_id", requestID(r.Context()))
				if ref.ConcealDeniedAsNotFound {
					cr.s.writeError(w, r, errEntityAuthorizationUnavailable)
				} else {
					cr.s.writeError(w, r, errForbidden)
				}
				return
			}
			if locatorErr != nil {
				// Do not turn JSON validity, the locator spelling or the body limit into
				// an oracle. First ask the exact collection-level authorization question;
				// only an admitted caller receives the deferred client error.
				p, tenant, witness, ok = cr.authzEntityResourcePolicy(
					w, r, perm, res, meta, ref.ConcealDeniedAsNotFound, governed,
				)
				if !ok {
					return
				}
				cr.s.writeError(w, r, locatorErr)
				return
			}
			p, tenant, witness, ok = cr.authzEntityResourcePolicy(
				w, r, perm, res, meta, ref.ConcealDeniedAsNotFound, governed,
			)
			if ok && ref.ConcealDeniedAsNotFound && !found {
				cr.s.writeError(w, r, store.ErrNotFound)
				return
			}
			resource = res
		}
		if !ok {
			return
		}
		// The recorder is an engine wrapper around the module route, not part of
		// the module handler. Keep the already-authenticated, pre-confinement
		// context for its tenant-global policy/session ledger. A recovered
		// workspace-scoped token otherwise makes recording.config unreadable and
		// turns every recorded module route into a false 503 after restart.
		recorderCtx := r.Context()
		// B-03: mark the request with the caller's workspace confinement,
		// AFTER authorization (so the PDP's own reads keep engine authority) and
		// BEFORE the handler. Every store handle the handler can reach — mc.Data
		// and the module's boot-time ModuleData alike — reads the mark from this
		// context, so a module route is row-confined without the handler filtering.
		r = r.WithContext(withModuleRequestBoundary(r.Context(), tenant, p))
		mc := ModuleContext{
			Principal: p,
			Tenant:    tenant,
			Resource:  resource,
			// ⛔ EL TESTIGO LLEGA AL HANDLER. Sin esto la decision se quedaba en un booleano y
			// ninguna invariante suya alcanzaba a los bytes del cable (invariante V). Para las
			// puertas no gobernadas es el valor cero, que no autoriza nada por construccion.
			Authorization: witness,
			Data:          NewScopedData(cr.s.st, tenant),
		}
		rec := cr.s.recorder
		if rec == nil {
			h(w, r, mc)
			return
		}
		// Privileged-session recording. Gate runs BEFORE the handler and is
		// deny-closed: on a recorded surface, no appendable evidence trail means no
		// privileged action (recording.go).
		call := recordedCall(r, cr.ns, method, pattern, perm, p, tenant)
		dec, err := rec.Gate(recorderCtx, call)
		if err != nil {
			if errors.Is(err, ErrRecordingConsentRequired) {
				cr.s.writeError(w, r, err)
				return
			}
			cr.s.log.Error("api: session-recording gate denied a privileged request (deny-closed)",
				"err", err, "namespace", cr.ns, "pattern", pattern, "request_id", requestID(r.Context()))
			cr.s.writeError(w, r, errRecordingUnavailable)
			return
		}
		if !dec.Record {
			h(w, r, mc)
			return
		}
		if dec.Session.IsZero() {
			cr.s.log.Error("api: session-recording gate returned a recorded decision without a session (deny-closed)",
				"namespace", cr.ns, "pattern", pattern, "request_id", requestID(r.Context()))
			cr.s.writeError(w, r, errRecordingUnavailable)
			return
		}
		// Carry the exact witness returned by Gate into the module handler. A
		// governed handler must never re-resolve an arbitrary active session by
		// credential after this point: another request may seal it and open a new
		// one before the governed transaction starts.
		mc.RecordingSession = dec.Session
		// Capture the outcome: response status via the same Unwrap-able recorder the
		// access log uses (streaming stays intact), request-body bytes through a
		// SHA-256 tee (a digest binds the exact bytes; content is never buffered).
		sw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		bh := newBodyHasher(r.Body)
		r.Body = bh
		start := cr.s.clock.Now().Time()
		// The frame append runs DEFERRED so a panicking handler still leaves its
		// frame (the recoverer above writes the 500; the panic is the action most
		// worth recording). It must also survive a client disconnect: the action
		// already ran, so its evidence cannot ride a context the peer can cancel.
		panicked := true
		defer func() {
			status := sw.status
			if panicked {
				status = http.StatusInternalServerError
			}
			res := RecordedResult{
				Status:     status,
				BodySHA256: bh.sum(),
				BodyBytes:  bh.n,
				DurationMS: cr.s.clock.Now().Time().Sub(start).Milliseconds(),
			}
			if rerr := rec.Record(context.WithoutCancel(recorderCtx), call, dec, res); rerr != nil {
				// The action already ran; a failed frame append cannot retro-deny it. The
				// recorder keeps the gap permanently evident (reserved > written); this log
				// line is the immediate operational signal.
				cr.s.log.Error("api: session-recording frame append failed — recording gap",
					"err", rerr, "namespace", cr.ns, "pattern", pattern,
					"session", string(dec.Session), "request_id", requestID(r.Context()))
			}
		}()
		h(sw, r, mc)
		panicked = false
	})
}

// NewHTTPServer returns a hardened *http.Server bound to addr serving this API.
// The caller attaches TLS and runs ListenAndServeTLS / Shutdown.
func (s *Server) NewHTTPServer(addr string) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelError),
	}
}

// licenseStatus returns the informational license status, licensee and the attested
// display-only labels (plan, support tier). It NEVER affects behavior (LICENSING.md): a
// missing/invalid/expired license is reported, never enforced; the labels are surfaced
// only for the deployment's own display and are never an entitlement input (LICENSING.md
// key custody). When the live edition service is wired it reads the LIVE status
// so a hot-applied install/renewal/expiry is reflected without a restart; otherwise it
// falls back to the static boot blob (an embedder/test that did not opt into the
// service). The live read is the cheap, no-DB LicenseDisplay (this is the
// unauthenticated server-info path — it must not count user accounts).
func (s *Server) licenseStatus() LicenseDisplayInfo {
	if s.license != nil {
		return s.license.LicenseDisplay()
	}
	if s.licenseBlob == "" || len(s.licensePub) != ed25519.PublicKeySize {
		return LicenseDisplayInfo{Status: "none"}
	}
	// VerifyEnvelope, not Verify: the engine must read BOTH signed containers. Until 2026-08-11
	// this line called Verify, which returns the flat claim set only, so an aggregate v3
	// credential — the one the license Worker issues for a Dodo purchase — came back
	// ErrMalformed and this deployment reported "invalid" for a license it had just been sold.
	v, err := license.VerifyEnvelope(s.licenseBlob, s.licensePub)
	if err != nil {
		return LicenseDisplayInfo{Status: "invalid"}
	}
	return LicenseDisplayInfo{
		Status:      string(v.Status(s.clock.Now().Time())),
		Licensee:    v.Licensee(),
		Plan:        v.Plan(),
		SupportTier: v.SupportTier(),
	}
}
