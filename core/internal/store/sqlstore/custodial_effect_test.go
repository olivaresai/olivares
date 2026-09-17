// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The custodial effect's engine cases (P2 / W1).
//
// Every case enters through the real production constructors: Open builds the
// store, Mutate/View builds the tenant Scope, store.ConfineWorkspace builds the
// confined one, and BindCustodialEffect is the real method on the real
// *tenantScope. Nothing here calls a helper that a caller could not call.
//
// The intended relation is registered as a FIXTURE, because that is the honest
// shape of W1: the capability is defined against a run descriptor that declares
// its authorization-workspace lineage, and the production module does not
// declare one yet. TestCustodialRelationRefusesTheCurrentProductionRunShape
// registers the production shape instead and proves the capability reports
// relation_invalid while ordinary startup and ordinary operations continue.

const (
	custodyTestActive  = "active"
	custodyTestHolder  = "agent:operator"
	custodyTestActor   = "user:operator"
	custodyTestActorKd = model.ActorUser
	custodyTestFence   = int64(7)
)

// registerCustodyRelation declares the INTENDED run/claim pair: the production
// columns the engine fixes, plus the authorization-workspace lineage W2 adds.
func registerCustodyRelation(reg store.ExtensionRegistry) error {
	return registerCustodyRelationWith(reg, true)
}

// registerProductionRunShape declares the run descriptor as it stands TODAY:
// the same columns, and no workspace lineage at all.
func registerProductionRunShape(reg store.ExtensionRegistry) error {
	return registerCustodyRelationWith(reg, false)
}

func registerCustodyRelationWith(reg store.ExtensionRegistry, lineage bool) error {
	run := model.EntityDescriptor{
		Kind:  custodyRunKind,
		Table: custodyRunTable,
		Fields: []model.FieldSpec{
			{Name: custodyRunRefColumn, Kind: model.KindText},
			{Name: custodyRunLaunchColumn, Kind: model.KindUUID, Nullable: true},
			{Name: custodyRunLineageColumn, Kind: model.KindUUID, Nullable: true},
			{Name: custodyRunClaimHolderCol, Kind: model.KindText, Nullable: true},
			{Name: custodyRunClaimFenceCol, Kind: model.KindInt, Nullable: true},
			{Name: custodyRunClaimSubjectCol, Kind: model.KindText, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			Name:    "sessions_run_ref_uniq",
			Columns: []string{model.ColTenantID, custodyRunRefColumn},
			Unique:  true,
		}},
	}
	if lineage {
		run.WorkspaceLineage = model.WorkspaceLineageSpec{
			Column:   custodyRunLineageColumn,
			Encoding: model.WorkspaceLineageID,
			Unset:    model.WorkspaceUnsetHidden,
		}
	}
	if err := reg.Register(run); err != nil {
		return err
	}
	return reg.Register(model.EntityDescriptor{
		Kind:                   custodyClaimKind,
		Table:                  custodyClaimTable,
		AuthorizationFact:      true,
		AuthorizationLockOrder: 40,
		AuthorizationLeaseFence: model.AuthorizationLeaseFenceSpec{
			SubjectColumn:  custodyClaimSubjectColumn,
			FenceColumn:    custodyClaimFenceColumn,
			StateColumn:    custodyClaimStateColumn,
			ActiveValue:    custodyTestActive,
			DeadlineColumn: custodyClaimDeadlineColumn,
		},
		Fields: []model.FieldSpec{
			{Name: custodyClaimSubjectColumn, Kind: model.KindText},
			{Name: custodyClaimHolderColumn, Kind: model.KindText},
			{Name: custodyClaimFenceColumn, Kind: model.KindInt},
			{Name: custodyClaimStateColumn, Kind: model.KindText, Indexed: true},
			{Name: custodyClaimDeadlineColumn, Kind: model.KindTimestamp, Indexed: true},
		},
		Indexes: []model.IndexSpec{{
			Name:    "sessions_claim_sid_uniq",
			Columns: []string{model.ColTenantID, custodyClaimSubjectColumn},
			Unique:  true,
		}},
	})
}

// custodyEngine names one real engine and how to configure a store on it. Both
// are real: SQLite on a file (so a case may reopen the SAME database with a
// different audit-spool policy) and the owned PostgreSQL 16 fixture.
type custodyEngine struct {
	name string
	// prepare returns the store configuration and, where the engine admits a
	// second concurrent writer, a DSN for an OUT-OF-BAND connection. SQLite
	// returns none: its single writer is held for the whole transaction, so a
	// second writer cannot exist while a custodial claim is open.
	prepare func(t *testing.T) (store.Config, string)
}

func custodyEngines() []custodyEngine {
	return []custodyEngine{
		{name: "sqlite", prepare: func(t *testing.T) (store.Config, string) {
			return store.Config{
				Engine: store.EngineSQLite,
				DSN:    filepath.Join(t.TempDir(), "custody.db"),
				Debug:  true,
			}, ""
		}},
		{name: "postgres", prepare: func(t *testing.T) (store.Config, string) {
			// AdminDSN is not optional here: the audit-spool budget the degrade and
			// block cases arm needs a cross-tenant read of audit_events, which FORCE
			// row-level security blocks on the application role.
			dsns := isolatedPG(t)
			return store.Config{
				Engine:   store.EnginePostgres,
				DSN:      dsns.App,
				AdminDSN: dsns.Admin,
				MaxConns: 6,
			}, dsns.Superuser
		}},
	}
}

func openCustodyStore(
	t *testing.T, cfg store.Config, register func(store.ExtensionRegistry) error,
) store.Store {
	t.Helper()
	st, err := Open(context.Background(), cfg, register)
	if err != nil {
		t.Fatalf("open %s store: %v", cfg.Engine, err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// custodyFixture is one tenant with a workspace, a second workspace, one run
// bound to the first workspace and its live admission claim.
type custodyFixture struct {
	t            *testing.T
	st           store.Store
	cfg          store.Config
	outOfBand    string
	tenant       model.TenantID
	workspace    model.ID
	other        model.ID
	runRef       string
	launch       model.ID
	sid          string
	claimID      model.ID
	claimVersion int64
}

func newCustodyFixture(t *testing.T, e custodyEngine) *custodyFixture {
	t.Helper()
	cfg, outOfBand := e.prepare(t)
	st := openCustodyStore(t, cfg, registerCustodyRelation)
	f := &custodyFixture{
		t: t, st: st, cfg: cfg, outOfBand: outOfBand,
		tenant: provisionTenant(t, st, "custody-"+uniqueSuffix()),
		runRef: "run-" + uniqueSuffix(),
		launch: model.NewID(),
		sid:    "sid-" + uniqueSuffix(),
	}
	f.seed()
	return f
}

func (f *custodyFixture) seed() {
	f.t.Helper()
	ctx := context.Background()
	err := f.st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		def, err := sc.DefaultWorkspace(ctx)
		if err != nil {
			return err
		}
		f.workspace = def.ID
		other, err := sc.Workspaces().Create(ctx, model.Workspace{
			Name: "other", Slug: "other-" + uniqueSuffix(),
		})
		if err != nil {
			return err
		}
		f.other = other.ID
		now, err := sc.(store.TransactionClock).TransactionNow(ctx)
		if err != nil {
			return err
		}
		claims, err := sc.Ext(custodyClaimKind)
		if err != nil {
			return err
		}
		claim, err := claims.Create(ctx, model.Record{
			custodyClaimSubjectColumn:  f.sid,
			custodyClaimHolderColumn:   custodyTestHolder,
			custodyClaimFenceColumn:    custodyTestFence,
			custodyClaimStateColumn:    custodyTestActive,
			custodyClaimDeadlineColumn: model.NewTimestamp(now.Time().Add(time.Hour)).String(),
		})
		if err != nil {
			return err
		}
		f.claimID = model.ID(claim.String(model.ColID))
		f.claimVersion = claim.Int(model.ColVersion)
		runs, err := sc.Ext(custodyRunKind)
		if err != nil {
			return err
		}
		_, err = runs.Create(ctx, model.Record{
			custodyRunRefColumn:       f.runRef,
			custodyRunLaunchColumn:    f.launch.String(),
			custodyRunLineageColumn:   f.workspace.String(),
			custodyRunClaimHolderCol:  custodyTestHolder,
			custodyRunClaimFenceCol:   custodyTestFence,
			custodyRunClaimSubjectCol: f.sid,
		})
		return err
	})
	if err != nil {
		f.t.Fatalf("seed custody fixture: %v", err)
	}
}

func (f *custodyFixture) operationID() string {
	return store.ManagedStopOperationPrefix + "op-" + uniqueSuffix()
}

func (f *custodyFixture) claimBinding(operationID string) store.CustodialEffectBinding {
	return store.CustodialEffectBinding{
		Mode:           store.CustodialClaim,
		OperationID:    operationID,
		SemanticDigest: "msv1:" + strings.Repeat("a", 64),
		RunRef:         f.runRef,
		RunLaunchID:    f.launch,
		ClaimVersion:   f.claimVersion,
		Actor:          custodyTestActor,
		ActorKind:      custodyTestActorKd,
	}
}

func (f *custodyFixture) settleBinding(operationID string) store.CustodialEffectBinding {
	b := f.claimBinding(operationID)
	b.Mode = store.CustodialSettle
	b.ClaimVersion = 0
	return b
}

func (f *custodyFixture) lookupBinding(operationID string) store.CustodialEffectBinding {
	b := f.claimBinding(operationID)
	b.Mode = store.CustodialLookup
	b.ClaimVersion = 0
	return b
}

// confined runs fn with a workspace-confined Scope built by the production
// ConfineWorkspace, in a real Mutate.
func (f *custodyFixture) confined(fn func(sc store.Scope) error) error {
	ctx := context.Background()
	return f.st.Mutate(ctx, f.tenant, func(raw store.Scope) error {
		sc, err := store.ConfineWorkspace(ctx, raw, f.workspace)
		if err != nil {
			return err
		}
		return fn(sc)
	})
}

func (f *custodyFixture) confinedTo(ws model.ID, fn func(sc store.Scope) error) error {
	ctx := context.Background()
	return f.st.Mutate(ctx, f.tenant, func(raw store.Scope) error {
		sc, err := store.ConfineWorkspace(ctx, raw, ws)
		if err != nil {
			return err
		}
		return fn(sc)
	})
}

func custodyClaimer(t *testing.T, sc store.Scope) store.CustodialEffectClaimer {
	t.Helper()
	claimer, ok := sc.(store.CustodialEffectClaimer)
	if !ok {
		t.Fatalf("scope %T does not expose the custodial effect capability", sc)
	}
	return claimer
}

// journalRow reads one operation row straight from the store, outside the
// transaction under test, so the assertions observe COMMITTED state.
func (f *custodyFixture) journalRow(operationID string) (model.EvidenceOperation, bool) {
	f.t.Helper()
	var (
		op    model.EvidenceOperation
		found bool
	)
	err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		got, err := sc.EvidenceOperations().Get(context.Background(), operationID)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		op, found = got, true
		return nil
	})
	if err != nil {
		f.t.Fatalf("read journal row: %v", err)
	}
	return op, found
}

func (f *custodyFixture) claimRow() model.Record {
	f.t.Helper()
	var out model.Record
	err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		claims, err := sc.Ext(custodyClaimKind)
		if err != nil {
			return err
		}
		rec, err := claims.Get(context.Background(), f.claimID)
		out = rec
		return err
	})
	if err != nil {
		f.t.Fatalf("read claim row: %v", err)
	}
	return out
}

func (f *custodyFixture) auditCount() int {
	f.t.Helper()
	var n int
	err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 1, func(model.AuditEvent) error {
			n++
			return nil
		})
	})
	if err != nil {
		f.t.Fatalf("walk ledger: %v", err)
	}
	return n
}

func forEachCustodyEngine(t *testing.T, fn func(t *testing.T, e custodyEngine)) {
	for _, e := range custodyEngines() {
		t.Run(e.name, func(t *testing.T) { fn(t, e) })
	}
}

// ---- readiness and the fixed relation ---------------------------------

