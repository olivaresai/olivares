// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// #1845, second half: a conversation cannot continue natively in ANOTHER tool, so a
// handoff starts a new session there, linked to the original, whose first turn is a
// summary the person reviewed and whose full transcript sits where the new tool can
// read it.

// handoffFixture is the move fixture with the Codex driver registered, so a Codex
// profile is a launchable target.
func handoffFixture(t *testing.T) *moveFixture {
	t.Helper()
	return newMoveFixture(t,
		WithProviderDriver(NewCodexDriver()),
		WithDriverProgram(providerDriverCodex, os.Args[0]),
		WithStopWaitDelay(2*time.Second),
		WithDriverTimeouts(20*time.Second, 2*time.Second),
	)
}

func (f *moveFixture) runCount(t *testing.T) int {
	t.Helper()
	return countRows(t, f.m, f.tenant, runKind)
}

// transcriptLine is the line of the first turn that names the attachment.
var transcriptLine = regexp.MustCompile(`(?m)^Transcript of the earlier session \(tool calls and results shortened\): (\S+)$`)

// The preview is read-only: it generates the summary from the conversation the tool
// stored, starts nothing and writes nothing.
func TestHandoff_PreviewIsGeneratedFromTheStoredConversationAndWritesNothing(t *testing.T) {
	f := newMoveFixture(t)
	f.stop(t)
	events, runs := len(f.events(t)), f.runCount(t)

	r := f.h.doJSON("GET", "/v1/m/sessions/runs/"+f.runRef+"/handoff", f.admin, nil, f.hdr)
	if r.code != http.StatusOK {
		t.Fatalf("preview = %d %s", r.code, r.raw)
	}
	summary, _ := r.body["summary"].(string)
	for _, want := range []string{"What is the codename of the migration?", "BLUE-HERON"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary lacks %q:\n%s", want, summary)
		}
	}
	if r.body["source_driver"] != "claude" || r.body["turns"] != float64(1) {
		t.Fatalf("preview = %s", r.raw)
	}
	if n, _ := r.body["transcript_bytes"].(float64); n <= 0 {
		t.Fatalf("transcript_bytes = %v", r.body["transcript_bytes"])
	}
	if strings.Contains(r.raw, f.homeA) || strings.Contains(r.raw, "tok-account-a") {
		t.Fatalf("preview leaks a home or a login: %s", r.raw)
	}
	if len(f.events(t)) != events || f.runCount(t) != runs {
		t.Fatal("a preview wrote an event or a session")
	}
	for _, home := range []string{f.homeA, f.homeB} {
		if _, err := os.Stat(filepath.Join(home, handoffAttachmentDir)); !os.IsNotExist(err) {
			t.Fatalf("a preview wrote an attachment under %s: %v", home, err)
		}
	}
}

