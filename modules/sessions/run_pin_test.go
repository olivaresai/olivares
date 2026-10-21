// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// seedRunRows creates run rows through the repository the runtime uses; a list or
// pin read needs no supervised process.
func seedRunRows(t *testing.T, st store.Store, tenant model.TenantID, state string, refs ...string) {
	t.Helper()
	ctx := context.Background()
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		for _, ref := range refs {
			if _, err := repo.Create(ctx, model.Record{colRunRef: ref, colState: state,
				colTransport: string(TransportStreamJSON), colPermissionMode: "default",
				colIsolation: string(IsolationNative), colLastEventSeq: int64(0)}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func runItemsOf(t *testing.T, r resp) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	items, _ := r.body["items"].([]any)
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if ref, ok := m["run_ref"].(string); ok {
			out[ref] = m
		}
	}
	return out
}

func countRunPins(t *testing.T, st store.Store, tenant model.TenantID) int {
	t.Helper()
	var n int
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runPinKind)
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

// TestRunPinIsTheCallersOwn pins and unpins one run over HTTP and proves the mark
// follows the person, not the session: the caller's list names it pinned, a
// colleague's does not, and both directions of the toggle are idempotent.
func TestRunPinIsTheCallersOwn(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "pins-own")
	viewer := h.viewerToken(admin, tenant, "v@pins.io")
	colleague := h.viewerToken(admin, tenant, "w@pins.io")
	seedRunRows(t, h.st, tenant, stateRunning, "run-1", "run-2")

	r := h.do("POST", "/v1/m/sessions/runs/run-1/pin", viewer, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["pinned"] != true {
		t.Fatalf("pin = %d %s", r.code, r.raw)
	}
	if r.body["run_ref"] != "run-1" {
		t.Fatalf("pin run_ref = %v", r.body["run_ref"])
	}
	if at, _ := r.body["pinned_at"].(string); at == "" {
		t.Fatalf("pin answer carries no pinned_at: %s", r.raw)
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
		got, ok := items["run-1"]["pinned"].(bool)
		if !ok {
			t.Fatalf("%s list omits pinned: %s", who, r.raw)
		}
		if want := who == "viewer"; got != want {
			t.Fatalf("%s sees pinned=%v, want %v", who, got, want)
		}
	}

	r = h.do("GET", "/v1/m/sessions/runs/run-1", viewer, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["pinned"] != true {
		t.Fatalf("get pinned run = %d %s", r.code, r.raw)
	}
	r = h.do("GET", "/v1/m/sessions/runs/run-1", colleague, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["pinned"] != false {
		t.Fatalf("colleague get = %d %s", r.code, r.raw)
	}

	r = h.do("DELETE", "/v1/m/sessions/runs/run-1/pin", viewer, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["pinned"] != false {
		t.Fatalf("unpin = %d %s", r.code, r.raw)
	}
	if r = h.do("GET", "/v1/m/sessions/runs", viewer, tenantHdr(tenant)); runItemsOf(t, r)["run-1"]["pinned"] != false {
		t.Fatalf("run stays pinned after unpin: %s", r.raw)
	}

	// Idempotent in both directions: a repeated call answers success, not 409.
	if r = h.do("DELETE", "/v1/m/sessions/runs/run-1/pin", viewer, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("repeat unpin = %d %s", r.code, r.raw)
	}
	if r = h.do("POST", "/v1/m/sessions/runs/run-1/pin", viewer, tenantHdr(tenant)); r.code != http.StatusOK || r.body["pinned"] != true {
		t.Fatalf("re-pin = %d %s", r.code, r.raw)
	}
	if r = h.do("POST", "/v1/m/sessions/runs/run-1/pin", viewer, tenantHdr(tenant)); r.code != http.StatusOK || r.body["pinned"] != true {
		t.Fatalf("repeat pin = %d %s", r.code, r.raw)
	}
}

// TestRunPinCeilingDropsTheOldest keeps the console's rule the server now owns:
// past twenty pins the oldest one leaves, so a twenty-first pin still succeeds.
func TestRunPinCeilingDropsTheOldest(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "pins-cap")
	viewer := h.viewerToken(admin, tenant, "v@pins.io")
	refs := make([]string, maxRunPins+1)
	for i := range refs {
		refs[i] = "run-" + string(rune('a'+i))
	}
	seedRunRows(t, h.st, tenant, stateRunning, refs...)

	for _, ref := range refs {
		if r := h.do("POST", "/v1/m/sessions/runs/"+ref+"/pin", viewer, tenantHdr(tenant)); r.code != http.StatusOK {
			t.Fatalf("pin %s = %d %s", ref, r.code, r.raw)
		}
	}
	r := h.do("GET", "/v1/m/sessions/runs", viewer, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("list = %d %s", r.code, r.raw)
	}
	pinned := 0
	for _, item := range runItemsOf(t, r) {
		if item["pinned"] == true {
			pinned++
		}
	}
	if pinned != maxRunPins {
		t.Fatalf("%d runs pinned, want %d (oldest dropped)", pinned, maxRunPins)
	}
	if runItemsOf(t, r)[refs[0]]["pinned"] != false {
		t.Fatalf("oldest pin %s survived the ceiling", refs[0])
	}
	if n := countRunPins(t, h.st, tenant); n != maxRunPins {
		t.Fatalf("%d pin rows stored, want %d", n, maxRunPins)
	}
}

// TestRunPinRefusals keeps the operate surface's refusal shapes: an absent run is
// a concealed 404 for an authorized caller and stays absent across tenants, and
// an unauthenticated call never reaches the store.
func TestRunPinRefusals(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenantA := h.createOrg(admin, "pins-a")
	tenantB := h.createOrg(admin, "pins-b")
	viewer := h.viewerToken(admin, tenantA, "v@pins.io")
	seedRunRows(t, h.st, tenantA, stateRunning, "run-1")

	if r := h.do("POST", "/v1/m/sessions/runs/nope/pin", viewer, tenantHdr(tenantA)); r.code != http.StatusNotFound {
		t.Fatalf("absent run pin = %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/sessions/runs/run-1/pin", viewer, tenantHdr(tenantB)); r.code != http.StatusNotFound {
		t.Fatalf("cross-tenant pin = %d %s (must look absent)", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/sessions/runs/run-1/pin", "", tenantHdr(tenantA)); r.code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated pin = %d %s", r.code, r.raw)
	}
	if r := h.do("DELETE", "/v1/m/sessions/runs/run-1/pin", "", tenantHdr(tenantA)); r.code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated unpin = %d %s", r.code, r.raw)
	}
	if n := countRunPins(t, h.st, tenantA); n != 0 {
		t.Fatalf("%d pin rows after refusals, want 0", n)
	}
}

// TestRunPinLeavesWhenTheSessionIsReleasedOrDeleted proves the issue's lifecycle
// rule through the real cleanup and delete paths: a released (cleaned) session
// leaves the pinned group even though its row remains, and a deleted session
// leaves no pin rows behind.
func TestRunPinLeavesWhenTheSessionIsReleasedOrDeleted(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "pins-life")
	viewer := h.viewerToken(admin, tenant, "v@pins.io")
	seedRunRows(t, h.st, tenant, stateStopped, "run-old")

	if r := h.do("POST", "/v1/m/sessions/runs/run-old/pin", viewer, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("pin = %d %s", r.code, r.raw)
	}

	ctx := context.Background()
	if _, err := m.cleanupRunWith(ctx, tenant, "run-old", "user:u1", model.ActorUser, cleanupOptions{}); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	r := h.do("GET", "/v1/m/sessions/runs", viewer, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("list after cleanup = %d %s", r.code, r.raw)
	}
	if item, ok := runItemsOf(t, r)["run-old"]; !ok || item["pinned"] == true {
		t.Fatalf("cleaned session stays pinned: %s", r.raw)
	}
	// The read above would also pass on the read-side refusal alone; the row itself
	// must be gone too.
	if n := countRunPins(t, h.st, tenant); n != 0 {
		t.Fatalf("%d pin rows after the release, want 0", n)
	}

	if err := m.deleteRun(ctx, tenant, "run-old"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if n := countRunPins(t, h.st, tenant); n != 0 {
		t.Fatalf("%d pin rows after delete, want 0", n)
	}
}

// TestRunPinReadRefusesAReleasedRunWhoseRowWasLeftBehind is the crash window of a
// release: the state change committed and the pin rows were not removed. The read
// path must still not mark that run pinned, and the release must not read as failed.
func TestRunPinReadRefusesAReleasedRunWhoseRowWasLeftBehind(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "pins-left")
	viewer := h.viewerToken(admin, tenant, "v@pins.io")
	seedRunRows(t, h.st, tenant, stateStopped, "run-left")
	if r := h.do("POST", "/v1/m/sessions/runs/run-left/pin", viewer, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("pin = %d %s", r.code, r.raw)
	}

	// The release's state change alone, without the pin cleanup that follows it.
	if _, err := m.transition(context.Background(), tenant, "run-left", transitionInput{
		event: "cleaned", toState: stateCleaned, detail: "released",
		actor: "user:u1", actorKind: model.ActorUser,
	}); err != nil {
		t.Fatalf("transition: %v", err)
	}
	if n := countRunPins(t, h.st, tenant); n != 1 {
		t.Fatalf("%d pin rows, want the 1 left behind", n)
	}
	r := h.do("GET", "/v1/m/sessions/runs/run-left", viewer, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("get = %d %s", r.code, r.raw)
	}
	if r.body["pinned"] != false {
		t.Fatalf("a released run reads as pinned: %s", r.raw)
	}
}

// releaseWithoutPinCleanup is a release's state change alone, the way a crash between
// it and the pin cleanup leaves it: the run is cleaned and its pin rows remain.
func releaseWithoutPinCleanup(t *testing.T, m *Module, tenant model.TenantID, ref string) {
	t.Helper()
	if _, err := m.transition(context.Background(), tenant, ref, transitionInput{
		event: "cleaned", toState: stateCleaned, detail: "released",
		actor: "user:u1", actorKind: model.ActorUser,
	}); err != nil {
		t.Fatalf("release %s: %v", ref, err)
	}
}

// A released session has left every pinned group for good: pinning it is refused
// rather than answering pinned and reading as unpinned, and no row is written.
func TestRunPinRefusesAReleasedRun(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "pins-released")
	viewer := h.viewerToken(admin, tenant, "v@pins.io")
	seedRunRows(t, h.st, tenant, stateCleaned, "run-gone")

	r := h.do("POST", "/v1/m/sessions/runs/run-gone/pin", viewer, tenantHdr(tenant))
	if r.code != http.StatusConflict {
		t.Fatalf("pin of a released run = %d %s, want 409", r.code, r.raw)
	}
	if n := countRunPins(t, h.st, tenant); n != 0 {
		t.Fatalf("%d pin rows after the refusal, want 0", n)
	}
	// Unpinning stays allowed: it is the state the caller asked for.
	if r := h.do("DELETE", "/v1/m/sessions/runs/run-gone/pin", viewer, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("unpin of a released run = %d %s", r.code, r.raw)
	}
}

// Rows a failed cleanup left behind read as unpinned but would still take slots from
// the twenty: at the ceiling they go first, and no live pin is evicted for them.
func TestRunPinAtTheCeilingDropsLeftoverRowsBeforeALivePin(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "pins-heal")
	viewer := h.viewerToken(admin, tenant, "v@pins.io")
	refs := make([]string, maxRunPins+1)
	for i := range refs {
		refs[i] = "run-" + string(rune('a'+i))
	}
	seedRunRows(t, h.st, tenant, stateStopped, refs...)
	for _, ref := range refs[:maxRunPins] {
		if r := h.do("POST", "/v1/m/sessions/runs/"+ref+"/pin", viewer, tenantHdr(tenant)); r.code != http.StatusOK {
			t.Fatalf("pin %s = %d %s", ref, r.code, r.raw)
		}
	}
	releaseWithoutPinCleanup(t, m, tenant, refs[5])

	if r := h.do("POST", "/v1/m/sessions/runs/"+refs[maxRunPins]+"/pin", viewer, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("21st pin = %d %s", r.code, r.raw)
	}
	r := h.do("GET", "/v1/m/sessions/runs", viewer, tenantHdr(tenant))
	items := runItemsOf(t, r)
	if items[refs[0]]["pinned"] != true {
		t.Fatalf("the oldest live pin %s was evicted for a leftover row: %s", refs[0], r.raw)
	}
	if items[refs[maxRunPins]]["pinned"] != true {
		t.Fatalf("the new pin is not pinned: %s", r.raw)
	}
	if n := countRunPins(t, h.st, tenant); n != maxRunPins {
		t.Fatalf("%d pin rows, want %d (19 live, the new one, no leftover)", n, maxRunPins)
	}
}

