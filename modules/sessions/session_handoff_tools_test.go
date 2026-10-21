// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"bufio"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #2243: a handoff reads the stored conversation of Codex, Gemini CLI and OpenCode
// as well as Claude Code's. The Gemini chat and the OpenCode export under testdata
// were produced by real Gemini CLI 0.64.0 and OpenCode 1.19.2 processes against a
// loopback model stand-in, and over the agent protocol (the sessions the engine
// runs); only paths and the conversation id are normalized.

const (
	geminiTestChat   = "testdata/gemini-0.64.0-chat.jsonl"
	openCodeTestFile = "testdata/opencode-1.19.2-export.json"
	// The id of the OpenCode session in its export.
	openCodeTestSID = "ses_ebd4ec0aeffeCqQUs7Zjq6Rmhv"
)

// toolSource makes the fixture's session a session of another tool: a profile of that
// tool on homes of its own, the run's stored profile pointing at it, and the tool's
// conversation id. It stays running unless stopped.
func toolSource(t *testing.T, f *moveFixture, driver, configHome, sid string) {
	t.Helper()
	if err := os.MkdirAll(configHome, 0o700); err != nil {
		t.Fatal(err)
	}
	user := t.TempDir()
	p := mustCreateProfile(t, f.m, f.tenant, CreateProfileInput{Driver: driver, ConfigHome: configHome, UserHome: user, AuthSource: AuthSourceAccountHome})
	for col, val := range map[string]any{colRunProfileID: p.Ref, colRunProfileDriver: driver, colRunProfileConfigHome: configHome, colRunProfileUserHome: user} {
		setRunColumn(t, f, col, val)
	}
	f.homeA = configHome
	setClaudeSessionID(t, f, sid)
	// The process that is still running was launched under the first profile; as the
	// run now belongs to this one, so does the process.
	if lr, ok := f.m.rt.getLive(f.tenant, f.runRef); ok && lr.profile != nil {
		lr.mu.Lock()
		snapshot := *lr.profile
		snapshot.ProfileID = p.Ref
		lr.profile = &snapshot
		lr.mu.Unlock()
	}
}

// handedOff starts the handoff to the fixture's second (Claude) profile and returns the
// transcript the new session was given.
func handedOff(t *testing.T, f *moveFixture) string {
	t.Helper()
	r := f.post(t, "handoff", map[string]any{"provider_profile_ref": f.b})
	if r.code != http.StatusCreated || r.body["handoff_of"] != f.runRef {
		t.Fatalf("handoff = %d %s", r.code, r.raw)
	}
	files := attachmentsIn(t, f.homeB)
	if len(files) != 1 {
		t.Fatalf("attachments = %v", files)
	}
	body, err := os.ReadFile(filepath.Join(f.homeB, handoffAttachmentDir, files[0]))
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := f.m.getRun(context.Background(), f.tenant, f.runRef); d.State != stateStopped {
		t.Fatalf("the source is %s after its handoff, want stopped", d.State)
	}
	return string(body)
}

func wantAll(t *testing.T, what, text string, needs ...string) {
	t.Helper()
	for _, need := range needs {
		if !strings.Contains(text, need) {
			t.Fatalf("%s lacks %q:\n%s", what, need, text)
		}
	}
}

func TestHandoff_FromCodexReadsTheRollout(t *testing.T) {
	f := newMoveFixture(t)
	home := t.TempDir()
	toolSource(t, f, providerDriverCodex, home, moveTestSID)
	writeFile(t, filepath.Join(home, "sessions", "2026", "10", "16", "rollout-2026-10-16T09-00-00-"+moveTestSID+".jsonl"),
		`{"timestamp":"2026-10-16T09:00:00Z","type":"session_meta","payload":{"id":"`+moveTestSID+`","source":"cli"}}
{"timestamp":"2026-10-16T09:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>cwd /work</environment_context>"}]}}
{"timestamp":"2026-10-16T09:00:02Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"What is the codename of the migration?"}]}}
{"timestamp":"2026-10-16T09:00:03Z","type":"response_item","payload":{"type":"reasoning","summary":[]}}
{"timestamp":"2026-10-16T09:00:04Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"c1","arguments":"{\"cmd\":\"cat CODENAME\"}"}}
{"timestamp":"2026-10-16T09:00:05Z","type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"BLUE-HERON"}}
{"timestamp":"2026-10-16T09:00:06Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"The codename is BLUE-HERON."}]}}
`)

	p := f.h.doJSON("GET", "/v1/m/sessions/runs/"+f.runRef+"/handoff", f.admin, nil, f.hdr)
	if p.code != http.StatusOK || p.body["source_driver"] != providerDriverCodex || p.body["turns"] != float64(1) {
		t.Fatalf("preview = %d %s", p.code, p.raw)
	}
	wantAll(t, "the summary", p.body["summary"].(string), "earlier Codex session", "What is the codename of the migration?", "BLUE-HERON")
	if strings.Contains(p.raw, "environment_context") {
		t.Fatalf("what Codex gives its model as context is not the person's message: %s", p.raw)
	}

	// The session is still running: the handoff stops it and reads the finished rollout.
	body := handedOff(t, f)
	wantAll(t, "the transcript", body, "# Transcript of an earlier Codex session", "## User\nWhat is the codename of the migration?",
		"- tool: exec_command {\"cmd\":\"cat CODENAME\"}", "- result: BLUE-HERON", "## Assistant\nThe codename is BLUE-HERON.")
	if strings.Contains(body, "environment_context") || strings.Contains(body, "reasoning") {
		t.Fatalf("context and reasoning are not carried:\n%s", body)
	}
}

