// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/hex"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// P1 adds two nullable columns to an append-only ledger. The engine's descriptor
// reconciler is what puts them on an existing database — no module SQL declares
// them, precisely so a fresh database and an upgraded one cannot disagree.
//
// These cases open a PRE-P1 database with the real engine, write a real event
// through it, close it, reopen it with the real post-P1 descriptor, and then check
// that the reconciler added the fields and left the historical row — its
// payload_hash included — exactly as it was.

// preP1Registry strips the two P1 fields from the run_event descriptor, so the real
// RegisterSchema builds the schema this module had before the change. Everything
// else, including every other entity and the migrations, passes through untouched.
type preP1Registry struct {
	store.ExtensionRegistry
	stripped bool
}

func (r *preP1Registry) Register(d model.EntityDescriptor) error {
	if d.Kind == runEventKind {
		kept := make([]model.FieldSpec, 0, len(d.Fields))
		for _, f := range d.Fields {
			if f.Name == colEvRetiredLaunchID || f.Name == colEvTerminalObservation {
				r.stripped = true
				continue
			}
			kept = append(kept, f)
		}
		d.Fields = kept
	}
	return r.ExtensionRegistry.Register(d)
}

// seedOldFormatEvents writes run_event rows the way the PRE-P1 code wrote them:
// the seven-field digest, no evidence columns, straight through the repo. Nothing in
// this path can emit P1 evidence, which is the point — the previous fixture drove the
// NEW finalize against a stripped descriptor, where genericRepo silently omits fields
// the descriptor lacks, so empty DTO fields proved nothing about the row's origin.
func seedOldFormatEvents(t *testing.T, st store.Store, tenant model.TenantID, runRef string) []string {
	t.Helper()
	ctx := context.Background()
	type seed struct {
		seq                                  int64
		event, from, to, detail, at, payload string
	}
	seeds := []seed{
		{0, "created", "", "pending", "", "2026-08-10T10:00:00Z", ""},
		{1, "launched", "pending", "running", "", "2026-08-10T10:01:00Z", ""},
		{2, "stopped", "running", "stopped", "exit 0", "2026-08-10T10:05:00Z", ""},
	}
	hashes := make([]string, 0, len(seeds))
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runEventKind)
		if err != nil {
			return err
		}
		for i := range seeds {
			s := &seeds[i]
			sum := runEventPayloadHash(runRef, s.seq, s.event, s.from, s.to, s.detail, s.at)
			s.payload = hex.EncodeToString(sum[:])
			row := model.Record{
				colEvRunRef: runRef, colEvSeq: s.seq, colEvAt: s.at, colEvEvent: s.event,
				colEvPayloadHash: s.payload, colEvAuditSeq: int64(0),
			}
			setIf(row, colEvFromState, s.from)
			setIf(row, colEvToState, s.to)
			setIf(row, colEvDetail, s.detail)
			if _, err := repo.Create(ctx, row); err != nil {
				return err
			}
			hashes = append(hashes, s.payload)
		}
		return nil
	}); err != nil {
		t.Fatalf("seed old-format events: %v", err)
	}
	return hashes
}

func terminalEvidenceUpgradeCase(t *testing.T, cfg store.Config) {
	t.Helper()
	ctx := context.Background()
	const runRef = "run-old-format-1"

	// --- 1. a pre-P1 database holding genuine old-format rows -------------------
	old := New(WithClock(&testClock{now: baseTime}), WithRunner(&fakeRunner{}),
		WithCredentialSource(staticCred()))
	var stripped bool
	st, err := engine.Open(ctx, cfg, func(reg store.ExtensionRegistry) error {
		wrapped := &preP1Registry{ExtensionRegistry: reg}
		err := old.RegisterSchema(wrapped)
		stripped = wrapped.stripped
		return err
	})
	if err != nil {
		t.Fatalf("open pre-P1: %v", err)
	}
	if !stripped {
		t.Fatal("fixture precondition: the pre-P1 descriptor still declares the P1 fields")
	}
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, e := sys.EnsureSystemTenant(ctx); e != nil {
			return e
		}
		org, e := sys.CreateOrg(ctx, model.Org{Name: "acme", Slug: "acme", Status: model.StatusActive})
		tenant = org.TenantID
		return e
	}); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	seeded := seedOldFormatEvents(t, st, tenant, runRef)

	before := listRunEvents(t, st, tenant, runRef)
	if len(before) != len(seeded) {
		t.Fatalf("seeded %d rows, read %d", len(seeded), len(before))
	}
	for i, ev := range before {
		if ev.PayloadHash != seeded[i] {
			t.Fatalf("seed %d stored %s, want the seven-field digest %s",
				i, ev.PayloadHash, seeded[i])
		}
		if ev.TerminalObservation != "" || ev.RetiredRuntimeLaunchID != "" {
			t.Fatalf("an old-format seed carries evidence: %+v", ev)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close pre-P1: %v", err)
	}

	// --- 2. reopen with the real post-P1 descriptor ------------------------------
	fresh := New(WithClock(&testClock{now: baseTime}), WithRunner(&fakeRunner{}),
		WithCredentialSource(staticCred()))
	st2, err := engine.Open(ctx, cfg, fresh.RegisterSchema)
	if err != nil {
		t.Fatalf("reopen with the P1 descriptor: %v", err)
	}
	defer st2.Close() //nolint:errcheck
	fresh.UseData(api.NewModuleData(st2))

	after := listRunEvents(t, st2, tenant, runRef)
	if len(after) != len(before) {
		t.Fatalf("the upgrade changed the event count: %d -> %d", len(before), len(after))
	}
	for i := range before {
		if after[i] != before[i] {
			t.Fatalf("event %d changed across the upgrade:\nbefore %+v\nafter  %+v",
				i, before[i], after[i])
		}
		if after[i].PayloadHash != seeded[i] {
			t.Fatalf("event %d digest changed across the upgrade", i)
		}
	}

	// --- 3. the reconciled columns are usable by the producer --------------------
	terminalEvidenceWriteAndRead(t, fresh, st2, tenant)
	fresh.Stop(ctx) //nolint:errcheck
}

