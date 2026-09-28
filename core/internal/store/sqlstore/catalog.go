// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"github.com/olivaresai/olivares/core/model"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// This file is the catalog of core entities: one EntityDescriptor (the schema)
// and one hand-written Codec (the struct<->Record mapping) per entity. The
// descriptors are the single source of truth for the schema: the core tables
// are GENERATED from them at migration time (see schema.go), so a table can
// never drift from its descriptor. Auditing of core mutations is deliberately
// off here — Owns the audit mechanism binds real actor/action
// semantics and turns auditing on per entity.

// coreDescriptors returns every core entity descriptor, in creation order
// (referenced tables first is not required: there are no DB-level foreign keys,
// only id references, to keep the model portable and tenant-partitionable).
func coreDescriptors() []model.EntityDescriptor {
	ds := []model.EntityDescriptor{
		orgDescriptor, agentDescriptor, sessionDescriptor, providerDescriptor,
		modelDescriptor, mcpServerDescriptor, skillDescriptor, toolDescriptor,
		resourceDescriptor, identityDescriptor, policyDescriptor, costDescriptor,
		evalDescriptor, findingDescriptor, healthDescriptor, deploymentDescriptor,
		accessEdgeDescriptor,
		// The durable evidence operation journal (q1) — appended so an
		// existing database gets the table via reconcileColumns, a fresh one via v2.
		evidenceOpDescriptor,
	}
	// The v26.9 access-evidence relations: policy artifacts, authority
	// transitions, observed action stages and authorization decisions, kept
	// SEPARATE from each other and from the AccessEdge projection. Appended for
	// the same reason as the journal above — a fresh database creates them in v2,
	// an existing one gets each created whole (guards and indexes included) by
	// reconcileColumns — so no relation that already exists is altered.
	//
	// ⚠ MEASURED, 2026-09-06, and unresolved at the time of writing: these are
	// APPEND-ONLY relations, so registering them changes the append-only census
	// (registry.appendOnlyTables()) and therefore the guard manifest's code_sha256,
	// from which every bootstrap receipt already recorded in a database derives its
	// rollout id. A database created by a build WITHOUT these four relations then
	// refuses to open with "this binary declares no authorized transition from the
	// guard edition the database records" (reproduced on SQLite; it is also why
	// TestK2GoldenCoreOnlySQLiteUpgradesAndReopens is red).
	//
	// Epochs 3 and 4 added append-only relations by declaring an edition edge whose
	// predecessor removes exactly that delta. That does not extend to these: epochs
	// 3 and 4 are MODULE deltas absent from a core-only census, while these are core
	// and present in every build — and requireCompleteGuardEdition demands the delta
	// of every epoch up to the current one, so a core-only build could never reach a
	// new epoch above them. Admitting an empty delta is explicitly refused by
	// guardManifestEditionEdge. The edition model needs a mandatory CORE delta above
	// conditional module deltas, which is an architecture decision, not a
	// silent catalog registration. Core v9 admits the four relations through
	// named access-evidence edition edges.
	ds = append(ds, accessEvidenceDescriptors()...)
	// K3's fenced directory: one mutable tenant epoch and two append-only,
	// retained retirement-evidence tables. Their engine-owned operations are
	// deliberately separate from the ordinary typed repositories.
	ds = append(ds, directoryDescriptors()...)
	// Authorization generation is a separate tenant-local fact. Keeping it out
	// of directoryDescriptors avoids claiming that directory-v7's specialized
	// guard receipt covers a relation it was never designed to attest.
	ds = append(ds, authorizationEpochDescriptor)
	ds = append(ds, lineageDescriptors()...)
	ds = append(ds, userAuthorityDescriptor)
	// The FASE X scoping entities (workspace, agent_group,
	// agent_group_member) are tenant-resident core entities.
	ds = append(ds, scopingDescriptors()...)
	// The authentication/authorization entities are core entities too (engine
	// generates and guards their tables); they live in the system tenant.
	return append(ds, authDescriptors()...)
}

// --- Org ---------------------------------------------------------------------

var orgDescriptor = model.EntityDescriptor{
	Kind:  "core.org",
	Table: "orgs",
	Fields: []model.FieldSpec{
		pdecl(field("name", model.KindText, false),
			model.None("the organization's display name, rendered only: core/model/entities.go:20, core/api/dto.go:146")),
		pdecl(field("slug", model.KindText, false),
			model.None("a unique URL-safe org handle: core/model/entities.go:22, core/api/dto.go:147")),
		pdecl(field("status", model.KindText, false), pdeclNoneLifecycle),
		// settings is a tenant's trust configuration (SET publisher issuers,
		// audiences and keys: core/auth/set_publisher.go:39) and, on the system
		// organization, the backup schedule (core/api/dr_schedule.go:87). The
		// schedule names the account that asked to disarm the restore gate's dual
		// control (core/api/dr_schedule.go:67), which the gate then refuses
		// (core/api/dr_handler.go:249): a reference that only restricts.
		pdecl(field("settings", model.KindJSON, true), model.Scan(model.ClassRestrict)),
		// data_region is the residency pin (OPS-4). Nullable so it is added
		// additively to an already-migrated orgs table by reconcileColumns;
		// empty/NULL means the tenant is unpinned. It is indexed so a region-scoped
		// instance can enumerate its resident tenants cheaply at boot.
		pdecl(indexedField("data_region", model.KindText, true),
			model.None("a residency region code: core/model/entities.go:28, core/api/dto.go:141")),
	},
	Indexes: []model.IndexSpec{{Name: "orgs_slug_uniq", Columns: []string{"slug"}, Unique: true}},
}

