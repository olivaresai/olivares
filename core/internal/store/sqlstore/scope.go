// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// tenantScope is a tenant-pinned unit of work over one transaction. Every
// repository it hands out is built from a genericRepo bound to sc.tenant, so a
// caller cannot reach another tenant's data through it. It implements
// store.Scope.
type tenantScope struct {
	lineageWriter   *lineageWriteTracker
	s               *sqlStore
	tx              *sql.Tx
	tenant          model.TenantID
	readOnly        bool                   // true in a View scope
	audit           *auditLog              // one chain head per transaction
	directoryWriter *directoryWriteTracker // shared by all protected repositories
	bindingPoison   error                  // binding failures terminate View and prevent Mutate commit
	authorityLocked bool                   // prevents late SYSTEM locks after tenant authority

	// The custodial effect state (P2 / W1). custodyMu guards all five fields and
	// the write gate that reads them; see custodial_write_gate.go for what each
	// phase admits. They are inert in every transaction that never binds one:
	// custodyForeign is the only field an ordinary write touches, and nothing
	// reads it unless a binding is attempted.
	custodyMu         sync.Mutex
	custodyPhase      custodyPhase
	custodyBound      bool
	custodyForeign    int
	custodyPoisoned   error
	finished          bool
	loginCapabilityMu sync.Mutex
	loginCapability   *loginCapabilityTx // R5 capability state shared by every port of this transaction

	transactionNowMu  sync.Mutex
	transactionNow    model.Timestamp // latest DB time observed through this exact transaction
	transactionNowErr error           // latest observation failure invalidates stamped writes
	hasTransactionNow bool
}

var _ store.TransactionClock = (*tenantScope)(nil)
var _ store.TransactionLocker = (*tenantScope)(nil)
var _ store.AuthoritySnapshotLocker = (*tenantScope)(nil)
var _ store.DirectorySnapshotReader = (*tenantScope)(nil)

func (sc *tenantScope) Tenant() model.TenantID { return sc.tenant }

// TransactionNow implements store.TransactionClock. The query runs through the
// exact *sql.Tx that backs this tenant Scope: PostgreSQL supplies its server
// wall clock, while SQLite evaluates its engine clock under the store's
// single-writer transaction. Neither path consults the injected application
// clock, which may be deliberately fixed or skewed in tests and deployments.
func (sc *tenantScope) TransactionNow(ctx context.Context) (model.Timestamp, error) {
	sc.transactionNowMu.Lock()
	defer sc.transactionNowMu.Unlock()
	sc.transactionNow = model.Timestamp{}
	sc.transactionNowErr = nil
	sc.hasTransactionNow = true
	var observed model.Timestamp
	switch sc.s.dia.Name() {
	case store.EnginePostgres:
		var now time.Time
		if err := sc.tx.QueryRowContext(ctx, "SELECT pg_catalog.clock_timestamp()").Scan(&now); err != nil {
			sc.transactionNowErr = wrapUnavailableErr(err)
			return model.Timestamp{}, sc.transactionNowErr
		}
		observed = model.NewTimestamp(now)
	case store.EngineSQLite:
		// %f has millisecond precision in SQLite. Appending six zeroes produces
		// model.Timestamp's canonical nine-digit fractional representation without
		// pretending the engine observed finer precision.
		var canonical string
		if err := sc.tx.QueryRowContext(ctx,
			`SELECT strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')`).Scan(&canonical); err != nil {
			sc.transactionNowErr = wrapUnavailableErr(err)
			return model.Timestamp{}, sc.transactionNowErr
		}
		now, err := model.ParseTimestamp(canonical)
		if err != nil {
			sc.transactionNowErr = fmt.Errorf(
				"sqlstore: parse SQLite transaction clock %q: %w", canonical, err,
			)
			return model.Timestamp{}, sc.transactionNowErr
		}
		observed = now
	default:
		sc.transactionNowErr = fmt.Errorf(
			"sqlstore: transaction clock: unsupported engine %q", sc.s.dia.Name(),
		)
		return model.Timestamp{}, sc.transactionNowErr
	}
	sc.transactionNow = observed
	return observed, nil
}

