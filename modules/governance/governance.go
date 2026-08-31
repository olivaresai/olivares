// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
)

// Name is the module's globally unique identifier (the runtime registry key).
const Name = "olivares.governance"

// Namespace is the module's store and API namespace: its entities are
// "governance.<entity>" and its routes mount under /v1/m/governance/.
const Namespace = "governance"

// The module's permissions, granted to the built-in roles by verb tier (viewer→
// read, editor→write, admin/owner→admin). Reading the identity roster and
// bindings is recon-relevant (docs/SECURITY-HARDENING.md) but read-tier and self-audited; binding
// agents, syncing the roster and authoring policy are admin-tier privileged
// mutations; requesting an approval is a write; deciding/sweeping is admin-tier.
const (
	permIdentityRead auth.Permission = "governance:identity:read"
	// permIdentityPostureRead gates the identity console's POSTURE surface (the WIF
	// object graph, the SSO connection state, the External Keys/CMEK inventory and
	// workspace residency). It is SEPARATE from permIdentityRead on purpose: the roster
	// reads above are deliberately viewer-tier (roster_identity_test.go:284 asserts it),
	// while the posture surface is NEGATIVE security posture — which workspaces have NO
	// customer-managed key, i.e. a map of where the weak link is — and is registered in
	// core/auth's privilegedReadPerms so it is granted from editor up and never to the
	// lowest viewer role. One permission for both would have had to choose between
	// breaking the roster's documented consumer and leaving the map viewer-readable.
	permIdentityPostureRead auth.Permission = "governance:idposture:read"
	permIdentityAdmin       auth.Permission = "governance:identity:admin"
	permPolicyRead          auth.Permission = "governance:policy:read"
	permPolicyAdmin         auth.Permission = "governance:policy:admin"
	permApprovalRead        auth.Permission = "governance:approval:read"
	permApprovalWrite       auth.Permission = "governance:approval:write"
	permApprovalAdmin       auth.Permission = "governance:approval:admin"
	// break-glass emergency access. Activation/revoke/review are admin-tier
	// privileged mutations; consume (recording a use under an active grant) is
	// write-tier so the bridge's editor-scoped service token can record uses;
	// reading grants and their use trail is read-tier.
	permBreakGlassRead  auth.Permission = "governance:breakglass:read"
	permBreakGlassUse   auth.Permission = "governance:breakglass:write"
	permBreakGlassAdmin auth.Permission = "governance:breakglass:admin"
	// the Claude Code managed-policy authoring tiers (the RBAC verbs the
	// console mirrors). read gates view/dry-run/validate/versions (and, the
	// signed-artifact pull + the distribution truth view); admin gates publish.
	// write gates the distribution agent's attested CHECK-IN — an editor-tier
	// machine token can report the observed config without holding publish rights.
	permClaudePolicyRead  auth.Permission = "governance:claude-policy:read"
	permClaudePolicyWrite auth.Permission = "governance:claude-policy:write"
	permClaudePolicyAdmin auth.Permission = "governance:claude-policy:admin"
	// NHI lifecycle. read gates the lifecycle list + posture; write gates the
	// owner/sponsor and rotation-policy authoring; admin gates the privileged
	// actuations (rotate, offboard, finalize, restore) and the staleness sweep.
	permNHIRead  auth.Permission = "governance:nhi:read"
	permNHIWrite auth.Permission = "governance:nhi:write"
	permNHIAdmin auth.Permission = "governance:nhi:admin"
	// the estate kill switch. read gates the stop state + evidence-free
	// views; admin gates engage/re-enable/review AND the evidence-pack export
	// (the pack reconstructs privileged incident history — docs/SECURITY-HARDENING.md treats
	// recon-grade reads as privileged and self-audited). Guardian rules are
	// admin-tier auto-containment authoring; reading them is read-tier.
	permKillSwitchRead  auth.Permission = "governance:killswitch:read"
	permKillSwitchAdmin auth.Permission = "governance:killswitch:admin"
	permGuardianRead    auth.Permission = "governance:guardian:read"
	permGuardianAdmin   auth.Permission = "governance:guardian:admin"
	// scoped administration + custom roles/permission-groups + delegation. read
	// gates the catalog and the listings; admin gates authoring roles/groups and
	// creating/revoking scoped grants. This is the COARSE RBAC gate on the endpoints;
	// the per-scope delegation ceiling (scopedadmin.go) confines WHAT a non-superadmin
	// may actually grant beneath it. A scoped-admin (not tenant-admin) reaches these
	// endpoints because the projection emits a tenant-wide rbac:read/admin permit for an
	// admin-capable subject — but its ceiling still bounds it to its own scope+role.
	permRBACRead  auth.Permission = "governance:rbac:read"
	permRBACAdmin auth.Permission = "governance:rbac:admin"
	// per-agent risk tier. read=list/get profiles; write=classify (run the
	// heuristic); admin=set operator tier and review.
	permAgentRiskRead  auth.Permission = "governance:agent-risk:read"
	permAgentRiskWrite auth.Permission = "governance:agent-risk:write"
	permAgentRiskAdmin auth.Permission = "governance:agent-risk:admin"
	// routine governance policies. read=list/get policies and posture;
	// admin=create/update/delete policies.
	permRoutineRead  auth.Permission = "governance:routine:read"
	permRoutineAdmin auth.Permission = "governance:routine:admin"
	// AgentCore Cedar export. Planning reads remote policy metadata and
	// apply mutates the remote engine, so both are admin-tier privileged actions.
	permAgentCoreExportAdmin auth.Permission = "governance:agentcore-export:admin"
)

