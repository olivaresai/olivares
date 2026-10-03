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
	// ready are the tools the resolve preview answers 200 for (others 409); ollama is
	// the product's Ollama service: started by POST /ollama/start, for ollamaTenant.
	ready         map[string]bool
	ollamaStarted bool
	ollamaTenant  any
	planRequest   map[string]any
}

func newFakeToolEngine(t *testing.T) *fakeToolEngine {
	t.Helper()
	f := &fakeToolEngine{signedIn: map[string]bool{}, signInRts: true, onPath: map[string]bool{}, started: make(chan struct{})}
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
	case r.URL.Path == profilesPath+"/resolve" && r.Method == "GET":
		if f.ready[r.URL.Query().Get("driver")] {
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
	case p == "/inventory":
		reply(200, map[string]any{
			"drivers": []string{"claude", "codex", "grok", "ollama", "opencode"},
			"inventory": map[string]any{"installed": []map[string]any{
				{"driver": "claude", "version": "2.1.280", "state": "installed", "installed_at": "2026-09-01T00:00:00Z"},
				{"driver": "claude", "version": "2.1.286", "state": "installed", "installed_at": "2026-10-01T09:45:19Z"},
			}},
		})
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
		if body["driver"] == "claude" {
			reply(202, map[string]any{"id": "s1", "driver": "claude", "state": "needs_code", "url": "https://claude.ai/oauth/authorize?x=1"})
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
		{"claude", "latest"}, {"codex", "latest"}, {"grok", "stable"}, {"opencode", "latest"}, {"ollama", "latest"},
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
				if f.planRequest["driver"] != tool.driver || f.planRequest["version"] != want {
					t.Fatalf("plan request = %v, want driver %q version %q", f.planRequest, tool.driver, want)
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
