// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// #1845: a stopped Claude Code session moves to another profile of the same tool,
// carrying the tool's own session state, and resumes there by the same id.

const (
	moveTestSID     = "5b0c1a6e-1845-4a2b-9c3d-000000000001"
	moveTestProject = "-work-demo-project"
	moveCredential  = `{"claudeAiOauth":{"accessToken":"tok-account-a-must-not-travel"}}`
)

// moveFixture is two signed-in-looking Claude profiles on two real homes with a
// stopped session on the first, whose conversation state lives in A's config home.
type moveFixture struct {
	m      *Module
	h      *harness
	admin  string
	tenant model.TenantID
	fr     *fakeRunner
	a, b   string // profile refs
	homeA  string // A's config home
	homeB  string // B's config home
	userB  string
	runRef string
	hdr    map[string]string
}

// writeFile creates path with content, making parents.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// seedClaudeState lays out what Claude Code writes under a config home for one
// session: the transcript, its per-session directory (subagents, tool results), an
// unrelated session that must stay behind, and the account's own login.
func seedClaudeState(t *testing.T, configHome, sid string) {
	t.Helper()
	proj := filepath.Join(configHome, "projects", moveTestProject)
	writeFile(t, filepath.Join(proj, sid+".jsonl"),
		`{"type":"user","sessionId":"`+sid+`","message":{"role":"user","content":"What is the codename of the migration?"}}`+"\n"+
			`{"type":"assistant","sessionId":"`+sid+`","message":{"role":"assistant","content":[{"type":"text","text":"The codename is BLUE-HERON."}]}}`+"\n")
	writeFile(t, filepath.Join(proj, sid, "subagents", "agent-a1.jsonl"), `{"type":"user","isSidechain":true}`+"\n")
	writeFile(t, filepath.Join(proj, sid, "subagents", "agent-a1.meta.json"), `{"agentType":"Explore"}`)
	writeFile(t, filepath.Join(proj, sid, "tool-results", "r1.txt"), "large tool output")
	writeFile(t, filepath.Join(proj, "unrelated-session.jsonl"), `{"type":"user","message":{"content":"other"}}`+"\n")
	writeFile(t, filepath.Join(configHome, ".credentials.json"), moveCredential)
}

func newMoveFixture(t *testing.T, opts ...Option) *moveFixture {
	t.Helper()
	return newMoveFixtureWith(t, nil, opts...)
}

// newMoveFixtureWith runs beforeRun after the profiles exist and before the
// session is created, for a test whose first launch needs the profile or the
// vault prepared.
func newMoveFixtureWith(t *testing.T, beforeRun func(*moveFixture), opts ...Option) *moveFixture {
	t.Helper()
	fr := &fakeRunner{initSID: moveTestSID}
	m := New(append([]Option{WithSessionWorkspaceRoot(t.TempDir()), WithRunner(fr), WithCredentialSource(staticCred())}, opts...)...)
	m.UseExecutionEnvironmentRef(testEnvRef)
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)
	configA, userA, configB, userB := twoHomes(t)
	mk := func(config, user, name string) string {
		r := h.doJSON("POST", "/v1/m/sessions/provider-profiles", admin,
			map[string]any{"driver": "claude", "config_home": config, "user_home": user, "display_name": name}, hdr)
		if r.code != http.StatusCreated {
			t.Fatalf("profile %s = %d %s", name, r.code, r.raw)
		}
		return r.body["profile_ref"].(string)
	}
	f := &moveFixture{m: m, h: h, admin: admin, tenant: tenant, fr: fr, hdr: hdr, homeA: configA, homeB: configB, userB: userB}
	f.a, f.b = mk(configA, userA, "Account A"), mk(configB, userB, "Account B")
	if beforeRun != nil {
		beforeRun(f)
	}
	r := h.doJSON("POST", "/v1/m/sessions/runs", admin,
		map[string]any{"transport": "stream-json", "permission_mode": "default", "isolation": "native", "provider_profile_ref": f.a}, hdr)
	if r.code != http.StatusCreated {
		t.Fatalf("launch = %d %s", r.code, r.raw)
	}
	f.runRef = r.body["run_ref"].(string)
	waitFor(t, "session id capture", func() bool {
		d, _ := m.getRun(context.Background(), tenant, f.runRef)
		return d.ClaudeSessionID == moveTestSID
	})
	seedClaudeState(t, configA, moveTestSID)
	return f
}

