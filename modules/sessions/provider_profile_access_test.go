// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Who may use which AI tool, account and model path (#2420). The decision is the
// source-scoping bindings' (modules/sourcescope, tested there with real departments and
// groups); these tests fix what this module does with the answer, on every route that
// shows or starts a profile.

// fakeProfileAccess answers from a deny list of profile references and "tool:<driver>"
// entries, and keeps what it was asked.
type fakeProfileAccess struct {
	mu     sync.Mutex
	denied map[string]bool
	// limited are profile references and "tool:<driver>" entries some assignment limits,
	// whoever asks (a denied entry is limited too).
	limited map[string]bool
	err     error
	asked   [][2]string
	// who is who each question was asked for: the tenant and the person.
	askedFor []askedFor
}

type askedFor struct {
	tenant model.TenantID
	user   model.ID
}

func (f *fakeProfileAccess) deny(keys ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.denied = map[string]bool{}
	for _, k := range keys {
		f.denied[k] = true
	}
}

func (f *fakeProfileAccess) MayUse(_ context.Context, tenant model.TenantID, principal auth.Principal, profileRef, driver string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, [2]string{profileRef, driver})
	f.askedFor = append(f.askedFor, askedFor{tenant: tenant, user: principal.UserID})
	if f.err != nil {
		return false, f.err
	}
	return !f.denied[profileRef] && !f.denied["tool:"+driver], nil
}

func (f *fakeProfileAccess) limit(keys ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.limited = map[string]bool{}
	for _, k := range keys {
		f.limited[k] = true
	}
}

func (f *fakeProfileAccess) Limited(_ context.Context, _ model.TenantID, _ auth.Principal, profileRef, driver string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return false, f.err
	}
	return f.limited[profileRef] || f.limited["tool:"+driver] || f.denied[profileRef] || f.denied["tool:"+driver], nil
}

func (f *fakeProfileAccess) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeProfileAccess) timesAskedAbout(profileRef string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, a := range f.asked {
		if a[0] == profileRef {
			n++
		}
	}
	return n
}

// everyQuestionWasAskedFor fails unless every question named the tenant and a person.
func (f *fakeProfileAccess) everyQuestionWasAskedFor(t *testing.T, tenant model.TenantID) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.askedFor) == 0 {
		t.Fatal("nothing was asked")
	}
	for i, a := range f.askedFor {
		if a.tenant != tenant || a.user.IsZero() {
			t.Fatalf("question %d (%v) was asked for tenant %q and person %q, want the caller's tenant %q and a person", i, f.asked[i], a.tenant, a.user, tenant)
		}
	}
}

func (f *fakeProfileAccess) askedAbout(profileRef string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.asked {
		if a[0] == profileRef {
			return true
		}
	}
	return false
}

// assignedFixture is the move fixture (two Claude profiles A and B, a running session on A)
// with the assignment port wired.
func assignedFixture(t *testing.T) (*moveFixture, *fakeProfileAccess) {
	t.Helper()
	f := newMoveFixture(t)
	access := &fakeProfileAccess{}
	f.m.rt.ProfileAccess = access
	return f, access
}

func (f *moveFixture) launchUnder(t *testing.T, profile string) resp {
	t.Helper()
	return f.h.doJSON("POST", "/v1/m/sessions/runs", f.admin,
		map[string]any{"transport": "stream-json", "permission_mode": "default", "isolation": "native", "provider_profile_ref": profile}, f.hdr)
}

func (f *moveFixture) get(t *testing.T, path string) resp {
	t.Helper()
	return f.h.doJSON("GET", "/v1/m/sessions/"+path, f.admin, nil, f.hdr)
}

func profileRefs(r resp) []string {
	var out []string
	items, _ := r.body["items"].([]any)
	for _, it := range items {
		m, _ := it.(map[string]any)
		for _, key := range []string{"profile_ref", "account_ref"} {
			if ref, ok := m[key].(string); ok {
				out = append(out, ref)
			}
		}
	}
	return out
}

func hasRef(refs []string, ref string) bool {
	for _, r := range refs {
		if r == ref {
			return true
		}
	}
	return false
}

func TestProfileAccess_LaunchUnderAnUnassignedProfileIsRefusedBeforeAnySpawn(t *testing.T) {
	f, access := assignedFixture(t)
	runs, spawned := f.runCount(t), launchCount(f.fr)

	access.deny(f.b)
	r := f.launchUnder(t, f.b)
	if r.code != http.StatusForbidden || !strings.Contains(r.raw, "request access from a department admin or owner") {
		t.Fatalf("launch under an unassigned profile = %d %s, want 403 with the request-access route", r.code, r.raw)
	}
	if f.runCount(t) != runs || launchCount(f.fr) != spawned {
		t.Fatalf("a refused launch left a run row (%d -> %d) or spawned a child (%d -> %d)", runs, f.runCount(t), spawned, launchCount(f.fr))
	}
	if !access.askedAbout(f.b) {
		t.Fatalf("the launch never asked about the profile it ran under: %v", access.asked)
	}

	access.deny(f.a) // the other profile is the unassigned one now
	if r := f.launchUnder(t, f.b); r.code != http.StatusCreated {
		t.Fatalf("launch under the assigned profile = %d %s, want 201", r.code, r.raw)
	}
}

func TestProfileAccess_AnUnassignedToolRefusesEveryProfileOfIt(t *testing.T) {
	f, access := assignedFixture(t)
	access.deny("tool:claude")
	if r := f.launchUnder(t, f.a); r.code != http.StatusForbidden {
		t.Fatalf("launch of an unassigned tool = %d %s, want 403", r.code, r.raw)
	}
	if r := f.h.doJSON("POST", "/v1/m/sessions/provider-profiles/resolve", f.admin, map[string]any{"driver": "claude"}, f.hdr); r.code != http.StatusForbidden {
		t.Fatalf("resolving an unassigned tool = %d %s, want 403", r.code, r.raw)
	}
	if r := f.get(t, "provider-profiles/resolve?driver=claude"); r.code != http.StatusForbidden {
		t.Fatalf("previewing an unassigned tool = %d %s, want 403", r.code, r.raw)
	}
}