var orgCodec = model.Codec[model.Org]{
	Base: func(o *model.Org) *model.BaseFields { return &o.BaseFields },
	Encode: func(o model.Org) (model.Record, error) {
		settings, err := encJSON(o.Settings)
		if err != nil {
			return nil, err
		}
		return model.Record{"name": o.Name, "slug": o.Slug, "status": string(o.Status),
			"settings": settings, "data_region": o.DataRegion}, nil
	},
	Decode: func(b model.BaseFields, r model.Record) (model.Org, error) {
		settings, err := decJSON(r, "settings")
		if err != nil {
			return model.Org{}, err
		}
		return model.Org{BaseFields: b, Name: r.String("name"), Slug: r.String("slug"),
			Status: model.LifecycleStatus(r.String("status")), Settings: settings,
			DataRegion: r.String("data_region")}, nil
	},
}

// pdeclNoneLifecycle, pdeclNoneWorkspaceLineage, pdeclNoneFreeContext and
// pdeclNoneCostOrigin are why no reader resolves these catalog columns to a
// principal.
var (
	pdeclNoneLifecycle        = model.None("a lifecycle state from the closed active/inactive/error/suspended vocabulary: core/model/enums.go:55")
	pdeclNoneWorkspaceLineage = model.None("the core workspace the row is scoped to, read only as its workspace lineage: core/model/descriptor.go:149, core/store/bounded_read.go:114")
	pdeclNoneFreeContext      = model.None("free-form, non-sensitive context that holds no payload or secret and is only rendered: core/model/entities.go:12")
	pdeclNoneCostOrigin       = model.None("a session, agent, model or provider row the cost is tied to: core/model/entities.go:267")
)

// --- Agent -------------------------------------------------------------------

var agentDescriptor = model.EntityDescriptor{
	Kind:                   "core.agent",
	Table:                  "agents",
	SoftDelete:             true,
	AuthorizationFact:      true,
	AuthorizationLockOrder: 20,
	Fields: []model.FieldSpec{
		pdecl(field("name", model.KindText, false),
			model.None("the agent's display name: core/model/entities.go:40, core/api/dto.go:57")),
		pdecl(indexedField("kind", model.KindText, false),
			model.None("an agent classification label: core/model/entities.go:42")),
		pdecl(field("external_id", model.KindText, true),
			model.None("the agent's id in its source system, matched only against agent origins: core/model/entities.go:44, modules/access-map/bridge.go:88")),
		pdecl(field("status", model.KindText, false), pdeclNoneLifecycle),
		// identity_id names the row of the identity roster the agent runs as, never
		// an account row; it records the binding.
		pdecl(field("identity_id", model.KindUUID, true),
			model.Ref(model.EncodeIdentity, model.ClassEvidence)),
		pdecl(field("labels", model.KindJSON, true),
			model.None("free-form tags, rendered and projected as display fields, never resolved: core/api/dto.go:59, cmd/olivares/protocollocalresource.go:98")),
		pdecl(field("metadata", model.KindJSON, true),
			model.None("free-form, non-sensitive context, rendered and projected as display fields, never resolved: core/api/dto.go:59, cmd/olivares/protocollocalresource.go:102")),
		// workspace_id is the FASE X scoping dimension. Nullable and
		// appended last so it is added additively to an already-migrated agents
		// table by reconcileColumns; NULL resolves to the tenant's default
		// workspace (back-compat).
		pdecl(indexedField("workspace_id", model.KindUUID, true), pdeclNoneWorkspaceLineage),
		// risk_tier is the agent's effective governance risk tier.
		// Nullable; reconcileColumns adds it additively. The governance module
		// is the sole writer; empty means unclassified.
		pdecl(indexedField("risk_tier", model.KindText, true),
			model.None("a governance risk tier from the closed low/medium/high/critical vocabulary: core/model/entities.go:56")),
	},
	WorkspaceLineage: model.WorkspaceLineageSpec{
		Column:   "workspace_id",
		Encoding: model.WorkspaceLineageID,
		Unset:    model.WorkspaceUnsetMeansDefault,
	},
}

var agentCodec = model.Codec[model.Agent]{
	Base: func(a *model.Agent) *model.BaseFields { return &a.BaseFields },
	Encode: func(a model.Agent) (model.Record, error) {
		labels, err := encJSON(a.Labels)
		if err != nil {
			return nil, err
		}
		meta, err := encJSON(a.Metadata)
		if err != nil {
			return nil, err
		}
		return model.Record{
			"name": a.Name, "kind": a.Kind, "external_id": a.ExternalID,
			"status": string(a.Status), "identity_id": encOptID(a.IdentityID),
			"workspace_id": encOptID(a.WorkspaceID), "risk_tier": encOptStr(a.RiskTier),
			"labels": labels, "metadata": meta,
		}, nil
	},
	Decode: func(b model.BaseFields, r model.Record) (model.Agent, error) {
		labels, err := decJSON(r, "labels")
		if err != nil {
			return model.Agent{}, err
		}
		meta, err := decJSON(r, "metadata")
		if err != nil {
			return model.Agent{}, err
		}
		return model.Agent{BaseFields: b, Name: r.String("name"), Kind: r.String("kind"),
			ExternalID: r.String("external_id"), Status: model.LifecycleStatus(r.String("status")),
			IdentityID: decID(r, "identity_id"), WorkspaceID: decID(r, "workspace_id"),
			RiskTier: r.String("risk_tier"),
			Labels:   labels, Metadata: meta}, nil
	},
}

// --- Session -----------------------------------------------------------------

