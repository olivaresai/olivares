// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package model

import "time"

// ObservationType is the closed wire discriminator for the kinds of fact a
// SourceConnector can emit. A host maps each value 1:1 onto the wire payload, and
// a value outside this set is rejected at runtime (a defined error), not guessed.
// Adding a kind is an additive change to this package; the maintainer must extend
// the host switches, and the runtime default-case error catches any kind that was
// missed — the seal only guarantees no third party can introduce a kind.
type ObservationType string

// The observation kinds seeded in v1.
const (
	// ObsEdge is an EdgeObservation (an origin touched a resource, R/RW).
	ObsEdge ObservationType = "edge"
	// ObsCost is a CostSample (model/provider usage cost).
	ObsCost ObservationType = "cost"
	// ObsFinding is a FindingReport (guardrail/red-team/forensic finding).
	ObsFinding ObservationType = "finding"
	// ObsMetric is a MetricSample (a non-cost, non-edge productivity/adoption measure).
	ObsMetric ObservationType = "metric"
)

// Observation is the sealed sum type of facts a SourceConnector emits through a
// Sink. The set is closed by design: it is part of the versioned wire contract,
// so only this package defines observation types. The interface is sealed with
// an unexported marker (isObservation) that only the SDK's own DTOs implement,
// so a third party cannot satisfy it and slip an unencodable value into a host.
//
// The DTOs use value receivers, so a *EdgeObservation also satisfies Observation;
// callers may Emit a value or a pointer and the engine normalizes both. An
// unknown observation kind is impossible to construct externally and is a defined
// error (never a panic) if one ever reaches the wire.
type Observation interface {
	// ObservationType returns the wire discriminator for this observation.
	ObservationType() ObservationType
	isObservation()
}

// EdgeObservation is the normalized, minimal-data fact a SourceConnector emits
// when it sees an origin (agent/identity/session) touch a resource. It is the
// wire shape only: it carries identifiers and the access classification, never
// payloads, SQL bodies, secrets or PII (docs/SECURITY-HARDENING.md). The engine resolves the
// string references to entities and merges the observation into a persisted
// AccessEdge (ARCHITECTURE.md).
//
// Fields are deliberately strings rather than engine IDs: a connector knows a
// resource by its natural name (a table, a bucket, an API), not by the engine's
// internal identifier. Resolution and de-duplication happen in the engine.
type EdgeObservation struct {
	// OriginKind is what acted: "agent", "identity" or "session".
	OriginKind string
	// OriginRef is the connector's natural reference for the origin (e.g. a
	// role name, an application_name, an agent external id).
	OriginRef string
	// ResourceKind is the class of resource (e.g. "postgres.table",
	// "s3.bucket", "http.api").
	ResourceKind string
	// ResourceRef is the connector's natural reference for the resource (e.g.
	// "public.customers", "arn:aws:s3:::bucket").
	ResourceRef string
	// Mode is the read/write classification of this access.
	Mode AccessMode
	// Source is the collector that produced the observation.
	Source SignalSource
	// Confidence is the trust in the attribution.
	Confidence Confidence
	// ToolRef optionally names the tool/operation that performed the access.
	ToolRef string
	// ObservedAt is when the access happened, in the connector's clock. It is
	// the natural-key timestamp consumers use to de-duplicate re-emitted edges
	// after a connector restart (the contract is at-least-once delivery).
	ObservedAt time.Time

	// Labels are OPERATOR-SUPPLIED attribution tags riding the observation
	// (ADDITIVE) — e.g. the OTEL_RESOURCE_ATTRIBUTES an org sets on its
	// Claude Code fleet (team, project, cost_center). The producing connector
	// allowlists which keys it honors and scrubs the values; they are attribution
	// dimensions, never PII, secrets or payloads (docs/SECURITY-HARDENING.md). nil = none. They
	// are NOT part of any natural identity: consumers must not fork dedup keys on
	// them.
	Labels map[string]string
}

