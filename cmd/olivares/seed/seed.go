// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package seed is the reusable demo/E2E dataset: a credible AI-agent estate
// expressed as normalized SourceConnector observations (edges, costs, findings)
// plus the handful of core entities the access-graph bridge resolves against.
//
// It is the single source of truth for "what a populated Olivares estate looks
// like", consumed by (a) the in-process E2E suite (cmd/olivares, which feeds
// it through the REAL event bus via a registered SourceConnector and asserts the
// nuclear flow end-to-end) and (b) the `serve --seed-demo` affordance that lets
// an operator see the R/RW graph and the executive dashboard the minute they
// install the binary ("value at minute 1").
//
// Minimal-data by construction (docs/SECURITY-HARDENING.md): every observation carries
// identifiers and an access classification — never a secret, a SQL body, a
// payload or real PII. Resource and identity references are synthetic.
//
// Connector data is expressed in the Apache SDK vocabulary (sdk/model), while
// request-driven demo state uses plain string/number specs. The package therefore
// stays free of engine types; cmd/olivares materializes the specs and the engine's
// modules consume the connector stream exactly as they would in production.
package seed

import (
	"context"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Stable references used across the estate. Tests assert against these exact
// strings, and `serve --seed-demo` surfaces them in the UI, so they are part of
// the seed's contract — do not rename without updating both consumers.
const (
	// SourceName is the connector descriptor name the runtime stamps onto every
	// seeded event's Source field.
	SourceName = "olivares.seed-demo"

	// Agents (cooperative Claude Code instances). Agent-origin edges attribute to
	// these by external id, so the harness MUST create them (POST /v1/agents)
	// before the source emits, or the bridge honestly falls back to identity.
	AgentCoder    = "agent-claude-coder-7"  // bound to IdentityAppRole in governance
	AgentReviewer = "agent-claude-review-3" // a second cooperative agent
	AgentIndexer  = "agent-claude-index-9"  // deliberately UNBOUND (no identity) → drives reconciliation_pending

	// Named workspace and agent group surfaced by the scoping console. Slugs and
	// membership refs are stable because the materializer uses them to resolve the
	// reviewer into the Billing workspace and PR-reviewers group.
	WorkspaceBillingName    = "Billing"
	WorkspaceBillingSlug    = "billing"
	AgentGroupReviewersName = "PR reviewers"
	AgentGroupReviewersSlug = "pr-reviewers"

	// Knowledge catalog names and refs. The document refs are synthetic source
	// identifiers; the titles and prompt/memory names are visible in the console.
	KnowledgeBaseName              = "Demo Estate Handbook"
	KnowledgeBillingDocumentTitle  = "Billing service runbook"
	KnowledgeBillingDocumentRef    = "billing-service-runbook"
	KnowledgeIncidentDocumentTitle = "Prompt-injection incident response"
	KnowledgeIncidentDocumentRef   = "prompt-injection-incident-response"
	KnowledgeReviewerPromptName    = "Reviewer system prompt"
	KnowledgeReviewerMemoryKey     = "Reviewer style guidance"

	// EvalSuiteName is the stable suite name the demo scorecard resolves through.
	EvalSuiteName = "governance-regression"

	// Sessions (discovered cooperatively via OTEL). Self-materialize from
	// session-origin edges; no pre-seed needed.
	SessionLive  = "sess-coder-7a3f" // a live session with an in-flight tool call
	SessionEvade = "sess-coder-9c21" // a session flagged by anti-evasion correlation

	// Identities (NHI / DB roles). Self-materialize from identity-origin edges.
	IdentityAppRole = "app_role" // the role the coder agent runs as (firmly attributed)
	IdentityPool    = "svc_pool" // a SHARED service account → approximate attribution
	IdentityEtl     = "etl_role" // a role with a declared grant it never exercises

	// MCP servers / capabilities discovered from the cooperative stream.
	MCPGitHub     = "github"
	MCPFilesystem = "filesystem"
	ToolCreateIss = "create_issue"

	// Resources (postgres tables / object stores) touched by the estate.
	ResCustomers = "appdb.public.customers" // R, attributed     (observed, with a matching grant → reconciled)
	ResOrders    = "appdb.public.orders"    // RW, attributed
	ResExports   = "data-lake/exports"      // R, attributed (S3/CloudTrail)
	ResLogs      = "appdb.public.logs"      // W, approximate (shared pool)
	ResSecrets   = "appdb.public.secrets"   // R, attributed — UNEXPECTED (no grant)
	ResBilling   = "appdb.public.billing"   // R by an unbound agent — RECONCILIATION PENDING
	ResArchive   = "appdb.public.archive"   // a granted-but-never-used write — UNUSED GRANT
)

// AgentSpec is one core Agent the harness creates through the real POST /v1/agents
// channel before the source emits, so agent-origin edges attribute firmly.
type AgentSpec struct {
	ExternalID    string
	Name          string
	Kind          string
	WorkspaceSlug string
}

// Agents returns the cooperative agents the estate references. The harness
// creates them via the API; their presence is what lets the bridge attribute
// agent-origin edges (and bind one to an NHI for the firm-attribution path).
func Agents() []AgentSpec {
	return []AgentSpec{
		{ExternalID: AgentCoder, Name: "Claude Code — billing service", Kind: "claude_code"},
		{ExternalID: AgentReviewer, Name: "Claude Code — PR reviewer", Kind: "claude_code", WorkspaceSlug: WorkspaceBillingSlug},
		{ExternalID: AgentIndexer, Name: "Claude Code — repo indexer", Kind: "claude_code"},
	}
}

// WorkspaceSpec is one named workspace in the demo estate. It deliberately uses
// only plain seed vocabulary; cmd/olivares maps it to the engine's core model.
type WorkspaceSpec struct {
	Name string
	Slug string
}

// Workspaces returns the named workspaces beyond the automatically provisioned
// default workspace.
func Workspaces() []WorkspaceSpec {
	return []WorkspaceSpec{{Name: WorkspaceBillingName, Slug: WorkspaceBillingSlug}}
}

// AgentGroupSpec is one named agent collection and its single synthetic member.
// WorkspaceSlug and MemberAgentRef are resolved by the composition-root
// materializer after the corresponding core rows exist.
type AgentGroupSpec struct {
	Name           string
	Slug           string
	Description    string
	WorkspaceSlug  string
	MemberAgentRef string
}

// AgentGroups returns the demo estate's scoped reviewer group.
func AgentGroups() []AgentGroupSpec {
	return []AgentGroupSpec{{
		Name: AgentGroupReviewersName, Slug: AgentGroupReviewersSlug,
		Description:   "Reviews synthetic pull requests for governance regressions.",
		WorkspaceSlug: WorkspaceBillingSlug, MemberAgentRef: AgentReviewer,
	}}
}

// KnowledgeDocumentSpec is one synthetic document descriptor. Content is kept
// to one sentence and is used only to derive the stored content hash.
type KnowledgeDocumentSpec struct {
	SourceDocID string
	Title       string
	Content     string
}

// KnowledgeSpec is the neutral, minimal-data knowledge catalog. The composition
// root maps it to the module's extension rows without importing engine types here.
type KnowledgeSpec struct {
	BaseName       string
	Classification string
	Documents      []KnowledgeDocumentSpec
	PromptName     string
	PromptContent  string
	MemoryAgentRef string
	MemoryKey      string
	MemoryContent  string
	// MemoryClassification is the memory entry's own classification, decoupled from
	// the KB's: the reviewer guidance is non-sensitive, so it stays public and the
	// clearance-gated memory list shows it to a public-clearance reader.
	MemoryClassification string
}

// Knowledge returns the governed knowledge data shown in demo mode.
func Knowledge() KnowledgeSpec {
	return KnowledgeSpec{
		BaseName: KnowledgeBaseName, Classification: "internal",
		Documents: []KnowledgeDocumentSpec{
			{
				SourceDocID: KnowledgeBillingDocumentRef, Title: KnowledgeBillingDocumentTitle,
				Content: "Review billing alerts before restarting the synthetic service.",
			},
			{
				SourceDocID: KnowledgeIncidentDocumentRef, Title: KnowledgeIncidentDocumentTitle,
				Content: "Isolate affected sessions after a synthetic prompt-injection alert.",
			},
		},
		PromptName:     KnowledgeReviewerPromptName,
		PromptContent:  "Review changes for governance risks and return one concise recommendation.",
		MemoryAgentRef: AgentReviewer, MemoryKey: KnowledgeReviewerMemoryKey,
		MemoryContent:        "Prefer concise reviews that explain the highest-priority governance risk.",
		MemoryClassification: "public",
	}
}

// EvalSuiteSpec is the stable definition behind the demo scorecard.
type EvalSuiteSpec struct {
	Name                string
	Description         string
	SubjectKind         string
	Scorer              string
	Criterion           string
	PassThreshold       float64
	RegressionThreshold float64
	SuiteVersion        int64
}

// EvalRunSpec is one already-terminal aggregate. AgeMinutes lets demo.go stamp
// both run timestamps relative to its boot clock while this package stays free
// of engine time types.
type EvalRunSpec struct {
	SubjectRef string
	Status     string
	Total      int64
	Passed     int64
	Failed     int64
	Errors     int64
	Skipped    int64
	Score      float64
	PassRate   float64
	AgeMinutes int64
}

// Evals returns one suite and two completed runs with a non-trivial score trend.
func Evals() (EvalSuiteSpec, []EvalRunSpec) {
	return EvalSuiteSpec{
			Name: EvalSuiteName, Description: "Checks the synthetic reviewer against governance expectations.",
			SubjectKind: "agent", Scorer: "exact",
			Criterion:     "The reviewer should identify the highest-priority governance risk.",
			PassThreshold: 0.8, RegressionThreshold: 0.1, SuiteVersion: 1,
		}, []EvalRunSpec{
			{SubjectRef: AgentReviewer, Status: "completed", Total: 10, Passed: 8, Failed: 2, Score: 0.76, PassRate: 0.8, AgeMinutes: 2 * 24 * 60},
			{SubjectRef: AgentReviewer, Status: "completed", Total: 10, Passed: 9, Failed: 1, Score: 0.94, PassRate: 0.9, AgeMinutes: 15},
		}
}

// edge is a terser builder for an EdgeObservation; ObservedAt is stamped by the
// caller so the whole estate shares one deterministic clock.
func edge(originKind, originRef, resKind, resRef string, mode model.AccessMode, src model.SignalSource, conf model.Confidence, tool string, at time.Time) model.EdgeObservation {
	return model.EdgeObservation{
		OriginKind: originKind, OriginRef: originRef,
		ResourceKind: resKind, ResourceRef: resRef,
		Mode: mode, Source: src, Confidence: conf, ToolRef: tool, ObservedAt: at,
	}
}

// Observations returns the full estate as a SourceConnector would emit it,
// stamped relative to now so the live session reads as active and the FinOps
// trend spans several days. The order is irrelevant (each is upserted on its
// natural key), but observed edges precede their matching policy grants for
// readability.
//
// The access-graph shape this produces (verified against modules/access-map):
//   - R (blue) + RW (copper) attributed edges, plus one approximate (shared pool)
//   - a RECONCILED pair (app_role observed-read customers + its policy grant) →
//     absent from drift, proving reconciliation is real, not "everything is drift"
//   - a FIRM unexpected access (coder → secrets, no grant)
//   - a RECONCILIATION-PENDING access (unbound indexer → billing, a grant exists
//     for the resource but the agent has no resolved identity)
//   - an UNUSED GRANT (etl_role granted write archive, never observed)
func Observations(now time.Time) []model.Observation {
	const (
		R  = model.ModeRead
		W  = model.ModeWrite
		RW = model.ModeReadWrite
	)
	const (
		att = model.ConfidenceAttributed
		apx = model.ConfidenceApproximate
	)
	day := 24 * time.Hour

	obs := []model.Observation{
		// ---- Observed access (real collectors → observed=true, permitted=false) ----
		// Coder agent's firmly-attributed reads/writes. Each is ALSO granted below
		// (same natural key), so the upsert merges them to observed&&permitted — the
		// healthy "granted and exercised" case that must NOT appear in drift.
		edge("agent", AgentCoder, "postgres.table", ResCustomers, R, model.SignalPGAudit, att, "", now.Add(-2*time.Minute)),
		edge("agent", AgentCoder, "postgres.table", ResOrders, RW, model.SignalEBPF, att, "", now.Add(-3*time.Minute)),
		edge("agent", AgentCoder, "s3.bucket", ResExports, R, model.SignalCloudTrail, att, "", now.Add(-5*time.Minute)),
		// FIRM UNEXPECTED ACCESS: coder reads secrets with no matching grant.
		edge("agent", AgentCoder, "postgres.table", ResSecrets, R, model.SignalPGAudit, att, "", now.Add(-90*time.Second)),
		// APPROXIMATE (shared pool) unexpected write — honest low-confidence attribution.
		edge("identity", IdentityPool, "postgres.table", ResLogs, W, model.SignalPGAudit, apx, "", now.Add(-7*time.Minute)),
		// RECONCILIATION PENDING: the UNBOUND indexer reads billing; a grant exists
		// for billing+read (etl_role, below) but the agent has no resolved identity.
		edge("agent", AgentIndexer, "postgres.table", ResBilling, R, model.SignalPGAudit, att, "", now.Add(-30*time.Second)),
		// The reviewer agent reads its own review table (so all three agents are a
		// visible fleet; an unexpected access, honestly flagged).
		edge("agent", AgentReviewer, "postgres.table", "appdb.public.reviews", R, model.SignalPGAudit, att, "", now.Add(-6*time.Minute)),

		// ---- Live cooperative session (OTEL) — drives an internal design note (not shipped) ----
		// Order matters: sessions sets current_action per-event in EMIT order, so the
		// in-flight tool call (create_issue) is emitted LAST to be the current action.
		// A supervisor→worker delegation (orchestration relation).
		edge("session", SessionLive, "agent.task", "worker-indexer", R, model.SignalOTEL, att, "Task", now.Add(-30*time.Second)),
		edge("session", SessionLive, "mcp.server", MCPGitHub, R, model.SignalOTEL, att, "", now.Add(-25*time.Second)),
		// The in-flight tool call on the GitHub MCP (current_action in the live view).
		edge("session", SessionLive, "mcp.tool", MCPGitHub+"/"+ToolCreateIss, W, model.SignalOTEL, att, ToolCreateIss, now.Add(-15*time.Second)),
		// The reviewer's session wiring to the filesystem MCP.
		edge("session", SessionEvade, "mcp.server", MCPFilesystem, R, model.SignalOTEL, att, "", now.Add(-9*time.Minute)),

		// ---- Declared/permitted grants (Source=policy → permitted=true, observed=false) ----
		// Grants matching the coder's observed accesses (same natural key) → MERGE to
		// observed&&permitted, so customers/orders/exports are NOT drift.
		edge("agent", AgentCoder, "postgres.table", ResCustomers, R, model.SignalPolicy, att, "", now.Add(-1*day)),
		edge("agent", AgentCoder, "postgres.table", ResOrders, RW, model.SignalPolicy, att, "", now.Add(-1*day)),
		edge("agent", AgentCoder, "s3.bucket", ResExports, R, model.SignalPolicy, att, "", now.Add(-1*day)),
		// A grant for billing+read held by etl_role → satisfies grantForRM for the
		// pending case above, AND is itself never observed (an unused grant).
		edge("identity", IdentityEtl, "postgres.table", ResBilling, R, model.SignalPolicy, att, "", now.Add(-1*day)),
		// UNUSED GRANT: app_role may write archive but never has.
		edge("identity", IdentityAppRole, "postgres.table", ResArchive, W, model.SignalPolicy, att, "", now.Add(-1*day)),
	}

	// ---- Cost samples (model/provider usage) — FinOps spend + session tokens + model catalog ----
	// Spread across several days so /spend/trend has >=4 points and /forecast projects.
	cost := func(sessionRef string, in, out, micro int64, at time.Time) model.CostSample {
		return model.CostSample{
			ProviderRef: "anthropic", ModelRef: "claude-opus-4-8",
			SessionRef: sessionRef, InputTokens: in, OutputTokens: out,
			CostMicroUSD: micro, OccurredAt: at,
		}
	}
	obs = append(obs,
		cost(SessionLive, 1200, 800, 42000, now.Add(-20*time.Second)), // ties tokens to the live session
		cost("sess-batch-1", 50000, 12000, 310000, now.Add(-1*day)),
		cost("sess-batch-2", 48000, 11000, 295000, now.Add(-3*day)),
		cost("sess-batch-3", 90000, 22000, 540000, now.Add(-5*day)),
		cost("sess-batch-4", 30000, 7000, 180000, now.Add(-6*day)),
	)
	// A second model so the catalog/routing estate is non-trivial.
	obs = append(obs, model.CostSample{
		ProviderRef: "openai", ModelRef: "gpt-4o", SessionRef: "sess-batch-2",
		InputTokens: 8000, OutputTokens: 2000, CostMicroUSD: 60000, OccurredAt: now.Add(-3 * day),
	})

	// ---- Findings (guardrail / anti-evasion) — security anomalies + sessions timeline + notify ----
	finding := func(kind string, sev model.Severity, subjKind, subjRef, title string, at time.Time) model.FindingReport {
		return model.FindingReport{
			Kind: kind, Severity: sev, SubjectKind: subjKind, SubjectRef: subjRef,
			Title: title, DetailHash: "", OccurredAt: at,
		}
	}
	obs = append(obs,
		// A high-severity guardrail finding on the live session (security anomaly + timeline).
		finding("guardrail", model.SeverityHigh, "session", SessionLive, "possible prompt-injection in tool args", now.Add(-40*time.Second)),
		// An anti-evasion correlation pair on the evade session (kernel + cooperative)
		// → security correlates, sessions flips that session to silent_evasion.
		finding("anti_evasion", model.SeverityHigh, "identity", IdentityPool, "kernel access with no cooperative trace", now.Add(-8*time.Minute)),
		finding("anti_evasion", model.SeverityHigh, "session", SessionEvade, "cooperative trace gap", now.Add(-8*time.Minute)),
	)

	return obs
}

// source is a SourceConnector that emits the demo estate once and returns. It is
// the real-channel path: the runtime lifts each Emit onto the bus exactly as a
// live pg-audit/OTEL collector would, so every subscribing module reacts through
// its production handler.
type source struct {
	name string
	now  time.Time
}

// NewSource builds a one-shot demo SourceConnector. name must be unique per
// registration (the runtime reserves descriptor names); now anchors the estate's
// clock so the live session reads as active.
func NewSource(name string, now time.Time) sdk.SourceConnector {
	if name == "" {
		name = SourceName
	}
	return &source{name: name, now: now}
}

func (s *source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name: s.name, Version: "0.1.0", APIVersion: sdk.APIVersion, Type: sdk.TypeSource,
		Title:       "Olivares demo estate",
		Description: "Emits a synthetic, minimal-data AI-agent estate for demos and E2E.",
	}
}

func (s *source) Open(context.Context, sdk.Config) error { return nil }

// Gather emits every observation once, in order, honoring ctx, then returns nil
// (a completed batch). The engine does not re-invoke a clean-returning batch
// source, so the estate is emitted exactly once per registration.
func (s *source) Gather(ctx context.Context, sink sdk.Sink) error {
	for _, o := range Observations(s.now) {
		if err := sink.Emit(ctx, o); err != nil {
			return err
		}
	}
	return nil
}

func (s *source) Close(context.Context) error { return nil }
