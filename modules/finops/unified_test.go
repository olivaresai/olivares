// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// TestUnifiedCrossSurface proves the unified view joins admin-API surfaces and
// cloud-connector surfaces with correct provenance per surface.
func TestUnifiedCrossSurface(t *testing.T) {
	m, st, tenant, _ := newFin(t)

	// Admin API surface: direct (estimated usage_report + billed cost_report).
	directEst := mkCost("anthropic", "claude-opus-4-8", "", 100, 50, 500, baseTime)
	directEst.Gateway = sdkmodel.GatewayDirect
	directEst.Provenance = sdkmodel.ProvenanceEstimated
	m.ingest(t, tenant, directEst)

	directBilled := mkCost("anthropic", "claude-opus-4-8", "", 100, 50, 480, baseTime.Add(time.Minute))
	directBilled.Gateway = sdkmodel.GatewayDirect
	directBilled.Provenance = sdkmodel.ProvenanceBilled
	m.ingest(t, tenant, directBilled)

	// Cloud-connector surface: bedrock-mantle (cloud connector derived).
	bedrockSample := mkCost("anthropic", "claude-opus-4-8", "", 50, 25, 250, baseTime.Add(2*time.Minute))
	bedrockSample.Gateway = sdkmodel.GatewayBedrockMantle
	bedrockSample.Provenance = sdkmodel.ProvenanceEstimated
	m.ingest(t, tenant, bedrockSample)

	// Cloud-connector surface: vertex.
	vertexSample := mkCost("google", "claude-sonnet-4-6", "", 30, 15, 150, baseTime.Add(3*time.Minute))
	vertexSample.Gateway = sdkmodel.GatewayVertex
	vertexSample.Provenance = sdkmodel.ProvenanceEstimated
	m.ingest(t, tenant, vertexSample)

	var out unifiedResponse
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		var e error
		out, e = unifiedCrossSurface(context.Background(), sc, time.Time{}, false, time.Time{}, false)
		return e
	}); err != nil {
		t.Fatal(err)
	}

	// Total should include all four samples.
	wantTotal := int64(500 + 480 + 250 + 150)
	if out.TotalMicroUSD != wantTotal {
		t.Errorf("total = %d, want %d", out.TotalMicroUSD, wantTotal)
	}

	// Check provenance per surface.
	prov := map[string]CostProvenance{}
	for _, b := range out.Surfaces {
		prov[b.Surface+"_"+string(b.Provenance)] = b.Provenance
	}

	// Direct surface should have both admin-api-estimated and admin-api-billed.
	if _, ok := prov["direct_admin-api-estimated"]; !ok {
		t.Error("direct surface missing admin-api-estimated provenance")
	}
	if _, ok := prov["direct_admin-api-billed"]; !ok {
		t.Error("direct surface missing admin-api-billed provenance")
	}

	// Bedrock surface should be cloud-connector-derived.
	if _, ok := prov["bedrock-mantle_cloud-connector-derived"]; !ok {
		t.Error("bedrock-mantle surface missing cloud-connector-derived provenance")
	}

	// Vertex surface should be cloud-connector-derived.
	if _, ok := prov["vertex_cloud-connector-derived"]; !ok {
		t.Error("vertex surface missing cloud-connector-derived provenance")
	}
}

// TestClassifyProvenance pins the provenance classification rules.
func TestClassifyProvenance(t *testing.T) {
	cases := []struct {
		gateway, prov string
		want          CostProvenance
	}{
		{"direct", "billed", ProvenanceAdminBilled},
		{"direct", "estimated", ProvenanceAdminEstimated},
		{"direct", "", ProvenanceAdminEstimated},
		{"claude-platform-aws", "billed", ProvenanceAdminBilled},
		{"claude-platform-aws", "estimated", ProvenanceAdminEstimated},
		{"bedrock-mantle", "estimated", ProvenanceCloudConnector},
		{"bedrock-legacy", "estimated", ProvenanceCloudConnector},
		{"vertex", "estimated", ProvenanceCloudConnector},
		{"foundry", "estimated", ProvenanceCloudConnector},
		{"", "estimated", ProvenanceAdminEstimated},
	}
	for _, c := range cases {
		got := classifyProvenance(c.gateway, c.prov)
		if got != c.want {
			t.Errorf("classifyProvenance(%q, %q) = %q, want %q", c.gateway, c.prov, got, c.want)
		}
	}
}