// The GOVERNANCE ATTRIBUTION labels. They are declared here, once, because they
// are not operator-supplied attribution like the rest of Labels: they are the
// engine's own statement about WHICH agent engine produced an access and WHETHER
// the governing PEP could actually have stopped it.
//
// They lived as private string constants in three places — the Codex connector
// that produced them, the sessions module that folds them, and nothing at all on
// the Claude side, which produced no edges whatsoever. Three copies of a wire
// contract agreeing by inspection is a contract waiting to drift; the Claude PEP
// having none is how a governed session and an ungoverned one painted the same.
//
// A producer MUST set both. A consumer that sees neither is looking at an edge
// from a producer that has not declared itself, and must say "unknown" rather
// than assume either value.
const (
	// LabelEngine names the agent engine: EngineClaude, EngineCodex, EngineGrok.
	LabelEngine = "engine"
	// LabelPosture says whether the decision could impede: PostureEnforced or
	// PostureObserved.
	LabelPosture = "posture"
)

// Engine keys. Lowercase is canonical (the same string is the provider alias).
const (
	// EngineClaude is the Claude Code engine.
	EngineClaude = "claude"
	// EngineCodex is the Codex engine.
	EngineCodex = "codex"
	// EngineGrok is xAI's Grok Build engine (AGT-04, tier 1 by order of
	// 2026-08-17). It is the AGENT, not the xAI model API: connectors/xai reads the
	// latter and carries `grok-build-0.1` as a MODEL. Same string, different thing.
	EngineGrok = "grok"
)

// Postures an engine can honestly claim about one access.
const (
	// PostureEnforced: the PEP was in a position to refuse this call, and the
	// decision it returned was binding.
	PostureEnforced = "enforced"
	// PostureObserved: the call was seen but the hook could not have impeded it.
	// It is the WEAKER value, and a session that mixes the two folds to this one:
	// a session with one merely observed action is not an enforced session.
	PostureObserved = "observed"
)

// ObservationType identifies this as an edge observation.
func (EdgeObservation) ObservationType() ObservationType { return ObsEdge }
func (EdgeObservation) isObservation()                   {}

// CostSample is a normalized model-usage cost fact emitted by a model/provider
// connector. Monetary amounts are integer micro-units of USD to avoid floating
// point in money (ARCHITECTURE.md, module XI).
//
// The fields below the original seven are an ADDITIVE, provider-neutral extension
//: they are zero-valued for connectors that do not report them, so adding
// them never breaks an existing emitter. They are aligned to the OpenTelemetry
// gen_ai.* convention (token splits) and to FOCUS export columns (attribution +
// provenance) so a single contract serves every provider and the FOCUS export
// without re-breaking the sealed sum type. Empty/zero means "not reported", never
// "zero" — consumers must not infer absence from a missing dimension (ARCHITECTURE.md).
type CostSample struct {
	// ProviderRef and ModelRef are the connector's natural references.
	ProviderRef string
	ModelRef    string
	// SessionRef optionally ties the cost to an agent session.
	SessionRef string
	// InputTokens is the TOTAL input volume (uncached + cache-write + cache-read);
	// OutputTokens is the output count. The cache split below is a breakdown OF
	// InputTokens, not additional to it — the cost differences between tiers are
	// already settled in CostMicroUSD. This meaning is unchanged from v1.
	InputTokens  int64
	OutputTokens int64
	// CostMicroUSD is the cost in millionths of a US dollar.
	CostMicroUSD int64
	// OccurredAt is when the usage happened.
	OccurredAt time.Time

	// --- Additive: cache breakdown (a subset of InputTokens; 0 = not reported).
	// Prompt caching is the dominant cost lever for Claude; carrying the split lets
	// module XI measure realized cache savings instead of disclaiming it.
	// CacheReadTokens are cache-hit input tokens (priced ~0.1× base input).
	CacheReadTokens int64
	// CacheCreation1hTokens / CacheCreation5mTokens are cache-write tokens by TTL,
	// priced distinctly (~2.0× base for 1h, ~1.25× for 5m). Providers without a TTL
	// split leave these 0.
	CacheCreation1hTokens int64
	CacheCreation5mTokens int64

	// --- Additive: attribution dimensions (empty = not reported). These are
	// the dimensions finance allocates on; they flow into the FinOps read-model.
	// WorkspaceRef is the billing workspace/project; APIKeyRef is the api-key or
	// service-account reference (a masked id, never the secret value — docs/SECURITY-HARDENING.md).
	WorkspaceRef string
	APIKeyRef    string
	// Actor is the principal that incurred the cost — a developer/account or service
	// identity (e.g. an OAuth account id, or a Claude Code developer email). It is the
	// "who" dimension for per-developer/per-team chargeback. Empty = not reported.
	Actor string
	// ServiceTier is the billing tier (e.g. standard|batch|priority|priority_on_demand
	// |flex|flex_discount for Claude). ContextWindow is the context band (e.g.
	// "0-200k"|"200k-1M"). InferenceGeo is the data-residency region (e.g.
	// global|us|not_available). Carried as provider vocabulary, not sealed enums.
	ServiceTier   string
	ContextWindow string
	InferenceGeo  string
	// Speed is the inference-speed band ORTHOGONAL to ServiceTier (e.g. standard|fast
	// for Claude fast-mode). It is a request-time attribution dimension a producer
	// tags onto the sample (the provider does not echo it in the usage row), so it is
	// set by filtering the pull per speed; empty = not reported / not split by speed.
	// Carried as provider vocabulary, not a sealed enum.
	Speed string

	// --- Additive: surface + provenance.
	// Gateway is the deployment surface that served the call (direct|bedrock-*|vertex
	// |foundry|claude-platform-aws); empty is treated as direct by consumers.
	Gateway Gateway
	// Provenance records whether CostMicroUSD is billed (provider cost API) or
	// estimated (derived from list pricing); empty is treated as estimated.
	Provenance CostProvenance
	// CostType classifies non-token server-tool charges (e.g. Claude cost_report:
	// tokens|web_search|code_execution|session_usage). Empty means ordinary token
	// cost. It lets FinOps attribute server-tool spend the usage report cannot price.
	CostType string

	// Labels are OPERATOR-SUPPLIED attribution tags riding the sample (ADDITIVE) — e.g. the OTEL_RESOURCE_ATTRIBUTES an org sets on its Claude Code
	// fleet (team, project, cost_center), so FinOps can slice spend by the org's
	// own dimensions. The producing connector allowlists which keys it honors and
	// scrubs the values (never PII/secrets, docs/SECURITY-HARDENING.md). nil = none. Labels do
	// NOT join the dedup natural key (a re-pulled bucket with different labels is
	// the same bucket); curated entity labels (e.g. an Agent's team) outrank them
	// in consumers.
	Labels map[string]string
}