func (sc *tenantScope) observedTransactionTime() (model.Timestamp, bool) {
	sc.transactionNowMu.Lock()
	defer sc.transactionNowMu.Unlock()
	return sc.transactionNow, sc.hasTransactionNow && sc.transactionNowErr == nil
}

// LockTransaction implements store.TransactionLocker on the exact *sql.Tx that
// backs this tenant scope. PostgreSQL uses the repository's established
// transaction-scoped advisory-lock pattern; SQLite already admits only one
// writer, so the corresponding Mutate operation needs no extra engine lock.
func (sc *tenantScope) LockTransaction(ctx context.Context, key string) error {
	if sc.readOnly {
		return store.ErrReadOnly
	}
	if key == "" {
		return errors.New("sqlstore: transaction lock key is empty")
	}
	if err := sc.guardScopeWrite(scopeRowLock, originOrdinary, "core.transaction_lock", model.ID(key)); err != nil {
		return err
	}
	switch sc.s.dia.Name() {
	case store.EnginePostgres:
		if _, err := sc.tx.ExecContext(ctx,
			`SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended($1, 0))`,
			key); err != nil {
			return fmt.Errorf("sqlstore: lock transaction: %w", wrapUnavailableErr(err))
		}
		return nil
	case store.EngineSQLite:
		return nil
	default:
		return fmt.Errorf("sqlstore: transaction lock: unsupported engine %q", sc.s.dia.Name())
	}
}

// LockAuthoritySnapshot pins an opaque set of server-observed authorization
// facts to this transaction. Descriptor opt-in is the allowlist: arbitrary
// module/core rows cannot be probed through a workspace-confined scope. The
// method returns no payload and sorts by the declared global order before
// taking locks, so callers cannot manufacture a lock-order inversion.
func (sc *tenantScope) LockAuthoritySnapshot(ctx context.Context, refs []store.AuthorizationFactRef) error {
	if sc.readOnly {
		return store.ErrReadOnly
	}
	if sc.directoryWriter != nil && !sc.directoryWriter.locked {
		sc.directoryWriter.authorityBeforeDirectory = true
	}
	sc.authorityLocked = true
	return sc.validateAuthoritySnapshot(ctx, refs, true)
}

// ValidateAuthoritySnapshot uses the identical grammar and version/lease checks
// in a new stable View. It acquires no row/table locks and never renews a lease.
func (sc *tenantScope) ValidateAuthoritySnapshot(ctx context.Context, refs []store.AuthorizationFactRef) error {
	if sc.bindingPoison != nil {
		return sc.bindingPoison
	}
	if !sc.readOnly {
		return errors.New("sqlstore: read authority barrier requires View")
	}
	return sc.validateAuthoritySnapshot(ctx, refs, false)
}

func (sc *tenantScope) validateAuthoritySnapshot(
	ctx context.Context,
	refs []store.AuthorizationFactRef,
	lock bool,
) error {
	facts, locksIdentityTable, err := sc.prepareAuthoritySnapshot(refs)
	if err != nil {
		return err
	}
	return sc.applyAuthoritySnapshot(ctx, facts, locksIdentityTable, lock)
}

type orderedAuthorityFact struct {
	ref      store.AuthorizationFactRef
	desc     model.EntityDescriptor
	subject  string
	fence    int64
	deadline model.Timestamp
	touch    bool
	locked   model.Record
}

// prepareAuthoritySnapshot validates the snapshot grammar for THIS scope's own
// tenant. It is the only entrypoint ordinary tenant callers use.
func (sc *tenantScope) prepareAuthoritySnapshot(refs []store.AuthorizationFactRef) ([]orderedAuthorityFact, bool, error) {
	return sc.prepareAuthoritySnapshotFor(sc.tenant, refs)
}