func TestHandoff_CrossToolStartsALinkedSessionWithTheReviewedSummary(t *testing.T) {
	f := handoffFixture(t)
	codex := mustCreateProfile(t, f.m, f.tenant, CreateProfileInput{
		Driver: providerDriverCodex, ConfigHome: t.TempDir(), UserHome: t.TempDir(),
		DisplayName: "codex-b", AuthSource: AuthSourceAccountHome,
	})
	record := setCodexFixture(t, codex, codexFixture{ThreadID: "thread-handoff", CompleteTurn: true})
	f.stop(t)
	// The fake runner that stood in for Claude Code cannot speak Codex's protocol;
	// the Codex session is spawned for real, against the fixture peer.
	f.m.rt.Runner = NewProcRunner()

	const reviewed = "REVIEWED SUMMARY: continue the BLUE-HERON migration rollout."
	r := f.post(t, "handoff", map[string]any{
		"provider_profile_ref": codex.Ref, "summary": reviewed, "name": "Rollout on Codex",
	})
	if r.code != http.StatusCreated {
		t.Fatalf("handoff = %d %s", r.code, r.raw)
	}
	newRef, _ := r.body["run_ref"].(string)
	if newRef == "" || newRef == f.runRef {
		t.Fatalf("handoff must start a NEW session: %s", r.raw)
	}
	t.Cleanup(func() {
		f.h.doJSON("POST", "/v1/m/sessions/runs/"+newRef+"/stop", f.admin, nil, f.hdr)
	})
	if r.body["handoff_of"] != f.runRef || r.body["provider_driver"] != providerDriverCodex ||
		r.body["provider_profile_ref"] != codex.Ref || r.body["name"] != "Rollout on Codex" {
		t.Fatalf("linked session = %s", r.raw)
	}
	// Delivered is a certainty, and then the response carries no path of a home.
	if _, has := r.body["first_turn"]; r.body["summary_state"] != handoffDelivered || has {
		t.Fatalf("first turn state = %v / %v", r.body["summary_state"], r.body["first_turn"])
	}
	if strings.Contains(r.raw, codex.ConfigHome) || strings.Contains(r.raw, f.homeA) {
		t.Fatalf("handoff response leaks a home: %s", r.raw)
	}

	// The first turn the new tool received is the REVIEWED text plus the pointer to
	// the attachment, and nothing the person did not see.
	var first string
	waitFor(t, "the new tool received the first turn", func() bool {
		rec := readFixtureRecord(t, record)
		if len(rec.Inputs) == 0 {
			return false
		}
		first = rec.Inputs[0]
		return true
	})
	if !strings.HasPrefix(first, reviewed) {
		t.Fatalf("first turn does not start with the reviewed summary:\n%s", first)
	}
	m := transcriptLine.FindStringSubmatch(first)
	if m == nil {
		t.Fatalf("first turn names no transcript:\n%s", first)
	}
	// A place the new tool can read, under a name of this handoff's own.
	if filepath.Dir(m[1]) != filepath.Join(codex.ConfigHome, handoffAttachmentDir) ||
		!regexp.MustCompile(`^`+regexp.QuoteMeta(f.runRef)+`-[0-9a-f]{16}\.md$`).MatchString(filepath.Base(m[1])) {
		t.Fatalf("transcript path = %q", m[1])
	}
	info, err := os.Stat(m[1])
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("attachment: %v %v", info, err)
	}
	body, _ := os.ReadFile(m[1])
	for _, need := range []string{"What is the codename of the migration?", "The codename is BLUE-HERON."} {
		if !strings.Contains(string(body), need) {
			t.Fatalf("attachment lacks %q:\n%s", need, body)
		}
	}
	if strings.Contains(string(body), "tok-account-a") {
		t.Fatal("the account login reached the attachment")
	}

	// The record: the original is untouched except for the link, and says where it went.
	orig, _ := f.m.getRun(context.Background(), f.tenant, f.runRef)
	if orig.State != stateStopped || orig.ProviderProfileRef != f.a {
		t.Fatalf("original changed: %+v", orig)
	}
	var linked bool
	for _, ev := range f.events(t) {
		if detail, _ := ev["detail"].(string); ev["event"] == "handed_off" && strings.Contains(detail, newRef) {
			linked = true
		}
	}
	if !linked {
		t.Fatalf("the original's ledger does not say it was handed off to %s", newRef)
	}
}

// Refusals start nothing: no session, no attachment.
func TestHandoff_RefusalsStartNothing(t *testing.T) {
	f := newMoveFixture(t)
	f.stop(t)
	runs := f.runCount(t)
	refuse := func(name string, want int, body map[string]any) {
		t.Helper()
		r := f.post(t, "handoff", body)
		if r.code != want {
			t.Fatalf("%s = %d %s, want %d", name, r.code, r.raw, want)
		}
		if f.runCount(t) != runs {
			t.Fatalf("%s started a session", name)
		}
		noStateIn(t, f.homeB)
	}
	refuse("unknown profile", http.StatusNotFound, map[string]any{"provider_profile_ref": "ppf_" + model.NewID().String()})
	refuse("no profile", http.StatusBadRequest, map[string]any{"summary": "x"})
	refuse("unknown field", http.StatusBadRequest, map[string]any{"provider_profile_ref": f.b, "config_home": "/x"})

	// A session that was given vault secrets cannot be forwarded to another
	// provider: what it printed cannot be proven free of them.
	setRunSecretEnv(t, f, `[{"env":"DEPLOY_KEY","secret":"env/deploy"}]`)
	refuse("session with vault secrets", http.StatusConflict, map[string]any{"provider_profile_ref": f.b})
}