func (f *moveFixture) post(t *testing.T, action string, body any) resp {
	t.Helper()
	return f.h.doJSON("POST", "/v1/m/sessions/runs/"+f.runRef+"/"+action, f.admin, body, f.hdr)
}

func (f *moveFixture) stop(t *testing.T) {
	t.Helper()
	if r := f.post(t, "stop", nil); r.code != http.StatusOK {
		t.Fatalf("stop = %d %s", r.code, r.raw)
	}
}

func (f *moveFixture) events(t *testing.T) []map[string]any {
	t.Helper()
	r := f.h.doJSON("GET", "/v1/m/sessions/runs/"+f.runRef+"/events", f.admin, nil, f.hdr)
	if r.code != http.StatusOK {
		t.Fatalf("events = %d %s", r.code, r.raw)
	}
	var out []map[string]any
	for _, it := range r.body["items"].([]any) {
		out = append(out, it.(map[string]any))
	}
	return out
}

// The move carries the conversation state and nothing of the account, rebinds the
// SAME Olivares session to the target profile, records who moved it from where, and
// the resume then continues the same provider conversation under the target's home.
func TestMoveRun_CarriesStateAndResumesUnderTargetProfile(t *testing.T) {
	f := newMoveFixture(t)
	ctx := context.Background()
	f.stop(t)
	before, err := f.m.loadRun(ctx, f.tenant, f.runRef)
	if err != nil {
		t.Fatal(err)
	}
	sidBefore := before.String(colRunClaimSID)
	launches := launchCount(f.fr)

	r := f.post(t, "move", map[string]any{"provider_profile_ref": f.b})
	if r.code != http.StatusOK {
		t.Fatalf("move = %d %s", r.code, r.raw)
	}
	if r.body["run_ref"] != f.runRef || r.body["provider_profile_ref"] != f.b || r.body["state"] != stateStopped {
		t.Fatalf("moved run = %s", r.raw)
	}
	if r.body["claude_session_id"] != moveTestSID {
		t.Fatalf("the provider conversation id must not change: %s", r.raw)
	}
	if strings.Contains(r.raw, f.homeA) || strings.Contains(r.raw, f.homeB) {
		t.Fatalf("move response leaks a home: %s", r.raw)
	}
	if launchCount(f.fr) != launches {
		t.Fatal("a move starts no process")
	}

	// The tool's own state arrived byte for byte, under the same project directory.
	proj := filepath.Join(f.homeB, "projects", moveTestProject)
	for _, rel := range []string{
		moveTestSID + ".jsonl",
		filepath.Join(moveTestSID, "subagents", "agent-a1.jsonl"),
		filepath.Join(moveTestSID, "subagents", "agent-a1.meta.json"),
		filepath.Join(moveTestSID, "tool-results", "r1.txt"),
	} {
		want, err := os.ReadFile(filepath.Join(f.homeA, "projects", moveTestProject, rel))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(proj, rel))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s did not travel intact: %v", rel, err)
		}
	}
	// Credentials and other conversations never move.
	if _, err := os.Stat(filepath.Join(f.homeB, ".credentials.json")); !os.IsNotExist(err) {
		t.Fatalf("the account login traveled: %v", err)
	}
	if _, err := os.Stat(filepath.Join(proj, "unrelated-session.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("another conversation traveled: %v", err)
	}
	// The source keeps its own copy: a move never deletes the account's history.
	if _, err := os.Stat(filepath.Join(f.homeA, "projects", moveTestProject, moveTestSID+".jsonl")); err != nil {
		t.Fatalf("source transcript removed: %v", err)
	}

	// The record: one 'moved' event, sealed in the audit chain, naming both profiles.
	var moved []map[string]any
	for _, ev := range f.events(t) {
		if ev["event"] == "moved" {
			moved = append(moved, ev)
		}
	}
	if len(moved) != 1 {
		t.Fatalf("moved events = %d", len(moved))
	}
	detail, _ := moved[0]["detail"].(string)
	if !strings.Contains(detail, f.a) || !strings.Contains(detail, f.b) || moved[0]["audit_seq"].(float64) <= 0 {
		t.Fatalf("moved event does not record the move: %v", moved[0])
	}
	if moved[0]["actor"] == "" || moved[0]["from_state"] != stateStopped || moved[0]["to_state"] != stateStopped {
		t.Fatalf("moved event actor/states: %v", moved[0])
	}

	// Resume continues the same conversation id under the TARGET home.
	rr := f.post(t, "resume", nil)
	if rr.code != http.StatusOK || rr.body["provider_profile_ref"] != f.b {
		t.Fatalf("resume after move = %d %s", rr.code, rr.raw)
	}
	spec := f.fr.lastSpec()
	if v, _ := envValue(spec, "CLAUDE_CONFIG_DIR"); v != f.homeB {
		t.Fatalf("resumed under CLAUDE_CONFIG_DIR %q, want %q", v, f.homeB)
	}
	if v, _ := envValue(spec, "HOME"); v != f.userB {
		t.Fatalf("resumed under HOME %q, want %q", v, f.userB)
	}
	if i := slices.Index(spec.Args, "--resume"); i < 0 || spec.Args[i+1] != moveTestSID {
		t.Fatalf("resume args = %v", spec.Args)
	}
	waitFor(t, "alias under target profile", func() bool {
		a, ok, _ := f.m.LookupScopedAlias(ctx, f.tenant, f.b, "claude", moveTestSID)
		return ok && a.SID == sidBefore && a.RunRef == f.runRef
	})
	if n := countRows(t, f.m, f.tenant, runKind); n != 1 {
		t.Fatalf("a move created another session: %d runs", n)
	}
	// The plane's own live row follows the session, or usage and observations
	// would stay attributed to the account it left.
	if n := countRows(t, f.m, f.tenant, liveKind, eq(colLiveRunRef, f.runRef), eq(colLiveProfileID, f.b)); n != 1 {
		t.Fatalf("live rows attributed to the target profile = %d, want 1", n)
	}
	if n := countRows(t, f.m, f.tenant, liveKind, eq(colLiveRunRef, f.runRef), eq(colLiveProfileID, f.a)); n != 0 {
		t.Fatalf("live rows still attributed to the source profile = %d", n)
	}
}

