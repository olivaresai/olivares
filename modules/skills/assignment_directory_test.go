// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package skills_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/skills"
)

// directoryAuthority resolves the stored workspace, agent group and agent rows
// a directory assignment is pinned to. As in the workspace fixture, native
// authorization is the composition root's; this adapter reads the real rows
// through the request's own scope. Templates and sessions are named statically,
// as in the session fixture, so a launch can name the whole tree.
type directoryAuthority struct{}

func (directoryAuthority) ResolveSkillsTarget(ctx context.Context, sc store.Scope, p auth.Principal, target skills.Target, write bool) (skills.StoredTarget, error) {
	id := model.ID(target.ID)
	switch target.Kind {
	case "agent_group":
		row, err := sc.AgentGroups().Get(ctx, id)
		if err != nil {
			return skills.StoredTarget{}, err
		}
		if write {
			if row, err = sc.AgentGroups().Update(ctx, row); err != nil {
				return skills.StoredTarget{}, err
			}
		}
		return skills.StoredTarget{Target: target, Version: row.Version, WorkspaceID: row.WorkspaceID}, nil
	case "agent":
		row, err := sc.Agents().Get(ctx, id)
		if err != nil {
			return skills.StoredTarget{}, err
		}
		if write {
			if row, err = sc.Agents().Update(ctx, row); err != nil {
				return skills.StoredTarget{}, err
			}
		}
		return skills.StoredTarget{Target: target, Version: row.Version, WorkspaceID: row.WorkspaceID}, nil
	case "template", "session":
		return skills.StoredTarget{Target: target, Version: 1}, nil
	}
	return workspaceAuthority{}.ResolveSkillsTarget(ctx, sc, p, target, write)
}

