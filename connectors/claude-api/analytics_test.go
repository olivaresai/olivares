// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import (
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk/model"
)

// TestBillingSourceOf_SplitsSubscriptionFromAPI verifies the classifier mirrors the
// connector's own emission semantics: cost_report and usage_report rows are API; the
// Claude Code subscription feed row is subscription; an undecidable sample is unknown.
func TestBillingSourceOf_SplitsSubscriptionFromAPI(t *testing.T) {
	at := time.Unix(1_700_000_000, 0).UTC()

	// The subscription Claude Code feed sample (as gatherClaudeCode emits it): estimated,
	// direct, actor=email, NO api dimensions.
	sub := claudeCodeSample("dev@corp.example", claudeCodeModelSpan{
		Model:         "claude-opus-4-8",
		Tokens:        claudeCodeTokens{Input: 100, Output: 50},
		EstimatedCost: claudeCodeCostSpan{Amount: 186, Currency: "USD"},
	}, at)
	if got := BillingSourceOf(sub); got != BillingSubscription {
		t.Errorf("claude_code subscription sample classified %q, want subscription", got)
	}

	// A usage_report sample (estimated, with api_key + service_tier) is API.
	usage := model.CostSample{
		ProviderRef: modelprovider.ProviderAnthropic, ModelRef: "claude-opus-4-8",
		APIKeyRef: "apikey_123", ServiceTier: "standard", Gateway: model.GatewayDirect,
		Provenance: model.ProvenanceEstimated, OccurredAt: at,
	}
	if got := BillingSourceOf(usage); got != BillingAPI {
		t.Errorf("usage_report sample classified %q, want api", got)
	}

	// A billed cost_report sample is API.
	billed := model.CostSample{
		ProviderRef: modelprovider.ProviderAnthropic, ModelRef: "claude-opus-4-8",
		WorkspaceRef: "wrkspc_1", Provenance: model.ProvenanceBilled, Gateway: model.GatewayDirect, OccurredAt: at,
	}
	if got := BillingSourceOf(billed); got != BillingAPI {
		t.Errorf("cost_report billed sample classified %q, want api", got)
	}

	// An empty/undecidable sample is honestly unknown.
	if got := BillingSourceOf(model.CostSample{ModelRef: "x"}); got != BillingUnknown {
		t.Errorf("undecidable sample classified %q, want unknown", got)
	}
}

// TestEnterpriseAnalyticsFamilies_NoConflation verifies the four admin-plane families
// are modeled distinctly, with the right billing-source attribution and honest ownership
// of the depth not implemented here.
func TestEnterpriseAnalyticsFamilies_NoConflation(t *testing.T) {
	fams := EnterpriseAnalyticsFamilies()
	if len(fams) != 4 {
		t.Fatalf("want 4 admin-plane families (Usage&Cost / Claude Code Analytics / Enterprise Analytics / Compliance), got %d", len(fams))
	}
	byName := map[string]AdminAPIFamily{}
	for _, f := range fams {
		byName[f.Name] = f
	}
	// Subscription is attributed by Claude Code Analytics + Enterprise Analytics, never
	// by Usage & Cost.
	if byName["Usage & Cost"].Attributes != BillingAPI {
		t.Errorf("Usage & Cost must attribute API spend, got %q", byName["Usage & Cost"].Attributes)
	}
	if byName["Claude Code Analytics"].Attributes != BillingSubscription {
		t.Errorf("Claude Code Analytics must attribute subscription spend, got %q", byName["Claude Code Analytics"].Attributes)
	}
	if byName["Enterprise Analytics"].Attributes != BillingSubscription {
		t.Errorf("Enterprise Analytics must attribute subscription spend, got %q", byName["Enterprise Analytics"].Attributes)
	}
	// Compliance is governance-only (no billing source).
	if byName["Compliance"].Attributes != "" {
		t.Errorf("Compliance is governance evidence, not a cost family: %q", byName["Compliance"].Attributes)
	}
	// Enterprise Analytics is now ingested (enterprise.go): implemented, with its
	// own endpoint and a DISTINCT credential (read:analytics), never conflated with the
	// Admin key.
	ea := byName["Enterprise Analytics"]
	if !ea.Implemented {
		t.Errorf("Enterprise Analytics is ingested by and must be marked implemented: %+v", ea)
	}
	if ea.Endpoint == "" {
		t.Error("Enterprise Analytics must name its endpoint now that it is ingested")
	}
	if !strings.Contains(ea.KeyType, "read:analytics") {
		t.Errorf("Enterprise Analytics must use the DISTINCT read:analytics key, got %q", ea.KeyType)
	}
	if ea.AsOf != "2026-07-04" {
		t.Errorf("Enterprise Analytics AsOf = %q, want 2026-07-04", ea.AsOf)
	}
	if strings.Contains(ea.Notes, "to-confirm") || !strings.Contains(ea.Notes, "verified per-day summaries") ||
		!strings.Contains(ea.Notes, "per-product splits") {
		t.Errorf("Enterprise Analytics notes must describe the verified per-day/per-product schema, got %q", ea.Notes)
	}
	// All four families this connector / its sibling ingest are marked implemented.
	if !byName["Usage & Cost"].Implemented || !byName["Claude Code Analytics"].Implemented {
		t.Error("Usage & Cost and Claude Code Analytics are ingested here and must be marked implemented")
	}
	// Returned slice is a copy.
	fams[0].Name = "tampered"
	if EnterpriseAnalyticsFamilies()[0].Name == "tampered" {
		t.Fatal("EnterpriseAnalyticsFamilies must return a copy")
	}
}