// TestCustodialRelationRefusesTheCurrentProductionRunShape is the state of an
// ordinary build: the run descriptor declares no authorization-workspace
// lineage, so the capability is unavailable with a named reason — and the store
// opens, the module tables exist and ordinary writes still work.
func TestCustodialRelationRefusesTheCurrentProductionRunShape(t *testing.T) {
	forEachCustodyEngine(t, func(t *testing.T, e custodyEngine) {
		ctx := context.Background()
		cfg, _ := e.prepare(t)
		st := openCustodyStore(t, cfg, registerProductionRunShape)
		readiness := st.(store.CustodialEffectReadiness).ManagedStopReadiness(ctx)
		if readiness.Ready || readiness.Reason != store.CustodyUnreadyRelation {
			t.Fatalf("readiness = %+v, want relation_invalid", readiness)
		}
		if !strings.Contains(readiness.Detail, "declares no workspace lineage") {
			t.Fatalf("readiness detail = %q", readiness.Detail)
		}
		tenant := provisionTenant(t, st, "custody-prod-"+uniqueSuffix())
		// Ordinary startup and ordinary operations continue.
		if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
			runs, err := sc.Ext(custodyRunKind)
			if err != nil {
				return err
			}
			_, err = runs.Create(ctx, model.Record{custodyRunRefColumn: "run-" + uniqueSuffix()})
			return err
		}); err != nil {
			t.Fatalf("ordinary module write on an unavailable capability: %v", err)
		}
		// Every bind refuses, before any lock, read or write.
		err := st.Mutate(ctx, tenant, func(raw store.Scope) error {
			_, bindErr := custodyClaimer(t, raw).BindCustodialEffect(ctx, store.CustodialEffectBinding{
				Mode: store.CustodialClaim, OperationID: store.ManagedStopOperationPrefix + "x",
				SemanticDigest: "msv1:x", RunRef: "run", RunLaunchID: model.NewID(), ClaimVersion: 1,
				Actor: custodyTestActor, ActorKind: custodyTestActorKd,
			})
			if !errors.Is(bindErr, store.ErrCustodyUnavailable) ||
				!errors.Is(bindErr, store.ErrCustodyRelationInvalid) {
				t.Fatalf("bind error = %v, want unavailable/relation_invalid", bindErr)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("mutate: %v", err)
		}
	})
}

// TestCustodialRelationDeviationsAreEachRefused walks the exact deviations the
// engine's fixed relation rejects. It runs on the registry, which is where the
// verdict is taken, and names each one.
func TestCustodialRelationDeviationsAreEachRefused(t *testing.T) {
	valid := func() (model.EntityDescriptor, model.EntityDescriptor) {
		reg := newRegistry()
		if err := registerCustodyRelation(reg); err != nil {
			t.Fatalf("register intended relation: %v", err)
		}
		run, _ := reg.lookup(custodyRunKind)
		claim, _ := reg.lookup(custodyClaimKind)
		return run, claim
	}
	baseRun, baseClaim := valid()
	if _, err := resolveCustodialRelationFromDescriptors(baseRun, baseClaim); err != nil {
		t.Fatalf("the intended relation must resolve: %v", err)
	}
	cases := []struct {
		name  string
		run   func(*model.EntityDescriptor)
		claim func(*model.EntityDescriptor)
		want  string
	}{
		{name: "run table renamed", run: func(d *model.EntityDescriptor) { d.Table = "sessions_run_v2" },
			want: "is table"},
		{name: "run key column absent", run: func(d *model.EntityDescriptor) {
			d.Fields = dropField(d.Fields, custodyRunRefColumn)
		}, want: "declares no column"},
		{name: "run generation retyped", run: func(d *model.EntityDescriptor) {
			d.Fields = retypeField(d.Fields, custodyRunLaunchColumn, model.KindText)
		}, want: "is text, not uuid"},
		{name: "run lineage absent", run: func(d *model.EntityDescriptor) {
			d.WorkspaceLineage = model.WorkspaceLineageSpec{}
		}, want: "declares no workspace lineage"},
		{name: "run lineage on another column", run: func(d *model.EntityDescriptor) {
			d.Fields = append(d.Fields, model.FieldSpec{Name: "workspace_ref", Kind: model.KindUUID, Nullable: true})
			d.WorkspaceLineage.Column = "workspace_ref"
		}, want: "declares lineage on"},
		{name: "run lineage unset means default", run: func(d *model.EntityDescriptor) {
			d.WorkspaceLineage.Unset = model.WorkspaceUnsetMeansDefault
		}, want: "declares unset semantics"},
		{name: "run unique index absent", run: func(d *model.EntityDescriptor) { d.Indexes = nil },
			want: "declares no unique index"},
		{name: "claim lease fence absent", claim: func(d *model.EntityDescriptor) {
			d.AuthorizationLeaseFence = model.AuthorizationLeaseFenceSpec{}
		}, want: "declares no authorization lease fence"},
		{name: "claim lease fence on other columns", claim: func(d *model.EntityDescriptor) {
			d.Fields = append(d.Fields, model.FieldSpec{Name: "other_fence", Kind: model.KindInt})
			d.AuthorizationLeaseFence.FenceColumn = "other_fence"
		}, want: "declares lease coordinates"},
		{name: "claim holder absent", claim: func(d *model.EntityDescriptor) {
			d.Fields = dropField(d.Fields, custodyClaimHolderColumn)
		}, want: "declares no column"},
		{name: "claim unique index absent", claim: func(d *model.EntityDescriptor) { d.Indexes = nil },
			want: "declares no unique index"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			run, claim := valid()
			if c.run != nil {
				c.run(&run)
			}
			if c.claim != nil {
				c.claim(&claim)
			}
			_, err := resolveCustodialRelationFromDescriptors(run, claim)
			if !errors.Is(err, store.ErrCustodyRelationInvalid) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error = %v, want relation_invalid containing %q", err, c.want)
			}
		})
	}
}

func dropField(in []model.FieldSpec, name string) []model.FieldSpec {
	out := make([]model.FieldSpec, 0, len(in))
	for _, f := range in {
		if f.Name != name {
			out = append(out, f)
		}
	}
	return out
}

func retypeField(in []model.FieldSpec, name string, kind model.SQLKind) []model.FieldSpec {
	out := append([]model.FieldSpec(nil), in...)
	for i := range out {
		if out[i].Name == name {
			out[i].Kind = kind
		}
	}
	return out
}

// TestCustodialReadinessReportsEachUnavailableHalf proves the three reasons are
// distinguishable, because their remedies are.
func TestCustodialReadinessReportsEachUnavailableHalf(t *testing.T) {
	ctx := context.Background()
	cfg := store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(t.TempDir(), "readiness.db"), Debug: true}
	st := openCustodyStore(t, cfg, registerCustodyRelation).(*sqlStore)
	if got := st.ManagedStopReadiness(ctx); !got.Ready {
		t.Fatalf("intended relation readiness = %+v, want ready", got)
	}
	// The schema half. A store whose journal has not crossed the controlled
	// transition cannot open at all today — boot refuses before serving — so the
	// flag is set here directly. The negative control is the GATE, not a database
	// this deployment can produce.
	st.evidenceRefusedSupported = false
	if got := st.ManagedStopReadiness(ctx); got.Ready || got.Reason != store.CustodyUnreadySchema {
		t.Fatalf("schema readiness = %+v, want schema_unsupported", got)
	}
	st.evidenceRefusedSupported = true
	// The fencing half.
	st.elector = &custodyUnfencedElector{}
	if got := st.ManagedStopReadiness(ctx); got.Ready || got.Reason != store.CustodyUnreadyLeader {
		t.Fatalf("leader readiness = %+v, want leader_unwired", got)
	}
	tenant := provisionTenantOnLeader(t, st, "custody-readiness")
	err := st.Mutate(ctx, tenant, func(raw store.Scope) error {
		_, bindErr := custodyClaimer(t, raw).BindCustodialEffect(ctx, store.CustodialEffectBinding{
			Mode: store.CustodialLookup, OperationID: store.ManagedStopOperationPrefix + "x",
			SemanticDigest: "msv1:x", RunRef: "r", Actor: custodyTestActor, ActorKind: custodyTestActorKd,
		})
		if !errors.Is(bindErr, store.ErrCustodyLeaderUnwired) {
			t.Fatalf("bind without a fencer = %v", bindErr)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("mutate: %v", err)
	}
}

// provisionTenantOnLeader provisions while the store still has its own elector,
// then restores the test elector the caller installed.
func provisionTenantOnLeader(t *testing.T, st *sqlStore, slug string) model.TenantID {
	t.Helper()
	swapped := st.elector
	st.elector = newAlwaysLeader()
	tenant := provisionTenant(t, st, slug+"-"+uniqueSuffix())
	st.elector = swapped
	return tenant
}

// custodyUnfencedElector is a leader WITHOUT the durable fence capability.
type custodyUnfencedElector struct{}

func (custodyUnfencedElector) IsLeader() bool                        { return true }
func (custodyUnfencedElector) Active() bool                          { return true }
func (custodyUnfencedElector) active() bool                          { return true }
func (custodyUnfencedElector) Run(context.Context) error             { return nil }
func (custodyUnfencedElector) Resign(context.Context) error          { return nil }
func (custodyUnfencedElector) Epoch() uint64                         { return 1 }
func (custodyUnfencedElector) OnPromote(func(context.Context) error) {}

// custodyScriptedElector is a leader whose DURABLE fence is scripted: each call
// returns the next value, and the last value repeats.
type custodyScriptedElector struct {
	mu     sync.Mutex
	epochs []uint64
	err    error
	calls  int
}

func (e *custodyScriptedElector) IsLeader() bool                        { return true }
func (e *custodyScriptedElector) Active() bool                          { return true }
func (e *custodyScriptedElector) active() bool                          { return true }
func (e *custodyScriptedElector) Run(context.Context) error             { return nil }
func (e *custodyScriptedElector) Resign(context.Context) error          { return nil }
func (e *custodyScriptedElector) Epoch() uint64                         { return 1 }
func (e *custodyScriptedElector) OnPromote(func(context.Context) error) {}

func (e *custodyScriptedElector) FencedEpoch(context.Context) (uint64, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil {
		return 0, e.err
	}
	i := e.calls
	e.calls++
	if i >= len(e.epochs) {
		i = len(e.epochs) - 1
	}
	return e.epochs[i], nil
}

// ---- binding shape, transaction limits and handle lifetime ------------

// TestCustodialBindingShapeIsRefusedBeforeAnyIO walks the malformed bindings a
// caller can present. Each is a caller bug: it refuses with no I/O, does not
// poison, and does not consume the transaction's single binding.
func TestCustodialBindingShapeIsRefusedBeforeAnyIO(t *testing.T) {
	f := newCustodyFixture(t, custodyEngines()[0])
	valid := f.claimBinding(f.operationID())
	cases := []struct {
		name string
		bind func(*store.CustodialEffectBinding)
	}{
		{"unknown mode", func(b *store.CustodialEffectBinding) { b.Mode = 0 }},
		{"blank operation id", func(b *store.CustodialEffectBinding) { b.OperationID = "" }},
		{"padded operation id", func(b *store.CustodialEffectBinding) { b.OperationID = " " + b.OperationID }},
		{"foreign operation id", func(b *store.CustodialEffectBinding) { b.OperationID = "mcp.gateway:x" }},
		{"prefix only", func(b *store.CustodialEffectBinding) { b.OperationID = store.ManagedStopOperationPrefix }},
		{"oversized operation id", func(b *store.CustodialEffectBinding) {
			b.OperationID = store.ManagedStopOperationPrefix + strings.Repeat("x", 200)
		}},
		{"blank semantic digest", func(b *store.CustodialEffectBinding) { b.SemanticDigest = "  " }},
		{"blank run reference", func(b *store.CustodialEffectBinding) { b.RunRef = "" }},
		{"blank actor", func(b *store.CustodialEffectBinding) { b.Actor = "" }},
		{"blank actor kind", func(b *store.CustodialEffectBinding) { b.ActorKind = "" }},
		{"claim without a generation", func(b *store.CustodialEffectBinding) { b.RunLaunchID = "" }},
		{"claim without an observed version", func(b *store.CustodialEffectBinding) { b.ClaimVersion = 0 }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := f.confined(func(sc store.Scope) error {
				b := valid
				c.bind(&b)
				if _, err := custodyClaimer(t, sc).BindCustodialEffect(context.Background(), b); !errors.Is(err, store.ErrCustodyBindingInvalid) {
					t.Fatalf("bind error = %v, want ErrCustodyBindingInvalid", err)
				}
				// The transaction is untouched: a valid binding still succeeds.
				h, err := custodyClaimer(t, sc).BindCustodialEffect(context.Background(), valid)
				if err != nil || h == nil {
					t.Fatalf("valid bind after a malformed one = %v", err)
				}
				return nil
			}); err != nil {
				t.Fatalf("mutate: %v", err)
			}
		})
	}
}

// TestCustodialBindAdmitsOneBindingPerTransaction proves the second binding is
// refused AND poisons, so a transaction that tried two cannot commit.
func TestCustodialBindAdmitsOneBindingPerTransaction(t *testing.T) {
	forEachCustodyEngine(t, func(t *testing.T, e custodyEngine) {
		f := newCustodyFixture(t, e)
		ctx := context.Background()
		err := f.confined(func(sc store.Scope) error {
			claimer := custodyClaimer(t, sc)
			if _, err := claimer.BindCustodialEffect(ctx, f.claimBinding(f.operationID())); err != nil {
				return err
			}
			if _, err := claimer.BindCustodialEffect(ctx, f.claimBinding(f.operationID())); !errors.Is(err, store.ErrCustodyAlreadyBound) {
				t.Fatalf("second binding = %v, want ErrCustodyAlreadyBound", err)
			}
			return nil // swallowed on purpose
		})
		if !errors.Is(err, store.ErrCustodyAlreadyBound) {
			t.Fatalf("commit after a swallowed second binding = %v, want a refusal", err)
		}
	})
}

