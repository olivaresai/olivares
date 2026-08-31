// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
)

// Store is the root handle. It owns the connection pools and hands out
// tenant-scoped units of work. It exposes no method that returns a raw
// connection and none that takes a tenant id alongside a payload: a caller
// binds a tenant once via View or Mutate and works through the resulting Scope.
type Store interface {
	// View runs fn in one consistent read-only transaction snapshot pinned to
	// tenant. Every read in the callback observes the same committed database
	// state; a backend must not assign a newer snapshot to later statements. It
	// fails with ErrNoTenant if tenant is zero.
	View(ctx context.Context, tenant model.TenantID, fn func(Scope) error) error
	// Mutate runs fn in a read-write transaction pinned to tenant; the whole
	// closure commits or rolls back atomically. It fails with ErrNoTenant if
	// tenant is zero.
	Mutate(ctx context.Context, tenant model.TenantID, fn func(Scope) error) error
	// Custody runs fn in a read-write transaction pinned to tenant over that
	// tenant's evidence ledger and its own org row — and nothing else (see
	// CustodyScope, which states the width exactly).
	//
	// It exists because withdrawing SERVICE and abandoning CUSTODY are two
	// different decisions, and only the first is commercial. A tenant whose service
	// is withdrawn still has an audit chain that must stay anchored and provable:
	// during a grace period that is exactly when it may be needed. Suspension
	// (core/suspension) therefore does NOT gate this path: checkpointing, the DR
	// chain tip, restore verification, the key-transition marker and `audit
	// verify`/`audit recover` all keep working for a withdrawn tenant. Residency
	// (core/residency) DOES gate it: custody is never a reason to move a tenant's
	// data out of the region it is pinned to.
	//
	// NOT covered, and this is a known gap rather than a decision: the WORM ledger
	// archive. Its resume bookkeeping lives in Org.Settings and must commit in the
	// SAME transaction as its anchor event, and this scope deliberately withholds
	// SetOrgSettings. The fix is to move that bookkeeping to its own table.
	//
	// It is deliberately a distinct METHOD with a distinct SCOPE TYPE rather than a
	// flag on View/Mutate. A context flag threaded past a deny-closed gate would be
	// an unaudited escape hatch stronger than the door beside it; this is visible in
	// the type, greppable, and unreachable from a module, which only ever receives a
	// Scope and never the Store.
	//
	// COST, stated rather than hidden: it is read-write because its two writing
	// callers (checkpoint append, key-transition marker) need to be, so the
	// read-only callers (DR tip, RestoreVerify, `audit verify`) take the single
	// SQLite writer. They are administrative or low-cadence, which is why this is
	// one method and not a CustodyView/CustodyMutate pair.
	Custody(ctx context.Context, tenant model.TenantID, fn func(CustodyScope) error) error
	// Export runs fn over the tenant's OWN data for the purpose of getting it OUT:
	// its evidence ledger and its module records. It is the deliberate, named
	// carve-out in the service door.
	//
	// Withdrawing service must deny mutations and INTERACTIVE SERVICE, and it must
	// NOT deny a customer the copy of their own data. Those are not the same freeze:
	// allowing every read leaves a non-paying tenant OPERATING (a routing policy
	// resolves and a model executes under View), which is service; denying every
	// read withdraws GET /v1/m/knowledge/memory/export and /v1/audit/export, which
	// is a subject-access and anti-lock-in copy, and custody does not lapse for
	// non-payment. The line runs between them, and this method is where it is drawn.
	//
	// EXPLICIT: it is a distinct method with a distinct scope type, so a call site
	// declares what it is doing in the type system rather than in a comment. That
	// answers, and retires, core/suspension's old objection that "the store sees a
	// View, not the HTTP route" — true of Scope, and not of a separate door.
	//
	// AUDITED: an export taken while service is withdrawn appends an event to the
	// tenant's own chain (core/suspension), in the SAME transaction as the export,
	// so a copy taken during a grace period cannot be taken silently and both sides
	// can prove afterwards what left and when.
	//
	// It is read-write for that reason, and therefore leader-gated like any write:
	// on an HA standby an export during suspension fails with ErrNotLeader. Stated
	// rather than discovered — on a single-node store the elector is always active,
	// so this is an HA-only consequence, and a standby refusing to append to a
	// signed chain is the behavior the write-gate exists for.
	Export(ctx context.Context, tenant model.TenantID, fn func(ExportScope) error) error
	// System runs fn in a privileged, cross-tenant transaction (provisioning,
	// tenant deletion, global verification). Only the engine's own boot code
	// holds a Store, so a module — which only ever receives a Scope — cannot
	// reach this.
	System(ctx context.Context, fn func(SystemScope) error) error
	// AuthView runs fn against the authentication/authorization partition in a
	// read-only transaction pinned to the reserved system tenant. It is the hot
	// path for credential validation (it does not take the single SQLite writer).
	// Like System, only the engine's boot code can reach it.
	AuthView(ctx context.Context, fn func(AuthScope) error) error
	// AuthMutate runs fn against the auth partition in a read-write transaction
	// pinned to the system tenant (login, user/token/membership changes), so the
	// change and its audit event commit atomically.
	AuthMutate(ctx context.Context, fn func(AuthScope) error) error
	// Engine reports the active backend.
	Engine() Engine
	// Ping checks connectivity.
	Ping(ctx context.Context) error
	// Leader returns this node's leadership elector. It is ALWAYS non-nil:
	// the SQLite/single-node store returns an always-leader implementation, so a
	// caller never needs to special-case the embedded mode. The engine's boot code
	// calls Leader().Run before serving and Leader().Resign at shutdown; /readyz
	// and public background loops consult the established-leadership predicate.
	// The store's write gate has an internal bootstrap exception for OnPromote.
	Leader() LeaderElector
	// Close releases the pools.
	Close() error
}

