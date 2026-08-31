// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// This file makes the SUBSCRIPTION-vs-API billing distinction a first-class, queryable
// attribution for FinOps (module XI), and models the FOUR distinct Anthropic admin-plane
// data families the control plane reads so a consumer never conflates them.
//
// Why it matters (G1/G2): the interactive developer mostly runs Claude Code
// on a Pro/Max SUBSCRIPTION, which a third party may OBSERVE (OTEL/Analytics, NOT plan-
// gated) but must NEVER proxy or meter as if it were an API key. Programmatic/3rd-party
// use runs on an API key and IS metered by the Usage & Cost API. The two cost in
// different places: subscription Claude Code spend appears ONLY in the Claude Code /
// Enterprise Analytics surfaces (as an ESTIMATE, never a per-key bill); API spend is in
// usage_report/cost_report. FinOps must split them to attribute correctly — that is what
// BillingSourceOf does — and must know which family carries which (EnterpriseAnalyticsFamilies).
//
// Honesty: this connector already ingests Usage & Cost and Claude Code Analytics. The
// Enterprise Analytics family (aggregate Claude.ai Enterprise/Teams seat analytics) is
// MODELED here for the attribution map; its deep, multi-endpoint ingest (members/SCIM/
// audit-CSV/key-rotation) is the admin-plane session — not fabricated here.
package claudeapi

import (
	"sort"

	"github.com/olivaresai/olivares/sdk/model"
)

// analyticsAsOf stamps the admin-family model with the date it was recorded.
const analyticsAsOf = "2026-07-04"

// BillingSource is how a unit of Claude spend is paid for, the dimension FinOps must
// split on. It is provider-neutral vocabulary, not a sealed enum on the wire.
type BillingSource string

const (
	// BillingSubscription is Pro/Max/Team/Enterprise SEAT spend (Claude Code under a
	// subscription). It is OBSERVED and ESTIMATED — never metered per API key, never
	// proxied. Its only cost surface is Claude Code / Enterprise Analytics.
	BillingSubscription BillingSource = "subscription"
	// BillingAPI is pay-as-you-go API-key spend, metered by the Usage & Cost API
	// (per workspace / api_key / service_tier).
	BillingAPI BillingSource = "api"
	// BillingUnknown is the honest classification when a sample carries no dimension
	// that decides it (never a guessed split).
	BillingUnknown BillingSource = "unknown"
)

// hasAPIDimensions reports whether a CostSample carries any dimension that only the
// API Usage/Cost reports populate (a per-key, per-workspace, per-tier or residency
// dimension). The subscription Claude Code feed carries NONE of these.
func hasAPIDimensions(s model.CostSample) bool {
	return s.APIKeyRef != "" || s.WorkspaceRef != "" || s.ServiceTier != "" ||
		s.ContextWindow != "" || s.InferenceGeo != ""
}

// BillingSourceOf classifies a CostSample PRODUCED BY THIS CONNECTOR into its billing
// source, so FinOps can split subscription from API spend. The rules mirror the
// connector's own emission semantics (so the classification is reliable for its output,
// not a fragile guess):
//
//   - a BILLED sample is the cost_report (API billing) → api;
//   - any sample carrying an API dimension (api_key/workspace/service_tier/context/geo)
//     is the Usage or Cost report → api;
//   - an ESTIMATED, direct-surface sample with an Actor and NO API dimension is the
//     Claude Code Analytics subscription feed (gatherClaudeCode emits these for
//     customer_type="subscription" only) → subscription;
//   - anything else is honestly unknown.
func BillingSourceOf(s model.CostSample) BillingSource {
	if s.Provenance == model.ProvenanceBilled {
		return BillingAPI
	}
	if hasAPIDimensions(s) {
		return BillingAPI
	}
	if s.Actor != "" && s.Gateway == model.GatewayDirect {
		return BillingSubscription
	}
	return BillingUnknown
}

