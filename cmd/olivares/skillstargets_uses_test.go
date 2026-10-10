// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance/testsupport"
	"github.com/olivaresai/olivares/modules/skills"
)

// A pack's uses are read through the production target authority: a confined
// department admin sees only its department's pins, and a Cedar forbid on a
// target's native read hides that pin from the owner too.
func TestSkillsPackUsesListOnlyNativelyReadableTargetsThroughHTTP(t *testing.T) {
	c := bootSkillsComposition(t)
	ctx := context.Background()
	revision := c.installPack("uses-pack")
	tenant, err := model.ParseTenantID(c.tenant)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	var home model.ID
	err = c.eng.store.Mutate(ctx, tenant, func(sc store.Scope) error {
		for _, slug := range []string{"home", "other"} {
			ws, err := sc.Workspaces().Create(ctx, model.Workspace{Name: slug, Slug: slug, Status: model.StatusActive})
			if err != nil {
				return err
			}
			if slug == "home" {
				home = ws.ID
			}
			group, err := sc.AgentGroups().Create(ctx, model.AgentGroup{WorkspaceID: ws.ID, Name: slug, Slug: slug, Status: model.StatusActive})
			if err != nil {
				return err
			}
			agent, err := sc.Agents().Create(ctx, model.Agent{WorkspaceID: ws.ID, Name: slug, ExternalID: slug, Kind: "claude-code", Status: model.StatusActive})
			if err != nil {
				return err
			}
			ids["workspace/"+slug], ids["agent_group/"+slug], ids["agent/"+slug] = ws.ID.String(), group.ID.String(), agent.ID.String()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var pack string
	for _, key := range []string{"workspace/home", "workspace/other", "agent_group/home", "agent_group/other", "agent/other"} {
		code, raw := c.assign(c, strings.Split(key, "/")[0], ids[key], revision)
		var result skills.AssignmentResult
		if code != http.StatusCreated || json.Unmarshal(raw, &result) != nil {
			t.Fatalf("owner pins %s = %d %s", key, code, raw)
		}
		pack = result.Assignment.PackID
	}
	uses := func(who skillsComposition) []string {
		t.Helper()
		code, raw := who.do("GET", "/v1/m/skills/packs/"+pack+"/assignments", who.token, nil)
		var page struct {
			Items   []skills.Assignment `json:"items"`
			HasMore bool                `json:"has_more"`
		}
		if code != http.StatusOK || json.Unmarshal(raw, &page) != nil || page.HasMore {
			t.Fatalf("pack uses = %d %s", code, raw)
		}
		var out []string
		for _, a := range page.Items {
			for key, id := range ids {
				if id == a.Target.ID && strings.HasPrefix(key, a.Kind+"/") {
					out = append(out, key)
				}
			}
		}
		sort.Strings(out)
		return out
	}
	want := func(got []string, keys ...string) {
		t.Helper()
		if strings.Join(got, ",") != strings.Join(keys, ",") {
			t.Fatalf("pack uses = %v, want %v", got, keys)
		}
	}

	want(uses(c), "agent/other", "agent_group/home", "agent_group/other", "workspace/home", "workspace/other")
	admin := c.member("department-uses@x.io", "admin")
	c.confineMember(admin, home)
	want(uses(admin), "agent_group/home", "workspace/home")

	source := `forbid(principal, action, resource) when { context.permission == "agent:read" && resource in AgentGroup::"other" };`
	testsupport.SeedCedar(t, c.eng.store, model.TenantID(c.tenant), source, c.eng.nhiEnforcer)
	want(uses(c), "agent/other", "agent_group/home", "workspace/home", "workspace/other")

	// A deleted agent's pin stays listed so it can be unpinned and the pack retired.
	if err := c.eng.store.Mutate(ctx, tenant, func(sc store.Scope) error {
		return sc.Agents().Delete(ctx, model.ID(ids["agent/other"]))
	}); err != nil {
		t.Fatal(err)
	}
	want(uses(c), "agent/other", "agent_group/home", "workspace/home", "workspace/other")

	// The pack's own read policy guards its uses as it guards the pack.
	source += `
forbid(principal, action, resource == Resource::"` + pack + `") when { context.permission == "skills:catalog:read" };`
	testsupport.SeedCedar(t, c.eng.store, model.TenantID(c.tenant), source, c.eng.nhiEnforcer)
	if code, raw := c.do("GET", "/v1/m/skills/packs/"+pack+"/assignments", c.token, nil); code != http.StatusNotFound {
		t.Fatalf("uses of a forbidden pack = %d %s", code, raw)
	}
}