// ObservationType identifies this as a cost sample.
func (CostSample) ObservationType() ObservationType { return ObsCost }
func (CostSample) isObservation()                   {}

// FindingReport is a normalized guardrail/red-team/forensic finding emitted by a
// security connector. It carries a hash of any sensitive detail, never the raw
// detail itself (docs/SECURITY-HARDENING.md).
type FindingReport struct {
	// Kind classifies the finding (e.g. "guardrail", "redteam", "forensic").
	Kind string
	// Severity grades the finding on the shared severity scale.
	Severity Severity
	// SubjectKind and SubjectRef name what the finding is about.
	SubjectKind string
	SubjectRef  string
	// Title is a short, non-sensitive summary safe to display.
	Title string
	// DetailHash is a hex SHA-256 of the redacted detail; the raw detail is not
	// transmitted or stored (minimal-data).
	DetailHash string
	// OccurredAt is when the finding was produced.
	OccurredAt time.Time
	// OWASPLLM, OWASPASI and ATLAS are the finding's multi-taxonomy references
	//: OWASP Top 10 for LLM Applications ids (e.g. "LLM01:2025"), OWASP Top
	// 10 for Agentic Applications ids (e.g. "ASI01"), and MITRE ATLAS technique ids
	// (e.g. "AML.T0051.001"). A single finding may map to several axes at once — an
	// indirect prompt injection is simultaneously LLM01:2025 + ASI01 + AML.T0051.001 —
	// so each axis is a SET; a producer keeps them deterministic (sorted, de-duped).
	// All three are additive and optional: a finding with no framework reference
	// leaves them nil, which is byte-identical to the pre wire shape. This does
	// NOT add a new Observation kind (the sum type stays the sealed three) — it only
	// carries the reference ids a SIEM/auditor needs to query a finding by framework.
	OWASPLLM []string
	OWASPASI []string
	ATLAS    []string
}