// prepareAuthoritySnapshotFor is the same grammar with the epoch-binding tenant
// supplied explicitly. The auth partition needs to validate a BUSINESS tenant's
// facts before it borrows that tenant's presentation, which is the one thing the
// scope's own pin cannot express while the scope is pinned to SYSTEM. Nothing
// else changes: the allowlist, lease coordinates, duplicate rules, budget,
// identity-table predicate and canonical order are the unchanged originals, and
// the ordinary entrypoint above still passes sc.tenant.
func (sc *tenantScope) prepareAuthoritySnapshotFor(
	tenant model.TenantID,
	refs []store.AuthorizationFactRef,
) ([]orderedAuthorityFact, bool, error) {
	if len(refs) == 0 || len(refs) > 64 {
		return nil, false, errors.New("sqlstore: authorization snapshot must contain 1..64 facts")
	}

	facts := make([]orderedAuthorityFact, 0, len(refs))
	seen := make(map[string]struct{}, len(refs))
	seenLeaseSubject := make(map[string]struct{}, len(refs))
	locksIdentityTable := false
	for _, ref := range refs {
		if ref.Kind == model.UserAuthorityKind {
			return nil, false, errors.New("sqlstore: User authority requires the typed snapshot bundle")
		}
		if validateCreateID(ref.ID) != nil || ref.Version < 1 {
			return nil, false, errors.New("sqlstore: authorization snapshot contains a malformed fact")
		}
		desc, ok := sc.s.reg.lookup(ref.Kind)
		if !ok || !allowedAuthorizationFactKind(ref.Kind) ||
			!desc.AuthorizationFact || desc.AuthorizationLockOrder == 0 {
			return nil, false, fmt.Errorf("sqlstore: entity %q is not an authorization fact", ref.Kind)
		}
		if (ref.Kind == model.DirectoryEpochKind || ref.Kind == model.AuthorizationEpochKind || model.IsLineageEpochKind(ref.Kind)) && ref.ID != model.ID(tenant) {
			return nil, false, errors.New("sqlstore: authorization epoch crosses tenant")
		}
		key := string(ref.Kind) + "\x00" + ref.ID.String()
		if _, duplicate := seen[key]; duplicate {
			return nil, false, errors.New("sqlstore: authorization snapshot contains a duplicate fact")
		}
		seen[key] = struct{}{}
		subject, fence, deadline, hasLeaseFence := ref.LeaseFenceWitness()
		if desc.AuthorizationLeaseFence.Declared() != hasLeaseFence {
			return nil, false, errors.New(
				"sqlstore: authorization snapshot lease/fence witness does not match descriptor",
			)
		}
		if hasLeaseFence {
			subjectKey := string(ref.Kind) + "\x00" + subject
			if _, duplicate := seenLeaseSubject[subjectKey]; duplicate {
				return nil, false, errors.New(
					"sqlstore: authorization snapshot contains a duplicate leased subject",
				)
			}
			seenLeaseSubject[subjectKey] = struct{}{}
		}
		facts = append(facts, orderedAuthorityFact{
			ref: ref, desc: desc, subject: subject, fence: fence,
			deadline: deadline, touch: hasLeaseFence,
		})
		locksIdentityTable = locksIdentityTable || ref.Kind == "core.identity"
	}
	sort.Slice(facts, func(i, j int) bool {
		if facts[i].desc.AuthorizationLockOrder != facts[j].desc.AuthorizationLockOrder {
			return facts[i].desc.AuthorizationLockOrder < facts[j].desc.AuthorizationLockOrder
		}
		if facts[i].ref.Kind != facts[j].ref.Kind {
			return facts[i].ref.Kind < facts[j].ref.Kind
		}
		if facts[i].touch && facts[j].touch {
			if facts[i].subject != facts[j].subject {
				return facts[i].subject < facts[j].subject
			}
			if facts[i].fence != facts[j].fence {
				return facts[i].fence < facts[j].fence
			}
		}
		return facts[i].ref.ID.String() < facts[j].ref.ID.String()
	})
	return facts, locksIdentityTable, nil
}