// noStateIn fails when home holds any conversation state, which is how a refused
// move proves it wrote nothing: the plan is complete before the first write.
func noStateIn(t *testing.T, home string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(home, "projects")); !os.IsNotExist(err) {
		t.Fatalf("a refused move left state in %s: %v", home, err)
	}
}

// Every refusal leaves the session where it was: same profile, no new ledger
// event, no process, nothing written to the target.
func TestMoveRun_RefusalsChangeNothing(t *testing.T) {
	// The Codex driver is registered so the other tool's profile is launchable and
	// the refusal under test is the move's own, not "no runner for this driver".
	f := newMoveFixture(t, WithProviderDriver(NewCodexDriver()))
	ctx := context.Background()
	moveTo := func(ref string) map[string]any { return map[string]any{"provider_profile_ref": ref} }
	refuse := func(name string, want int, wantMsg string, body any) {
		t.Helper()
		events, launches := len(f.events(t)), launchCount(f.fr)
		r := f.post(t, "move", body)
		if r.code != want || !strings.Contains(r.raw, wantMsg) {
			t.Fatalf("%s = %d %s, want %d containing %q", name, r.code, r.raw, want, wantMsg)
		}
		if got := len(f.events(t)); got != events {
			t.Fatalf("%s wrote %d ledger events", name, got-events)
		}
		if launchCount(f.fr) != launches {
			t.Fatalf("%s started a process", name)
		}
		if d, _ := f.m.getRun(ctx, f.tenant, f.runRef); d.ProviderProfileRef != f.a {
			t.Fatalf("%s moved the session to %s", name, d.ProviderProfileRef)
		}
	}

	refuse("running session", http.StatusConflict, "must be stopped", moveTo(f.b))
	f.stop(t)
	noStateIn(t, f.homeB)

	refuse("same profile", http.StatusConflict, "already runs under this profile", moveTo(f.a))
	refuse("unknown profile", http.StatusNotFound, "", moveTo("ppf_"+model.NewID().String()))
	refuse("malformed ref", http.StatusBadRequest, "provider_profile_ref", moveTo("../etc"))
	refuse("no body fields", http.StatusBadRequest, "", map[string]any{"home": f.homeB})
	noStateIn(t, f.homeB)

	disabled := ProfileDisabled
	if _, err := f.m.PatchProfile(ctx, f.tenant, f.b, ProfilePatch{State: &disabled}); err != nil {
		t.Fatal(err)
	}
	refuse("disabled target", http.StatusConflict, "", moveTo(f.b))
	active := ProfileActive
	if _, err := f.m.PatchProfile(ctx, f.tenant, f.b, ProfilePatch{State: &active}); err != nil {
		t.Fatal(err)
	}
	noStateIn(t, f.homeB)

	// Another tool's profile: a move carries Claude Code's state only.
	other := mustCreateProfile(t, f.m, f.tenant, CreateProfileInput{
		Driver: "codex", ConfigHome: t.TempDir(), UserHome: t.TempDir(), AuthSource: AuthSourceAccountHome,
	})
	refuse("another tool", http.StatusConflict, "another tool", moveTo(other.Ref))

	// A link inside the conversation tree could carry the account's login under a
	// harmless name. The whole move is refused before a byte is written.
	proj := filepath.Join(f.homeA, "projects", moveTestProject)
	leak := filepath.Join(proj, moveTestSID, "tool-results", "leak")
	if err := os.Symlink("../../../../.credentials.json", leak); err != nil {
		t.Fatal(err)
	}
	refuse("symlink in the conversation", http.StatusUnprocessableEntity, "link or special file", moveTo(f.b))
	noStateIn(t, f.homeB)
	if err := os.Remove(leak); err != nil {
		t.Fatal(err)
	}

	// The target holds a DIFFERENT conversation under the same id: never overwritten.
	theirs := filepath.Join(f.homeB, "projects", moveTestProject, moveTestSID+".jsonl")
	writeFile(t, theirs, `{"type":"user","message":{"content":"a different conversation"}}`+"\n")
	refuse("diverged target", http.StatusConflict, "different conversation", moveTo(f.b))
	if got, _ := os.ReadFile(theirs); !strings.Contains(string(got), "a different conversation") {
		t.Fatalf("the target's conversation was overwritten: %q", got)
	}
	if err := os.RemoveAll(filepath.Join(f.homeB, "projects")); err != nil {
		t.Fatal(err)
	}

	// A conversation id that is not a plain identifier is never used as a path.
	setClaudeSessionID(t, f, "../../.credentials")
	refuse("unsafe conversation id", http.StatusBadRequest, "not a plain identifier", moveTo(f.b))
	setClaudeSessionID(t, f, "no-such-conversation")
	refuse("conversation not on disk", http.StatusConflict, "nothing to carry", moveTo(f.b))
	setClaudeSessionID(t, f, moveTestSID)
	noStateIn(t, f.homeB)

	// The refusals did not poison the run: the same request now succeeds.
	if r := f.post(t, "move", moveTo(f.b)); r.code != http.StatusOK {
		t.Fatalf("move after refusals = %d %s", r.code, r.raw)
	}

	// A run that predates profiles has no proven home to carry from.
	persistLegacyRunWithoutProfile(t, f.m, f.tenant, f.runRef)
	if r := f.post(t, "move", moveTo(f.a)); r.code != http.StatusConflict || !strings.Contains(r.raw, "legacy session") {
		t.Fatalf("legacy move = %d %s", r.code, r.raw)
	}
}

