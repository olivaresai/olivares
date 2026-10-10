// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// fakeToolEngine serves /v1/m/agenttools/* the way the engine does: an inventory,
// a plan/install/job sequence, and the tools' own sign-in.
type fakeToolEngine struct {
	*httptest.Server
	mu        sync.Mutex
	hits      []string
	signedIn  map[string]bool
	signInRts bool // false: an engine older than the sign-in routes
	onPath    map[string]bool
	jobPolls  int
	code      string
	// pending keeps a Codex device login waiting; started closes when the CLI
	// reaches its input wait (Claude) or status poll (Codex).
	pending     bool
	started     chan struct{}
	startedOnce sync.Once
	polling     chan struct{}
	pollingOnce sync.Once
	// ready are the tools the readiness answer says can start; ollama is the product's
	// Ollama service: started by POST /ollama/start, for ollamaTenant.
	ready map[string]bool
	// refused: OpenCode's only key was refused at its last test. As in the engine, the
	// preview still names that key (200) while the readiness says OpenCode cannot start.
	refused       bool
	ollamaStarted bool
	ollamaTenant  any
	planRequest   map[string]any
	// providers is what GET providers answers (nil: an engine without the route).
	providers []map[string]any
	// presence is the inventory's session-tool presence answer (nil: an engine
	// older than it): session tools with no installer or sign-in (driverfacts).
	presence map[string]any
	// signInFailure, when set, is the message of a Codex sign-in the tool ended.
	signInFailure string
}