func (sc *tenantScope) applyAuthoritySnapshot(ctx context.Context, facts []orderedAuthorityFact, locksIdentityTable, lock bool) error {
	identityTableLocked := false
	for i := range facts {
		fact := &facts[i]
		if lock && locksIdentityTable && !identityTableLocked && fact.ref.Kind == identityDescriptor.Kind {
			// Epoch is order 5 and Identity is order 10. Take every lower-order
			// row first, then fence Identity's ExternalID predicate immediately
			// before its first row. DropTenant takes epoch before its ordinary
			// Identity/Agent deletes, so this placement prevents the cycle
			// SHARE(identity)→epoch versus epoch→RowExclusive(identity).
			if statement := authorityIdentityTableLockSQL(sc.s.dia.Name()); statement != "" {
				if _, err := sc.tx.ExecContext(ctx, statement); err != nil {
					return fmt.Errorf("sqlstore: lock identity authority set: %w",
						wrapUnavailableErr(err))
				}
			}
			identityTableLocked = true
		}
		var locked model.Record
		var err error
		if lock && sc.s.dia.Name() == store.EnginePostgres && model.IsLineageEpochKind(fact.ref.Kind) {
			// FOR UPDATE requires UPDATE privilege in PostgreSQL. Keep epochs
			// trigger-only and expose only this closed owner routine for locks.
			// #nosec G202 -- the routine name is "olivares_lock_"+fact.desc.Table under a model.IsLineageEpochKind branch: desc comes from reg.lookup for one of six compiled core kinds whose Table registerCore takes from lineageRelations, which is the same six-name set postgresLineageRoutines CREATEs (lineage_functions.go:52), and validateModule refuses the core namespace so no module can widen it. The tenant travels as $1.
			_, err = sc.tx.ExecContext(ctx, "SELECT public.olivares_lock_"+fact.desc.Table+"($1)", sc.tenant.String())
			if err == nil {
				locked, err = sc.repo(fact.desc).Get(ctx, fact.ref.ID)
			}
		} else if lock {
			locked, err = sc.repo(fact.desc).Lock(ctx, fact.ref.ID)
		} else {
			locked, err = sc.repo(fact.desc).Get(ctx, fact.ref.ID)
		}
		if err != nil {
			if fact.touch && errors.Is(err, store.ErrNotFound) {
				return store.ErrConflict
			}
			return err
		}
		if locked.Int(model.ColVersion) != fact.ref.Version {
			if lock && fact.touch {
				return store.NewLockedLeasedVersionConflict(fact.ref)
			}
			return store.ErrConflict
		}
		fact.locked = locked
		if fact.ref.Kind == "core.identity" {
			// The lifecycle stores sponsor_ref as an ExternalID, not an internal
			// Identity.ID. A row lock alone cannot prevent a PostgreSQL phantom from
			// making that reference ambiguous. The table SHARE lock above blocks the
			// RowExclusive lock of identity INSERT/UPDATE; now re-count the exact
			// ExternalID set and require this locked ID to remain its sole member.
			externalID := locked.String("external_id")
			if externalID == "" {
				return store.ErrConflict
			}
			matches, page, err := sc.repo(fact.desc).List(ctx, model.Query{
				Filters: []model.Filter{{
					Column: "external_id", Op: model.OpEq, Value: externalID,
				}},
				Limit: 2,
			})
			if err != nil {
				return err
			}
			if page.HasMore || len(matches) != 1 ||
				model.ID(matches[0].String(model.ColID)) != fact.ref.ID {
				return store.ErrConflict
			}
		}
	}

	var authorityNow model.Timestamp
	hasLeaseTouch := false
	for i := range facts {
		hasLeaseTouch = hasLeaseTouch || facts[i].touch
	}
	if hasLeaseTouch {
		var err error
		authorityNow, err = sc.TransactionNow(ctx)
		if err != nil {
			return fmt.Errorf("sqlstore: observe leased authority time: %w", err)
		}
	}
	for i := range facts {
		fact := &facts[i]
		if !fact.touch {
			continue
		}
		spec := fact.desc.AuthorizationLeaseFence
		if fact.locked.String(spec.SubjectColumn) != fact.subject ||
			fact.locked.Int(spec.FenceColumn) != fact.fence ||
			fact.locked.String(spec.StateColumn) != spec.ActiveValue ||
			fact.locked.String(spec.DeadlineColumn) != fact.deadline.String() ||
			!authorityNow.Before(fact.deadline) {
			return store.ErrConflict
		}
		if lock {
			// THE authority-touch site. Its input is fact.locked — the record this
			// engine read and locked above — so the origin attributes a renewal of
			// the engine's own leased authority, never a consumer write.
			if _, err := sc.authorityTouchRepo(fact.desc).UpdateAtTransactionTime(ctx, fact.locked); err != nil {
				return err
			}
		}
	}
	return nil
}