// AdminAPIFamily is one of the distinct Anthropic admin-plane data families. Conflating
// them is the mistake (A2/A3/A4/A5) flagged: each uses a different key/question
// and attributes a different billing source. This descriptive record keeps them apart.
type AdminAPIFamily struct {
	// Name is the family label.
	Name string
	// Endpoint is a representative endpoint ("" when the family is modeled but its deep
	// ingest is owned elsewhere — honest, never a fabricated path).
	Endpoint string
	// KeyType is the credential the family requires (Admin API key / Compliance key).
	KeyType string
	// Attributes is the billing source this family attributes ("" for a non-cost,
	// governance-only family like Compliance).
	Attributes BillingSource
	// Implemented reports whether THIS connector (or its sibling) ingests the family today.
	Implemented bool
	// Owner names the session that owns the deep ingest when it is not done here.
	Owner string
	// AsOf stamps when the record was recorded.
	AsOf string
	// Notes carries the honest caveat for the family.
	Notes string
}

// adminAPIFamilies is the four-family model (A2/A3/A4/A5). Usage & Cost and
// Claude Code Analytics are ingested by this connector; Enterprise Analytics is modeled
// for the attribution map (deep ingest =); Compliance is the sibling connector
// (claude-compliance) and is governance evidence, not cost.
var adminAPIFamilies = []AdminAPIFamily{
	{
		Name:        "Usage & Cost",
		Endpoint:    usageReportPath + " + " + costReportPath,
		KeyType:     "Admin API key",
		Attributes:  BillingAPI,
		Implemented: true,
		AsOf:        analyticsAsOf,
		Notes:       "API-key metered spend: usage_report (estimated, full group_by incl. service_tier/inference_geo) + cost_report (billed). Never carries subscription seat spend.",
	},
	{
		Name:        "Claude Code Analytics",
		Endpoint:    claudeCodePath,
		KeyType:     "Admin API key",
		Attributes:  BillingSubscription,
		Implemented: true,
		AsOf:        analyticsAsOf,
		Notes:       "Per-developer Claude Code feed; customer_type ∈ {subscription, api}. This connector emits cost ONLY for subscription rows (API rows are already in usage_report) — so it is the sole cost surface for subscription Claude Code spend (estimated, never billed-per-key). API rows feed the shadow-auth detector instead. BOUNDARY (verified 2026-06-10): the feed only tracks usage on the Claude API — Claude Platform on AWS, Microsoft Foundry, Amazon Bedrock and Vertex AI are NOT included, so it can never prove a 3P-provider fleet clean.",
	},
	{
		Name:        "Enterprise Analytics",
		Endpoint:    analyticsSummariesPath,
		KeyType:     "Enterprise Analytics key (x-api-key, read:analytics) — DISTINCT from the Admin key",
		Attributes:  BillingSubscription,
		Implemented: true,
		AsOf:        analyticsAsOf,
		Notes:       "Aggregate Claude.ai Enterprise/Teams seat analytics — the org-level subscription attribution surface. Observed/aggregated, never proxied. Ingests verified per-day summaries (DAU/WAU/MAU, adoption rates, seats, Cowork, and optional Chat/Claude Code per-product splits) as ENGAGEMENT evidence (enterprise.go); it emits NO cost (the user_cost_report would double-count the Usage & Cost API). Uses its OWN read:analytics credential, deny-closed.",
	},
	{
		Name:        "Compliance",
		Endpoint:    "/v1/compliance/activities",
		KeyType:     "Admin API key (read:compliance_activities) — Enterprise-gated",
		Attributes:  "", // governance evidence, not a cost family
		Implemented: true,
		Owner:       "connectors/claude-compliance (sibling)",
		AsOf:        analyticsAsOf,
		Notes:       "Activity Feed governance evidence (NOT cost). Ingested by the claude-compliance connector; content retrieve/DELETE is. Listed here so it is never mistaken for a cost/usage family.",
	},
}

// EnterpriseAnalyticsFamilies returns the four admin-plane data families in stable
// order (a copy, so a caller cannot mutate package state). It is the authoritative
// attribution map: which family a consumer must read for subscription vs API spend, and
// which is governance-only. FinOps (module XI) uses it to avoid the conflation
// flagged; the deep ingest of the not-yet-implemented family is elsewhere.
func EnterpriseAnalyticsFamilies() []AdminAPIFamily {
	out := append([]AdminAPIFamily(nil), adminAPIFamilies...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