var sessionDescriptor = model.EntityDescriptor{
	Kind:       "core.session",
	Table:      "sessions",
	SoftDelete: true,
	Fields: []model.FieldSpec{
		// agent_id is nullable: a session discovered from cooperative telemetry
		// (OTEL session.id) has no agent reference, so it stays unlinked (NULL)
		// rather than carrying an empty sentinel — matching the other optional
		// links (model_id, mcp_server_id).
		pdecl(indexedField("agent_id", model.KindUUID, true),
			model.None("the agent row that owns the session: core/model/entities.go:73")),
		pdecl(field("external_id", model.KindText, true),
			model.None("the session's id in its source system, matched only against session origins: core/model/entities.go:75, modules/access-map/bridge.go:103")),
		pdecl(field("state", model.KindText, false),
			model.None("a session state from the closed running/completed/failed vocabulary: core/model/enums.go:79")),
		pdecl(field("goal", model.KindText, true),
			model.None("the session's stated objective, prose only: core/model/entities.go:79")),
		pdecl(field("summary", model.KindText, true),
			model.None("a short, non-sensitive session summary: core/model/entities.go:81")),
		pdecl(field("model_id", model.KindUUID, true),
			model.None("the model row the session used: core/model/entities.go:89")),
		field("started_at", model.KindTimestamp, false),
		field("ended_at", model.KindTimestamp, true),
		pdecl(field("metadata", model.KindJSON, true), pdeclNoneFreeContext),
		// workspace_id is the FASE X scoping dimension. Nullable, appended
		// last for additive reconcile; NULL resolves to the default workspace.
		pdecl(indexedField("workspace_id", model.KindUUID, true), pdeclNoneWorkspaceLineage),
	},
	WorkspaceLineage: model.WorkspaceLineageSpec{
		Column:   "workspace_id",
		Encoding: model.WorkspaceLineageID,
		Unset:    model.WorkspaceUnsetMeansDefault,
	},
}

var sessionCodec = model.Codec[model.Session]{
	Base: func(s *model.Session) *model.BaseFields { return &s.BaseFields },
	Encode: func(s model.Session) (model.Record, error) {
		meta, err := encJSON(s.Metadata)
		if err != nil {
			return nil, err
		}
		return model.Record{
			"agent_id": encOptID(s.AgentID), "external_id": s.ExternalID, "state": string(s.State),
			"goal": s.Goal, "summary": s.Summary, "model_id": encOptID(s.ModelID),
			"started_at": encTS(s.StartedAt), "ended_at": encOptTS(s.EndedAt), "metadata": meta,
			"workspace_id": encOptID(s.WorkspaceID),
		}, nil
	},
	Decode: func(b model.BaseFields, r model.Record) (model.Session, error) {
		meta, err := decJSON(r, "metadata")
		if err != nil {
			return model.Session{}, err
		}
		started, err := decTS(r, "started_at")
		if err != nil {
			return model.Session{}, err
		}
		ended, err := decOptTS(r, "ended_at")
		if err != nil {
			return model.Session{}, err
		}
		return model.Session{BaseFields: b, AgentID: decID(r, "agent_id"), ExternalID: r.String("external_id"),
			State: model.SessionState(r.String("state")), Goal: r.String("goal"), Summary: r.String("summary"),
			ModelID: decID(r, "model_id"), StartedAt: started, EndedAt: ended, Metadata: meta,
			WorkspaceID: decID(r, "workspace_id")}, nil
	},
}

// --- Provider ----------------------------------------------------------------

var providerDescriptor = model.EntityDescriptor{
	Kind:  "core.provider",
	Table: "providers",
	Fields: []model.FieldSpec{
		pdecl(field("name", model.KindText, false),
			model.None("the provider's display name: core/model/entities.go:102")),
		pdecl(indexedField("kind", model.KindText, false),
			model.None("a provider kind label: core/model/entities.go:104")),
		pdecl(field("base_url", model.KindText, true),
			model.None("the provider API endpoint: core/model/entities.go:106")),
		pdecl(field("status", model.KindText, false), pdeclNoneLifecycle),
		pdecl(field("config", model.KindJSON, true),
			model.None("non-secret provider configuration: core/model/entities.go:110")),
	},
}

var providerCodec = model.Codec[model.Provider]{
	Base: func(p *model.Provider) *model.BaseFields { return &p.BaseFields },
	Encode: func(p model.Provider) (model.Record, error) {
		config, err := encJSON(p.Config)
		if err != nil {
			return nil, err
		}
		return model.Record{"name": p.Name, "kind": p.Kind, "base_url": p.BaseURL,
			"status": string(p.Status), "config": config}, nil
	},
	Decode: func(b model.BaseFields, r model.Record) (model.Provider, error) {
		config, err := decJSON(r, "config")
		if err != nil {
			return model.Provider{}, err
		}
		return model.Provider{BaseFields: b, Name: r.String("name"), Kind: r.String("kind"),
			BaseURL: r.String("base_url"), Status: model.LifecycleStatus(r.String("status")), Config: config}, nil
	},
}

// --- Model -------------------------------------------------------------------

var modelDescriptor = model.EntityDescriptor{
	Kind:  "core.model",
	Table: "models",
	Fields: []model.FieldSpec{
		pdecl(indexedField("provider_id", model.KindUUID, false),
			model.None("the owning provider row: core/model/entities.go:117")),
		pdecl(field("name", model.KindText, false),
			model.None("the model name: core/model/entities.go:119")),
		pdecl(field("family", model.KindText, true),
			model.None("a model family label: core/model/entities.go:121")),
		field("context_window", model.KindInt, false),
		field("input_cost_micro_usd", model.KindInt, false),
		field("output_cost_micro_usd", model.KindInt, false),
		pdecl(field("modality", model.KindText, true),
			model.None("a modality label: core/model/entities.go:129")),
		pdecl(field("status", model.KindText, false), pdeclNoneLifecycle),
		pdecl(field("metadata", model.KindJSON, true), pdeclNoneFreeContext),
	},
}

var modelCodec = model.Codec[model.Model]{
	Base: func(m *model.Model) *model.BaseFields { return &m.BaseFields },
	Encode: func(m model.Model) (model.Record, error) {
		meta, err := encJSON(m.Metadata)
		if err != nil {
			return nil, err
		}
		return model.Record{"provider_id": m.ProviderID.String(), "name": m.Name, "family": m.Family,
			"context_window": m.ContextWindow, "input_cost_micro_usd": m.InputCostMicroUSD,
			"output_cost_micro_usd": m.OutputCostMicroUSD, "modality": m.Modality,
			"status": string(m.Status), "metadata": meta}, nil
	},
	Decode: func(b model.BaseFields, r model.Record) (model.Model, error) {
		meta, err := decJSON(r, "metadata")
		if err != nil {
			return model.Model{}, err
		}
		return model.Model{BaseFields: b, ProviderID: decID(r, "provider_id"), Name: r.String("name"),
			Family: r.String("family"), ContextWindow: r.Int("context_window"),
			InputCostMicroUSD: r.Int("input_cost_micro_usd"), OutputCostMicroUSD: r.Int("output_cost_micro_usd"),
			Modality: r.String("modality"), Status: model.LifecycleStatus(r.String("status")), Metadata: meta}, nil
	},
}

