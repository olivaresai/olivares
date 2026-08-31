// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/connectors/modelrouter"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"

	mp "github.com/olivaresai/olivares/connectors/modelprovider"
)

// The module's permissions, granted to the built-in roles by verb tier (a viewer
// gets the :read permissions, an editor :write). Catalog/feature reads are not
// sensitive; routing and key governance writes are auditable changes.
const (
	permCatalogRead  auth.Permission = "models:catalog:read"
	permRoutingRead  auth.Permission = "models:routing:read"
	permRoutingWrite auth.Permission = "models:routing:write"
	permKeysRead     auth.Permission = "models:keys:read"
	permKeysWrite    auth.Permission = "models:keys:write"
	// permRoutingExecute gates the governed routing-EXECUTION path (a spend/actuation):
	// ADMIN-tier (verb "admin"), matching the actuation convention of the other modules
	// (deploy:deployment:admin, orchestration:schedule:admin) — distinct from the
	// read-tier resolve. A viewer/editor cannot spend against a provider.
	permRoutingExecute auth.Permission = "models:routing:admin"
	// permRatelimitsRead gates the read-only Anthropic Rate Limits inventory (ANT2-05).
	permRatelimitsRead auth.Permission = "models:ratelimits:read"
	// permPlatformsRead gates the declared deployment-surface / lifecycle reference
	// (ANT2-01/03) — read-only, non-sensitive reference data.
	permPlatformsRead auth.Permission = "models:platforms:read"
	// Module XXIII — own-model registry / versions / local inference / fine-tune
	// jobs. Reads are inventory; writes are governance changes (audited).
	permRegistryRead  auth.Permission = "models:registry:read"
	permRegistryWrite auth.Permission = "models:registry:write"
	// FIN-13 — per-provider GPAI compliance posture (operator-attested).
	permGPAIRead  auth.Permission = "models:gpai:read"
	permGPAIWrite auth.Permission = "models:gpai:write"
	// signed-model admission. Reads are the verdict inventory; admit is the
	// (editor) verification action; admin governs the trust root (deny-closed gate).
	permAdmissionRead  auth.Permission = "models:admission:read"
	permAdmissionWrite auth.Permission = "models:admission:write"
	permAdmissionAdmin auth.Permission = "models:admission:admin"
	// Claude model-access governance. model-groups are reference sets (read =
	// inventory, write = governance change); model-access GRANTS decide who may use what,
	// so authoring them is an ADMIN-tier change (verb "admin"), matching the actuation/
	// governance convention — a viewer/editor cannot widen who may spend against a model.
	permModelGroupRead   auth.Permission = "models:model-group:read"
	permModelGroupWrite  auth.Permission = "models:model-group:write"
	permModelAccessRead  auth.Permission = "models:model-access:read"
	permModelAccessAdmin auth.Permission = "models:model-access:admin"
)

// APINamespace returns the module's namespace; it roots routes at /v1/m/models/.
func (m *Module) APINamespace() string { return Namespace }

// Permissions declares the permissions the module's routes require.
func (m *Module) Permissions() []auth.Permission {
	return []auth.Permission{
		permCatalogRead, permRoutingRead, permRoutingWrite, permRoutingExecute, permRatelimitsRead,
		permPlatformsRead,
		permKeysRead, permKeysWrite,
		permRegistryRead, permRegistryWrite, permGPAIRead, permGPAIWrite,
		permAdmissionRead, permAdmissionWrite, permAdmissionAdmin,
		permModelGroupRead, permModelGroupWrite, permModelAccessRead, permModelAccessAdmin,
	}
}