func TestHandoff_FromGeminiCLIReadsTheChatLog(t *testing.T) {
	f := newMoveFixture(t)
	f.stop(t)
	home := filepath.Join(t.TempDir(), ".gemini")
	toolSource(t, f, providerDriverGemini, home, moveTestSID)
	chat, err := os.ReadFile(geminiTestChat)
	if err != nil {
		t.Fatal(err)
	}
	chats := filepath.Join(home, "tmp", "work", "chats")
	writeFile(t, filepath.Join(chats, "session-2026-10-16T03-08-5b0c1a6e.jsonl"), string(chat))
	// A chat of another conversation whose id starts alike is not this one.
	writeFile(t, filepath.Join(chats, "session-2026-10-16T03-09-5b0c1a6e.jsonl"),
		`{"sessionId":"5b0c1a6e-ffff-4a2b-9c3d-000000000009","kind":"main"}`+"\n"+
			`{"id":"x","type":"user","content":[{"text":"SOMEONE ELSE"}]}`+"\n")

	p := f.h.doJSON("GET", "/v1/m/sessions/runs/"+f.runRef+"/handoff", f.admin, nil, f.hdr)
	if p.code != http.StatusOK || p.body["source_driver"] != providerDriverGemini || p.body["turns"] != float64(1) {
		t.Fatalf("preview = %d %s", p.code, p.raw)
	}
	wantAll(t, "the summary", p.body["summary"].(string), "earlier Gemini CLI session", "please list the directory", "ok, done")
	if strings.Contains(p.raw, "SOMEONE ELSE") || strings.Contains(p.raw, "session_context") {
		t.Fatalf("preview carries another chat or the tool's own context: %s", p.raw)
	}

	body := handedOff(t, f)
	wantAll(t, "the transcript", body, "# Transcript of an earlier Gemini CLI session", "## User\nplease list the directory",
		"- tool: list_directory {\"dir_path\":\".\"}", "a.txt (0 bytes)", "## Assistant\nok, done")
	if strings.Contains(body, "session_context") || strings.Contains(body, "SOMEONE ELSE") {
		t.Fatalf("transcript carries the tool's own context or another chat:\n%s", body)
	}
}

func TestHandoff_FromGeminiCLIRefusesWhatItCannotChoose(t *testing.T) {
	f := newMoveFixture(t)
	f.stop(t)
	home := filepath.Join(t.TempDir(), ".gemini")
	toolSource(t, f, providerDriverGemini, home, moveTestSID)
	refuse := func(name string, want int, msg string) {
		t.Helper()
		r := f.post(t, "handoff", map[string]any{"provider_profile_ref": f.b})
		if r.code != want || !strings.Contains(r.raw, msg) || len(attachmentsIn(t, f.homeB)) != 0 {
			t.Fatalf("%s = %d %s, want %d containing %q and no attachment", name, r.code, r.raw, want, msg)
		}
	}
	refuse("no chat directory", http.StatusConflict, "nothing to hand off")
	chat := `{"sessionId":"` + moveTestSID + `","kind":"main"}` + "\n" + `{"id":"a","type":"user","content":[{"text":"hi"}]}` + "\n"
	writeFile(t, filepath.Join(home, "tmp", "one", "chats", "session-2026-10-16T03-08-5b0c1a6e.jsonl"), chat)
	writeFile(t, filepath.Join(home, "tmp", "two", "chats", "session-2026-10-16T03-09-5b0c1a6e.jsonl"), chat)
	refuse("two chats of one id", http.StatusConflict, "more than one stored conversation")
}