// --- MCPServer ---------------------------------------------------------------

var mcpServerDescriptor = model.EntityDescriptor{
	Kind:  "core.mcp_server",
	Table: "mcp_servers",
	Fields: []model.FieldSpec{
		pdecl(field("name", model.KindText, false),
			model.None("the server's display name: core/model/entities.go:140")),
		pdecl(field("transport", model.KindText, false),
			model.None("the MCP transport label: core/model/entities.go:142")),
		pdecl(field("endpoint", model.KindText, true),
			model.None("the server address or command: core/model/entities.go:144")),
		pdecl(field("server_version", model.KindText, true),
			model.None("the reported server version: core/model/entities.go:146")),
		pdecl(field("status", model.KindText, false), pdeclNoneLifecycle),
		pdecl(field("metadata", model.KindJSON, true), pdeclNoneFreeContext),
	},
}

var mcpServerCodec = model.Codec[model.MCPServer]{
	Base: func(m *model.MCPServer) *model.BaseFields { return &m.BaseFields },
	Encode: func(m model.MCPServer) (model.Record, error) {
		meta, err := encJSON(m.Metadata)
		if err != nil {
			return nil, err
		}
		return model.Record{"name": m.Name, "transport": m.Transport, "endpoint": m.Endpoint,
			"server_version": m.Version, "status": string(m.Status), "metadata": meta}, nil
	},
	Decode: func(b model.BaseFields, r model.Record) (model.MCPServer, error) {
		meta, err := decJSON(r, "metadata")
		if err != nil {
			return model.MCPServer{}, err
		}
		return model.MCPServer{BaseFields: b, Name: r.String("name"), Transport: r.String("transport"),
			Endpoint: r.String("endpoint"), Version: r.String("server_version"),
			Status: model.LifecycleStatus(r.String("status")), Metadata: meta}, nil
	},
}

// --- Skill -------------------------------------------------------------------

var skillDescriptor = model.EntityDescriptor{
	Kind:  "core.skill",
	Table: "skills",
	Fields: []model.FieldSpec{
		pdecl(field("name", model.KindText, false),
			model.None("the skill's display name: core/model/entities.go:157")),
		pdecl(field("source", model.KindText, true),
			model.None("where the skill is defined, a repository or server: core/model/entities.go:159")),
		pdecl(field("skill_version", model.KindText, true),
			model.None("the skill version: core/model/entities.go:161")),
		pdecl(field("mcp_server_id", model.KindUUID, true),
			model.None("the MCP server row that provides the skill: core/model/entities.go:163")),
		pdecl(field("description", model.KindText, true),
			model.None("a short, non-sensitive description: core/model/entities.go:165")),
		pdecl(field("status", model.KindText, false), pdeclNoneLifecycle),
		pdecl(field("metadata", model.KindJSON, true), pdeclNoneFreeContext),
	},
}

var skillCodec = model.Codec[model.Skill]{
	Base: func(s *model.Skill) *model.BaseFields { return &s.BaseFields },
	Encode: func(s model.Skill) (model.Record, error) {
		meta, err := encJSON(s.Metadata)
		if err != nil {
			return nil, err
		}
		return model.Record{"name": s.Name, "source": s.Source, "skill_version": s.Version,
			"mcp_server_id": encOptID(s.MCPServerID), "description": s.Description,
			"status": string(s.Status), "metadata": meta}, nil
	},
	Decode: func(b model.BaseFields, r model.Record) (model.Skill, error) {
		meta, err := decJSON(r, "metadata")
		if err != nil {
			return model.Skill{}, err
		}
		return model.Skill{BaseFields: b, Name: r.String("name"), Source: r.String("source"),
			Version: r.String("skill_version"), MCPServerID: decID(r, "mcp_server_id"),
			Description: r.String("description"), Status: model.LifecycleStatus(r.String("status")), Metadata: meta}, nil
	},
}

// --- Tool --------------------------------------------------------------------

var toolDescriptor = model.EntityDescriptor{
	Kind:  "core.tool",
	Table: "tools",
	Fields: []model.FieldSpec{
		pdecl(field("name", model.KindText, false),
			model.None("the tool name: core/model/entities.go:177")),
		pdecl(field("mcp_server_id", model.KindUUID, true),
			model.None("the MCP server row that exposes the tool: core/model/entities.go:179")),
		pdecl(field("kind", model.KindText, true),
			model.None("a tool classification label: core/model/entities.go:181")),
		field("read_only_hint", model.KindBool, false),
		field("destructive_hint", model.KindBool, false),
		pdecl(field("schema_hash", model.KindBytes, true),
			model.None("a hash of the tool input schema, for change detection: core/model/entities.go:187")),
		pdecl(field("description", model.KindText, true),
			model.None("a short, non-sensitive description: core/model/entities.go:189")),
		pdecl(field("metadata", model.KindJSON, true), pdeclNoneFreeContext),
	},
}

var toolCodec = model.Codec[model.Tool]{
	Base: func(t *model.Tool) *model.BaseFields { return &t.BaseFields },
	Encode: func(t model.Tool) (model.Record, error) {
		meta, err := encJSON(t.Metadata)
		if err != nil {
			return nil, err
		}
		return model.Record{"name": t.Name, "mcp_server_id": encOptID(t.MCPServerID), "kind": t.Kind,
			"read_only_hint": t.ReadOnlyHint, "destructive_hint": t.DestructiveHint,
			"schema_hash": encBytes(t.SchemaHash), "description": t.Description, "metadata": meta}, nil
	},
	Decode: func(b model.BaseFields, r model.Record) (model.Tool, error) {
		meta, err := decJSON(r, "metadata")
		if err != nil {
			return model.Tool{}, err
		}
		return model.Tool{BaseFields: b, Name: r.String("name"), MCPServerID: decID(r, "mcp_server_id"),
			Kind: r.String("kind"), ReadOnlyHint: r.Bool("read_only_hint"), DestructiveHint: r.Bool("destructive_hint"),
			SchemaHash: r.Bytes("schema_hash"), Description: r.String("description"), Metadata: meta}, nil
	},
}