// TestCustodialBindRefusesAForeignPreBindWriteSet proves the origin gate: an
// ordinary consumer write before the binding refuses and poisons, while the
// engine's OWN leased-authority touch does not.
func TestCustodialBindRefusesAForeignPreBindWriteSet(t *testing.T) {
	forEachCustodyEngine(t, func(t *testing.T, e custodyEngine) {
		f := newCustodyFixture(t, e)
		ctx := context.Background()

		t.Run("ordinary write refuses", func(t *testing.T) {
			err := f.confined(func(sc store.Scope) error {
				runs, err := sc.Ext(custodyRunKind)
				if err != nil {
					return err
				}
				if _, err := runs.Create(ctx, model.Record{
					custodyRunRefColumn:     "run-" + uniqueSuffix(),
					custodyRunLineageColumn: f.workspace.String(),
				}); err != nil {
					return err
				}
				if _, err := custodyClaimer(t, sc).BindCustodialEffect(ctx, f.claimBinding(f.operationID())); !errors.Is(err, store.ErrCustodyWriteSet) {
					t.Fatalf("bind after an ordinary write = %v, want ErrCustodyWriteSet", err)
				}
				return nil // swallowed on purpose
			})
			if !errors.Is(err, store.ErrCustodyWriteSet) {
				t.Fatalf("commit after a swallowed write-set refusal = %v", err)
			}
		})

		t.Run("audit append refuses", func(t *testing.T) {
			err := f.confined(func(sc store.Scope) error {
				if _, err := sc.Audit().Append(ctx, model.AuditDraft{
					Actor: custodyTestActor, ActorKind: custodyTestActorKd, Action: "agent.update",
				}); err != nil {
					return err
				}
				_, err := custodyClaimer(t, sc).BindCustodialEffect(ctx, f.claimBinding(f.operationID()))
				return err
			})
			if !errors.Is(err, store.ErrCustodyWriteSet) {
				t.Fatalf("bind after an audit append = %v, want ErrCustodyWriteSet", err)
			}
		})

		t.Run("engine authority touch is admitted", func(t *testing.T) {
			operation := f.operationID()
			// The claim row is read BEFORE the transaction opens. A View nested
			// inside a Mutate deadlocks on the supported single-connection SQLite
			// slice, and a fixture that only worked on PostgreSQL would be a
			// fixture that proves nothing about the engine we ship embedded.
			claim := f.claimRow()
			err := f.confined(func(sc store.Scope) error {
				// The bundle locker renews the claim's lease fact through the
				// engine's own locked-record touch, which is the ONE write the
				// custodial contract admits before a binding.
				locker, ok := sc.(store.AuthoritySnapshotBundleLocker)
				if !ok {
					t.Fatalf("confined scope %T exposes no bundle locker", sc)
				}
				deadline, perr := model.ParseTimestamp(claim.String(custodyClaimDeadlineColumn))
				if perr != nil {
					return perr
				}
				ref, rerr := store.NewLeaseFenceAuthorizationFactRef(
					custodyClaimKind, f.claimID, claim.Int(model.ColVersion),
					f.sid, custodyTestFence, deadline)
				if rerr != nil {
					return rerr
				}
				if err := locker.LockAuthoritySnapshotBundle(ctx, store.AuthoritySnapshotBundle{
					Facts: []store.AuthorizationFactRef{ref},
				}); err != nil {
					return err
				}
				b := f.claimBinding(operation)
				// The engine touch advanced the claim's version, so the observed
				// version travels with it.
				b.ClaimVersion = claim.Int(model.ColVersion) + 1
				h, err := custodyClaimer(t, sc).BindCustodialEffect(ctx, b)
				if err != nil {
					return err
				}
				res, err := h.Claim(ctx)
				if err != nil {
					return err
				}
				if res.Outcome != store.CustodialFreshAnchored {
					t.Fatalf("claim after an authority touch = %s", res.Outcome)
				}
				return nil
			})
			if err != nil {
				t.Fatalf("claim after an engine authority touch: %v", err)
			}
			if _, ok := f.journalRow(operation); !ok {
				t.Fatal("the claim did not commit")
			}
		})
	})
}

// TestCustodialHandleModeAndLifetimeLimits proves the per-handle ordering: a
// mode admits only its own operations, each runs once, and a handle used after
// its transaction is stale rather than issuing SQL on a finished transaction.
func TestCustodialHandleModeAndLifetimeLimits(t *testing.T) {
	f := newCustodyFixture(t, custodyEngines()[0])
	ctx := context.Background()

	t.Run("a lookup handle claims nothing", func(t *testing.T) {
		if err := f.confined(func(sc store.Scope) error {
			h, err := custodyClaimer(t, sc).BindCustodialEffect(ctx, f.lookupBinding(f.operationID()))
			if err != nil {
				return err
			}
			if _, err := h.Claim(ctx); !errors.Is(err, store.ErrCustodyHandleStale) {
				t.Fatalf("Claim on a lookup handle = %v", err)
			}
			if err := h.TouchClaim(ctx); !errors.Is(err, store.ErrCustodyHandleStale) {
				t.Fatalf("TouchClaim on a lookup handle = %v", err)
			}
			if _, err := h.Settle(ctx, store.CustodialSettlement{State: model.EvidenceOpCompleted}); !errors.Is(err, store.ErrCustodyHandleStale) {
				t.Fatalf("Settle on a lookup handle = %v", err)
			}
			return nil
		}); err != nil {
			t.Fatalf("mutate: %v", err)
		}
	})

	t.Run("a claim handle settles nothing and touches once", func(t *testing.T) {
		err := f.confined(func(sc store.Scope) error {
			h, err := custodyClaimer(t, sc).BindCustodialEffect(ctx, f.claimBinding(f.operationID()))
			if err != nil {
				return err
			}
			if _, err := h.Settle(ctx, store.CustodialSettlement{State: model.EvidenceOpCompleted}); !errors.Is(err, store.ErrCustodyHandleStale) {
				t.Fatalf("Settle on a claim handle = %v", err)
			}
			if err := h.TouchClaim(ctx); err != nil {
				return err
			}
			if err := h.TouchClaim(ctx); !errors.Is(err, store.ErrCustodyAlreadyClaimed) {
				t.Fatalf("second TouchClaim = %v", err)
			}
			return nil
		})
		if !errors.Is(err, store.ErrCustodyAlreadyClaimed) {
			t.Fatalf("commit after a swallowed second touch = %v", err)
		}
	})

	t.Run("a handle outlives nothing", func(t *testing.T) {
		var escaped store.CustodialEffectHandle
		if err := f.confined(func(sc store.Scope) error {
			h, err := custodyClaimer(t, sc).BindCustodialEffect(ctx, f.lookupBinding(f.operationID()))
			escaped = h
			return err
		}); err != nil {
			t.Fatalf("mutate: %v", err)
		}
		if _, _, err := escaped.Lookup(ctx); !errors.Is(err, store.ErrCustodyHandleStale) {
			t.Fatalf("Lookup after the transaction finished = %v", err)
		}
		if _, err := escaped.Claim(ctx); !errors.Is(err, store.ErrCustodyHandleStale) {
			t.Fatalf("Claim after the transaction finished = %v", err)
		}
	})

	t.Run("a lookup binding neither restricts nor seals", func(t *testing.T) {
		// A Lookup writes nothing, so it must not turn its transaction into a
		// custodial one: the caller's ordinary work still commits around it.
		runRef := "run-" + uniqueSuffix()
		if err := f.confined(func(sc store.Scope) error {
			h, err := custodyClaimer(t, sc).BindCustodialEffect(ctx, f.lookupBinding(f.operationID()))
			if err != nil {
				return err
			}
			if _, found, lerr := h.Lookup(ctx); lerr != nil || found {
				t.Fatalf("lookup of an unclaimed operation = %t, %v", found, lerr)
			}
			runs, err := sc.Ext(custodyRunKind)
			if err != nil {
				return err
			}
			_, err = runs.Create(ctx, model.Record{
				custodyRunRefColumn:     runRef,
				custodyRunLineageColumn: f.workspace.String(),
			})
			return err
		}); err != nil {
			t.Fatalf("an ordinary write after a lookup binding: %v", err)
		}
		// The write committed, which is the whole claim.
		if err := f.st.View(ctx, f.tenant, func(sc store.Scope) error {
			runs, err := sc.Ext(custodyRunKind)
			if err != nil {
				return err
			}
			rows, _, err := runs.List(ctx, model.Query{
				Filters: []model.Filter{{Column: custodyRunRefColumn, Op: model.OpEq, Value: runRef}},
				Limit:   1,
			})
			if err != nil {
				return err
			}
			if len(rows) != 1 {
				t.Fatalf("the ordinary write did not commit: %d rows", len(rows))
			}
			return nil
		}); err != nil {
			t.Fatalf("view: %v", err)
		}
	})

	t.Run("a poisoned view refuses even when the callback swallows it", func(t *testing.T) {
		// A View holds neither the directory nor the lineage writer, so the
		// scope's own custodial fault is the ONLY thing that can refuse there.
		err := f.st.View(ctx, f.tenant, func(raw store.Scope) error {
			sc, cerr := store.ConfineWorkspace(ctx, raw, f.workspace)
			if cerr != nil {
				return cerr
			}
			claimer := custodyClaimer(t, sc)
			if _, err := claimer.BindCustodialEffect(ctx, f.lookupBinding(f.operationID())); err != nil {
				return err
			}
			if _, err := claimer.BindCustodialEffect(ctx, f.lookupBinding(f.operationID())); !errors.Is(err, store.ErrCustodyAlreadyBound) {
				t.Fatalf("second binding in a View = %v", err)
			}
			return nil // swallowed on purpose
		})
		if !errors.Is(err, store.ErrCustodyAlreadyBound) {
			t.Fatalf("View after a swallowed custodial fault = %v", err)
		}
	})

	t.Run("a view admits only a lookup", func(t *testing.T) {
		err := f.st.View(ctx, f.tenant, func(raw store.Scope) error {
			sc, err := store.ConfineWorkspace(ctx, raw, f.workspace)
			if err != nil {
				return err
			}
			if _, err := custodyClaimer(t, sc).BindCustodialEffect(ctx, f.claimBinding(f.operationID())); !errors.Is(err, store.ErrReadOnly) {
				t.Fatalf("claim binding in a View = %v", err)
			}
			h, err := custodyClaimer(t, sc).BindCustodialEffect(ctx, f.lookupBinding(f.operationID()))
			if err != nil {
				return err
			}
			_, found, err := h.Lookup(ctx)
			if err != nil || found {
				t.Fatalf("lookup of an unclaimed operation = %t, %v", found, err)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("view: %v", err)
		}
	})
}

// ---- the fixed target read and the fixed claim qualification ----------

func (f *custodyFixture) runID() model.ID {
	f.t.Helper()
	var id model.ID
	err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		runs, err := sc.Ext(custodyRunKind)
		if err != nil {
			return err
		}
		rows, _, err := runs.List(context.Background(), model.Query{
			Filters: []model.Filter{{Column: custodyRunRefColumn, Op: model.OpEq, Value: f.runRef}},
			Limit:   1,
		})
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			return fmt.Errorf("run %s resolved %d rows", f.runRef, len(rows))
		}
		id = model.ID(rows[0].String(model.ColID))
		return nil
	})
	if err != nil {
		f.t.Fatalf("read run id: %v", err)
	}
	return id
}

func (f *custodyFixture) updateRow(kind model.Kind, id model.ID, mutate func(model.Record)) model.Record {
	f.t.Helper()
	ctx := context.Background()
	var out model.Record
	err := f.st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(kind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, id)
		if err != nil {
			return err
		}
		mutate(rec)
		out, err = repo.Update(ctx, rec)
		return err
	})
	if err != nil {
		f.t.Fatalf("update %s: %v", kind, err)
	}
	return out
}