// ObservationType identifies this as a finding report.
func (FindingReport) ObservationType() ObservationType { return ObsFinding }
func (FindingReport) isObservation()                   {}

// MetricSample is a normalized, non-cost MEASURE a SourceConnector emits — one metric
// datapoint (a count, a token tally, a duration) attributed to a subject and an
// instant. It is the carrier for signals that are neither an access edge, a monetary
// cost, nor a security finding: e.g. the Claude Code productivity/adoption metrics
// (sessions, lines of code, commits, pull requests, tool accept/reject, per-model
// tokens). It is minimal-data by construction — identifiers, dimensional attributes and
// an integer value, never payloads, prompt text, secrets or a developer's raw activity
// (docs/SECURITY-HARDENING.md). A consumer aggregates samples by subject/dimension/time; monetary cost
// stays on the authoritative CostSample path so a measure here never double-counts cost.
//
// Aggregation semantics travel in Additive so a consumer folds samples correctly into
// a per-(subject, dimensions, day) bucket: an Additive sample is a delta the consumer
// SUMS (an OTLP delta counter — many per day); a non-Additive sample is a level/snapshot
// the consumer keeps as the latest/max for its bucket (a daily total — one per day). The
// day comes from OccurredAt, which is BOTH the time dimension and the producer's
// ordering signal (a consumer dedups re-delivered deltas by the monotonic OccurredAt
// high-water; a re-pulled snapshot carries the same day, so max() is idempotent).
type MetricSample struct {
	// Name is the metric's natural name (e.g. "claude_code.lines_of_code.count").
	Name string
	// Value is the measure in its natural integer unit (a count, a token tally, a
	// duration in milliseconds). Integer to keep measure/money arithmetic exact across
	// the plane (ARCHITECTURE.md); a producer with a fractional source rounds to the unit.
	Value int64
	// Additive declares how a consumer folds repeated samples of the same (subject,
	// name, dimensions, day): true = Value is a delta/increment to SUM (an OTLP delta
	// counter); false (default) = Value is a level/snapshot to keep as the latest/max
	// (a daily total, a gauge). See the type doc.
	Additive bool
	// Unit names Value's unit ("lines"|"commits"|"sessions"|"tokens"|"ms"|"1"|…) so a
	// consumer renders it without guessing. Empty = a dimensionless count.
	Unit string
	// SubjectKind and SubjectRef name WHO/WHAT the measure is about — the aggregation
	// subject. SubjectKind is the subject class ("developer"|"team"|"session"|"account"
	// |"org"|"agent"); SubjectRef is its natural reference. A developer ref is the
	// org-internal email/key-name the ROI subject needs (the same accepted attribution
	// exception the cost path makes for Actor) — never a credential.
	SubjectKind string
	SubjectRef  string
	// OccurredAt is the datapoint's instant (a delta counter) or the bucket day (a
	// snapshot). It is BOTH the time dimension and the producer-controlled idempotency
	// key — see the type doc.
	OccurredAt time.Time
	// Dimensions are the metric's own breakdown attributes (e.g. tool, decision, type=
	// added/removed, model, language, customer_type), the axes a consumer slices on.
	// They are structural labels, never payloads/PII, and they DO join the natural key.
	// nil = none.
	Dimensions map[string]string
	// Labels are OPERATOR-SUPPLIED attribution tags riding the sample (the allowlisted
	// OTEL_RESOURCE_ATTRIBUTES a connector honors — team/project/cost_center), scrubbed
	// at collection. Distinct from Dimensions (which the metric itself carries); like
	// the Edge/Cost labels they NEVER join the natural key. nil = none.
	Labels map[string]string
}

// ObservationType identifies this as a metric sample.
func (MetricSample) ObservationType() ObservationType { return ObsMetric }
func (MetricSample) isObservation()                   {}

// The taxonomy Field keys a finding's framework axes travel under when a
// FindingReport is projected onto a Notification's Fields for SIEM export.
// The notify bridge writes them; siemfmt reads/forwards them. Each value is a
// deterministic, comma-joined, sorted id list so the SIEM output is byte-stable.
const (
	FieldOWASPLLM = "owasp_llm"
	FieldOWASPASI = "owasp_asi"
	FieldATLAS    = "atlas"
)
