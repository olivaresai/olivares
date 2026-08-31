// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import (
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

// TestAllSurfacesModeled proves the matrix covers exactly the six seeded gateways,
// in a stable order, so the compliance/deploy matrix and the contract enumerate
// every surface (ANT2-01: four AWS/Azure surfaces + direct + Vertex).
func TestAllSurfacesModeled(t *testing.T) {
	want := []model.Gateway{
		model.GatewayDirect, model.GatewayBedrockMantle, model.GatewayBedrockLegacy,
		model.GatewayVertex, model.GatewayFoundry, model.GatewayClaudePlatformAWS,
	}
	got := AllSurfaces()
	if len(got) != len(want) {
		t.Fatalf("AllSurfaces() = %d surfaces, want %d", len(got), len(want))
	}
	seen := map[model.Gateway]bool{}
	for _, s := range got {
		seen[s.Gateway] = true
		if s.AsOf == "" {
			t.Errorf("surface %q has no AsOf stamp", s.Gateway)
		}
		if !s.APIs.Messages {
			t.Errorf("surface %q must support the Messages (inference) API", s.Gateway)
		}
	}
	for _, g := range want {
		if !seen[g] {
			t.Errorf("surface %q not modeled", g)
		}
	}
}

// TestFoundryAPIMatrix pins the ANT2-01 fact that Microsoft Foundry exposes NO
// Admin/Compliance/Models/Batches API — only inference (and the MCP connector). A
// connector polling the Admin API there must degrade honestly, never report empty as
// "no findings".
func TestFoundryAPIMatrix(t *testing.T) {
	s, ok := SurfaceFor(model.GatewayFoundry)
	if !ok {
		t.Fatal("Foundry surface not modeled")
	}
	for _, api := range []string{"admin", "compliance", "models", "batches"} {
		if s.Supports(api) {
			t.Errorf("Foundry must NOT support the %q API (ANT2-01)", api)
		}
	}
	if !s.Supports("messages") {
		t.Error("Foundry must support the Messages API")
	}
	if s.WorkspaceHeader != "" {
		t.Errorf("Foundry scopes by deployment, not anthropic-workspace-id; got header %q", s.WorkspaceHeader)
	}
}

// TestClaudePlatformAWSDistinctFromBedrock pins the ANT2-01 facts that distinguish
// the four AWS/Azure surfaces: SigV4 service names differ, only Claude-Platform-on-AWS
// carries the anthropic-workspace-id header and the full Admin API, and its HIPAA
// posture is explicitly "no".
func TestClaudePlatformAWSDistinctFromBedrock(t *testing.T) {
	cpaws, _ := SurfaceFor(model.GatewayClaudePlatformAWS)
	mantle, _ := SurfaceFor(model.GatewayBedrockMantle)
	legacy, _ := SurfaceFor(model.GatewayBedrockLegacy)

	if cpaws.SigV4Service != "aws-external-anthropic" {
		t.Errorf("Claude Platform on AWS SigV4 service = %q, want aws-external-anthropic", cpaws.SigV4Service)
	}
	if mantle.SigV4Service != "bedrock-mantle" {
		t.Errorf("Bedrock Mantle SigV4 service = %q, want bedrock-mantle", mantle.SigV4Service)
	}
	if legacy.SigV4Service != "bedrock" {
		t.Errorf("Bedrock legacy SigV4 service = %q, want bedrock", legacy.SigV4Service)
	}
	if cpaws.WorkspaceHeader != "anthropic-workspace-id" {
		t.Errorf("Claude Platform on AWS must carry anthropic-workspace-id; got %q", cpaws.WorkspaceHeader)
	}
	// Anthropic-operated → full Admin API; partner-operated Bedrock → none.
	if !cpaws.Supports("admin") {
		t.Error("Claude Platform on AWS is Anthropic-operated and must support the Admin API")
	}
	if mantle.Supports("admin") {
		t.Error("Bedrock Mantle is AWS-governed and must NOT expose the Anthropic Admin API")
	}
	// HIPAA: explicit "no" on Claude-Platform-on-AWS (confirmed); "yes" on Bedrock.
	if cpaws.HIPAA != "no" || cpaws.HIPAAStatus != statusConfirmed {
		t.Errorf("Claude Platform on AWS HIPAA = %q/%q, want no/confirmed (ANT2-01)", cpaws.HIPAA, cpaws.HIPAAStatus)
	}
	if mantle.HIPAA != "yes" {
		t.Errorf("Bedrock Mantle HIPAA = %q, want yes", mantle.HIPAA)
	}
	if !legacy.Deprecated {
		t.Error("Bedrock legacy must be marked Deprecated (observe-only, not a build target)")
	}
}

// TestSurfaceModelID checks per-surface model-id formation and that the bedrock-legacy
// Global-CRIS opus id is to-confirm (format correct, concrete id NOT fabricated —
// §5).
func TestSurfaceModelID(t *testing.T) {
	if id, st := SurfaceModelID(model.GatewayBedrockMantle, "claude-opus-4-8"); id != "anthropic.claude-opus-4-8" || st != statusConfirmed {
		t.Errorf("Mantle model id = %q/%q, want anthropic.claude-opus-4-8/confirmed", id, st)
	}
	// Re-prefixing must not double-prefix.
	if id, _ := SurfaceModelID(model.GatewayBedrockMantle, "anthropic.claude-opus-4-8"); id != "anthropic.claude-opus-4-8" {
		t.Errorf("Mantle already-prefixed id double-prefixed: %q", id)
	}
	if id, st := SurfaceModelID(model.GatewayBedrockLegacy, "claude-opus-4-8"); id != "global.anthropic.claude-opus-4-8" || st != statusToConfirm {
		t.Errorf("legacy Global-CRIS id = %q/%q, want global.anthropic.claude-opus-4-8/to-confirm", id, st)
	}
	if id, st := SurfaceModelID(model.GatewayDirect, "claude-opus-4-8"); id != "claude-opus-4-8" || st != statusConfirmed {
		t.Errorf("direct id = %q/%q, want bare/confirmed", id, st)
	}
}
