// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/modules/skills"
)

// A skill is attached to a provider profile only by a caller the profile was assigned
// to: any other caller meets the answer for a profile that does not exist, on write,
// read and list. The callers are editors, who hold both skills:assignment:write and
// sessions:profile:write, so only the assignment can tell them apart. Assignments are
// stored the way a Business install stores them, on the real source-scope resolver.
func TestSkillsProfileTargetFollowsTheProfileAssignment(t *testing.T) {
	c := bootSkillsComposition(t)
	tenant := model.TenantID(c.tenant)
	profile := func() string {
		p, err := c.eng.sessionsMod.CreateProfile(t.Context(), tenant, sessions.CreateProfileInput{Driver: "claude", ConfigHome: t.TempDir(), UserHome: t.TempDir(), AuthSource: sessions.AuthSourceAccountHome})
		if err != nil {
			t.Fatal(err)
		}
		return p.Ref
	}
	forEngineering, forSales, open := profile(), profile(), profile()

	userID := func(who skillsComposition) model.ID {
		p, err := c.eng.authr.Authenticate(t.Context(), who.token)
		if err != nil {
			t.Fatal(err)
		}
		return p.UserID
	}
	engineer, platformer, seller := c.member("engineer@x.io", "editor"), c.member("platform@x.io", "editor"), c.member("seller@x.io", "editor")
	group := func(name string, parent model.ID, member model.ID) model.ID {
		var id model.ID
		if err := c.eng.store.AuthMutate(t.Context(), func(as store.AuthScope) error {
			g, err := as.Groups().Create(t.Context(), model.UserGroup{TargetTenantID: tenant, DisplayName: name, ParentGroupID: parent})
			if err != nil {
				return err
			}
			id = g.ID
			_, err = as.GroupMembers().Create(t.Context(), model.UserGroupMember{GroupID: g.ID, UserID: member})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	engineering := group("engineering", "", userID(engineer))
	group("platform", engineering, userID(platformer)) // nested under engineering: inside it
	sales := group("sales", "", userID(seller))
	assign := func(ref string, g model.ID) {
		if err := c.eng.store.Mutate(t.Context(), tenant, func(sc store.Scope) error {
			repo, err := sc.Ext("sourcescope.binding")
			if err != nil {
				return err
			}
			_, err = repo.Create(t.Context(), model.Record{"source_type": "profile", "source_ref": ref, "scope_tree": "user_group", "scope_ref": g.String(), "enabled": true, "created_by": "fixture", "effect": "allow"})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	assign(forEngineering, engineering)
	assign(forSales, sales)

	revision := c.installPack("assigned-profile-pin")
	// The administrator pins the sales profile first, so there is a pin to read.
	code, raw := c.assign(c, "profile", forSales, revision)
	var pin skills.AssignmentResult
	if code != http.StatusCreated || json.Unmarshal(raw, &pin) != nil {
		t.Fatalf("administrator pins the sales profile = %d %s", code, raw)
	}

	// Denied: another department's profile is not found, whatever the route.
	for name, who := range map[string]skillsComposition{"engineering": engineer, "platform, nested in engineering": platformer} {
		if code, raw := c.assign(who, "profile", forSales, revision); code != http.StatusNotFound {
			t.Errorf("%s pins the sales profile = %d %s, want 404", name, code, raw)
		}
		for _, route := range []string{"assignments", "selection"} {
			if code, raw := c.do("GET", "/v1/m/skills/"+route+"?target_kind=profile&target_id="+forSales, who.token, nil); code != http.StatusNotFound {
				t.Errorf("%s reads %s of the sales profile = %d %s, want 404", name, route, code, raw)
			}
		}
		code, raw := c.do("GET", "/v1/m/skills/packs/"+pin.Assignment.PackID+"/assignments", who.token, nil)
		var uses struct {
			Items []skills.Assignment `json:"items"`
		}
		if code != http.StatusOK || json.Unmarshal(raw, &uses) != nil || len(uses.Items) != 0 {
			t.Errorf("%s lists the pack's uses = %d %s, want the sales pin hidden", name, code, raw)
		}
	}
	if code, raw := c.assign(seller, "profile", forEngineering, revision); code != http.StatusNotFound {
		t.Errorf("sales pins the engineering profile = %d %s, want 404", code, raw)
	}

	// Allowed: the profile assigned to the caller, the one inherited from the department
	// above, and one nobody limited.
	for i, call := range []struct {
		name    string
		who     skillsComposition
		profile string
	}{
		{"engineering on its own profile", engineer, forEngineering},
		{"platform on the profile inherited", platformer, forEngineering},
		{"sales on its own profile", seller, forSales},
		{"engineering on a profile nobody limited", engineer, open},
	} {
		name := call.name
		code, raw := c.assign(call.who, "profile", call.profile, c.installPack("allowed-"+strconv.Itoa(i)))
		if code != http.StatusCreated {
			t.Errorf("%s: pin = %d %s, want 201", name, code, raw)
			continue
		}
		if code, raw := c.do("GET", "/v1/m/skills/selection?target_kind=profile&target_id="+call.profile, call.who.token, nil); code != http.StatusOK {
			t.Errorf("%s: read = %d %s, want 200", name, code, raw)
		}
	}
}
