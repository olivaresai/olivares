// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import (
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// keyRefKind is the module's owned entity: a governance REFERENCE to a provider
// API key or workspace/project — which agent/team uses which credential — never
// the credential value itself (docs/SECURITY-HARDENING.md).
const keyRefKind model.Kind = "models.key_ref"

// keyRefTable is its physical table.
const keyRefTable = "models_key_ref"

// key-ref reference kinds.
const (
	keyRefAPIKey    = "api_key"
	keyRefWorkspace = "workspace"
)

// key-ref columns.
const (
	colProviderRef  = "provider_ref"
	colRefKind      = "ref_kind"
	colExtID        = "ext_id"
	colKeyName      = "name"
	colWorkspaceRef = "workspace_ref"
	colKeyStatus    = "status"
	colHint         = "hint"
	colOwnerRef     = "owner_ref"
	colCreatedAt    = "ref_created_at"
)

// RegisterSchema declares the module's owned entities. Routing policies and
// budgets reuse the core Policy entity (Kind="routing"); the only thing core does
// not model is the API-key/workspace governance reference, registered here.
//
// The key-ref table is MINIMAL-DATA by construction: it has no column that could
// hold a usable credential — only a masked Hint (e.g. "sk-…aB12") the provider
// Admin API returns, which is safe to display (docs/SECURITY-HARDENING.md).
//
// Governing which credential an agent/team may use IS a security-sensitive change
// that belongs in the append-only ledger (docs/SECURITY-HARDENING.md) — but the descriptor's
// auto-audit attributes every mutation to the SYSTEM actor (generic.go maybeAudit),
// which would defeat the self-audit purpose ("who changed which credential"). So
// the table is NOT descriptor-audited; instead each key handler appends a semantic
// audit event attributed to the real principal in the same transaction (keys.go),
// exactly as the core entity handlers do (handlers_core.go appendAudit).
func (m *Module) RegisterSchema(reg store.ExtensionRegistry) error {
	if err := reg.Register(model.EntityDescriptor{
		Kind:  keyRefKind,
		Table: keyRefTable,
		Fields: []model.FieldSpec{
			{Name: colProviderRef, Kind: model.KindText, Indexed: true},
			{Name: colRefKind, Kind: model.KindText, Indexed: true},
			{Name: colExtID, Kind: model.KindText},
			{Name: colKeyName, Kind: model.KindText},
			{Name: colWorkspaceRef, Kind: model.KindText, Nullable: true},
			{Name: colKeyStatus, Kind: model.KindText, Indexed: true},
			{Name: colHint, Kind: model.KindText, Nullable: true},
			{Name: colOwnerRef, Kind: model.KindText, Nullable: true},
			{Name: colCreatedAt, Kind: model.KindTimestamp, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			// One governance reference per (kind, provider, external id). The unique
			// index leads with tenant_id so it cannot couple tenants.
			Name:    "models_key_ref_uniq",
			Columns: []string{model.ColTenantID, colRefKind, colProviderRef, colExtID},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}
	// Module XXIII — own-model registry / versions / local inference / fine-tune
	// jobs (owned.go); and FIN-13 — per-provider GPAI compliance posture (gpai.go).
	if err := registerOwnedSchemas(reg); err != nil {
		return err
	}
	if err := registerGPAISchema(reg); err != nil {
		return err
	}
	// signed-model admission (trust root + verdict, admission.go), datasets
	// for AIBOM lineage (dataset.go) and the sealed CycloneDX AIBOM record (aibom.go).
	if err := registerAdmissionSchemas(reg); err != nil {
		return err
	}
	if err := registerDatasetSchema(reg); err != nil {
		return err
	}
	if err := registerAIBOMSchema(reg); err != nil {
		return err
	}
	// agent-artifact supply chain: the four governed artifact classes
	// (skill/.mcpb/ui-template/AGENTS.md) registered for the agent-supply-chain
	// BOM (agentartifacts.go).
	if err := registerAgentArtifactSchema(reg); err != nil {
		return err
	}
	// per-workspace inference-geo residency reference (residency.go), the
	// PERMITTED side modules/compliance probes for the geo-drift scan.
	if err := registerWorkspaceResidencySchema(reg); err != nil {
		return err
	}
	// provider-entitlement attestation for restricted access tiers (e.g.
	// Glasswing), enforced by routing governance when a provider-side entitlement is
	// operator-attested as suspended.
	if err := registerAccessTierEntitlementSchema(reg); err != nil {
		return err
	}
	// Claude model-access governance (FASE X): model-groups + model-access
	// grants (who/group/agent-group may use which model/model-group in which workspace
	// on which surface), enforced deny-closed in the routing select/execute chain.
	return registerModelGovernanceSchema(reg)
}