// TestCustodialClaimQualificationIsFixedAndUnsubstitutable is T-X3: every
// disqualifying coordinate refuses, poisons and commits nothing — and a version
// that is accurate for ANOTHER claim row is one of them, because the caller
// names no row.
func TestCustodialClaimQualificationIsFixedAndUnsubstitutable(t *testing.T) {
	forEachCustodyEngine(t, func(t *testing.T, e custodyEngine) {
		ctx := context.Background()
		cases := []struct {
			name  string
			setup func(f *custodyFixture) store.CustodialEffectBinding
			want  error
		}{
			{name: "another row's accurate version", want: store.ErrCustodyClaimMismatch,
				setup: func(f *custodyFixture) store.CustodialEffectBinding {
					// A second, entirely valid claim row, advanced so its version is
					// a real integer that names a real row — just not this one.
					var sibling model.ID
					if err := f.st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
						claims, err := sc.Ext(custodyClaimKind)
						if err != nil {
							return err
						}
						now, err := sc.(store.TransactionClock).TransactionNow(ctx)
						if err != nil {
							return err
						}
						rec, err := claims.Create(ctx, model.Record{
							custodyClaimSubjectColumn:  "sid-sibling-" + uniqueSuffix(),
							custodyClaimHolderColumn:   custodyTestHolder,
							custodyClaimFenceColumn:    custodyTestFence,
							custodyClaimStateColumn:    custodyTestActive,
							custodyClaimDeadlineColumn: model.NewTimestamp(now.Time().Add(time.Hour)).String(),
						})
						sibling = model.ID(rec.String(model.ColID))
						return err
					}); err != nil {
						t.Fatalf("create sibling claim: %v", err)
					}
					var version int64
					for i := 0; i < 4; i++ {
						version = f.updateRow(custodyClaimKind, sibling, func(model.Record) {}).Int(model.ColVersion)
					}
					if version == f.claimVersion {
						t.Fatalf("sibling version %d must differ from the bound row's %d", version, f.claimVersion)
					}
					b := f.claimBinding(f.operationID())
					b.ClaimVersion = version
					return b
				}},
			{name: "stale observed version", want: store.ErrCustodyClaimMismatch,
				setup: func(f *custodyFixture) store.CustodialEffectBinding {
					b := f.claimBinding(f.operationID())
					f.updateRow(custodyClaimKind, f.claimID, func(model.Record) {})
					return b
				}},
			{name: "holder changed on the claim", want: store.ErrCustodyClaimMismatch,
				setup: func(f *custodyFixture) store.CustodialEffectBinding {
					b := f.claimBinding(f.operationID())
					rec := f.updateRow(custodyClaimKind, f.claimID, func(r model.Record) {
						r[custodyClaimHolderColumn] = "agent:takeover"
					})
					b.ClaimVersion = rec.Int(model.ColVersion)
					return b
				}},
			{name: "fence advanced by a takeover", want: store.ErrCustodyClaimMismatch,
				setup: func(f *custodyFixture) store.CustodialEffectBinding {
					b := f.claimBinding(f.operationID())
					rec := f.updateRow(custodyClaimKind, f.claimID, func(r model.Record) {
						r[custodyClaimFenceColumn] = custodyTestFence + 1
					})
					b.ClaimVersion = rec.Int(model.ColVersion)
					return b
				}},
			{name: "claim no longer active", want: store.ErrCustodyClaimMismatch,
				setup: func(f *custodyFixture) store.CustodialEffectBinding {
					b := f.claimBinding(f.operationID())
					rec := f.updateRow(custodyClaimKind, f.claimID, func(r model.Record) {
						r[custodyClaimStateColumn] = "expired"
					})
					b.ClaimVersion = rec.Int(model.ColVersion)
					return b
				}},
			{name: "lease already lapsed", want: store.ErrCustodyClaimMismatch,
				setup: func(f *custodyFixture) store.CustodialEffectBinding {
					b := f.claimBinding(f.operationID())
					rec := f.updateRow(custodyClaimKind, f.claimID, func(r model.Record) {
						r[custodyClaimDeadlineColumn] = model.NewTimestamp(time.Now().Add(-time.Hour)).String()
					})
					b.ClaimVersion = rec.Int(model.ColVersion)
					return b
				}},
			{name: "run carries no admission stamp", want: store.ErrCustodyClaimMismatch,
				setup: func(f *custodyFixture) store.CustodialEffectBinding {
					f.updateRow(custodyRunKind, f.runID(), func(r model.Record) {
						r[custodyRunClaimSubjectCol] = nil
					})
					return f.claimBinding(f.operationID())
				}},
			{name: "run points at an absent claim", want: store.ErrCustodyClaimMismatch,
				setup: func(f *custodyFixture) store.CustodialEffectBinding {
					f.updateRow(custodyRunKind, f.runID(), func(r model.Record) {
						r[custodyRunClaimSubjectCol] = "sid-absent-" + uniqueSuffix()
					})
					return f.claimBinding(f.operationID())
				}},
			{name: "launch generation superseded", want: store.ErrCustodyTargetGeneration,
				setup: func(f *custodyFixture) store.CustodialEffectBinding {
					f.updateRow(custodyRunKind, f.runID(), func(r model.Record) {
						r[custodyRunLaunchColumn] = model.NewID().String()
					})
					return f.claimBinding(f.operationID())
				}},
			{name: "run absent", want: store.ErrCustodyTargetConcealed,
				setup: func(f *custodyFixture) store.CustodialEffectBinding {
					b := f.claimBinding(f.operationID())
					b.RunRef = "run-absent-" + uniqueSuffix()
					return b
				}},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				f := newCustodyFixture(t, e)
				b := c.setup(f)
				before := f.auditCount()
				err := f.confined(func(sc store.Scope) error {
					_, bindErr := custodyClaimer(t, sc).BindCustodialEffect(ctx, b)
					if !errors.Is(bindErr, c.want) {
						t.Fatalf("bind = %v, want %v", bindErr, c.want)
					}
					return nil // swallowed on purpose
				})
				// Concealment is a clean refusal with nothing staged; the two
				// poisoned classes must also refuse the COMMIT.
				if c.want == store.ErrCustodyTargetConcealed {
					if err != nil {
						t.Fatalf("concealed bind must leave a committable transaction, got %v", err)
					}
				} else if !errors.Is(err, c.want) {
					t.Fatalf("commit after a swallowed %v = %v", c.want, err)
				}
				if _, ok := f.journalRow(b.OperationID); ok {
					t.Fatal("a refused qualification wrote a journal row")
				}
				if after := f.auditCount(); after != before {
					t.Fatalf("ledger grew from %d to %d on a refused qualification", before, after)
				}
			})
		}
	})
}

// TestCustodialTouchClaimPreservesEveryOtherValue proves the touch assigns
// exactly updated_at and version, by construction of its SQL rather than by
// comparing a caller-supplied record.
func TestCustodialTouchClaimPreservesEveryOtherValue(t *testing.T) {
	forEachCustodyEngine(t, func(t *testing.T, e custodyEngine) {
		f := newCustodyFixture(t, e)
		ctx := context.Background()
		before := f.claimRow()
		if err := f.confined(func(sc store.Scope) error {
			h, err := custodyClaimer(t, sc).BindCustodialEffect(ctx, f.claimBinding(f.operationID()))
			if err != nil {
				return err
			}
			return h.TouchClaim(ctx)
		}); err != nil {
			t.Fatalf("touch: %v", err)
		}
		after := f.claimRow()
		if after.Int(model.ColVersion) != before.Int(model.ColVersion)+1 {
			t.Fatalf("version %d -> %d, want exactly one advance",
				before.Int(model.ColVersion), after.Int(model.ColVersion))
		}
		for _, column := range []string{
			model.ColID, model.ColTenantID, model.ColCreatedAt,
			custodyClaimSubjectColumn, custodyClaimHolderColumn,
			custodyClaimStateColumn, custodyClaimDeadlineColumn,
		} {
			if before.String(column) != after.String(column) {
				t.Fatalf("column %q changed: %q -> %q", column, before.String(column), after.String(column))
			}
		}
		if before.Int(custodyClaimFenceColumn) != after.Int(custodyClaimFenceColumn) {
			t.Fatal("the touch changed the fence")
		}
	})
}

// TestCustodialRestrictedScopeRefusesOrdinaryWrites proves what a bound
// transaction may still do: read, and nothing else. It also records the fact
// that makes the qualification safe — a workspace-confined scope cannot reach
// the claim relation generically AT ALL, because that relation declares no
// lineage, so the engine's internal read from the visible run is the only route
// to it.
func TestCustodialRestrictedScopeRefusesOrdinaryWrites(t *testing.T) {
	forEachCustodyEngine(t, func(t *testing.T, e custodyEngine) {
		f := newCustodyFixture(t, e)
		ctx := context.Background()
		operation := f.operationID()
		runID := f.runID()
		err := f.confined(func(sc store.Scope) error {
			if _, claimErr := sc.Ext(custodyClaimKind); !errors.Is(claimErr, store.ErrWorkspaceLineageRequired) {
				t.Fatalf("confined Ext(%s) = %v, want ErrWorkspaceLineageRequired", custodyClaimKind, claimErr)
			}
			runs, err := sc.Ext(custodyRunKind)
			if err != nil {
				return err
			}
			// Before the binding an ordinary read is free.
			run, err := runs.Get(ctx, runID)
			if err != nil {
				return err
			}
			if _, err := custodyClaimer(t, sc).BindCustodialEffect(ctx, f.claimBinding(operation)); err != nil {
				return err
			}
			// Reads remain allowed.
			if _, err := runs.Get(ctx, runID); err != nil {
				t.Fatalf("read inside a bound transaction = %v", err)
			}
			// Every consumer write does not.
			if _, err := runs.Update(ctx, run); !errors.Is(err, store.ErrCustodyWriteSet) {
				t.Fatalf("ordinary update inside a bound transaction = %v", err)
			}
			if _, err := sc.Audit().Append(ctx, model.AuditDraft{
				Actor: custodyTestActor, ActorKind: custodyTestActorKd, Action: "agent.update",
			}); !errors.Is(err, store.ErrCustodyWriteSet) {
				t.Fatalf("audit append inside a bound transaction = %v", err)
			}
			return nil // swallowed on purpose
		})
		if !errors.Is(err, store.ErrCustodyWriteSet) {
			t.Fatalf("commit = %v, want the write-set refusal", err)
		}
		if _, ok := f.journalRow(operation); ok {
			t.Fatal("a refused transaction wrote a journal row")
		}
	})
}

// ---- the custodial claim order ----------------------------------------

// reopenWithSpool reopens the SAME database with a different audit-spool
// policy. The policy is a store-wide Config decision, so this is how a real
// deployment reaches the degrade and block behaviors.
func (f *custodyFixture) reopenWithSpool(maxBytes int64, mode store.AuditSpoolMode) {
	f.t.Helper()
	if err := f.st.Close(); err != nil {
		f.t.Fatalf("close before reopen: %v", err)
	}
	cfg := f.cfg
	cfg.AuditSpoolMaxBytes = maxBytes
	cfg.AuditSpoolOnFull = mode
	f.st = openCustodyStore(f.t, cfg, registerCustodyRelation)
}

func (f *custodyFixture) auditEvents(action string) []model.AuditEvent {
	f.t.Helper()
	var out []model.AuditEvent
	err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 1, func(ev model.AuditEvent) error {
			if ev.Action == action {
				out = append(out, ev)
			}
			return nil
		})
	})
	if err != nil {
		f.t.Fatalf("walk ledger: %v", err)
	}
	return out
}

// claimOnce runs one complete custodial claim through the real confined scope.
func (f *custodyFixture) claimOnce(b store.CustodialEffectBinding) (store.CustodialClaimResult, error) {
	ctx := context.Background()
	var res store.CustodialClaimResult
	err := f.confined(func(sc store.Scope) error {
		h, err := custodyClaimer(f.t, sc).BindCustodialEffect(ctx, b)
		if err != nil {
			return err
		}
		if err := h.TouchClaim(ctx); err != nil {
			return err
		}
		res, err = h.Claim(ctx)
		return err
	})
	return res, err
}

// TestCustodialClaimFreshAnchorsOneRowAndOneEvent is the positive path: one
// journal row, claimed, whose claim reference IS the hash of the one claim
// event appended in the same transaction, under the stamped epoch.
func TestCustodialClaimFreshAnchorsOneRowAndOneEvent(t *testing.T) {
	forEachCustodyEngine(t, func(t *testing.T, e custodyEngine) {
		f := newCustodyFixture(t, e)
		operation := f.operationID()
		res, err := f.claimOnce(f.claimBinding(operation))
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if res.Outcome != store.CustodialFreshAnchored || !res.Outcome.Fresh() {
			t.Fatalf("outcome = %s", res.Outcome)
		}
		op, ok := f.journalRow(operation)
		if !ok {
			t.Fatal("the claim did not commit a row")
		}
		if op.State != model.EvidenceOpClaimed {
			t.Fatalf("state = %s, want claimed", op.State)
		}
		if op.Surface != store.ManagedStopSurface || op.Action != store.ManagedStopAction {
			t.Fatalf("surface/action = %s/%s", op.Surface, op.Action)
		}
		if !strings.HasPrefix(op.EffectDigest, "cev1:") {
			t.Fatalf("effect digest %q is not an engine digest", op.EffectDigest)
		}
		if op.OutcomeEvidenceRef != "" || op.ResultDigest != "" || op.DispatchRef != "" {
			t.Fatalf("a claimed row carries settlement references: %+v", op)
		}
		fenced, ferr := f.st.Leader().(store.EpochFencer).FencedEpoch(context.Background())
		if ferr != nil {
			t.Fatalf("read the durable fence: %v", ferr)
		}
		if op.LeaderEpoch != fenced {
			t.Fatalf("leader epoch = %d, want the engine's own fenced %d", op.LeaderEpoch, fenced)
		}
		events := f.auditEvents(store.ManagedStopAction + ".claim")
		if len(events) != 1 {
			t.Fatalf("%d claim events, want exactly one", len(events))
		}
		if op.ClaimEvidenceRef != hex.EncodeToString(events[0].Hash) {
			t.Fatalf("claim ref %q is not the appended event's hash", op.ClaimEvidenceRef)
		}
		if events[0].Actor != custodyTestActor || events[0].ActorKind != custodyTestActorKd {
			t.Fatalf("claim event attribution = %s/%s", events[0].Actor, events[0].ActorKind)
		}
		// The touch advanced the claim row exactly once.
		if got := f.claimRow().Int(model.ColVersion); got != f.claimVersion+1 {
			t.Fatalf("claim version = %d, want %d", got, f.claimVersion+1)
		}
	})
}