// --- Resource ----------------------------------------------------------------

var resourceDescriptor = model.EntityDescriptor{
	Kind:  "core.resource",
	Table: "resources",
	Fields: []model.FieldSpec{
		pdecl(field("name", model.KindText, false),
			model.None("the resource's display name: core/model/entities.go:202")),
		pdecl(indexedField("kind", model.KindText, false),
			model.None("a resource classification label: core/model/entities.go:204")),
		pdecl(field("uri", model.KindText, true),
			model.None("the resource's natural identifier: core/model/entities.go:207")),
		pdecl(field("sensitivity", model.KindText, true),
			model.None("an operator-assigned sensitivity label: core/model/entities.go:209")),
		// owner is free text naming the responsible team or person, so it may carry
		// an account alias; it is matched as text and records who is responsible.
		pdecl(field("owner", model.KindText, true), model.Scan(model.ClassEvidence)),
		pdecl(field("metadata", model.KindJSON, true), pdeclNoneFreeContext),
		// FASE X scoping + hierarchy columns. All nullable and appended
		// last for additive reconcile. workspace_id NULL resolves to the default
		// workspace; parent_id NULL is a tree root; path is the store-maintained
		// materialized path, indexed so a subtree is one prefix scan.
		pdecl(indexedField("workspace_id", model.KindUUID, true), pdeclNoneWorkspaceLineage),
		pdecl(indexedField("parent_id", model.KindUUID, true),
			model.None("the parent resource row: core/model/entities.go:217")),
		pdecl(indexedField("path", model.KindText, true),
			model.None("the store-maintained path of ancestor resource ids: core/model/entities.go:221")),
	},
	WorkspaceLineage: model.WorkspaceLineageSpec{
		Column:   "workspace_id",
		Encoding: model.WorkspaceLineageID,
		Unset:    model.WorkspaceUnsetMeansDefault,
	},
}

var resourceCodec = model.Codec[model.Resource]{
	Base: func(r *model.Resource) *model.BaseFields { return &r.BaseFields },
	Encode: func(rs model.Resource) (model.Record, error) {
		meta, err := encJSON(rs.Metadata)
		if err != nil {
			return nil, err
		}
		return model.Record{"name": rs.Name, "kind": rs.Kind, "uri": rs.URI,
			"sensitivity": rs.Sensitivity, "owner": rs.Owner, "metadata": meta,
			"workspace_id": encOptID(rs.WorkspaceID), "parent_id": encOptID(rs.ParentID),
			"path": encOptStr(rs.Path)}, nil
	},
	Decode: func(b model.BaseFields, r model.Record) (model.Resource, error) {
		meta, err := decJSON(r, "metadata")
		if err != nil {
			return model.Resource{}, err
		}
		return model.Resource{BaseFields: b, Name: r.String("name"), Kind: r.String("kind"),
			URI: r.String("uri"), Sensitivity: r.String("sensitivity"), Owner: r.String("owner"), Metadata: meta,
			WorkspaceID: decID(r, "workspace_id"), ParentID: decID(r, "parent_id"), Path: r.String("path")}, nil
	},
}

// --- Identity ----------------------------------------------------------------

var identityDescriptor = model.EntityDescriptor{
	Kind:                   "core.identity",
	Table:                  "identities",
	AuthorizationFact:      true,
	AuthorizationLockOrder: 10,
	Fields: []model.FieldSpec{
		// name falls back to the directory reference itself when the roster has no
		// display name, so it may carry an account alias verbatim.
		pdecl(field("name", model.KindText, false), model.Scan(model.ClassEvidence)),
		pdecl(indexedField("kind", model.KindText, false),
			model.None("an identity classification label: core/model/entities.go:240")),
		pdecl(field("external_id", model.KindText, true),
			model.Ref(model.EncodeExternalID, model.ClassEvidence)),
		pdecl(field("provider", model.KindText, true),
			model.None("the identity provider label: core/model/entities.go:244")),
		pdecl(field("metadata", model.KindJSON, true),
			model.None("closed governance keys and allow-listed, non-identifying directory attributes: modules/governance/roster.go:45, modules/governance/roster.go:277")),
	},
}

var identityCodec = model.Codec[model.Identity]{
	Base: func(i *model.Identity) *model.BaseFields { return &i.BaseFields },
	Encode: func(i model.Identity) (model.Record, error) {
		meta, err := encJSON(i.Metadata)
		if err != nil {
			return nil, err
		}
		return model.Record{"name": i.Name, "kind": i.Kind, "external_id": i.ExternalID,
			"provider": i.Provider, "metadata": meta}, nil
	},
	Decode: func(b model.BaseFields, r model.Record) (model.Identity, error) {
		meta, err := decJSON(r, "metadata")
		if err != nil {
			return model.Identity{}, err
		}
		return model.Identity{BaseFields: b, Name: r.String("name"), Kind: r.String("kind"),
			ExternalID: r.String("external_id"), Provider: r.String("provider"), Metadata: meta}, nil
	},
}

// --- Policy ------------------------------------------------------------------

var policyDescriptor = model.EntityDescriptor{
	Kind:  "core.policy",
	Table: "policies",
	Fields: []model.FieldSpec{
		pdecl(field("name", model.KindText, false),
			model.None("the policy name: core/model/entities.go:253")),
		pdecl(indexedField("kind", model.KindText, false),
			model.None("a policy kind, refused unless the policy-kind registry has it: policy_snapshot.go:60")),
		// The spec's shape is the variant its kind selects from the policy-kind
		// registry, the same registry the policy writer refuses an unknown kind by.
		pdecl(field("spec", model.KindJSON, true), model.Union("kind", model.PolicyKinds)),
		field("enabled", model.KindBool, false),
	},
}