func newFakeToolEngine(t *testing.T) *fakeToolEngine {
	t.Helper()
	f := &fakeToolEngine{signedIn: map[string]bool{}, signInRts: true, onPath: map[string]bool{}, started: make(chan struct{}), polling: make(chan struct{})}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeToolEngine) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hits = append(f.hits, r.Method+" "+r.URL.RequestURI())
	reply := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	var body map[string]any
	if r.Body != nil {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
	}
	p := strings.TrimPrefix(r.URL.Path, agentToolsPath)
	switch {
	case r.URL.Path == profilesPath+"/readiness" && r.Method == "GET":
		answer := fakeReadiness(f.ready)
		if f.refused {
			for _, tool := range answer["tools"].([]map[string]any) {
				if tool["driver"] == "opencode" {
					tool["reason"], tool["code"], tool["message"] = "api_key", "key_refused", refusedKeySentence("opencode")
				}
			}
		}
		reply(200, answer)
	case r.URL.Path == profilesPath+"/resolve" && r.Method == "GET":
		if d := r.URL.Query().Get("driver"); f.ready[d] || (f.refused && d == "opencode") {
			reply(200, map[string]any{"reason": "api_key"})
			return
		}
		reply(409, map[string]any{"error": map[string]any{"code": "nothing_to_run_on", "message": "not ready"}})
	case p == "/ollama/start" && r.Method == "POST":
		f.ollamaStarted, f.ollamaTenant = true, body["tenant_id"]
		reply(202, map[string]any{"installed": true, "state": "starting"})
	case p == "/ollama" && r.Method == "GET":
		if f.ollamaStarted {
			reply(200, map[string]any{"installed": true, "state": "running", "endpoint": "http://127.0.0.1:11434", "models": []string{}})
			return
		}
		reply(200, map[string]any{"installed": true, "state": "stopped", "models": []string{}})
	case p == "/providers" && r.Method == "GET":
		if f.providers == nil {
			reply(404, map[string]any{"error": map[string]any{"code": "not_found", "message": "no route"}})
			return
		}
		if r.URL.Query().Get("tenant_id") != "tenant-a" {
			reply(400, map[string]any{"error": map[string]any{"code": "bad_request", "message": "tenant_id must name an organization."}})
			return
		}
		reply(200, map[string]any{"providers": f.providers})
	case p == "/inventory":
		answer := map[string]any{
			"drivers": []string{"claude", "codex", "grok", "ollama", "opencode"},
			"inventory": map[string]any{"installed": []map[string]any{
				{"driver": "claude", "version": "2.1.280", "state": "installed", "installed_at": "2026-09-01T00:00:00Z"},
				{"driver": "claude", "version": "2.1.286", "state": "installed", "installed_at": "2026-10-01T09:45:19Z"},
			}},
		}
		if f.presence != nil {
			answer["presence"] = f.presence
		}
		reply(200, answer)
	case p == "/plans":
		f.planRequest = body
		reply(200, map[string]any{"digest": "d1", "driver": body["driver"], "version": "0.159.3", "verification": "sigstore-cosign"})
	case p == "/installs":
		if body["plan_digest"] != "d1" || body["request_id"] == "" {
			reply(400, map[string]any{"error": map[string]any{"code": "bad_request", "message": "bad install"}})
			return
		}
		reply(202, map[string]any{"id": "job-1", "driver": "codex", "version": "0.159.3", "state": "running"})
	case p == "/jobs/job-1":
		f.jobPolls++
		reply(200, map[string]any{"id": "job-1", "driver": "codex", "version": "0.159.3", "state": "succeeded"})
	case !f.signInRts && strings.HasPrefix(p, "/sign-in"):
		reply(404, map[string]any{"error": map[string]any{"code": "not_found", "message": "no route"}})
	case p == "/sign-in" && r.Method == "GET":
		// FH 036: the own login is the organization's; the CLI names its tenant.
		if r.URL.Query().Get("tenant_id") != "tenant-a" {
			reply(400, map[string]any{"error": map[string]any{"code": "bad_request", "message": "Name the organization the login is for (tenant_id)."}})
			return
		}
		d := r.URL.Query().Get("driver")
		installed := d == "claude" || f.onPath[d] // claude: the managed release in the inventory
		reply(200, map[string]any{"driver": d, "installed": installed, "signed_in": installed && f.signedIn[d], "account": map[bool]string{true: "ana@example.com"}[f.signedIn[d]]})
	case p == "/sign-in" && r.Method == "POST":
		if body["tenant_id"] != "tenant-a" {
			reply(400, map[string]any{"error": map[string]any{"code": "bad_request", "message": "Name the organization the login is for (tenant_id)."}})
			return
		}
		if body["driver"] == "opencode" && f.pending {
			reply(202, map[string]any{"id": "s3", "driver": "opencode", "state": "starting"})
			return
		}
		if body["driver"] == "claude" {
			reply(202, map[string]any{"id": "s1", "driver": "claude", "state": "needs_code", "url": "https://claude.ai/oauth/authorize?x=1"})
			return
		}
		if f.signInFailure != "" {
			reply(202, map[string]any{"id": "s2", "driver": "codex", "state": "failed", "message": f.signInFailure})
			return
		}
		reply(202, map[string]any{"id": "s2", "driver": "codex", "state": "waiting", "url": "https://auth.openai.com/codex/device", "user_code": "ABCD-1234"})
	case p == "/sign-in/s1/code":
		f.code, _ = body["code"].(string)
		if f.code != "good-code" {
			reply(202, map[string]any{"id": "s1", "state": "needs_code", "url": "https://claude.ai/oauth/authorize?x=1",
				"message": "That code was not accepted. Paste the newest code from the sign-in page."})
			return
		}
		f.signedIn["claude"] = true
		reply(202, map[string]any{"id": "s1", "state": "signed_in"})
	case p == "/sign-in/s2":
		if r.Method == "DELETE" {
			reply(200, map[string]any{})
			return
		}
		f.startedOnce.Do(func() { close(f.started) })
		if f.pending {
			reply(200, map[string]any{"id": "s2", "state": "waiting"})
			return
		}
		f.signedIn["codex"] = true
		reply(200, map[string]any{"id": "s2", "state": "signed_in"})
	case p == "/sign-in/s3" && r.Method == "GET":
		f.pollingOnce.Do(func() { close(f.polling) })
		reply(200, map[string]any{"id": "s3", "driver": "opencode", "state": "starting"})
	case strings.HasPrefix(p, "/sign-in/") && r.Method == "DELETE":
		reply(200, map[string]any{})
	default:
		reply(404, map[string]any{"error": map[string]any{"code": "not_found", "message": "no route"}})
	}
}

func (f *fakeToolEngine) allHits() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.hits, "\n")
}

