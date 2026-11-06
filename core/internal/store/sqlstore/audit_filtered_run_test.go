// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The audit list's tool= and folder= filters (#2750). The ledger row carries no
// tool and no folder: they are facts of the sessions.run row an entry targets,
// and of the approval that run names. These tests pin that the store resolves
// them by that relation, in SQL, on both engines.

// registerRunFacts declares the sessions.run columns the filters read, as the
// sessions module does (modules/sessions/runtime_schema.go).
func registerRunFacts(reg store.ExtensionRegistry) error {
	return reg.Register(model.EntityDescriptor{
		Kind:  custodyRunKind,
		Table: custodyRunTable,
		Fields: []model.FieldSpec{
			{Name: "run_ref", Kind: model.KindText},
			{Name: auditRunToolColumn, Kind: model.KindText, Nullable: true},
			{Name: auditRunFolderColumn, Kind: model.KindText, Nullable: true},
			{Name: auditRunApprovalColumn, Kind: model.KindText, Nullable: true},
		},
	})
}

type runFactsLedger struct {
	tenant model.TenantID
	// seq of each seeded entry, by name.
	seq map[string]int64
}

func seedRunFactsLedger(t *testing.T, st store.Store) runFactsLedger {
	t.Helper()
	ctx := context.Background()
	out := runFactsLedger{seq: map[string]int64{}}
	out.tenant = provisionTenant(t, st, "runfacts-"+uniqueSuffix())
	approvalA, approvalC := model.NewID().String(), model.NewID().String()
	// The approval another tenant's codex run names: an entry of THIS tenant about
	// it must not match either, so the approval branch carries its own tenant clause.
	approvalForeign := model.NewID().String()
	// Another tenant's codex run. An entry of THIS tenant that names its id must
	// not match: the subquery is the tenant's own runs, not the table's.
	otherTenant := provisionTenant(t, st, "runfacts-other-"+uniqueSuffix())
	var foreignRun model.ID
	if err := st.Mutate(ctx, otherTenant, func(sc store.Scope) error {
		runs, err := sc.Ext(custodyRunKind)
		if err != nil {
			return err
		}
		created, err := runs.Create(ctx, model.Record{
			"run_ref": "run-foreign", auditRunToolColumn: "codex", auditRunFolderColumn: "/work/a",
			auditRunApprovalColumn: approvalForeign,
		})
		foreignRun = model.ID(created.String(model.ColID))
		return err
	}); err != nil {
		t.Fatalf("seed the other tenant: %v", err)
	}
	err := st.Mutate(ctx, out.tenant, func(sc store.Scope) error {
		runs, err := sc.Ext(custodyRunKind)
		if err != nil {
			return err
		}
		mk := func(ref, tool, folder, approval string) (model.ID, error) {
			rec := model.Record{"run_ref": ref, auditRunToolColumn: tool, auditRunFolderColumn: folder}
			if approval != "" {
				rec[auditRunApprovalColumn] = approval
			}
			created, err := runs.Create(ctx, rec)
			return model.ID(created.String(model.ColID)), err
		}
		runA, err := mk("run-a", "claude", "/work/a", approvalA)
		if err != nil {
			return err
		}
		runB, err := mk("run-b", "codex", "/work/a", "")
		if err != nil {
			return err
		}
		runC, err := mk("run-c", "codex", "/work/b", approvalC)
		if err != nil {
			return err
		}
		// A run that predates the columns (all NULL) and one whose approval_ref is
		// the empty string: neither may attract an entry that has no id.
		nullRow, err := runs.Create(ctx, model.Record{"run_ref": "run-null"})
		if err != nil {
			return err
		}
		runNull := model.ID(nullRow.String(model.ColID))
		if _, err := runs.Create(ctx, model.Record{
			"run_ref": "run-empty-approval", auditRunToolColumn: "codex",
			auditRunFolderColumn: "/work/c", auditRunApprovalColumn: "",
		}); err != nil {
			return err
		}
		add := func(name, action string, kind model.Kind, target model.ID) error {
			ev, err := sc.Audit().Append(ctx, model.AuditDraft{
				Actor: "user:alice", Action: action, TargetKind: kind, TargetID: target,
			})
			out.seq[name] = ev.Seq
			return err
		}
		for _, e := range []struct {
			name, action string
			kind         model.Kind
			target       model.ID
		}{
			{"run-a", "sessions.run.launch", custodyRunKind, runA},
			{"run-b", "sessions.run.launch", custodyRunKind, runB},
			{"run-c", "sessions.run.launch", custodyRunKind, runC},
			{"approval-a", "governance.approval.decision", auditApprovalKind, model.ID(approvalA)},
			{"approval-c", "governance.approval.decision", auditApprovalKind, model.ID(approvalC)},
			// An approval no run names, and an entry of another kind that merely
			// reuses a run's id: neither may be claimed by a tool or a folder.
			{"approval-stray", "governance.approval.decision", auditApprovalKind, model.NewID()},
			{"other-kind-same-id", "core.session.read", model.Kind("core.session"), runC},
			// Each branch of the predicate takes only its own kind: an approval entry
			// that carries a run's id, and a run entry that carries an approval's id.
			{"approval-kind-run-id", "governance.approval.decision", auditApprovalKind, runC},
			{"run-kind-approval-id", "sessions.run.launch", custodyRunKind, model.ID(approvalC)},
			{"no-target", "audit.read", model.Kind("core.audit_event"), ""},
			// No id on an approval entry, a run with no facts, and a run of another tenant.
			{"approval-no-id", "governance.approval.decision", auditApprovalKind, ""},
			{"null-run", "sessions.run.launch", custodyRunKind, runNull},
			{"foreign-run", "sessions.run.launch", custodyRunKind, foreignRun},
			{"foreign-approval", "governance.approval.decision", auditApprovalKind, model.ID(approvalForeign)},
		} {
			if err := add(e.name, e.action, e.kind, e.target); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed run facts ledger: %v", err)
	}
	return out
}

func walkRunFacts(t *testing.T, st store.Store, tenant model.TenantID, f store.AuditFilter) ([]int64, error) {
	t.Helper()
	var got []int64
	err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		fw, ok := sc.Audit().(store.FilteredWalker)
		if !ok {
			t.Fatal("the sqlstore audit log must offer FilteredWalker")
		}
		return fw.WalkFiltered(context.Background(), 0, f, func(ev model.AuditEvent) error {
			got = append(got, ev.Seq)
			return nil
		})
	})
	return got, err
}