// TestCustodialClaimReplaysWithoutWritingAgain proves a repeat of the same
// identity and the same semantics returns the recorded decision and appends
// nothing.
func TestCustodialClaimReplaysWithoutWritingAgain(t *testing.T) {
	forEachCustodyEngine(t, func(t *testing.T, e custodyEngine) {
		f := newCustodyFixture(t, e)
		operation := f.operationID()
		if _, err := f.claimOnce(f.claimBinding(operation)); err != nil {
			t.Fatalf("first claim: %v", err)
		}
		first, _ := f.journalRow(operation)
		beforeEvents := f.auditCount()
		beforeClaim := f.claimRow().Int(model.ColVersion)

		b := f.claimBinding(operation)
		b.ClaimVersion = beforeClaim
		ctx := context.Background()
		var res store.CustodialClaimResult
		if err := f.confined(func(sc store.Scope) error {
			h, err := custodyClaimer(t, sc).BindCustodialEffect(ctx, b)
			if err != nil {
				return err
			}
			res, err = h.Claim(ctx)
			return err
		}); err != nil {
			t.Fatalf("replay: %v", err)
		}
		if res.Outcome != store.CustodialReplayClaimed || res.Outcome.Fresh() {
			t.Fatalf("replay outcome = %s", res.Outcome)
		}
		if res.Op.ID != first.ID || res.Op.Version != first.Version {
			t.Fatalf("replay returned another row: %+v vs %+v", res.Op, first)
		}
		if after := f.auditCount(); after != beforeEvents {
			t.Fatalf("replay appended %d events", after-beforeEvents)
		}
		if after := f.claimRow().Int(model.ColVersion); after != beforeClaim {
			t.Fatalf("replay touched the claim row: %d -> %d", beforeClaim, after)
		}
	})
}

// TestCustodialClaimRebindsOnAChangedSemanticDigest proves the single-use
// identity is bound to ONE effect: the same operation id under different
// semantics refuses with no write.
func TestCustodialClaimRebindsOnAChangedSemanticDigest(t *testing.T) {
	forEachCustodyEngine(t, func(t *testing.T, e custodyEngine) {
		f := newCustodyFixture(t, e)
		operation := f.operationID()
		if _, err := f.claimOnce(f.claimBinding(operation)); err != nil {
			t.Fatalf("first claim: %v", err)
		}
		before, _ := f.journalRow(operation)
		beforeEvents := f.auditCount()

		b := f.claimBinding(operation)
		b.SemanticDigest = "msv1:" + strings.Repeat("b", 64)
		b.ClaimVersion = f.claimRow().Int(model.ColVersion)
		ctx := context.Background()
		err := f.confined(func(sc store.Scope) error {
			h, herr := custodyClaimer(t, sc).BindCustodialEffect(ctx, b)
			if herr != nil {
				return herr
			}
			if _, cerr := h.Claim(ctx); !errors.Is(cerr, store.ErrEvidenceRebind) {
				t.Fatalf("claim under changed semantics = %v", cerr)
			}
			return nil // swallowed on purpose
		})
		if !errors.Is(err, store.ErrEvidenceRebind) {
			t.Fatalf("commit after a swallowed rebind = %v", err)
		}
		after, _ := f.journalRow(operation)
		if after.Version != before.Version || after.State != before.State {
			t.Fatalf("a rebind changed the row: %+v -> %+v", before, after)
		}
		if got := f.auditCount(); got != beforeEvents {
			t.Fatalf("a rebind appended %d events", got-beforeEvents)
		}
	})
}

// TestCustodialClaimSeq0BurnsTheIdentityAndCommits is the reason the custodial
// order inverts the generic one: a degrade-mode drop leaves a DURABLE refused
// row, so the identity cannot be presented again as if it had never been used.
func TestCustodialClaimSeq0BurnsTheIdentityAndCommits(t *testing.T) {
	forEachCustodyEngine(t, func(t *testing.T, e custodyEngine) {
		f := newCustodyFixture(t, e)
		operation := f.operationID()
		f.reopenWithSpool(1, store.AuditSpoolDegrade)
		res, err := f.claimOnce(f.claimBinding(operation))
		if err != nil {
			t.Fatalf("degraded claim: %v", err)
		}
		if res.Outcome != store.CustodialRefusedFresh || !res.Outcome.Fresh() {
			t.Fatalf("outcome = %s, want refused_fresh", res.Outcome)
		}
		op, ok := f.journalRow(operation)
		if !ok {
			t.Fatal("the degraded claim committed no row: the identity was not burned")
		}
		if op.State != model.EvidenceOpRefused {
			t.Fatalf("state = %s, want refused", op.State)
		}
		if op.ClaimEvidenceRef != "" || op.OutcomeEvidenceRef != "" ||
			op.ResultDigest != "" || op.DispatchRef != "" {
			t.Fatalf("a refused row carries an anchor: %+v", op)
		}
		if len(f.auditEvents(store.ManagedStopAction+".claim")) != 0 {
			t.Fatal("the degraded claim appended a claim event")
		}
		// A replay answers from the burned row and appends nothing.
		before := f.auditCount()
		b := f.claimBinding(operation)
		b.ClaimVersion = f.claimRow().Int(model.ColVersion)
		ctx := context.Background()
		var replay store.CustodialClaimResult
		if err := f.confined(func(sc store.Scope) error {
			h, err := custodyClaimer(t, sc).BindCustodialEffect(ctx, b)
			if err != nil {
				return err
			}
			replay, err = h.Claim(ctx)
			return err
		}); err != nil {
			t.Fatalf("replay of a refused row: %v", err)
		}
		if replay.Outcome != store.CustodialReplayRefused {
			t.Fatalf("replay outcome = %s, want replay_refused", replay.Outcome)
		}
		if after := f.auditCount(); after != before {
			t.Fatalf("the replay appended %d events", after-before)
		}
	})
}

// TestCustodialClaimWithoutABudgetCannotDrop is the positive control for the
// case above: with the budget disabled the append always persists, so a
// refused_fresh outcome can only come from the degrade policy.
func TestCustodialClaimWithoutABudgetCannotDrop(t *testing.T) {
	f := newCustodyFixture(t, custodyEngines()[0])
	operation := f.operationID()
	f.reopenWithSpool(0, store.AuditSpoolDegrade)
	res, err := f.claimOnce(f.claimBinding(operation))
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if res.Outcome != store.CustodialFreshAnchored {
		t.Fatalf("outcome = %s, want fresh_anchored with the budget disabled", res.Outcome)
	}
}

// TestCustodialClaimAppendFaultPoisonsASwallowingCallback is the block-mode
// refusal: the append fails, the callback swallows it, and nothing commits.
func TestCustodialClaimAppendFaultPoisonsASwallowingCallback(t *testing.T) {
	forEachCustodyEngine(t, func(t *testing.T, e custodyEngine) {
		f := newCustodyFixture(t, e)
		operation := f.operationID()
		f.reopenWithSpool(1, store.AuditSpoolBlock)
		ctx := context.Background()
		err := f.confined(func(sc store.Scope) error {
			h, herr := custodyClaimer(t, sc).BindCustodialEffect(ctx, f.claimBinding(operation))
			if herr != nil {
				return herr
			}
			if _, cerr := h.Claim(ctx); !errors.Is(cerr, store.ErrAuditSpoolFull) {
				t.Fatalf("claim under a full spool = %v", cerr)
			}
			return nil // swallowed on purpose
		})
		if !errors.Is(err, store.ErrAuditSpoolFull) {
			t.Fatalf("commit after a swallowed append fault = %v", err)
		}
		if _, ok := f.journalRow(operation); ok {
			t.Fatal("the provisional row committed after an append fault")
		}
	})
}

// TestCustodialClaimPreCommitEpochChangeRollsBack proves the stamped fence is
// re-verified before the callback returns: a leader that lost its session
// mid-transaction commits nothing.
func TestCustodialClaimPreCommitEpochChangeRollsBack(t *testing.T) {
	forEachCustodyEngine(t, func(t *testing.T, e custodyEngine) {
		f := newCustodyFixture(t, e)
		operation := f.operationID()
		// Bind stamps the first value; the pre-commit re-verification takes the
		// second.
		f.st.(*sqlStore).elector = &custodyScriptedElector{epochs: []uint64{4, 5}}
		ctx := context.Background()
		err := f.confined(func(sc store.Scope) error {
			h, herr := custodyClaimer(t, sc).BindCustodialEffect(ctx, f.claimBinding(operation))
			if herr != nil {
				return herr
			}
			if _, cerr := h.Claim(ctx); !errors.Is(cerr, store.ErrCustodyEpochChanged) {
				t.Fatalf("claim across an epoch change = %v", cerr)
			}
			return nil // swallowed on purpose
		})
		if !errors.Is(err, store.ErrCustodyEpochChanged) {
			t.Fatalf("commit after a swallowed epoch change = %v", err)
		}
		if _, ok := f.journalRow(operation); ok {
			t.Fatal("a claim committed under a superseded epoch")
		}
	})
}

