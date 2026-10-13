// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/governance/testsupport"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/modules/sessions/confine"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWikiCLIStartsExistingSessionWithMatchingOperation(t *testing.T) {
	for _, tool := range []string{"claude", "codex"} {
		for _, op := range []string{"ingest", "query", "lint"} {
			t.Run(tool+"/"+op, func(t *testing.T) {
				f := newFakeSessionEngine(t)
				f.launchDriver = tool
				dir := t.TempDir()
				f.workspaces = []map[string]any{{"workspace_ref": "ws-new", "root_path": dir, "state": "active"}}
				args := []string{"wiki", op, dir, "--tool", tool, "-o", "json"}
				if op == "ingest" {
					args = append(args, "raw/source one.md")
				}
				if op == "query" {
					args = append(args, "Which sources agree?")
				}
				out, errb, err := execSessionCLI(t, nil, append(args, sessionCreds(f.URL)...)...)
				if err != nil {
					t.Fatalf("%v: %s", err, errb)
				}
				if !strings.Contains(out, "run_ref") {
					t.Fatalf("no session id: %s", out)
				}
				posts := f.posts[sessionRunsPath]
				if len(posts) != 1 || posts[0]["workspace_ref"] != "ws-new" {
					t.Fatalf("wrong session start: %+v", posts)
				}
				input := f.posts[sessionRunsPath+"/01a0f6dc-0000-7000-8000-000000000009/input"]
				if len(input) != 1 {
					t.Fatalf("wrong turn: %+v", f.posts)
				}
				if tool == "codex" && input[0]["text"] == nil || tool == "claude" && input[0]["line"] == nil {
					t.Fatalf("wrong native input shape: %+v", input[0])
				}
				// Both driver input shapes preserve the command as data.
				if !strings.Contains(fmtWikiBody(input[0]), "olivares-wiki") || !strings.Contains(fmtWikiBody(input[0]), op) {
					t.Fatalf("wrong operation: %+v", input)
				}
			})
		}
	}
}

func fmtWikiBody(v map[string]any) string { return fmt.Sprint(v) }

func TestWikiInitAPIUsesPinnedFolderAssignmentAndIsIdempotent(t *testing.T) {
	c := bootSkillsComposition(t)
	dir := t.TempDir()
	code, raw := c.do("POST", sessionWorkspacesPath, c.token, map[string]any{"root_path": dir, "name": "wiki fixture"})
	if code != 201 {
		t.Fatalf("register: %d %s", code, raw)
	}
	var ws map[string]any
	if err := json.Unmarshal(raw, &ws); err != nil {
		t.Fatal(err)
	}
	ref := ws["workspace_ref"].(string)
	path := sessionWorkspacesPath + "/" + ref + "/wiki"
	var first sessions.WikiInitResult
	for i := 0; i < 2; i++ {
		code, raw = c.do("POST", path, c.token, nil)
		if code != 200 {
			t.Fatalf("init: %d %s", code, raw)
		}
		var got sessions.WikiInitResult
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got.Assignment.Kind != "folder" || got.Assignment.ID == "" || got.Assignment.Target.ID != ref {
			t.Fatalf("not a folder pin: %+v", got)
		}
		if i == 0 {
			first = got
		} else if got.Assignment.ID != first.Assignment.ID || got.Assignment.RevisionID != first.Assignment.RevisionID {
			t.Fatal("retry changed the assignment")
		}
	}
	for _, name := range []string{"index.md", "log.md", "AGENTS.md"} {
		if body, err := os.ReadFile(filepath.Join(dir, name)); err != nil || len(body) == 0 {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
}

func TestWikiInitAPIRefusesForeignTenantAndMissingAuthentication(t *testing.T) {
	c := bootSkillsComposition(t)
	code, raw := c.do("POST", sessionWorkspacesPath, c.token, map[string]any{"root_path": t.TempDir()})
	if code != 201 {
		t.Fatalf("register: %d %s", code, raw)
	}
	ref := decodeField(t, raw, "workspace_ref")
	path := sessionWorkspacesPath + "/" + ref + "/wiki"
	if code, _ := c.do("POST", path, "", nil); code != 401 {
		t.Fatalf("anonymous init: %d", code)
	}
	org := createOrganizationFixture(t, c.eng.store, "other wiki", "other-wiki")
	other := c
	other.tenant = org.TenantID.String()
	if code, _ := other.do("POST", path, other.token, nil); code != 404 {
		t.Fatalf("foreign init: %d", code)
	}
}

func TestWikiCLIRejectsIngestOutsideRawBeforeStartingSession(t *testing.T) {
	for _, source := range []string{"../secret.md", "/etc/passwd", "raw/../index.md", "raw", "raw/../../secret.md"} {
		t.Run(source, func(t *testing.T) {
			f := newFakeSessionEngine(t)
			_, _, err := execSessionCLI(t, nil, append([]string{"wiki", "ingest", t.TempDir(), source}, sessionCreds(f.URL)...)...)
			if err == nil {
				t.Fatal("invalid source accepted")
			}
			if len(f.postsTo(sessionRunsPath)) != 0 {
				t.Fatal("started session for invalid source")
			}
		})
	}
}

func TestWikiInitAPIKeepsUserSchemaAndSourceBytes(t *testing.T) {
	c := bootSkillsComposition(t)
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "raw"), 0755); err != nil {
		t.Fatal(err)
	}
	source := []byte("Original source\n")
	schema := []byte("Original project schema\n")
	for name, body := range map[string][]byte{"AGENTS.md": schema, "raw/source.md": source} {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0644); err != nil {
			t.Fatal(err)
		}
	}
	code, raw := c.do("POST", sessionWorkspacesPath, c.token, map[string]any{"root_path": dir})
	if code != 201 {
		t.Fatalf("register: %d %s", code, raw)
	}
	ref := decodeField(t, raw, "workspace_ref")
	if code, raw := c.do("POST", sessionWorkspacesPath+"/"+ref+"/wiki", c.token, nil); code != 200 {
		t.Fatalf("init: %d %s", code, raw)
	}
	for name, want := range map[string][]byte{"AGENTS.md": schema, "raw/source.md": source} {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(body) != string(want) {
			t.Fatalf("overwrote %s: %v", name, err)
		}
	}
}