func TestProfileAccess_AnUnreadableAssignmentIsNeverOpen(t *testing.T) {
	f, access := assignedFixture(t)
	runs := f.runCount(t)
	if r := f.h.doJSON("POST", "/v1/m/sessions/provider-accounts/"+f.a+"/adopt", f.admin, map[string]any{}, f.hdr); r.code != http.StatusOK {
		t.Fatalf("adopt = %d %s", r.code, r.raw)
	}
	access.setErr(errors.New("store unavailable"))
	if r := f.launchUnder(t, f.b); r.code != http.StatusServiceUnavailable {
		t.Fatalf("a launch whose assignment could not be read = %d %s, want 503", r.code, r.raw)
	}
	if f.runCount(t) != runs {
		t.Fatal("a launch whose assignment could not be read left a run row")
	}
	for _, path := range []string{"provider-profiles", "provider-accounts", "provider-profiles/" + f.b, "provider-profiles/readiness"} {
		if r := f.get(t, path); r.code != http.StatusServiceUnavailable {
			t.Errorf("GET %s with an unreadable assignment = %d %s, want 503 (never the profile, never 404)", path, r.code, r.raw)
		}
	}
	// An unknown tool keeps its own answer with the port wired.
	access.setErr(nil)
	if r := f.h.doJSON("POST", "/v1/m/sessions/provider-profiles/resolve", f.admin, map[string]any{"driver": "nope"}, f.hdr); r.code != http.StatusBadRequest {
		t.Errorf("resolving an unknown tool = %d %s, want 400", r.code, r.raw)
	}
}

func TestProfileAccess_ListsAndReadsShowOnlyWhatIsAssigned(t *testing.T) {
	f, access := assignedFixture(t)
	for _, ref := range []string{f.a, f.b} {
		if r := f.h.doJSON("POST", "/v1/m/sessions/provider-accounts/"+ref+"/adopt", f.admin, map[string]any{}, f.hdr); r.code != http.StatusOK {
			t.Fatalf("adopt %s = %d %s", ref, r.code, r.raw)
		}
	}
	if refs := profileRefs(f.get(t, "provider-profiles")); !hasRef(refs, f.a) || !hasRef(refs, f.b) {
		t.Fatalf("baseline: both profiles are listed, got %v", refs)
	}

	access.deny(f.a)
	profiles := profileRefs(f.get(t, "provider-profiles"))
	if hasRef(profiles, f.a) || !hasRef(profiles, f.b) {
		t.Fatalf("profiles listed = %v, want only the assigned %s", profiles, f.b)
	}
	accounts := profileRefs(f.get(t, "provider-accounts"))
	if hasRef(accounts, f.a) || !hasRef(accounts, f.b) {
		t.Fatalf("accounts listed = %v, want only the assigned %s", accounts, f.b)
	}
	for _, path := range []string{
		"provider-profiles/" + f.a, "provider-profiles/" + f.a + "/models", "provider-profiles/" + f.a + "/host-tools",
		"provider-profiles/" + f.a + "/launch-readiness", "provider-profiles/" + f.a + "/configuration", "provider-accounts/" + f.a,
	} {
		if r := f.get(t, path); r.code != http.StatusNotFound {
			t.Errorf("GET %s = %d %s, want 404: an unassigned profile does not exist for the caller", path, r.code, r.raw)
		}
	}
	if r := f.h.doJSON("PATCH", "/v1/m/sessions/provider-profiles/"+f.a, f.admin, map[string]any{"display_name": "Renamed"}, f.hdr); r.code != http.StatusNotFound {
		t.Errorf("PATCH of an unassigned profile = %d %s, want 404", r.code, r.raw)
	}
	if r := f.get(t, "provider-profiles/"+f.b); r.code != http.StatusOK {
		t.Errorf("GET of the assigned profile = %d %s, want 200", r.code, r.raw)
	}
}

func TestProfileAccess_ToolsReadinessListsOnlyAssignedTools(t *testing.T) {
	f, access := assignedFixture(t)
	tools := func() []string {
		r := f.get(t, "provider-profiles/readiness")
		if r.code != http.StatusOK {
			t.Fatalf("readiness = %d %s", r.code, r.raw)
		}
		var out []string
		list, _ := r.body["tools"].([]any)
		for _, it := range list {
			m, _ := it.(map[string]any)
			out = append(out, m["driver"].(string))
		}
		return out
	}
	if !hasRef(tools(), "claude") {
		t.Fatalf("baseline: claude is listed, got %v", tools())
	}
	access.deny("tool:claude")
	if hasRef(tools(), "claude") {
		t.Fatalf("an unassigned tool is listed: %v", tools())
	}
}

func TestProfileAccess_AResumeFollowsTheAssignmentAtThatMoment(t *testing.T) {
	f, access := assignedFixture(t)
	f.stop(t)
	access.deny(f.a)
	r := f.post(t, "resume", nil)
	if r.code != http.StatusForbidden || !strings.Contains(r.raw, "not assigned") {
		t.Fatalf("resume after the assignment was withdrawn = %d %s, want 403", r.code, r.raw)
	}
	if got := f.stateOf(t); got != stateStopped {
		t.Fatalf("a refused resume left the session %s, want %s", got, stateStopped)
	}
	access.deny()
	if r := f.post(t, "resume", nil); r.code != http.StatusOK {
		t.Fatalf("resume once assigned again = %d %s, want 200", r.code, r.raw)
	}
}

func TestProfileAccess_AMoveToAnUnassignedProfileChangesNothing(t *testing.T) {
	f, access := assignedFixture(t)
	f.stop(t)
	access.deny(f.b)
	r := f.post(t, "move", map[string]any{"provider_profile_ref": f.b})
	if r.code != http.StatusForbidden || !strings.Contains(r.raw, "not assigned") {
		t.Fatalf("move to an unassigned profile = %d %s, want 403", r.code, r.raw)
	}
	if d, _ := f.m.getRun(context.Background(), f.tenant, f.runRef); d.ProviderProfileRef != f.a {
		t.Fatalf("a refused move rebound the session to %s, want %s", d.ProviderProfileRef, f.a)
	}
}

func TestProfileAccess_AHandoffToAnUnassignedProfileDoesNotStopTheSession(t *testing.T) {
	f, access := assignedFixture(t)
	runs := f.runCount(t)
	access.deny(f.b)
	r := f.post(t, "handoff", map[string]any{"provider_profile_ref": f.b})
	if r.code != http.StatusForbidden || !strings.Contains(r.raw, "not assigned") {
		t.Fatalf("handoff to an unassigned profile = %d %s, want 403", r.code, r.raw)
	}
	if got := f.stateOf(t); got != stateRunning {
		t.Fatalf("a refused handoff stopped the source session (%s)", got)
	}
	if f.runCount(t) != runs || len(attachmentsIn(t, f.homeB)) != 0 {
		t.Fatal("a refused handoff started a session or wrote an attachment")
	}
}

