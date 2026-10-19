// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// projectConfigCase is one tool's repo-resident file: content whose flagged
// part runs a marker command, and one edit each of the flagged and the
// unflagged part.
type projectConfigCase struct {
	name, driver, file, content string
	flaggedEdit                 [2]string // replace [0] with [1] inside the flagged part
	unflaggedEdit               [2]string // same inside a key nobody flags; empty: none
}

func projectConfigCases(marker string) []projectConfigCase {
	run := "touch " + marker
	return []projectConfigCase{
		{
			name: "claude settings hook", driver: providerDriverClaude, file: ".claude/settings.json",
			content:     `{"model":"sonnet","hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"` + run + `"}]}]}}`,
			flaggedEdit: [2]string{"touch", "tOuch"}, unflaggedEdit: [2]string{`"sonnet"`, `"opus"`},
		},
		{
			name: "claude settings base url", driver: providerDriverClaude, file: ".claude/settings.local.json",
			content:     `{"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:9","KEEP":"a"}}`,
			flaggedEdit: [2]string{"127.0.0.1:9", "127.0.0.1:8"}, unflaggedEdit: [2]string{`"KEEP":"a"`, `"KEEP":"b"`},
		},
		{
			name: "claude project mcp server", driver: providerDriverClaude, file: ".mcp.json",
			content:     `{"mcpServers":{"probe":{"command":"sh","args":["-c","` + run + `"]}}}`,
			flaggedEdit: [2]string{"touch", "tOuch"},
		},
		{
			name: "codex project config", driver: providerDriverCodex, file: ".codex/config.toml",
			content:     "model = \"x\"\nnotify = [\"sh\", \"-c\", \"" + run + "\"]\n",
			flaggedEdit: [2]string{"touch", "tOuch"}, unflaggedEdit: [2]string{`"x"`, `"y"`},
		},
		{
			name: "gemini workspace settings", driver: providerDriverGemini, file: ".gemini/settings.json",
			content:     "// comments are allowed in this file\n{\"theme\":\"dark\",\"hooks\":{\"BeforeTool\":[{\"command\":\"" + run + "\"}]}}\n",
			flaggedEdit: [2]string{"touch", "tOuch"}, unflaggedEdit: [2]string{`"dark"`, `"light"`},
		},
	}
}

// projectConfigDriver answers the driver contract under another tool's key, so a
// launch of that tool completes without its real protocol.
type projectConfigDriver struct {
	fakeTurnDriver
	key string
}

func (d projectConfigDriver) Key() string            { return d.key }
func (d projectConfigDriver) ConfigHomeEnv() string  { return strings.ToUpper(d.key) + "_HOME" }
func (d projectConfigDriver) DefaultProgram() string { return d.key }
func (d projectConfigDriver) OpenSession(DriverSessionConfig) DriverSession {
	return newFakeTurnSession()
}

// projectConfigFixture is a module with a fake runner, one registered folder and
// the HTTP surface over it.
type projectConfigFixture struct {
	t      *testing.T
	h      *harness
	runner *fakeRunner
	admin  string
	tenant model.TenantID
	dir    string
	ref    string
}

func newProjectConfigFixture(t *testing.T, extra ...Option) *projectConfigFixture {
	t.Helper()
	runner := &fakeRunner{initSID: "sess-project-config"}
	opts := append([]Option{
		WithRunner(runner), WithCredentialSource(staticCred()),
		WithProviderDriver(projectConfigDriver{key: providerDriverCodex}),
		WithProviderDriver(projectConfigDriver{key: providerDriverGemini}),
	}, extra...)
	m := New(opts...)
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "project-config")
	dir := t.TempDir()
	created := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{"root_path": dir}, tenantHdr(tenant))
	if created.code != http.StatusCreated {
		t.Fatalf("register folder: %d %s", created.code, created.raw)
	}
	return &projectConfigFixture{t: t, h: h, runner: runner, admin: admin, tenant: tenant, dir: dir, ref: created.body["workspace_ref"].(string)}
}