// CustodyScope is a tenant-pinned unit of CUSTODIAL work: the evidence ledger plus
// the tenant's own org row, and nothing else. It is what Store.Custody hands out.
//
// State the width exactly, because "the ledger and nothing else" is what this
// comment said first and it was not true: Org() returns the whole model.Org —
// name, slug, status, data region and Settings. That is READ-ONLY here (there is
// no SetOrgSettings on this type, deliberately: that method can rewrite
// security-relevant configuration such as the SCIM receiver's verification key),
// and the row is why the type exists in this shape — see Org below. But a contract
// that undersells its own surface is the same defect as one that oversells a
// control, so it is written down rather than rounded off.
//
// The narrowness is the whole point, and it is enforced by the TYPE rather than by
// a rule someone has to remember. core/suspension's package doc argued that a
// store-level guard could not carve "export" reads out of "product" reads because
// "the store sees a View, not the HTTP route" — which is true of Scope. It is not
// true here: a CustodyScope has no Agents(), no Policies(), no Models(), no Ext(),
// so it cannot resolve a routing policy or execute a model. The objection that
// reading IS the service, correct for View, does not reach this type, because
// there is no service to be had from it.
//
// Anything that needs more than the ledger is not custody and must go through
// View/Mutate, where the service door applies.
type CustodyScope interface {
	// Tenant returns the bound tenant.
	Tenant() model.TenantID
	// Org returns the bound tenant's own organization row.
	//
	// It is here for one reason, and it is not caller convenience: a decorator that
	// must gate custody (residency.Guard checks the region pin) has to read the row
	// INSIDE the same transaction as the work. It cannot open a System transaction
	// to do it — the default SQLite store is single-connection, so a nested
	// transaction deadlocks, which is a measured failure mode in this codebase and
	// not a theoretical one (see the split enumeration in cmd/olivares/boot.go).
	// Widening this scope by one row was the cheaper of the two, against checking
	// the pin outside the transaction and living with the window between. The row is
	// not nothing — it carries Settings — so it is READ-only here and the width is
	// stated on the interface above rather than waved through as "just the ledger".
	Org(ctx context.Context) (model.Org, error)
	// Audit returns the bound tenant's append-only evidence ledger. It is the same
	// AuditLog a Scope would hand out, so the optional capabilities callers already
	// type-assert off it (RecordedHeadReader, AuditAppendLocker) keep working.
	Audit() AuditLog
}

// ExportScope is a tenant-pinned unit of PORTABILITY work: the surface a customer
// needs to take their own data with them, and nothing beyond it.
//
// It is deliberately NOT a read-only Scope. A read-only Scope would still resolve
// routing policies, list agents and read models — i.e. it would still be the
// product, which is what withdrawing service has to stop. What an export needs is
// narrower and is written down here: the evidence ledger (/v1/audit/export) and
// the module records (/v1/m/knowledge/memory/export).
type ExportScope interface {
	// Tenant returns the bound tenant.
	Tenant() model.TenantID
	// Org returns the bound tenant's own organization row.
	Org(ctx context.Context) (model.Org, error)
	// Audit returns the bound tenant's append-only evidence ledger.
	Audit() AuditLog
	// Ext returns a registered module repository by kind, or ErrUnknownEntity. It is
	// tenant-pinned exactly like the typed repositories.
	Ext(kind model.Kind) (GenericRepo, error)
}

