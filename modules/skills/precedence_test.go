// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package skills_test

import (
	"context"
	"slices"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/skills"
)

// A project's own skill overrides a generic one of the same name from a broader
// target, without editing the generic pack: the narrower target in the tree wins.
func TestSkillsSelectionLetsANarrowerTargetOverrideASkillNameFromABroaderOne(t *testing.T) {
	h := catalogOptions(t, skills.Options{Targets: directoryAuthority{}})
	generic := h.install(t, "generic-pack", "generic support")
	project := h.install(t, "project-pack", "project support")
	workspace := h.workspace(t, false)
	template := skills.Target{Kind: "template", ID: model.NewID().String()}
	h.assign(t, workspace, generic.Revision.ID)
	h.assign(t, template, project.Revision.ID)

	got, err := h.selection(workspace, template)
	if err != nil {
		t.Fatalf("a broader and a narrower target disagree on a name: %v", err)
	}
	if len(got.Members) != 1 || got.Members[0].PackID != project.Pack.ID || len(got.Members[0].Origins) != 1 || got.Members[0].Origins[0].Target != template {
		t.Fatalf("the narrower target did not win: %+v", got.Members)
	}
	// What it replaced is recorded with the winner, so a preview and the delivery
	// audit can say so.
	if over := got.Members[0].Overrides; len(over) != 1 || over[0].PackID != generic.Pack.ID || over[0].RevisionID != generic.Revision.ID || over[0].Target != workspace {
		t.Fatalf("the replaced skill is not recorded: %+v", over)
	}
	// Without the narrower target the generic skill is delivered, unchanged.
	alone, err := h.selection(workspace)
	if err != nil || len(alone.Members) != 1 || alone.Members[0].PackID != generic.Pack.ID {
		t.Fatalf("workspace alone: %v %+v", err, alone.Members)
	}
	// The order the caller lists the targets in changes nothing.
	reversed, err := h.selection(template, workspace)
	if err != nil || reversed.Digest != got.Digest {
		t.Fatalf("order changed the selection: %v %s vs %s", err, reversed.Digest, got.Digest)
	}
}

// A resumed conversation keeps its pins; a pin from a narrower target is not
// displaced by a broader target that appears later, and a narrower one added
// later displaces a broader pin.
func TestSkillsSelectionPrecedenceHoldsAcrossAResumedSnapshot(t *testing.T) {
	h := catalogOptions(t, skills.Options{Targets: directoryAuthority{}})
	generic := h.install(t, "generic-resume", "generic support")
	project := h.install(t, "project-resume", "project support")
	session := h.install(t, "session-resume", "session support")
	broad := h.install(t, "broad-resume", "broad support")
	workspace := h.workspace(t, false)
	template := skills.Target{Kind: "template", ID: model.NewID().String()}
	run := skills.Target{Kind: "session", ID: model.NewID().String()}
	h.assign(t, workspace, generic.Revision.ID)
	h.assign(t, template, project.Revision.ID)
	prior, err := h.selection(workspace, template)
	if err != nil {
		t.Fatal(err)
	}
	extend := func(targets ...skills.Target) (skills.Selection, error) {
		var stored []skills.StoredTarget
		for _, target := range targets {
			fact := skills.StoredTarget{Target: target, Version: 1}
			if target.Kind == "workspace" {
				fact.WorkspaceID = workspaceOf(target)
			}
			stored = append(stored, fact)
		}
		var out skills.Selection
		err := h.store.View(context.Background(), h.tenant, func(sc store.Scope) error {
			var err error
			out, err = h.module.ExtendStoredSelection(context.Background(), sc, stored, prior)
			return err
		})
		return out, err
	}

	// A session assignment is narrower than the template's pin and replaces it.
	h.assign(t, run, session.Revision.ID)
	got, err := extend(run)
	if err != nil || len(got.Members) != 1 || got.Members[0].PackID != session.Pack.ID {
		t.Fatalf("a narrower session pin: %v %+v", err, got.Members)
	}

	// A broader target joining the snapshot cannot displace the template's pin.
	other := h.workspace(t, false)
	h.assign(t, other, broad.Revision.ID)
	kept, err := extend(other)
	if err != nil || len(kept.Members) != 1 || kept.Members[0].PackID != project.Pack.ID {
		t.Fatalf("a broader target displaced a narrower pin: %v %+v", err, kept.Members)
	}
	var shadowed []string
	for _, o := range kept.Members[0].Overrides {
		shadowed = append(shadowed, o.PackID)
	}
	if !slices.Contains(shadowed, broad.Pack.ID) {
		t.Fatalf("the broader pin that lost is not recorded: %+v", kept.Members[0].Overrides)
	}
}

func TestSkillsProfilePrecedenceIsBetweenWorkspaceAndFolder(t *testing.T) {
	folder := skills.Target{Kind: "folder", ID: model.NewID().String()}
	h := catalogOptions(t, skills.Options{Targets: directoryAuthority{folderID: folder.ID}})
	workspace := h.workspace(t, false)
	profile := skills.Target{Kind: "profile", ID: "ppf_" + model.NewID().String()}
	for _, target := range []skills.Target{workspace, profile, folder} {
		h.assign(t, target, h.install(t, target.Kind+"-precedence", target.Kind+" support").Revision.ID)
	}
	for _, scenario := range []struct {
		targets []skills.Target
		winner  skills.Target
	}{
		{[]skills.Target{profile, workspace}, profile},
		{[]skills.Target{profile, folder, workspace}, folder},
	} {
		got, err := h.selection(scenario.targets...)
		if err != nil || len(got.Members) != 1 || len(got.Members[0].Origins) != 1 || got.Members[0].Origins[0].Target != scenario.winner {
			t.Fatalf("precedence: %+v %v", got, err)
		}
	}
}
