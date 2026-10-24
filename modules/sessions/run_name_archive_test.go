// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// runAuditRow is one audit row of a run action and its stored metadata, which a
// walked event does not carry in Meta.
type runAuditRow struct {
	model.AuditEvent
	meta string
}

// runAuditEvents is the tenant's audit rows of one action, oldest first.
func runAuditEvents(t *testing.T, st store.Store, tenant model.TenantID, action string) []runAuditRow {
	t.Helper()
	var out []runAuditRow
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		walker, ok := sc.Audit().(store.CanonicalWalker)
		if !ok {
			t.Fatal("the store's audit log cannot walk stored metadata")
		}
		return walker.WalkCanonical(context.Background(), 1, func(e model.AuditEvent, meta string, _ []byte) error {
			if e.Action == action {
				out = append(out, runAuditRow{AuditEvent: e, meta: meta})
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// #2795: a session is renamed from its row. The name is trimmed, an empty one
// clears it, the answer is the run as GET returns it, and the ledger says that the
// run was renamed without repeating the name.
func TestRunRenameSetsAndClearsTheName(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "run-rename")
	viewer := h.viewerToken(admin, tenant, "v@rename.io")
	seedRunRows(t, h.st, tenant, stateRunning, "run-1")
	seedRunRows(t, h.st, tenant, stateCleaned, "run-released")
	path := "/v1/m/sessions/runs/run-1/name"

	r := h.doJSON("PUT", path, admin, map[string]any{"name": "  Release notes draft  "}, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["name"] != "Release notes draft" || r.body["run_ref"] != "run-1" || r.body["state"] == nil {
		t.Fatalf("rename = %d %s", r.code, r.raw)
	}
	if got := h.do("GET", "/v1/m/sessions/runs/run-1", viewer, tenantHdr(tenant)); got.body["name"] != "Release notes draft" {
		t.Fatalf("read after rename = %d %s", got.code, got.raw)
	}
	r = h.doJSON("PUT", path, admin, map[string]any{"name": "   "}, tenantHdr(tenant))
	if _, named := r.body["name"]; r.code != http.StatusOK || named {
		t.Fatalf("clear = %d %s, want 200 without a name", r.code, r.raw)
	}
	if got := h.do("GET", "/v1/m/sessions/runs/run-1", viewer, tenantHdr(tenant)); got.body["name"] != nil {
		t.Fatalf("read after clear = %s", got.raw)
	}

	for _, body := range []map[string]any{
		{},
		{"name": strings.Repeat("\u00e9", maxRunNameRunes+1)},
		{"name": "line\nbreak"},
		{"name": "bell\a"},
		// Invisible and direction-changing characters: a name that looks like another's
		// would carry no distinguishing tail in the list.
		{"name": "deploy-prod\u200b"},
		{"name": "\u202eprod"},
		{"name": "a\u2028b"},
		{"name": "\ufeffprod"},
		// Default-ignorable characters render as nothing: a name of fillers looks blank.
		{"name": "\u3164\u3164"},
		{"name": "prod\ufe0f"},
		{"name": "a\u2029b"},
		{"name": "soft\u00adhyphen"},
		// The braille blank is a symbol that draws nothing.
		{"name": "\u2800\u2800"},
		{"name": "tab\tbed"},
		{"name": "x", "other": true},
	} {
		if r := h.doJSON("PUT", path, admin, body, tenantHdr(tenant)); r.code != http.StatusBadRequest {
			t.Fatalf("rename %v = %d %s, want 400", body, r.code, r.raw)
		}
	}
	if r := h.doJSON("PUT", path, admin, map[string]any{"name": strings.Repeat("\u00e9", maxRunNameRunes)}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("a name of exactly %d runes = %d %s", maxRunNameRunes, r.code, r.raw)
	}
	// What a name may hold: a private-use icon (fonts draw them), an ideographic space
	// inside a CJK name, other braille and other symbols.
	for _, name := range []string{"\ue0a0 main", "日本\u3000語", "\u2801 dots", "🚀 launch"} {
		if r := h.doJSON("PUT", path, admin, map[string]any{"name": name}, tenantHdr(tenant)); r.code != http.StatusOK || r.body["name"] != name {
			t.Fatalf("rename %q = %d %s, want 200", name, r.code, r.raw)
		}
	}
	if r := h.doJSON("PUT", path, viewer, map[string]any{"name": "mine"}, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("viewer rename = %d %s, want 403", r.code, r.raw)
	}
	if r := h.doJSON("PUT", "/v1/m/sessions/runs/run-absent/name", admin, map[string]any{"name": "x"}, tenantHdr(tenant)); r.code != http.StatusNotFound {
		t.Fatalf("rename of an absent run = %d %s, want 404", r.code, r.raw)
	}
	// A released session keeps its row until it is deleted, and its name with it.
	if r := h.doJSON("PUT", "/v1/m/sessions/runs/run-released/name", admin, map[string]any{"name": "kept"}, tenantHdr(tenant)); r.code != http.StatusOK || r.body["name"] != "kept" {
		t.Fatalf("rename of a released run = %d %s", r.code, r.raw)
	}

	// The name it already has changes nothing and records nothing.
	if r := h.doJSON("PUT", "/v1/m/sessions/runs/run-released/name", admin, map[string]any{"name": " kept "}, tenantHdr(tenant)); r.code != http.StatusOK || r.body["name"] != "kept" {
		t.Fatalf("rename to the same name = %d %s", r.code, r.raw)
	}

	events := runAuditEvents(t, h.st, tenant, "sessions.run.renamed")
	if len(events) != 8 {
		t.Fatalf("renamed audit rows = %d, want 8 (set, clear, longest, the 4 accepted, released)", len(events))
	}
	for _, e := range events {
		if e.TargetKind != runKind || e.TargetID == "" || !strings.Contains(e.meta, `"run_ref":"run-`) ||
			!strings.HasPrefix(e.Actor, "user:") || e.ActorKind != model.ActorUser {
			t.Fatalf("audit row = %+v", e)
		}
		if strings.Contains(e.meta, "Release notes") || strings.Contains(e.meta, "kept") {
			t.Fatalf("the audit row repeats the name: %s", e.meta)
		}
	}
}

// Archiving is the session's own state, shared by everyone who reads it: both
// directions are idempotent, the run carries archived_at, the list still returns
// it (clients filter) and each change is in the ledger once.
func TestRunArchiveIsIdempotentAndAudited(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "run-archive")
	viewer := h.viewerToken(admin, tenant, "v@archive.io")
	seedRunRows(t, h.st, tenant, stateStopped, "run-1", "run-2")
	path := "/v1/m/sessions/runs/run-1/archive"

	r := h.do("POST", path, admin, tenantHdr(tenant))
	at, _ := r.body["archived_at"].(string)
	if r.code != http.StatusOK || r.body["run_ref"] != "run-1" || at == "" {
		t.Fatalf("archive = %d %s", r.code, r.raw)
	}
	if _, err := model.ParseTimestamp(at); err != nil || !strings.HasSuffix(at, "Z") {
		t.Fatalf("archived_at %q is not an RFC 3339 UTC time: %v", at, err)
	}
	if again := h.do("POST", path, admin, tenantHdr(tenant)); again.code != http.StatusOK || again.body["archived_at"] != at {
		t.Fatalf("repeat archive = %d %s, want 200 with the first archived_at %s", again.code, again.raw, at)
	}
	list := runItemsOf(t, h.do("GET", "/v1/m/sessions/runs", viewer, tenantHdr(tenant)))
	if len(list) != 2 || list["run-1"]["archived_at"] != at || list["run-2"]["archived_at"] != nil {
		t.Fatalf("the list must carry every run and mark the archived one: %v", list)
	}
	if got := h.do("GET", "/v1/m/sessions/runs/run-1", viewer, tenantHdr(tenant)); got.body["archived_at"] != at {
		t.Fatalf("get after archive = %s", got.raw)
	}
	if r := h.do("POST", path, viewer, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("viewer archive = %d %s, want 403", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/sessions/runs/run-absent/archive", admin, tenantHdr(tenant)); r.code != http.StatusNotFound {
		t.Fatalf("archive of an absent run = %d %s, want 404", r.code, r.raw)
	}

	for range 2 {
		r = h.do("DELETE", path, admin, tenantHdr(tenant))
		if _, archived := r.body["archived_at"]; r.code != http.StatusOK || archived {
			t.Fatalf("unarchive = %d %s, want 200 without archived_at", r.code, r.raw)
		}
	}
	if got := h.do("GET", "/v1/m/sessions/runs/run-1", viewer, tenantHdr(tenant)); got.body["archived_at"] != nil {
		t.Fatalf("get after unarchive = %s", got.raw)
	}

	for action, want := range map[string]int{"sessions.run.archived": 1, "sessions.run.unarchived": 1} {
		events := runAuditEvents(t, h.st, tenant, action)
		if len(events) != want {
			t.Fatalf("%s audit rows = %d, want %d: a repeated call changes nothing and records nothing", action, len(events), want)
		}
		if events[0].TargetKind != runKind || !strings.Contains(events[0].meta, `"run_ref":"run-1"`) ||
			!strings.HasPrefix(events[0].Actor, "user:") || events[0].ActorKind != model.ActorUser {
			t.Fatalf("%s audit row = %+v", action, events[0])
		}
	}
}

// Deleting a session's record is an administrator's act and is in the ledger, in
// the transaction of the delete. A session that is still running is refused.
func TestRunDeleteIsAuditedAndRefusesARunningSession(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "run-delete-audit")
	seedRunRows(t, h.st, tenant, stateRunning, "run-live")
	seedRunRows(t, h.st, tenant, stateCleaned, "run-released")

	r := h.do("DELETE", "/v1/m/sessions/runs/run-live", admin, tenantHdr(tenant))
	if r.code != http.StatusConflict || !strings.Contains(r.raw, "session must be cleaned before delete (state=running)") {
		t.Fatalf("delete of a running session = %d %s, want 409 naming its state", r.code, r.raw)
	}
	if n := len(runAuditEvents(t, h.st, tenant, "sessions.run.deleted")); n != 0 {
		t.Fatalf("a refused delete wrote %d audit rows", n)
	}
	if r := h.do("DELETE", "/v1/m/sessions/runs/run-released", admin, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("delete of a released session = %d %s", r.code, r.raw)
	}
	events := runAuditEvents(t, h.st, tenant, "sessions.run.deleted")
	if len(events) != 1 || events[0].TargetKind != runKind || events[0].TargetID == "" ||
		!strings.Contains(events[0].meta, `"run_ref":"run-released"`) || !strings.HasPrefix(events[0].Actor, "user:") {
		t.Fatalf("deleted audit rows = %+v", events)
	}
}

// Rename and archive take the run's WRITE permission and delete its ADMIN one: an
// editor names and files a session but cannot remove its record.
func TestRunRenameAndArchiveNeedWriteAndDeleteNeedsAdmin(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "run-perm-split")
	editor := h.userToken(admin, tenant, "e@perm.io", auth.RoleEditor)
	seedRunRows(t, h.st, tenant, stateCleaned, "run-1")

	if r := h.doJSON("PUT", "/v1/m/sessions/runs/run-1/name", editor, map[string]any{"name": "mine"}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("editor rename = %d %s, want 200", r.code, r.raw)
	}
	for _, method := range []string{"POST", "DELETE"} {
		if r := h.do(method, "/v1/m/sessions/runs/run-1/archive", editor, tenantHdr(tenant)); r.code != http.StatusOK {
			t.Fatalf("editor %s archive = %d %s, want 200", method, r.code, r.raw)
		}
	}
	if r := h.do("DELETE", "/v1/m/sessions/runs/run-1", editor, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("editor delete = %d %s, want 403", r.code, r.raw)
	}
	if n := len(runAuditEvents(t, h.st, tenant, "sessions.run.deleted")); n != 0 {
		t.Fatalf("a refused delete wrote %d audit rows", n)
	}
}

// preArchiveRegistry strips archived_at from the run descriptor, so the real
// RegisterSchema builds the schema this module had before #2795.
type preArchiveRegistry struct {
	store.ExtensionRegistry
	stripped bool
}

func (r *preArchiveRegistry) Register(d model.EntityDescriptor) error {
	if d.Kind == runKind {
		kept := make([]model.FieldSpec, 0, len(d.Fields))
		for _, f := range d.Fields {
			if f.Name == colRunArchivedAt {
				r.stripped = true
				continue
			}
			kept = append(kept, f)
		}
		d.Fields = kept
	}
	return r.ExtensionRegistry.Register(d)
}

// archiveUpgradeCase opens a database without archived_at, writes a run, reopens
// it with the real descriptor and archives that run: the descriptor reconciler is
// what adds the column to an existing sessions_run, as it did for every nullable
// run column before it.
func archiveUpgradeCase(t *testing.T, cfg store.Config) {
	t.Helper()
	ctx := context.Background()
	old := New(WithSessionWorkspaceRoot(t.TempDir()))
	wrapped := &preArchiveRegistry{}
	st, err := engine.Open(ctx, cfg, func(reg store.ExtensionRegistry) error {
		wrapped.ExtensionRegistry = reg
		return old.RegisterSchema(wrapped)
	})
	if err != nil {
		t.Fatalf("open before archived_at: %v", err)
	}
	if !wrapped.stripped {
		t.Fatal("fixture precondition: the old descriptor still declares archived_at")
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
	seedRunRows(t, st, tenant, stateStopped, "run-old")
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	fresh := New(WithSessionWorkspaceRoot(t.TempDir()))
	st2, err := engine.Open(ctx, cfg, fresh.RegisterSchema)
	if err != nil {
		t.Fatalf("reopen with archived_at: %v", err)
	}
	defer st2.Close() //nolint:errcheck // best-effort close after the assertions
	fresh.UseData(api.NewModuleData(st2))
	at := model.NewTimestamp(baseTime).String()
	if err := st2.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rec, err := findRunRec(ctx, repo, "run-old")
		if err != nil {
			return err
		}
		if fresh.toRunDTO(rec).ArchivedAt != "" {
			t.Fatal("a run that predates the column reads as archived")
		}
		rec[colRunArchivedAt] = at
		_, err = repo.Update(ctx, rec)
		return err
	}); err != nil {
		t.Fatalf("archive through the reconciled column: %v", err)
	}
	if err := st2.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rec, err := findRunRec(ctx, repo, "run-old")
		if err != nil {
			return err
		}
		if got := fresh.toRunDTO(rec).ArchivedAt; got != at {
			t.Fatalf("archived_at round-trip = %q, want %q", got, at)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRunArchivedAtUpgradesASQLiteDatabase(t *testing.T) {
	archiveUpgradeCase(t, store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(t.TempDir(), "pre-archive.db")})
}

func TestRunArchivedAtUpgradesAPostgresDatabase(t *testing.T) {
	if !enginetest.PostgresAvailable(t) {
		t.Skipf("%s unset: the project PostgreSQL runtime is not available to this run",
			enginetest.EnvSuperuserDSN)
	}
	pg := enginetest.IsolatedPostgres(t)
	archiveUpgradeCase(t, store.Config{Engine: store.EnginePostgres, DSN: pg.App})
}
