// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package skills_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/skills"
)

// This test adapter resolves actual tenant-pinned workspace rows. Native target
// authorization is represented by a stored fixture choice; runtime integration
// must supply the sessions owner's production adapter.
type workspaceAuthority struct{}

func (workspaceAuthority) ResolveSkillsTarget(ctx context.Context, sc store.Scope, p auth.Principal, target skills.Target, write bool) (skills.StoredTarget, error) {
	if target.Kind != "workspace" {
		return skills.StoredTarget{}, store.ErrNotFound
	}
	workspace, err := sc.Workspaces().Get(ctx, model.ID(target.ID))
	if err != nil {
		return skills.StoredTarget{}, err
	}
	if workspace.Settings["skills_denied"] == true {
		return skills.StoredTarget{}, auth.ErrRouteDenied
	}
	if write {
		workspace, err = sc.Workspaces().Update(ctx, workspace)
		if err != nil {
			return skills.StoredTarget{}, err
		}
	}
	return skills.StoredTarget{Target: target, Version: workspace.Version, WorkspaceID: workspace.ID}, nil
}
func (h catalogHarness) workspace(t *testing.T, denied bool) skills.Target {
	t.Helper()
	var row model.Workspace
	err := h.store.Mutate(context.Background(), h.tenant, func(sc store.Scope) error {
		var err error
		row, err = sc.Workspaces().Create(context.Background(), model.Workspace{Name: "Skills workspace", Slug: model.NewID().String(), Status: model.StatusActive, Settings: map[string]any{"skills_denied": denied}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return skills.Target{Kind: "workspace", ID: row.ID.String()}
}
func (h catalogHarness) install(t *testing.T, key, content string) skills.InstallResult {
	t.Helper()
	response := h.upload(t, key, key, "", archive(t, []string{"research/SKILL.md", "research/support.md"}, []string{harmless, content}))
	if response.Code != http.StatusCreated {
		t.Fatalf("install: %d %s", response.Code, response.Body.String())
	}
	var result skills.InstallResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func TestSkillsAssignmentRefusesWithoutNativeAuthority(t *testing.T) {
	h := catalog(t)
	pack := h.install(t, "no-authority", "fixture")
	target := h.workspace(t, false)
	response := h.request("POST", "/v1/m/skills/assignments", skills.AssignmentRequest{Target: target, RevisionID: pack.Revision.ID}, "")
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing native authority: %d %s", response.Code, response.Body.String())
	}
}
func TestSkillsAssignmentUsesStoredTargetAuthority(t *testing.T) {
	h := catalogOptions(t, skills.Options{Targets: workspaceAuthority{}})
	pack := h.install(t, "target-authority", "fixture")
	denied := h.workspace(t, true)
	response := h.request("POST", "/v1/m/skills/assignments", skills.AssignmentRequest{Target: denied, RevisionID: pack.Revision.ID}, "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("native target denial: %d %s", response.Code, response.Body.String())
	}
	target := h.workspace(t, false)
	response = h.request("POST", "/v1/m/skills/assignments", skills.AssignmentRequest{Target: target, RevisionID: pack.Revision.ID}, "")
	if response.Code != http.StatusCreated {
		t.Fatalf("assignment: %d %s", response.Code, response.Body.String())
	}
	var assigned skills.AssignmentResult
	if err := json.Unmarshal(response.Body.Bytes(), &assigned); err != nil {
		t.Fatal(err)
	}
	if assigned.Assignment.Target != target || assigned.Assignment.PackID != pack.Pack.ID || len(assigned.Assignment.Members) != 1 || assigned.Assignment.Members[0] != "research" {
		t.Fatalf("stored assignment: %+v", assigned)
	}
	route := "/v1/m/skills/assignments/" + assigned.Assignment.ID
	stale := h.request("DELETE", route, nil, strconv.FormatInt(assigned.Assignment.Version+1, 10))
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale unassign: %d %s", stale.Code, stale.Body.String())
	}
	removed := h.request("DELETE", route, nil, strconv.FormatInt(assigned.Assignment.Version, 10))
	if removed.Code != http.StatusOK {
		t.Fatalf("unassign: %d %s", removed.Code, removed.Body.String())
	}
	listed := h.request("GET", "/v1/m/skills/assignments?target_kind=workspace&target_id="+target.ID, nil, "")
	var bindings struct {
		Items []skills.Assignment `json:"items"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &bindings); err != nil || listed.Code != http.StatusOK || len(bindings.Items) != 0 {
		t.Fatalf("after unassign: %d %s", listed.Code, listed.Body.String())
	}
}
func TestSkillsSelectionRefusesDifferentSupportBytesForSameName(t *testing.T) {
	h := catalogOptions(t, skills.Options{Targets: workspaceAuthority{}})
	first := h.install(t, "pack-one", "reviewed support one")
	second := h.install(t, "pack-two", "different reviewed support")
	target := h.workspace(t, false)
	for _, pack := range []skills.InstallResult{first, second} {
		response := h.request("POST", "/v1/m/skills/assignments", skills.AssignmentRequest{Target: target, RevisionID: pack.Revision.ID}, "")
		if response.Code != http.StatusCreated {
			t.Fatalf("assign: %d %s", response.Code, response.Body.String())
		}
	}
	err := h.store.View(context.Background(), h.tenant, func(sc store.Scope) error {
		_, err := h.module.ResolveSelection(context.Background(), sc, auth.Principal{}, []skills.Target{target})
		return err
	})
	refusal, ok := err.(*skills.ImportError)
	if !ok || refusal.Code != "skill_name_conflict" {
		t.Fatalf("same instructions, different supporting bytes: %v", err)
	}
}