func runFactsSuite(t *testing.T, st store.Store) {
	t.Helper()
	l := seedRunFactsLedger(t, st)
	// Before every entry of the seeded ledger, on the fixed SQLite clock and on
	// the real clock the Postgres leg runs on.
	early := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seqs := func(names ...string) []int64 {
		out := []int64{}
		for _, n := range names {
			out = append(out, l.seq[n])
		}
		return out
	}
	for _, c := range []struct {
		name   string
		filter store.AuditFilter
		want   []int64
	}{
		{"tool codex: its runs and their approvals", store.AuditFilter{Tool: "codex"}, seqs("run-b", "run-c", "approval-c")},
		{"tool claude", store.AuditFilter{Tool: "claude"}, seqs("run-a", "approval-a")},
		{"tool nobody runs", store.AuditFilter{Tool: "gemini"}, seqs()},
		{"an approval named only by another tenant's run", store.AuditFilter{Folder: "/work/a", Tool: "codex", ActionPrefix: "governance."}, seqs()},
		{"folder a: both runs there and the approval of one", store.AuditFilter{Folder: "/work/a"}, seqs("run-a", "run-b", "approval-a")},
		{"folder b", store.AuditFilter{Folder: "/work/b"}, seqs("run-c", "approval-c")},
		{"folder is exact, not a prefix", store.AuditFilter{Folder: "/work"}, seqs()},
		// Exact means exact: no SQL wildcard, no case folding, no trailing slash.
		{"tool: percent is not a wildcard", store.AuditFilter{Tool: "cod%"}, seqs()},
		{"tool: underscore is not a wildcard", store.AuditFilter{Tool: "cod_x"}, seqs()},
		{"tool: case matters", store.AuditFilter{Tool: "CODEX"}, seqs()},
		{"folder: percent is not a wildcard", store.AuditFilter{Folder: "/work/%"}, seqs()},
		{"folder: underscore is not a wildcard", store.AuditFilter{Folder: "/work/_"}, seqs()},
		{"folder: case matters", store.AuditFilter{Folder: "/WORK/A"}, seqs()},
		{"folder: a trailing slash is another path", store.AuditFilter{Folder: "/work/a/"}, seqs()},
		// The approval of a run that fails the OTHER half of the filter is excluded:
		// approval-a belongs to a claude run in /work/a, approval-c to a codex run in /work/b.
		{"tool and folder together", store.AuditFilter{Tool: "codex", Folder: "/work/a"}, seqs("run-b")},
		// The run clause meets every other filter in one statement.
		{"with an action prefix", store.AuditFilter{Tool: "codex", ActionPrefix: "governance."}, seqs("approval-c")},
		{"with an excluded action", store.AuditFilter{Tool: "codex", ExcludeActionPrefixes: []string{"governance."}}, seqs("run-b", "run-c")},
		{"with the actor", store.AuditFilter{Tool: "codex", Actor: "user:alice"}, seqs("run-b", "run-c", "approval-c")},
		{"with another actor", store.AuditFilter{Tool: "codex", Actor: "user:bob"}, seqs()},
		{"with a target kind", store.AuditFilter{Folder: "/work/a", TargetKind: "governance.approval"}, seqs("approval-a")},
		{"with a text", store.AuditFilter{Tool: "codex", Q: "DECISION"}, seqs("approval-c")},
		{"since an early date", store.AuditFilter{Tool: "claude", Since: &early}, seqs("run-a", "approval-a")},
		{"until an early date", store.AuditFilter{Tool: "claude", Until: &early}, seqs()},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := walkRunFacts(t, st, l.tenant, c.filter)
			if err != nil {
				t.Fatal(err)
			}
			if got == nil {
				got = []int64{}
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("%+v = %v, want %v", c.filter, got, c.want)
			}
		})
	}
}