// openCodeRunner stands in for `opencode export`: it answers the export of its one
// session the way the real tool did, and anything else as the tool does for an id it
// does not hold. Every other launch is the fixture's own.
type openCodeRunner struct {
	inner  *fakeRunner
	export string
	exit   int
	specs  []LaunchSpec
}

func (r *openCodeRunner) Launch(ctx context.Context, spec LaunchSpec) (Process, error) {
	if len(spec.Args) != 3 || spec.Args[1] != "export" {
		return r.inner.Launch(ctx, spec)
	}
	r.specs = append(r.specs, spec)
	p := &fakeProc{out: make(chan OutputFrame, 1024), stopped: make(chan struct{}), exit: r.exit}
	if spec.Args[2] == openCodeTestSID { // the export is printed whatever the exit says
		sc := bufio.NewScanner(strings.NewReader(r.export))
		sc.Buffer(nil, 1<<20)
		for sc.Scan() {
			p.out <- OutputFrame{Stream: streamStdout, Data: []byte(sc.Text())}
		}
	}
	p.out <- OutputFrame{Stream: streamStderr, Data: []byte("Exporting session: " + spec.Args[2])}
	p.finish(r.exit) // closes the output: what is queued is still read first
	return p, nil
}

func openCodeExportFixture(t *testing.T, exit int) (*moveFixture, *openCodeRunner) {
	t.Helper()
	f := newMoveFixture(t, WithProviderDriver(NewOpenCodeDriver()), WithDriverProgram(providerDriverOpenCode, os.Args[0]))
	export, err := os.ReadFile(openCodeTestFile)
	if err != nil {
		t.Fatal(err)
	}
	toolSource(t, f, providerDriverOpenCode, t.TempDir(), openCodeTestSID)
	r := &openCodeRunner{inner: f.fr, export: string(export), exit: exit}
	f.m.rt.Runner = r
	return f, r
}

func TestHandoff_FromOpenCodeReadsItsOwnExport(t *testing.T) {
	f, runner := openCodeExportFixture(t, 0)

	p := f.h.doJSON("GET", "/v1/m/sessions/runs/"+f.runRef+"/handoff", f.admin, nil, f.hdr)
	if p.code != http.StatusOK || p.body["source_driver"] != providerDriverOpenCode || p.body["turns"] != float64(1) {
		t.Fatalf("preview = %d %s", p.code, p.raw)
	}
	wantAll(t, "the summary", p.body["summary"].(string), "earlier OpenCode session", "please list the directory", "ok, done")

	body := handedOff(t, f) // the session was running: stopped first
	wantAll(t, "the transcript", body, "# Transcript of an earlier OpenCode session", "## User\nplease list the directory",
		`- tool: bash {"command":"ls","description":"list files"}`, "- result: (no output)", "## Assistant\nok, done")

	// The tool is read the way it documents, under the session's own profile, offline.
	if len(runner.specs) != 2 {
		t.Fatalf("export launches = %d, want one for the preview and one for the handoff", len(runner.specs))
	}
	spec := runner.specs[1]
	rec, err := f.m.loadRun(context.Background(), f.tenant, f.runRef)
	if err != nil {
		t.Fatal(err)
	}
	src := storedProfileSnapshot(rec)
	env := map[string]string{}
	for _, e := range spec.Env {
		env[e.Name] = e.Value
	}
	// --pure: without it the export runs the plugins the profile's configuration names.
	if spec.Args[0] != "--pure" || spec.Args[2] != openCodeTestSID || spec.NetworkPolicy == nil || !spec.NetworkPolicy.Offline ||
		env["HOME"] != src.UserHome || env["XDG_DATA_HOME"] != filepath.Join(src.UserHome, ".local", "share") {
		t.Fatalf("export launch = %+v env=%v", spec, env)
	}
}

// What can only fail after the stop (the export is a process) says the session was
// stopped, and leaves it resumable and nothing started.
func TestHandoff_FromOpenCodeRefusesAnExportThatFailed(t *testing.T) {
	f, _ := openCodeExportFixture(t, 1)
	runs := f.runCount(t)
	r := f.post(t, "handoff", map[string]any{"provider_profile_ref": f.b})
	if r.code != http.StatusBadGateway || !strings.Contains(r.raw, "did not export") || !strings.Contains(r.raw, "was stopped and can be resumed") {
		t.Fatalf("failed export = %d %s", r.code, r.raw)
	}
	if f.stateOf(t) != stateStopped {
		t.Fatalf("state = %s, want stopped", f.stateOf(t))
	}
	if f.runCount(t) != runs || len(attachmentsIn(t, f.homeB)) != 0 {
		t.Fatal("a failed export started a session or wrote an attachment")
	}
}