// The list page is the newest runs; a pin on an older session still reaches the
// client, flagged, so "first in my list" holds however many sessions came after.
func TestRunPinReachesTheCallerPastTheListWindow(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "pins-window")
	viewer := h.viewerToken(admin, tenant, "v@pins.io")
	colleague := h.viewerToken(admin, tenant, "c@pins.io")
	seedRunRows(t, h.st, tenant, stateRunning, "run-oldest", "run-middle", "run-newest")
	if r := h.do("POST", "/v1/m/sessions/runs/run-oldest/pin", viewer, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("pin = %d %s", r.code, r.raw)
	}

	r := h.do("GET", "/v1/m/sessions/runs?limit=1", viewer, tenantHdr(tenant))
	items := runItemsOf(t, r)
	if _, ok := items["run-newest"]; !ok || items["run-oldest"]["pinned"] != true || len(items) != 2 {
		t.Fatalf("viewer's one-run page = %s, want the newest run plus their pinned oldest", r.raw)
	}
	// The colleague pinned nothing: their page is the page, with nothing added.
	r = h.do("GET", "/v1/m/sessions/runs?limit=1", colleague, tenantHdr(tenant))
	if items := runItemsOf(t, r); len(items) != 1 {
		t.Fatalf("colleague's one-run page = %s, want only the newest run", r.raw)
	}
	// A list narrowed to one session answers for that session alone.
	r = h.do("GET", "/v1/m/sessions/runs?limit=1&claude_session_id=none", viewer, tenantHdr(tenant))
	if items := runItemsOf(t, r); len(items) != 0 {
		t.Fatalf("a narrowed list gained pinned runs: %s", r.raw)
	}
}