var policyCodec = model.Codec[model.Policy]{
	Base: func(p *model.Policy) *model.BaseFields { return &p.BaseFields },
	Encode: func(p model.Policy) (model.Record, error) {
		spec, err := encJSON(p.Spec)
		if err != nil {
			return nil, err
		}
		return model.Record{"name": p.Name, "kind": p.Kind, "spec": spec, "enabled": p.Enabled}, nil
	},
	Decode: func(b model.BaseFields, r model.Record) (model.Policy, error) {
		spec, err := decJSON(r, "spec")
		if err != nil {
			return model.Policy{}, err
		}
		return model.Policy{BaseFields: b, Name: r.String("name"), Kind: r.String("kind"),
			Spec: spec, Enabled: r.Bool("enabled")}, nil
	},
}

// --- CostRecord --------------------------------------------------------------

var costDescriptor = model.EntityDescriptor{
	Kind:  "core.cost_record",
	Table: "cost_records",
	Fields: []model.FieldSpec{
		pdecl(field("session_id", model.KindUUID, true), pdeclNoneCostOrigin),
		pdecl(field("agent_id", model.KindUUID, true), pdeclNoneCostOrigin),
		pdecl(field("model_id", model.KindUUID, true), pdeclNoneCostOrigin),
		pdecl(field("provider_id", model.KindUUID, true), pdeclNoneCostOrigin),
		indexedField("occurred_at", model.KindTimestamp, false),
		field("input_tokens", model.KindInt, false),
		field("output_tokens", model.KindInt, false),
		field("cost_micro_usd", model.KindInt, false),
		pdecl(field("currency", model.KindText, true),
			model.None("the display currency code: core/model/entities.go:280")),
		// metadata carries the usage actor and identity references the ingest
		// copies from the provider; erasure matches the actor against a subject.
		pdecl(field("metadata", model.KindJSON, true), model.Scan(model.ClassEvidence)),
	},
}

var costCodec = model.Codec[model.CostRecord]{
	Base: func(c *model.CostRecord) *model.BaseFields { return &c.BaseFields },
	Encode: func(c model.CostRecord) (model.Record, error) {
		meta, err := encJSON(c.Metadata)
		if err != nil {
			return nil, err
		}
		return model.Record{"session_id": encOptID(c.SessionID), "agent_id": encOptID(c.AgentID),
			"model_id": encOptID(c.ModelID), "provider_id": encOptID(c.ProviderID),
			"occurred_at": encTS(c.OccurredAt), "input_tokens": c.InputTokens, "output_tokens": c.OutputTokens,
			"cost_micro_usd": c.CostMicroUSD, "currency": c.Currency, "metadata": meta}, nil
	},
	Decode: func(b model.BaseFields, r model.Record) (model.CostRecord, error) {
		meta, err := decJSON(r, "metadata")
		if err != nil {
			return model.CostRecord{}, err
		}
		occurred, err := decTS(r, "occurred_at")
		if err != nil {
			return model.CostRecord{}, err
		}
		return model.CostRecord{BaseFields: b, SessionID: decID(r, "session_id"), AgentID: decID(r, "agent_id"),
			ModelID: decID(r, "model_id"), ProviderID: decID(r, "provider_id"), OccurredAt: occurred,
			InputTokens: r.Int("input_tokens"), OutputTokens: r.Int("output_tokens"),
			CostMicroUSD: r.Int("cost_micro_usd"), Currency: r.String("currency"), Metadata: meta}, nil
	},
}

// --- EvalResult --------------------------------------------------------------

var evalDescriptor = model.EntityDescriptor{
	Kind:  "core.eval_result",
	Table: "eval_results",
	Fields: []model.FieldSpec{
		pdecl(indexedField("suite", model.KindText, false),
			model.None("the eval suite name: core/model/entities.go:289")),
		pdecl(field("subject_kind", model.KindText, false),
			model.None("the kind of the evaluated subject, read as the kind of subject_id: core/model/entities.go:291")),
		pdecl(field("subject_id", model.KindUUID, false),
			model.KindRef("subject_kind", model.ClassEvidence)),
		field("score", model.KindFloat, false),
		field("passed", model.KindBool, false),
		field("occurred_at", model.KindTimestamp, false),
		pdecl(field("metrics", model.KindJSON, true),
			model.None("free-form metric counts, rates and scores: core/model/entities.go:300, modules/evals/runs.go:194")),
		pdecl(field("metadata", model.KindJSON, true), pdeclNoneFreeContext),
	},
}

var evalCodec = model.Codec[model.EvalResult]{
	Base: func(e *model.EvalResult) *model.BaseFields { return &e.BaseFields },
	Encode: func(e model.EvalResult) (model.Record, error) {
		metrics, err := encJSON(e.Metrics)
		if err != nil {
			return nil, err
		}
		meta, err := encJSON(e.Metadata)
		if err != nil {
			return nil, err
		}
		return model.Record{"suite": e.Suite, "subject_kind": e.SubjectKind, "subject_id": e.SubjectID.String(),
			"score": e.Score, "passed": e.Passed, "occurred_at": encTS(e.OccurredAt),
			"metrics": metrics, "metadata": meta}, nil
	},
	Decode: func(b model.BaseFields, r model.Record) (model.EvalResult, error) {
		metrics, err := decJSON(r, "metrics")
		if err != nil {
			return model.EvalResult{}, err
		}
		meta, err := decJSON(r, "metadata")
		if err != nil {
			return model.EvalResult{}, err
		}
		occurred, err := decTS(r, "occurred_at")
		if err != nil {
			return model.EvalResult{}, err
		}
		return model.EvalResult{BaseFields: b, Suite: r.String("suite"), SubjectKind: r.String("subject_kind"),
			SubjectID: decID(r, "subject_id"), Score: r.Float("score"), Passed: r.Bool("passed"),
			OccurredAt: occurred, Metrics: metrics, Metadata: meta}, nil
	},
}

