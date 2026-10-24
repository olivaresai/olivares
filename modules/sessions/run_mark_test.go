// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func countRunMarks(t *testing.T, st store.Store, tenant model.TenantID) int {
	t.Helper()
	var n int
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runMarkKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(context.Background(), model.Query{Limit: 1000})
		if err != nil {
			return err
		}
		n = len(recs)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

func snoozeBody(until time.Time) map[string]any {
	return map[string]any{"until": until.UTC().Format(time.RFC3339)}
}

// TestRunSnoozeAndDoneAreTheCallersOwn snoozes a run and marks it done over HTTP
// and proves both marks follow the person, not the session: the caller's list and
// get carry snoozed_until and done, a colleague's do not, every direction is
// idempotent, and clearing both leaves no row behind.
func TestRunSnoozeAndDoneAreTheCallersOwn(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "marks-own")
	viewer := h.viewerToken(admin, tenant, "v@marks.io")
	colleague := h.viewerToken(admin, tenant, "w@marks.io")
	seedRunRows(t, h.st, tenant, stateRunning, "run-1", "run-2")
	until := time.Now().Add(time.Hour).Truncate(time.Second)

	r := h.doJSON("POST", "/v1/m/sessions/runs/run-1/snooze", viewer, snoozeBody(until), tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["run_ref"] != "run-1" || r.body["done"] != false {
		t.Fatalf("snooze = %d %s", r.code, r.raw)
	}
	got, _ := r.body["snoozed_until"].(string)
	if at, err := time.Parse(time.RFC3339Nano, got); err != nil || !at.Equal(until) {
		t.Fatalf("snooze answer snoozed_until = %q, want %s", got, until.UTC().Format(time.RFC3339))
	}
	if r = h.do("POST", "/v1/m/sessions/runs/run-1/done", viewer, tenantHdr(tenant)); r.code != http.StatusOK || r.body["done"] != true {
		t.Fatalf("done = %d %s", r.code, r.raw)
	}
	if r.body["snoozed_until"] != got {
		t.Fatalf("done dropped the snooze: %s", r.raw)
	}

	for who, tok := range map[string]string{"viewer": viewer, "colleague": colleague} {
		r = h.do("GET", "/v1/m/sessions/runs", tok, tenantHdr(tenant))
		if r.code != http.StatusOK {
			t.Fatalf("%s list = %d %s", who, r.code, r.raw)
		}
		items := runItemsOf(t, r)
		if len(items) != 2 {
			t.Fatalf("%s sees %d runs, want 2: %s", who, len(items), r.raw)
		}
		done, ok := items["run-1"]["done"].(bool)
		if !ok {
			t.Fatalf("%s list omits done: %s", who, r.raw)
		}
		mine := who == "viewer"
		if done != mine {
			t.Fatalf("%s sees done=%v, want %v", who, done, mine)
		}
		if _, snoozed := items["run-1"]["snoozed_until"]; snoozed != mine {
			t.Fatalf("%s sees snoozed_until present=%v, want %v: %s", who, snoozed, mine, r.raw)
		}
		if items["run-2"]["done"] != false || items["run-2"]["snoozed_until"] != nil {
			t.Fatalf("%s sees a mark on the unmarked run: %s", who, r.raw)
		}
	}
	r = h.do("GET", "/v1/m/sessions/runs/run-1", viewer, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["done"] != true || r.body["snoozed_until"] != got {
		t.Fatalf("get marked run = %d %s", r.code, r.raw)
	}
	r = h.do("GET", "/v1/m/sessions/runs/run-1", colleague, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["done"] != false || r.body["snoozed_until"] != nil {
		t.Fatalf("colleague get = %d %s", r.code, r.raw)
	}

	// A second snooze moves the moment; it never adds a row.
	later := until.Add(time.Hour)
	if r = h.doJSON("POST", "/v1/m/sessions/runs/run-1/snooze", viewer, snoozeBody(later), tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("re-snooze = %d %s", r.code, r.raw)
	}
	if at, _ := time.Parse(time.RFC3339Nano, r.body["snoozed_until"].(string)); !at.Equal(later) {
		t.Fatalf("re-snooze answer = %s, want %s", r.raw, later)
	}
	if n := countRunMarks(t, h.st, tenant); n != 1 {
		t.Fatalf("%d mark rows after two snoozes and a done, want 1", n)
	}

	// The marks are independent: ending the snooze keeps done, and the row with it.
	for range 2 { // idempotent: a repeated clear is the same success
		if r = h.do("DELETE", "/v1/m/sessions/runs/run-1/snooze", viewer, tenantHdr(tenant)); r.code != http.StatusOK {
			t.Fatalf("unsnooze = %d %s", r.code, r.raw)
		}
	}
	if r.body["done"] != true || r.body["snoozed_until"] != nil {
		t.Fatalf("unsnooze answer = %s, want done kept and no snooze", r.raw)
	}
	if n := countRunMarks(t, h.st, tenant); n != 1 {
		t.Fatalf("%d mark rows while done remains, want 1", n)
	}
	// And clearing done keeps a snooze.
	if r = h.doJSON("POST", "/v1/m/sessions/runs/run-1/snooze", viewer, snoozeBody(later), tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("snooze again = %d %s", r.code, r.raw)
	}
	for range 2 {
		if r = h.do("DELETE", "/v1/m/sessions/runs/run-1/done", viewer, tenantHdr(tenant)); r.code != http.StatusOK {
			t.Fatalf("undone = %d %s", r.code, r.raw)
		}
	}
	if r.body["done"] != false || r.body["snoozed_until"] == nil {
		t.Fatalf("undone answer = %s, want the snooze kept", r.raw)
	}
	if n := countRunMarks(t, h.st, tenant); n != 1 {
		t.Fatalf("%d mark rows while the snooze remains, want 1", n)
	}
	if r = h.do("DELETE", "/v1/m/sessions/runs/run-1/snooze", viewer, tenantHdr(tenant)); r.code != http.StatusOK ||
		r.body["done"] != false || r.body["snoozed_until"] != nil {
		t.Fatalf("cleared marks answer = %d %s", r.code, r.raw)
	}
	if n := countRunMarks(t, h.st, tenant); n != 0 {
		t.Fatalf("%d mark rows after both were cleared, want 0", n)
	}
	if r = h.do("POST", "/v1/m/sessions/runs/run-1/done", viewer, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("done = %d %s", r.code, r.raw)
	}
	if r = h.do("POST", "/v1/m/sessions/runs/run-1/done", viewer, tenantHdr(tenant)); r.code != http.StatusOK || r.body["done"] != true {
		t.Fatalf("repeat done = %d %s", r.code, r.raw)
	}
	if n := countRunMarks(t, h.st, tenant); n != 1 {
		t.Fatalf("%d mark rows after two dones, want 1", n)
	}
}

// A snooze names a moment still to come: a missing, unreadable or past moment is a
// 400 that stores nothing, and a released session refuses both marks with 409.
func TestRunSnoozeRefusesABadMomentAndAReleasedSession(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "marks-bad")
	viewer := h.viewerToken(admin, tenant, "v@marks.io")
	seedRunRows(t, h.st, tenant, stateRunning, "run-1")
	seedRunRows(t, h.st, tenant, stateCleaned, "run-gone")

	for name, body := range map[string]any{
		"no until":   map[string]any{},
		"empty":      map[string]any{"until": ""},
		"unreadable": map[string]any{"until": "tomorrow"},
		"past":       snoozeBody(time.Now().Add(-time.Minute)),
		"unknown":    map[string]any{"until": time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "for": "1h"},
		// Year 10000 in UTC: a moment the store could keep but never read back.
		"past 9999": map[string]any{"until": "9999-12-31T23:59:59-12:00"},
	} {
		if r := h.doJSON("POST", "/v1/m/sessions/runs/run-1/snooze", viewer, body, tenantHdr(tenant)); r.code != http.StatusBadRequest {
			t.Fatalf("%s: snooze = %d %s, want 400", name, r.code, r.raw)
		}
	}
	if n := countRunMarks(t, h.st, tenant); n != 0 {
		t.Fatalf("%d mark rows after refused snoozes, want 0", n)
	}
	// Clearing a mark that was never set answers 200 and writes no row. Counted after
	// each clear: a row the first wrote would be deleted by the second.
	for _, route := range []string{"snooze", "done"} {
		if r := h.do("DELETE", "/v1/m/sessions/runs/run-1/"+route, viewer, tenantHdr(tenant)); r.code != http.StatusOK {
			t.Fatalf("clear %s with no mark = %d %s, want 200", route, r.code, r.raw)
		}
		if n := countRunMarks(t, h.st, tenant); n != 0 {
			t.Fatalf("%d mark rows after clearing %s with no mark, want 0", n, route)
		}
	}
	if r := h.doJSON("POST", "/v1/m/sessions/runs/run-gone/snooze", viewer, snoozeBody(time.Now().Add(time.Hour)), tenantHdr(tenant)); r.code != http.StatusConflict {
		t.Fatalf("snooze of a released run = %d %s, want 409", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/sessions/runs/run-gone/done", viewer, tenantHdr(tenant)); r.code != http.StatusConflict {
		t.Fatalf("done of a released run = %d %s, want 409", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/sessions/runs/no-such/done", viewer, tenantHdr(tenant)); r.code != http.StatusNotFound {
		t.Fatalf("done of a missing run = %d %s, want 404", r.code, r.raw)
	}
	// Another tenant's member naming this tenant's run finds no such run there.
	other := h.createOrg(admin, "marks-other")
	outsider := h.viewerToken(admin, other, "o@marks.io")
	if r := h.do("POST", "/v1/m/sessions/runs/run-1/done", outsider, tenantHdr(other)); r.code != http.StatusNotFound {
		t.Fatalf("another tenant's done of this run = %d %s, want 404", r.code, r.raw)
	}
	if n := countRunMarks(t, h.st, tenant); n != 0 {
		t.Fatalf("%d mark rows after another tenant's attempt, want 0", n)
	}
}

// A snooze whose moment has passed is no snooze: the read paths stop carrying it,
// so every client shows the session again without a clock of its own.
func TestRunSnoozeEndsAtItsMoment(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "marks-expired")
	viewer := h.viewerToken(admin, tenant, "v@marks.io")
	seedRunRows(t, h.st, tenant, stateRunning, "run-1")

	if r := h.doJSON("POST", "/v1/m/sessions/runs/run-1/snooze", viewer, snoozeBody(time.Now().Add(time.Hour)), tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("snooze = %d %s", r.code, r.raw)
	}
	// The moment passes: move the stored row into the past, as an hour would.
	ctx := context.Background()
	if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runMarkKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(ctx, model.Query{Limit: 1})
		if err != nil || len(recs) != 1 {
			t.Fatalf("mark rows = %d, %v", len(recs), err)
		}
		recs[0][colRunMarkSnoozedUntil] = model.NewTimestamp(time.Now().Add(-time.Second)).String()
		_, err = repo.Update(ctx, recs[0])
		return err
	}); err != nil {
		t.Fatal(err)
	}
	r := h.do("GET", "/v1/m/sessions/runs", viewer, tenantHdr(tenant))
	if item, ok := runItemsOf(t, r)["run-1"]; r.code != http.StatusOK || !ok || item["snoozed_until"] != nil {
		t.Fatalf("an ended snooze is still listed: %d %s", r.code, r.raw)
	}
	if r = h.do("GET", "/v1/m/sessions/runs/run-1", viewer, tenantHdr(tenant)); r.code != http.StatusOK || r.body["snoozed_until"] != nil {
		t.Fatalf("an ended snooze is still read: %d %s", r.code, r.raw)
	}
}