// Scope is a tenant-pinned unit of work. Every repository reachable from it is
// already bound to Tenant(); there is no way to ask it for another tenant's
// data, and it is valid only inside the fn it was passed to.
type Scope interface {
	// Tenant returns the bound tenant.
	Tenant() model.TenantID

	// Org returns the bound tenant's own organization row.
	Org(ctx context.Context) (model.Org, error)
	// SetOrgSettings replaces the bound tenant's own free-form Settings (no
	// secrets — docs/SECURITY-HARDENING.md) and returns the updated org row. Because an org's id
	// equals its tenant id, it can only ever write the caller's own tenant, so it
	// is the safe write path for tenant-level configuration such as the SCIM SET
	// receiver's publisher verification key (a PUBLIC key, not a secret).
	SetOrgSettings(ctx context.Context, settings map[string]any) (model.Org, error)

	// Agents returns the agent repository.
	Agents() Repository[model.Agent]
	// Sessions returns the session repository.
	Sessions() Repository[model.Session]
	// Providers returns the provider repository.
	Providers() Repository[model.Provider]
	// Models returns the model repository.
	Models() Repository[model.Model]
	// MCPServers returns the MCP-server repository.
	MCPServers() Repository[model.MCPServer]
	// Skills returns the skill repository.
	Skills() Repository[model.Skill]
	// Tools returns the tool repository.
	Tools() Repository[model.Tool]
	// Resources returns the resource repository, including the tree operations
	// (folders/hierarchy, FASE X /) over the flat CRUD.
	Resources() ResourceRepo
	// Identities returns the identity repository without hard Delete. Definitive
	// retirement belongs to core.engine.RetireDirectoryPrincipal so absence can
	// never be mistaken for irreversible evidence.
	Identities() MutableRepository[model.Identity]
	// Policies returns the policy repository.
	Policies() Repository[model.Policy]
	// Costs returns the cost-record repository.
	Costs() Repository[model.CostRecord]
	// Evals returns the eval-result repository.
	Evals() Repository[model.EvalResult]
	// Findings returns the finding repository.
	Findings() Repository[model.Finding]
	// Health returns the health-status repository.
	Health() Repository[model.HealthStatus]
	// Deployments returns the deployment repository.
	Deployments() Repository[model.Deployment]

	// Workspaces returns the workspace repository (FASE X /): the first-class
	// containers an enterprise scopes agents, sessions and resources to. Every
	// tenant has one default workspace (DefaultWorkspace); a workspace is SOFT
	// isolation, not a tenancy boundary.
	Workspaces() Repository[model.Workspace]
	// AgentGroups returns the agent-group repository (FASE X /): named
	// collections of agents the access engine targets with one grant.
	AgentGroups() Repository[model.AgentGroup]
	// AgentGroupMembers returns the (group → member agent) repository, enumerated
	// by group_id (a group's roster) or agent_id (an agent's groups).
	AgentGroupMembers() Repository[model.AgentGroupMember]
	// DefaultWorkspace returns the bound tenant's default workspace — the one an
	// entity with an unset WorkspaceID belongs to (the back-compat resolution that
	// keeps a pre-FASE-X binary working). It is materialized when a tenant is
	// provisioned and back-filled for pre tenants; it returns ErrNotFound
	// only on a tenant whose default workspace has not yet been seeded (call
	// SystemScope.EnsureDefaultWorkspaces, which boot does).
	DefaultWorkspace(ctx context.Context) (model.Workspace, error)

	// AccessEdges returns the access-edge repository and graph queries.
	AccessEdges() AccessEdgeRepo
	// Audit returns the append-only evidence ledger.
	Audit() AuditLog
	// EvidenceOperations returns the durable evidence operation journal (q1): the tenant-scoped single-use claim + settlement record of governed
	// EXTERNAL effects, whose claim/settle primitives append their ledger events
	// through the SAME transaction as their row changes (see EvidenceOperationRepo).
	EvidenceOperations() EvidenceOperationRepo

	// Ext returns a registered module repository by kind, or ErrUnknownEntity.
	// It is tenant-pinned exactly like the typed repositories.
	Ext(kind model.Kind) (GenericRepo, error)
}

