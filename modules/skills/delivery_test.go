// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package skills_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/skills"
)

func TestSkillsDeliveryReadsOnlyPinnedMembersAndVerifiesSupportBytes(t *testing.T) {
	h := catalogOptions(t, skills.Options{Targets: workspaceAuthority{}})
	response := h.upload(t, "native-delivery", "native-delivery", "", archive(t, []string{"research/SKILL.md", "research/support.md", "other/SKILL.md", "other/support.md"}, []string{harmless, "reviewed support", "---\nname: other\ndescription: An unselected skill.\nlicense: MIT\n---\nOther instructions.\n", "do not deliver"}))
	var pack skills.InstallResult
	if err := json.Unmarshal(response.Body.Bytes(), &pack); err != nil || response.Code != http.StatusCreated {
		t.Fatalf("install: %d %s", response.Code, response.Body.String())
	}
	target := h.workspace(t, false)
	if reply := h.request("POST", "/v1/m/skills/assignments", skills.AssignmentRequest{Target: target, RevisionID: pack.Revision.ID, Members: []string{"research"}}, ""); reply.Code != http.StatusCreated {
		t.Fatalf("assign: %d %s", reply.Code, reply.Body.String())
	}
	ctx := context.Background()
	var selection skills.Selection
	err := h.store.View(ctx, h.tenant, func(sc store.Scope) error {
		stored, err := (workspaceAuthority{}).ResolveSkillsTarget(ctx, sc, auth.Principal{}, target, false)
		if err != nil {
			return err
		}
		selection, err = h.module.ResolveStoredSelection(ctx, sc, []skills.StoredTarget{stored})
		if err != nil {
			return err
		}
		files, err := h.module.ReadSelectionFiles(ctx, sc, selection)
		if err != nil {
			return err
		}
		if len(files) != 2 || files[0].Path != "research/SKILL.md" || string(files[0].Bytes) != harmless || files[1].Path != "research/support.md" || string(files[1].Bytes) != "reviewed support" {
			t.Fatalf("native delivery files: %+v", files)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var otherTenant model.TenantID
	if err := h.store.System(ctx, func(sys store.SystemScope) error {
		org, err := sys.CreateOrg(ctx, model.Org{Name: "Other delivery tenant", Slug: "other-delivery-tenant", Status: model.StatusActive})
		otherTenant = org.TenantID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.View(ctx, otherTenant, func(sc store.Scope) error { _, err := h.module.ReadSelectionFiles(ctx, sc, selection); return err }); err == nil {
		t.Fatal("selection crossed tenant boundary")
	}
	artifact := filepath.Join(h.artifacts, h.tenant.String(), pack.Revision.ID, "research", "support.md")
	if err := os.Chmod(artifact, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact, []byte("tampered support"), 0600); err != nil {
		t.Fatal(err)
	}
	err = h.store.View(ctx, h.tenant, func(sc store.Scope) error {
		_, err := h.module.ReadSelectionFiles(ctx, sc, selection)
		return err
	})
	if err == nil {
		t.Fatal("tampered assigned bytes were delivered")
	}
}