// TestCustodialBindRefusesAnUnavailableFence proves a fence that cannot be
// taken right now refuses cleanly, with nothing staged and nothing poisoned.
func TestCustodialBindRefusesAnUnavailableFence(t *testing.T) {
	f := newCustodyFixture(t, custodyEngines()[0])
	f.st.(*sqlStore).elector = &custodyScriptedElector{
		epochs: []uint64{1}, err: errors.New("lock session lost"),
	}
	ctx := context.Background()
	err := f.confined(func(sc store.Scope) error {
		_, bindErr := custodyClaimer(t, sc).BindCustodialEffect(ctx, f.claimBinding(f.operationID()))
		if !errors.Is(bindErr, store.ErrCustodyLeaderUnavailable) {
			t.Fatalf("bind with an unavailable fence = %v", bindErr)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("an unavailable fence must leave a committable transaction, got %v", err)
	}
}

// TestCustodialScopeSealsEveryWriteAfterTheDecision proves the seal: once the
// operation is decided the callback's only permitted action is to return.
func TestCustodialScopeSealsEveryWriteAfterTheDecision(t *testing.T) {
	forEachCustodyEngine(t, func(t *testing.T, e custodyEngine) {
		f := newCustodyFixture(t, e)
		ctx := context.Background()
		operation := f.operationID()
		runID := f.runID()
		err := f.st.Mutate(ctx, f.tenant, func(raw store.Scope) error {
			sc, err := store.ConfineWorkspace(ctx, raw, f.workspace)
			if err != nil {
				return err
			}
			// The confined journal is denied by confinement, before and after the
			// seal: EvidenceOperations() stays the denied implementation on every
			// confined base.
			if _, err := sc.EvidenceOperations().Get(ctx, operation); !errors.Is(err, store.ErrWorkspaceLineageRequired) {
				t.Fatalf("confined journal read = %v, want ErrWorkspaceLineageRequired", err)
			}
			runs, err := sc.Ext(custodyRunKind)
			if err != nil {
				return err
			}
			run, err := runs.Get(ctx, runID)
			if err != nil {
				return err
			}
			h, err := custodyClaimer(t, sc).BindCustodialEffect(ctx, f.claimBinding(operation))
			if err != nil {
				return err
			}
			if _, err := h.Claim(ctx); err != nil {
				return err
			}
			// Reads still work; every write, lock, append and journal call does not.
			if _, err := runs.Get(ctx, runID); err != nil {
				t.Fatalf("read after the seal = %v", err)
			}
			sealed := []struct {
				name string
				call func() error
			}{
				{"generic update", func() error { _, err := runs.Update(ctx, run); return err }},
				{"generic create", func() error {
					_, err := runs.Create(ctx, model.Record{
						custodyRunRefColumn:     "run-" + uniqueSuffix(),
						custodyRunLineageColumn: f.workspace.String(),
					})
					return err
				}},
				{"generic delete", func() error { return runs.Delete(ctx, runID) }},
				{"raw policy state", func() error {
					// A hand-built statement on a typed repository. The CONFINED
					// policy repository refuses by confinement (policies carry no
					// lineage), so the seal is exercised on the raw scope, where it
					// is the only thing that can refuse.
					_, err := raw.Policies().(store.PolicyStateWriter).SetPolicyEnabled(
						ctx, model.NewID(), "core.policy", 1, true)
					return err
				}},
				{"audit append", func() error {
					_, err := sc.Audit().Append(ctx, model.AuditDraft{
						Actor: custodyTestActor, ActorKind: custodyTestActorKd, Action: "agent.update",
					})
					return err
				}},
				{"raw evidence claim", func() error {
					// The CONFINED journal is denied by confinement itself (it carries
					// no workspace lineage), which is asserted separately below. The
					// seal must also stop the journal on the RAW scope the confinement
					// wraps, so that is the one exercised here.
					_, err := raw.EvidenceOperations().Claim(ctx, store.EvidenceClaim{
						OperationID: "mcp.gateway:" + uniqueSuffix(), EffectDigest: "d",
						Surface: "mcp.gateway", Action: "tool.call",
						Actor: custodyTestActor, ActorKind: custodyTestActorKd,
					})
					return err
				}},
				{"raw audit append", func() error {
					_, err := raw.Audit().Append(ctx, model.AuditDraft{
						Actor: custodyTestActor, ActorKind: custodyTestActorKd, Action: "agent.update",
					})
					return err
				}},
				{"transaction lock", func() error {
					return sc.(store.TransactionLocker).LockTransaction(ctx, "custody-seal")
				}},
				{"a second claim on the handle", func() error { _, err := h.Claim(ctx); return err }},
				{"a touch after the claim", func() error { return h.TouchClaim(ctx) }},
			}
			// The sites are COUNTED and named in the receipt, so "the seal covers
			// every write path" is an executed observation rather than a claim a
			// reader has to take from the source.
			exercised := make([]string, 0, len(sealed))
			for _, c := range sealed {
				callErr := c.call()
				if !errors.Is(callErr, store.ErrCustodySealed) &&
					!errors.Is(callErr, store.ErrCustodyAlreadyClaimed) {
					t.Fatalf("%s after the seal = %v, want a sealed refusal", c.name, callErr)
				}
				exercised = append(exercised, c.name)
			}
			if len(exercised) != len(sealed) || len(sealed) != 10 {
				t.Fatalf("%d of %d sealed call sites exercised, want all 10",
					len(exercised), len(sealed))
			}
			t.Logf("sealed call sites refused: %s", strings.Join(exercised, ", "))
			return nil // swallowed on purpose
		})
		if err == nil {
			t.Fatal("a transaction that wrote after the seal committed")
		}
		if !errors.Is(err, store.ErrCustodySealed) && !errors.Is(err, store.ErrCustodyAlreadyClaimed) {
			t.Fatalf("commit after the seal = %v", err)
		}
		if _, ok := f.journalRow(operation); ok {
			t.Fatal("a sealed-violation transaction committed its journal row")
		}
	})
}

// TestCustodialEpilogueFailureIsNotACommittedDecision is T-X4: the mandatory
// directory and lineage epilogues still run after the seal, and a failure of
// either returns before Commit — a known rollback with no dispatch.
func TestCustodialEpilogueFailureIsNotACommittedDecision(t *testing.T) {
	forEachCustodyEngine(t, func(t *testing.T, e custodyEngine) {
		for _, epilogue := range []string{epilogueDirectory, epilogueLineage} {
			t.Run(epilogue, func(t *testing.T) {
				f := newCustodyFixture(t, e)
				operation := f.operationID()
				fault := errors.New(epilogue + " epilogue faulted")
				f.st.(*sqlStore).custodyEpilogueTestHook = func(name string) error {
					if name == epilogue {
						return fault
					}
					return nil
				}
				res, err := f.claimOnce(f.claimBinding(operation))
				if !errors.Is(err, fault) {
					t.Fatalf("commit with a faulting %s epilogue = %v", epilogue, err)
				}
				if res.Outcome == store.CustodialFreshAnchored {
					// The in-transaction outcome is real; what must not happen is
					// that it commits.
					if _, ok := f.journalRow(operation); ok {
						t.Fatal("the decision committed despite a failed epilogue")
					}
				}
				if _, ok := f.journalRow(operation); ok {
					t.Fatal("the decision committed despite a failed epilogue")
				}
				if len(f.auditEvents(store.ManagedStopAction+".claim")) != 0 {
					t.Fatal("the claim event committed despite a failed epilogue")
				}
			})
		}
	})
}

// ---- competing claims -------------------------------------------------

// TestSameTenantMutationsSerializeAroundACustodialClaim measures WHY the
// custodial insert conflict is not reachable from two ordinary transactions on
// one tenant. Every Mutate arms the lineage writer for its tenant before the
// callback runs, and that gate is exclusive per tenant — the SQLite single
// writer on one engine, a transaction-scoped advisory lock on the other. The
// second transaction therefore cannot begin until the first commits.
//
// It is measured rather than asserted from the source, because the whole
// concurrency story of the custodial claim rests on it: with this gate the
// second claim always finds the committed winner and REPLAYS, and the
// unique-index branch is a backstop for writers outside the protocol.
func TestSameTenantMutationsSerializeAroundACustodialClaim(t *testing.T) {
	forEachCustodyEngine(t, func(t *testing.T, e custodyEngine) {
		f := newCustodyFixture(t, e)
		ctx := context.Background()
		inside := make(chan struct{})
		release := make(chan struct{})
		first := make(chan error, 1)
		go func() {
			first <- f.st.Mutate(ctx, f.tenant, func(store.Scope) error {
				close(inside)
				<-release
				return nil
			})
		}()
		<-inside
		second := make(chan error, 1)
		go func() {
			second <- f.st.Mutate(ctx, f.tenant, func(store.Scope) error { return nil })
		}()
		select {
		case err := <-second:
			t.Fatalf("a second same-tenant transaction ran while the first held its gate (err=%v)", err)
		case <-time.After(750 * time.Millisecond):
			// Blocked, which is the measurement.
		}
		close(release)
		if err := <-first; err != nil {
			t.Fatalf("first transaction: %v", err)
		}
		select {
		case err := <-second:
			if err != nil {
				t.Fatalf("second transaction after the gate was released: %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("the second transaction never completed after the gate was released")
		}
	})
}

// TestCustodialClaimCompetingSameIdentityReplays drives two concurrent claims
// of the SAME identity through the real confined scope. Because of the gate
// measured above, exactly one is fresh and the other replays — on both engines
// — and exactly one claim event reaches the ledger.
func TestCustodialClaimCompetingSameIdentityReplays(t *testing.T) {
	forEachCustodyEngine(t, func(t *testing.T, e custodyEngine) {
		f := newCustodyFixture(t, e)
		ctx := context.Background()
		operation := f.operationID()
		// No TouchClaim here: the observed claim version must stay valid for both
		// competitors, and the touch is what would advance it.
		const competitors = 4
		results := make([]error, competitors)
		outcomes := make([]store.CustodialClaimResult, competitors)
		var wg sync.WaitGroup
		wg.Add(competitors)
		for i := range results {
			go func(i int) {
				defer wg.Done()
				results[i] = f.confined(func(sc store.Scope) error {
					h, err := custodyClaimer(t, sc).BindCustodialEffect(ctx, f.claimBinding(operation))
					if err != nil {
						return err
					}
					outcomes[i], err = h.Claim(ctx)
					return err
				})
			}(i)
		}
		wg.Wait()

		fresh, replays := 0, 0
		for i, err := range results {
			if err != nil {
				t.Fatalf("competitor %d = %v", i, err)
			}
			switch outcomes[i].Outcome {
			case store.CustodialFreshAnchored:
				fresh++
			case store.CustodialReplayClaimed:
				replays++
			default:
				t.Fatalf("competitor %d outcome = %s", i, outcomes[i].Outcome)
			}
		}
		if fresh != 1 || replays != competitors-1 {
			t.Fatalf("%d fresh and %d replays over %d competitors", fresh, replays, competitors)
		}
		if n := len(f.auditEvents(store.ManagedStopAction + ".claim")); n != 1 {
			t.Fatalf("%d claim events committed, want exactly one", n)
		}
	})
}

// TestCustodialClaimLosesToAnOutOfBandInsert is the unique-index backstop, and
// it is measured through the one writer that can actually reach it: a
// connection OUTSIDE the transaction protocol. That residual is named in the
// construction — out-of-band SQL is not prevented, it is DETECTED — and this is
// its detection: the provisional insert loses, the transaction is poisoned, and
// no ledger sequence was burned, because the insert precedes the append.
//
// SQLite has no such writer while a custodial claim is open: its single writer
// is held by that very transaction. The case reports NOT APPLICABLE there
// rather than passing vacuously.
func TestCustodialClaimLosesToAnOutOfBandInsert(t *testing.T) {
	forEachCustodyEngine(t, func(t *testing.T, e custodyEngine) {
		f := newCustodyFixture(t, e)
		if f.outOfBand == "" {
			t.Skipf("not applicable on %s: a second writer cannot exist while the claim transaction holds the single writer", f.cfg.Engine)
		}
		ctx := context.Background()
		operation := f.operationID()
		beforeEvents := f.auditCount()

		outOfBand, err := sql.Open("pgx", f.outOfBand)
		if err != nil {
			t.Fatalf("open the out-of-band connection: %v", err)
		}
		defer func() { _ = outOfBand.Close() }()

		var planted bool
		f.st.(*sqlStore).evidenceClaimAfterMissTestHook = func(_ context.Context, id string) error {
			if id != operation || planted {
				return nil
			}
			planted = true
			now := model.NewTimestamp(time.Now()).String()
			_, err := outOfBand.ExecContext(ctx,
				`INSERT INTO public.evidence_operations
                 (id, tenant_id, created_at, updated_at, version, operation_id, effect_digest,
                  surface, action, state, claim_evidence_ref, outcome_evidence_ref,
                  result_digest, dispatch_ref, leader_epoch)
                 VALUES ($1,$2,$3,$4,1,$5,$6,$7,$8,'refused','',NULL,NULL,NULL,0)`,
				model.NewID().String(), f.tenant.String(), now, now, operation,
				"cev1:out-of-band", store.ManagedStopSurface, store.ManagedStopAction)
			return err
		}

		err = f.confined(func(sc store.Scope) error {
			h, herr := custodyClaimer(t, sc).BindCustodialEffect(ctx, f.claimBinding(operation))
			if herr != nil {
				return herr
			}
			if _, cerr := h.Claim(ctx); !errors.Is(cerr, store.ErrEvidenceRaced) {
				t.Fatalf("claim against a planted row = %v, want ErrEvidenceRaced", cerr)
			}
			return nil // swallowed on purpose
		})
		if !errors.Is(err, store.ErrEvidenceRaced) {
			t.Fatalf("commit after a swallowed race = %v", err)
		}
		if !planted {
			t.Fatal("the out-of-band row was never planted: the case measured nothing")
		}
		if after := f.auditCount(); after != beforeEvents {
			t.Fatalf("the losing claim burned %d ledger sequences", after-beforeEvents)
		}
		// The planted row is still there, and it is deny-closed: a custodial
		// replay sees a different engine digest and refuses to present it.
		op, ok := f.journalRow(operation)
		if !ok || op.EffectDigest != "cev1:out-of-band" {
			t.Fatalf("planted row = %+v, ok=%t", op, ok)
		}
		if _, rerr := f.claimOnce(f.claimBinding(operation)); !errors.Is(rerr, store.ErrEvidenceRebind) {
			t.Fatalf("replay over a planted row = %v, want a rebind refusal", rerr)
		}
	})
}

// ---- settlement -------------------------------------------------------

func (f *custodyFixture) settle(
	b store.CustodialEffectBinding, s store.CustodialSettlement,
) (store.CustodialSettleResult, error) {
	ctx := context.Background()
	var res store.CustodialSettleResult
	err := f.confined(func(sc store.Scope) error {
		h, err := custodyClaimer(f.t, sc).BindCustodialEffect(ctx, b)
		if err != nil {
			return err
		}
		res, err = h.Settle(ctx, s)
		return err
	})
	return res, err
}

// TestCustodialSettlementLifecycle walks the settle contract on both engines:
// the fresh settlement, the idempotent re-settle, the divergent outcome, a
// refused row, a non-terminal request and an absent row.
func TestCustodialSettlementLifecycle(t *testing.T) {
	forEachCustodyEngine(t, func(t *testing.T, e custodyEngine) {
		f := newCustodyFixture(t, e)
		operation := f.operationID()
		if _, err := f.claimOnce(f.claimBinding(operation)); err != nil {
			t.Fatalf("claim: %v", err)
		}
		outcome := store.CustodialSettlement{
			State:        model.EvidenceOpCompleted,
			ResultDigest: "msr1:" + strings.Repeat("c", 64),
			DispatchRef:  "launch:" + f.launch.String(),
		}
		res, err := f.settle(f.settleBinding(operation), outcome)
		if err != nil {
			t.Fatalf("settle: %v", err)
		}
		if !res.Fresh || res.Dropped || res.Op.State != model.EvidenceOpCompleted {
			t.Fatalf("settle result = %+v", res)
		}
		events := f.auditEvents(store.ManagedStopAction + ".settle")
		if len(events) != 1 {
			t.Fatalf("%d settle events, want one", len(events))
		}
		committed, _ := f.journalRow(operation)
		if committed.OutcomeEvidenceRef != hex.EncodeToString(events[0].Hash) {
			t.Fatalf("outcome ref %q is not the settle event's hash", committed.OutcomeEvidenceRef)
		}
		if committed.ResultDigest != outcome.ResultDigest || committed.DispatchRef != outcome.DispatchRef {
			t.Fatalf("settled row = %+v", committed)
		}

		t.Run("exact re-settle replays", func(t *testing.T) {
			before := f.auditCount()
			again, err := f.settle(f.settleBinding(operation), outcome)
			if err != nil {
				t.Fatalf("re-settle: %v", err)
			}
			if again.Fresh || again.Op.Version != committed.Version {
				t.Fatalf("re-settle = %+v", again)
			}
			if after := f.auditCount(); after != before {
				t.Fatalf("the re-settle appended %d events", after-before)
			}
		})

		t.Run("a divergent outcome refuses", func(t *testing.T) {
			divergent := outcome
			divergent.DispatchRef = "launch:" + model.NewID().String()
			if _, err := f.settle(f.settleBinding(operation), divergent); !errors.Is(err, store.ErrEvidenceIntegrity) {
				t.Fatalf("divergent settle = %v", err)
			}
		})

		t.Run("a non-terminal state refuses", func(t *testing.T) {
			if _, err := f.settle(f.settleBinding(operation), store.CustodialSettlement{
				State: model.EvidenceOpRefused,
			}); !errors.Is(err, store.ErrEvidenceInvalid) {
				t.Fatalf("settle refused = %v", err)
			}
			if _, err := f.settle(f.settleBinding(operation), store.CustodialSettlement{
				State: model.EvidenceOpClaimed,
			}); !errors.Is(err, store.ErrEvidenceInvalid) {
				t.Fatalf("settle claimed = %v", err)
			}
		})

		t.Run("an absent operation refuses", func(t *testing.T) {
			if _, err := f.settle(f.settleBinding(f.operationID()), outcome); !errors.Is(err, store.ErrEvidenceIntegrity) {
				t.Fatalf("settle of an absent operation = %v", err)
			}
		})
	})
}

// TestCustodialSettleRejectsARefusedRow proves a burned identity can never be
// settled: the refusal comes BEFORE any re-settle comparison, so no requested
// state can be read as an idempotent replay of it.
func TestCustodialSettleRejectsARefusedRow(t *testing.T) {
	forEachCustodyEngine(t, func(t *testing.T, e custodyEngine) {
		f := newCustodyFixture(t, e)
		operation := f.operationID()
		f.reopenWithSpool(1, store.AuditSpoolDegrade)
		if res, err := f.claimOnce(f.claimBinding(operation)); err != nil ||
			res.Outcome != store.CustodialRefusedFresh {
			t.Fatalf("degraded claim = %s, %v", res.Outcome, err)
		}
		f.reopenWithSpool(0, "")
		before, _ := f.journalRow(operation)
		_, err := f.settle(f.settleBinding(operation), store.CustodialSettlement{
			State: model.EvidenceOpCompleted,
		})
		if !errors.Is(err, store.ErrEvidenceIntegrity) {
			t.Fatalf("settle of a refused row = %v", err)
		}
		after, _ := f.journalRow(operation)
		if after.State != model.EvidenceOpRefused || after.Version != before.Version {
			t.Fatalf("the refused row changed: %+v -> %+v", before, after)
		}
	})
}

// TestCustodialSettleRefusesUnderAnotherEpoch proves the settlement-epoch
// decision: a node fenced at a NEWER epoch refuses with no write and does NOT
// poison, so the row stays claimed — the safe, non-replayable shape — and the
// transaction remains committable for whatever else it had decided.
func TestCustodialSettleRefusesUnderAnotherEpoch(t *testing.T) {
	forEachCustodyEngine(t, func(t *testing.T, e custodyEngine) {
		f := newCustodyFixture(t, e)
		operation := f.operationID()
		if _, err := f.claimOnce(f.claimBinding(operation)); err != nil {
			t.Fatalf("claim: %v", err)
		}
		claimed, _ := f.journalRow(operation)
		f.st.(*sqlStore).elector = &custodyScriptedElector{epochs: []uint64{claimed.LeaderEpoch + 1}}
		ctx := context.Background()
		var settleErr error
		commitErr := f.confined(func(sc store.Scope) error {
			h, err := custodyClaimer(t, sc).BindCustodialEffect(ctx, f.settleBinding(operation))
			if err != nil {
				return err
			}
			_, settleErr = h.Settle(ctx, store.CustodialSettlement{State: model.EvidenceOpCompleted})
			return nil // swallowed on purpose: the refusal must not poison
		})
		if !errors.Is(settleErr, store.ErrCustodyEpochChanged) {
			t.Fatalf("settle under a newer epoch = %v", settleErr)
		}
		if commitErr != nil {
			t.Fatalf("an epoch refusal poisoned the transaction: %v", commitErr)
		}
		after, _ := f.journalRow(operation)
		if after.State != model.EvidenceOpClaimed || after.Version != claimed.Version {
			t.Fatalf("the claimed row changed: %+v -> %+v", claimed, after)
		}
	})
}

// TestCustodialSettleSeq0LeavesTheRowClaimed is the ambiguous-but-safe shape: a
// dropped outcome event commits only the loss accounting, so a claimed
// operation is never re-dispatched and a later read still says claimed.
func TestCustodialSettleSeq0LeavesTheRowClaimed(t *testing.T) {
	forEachCustodyEngine(t, func(t *testing.T, e custodyEngine) {
		f := newCustodyFixture(t, e)
		operation := f.operationID()
		if _, err := f.claimOnce(f.claimBinding(operation)); err != nil {
			t.Fatalf("claim: %v", err)
		}
		claimed, _ := f.journalRow(operation)
		f.reopenWithSpool(1, store.AuditSpoolDegrade)
		res, err := f.settle(f.settleBinding(operation), store.CustodialSettlement{
			State: model.EvidenceOpCompleted, DispatchRef: "launch:" + f.launch.String(),
		})
		if err != nil {
			t.Fatalf("degraded settle: %v", err)
		}
		if !res.Dropped || res.Fresh {
			t.Fatalf("degraded settle = %+v, want dropped", res)
		}
		after, _ := f.journalRow(operation)
		if after.State != model.EvidenceOpClaimed || after.Version != claimed.Version {
			t.Fatalf("a dropped settlement changed the row: %+v -> %+v", claimed, after)
		}
		if after.OutcomeEvidenceRef != "" {
			t.Fatalf("a dropped settlement recorded an outcome anchor %q", after.OutcomeEvidenceRef)
		}
	})
}

// ---- confinement on the real scope ------------------------------------

// addRun seeds a second run and its live claim in the named workspace.
func (f *custodyFixture) addRun(workspace model.ID) (runRef string, launch model.ID, claimVersion int64) {
	f.t.Helper()
	ctx := context.Background()
	runRef = "run-" + uniqueSuffix()
	launch = model.NewID()
	sid := "sid-" + uniqueSuffix()
	err := f.st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		now, err := sc.(store.TransactionClock).TransactionNow(ctx)
		if err != nil {
			return err
		}
		claims, err := sc.Ext(custodyClaimKind)
		if err != nil {
			return err
		}
		claim, err := claims.Create(ctx, model.Record{
			custodyClaimSubjectColumn:  sid,
			custodyClaimHolderColumn:   custodyTestHolder,
			custodyClaimFenceColumn:    custodyTestFence,
			custodyClaimStateColumn:    custodyTestActive,
			custodyClaimDeadlineColumn: model.NewTimestamp(now.Time().Add(time.Hour)).String(),
		})
		if err != nil {
			return err
		}
		claimVersion = claim.Int(model.ColVersion)
		runs, err := sc.Ext(custodyRunKind)
		if err != nil {
			return err
		}
		_, err = runs.Create(ctx, model.Record{
			custodyRunRefColumn:       runRef,
			custodyRunLaunchColumn:    launch.String(),
			custodyRunLineageColumn:   workspace.String(),
			custodyRunClaimHolderCol:  custodyTestHolder,
			custodyRunClaimFenceCol:   custodyTestFence,
			custodyRunClaimSubjectCol: sid,
		})
		return err
	})
	if err != nil {
		f.t.Fatalf("seed a run in workspace %s: %v", workspace, err)
	}
	return runRef, launch, claimVersion
}

// TestCustodialConfinementConcealsAndRebindsAcrossWorkspaces proves the two
// halves of confinement on the REAL scope: a run outside the boundary is
// concealed, and an operation identity claimed for one workspace cannot be
// reached from another, because the engine digest binds the target's PERSISTED
// lineage rather than the caller's assertion.
func TestCustodialConfinementConcealsAndRebindsAcrossWorkspaces(t *testing.T) {
	forEachCustodyEngine(t, func(t *testing.T, e custodyEngine) {
		f := newCustodyFixture(t, e)
		ctx := context.Background()
		operation := f.operationID()
		if _, err := f.claimOnce(f.claimBinding(operation)); err != nil {
			t.Fatalf("claim in the first workspace: %v", err)
		}

		t.Run("a foreign workspace conceals the run", func(t *testing.T) {
			err := f.confinedTo(f.other, func(sc store.Scope) error {
				_, bindErr := custodyClaimer(t, sc).BindCustodialEffect(ctx, f.claimBinding(f.operationID()))
				if !errors.Is(bindErr, store.ErrCustodyTargetConcealed) {
					t.Fatalf("bind from the other workspace = %v", bindErr)
				}
				return nil
			})
			if err != nil {
				t.Fatalf("mutate: %v", err)
			}
		})

		t.Run("a foreign workspace cannot reach the recorded state", func(t *testing.T) {
			// A run the other workspace CAN see, presented under the first
			// workspace's operation identity and its semantics.
			otherRun, otherLaunch, otherClaim := f.addRun(f.other)
			b := f.claimBinding(operation)
			b.RunRef, b.RunLaunchID, b.ClaimVersion = otherRun, otherLaunch, otherClaim
			err := f.confinedTo(f.other, func(sc store.Scope) error {
				h, herr := custodyClaimer(t, sc).BindCustodialEffect(ctx, b)
				if herr != nil {
					return herr
				}
				if _, cerr := h.Claim(ctx); !errors.Is(cerr, store.ErrEvidenceRebind) {
					t.Fatalf("cross-workspace claim = %v, want a rebind refusal", cerr)
				}
				return nil // swallowed on purpose
			})
			if !errors.Is(err, store.ErrEvidenceRebind) {
				t.Fatalf("commit after a swallowed cross-workspace rebind = %v", err)
			}
			op, _ := f.journalRow(operation)
			if op.State != model.EvidenceOpClaimed {
				t.Fatalf("the first workspace's row changed: %+v", op)
			}
		})

		t.Run("the lookup of another workspace's identity refuses too", func(t *testing.T) {
			otherRun, otherLaunch, _ := f.addRun(f.other)
			b := f.lookupBinding(operation)
			b.RunRef, b.RunLaunchID = otherRun, otherLaunch
			err := f.st.View(ctx, f.tenant, func(raw store.Scope) error {
				sc, cerr := store.ConfineWorkspace(ctx, raw, f.other)
				if cerr != nil {
					return cerr
				}
				h, herr := custodyClaimer(t, sc).BindCustodialEffect(ctx, b)
				if herr != nil {
					return herr
				}
				if _, _, lerr := h.Lookup(ctx); !errors.Is(lerr, store.ErrEvidenceRebind) {
					t.Fatalf("cross-workspace lookup = %v", lerr)
				}
				return nil
			})
			if err != nil {
				t.Fatalf("view: %v", err)
			}
		})
	})
}

// TestCustodialUnconfinedScopeReadsWithoutAFilter records what an UNCONFINED
// engine scope does: it reads the target through its own (absent) confinement,
// so it can bind a run in any workspace of its tenant — and the digest it
// computes is the SAME as a confined caller's, because the digest binds the
// row's persisted lineage, not the caller's boundary.
func TestCustodialUnconfinedScopeReadsWithoutAFilter(t *testing.T) {
	f := newCustodyFixture(t, custodyEngines()[0])
	ctx := context.Background()
	operation := f.operationID()
	if err := f.st.Mutate(ctx, f.tenant, func(raw store.Scope) error {
		h, err := custodyClaimer(t, raw).BindCustodialEffect(ctx, f.claimBinding(operation))
		if err != nil {
			return err
		}
		res, err := h.Claim(ctx)
		if err != nil {
			return err
		}
		if res.Outcome != store.CustodialFreshAnchored {
			t.Fatalf("unconfined claim = %s", res.Outcome)
		}
		return nil
	}); err != nil {
		t.Fatalf("unconfined claim: %v", err)
	}
	// The confined caller replays the SAME row: one digest, one identity.
	b := f.claimBinding(operation)
	b.ClaimVersion = f.claimRow().Int(model.ColVersion)
	var res store.CustodialClaimResult
	if err := f.confined(func(sc store.Scope) error {
		h, err := custodyClaimer(t, sc).BindCustodialEffect(ctx, b)
		if err != nil {
			return err
		}
		res, err = h.Claim(ctx)
		return err
	}); err != nil {
		t.Fatalf("confined replay: %v", err)
	}
	if res.Outcome != store.CustodialReplayClaimed {
		t.Fatalf("confined replay = %s, want replay_claimed", res.Outcome)
	}
}

// ---- the reservation in the generic producers -------------------------

// TestGenericProducersRefuseTheReservedPrefix proves the reservation is in the
// PRODUCERS and nowhere else: every generic claim and settle path refuses a
// custodial identity, while the readers keep working and the v11 refused-state
// integrity is untouched.
func TestGenericProducersRefuseTheReservedPrefix(t *testing.T) {
	forEachCustodyEngine(t, func(t *testing.T, e custodyEngine) {
		f := newCustodyFixture(t, e)
		ctx := context.Background()
		operation := f.operationID()
		f.reopenWithSpool(1, store.AuditSpoolDegrade)
		if res, err := f.claimOnce(f.claimBinding(operation)); err != nil ||
			res.Outcome != store.CustodialRefusedFresh {
			t.Fatalf("burn the identity = %s, %v", res.Outcome, err)
		}
		f.reopenWithSpool(0, "")

		reserved := store.EvidenceClaim{
			OperationID: operation, EffectDigest: "cev1:whatever",
			Surface: store.ManagedStopSurface, Action: store.ManagedStopAction,
			Actor: custodyTestActor, ActorKind: custodyTestActorKd,
		}
		if err := f.st.Mutate(ctx, f.tenant, func(raw store.Scope) error {
			if _, err := raw.EvidenceOperations().Claim(ctx, reserved); !errors.Is(err, store.ErrEvidenceInvalid) {
				t.Fatalf("generic claim of a reserved identity = %v", err)
			}
			if _, err := raw.EvidenceOperations().Settle(ctx, store.EvidenceSettlement{
				OperationID: operation, EffectDigest: "cev1:whatever",
				State: model.EvidenceOpCompleted,
				Actor: custodyTestActor, ActorKind: custodyTestActorKd,
			}); !errors.Is(err, store.ErrEvidenceInvalid) {
				t.Fatalf("generic settle of a reserved identity = %v", err)
			}
			// The READER is untouched: the burned row still decodes, and its blank
			// claim anchor is what keeps a generic consumer deny-closed on it.
			op, err := raw.EvidenceOperations().Get(ctx, operation)
			if err != nil {
				return err
			}
			if op.State != model.EvidenceOpRefused || op.ClaimEvidenceRef != "" {
				t.Fatalf("reserved row read back as %+v", op)
			}
			// An ordinary identity still claims, so the reservation narrowed
			// nothing else.
			ordinary := store.EvidenceClaim{
				OperationID: "mcp.gateway:" + uniqueSuffix(), EffectDigest: "d1",
				Surface: "mcp.gateway", Action: "tool.call",
				Actor: custodyTestActor, ActorKind: custodyTestActorKd,
			}
			res, err := raw.EvidenceOperations().Claim(ctx, ordinary)
			if err != nil || !res.Fresh {
				t.Fatalf("ordinary generic claim = %+v, %v", res, err)
			}
			return nil
		}); err != nil {
			t.Fatalf("mutate: %v", err)
		}

		// The two drivers refuse before they open a transaction.
		if _, err := store.ClaimEvidenceOperation(ctx, f.st, f.tenant, reserved); !errors.Is(err, store.ErrEvidenceInvalid) {
			t.Fatalf("ClaimEvidenceOperation of a reserved identity = %v", err)
		}
		if _, err := store.SettleEvidenceOperation(ctx, f.st, f.tenant, store.EvidenceSettlement{
			OperationID: operation, EffectDigest: "cev1:whatever",
			State: model.EvidenceOpCompleted,
			Actor: custodyTestActor, ActorKind: custodyTestActorKd,
		}); !errors.Is(err, store.ErrEvidenceInvalid) {
			t.Fatalf("SettleEvidenceOperation of a reserved identity = %v", err)
		}
	})
}

// TestReservedCustodialOperationIDBoundaries is the engine-free predicate. The
// reservation is a PREFIX rule with the journal's own TrimSpace semantics, so a
// leading space cannot smuggle a reserved identity past it.
func TestReservedCustodialOperationIDBoundaries(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{store.ManagedStopOperationPrefix + "abc", true},
		{store.ManagedStopSurface + ".", true},
		{store.ManagedStopSurface + ".v2:abc", true},
		{"  " + store.ManagedStopOperationPrefix + "abc", true},
		{store.ManagedStopSurface, false},
		{"sessions.managed_stopx:abc", false},
		{"mcp.gateway:sessions.managed_stop.v1:abc", false},
		{"", false},
		{"   ", false},
	}
	for _, c := range cases {
		if got := store.ReservedCustodialOperationID(c.id); got != c.want {
			t.Fatalf("ReservedCustodialOperationID(%q) = %t, want %t", c.id, got, c.want)
		}
	}
}

// ---- the engine digest ------------------------------------------------

// TestCustodialEffectDigestFixedVector pins the engine digest to independently
// computed bytes. The expected value is built HERE, field by field, rather than
// by calling the production encoder: a vector derived from the code under test
// proves only that the code equals itself.
func TestCustodialEffectDigestFixedVector(t *testing.T) {
	const (
		tenant     = model.TenantID("0192f7a0-1111-7000-8000-0000000000aa")
		targetKey  = "run-fixed-vector"
		generation = "0192f7a0-2222-7000-8000-0000000000bb"
		lineage    = "0192f7a0-3333-7000-8000-0000000000cc"
		semantic   = "msv1:0123456789abcdef"
	)
	fields := []string{
		string(tenant),
		"sessions.managed_stop",
		"sessions.run",
		targetKey,
		generation,
		lineage,
		semantic,
	}
	preimage := []byte("olivares.store.custodial-effect.v1")
	preimage = append(preimage, 0x00)
	for i, field := range fields {
		preimage = append(preimage, byte(i+1))
		length := len(field)
		for shift := 56; shift >= 0; shift -= 8 {
			preimage = append(preimage, byte(length>>shift))
		}
		preimage = append(preimage, field...)
	}
	sum := sha256.Sum256(preimage)
	want := "cev1:" + hex.EncodeToString(sum[:])

	got := custodialEffectDigest(tenant, custodyRunKind, targetKey, generation, lineage, semantic)
	if got != want {
		t.Fatalf("digest = %s, want %s", got, want)
	}
	// Each field is framed, so no two distinct vectors share a preimage: moving a
	// byte across a boundary must change the digest.
	shifted := custodialEffectDigest(tenant, custodyRunKind, targetKey+"x", generation, lineage, semantic)
	if shifted == got {
		t.Fatal("the digest is insensitive to the target key")
	}
	relineaged := custodialEffectDigest(tenant, custodyRunKind, targetKey, generation, lineage+"d", semantic)
	if relineaged == got {
		t.Fatal("the digest is insensitive to the persisted lineage")
	}
	retenanted := custodialEffectDigest(
		model.TenantID("0192f7a0-1111-7000-8000-0000000000ab"),
		custodyRunKind, targetKey, generation, lineage, semantic)
	if retenanted == got {
		t.Fatal("the digest is insensitive to the tenant")
	}
	// Concatenation confusion: two adjacent fields whose bytes are rearranged
	// across their boundary must not collide.
	if custodialEffectDigest(tenant, custodyRunKind, "ab", "c", lineage, semantic) ==
		custodialEffectDigest(tenant, custodyRunKind, "a", "bc", lineage, semantic) {
		t.Fatal("the framing admits a concatenation collision")
	}
}

// ---- the authorization epoch is an ordinary consumer write ------------

// epochStore returns the confined scope's authorization-epoch capability. It is
// forwarded onto the confined bases, so a module holding a confined Mutate scope
// can reach it — which is exactly why its write has to pass the gate.
func epochStore(t *testing.T, sc store.Scope) store.AuthorizationEpochStore {
	t.Helper()
	epochs, ok := sc.(store.AuthorizationEpochStore)
	if !ok {
		t.Fatalf("scope %T does not expose the authorization epoch capability", sc)
	}
	return epochs
}

// epochVersion reads the tenant's committed authorization generation, outside
// any transaction under test.
func (f *custodyFixture) epochVersion() int64 {
	f.t.Helper()
	var version int64
	err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		got, err := epochStore(f.t, sc).ReadAuthorizationEpoch(context.Background())
		version = got.Version
		return err
	})
	if err != nil {
		f.t.Fatalf("read the authorization epoch: %v", err)
	}
	return version
}