func (f *projectConfigFixture) profile(driver string) string {
	f.t.Helper()
	m := f.h.m
	if driver == providerDriverClaude {
		return ensureRuntimeTestProfileRef(f.t, m, f.tenant)
	}
	if m.rt.environmentRef == "" {
		m.UseExecutionEnvironmentRef(testEnvRef)
	}
	userHome := f.t.TempDir()
	configHome := f.t.TempDir()
	if driver == providerDriverGemini {
		configHome = filepath.Join(userHome, ".gemini")
		if err := os.MkdirAll(configHome, 0o700); err != nil {
			f.t.Fatal(err)
		}
	}
	return mustCreateProfile(f.t, m, f.tenant, CreateProfileInput{
		Driver: driver, ConfigHome: configHome, UserHome: userHome,
		DisplayName: "project config " + driver, AuthSource: AuthSourceAccountHome,
	}).Ref
}

// reportHash is the hash the report shows now; "" when the report is not a 200.
func (f *projectConfigFixture) reportHash() string {
	f.t.Helper()
	hash, _ := f.report().body["sha256"].(string)
	return hash
}

func (f *projectConfigFixture) start(profile string) resp {
	f.t.Helper()
	return f.h.doJSON("POST", "/v1/m/sessions/runs", f.admin, map[string]any{"provider_profile_ref": profile, "workspace_ref": f.ref}, tenantHdr(f.tenant))
}

func (f *projectConfigFixture) report() resp {
	f.t.Helper()
	return f.h.do("GET", "/v1/m/sessions/workspaces/"+f.ref+"/project-config", f.admin, tenantHdr(f.tenant))
}

func (f *projectConfigFixture) approve(hash string) resp {
	f.t.Helper()
	return f.h.doJSON("POST", "/v1/m/sessions/workspaces/"+f.ref+"/project-config/approve", f.admin, map[string]any{"sha256": hash}, tenantHdr(f.tenant))
}

func (f *projectConfigFixture) write(rel, content string) {
	f.t.Helper()
	path := filepath.Join(f.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *projectConfigFixture) launches() int {
	f.runner.mu.Lock()
	defer f.runner.mu.Unlock()
	return len(f.runner.specs)
}

// wantStart starts a session and checks the answer; a refusal must also have
// reached no runner, so nothing was spawned.
func (f *projectConfigFixture) wantStart(profile string, code int) resp {
	f.t.Helper()
	before := f.launches()
	r := f.start(profile)
	if r.code != code {
		f.t.Fatalf("start: %d %s, want %d", r.code, r.raw, code)
	}
	if code != http.StatusCreated && f.launches() != before {
		f.t.Fatalf("a refused start reached the runner (%d launches, was %d)", f.launches(), before)
	}
	return r
}

// Per tool: set, approve, change voids, refusal text, nothing executed.
func TestProjectConfigApprovalGuardsEachToolsLaunch(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "must-not-run")
	for _, tc := range projectConfigCases(marker) {
		t.Run(tc.name, func(t *testing.T) {
			f := newProjectConfigFixture(t)
			profile := f.profile(tc.driver)
			f.wantStart(profile, http.StatusCreated) // nothing flagged: no change in behavior

			f.write(tc.file, tc.content)
			refused := f.wantStart(profile, http.StatusConflict)
			shown := f.report()
			hash, _ := shown.body["sha256"].(string)
			files, _ := shown.body["files"].([]any)
			if shown.code != http.StatusOK || len(hash) != 64 || len(files) != 1 || shown.body["approved"] != false {
				t.Fatalf("report: %d %s", shown.code, shown.raw)
			}
			entry := files[0].(map[string]any)
			if entry["file"] != tc.file || len(entry["flagged"].(map[string]any)) == 0 {
				t.Fatalf("report entry: %v", entry)
			}
			for _, want := range []string{tc.file, "approve-project-config", f.ref, hash} {
				if !strings.Contains(refused.raw, want) {
					t.Fatalf("refusal lacks %q: %s", want, refused.raw)
				}
			}

			// An approval names a hash: another hash approves nothing here.
			if r := f.approve(strings.Repeat("0", 64)); r.code != http.StatusOK {
				t.Fatalf("approve: %d %s", r.code, r.raw)
			}
			f.wantStart(profile, http.StatusConflict)

			approved := f.approve(hash)
			if approved.code != http.StatusOK || approved.body["sha256"] != hash || approved.body["actor"] == "" || approved.body["at"] == "" {
				t.Fatalf("approve: %d %s", approved.code, approved.raw)
			}
			f.wantStart(profile, http.StatusCreated)
			if r := f.report(); r.body["approved"] != true {
				t.Fatalf("report after approval: %s", r.raw)
			}

			if tc.unflaggedEdit[0] != "" { // the hash covers the flagged part only
				f.write(tc.file, strings.Replace(tc.content, tc.unflaggedEdit[0], tc.unflaggedEdit[1], 1))
				f.wantStart(profile, http.StatusCreated)
			}

			f.write(tc.file, strings.Replace(tc.content, tc.flaggedEdit[0], tc.flaggedEdit[1], 1)) // one character
			f.wantStart(profile, http.StatusConflict)
			if r := f.report(); r.body["approved"] != false || r.body["sha256"] == hash {
				t.Fatalf("changed text kept its approval: %s", r.raw)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("repo configuration was executed: %v", err)
			}
		})
	}
}