func TestToolListShowsVersionAndSignIn(t *testing.T) {
	f := newFakeToolEngine(t)
	out, errb, err := execSessionCLI(t, nil, append([]string{"tool", "ls"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatalf("tool ls: %v\n%s", err, errb)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 3 || !strings.HasPrefix(lines[1], "Claude Code") || !strings.Contains(lines[1], "2.1.286") ||
		!strings.Contains(lines[1], "not signed in") || !strings.HasPrefix(lines[2], "Codex") || !strings.Contains(lines[2], "not installed") {
		t.Fatalf("tool ls =\n%s", out)
	}
	if !strings.Contains(out, "next: olivares tool login claude") {
		t.Fatalf("tool ls must name the next step:\n%s", out)
	}
}

// Inventory presence remains compatible with older engines that did not install
// or sign Gemini CLI in. A host executable is still discoverable.
func TestToolListPreservesOlderGeminiPresenceAnswers(t *testing.T) {
	geminiRow := func(out string) string {
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, "Gemini CLI") {
				return line
			}
		}
		return ""
	}
	t.Run("present on the host", func(t *testing.T) {
		f := newFakeToolEngine(t)
		f.presence = map[string]any{"gemini-cli": map[string]any{"program": "gemini", "present": true}}
		out, errb, err := execSessionCLI(t, nil, append([]string{"tool", "ls"}, sessionCreds(f.URL)...)...)
		if err != nil {
			t.Fatalf("tool ls: %v\n%s", err, errb)
		}
		if row := geminiRow(out); row == "" || !strings.Contains(row, "(on this host)") {
			t.Fatalf("Gemini CLI row = %q in\n%s", row, out)
		}
	})
	t.Run("absent from the host", func(t *testing.T) {
		f := newFakeToolEngine(t)
		f.presence = map[string]any{"gemini-cli": map[string]any{"program": "gemini", "present": false}}
		out, errb, err := execSessionCLI(t, nil, append([]string{"tool", "ls"}, sessionCreds(f.URL)...)...)
		if err != nil {
			t.Fatalf("tool ls: %v\n%s", err, errb)
		}
		if row := geminiRow(out); row == "" || !strings.Contains(row, "not installed") {
			t.Fatalf("Gemini CLI row = %q in\n%s", row, out)
		}
	})
	t.Run("an engine older than the presence answer lists no row", func(t *testing.T) {
		f := newFakeToolEngine(t)
		out, errb, err := execSessionCLI(t, nil, append([]string{"tool", "ls"}, sessionCreds(f.URL)...)...)
		if err != nil {
			t.Fatalf("tool ls: %v\n%s", err, errb)
		}
		if row := geminiRow(out); row != "" {
			t.Fatalf("Gemini CLI row = %q against an engine without presence\n%s", row, out)
		}
	})
}

func TestToolInstallGoesThroughTheEnginesPlanAndJob(t *testing.T) {
	f := newFakeToolEngine(t)
	out, errb, err := execSessionCLI(t, nil, append([]string{"tool", "install", "codex"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatalf("tool install: %v\n%s", err, errb)
	}
	hits := f.allHits()
	for _, want := range []string{"POST " + agentToolsPath + "/plans", "POST " + agentToolsPath + "/installs", "GET " + agentToolsPath + "/jobs/job-1"} {
		if !strings.Contains(hits, want) {
			t.Fatalf("missing %s in\n%s", want, hits)
		}
	}
	if !strings.Contains(errb, "Installing Codex 0.159.3 (verified: sigstore-cosign)") || !strings.Contains(out, "Installed Codex 0.159.3.") ||
		!strings.Contains(out, "next: olivares tool login codex") {
		t.Fatalf("stdout=%q stderr=%q", out, errb)
	}
}

func TestToolInstallUsesTheConsoleReleaseDefaultAndKeepsExplicitVersions(t *testing.T) {
	for _, tool := range []struct{ driver, defaultVersion string }{
		{"claude", "latest"}, {"codex", "latest"}, {"grok", "stable"}, {"opencode", "latest"}, {"gemini", "latest"}, {"ollama", "latest"},
	} {
		for _, version := range []string{"", "latest", "stable", "0.159.3"} {
			name, want := version, version
			args := []string{"tool", "install", tool.driver}
			if version == "" {
				name, want = "default", tool.defaultVersion
			} else {
				args = append(args, "--version", version)
			}
			t.Run(tool.driver+"/"+name, func(t *testing.T) {
				f := newFakeToolEngine(t)
				_, errb, err := execSessionCLI(t, nil, append(args, sessionCreds(f.URL)...)...)
				if err != nil {
					t.Fatalf("tool install: %v\n%s", err, errb)
				}
				f.mu.Lock()
				defer f.mu.Unlock()
				wantDriver := tool.driver
				if wantDriver == "gemini" {
					wantDriver = "gemini-cli"
				}
				if f.planRequest["driver"] != wantDriver || f.planRequest["version"] != want {
					t.Fatalf("plan request = %v, want driver %q version %q", f.planRequest, wantDriver, want)
				}
			})
		}
	}
}

func TestToolLoginClaudeTakesThePastedCode(t *testing.T) {
	f := newFakeToolEngine(t)
	out, errb, err := execSessionCLI(t, strings.NewReader("wrong\ngood-code\n"),
		append([]string{"tool", "login", "claude"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatalf("tool login claude: %v\n%s", err, errb)
	}
	for _, want := range []string{"https://claude.ai/oauth/authorize?x=1", "Paste the code from the page: ",
		"That code was not accepted.", "Claude Code is signed in."} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in\n%s", want, out)
		}
	}
	if f.code != "good-code" {
		t.Fatalf("last code sent = %q", f.code)
	}
	if strings.Contains(f.allHits(), "DELETE") {
		t.Fatal("a finished sign-in must not be cancelled")
	}
}

// A native tool can still be starting when the POST returns after ten
// seconds. That live flow later supplies its device link, or a terminal refusal.
func TestToolLoginWaitsForTheNativeDeviceFlowToStart(t *testing.T) {
	for _, terminal := range []string{"signed_in", "failed"} {
		t.Run(terminal, func(t *testing.T) {
			polls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				flow := map[string]any{"id": "native-starting", "driver": "opencode", "state": "starting"}
				switch {
				case r.Method == "GET" && r.URL.Path == agentToolsPath+"/sign-in":
					flow = map[string]any{"installed": true, "signed_in": false}
				case r.Method == "POST" && r.URL.Path == agentToolsPath+"/sign-in":
					w.WriteHeader(http.StatusAccepted)
				case r.Method == "GET" && r.URL.Path == agentToolsPath+"/sign-in/native-starting":
					polls++
					if polls == 2 && terminal == "failed" {
						flow["state"], flow["message"] = "failed", "Native authorization could not start."
					} else if polls >= 2 {
						flow["state"], flow["url"], flow["user_code"] = "waiting", "https://auth.openai.com/codex/device", "ABCD-12345"
						if polls >= 3 {
							flow["state"] = "signed_in"
						}
					}
				case r.Method == "DELETE" && r.URL.Path == agentToolsPath+"/sign-in/native-starting":
					flow = map[string]any{"ok": true}
				default:
					t.Errorf("unexpected native flow request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
				_ = json.NewEncoder(w).Encode(flow)
			}))
			defer server.Close()
			out, _, err := execSessionCLI(t, nil, append([]string{"tool", "login", "opencode"}, sessionCreds(server.URL)...)...)
			if strings.Count(out, "Starting OpenCode sign-in...") != 1 {
				t.Fatalf("startup guidance must appear once: %q", out)
			}
			if terminal == "failed" {
				if err == nil || !strings.Contains(err.Error(), "Native authorization could not start.") {
					t.Fatalf("native refusal = %v, want its terminal message", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("live native startup was refused: %v", err)
			}
			for _, want := range []string{"https://auth.openai.com/codex/device", "ABCD-12345", "OpenCode is signed in."} {
				if strings.Count(out, want) != 1 {
					t.Fatalf("want %q once in %q", want, out)
				}
			}
		})
	}
}

// TestToolLoginWithAPipedCodeEndsThePromptLine is item 13 of the CLI audit of 09b: with
// the code on a pipe nothing echoes the Enter, so "Paste the code from the page: Claude
// Code is signed in." came out as one line. Each prompt now ends its own line.
func TestToolLoginWithAPipedCodeEndsThePromptLine(t *testing.T) {
	f := newFakeToolEngine(t)
	out, errb, err := execSessionCLI(t, strings.NewReader("wrong\ngood-code\n"),
		append([]string{"tool", "login", "claude"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatalf("tool login claude: %v\n%s", err, errb)
	}
	if n := strings.Count(out, "Paste the code from the page: \n"); n != 2 {
		t.Fatalf("%d prompt(s) end their line, want 2:\n%s", n, out)
	}
	for _, joined := range []string{"page: That code", "page: Claude Code is signed in."} {
		if strings.Contains(out, joined) {
			t.Fatalf("%q shares the prompt's line:\n%s", joined, out)
		}
	}
}

func TestToolLoginCodexShowsTheDeviceCodeAndWaits(t *testing.T) {
	f := newFakeToolEngine(t)
	out, errb, err := execSessionCLI(t, nil, append([]string{"tool", "login", "codex"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatalf("tool login codex: %v\n%s", err, errb)
	}
	if !strings.Contains(out, "https://auth.openai.com/codex/device") || !strings.Contains(out, "Enter this code there: ABCD-1234") ||
		!strings.Contains(out, "Codex is signed in.") {
		t.Fatalf("tool login codex =\n%s", out)
	}
}

// #470: a failed sign-in shows the tool's own reason and the engine's one next
// step, not a second "Try again" after it.
func TestToolLoginFailureShowsTheToolsReasonAndOneNextStep(t *testing.T) {
	f := newFakeToolEngine(t)
	f.signInFailure = "Codex stopped (exit status 1): device code request failed with status 403 Forbidden. Fix that, then start the sign-in again."
	_, _, err := execSessionCLI(t, nil, append([]string{"tool", "login", "codex"}, sessionCreds(f.URL)...)...)
	want := "Codex is not signed in. " + f.signInFailure
	if exitcode.From(err) != exitcode.Err || err == nil || err.Error() != want {
		t.Fatalf("err = %v (exit %d)\nwant  %s", err, exitcode.From(err), want)
	}
}

// HU-R15 (refresh 06): `olivares tool login grok` answered "Grok Build has no sign-in
// here". Grok Build has a device login like Codex's; the CLI relays it the same way.
func TestToolLoginGrokShowsTheDeviceCodeAndWaits(t *testing.T) {
	f := newFakeToolEngine(t)
	out, errb, err := execSessionCLI(t, nil, append([]string{"tool", "login", "grok"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatalf("tool login grok: %v\n%s", err, errb)
	}
	if !strings.Contains(out, "Enter this code there: ABCD-1234") || !strings.Contains(out, "Grok Build is signed in.") {
		t.Fatalf("tool login grok =\n%s", out)
	}
}

func TestToolLoginWhenAlreadySignedIn(t *testing.T) {
	f := newFakeToolEngine(t)
	f.signedIn["claude"] = true
	out, _, err := execSessionCLI(t, nil, append([]string{"tool", "login", "claude"}, sessionCreds(f.URL)...)...)
	if err != nil || strings.TrimSpace(out) != "Claude Code is already signed in as ana@example.com." {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if strings.Contains(f.allHits(), "POST") {
		t.Fatal("an already signed-in tool must not start a new login")
	}
}

func TestToolLoginOnAnEngineWithoutSignIn(t *testing.T) {
	f := newFakeToolEngine(t)
	f.signInRts = false
	_, _, err := execSessionCLI(t, nil, append([]string{"tool", "login", "claude"}, sessionCreds(f.URL)...)...)
	if exitcode.From(err) != exitcode.NotFound || !strings.Contains(err.Error(), "older than this CLI") {
		t.Fatalf("err = %v, want the engine-too-old sentence", err)
	}
}

// TestToolListFindsWhatTheEnginesResolverFinds is HU's R1 refresh 01 finding: a tool
// on the engine host's PATH is installed for sessions, so `tool ls` must not say
// "not installed".
func TestToolListFindsWhatTheEnginesResolverFinds(t *testing.T) {
	f := newFakeToolEngine(t)
	f.onPath["codex"] = true
	out, errb, err := execSessionCLI(t, nil, append([]string{"tool", "ls"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatalf("tool ls: %v\n%s", err, errb)
	}
	lines := strings.Split(out, "\n")
	if !strings.HasPrefix(lines[2], "Codex") || !strings.Contains(lines[2], "(on this host)") || !strings.Contains(lines[2], "not signed in") {
		t.Fatalf("tool ls =\n%s", out)
	}
}