// A cursor traversal is the published "every row once, in stable ID order, `limit` per
// page": a pin must not add rows to it, since the traversal reaches every run already.
func TestRunPinDoesNotChangeACursorTraversal(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "pins-cursor")
	viewer := h.viewerToken(admin, tenant, "v@pins-cursor.io")
	seedRunRows(t, h.st, tenant, stateRunning, "run-a", "run-b", "run-c")
	if r := h.do("POST", "/v1/m/sessions/runs/run-c/pin", viewer, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("pin = %d %s", r.code, r.raw)
	}

	var walked []string
	cursor := ""
	for page := 0; page < 5; page++ {
		r := h.do("GET", "/v1/m/sessions/runs?pagination=cursor&limit=1&cursor="+url.QueryEscape(cursor), viewer, tenantHdr(tenant))
		if r.code != http.StatusOK {
			t.Fatalf("page %d = %d %s", page, r.code, r.raw)
		}
		items, _ := r.body["items"].([]any)
		if len(items) > 1 {
			t.Fatalf("page %d has %d rows, limit is 1: %s", page, len(items), r.raw)
		}
		for _, it := range items {
			item := it.(map[string]any)
			ref := item["run_ref"].(string)
			if got := item["pinned"] == true; got != (ref == "run-c") {
				t.Fatalf("%s pinned = %v in a cursor page: %s", ref, got, r.raw)
			}
			walked = append(walked, ref)
		}
		if r.body["has_more"] == false {
			break
		}
		next, _ := r.body["cursor"].(string)
		if next == "" || next == cursor {
			t.Fatal("nonadvancing cursor")
		}
		cursor = next
	}
	if len(walked) != 3 || walked[0] >= walked[1] || walked[1] >= walked[2] {
		t.Fatalf("cursor traversal = %v, want each of the three runs once in ID order", walked)
	}
}