// Every route that shows or changes one profile, and the tool half of it: refused for a
// profile the caller was not assigned and for a profile of a tool it was not assigned, and
// each refusal leaves the profile as it was. The fake tells the profile from the tool, so a
// route that asks about the wrong one, or about the wrong person or tenant, is caught.
func TestProfileAccess_EveryRouteOnOneProfileFollowsBothTheProfileAndTheTool(t *testing.T) {
	for _, deny := range []struct {
		name string
		keys func(f *moveFixture) []string
	}{
		{"the profile", func(f *moveFixture) []string { return []string{f.a} }},
		{"the tool", func(*moveFixture) []string { return []string{"tool:claude"} }},
	} {
		t.Run(deny.name, func(t *testing.T) {
			f, access := assignedFixture(t)
			if r := f.h.doJSON("POST", "/v1/m/sessions/provider-accounts/"+f.a+"/adopt", f.admin, map[string]any{"name": "first"}, f.hdr); r.code != http.StatusOK {
				t.Fatalf("adopt = %d %s", r.code, r.raw)
			}
			access.deny(deny.keys(f)...)
			for _, route := range []struct {
				method, path string
				body         any
			}{
				{"GET", "provider-profiles/" + f.a, nil},
				{"GET", "provider-profiles/" + f.a + "/models", nil},
				{"GET", "provider-profiles/" + f.a + "/host-tools", nil},
				{"GET", "provider-profiles/" + f.a + "/launch-readiness", nil},
				{"GET", "provider-profiles/" + f.a + "/configuration", nil},
				{"PATCH", "provider-profiles/" + f.a, map[string]any{"display_name": "Renamed"}},
				{"POST", "provider-profiles/" + f.a + "/retire", map[string]any{}},
				{"GET", "provider-accounts/" + f.a, nil},
				{"PATCH", "provider-accounts/" + f.a, map[string]any{"name": "second"}},
				{"POST", "provider-accounts/" + f.a + "/adopt", map[string]any{"name": "third"}},
			} {
				r := f.h.doJSON(route.method, "/v1/m/sessions/"+route.path, f.admin, route.body, f.hdr)
				if r.code != http.StatusNotFound {
					t.Errorf("%s %s with %s denied = %d %s, want 404", route.method, route.path, deny.name, r.code, r.raw)
				}
			}
			// Nothing was changed by the refusals.
			access.deny()
			got := f.get(t, "provider-profiles/"+f.a)
			if got.body["state"] != ProfileActive || got.body["display_name"] != "Account A" {
				t.Errorf("a refused route changed the profile: %s", got.raw)
			}
			if acct := f.get(t, "provider-accounts/"+f.a); acct.body["name"] != "first" {
				t.Errorf("a refused route renamed or re-adopted the account: %s", acct.raw)
			}
			access.everyQuestionWasAskedFor(t, f.tenant)
		})
	}
}

// A caller not assigned a tool cannot make a profile or an account for it either: it
// would be created, listed to nobody who made it and usable by none.
func TestProfileAccess_AnUnassignedToolCannotBeGivenAProfileOrAnAccount(t *testing.T) {
	f, access := assignedFixture(t)
	access.deny("tool:claude")
	home, userHome := t.TempDir(), t.TempDir()
	if r := f.h.doJSON("POST", "/v1/m/sessions/provider-profiles", f.admin,
		map[string]any{"driver": "claude", "config_home": home, "user_home": userHome, "display_name": "Nope"}, f.hdr); r.code != http.StatusForbidden {
		t.Fatalf("create a profile of an unassigned tool = %d %s, want 403", r.code, r.raw)
	}
	if r := f.h.doJSON("POST", "/v1/m/sessions/provider-accounts", f.admin, map[string]any{"driver": "claude", "name": "nope"}, f.hdr); r.code != http.StatusForbidden {
		t.Fatalf("create an account of an unassigned tool = %d %s, want 403", r.code, r.raw)
	}
	access.deny()
	if refs := profileRefs(f.get(t, "provider-profiles")); len(refs) != 2 {
		t.Fatalf("a refused create left %v, want the two profiles of the fixture", refs)
	}
}

// A move with a reference that is no profile reference keeps its own 400 with the port wired.
func TestProfileAccess_AMoveWithoutAProfileReferenceKeepsItsOwnAnswer(t *testing.T) {
	f, _ := assignedFixture(t)
	f.stop(t)
	if r := f.post(t, "move", map[string]any{"provider_profile_ref": ""}); r.code != http.StatusBadRequest {
		t.Fatalf("move without a profile = %d %s, want 400", r.code, r.raw)
	}
}

// A page the caller can use nothing on is not returned empty with more behind it.
func TestProfileAccess_AListDoesNotEndOnAnEmptyPageWithMoreBehindIt(t *testing.T) {
	f, access := assignedFixture(t)
	first := f.get(t, "provider-profiles?limit=1")
	if first.code != http.StatusOK || len(profileRefs(first)) != 1 {
		t.Fatalf("baseline page = %d %s", first.code, first.raw)
	}
	denied := profileRefs(first)[0]
	assigned := f.a
	if denied == f.a {
		assigned = f.b
	}
	access.deny(denied)
	page := f.get(t, "provider-profiles?limit=1")
	if got := profileRefs(page); len(got) != 1 || got[0] != assigned {
		t.Fatalf("the first page of an unassigned profile's turn = %v (%s), want the assigned %s", got, page.raw, assigned)
	}
}

// A launch that waited for an approval asks again, for the person it restores, when the
// approval arrives: an assignment withdrawn in between stops the launch before it spawns.
func TestProfileAccess_AQueuedLaunchAsksAgainForTheRestoredLauncher(t *testing.T) {
	gate := &controlledLaunchApproval{}
	runner := &fakeRunner{initSID: "queued-assigned"}
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(runner), WithCredentialSource(staticCred()), WithLaunchGate(gate))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "queued-assigned")
	if r := h.doJSON("POST", "/v1/users", admin, map[string]any{"email": "queued@fh.invalid", "password": "synthetic-password1", "tenant": tenant.String(), "role": auth.RoleAdmin}, nil); r.code != http.StatusCreated {
		t.Fatalf("tenant admin = %d %s", r.code, r.raw)
	}
	login := h.doJSON("POST", "/v1/auth/login", "", map[string]any{"email": "queued@fh.invalid", "password": "synthetic-password1"}, nil)
	token, _ := login.body["token"].(string)
	issuer := auth.NewAuthenticator(h.st, nil)
	m.QueuedCredentialCapture = issuer.BindQueuedCredential
	m.QueuedLaunchAuthorization = func(ctx context.Context, tenant model.TenantID, credential auth.QueuedCredential, runID string, workspace model.ID) (auth.Principal, error) {
		return issuer.RevalidateQueuedCredential(ctx, credential)
	}
	profile := ensureRuntimeTestProfileRef(t, m, tenant)
	access := &fakeProfileAccess{}
	m.rt.ProfileAccess = access

	queued := h.doJSON("POST", "/v1/m/sessions/runs", token, map[string]any{"provider_profile_ref": profile, "name": "queued assigned launch"}, tenantHdr(tenant))
	if queued.code != http.StatusAccepted || launchCount(runner) != 0 {
		t.Fatalf("queued = %d %s", queued.code, queued.raw)
	}
	if n := access.timesAskedAbout(profile); n != 1 {
		t.Fatalf("the launch asked %d times before its approval, want 1", n)
	}
	access.deny(profile)
	gate.approved.Store(true)
	waitFor(t, "the approved launch to ask again", func() bool { return access.timesAskedAbout(profile) >= 2 })
	if launchCount(runner) != 0 {
		t.Fatal("a launch whose assignment was withdrawn while it waited still spawned")
	}
	access.everyQuestionWasAskedFor(t, tenant)
}

