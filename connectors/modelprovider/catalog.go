// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package modelprovider

import (
	"context"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

// The provider natural references. They are the values a connector puts in
// model.CostSample.ProviderRef (and in Provider.Ref / Model.ProviderRef), so the
// engine resolves one Provider entity per value. For the cost stream this ref is
// the provenance discriminator (the per-provider "signal source"); the event-level
// provenance is additionally carried by event.Event.Source = the connector name.
const (
	// ProviderAnthropic is the Claude API / Console (claude-api connector).
	ProviderAnthropic = "anthropic"
	// ProviderOpenAI is the OpenAI platform (openai connector).
	ProviderOpenAI = "openai"
	// ProviderAzureOpenAI is OpenAI served through Azure (openai connector, azure mode).
	ProviderAzureOpenAI = "azure-openai"
	// ProviderOpenAICodex is OpenAI's Codex coding-agent surface (codex connector). It is
	// billed by OpenAI but tracked under its own ref so FinOps can attribute Codex
	// adoption/spend distinctly from raw OpenAI API usage (the openai connector), and so
	// module X's catalog/governance views do not collide the two OpenAI surfaces.
	ProviderOpenAICodex = "openai-codex"
	// ProviderFal is the fal.ai media-inference platform (fal connector). It bills
	// pay-per-output (per image/second/megapixel), NOT per token, so its catalog models
	// carry no token-based ModelPricing — cost is metered around the queue API.
	ProviderFal = "fal"
	// ProviderGLM is the Zhipu AI / Z.ai GLM platform (glm connector). A PRC-nexus frontier
	// provider (parent Entity-Listed); the connector is catalog-only + Meter (no usage API)
	// with a sovereignty caveat on BOTH the z.ai (Singapore-wrapped) and bigmodel.cn surfaces.
	ProviderGLM = "glm"
	// ProviderGoogle is Gemini / Gemini Enterprise Agent Platform (formerly Vertex AI)
	// (gemini connector).
	ProviderGoogle = "google"
	// ProviderCursor is the Cursor AI code-editor agent surface (cursor connector). It
	// bills usage-based spend (model cost + Cursor token rate) per event via the team
	// Admin API; tracked under its own ref so FinOps attributes Cursor agent spend
	// distinctly from the underlying model providers it routes to.
	ProviderCursor = "cursor"
	// ProviderMistral is the Mistral AI platform / la Plateforme (mistral connector). A
	// European frontier provider with its own Organization → Workspace structure; tracked
	// under its own ref so the catalog/governance views and FinOps attribute Mistral usage
	// and spend distinctly.
	ProviderMistral = "mistral"
	// ProviderXAI is the xAI / Grok platform (xai connector). A frontier provider whose
	// Management API exposes key + ACL inventory and whose usage/billing feeds cost;
	// tracked under its own ref so the catalog/governance views and FinOps attribute Grok
	// usage and spend distinctly.
	ProviderXAI = "xai"
	// ProviderDeepSeek is the DeepSeek platform (deepseek connector). A frontier
	// provider whose hosted API runs on PRC servers under Chinese law; the connector
	// is catalog-only with an explicit sovereignty caveat. Self-hosted open-weight
	// inference (V3/R1/V4) uses the local-inference providers (Ollama/vLLM), not this
	// ref — this ref is the HOSTED API surface only.
	ProviderDeepSeek = "deepseek"
	// ProviderCohere is the Cohere platform / North (cohere connector). An enterprise
	// provider with on-prem/VPC sovereign deployment via Model Vault; catalog-only
	// connector with cost via Meter (no usage API). Tracked under its own ref so the
	// catalog/governance views attribute Cohere usage distinctly.
	ProviderCohere = "cohere"
	// ProviderOpenRouter is the OpenRouter aggregation gateway (openrouter connector). A
	// unified API in FRONT of 400+ models across many upstream providers; the connector
	// reads the live model catalog (GET /api/v1/models, with per-token list pricing) and
	// the account usage/limit posture (GET /api/v1/auth/key), governs an approved-model
	// allow/deny policy, and meters spend distinctly under this ref so FinOps separates
	// OpenRouter-routed cost from the underlying upstream providers it fans out to.
	ProviderOpenRouter = "openrouter"
	// ProviderOllama is local inference via Ollama (local connector).
	ProviderOllama = "ollama"
	// ProviderVLLM is local inference via vLLM (local connector).
	ProviderVLLM = "vllm"
)

// ProviderKind classifies how a provider is reached, which the router and FinOps
// use to reason about cost (a hosted API bills per token; local inference bills
// compute, not $/token).
type ProviderKind string

const (
	// KindHostedAPI is a hosted, per-token-billed API (Anthropic, OpenAI, Gemini).
	KindHostedAPI ProviderKind = "hosted_api"
	// KindLocalInference is operator-run inference (Ollama, vLLM): no $/token price.
	KindLocalInference ProviderKind = "local_inference"
	// KindGateway is a multi-provider gateway the operator routes through.
	KindGateway ProviderKind = "gateway"
)

// Capability is a single model/provider capability flag. The set covers the full
// Claude stack (README.md, module X) plus the cross-vendor analogs, so module X
// can render one capability matrix across providers and the router can require a
// capability (e.g. "must support vision").
type Capability string

// The capability flags. The Claude-stack set is explicit because surfacing the
// whole stack (caching, batch, files, thinking, computer use, memory, context
// management, vision/PDF, structured outputs, citations) is a module-X requirement.
const (
	// CapStreaming is incremental token streaming.
	CapStreaming Capability = "streaming"
	// CapToolUse is tool/function calling.
	CapToolUse Capability = "tool_use"
	// CapVision is image input understanding.
	CapVision Capability = "vision"
	// CapPDF is native PDF document input.
	CapPDF Capability = "pdf"
	// CapStructuredOutputs is schema-constrained (JSON-schema) output.
	CapStructuredOutputs Capability = "structured_outputs"
	// CapPromptCaching is prompt/context caching (cache-write + cache-read tiers).
	CapPromptCaching Capability = "prompt_caching"
	// CapBatch is asynchronous batch processing.
	CapBatch Capability = "batch"
	// CapFiles is the files API (upload/reference of documents).
	CapFiles Capability = "files"
	// CapExtendedThinking is extended/interleaved reasoning ("thinking").
	CapExtendedThinking Capability = "extended_thinking"
	// CapComputerUse is the computer-use tool (screen/keyboard/mouse).
	CapComputerUse Capability = "computer_use"
	// CapMemoryTool is the memory tool (persistent agent memory).
	CapMemoryTool Capability = "memory_tool"
	// CapContextManagement is server-side context management / compaction.
	CapContextManagement Capability = "context_management"
	// CapCitations is grounded citations in the output.
	CapCitations Capability = "citations"
)

// Has reports whether caps contains c. It is the small helper the router uses to
// test a required capability against a Model's declared set.
func Has(caps []Capability, c Capability) bool {
	for _, x := range caps {
		if x == c {
			return true
		}
	}
	return false
}

// Provider is one model provider present in the estate. It carries only
// non-sensitive configuration metadata (a base URL, never a credential).
type Provider struct {
	// Ref is the provider natural reference (one of the Provider* constants, or an
	// operator-defined value for a self-hosted gateway).
	Ref string
	// Kind classifies how the provider is reached and billed.
	Kind ProviderKind
	// Title is a short human display label ("Anthropic", "OpenAI").
	Title string
	// BaseURL is the configured endpoint, with no credential or query secret.
	BaseURL string
	// Local is true for operator-run inference (mirrors Kind==KindLocalInference).
	Local bool
}

// Model is one model offered by a provider, with its declared capabilities and
// pricing. Pricing is nil when unknown or not applicable (local inference has no
// $/token list price); a nil Pricing means cost cannot be derived for this model.
type Model struct {
	// ProviderRef ties the model to its Provider.
	ProviderRef string
	// Ref is the model identifier as the provider names it ("claude-sonnet-4-5").
	Ref string
	// DisplayName is the human label the provider publishes, when known.
	DisplayName string
	// Capabilities is the declared capability set.
	Capabilities []Capability
	// ContextWindow is the maximum input context in tokens (0 if unknown).
	ContextWindow int64
	// MaxOutputTokens is the maximum output in tokens (0 if unknown).
	MaxOutputTokens int64
	// Pricing is the declared list price used to derive cost (nil if unavailable).
	Pricing *ModelPricing
	// Deprecated marks a model the provider has retired or is sunsetting.
	Deprecated bool
	// CreatedAt is the model's publish date from the provider's models API (zero
	// when the API does not report it).
	CreatedAt time.Time
	// ObservedLatencyMillis is a measured response latency in milliseconds, used by
	// the router's latency policy (0 = unknown/unmeasured). It is populated where a
	// connector can probe it cheaply (e.g. local inference); hosted-API connectors
	// leave it 0 unless the operator supplies a measurement.
	ObservedLatencyMillis int64

	// --- Additive: live Models-API source-of-truth (ANT2-16). These are
	// populated when a connector reads the provider's models API capabilities instead
	// of relying on a hardcoded catalog; they are zero/nil when the surface does not
	// expose them (e.g. Bedrock/Vertex/Foundry have no Models API) and the connector
	// degraded to the offline catalog. They never replace Capabilities (the coarse
	// cross-vendor flags) — they refine it with the per-model knobs only the live API
	// reports.

	// MaxInputTokens is the model's maximum input context the provider's Models API
	// reports (0 if unknown). It is distinct from ContextWindow only where the API
	// reports input and total windows separately; otherwise they coincide.
	MaxInputTokens int64
	// APICapabilities is the per-model knob set the provider's Models API reports
	// (effort levels, thinking modes, context-management strategies, batch/structured
	// outputs). Nil when not read from the live API. It is the source-of-truth a
	// hardcoded catalog cannot keep current (ANT2-16).
	APICapabilities *ModelCapabilities
	// CapabilitySource records whether Capabilities/APICapabilities came from the live
	// provider API ("live") or the connector's declared offline catalog ("declared").
	// Empty is treated as "declared" by consumers, so an estimate is never shown as
	// authoritative (ARCHITECTURE.md).
	CapabilitySource string
	// Retirements is the per-deployment-surface retirement schedule (ANT2-03). Model
	// lifecycle is PER-PLATFORM: the first-party/Claude-Platform-on-AWS/Foundry
	// retirement date differs from Bedrock and Vertex (e.g. Sonnet 4 first-party
	// 2026-06-15 vs Vertex 2026-09-14), so a single Deprecated flag would give false
	// migration deadlines. Empty/nil means no scheduled retirement is known.
	Retirements []ModelRetirement
	// SurfaceContextWindows is the per-deployment-surface context window (ANT2-01).
	// Like retirement, the maximum input context DIVERGES per surface: Claude Opus 4.8
	// is 1M on the Claude API / Bedrock / Vertex but only 200K on Microsoft Foundry, so
	// the single ContextWindow above (the STANDARD window) is silently wrong on Foundry.
	// Empty/nil means no per-surface divergence is modeled (ContextWindow applies on
	// every surface). See SurfaceContextWindow.
	SurfaceContextWindows []SurfaceContextWindow

	// DefaultEffort is the model's default effort level when no explicit effort is
	// specified in the request (e.g. "high" for Opus 4.8). Empty means unknown or the
	// model does not support effort control. This is declared per model from primary
	// docs, NOT from the live API (the Models API reports supported levels, not
	// defaults). AsOf-stamped alongside the model.
	DefaultEffort string

	// SurfaceMaxOutputs is the per-deployment-surface maximum output token override.
	// Like SurfaceContextWindows, the max output DIVERGES per (model, surface) when a
	// beta (e.g. output-300k-2026-03-24) applies to some surfaces but not others.
	// Empty/nil means MaxOutputTokens applies on every surface.
	SurfaceMaxOutputs []SurfaceMaxOutput
}

// SurfaceMaxOutput is one deployment surface's maximum output token limit for a model.
// The output cap can diverge per (model, surface) when a beta like output-300k applies
// to some surfaces but not others. Model.MaxOutputTokens is the standard value; this
// overrides it for the surfaces where the authority published a different value.
type SurfaceMaxOutput struct {
	// Surface is the deployment surface this output limit applies to.
	Surface model.Gateway
	// MaxOutputTokens is the maximum output in tokens on this surface.
	MaxOutputTokens int64
	// Beta names the output beta that grants this higher limit (e.g.
	// "output-300k-2026-03-24"), empty when the limit is the standard value.
	Beta string
	// AsOf stamps when this was recorded (UTC date).
	AsOf string
}

// ModelCapabilities is the per-model knob set a provider's Models API reports
// (ANT2-16). It is the live source-of-truth that supersedes a hardcoded catalog: a
// new model version's effort levels, thinking modes and context-management
// strategies are read from the API rather than guessed. Every field is optional;
// an empty slice/false means "the API did not report it", never "unsupported"
// (ARCHITECTURE.md). Carried as provider vocabulary (string slices), not sealed enums, so
// a new effort tier or context-management strategy needs no SDK release.
type ModelCapabilities struct {
	// Batch reports whether the model supports the asynchronous Batches API.
	Batch bool
	// StructuredOutputs reports whether the model supports schema-constrained output.
	StructuredOutputs bool
	// EffortLevels are the supported effort tiers in API order (e.g.
	// "low","medium","high","xhigh"). Empty = the API reports no effort control.
	EffortLevels []string
	// ThinkingModes are the supported extended-thinking modes (e.g.
	// "adaptive","enabled"). Empty = the API reports no thinking control.
	ThinkingModes []string
	// ContextManagement are the server-side context-management strategy ids the model
	// supports (e.g. "clear_thinking_20251015"). Empty = none reported.
	ContextManagement []string
	// AsOf stamps when these capabilities were captured (the connector's clock, UTC
	// date), so a consumer can show staleness rather than imply the snapshot is live.
	AsOf string
}

// ModelRetirement is one deployment surface's retirement schedule for a model
// (ANT2-03). The same model retires on different dates per surface, so the schedule
// is a SET keyed by surface, AsOf-stamped, never a single global date.
type ModelRetirement struct {
	// Surface is the deployment surface this date applies to (direct, bedrock-mantle,
	// vertex, foundry, claude-platform-aws).
	Surface model.Gateway
	// DeprecatedOn is the date the provider deprecated the model (ISO-8601 date, e.g.
	// "2026-04-14"). Empty means "not deprecated / date not published" — an absent
	// published date, never an inferred one (ARCHITECTURE.md).
	DeprecatedOn string
	// RetiresOn is the retirement date on this surface (ISO-8601 date, e.g.
	// "2026-09-14"). Empty means "scheduled but date not published".
	RetiresOn string
	// ReplacementRef is the recommended successor model id, when the provider names
	// one (empty if none).
	ReplacementRef string
	// AsOf stamps when this schedule entry was recorded (the connector's clock, UTC
	// date), so migration planning can weigh staleness (ARCHITECTURE.md).
	AsOf string
}

// SurfaceContextWindow is one deployment surface's maximum input context for a model
// (ANT2-01). Model context windows DIVERGE per surface, so a single ContextWindow is
// silently wrong on the surfaces where it does not hold: e.g. Claude Opus 4.8 has a
// 1M-token window on the Claude API / Amazon Bedrock / Gemini Enterprise Agent Platform
// (formerly Vertex AI) but only 200K on
// Microsoft Foundry (verified jun-2026). Model.ContextWindow carries the model's
// STANDARD window; Model.SurfaceContextWindows enumerates the per-surface reality so a
// router/governance check knows a 1M request to Foundry will be truncated or rejected
// (stop_reason model_context_window_exceeded) instead of assuming the standard window
// applies everywhere. Empty/nil means no per-surface divergence is modeled.
type SurfaceContextWindow struct {
	// Surface is the deployment surface this window applies to (direct, bedrock-mantle,
	// bedrock-legacy, vertex, foundry, claude-platform-aws).
	Surface model.Gateway
	// ContextWindow is the maximum input context in tokens on this surface (the
	// surface-effective value, already reflecting any per-surface cap).
	ContextWindow int64
	// AsOf stamps when this was recorded (UTC date), so a consumer can weigh staleness.
	AsOf string
}

// HasCapability reports whether the model declares capability c.
func (m Model) HasCapability(c Capability) bool { return Has(m.Capabilities, c) }

// KeyRef is API-key inventory METADATA — never the key value. Admin APIs return a
// partial hint (a masked suffix) and never the secret; this type mirrors that:
// there is deliberately no field that could hold a usable credential (docs/SECURITY-HARDENING.md).
type KeyRef struct {
	// ID is the provider's key identifier.
	ID string
	// Name is the operator-assigned key name.
	Name string
	// WorkspaceRef ties the key to a workspace/project (empty if none).
	WorkspaceRef string
	// Status is the key lifecycle state ("active", "inactive", "archived").
	Status string
	// Hint is the masked partial the provider returns (e.g. "sk-…aB12"); it is NOT
	// a usable credential and is safe to display.
	Hint string
	// CreatedAt is when the key was created (zero if unknown).
	CreatedAt time.Time
	// ExpiresAt is when the key is scheduled to expire (zero = no expiry / unknown).
	// It is the key-lifecycle/rotation governance signal: a key with no expiry, or one
	// long past its creation with no rotation, is a hygiene posture finding.
	ExpiresAt time.Time
	// CreatedBy is the reference of the principal that created the key (empty if
	// unknown). Attribution metadata for key-lifecycle governance, never a credential.
	CreatedBy string
	// PrincipalType is what the key authenticates AS — "service_account" | "user" |
	// "" (unbound/unknown). A governance attribution signal: a long-lived
	// service-account key carries a different rotation expectation than a user key.
	PrincipalType string
}

// WorkspaceRef is workspace/project inventory metadata for governance views.
type WorkspaceRef struct {
	// ID is the provider's workspace/project identifier.
	ID string
	// Name is the workspace display name.
	Name string
	// Archived is true for an archived/closed workspace.
	Archived bool
	// CreatedAt is when the workspace was created (zero if unknown).
	CreatedAt time.Time

	// --- Additive: the governance Workspace object (ANT2-06). These model the
	// data-residency, customer-managed-encryption (CMEK), compartment and tag
	// metadata the workspace carries; they are inventory/refs only (NEVER the key
	// material — ExternalKeyID is the ekey_ reference, not the KMS key). Empty/nil
	// means "not reported by this surface" (Foundry has no Admin API), never "absent".

	// Residency is the workspace's data-residency policy (allowed + default inference
	// geos). Nil when the surface does not expose it.
	Residency *DataResidency
	// ExternalKeyID is the customer-managed-key (CMEK) reference bound to the workspace
	// (an ekey_ id; write-once at the provider). Empty means provider-managed encryption
	// — a posture finding worth surfacing for regulated estates. It is a REFERENCE, never
	// the key value (docs/SECURITY-HARDENING.md).
	ExternalKeyID string
	// CompartmentID is the cloud-KMS compartment/partition the CMEK key-policy is scoped
	// to (used by the key policy in the customer's KMS). Empty if not set.
	CompartmentID string
	// Geo is the workspace's immutable home region (e.g. "us"). Distinct from the
	// inference-geo: it is where workspace metadata lives, fixed at creation.
	Geo string
	// Tags are operator-assigned workspace tags (cost-center / environment labels).
	// Nil if none. Values are non-secret governance metadata, never credentials.
	Tags map[string]string
}

// DataResidency is a workspace's data-residency policy (ANT2-06/17): which inference
// geos are permitted and which is the default. It is the structural basis of the
// residency/compliance matrix (module XIII): an observed per-request inference_geo
// (CostSample.InferenceGeo) outside AllowedInferenceGeos is a residency violation.
type DataResidency struct {
	// AllowedInferenceGeos are the geos the workspace permits inference in (e.g.
	// "us","global"). Empty means "unrestricted/unreported" — never inferred as denied.
	AllowedInferenceGeos []string
	// DefaultInferenceGeo is the geo used when a request does not pin one. Empty if
	// the provider does not report a default.
	DefaultInferenceGeo string
}

// ExternalKeyRef is customer-managed-encryption-key (CMEK / External Keys) inventory
// METADATA — never the key material (ANT2-04). The Admin API returns the ekey_
// reference, the cloud-KMS provider it wraps, and the last validation state; this
// type mirrors that and has deliberately NO field that could hold key bytes or a
// usable KMS credential (docs/SECURITY-HARDENING.md). It populates the governance/posture view
// renders; the connector also emits posture findings for workspaces without a CMEK.
type ExternalKeyRef struct {
	// ID is the provider's external-key reference (an ekey_ id), never the key value.
	ID string
	// Provider is the cloud KMS the key lives in ("aws_kms","gcp_kms","azure_keyvault").
	Provider string
	// Name is the operator-assigned display label (empty if none).
	Name string
	// State is the key lifecycle/validation state the provider reports
	// ("active","validating","invalid","disabled"); empty if unknown.
	State string
	// LastValidatedAt is when the provider last completed the validate round-trip
	// (encrypt/decrypt within the documented bound); zero if never/unknown.
	LastValidatedAt time.Time
	// InUse reports whether the key is currently referenced by a workspace
	// (external keys are immutable while referenced — a deletion/posture signal).
	InUse bool
	// CreatedAt is when the key was registered (zero if unknown).
	CreatedAt time.Time
}

// RateLimitValue is one limiter inside a rate-limit group ({type, value} on the
// wire). OrgLimit carries the workspace endpoint's org_limit echo (the org-level
// value for the same limiter); 0 means "not reported / not applicable" (org-scoped
// rows never carry it, and a null org_limit means the org has no configured value),
// never a hard 0 ceiling.
type RateLimitValue struct {
	Type     string
	Value    int64
	OrgLimit int64
}

// RateLimitRef is one organization- or workspace-scoped rate-limit GROUP the
// read-only Rate Limits API reports (ANT2-05, verified 2026-07-04). It is inventory a
// gateway/proxy operator must keep in sync; it is NOT a control the connector mutates.
// group_type partitions the group (model_group/batch/token_count/files/skills/
// web_search, open vocabulary). models carries every model id and alias for
// model_group rows and is nil otherwise. Workspace rows are OVERRIDES ONLY: absence of
// a workspace group or limiter means that workspace inherits the organization value;
// it is NOT unlimited. Managed Agents are explicitly NOT covered by this API (a
// documented gap, surfaced as a caveat).
type RateLimitRef struct {
	// WorkspaceRef is the workspace the group is scoped to; empty for an
	// organization-wide group.
	WorkspaceRef string
	// GroupType partitions the group (model_group|batch|token_count|files|skills|
	// web_search). Carried as provider vocabulary, not a sealed enum.
	GroupType string
	// Models carries model ids and aliases for model_group entries; nil otherwise.
	Models []string
	// Limits are the concrete limiters reported inside this group.
	Limits []RateLimitValue
}

// Catalog is the point-in-time snapshot a model/provider connector exposes to
// module X / through CatalogProvider. It is reference data, distinct from the
// CostSample observation stream: the models/capabilities/pricing populate the
// model-management view; the key/workspace inventory populates the governance view.
type Catalog struct {
	// Provider describes the provider this snapshot is for.
	Provider Provider
	// Models is the provider's model list with capabilities and pricing.
	Models []Model
	// Keys is the API-key inventory (metadata only; never key values).
	Keys []KeyRef
	// Workspaces is the workspace/project inventory.
	Workspaces []WorkspaceRef
	// CapturedAt is when the snapshot was taken (the connector's clock, UTC).
	CapturedAt time.Time

	// --- Additive: governance inventory for the posture/admin views.

	// ExternalKeys is the customer-managed-encryption-key inventory (ANT2-04). Metadata
	// only — never key material. Nil when the surface has no Admin API / no CMEK.
	ExternalKeys []ExternalKeyRef
	// RateLimits is the read-only rate-limit inventory a gateway must mirror (ANT2-05).
	// Nil when the surface does not expose the Rate Limits API.
	RateLimits []RateLimitRef
	// BetaHeaders is the inventory of provider beta-feature-flag header values the
	// catalog recognizes (e.g. the anthropic-beta header enum), so the governance view
	// can show which experimental surfaces are reachable. Empty if not modeled.
	BetaHeaders []string
}

// FindModel returns the model with the given ref and whether it was found.
func (c Catalog) FindModel(ref string) (Model, bool) {
	for _, m := range c.Models {
		if m.Ref == ref {
			return m, true
		}
	}
	return Model{}, false
}

// CatalogProvider is implemented by a model/provider connector in ADDITION to
// sdk.SourceConnector. The SourceConnector half streams usage/cost as CostSample
// observations; this half exposes the live catalog (models, capabilities,
// pricing, key/workspace inventory) to module X. The host or module type-asserts
// a connector to CatalogProvider to read it. Snapshot is read-only and must not
// emit observations.
type CatalogProvider interface {
	// Snapshot returns the current catalog. It performs read-only provider API
	// calls (or returns the connector's declared/offline catalog when no
	// credential is configured) and honors ctx for cancellation.
	Snapshot(ctx context.Context) (Catalog, error)
}