// RosterBinding pairs an identity GraphProvider (an connector that also
// implements identitysource.GraphProvider) with the tenant whose roster it feeds.
// The composition root constructs these from the deployment's configured identity
// connectors and hands them to the module via UseRosterProviders; the module
// never holds a Store or a connector registry of its own.
type RosterBinding struct {
	// Provider is the identity source to snapshot.
	Provider identitysource.GraphProvider
	// TenantRef is the business tenant the provider's roster belongs to.
	TenantRef string
}

// AgentAutonomySignal is the minimal-data autonomy view of an agent the
// risk-tier heuristic folds in: whether it runs on a schedule and whether
// it acts unattended. Mirrors compliance.AutonomySignal.
type AgentAutonomySignal struct {
	Scheduled  bool
	Autonomous bool
}

// AgentAutonomySource yields an agent's autonomy signal. Without it the
// heuristic uses only observed edges and findings — it never inflates a
// tier on a fabricated signal.
type AgentAutonomySource interface {
	Autonomy(ctx context.Context, tenant model.TenantID, agentRef string) (AgentAutonomySignal, error)
}

// Option configures a Module at construction.
type Option func(*Module)

// WithClock overrides the module clock (tests inject a deterministic clock to
// exercise approval expiry/escalation without sleeping).
func WithClock(c model.Clock) Option {
	return func(m *Module) { m.clock = c }
}

// WithOfflinePolicyStaleness sets the deployment-wide offline-trust bound (ADR-0024
// Q1): past this staleness without the scoped-grant policy being re-established on this
// node, a tenant's POSITIVE grants expire deny-closed while its forbid/deny rules stay
// enforced. Zero (the default) means no bound — a connected deployment behaves exactly as
// before. An edge/DDIL deployment sets it (the ratified default is 72h) so a grant that
// can no longer be refreshed from the center cannot authorize forever.
func WithOfflinePolicyStaleness(d time.Duration) Option {
	return func(m *Module) {
		if d > 0 {
			m.offlineStaleness = d
		}
	}
}

// WithExternalPDP wires an external policy engine (IDN-09: embedded Cedar or
// OPA-over-HTTP, built via NewExternalPDP) behind the same PolicyEvaluator seam. It
// is composed with the native ABAC engine into a deny-only chain, so it can only
// further-restrict an RBAC grant, never widen it. nil leaves only the native engine.
func WithExternalPDP(pdp auth.PolicyEvaluator) Option {
	return func(m *Module) { m.pdp = pdp }
}

// WithAgentAutonomySource wires a richer agent-autonomy signal for the
// risk-tier heuristic. Without it the classifier uses only observed
// access edges and findings.
func WithAgentAutonomySource(s AgentAutonomySource) Option {
	return func(m *Module) {
		if s != nil {
			m.autonomy = s
		}
	}
}