func TestWikiCLIExplicitReferencePreservesRegistration(t *testing.T) {
	for _, op := range []string{"init", "ingest", "query", "lint"} {
		t.Run(op, func(t *testing.T) {
			f := newFakeSessionEngine(t)
			folder := t.TempDir()
			first := "01a0f6dc-0000-7000-8000-000000000011"
			wanted := "01a0f6dc-0000-7000-8000-000000000012"
			f.workspaces = []map[string]any{{"workspace_ref": first, "root_path": folder, "state": "active"}, {"workspace_ref": wanted, "root_path": folder, "state": "active"}}
			args := []string{"wiki", op, wanted, "-o", "json"}
			if op == "ingest" {
				args = append(args, "raw/source.md")
			}
			if op == "query" {
				args = append(args, "What changed?")
			}
			_, stderr, err := execSessionCLI(t, nil, append(args, sessionCreds(f.URL)...)...)
			if err != nil {
				t.Fatalf("%v %s", err, stderr)
			}
			if op == "init" {
				if len(f.postsTo(sessionWorkspacesPath+"/"+wanted+"/wiki")) != 1 {
					t.Fatal("initialized wrong registration")
				}
			} else {
				if got := f.postsTo(sessionRunsPath); len(got) != 1 || got[0]["workspace_ref"] != wanted {
					t.Fatalf("started wrong registration: %+v", got)
				}
			}
			if len(f.postsTo(sessionWorkspacesPath)) != 0 {
				t.Fatal("created replacement registration")
			}
		})
	}
}

func TestWikiCLIExplicitDisabledReferenceRefuses(t *testing.T) {
	f := newFakeSessionEngine(t)
	ref := "01a0f6dc-0000-7000-8000-000000000012"
	f.workspaces = []map[string]any{{"workspace_ref": ref, "root_path": t.TempDir(), "state": "disabled"}}
	if _, _, err := execSessionCLI(t, nil, append([]string{"wiki", "init", ref}, sessionCreds(f.URL)...)...); err == nil {
		t.Fatal("disabled registration accepted")
	}
	if len(f.postsTo(sessionWorkspacesPath)) != 0 || len(f.postsTo(sessionWorkspacesPath+"/"+ref+"/wiki")) != 0 {
		t.Fatal("replaced or initialized disabled registration")
	}
}