func setProfileRecord(t *testing.T, f *moveFixture, profileRef, record string) {
	t.Helper()
	ctx := context.Background()
	if err := f.m.Data.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(providerProfileKind)
		if err != nil {
			return err
		}
		rec, err := findProfileRec(ctx, sc, profileRef)
		if err != nil {
			return err
		}
		rec[colPPProviderRecordRef] = record
		_, err = repo.Update(ctx, rec)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// Asking for "the engine's choice" is a way to start on a profile too: the launch with no
// profile, the resolve rule and its preview all end on the profile the rule would reuse, and
// the tool being open does not open that profile.
func TestProfileAccess_TheEnginesChoiceOfAnUnassignedProfileIsRefused(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fr := &fakeRunner{initSID: "sess-default"}
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(fr), WithCredentialSource(staticCred()), WithProviderSecretVault(newFakeVault()))
	m.UseExecutionEnvironmentRef(testEnvRef)
	m.UseProfileHomesRoot(t.TempDir())
	m.UseToolLoginsRoot(t.TempDir())
	stub := &loginStub{installed: map[string]bool{"claude": true}, signedIn: map[string]bool{"claude": true}}
	m.ToolLogin = stub.status
	access := &fakeProfileAccess{}
	m.rt.ProfileAccess = access
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	first := h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{}, hdr)
	if first.code != http.StatusCreated {
		t.Fatalf("launch with no profile = %d %s, want 201", first.code, first.raw)
	}
	chosen := first.body["provider_profile_ref"].(string)

	access.deny(chosen) // the tool stays open: only the profile the rule would reuse is not the caller's
	spawned := launchCount(fr)
	for _, ask := range []struct {
		name, method, path string
		body               any
	}{
		{"a launch with no profile", "POST", "/v1/m/sessions/runs", map[string]any{}},
		{"resolve", "POST", "/v1/m/sessions/provider-profiles/resolve", map[string]any{"driver": "claude"}},
		{"resolve of that account", "POST", "/v1/m/sessions/provider-profiles/resolve", map[string]any{"driver": "claude", "account": chosen}},
		{"the preview", "GET", "/v1/m/sessions/provider-profiles/resolve?driver=claude", nil},
	} {
		r := h.doJSON(ask.method, ask.path, admin, ask.body, hdr)
		if r.code != http.StatusForbidden || !strings.Contains(r.raw, "not assigned") {
			t.Errorf("%s ending on an unassigned profile = %d %s, want 403 not assigned", ask.name, r.code, r.raw)
		}
	}
	if launchCount(fr) != spawned {
		t.Fatal("a refused launch spawned a child")
	}
	access.deny()
	if r := h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{}, hdr); r.code != http.StatusCreated {
		t.Fatalf("launch with no profile once assigned = %d %s, want 201", r.code, r.raw)
	}
}

// The model path stays with the assignment: a provider record is its own object, and a
// profile made on it, or moved onto it, is open to everyone who may run a session unless an
// assignment is written for it. So a record some assignment limits is shared (a profile made
// or re-pointed on it) by a profile administrator only, also when the caller may use the
// profile that already runs on it.
func TestProfileAccess_AKeyAnAssignmentLimitsIsSharedByProfileAdministratorsOnly(t *testing.T) {
	f, access := assignedFixture(t)
	setProfileRecord(t, f, f.a, "prv_finance")
	editor := f.h.userToken(f.admin, f.tenant, "editor@acme.invalid", auth.RoleEditor)
	home, userHome := t.TempDir(), t.TempDir()
	createOn := func(token, ref string) resp {
		return f.h.doJSON("POST", "/v1/m/sessions/provider-profiles", token, map[string]any{
			"driver": "claude", "config_home": home, "user_home": userHome, "display_name": "On finance's key",
			"auth_source": "managed_injection", "provider_record_ref": ref,
		}, f.hdr)
	}
	patchTo := func(token, ref string) resp {
		return f.h.doJSON("PATCH", "/v1/m/sessions/provider-profiles/"+f.b, token, map[string]any{"provider_record_ref": ref}, f.hdr)
	}
	create := func(token string) resp { return createOn(token, "prv_finance") }
	patch := func(token string) resp { return patchTo(token, "prv_finance") }
	access.limit(f.a) // assigned to a department; the editor is in it and may use the profile
	// The store trims the reference it keeps, so a padded one is the same key.
	for name, r := range map[string]resp{"create": create(editor), "patch": patch(editor),
		"create with a padded reference": createOn(editor, "  prv_finance "), "patch with a padded reference": patchTo(editor, "\tprv_finance\n")} {
		if r.code != http.StatusForbidden {
			t.Errorf("%s by a person who may use the profile but does not administer them = %d %s, want 403", name, r.code, r.raw)
		}
	}
	if got := f.get(t, "provider-profiles/"+f.b); got.body["provider_record_ref"] == "prv_finance" {
		t.Error("a refused patch re-pointed the profile at the limited key")
	}
	// The administrator is not refused by the assignment (the record itself does not exist in
	// this fixture, which is the next answer and not this gate's).
	for name, r := range map[string]resp{"create": create(f.admin), "patch": patch(f.admin)} {
		if r.code == http.StatusForbidden {
			t.Errorf("%s by a profile administrator was refused by the assignment: %d %s", name, r.code, r.raw)
		}
	}
	// Nothing limits the record: the editor shares it as before assignments existed.
	access.limit()
	for name, r := range map[string]resp{"create": create(editor), "patch": patch(editor)} {
		if r.code == http.StatusForbidden {
			t.Errorf("%s on a record no assignment limits was refused: %d %s", name, r.code, r.raw)
		}
	}
}

// A profile that is retired cannot run anything, so an open one does not make the key it
// ran on usable by people the live profiles of that key were not assigned to.
func TestProfileAccess_ARetiredOpenProfileDoesNotOpenAKey(t *testing.T) {
	f, access := assignedFixture(t)
	setProfileRecord(t, f, f.a, "prv_finance")
	setProfileRecord(t, f, f.b, "prv_finance")
	mc := api.ModuleContext{Tenant: f.tenant, Principal: auth.Principal{Kind: auth.KindUser, UserID: model.NewID()}}
	access.deny(f.a)
	if err := f.m.requireRecordAccess(context.Background(), mc, "prv_finance"); err != nil {
		t.Fatalf("a key an open live profile runs on = %v, want it usable", err)
	}
	ctx := context.Background()
	retireProfile(t, f, f.b)
	if err := f.m.requireRecordAccess(ctx, mc, "prv_finance"); err != ErrProfileNotAssigned {
		t.Fatalf("a key whose only open profile is retired = %v, want the refusal", err)
	}
	// Sharing the key is another question: a retired profile that an assignment limited
	// still makes it a limited key, so the person who is no administrator does not share it.
	access.deny()
	access.limit(f.b)
	if err := f.m.requireRecordShare(ctx, mc, "prv_finance", ""); err != errKeyLimited {
		t.Fatalf("sharing a key whose limited profile is retired = %v, want the refusal", err)
	}
}