// A list narrowed by state or provider_profile_ref answers the narrowed query alone:
// the caller's pinned runs outside the page ride along on the DEFAULT list only, or a
// state=stopped answer would hold a running run and an account's list another
// account's, because the pin append reads rows the store filter never saw. The pin
// still marks its row when the narrowed page itself reaches it.
func TestRunPinStaysOutOfNarrowedLists(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "pins-narrowed")
	viewer := h.viewerToken(admin, tenant, "v@pins-narrowed.io")
	seedRunRows(t, h.st, tenant, stateRunning, "run-running")
	seedRunRows(t, h.st, tenant, stateStopped, "run-stopped-old", "run-stopped-new")
	seedProfileRunRows(t, h.st, tenant, "ppf_pins-a", "run-acct-a", "run-acct-a2")
	seedProfileRunRows(t, h.st, tenant, "ppf_pins-b", "run-acct-b")
	if r := h.do("POST", "/v1/m/sessions/runs/run-running/pin", viewer, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("pin = %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/sessions/runs/run-acct-a/pin", viewer, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("pin = %d %s", r.code, r.raw)
	}

	// state=stopped: the pinned running run is not part of the stopped answer.
	r := h.do("GET", "/v1/m/sessions/runs?limit=1&state=stopped", viewer, tenantHdr(tenant))
	if items := runItemsOf(t, r); len(items) != 1 || items["run-stopped-new"] == nil {
		t.Fatalf("state=stopped page = %s, want only the newest stopped run, not the pinned running one", r.raw)
	}
	// provider_profile_ref: the pinned run of account A is not in account B's answer.
	r = h.do("GET", "/v1/m/sessions/runs?provider_profile_ref=ppf_pins-b", viewer, tenantHdr(tenant))
	if items := runItemsOf(t, r); len(items) != 1 || items["run-acct-b"] == nil {
		t.Fatalf("account B list = %s, want only run-acct-b, not account A's pinned run", r.raw)
	}
	// The pin still marks its row when the narrowed page reaches it by itself.
	r = h.do("GET", "/v1/m/sessions/runs?provider_profile_ref=ppf_pins-a&limit=2", viewer, tenantHdr(tenant))
	if items := runItemsOf(t, r); len(items) != 2 || items["run-acct-a"]["pinned"] != true || items["run-acct-a2"]["pinned"] != false {
		t.Fatalf("account A list = %s, want both runs, only run-acct-a pinned", r.raw)
	}

	// The derived states narrow the same way: a pinned run that DERIVES to the
	// requested state and sits outside the page is not appended either. (The
	// displayed-state filter after the read would keep such a row, which is why
	// this leg is its own variant and not covered by the stored-state one.)
	seedRunRows(t, h.st, tenant, stateRunning, "run-hot-old", "run-hot-new")
	seedIdleRunRows(t, h.st, tenant, "run-idle-old", "run-idle-new")
	if r := h.do("POST", "/v1/m/sessions/runs/run-hot-old/pin", viewer, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("pin = %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/sessions/runs/run-idle-old/pin", viewer, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("pin = %d %s", r.code, r.raw)
	}
	r = h.do("GET", "/v1/m/sessions/runs?state=running&limit=1", viewer, tenantHdr(tenant))
	if items := runItemsOf(t, r); len(items) != 1 || items["run-hot-new"] == nil {
		t.Fatalf("state=running page = %s, want only the newest running run, not the pinned older one", r.raw)
	}
	r = h.do("GET", "/v1/m/sessions/runs?state=idle&limit=1", viewer, tenantHdr(tenant))
	if items := runItemsOf(t, r); len(items) != 1 || items["run-idle-new"] == nil {
		t.Fatalf("state=idle page = %s, want only the newest idle run, not the pinned older one", r.raw)
	}
}

// seedProfileRunRows is seedRunRows with the run's provider profile set, the column
// the provider_profile_ref filter of the runs list narrows on.
func seedProfileRunRows(t *testing.T, st store.Store, tenant model.TenantID, profile string, refs ...string) {
	t.Helper()
	ctx := context.Background()
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		for _, ref := range refs {
			if _, err := repo.Create(ctx, model.Record{colRunRef: ref, colState: stateRunning,
				colRunProfileID: profile, colTransport: string(TransportStreamJSON), colPermissionMode: "default",
				colIsolation: string(IsolationNative), colLastEventSeq: int64(0)}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// seedIdleRunRows seeds stored-running runs whose last activity is an hour old, past
// the runtime's idle window, so they derive to the displayed state idle.
func seedIdleRunRows(t *testing.T, st store.Store, tenant model.TenantID, refs ...string) {
	t.Helper()
	ctx := context.Background()
	quiet := model.NewTimestamp(time.Now().Add(-time.Hour)).String()
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		for _, ref := range refs {
			if _, err := repo.Create(ctx, model.Record{colRunRef: ref, colState: stateRunning,
				colLastActivityAt: quiet, colTransport: string(TransportStreamJSON), colPermissionMode: "default",
				colIsolation: string(IsolationNative), colLastEventSeq: int64(0)}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// A release or a delete takes EVERY person's pin on the session with it, and a delete
// does it on its own, not by finding the rows already gone.
func TestRunPinReleaseAndDeleteDropEveryPersonsPin(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "pins-everyone")
	viewer := h.viewerToken(admin, tenant, "v@pins.io")
	colleague := h.viewerToken(admin, tenant, "c@pins.io")
	seedRunRows(t, h.st, tenant, stateStopped, "run-rel", "run-del")
	for _, ref := range []string{"run-rel", "run-del"} {
		for _, who := range []string{viewer, colleague} {
			if r := h.do("POST", "/v1/m/sessions/runs/"+ref+"/pin", who, tenantHdr(tenant)); r.code != http.StatusOK {
				t.Fatalf("pin %s = %d %s", ref, r.code, r.raw)
			}
		}
	}
	if n := countRunPins(t, h.st, tenant); n != 4 {
		t.Fatalf("%d pin rows, want 4", n)
	}

	ctx := context.Background()
	if _, err := m.cleanupRunWith(ctx, tenant, "run-rel", "user:u1", model.ActorUser, cleanupOptions{}); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if n := countRunPins(t, h.st, tenant); n != 2 {
		t.Fatalf("%d pin rows after the release, want only run-del's 2", n)
	}

	releaseWithoutPinCleanup(t, m, tenant, "run-del")
	if n := countRunPins(t, h.st, tenant); n != 2 {
		t.Fatalf("%d pin rows before the delete, want the 2 left behind", n)
	}
	if err := m.deleteRun(ctx, tenant, "run-del"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if n := countRunPins(t, h.st, tenant); n != 0 {
		t.Fatalf("%d pin rows after the delete, want 0", n)
	}
}

// seedRunIn seeds one run row in a workspace, the lineage a confined caller reads by.
func seedRunIn(t *testing.T, st store.Store, tenant model.TenantID, ref string, workspace model.ID) {
	t.Helper()
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		_, err = repo.Create(context.Background(), model.Record{colRunRef: ref, colState: stateRunning,
			colTransport: string(TransportStreamJSON), colPermissionMode: "default",
			colIsolation: string(IsolationNative), colLastEventSeq: int64(0),
			colRunAuthzWorkspaceID: workspace.String()})
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// A member confined to one workspace keeps the run list and get they had: the pin
// entity carries the run's lineage, so the confined scope serves it instead of
// refusing it (which would have turned GET /runs into a 403). They pin the runs of
// their workspace and see a run of another as absent.
func TestRunPinWorksForAWorkspaceConfinedMember(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "pins-confined")
	workspaceA := workAPICreateWorkspace(t, h, tenant, "pins-a")
	workspaceB := workAPICreateWorkspace(t, h, tenant, "pins-b")
	seedRunIn(t, h.st, tenant, "run-a", workspaceA)
	seedRunIn(t, h.st, tenant, "run-b", workspaceB)
	confined := workAPIRoleTokenIn(t, h, admin, tenant, auth.RoleViewer, "confined@pins.io", workspaceA)

	r := h.do("GET", "/v1/m/sessions/runs", confined, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("confined list = %d %s", r.code, r.raw)
	}
	if items := runItemsOf(t, r); len(items) != 1 || items["run-a"]["pinned"] != false {
		t.Fatalf("confined list = %s, want only run-a, not pinned", r.raw)
	}
	if r = h.do("POST", "/v1/m/sessions/runs/run-a/pin", confined, tenantHdr(tenant)); r.code != http.StatusOK || r.body["pinned"] != true {
		t.Fatalf("confined pin of their run = %d %s", r.code, r.raw)
	}
	if r = h.do("POST", "/v1/m/sessions/runs/run-b/pin", confined, tenantHdr(tenant)); r.code != http.StatusNotFound {
		t.Fatalf("confined pin of another workspace's run = %d %s, want 404", r.code, r.raw)
	}
	r = h.do("GET", "/v1/m/sessions/runs/run-a", confined, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["pinned"] != true {
		t.Fatalf("confined get = %d %s", r.code, r.raw)
	}
	if r = h.do("DELETE", "/v1/m/sessions/runs/run-a/pin", confined, tenantHdr(tenant)); r.code != http.StatusOK || r.body["pinned"] != false {
		t.Fatalf("confined unpin = %d %s", r.code, r.raw)
	}
}
