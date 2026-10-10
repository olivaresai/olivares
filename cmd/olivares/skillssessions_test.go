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
	"testing"
	"time"

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
