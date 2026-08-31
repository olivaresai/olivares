// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package deploy

import (
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Owned entity kinds and their physical tables. Table names stay within the
// 40-char module-table cap: the longest, deploy_operation, is 16 chars.
//
// The canonical APPLIED-state snapshot is the CORE Deployment entity
// (sc.Deployments()), which the Terraform provider and already read.
// These four namespaced entities add the declarative control plane on top of it:
//
//   - definition: the desired-state record (mutable pointer to the current
//     revision + the applied version actually reconciled to infra).
//   - revision:   the APPEND-ONLY, immutable spec history that makes rollback a
//     re-declaration of a prior known-good spec (reversible by design).
//   - wiring:     the declared PERMITTED connectivity agent→resource — the
//     contract (permitted-vs-observed) and (change evidence) consume.
//   - operation:  the APPEND-ONLY lifecycle/governance/result log binding each
//     plan→apply→verify→retire→rollback to its approval and outcome.
const (
	definitionKind  model.Kind = "deploy.definition"
	definitionTable            = "deploy_definition"
	revisionKind    model.Kind = "deploy.revision"
	revisionTable              = "deploy_revision"
	wiringKind      model.Kind = "deploy.wiring"
	wiringTable                = "deploy_wiring"
	operationKind   model.Kind = "deploy.operation"
	operationTable             = "deploy_operation"
)

// definition columns — the desired-state record (mutable lifecycle).
const (
	colSubjectKind   = "subject_kind"    // "agent" | "mcp_server"
	colSubjectRef    = "subject_ref"     // logical name / external id of the subject
	colDefName       = "name"            // logical deployment name (unique per environment)
	colEnvironment   = "environment"     // "prod" | "staging" | ...
	colTarget        = "target"          // Inventory target ref, e.g. "docker.host/<host>", "k8s.namespace/<ns>"
	colRuntime       = "runtime"         // executor kind: "docker" | "k8s" | ...
	colDesiredStatus = "desired_status"  // "active" | "retired"
	colCurrentVer    = "current_version" // version of the latest declared revision (desired)
	colAppliedVer    = "applied_version" // version actually reconciled to infra (real); 0 = never applied
	colSpecHash      = "spec_hash"       // hex SHA-256 of the current desired spec (no secrets)
	colSourceRef     = "source_ref"      // GitOps source (redacted), e.g. "git:<repo>#<commit>"
	colDeploymentID  = "deployment_id"   // link to the canonical core Deployment snapshot (real/applied state)
)

// revision columns — the append-only immutable spec history.
const (
	colDefinitionRef = "definition_ref" // owning definition id
	colRevNum        = "rev_num"        // monotone revision number ("version" is a reserved base column)
	colSpec          = "spec"           // the typed, re-serialized desired spec (JSON); no secrets, refs only
	colNote          = "note"           // bounded operator prose
	colCreatedByCol  = "created_by"     // audit-actor string (provenance only)
)

// wiring columns — the declared PERMITTED connectivity (the contract).
const (
	colAgentRef     = "agent_ref"     // origin: the agent external id / identity ref it runs as
	colIdentityRef  = "identity_ref"  // the NHI identity ref bound (empty when attribution degraded)
	colResourceKind = "resource_kind" // class of resource, e.g. "postgres.table", "r2.bucket", "http.api"
	colResourceRef  = "resource_ref"  // redacted natural ref of the resource
	colMode         = "mode"          // "read" | "readwrite"
	colSecretRef    = "secret_ref"    // reference to a secret-store entry — NEVER a cleartext secret
	colWiringStatus = "wiring_status" // "declared" | "applied" | "revoked"
	colAttribution  = "attribution"   // "firm" (identity bound by the binder) | "degraded" (binder unavailable)
)

// operation columns — the append-only governance/result ledger.
const (
	colOp          = "op"           // "plan" | "apply" | "verify" | "retire" | "rollback"
	colFromVersion = "from_version" // applied version before the op
	colToVersion   = "to_version"   // target version of the op
	colPlanHash    = "plan_hash"    // hash of the transition the op is bound to (anti-TOCTOU)
	colApprovalRef = "approval_ref" // the governance approval id (when gated)
	colGateStatus  = "gate_status"  // effective decision consumed: approved/pending/expired/no_gate/not_required
	colOpStatus    = "op_status"    // "planned" | "blocked" | "applied" | "verified" | "failed" | "rolled_back"
	colActor       = "actor"        // audit-actor string (provenance)
	colResult      = "result"       // short, non-sensitive outcome summary
	colOccurredAt  = "occurred_at"  // when the op ran
)

// RegisterSchema declares the module's four owned entities. It satisfies the
// engine-side runtime.SchemaProvider seam (structural — no runtime import) and is
// called once, at store construction, before any Scope exists (S02 §7 /):
// the engine creates the tables, injects the base columns and attaches the
// tenant, audit and append-only guards. A module cannot opt out of isolation.
//
// Minimal data (docs/SECURITY-HARDENING.md): no column can hold a usable credential. A spec
// carries image/command/resource refs and SECRET REFERENCES only — validated by
// the typed-spec guard (lifecycle.go) before it is ever stored; secret_ref on a
// wiring is a secret-store reference, never the secret. The revision and
// operation tables are APPEND-ONLY so the version history and the change-of-infra
// evidence cannot be silently rewritten (docs/SECURITY-HARDENING.md).
//
// None of the four is descriptor-Audited: the privileged mutations each append a
// SEMANTIC self-audit attributed to the real principal in their own transaction
// (helpers.go auditEvent) — the who/what/version/approval the per-row engine
// audit could not attribute.
func (m *Module) RegisterSchema(reg store.ExtensionRegistry) error {
	if err := reg.Register(model.EntityDescriptor{
		Kind:  definitionKind,
		Table: definitionTable,
		Fields: []model.FieldSpec{
			{Name: colSubjectKind, Kind: model.KindText, Indexed: true},
			{Name: colSubjectRef, Kind: model.KindText, Indexed: true},
			{Name: colDefName, Kind: model.KindText, Indexed: true},
			{Name: colEnvironment, Kind: model.KindText, Indexed: true},
			{Name: colTarget, Kind: model.KindText},
			{Name: colRuntime, Kind: model.KindText},
			{Name: colDesiredStatus, Kind: model.KindText, Indexed: true},
			{Name: colCurrentVer, Kind: model.KindInt},
			{Name: colAppliedVer, Kind: model.KindInt},
			{Name: colSpecHash, Kind: model.KindText, Nullable: true},
			{Name: colSourceRef, Kind: model.KindText, Nullable: true},
			{Name: colDeploymentID, Kind: model.KindUUID, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			// One definition per (name, environment). The unique index leads with
			// tenant_id so it cannot couple tenants or leak existence.
			Name:    "deploy_definition_uniq",
			Columns: []string{model.ColTenantID, colDefName, colEnvironment},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	if err := reg.Register(model.EntityDescriptor{
		Kind:       revisionKind,
		Table:      revisionTable,
		AppendOnly: true, // immutable spec history — the reversible source of truth (docs/SECURITY-HARDENING.md)
		Fields: []model.FieldSpec{
			{Name: colDefinitionRef, Kind: model.KindUUID, Indexed: true},
			{Name: colRevNum, Kind: model.KindInt, Indexed: true},
			{Name: colSpec, Kind: model.KindJSON},
			{Name: colSpecHash, Kind: model.KindText},
			{Name: colSourceRef, Kind: model.KindText, Nullable: true},
			{Name: colNote, Kind: model.KindText, Nullable: true},
			{Name: colCreatedByCol, Kind: model.KindText},
		},
		Indexes: []model.IndexSpec{{
			// One revision row per (definition, version): monotone, gap-free history.
			Name:    "deploy_revision_uniq",
			Columns: []string{model.ColTenantID, colDefinitionRef, colRevNum},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	if err := reg.Register(model.EntityDescriptor{
		Kind:  wiringKind,
		Table: wiringTable,
		Fields: []model.FieldSpec{
			{Name: colDefinitionRef, Kind: model.KindUUID, Indexed: true},
			{Name: colAgentRef, Kind: model.KindText, Indexed: true},
			{Name: colIdentityRef, Kind: model.KindText, Nullable: true},
			{Name: colResourceKind, Kind: model.KindText, Indexed: true},
			{Name: colResourceRef, Kind: model.KindText, Indexed: true},
			{Name: colMode, Kind: model.KindText},
			{Name: colSecretRef, Kind: model.KindText, Nullable: true},
			{Name: colWiringStatus, Kind: model.KindText, Indexed: true},
			{Name: colAttribution, Kind: model.KindText},
			{Name: colRevNum, Kind: model.KindInt},
		},
		Indexes: []model.IndexSpec{{
			// One declared edge per (definition, agent, resource, mode): re-applying
			// the same spec upserts in place rather than duplicating the wiring.
			Name:    "deploy_wiring_uniq",
			Columns: []string{model.ColTenantID, colDefinitionRef, colAgentRef, colResourceRef, colMode},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	return reg.Register(model.EntityDescriptor{
		Kind:       operationKind,
		Table:      operationTable,
		AppendOnly: true, // immutable change-management evidence (docs/SECURITY-HARDENING.md consumes it)
		Fields: []model.FieldSpec{
			{Name: colDefinitionRef, Kind: model.KindUUID, Indexed: true},
			{Name: colOp, Kind: model.KindText, Indexed: true},
			{Name: colFromVersion, Kind: model.KindInt},
			{Name: colToVersion, Kind: model.KindInt},
			{Name: colPlanHash, Kind: model.KindText, Nullable: true, Indexed: true},
			{Name: colApprovalRef, Kind: model.KindText, Nullable: true},
			{Name: colGateStatus, Kind: model.KindText},
			{Name: colOpStatus, Kind: model.KindText, Indexed: true},
			{Name: colActor, Kind: model.KindText},
			{Name: colResult, Kind: model.KindText, Nullable: true},
			{Name: colOccurredAt, Kind: model.KindTimestamp, Indexed: true},
		},
	})
}
