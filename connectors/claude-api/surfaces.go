// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// This file models the Anthropic DEPLOYMENT SURFACES (ANT2-01) — the materialized,
// per-surface detail that refines the coarse model.Gateway enum seeded. The
// 2026 reality is NOT "Bedrock vs not": Claude reaches the same models through SIX
// distinct surfaces, each with its own base-URL, auth scheme, model-id form,
// workspace header, billing, residency, compliance posture and — critically — its
// own SUBSET of which Anthropic APIs apply. A governance/FinOps consumer that
// collapses them (e.g. assumes the Admin API exists everywhere, or that one model-id
// form works everywhere) is wrong on residency, compliance and ingest coverage.
//
// This is DESCRIPTIVE reference data (a matrix), AsOf-stamped, with an explicit
// honesty status per field (ARCHITECTURE.md): every base-URL/SigV4
// service/header/model-id form below is verbatim from the cited authority; anything
// NOT confirmed against primary docs is marked to-confirm and NOT fabricated. It
// REFINES gateway dimension — it does not replace the cooperative
// gatewayForModel resolution (which still classifies an OBSERVED id to its surface);
// it is the attribute table compliance/lifecycle matrix and the deploy docs
// read. The build target on AWS is Bedrock _Mantle_; the legacy InvokeModel/Converse
// surface is observe-only/deprecated (never a new build target). The connector
// imports only /sdk (model.Gateway), never /core or the AGPL models module — so the
// Bedrock id helpers here are a small, surface-descriptive reimplementation, not an
// import of modules/models.
//
// Authority (verbatim, jun-2026): platform.claude.com/docs/en/build-with-claude/
// claude-platform-on-aws (ANT2-01); …/manage-claude/data-residency + …/api/
// service-tiers (ANT2-17 compliance/residency per surface).
package claudeapi

import (
	"sort"
	"strings"

	"github.com/olivaresai/olivares/sdk/model"
)

// surfacesAsOf stamps the deployment-surface matrix with the date it was recorded.
const surfacesAsOf = "2026-06-06"

// ConfirmStatus is an honesty marker for a surface fact: a value that is verified
// against primary docs is "confirmed"; one that the authority page did not state (so
// we model the pattern but not the literal) is "to-confirm". It is shown to the
// operator so the product never presents an unverified fact as authoritative.
type ConfirmStatus string

const (
	statusConfirmed ConfirmStatus = "confirmed"
	statusToConfirm ConfirmStatus = "to-confirm"
)

// APISupport records, per surface, which Anthropic API families apply. Foundry,
// for example, exposes only the inference (Messages) surface — NOT Admin, Compliance,
// Models or Batches — so a connector that polls the Admin API there must degrade
// honestly (documented), never silently report empty inventory as "no findings".
type APISupport struct {
	Messages     bool // POST /v1/messages (inference) — every surface
	Admin        bool // /v1/organizations/* (usage/cost/keys/workspaces/external_keys/rate_limits)
	Compliance   bool // /v1/compliance/* activity feed
	Models       bool // GET /v1/models capabilities (ANT2-16 source-of-truth)
	Batches      bool // /v1/messages/batches
	MCPConnector bool // Messages-API mcp_toolset (mirrors MCPConnectorAvailability)
}

// Surface is the full attribute set of one Claude deployment surface (ANT2-01). It is
// keyed by the model.Gateway enum value so an observation/cost sample tagged with a
// Gateway resolves to exactly this descriptive record.
type Surface struct {
	// Gateway is the enum key (the value carried on CostSample.Gateway).
	Gateway model.Gateway
	// DisplayName is the human label ("Claude Platform on AWS").
	DisplayName string
	// Operator is who operates the inference plane and thus who can access data:
	// "Anthropic", "AWS", "Google", "Microsoft".
	Operator string
	// OperatorDataAccess records the operator's data-access posture verbatim
	// (e.g. "Anthropic-operated", "zero operator access; data governed by AWS").
	OperatorDataAccess string
	// BaseURLPattern is the endpoint template, with {region}/{resource} placeholders.
	BaseURLPattern string
	// AuthScheme is the human description of how the credential is presented.
	AuthScheme string
	// SigV4Service is the AWS SigV4 signing service name ("" for non-AWS surfaces).
	// It DIFFERS per AWS surface — that is the whole point of ANT2-01: the same model
	// signs under aws-external-anthropic, bedrock-mantle or bedrock (legacy).
	SigV4Service string
	// WorkspaceHeader is the workspace-scoping request header ("anthropic-workspace-id"
	// on Claude Platform on AWS; "" where the surface scopes by key/deployment instead).
	WorkspaceHeader string
	// ModelIDForm describes how a model id is formed on this surface (bare,
	// "anthropic.<model>", "<geo>.anthropic.<model>", or deployment-name indirection).
	ModelIDForm string
	// APIs is the subset of Anthropic API families that apply here.
	APIs APISupport
	// Billing is the billing channel ("Anthropic invoice", "AWS Marketplace / CCU",
	// "Azure", "GCP").
	Billing string
	// HIPAA is the surface's HIPAA posture ("yes", "no", or "to-confirm" when the
	// authority page does not state it — never fabricated).
	HIPAA string
	// HIPAAStatus marks whether HIPAA is confirmed against primary docs or to-confirm.
	HIPAAStatus ConfirmStatus
	// ZDR is the Zero-Data-Retention posture ("opt-in", "on-request", "yes",
	// "AWS-governed", "Google-governed").
	ZDR string
	// Residency describes the data-residency model in one line.
	Residency string
	// Deprecated marks an observe-only/deprecated surface (the legacy InvokeModel/
	// Converse path) — present in real estates, never a new build target.
	Deprecated bool
	// AsOf stamps when this record was recorded.
	AsOf string
	// Notes carries surface-specific caveats verbatim from the authority.
	Notes string
}