// Module is module VI — identity, permissions and governance. See doc.go for the
// five subsystems and the honest composition-root caveat.
type Module struct {
	log   *slog.Logger
	data  api.ModuleData
	host  sdk.Host
	clock model.Clock
	// offlineStaleness is the deployment-wide offline-trust bound (ADR-0024 Q1), wired
	// into the scoped-grant engine at construction. Zero ⇒ no bound (connected default).
	offlineStaleness time.Duration
	eval             *evaluator
	pdp              auth.PolicyEvaluator // optional external PDP (IDN-09: Cedar/OPA), composed deny-only
	// grants is the per-tenant SCOPED-GRANT engine (grants.go): the C
	// authored Cedar policy, now a positive grant engine. It is the auth.ScopedAuthorizer
	// the main request authorizer consults BESIDE the deny-overlay (a permit GRANTS
	// within the scope tree, a forbid RESTRICTS), AND the restrict-view
	// PolicyEvaluator the hooks PEP composes into its deny-overlay (the SAME policy,
	// evaluated forbid-only). It starts empty (abstains) and is recomposed when an admin
	// publishes a compiling Cedar policy (deny-closed: a policy that fails compilation
	// never swaps in; the prior one stands).
	grants *scopedEngine
	// reloadGrantsFn overrides the live grant-engine swap. Nil ⇒ reloadTenantGrants,
	// the real path. It exists so a test can force the DEFERRED activation outcome
	// (publish/rollback committed, evaluator NOT swapped): that branch is otherwise
	// only reachable through a store fault, so without this seam the honesty of
	// `live_activation: "deferred"` could not be pinned by any test.
	reloadGrantsFn func(context.Context, model.TenantID) error

	mu        sync.Mutex
	cancel    func() // bus unsubscribe (the lifecycle finding-driven trigger)
	providers []RosterBinding
	// NHI lifecycle: the governed HITL gate (deny-closed default) and the
	// per-(tenant,source) write-capable actuators the composition root wires.
	lifecycleGate LifecycleGate
	actuators     map[model.TenantID]map[string]identitysource.LifecycleActuator
	// break-glass mandatory session recording (deny-closed default).
	recordingGate RecordingGate
	// per-agent autonomy signal for the risk-tier heuristic. Without it
	// the classifier uses only observed edges and findings — it never invents
	// an autonomy signal.
	autonomy AgentAutonomySource
	// optional AgentCore Cedar exporter. Nil/empty means the export route
	// is inert and returns an honest 501; cmd wires it only when the operator
	// provisions per-tenant write credentials and governance gates.
	agentCoreExports   map[model.TenantID]agentCoreExportTarget
	agentCoreProviders []AgentCoreExportProvider
}

// Compile-time proof the module satisfies the SDK lifecycle, the engine-side
// schema seam, the API route/permission seam and the data-consumer seam.
var (
	_ sdk.Module       = (*Module)(nil)
	_ api.Module       = (*Module)(nil)
	_ api.DataConsumer = (*Module)(nil)
)

// New returns a governance module with a system clock and a constructed (but
// data-less) ABAC evaluator; the engine wires the data handle via UseData before
// Start, which the evaluator shares.
func New(opts ...Option) *Module {
	m := &Module{clock: model.SystemClock{}}
	m.eval = &evaluator{}
	m.grants = &scopedEngine{}
	for _, o := range opts {
		o(m)
	}
	// Wire the scoped-grant engine's time source to the (possibly test-injected) module
	// clock so its offline-staleness window is deterministic under WithClock, and hand it
	// the offline-trust bound. Both are read on the hot path AFTER boot completes, so
	// setting them here (before Init/serve) needs no lock. Unix()-based Cedar `context.time`
	// is timezone-independent, so a real SystemClock keeps identical behavior.
	clock := m.clock
	m.grants.now = func() time.Time { return clock.Now().Time() }
	m.grants.maxStaleness = m.offlineStaleness
	return m
}

// Descriptor returns the module's self-description.
func (m *Module) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeModule,
		Title:       "Identity, permissions & governance",
		Description: "Governs who/what may do what: reconciles the identity roster, binds agents to their NHI identity (the access-map attribution bridge), authors RBAC/ABAC policy enforced through the engine's further-restrict-only seam, and runs the human-in-the-loop approval chain with an immutable action→human decision trail.",
	}
}