// APIRoutes mounts the module's routes. The engine wraps each with authentication,
// tenant resolution and the declared permission check before the handler runs.
func (m *Module) APIRoutes(reg api.RouteRegistrar) {
	// Catalog & capability surface (declared reference + governed estate).
	reg.Handle("GET", "/catalog", permCatalogRead, m.handleCatalog)
	reg.Handle("GET", "/features", permCatalogRead, m.handleFeatures)
	reg.Handle("GET", "/data-governance", permCatalogRead, m.handleDataGovernance)
	reg.Handle("GET", "/tool-types", permCatalogRead, m.handleToolTypes)
	reg.Handle("GET", "/models", permCatalogRead, m.handleListModels)
	reg.Handle("GET", "/models/{id}", permCatalogRead, m.handleGetModel)

	// Routing/selection/fallback/version policy.
	reg.Handle("GET", "/routing-policies", permRoutingRead, m.handleListRouting)
	reg.Handle("POST", "/routing-policies", permRoutingWrite, m.handleCreateRouting)
	reg.Handle("GET", "/routing-policies/{id}", permRoutingRead, m.handleGetRouting)
	reg.Handle("PUT", "/routing-policies/{id}", permRoutingWrite, m.handleUpdateRouting)
	reg.Handle("DELETE", "/routing-policies/{id}", permRoutingWrite, m.handleDeleteRouting)
	reg.Handle("POST", "/routing-policies/{id}/resolve", permRoutingRead, m.handleResolveRouting)
	// Governed routing EXECUTION (resolve → act through the deny-closed Executor →
	// emit CostSample). Admin-tier spend, distinct from the read-tier resolve.
	reg.Handle("POST", "/routing-policies/{id}/execute", permRoutingExecute, m.handleExecuteRouting)

	// Read-only Anthropic Rate Limits inventory (ANT2-05) a gateway/proxy must mirror.
	reg.Handle("GET", "/rate-limits", permRatelimitsRead, m.handleRateLimits)

	// Declared deployment-surface matrix + per-platform model lifecycle reference
	// (ANT2-01/03) — the data the platforms view renders, served from the
	// engine instead of static web files. Degrades to available=false when unwired.
	reg.Handle("GET", "/platforms", permPlatformsRead, m.handlePlatforms)

	// API-key / workspace governance references (minimal-data, never secrets).
	reg.Handle("GET", "/keys", permKeysRead, m.handleListKeys)
	reg.Handle("POST", "/keys", permKeysWrite, m.handleCreateKey)
	reg.Handle("PUT", "/keys/{id}", permKeysWrite, m.handleUpdateKey)
	reg.Handle("DELETE", "/keys/{id}", permKeysWrite, m.handleDeleteKey)

	// per-workspace inference-geo residency reference (PERMITTED side of the
	// compliance geo-drift scan). Workspace governance, so it rides the keys perms.
	reg.Handle("GET", "/workspace-residency", permKeysRead, m.handleListWorkspaceResidency)
	reg.Handle("PUT", "/workspace-residency", permKeysWrite, m.handleUpsertWorkspaceResidency)
	// operator-attested provider entitlement state for restricted access tiers
	// (e.g. Glasswing): routing governance, so it rides the routing read/write tier.
	reg.Handle("GET", "/access-tier-entitlements", permRoutingRead, m.handleListAccessTierEntitlements)
	reg.Handle("PUT", "/access-tier-entitlements", permRoutingWrite, m.handleUpsertAccessTierEntitlement)

	// Module XXIII — own-model registry, versions, local inference deployments,
	// fine-tune job records (govern/inventory, not train).
	reg.Handle("GET", "/owned-models", permRegistryRead, m.handleListOwnedModels)
	reg.Handle("POST", "/owned-models", permRegistryWrite, m.handleCreateOwnedModel)
	reg.Handle("GET", "/owned-models/{id}", permRegistryRead, m.handleGetOwnedModel)
	reg.Handle("PUT", "/owned-models/{id}", permRegistryWrite, m.handleUpdateOwnedModel)
	reg.Handle("DELETE", "/owned-models/{id}", permRegistryWrite, m.handleDeleteOwnedModel)
	reg.Handle("GET", "/model-versions", permRegistryRead, m.handleListVersions)
	reg.Handle("POST", "/model-versions", permRegistryWrite, m.handleCreateVersion)
	reg.Handle("DELETE", "/model-versions/{id}", permRegistryWrite, m.handleDeleteVersion)
	reg.Handle("GET", "/inference-deployments", permRegistryRead, m.handleListDeployments)
	reg.Handle("POST", "/inference-deployments", permRegistryWrite, m.handleCreateDeployment)
	reg.Handle("PUT", "/inference-deployments/{id}", permRegistryWrite, m.handleUpdateDeployment)
	reg.Handle("DELETE", "/inference-deployments/{id}", permRegistryWrite, m.handleDeleteDeployment)
	reg.Handle("GET", "/finetune-jobs", permRegistryRead, m.handleListJobs)
	reg.Handle("POST", "/finetune-jobs", permRegistryWrite, m.handleCreateJob)
	reg.Handle("GET", "/finetune-jobs/{id}", permRegistryRead, m.handleGetJob)
	reg.Handle("PUT", "/finetune-jobs/{id}", permRegistryWrite, m.handleUpdateJob)

	// FIN-13 — per-provider GPAI compliance posture (operator attestation,
	// claim vs verified). Probed as evidence for ISO 42001 A.10.3 / NIST.
	reg.Handle("GET", "/gpai-posture", permGPAIRead, m.handleListGPAIPosture)
	reg.Handle("PUT", "/gpai-posture", permGPAIWrite, m.handleAttestGPAIPosture)

	// signed-model admission (G15): the per-tenant trust root (admin), the
	// deny-closed verify-before-admit action (editor), and the verdict inventory.
	reg.Handle("GET", "/admission-policy", permAdmissionRead, m.handleGetAdmissionPolicy)
	reg.Handle("PUT", "/admission-policy", permAdmissionAdmin, m.handlePutAdmissionPolicy)
	reg.Handle("POST", "/model-versions/{id}/admit", permAdmissionWrite, m.handleAdmitVersion)
	reg.Handle("GET", "/model-admissions", permAdmissionRead, m.handleListAdmissions)

	// datasets (AIBOM lineage components) and the CycloneDX AIBOM itself
	// (generate read-only; seal anchors a content hash to the ledger as evidence).
	reg.Handle("GET", "/datasets", permRegistryRead, m.handleListDatasets)
	reg.Handle("POST", "/datasets", permRegistryWrite, m.handleCreateDataset)
	reg.Handle("DELETE", "/datasets/{id}", permRegistryWrite, m.handleDeleteDataset)
	reg.Handle("GET", "/owned-models/{id}/aibom", permRegistryRead, m.handleGenerateAIBOM)
	reg.Handle("POST", "/owned-models/{id}/aibom", permRegistryWrite, m.handleSealAIBOM)
	reg.Handle("GET", "/aiboms", permRegistryRead, m.handleListAIBOMs)

	// model card generated from the same governed inventory (?format=md for
	// Markdown); the AIBOM route additionally serves ?format=spdx (SPDX 3.0.1 AI
	// Profile JSON-LD). Both are read-only exports; the seal stays CycloneDX.
	reg.Handle("GET", "/owned-models/{id}/model-card", permRegistryRead, m.handleModelCard)

	// agent-artifact supply chain (CUR-7): the four governed artifact
	// classes (skill, mcpb_extension, mcp_app_template, agents_md) as registry
	// entries with provenance + posture verdict, and the dedicated
	// agent-supply-chain CycloneDX BOM (generate read-only; seal anchors to the
	// ledger as its OWN append-only kind, models.agent_aibom — a separate
	// evidence class from the model-lineage seals compliance counts).
	reg.Handle("GET", "/agent-artifacts", permRegistryRead, m.handleListAgentArtifacts)
	reg.Handle("POST", "/agent-artifacts", permRegistryWrite, m.handleCreateAgentArtifact)
	reg.Handle("DELETE", "/agent-artifacts/{id}", permRegistryWrite, m.handleDeleteAgentArtifact)
	reg.Handle("GET", "/agent-artifacts/aibom", permRegistryRead, m.handleGenerateAgentArtifactBOM)
	reg.Handle("POST", "/agent-artifacts/aibom", permRegistryWrite, m.handleSealAgentArtifactBOM)
	reg.Handle("GET", "/agent-artifacts/aiboms", permRegistryRead, m.handleListAgentArtifactBOMs)

	// Claude model-access governance (FASE X). model-groups are admin-defined
	// named sets (hybrid: explicit refs + catalog selectors); model-access grants decide
	// which subject (user/role/agent-group) may use which model/model-group in which
	// workspace on which surface — enforced deny-closed in the routing select/execute
	// chain (modelaccessgate.go). The console authors these over REST.
	reg.Handle("GET", "/model-groups", permModelGroupRead, m.handleListModelGroups)
	reg.Handle("POST", "/model-groups", permModelGroupWrite, m.handleCreateModelGroup)
	reg.Handle("GET", "/model-groups/{id}", permModelGroupRead, m.handleGetModelGroup)
	reg.Handle("PUT", "/model-groups/{id}", permModelGroupWrite, m.handleUpdateModelGroup)
	reg.Handle("DELETE", "/model-groups/{id}", permModelGroupWrite, m.handleDeleteModelGroup)
	reg.Handle("GET", "/model-access", permModelAccessRead, m.handleListModelAccess)
	reg.Handle("POST", "/model-access", permModelAccessAdmin, m.handleCreateModelAccess)
	reg.Handle("PUT", "/model-access/{id}", permModelAccessAdmin, m.handleUpdateModelAccess)
	reg.Handle("DELETE", "/model-access/{id}", permModelAccessAdmin, m.handleDeleteModelAccess)
}