// The tool gate does not make a tool of the profile plane that a profile may be made for
// without it: a driver key that is no session tool cannot be assigned, so nothing limits it
// and the call keeps the answer it had before assignments existed.
func TestProfileAccess_ADriverThatIsNoSessionToolKeepsItsOwnAnswer(t *testing.T) {
	f, _ := assignedFixture(t)
	r := f.h.doJSON("POST", "/v1/m/sessions/provider-profiles", f.admin, map[string]any{
		"driver": "ollama", "config_home": t.TempDir(), "user_home": t.TempDir(), "display_name": "Not a session tool",
	}, f.hdr)
	if r.code != http.StatusCreated {
		t.Fatalf("a profile of a driver that is no session tool = %d %s, want 201 as before", r.code, r.raw)
	}
}

// A refused handoff target tells the caller nothing of the profile: the assignment is asked
// before its state, as for a launch.
func TestProfileAccess_AHandoffToAnUnassignedProfileLearnsNothingOfIt(t *testing.T) {
	f, access := assignedFixture(t)
	if r := f.h.doJSON("PATCH", "/v1/m/sessions/provider-profiles/"+f.b, f.admin, map[string]any{"state": "disabled"}, f.hdr); r.code != http.StatusOK {
		t.Fatalf("disable = %d %s", r.code, r.raw)
	}
	access.deny(f.b)
	r := f.post(t, "handoff", map[string]any{"provider_profile_ref": f.b})
	if r.code != http.StatusForbidden || !strings.Contains(r.raw, "not assigned") || strings.Contains(r.raw, "disabled") {
		t.Fatalf("handoff to an unassigned, disabled profile = %d %s, want 403 not assigned and nothing about its state", r.code, r.raw)
	}
}

// A resolve that has to make a profile on a key shares the key: the new profile is open to
// everyone who may run a session. The person assigned the profile that runs on a key may
// disable it (a right they have on a profile they use), and the rule would then make a new,
// unbound profile on that key, so only a profile administrator gets it.
func TestProfileAccess_AResolveThatMakesAProfileOnALimitedKeyIsSharing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithProviderSecretVault(newFakeVault()))
	m.UseExecutionEnvironmentRef(testEnvRef)
	m.UseProfileHomesRoot(t.TempDir())
	m.UseToolLoginsRoot(t.TempDir())
	stub := &loginStub{installed: map[string]bool{"claude": true}, signedIn: map[string]bool{}} // not signed in: the rule picks the key
	m.ToolLogin = stub.status
	access := &fakeProfileAccess{}
	m.rt.ProfileAccess = access
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)
	addRecord(t, m, tenant, ProviderKindAnthropic, "Finance key", "", "sk-ant-fixture-0123456789")
	editor := h.userToken(admin, tenant, "editor@acme.invalid", auth.RoleEditor)
	resolve := func(token string) resp {
		return h.doJSON("POST", "/v1/m/sessions/provider-profiles/resolve", token, map[string]any{"driver": "claude"}, hdr)
	}
	profileCount := func() int {
		profiles, _, err := m.ListProfiles(context.Background(), tenant, "", model.Query{Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		return len(profiles)
	}

	made := resolve(admin)
	if made.code != http.StatusOK || made.body["created"] != true {
		t.Fatalf("the administrator's first resolve = %d %s, want a profile made on the key", made.code, made.raw)
	}
	assigned := made.body["profile"].(map[string]any)["profile_ref"].(string)
	access.limit(assigned) // assigned to a department; the editor is in it

	if r := resolve(editor); r.code != http.StatusOK || r.body["created"] != false {
		t.Fatalf("the editor's resolve while the assigned profile is active = %d %s, want it reused", r.code, r.raw)
	}
	if r := h.doJSON("PATCH", "/v1/m/sessions/provider-profiles/"+assigned, editor, map[string]any{"state": "disabled"}, hdr); r.code != http.StatusOK {
		t.Fatalf("the editor disabling the profile it is assigned = %d %s, want 200", r.code, r.raw)
	}
	if r := resolve(editor); r.code != http.StatusForbidden {
		t.Fatalf("a resolve that would make a profile on the limited key, by an editor = %d %s, want 403", r.code, r.raw)
	}
	if n := profileCount(); n != 1 {
		t.Fatalf("%d profiles after the refused resolve, want the one that was there", n)
	}
	if r := resolve(admin); r.code != http.StatusOK || r.body["created"] != true {
		t.Fatalf("the administrator's resolve = %d %s, want a profile made", r.code, r.raw)
	}
}

// The answer that says what a tool would run on names the key: its display name, default model
// and model list. Whoever may use no profile on that key is not told, in the preview or in the
// readiness of the tool; once a profile on it is theirs, they are.
func TestProfileAccess_TheKeyAToolWouldRunOnIsNotShownToWhoCannotUseIt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithProviderSecretVault(newFakeVault()))
	m.UseExecutionEnvironmentRef(testEnvRef)
	m.UseProfileHomesRoot(t.TempDir())
	m.UseToolLoginsRoot(t.TempDir())
	stub := &loginStub{installed: map[string]bool{"claude": true}, signedIn: map[string]bool{}} // not signed in: the rule picks the key
	m.ToolLogin = stub.status
	access := &fakeProfileAccess{}
	m.rt.ProfileAccess = access
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)
	addRecord(t, m, tenant, ProviderKindAnthropic, "Finance key", "", "sk-ant-fixture-0123456789")
	editor := h.userToken(admin, tenant, "editor@acme.invalid", auth.RoleEditor)

	made := h.doJSON("POST", "/v1/m/sessions/provider-profiles/resolve", admin, map[string]any{"driver": "claude"}, hdr)
	if made.code != http.StatusOK {
		t.Fatalf("resolve = %d %s", made.code, made.raw)
	}
	onKey := made.body["profile"].(map[string]any)["profile_ref"].(string)
	// The profile on the key is assigned elsewhere and not active, so the preview names the key
	// and no profile.
	if r := h.doJSON("PATCH", "/v1/m/sessions/provider-profiles/"+onKey, admin, map[string]any{"state": "disabled"}, hdr); r.code != http.StatusOK {
		t.Fatalf("disable = %d %s", r.code, r.raw)
	}
	access.deny(onKey)

	preview := h.do("GET", "/v1/m/sessions/provider-profiles/resolve?driver=claude", editor, hdr)
	if preview.code != http.StatusForbidden || strings.Contains(preview.raw, "Finance key") {
		t.Errorf("preview of a key the caller may use no profile on = %d %s, want 403 and no key", preview.code, preview.raw)
	}
	ready := h.do("GET", "/v1/m/sessions/provider-profiles/readiness", editor, hdr)
	if ready.code != http.StatusOK || strings.Contains(ready.raw, "Finance key") || strings.Contains(ready.raw, `"claude"`) {
		t.Errorf("readiness of a tool that would run on a key the caller may use no profile on = %d %s, want the tool left out", ready.code, ready.raw)
	}
	access.deny()
	if ready := h.do("GET", "/v1/m/sessions/provider-profiles/readiness", editor, hdr); ready.code != http.StatusOK || !strings.Contains(ready.raw, "Finance key") {
		t.Errorf("readiness once a profile on the key is theirs = %d %s, want the tool with its key", ready.code, ready.raw)
	}
}