func authorityIdentityTableLockSQL(engine store.Engine) string {
	if engine == store.EnginePostgres {
		return "LOCK TABLE ONLY public.identities IN SHARE MODE"
	}
	return ""
}

// repo builds a tenant-pinned generic repository for a descriptor, wiring the
// shared audit log when the descriptor is audited.
func (sc *tenantScope) repo(desc model.EntityDescriptor) *genericRepo {
	var a *auditLog
	if desc.Audited {
		a = sc.auditLog()
	}
	return &genericRepo{
		tx:               sc.tx,
		tenant:           sc.tenant,
		dia:              sc.s.dia,
		desc:             desc,
		clock:            sc.s.clock,
		audit:            a,
		debug:            sc.s.debug,
		readOnly:         sc.readOnly,
		engineQualified:  isDirectoryAuthorityTable(desc.Table),
		transactionStamp: sc.observedTransactionTime,
		// Every repository a tenant Scope hands out reports its writes to the
		// one gate. origin stays the zero value (ordinary) here: only
		// custodyRepo and authorityTouchRepo change it.
		writeGuard: sc.guardScopeWrite,
		poison: func(err error) {
			if sc.lineageWriter != nil {
				sc.lineageWriter.poison(err)
			}
			if sc.directoryWriter != nil {
				sc.directoryWriter.poison(err)
			}
		},
	}
}

func (sc *tenantScope) auditLog() *auditLog {
	if sc.audit == nil {
		sc.audit = &auditLog{
			tx: sc.tx, tenant: sc.tenant, dia: sc.s.dia, clock: sc.s.clock,
			readOnly: sc.readOnly, signEvent: sc.s.signEvent,
			spoolMaxBytes: sc.s.spoolMaxBytes, spoolOnFull: sc.s.spoolOnFull,
			blindMeta: sc.s.blindMeta, directoryWriter: sc.directoryWriter,
			writeGuard: sc.guardScopeWrite,
		}
	}
	return sc.audit
}

func (sc *tenantScope) Org(ctx context.Context) (model.Org, error) {
	// An org's id equals its tenant id, so its own row is reachable by that id
	// inside its own scope.
	rec, err := sc.repo(orgDescriptor).Get(ctx, model.ID(sc.tenant.String()))
	if err != nil {
		return model.Org{}, err
	}
	base, err := baseFromRecord(rec)
	if err != nil {
		return model.Org{}, err
	}
	return orgCodec.Decode(base, rec)
}