// allCapabilities is the full ordered capability vocabulary the module governs
// (the Claude stack plus cross-vendor analogs).
var allCapabilities = []mp.Capability{
	mp.CapStreaming, mp.CapToolUse, mp.CapVision, mp.CapPDF, mp.CapStructuredOutputs,
	mp.CapPromptCaching, mp.CapBatch, mp.CapFiles, mp.CapExtendedThinking,
	mp.CapComputerUse, mp.CapMemoryTool, mp.CapContextManagement, mp.CapCitations,
}

// catalogResponse is the declared reference catalog: the capability/feature matrix
// and list pricing per model family. It is static governance reference data,
// distinct from the governed live estate (GET /models).
type catalogResponse struct {
	Models       []catalogModelDTO `json:"models"`
	Capabilities []string          `json:"capabilities"`
	PricingAsOf  string            `json:"pricing_as_of"`
	PricingNote  string            `json:"pricing_note"`
}

// handleCatalog returns the declared reference catalog.
func (m *Module) handleCatalog(w http.ResponseWriter, r *http.Request, _ api.ModuleContext) {
	out := catalogResponse{
		Capabilities: capStrings(allCapabilities),
		PricingAsOf:  referencePricingAsOf,
		PricingNote:  "declared list-price defaults; verify against each provider's pricing page and override per model",
	}
	// One row per FAMILY, not per table row. A family needs a second row when an
	// id form does not start with the alias prefix (claude-opus-4-20250514 does
	// not begin with "claude-opus-4-0"), and the DTO does not project Prefix — so
	// without this the catalog served two byte-identical objects the caller could
	// neither tell apart nor justify, and the console keyed React rows on family.
	seen := make(map[string]bool, len(referenceTable))
	for _, ref := range referenceTable {
		if seen[ref.Family] {
			continue
		}
		seen[ref.Family] = true
		out.Models = append(out.Models, toCatalogModelDTO(ref))
	}
	writeJSON(w, http.StatusOK, out)
}