// UseData receives the least-privilege, tenant-parameterized data handle from the
// engine boot (the api.DataConsumer seam), before Start. The ABAC evaluator shares
// it so it can serve the authorization hot path off its per-tenant cache, loading
// a tenant's policies through this handle on a cache miss.
func (m *Module) UseData(d api.ModuleData) {
	m.data = d
	m.eval.data = d
	// The scoped-grant engine resolves each request's lineage through this same
	// least-privilege, tenant-scoped handle (read-only View on the authorization path).
	m.grants.resolver = &scopeResolver{data: d}
}

// UseRosterProviders wires the identity GraphProviders the composition root built
// from the deployment's identity connectors. It is an ADDITIVE module-level
// injection method (parallel to UseData), not a reuse of an existing seam: there
// is no engine seam that hands a module live connectors today, so the composition
// root — which lives in /core and may hold both — calls this. Tests pass a fake
// GraphProvider here. It is safe to call before Start.
func (m *Module) UseRosterProviders(bindings []RosterBinding) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.providers = append([]RosterBinding(nil), bindings...)
}

// Evaluator returns the module's DENY-OVERLAY policy evaluator: the native ABAC engine,
// the optional external PDP (IDN-09 Cedar/OPA), and the restrict-view of the authored
// scoped-grant policy (its FORBID rules), composed into a deny-only chain (each can only
// further-restrict), with restrictions logged to the audit trail. It is the overlay the
// Hooks PEP consults for tool-call gating. The MAIN request authorizer instead uses
// RequestEvaluator() + ScopedGrants() (so the authored policy's grants AND forbids are
// evaluated ONCE through the scoped seam, not twice). Calling it before UseData is fine
// (a data-less chain imposes no restriction).
func (m *Module) Evaluator() auth.PolicyEvaluator {
	return composeEvaluators(m.auditDecision, m.eval, m.pdp, m.grants)
}

// RequestEvaluator returns the deny-overlay for the MAIN request authorizer: the native
// ABAC engine + the optional external PDP, but NOT the authored scoped-grant policy —
// that policy's grants AND forbids are handled by ScopedGrants() (the three-valued scoped
// seam), so including its restrict-view here too would double-evaluate it (and re-resolve
// the scope tree) on every request. Pairs with ScopedGrants() in
// auth.NewAuthorizer(gov.RequestEvaluator(), auth.WithScopedGrants(gov.ScopedGrants())).
func (m *Module) RequestEvaluator() auth.PolicyEvaluator {
	return composeEvaluators(m.auditDecision, m.eval, m.pdp)
}

// ScopedGrants returns the positive scoped-grant engine the composition root wires
// into the Authorizer via auth.WithScopedGrants. It resolves the scope tree and
// answers GRANT/FORBID/ABSTAIN per request; a tenant with no authored grant policy
// abstains before any store read (the hot path pays nothing until an operator opts in).
func (m *Module) ScopedGrants() auth.ScopedAuthorizer {
	return m.grants
}

// auditDecision records a policy restriction on the audit trail (docs/SECURITY-HARDENING.md). It is
// called only when the chain RESTRICTS (an allow imposes nothing to audit), so the
// hot path logs only the security-relevant denials. It is nil-safe before Init.
func (m *Module) auditDecision(req auth.Request, dec auth.Decision) {
	if m.log == nil {
		return
	}
	m.log.Info("abac policy restriction",
		"tenant", string(req.Tenant),
		"principal_kind", string(req.Principal.Kind),
		"cred_id", string(req.Principal.CredID),
		"permission", string(req.Permission),
		"resource_kind", req.Resource.Kind,
		"resource_id", req.Resource.ID,
		"sensitivity", req.Resource.Sensitivity,
		"reason", dec.Reason,
	)
}

