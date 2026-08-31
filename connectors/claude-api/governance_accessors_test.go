// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/sdk"
)

// the identity console's posture tab reads the External Keys / CMEK inventory and
// the workspace governance object. Both were already GATHERED (fetchExternalKeys /
// fetchWorkspaces feed the catalog) but had no exported accessor, so no module could
// serve them and the console got a 404. These are the read-only accessors — the same
// shape as the RateLimits accessor next door.

func TestExternalKeysAccessorReturnsTheCollectedInventory(t *testing.T) {
	s, _ := newLive(t)
	keys, err := s.ExternalKeys(context.Background())
	if err != nil {
		t.Fatalf("ExternalKeys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("keys = %d, want 1", len(keys))
	}
	k := keys[0]
	if k.ID != "ekey_prod_kms" || k.Provider != "aws_kms" || k.Name != "prod-cmek" {
		t.Errorf("key = %+v", k)
	}
	if k.State != "active" || !k.InUse {
		t.Errorf("state = %q in_use = %v, want active/true", k.State, k.InUse)
	}
	if k.LastValidatedAt.IsZero() || k.CreatedAt.IsZero() {
		t.Errorf("timestamps not parsed: validated=%v created=%v", k.LastValidatedAt, k.CreatedAt)
	}
}

func TestWorkspacesAccessorReturnsTheGovernanceObject(t *testing.T) {
	s, _ := newLive(t)
	ws, err := s.Workspaces(context.Background())
	if err != nil {
		t.Fatalf("Workspaces: %v", err)
	}
	if len(ws) != 2 {
		t.Fatalf("workspaces = %d, want 2 (the archived one is carried, and the CALLER decides)", len(ws))
	}
	w := ws[0]
	if w.ID != "wrkspc_01" || w.Name != "Production" {
		t.Fatalf("workspaces[0] = %+v", w)
	}
	if w.Geo != "us" || w.ExternalKeyID != "ekey_prod_kms" || w.CompartmentID != "cmpt_prod" {
		t.Errorf("governance fields = geo:%q ekey:%q compartment:%q", w.Geo, w.ExternalKeyID, w.CompartmentID)
	}
	if w.Tags["env"] != "prod" || w.Tags["cost_center"] != "platform" {
		t.Errorf("tags = %v", w.Tags)
	}
	if w.Residency == nil {
		t.Fatal("residency is nil though the fixture reports one")
	}
	if len(w.Residency.AllowedInferenceGeos) != 1 || w.Residency.AllowedInferenceGeos[0] != "us" {
		t.Errorf("allowed_inference_geos = %v", w.Residency.AllowedInferenceGeos)
	}
	if w.Residency.DefaultInferenceGeo != "us" {
		t.Errorf("default_inference_geo = %q", w.Residency.DefaultInferenceGeo)
	}
	if !ws[1].Archived {
		t.Error("wrkspc_02 has an archived_at and must be marked archived")
	}
}

// NO CREDENTIAL IS AN EMPTY INVENTORY, NOT AN ERROR — so a caller can tell "not wired"
// (handled by not wiring the provider at all) from a transient fault. This mirrors the
// RateLimits accessor's documented contract exactly.
func TestGovernanceAccessorsWithoutAnAdminCredentialAreEmptyNotAnError(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	keys, err := s.ExternalKeys(context.Background())
	if err != nil || len(keys) != 0 {
		t.Errorf("ExternalKeys = %v, %v; want empty and no error", keys, err)
	}
	ws, err := s.Workspaces(context.Background())
	if err != nil || len(ws) != 0 {
		t.Errorf("Workspaces = %v, %v; want empty and no error", ws, err)
	}
}