// SystemScope is the privileged, cross-tenant unit of work used only by the
// engine for provisioning, tenant deletion and global verification. Every
// operation through it is itself recorded to the reserved system audit chain.
type SystemScope interface {
	// EnsureSystemTenant idempotently provisions the reserved system tenant's own
	// org row (id == SystemTenantID) and seeds its audit chain genesis. It is
	// called once at boot, before serving, so the system-tenant chain that holds
	// auth and cross-tenant events is well-formed from sequence 1. Calling it
	// again is a no-op that returns the existing row.
	EnsureSystemTenant(ctx context.Context) (model.Org, error)
	// CreateOrg provisions a new tenant: it allocates the tenant id, creates the
	// org row (whose tenant_id equals its id) and seeds the org's audit chain. The
	// org's DataRegion residency pin, when set, is persisted as provided.
	// It also seeds the tenant's DEFAULT workspace (FASE X /) in the same
	// transaction, so a new tenant always has the workspace that an unset
	// WorkspaceID resolves to.
	CreateOrg(ctx context.Context, org model.Org) (model.Org, error)
	// EnsureDefaultWorkspaces back-fills the default workspace (FASE X /) for
	// every existing tenant that lacks one — the boot path for tenants provisioned
	// before (new tenants get theirs in CreateOrg). It is idempotent: a
	// tenant that already has its default workspace is left untouched, so it is
	// safe to call on every boot. It iterates ListOrgs, so on a multi-tenant
	// Postgres deployment it back-fills the tenants the configured pool can see
	// (full coverage needs the BYPASSRLS admin pool, like every cross-tenant
	// System read); SQLite and per-tenant CreateOrg are unaffected.
	EnsureDefaultWorkspaces(ctx context.Context) error
	// SetOrgRegion sets (or clears, with region == "") a tenant's data-residency
	// pin (orgs.data_region —). It is a cross-tenant System op: the pin is a
	// governance fact set out of band of the tenant's own scope, version-checked so
	// it cannot lose a concurrent org update, and recorded to the tenant's audit
	// chain. The caller validates region against the residency registry first.
	SetOrgRegion(ctx context.Context, tenant model.TenantID, region string) (model.Org, error)
	// SetOrgStatus withdraws or restores a tenant's SERVICE (orgs.status —)
	// without touching a single row of its data. It is the operation that makes the
	// cloud grace period possible: before it, the only way to stop serving a tenant
	// was DropTenant, which destroys exactly the data the grace period exists to
	// protect. Retiring ACCESS and destroying DATA are two different decisions.
	//
	// Reversibility is the point, so it is deliberately NARROW: it writes ONE
	// column. It revokes no credential, drops no session and deletes nothing — so
	// restoring service is lossless and the tenant resumes with the estate it had.
	// (This is the deliberate difference from the superadmin account lifecycle in
	// core/auth, which cuts credentials on disable BECAUSE its enforcement point —
	// the authenticator — does not re-read status; here enforcement is the store
	// guard, so nothing needs to be destroyed to make the withdrawal bite.)
	//
	// Like SetOrgRegion it is a cross-tenant System op: version-checked so it cannot
	// lose a concurrent org update, and recorded to the tenant's own audit chain so
	// the withdrawal and the restoration are both evidence. It is idempotent, and
	// deny-closed on the reserved system tenant — suspending the system partition
	// would lock out the very path that could restore it.
	SetOrgStatus(ctx context.Context, tenant model.TenantID, status model.LifecycleStatus) (model.Org, error)
	// GetOrg returns one tenant's org row.
	GetOrg(ctx context.Context, tenant model.TenantID) (model.Org, error)
	// ListOrgs returns every tenant's org row, or ErrEnumerationNotAuthoritative
	// when this store cannot make that claim — Postgres without a BYPASSRLS admin
	// pool, where the read is RLS-limited to nothing. It NEVER reports an
	// RLS-limited result as a complete one: "I could not look" and "there is
	// nothing there" are different answers and this is where they part.
	//
	// Callers that certify coverage (checkpoint sweeps, key-transition, archival,
	// DR manifests, retention) want exactly this: the error propagates and the
	// ceremony fails closed instead of declaring success over what it never saw.
	ListOrgs(ctx context.Context) ([]model.Org, error)
	// ListOrgsVisible is the NAMED exception to that rule, for the callers whose
	// work is legitimately per-tenant and best-effort — an idempotent back-fill
	// re-run on every boot, an advisory cache warm — where a tenant this pool
	// cannot see is simply handled on a later pass, and refusing to boot over it
	// would be the worse failure.
	//
	// It returns the rows this pool can actually see and whether that set is
	// authoritative. Calling it is a decision the caller must write down: the
	// authoritative flag cannot be reached without naming it, so tolerance of a
	// partial list is always visible in the code that chose it.
	ListOrgsVisible(ctx context.Context) (orgs []model.Org, authoritative bool, err error)
	// DropTenant deletes every ordinary row owned by tenant across the registered
	// tables, recording the deletion to the system chain before it runs. Append-only
	// rows and descriptors that declare RetainOnTenantDrop remain for the separate
	// retention ceremony.
	DropTenant(ctx context.Context, tenant model.TenantID) error
	// Verify checks a tenant's audit chain from fromSeq.
	Verify(ctx context.Context, tenant model.TenantID, fromSeq int64) (VerifyReport, error)
}
