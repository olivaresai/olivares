// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionRaceFansPromptIntoWorktrees(t *testing.T) {
	for _, waiting := range []bool{false, true} {
		t.Run(map[bool]string{false: "running", true: "approval"}[waiting], func(t *testing.T) {
			f := newFakeSessionEngine(t)
			f.launchWaits = waiting
			srv := raceTestServer(t, f)
			dir := t.TempDir()
			prompt := filepath.Join(dir, "prompt.txt")
			if err := os.WriteFile(prompt, []byte("Fix the test\n"), 0600); err != nil {
				t.Fatal(err)
			}
			out, errb, err := execSessionCLI(t, nil, append([]string{"session", "race", dir, "--tools", "claude,codex", "--prompt", prompt}, sessionCreds(srv.URL)...)...)
			if err != nil {
				t.Fatalf("race: %v\n%s", err, errb)
			}
			if waiting && (!strings.Contains(out, "waits for approval") || strings.Contains(out, "Started ")) {
				t.Fatalf("approval status misreported: %s", out)
			}
			launches := f.postsTo(sessionRunsPath)
			if len(launches) != 2 {
				t.Fatalf("launches=%v", launches)
			}
			for _, launch := range launches {
				if launch["worktree"] != true {
					t.Fatalf("not isolated: %v", launch)
				}
			}
			if launches[0]["provider_profile_ref"] != "ppf-new-claude" || launches[1]["provider_profile_ref"] != "ppf-new-codex" {
				t.Fatalf("tool bindings changed: %v", launches)
			}
			if launches[1]["worktree_from"] != raceTestBase {
				t.Fatalf("not a common base: %v", launches)
			}
			for i := 9; i <= 10; i++ {
				inputs := f.postsTo(sessionRunsPath + fmt.Sprintf("/01a0f6dc-0000-7000-8000-%012d/queue", i))
				if len(inputs) != 1 || inputs[0]["text"] != "Fix the test\n" {
					t.Fatalf("prompt %d = %v", i, inputs)
				}
			}
			if !strings.Contains(out, "/agentops?race=") {
				t.Fatalf("no comparison link: %s", out)
			}
		})
	}
}

func TestSessionRaceValidatesBeforeLaunch(t *testing.T) {
	for _, tools := range []string{"", "claude", "claude,unknown", "claude,", "claude,claude"} {
		t.Run(tools, func(t *testing.T) {
			f := newFakeSessionEngine(t)
			out, _, err := execSessionCLI(t, nil, append([]string{"session", "race", t.TempDir(), "--tools", tools, "--prompt", "missing"}, sessionCreds(f.URL)...)...)
			if err == nil {
				t.Fatalf("accepted %q: %s", tools, out)
			}
			if len(f.postsTo(sessionRunsPath)) != 0 {
				t.Fatal("invalid request launched a session")
			}
		})
	}
}

func TestSessionRaceRejectsEmptyAndOversizedPromptBeforeLaunch(t *testing.T) {
	for _, text := range []string{"  \n", strings.Repeat("x", 128*1024+1)} {
		f := newFakeSessionEngine(t)
		_, _, err := execSessionCLI(t, strings.NewReader(text), append([]string{"session", "race", t.TempDir(), "--tools", "claude,codex", "--prompt", "-"}, sessionCreds(f.URL)...)...)
		if err == nil {
			t.Fatal("accepted invalid prompt")
		}
		if len(f.postsTo(sessionRunsPath)) != 0 {
			t.Fatal("invalid prompt launched a session")
		}
	}
}

const raceTestBase = "0123456789abcdef0123456789abcdef01234567"

// The surrounding fixture retains normal profile and folder setup; this adapter
// gives successive launches unique run refs and exposes their actual branch base.
func raceTestServer(t *testing.T, f *fakeSessionEngine) *httptest.Server {
	t.Helper()
	launch := 8
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/diff") {
			_ = json.NewEncoder(w).Encode(map[string]any{"base": raceTestBase, "head": raceTestBase, "files": []any{}})
			return
		}
		if r.Method == "POST" && r.URL.Path == sessionRunsPath {
			recorder := httptest.NewRecorder()
			f.serve(recorder, r)
			var run map[string]any
			if err := json.Unmarshal(recorder.Body.Bytes(), &run); err != nil {
				t.Error(err)
				return
			}
			if recorder.Code < 300 {
				launch++
				run["run_ref"] = fmt.Sprintf("01a0f6dc-0000-7000-8000-%012d", launch)
			}
			w.WriteHeader(recorder.Code)
			_ = json.NewEncoder(w).Encode(run)
			return
		}
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/queue") {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				return
			}
			f.mu.Lock()
			f.posts[r.URL.Path] = append(f.posts[r.URL.Path], body)
			f.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		f.serve(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSessionRacePreservesPartialLaunch(t *testing.T) {
	f := newFakeSessionEngine(t)
	srv := raceTestServer(t, f)
	// The second profile is refused after the first session has started.
	f.resolveFails = map[string]int{"codex": http.StatusConflict}
	out, _, err := execSessionCLI(t, strings.NewReader("fix it"), append([]string{"session", "race", t.TempDir(), "--tools", "claude,codex", "--prompt", "-"}, sessionCreds(srv.URL)...)...)
	if err == nil {
		t.Fatal("second launch unexpectedly accepted")
	}
	if !strings.Contains(out, "/agentops?race=") || !strings.Contains(out, "01a0f6dc-0000-7000-8000-000000000009") {
		t.Fatalf("partial sessions hidden: %s", out)
	}
	if len(f.postsTo(sessionRunsPath)) != 1 {
		t.Fatal("wrong number of starts")
	}
	if len(f.postsTo(sessionRunsPath+"/01a0f6dc-0000-7000-8000-000000000009/queue")) != 0 {
		t.Fatal("partial race was sent work")
	}
}

func TestSessionRaceKeepsFolderPermissions(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprint(explicit), func(t *testing.T) {
			f := newFakeSessionEngine(t)
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			f.workspaces = []map[string]any{{"workspace_ref": "ws-1", "root_path": root, "state": "active", "default_permission": "supervised"}}
			srv := raceTestServer(t, f)
			args := []string{"session", "race", root, "--tools", "claude,codex", "--prompt", "-"}
			mode := "default"
			if explicit {
				args = append(args, "--permission", "read-only")
				mode = "plan"
			}
			_, errb, err := execSessionCLI(t, strings.NewReader("Fix it"), append(args, sessionCreds(srv.URL)...)...)
			if err != nil {
				t.Fatalf("race: %v %s", err, errb)
			}
			for _, launch := range f.postsTo(sessionRunsPath) {
				if launch["permission_mode"] != mode || launch["template_id"] != nil {
					t.Fatalf("folder permission overridden: %v", launch)
				}
			}
		})
	}
}