// setRunSecretEnv records that the run was launched with vault secrets.
func setRunSecretEnv(t *testing.T, f *moveFixture, encoded string) {
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
		rec[colRunSecretEnv] = encoded
		_, err = repo.Update(ctx, rec)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// failSendRunner stands in for a tool whose process takes the launch and then
// refuses input, which is how a first turn fails to arrive.
type failSendRunner struct{ inner *fakeRunner }

type failSendProc struct{ Process }

func (failSendProc) Send(context.Context, []byte) error { return errors.New("stdin closed") }

func (r failSendRunner) Launch(ctx context.Context, spec LaunchSpec) (Process, error) {
	p, err := r.inner.Launch(ctx, spec)
	if err != nil {
		return nil, err
	}
	return failSendProc{p}, nil
}

// sentByProcess is the lines each launched process was sent, in launch order.
func (r *fakeRunner) sentByProcess() [][][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out [][][]byte
	for _, p := range r.procs {
		p.mu.Lock()
		out = append(out, append([][]byte(nil), p.sent...))
		p.mu.Unlock()
	}
	return out
}

func attachmentsIn(t *testing.T, home string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(home, handoffAttachmentDir))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// A handoff reads a conversation that is finished. A running session is still
// writing its file, and neither the preview nor the start answers for it.
func TestHandoff_ARunningSessionIsRefused(t *testing.T) {
	f := newMoveFixture(t)
	r := f.h.doJSON("GET", "/v1/m/sessions/runs/"+f.runRef+"/handoff", f.admin, nil, f.hdr)
	if r.code != http.StatusConflict || !strings.Contains(r.raw, "stop the session first") {
		t.Fatalf("preview of a running session = %d %s", r.code, r.raw)
	}
	runs := f.runCount(t)
	if r := f.post(t, "handoff", map[string]any{"provider_profile_ref": f.b}); r.code != http.StatusConflict {
		t.Fatalf("handoff of a running session = %d %s", r.code, r.raw)
	}
	if f.runCount(t) != runs || len(attachmentsIn(t, f.homeB)) != 0 {
		t.Fatal("a refused handoff started a session or wrote an attachment")
	}
}

// What the engine cannot prove it may forward, it does not forward.
func TestHandoff_RefusesWhatCannotBeProven(t *testing.T) {
	f := newMoveFixture(t)
	f.stop(t)
	runs := f.runCount(t)
	refuse := func(name string, want int, msg string) {
		t.Helper()
		r := f.post(t, "handoff", map[string]any{"provider_profile_ref": f.b})
		if r.code != want || !strings.Contains(r.raw, msg) {
			t.Fatalf("%s = %d %s, want %d containing %q", name, r.code, r.raw, want, msg)
		}
		if f.runCount(t) != runs || len(attachmentsIn(t, f.homeB)) != 0 {
			t.Fatalf("%s started a session or wrote an attachment", name)
		}
	}
	setRunColumn(t, f, colRunGitRead, "binding:owner/name")
	refuse("repository credential", http.StatusConflict, "repository credential")
	setRunColumn(t, f, colRunGitRead, nil)

	real := f.homeA
	setRunColumn(t, f, colRunProfileConfigHome, t.TempDir())
	refuse("unproven home", http.StatusConflict, "unproven home")
	setRunColumn(t, f, colRunProfileConfigHome, real)

	setRunColumn(t, f, colRunProfileDriver, "codex")
	refuse("another tool's session", http.StatusUnprocessableEntity, "Claude Code conversations only")
	setRunColumn(t, f, colRunProfileDriver, "claude")

	setClaudeSessionID(t, f, "")
	refuse("no stored conversation", http.StatusConflict, "no stored conversation")
	setClaudeSessionID(t, f, moveTestSID)

	// A conversation file the tool never wrote.
	if err := os.Remove(filepath.Join(f.homeA, "projects", moveTestProject, moveTestSID+".jsonl")); err != nil {
		t.Fatal(err)
	}
	refuse("conversation not on disk", http.StatusConflict, "nothing to hand off")
}

// A handoff is a permissioned act: reading is not enough to start a session.
func TestHandoff_RequiresRunWrite(t *testing.T) {
	f := newMoveFixture(t)
	f.stop(t)
	viewer := f.h.viewerToken(f.admin, f.tenant, "viewer@example.test")
	runs := f.runCount(t)
	r := f.h.doJSON("POST", "/v1/m/sessions/runs/"+f.runRef+"/handoff", viewer, map[string]any{"provider_profile_ref": f.b}, f.hdr)
	if r.code != http.StatusForbidden && r.code != http.StatusNotFound {
		t.Fatalf("viewer handoff = %d %s", r.code, r.raw)
	}
	if f.runCount(t) != runs || len(attachmentsIn(t, f.homeB)) != 0 {
		t.Fatal("a viewer started a session")
	}
}

// Two handoffs of one session to one profile keep their own attachments; a later
// handoff that fails removes only the file it wrote.
func TestHandoff_EachHandoffKeepsItsOwnAttachment(t *testing.T) {
	f := newMoveFixture(t)
	f.stop(t)
	first := f.post(t, "handoff", map[string]any{"provider_profile_ref": f.b, "name": "first"})
	second := f.post(t, "handoff", map[string]any{"provider_profile_ref": f.b, "name": "second"})
	if first.code != http.StatusCreated || second.code != http.StatusCreated {
		t.Fatalf("handoffs = %d %d (%s)", first.code, second.code, second.raw)
	}
	files := attachmentsIn(t, f.homeB)
	if len(files) != 2 {
		t.Fatalf("attachments = %v, want one per handoff", files)
	}
	// The launch of a third is refused: the file it wrote is gone, the others stay.
	f.fr.failNextLaunch(errors.New("launch refused"))
	runs := f.runCount(t)
	if r := f.post(t, "handoff", map[string]any{"provider_profile_ref": f.b}); r.code == http.StatusCreated {
		t.Fatalf("handoff with a refused launch = %d %s", r.code, r.raw)
	}
	// A launch that fails leaves the ordinary FAILED record of the attempt, as any
	// launch does, and never a live session.
	if n := f.runCount(t); n > runs+1 {
		t.Fatalf("a refused launch left %d sessions", n-runs)
	}
	if n := countRows(t, f.m, f.tenant, runKind, eq(colState, stateRunning), eq(colRunHandoffOf, f.runRef)); n != 2 {
		t.Fatalf("running linked sessions = %d, want the 2 that started", n)
	}
	after := attachmentsIn(t, f.homeB)
	if len(after) != 2 || after[0] != files[0] || after[1] != files[1] {
		t.Fatalf("attachments after a refused third = %v, want %v", after, files)
	}
	// And the first turn of each went to its own session.
	for _, p := range f.fr.sentByProcess() {
		if len(p) > 0 && !strings.Contains(string(p[0]), "Transcript of the earlier session") {
			t.Fatalf("first turn = %s", p[0])
		}
	}
}

// A first turn whose delivery failed after it was attempted is reported as unknown,
// never as not delivered, because sending it again could repeat it; the text is
// returned either way so the person is not left without it.
func TestHandoff_ReportsAFirstTurnThatMayHaveArrivedAndGivesTheText(t *testing.T) {
	f := newMoveFixture(t)
	f.stop(t)
	f.m.rt.Runner = failSendRunner{inner: f.fr}
	r := f.post(t, "handoff", map[string]any{"provider_profile_ref": f.b, "summary": "REVIEWED: carry on."})
	if r.code != http.StatusCreated {
		t.Fatalf("handoff = %d %s", r.code, r.raw)
	}
	if r.body["summary_state"] != handoffDeliveryUnknown {
		t.Fatalf("summary_state = %v, want %s", r.body["summary_state"], handoffDeliveryUnknown)
	}
	text, _ := r.body["first_turn"].(string)
	if !strings.HasPrefix(text, "REVIEWED: carry on.") || !transcriptLine.MatchString(text) {
		t.Fatalf("first_turn = %q", text)
	}
	// The path it names is a file that exists, so sending the text is enough.
	if m := transcriptLine.FindStringSubmatch(text); m == nil {
		t.Fatal("no transcript path in the first turn")
	} else if _, err := os.Stat(m[1]); err != nil {
		t.Fatalf("the named transcript is missing: %v", err)
	}
}

// A first turn that provably did not cross is "not delivered".
func TestDeliverFirstTurn_ASessionThatIsGoneIsNotDelivered(t *testing.T) {
	f := newMoveFixture(t)
	if got := f.m.deliverFirstTurn(context.Background(), f.tenant, "no-such-run", "text"); got != handoffNotDelivered {
		t.Fatalf("state = %s", got)
	}
}
