// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package skills_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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

// A tool acts on hooks, shell interpolation and tool pre-approvals the moment a
// skill loads, so a selection that carries them is pinned only with approval.
func TestSkillsAssignmentNeedsApprovalForContentTheToolActsOn(t *testing.T) {
	h := catalogOptions(t, skills.Options{Targets: workspaceAuthority{}})
	reply := h.upload(t, "active-pack", "active", "", archive(t,
		[]string{"risky/SKILL.md", "safe/SKILL.md"},
		[]string{skillText("risky", "allowed-tools: Bash\n", "Run it.\n"), skillText("safe", "origin: ECC\n", "Read it.\n")}))
	var pack skills.InstallResult
	if err := json.Unmarshal(reply.Body.Bytes(), &pack); err != nil || reply.Code != http.StatusCreated {
		t.Fatalf("install: %d %s", reply.Code, reply.Body.String())
	}
	var risky skills.Member
	for _, m := range pack.Revision.Members {
		if m.Name == "risky" {
			risky = m
		}
	}
	if len(risky.Active) != 1 || risky.Active[0] != "allowed-tools" {
		t.Fatalf("the revision does not show what it carries: %+v", pack.Revision.Members)
	}
	target := h.workspace(t, false)
	post := func(members []string, approve bool) *httptest.ResponseRecorder {
		return h.request("POST", "/v1/m/skills/assignments", skills.AssignmentRequest{Target: target, RevisionID: pack.Revision.ID, Members: members, ApproveActiveContent: approve}, "")
	}

	if refused := post(nil, false); refused.Code != http.StatusBadRequest || !strings.Contains(refused.Body.String(), "active_content_unapproved") {
		t.Fatalf("all members without approval: %d %s", refused.Code, refused.Body.String())
	}
	listed := h.request("GET", "/v1/m/skills/assignments?target_kind=workspace&target_id="+target.ID, nil, "")
	if strings.Contains(listed.Body.String(), pack.Pack.ID) {
		t.Fatalf("a refused assignment was stored: %s", listed.Body.String())
	}
	// The member that carries nothing needs no approval.
	safe := post([]string{"safe"}, false)
	var assigned skills.AssignmentResult
	if err := json.Unmarshal(safe.Body.Bytes(), &assigned); err != nil || safe.Code != http.StatusCreated {
		t.Fatalf("safe member: %d %s", safe.Code, safe.Body.String())
	}
	// Adding the risky member to that pin is a change that needs approval too.
	route := "/v1/m/skills/assignments/" + assigned.Assignment.ID
	version := strconv.FormatInt(assigned.Assignment.Version, 10)
	body := skills.AssignmentRequest{Target: target, RevisionID: pack.Revision.ID}
	if refused := h.request("PUT", route, body, version); refused.Code != http.StatusBadRequest || !strings.Contains(refused.Body.String(), "active_content_unapproved") {
		t.Fatalf("widening without approval: %d %s", refused.Code, refused.Body.String())
	}
	body.ApproveActiveContent = true
	approved := h.request("PUT", route, body, version)
	var widened skills.AssignmentResult
	if err := json.Unmarshal(approved.Body.Bytes(), &widened); err != nil || approved.Code != http.StatusOK {
		t.Fatalf("widening with approval: %d %s", approved.Code, approved.Body.String())
	}
	// Narrowing keeps what was approved: no second approval for the member it holds.
	narrow := skills.AssignmentRequest{Target: target, RevisionID: pack.Revision.ID, Members: []string{"risky"}}
	if narrowed := h.request("PUT", route, narrow, strconv.FormatInt(widened.Assignment.Version, 10)); narrowed.Code != http.StatusOK {
		t.Fatalf("narrowing an approved pin: %d %s", narrowed.Code, narrowed.Body.String())
	}
	// Only the accepted writes are in the audit trail; the refused ones left nothing.
	pins := 0
	err := h.store.View(context.Background(), h.tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 1, func(row model.AuditEvent) error {
			if row.Action == "skills.assignment.pin" {
				pins++
			}
			return nil
		})
	})
	if err != nil || pins != 3 {
		t.Fatalf("pin audit events = %d, want 3: %v", pins, err)
	}
}

// Approval is for what was reviewed: the same skill name at a new revision can carry
// a different hook body, so the next revision needs its own approval.
func TestSkillsAssignmentNeedsApprovalAgainAtANewRevision(t *testing.T) {
	h := catalogOptions(t, skills.Options{Targets: workspaceAuthority{}})
	files := func(body string) []byte {
		return archive(t, []string{"risky/SKILL.md"}, []string{skillText("risky", "allowed-tools: Bash\n", body)})
	}
	first := h.upload(t, "again-1", "again", "", files("Run the first script.\n"))
	var one skills.InstallResult
	if err := json.Unmarshal(first.Body.Bytes(), &one); err != nil || first.Code != http.StatusCreated {
		t.Fatalf("first: %d %s", first.Code, first.Body.String())
	}
	second := h.upload(t, "again-2", "", one.Pack.ID, files("Run a different script.\n"))
	var two skills.InstallResult
	if err := json.Unmarshal(second.Body.Bytes(), &two); err != nil || second.Code != http.StatusCreated {
		t.Fatalf("second: %d %s", second.Code, second.Body.String())
	}
	target := h.workspace(t, false)
	pin := h.request("POST", "/v1/m/skills/assignments", skills.AssignmentRequest{Target: target, RevisionID: one.Revision.ID, ApproveActiveContent: true}, "")
	var assigned skills.AssignmentResult
	if err := json.Unmarshal(pin.Body.Bytes(), &assigned); err != nil || pin.Code != http.StatusCreated {
		t.Fatalf("approved pin: %d %s", pin.Code, pin.Body.String())
	}
	route := "/v1/m/skills/assignments/" + assigned.Assignment.ID
	version := strconv.FormatInt(assigned.Assignment.Version, 10)
	move := skills.AssignmentRequest{Target: target, RevisionID: two.Revision.ID}
	if refused := h.request("PUT", route, move, version); refused.Code != http.StatusBadRequest || !strings.Contains(refused.Body.String(), "active_content_unapproved") {
		t.Fatalf("a new revision moved without approval: %d %s", refused.Code, refused.Body.String())
	}
	move.ApproveActiveContent = true
	if approved := h.request("PUT", route, move, version); approved.Code != http.StatusOK {
		t.Fatalf("a new revision with approval: %d %s", approved.Code, approved.Body.String())
	}
}