// Supports reports whether an Anthropic API family applies on this surface. api is
// one of "messages","admin","compliance","models","batches","mcp_connector"; an
// unknown family returns false (fail-closed: never assume an API exists).
func (s Surface) Supports(api string) bool {
	switch strings.ToLower(strings.TrimSpace(api)) {
	case "messages":
		return s.APIs.Messages
	case "admin":
		return s.APIs.Admin
	case "compliance":
		return s.APIs.Compliance
	case "models":
		return s.APIs.Models
	case "batches":
		return s.APIs.Batches
	case "mcp_connector", "mcp":
		return s.APIs.MCPConnector
	default:
		return false
	}
}

// anthropicSurfaces is the materialized matrix of the six deployment surfaces,
// keyed by gateway. The four AWS/Azure surfaces (ANT2-01) replace the obsolete single
// "Bedrock" notion; direct (first-party) and Vertex (Google) complete the estate.
// Every field is verbatim from the cited authority or marked to-confirm.
var anthropicSurfaces = map[model.Gateway]Surface{
	model.GatewayDirect: {
		Gateway:            model.GatewayDirect,
		DisplayName:        "Anthropic API (first-party)",
		Operator:           "Anthropic",
		OperatorDataAccess: "Anthropic-operated",
		BaseURLPattern:     "https://api.anthropic.com",
		AuthScheme:         "x-api-key (workspace API key) / Admin key for /v1/organizations/*",
		SigV4Service:       "",
		WorkspaceHeader:    "",
		ModelIDForm:        "bare model id, e.g. claude-opus-4-8",
		APIs:               APISupport{Messages: true, Admin: true, Compliance: true, Models: true, Batches: true, MCPConnector: true},
		Billing:            "Anthropic invoice",
		HIPAA:              "to-confirm",
		HIPAAStatus:        statusToConfirm,
		ZDR:                "on-request",
		Residency:          "per-request inference_geo (us|global); workspace data_residency policy",
		AsOf:               surfacesAsOf,
		Notes:              "The only surface exposing the full API set (Admin/Compliance/Models/Batches) and the Models-API source-of-truth (ANT2-16).",
	},
	model.GatewayClaudePlatformAWS: {
		Gateway:            model.GatewayClaudePlatformAWS,
		DisplayName:        "Claude Platform on AWS (Anthropic-operated)",
		Operator:           "Anthropic",
		OperatorDataAccess: "Anthropic-operated on AWS (distinct from partner-operated Bedrock)",
		BaseURLPattern:     "https://aws-external-anthropic.{region}.api.aws",
		AuthScheme:         "AWS SigV4 (service aws-external-anthropic) + IAM (65 actions; over-read in Get*)",
		SigV4Service:       "aws-external-anthropic",
		WorkspaceHeader:    "anthropic-workspace-id",
		ModelIDForm:        "bare model id (Anthropic-native), workspace selected via anthropic-workspace-id header",
		APIs:               APISupport{Messages: true, Admin: true, Compliance: true, Models: true, Batches: true, MCPConnector: true},
		Billing:            "AWS Marketplace / CCU (committed-compute units)",
		HIPAA:              "no",
		HIPAAStatus:        statusConfirmed,
		ZDR:                "opt-in (on-request)",
		Residency:          "AWS region of the endpoint; ZDR opt-in",
		AsOf:               surfacesAsOf,
		Notes:              "Anthropic-operated, so the full Admin/Compliance/Models APIs apply (unlike Bedrock). HIPAA explicitly NOT supported (ANT2-01). IAM grants 65 actions with documented over-read in Get*. Env contract VERIFIED 2026-07-03 against code.claude.com/docs/en/claude-platform-on-aws: selector CLAUDE_CODE_USE_ANTHROPIC_AWS; workspace pinning via ANTHROPIC_AWS_WORKSPACE_ID -> anthropic-workspace-id; ANTHROPIC_AWS_BASE_URL overrides the endpoint; optional ANTHROPIC_AWS_API_KEY is sent as x-api-key and takes precedence over SigV4.",
	},
	model.GatewayBedrockMantle: {
		Gateway:            model.GatewayBedrockMantle,
		DisplayName:        "Amazon Bedrock (Mantle — current surface)",
		Operator:           "AWS",
		OperatorDataAccess: "zero operator access; data governed by AWS",
		BaseURLPattern:     "https://bedrock-mantle.{region}.api.aws",
		AuthScheme:         "AWS SigV4 (service bedrock-mantle) + IAM",
		SigV4Service:       "bedrock-mantle",
		WorkspaceHeader:    "",
		ModelIDForm:        "anthropic.<model>, e.g. anthropic.claude-opus-4-8",
		APIs:               APISupport{Messages: true, Admin: false, Compliance: false, Models: false, Batches: true, MCPConnector: false},
		Billing:            "AWS (Bedrock) — no Anthropic Admin cost API",
		HIPAA:              "yes",
		HIPAAStatus:        statusConfirmed,
		ZDR:                "AWS-governed",
		Residency:          "AWS region; HIPAA/FedRAMP/IL4-5 eligible (ANT2-17)",
		AsOf:               surfacesAsOf,
		Notes:              "The CURRENT Bedrock surface and the build target. No Anthropic Admin/Models/Compliance API (AWS-governed) → Anthropic-side ingest degrades to CloudTrail/usage-derived; document, never fake. HIPAA/FedRAMP/IL4-5 live here.",
	},
	model.GatewayBedrockLegacy: {
		Gateway:            model.GatewayBedrockLegacy,
		DisplayName:        "Amazon Bedrock (legacy InvokeModel/Converse — DEPRECATED)",
		Operator:           "AWS",
		OperatorDataAccess: "zero operator access; data governed by AWS",
		BaseURLPattern:     "https://bedrock-runtime.{region}.amazonaws.com",
		AuthScheme:         "AWS SigV4 (service bedrock) + IAM (bedrock:*)",
		SigV4Service:       "bedrock",
		WorkspaceHeader:    "",
		ModelIDForm:        "<geo>.anthropic.<model> cross-region inference profile, geo ∈ {us, eu, apac, global}",
		APIs:               APISupport{Messages: true, Admin: false, Compliance: false, Models: false, Batches: true, MCPConnector: false},
		Billing:            "AWS (Bedrock)",
		HIPAA:              "yes",
		HIPAAStatus:        statusConfirmed,
		ZDR:                "AWS-governed",
		Residency:          "AWS region / cross-region inference profile geo",
		Deprecated:         true,
		AsOf:               surfacesAsOf,
		Notes:              "OBSERVE-ONLY/deprecated — NOT a new build target. The specific opus Global-CRIS inference-profile id (global.anthropic.claude-opus-4-…) is NOT verified against the AWS page (only Sonnet was listed); the FORMAT is confirmed, that concrete id is to-confirm — NOT fabricated.",
	},
	model.GatewayVertex: {
		Gateway:            model.GatewayVertex,
		DisplayName:        "Google Vertex AI",
		Operator:           "Google",
		OperatorDataAccess: "Google-governed",
		BaseURLPattern:     "https://{region}-aiplatform.googleapis.com",
		AuthScheme:         "Google ADC / OAuth2 access token",
		SigV4Service:       "",
		WorkspaceHeader:    "",
		ModelIDForm:        "publisher model id (vertex form), per-platform versioning",
		APIs:               APISupport{Messages: true, Admin: false, Compliance: false, Models: false, Batches: true, MCPConnector: false},
		Billing:            "GCP",
		HIPAA:              "to-confirm",
		HIPAAStatus:        statusToConfirm,
		ZDR:                "Google-governed",
		Residency:          "GCP region; Google-governed retention",
		AsOf:               surfacesAsOf,
		Notes:              "Lifecycle dates differ from first-party (ANT2-03): e.g. Sonnet 4 retires 2026-09-14 on Vertex vs 2026-06-15 first-party. No Anthropic Admin/Models API.",
	},
	model.GatewayFoundry: {
		Gateway:            model.GatewayFoundry,
		DisplayName:        "Microsoft Foundry",
		Operator:           "Microsoft",
		OperatorDataAccess: "Microsoft-governed (Azure)",
		BaseURLPattern:     "https://{resource}.services.ai.azure.com/anthropic/v1/*",
		AuthScheme:         "Entra ID (Azure AD OAuth2)",
		SigV4Service:       "",
		WorkspaceHeader:    "",
		ModelIDForm:        "deployment-name indirection (the Azure deployment name maps to the model)",
		APIs:               APISupport{Messages: true, Admin: false, Compliance: false, Models: false, Batches: false, MCPConnector: true},
		Billing:            "Azure",
		HIPAA:              "to-confirm",
		HIPAAStatus:        statusToConfirm,
		ZDR:                "yes",
		Residency:          "Azure region of the resource",
		AsOf:               surfacesAsOf,
		Notes:              "NO Admin/Compliance/Models/Batches API (ANT2-01) — Anthropic-side governance ingest is unavailable here; model access is by deployment-name indirection, not model id. MCP connector IS available (mirrors MCPConnectorAvailability). ZDR supported. Env contract VERIFIED 2026-07-03 against code.claude.com/docs/en/microsoft-foundry: ANTHROPIC_FOUNDRY_RESOURCE computes https://{resource}.services.ai.azure.com/anthropic, ANTHROPIC_FOUNDRY_BASE_URL overrides it, and auth uses ANTHROPIC_FOUNDRY_API_KEY or DefaultAzureCredential.",
	},
}

