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
	"github.com/olivaresai/olivares/modules/governance/testsupport"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/modules/skills"
)

func TestSkillsProfileAssignmentsUseNativeAuthorizationAndPublicRef(t *testing.T) {
	c := bootSkillsComposition(t)
	revision := c.installPack("profile-pin")
	profile, err := c.eng.sessionsMod.CreateProfile(t.Context(), model.TenantID(c.tenant), sessions.CreateProfileInput{Driver: "claude", ConfigHome: t.TempDir(), UserHome: t.TempDir(), AuthSource: sessions.AuthSourceAccountHome})
	if err != nil {
		t.Fatal(err)
	}
	code, raw := c.assign(c, "profile", profile.Ref, revision)
	var result skills.AssignmentResult
	if code != http.StatusCreated || json.Unmarshal(raw, &result) != nil || result.Assignment.ID == "" {
		t.Fatalf("pin profile = %d %s", code, raw)
	}
	pin := result.Assignment
	path := "/v1/m/skills/assignments/" + pin.ID
	for _, route := range []string{"/assignments?target_kind=profile&target_id=" + profile.Ref, "/selection?target_kind=profile&target_id=" + profile.Ref} {
		if code, raw := c.do("GET", "/v1/m/skills"+route, c.token, nil); code != http.StatusOK {
			t.Fatalf("read profile pin = %d %s", code, raw)
		}
	}
	testsupport.SeedCedar(t, c.eng.store, model.TenantID(c.tenant), `forbid(principal, action, resource == Resource::"`+profile.ID.String()+`") when { context.permission == "sessions:profile:write" };`, c.eng.nhiEnforcer)
	for _, probe := range []struct {
		method, path, version string
		body                  any
	}{
		{"POST", "/v1/m/skills/assignments", "", skills.AssignmentRequest{Target: pin.Target, RevisionID: revision}},
		{"PUT", path, strconv.FormatInt(pin.Version, 10), skills.AssignmentRequest{Target: pin.Target, RevisionID: revision}},
		{"DELETE", path, strconv.FormatInt(pin.Version, 10), nil},
		{"GET", "/v1/m/skills/assignments?target_kind=profile&target_id=" + profile.Ref, "", nil},
		{"GET", "/v1/m/skills/selection?target_kind=profile&target_id=" + profile.Ref, "", nil},
	} {
		if code, raw := c.doHeader(probe.method, probe.path, c.token, probe.version, probe.body); code != http.StatusNotFound {
			t.Fatalf("denied profile %s %s = %d %s", probe.method, probe.path, code, raw)
		}
	}
	code, raw = c.do("GET", "/v1/m/skills/packs/"+pin.PackID+"/assignments", c.token, nil)
	var page struct {
		Items []skills.Assignment `json:"items"`
	}
	if code != http.StatusOK || json.Unmarshal(raw, &page) != nil || len(page.Items) != 0 {
		t.Fatalf("denied profile leaks through uses = %d %s", code, raw)
	}
	if code, raw := c.assign(c, "profile", "ppf_"+model.NewID().String(), revision); code != http.StatusNotFound {
		t.Fatalf("absent profile = %d %s", code, raw)
	}
	if code, raw := c.assign(c, "profile", profile.ID.String(), revision); code != http.StatusNotFound {
		t.Fatalf("bare row ID = %d %s", code, raw)
	}
}

func TestSkillsProfilePinsReachOnlyRunsUsingThatProfile(t *testing.T) {
	c := bootSkillsComposition(t)
	tenant := model.TenantID(c.tenant)
	workspace := c.workspaceID()
	profiles := make([]sessions.ProviderProfile, 2)
	for i := range profiles {
		p, err := c.eng.sessionsMod.CreateProfile(t.Context(), tenant, sessions.CreateProfileInput{Driver: "claude", ConfigHome: t.TempDir(), UserHome: t.TempDir(), AuthSource: sessions.AuthSourceAccountHome})
		if err != nil {
			t.Fatal(err)
		}
		profiles[i] = p
	}
	revision := c.installPackMembers("profile-pair", "research", "review")
	code, raw := c.assign(c, "profile", profiles[0].Ref, revision)
	var pin skills.AssignmentResult
	if code != http.StatusCreated || json.Unmarshal(raw, &pin) != nil {
		t.Fatalf("pin pair: %d %s", code, raw)
	}
	catalog := skills.New(skills.Options{})
	for i, profile := range profiles {
		if err := c.eng.store.Mutate(t.Context(), tenant, func(sc store.Scope) error {
			runs, err := sc.Ext("sessions.run")
			if err != nil {
				return err
			}
			ref := model.NewID().String()
			if _, err := runs.Create(t.Context(), model.Record{"run_ref": ref, "transport": "stream-json", "permission_mode": "default", "isolation": "native", "state": "stopped", "last_event_seq": int64(0), "authz_workspace_id": workspace, "provider_profile_id": profile.Ref}); err != nil {
				return err
			}
			targets, err := c.eng.sessionsMod.RunAssignmentTargets(t.Context(), sc, ref)
			if err != nil {
				return err
			}
			selected, err := catalog.ResolveStoredSelection(t.Context(), sc, targets)
			if err != nil {
				return err
			}
			want := 2
			if i == 1 {
				want = 0
			}
			if len(selected.Members) != want {
				t.Fatalf("run on profile %d gets %+v", i, selected.Members)
			}
			for _, member := range selected.Members {
				if member.RevisionID != revision || len(member.Origins) != 1 || member.Origins[0].Kind != "profile" || member.Origins[0].ID != profile.Ref {
					t.Fatalf("profile origin lost: %+v", member)
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if code, raw := c.doHeader("DELETE", "/v1/m/skills/assignments/"+pin.Assignment.ID, c.token, strconv.FormatInt(pin.Assignment.Version, 10), nil); code != http.StatusOK {
		t.Fatalf("disable profile pair: %d %s", code, raw)
	}
	code, raw = c.do("GET", "/v1/m/skills/selection?target_kind=profile&target_id="+profiles[0].Ref, c.token, nil)
	var selection skills.Selection
	if code != http.StatusOK || json.Unmarshal(raw, &selection) != nil || len(selection.Members) != 0 {
		t.Fatalf("disabled pair: %d %s", code, raw)
	}
}
