// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package skills_test

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/skills"
)

// The console's "Installed here" reads a workspace's skills through GET /selection:
// the answer is the selection a new session there pins at launch, from the same
// resolution (ResolveStoredSelection over the workspace's stored target).
func TestSkillsSelectionRouteMatchesLaunchResolution(t *testing.T) {
	h := catalogOptions(t, skills.Options{Targets: workspaceAuthority{}})
	pack := h.install(t, "installed-here", "fixture")
	target := h.workspace(t, false)
	h.assign(t, target, pack.Revision.ID)

	response := h.request("GET", "/v1/m/skills/selection?target_kind=workspace&target_id="+target.ID, nil, "")
	if response.Code != http.StatusOK {
		t.Fatalf("selection: %d %s", response.Code, response.Body.String())
	}
	var got skills.Selection
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	var launch skills.Selection
	err := h.store.View(context.Background(), h.tenant, func(sc store.Scope) error {
		stored, err := workspaceAuthority{}.ResolveSkillsTarget(context.Background(), sc, auth.Principal{}, target, false)
		if err != nil {
			return err
		}
		launch, err = h.module.ResolveStoredSelection(context.Background(), sc, []skills.StoredTarget{stored})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Members) != 1 || got.Members[0].Name != "research" || !reflect.DeepEqual(got, launch) {
		t.Fatalf("route %+v, launch %+v", got, launch)
	}

	empty := h.workspace(t, false)
	response = h.request("GET", "/v1/m/skills/selection?target_kind=workspace&target_id="+empty.ID, nil, "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"members":[]`) {
		t.Fatalf("workspace without pins: %d %s", response.Code, response.Body.String())
	}
}

func TestSkillsSelectionRouteRefusesWhatALaunchRefuses(t *testing.T) {
	h := catalogOptions(t, skills.Options{Targets: workspaceAuthority{}})
	denied := h.workspace(t, true)
	if response := h.request("GET", "/v1/m/skills/selection?target_kind=workspace&target_id="+denied.ID, nil, ""); response.Code != http.StatusNotFound {
		t.Fatalf("native target denial: %d %s", response.Code, response.Body.String())
	}
	if response := h.request("GET", "/v1/m/skills/selection?target_kind=workspace", nil, ""); response.Code != http.StatusBadRequest {
		t.Fatalf("missing target: %d %s", response.Code, response.Body.String())
	}
	target := h.workspace(t, false)
	h.assign(t, target, h.install(t, "pack-one", "reviewed support one").Revision.ID)
	h.assign(t, target, h.install(t, "pack-two", "different reviewed support").Revision.ID)
	response := h.request("GET", "/v1/m/skills/selection?target_kind=workspace&target_id="+target.ID, nil, "")
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "skill_name_conflict") {
		t.Fatalf("a launch would refuse this name conflict: %d %s", response.Code, response.Body.String())
	}
}

// A target the native authority does not admit is not found, whatever its kind,
// and reading a selection never writes its target (the version stays).
func TestSkillsSelectionRouteIsAReadWithNativeAuthority(t *testing.T) {
	h := catalogOptions(t, skills.Options{Targets: workspaceAuthority{}})
	target := h.workspace(t, false)
	h.assign(t, target, h.install(t, "read-only", "fixture").Revision.ID)
	version := func() int64 {
		var v int64
		if err := h.store.View(context.Background(), h.tenant, func(sc store.Scope) error {
			w, err := sc.Workspaces().Get(context.Background(), model.ID(target.ID))
			v = w.Version
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return v
	}
	before := version()
	if response := h.request("GET", "/v1/m/skills/selection?target_kind=workspace&target_id="+target.ID, nil, ""); response.Code != http.StatusOK {
		t.Fatalf("selection: %d %s", response.Code, response.Body.String())
	}
	if after := version(); after != before {
		t.Fatalf("reading the selection wrote its workspace: version %d -> %d", before, after)
	}
	for _, kind := range []string{"folder", "template", "agent_group", "agent", "session"} {
		route := "/v1/m/skills/selection?target_kind=" + kind + "&target_id=" + model.NewID().String()
		if response := h.request("GET", route, nil, ""); response.Code != http.StatusNotFound {
			t.Fatalf("%s outside the native authority: %d %s", kind, response.Code, response.Body.String())
		}
	}
}

// A preview names the tree a launch would use: the answer is the union of the
// named targets, resolved with the launch's own precedence, and a narrower target
// that overrides a skill says so by being the only origin.
func TestSkillsSelectionRouteResolvesTheWholeNamedTree(t *testing.T) {
	h := catalogOptions(t, skills.Options{Targets: directoryAuthority{}})
	workspace := h.workspace(t, false)
	template := skills.Target{Kind: "template", ID: model.NewID().String()}
	generic := h.install(t, "tree-generic", "generic support")
	own := h.upload(t, "tree-own", "tree-own", "", archive(t, []string{"other/SKILL.md"}, []string{strings.Replace(harmless, "name: research", "name: other", 1)}))
	var ownPack skills.InstallResult
	if err := json.Unmarshal(own.Body.Bytes(), &ownPack); err != nil || own.Code != http.StatusCreated {
		t.Fatalf("second pack: %d %s", own.Code, own.Body.String())
	}
	h.assign(t, workspace, generic.Revision.ID)
	h.assign(t, template, ownPack.Revision.ID)

	query := "?target=workspace:" + workspace.ID + "&target=template:" + template.ID
	response := h.request("GET", "/v1/m/skills/selection"+query, nil, "")
	var got skills.Selection
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil || response.Code != http.StatusOK {
		t.Fatalf("tree selection: %d %s", response.Code, response.Body.String())
	}
	var launch skills.Selection
	err := h.store.View(context.Background(), h.tenant, func(sc store.Scope) error {
		var err error
		launch, err = h.module.ResolveSelection(context.Background(), sc, auth.Principal{}, []skills.Target{workspace, template})
		return err
	})
	if err != nil || len(got.Members) != 2 || !reflect.DeepEqual(got, launch) {
		t.Fatalf("route %+v, launch %+v (%v)", got.Members, launch.Members, err)
	}
	// One target in the repeated form answers what the single-target form does.
	single := h.request("GET", "/v1/m/skills/selection?target=workspace:"+workspace.ID, nil, "")
	legacy := h.request("GET", "/v1/m/skills/selection?target_kind=workspace&target_id="+workspace.ID, nil, "")
	if single.Code != http.StatusOK || single.Body.String() != legacy.Body.String() {
		t.Fatalf("forms differ: %d %s vs %d %s", single.Code, single.Body.String(), legacy.Code, legacy.Body.String())
	}
	for name, bad := range map[string]string{
		"malformed pair":  "?target=workspace",
		"unknown kind":    "?target=department:" + workspace.ID,
		"both forms":      "?target=workspace:" + workspace.ID + "&target_kind=workspace&target_id=" + workspace.ID,
		"repeated target": "?target=workspace:" + workspace.ID + "&target=workspace:" + workspace.ID,
		"two workspaces":  "?target=workspace:" + workspace.ID + "&target=workspace:" + model.NewID().String(),
	} {
		if r := h.request("GET", "/v1/m/skills/selection"+bad, nil, ""); r.Code != http.StatusBadRequest && r.Code != http.StatusNotFound {
			t.Fatalf("%s: %d %s", name, r.Code, r.Body.String())
		}
	}
}