// A tool that does not read a file is not refused for it, and the report still
// lists it with the one hash.
func TestProjectConfigOnlyRefusesTheToolThatReadsTheFile(t *testing.T) {
	f := newProjectConfigFixture(t)
	for _, tc := range projectConfigCases(filepath.Join(f.dir, "never")) {
		if tc.driver != providerDriverClaude {
			f.write(tc.file, tc.content)
		}
	}
	f.wantStart(f.profile(providerDriverClaude), http.StatusCreated)
	shown := f.report()
	if files, _ := shown.body["files"].([]any); len(files) != 2 || len(shown.body["sha256"].(string)) != 64 {
		t.Fatalf("report: %s", shown.raw)
	}
}

// A file the tool would read but the engine cannot read as its format is
// flagged: it cannot be shown safe. A file that is not JSON, one over the size
// limit and a folder in its place each count.
func TestProjectConfigUnreadableFileIsFlagged(t *testing.T) {
	f := newProjectConfigFixture(t)
	profile := f.profile(providerDriverClaude)
	f.write(".claude/settings.json", `{"hooks": `)
	refused := f.wantStart(profile, http.StatusConflict)
	if !strings.Contains(refused.raw, ".claude/settings.json") {
		t.Fatalf("refusal: %s", refused.raw)
	}
	f.write(".claude/settings.json", strings.Repeat(" ", 1<<20+1))
	f.wantStart(profile, http.StatusConflict)
	if err := os.Remove(filepath.Join(f.dir, ".claude", "settings.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(f.dir, ".claude", "settings.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.wantStart(profile, http.StatusConflict)
}

// Display masks a password inside a URL; the approval still binds the real text.
func TestProjectConfigReportMasksURLCredentials(t *testing.T) {
	f := newProjectConfigFixture(t)
	f.write(".claude/settings.json", `{"env":{"HTTPS_PROXY":"http://user:hunter2@proxy.invalid:3128"}}`)
	shown := f.report()
	if shown.code != http.StatusOK || strings.Contains(shown.raw, "hunter2") || !strings.Contains(shown.raw, "proxy.invalid") {
		t.Fatalf("report: %d %s", shown.code, shown.raw)
	}
	hash := shown.body["sha256"].(string)
	f.write(".claude/settings.json", `{"env":{"HTTPS_PROXY":"http://user:hunter3@proxy.invalid:3128"}}`)
	if again := f.report(); again.body["sha256"] == hash {
		t.Fatalf("a changed password kept the same hash: %s", again.raw)
	}
}

// Only a person approves, only with administration, only inside the tenant, and
// a failed audit approves nothing.
func TestProjectConfigApprovalRequiresAdministrationAndAPerson(t *testing.T) {
	f := newProjectConfigFixture(t)
	other := f.h.createOrg(f.admin, "project-config-other")
	f.write(".mcp.json", `{"mcpServers":{"a":{"command":"x"}}}`)
	hash := f.reportHash()
	path := "/v1/m/sessions/workspaces/" + f.ref + "/project-config/approve"
	for _, tc := range []struct {
		token  string
		tenant model.TenantID
		code   int
	}{{"", f.tenant, 401}, {f.admin, other, 404}} {
		if r := f.h.doJSON("POST", path, tc.token, map[string]any{"sha256": hash}, tenantHdr(tc.tenant)); r.code != tc.code {
			t.Fatalf("authority: %d %s, want %d", r.code, r.raw, tc.code)
		}
	}
	for _, body := range []map[string]any{{}, {"sha256": "abc"}, {"sha256": strings.ToUpper(hash)}} {
		if r := f.h.doJSON("POST", path, f.admin, body, tenantHdr(f.tenant)); r.code != http.StatusBadRequest {
			t.Fatalf("invalid body %v: %d %s", body, r.code, r.raw)
		}
	}
	agent := k2AgentToken(t, f.h, f.tenant, "project-config-agent", "", auth.RoleOwner)
	if r := f.h.doJSON("POST", path, agent, map[string]any{"sha256": hash}, tenantHdr(f.tenant)); r.code != http.StatusForbidden {
		t.Fatalf("agent approval: %d %s", r.code, r.raw)
	}
	original := f.h.m.Data
	f.h.m.Data = metadataRollbackData{original}
	r := f.h.doJSON("POST", path, f.admin, map[string]any{"sha256": hash}, tenantHdr(f.tenant))
	f.h.m.Data = original
	if r.code < 400 {
		t.Fatalf("audit failure approved: %d %s", r.code, r.raw)
	}
	if shown := f.report(); shown.body["approved"] != false || shown.body["approval"] != nil {
		t.Fatalf("failed approval persisted: %s", shown.raw)
	}
	if events := acctAuditsOf(t, f.h.m, f.tenant, "sessions.workspace.approve-project-config"); len(events) != 0 {
		t.Fatal("a refused approval left an audit entry")
	}
	if r := f.approve(hash); r.code != http.StatusOK {
		t.Fatalf("approve: %d %s", r.code, r.raw)
	}
	if events := acctAuditsOf(t, f.h.m, f.tenant, "sessions.workspace.approve-project-config"); len(events) != 1 {
		t.Fatalf("approval audit entries: %d", len(events))
	}
}

// A resume reads the folder again: configuration added after the start is
// refused until approved, and the stopped run keeps its place.
func TestProjectConfigGuardsResume(t *testing.T) {
	f := newProjectConfigFixture(t)
	profile := f.profile(providerDriverClaude)
	started := f.wantStart(profile, http.StatusCreated)
	runRef := started.body["run_ref"].(string)
	waitFor(t, "provider identity", func() bool {
		r := f.h.do("GET", "/v1/m/sessions/runs/"+runRef, f.admin, tenantHdr(f.tenant))
		return r.code == http.StatusOK && r.body["claude_session_id"] == "sess-project-config"
	})
	if r := f.h.doJSON("POST", "/v1/m/sessions/runs/"+runRef+"/stop", f.admin, map[string]any{}, tenantHdr(f.tenant)); r.code != http.StatusOK {
		t.Fatalf("stop: %d %s", r.code, r.raw)
	}
	f.write(".claude/settings.json", `{"statusLine":{"type":"command","command":"echo hi"}}`)
	resume := func(want int) resp {
		t.Helper()
		r := f.h.doJSON("POST", "/v1/m/sessions/runs/"+runRef+"/resume", f.admin, map[string]any{}, tenantHdr(f.tenant))
		if r.code != want {
			t.Fatalf("resume: %d %s, want %d", r.code, r.raw, want)
		}
		return r
	}
	refused := resume(http.StatusConflict)
	if !strings.Contains(refused.raw, ".claude/settings.json") || !strings.Contains(refused.raw, "approve-project-config") {
		t.Fatalf("refusal: %s", refused.raw)
	}
	if r := f.approve(f.reportHash()); r.code != http.StatusOK {
		t.Fatalf("approve: %d %s", r.code, r.raw)
	}
	resume(http.StatusOK)
}