// Releasing or deleting a session drops every person's marks on it, through the
// real cleanup and delete paths, the way it drops their pins.
func TestRunMarksLeaveWhenTheSessionIsReleasedOrDeleted(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "marks-life")
	viewer := h.viewerToken(admin, tenant, "v@marks.io")
	colleague := h.viewerToken(admin, tenant, "w@marks.io")
	seedRunRows(t, h.st, tenant, stateStopped, "run-old", "run-del")
	for _, ref := range []string{"run-old", "run-del"} {
		for _, tok := range []string{viewer, colleague} {
			if r := h.do("POST", "/v1/m/sessions/runs/"+ref+"/done", tok, tenantHdr(tenant)); r.code != http.StatusOK {
				t.Fatalf("done %s = %d %s", ref, r.code, r.raw)
			}
		}
	}

	ctx := context.Background()
	if _, err := m.cleanupRunWith(ctx, tenant, "run-old", "user:u1", model.ActorUser, cleanupOptions{}); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if n := countRunMarks(t, h.st, tenant); n != 2 {
		t.Fatalf("%d mark rows after the release, want the other run's 2", n)
	}
	// The release's state change alone, so the delete is what must drop the rows.
	releaseWithoutPinCleanup(t, m, tenant, "run-del")
	if n := countRunMarks(t, h.st, tenant); n != 2 {
		t.Fatalf("%d mark rows before the delete, want the 2 left behind", n)
	}
	// Rows a crashed release left behind read as no mark, and clearing them is still
	// the same success.
	r := h.do("GET", "/v1/m/sessions/runs/run-del", viewer, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["done"] != false {
		t.Fatalf("a released run reads as marked: %d %s", r.code, r.raw)
	}
	if r = h.do("DELETE", "/v1/m/sessions/runs/run-del/done", viewer, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("undone of a released run = %d %s, want 200", r.code, r.raw)
	}
	if err := m.deleteRun(ctx, tenant, "run-del", "test", model.ActorSystem); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if n := countRunMarks(t, h.st, tenant); n != 0 {
		t.Fatalf("%d mark rows after delete, want 0", n)
	}
}