func TestAuditFilteredWalkByToolAndFolder(t *testing.T) {
	st, err := Open(context.Background(), store.Config{
		Engine: store.EngineSQLite, DSN: ":memory:", Clock: &tickClock{t: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)},
	}, registerRunFacts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	runFactsSuite(t, st)
}

func TestAuditFilteredWalkByToolAndFolderPostgres(t *testing.T) {
	pg := isolatedPG(t)
	st, err := Open(context.Background(), store.Config{
		Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner, AdminDSN: pg.Admin,
	}, registerRunFacts)
	if err != nil {
		t.Fatalf("open PG store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	runFactsSuite(t, st)
}

// A store whose sessions module is not registered cannot say which entries belong
// to a tool or a folder. Answering with an empty page would claim there are none;
// answering with everything would ignore the filter. It must refuse by name.
func TestAuditFilteredWalkByToolRefusesWithoutSessions(t *testing.T) {
	st, err := Open(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	tenant := provisionTenant(t, st, "norun-"+uniqueSuffix())
	for _, f := range []store.AuditFilter{{Tool: "codex"}, {Folder: "/work/a"}} {
		_, err := walkRunFacts(t, st, tenant, f)
		if !errors.Is(err, store.ErrAuditFilterUnavailable) {
			t.Fatalf("%+v: err = %v, want ErrAuditFilterUnavailable", f, err)
		}
	}
	if _, err := walkRunFacts(t, st, tenant, store.AuditFilter{Actor: "user:alice"}); err != nil {
		t.Fatalf("a filter that needs no run facts must keep working: %v", err)
	}
}

// The columns are checked, not assumed: a sessions.run entity that lacks one of
// them must refuse by name rather than fail inside the SQL.
func TestAuditFilteredWalkByToolRefusesAMisshapenRunEntity(t *testing.T) {
	columns := []string{auditRunToolColumn, auditRunFolderColumn, auditRunApprovalColumn}
	for _, missing := range columns {
		t.Run("without "+missing, func(t *testing.T) {
			st, err := Open(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, func(reg store.ExtensionRegistry) error {
				fields := []model.FieldSpec{{Name: "run_ref", Kind: model.KindText}}
				for _, c := range columns {
					if c != missing {
						fields = append(fields, model.FieldSpec{Name: c, Kind: model.KindText, Nullable: true})
					}
				}
				return reg.Register(model.EntityDescriptor{Kind: custodyRunKind, Table: custodyRunTable, Fields: fields})
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			tenant := provisionTenant(t, st, "shape-"+uniqueSuffix())
			for _, f := range []store.AuditFilter{{Tool: "codex"}, {Folder: "/work/a"}} {
				if _, err := walkRunFacts(t, st, tenant, f); !errors.Is(err, store.ErrAuditFilterUnavailable) {
					t.Fatalf("%+v: err = %v, want ErrAuditFilterUnavailable", f, err)
				}
			}
		})
	}
}
