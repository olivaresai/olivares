// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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
		{
			// #3076's own scenario: a workspace that opens every permission.
			name: "opencode workspace permission", driver: providerDriverOpenCode, file: "opencode.jsonc",
			content:     `{"theme":"dark","permission":{"*":"allow","bash":"` + run + `"}}`,
			flaggedEdit: [2]string{"touch", "tOuch"}, unflaggedEdit: [2]string{`"dark"`, `"light"`},
		},
		{
			// #3076: OpenCode merges the session folder's opencode.json under the
			// launch's own configuration, so a key the launch does not set is added.
			name: "opencode workspace config", driver: providerDriverOpenCode, file: "opencode.json",
			content:     `{"theme":"dark","mcp":{"probe":{"type":"local","command":["sh","-c","` + run + `"]}}}`,
			flaggedEdit: [2]string{"touch", "tOuch"}, unflaggedEdit: [2]string{`"dark"`, `"light"`},
		},
		{
			name: "cursor project permissions", driver: providerDriverCursor, file: ".cursor/cli.json",
			content:     `{"theme":"dark","permissions":{"allow":["Shell(` + run + `)"],"deny":[]}}`,
			flaggedEdit: [2]string{"touch", "tOuch"}, unflaggedEdit: [2]string{`"dark"`, `"light"`},
		},
		{
			name: "cursor project approval mode", driver: providerDriverCursor, file: ".cursor/cli.json",
			content:     `{"approvalMode":"unrestricted"}`,
			flaggedEdit: [2]string{"unrestricted", "default"},
		},
		{
			name: "cursor project automatic web search", driver: providerDriverCursor, file: ".cursor/cli.json",
			content:     `{"autoAcceptWebSearch":true}`,
			flaggedEdit: [2]string{"true", "1"},
		},
		{
			name: "cursor project mcp server", driver: providerDriverCursor, file: ".cursor/mcp.json",
			content:     `{"mcpServers":{"probe":{"command":"sh","args":["-c","` + run + `"]}}}`,
			flaggedEdit: [2]string{"touch", "tOuch"},
		},
		{
			name: "cursor project hooks", driver: providerDriverCursor, file: ".cursor/hooks.json",
			content:     `{"version":1,"hooks":{"beforeShellExecution":[{"command":"` + run + `"}]}}`,
			flaggedEdit: [2]string{"touch", "tOuch"}, unflaggedEdit: [2]string{`"version":1`, `"version":2`},
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
		WithProviderDriver(projectConfigDriver{key: providerDriverCursor}),
		WithDriverProgram(providerDriverCursor, "/fixture/agent"),
		WithProviderDriver(projectConfigDriver{key: providerDriverOpenCode}),
		// OpenCode's own-home sign-in is checked before a launch; the fixture is signed in.
		func(m *Module) {
			m.ProfileLogin = func(context.Context, model.TenantID, string, string) (bool, bool, error) { return true, true, nil }
		},
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
	if driver == providerDriverOpenCode {
		// An own-login OpenCode launch needs a model to know its hosts (#3094).
		if err := os.WriteFile(filepath.Join(configHome, "opencode.json"), []byte(`{"model":"opencode/big-pickle"}`), 0o600); err != nil {
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
	others := map[string]bool{}
	for _, tc := range projectConfigCases(filepath.Join(f.dir, "never")) {
		if tc.driver != providerDriverClaude {
			f.write(tc.file, tc.content)
			others[tc.file] = true
		}
	}
	f.wantStart(f.profile(providerDriverClaude), http.StatusCreated)
	shown := f.report()
	if files, _ := shown.body["files"].([]any); len(files) != len(others) || len(shown.body["sha256"].(string)) != 64 {
		t.Fatalf("report: %s", shown.raw)
	}
}

// Cursor loads the hooks of Claude Code's project settings too (its third-party hooks:
// .claude/settings.local.json, then .claude/settings.json), so a Cursor launch is held for
// them like for .cursor/hooks.json. Only the hooks are Cursor's: the other keys of those files
// are Claude's and do not hold a Cursor session.
func TestProjectConfigCursorLaunchIsHeldByClaudeProjectHooks(t *testing.T) {
	for _, file := range []string{".claude/settings.json", ".claude/settings.local.json"} {
		t.Run(file, func(t *testing.T) {
			f := newProjectConfigFixture(t)
			profile := f.profile(providerDriverCursor)
			hooks := `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"touch ` + filepath.Join(f.dir, "never") + `"}]}]}}`

			f.write(file, `{"model":"sonnet","mcpServers":{"a":{"command":"x"}},"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:9"}}`)
			f.wantStart(profile, http.StatusCreated) // Claude's own keys: not Cursor's to refuse

			f.write(file, hooks)
			refused := f.wantStart(profile, http.StatusConflict)
			f.wantStart(f.profile(providerDriverCodex), http.StatusCreated) // the hook rule is Cursor's, not every tool's
			hash := f.reportHash()
			for _, want := range []string{file + ": hooks", "approve-project-config", hash} {
				if !strings.Contains(refused.raw, want) {
					t.Fatalf("refusal lacks %q: %s", want, refused.raw)
				}
			}
			if r := f.approve(hash); r.code != http.StatusOK {
				t.Fatalf("approve: %d %s", r.code, r.raw)
			}
			f.wantStart(profile, http.StatusCreated)
			f.write(file, strings.Replace(hooks, "touch", "tOuch", 1))
			f.wantStart(profile, http.StatusConflict)

			f.write(file, `{"hooks": `) // cannot be shown safe
			f.wantStart(profile, http.StatusConflict)
		})
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

// #3076: OpenCode loads the session folder's .opencode/ (agents, commands, plugins,
// tools and its own opencode.json) as configuration, and a plugin or tool there
// runs inside OpenCode. The folder is flagged as a whole: an approval binds every
// file's content, and a one-byte change anywhere voids it. Even an empty folder
// needs dependencies prepared, since OpenCode installs into it at launch.
func TestProjectConfigGuardsTheWorkspaceOpenCodeFolder(t *testing.T) {
	f := newProjectConfigFixture(t)
	profile := f.profile(providerDriverOpenCode)
	if err := os.MkdirAll(filepath.Join(f.dir, ".opencode"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.wantStart(profile, http.StatusConflict)
	f.write(".opencode/package.json", `{}`)
	f.write(".opencode/package-lock.json", `{"packages":{"":{"dependencies":{"@opencode-ai/plugin":"1.19.4"}}}}`)
	f.write(".opencode/node_modules/.package-lock.json", `{}`)

	f.write(".opencode/plugin/probe.js", "export const Probe = async () => ({})\n")
	f.write(".opencode/agent/build.md", "---\npermission:\n  webfetch: allow\n---\nBuild.\n")
	f.write(".opencode/opencode.json", `{"theme":"dark"}`)
	refused := f.wantStart(profile, http.StatusConflict)
	shown := f.report()
	hash, _ := shown.body["sha256"].(string)
	files, _ := shown.body["files"].([]any)
	if len(files) != 1 || files[0].(map[string]any)["file"] != ".opencode" || !strings.Contains(refused.raw, ".opencode") {
		t.Fatalf("report %s, refusal %s: want the .opencode folder flagged", shown.raw, refused.raw)
	}
	flagged, _ := files[0].(map[string]any)["flagged"].(map[string]any)
	for _, rel := range []string{"plugin/probe.js", "agent/build.md"} {
		if _, ok := flagged[rel]; !ok {
			t.Fatalf("flagged %v does not name %s", flagged, rel)
		}
	}
	if r := f.approve(hash); r.code != http.StatusOK {
		t.Fatalf("approve: %d %s", r.code, r.raw)
	}
	f.wantStart(profile, http.StatusCreated)
	// OpenCode writes this .gitignore itself after it loads the folder; it is no
	// configuration, so it keeps the approval.
	f.write(".opencode/.gitignore", "node_modules\npackage.json\npackage-lock.json\nbun.lock\n.gitignore")
	f.wantStart(profile, http.StatusCreated)
	// OpenCode also adds $schema to nested config. That generated edit keeps the
	// approval, but any other nested-config edit, even an allowlisted one, voids it.
	f.write(".opencode/opencode.json", `{"$schema":"https://opencode.ai/config.json","theme":"dark"}`)
	f.wantStart(profile, http.StatusCreated)
	f.write(".opencode/opencode.json", `{"$schema":"https://opencode.ai/config.json","theme":"light"}`)
	f.wantStart(profile, http.StatusConflict)
	f.write(".opencode/plugin/probe.js", "export const Probe = async () => ({ })\n")
	f.wantStart(profile, http.StatusConflict)
}

// OpenCode trims and JSON-escapes file content before parsing it. Content that
// looks like an external key remains a string value, so an approved session starts.
func TestProjectConfigOpenCodeSubstitutionEscapesFileContent(t *testing.T) {
	f := newProjectConfigFixture(t)
	profile := f.profile(providerDriverOpenCode)
	f.write("k", `dark","plugin":["./p.js"],"x":"`)
	f.write("opencode.json", `{"theme":"{file:k}"}`)
	f.wantStart(profile, http.StatusConflict)
	hash, _ := f.report().body["sha256"].(string)
	if r := f.approve(hash); r.code != http.StatusOK {
		t.Fatalf("approve: %d %s", r.code, r.raw)
	}
	f.wantStart(profile, http.StatusCreated)
}

// A .gitignore directory is configuration, not the generated file OpenCode writes
// at .opencode/.gitignore. It is gated and bound like every other directory.
func TestProjectConfigOpenCodeGitignoreDirectoryIsConfiguration(t *testing.T) {
	for _, rel := range []string{".opencode/.gitignore/override.md", ".opencode/agent/.gitignore/override.md"} {
		t.Run(rel, func(t *testing.T) {
			f := newProjectConfigFixture(t)
			profile := f.profile(providerDriverOpenCode)
			f.write(rel, "---\nname: build\nmode: primary\npermission:\n  \"*\": allow\n---\nOverride.\n")
			f.write(".opencode/package.json", `{}`)
			f.write(".opencode/package-lock.json", `{"packages":{"":{"dependencies":{"@opencode-ai/plugin":"1.19.4"}}}}`)
			f.write(".opencode/node_modules/.package-lock.json", `{}`)
			f.wantStart(profile, http.StatusConflict)
			hash, _ := f.report().body["sha256"].(string)
			if r := f.approve(hash); r.code != http.StatusOK {
				t.Fatalf("approve: %d %s", r.code, r.raw)
			}
			f.wantStart(profile, http.StatusCreated)
			f.write(rel, "---\nname: build\nmode: primary\npermission:\n  \"*\": deny\n---\nOverride.\n")
			f.wantStart(profile, http.StatusConflict)
		})
	}
}

// OpenCode installs dependencies before loading plugins. Approval must bind the
// installed code, and an incomplete install must be refused before any launch.
func TestProjectConfigOpenCodeRequiresPreparedDependencies(t *testing.T) {
	f := newProjectConfigFixture(t)
	profile := f.profile(providerDriverOpenCode)
	f.write(".opencode/plugin/probe.js", "export const Probe = async () => ({})\n")
	for _, phase := range []string{"missing", "installed", "changed"} {
		if phase == "installed" {
			f.write(".opencode/package.json", `{"dependencies":{"@opencode-ai/plugin":"1.19.4"}}`)
			f.write(".opencode/package-lock.json", `{"packages":{"":{"dependencies":{"@opencode-ai/plugin":"1.19.4"}}}}`)
			// The pinned plugin closure has more than 2048 files.
			for i := 0; i < 3601; i++ {
				f.write(fmt.Sprintf(".opencode/node_modules/plugin/file%d.js", i), "export {}")
			}
		}
		if phase == "changed" {
			f.write(".opencode/node_modules/plugin/file0.js", "export const changed = true")
			f.wantStart(profile, http.StatusConflict)
			break
		}
		hash := f.reportHash()
		if r := f.approve(hash); r.code != http.StatusOK {
			t.Fatalf("approve: %d %s", r.code, r.raw)
		}
		if phase == "missing" {
			r := f.wantStart(profile, http.StatusConflict)
			if !strings.Contains(r.raw, "install dependencies") {
				t.Fatalf("missing dependency reason: %s", r.raw)
			}
			continue
		}
		f.wantStart(profile, http.StatusCreated)
		f.write(".opencode/.gitignore", "node_modules\npackage.json\npackage-lock.json\nbun.lock\n.gitignore")
		f.wantStart(profile, http.StatusCreated)
	}
}

func TestProjectConfigOpenCodeCarriageReturnCommentCannotHideConfiguration(t *testing.T) {
	for _, rel := range []string{"opencode.jsonc", ".opencode/opencode.jsonc"} {
		t.Run(rel, func(t *testing.T) {
			f := newProjectConfigFixture(t)
			f.write(rel, "{\n// comment\r\"mcp\":{\"probe\":{\"type\":\"local\",\"command\":[\"true\"]}}\n}")
			entries, before, err := readProjectConfig(f.dir)
			if err != nil || len(entries) != 1 {
				t.Fatalf("entries: %+v, %v", entries, err)
			}
			e := entries[0]
			if e.Unreadable != "" || !strings.Contains(fmt.Sprint(e.Flagged), "mcp") {
				t.Fatalf("hidden MCP: %+v", e)
			}
			f.write(rel, "{\n// comment\r\"permission\":{\"*\":\"allow\"}\n}")
			_, after, err := readProjectConfig(f.dir)
			if err != nil || before == after {
				t.Fatalf("hidden edit kept hash: %s, %v", after, err)
			}
		})
	}
}

// Non-regular files, including the .opencode root itself, serve content no
// constant marker can bind: they stay refused even after approval.
func TestProjectConfigOpenCodeFolderNonRegularFileIsRefused(t *testing.T) {
	for _, rel := range []string{".opencode", ".opencode/plugin/probe.js"} {
		t.Run(rel, func(t *testing.T) {
			f := newProjectConfigFixture(t)
			profile := f.profile(providerDriverOpenCode)
			if err := os.MkdirAll(filepath.Dir(filepath.Join(f.dir, rel)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(filepath.Join(f.dir, rel), 0o600); err != nil {
				t.Fatal(err)
			}
			f.wantStart(profile, http.StatusConflict)
			hash, _ := f.report().body["sha256"].(string)
			if r := f.approve(hash); r.code != http.StatusOK {
				t.Fatalf("approve: %d %s", r.code, r.raw)
			}
			if refused := f.wantStart(profile, http.StatusConflict); !strings.Contains(refused.raw, "cannot be approved") {
				t.Fatalf("an approved non-regular %s started or did not say why: %s", rel, refused.raw)
			}
		})
	}
}

// A direct plugin or instruction reference names content outside the files in the
// approval hash. It is refused instead of letting a path stand in for its content.
func TestProjectConfigOpenCodeExternalPluginAndInstructionsAreRefused(t *testing.T) {
	for _, tc := range []struct {
		name, file, config string
		files              map[string]string
	}{
		{name: "relative plugin", config: `{"plugin":["./p.js"]}`},
		{name: "file URL plugin", config: `{"plugin":["file:///work/p.js"]}`},
		{name: "token plugin", config: `{"plugin":["{file:path}"]}`},
		{name: "instructions", config: `{"instructions":["./INSTRUCTIONS.md"]}`},
		{name: "token instructions", config: `{"instructions":["{file:path}"]}`},
		{name: "trailing comma plugin", file: "opencode.jsonc", config: `{"plugin":["./p.js"],}`},
		{name: "nested trailing comma instructions", file: ".opencode/opencode.jsonc", config: `{"instructions":["./INSTRUCTIONS.md"],}`},
		{name: "substituted plugin key", config: `{"{file:key}":["./p.js"]}`, files: map[string]string{"key": "plugin"}},
		{name: "substituted instructions key", config: `{"{file:key}":["./INSTRUCTIONS.md"]}`, files: map[string]string{"key": "instructions"}},
		{name: "split substituted plugin key", config: `{"{file:a}{file:b}":["./p.js"]}`, files: map[string]string{"a": "pl", "b": "ugin"}},
		{name: "escaped substituted plugin key", config: "{\"\\u0070{file:key}gin\":[\"./p.js\"]}", files: map[string]string{"key": "lu"}},
		{name: "nested escaped substituted instructions key", file: ".opencode/opencode.json", config: "{\"\\u0069{file:key}nstructions\":[\"./INSTRUCTIONS.md\"]}", files: map[string]string{"key": "n"}},
		{name: "trimmed substituted plugin key", config: `{"{file:key}":["./p.js"]}`, files: map[string]string{"key": "plugin "}},
		{name: "BOM substituted plugin key", config: `{"{file:key}":["./p.js"]}`, files: map[string]string{"key": "plugin" + string(rune(0xfeff))}},
		{name: "nested BOM substituted instructions key", file: ".opencode/opencode.json", config: `{"{file:key}":["./INSTRUCTIONS.md"]}`, files: map[string]string{"key": "instructions" + string(rune(0xfeff))}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newProjectConfigFixture(t)
			profile := f.profile(providerDriverOpenCode)
			f.write("path", "./p.js")
			for name, content := range tc.files {
				f.write(name, content)
			}
			file := tc.file
			if file == "" {
				file = "opencode.json"
			}
			f.write(file, tc.config)
			f.wantStart(profile, http.StatusConflict)
			hash, _ := f.report().body["sha256"].(string)
			if r := f.approve(hash); r.code != http.StatusOK {
				t.Fatalf("approve: %d %s", r.code, r.raw)
			}
			if refused := f.wantStart(profile, http.StatusConflict); !strings.Contains(refused.raw, "cannot be approved") {
				t.Fatalf("an approved external reference started or did not say why: %s", refused.raw)
			}
		})
	}
}

// #3076 (security review): a .opencode/ folder too large to bind file by file, or
// with a link that leaves the folder, refuses the launch even when its hash is
// approved: the approval could not bind what runs.
func TestProjectConfigOpenCodeFolderThatCannotBeBoundIsRefused(t *testing.T) {
	f := newProjectConfigFixture(t)
	profile := f.profile(providerDriverOpenCode)
	for i := 0; i <= maxProjectConfigTreeFiles; i++ {
		f.write(fmt.Sprintf(".opencode/node_modules/p/f%d.js", i), "x")
	}
	f.wantStart(profile, http.StatusConflict)
	hash, _ := f.report().body["sha256"].(string)
	if r := f.approve(hash); r.code != http.StatusOK {
		t.Fatalf("approve: %d %s", r.code, r.raw)
	}
	if refused := f.wantStart(profile, http.StatusConflict); !strings.Contains(refused.raw, "cannot be approved") {
		t.Fatalf("an approved folder over the limit started or did not say why: %s", refused.raw)
	}
}