// agentGroup creates a group in workspace (the zero ID is no workspace).
func (h catalogHarness) agentGroup(t *testing.T, slug string, workspace model.ID) skills.Target {
	t.Helper()
	var row model.AgentGroup
	err := h.store.Mutate(context.Background(), h.tenant, func(sc store.Scope) error {
		var err error
		row, err = sc.AgentGroups().Create(context.Background(), model.AgentGroup{WorkspaceID: workspace, Name: slug, Slug: slug, Status: model.StatusActive})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return skills.Target{Kind: "agent_group", ID: row.ID.String()}
}

func (h catalogHarness) agent(t *testing.T, name string, workspace model.ID) skills.Target {
	t.Helper()
	var row model.Agent
	err := h.store.Mutate(context.Background(), h.tenant, func(sc store.Scope) error {
		var err error
		row, err = sc.Agents().Create(context.Background(), model.Agent{WorkspaceID: workspace, Name: name, Kind: "claude-code", ExternalID: name, Status: model.StatusActive})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return skills.Target{Kind: "agent", ID: row.ID.String()}
}

func (h catalogHarness) assign(t *testing.T, target skills.Target, revision string) skills.Assignment {
	t.Helper()
	response := h.request("POST", "/v1/m/skills/assignments", skills.AssignmentRequest{Target: target, RevisionID: revision}, "")
	if response.Code != http.StatusCreated {
		t.Fatalf("assign to %s: %d %s", target.Kind, response.Code, response.Body.String())
	}
	var result skills.AssignmentResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result.Assignment
}

func (h catalogHarness) selection(targets ...skills.Target) (skills.Selection, error) {
	var out skills.Selection
	err := h.store.View(context.Background(), h.tenant, func(sc store.Scope) error {
		var err error
		out, err = h.module.ResolveSelection(context.Background(), sc, auth.Principal{}, targets)
		return err
	})
	return out, err
}

func workspaceOf(target skills.Target) model.ID { return model.ID(target.ID) }

// A skill set is pinned to an agent group or to one agent through the same
// assignment route a workspace uses, and listed back by its target.
func TestSkillsAssignmentPinsToAgentGroupAndAgent(t *testing.T) {
	h := catalogOptions(t, skills.Options{Targets: directoryAuthority{}})
	pack := h.install(t, "directory-pack", "fixture")
	home := workspaceOf(h.workspace(t, false))
	for _, target := range []skills.Target{h.agentGroup(t, "platform", home), h.agent(t, "reviewer", home)} {
		assigned := h.assign(t, target, pack.Revision.ID)
		if assigned.Target != target || assigned.TargetVersion < 1 || len(assigned.Members) != 1 {
			t.Fatalf("%s assignment: %+v", target.Kind, assigned)
		}
		listed := h.request("GET", "/v1/m/skills/assignments?target_kind="+target.Kind+"&target_id="+target.ID, nil, "")
		var page struct {
			Items []skills.Assignment `json:"items"`
		}
		if err := json.Unmarshal(listed.Body.Bytes(), &page); err != nil || listed.Code != http.StatusOK || len(page.Items) != 1 || page.Items[0].ID != assigned.ID {
			t.Fatalf("list %s: %d %s", target.Kind, listed.Code, listed.Body.String())
		}
	}
}

// A group or agent that is not a stored row is never a target: the adapter's
// answer decides, not the request's words. A kind the module does not know is
// refused before any adapter is asked.
func TestSkillsAssignmentRefusesAbsentRowsAndUnknownKinds(t *testing.T) {
	h := catalogOptions(t, skills.Options{Targets: directoryAuthority{}})
	pack := h.install(t, "absent-directory", "fixture")
	for _, kind := range []string{"agent_group", "agent"} {
		absent := skills.Target{Kind: kind, ID: model.NewID().String()}
		response := h.request("POST", "/v1/m/skills/assignments", skills.AssignmentRequest{Target: absent, RevisionID: pack.Revision.ID}, "")
		if response.Code != http.StatusNotFound {
			t.Fatalf("absent %s: %d %s", kind, response.Code, response.Body.String())
		}
	}
	for _, kind := range []string{"agent_groups", "department", "folder", ""} {
		unknown := skills.Target{Kind: kind, ID: model.NewID().String()}
		post := h.request("POST", "/v1/m/skills/assignments", skills.AssignmentRequest{Target: unknown, RevisionID: pack.Revision.ID}, "")
		list := h.request("GET", "/v1/m/skills/assignments?target_kind="+kind+"&target_id="+unknown.ID, nil, "")
		if post.Code != http.StatusBadRequest || list.Code != http.StatusBadRequest {
			t.Fatalf("unknown kind %q: pin %d, list %d", kind, post.Code, list.Code)
		}
	}
}

// A pin is updated and removed at its version through the target's authority.
func TestSkillsDirectoryAssignmentUpdateAndUnpinAreVersioned(t *testing.T) {
	h := catalogOptions(t, skills.Options{Targets: directoryAuthority{}})
	pack := h.install(t, "versioned", "fixture")
	home := workspaceOf(h.workspace(t, false))
	for _, target := range []skills.Target{h.agentGroup(t, "versioned", home), h.agent(t, "versioned", home)} {
		assigned := h.assign(t, target, pack.Revision.ID)
		route := "/v1/m/skills/assignments/" + assigned.ID
		body := skills.AssignmentRequest{Target: target, RevisionID: pack.Revision.ID, Members: []string{"research"}}
		if stale := h.request("PUT", route, body, strconv.FormatInt(assigned.Version+1, 10)); stale.Code != http.StatusConflict {
			t.Fatalf("%s stale update: %d %s", target.Kind, stale.Code, stale.Body.String())
		}
		updated := h.request("PUT", route, body, strconv.FormatInt(assigned.Version, 10))
		var result skills.AssignmentResult
		if err := json.Unmarshal(updated.Body.Bytes(), &result); err != nil || updated.Code != http.StatusOK || result.Assignment.Version <= assigned.Version || result.Assignment.Target != target {
			t.Fatalf("%s update: %d %s", target.Kind, updated.Code, updated.Body.String())
		}
		if stale := h.request("DELETE", route, nil, strconv.FormatInt(assigned.Version, 10)); stale.Code != http.StatusConflict {
			t.Fatalf("%s stale unpin: %d %s", target.Kind, stale.Code, stale.Body.String())
		}
		if removed := h.request("DELETE", route, nil, strconv.FormatInt(result.Assignment.Version, 10)); removed.Code != http.StatusOK {
			t.Fatalf("%s unpin: %d %s", target.Kind, removed.Code, removed.Body.String())
		}
	}
}

// An agent group is deleted outright, so its pin must not outlive it: the pin
// is removable and its pack can be retired.
func TestSkillsPinOfADeletedAgentGroupCanBeUnpinned(t *testing.T) {
	h := catalogOptions(t, skills.Options{Targets: directoryAuthority{}, RefuseReferencedPack: func(context.Context, store.Scope, model.ID) error { return nil }})
	pack := h.install(t, "orphaned", "fixture")
	group := h.agentGroup(t, "doomed", workspaceOf(h.workspace(t, false)))
	assigned := h.assign(t, group, pack.Revision.ID)
	err := h.store.Mutate(context.Background(), h.tenant, func(sc store.Scope) error {
		return sc.AgentGroups().Delete(context.Background(), model.ID(group.ID))
	})
	if err != nil {
		t.Fatal(err)
	}
	removed := h.request("DELETE", "/v1/m/skills/assignments/"+assigned.ID, nil, strconv.FormatInt(assigned.Version, 10))
	if removed.Code != http.StatusOK {
		t.Fatalf("unpin after the group is deleted: %d %s", removed.Code, removed.Body.String())
	}
	route := "/v1/m/skills/packs/" + pack.Pack.ID
	current := h.request("GET", route, nil, "")
	var detail skills.PackDetail
	if err := json.Unmarshal(current.Body.Bytes(), &detail); err != nil || current.Code != http.StatusOK {
		t.Fatalf("read pack: %d %s", current.Code, current.Body.String())
	}
	if retired := h.request("DELETE", route, nil, strconv.FormatInt(detail.Pack.Version, 10)); retired.Code != http.StatusOK {
		t.Fatalf("retire the pack after the pin is gone: %d %s", retired.Code, retired.Body.String())
	}
}

// The pin records the workspace its group belongs to, and a selection refuses a
// group that has since moved to another workspace.
func TestSkillsGroupAssignmentRecordsAndChecksItsWorkspaceLineage(t *testing.T) {
	h := catalogOptions(t, skills.Options{Targets: directoryAuthority{}})
	pack := h.install(t, "lineage", "fixture")
	first, second := workspaceOf(h.workspace(t, false)), workspaceOf(h.workspace(t, false))
	group := h.agentGroup(t, "wanderer", first)
	assigned := h.assign(t, group, pack.Revision.ID)
	err := h.store.View(context.Background(), h.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(skills.AssignmentKind)
		if err != nil {
			return err
		}
		row, err := repo.Get(context.Background(), model.ID(assigned.ID))
		if err != nil {
			return err
		}
		if got := row.String("workspace_id"); got != first.String() {
			t.Errorf("pin workspace = %q, want %q", got, first)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.selection(group); err != nil {
		t.Fatalf("resolve before the move: %v", err)
	}
	err = h.store.Mutate(context.Background(), h.tenant, func(sc store.Scope) error {
		row, err := sc.AgentGroups().Get(context.Background(), model.ID(group.ID))
		if err != nil {
			return err
		}
		row.WorkspaceID = second
		_, err = sc.AgentGroups().Update(context.Background(), row)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.selection(group); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("resolve after the group moved workspaces: %v, want a conflict", err)
	}
}

// A launch passes every target it resolved; the selection reports each one's
// origin in tree order (workspace, template, groups by ID, agent, session), so
// the same launch always digests the same whatever order the caller lists them.
func TestSkillsSelectionCarriesTheWholeTreeInOrder(t *testing.T) {
	h := catalogOptions(t, skills.Options{Targets: directoryAuthority{}})
	pack := h.install(t, "tree-order", "fixture")
	workspace := h.workspace(t, false)
	home := workspaceOf(workspace)
	template := skills.Target{Kind: "template", ID: model.NewID().String()}
	session := skills.Target{Kind: "session", ID: model.NewID().String()}
	agent := h.agent(t, "ordered", home)
	groups := []skills.Target{h.agentGroup(t, "first", home), h.agentGroup(t, "second", home)}
	if groups[0].ID > groups[1].ID {
		groups[0], groups[1] = groups[1], groups[0]
	}
	want := []skills.Target{workspace, template, groups[0], groups[1], agent, session}
	for _, target := range want {
		h.assign(t, target, pack.Revision.ID)
	}
	selected, err := h.selection(session, agent, groups[1], workspace, groups[0], template)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	reversed, err := h.selection(template, groups[0], workspace, groups[1], agent, session)
	if err != nil || reversed.Digest != selected.Digest {
		t.Fatalf("selection depends on the caller's order: %v %s vs %s", err, reversed.Digest, selected.Digest)
	}
	if len(selected.Members) != 1 || len(selected.Members[0].Origins) != len(want) {
		t.Fatalf("members: %+v", selected.Members)
	}
	for i, origin := range selected.Members[0].Origins {
		if origin.Target != want[i] {
			t.Fatalf("origin %d: %+v want %+v", i, origin.Target, want[i])
		}
	}
}

// Two groups may not pin the same skill name to different bytes: the launch is
// refused by name, as it is for any two targets.
func TestSkillsSelectionRefusesGroupsThatDisagreeOnASkill(t *testing.T) {
	h := catalogOptions(t, skills.Options{Targets: directoryAuthority{}})
	one, two := h.install(t, "group-one", "reviewed support one"), h.install(t, "group-two", "different reviewed support")
	home := workspaceOf(h.workspace(t, false))
	first, second := h.agentGroup(t, "alpha", home), h.agentGroup(t, "beta", home)
	h.assign(t, first, one.Revision.ID)
	h.assign(t, second, two.Revision.ID)
	_, err := h.selection(first, second)
	refusal, ok := err.(*skills.ImportError)
	if !ok || refusal.Code != "skill_name_conflict" {
		t.Fatalf("groups disagree on a skill: %v", err)
	}
}

// countingAuthority counts the rows a selection asks its adapter to read.
type countingAuthority struct {
	directoryAuthority
	reads *int
}

func (a countingAuthority) ResolveSkillsTarget(ctx context.Context, sc store.Scope, p auth.Principal, target skills.Target, write bool) (skills.StoredTarget, error) {
	*a.reads++
	return a.directoryAuthority.ResolveSkillsTarget(ctx, sc, p, target, write)
}

// A launch that names a kind the module does not know is refused before any
// row is read, whatever else it names.
func TestSkillsSelectionRefusesAnUnknownKindBeforeReadingAnyRow(t *testing.T) {
	reads := 0
	h := catalogOptions(t, skills.Options{Targets: countingAuthority{reads: &reads}})
	workspace := h.workspace(t, false)
	// An unknown kind ranks with the workspace; its ID sorts after any real one, so
	// without the early refusal the workspace row would be read first.
	unknown := skills.Target{Kind: "department", ID: "zzzzzzzz-zzzz-zzzz-zzzz-zzzzzzzzzzzz"}
	if _, err := h.selection(workspace, unknown); err == nil || reads != 0 {
		t.Fatalf("unknown kind next to a workspace: %v after %d row reads", err, reads)
	}
}

// A launch names each target once, at most one of each kind but the agent
// groups, and a bounded list of groups. Real, pinned groups make the ceiling the
// only reason for a refusal.
func TestSkillsSelectionBoundsAndDeduplicatesTargets(t *testing.T) {
	h := catalogOptions(t, skills.Options{Targets: directoryAuthority{}})
	pack := h.install(t, "bounds", "fixture")
	home := workspaceOf(h.workspace(t, false))
	groups := make([]skills.Target, 0, skills.MaxSelectionGroups+1)
	for i := 0; i <= skills.MaxSelectionGroups; i++ {
		group := h.agentGroup(t, "bounded-"+strconv.Itoa(i), home)
		h.assign(t, group, pack.Revision.ID)
		groups = append(groups, group)
	}
	if selected, err := h.selection(groups[:skills.MaxSelectionGroups]...); err != nil || len(selected.Members) != 1 || len(selected.Members[0].Origins) != skills.MaxSelectionGroups {
		t.Fatalf("the ceiling's worth of groups: %v %+v", err, selected.Members)
	}
	refusedAtTheCeiling := func(err error) bool { return err != nil && !errors.Is(err, store.ErrNotFound) }
	if _, err := h.selection(groups...); !refusedAtTheCeiling(err) {
		t.Fatalf("one group over the ceiling: %v", err)
	}
	absent := make([]skills.Target, 0, skills.MaxSelectionGroups+1)
	for i := 0; i <= skills.MaxSelectionGroups; i++ {
		absent = append(absent, skills.Target{Kind: "agent_group", ID: model.NewID().String()})
	}
	if _, err := h.selection(absent...); !refusedAtTheCeiling(err) {
		t.Fatalf("the ceiling is checked before any group is read: %v", err)
	}
	if _, err := h.selection(groups[0], groups[0]); !refusedAtTheCeiling(err) {
		t.Fatalf("the same group named twice: %v", err)
	}
	for _, kind := range []string{"workspace", "template", "agent", "session"} {
		one := skills.Target{Kind: kind, ID: model.NewID().String()}
		two := skills.Target{Kind: kind, ID: model.NewID().String()}
		if _, err := h.selection(one, two); !refusedAtTheCeiling(err) {
			t.Fatalf("two %s targets: %v", kind, err)
		}
	}
	if _, err := h.selection(skills.Target{Kind: "department", ID: model.NewID().String()}); !refusedAtTheCeiling(err) {
		t.Fatalf("an unknown kind: %v", err)
	}
}