// --- Finding -----------------------------------------------------------------

var findingDescriptor = model.EntityDescriptor{
	Kind:       "core.finding",
	Table:      "findings",
	SoftDelete: true,
	Fields: []model.FieldSpec{
		pdecl(indexedField("kind", model.KindText, false),
			model.None("a finding classification label: core/model/entities.go:310")),
		pdecl(field("severity", model.KindText, false),
			model.None("a severity from the closed low/medium/high/critical vocabulary: core/model/enums.go:8")),
		pdecl(indexedField("status", model.KindText, false),
			model.None("a triage state from the closed open/triaged/resolved/dismissed vocabulary: core/model/enums.go:23")),
		pdecl(field("source", model.KindText, true),
			model.None("the detector or connector that produced the finding: core/model/entities.go:316")),
		pdecl(field("subject_kind", model.KindText, true),
			model.None("the kind of the finding's subject, read as the kind of subject_id: core/model/entities.go:318")),
		pdecl(field("subject_id", model.KindUUID, true),
			model.KindRef("subject_kind", model.ClassEvidence)),
		pdecl(field("title", model.KindText, false),
			model.None("a short, non-sensitive summary safe to display: core/model/entities.go:321")),
		pdecl(field("detail_hash", model.KindBytes, true),
			model.None("a hash of the redacted detail; the raw detail is not kept: core/model/entities.go:323")),
		field("occurred_at", model.KindTimestamp, false),
		// metadata may carry a subject reference that readers match as text.
		pdecl(field("metadata", model.KindJSON, true), model.Scan(model.ClassEvidence)),
	},
}

var findingCodec = model.Codec[model.Finding]{
	Base: func(f *model.Finding) *model.BaseFields { return &f.BaseFields },
	Encode: func(f model.Finding) (model.Record, error) {
		meta, err := encJSON(f.Metadata)
		if err != nil {
			return nil, err
		}
		return model.Record{"kind": f.Kind, "severity": string(f.Severity), "status": string(f.Status),
			"source": f.Source, "subject_kind": f.SubjectKind, "subject_id": encOptID(f.SubjectID),
			"title": f.Title, "detail_hash": encBytes(f.DetailHash), "occurred_at": encTS(f.OccurredAt),
			"metadata": meta}, nil
	},
	Decode: func(b model.BaseFields, r model.Record) (model.Finding, error) {
		meta, err := decJSON(r, "metadata")
		if err != nil {
			return model.Finding{}, err
		}
		occurred, err := decTS(r, "occurred_at")
		if err != nil {
			return model.Finding{}, err
		}
		return model.Finding{BaseFields: b, Kind: r.String("kind"), Severity: model.Severity(r.String("severity")),
			Status: model.FindingStatus(r.String("status")), Source: r.String("source"),
			SubjectKind: r.String("subject_kind"), SubjectID: decID(r, "subject_id"), Title: r.String("title"),
			DetailHash: r.Bytes("detail_hash"), OccurredAt: occurred, Metadata: meta}, nil
	},
}

// --- HealthStatus ------------------------------------------------------------

var healthDescriptor = model.EntityDescriptor{
	Kind:  "core.health_status",
	Table: "health_statuses",
	Fields: []model.FieldSpec{
		pdecl(field("subject_kind", model.KindText, false),
			model.None("the kind of the monitored subject, read as the kind of subject_id: core/model/entities.go:335")),
		pdecl(field("subject_id", model.KindUUID, false),
			model.KindRef("subject_kind", model.ClassEvidence)),
		pdecl(field("state", model.KindText, false),
			model.None("a health state from the closed unknown/healthy/degraded/down vocabulary: core/model/enums.go:39")),
		field("checked_at", model.KindTimestamp, false),
		field("latency_ms", model.KindInt, false),
		pdecl(field("detail", model.KindText, true),
			model.None("a short, non-sensitive health detail: core/model/entities.go:344")),
		pdecl(field("metadata", model.KindJSON, true), pdeclNoneFreeContext),
	},
}

var healthCodec = model.Codec[model.HealthStatus]{
	Base: func(h *model.HealthStatus) *model.BaseFields { return &h.BaseFields },
	Encode: func(h model.HealthStatus) (model.Record, error) {
		meta, err := encJSON(h.Metadata)
		if err != nil {
			return nil, err
		}
		return model.Record{"subject_kind": h.SubjectKind, "subject_id": h.SubjectID.String(),
			"state": string(h.State), "checked_at": encTS(h.CheckedAt), "latency_ms": h.LatencyMS,
			"detail": h.Detail, "metadata": meta}, nil
	},
	Decode: func(b model.BaseFields, r model.Record) (model.HealthStatus, error) {
		meta, err := decJSON(r, "metadata")
		if err != nil {
			return model.HealthStatus{}, err
		}
		checked, err := decTS(r, "checked_at")
		if err != nil {
			return model.HealthStatus{}, err
		}
		return model.HealthStatus{BaseFields: b, SubjectKind: r.String("subject_kind"),
			SubjectID: decID(r, "subject_id"), State: model.HealthState(r.String("state")),
			CheckedAt: checked, LatencyMS: r.Int("latency_ms"), Detail: r.String("detail"), Metadata: meta}, nil
	},
}

// --- Deployment --------------------------------------------------------------

var deploymentDescriptor = model.EntityDescriptor{
	Kind:  "core.deployment",
	Table: "deployments",
	Fields: []model.FieldSpec{
		pdecl(field("subject_kind", model.KindText, false),
			model.None("the kind of the deployed subject, read as the kind of subject_id: core/model/entities.go:354")),
		pdecl(field("subject_id", model.KindUUID, false),
			model.KindRef("subject_kind", model.ClassEvidence)),
		pdecl(field("target", model.KindText, true),
			model.None("where the subject was deployed, a host or cluster: core/model/entities.go:357")),
		pdecl(indexedField("environment", model.KindText, true),
			model.None("the deployment environment label: core/model/entities.go:359")),
		pdecl(field("status", model.KindText, false),
			model.None("the deployment status label: core/model/entities.go:361")),
		pdecl(field("release_version", model.KindText, true),
			model.None("the deployed version: core/model/entities.go:363")),
		field("deployed_at", model.KindTimestamp, false),
		pdecl(field("config_hash", model.KindBytes, true),
			model.None("a hash of the applied configuration: core/model/entities.go:367")),
		pdecl(field("metadata", model.KindJSON, true), pdeclNoneFreeContext),
	},
}