// terminalEvidenceWriteAndRead drives a real launch and a real terminal transition on
// whatever schema it is handed, then reads the evidence back through the actual DTO
// mapping. Used for the upgraded database AND for a fresh one.
func terminalEvidenceWriteAndRead(t *testing.T, m *Module, st store.Store, tenant model.TenantID) {
	t.Helper()
	ctx := context.Background()
	dto, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: model.ActorUser,
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	rec, err := m.loadRun(ctx, tenant, dto.RunRef)
	if err != nil {
		t.Fatal(err)
	}
	launchID := rec.String(colRuntimeLaunchID)
	if launchID == "" {
		t.Fatal("the launched run carries no runtime_launch_id")
	}
	if _, err := m.transition(ctx, tenant, dto.RunRef, transitionInput{
		event: "stopped", toState: stateStopped, detail: "exit 0",
		actor: "user:u1", actorKind: model.ActorUser,
		terminalObservation: obsProcessExitObserved,
		mutate:              func(rec model.Record) { rec[colRuntimeLaunchID] = nil },
	}); err != nil {
		t.Fatalf("terminal transition: %v", err)
	}
	events := listRunEvents(t, st, tenant, dto.RunRef)
	last := events[len(events)-1]
	if last.TerminalObservation != obsProcessExitObserved || last.RetiredRuntimeLaunchID != launchID {
		t.Fatalf("the P1 columns did not take the evidence: %+v", last)
	}
	if last.PayloadHash == "" {
		t.Fatal("the terminal event stored no payload hash")
	}
}

func TestTerminalEvidenceUpgradesAPreP1SQLiteDatabase(t *testing.T) {
	terminalEvidenceUpgradeCase(t, store.Config{
		Engine: store.EngineSQLite,
		DSN:    filepath.Join(t.TempDir(), "pre-p1.db"),
		Debug:  true,
	})
}

// R5: an EMPTY database opened with the full P1 descriptor, so fresh schema creation
// is executed on PostgreSQL rather than only the upgrade path.
func TestTerminalEvidenceFreshPostgresP1SchemaWritesAndReads(t *testing.T) {
	if !enginetest.PostgresAvailable(t) {
		t.Skipf("%s unset: the project PostgreSQL runtime is not available to this run",
			enginetest.EnvSuperuserDSN)
	}
	pg := enginetest.IsolatedPostgres(t)
	ctx := context.Background()
	m := New(WithClock(&testClock{now: baseTime}), WithRunner(&fakeRunner{}),
		WithCredentialSource(staticCred()))
	st, err := engine.Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: pg.App, Debug: true,
	}, m.RegisterSchema)
	if err != nil {
		t.Fatalf("fresh P1 open: %v", err)
	}
	defer st.Close() //nolint:errcheck
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, e := sys.EnsureSystemTenant(ctx); e != nil {
			return e
		}
		org, e := sys.CreateOrg(ctx, model.Org{Name: "acme", Slug: "acme", Status: model.StatusActive})
		tenant = org.TenantID
		return e
	}); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	m.UseData(api.NewModuleData(st))
	terminalEvidenceWriteAndRead(t, m, st, tenant)
	m.Stop(ctx) //nolint:errcheck
}

func TestTerminalEvidenceUpgradesAPreP1PostgresDatabase(t *testing.T) {
	if !enginetest.PostgresAvailable(t) {
		t.Skipf("%s unset: the project PostgreSQL runtime is not available to this run",
			enginetest.EnvSuperuserDSN)
	}
	pg := enginetest.IsolatedPostgres(t)
	terminalEvidenceUpgradeCase(t, store.Config{
		Engine: store.EnginePostgres, DSN: pg.App, Debug: true,
	})
}