// toolTypesResponse is the declared dated tool-type catalog (CLA-08): exact
// identifiers, execution surface, ZDR eligibility and the cost_type cross-walk.
type toolTypesResponse struct {
	ToolTypes []ToolType `json:"tool_types"`
	AsOf      string     `json:"as_of"`
	Note      string     `json:"note"`
}

// handleToolTypes returns the declared dated Anthropic tool-type catalog.
func (m *Module) handleToolTypes(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusOK, toolTypesResponse{
		ToolTypes: ClaudeToolTypes(),
		AsOf:      toolTypesAsOf,
		Note:      "declared dated tool-type identifiers; verify against the provider tool reference and override — identifiers change quarterly",
	})
}

// featureRow lists the families that declare one capability.
type featureRow struct {
	Capability string   `json:"capability"`
	Families   []string `json:"families"`
}

// handleFeatures returns the capability matrix: per API feature, which declared
// families support it.
func (m *Module) handleFeatures(w http.ResponseWriter, r *http.Request, _ api.ModuleContext) {
	rows := make([]featureRow, 0, len(allCapabilities))
	for _, c := range allCapabilities {
		row := featureRow{Capability: string(c), Families: []string{}}
		// Each family once per capability, whatever prefixes it needs (the two
		// dual-prefix families carry capsClaudeFull, so every one of the 13
		// capability rows listed them twice).
		inRow := make(map[string]bool, len(referenceTable))
		for _, ref := range referenceTable {
			if mp.Has(ref.Capabilities, c) && !inRow[ref.Family] {
				inRow[ref.Family] = true
				row.Families = append(row.Families, ref.Family)
			}
		}
		rows = append(rows, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"capabilities": rows})
}