// TestCustodialGateCoversTheAuthorizationEpochWrite is the permanent control for
// the ONE consumer write path that reached the database without passing the
// gate.
//
// BumpAuthorizationEpoch builds its compare-and-swap by hand rather than through
// genericRepo, and it is offered to a CONFINED Mutate scope, so a module can
// issue it. Before it reported itself, an epoch bump was invisible to both
// custody lifetimes at once: it did not count toward the pre-bind write set, so
// a custodial claim bound and committed beside an authorization-state write it
// never inspected; and it was not refused after the seal, so it committed after
// the operation's identity had already been decided.
//
// The third case is the reason the first two are not a guess: OUTSIDE a
// custodial transaction the same capability still reads, still bumps, and still
// commits exactly as before.
func TestCustodialGateCoversTheAuthorizationEpochWrite(t *testing.T) {
	forEachCustodyEngine(t, func(t *testing.T, e custodyEngine) {
		ctx := context.Background()

		t.Run("an epoch bump before the binding is a foreign write", func(t *testing.T) {
			f := newCustodyFixture(t, e)
			operation := f.operationID()
			before := f.epochVersion()
			err := f.confined(func(sc store.Scope) error {
				epochs := epochStore(t, sc)
				observed, rerr := epochs.ReadAuthorizationEpoch(ctx)
				if rerr != nil {
					return rerr
				}
				if _, berr := epochs.BumpAuthorizationEpoch(ctx, observed); berr != nil {
					return berr
				}
				if _, bindErr := custodyClaimer(t, sc).BindCustodialEffect(
					ctx, f.claimBinding(operation),
				); !errors.Is(bindErr, store.ErrCustodyWriteSet) {
					t.Fatalf("bind after an epoch bump = %v, want ErrCustodyWriteSet", bindErr)
				}
				return nil // swallowed on purpose
			})
			if !errors.Is(err, store.ErrCustodyWriteSet) {
				t.Fatalf("commit after a swallowed write-set refusal = %v", err)
			}
			if _, ok := f.journalRow(operation); ok {
				t.Fatal("a refused transaction committed its journal row")
			}
			if after := f.epochVersion(); after != before {
				t.Fatalf("the refused transaction committed an epoch bump: %d -> %d", before, after)
			}
		})

		t.Run("an epoch bump after the seal is refused", func(t *testing.T) {
			f := newCustodyFixture(t, e)
			operation := f.operationID()
			before := f.epochVersion()
			err := f.confined(func(sc store.Scope) error {
				epochs := epochStore(t, sc)
				observed, rerr := epochs.ReadAuthorizationEpoch(ctx)
				if rerr != nil {
					return rerr
				}
				h, herr := custodyClaimer(t, sc).BindCustodialEffect(ctx, f.claimBinding(operation))
				if herr != nil {
					return herr
				}
				res, cerr := h.Claim(ctx)
				if cerr != nil {
					return cerr
				}
				if res.Outcome != store.CustodialFreshAnchored {
					t.Fatalf("claim outcome = %s", res.Outcome)
				}
				if _, berr := epochs.BumpAuthorizationEpoch(ctx, observed); !errors.Is(berr, store.ErrCustodySealed) {
					t.Fatalf("epoch bump after the seal = %v, want ErrCustodySealed", berr)
				}
				return nil // swallowed on purpose
			})
			if !errors.Is(err, store.ErrCustodySealed) {
				t.Fatalf("commit after a swallowed sealed refusal = %v", err)
			}
			if _, ok := f.journalRow(operation); ok {
				t.Fatal("a sealed-violation transaction committed its journal row")
			}
			if after := f.epochVersion(); after != before {
				t.Fatalf("a post-seal epoch bump committed: %d -> %d", before, after)
			}
		})

		t.Run("outside custody the epoch still reads, bumps and commits", func(t *testing.T) {
			f := newCustodyFixture(t, e)
			before := f.epochVersion()
			var returned int64
			if err := f.confined(func(sc store.Scope) error {
				epochs := epochStore(t, sc)
				observed, rerr := epochs.ReadAuthorizationEpoch(ctx)
				if rerr != nil {
					return rerr
				}
				if observed.Version != before {
					t.Fatalf("observed version %d, want the committed %d", observed.Version, before)
				}
				next, berr := epochs.BumpAuthorizationEpoch(ctx, observed)
				returned = next.Version
				return berr
			}); err != nil {
				t.Fatalf("ordinary epoch bump: %v", err)
			}
			if returned != before+1 {
				t.Fatalf("bump returned version %d, want %d", returned, before+1)
			}
			if after := f.epochVersion(); after != before+1 {
				t.Fatalf("committed version %d, want %d", after, before+1)
			}
			// A stale witness is still refused with the capability's own error,
			// not with a gate error: reporting the write did not change what the
			// compare-and-swap decides.
			if err := f.confined(func(sc store.Scope) error {
				_, berr := epochStore(t, sc).BumpAuthorizationEpoch(ctx, store.AuthorizationFactRef{
					Kind: model.AuthorizationEpochKind, ID: model.ID(f.tenant), Version: before,
				})
				if !errors.Is(berr, store.ErrAuthorizationEpochUnavailable) {
					t.Fatalf("stale witness = %v, want ErrAuthorizationEpochUnavailable", berr)
				}
				return nil
			}); err != nil {
				t.Fatalf("stale-witness transaction: %v", err)
			}
		})
	})
}