// A reference that names no profile has nothing to hide: every route that acts on one keeps the
// answer it gave before assignments existed, with the port wired or not. A profile that exists
// but is not the caller's answers like an unknown one, on the routes that name an account too.
func TestProfileAccess_AnUnknownReferenceKeepsTheAnswerOfItsRoute(t *testing.T) {
	f, access := assignedFixture(t)
	unknown := newProfileRef()
	source := model.NewID().String()
	type route struct {
		method, path string
		body         any
	}
	routes := func(ref string) []route {
		return []route{
			{"GET", "provider-accounts/" + ref, nil},
			{"PATCH", "provider-accounts/" + ref, map[string]any{"name": "renamed"}},
			{"POST", "provider-accounts/" + ref + "/adopt", map[string]any{"name": "adopted"}},
			{"GET", "provider-profiles/" + ref, nil},
			{"GET", "provider-profiles/" + ref + "/launch-readiness", nil},
			{"GET", "provider-profiles/" + ref + "/host-tools", nil},
			{"GET", "provider-profiles/" + ref + "/models", nil},
			// A binding checks its source revision before it looks the profile up, so an unknown
			// profile with a bad revision answers 400 and must not turn into a 404 first.
			{"POST", "provider-source-bindings", map[string]any{"source_id": source, "source_revision": 0, "profile_ref": ref}},
		}
	}
	ask := func(r route) resp {
		return f.h.doJSON(r.method, "/v1/m/sessions/"+r.path, f.admin, r.body, f.hdr)
	}
	wired := f.m.rt.ProfileAccess
	f.m.rt.ProfileAccess = nil
	before := map[string]resp{}
	for _, r := range routes(unknown) {
		before[r.method+" "+r.path] = ask(r)
	}
	f.m.rt.ProfileAccess = wired
	for _, r := range routes(unknown) {
		got, want := ask(r), before[r.method+" "+r.path]
		if got.code != want.code || got.raw != want.raw {
			t.Errorf("%s %s for an unknown reference with the port wired = %d %s, before assignments existed = %d %s", r.method, r.path, got.code, got.raw, want.code, want.raw)
		}
	}
	// A profile that exists and is not the caller's: the account routes say what they say for an unknown one.
	access.deny(f.a)
	for _, r := range routes(f.a)[:2] {
		got, want := ask(r), before[r.method+" "+strings.Replace(r.path, f.a, unknown, 1)]
		if got.code != want.code || got.raw != want.raw {
			t.Errorf("%s %s for a profile the caller is not assigned = %d %s, want what an unknown reference gets: %d %s", r.method, r.path, got.code, got.raw, want.code, want.raw)
		}
	}
}

// Resolving a named account tells a caller who may not use it nothing of it: not its driver,
// its environment, its state nor its sign-in, which the rule judges after the account is found.
func TestProfileAccess_AResolveOfAnAccountLearnsNothingOfAnUnassignedOne(t *testing.T) {
	f, access := assignedFixture(t)
	if r := f.h.doJSON("PATCH", "/v1/m/sessions/provider-profiles/"+f.b, f.admin, map[string]any{"state": "disabled"}, f.hdr); r.code != http.StatusOK {
		t.Fatalf("disable = %d %s", r.code, r.raw)
	}
	access.deny(f.b)
	r := f.h.doJSON("POST", "/v1/m/sessions/provider-profiles/resolve", f.admin, map[string]any{"driver": "claude", "account": f.b}, f.hdr)
	if r.code != http.StatusForbidden || !strings.Contains(r.raw, "not assigned") || strings.Contains(r.raw, "disabled") {
		t.Fatalf("resolve of an unassigned, disabled account = %d %s, want 403 not assigned and nothing about its state", r.code, r.raw)
	}
	access.deny()
	if r := f.h.doJSON("POST", "/v1/m/sessions/provider-profiles/resolve", f.admin, map[string]any{"driver": "claude", "account": f.b}, f.hdr); r.code != http.StatusConflict {
		t.Fatalf("resolve of the disabled account once it is the caller's = %d %s, want its own 409", r.code, r.raw)
	}
}

// The observation bindings of a profile follow the profile: a profile the caller was not
// assigned has none to list, read, revoke or add to.
func TestProfileAccess_TheBindingsOfAnUnassignedProfileAreHiddenToo(t *testing.T) {
	f, access := assignedFixture(t)
	src := model.NewID()
	resolver := &fakeSourceResolver{sources: map[model.ID]SourceRevision{
		src: {ID: src, Version: 1, Name: "claude-home-a", Tenant: f.tenant.String(), Kind: "claude", EnvironmentRef: testEnvRef},
	}}
	prof, err := f.m.GetProfile(context.Background(), f.tenant, f.a)
	if err != nil {
		t.Fatal(err)
	}
	binding := bindDedicated(t, f.m, f.tenant, resolver, src, 1, prof)

	access.deny(f.a)
	list := f.get(t, "provider-source-bindings")
	if list.code != http.StatusOK || len(list.body["items"].([]any)) != 0 {
		t.Errorf("listing the bindings with the profile unassigned = %d %s, want none", list.code, list.raw)
	}
	for _, route := range []struct {
		name, method, path string
		body               any
	}{
		{"read", "GET", "provider-source-bindings/" + binding.Ref, nil},
		{"revoke", "POST", "provider-source-bindings/" + binding.Ref + "/revoke", map[string]any{}},
		{"create", "POST", "provider-source-bindings", map[string]any{"source_id": src.String(), "source_revision": 1, "profile_ref": f.a}},
	} {
		if r := f.h.doJSON(route.method, "/v1/m/sessions/"+route.path, f.admin, route.body, f.hdr); r.code != http.StatusNotFound {
			t.Errorf("%s of a binding on an unassigned profile = %d %s, want 404", route.name, r.code, r.raw)
		}
	}
	access.deny()
	if got := f.get(t, "provider-source-bindings/"+binding.Ref); got.code != http.StatusOK || got.body["state"] != "active" {
		t.Errorf("a refused revoke changed the binding: %d %s", got.code, got.raw)
	}
}