var deploymentCodec = model.Codec[model.Deployment]{
	Base: func(d *model.Deployment) *model.BaseFields { return &d.BaseFields },
	Encode: func(d model.Deployment) (model.Record, error) {
		meta, err := encJSON(d.Metadata)
		if err != nil {
			return nil, err
		}
		return model.Record{"subject_kind": d.SubjectKind, "subject_id": d.SubjectID.String(),
			"target": d.Target, "environment": d.Environment, "status": d.Status, "release_version": d.Version,
			"deployed_at": encTS(d.DeployedAt), "config_hash": encBytes(d.ConfigHash), "metadata": meta}, nil
	},
	Decode: func(b model.BaseFields, r model.Record) (model.Deployment, error) {
		meta, err := decJSON(r, "metadata")
		if err != nil {
			return model.Deployment{}, err
		}
		deployed, err := decTS(r, "deployed_at")
		if err != nil {
			return model.Deployment{}, err
		}
		return model.Deployment{BaseFields: b, SubjectKind: r.String("subject_kind"),
			SubjectID: decID(r, "subject_id"), Target: r.String("target"), Environment: r.String("environment"),
			Status: r.String("status"), Version: r.String("release_version"), DeployedAt: deployed,
			ConfigHash: r.Bytes("config_hash"), Metadata: meta}, nil
	},
}

// --- AccessEdge --------------------------------------------------------------

var accessEdgeDescriptor = model.EntityDescriptor{
	Kind:  "core.access_edge",
	Table: "access_edges",
	Fields: []model.FieldSpec{
		pdecl(field("origin_kind", model.KindText, false),
			model.None("the acting node kind, agent, identity or session: core/model/accessedge.go:21, modules/access-map/bridge.go:83")),
		pdecl(field("origin_id", model.KindUUID, false),
			model.None("the agent, session or credential-identity row the bridge resolved, never an account: modules/access-map/bridge.go:93, modules/access-map/bridge.go:119, modules/access-map/bridge.go:127")),
		pdecl(field("resource_id", model.KindUUID, false),
			model.None("the accessed resource row: core/model/accessedge.go:25")),
		pdecl(field("mode", model.KindText, false),
			model.None("a read/write mode from the shared closed vocabulary: sdk/model/enums.go:19")),
		pdecl(field("signal_source", model.KindText, false),
			model.None("the collector label from the shared vocabulary: sdk/model/enums.go:47")),
		pdecl(field("confidence", model.KindText, false),
			model.None("an attribution confidence from the shared closed vocabulary: sdk/model/enums.go:127")),
		field("permitted", model.KindBool, false),
		field("observed", model.KindBool, false),
		pdecl(field("tool_id", model.KindUUID, true),
			model.None("the tool row that performed the access: core/model/accessedge.go:38")),
		pdecl(field("session_id", model.KindUUID, true),
			model.None("the session row the edge is tied to: core/model/accessedge.go:40")),
		field("first_seen", model.KindTimestamp, false),
		field("last_seen", model.KindTimestamp, false),
		field("occurrence_count", model.KindInt, false),
		// metadata keeps the connector's raw origin reference (a session id or a
		// credential name) as display evidence.
		pdecl(field("metadata", model.KindJSON, true), model.Scan(model.ClassEvidence)),
	},
	Indexes: []model.IndexSpec{{
		Name:    "access_edges_natural_key",
		Columns: []string{"tenant_id", "origin_kind", "origin_id", "resource_id", "mode"},
		Unique:  true,
	}},
}

var accessEdgeCodec = model.Codec[model.AccessEdge]{
	Base: func(e *model.AccessEdge) *model.BaseFields { return &e.BaseFields },
	Encode: func(e model.AccessEdge) (model.Record, error) {
		meta, err := encJSON(e.Metadata)
		if err != nil {
			return nil, err
		}
		return model.Record{"origin_kind": e.OriginKind, "origin_id": e.OriginID.String(),
			"resource_id": e.ResourceID.String(), "mode": string(e.Mode), "signal_source": string(e.SignalSource),
			"confidence": string(e.Confidence), "permitted": e.Permitted, "observed": e.Observed,
			"tool_id": encOptID(e.ToolID), "session_id": encOptID(e.SessionID),
			"first_seen": encTS(e.FirstSeen), "last_seen": encTS(e.LastSeen),
			"occurrence_count": e.OccurrenceCount, "metadata": meta}, nil
	},
	Decode: func(b model.BaseFields, r model.Record) (model.AccessEdge, error) {
		meta, err := decJSON(r, "metadata")
		if err != nil {
			return model.AccessEdge{}, err
		}
		first, err := decTS(r, "first_seen")
		if err != nil {
			return model.AccessEdge{}, err
		}
		last, err := decTS(r, "last_seen")
		if err != nil {
			return model.AccessEdge{}, err
		}
		return model.AccessEdge{BaseFields: b, OriginKind: r.String("origin_kind"), OriginID: decID(r, "origin_id"),
			ResourceID: decID(r, "resource_id"), Mode: sdkmodel.AccessMode(r.String("mode")),
			SignalSource: sdkmodel.SignalSource(r.String("signal_source")), Confidence: sdkmodel.Confidence(r.String("confidence")),
			Permitted: r.Bool("permitted"), Observed: r.Bool("observed"), ToolID: decID(r, "tool_id"),
			SessionID: decID(r, "session_id"), FirstSeen: first, LastSeen: last,
			OccurrenceCount: r.Int("occurrence_count"), Metadata: meta}, nil
	},
}