// SetOrgSettings replaces the bound tenant's own org Settings and returns the
// updated row. It reads the org (id == tenant id), swaps Settings, and updates
// through the same optimistic-concurrency path as any other row, so it cannot
// reach another tenant and respects the single-writer discipline.
func (sc *tenantScope) SetOrgSettings(ctx context.Context, settings map[string]any) (model.Org, error) {
	if sc.readOnly {
		return model.Org{}, store.ErrReadOnly
	}
	org, err := sc.Org(ctx)
	if err != nil {
		return model.Org{}, err
	}
	org.Settings = settings
	rec, err := orgCodec.Encode(org)
	if err != nil {
		return model.Org{}, err
	}
	rec[model.ColID] = org.ID.String()
	rec[model.ColVersion] = org.Version
	updated, err := sc.repo(orgDescriptor).Update(ctx, rec)
	if err != nil {
		return model.Org{}, err
	}
	base, err := baseFromRecord(updated)
	if err != nil {
		return model.Org{}, err
	}
	return orgCodec.Decode(base, updated)
}

func (sc *tenantScope) Agents() store.Repository[model.Agent] {
	inner := newTypedRepo(sc.repo(agentDescriptor), agentCodec)
	return newDirectoryTrackedRepo(
		inner, sc.directoryWriter,
		agentDirectoryResolver(sc, inner),
	)
}
func (sc *tenantScope) Sessions() store.Repository[model.Session] {
	inner := newTypedRepo(sc.repo(sessionDescriptor), sessionCodec)
	if sc.lineageWriter == nil {
		return inner
	}
	return &lineageTrackedRepo[model.Session]{Repository: inner, tracker: sc.lineageWriter}
}
func (sc *tenantScope) Providers() store.Repository[model.Provider] {
	return newTypedRepo(sc.repo(providerDescriptor), providerCodec)
}
func (sc *tenantScope) Models() store.Repository[model.Model] {
	return newTypedRepo(sc.repo(modelDescriptor), modelCodec)
}
func (sc *tenantScope) MCPServers() store.Repository[model.MCPServer] {
	return newTypedRepo(sc.repo(mcpServerDescriptor), mcpServerCodec)
}
func (sc *tenantScope) Skills() store.Repository[model.Skill] {
	return newTypedRepo(sc.repo(skillDescriptor), skillCodec)
}
func (sc *tenantScope) Tools() store.Repository[model.Tool] {
	return newTypedRepo(sc.repo(toolDescriptor), toolCodec)
}
func (sc *tenantScope) Resources() store.ResourceRepo {
	return newResourceRepo(sc.repo(resourceDescriptor))
}
func (sc *tenantScope) Identities() store.MutableRepository[model.Identity] {
	inner := newTypedRepo(sc.repo(identityDescriptor), identityCodec)
	tracked := newDirectoryTrackedRepo(
		inner, sc.directoryWriter,
		tenantLocalDirectoryResolver[model.Identity](sc.tenant),
	)
	return newMutableDirectoryRepo(tracked)
}
func (sc *tenantScope) Policies() store.Repository[model.Policy] {
	return newPolicyRepo(sc.repo(policyDescriptor))
}
func (sc *tenantScope) Costs() store.Repository[model.CostRecord] {
	return newTypedRepo(sc.repo(costDescriptor), costCodec)
}
func (sc *tenantScope) Evals() store.Repository[model.EvalResult] {
	return newTypedRepo(sc.repo(evalDescriptor), evalCodec)
}
func (sc *tenantScope) Findings() store.Repository[model.Finding] {
	return newTypedRepo(sc.repo(findingDescriptor), findingCodec)
}
func (sc *tenantScope) Health() store.Repository[model.HealthStatus] {
	return newTypedRepo(sc.repo(healthDescriptor), healthCodec)
}
func (sc *tenantScope) Deployments() store.Repository[model.Deployment] {
	return newTypedRepo(sc.repo(deploymentDescriptor), deploymentCodec)
}

