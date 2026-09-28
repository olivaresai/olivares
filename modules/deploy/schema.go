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

// Principal declarations shared by the descriptors below: what a stored column
// says about principals, with the reader lines that show it.
var (
	// The owning definition row id of a revision, wiring or operation row.
	pdeclNoneDefinitionRef = model.None("the owning deploy.definition row id, written from that row's own id: definitions.go:576, wiring.go:103, lifecycle.go:680")
	// The deployed agent or MCP server, never an account.
	pdeclNoneSubjectRef = model.None("the deployed agent or MCP server reference (subject_kind is refused unless agent or mcp_server: definitions.go:101), resolved against the agent roster by external id or name (cmd/olivares/deployidentity.go:189) and used as the kill-switch agent dimension (lifecycle.go:603); wiring.go:128 copies it into agent_ref")
	// A content fingerprint of the canonical desired spec.
	pdeclNoneSpecHash = model.None("hex SHA-256 of the canonical desired spec: spec.go:161, helpers.go:203")
	// A source-control reference, refused when it carries a credential.
	pdeclNoneSourceRef = model.None("a GitOps source reference, refused when it looks like a credential and otherwise only rendered: definitions.go:114, definitions.go:66, definitions.go:467")
	// A pointer into a secret store, never a secret and never a principal.
	pdeclNoneSecretRef = model.None("a secret-store reference <scheme>:<locator> from a closed scheme allow-list: helpers.go:260, helpers.go:284")
	// The enterprise resource a wiring connects to.
	pdeclNoneResource = model.None("the kind or natural reference of the enterprise resource a wiring reaches, published only as the edge resource: spec.go:50, wiring.go:144")
	// The permitted access mode of a wiring.
	pdeclNoneMode = model.None("an access mode from the closed set read|write|readwrite: spec.go:138")
	// A compute request key or value of the desired spec.
	pdeclNoneSpecResources = model.None("a compute request key or value such as cpu or mem, refused when it looks like a credential: spec.go:87")
	// The typed desired spec: decoded into deploySpec and re-serialized from it.
	pdeclRevisionSpec = model.Nested(deploySpec{}, model.ClassEvidence,
		model.Leaf("image", model.None("a container image or artifact reference, refused when it looks like a credential and handed to the executor: spec.go:77, lifecycle.go:116")),
		model.Leaf("command", model.None("an entrypoint override, refused when it looks like a credential and handed to the executor: spec.go:77, lifecycle.go:116")),
		model.Leaf("resources{key}", pdeclNoneSpecResources),
		model.Leaf("resources{}", pdeclNoneSpecResources),
		model.Leaf("env_refs[].name", model.None("an environment variable name, refused when it looks like a credential: spec.go:98")),
		model.Leaf("env_refs[].secret_ref", pdeclNoneSecretRef),
		model.Leaf("wirings[].resource_kind", pdeclNoneResource),
		model.Leaf("wirings[].resource_ref", pdeclNoneResource),
		model.Leaf("wirings[].mode", pdeclNoneMode),
		model.Leaf("wirings[].secret_ref", pdeclNoneSecretRef),
		model.Leaf("identity.identity_ref", model.None("the directory reference of the non-human identity the agent runs as, handed to the binder (wiring.go:76), whose governance endpoint refuses to bind a human identity: modules/governance/identity.go:188")),
	)
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
			{Name: colSubjectKind, Kind: model.KindText, Indexed: true, Principal: model.None("closed set agent|mcp_server: definitions.go:101")},
			{Name: colSubjectRef, Kind: model.KindText, Indexed: true, Principal: pdeclNoneSubjectRef},
			{Name: colDefName, Kind: model.KindText, Indexed: true, Principal: model.None("the logical deployment name, rendered and used as the approval subject label: definitions.go:63, lifecycle.go:122")},
			{Name: colEnvironment, Kind: model.KindText, Indexed: true, Principal: model.None("an environment label handed to the executor and used as a list filter: lifecycle.go:115, definitions.go:265")},
			{Name: colTarget, Kind: model.KindText, Principal: model.None("an infrastructure target reference handed to the executor, refused when it looks like a credential: lifecycle.go:114, definitions.go:114")},
			{Name: colRuntime, Kind: model.KindText, Principal: model.None("the executor runtime kind handed to the executor: lifecycle.go:114")},
			{Name: colDesiredStatus, Kind: model.KindText, Indexed: true, Principal: model.None("closed set active|retired: definitions.go:28, definitions.go:29")},
			{Name: colCurrentVer, Kind: model.KindInt},
			{Name: colAppliedVer, Kind: model.KindInt},
			{Name: colSpecHash, Kind: model.KindText, Nullable: true, Principal: pdeclNoneSpecHash},
			{Name: colSourceRef, Kind: model.KindText, Nullable: true, Principal: pdeclNoneSourceRef},
			{Name: colDeploymentID, Kind: model.KindUUID, Nullable: true, Principal: model.None("the linked core Deployment row id, read back only through the deployment repository: lifecycle.go:651, definitions.go:325")},
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
			{Name: colDefinitionRef, Kind: model.KindUUID, Indexed: true, Principal: pdeclNoneDefinitionRef},
			{Name: colRevNum, Kind: model.KindInt, Indexed: true},
			{Name: colSpec, Kind: model.KindJSON, Principal: pdeclRevisionSpec},
			{Name: colSpecHash, Kind: model.KindText, Principal: pdeclNoneSpecHash},
			{Name: colSourceRef, Kind: model.KindText, Nullable: true, Principal: pdeclNoneSourceRef},
			{Name: colNote, Kind: model.KindText, Nullable: true, Principal: model.None("a bounded operator note, only rendered: definitions.go:181, definitions.go:468")},
			{Name: colCreatedByCol, Kind: model.KindText, Principal: model.Ref(model.EncodeUserRef, model.ClassEvidence)},
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
			{Name: colDefinitionRef, Kind: model.KindUUID, Indexed: true, Principal: pdeclNoneDefinitionRef},
			{Name: colAgentRef, Kind: model.KindText, Indexed: true, Principal: pdeclNoneSubjectRef},
			{Name: colIdentityRef, Kind: model.KindText, Nullable: true, Principal: model.None("the bound identity's external id (modules/governance/identity.go:132), which the governance endpoint refuses for a human identity (modules/governance/identity.go:188), else the declared identity or agent subject ref (wiring.go:74, wiring.go:79); only rendered: wiring.go:223")},
			{Name: colResourceKind, Kind: model.KindText, Indexed: true, Principal: pdeclNoneResource},
			{Name: colResourceRef, Kind: model.KindText, Indexed: true, Principal: pdeclNoneResource},
			{Name: colMode, Kind: model.KindText, Principal: pdeclNoneMode},
			{Name: colSecretRef, Kind: model.KindText, Nullable: true, Principal: pdeclNoneSecretRef},
			{Name: colWiringStatus, Kind: model.KindText, Indexed: true, Principal: model.None("closed set declared|applied|revoked: wiring.go:21, wiring.go:23")},
			{Name: colAttribution, Kind: model.KindText, Principal: model.None("closed set firm|degraded: wiring.go:30, wiring.go:31")},
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
			{Name: colDefinitionRef, Kind: model.KindUUID, Indexed: true, Principal: pdeclNoneDefinitionRef},
			{Name: colOp, Kind: model.KindText, Indexed: true, Principal: model.None("closed set plan|apply|verify|retire: lifecycle.go:21, lifecycle.go:24")},
			{Name: colFromVersion, Kind: model.KindInt},
			{Name: colToVersion, Kind: model.KindInt},
			{Name: colPlanHash, Kind: model.KindText, Nullable: true, Indexed: true, Principal: model.None("a hex SHA-256 binding an approval to one transition, compared only as a hash: helpers.go:210, lifecycle.go:314")},
			{Name: colApprovalRef, Kind: model.KindText, Nullable: true, Principal: model.None("a governance approval reference (approval id, no-gate or break-glass handle) resolved only as an approval: cmd/olivares/approvalbridge.go:379, cmd/olivares/approvalbridge.go:382, cmd/olivares/approvalbridge.go:646; the stored copy is only rendered: operations.go:39")},
			{Name: colGateStatus, Kind: model.KindText, Principal: model.None("a gate decision from the closed GateStatus set (ports.go:31, ports.go:42) the governance adapter maps into: cmd/olivares/approvalbridge.go:943")},
			{Name: colOpStatus, Kind: model.KindText, Indexed: true, Principal: model.None("closed set of operation outcomes: lifecycle.go:29, lifecycle.go:36")},
			{Name: colActor, Kind: model.KindText, Principal: model.Ref(model.EncodeUserRef, model.ClassEvidence)},
			{Name: colResult, Kind: model.KindText, Nullable: true, Principal: model.None("a bounded outcome summary, only rendered: lifecycle.go:676, operations.go:40")},
			{Name: colOccurredAt, Kind: model.KindTimestamp, Indexed: true},
		},
	})
}