func TestWikiFolderAssignmentDeliversOnlyToItsSessionsAndRetainsOnResume(t *testing.T) {
	if state := confine.Probe(); state.Mode != confine.ModeLandlock || state.ABI < 3 {
		t.Skip("truncate-protected Landlock required")
	}
	tool := filepath.Join(t.TempDir(), "claude")
	script := `#!/bin/sh
if [ -f "$HOME/.claude/skills/olivares-wiki/SKILL.md" ]; then
 cat "$HOME/.claude/skills/olivares-wiki/SKILL.md" > delivered.md
fi
printf ready > peer-started.md
printf '{"type":"system","subtype":"init","session_id":"wiki-%s"}\n' "$$"
while IFS= read -r line; do :; done
`
	if err := os.WriteFile(tool, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envSessionClaudeBin, tool)
	c := bootSkillsComposition(t)
	pep, err := buildClaudeHookPEPServer(c.eng, discardLog())
	if err != nil || pep == nil {
		t.Fatalf("hook server: %v", err)
	}
	hooks := httptest.NewServer(pep.Handler)
	t.Cleanup(hooks.Close)
	if err := c.eng.hookCredentials().bindEndpoint(hooks.Listener.Addr().String()); err != nil {
		t.Fatal(err)
	}
	call := func(method, path string, body any, want int) map[string]any {
		t.Helper()
		code, raw := c.do(method, path, c.token, body)
		if code != want {
			t.Fatalf("%s %s: %d %s", method, path, code, raw)
		}
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	folders := []string{t.TempDir(), t.TempDir()}
	refs := []string{}
	for _, folder := range folders {
		refs = append(refs, call("POST", sessionWorkspacesPath, map[string]any{"root_path": folder}, 201)["workspace_ref"].(string))
	}
	initialized := call("POST", sessionWorkspacesPath+"/"+refs[0]+"/wiki", nil, 200)
	profile := call("POST", profilesPath, map[string]any{"driver": "claude", "auth_source": sessions.AuthSourceAccountHome, "config_home": t.TempDir(), "user_home": t.TempDir(), "display_name": "wiki native fixture"}, 201)
	runs := []string{}
	for _, ref := range refs {
		run := call("POST", sessionRunsPath, map[string]any{"transport": "stream-json", "permission_mode": "default", "isolation": "native", "workspace_ref": ref, "provider_profile_ref": profile["profile_ref"]}, 201)
		runRef := run["run_ref"].(string)
		runs = append(runs, runRef)
		t.Cleanup(func() { c.do("POST", sessionRunsPath+"/"+runRef+"/stop", c.token, nil) })
	}
	waitDelivered := func() {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			body, err := os.ReadFile(filepath.Join(folders[0], "delivered.md"))
			if err == nil && strings.Contains(string(body), "name: olivares-wiki") {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("wiki skill did not reach native home: %v", err)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	waitDelivered()
	// The sibling peer must have performed its discovery check before absence
	// proves isolation; process spawn alone does not prove that it has run.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(folders[1], "peer-started.md")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("sibling native peer did not perform its skill discovery check")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(folders[1], "delivered.md")); !os.IsNotExist(err) {
		t.Fatal("wiki assignment leaked to another folder")
	}
	call("POST", sessionRunsPath+"/"+runs[0]+"/stop", nil, 200)
	assignment := initialized["assignment"].(map[string]any)
	if code, raw := c.doHeader("DELETE", "/v1/m/skills/assignments/"+assignment["id"].(string), c.token, fmt.Sprint(assignment["version"]), nil); code != 200 {
		t.Fatalf("unassign: %d %s", code, raw)
	}
	if err := os.Remove(filepath.Join(folders[0], "delivered.md")); err != nil {
		t.Fatal(err)
	}
	call("POST", sessionRunsPath+"/"+runs[0]+"/resume", nil, 200)
	waitDelivered()
}

func TestWikiInitAPIHonorsEveryRequiredPermission(t *testing.T) {
	for _, permission := range []string{"sessions:workspace:admin", "sessions:workspace:write", "skills:catalog:write", "skills:assignment:write"} {
		t.Run(permission, func(t *testing.T) {
			c := bootSkillsComposition(t)
			folder := t.TempDir()
			code, raw := c.do("POST", sessionWorkspacesPath, c.token, map[string]any{"root_path": folder})
			if code != 201 {
				t.Fatalf("register: %d %s", code, raw)
			}
			ref := decodeField(t, raw, "workspace_ref")
			testsupport.SeedCedar(t, c.eng.store, model.TenantID(c.tenant), `forbid(principal, action, resource) when { context.permission == "`+permission+`" };`, c.eng.nhiEnforcer)
			code, raw = c.do("POST", sessionWorkspacesPath+"/"+ref+"/wiki", c.token, nil)
			want := 404
			if permission == "sessions:workspace:write" {
				want = 403
			}
			if code != want {
				t.Fatalf("denied %s: %d %s, want %d", permission, code, raw, want)
			}
			for _, name := range []string{"raw", "wiki", "index.md", "log.md", "AGENTS.md"} {
				if _, err := os.Stat(filepath.Join(folder, name)); !os.IsNotExist(err) {
					t.Errorf("created %s after refusal", name)
				}
			}
		})
	}
}

func TestWikiInitAPIReportsUnavailableArtifactsWithoutCreatingFiles(t *testing.T) {
	c := bootSkillsComposition(t)
	folder := t.TempDir()
	code, raw := c.do("POST", sessionWorkspacesPath, c.token, map[string]any{"root_path": folder})
	if code != 201 {
		t.Fatalf("register: %d %s", code, raw)
	}
	ref := decodeField(t, raw, "workspace_ref")
	if err := os.MkdirAll(filepath.Join(c.eng.dataDir, "skills"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.eng.dataDir, "skills", "artifacts"), []byte("occupied"), 0600); err != nil {
		t.Fatal(err)
	}
	code, raw = c.do("POST", sessionWorkspacesPath+"/"+ref+"/wiki", c.token, nil)
	if code != 503 {
		t.Fatalf("unavailable artifacts: %d %s", code, raw)
	}
	if _, err := os.Stat(filepath.Join(folder, "index.md")); !os.IsNotExist(err) {
		t.Fatal("created files without a verified pack")
	}
}