// retireProfile retires a profile the way the route does, without the route's own checks.
func retireProfile(t *testing.T, f *moveFixture, profileRef string) {
	t.Helper()
	ctx := context.Background()
	if err := f.m.Data.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(providerProfileKind)
		if err != nil {
			return err
		}
		rec, err := findProfileRec(ctx, sc, profileRef)
		if err != nil {
			return err
		}
		rec[colPPState] = ProfileRetired
		_, err = repo.Update(ctx, rec)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// recordRouteFixture is the move fixture with a vault and a probe, the assignment port wired,
// and three keys in the order the list returns them (oldest first): two a profile the caller
// was not assigned runs on, one open profile runs on, and one no profile runs on.
type recordRouteFixture struct {
	*moveFixture
	access                       *fakeProfileAccess
	probe                        *fakeProbe
	limited, limited2            ProviderRecord
	open, unused                 ProviderRecord
	limitedProfile, thirdProfile string
}

func newRecordRouteFixture(t *testing.T) *recordRouteFixture {
	t.Helper()
	probe := &fakeProbe{result: ProviderProbeResult{Models: []string{"claude-opus-5"}, Detail: "1 models listed"}}
	f := newMoveFixture(t, WithProviderSecretVault(newFakeVault()), WithProviderProbe(probe))
	access := &fakeProfileAccess{}
	f.m.rt.ProfileAccess = access
	r := &recordRouteFixture{moveFixture: f, access: access, probe: probe}
	r.limited = mustCreateRecord(t, f.m, f.tenant, anthropicInput("Finance key"))
	r.limited2 = mustCreateRecord(t, f.m, f.tenant, anthropicInput("Legal key"))
	r.open = mustCreateRecord(t, f.m, f.tenant, anthropicInput("Shared key"))
	r.unused = mustCreateRecord(t, f.m, f.tenant, anthropicInput("Key no profile runs on"))
	setProfileRecord(t, f, f.a, r.limited.Ref)
	setProfileRecord(t, f, f.b, r.open.Ref)
	third := f.h.doJSON("POST", "/v1/m/sessions/provider-profiles", f.admin, map[string]any{
		"driver": "claude", "config_home": t.TempDir(), "user_home": t.TempDir(), "display_name": "Legal's account",
		"auth_source": "managed_injection", "provider_record_ref": r.limited2.Ref,
	}, f.hdr)
	if third.code != http.StatusCreated {
		t.Fatalf("profile on the second limited key = %d %s", third.code, third.raw)
	}
	r.thirdProfile = third.body["profile_ref"].(string)
	r.limitedProfile = f.a
	first, _, err := f.m.ListProviderRecords(context.Background(), f.tenant, "", "", model.Query{Limit: 1})
	if err != nil || len(first) != 1 || first[0].Ref != r.limited.Ref {
		t.Fatalf("the list starts with %v (%v), want the first limited key: the paging cases below depend on that order", first, err)
	}
	return r
}

func (r *recordRouteFixture) ask(method, path string, body any) resp {
	return r.h.doJSON(method, "/v1/m/sessions/providers"+path, r.admin, body, r.hdr)
}

func providerRefsOf(r resp) []string {
	var out []string
	items, _ := r.body["items"].([]any)
	for _, it := range items {
		out = append(out, it.(map[string]any)["provider_ref"].(string))
	}
	return out
}

// The provider record is the key a profile runs on, so its own routes follow the profiles
// that run on it: a record every live profile of which the caller was not assigned does not
// exist for it (list, read, rename or re-point, test, revoke), a record no profile runs on
// yet stays open as before, and an unreadable assignment is a refusal.
func TestProfileAccess_TheRoutesOfAProviderRecordFollowTheProfilesThatRunOnIt(t *testing.T) {
	f := newRecordRouteFixture(t)
	f.access.deny(f.limitedProfile, f.thirdProfile)

	// The list shows what the caller may use, and a page it may use nothing on is not empty:
	// the walk follows the cursor over both hidden keys and finds every other key once.
	if r := f.ask("GET", "", nil); r.code != http.StatusOK || !slices.Equal(providerRefsOf(r), []string{f.open.Ref, f.unused.Ref}) {
		t.Errorf("list = %d %s, want the shared and the unused key, in order, and neither limited one", r.code, r.raw)
	}
	var walked []string
	cursor := ""
	for pages := 0; pages < 10; pages++ {
		page := f.ask("GET", "?limit=1&cursor="+cursor, nil)
		if page.code != http.StatusOK {
			t.Fatalf("page %d = %d %s, want 200", pages, page.code, page.raw)
		}
		if len(providerRefsOf(page)) == 0 && page.body["has_more"] == true {
			t.Errorf("page %d is empty with more behind it: %s, want the next page read until a key is found", pages, page.raw)
		}
		walked = append(walked, providerRefsOf(page)...)
		next, _ := page.body["cursor"].(string)
		if page.body["has_more"] != true || next == "" {
			break
		}
		cursor = next
	}
	if !slices.Equal(walked, []string{f.open.Ref, f.unused.Ref}) {
		t.Errorf("walking the list one key at a time found %v, want the shared and the unused key once each", walked)
	}

	// A hidden key answers like one that does not exist, on every route that names it, for a
	// good body and for one an unknown key answers differently (400s), and with no probe wired.
	unknown := providerRecordRefPrefix + model.NewID().String()
	type route struct {
		name, method, suffix string
		body                 any
	}
	routes := []route{
		{"read", "GET", "", nil},
		{"rename", "PATCH", "", map[string]any{"display_name": "Renamed by a stranger"}},
		{"change its model", "PATCH", "", map[string]any{"default_model": "claude-haiku-5"}},
		{"rename to nothing", "PATCH", "", map[string]any{"display_name": ""}},
		{"a model name no one can use", "PATCH", "", map[string]any{"default_model": "bad\nmodel"}},
		{"rotate the key", "PATCH", "", map[string]any{"api_key": "sk-ant-fixture-9876543210"}},
		{"nothing to change", "PATCH", "", map[string]any{}},
		{"test", "POST", "/test", map[string]any{}},
		{"revoke", "POST", "/revoke", map[string]any{}},
	}
	same := func(label string) {
		for _, hidden := range []ProviderRecord{f.limited, f.limited2} {
			for _, route := range routes {
				got := f.ask(route.method, "/"+hidden.Ref+route.suffix, route.body)
				want := f.ask(route.method, "/"+unknown+route.suffix, route.body)
				if got.code != want.code || got.raw != want.raw {
					t.Errorf("%s%s of %s = %d %s, want what an unknown key gets: %d %s", label, route.name, hidden.DisplayName, got.code, got.raw, want.code, want.raw)
				}
			}
		}
	}
	same("")
	if f.probe.calls != 0 {
		t.Errorf("a refused test reached the provider %d times", f.probe.calls)
	}
	f.m.rt.ProviderProbe = nil
	same("with no probe wired: ")
	f.m.rt.ProviderProbe = f.probe

	f.access.deny()
	for _, hidden := range []ProviderRecord{f.limited, f.limited2} {
		if r := f.ask("GET", "/"+hidden.Ref, nil); r.code != http.StatusOK || r.body["display_name"] != hidden.DisplayName || r.body["state"] != ProviderRecordActive || r.body["default_model"] != "" {
			t.Errorf("a refused write changed %s: %d %s", hidden.DisplayName, r.code, r.raw)
		}
	}
	f.access.deny(f.limitedProfile, f.thirdProfile)

	// What the caller may use works as before, and so does a record no profile runs on.
	for _, rec := range []ProviderRecord{f.open, f.unused} {
		for _, route := range []route{
			{"read", "GET", "", nil},
			{"rename", "PATCH", "", map[string]any{"display_name": "Renamed " + rec.Ref[len(rec.Ref)-4:]}},
			{"test", "POST", "/test", map[string]any{}},
		} {
			if r := f.ask(route.method, "/"+rec.Ref+route.suffix, route.body); r.code != http.StatusOK {
				t.Errorf("%s of %s the caller may use = %d %s, want 200", route.name, rec.DisplayName, r.code, r.raw)
			}
		}
	}

	// A tool nobody assigned the caller hides the keys its profiles run on, with every profile open.
	f.access.deny("tool:claude")
	if r := f.ask("GET", "", nil); r.code != http.StatusOK || !slices.Equal(providerRefsOf(r), []string{f.unused.Ref}) {
		t.Errorf("list with the tool not assigned = %d %s, want only the key no profile runs on", r.code, r.raw)
	}
	f.access.deny(f.limitedProfile, f.thirdProfile)

	// An unreadable assignment is never open, on a read or a write, and a refused write changes nothing.
	f.access.setErr(errors.New("store unavailable"))
	probed := f.probe.calls
	for _, c := range []struct {
		name, method, path string
		body               any
	}{
		{"list", "GET", "", nil},
		{"read of a limited key", "GET", "/" + f.limited.Ref, nil},
		{"read of a shared key", "GET", "/" + f.open.Ref, nil},
		{"rename", "PATCH", "/" + f.open.Ref, map[string]any{"display_name": "Renamed while unreadable"}},
		{"test", "POST", "/" + f.open.Ref + "/test", map[string]any{}},
		{"revoke", "POST", "/" + f.open.Ref + "/revoke", map[string]any{}},
	} {
		if r := f.ask(c.method, c.path, c.body); r.code != http.StatusServiceUnavailable || strings.Contains(r.raw, "Finance key") {
			t.Errorf("%s with the assignment unreadable = %d %s, want 503", c.name, r.code, r.raw)
		}
	}
	f.access.setErr(nil)
	if r := f.ask("GET", "/"+f.open.Ref, nil); r.body["state"] != ProviderRecordActive || strings.Contains(r.raw, "while unreadable") || f.probe.calls != probed {
		t.Errorf("a write refused for an unreadable assignment changed the key or reached the provider: %s (probe calls %d, was %d)", r.raw, f.probe.calls, probed)
	}
	f.access.everyQuestionWasAskedFor(t, f.tenant)
}

// Retiring the profile a department's key runs on does not hand the key to everyone else: a
// retired profile keeps the key from the people it was not assigned to, and never makes a key
// usable. The key stays its people's, and a key whose only profile is open to everyone stays open.
func TestProfileAccess_AKeyWhoseOnlyProfileIsRetiredStaysTheAssignedPeoples(t *testing.T) {
	f := newRecordRouteFixture(t)
	retireProfile(t, f.moveFixture, f.limitedProfile)

	f.access.deny(f.limitedProfile)
	if r := f.ask("GET", "/"+f.limited.Ref, nil); r.code != http.StatusNotFound {
		t.Errorf("read, by a person the retired profile was not assigned to, of the key it ran on = %d %s, want 404", r.code, r.raw)
	}
	if r := f.ask("GET", "", nil); slices.Contains(providerRefsOf(r), f.limited.Ref) {
		t.Errorf("list shows the key of a retired profile to a person it was not assigned to: %s", r.raw)
	}
	f.access.deny()
	if r := f.ask("PATCH", "/"+f.limited.Ref, map[string]any{"display_name": "Finance key 2"}); r.code != http.StatusOK {
		t.Errorf("rename, by the people the retired profile was assigned to, of its key = %d %s, want 200", r.code, r.raw)
	}
	// An open profile that is retired makes the key neither limited nor usable: with it alone, the key is open as before.
	retireProfile(t, f.moveFixture, f.b)
	f.access.deny(f.limitedProfile)
	if r := f.ask("GET", "/"+f.open.Ref, nil); r.code != http.StatusOK {
		t.Errorf("read of a key whose only profile is retired and open = %d %s, want 200", r.code, r.raw)
	}
}

// The models list asks about a provider key through MayUseProviderRecord, which gives the
// answer the key's own routes give; an unreadable assignment is an error and never a yes, and a
// deployment with no assignments shows every key as before.
func TestProfileAccess_TheModelsListAsksTheKeyRuleThroughMayUseProviderRecord(t *testing.T) {
	f := newRecordRouteFixture(t)
	principal := auth.Principal{Kind: auth.KindUser, UserID: model.NewID()}
	may := func(rec ProviderRecord) (bool, error) {
		return f.m.MayUseProviderRecord(context.Background(), f.tenant, principal, rec.Ref)
	}
	f.access.deny(f.limitedProfile)
	for rec, want := range map[string]bool{f.limited.Ref: false, f.open.Ref: true, f.unused.Ref: true} {
		if ok, err := f.m.MayUseProviderRecord(context.Background(), f.tenant, principal, rec); ok != want || err != nil {
			t.Errorf("MayUseProviderRecord(%s) = %v, %v, want %v, nil", rec, ok, err, want)
		}
	}
	f.access.setErr(errors.New("store unavailable"))
	if ok, err := may(f.open); ok || err == nil {
		t.Errorf("MayUseProviderRecord with the assignment unreadable = %v, %v, want an error and not a yes", ok, err)
	}
	f.access.setErr(nil)
	f.m.rt.ProfileAccess = nil
	if ok, err := may(f.limited); !ok || err != nil {
		t.Errorf("MayUseProviderRecord with no assignments wired = %v, %v, want every key shown, as before", ok, err)
	}
}