// SurfaceFor returns the descriptive surface record for a gateway, and whether it is
// one of the modeled surfaces. An unmodeled (third-party) gateway returns false — the
// caller keeps the Gateway value but has no attribute matrix for it (honest unknown,
// never a fabricated default).
func SurfaceFor(g model.Gateway) (Surface, bool) {
	s, ok := anthropicSurfaces[g]
	return s, ok
}

// AllSurfaces returns the full surface matrix in a stable order (by gateway value),
// for the compliance/deploy matrix renders and the contract docs.
func AllSurfaces() []Surface {
	out := make([]Surface, 0, len(anthropicSurfaces))
	for _, s := range anthropicSurfaces {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Gateway < out[j].Gateway })
	return out
}

// SurfaceModelID forms the model id for a surface from a bare Claude model id (e.g.
// "claude-opus-4-8"). It returns the formed id and a ConfirmStatus: the bedrock-legacy
// Global-CRIS opus id is to-confirm (the format is correct, the concrete id was not
// on the AWS page —), so the connector never presents it as verified. An
// already-prefixed id is returned unchanged. Foundry uses deployment-name indirection,
// so the model id is operator-pinned (returned unchanged, to-confirm).
func SurfaceModelID(g model.Gateway, bareID string) (string, ConfirmStatus) {
	id := strings.TrimSpace(bareID)
	switch g {
	case model.GatewayBedrockMantle:
		if id == "" || strings.HasPrefix(id, "anthropic.") || hasGeoPrefix(id) {
			return id, statusConfirmed
		}
		return "anthropic." + id, statusConfirmed
	case model.GatewayBedrockLegacy:
		// An already-geo-prefixed CRIS id is returned UNCHANGED — never re-region it to
		// global (that would silently change the inference geo, a residency-relevant
		// mutation). Otherwise default to the global cross-region inference profile: the
		// FORMAT is confirmed; the concrete opus id is to-confirm (pattern, not fabricated).
		if hasGeoPrefix(id) {
			return id, statusToConfirm
		}
		return "global.anthropic." + bareClaudeID(id), statusToConfirm
	case model.GatewayFoundry:
		// Deployment-name indirection: the id is the operator's Azure deployment name,
		// not derivable from the model id — return unchanged, to-confirm.
		return id, statusToConfirm
	default:
		// direct / claude-platform-aws / vertex: bare/native id.
		return id, statusConfirmed
	}
}

// geoPrefixes are the AWS cross-region inference-profile geographic prefixes.
var geoPrefixes = []string{"us", "eu", "apac", "global"}

// hasGeoPrefix reports whether id starts with "<geo>.anthropic.".
func hasGeoPrefix(id string) bool {
	for _, g := range geoPrefixes {
		if strings.HasPrefix(id, g+".anthropic.") {
			return true
		}
	}
	return false
}

// bareClaudeID strips a "<geo>.anthropic." or "anthropic." surface prefix to recover
// the underlying model id.
func bareClaudeID(id string) string {
	for _, g := range geoPrefixes {
		if p := g + ".anthropic."; strings.HasPrefix(id, p) {
			return strings.TrimPrefix(id, p)
		}
	}
	return strings.TrimPrefix(id, "anthropic.")
}