func (sc *tenantScope) Workspaces() store.Repository[model.Workspace] {
	inner := newTypedRepo(sc.repo(workspaceDescriptor), workspaceCodec)
	return newStableWorkspaceRepo(inner, sc.directoryWriter, sc.readOnly, sc.initializeWorkspace)
}
func (sc *tenantScope) AgentGroups() store.Repository[model.AgentGroup] {
	inner := newTypedRepo(sc.repo(agentGroupDescriptor), agentGroupCodec)
	return newDirectoryTrackedRepo(
		inner, sc.directoryWriter,
		tenantLocalDirectoryResolver[model.AgentGroup](sc.tenant),
	)
}
func (sc *tenantScope) AgentGroupMembers() store.Repository[model.AgentGroupMember] {
	inner := newTypedRepo(sc.repo(agentGroupMemberDescriptor), agentGroupMemberCodec)
	return newDirectoryTrackedRepo(
		inner, sc.directoryWriter,
		tenantLocalDirectoryResolver[model.AgentGroupMember](sc.tenant),
	)
}

// DefaultWorkspace returns the bound tenant's default workspace (the reserved
// "default" slug). It is the back-compat resolution target for an unset
// WorkspaceID. ErrNotFound means the tenant's default has not been seeded yet
// (EnsureDefaultWorkspaces, which boot runs, or CreateOrg for a new tenant).
func (sc *tenantScope) DefaultWorkspace(ctx context.Context) (model.Workspace, error) {
	ws, _, err := sc.Workspaces().List(ctx, model.Query{
		Filters: []model.Filter{{Column: "slug", Op: model.OpEq, Value: model.DefaultWorkspaceSlug}},
		Limit:   1,
	})
	if err != nil {
		return model.Workspace{}, err
	}
	if len(ws) == 0 {
		return model.Workspace{}, store.ErrNotFound
	}
	return ws[0], nil
}

func (sc *tenantScope) AccessEdges() store.AccessEdgeRepo {
	return newAccessEdgeRepo(sc.repo(accessEdgeDescriptor), sc)
}

func (sc *tenantScope) Audit() store.AuditLog { return sc.auditLog() }

// EvidenceOperations returns the durable evidence operation journal (q1),
// sharing the scope's audit log so a claim/settle's ledger event and its row
// change ride one chain head in one transaction.
func (sc *tenantScope) EvidenceOperations() store.EvidenceOperationRepo {
	return newEvidenceOpsRepo(
		sc.repo(evidenceOpDescriptor), sc.auditLog(), sc.s.evidenceClaimAfterMissTestHook,
	)
}

// AccessEvidence returns the tenant-pinned access-evidence store (v26.9
// increment A). It shares the scope's audit log, so a record and the ledger
// event anchoring it ride one chain head in one transaction, and it is handed
// the SAME evidence journal the rest of the engine uses — read-only, as the
// single authority on governed operations — rather than a second copy of its
// semantics.
func (sc *tenantScope) AccessEvidence() store.AccessEvidenceRepo {
	return newAccessEvidenceRepo(
		sc.repo(policyArtifactDescriptor),
		sc.repo(authorityTransitionDescriptor),
		sc.repo(actionObservationDescriptor),
		sc.repo(authorizationDecisionDescriptor),
		sc.EvidenceOperations(),
		sc.auditLog(),
	)
}

func (sc *tenantScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	// The generic extension path is for MODULE entities only. The reserved "core"
	// namespace — which includes the credential tables (users, api_tokens, …) — is
	// never reachable through Ext: a module that holds a Scope must not be able to
	// read or write engine-owned entities generically. Core entities are reached
	// only through their typed accessors (and credentials only through the
	// engine's AuthScope, which a module never holds).
	if kind.Namespace() == model.CoreNamespace {
		return nil, store.ErrUnknownEntity
	}
	d, ok := sc.s.reg.lookup(kind)
	if !ok {
		return nil, store.ErrUnknownEntity
	}
	return sc.repo(d), nil
}
