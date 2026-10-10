// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package skills_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/skills"
)

type packUses struct {
	Items   []skills.Assignment `json:"items"`
	Cursor  string              `json:"cursor"`
	HasMore bool                `json:"has_more"`
}

func (h catalogHarness) packUses(t *testing.T, pack string, query url.Values) packUses {
	t.Helper()
	response := h.request("GET", "/v1/m/skills/packs/"+pack+"/assignments?"+query.Encode(), nil, "")
	var out packUses
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &out) != nil {
		t.Fatalf("pack uses %s: %d %s", query.Encode(), response.Code, response.Body.String())
	}
	return out
}

func TestSkillsPackAssignmentsListOnlyReadableTargets(t *testing.T) {
	h := catalogOptions(t, skills.Options{Targets: workspaceAuthority{}})
	pack := h.install(t, "pack-uses", "fixture")
	other := h.install(t, "pack-other", "other fixture")
	first, hidden, last := h.workspace(t, false), h.workspace(t, false), h.workspace(t, false)
	visible := map[string]bool{}
	for _, target := range []skills.Target{first, last} {
		visible[h.assign(t, target, pack.Revision.ID).ID] = true
	}
	concealed := h.assign(t, hidden, pack.Revision.ID)
	h.assign(t, first, other.Revision.ID)
	// The caller loses native read on one target after its pin was made.
	h.deny(t, hidden)

	all := h.packUses(t, pack.Pack.ID, nil)
	if len(all.Items) != 2 || all.HasMore || all.Cursor != "" {
		t.Fatalf("pack uses: %+v", all)
	}
	for _, a := range all.Items {
		if !visible[a.ID] || a.PackID != pack.Pack.ID || a.RevisionID != pack.Revision.ID {
			t.Fatalf("listed pin %+v is not a readable pin of the pack", a)
		}
	}

	// One pin per page: every cursor is a listed pin, never the concealed one.
	seen := map[string]bool{}
	query := url.Values{"limit": {"1"}}
	for pages := 0; ; pages++ {
		if pages > 3 {
			t.Fatal("pack uses pagination did not end")
		}
		page := h.packUses(t, pack.Pack.ID, query)
		for _, a := range page.Items {
			seen[a.ID] = true
		}
		if page.Cursor == concealed.ID {
			t.Fatalf("cursor names the concealed pin: %+v", page)
		}
		if !page.HasMore {
			if page.Cursor != "" {
				t.Fatalf("last page names a cursor: %+v", page)
			}
			break
		}
		if len(page.Items) != 1 || page.Cursor != page.Items[0].ID {
			t.Fatalf("page cursor is not its listed pin: %+v", page)
		}
		query.Set("cursor", page.Cursor)
	}
	if len(seen) != 2 || seen[concealed.ID] {
		t.Fatalf("paged pack uses: %v", seen)
	}
}

func TestSkillsPackAssignmentsRefuseInvalidPagesAndUnknownPacks(t *testing.T) {
	h := catalogOptions(t, skills.Options{Targets: workspaceAuthority{}})
	pack := h.install(t, "pack-query", "fixture")
	for route, status := range map[string]int{
		"/v1/m/skills/packs/" + pack.Pack.ID + "/assignments?limit=201": http.StatusBadRequest,
		"/v1/m/skills/packs/" + model.NewID().String() + "/assignments": http.StatusNotFound,
	} {
		if response := h.request("GET", route, nil, ""); response.Code != status {
			t.Fatalf("%s: %d %s", route, response.Code, response.Body.String())
		}
	}
}

func (h catalogHarness) deny(t *testing.T, target skills.Target) {
	t.Helper()
	err := h.store.Mutate(context.Background(), h.tenant, func(sc store.Scope) error {
		row, err := sc.Workspaces().Get(context.Background(), model.ID(target.ID))
		if err != nil {
			return err
		}
		row.Settings["skills_denied"] = true
		_, err = sc.Workspaces().Update(context.Background(), row)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Pins are read in ID order, which is creation order. A page that fills inside a
// read batch continues after its last listed pin, not after the batch.
func TestSkillsPackAssignmentsCursorIsTheLastListedPin(t *testing.T) {
	h := catalogOptions(t, skills.Options{Targets: workspaceAuthority{}})
	pack := h.install(t, "pack-cursor", "fixture")
	var pins []skills.Assignment
	var targets []skills.Target
	for i := 0; i < 4; i++ {
		targets = append(targets, h.workspace(t, false))
		pins = append(pins, h.assign(t, targets[i], pack.Revision.ID))
	}
	h.deny(t, targets[1])
	first := h.packUses(t, pack.Pack.ID, url.Values{"limit": {"2"}})
	if len(first.Items) != 2 || first.Items[0].ID != pins[0].ID || first.Items[1].ID != pins[2].ID || !first.HasMore || first.Cursor != pins[2].ID {
		t.Fatalf("first page: %+v", first)
	}
	second := h.packUses(t, pack.Pack.ID, url.Values{"limit": {"2"}, "cursor": {first.Cursor}})
	if len(second.Items) != 1 || second.Items[0].ID != pins[3].ID || second.HasMore || second.Cursor != "" {
		t.Fatalf("second page: %+v", second)
	}
}

type failingTargetAuthority struct {
	workspaceAuthority
	fail string
}

func (a failingTargetAuthority) ResolveSkillsTarget(ctx context.Context, sc store.Scope, p auth.Principal, target skills.Target, write bool) (skills.StoredTarget, error) {
	if target.ID == a.fail && !write {
		return skills.StoredTarget{}, store.ErrStoreUnavailable
	}
	return a.workspaceAuthority.ResolveSkillsTarget(ctx, sc, p, target, write)
}

// A target check that fails, rather than denies, fails the listing: a shorter
// list would read as fewer uses.
func TestSkillsPackAssignmentsFailWhenATargetCannotBeChecked(t *testing.T) {
	authority := &failingTargetAuthority{}
	h := catalogOptions(t, skills.Options{Targets: authority})
	pack := h.install(t, "pack-failure", "fixture")
	readable, broken := h.workspace(t, false), h.workspace(t, false)
	h.assign(t, readable, pack.Revision.ID)
	h.assign(t, broken, pack.Revision.ID)
	authority.fail = broken.ID
	response := h.request("GET", "/v1/m/skills/packs/"+pack.Pack.ID+"/assignments", nil, "")
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("failed target check: %d %s", response.Code, response.Body.String())
	}
}
