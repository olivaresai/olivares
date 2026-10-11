// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package modelprovider

import (
	"context"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

// Provider references identify the provider in catalogs and CostSample observations.
// Event.Source separately identifies the connector that emitted an observation.
const (
	// ProviderAnthropic is the Claude API / Console (claude-api connector).
	ProviderAnthropic = "anthropic"
	// ProviderOpenAI is the OpenAI platform (openai connector).
	ProviderOpenAI = "openai"
	// ProviderAzureOpenAI is OpenAI served through Azure (openai connector, azure mode).
	ProviderAzureOpenAI = "azure-openai"
	// ProviderOpenAICodex identifies Codex separately from OpenAI API usage.
	ProviderOpenAICodex = "openai-codex"
	// ProviderFal identifies fal.ai; its output-based costs have no token pricing.
	ProviderFal = "fal"
	// ProviderGLM identifies the Zhipu AI / Z.ai GLM platform.
	ProviderGLM = "glm"
	// ProviderGoogle is Gemini / Gemini Enterprise Agent Platform (formerly Vertex AI)
	// (gemini connector).
	ProviderGoogle = "google"
	// ProviderCursor identifies Cursor separately from its upstream model providers.
	ProviderCursor = "cursor"
	// ProviderMistral identifies the Mistral AI platform / la Plateforme.
	ProviderMistral = "mistral"
	// ProviderXAI identifies the xAI / Grok platform.
	ProviderXAI = "xai"
	// ProviderDeepSeek identifies the hosted API; self-hosted models use local providers.
	ProviderDeepSeek = "deepseek"
	// ProviderCohere identifies the Cohere platform / North.
	ProviderCohere = "cohere"
	// ProviderOpenRouter identifies OpenRouter separately from its upstream providers.
	ProviderOpenRouter = "openrouter"
	// ProviderOllama is local inference via Ollama (local connector).
	ProviderOllama = "ollama"
	// ProviderVLLM is local inference via vLLM (local connector).
	ProviderVLLM = "vllm"
	// ProviderKimi labels Moonshot Kimi model metadata for an openai_compatible
	// provider record. It is not a provider kind and it does not create a store.
	ProviderKimi = "kimi"
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

// Capability is a declared model/provider capability flag used by the router.
type Capability string

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

	// MaxInputTokens is the model's maximum input context the provider's Models API
	// reports (0 if unknown). It is distinct from ContextWindow only where the API
	// reports input and total windows separately; otherwise they coincide.
	MaxInputTokens int64
	// APICapabilities refines Capabilities with the provider's live model settings.
	// Nil means these settings were not read from the live API.
	APICapabilities *ModelCapabilities
	// CapabilitySource records whether Capabilities/APICapabilities came from the live
	// provider API ("live") or the connector's declared offline catalog ("declared").
	// Empty is treated as "declared" by consumers, so an estimate is never shown as
	// authoritative (ARCHITECTURE.md).
	CapabilitySource string
	// Retirements is the per-deployment-surface retirement schedule.
	// Empty/nil means no scheduled retirement is known.
	Retirements []ModelRetirement
	// SurfaceContextWindows overrides ContextWindow for specific deployment surfaces.
	// Empty/nil means ContextWindow applies on every surface.
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

// ModelCapabilities holds settings reported by the provider's live Models API.
// Every field is optional: empty/false means unreported, not unsupported.
// Strings retain the provider's vocabulary rather than a closed enum.
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

// ModelRetirement is a model's retirement schedule for one deployment surface.
// Entries are keyed by surface and stamped with AsOf.
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

// SurfaceContextWindow overrides Model.ContextWindow for one deployment surface.
// Empty/nil Model.SurfaceContextWindows means the standard window applies everywhere.
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
	ExpiresAt time.Time
	// CreatedBy is the reference of the principal that created the key (empty if
	// unknown). Attribution metadata for key-lifecycle governance, never a credential.
	CreatedBy string
	// PrincipalType is "service_account", "user", or "" (unbound/unknown).
	PrincipalType string
}

// WorkspaceRef is workspace/project inventory metadata for governance views.
// Empty/nil governance metadata is unreported, never proof of absence.
type WorkspaceRef struct {
	// ID is the provider's workspace/project identifier.
	ID string
	// Name is the workspace display name.
	Name string
	// Archived is true for an archived/closed workspace.
	Archived bool
	// CreatedAt is when the workspace was created (zero if unknown).
	CreatedAt time.Time

	// Residency is the workspace's data-residency policy (allowed + default inference
	// geos). Nil when unreported.
	Residency *DataResidency
	// ExternalKeyID is the customer-managed-key (CMEK) reference bound to the workspace
	// (an ekey_ id; write-once at the provider, never key material).
	// Empty does not prove provider-managed encryption.
	ExternalKeyID string
	// CompartmentID is the cloud-KMS compartment/partition the CMEK key-policy is scoped
	// to. Empty does not distinguish unset from unreported.
	CompartmentID string
	// Geo is the workspace's immutable home region (e.g. "us"). Distinct from the
	// inference-geo: it is where workspace metadata lives, fixed at creation.
	Geo string
	// Tags are operator-assigned workspace tags (cost-center / environment labels).
	// Nil does not prove no tags. Values are non-secret metadata, never credentials.
	Tags map[string]string
}

// DataResidency records the workspace's allowed and default inference geographies.
type DataResidency struct {
	// AllowedInferenceGeos are the geos the workspace permits inference in (e.g.
	// "us","global"). Empty means "unrestricted/unreported" — never inferred as denied.
	AllowedInferenceGeos []string
	// DefaultInferenceGeo is the geo used when a request does not pin one. Empty if
	// the provider does not report a default.
	DefaultInferenceGeo string
}

// ExternalKeyRef is customer-managed-encryption-key inventory metadata.
// It holds references and validation state, never key bytes or usable KMS credentials.
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

// RateLimitRef is read-only organization- or workspace-scoped rate-limit inventory.
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

// Catalog is a connector's point-in-time reference data, exposed through CatalogProvider
// separately from the CostSample observation stream. Key inventory contains no secrets.
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

	// ExternalKeys is the customer-managed-encryption-key inventory. Metadata
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

// CatalogProvider exposes reference data alongside sdk.SourceConnector's observations.
// Snapshot is read-only and must not emit observations.
type CatalogProvider interface {
	// Snapshot returns the current catalog. It performs read-only provider API
	// calls (or returns the connector's declared/offline catalog when no
	// credential is configured) and honors ctx for cancellation.
	Snapshot(ctx context.Context) (Catalog, error)
}

// exactCompatibleModels are model IDs with no native connector. A tenant's
// openai_compatible record names the model; lookup is exact, not by prefix.
// Tool, vision, structured-output, and extended-thinking flags are omitted:
// the generic gateway that serves this route is text-only, and K3 requires
// the prior assistant message with its reasoning on later turns. Observed
// 2026-09-27.
//
// kimi-k3 is the platform ID (https://platform.kimi.ai/docs/pricing/chat).
// k3, k3-256k, kimi-for-coding, and kimi-for-coding-highspeed are Kimi Code
// channel IDs (https://www.kimi.com/code/docs/en/kimi-code/models.html).
// kimi-for-coding currently serves K2.8 Preview. kimi-for-coding-highspeed
// currently serves K2.7 Code HighSpeed. Code-channel token prices are not
// published on that page, so those rows stay unpriced.
// k3's 1,048,576 context is the higher-tier window. Plus and Moderato plans
// are capped at 262,144. That cap is not a second field on Model.
var exactCompatibleModels = []Model{
	{
		ProviderRef: ProviderKimi, Ref: "kimi-k3", DisplayName: "Kimi K3",
		Capabilities:    []Capability{CapStreaming, CapPromptCaching},
		ContextWindow:   1_048_576,
		MaxOutputTokens: 1_048_576,
		DefaultEffort:   "max",
		Pricing: &ModelPricing{
			InputPerMTokUSD: 3, OutputPerMTokUSD: 15,
			CacheWritePerMTokUSD: 3, CacheWrite1hPerMTokUSD: 6, CacheReadPerMTokUSD: 0.30,
			Currency: "USD", AsOf: "2026-09-27", Source: PricingList,
		},
	},
	{
		ProviderRef: ProviderKimi, Ref: "k3", DisplayName: "K3",
		ContextWindow: 1_048_576, DefaultEffort: "high",
	},
	{
		ProviderRef: ProviderKimi, Ref: "k3-256k", DisplayName: "K3 256K",
		ContextWindow: 262_144, DefaultEffort: "high",
	},
	{
		ProviderRef: ProviderKimi, Ref: "kimi-for-coding", DisplayName: "K2.8 Preview",
		ContextWindow: 1_048_576, DefaultEffort: "max",
	},
	{
		ProviderRef: ProviderKimi, Ref: "kimi-for-coding-highspeed", DisplayName: "K2.7 Code HighSpeed",
		ContextWindow: 262_144,
	},
}

// SnapshotDateSuffix reports whether id is prefix plus a snapshot date and
// nothing else. The accepted suffixes are "-" + YYYYMMDD and "-" + YYYY-MM-DD.
// The bare prefix is not a suffix; callers match that by equality.
func SnapshotDateSuffix(id, prefix string) bool {
	if prefix == "" || len(id) <= len(prefix)+1 {
		return false
	}
	if !strings.HasPrefix(id, prefix) || id[len(prefix)] != '-' {
		return false
	}
	rest := id[len(prefix)+1:]
	switch len(rest) {
	case 8:
		return allDigits(rest)
	case 10:
		return rest[4] == '-' && rest[7] == '-' &&
			allDigits(rest[0:4]) && allDigits(rest[5:7]) && allDigits(rest[8:10])
	default:
		return false
	}
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// ExactCompatibleModel returns the metadata row for an exact model ID that is
// reached through an openai_compatible provider record. ok is false when the
// ID is unknown. A family prefix never matches.
func ExactCompatibleModel(modelID string) (Model, bool) {
	id := strings.ToLower(strings.TrimSpace(modelID))
	if id == "" {
		return Model{}, false
	}
	for _, m := range exactCompatibleModels {
		if m.Ref != id {
			continue
		}
		out := m
		out.Capabilities = append([]Capability(nil), m.Capabilities...)
		if m.Pricing != nil {
			p := *m.Pricing
			out.Pricing = &p
		}
		return out, true
	}
	return Model{}, false
}