// A member confined to one workspace keeps the run list they had and marks the runs
// of their workspace; a run of another workspace stays absent.
func TestRunMarksWorkForAWorkspaceConfinedMember(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "marks-confined")
	workspaceA := workAPICreateWorkspace(t, h, tenant, "marks-a")
	workspaceB := workAPICreateWorkspace(t, h, tenant, "marks-b")
	seedRunIn(t, h.st, tenant, "run-a", workspaceA)
	seedRunIn(t, h.st, tenant, "run-b", workspaceB)
	confined := workAPIRoleTokenIn(t, h, admin, tenant, auth.RoleViewer, "confined@marks.io", workspaceA)

	if r := h.do("POST", "/v1/m/sessions/runs/run-a/done", confined, tenantHdr(tenant)); r.code != http.StatusOK || r.body["done"] != true {
		t.Fatalf("confined done of their run = %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/sessions/runs/run-b/done", confined, tenantHdr(tenant)); r.code != http.StatusNotFound {
		t.Fatalf("confined done of another workspace's run = %d %s, want 404", r.code, r.raw)
	}
	r := h.do("GET", "/v1/m/sessions/runs", confined, tenantHdr(tenant))
	if items := runItemsOf(t, r); r.code != http.StatusOK || len(items) != 1 || items["run-a"]["done"] != true {
		t.Fatalf("confined list = %d %s, want only run-a, done", r.code, r.raw)
	}
}