// Init keeps the host for publishing governance findings (shared-identity,
// approval escalation/expiry, NHI lifecycle) and subscribes to finding.reported so
// the NHI lifecycle reacts to external identity-revocation signals (CAEP via
// the ssf connector) and the guardian loop evaluates its containment rules —
// the event-driven halves of lifecycle and guardian, beside the explicit sweeps.
// It must not block; both handlers are fast and idempotent.
func (m *Module) Init(_ context.Context, host sdk.Host) error {
	m.log = host.Logger()
	m.host = host
	m.grants.log = m.log // scoped grants/forbids and per-rule eval errors hit the audit log
	cancel, err := host.Subscribe([]event.Type{event.TypeFindingReported}, func(ctx context.Context, e event.Event) error {
		m.onLifecycleSignal(ctx, e)
		m.onGuardianFinding(ctx, e)
		return nil
	})
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.cancel = cancel
	m.mu.Unlock()
	return nil
}

// Start has no background work (the cross-tenant approval sweep is explicit and
// tenant-scoped — a module cannot enumerate tenants). It only warns once if the
// data handle was never wired, so a silently-broken deployment is visible.
func (m *Module) Start(context.Context) error {
	if m.data == nil && m.log != nil {
		m.log.Warn("governance: started without a data handle; roster, bindings, policy and approvals will not persist")
	}
	return nil
}