// handleDataGovernance returns the Claude context-management / memory-tool +
// ZDR-eligibility matrix (CLA-15): for each feature, whether it can clear the
// model's working context server-side (a forensics implication), where its data
// persists, and whether it is ZDR-eligible (a data-residency concern). It turns
// the coarse context_management/memory_tool capability booleans into the depth a
// security/compliance audience needs.
func (m *Module) handleDataGovernance(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusOK, map[string]any{"features": ClaudeDataGovernance()})
}

// handleListModels lists the governed core models (the live estate, enriched).
func (m *Module) handleListModels(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	out := listResponse[governedModelDTO]{Items: []governedModelDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		// Resolve provider names once so every row carries the human ref without an
		// N-lookup; provider_id stays the uuid, provider holds the name.
		provs, err := listAllPages(func(q model.Query) ([]model.Provider, model.Page, error) {
			return sc.Providers().List(r.Context(), q)
		})
		if err != nil {
			return err
		}
		pid2name := make(map[string]string, len(provs))
		for _, p := range provs {
			pid2name[p.ID.String()] = p.Name
		}
		recs, page, err := sc.Models().List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, md := range recs {
			d := toGovernedModelDTO(md)
			d.Provider = pid2name[md.ProviderID.String()]
			out.Items = append(out.Items, d)
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGetModel returns one governed model with its provider reference.
func (m *Module) handleGetModel(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var out governedModelDTO
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		md, err := sc.Models().Get(r.Context(), id)
		if err != nil {
			return err
		}
		out = toGovernedModelDTO(md)
		if !md.ProviderID.IsZero() {
			if p, err := sc.Providers().Get(r.Context(), md.ProviderID); err == nil {
				out.Provider = p.Name
			}
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// --- routing policies --------------------------------------------------------

// routingPolicyDTO is a routing policy: a named, enabled governance policy whose
// spec drives connectors/modelrouter selection.
type routingPolicyDTO struct {
	ID      string `json:"id,omitempty"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	routingSpec
}

func toRoutingPolicyDTO(p model.Policy) routingPolicyDTO {
	return routingPolicyDTO{
		ID: p.ID.String(), Name: p.Name, Enabled: p.Enabled,
		routingSpec: parseRoutingSpec(p.Spec),
	}
}

// handleListRouting lists routing policies.
func (m *Module) handleListRouting(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	q.Filters = append(q.Filters, eq("kind", policyKindRouting))
	out := listResponse[routingPolicyDTO]{Items: []routingPolicyDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		recs, page, err := sc.Policies().List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, p := range recs {
			out.Items = append(out.Items, toRoutingPolicyDTO(p))
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGetRouting returns one routing policy.
func (m *Module) handleGetRouting(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var out routingPolicyDTO
	found := false
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		p, err := sc.Policies().Get(r.Context(), id)
		if err != nil {
			return err
		}
		if p.Kind != policyKindRouting {
			return nil
		}
		found, out = true, toRoutingPolicyDTO(p)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleCreateRouting creates a routing policy.
func (m *Module) handleCreateRouting(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in routingPolicyDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Name == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("name is required"))
		return
	}
	in.normalize()
	var out routingPolicyDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		p, err := sc.Policies().Create(r.Context(), model.Policy{
			Name: in.Name, Kind: policyKindRouting, Enabled: in.Enabled,
			Spec: in.toSpecMap(),
		})
		if err != nil {
			return err
		}
		out = toRoutingPolicyDTO(p)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// handleUpdateRouting updates a routing policy in place.
func (m *Module) handleUpdateRouting(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var in routingPolicyDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Name == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("name is required"))
		return
	}
	in.normalize()
	var out routingPolicyDTO
	notRouting := false
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		p, err := sc.Policies().Get(r.Context(), id)
		if err != nil {
			return err
		}
		if p.Kind != policyKindRouting {
			notRouting = true
			return nil
		}
		p.Name = in.Name
		p.Enabled = in.Enabled
		p.Spec = in.toSpecMap()
		p, err = sc.Policies().Update(r.Context(), p)
		if err != nil {
			return err
		}
		out = toRoutingPolicyDTO(p)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if notRouting {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDeleteRouting deletes a routing policy.
func (m *Module) handleDeleteRouting(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	notRouting := false
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		p, err := sc.Policies().Get(r.Context(), id)
		if err != nil {
			return err
		}
		if p.Kind != policyKindRouting {
			notRouting = true
			return nil
		}
		return sc.Policies().Delete(r.Context(), id)
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if notRouting {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// targetDTO is one routable destination in a routing decision.
type targetDTO struct {
	ProviderRef string `json:"provider_ref"`
	ModelRef    string `json:"model_ref"`
	ViaGateway  bool   `json:"via_gateway,omitempty"`
	Endpoint    string `json:"endpoint,omitempty"`
}

func toTargetDTO(t modelrouter.Target) targetDTO {
	return targetDTO{ProviderRef: t.ProviderRef, ModelRef: t.ModelRef, ViaGateway: t.ViaGateway, Endpoint: t.Endpoint}
}

// fromTargetDTO rebuilds the router target from its DTO so the execute path runs
// the (possibly governance-filtered) decision chain rather than the
// pre-filter router chain.
func fromTargetDTO(t targetDTO) modelrouter.Target {
	return modelrouter.Target{ProviderRef: t.ProviderRef, ModelRef: t.ModelRef, ViaGateway: t.ViaGateway, Endpoint: t.Endpoint}
}

// decisionDTO is the routing decision: the primary + fallback chain to try, or
// resolved=false with a reason when no governed model satisfies the policy, when an
// enforcing budget (FIN-08) caps the selected model (budget_action set), or when the
// model-governance policy denies every candidate (governance_deny set).
type decisionDTO struct {
	Resolved  bool        `json:"resolved"`
	Policy    string      `json:"policy,omitempty"`
	Primary   *targetDTO  `json:"primary,omitempty"`
	Fallbacks []targetDTO `json:"fallbacks"`
	Chain     []targetDTO `json:"chain"`
	Reason    string      `json:"reason"`
	// BudgetAction is set ("throttle"|"block") when the resolve was denied by an
	// enforcing budget at its cap (FIN-08), so a caller distinguishes a budget cap from
	// "no governed model satisfies the policy".
	BudgetAction string `json:"budget_action,omitempty"`
	// GovernanceDeny is set ("retired"|"deprecated"|"zdr"|"access_tier"|
	// "entitlement") when the resolve was denied by the model-governance policy
	//, so a caller distinguishes a governance deny from a budget cap or
	// a no-candidate result.
	GovernanceDeny string `json:"governance_deny,omitempty"`
	// Replacement is the published successor model ref when a lifecycle deny names
	// one — non-sensitive and actionable (migrate to it); empty otherwise.
	Replacement string `json:"replacement,omitempty"`
}

// handleResolveRouting resolves a stored routing policy against the governed
// estate and returns the routing decision. It is read-only: it computes a
// selection the gateway/connector then executes; it performs no inference.
func (m *Module) handleResolveRouting(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var (
		out            decisionDTO
		spec           routingSpec
		suspendedTiers []string
		notRouting     bool
	)
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		p, err := sc.Policies().Get(r.Context(), id)
		if err != nil {
			return err
		}
		if p.Kind != policyKindRouting {
			notRouting = true
			return nil
		}
		spec = parseRoutingSpec(p.Spec)
		cat, err := buildCatalog(r.Context(), sc)
		if err != nil {
			return err
		}
		dec, derr := spec.resolve(r.Context(), cat)
		if errors.Is(derr, modelrouter.ErrNoCandidate) {
			out = unresolvedDecisionDTO(spec.Strategy)
			return nil
		}
		if derr != nil {
			return derr
		}
		out = toDecisionDTO(dec)
		suspendedTiers, err = suspendedEntitlementTiers(r.Context(), sc)
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if notRouting {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	if out.Resolved && out.Primary != nil {
		// Model-governance gate (lifecycle/ZDR/access-tier), DENY-CLOSED. Runs
		// BEFORE the budget gate so the (possibly promoted) surviving primary is what
		// the budget check scopes.
		if status, denied := m.governanceDeniesRoute(spec, &out, suspendedTiers); denied {
			writeJSON(w, status, out)
			return
		}
		// Model-access PREVIEW, DENY-CLOSED for the decidable dimensions.
		// /resolve has no acting session, so this applies ONLY what is decidable without
		// one — a tenant-wide forbid on the principal's user/role identity, and the
		// user/role allow-list confinement — and DEFERS workspace/agent-group/surface to
		// the authoritative execute decision. A model denied under every possible
		// session is dropped from the preview; nothing here grants use. Runs before the
		// (fail-open) budget gate so a governance deny is never masked by a FinOps outage.
		if status, denied := m.modelAccessPreviewDeniesRoute(r, mc, &out); denied {
			writeJSON(w, status, out)
			return
		}
		// FIN-08 budget pre-flight: deny the resolution when an enforcing budget that
		// scopes the selected model is at its cap (Denial-of-Wallet). Runs OUTSIDE the
		// read txn above (the gate opens its own view) and ONLY for a resolved decision
		// — a no-candidate result has no model to scope and nothing to spend. Fails
		// OPEN (unlike the governance gate — finops.CheckBudget's documented contract).
		// /resolve is a preview with no acting session, so no session_ref to scope an
		// identity budget on (provider/model-scoped budgets still apply).
		if status, denied := m.budgetDeniesRoute(r, mc, &out, ""); denied {
			writeJSON(w, status, out)
			return
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// budgetDeniesRoute consults the FinOps budget gate for a resolved routing decision
// (FIN-08). When an enforcing budget that scopes the selected primary is at its cap it
// MUTATES the decision to resolved=false (the gateway then gets no target → no spend),
// records the budget action + a money-free reason, and returns the HTTP status to use
// (402 block / 429 throttle) with denied=true. It FAILS OPEN: a budget-gate error
// leaves the decision intact (the finops_budget_cap finding is the backstop), per
// finops.CheckBudget's contract. The minimal dims (provider+model refs) come from the
// resolved primary; global/provider/model enforcing budgets are what the router can cap
// pre-execution (docs/SECURITY-HARDENING.md).
func (m *Module) budgetDeniesRoute(r *http.Request, mc api.ModuleContext, dec *decisionDTO, sessionRef string) (int, bool) {
	verdict, err := m.budgetGate.Check(r.Context(), mc.Tenant, BudgetDims{
		ProviderRef: dec.Primary.ProviderRef, ModelRef: dec.Primary.ModelRef, SessionRef: sessionRef,
	})
	if err != nil {
		if m.log != nil {
			m.log.Error("models: budget gate error; failing open (routing resolve proceeds)", "err", err)
		}
		return 0, false
	}
	if verdict.Allowed {
		return 0, false
	}
	status := http.StatusPaymentRequired
	if verdict.Action == budgetActionThrottle {
		status = http.StatusTooManyRequests
	}
	if m.log != nil {
		m.log.Info("models: routing resolve denied by budget cap", "budget_ref", verdict.BudgetRef, "action", verdict.Action)
	}
	// Hand back NO usable target on a denial: resolved=false with the budget action.
	// The reason is a GENERIC, money- AND name-free string: /resolve is read-tier
	// (permRoutingRead — a viewer can call it), so unlike the admin-tier fire/open seams
	// it must not disclose the operator's budget name (verdict.Reason embeds it). The
	// budget_action distinguishes a cap from a no-candidate result; the budget id is
	// recorded operator-side (the log above + the finops_budget_cap finding), never here.
	*dec = decisionDTO{
		Resolved: false, Policy: dec.Policy, BudgetAction: verdict.Action,
		Reason:    "routing denied: an enforcing budget is at its cap",
		Fallbacks: []targetDTO{}, Chain: []targetDTO{},
	}
	return status, true
}

// governanceDeniesRoute applies the model-governance policy (lifecycle /
// ZDR / access-tier, lifecycle.go governanceDeny) to a resolved routing decision.
// It sits next to the budget gate but is DENY-CLOSED — only the budget seam is
// documented fail-open. It first drops impermissible candidates from the resolved
// chain and PROMOTES the first permissible one, mirroring how AllowDeprecated
// filters candidates: a governance deny must not fail the request while a
// permissible candidate exists (the chain is policy-ordered, so the first
// survivor is exactly what resolving over a pre-filtered catalog would pick).
// Only when EVERY candidate is denied does it mutate the decision to
// resolved=false (no usable target) with the primary's deny kind, a generic
// money-free reason and — for a lifecycle deny — the published replacement ref
// (non-sensitive, actionable), returning 403. Evaluation is against the module
// clock (time.Now, UTC calendar day), matching the rest of the module.
func (m *Module) governanceDeniesRoute(spec routingSpec, dec *decisionDTO, suspendedTiers []string) (int, bool) {
	now := time.Now().UTC()
	kept := make([]targetDTO, 0, len(dec.Chain))
	for _, t := range dec.Chain {
		if _, _, _, denied := spec.governanceDeny(t.ModelRef, now, suspendedTiers); !denied {
			kept = append(kept, t)
		}
	}
	if len(kept) == len(dec.Chain) {
		return 0, false // nothing impermissible in the chain
	}
	if len(kept) > 0 {
		primary := kept[0]
		note := fmt.Sprintf("model governance filtered %d candidate(s)", len(dec.Chain)-len(kept))
		dec.Primary, dec.Fallbacks, dec.Chain = &primary, kept[1:], kept
		if dec.Reason == "" {
			dec.Reason = note
		} else {
			dec.Reason += "; " + note
		}
		return 0, false
	}
	kind, reason, replacement, _ := spec.governanceDeny(dec.Primary.ModelRef, now, suspendedTiers)
	if m.log != nil {
		m.log.Info("models: routing resolve denied by model governance", "deny", kind, "model_ref", dec.Primary.ModelRef)
	}
	*dec = decisionDTO{
		Resolved: false, Policy: dec.Policy, GovernanceDeny: kind, Replacement: replacement,
		Reason:    reason,
		Fallbacks: []targetDTO{}, Chain: []targetDTO{},
	}
	return http.StatusForbidden, true
}
