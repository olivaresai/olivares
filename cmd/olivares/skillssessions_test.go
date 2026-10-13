// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/driverfacts"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/modules/sessions/confine"
	"github.com/olivaresai/olivares/modules/skills"
)

// This peer reports delivered bytes rather than implementing native discovery.
// Discovery itself is qualified separately against the official tool binaries.
func TestSkillsSessionAssignmentsDeliverOnResumeAndRetainPins(t *testing.T) {
	if state := confine.Probe(); state.Mode != confine.ModeLandlock || state.ABI < 3 {
		t.Skip("truncate-protected Landlock required")
	}
	agent := filepath.Join(t.TempDir(), "claude")
	script := `#!/bin/sh
printf '{"type":"system","subtype":"init","session_id":"skill-test-%s"}\n' "$$"
if [ -f "$HOME/.claude/skills/research/SKILL.md" ]; then
  cat "$HOME/.claude/skills/research/SKILL.md" > delivered.md
fi
while IFS= read -r line; do :; done
`
	if err := os.WriteFile(agent, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envSessionClaudeBin, agent)
	c := bootSkillsComposition(t)
	pep, err := buildClaudeHookPEPServer(c.eng, discardLog())
	if err != nil || pep == nil {
		t.Fatalf("hook listener: %v", err)
	}
	hooks := httptest.NewServer(pep.Handler)
	t.Cleanup(hooks.Close)
	if err := c.eng.hookCredentials().bindEndpoint(hooks.Listener.Addr().String()); err != nil {
		t.Fatal(err)
	}
	do := func(method, path string, body any, want int) map[string]any {
		t.Helper()
		code, raw := c.do(method, path, c.token, body)
		if code != want {
			t.Fatalf("%s %s = %d %s", method, path, code, raw)
		}
		var result map[string]any
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	folder := t.TempDir()
	workspace := do("POST", "/v1/m/sessions/workspaces", map[string]any{"root_path": folder, "name": "skills fixture"}, http.StatusCreated)
	profile := do("POST", "/v1/m/sessions/provider-profiles", map[string]any{"driver": "claude", "auth_source": sessions.AuthSourceAccountHome, "config_home": t.TempDir(), "user_home": t.TempDir(), "display_name": "skills fixture"}, http.StatusCreated)
	run := do("POST", "/v1/m/sessions/runs", map[string]any{"transport": "stream-json", "permission_mode": "default", "isolation": "native", "workspace_ref": workspace["workspace_ref"], "provider_profile_ref": profile["profile_ref"]}, http.StatusCreated)
	ref := run["run_ref"].(string)
	path := "/v1/m/sessions/runs/" + ref
	t.Cleanup(func() { _, _ = c.do("POST", path+"/stop", c.token, nil) })
	do("POST", path+"/stop", nil, http.StatusOK)
	if _, err := os.Stat(filepath.Join(folder, "delivered.md")); !os.IsNotExist(err) {
		t.Fatal("unassigned session delivered files")
	}
	revision := c.installPack("session-native")
	code, raw := c.assign(c, "session", ref, revision)
	if code != http.StatusCreated {
		t.Fatalf("assign session: %d %s", code, raw)
	}
	var binding skills.AssignmentResult
	if err := json.Unmarshal(raw, &binding); err != nil {
		t.Fatal(err)
	}
	do("POST", path+"/resume", nil, http.StatusOK)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if got, err := os.ReadFile(filepath.Join(folder, "delivered.md")); err == nil && len(got) != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("assigned session did not receive its SKILL.md")
		}
		time.Sleep(20 * time.Millisecond)
	}
	do("POST", path+"/stop", nil, http.StatusOK)
	if code, raw := c.doHeader("DELETE", "/v1/m/skills/assignments/"+binding.Assignment.ID, c.token, strconv.FormatInt(binding.Assignment.Version, 10), nil); code != http.StatusOK {
		t.Fatalf("unassign: %d %s", code, raw)
	}
	if err := os.Remove(filepath.Join(folder, "delivered.md")); err != nil {
		t.Fatal(err)
	}
	do("POST", path+"/resume", nil, http.StatusOK)
	deadline = time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(folder, "delivered.md")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("recorded pin was lost on resume after unassign")
		}
		time.Sleep(20 * time.Millisecond)
	}
	do("POST", path+"/stop", nil, http.StatusOK)
	detail := do("GET", "/v1/m/skills/packs/"+binding.Assignment.PackID, nil, http.StatusOK)
	version := strconv.FormatInt(int64(detail["pack"].(map[string]any)["version"].(float64)), 10)
	if code, raw := c.doHeader("DELETE", "/v1/m/skills/packs/"+binding.Assignment.PackID, c.token, version, nil); code != http.StatusConflict {
		t.Fatalf("recorded use retirement fence: %d %s", code, raw)
	}

	// Actual agent target IDs differ from the runtime's external identity, and
	// omitted agent/group workspaces inherit the tenant's default workspace.
	var agentID, groupID model.ID
	tenant := model.TenantID(c.tenant)
	if err := c.eng.store.Mutate(t.Context(), tenant, func(sc store.Scope) error {
		a, err := sc.Agents().Create(t.Context(), model.Agent{Name: "skills agent", Kind: "test", ExternalID: "skill-agent-external", Status: model.StatusActive})
		if err != nil {
			return err
		}
		agentID = a.ID
		g, err := sc.AgentGroups().Create(t.Context(), model.AgentGroup{Name: "skills group", Slug: "skills-group", Status: model.StatusActive})
		if err != nil {
			return err
		}
		groupID = g.ID
		_, err = sc.AgentGroupMembers().Create(t.Context(), model.AgentGroupMember{GroupID: g.ID, AgentID: a.ID})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, target := range []skills.Target{{Kind: "agent", ID: agentID.String()}, {Kind: "agent_group", ID: groupID.String()}, {Kind: "workspace", ID: c.workspaceID()}} {
		if code, raw := c.assign(c, target.Kind, target.ID, revision); code != http.StatusCreated {
			t.Fatalf("assign %s: %d %s", target.Kind, code, raw)
		}
	}
	catalog := skills.New(skills.Options{ArtifactRoot: filepath.Join(c.eng.dataDir, "skills", "artifacts")})
	m := sessions.New()
	template := c.template("skills template")
	if code, raw := c.assign(c, "template", template, revision); code != http.StatusCreated {
		t.Fatalf("assign template: %d %s", code, raw)
	}
	active := do("POST", "/v1/m/sessions/runs", map[string]any{"transport": "stream-json", "permission_mode": "default", "isolation": "native", "workspace_ref": workspace["workspace_ref"], "provider_profile_ref": profile["profile_ref"], "template_id": template}, http.StatusCreated)["run_ref"].(string)
	t.Cleanup(func() { _, _ = c.do("POST", "/v1/m/sessions/runs/"+active+"/stop", c.token, nil) })
	if err := c.eng.store.Mutate(t.Context(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("sessions.run")
		if err != nil {
			return err
		}
		rows, _, err := repo.List(t.Context(), model.Query{Limit: 1, Filters: []model.Filter{{Column: "run_ref", Op: model.OpEq, Value: active}}})
		if err != nil {
			return err
		}
		row := rows[0]
		row["skills_selection"], row["agent_ref"] = nil, "skill-agent-external"
		if _, err := repo.Update(t.Context(), row); err != nil {
			return err
		}
		selection, err := m.PinSessionSkillsSelection(t.Context(), sc, active, catalog)
		if err != nil {
			return err
		}
		if len(selection.Members) != 1 || len(selection.Members[0].Origins) != 4 {
			t.Fatalf("agent/group/workspace/template inheritance: %+v", selection)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// A labeled ACP stand-in reads the documented discovery home and attempts to
// modify it. The real composition, imported artifact, workspace assignment,
// provider profile, native process, Landlock boundary and audit are exercised.
func TestWorkspaceSkillsReachGeminiAndOpenCodeSealedAndAudited(t *testing.T) {
	if state := confine.Probe(); state.Mode != confine.ModeLandlock || state.ABI < 3 {
		t.Skip("truncate-protected Landlock required")
	}
	for _, driver := range []string{"gemini-cli", "opencode"} {
		t.Run(driver, func(t *testing.T) {
			facts, _ := driverfacts.Lookup(driver)
			program := filepath.Join(t.TempDir(), facts.Program)
			script := `#!/bin/sh
set -eu
skill_home="$HOME"
config="$OPENCODE_CONFIG_DIR"
if [ "${1:-} ${2:-}" = "auth list" ]; then
  if [ -f "$XDG_DATA_HOME/opencode/auth.json" ]; then printf '●  OpenAI oauth\n└  1 credentials\n'; else printf '└  0 credentials\n'; fi
  exit 0
fi
`
			if driver == "gemini-cli" {
				script = `#!/bin/sh
set -eu
skill_home="$GEMINI_CLI_HOME"
config="$GEMINI_CLI_HOME/.gemini"
`
			}
			script += `cat "$skill_home/.agents/skills/research/SKILL.md" > delivered.md
cat "$config/profile-marker" > profile-marker
if printf changed > "$skill_home/.agents/skills/research/SKILL.md"; then exit 11; fi
if rm "$skill_home/.agents/skills/research/SKILL.md"; then exit 12; fi
if mv "$skill_home/.agents" "$skill_home/replaced"; then exit 13; fi
printf preserved > "$config/session-marker"
printf sealed > sealed
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  [ -n "$id" ] || continue
  case "$line" in
    *'"method":"initialize"'*) result='{"protocolVersion":1,"agentInfo":{"name":"skills-acp-stand-in","version":"test"},"authMethods":[],"agentCapabilities":{"loadSession":true,"mcpCapabilities":{"http":true}}}' ;;
    *'"method":"session/new"'*|*'"method":"session/load"'*) result='{"sessionId":"skills-acp-test","modes":{"currentModeId":"default","availableModes":[{"id":"default"},{"id":"plan"}]}}' ;;
    *'"method":"session/set_mode"'*) result='{}' ;;
    *) printf '{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"unknown stand-in method"}}\n' "$id"; continue ;;
  esac
  printf '{"jsonrpc":"2.0","id":%s,"result":%s}\n' "$id" "$result"
done
`
			if err := os.WriteFile(program, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv(facts.RuntimeBinEnv, program)
			t.Setenv("PATH", filepath.Dir(program)+string(os.PathListSeparator)+os.Getenv("PATH"))
			c := bootSkillsComposition(t)
			pep, err := buildClaudeHookPEPServer(c.eng, discardLog())
			if err != nil || pep == nil {
				t.Fatalf("hook listener: %v", err)
			}
			hooks := httptest.NewServer(pep.Handler)
			t.Cleanup(hooks.Close)
			if err := c.eng.hookCredentials().bindEndpoint(hooks.Listener.Addr().String()); err != nil {
				t.Fatal(err)
			}
			do := func(method, path string, body any, want int) map[string]any {
				t.Helper()
				code, raw := c.do(method, path, c.token, body)
				if code != want {
					t.Fatalf("%s %s = %d %s", method, path, code, raw)
				}
				var result map[string]any
				if err := json.Unmarshal(raw, &result); err != nil {
					t.Fatal(err)
				}
				return result
			}
			folder := t.TempDir()
			workspace := do("POST", "/v1/m/sessions/workspaces", map[string]any{"root_path": folder, "name": "skills workspace"}, http.StatusCreated)
			revision := c.installPack("workspace-native")
			if code, raw := c.assign(c, "workspace", c.workspaceID(), revision); code != http.StatusCreated {
				t.Fatalf("assign workspace = %d %s", code, raw)
			}
			config := t.TempDir()
			if driver == "gemini-cli" {
				config = filepath.Join(config, ".gemini")
				if err := os.Mkdir(config, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(config, "profile-marker"), []byte("selected profile"), 0600); err != nil {
				t.Fatal(err)
			}
			userHome := t.TempDir()
			if driver == "opencode" {
				authFile := filepath.Join(userHome, ".local", "share", "opencode", "auth.json")
				if err := os.MkdirAll(filepath.Dir(authFile), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(authFile, []byte(`{"openai":{"type":"oauth","refresh":"fixture","access":"fixture","expires":0}}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			profile := do("POST", "/v1/m/sessions/provider-profiles", map[string]any{"driver": driver, "auth_source": sessions.AuthSourceAccountHome, "config_home": config, "user_home": userHome, "display_name": "skills profile"}, http.StatusCreated)
			run := do("POST", "/v1/m/sessions/runs", map[string]any{"transport": "stream-json", "permission_mode": "default", "isolation": "native", "workspace_ref": workspace["workspace_ref"], "provider_profile_ref": profile["profile_ref"]}, http.StatusCreated)
			ref := run["run_ref"].(string)
			t.Cleanup(func() { _, _ = c.do("POST", "/v1/m/sessions/runs/"+ref+"/stop", c.token, nil) })
			for name, want := range map[string]string{"delivered.md": "name: research", "profile-marker": "selected profile", "sealed": "sealed"} {
				got, err := os.ReadFile(filepath.Join(folder, name))
				if err != nil || !strings.Contains(string(got), want) {
					t.Fatalf("%s = %q, %v", name, got, err)
				}
			}
			count := 0
			if err := c.eng.store.View(t.Context(), model.TenantID(c.tenant), func(sc store.Scope) error {
				return sc.Audit().(store.CanonicalWalker).WalkCanonical(t.Context(), 0, func(ev model.AuditEvent, raw string, _ []byte) error {
					if ev.Action == "sessions.skills.delivered" {
						var meta struct {
							RunRef  string                  `json:"run_ref"`
							Members []skills.SelectedMember `json:"members"`
						}
						if err := json.Unmarshal([]byte(raw), &meta); err != nil {
							return err
						}
						count++
						if meta.RunRef != ref || len(meta.Members) != 1 || meta.Members[0].RevisionID != revision {
							t.Fatalf("unexpected delivery audit: %+v", meta)
						}
					}
					return nil
				})
			}); err != nil || count != 1 {
				t.Fatalf("workspace delivery audit count=%d err=%v", count, err)
			}
			do("POST", "/v1/m/sessions/runs/"+ref+"/stop", nil, http.StatusOK)
			if got, err := os.ReadFile(filepath.Join(config, "session-marker")); err != nil || string(got) != "preserved" {
				t.Fatalf("selected configuration home is no longer writable: %q %v", got, err)
			}

		})
	}
}