// setClaudeSessionID rewrites the provider conversation id the run row captured.
func setClaudeSessionID(t *testing.T, f *moveFixture, id string) {
	t.Helper()
	ctx := context.Background()
	if err := f.m.Data.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rec, err := findRunRec(ctx, repo, f.runRef)
		if err != nil {
			return err
		}
		rec[colClaudeSessionID] = id
		_, err = repo.Update(ctx, rec)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// A session that FAILED (the quota ran out) moves, which is the case the move
// exists for. Moving it back finds an older copy of itself and replaces it; a copy
// that is not an older version of the same conversation refuses.
func TestMoveRun_FailedSessionMovesAndMovingBackAdoptsTheNewerCopy(t *testing.T) {
	f := newMoveFixture(t)
	ctx := context.Background()
	f.stop(t)
	f.fr.failNextLaunch(errors.New("provider account out of quota"))
	if r := f.post(t, "resume", nil); r.code == http.StatusOK {
		t.Fatalf("resume unexpectedly succeeded: %s", r.raw)
	}
	if d, _ := f.m.getRun(ctx, f.tenant, f.runRef); d.State != stateFailed {
		t.Fatalf("state = %s, want failed", d.State)
	}
	f.fr.failNextLaunch(nil)

	if r := f.post(t, "move", map[string]any{"provider_profile_ref": f.b}); r.code != http.StatusOK {
		t.Fatalf("move of a failed session = %d %s", r.code, r.raw)
	}
	if d, _ := f.m.getRun(ctx, f.tenant, f.runRef); d.State != stateFailed || d.ProviderProfileRef != f.b {
		t.Fatalf("after move: %+v", d)
	}

	// The conversation continued under B: its copy grew past A's.
	onB := filepath.Join(f.homeB, "projects", moveTestProject, moveTestSID+".jsonl")
	grown, err := os.ReadFile(onB)
	if err != nil {
		t.Fatal(err)
	}
	grown = append(grown, []byte(`{"type":"user","message":{"role":"user","content":"and the second codename?"}}`+"\n")...)
	if err := os.WriteFile(onB, grown, 0o600); err != nil {
		t.Fatal(err)
	}
	if r := f.post(t, "move", map[string]any{"provider_profile_ref": f.a}); r.code != http.StatusOK {
		t.Fatalf("move back = %d %s", r.code, r.raw)
	}
	onA := filepath.Join(f.homeA, "projects", moveTestProject, moveTestSID+".jsonl")
	if got, _ := os.ReadFile(onA); !bytes.Equal(got, grown) {
		t.Fatalf("the older copy on A was not replaced by the newer one: %q", got)
	}

	// Now B holds an older copy of A's... unless it was edited to something else.
	if err := os.WriteFile(onB, []byte(`{"type":"user","message":{"content":"unrelated"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := f.post(t, "move", map[string]any{"provider_profile_ref": f.b})
	if r.code != http.StatusConflict || !strings.Contains(r.raw, "different conversation") {
		t.Fatalf("move onto a diverged copy = %d %s", r.code, r.raw)
	}
	var movedEvents int
	for _, ev := range f.events(t) {
		if ev["event"] == "moved" {
			movedEvents++
		}
	}
	if movedEvents != 2 {
		t.Fatalf("moved events = %d, want 2 (a refusal records nothing)", movedEvents)
	}
}

// A move is a permissioned act: a reader of the session cannot make it.
func TestMoveRun_RequiresRunWrite(t *testing.T) {
	f := newMoveFixture(t)
	f.stop(t)
	viewer := f.h.viewerToken(f.admin, f.tenant, "viewer@example.test")
	r := f.h.doJSON("POST", "/v1/m/sessions/runs/"+f.runRef+"/move", viewer, map[string]any{"provider_profile_ref": f.b}, f.hdr)
	if r.code != http.StatusForbidden && r.code != http.StatusNotFound {
		t.Fatalf("viewer move = %d %s", r.code, r.raw)
	}
	if d, _ := f.m.getRun(context.Background(), f.tenant, f.runRef); d.ProviderProfileRef != f.a {
		t.Fatalf("a viewer moved the session to %s", d.ProviderProfileRef)
	}
	noStateIn(t, f.homeB)
}

// setRunColumn writes one column of the run row, the way a historical or damaged row
// would hold it.
func setRunColumn(t *testing.T, f *moveFixture, col string, value any) {
	t.Helper()
	ctx := context.Background()
	if err := f.m.Data.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rec, err := findRunRec(ctx, repo, f.runRef)
		if err != nil {
			return err
		}
		rec[col] = value
		_, err = repo.Update(ctx, rec)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func (f *moveFixture) refuseMove(t *testing.T, name string, want int, wantMsg string) {
	t.Helper()
	events, launches := len(f.events(t)), launchCount(f.fr)
	r := f.post(t, "move", map[string]any{"provider_profile_ref": f.b})
	if r.code != want || !strings.Contains(r.raw, wantMsg) {
		t.Fatalf("%s = %d %s, want %d containing %q", name, r.code, r.raw, want, wantMsg)
	}
	if len(f.events(t)) != events || launchCount(f.fr) != launches {
		t.Fatalf("%s wrote an event or started a process", name)
	}
	if d, _ := f.m.getRun(context.Background(), f.tenant, f.runRef); d.ProviderProfileRef != f.a {
		t.Fatalf("%s moved the session to %s", name, d.ProviderProfileRef)
	}
}

// What the engine cannot prove it may carry, it does not carry: a session that was
// given secrets, a row that names a home its profile does not own, another tool's
// session, and a conversation id already bound to another session under the target.
func TestMoveRun_RefusesWhatCannotBeProven(t *testing.T) {
	f := newMoveFixture(t)
	f.stop(t)

	setRunColumn(t, f, colRunSecretEnv, `[{"env":"DB_URL","secret":"env/db"}]`)
	f.refuseMove(t, "session with vault secrets", http.StatusConflict, "vault secrets")
	setRunColumn(t, f, colRunSecretEnv, nil)
	setRunColumn(t, f, colRunSecretEnv, `not json`)
	f.refuseMove(t, "unreadable secret record", http.StatusConflict, "vault secrets")
	setRunColumn(t, f, colRunSecretEnv, nil)
	setRunColumn(t, f, colRunGitRead, "binding:owner/name")
	f.refuseMove(t, "session with a repository credential", http.StatusConflict, "repository credential")
	setRunColumn(t, f, colRunGitRead, nil)

	// A row whose persisted home is not the one its profile registered.
	real := f.homeA
	setRunColumn(t, f, colRunProfileConfigHome, t.TempDir())
	f.refuseMove(t, "unproven source home", http.StatusConflict, "unproven home")
	setRunColumn(t, f, colRunProfileConfigHome, real)

	// Another tool's session has no conversation this runtime reads.
	setRunColumn(t, f, colRunProfileDriver, "codex")
	f.refuseMove(t, "another tool's session", http.StatusUnprocessableEntity, "only Claude Code")
	setRunColumn(t, f, colRunProfileDriver, "claude")
	noStateIn(t, f.homeB)

	// The same conversation id is already bound, under the target, to another
	// session: a second session launched on B announces it.
	other := f.h.doJSON("POST", "/v1/m/sessions/runs", f.admin,
		map[string]any{"transport": "stream-json", "permission_mode": "default", "isolation": "native", "provider_profile_ref": f.b}, f.hdr)
	if other.code != http.StatusCreated {
		t.Fatalf("second session = %d %s", other.code, other.raw)
	}
	otherRef := other.body["run_ref"].(string)
	waitFor(t, "alias under B", func() bool {
		_, ok, _ := f.m.LookupScopedAlias(context.Background(), f.tenant, f.b, "claude", moveTestSID)
		return ok
	})
	f.refuseMove(t, "conversation id bound to another session", http.StatusConflict, "already bound")
	_ = otherRef

	// And the refusals did not poison it: with the other session gone, it moves.
	if r := f.h.doJSON("POST", "/v1/m/sessions/runs/"+otherRef+"/stop", f.admin, nil, f.hdr); r.code != http.StatusOK {
		t.Fatal(r.raw)
	}
}

// A failed session keeps the reason it failed. A move is not a new outcome.
func TestMoveRun_KeepsTheReasonAFailedSessionFailed(t *testing.T) {
	f := newMoveFixture(t)
	ctx := context.Background()
	f.stop(t)
	f.fr.failNextLaunch(errors.New("provider account out of quota"))
	f.post(t, "resume", nil)
	before, _ := f.m.getRun(ctx, f.tenant, f.runRef)
	if before.State != stateFailed || before.Reason == "" {
		t.Fatalf("fixture: %+v", before)
	}
	f.fr.failNextLaunch(nil)
	if r := f.post(t, "move", map[string]any{"provider_profile_ref": f.b}); r.code != http.StatusOK {
		t.Fatalf("move = %d %s", r.code, r.raw)
	}
	after, _ := f.m.getRun(ctx, f.tenant, f.runRef)
	if after.Reason != before.Reason {
		t.Fatalf("reason %q replaced by %q", before.Reason, after.Reason)
	}
	for _, ev := range f.events(t) {
		if ev["event"] == "moved" {
			if actor, _ := ev["actor"].(string); actor == "" || !strings.HasPrefix(actor, "user:") {
				t.Fatalf("moved event actor = %v", ev["actor"])
			}
		}
	}
}

// The plan is complete before the first byte is written: a conflict on any later
// file, or a target already holding a longer copy, leaves the transcript unwritten.
func TestMoveRun_ADivergedLaterFileOrALongerCopyWritesNothing(t *testing.T) {
	f := newMoveFixture(t)
	f.stop(t)
	projB := filepath.Join(f.homeB, "projects", moveTestProject)

	// A later file of the tree differs on the target.
	writeFile(t, filepath.Join(projB, moveTestSID, "subagents", "agent-a1.jsonl"), "someone else's subagent\n")
	f.refuseMove(t, "diverged subagent file", http.StatusConflict, "different conversation")
	if _, err := os.Stat(filepath.Join(projB, moveTestSID+".jsonl")); !os.IsNotExist(err) {
		t.Fatalf("the transcript was written although a later file refused: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(f.homeB, "projects")); err != nil {
		t.Fatal(err)
	}

	// The target holds a NEWER copy of the same conversation (longer, source is its
	// prefix): moving the older one over it would lose turns.
	src, err := os.ReadFile(filepath.Join(f.homeA, "projects", moveTestProject, moveTestSID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(projB, moveTestSID+".jsonl"), string(src)+`{"type":"user","message":{"content":"newer turn"}}`+"\n")
	f.refuseMove(t, "longer copy on the target", http.StatusConflict, "different conversation")
	if got, _ := os.ReadFile(filepath.Join(projB, moveTestSID+".jsonl")); !strings.Contains(string(got), "newer turn") {
		t.Fatal("the newer copy was overwritten")
	}
}

// A link, or a second name for a login, inside the conversation is never carried,
// and a conversation behind a linked project directory is not reported as absent.
func TestMoveRun_RefusesLinksAndSecondNames(t *testing.T) {
	f := newMoveFixture(t)
	f.stop(t)
	projA := filepath.Join(f.homeA, "projects", moveTestProject)

	// The conversation directory itself a link.
	moved := filepath.Join(f.homeA, "stash")
	if err := os.Rename(filepath.Join(projA, moveTestSID), moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, filepath.Join(projA, moveTestSID)); err != nil {
		t.Fatal(err)
	}
	f.refuseMove(t, "linked conversation directory", http.StatusUnprocessableEntity, "link or special file")
	if err := os.Remove(filepath.Join(projA, moveTestSID)); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(moved, filepath.Join(projA, moveTestSID)); err != nil {
		t.Fatal(err)
	}

	// A second name for the account's login, inside the tree: a regular file to
	// Lstat, and the login by its content.
	hard := filepath.Join(projA, moveTestSID, "tool-results", "login-copy")
	if err := os.Link(filepath.Join(f.homeA, ".credentials.json"), hard); err != nil {
		t.Skipf("hard links are not available here: %v", err)
	}
	f.refuseMove(t, "second name for the login", http.StatusUnprocessableEntity, "link or special file")
	noStateIn(t, f.homeB)
	if err := os.Remove(hard); err != nil {
		t.Fatal(err)
	}

	// The whole project directory is a link: not "nothing to carry".
	real := filepath.Join(f.homeA, "projects-real")
	if err := os.Rename(projA, real); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, projA); err != nil {
		t.Fatal(err)
	}
	f.refuseMove(t, "linked project directory", http.StatusUnprocessableEntity, "link or special file")
}

// countdownCtx cancels itself after n calls to Err, so a copy can be interrupted
// at every point it checks.
type countdownCtx struct {
	context.Context
	left int
}

func (c *countdownCtx) Err() error {
	if c.left <= 0 {
		return context.Canceled
	}
	c.left--
	return nil
}

func filesUnder(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// A copy interrupted at ANY point leaves the target holding nothing of it: not a
// half transcript, not a temporary file. Completed, it holds exactly the source.
func TestCopyClaudeSessionState_InterruptedAtAnyPointLeavesNothing(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	seedClaudeState(t, src, moveTestSID)
	want := len(filesUnder(t, filepath.Join(src, "projects", moveTestProject))) - 1 // not the unrelated session
	completed := false
	for n := 0; n < 60 && !completed; n++ {
		ctx := &countdownCtx{Context: context.Background(), left: n}
		got, err := copyClaudeSessionState(ctx, src, dst, moveTestSID)
		files := filesUnder(t, dst)
		if err == nil {
			completed = true
			if len(files) != want || got.Files != want {
				t.Fatalf("n=%d completed with %d files in the target (%d carried), want %d", n, len(files), got.Files, want)
			}
			continue
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("n=%d: %v", n, err)
		}
		if len(files) != 0 {
			t.Fatalf("n=%d: an interrupted copy left %v", n, files)
		}
	}
	if !completed {
		t.Fatal("the copy never completed within the sweep")
	}
}

// A file swapped after the plan is refused by its descriptor, never read through.
func TestWriteClaudeStateFile_RefusesWhatIsNotThePlannedFile(t *testing.T) {
	srcHome, dstHome := t.TempDir(), t.TempDir()
	rel := "projects/-p/" + moveTestSID + ".jsonl"
	writeFile(t, filepath.Join(srcHome, rel), "planned\n")
	writeFile(t, filepath.Join(srcHome, ".credentials.json"), moveCredential)
	src, err := os.OpenRoot(srcHome)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenRoot(dstHome)
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	planned, err := src.Lstat(rel)
	if err != nil {
		t.Fatal(err)
	}
	// The planned file stays open, so its inode stays taken: a file put under the name
	// afterwards cannot be given the planned device and inode, whatever the filesystem
	// does with freed inodes.
	pin, err := os.Open(filepath.Join(srcHome, rel))
	if err != nil {
		t.Fatal(err)
	}
	defer pin.Close()
	entry := claudeStateEntry{rel: rel, size: planned.Size(), info: planned}
	ctx := context.Background()

	// Replaced by a link to the login after the plan.
	if err := os.Remove(filepath.Join(srcHome, rel)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../.credentials.json", filepath.Join(srcHome, rel)); err != nil {
		t.Fatal(err)
	}
	if err := writeClaudeStateFile(ctx, src, dst, entry); err == nil {
		t.Fatal("a link swapped in after the plan was copied")
	}
	// Replaced by a different regular file of the same size and modification time, on
	// another inode: only its identity says it is not the planned file.
	if err := os.Remove(filepath.Join(srcHome, rel)); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(srcHome, rel), "swapped\n")
	if err := os.Chtimes(filepath.Join(srcHome, rel), planned.ModTime(), planned.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := writeClaudeStateFile(ctx, src, dst, entry); !errors.Is(err, errClaudeStateChanged) {
		t.Fatalf("a different file of the same size was copied, or refused for another reason: %v", err)
	}
	// Grown since the plan, the SAME file: only its size says it changed.
	grown := "projects/-p/grown.jsonl"
	writeFile(t, filepath.Join(srcHome, grown), "planned\n")
	growInfo, err := src.Lstat(grown)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(srcHome, grown), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("appended after the plan\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	// Its modification time is put back, so the size is all that differs.
	if err := os.Chtimes(filepath.Join(srcHome, grown), growInfo.ModTime(), growInfo.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := writeClaudeStateFile(ctx, src, dst, claudeStateEntry{rel: grown, size: growInfo.Size(), info: growInfo}); !errors.Is(err, errClaudeStateChanged) {
		t.Fatalf("a grown file was copied truncated, or refused for another reason: %v", err)
	}
	if files := filesUnder(t, dstHome); len(files) != 0 {
		t.Fatalf("a refused file left %v in the target", files)
	}
}

// #1919: the planned file's device and inode cannot tell it from a different file
// that was given the same inode, so the case is built without the filesystem's help:
// the file is rewritten in place and keeps its device, inode, mode, link count and
// size. Only its modification time, newer than the plan saw, says it changed.
func TestWriteClaudeStateFile_RefusesAFileRewrittenInPlaceWithTheSameSize(t *testing.T) {
	srcHome, dstHome := t.TempDir(), t.TempDir()
	rel := "projects/-p/" + moveTestSID + ".jsonl"
	path := filepath.Join(srcHome, rel)
	writeFile(t, path, "planned\n")
	old := time.Now().Add(-time.Hour) // a stopped session's files are old
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	src, err := os.OpenRoot(srcHome)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenRoot(dstHome)
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	planned, err := src.Lstat(rel)
	if err != nil {
		t.Fatal(err)
	}
	entry := claudeStateEntry{rel: rel, size: planned.Size(), info: planned}
	ctx := context.Background()
	// The unchanged file is carried, so the refusal below is the rewrite's and not
	// something the aged file earns.
	control, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	if err := writeClaudeStateFile(ctx, src, control, entry); err != nil {
		t.Fatalf("the unchanged planned file was refused: %v", err)
	}
	if err := os.WriteFile(path, []byte("swapped\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now, err := src.Lstat(rel)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(planned, now) || now.Size() != planned.Size() {
		t.Fatal("the rewrite did not keep the planned file's identity and size; the case is not what it claims")
	}
	if err := writeClaudeStateFile(ctx, src, dst, entry); !errors.Is(err, errClaudeStateChanged) {
		t.Fatalf("a file rewritten in place with the same size was copied, or refused for another reason: %v", err)
	}
	if files := filesUnder(t, dstHome); len(files) != 0 {
		t.Fatalf("a refused file left %v in the target", files)
	}
}