// Stop is idempotent (no background work, no live subscription in v1).
func (m *Module) Stop(context.Context) error {
	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

// APINamespace returns the module's namespace; it roots routes at
// /v1/m/governance/.
func (m *Module) APINamespace() string { return Namespace }

// Permissions declares the permissions the module's routes require so the
// built-in roles grant them by verb tier.
func (m *Module) Permissions() []auth.Permission {
	return []auth.Permission{
		permIdentityRead, permIdentityAdmin, permIdentityPostureRead,
		permPolicyRead, permPolicyAdmin,
		permApprovalRead, permApprovalWrite, permApprovalAdmin,
		permBreakGlassRead, permBreakGlassUse, permBreakGlassAdmin,
		permNHIRead, permNHIWrite, permNHIAdmin,
		permKillSwitchRead, permKillSwitchAdmin,
		permGuardianRead, permGuardianAdmin,
		permRBACRead, permRBACAdmin,
		permRoutineRead, permRoutineAdmin,
		permAgentCoreExportAdmin,
		// the five agent-risk routes (:521-525) were mounted requiring these three
		// permissions while Permissions() never declared them. Undeclared meant no role
		// could be granted them explicitly — they rode the module verb tier alone — and it
		// kept them out of the scope-grantable catalog, so the agent-risk surface would
		// have stayed undelegable even after the catalog widened. mountModules now rejects
		// an undeclared route permission at boot so this cannot recur silently.
		permAgentRiskRead, permAgentRiskWrite, permAgentRiskAdmin,
	}
}

// APIRoutes mounts the module's routes. The engine wraps each with authentication,
// tenant resolution and the declared permission check before the handler runs, and
// pins the data handle to the resolved tenant.
func (m *Module) APIRoutes(reg api.RouteRegistrar) {
	// Subsystem A — identity roster (reconciled from GraphProviders).
	reg.Handle("GET", "/identities", permIdentityRead, m.handleListIdentities)
	reg.Handle("GET", "/groups", permIdentityRead, m.handleListGroups)
	reg.Handle("GET", "/groups/{ref}/members", permIdentityRead, m.handleGroupMembers)
	reg.Handle("POST", "/roster/sync", permIdentityAdmin, m.handleRosterSync)

	// Subsystem B — agent↔identity binding (the NHI attribution bridge).
	// The binding topology carries `shared`/`agent_count` — an identity used by several
	// agents is lost attribution, and the product emits a FINDING for exactly that
	// (roster_identity_test.go:218). That is a posture attribute, so this route takes the
	// privileged permission while the rest of the roster stays viewer-readable. The field
	// is not gated on its own because handleListBindings returns EVERY binding and derives
	// the counts from that same set, so a caller holding the list can recompute it.
	reg.Handle("GET", "/bindings", permIdentityPostureRead, m.handleListBindings)
	reg.Handle("POST", "/agents/{agentID}/identity", permIdentityAdmin, m.handleBindAgent)
	reg.Handle("DELETE", "/agents/{agentID}/identity", permIdentityAdmin, m.handleUnbindAgent)

	// Subsystem C — policy authoring (the ABAC engine consumes these).
	reg.Handle("GET", "/policies", permPolicyRead, m.handleListPolicies)
	reg.Handle("POST", "/policies", permPolicyAdmin, m.handleCreatePolicy)
	reg.Handle("GET", "/policies/{id}", permPolicyRead, m.handleGetPolicy)
	reg.Handle("PUT", "/policies/{id}", permPolicyAdmin, m.handleUpdatePolicy)
	reg.Handle("DELETE", "/policies/{id}", permPolicyAdmin, m.handleDeletePolicy)

	// IDN-12 — emerging agent-identity standards, design-toward registry (read-only,
	// tracked-not-implemented). Reference data; identity-read tier.
	reg.Handle("GET", "/emerging-identity-standards", permIdentityRead, m.handleEmergingStandards)

	// C — Cedar/OPA policy-as-code authoring over the PDP. validate/explain/
	// dry-run are no-effect reads; publish is a privileged, deny-closed activation that
	// recomposes the live evaluator. Mounts under /v1/m/governance/pdp/.
	reg.Handle("POST", "/pdp/validate", permPolicyRead, m.handlePdpValidate)
	reg.Handle("POST", "/pdp/explain", permPolicyRead, m.handlePdpExplain)
	reg.Handle("POST", "/pdp/dry-run", permPolicyRead, m.handlePdpDryRun)
	reg.Handle("GET", "/pdp/versions", permPolicyRead, m.handlePdpVersions)
	// One revision WITH its content (?engine=), and what the store currently selects.
	// Read tier: both only reflect what /pdp/versions already lists.
	reg.Handle("GET", "/pdp/versions/{revision}", permPolicyRead, m.handlePdpGetVersion)
	reg.Handle("GET", "/pdp/active", permPolicyRead, m.handlePdpActive)
	reg.Handle("GET", "/pdp/tests", permPolicyRead, m.handlePdpTests)
	reg.Handle("POST", "/pdp/publish", permPolicyAdmin, m.handlePdpPublish)
	reg.Handle("POST", "/pdp/rollback", permPolicyAdmin, m.handlePdpRollback)

	// Subsystem D — human-in-the-loop approvals + the immutable decision trail.
	reg.Handle("GET", "/approvals", permApprovalRead, m.handleListApprovals)
	reg.Handle("POST", "/approvals", permApprovalWrite, m.handleCreateApproval)
	reg.Handle("GET", "/approvals/{id}", permApprovalRead, m.handleGetApproval)
	reg.Handle("GET", "/approvals/{id}/decisions", permApprovalRead, m.handleListDecisions)
	reg.Handle("POST", "/approvals/{id}/decisions", permApprovalAdmin, m.handleDecide)
	reg.Handle("POST", "/approvals/{id}/cancel", permApprovalWrite, m.handleCancel)
	// (F-02): single-use SPEND of an approved request. Write-tier (the
	// bridge's editor service token) — like break-glass consume, it records a use,
	// it never DECIDES. Deny-closed: a non-approved request cannot be consumed.
	reg.Handle("POST", "/approvals/{id}/consume", permApprovalWrite, m.handleConsumeApproval)
	reg.Handle("POST", "/approvals/sweep", permApprovalAdmin, m.handleSweep)

	// Subsystem E — break-glass emergency access: audited, notified,
	// time-boxed, forced post-review. Never a silent bypass (breakglass.go).
	reg.Handle("GET", "/breakglass", permBreakGlassRead, m.handleListBreakGlass)
	reg.Handle("POST", "/breakglass", permBreakGlassAdmin, m.handleActivateBreakGlass)
	reg.Handle("POST", "/breakglass/consume", permBreakGlassUse, m.handleConsumeBreakGlass)
	reg.Handle("GET", "/breakglass/{id}", permBreakGlassRead, m.handleGetBreakGlass)
	reg.Handle("GET", "/breakglass/{id}/uses", permBreakGlassRead, m.handleListBreakGlassUses)
	reg.Handle("POST", "/breakglass/{id}/revoke", permBreakGlassAdmin, m.handleRevokeBreakGlass)
	reg.Handle("POST", "/breakglass/{id}/review", permBreakGlassAdmin, m.handleReviewBreakGlass)

	// Subsystem F — NHI lifecycle: rotation, expiry/staleness enforcement
	// and governed offboarding on top of the roster (nhilifecycle.go).
	reg.Handle("GET", "/nhi", permNHIRead, m.handleListNHI)
	reg.Handle("GET", "/nhi/posture", permNHIRead, m.handleNHIPosture)
	reg.Handle("GET", "/nhi/{ref}", permNHIRead, m.handleGetNHI)
	reg.Handle("GET", "/nhi/{ref}/events", permNHIRead, m.handleListNHIEvents)
	reg.Handle("PUT", "/nhi/{ref}/ownership", permNHIWrite, m.handleSetNHIOwnership)
	reg.Handle("PUT", "/nhi/{ref}/policy", permNHIWrite, m.handleSetNHIPolicy)
	reg.Handle("POST", "/nhi/{ref}/rotate", permNHIAdmin, m.handleRotateNHI)
	reg.Handle("POST", "/nhi/{ref}/offboard", permNHIAdmin, m.handleOffboardNHI)
	reg.Handle("POST", "/nhi/{ref}/offboard/finalize", permNHIAdmin, m.handleFinalizeNHI)
	reg.Handle("POST", "/nhi/{ref}/restore", permNHIAdmin, m.handleRestoreNHI)
	reg.Handle("POST", "/nhi/sweep", permNHIAdmin, m.handleNHISweep)

	// Agent identity: first-class specialization of NHI lifecycle with
	// mandatory human sponsor and JML (agentidentity.go). POST /agents is the
	// joiner; the mover uses the existing PUT /nhi/{ref}/ownership; the leaver
	// uses the existing POST /nhi/{ref}/offboard.
	reg.Handle("POST", "/agents", permNHIAdmin, m.handleRegisterAgent)

	// Subsystem G — the estate kill switch: one-click graduated emergency
	// stop, dual-control re-enable, forced post-review, incident evidence pack
	// (killswitch.go, killswitch_evidence.go). Engage is admin-tier and CHEAP by
	// design; re-enable/review carry the controls; the evidence export is a
	// privileged, self-audited read.
	reg.Handle("GET", "/killswitch", permKillSwitchRead, m.handleListKillSwitch)
	reg.Handle("GET", "/killswitch/state", permKillSwitchRead, m.handleKillSwitchState)
	reg.Handle("POST", "/killswitch", permKillSwitchAdmin, m.handleEngageKillSwitch)
	reg.Handle("GET", "/killswitch/{id}", permKillSwitchRead, m.handleGetKillSwitch)
	reg.Handle("POST", "/killswitch/{id}/reenable", permKillSwitchAdmin, m.handleReenableKillSwitch)
	reg.Handle("POST", "/killswitch/{id}/review", permKillSwitchAdmin, m.handleReviewKillSwitch)
	reg.Handle("GET", "/killswitch/{id}/evidence", permKillSwitchAdmin, m.handleKillSwitchEvidence)

	// Subsystem H — guardian-agent rules: operator-authored semi-autonomous
	// containment over the finding rail, deny-closed, per-rule HITL (guardian.go).
	reg.Handle("GET", "/guardian/rules", permGuardianRead, m.handleListGuardianRules)
	reg.Handle("POST", "/guardian/rules", permGuardianAdmin, m.handleCreateGuardianRule)
	reg.Handle("PUT", "/guardian/rules/{id}", permGuardianAdmin, m.handleUpdateGuardianRule)
	reg.Handle("DELETE", "/guardian/rules/{id}", permGuardianAdmin, m.handleDeleteGuardianRule)
	reg.Handle("GET", "/guardian/actions", permGuardianRead, m.handleListGuardianActions)

	// Subsystem I — scoped administration: custom roles, permission-groups and
	// scoped-admin grants, projected to the engine with a per-scope delegation
	// ceiling (scopedadmin.go / scopedadmin_handlers.go). read=catalog+listings;
	// admin=authoring (definitions need tenant-admin; grants need delegation authority).
	reg.Handle("GET", "/rbac/catalog", permRBACRead, m.handleRBACCatalog)
	reg.Handle("GET", "/rbac/delegation-authority", permRBACRead, m.handleDelegationAuthority)
	reg.Handle("GET", "/rbac/roles", permRBACRead, m.handleListCustomRoles)
	reg.Handle("POST", "/rbac/roles", permRBACAdmin, m.handleCreateCustomRole)
	reg.Handle("GET", "/rbac/roles/{name}", permRBACRead, m.handleGetCustomRole)
	reg.Handle("PUT", "/rbac/roles/{name}", permRBACAdmin, m.handleUpdateCustomRole)
	reg.Handle("DELETE", "/rbac/roles/{name}", permRBACAdmin, m.handleDeleteCustomRole)
	reg.Handle("GET", "/rbac/permission-groups", permRBACRead, m.handleListPermGroups)
	reg.Handle("POST", "/rbac/permission-groups", permRBACAdmin, m.handleCreatePermGroup)
	reg.Handle("GET", "/rbac/permission-groups/{name}", permRBACRead, m.handleGetPermGroup)
	reg.Handle("PUT", "/rbac/permission-groups/{name}", permRBACAdmin, m.handleUpdatePermGroup)
	reg.Handle("DELETE", "/rbac/permission-groups/{name}", permRBACAdmin, m.handleDeletePermGroup)
	reg.Handle("GET", "/rbac/grants", permRBACRead, m.handleListScopedGrants)
	reg.Handle("POST", "/rbac/grants", permRBACAdmin, m.handleCreateScopedGrant)
	reg.Handle("GET", "/rbac/grants/{id}", permRBACRead, m.handleGetScopedGrant)
	reg.Handle("DELETE", "/rbac/grants/{id}", permRBACAdmin, m.handleRevokeScopedGrant)

	// Subsystem J — per-agent risk/autonomy tier: heuristic classification
	// from observed signals, operator override, human review. The effective tier
	// scales controls across the governance layer (guardian floors, policy filters).
	reg.Handle("GET", "/agent-risk-profiles", permAgentRiskRead, m.handleListAgentRiskProfiles)
	reg.Handle("POST", "/agent-risk-profiles/classify", permAgentRiskWrite, m.handleClassifyAgentRisk)
	reg.Handle("GET", "/agent-risk-profiles/{id}", permAgentRiskRead, m.handleGetAgentRiskProfile)
	reg.Handle("PUT", "/agent-risk-profiles/{id}/tier", permAgentRiskAdmin, m.handleSetAgentRiskTier)
	reg.Handle("POST", "/agent-risk-profiles/{id}/review", permAgentRiskAdmin, m.handleReviewAgentRisk)

	// Subsystem K — routine governance policies: operator-authored cadence,
	// concurrency and approval controls for Claude Code Routines (routines.go).
	reg.Handle("GET", "/routine-policies", permRoutineRead, m.handleListRoutinePolicies)
	reg.Handle("GET", "/routine-policies/posture", permRoutineRead, m.handleRoutinePosture)
	reg.Handle("POST", "/routine-policies", permRoutineAdmin, m.handleCreateRoutinePolicy)
	reg.Handle("GET", "/routine-policies/{id}", permRoutineRead, m.handleGetRoutinePolicy)
	reg.Handle("PUT", "/routine-policies/{id}", permRoutineAdmin, m.handleUpdateRoutinePolicy)
	reg.Handle("DELETE", "/routine-policies/{id}", permRoutineAdmin, m.handleDeleteRoutinePolicy)

	// Subsystem L — governed AgentCore Cedar export. Admin-tier because it
	// projects local governance into a remote policy engine and apply is a
	// write-side governed actuation. Missing cmd binding returns 501, never a
	// silently empty or ungated exporter.
	reg.Handle("POST", "/agentcore-export/plan", permAgentCoreExportAdmin, m.handleAgentCoreExportPlan)
	reg.Handle("POST", "/agentcore-export/apply", permAgentCoreExportAdmin, m.handleAgentCoreExportApply)
}

// tenantOf resolves an event's string tenant reference to a usable business
// tenant, or false for a placeholder/system reference (the module never writes to
// the system partition).
func tenantOf(ref string) (model.TenantID, bool) {
	t, err := model.ParseTenantID(ref)
	if err != nil || t.IsZero() || t.IsSystem() {
		return "", false
	}
	return t, true
}

// debugf logs at debug level if a logger is set.
func (m *Module) debugf(msg string, args ...any) {
	if m.log != nil {
		m.log.Debug(msg, args...)
	}
}
