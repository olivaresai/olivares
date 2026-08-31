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
	// K3's fenced directory: one mutable tenant epoch and two append-only,
	// retained retirement-evidence tables. Their engine-owned operations are
	// deliberately separate from the ordinary typed repositories.
	ds = append(ds, directoryDescriptors()...)
	// Authorization generation is a separate tenant-local fact. Keeping it out
	// of directoryDescriptors avoids claiming that directory-v7's specialized
	// guard receipt covers a relation it was never designed to attest.
	ds = append(ds, authorizationEpochDescriptor)
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
		field("name", model.KindText, false),
		field("slug", model.KindText, false),
		field("status", model.KindText, false),
		field("settings", model.KindJSON, true),
		// data_region is the residency pin (OPS-4). Nullable so it is added
		// additively to an already-migrated orgs table by reconcileColumns;
		// empty/NULL means the tenant is unpinned. It is indexed so a region-scoped
		// instance can enumerate its resident tenants cheaply at boot.
		indexedField("data_region", model.KindText, true),
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

// --- Agent -------------------------------------------------------------------

var agentDescriptor = model.EntityDescriptor{
	Kind:                   "core.agent",
	Table:                  "agents",
	SoftDelete:             true,
	AuthorizationFact:      true,
	AuthorizationLockOrder: 20,
	Fields: []model.FieldSpec{
		field("name", model.KindText, false),
		indexedField("kind", model.KindText, false),
		field("external_id", model.KindText, true),
		field("status", model.KindText, false),
		field("identity_id", model.KindUUID, true),
		field("labels", model.KindJSON, true),
		field("metadata", model.KindJSON, true),
		// workspace_id is the FASE X scoping dimension. Nullable and
		// appended last so it is added additively to an already-migrated agents
		// table by reconcileColumns; NULL resolves to the tenant's default
		// workspace (back-compat).
		indexedField("workspace_id", model.KindUUID, true),
		// risk_tier is the agent's effective governance risk tier.
		// Nullable; reconcileColumns adds it additively. The governance module
		// is the sole writer; empty means unclassified.
		indexedField("risk_tier", model.KindText, true),
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
		indexedField("agent_id", model.KindUUID, true),
		field("external_id", model.KindText, true),
		field("state", model.KindText, false),
		field("goal", model.KindText, true),
		field("summary", model.KindText, true),
		field("model_id", model.KindUUID, true),
		field("started_at", model.KindTimestamp, false),
		field("ended_at", model.KindTimestamp, true),
		field("metadata", model.KindJSON, true),
		// workspace_id is the FASE X scoping dimension. Nullable, appended
		// last for additive reconcile; NULL resolves to the default workspace.
		indexedField("workspace_id", model.KindUUID, true),
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
		field("name", model.KindText, false),
		indexedField("kind", model.KindText, false),
		field("base_url", model.KindText, true),
		field("status", model.KindText, false),
		field("config", model.KindJSON, true),
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
		indexedField("provider_id", model.KindUUID, false),
		field("name", model.KindText, false),
		field("family", model.KindText, true),
		field("context_window", model.KindInt, false),
		field("input_cost_micro_usd", model.KindInt, false),
		field("output_cost_micro_usd", model.KindInt, false),
		field("modality", model.KindText, true),
		field("status", model.KindText, false),
		field("metadata", model.KindJSON, true),
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
		field("name", model.KindText, false),
		field("transport", model.KindText, false),
		field("endpoint", model.KindText, true),
		field("server_version", model.KindText, true),
		field("status", model.KindText, false),
		field("metadata", model.KindJSON, true),
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
		field("name", model.KindText, false),
		field("source", model.KindText, true),
		field("skill_version", model.KindText, true),
		field("mcp_server_id", model.KindUUID, true),
		field("description", model.KindText, true),
		field("status", model.KindText, false),
		field("metadata", model.KindJSON, true),
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
		field("name", model.KindText, false),
		field("mcp_server_id", model.KindUUID, true),
		field("kind", model.KindText, true),
		field("read_only_hint", model.KindBool, false),
		field("destructive_hint", model.KindBool, false),
		field("schema_hash", model.KindBytes, true),
		field("description", model.KindText, true),
		field("metadata", model.KindJSON, true),
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
		field("name", model.KindText, false),
		indexedField("kind", model.KindText, false),
		field("uri", model.KindText, true),
		field("sensitivity", model.KindText, true),
		field("owner", model.KindText, true),
		field("metadata", model.KindJSON, true),
		// FASE X scoping + hierarchy columns. All nullable and appended
		// last for additive reconcile. workspace_id NULL resolves to the default
		// workspace; parent_id NULL is a tree root; path is the store-maintained
		// materialized path, indexed so a subtree is one prefix scan.
		indexedField("workspace_id", model.KindUUID, true),
		indexedField("parent_id", model.KindUUID, true),
		indexedField("path", model.KindText, true),
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
		field("name", model.KindText, false),
		indexedField("kind", model.KindText, false),
		field("external_id", model.KindText, true),
		field("provider", model.KindText, true),
		field("metadata", model.KindJSON, true),
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
		field("name", model.KindText, false),
		indexedField("kind", model.KindText, false),
		field("spec", model.KindJSON, true),
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
		field("session_id", model.KindUUID, true),
		field("agent_id", model.KindUUID, true),
		field("model_id", model.KindUUID, true),
		field("provider_id", model.KindUUID, true),
		indexedField("occurred_at", model.KindTimestamp, false),
		field("input_tokens", model.KindInt, false),
		field("output_tokens", model.KindInt, false),
		field("cost_micro_usd", model.KindInt, false),
		field("currency", model.KindText, true),
		field("metadata", model.KindJSON, true),
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
		indexedField("suite", model.KindText, false),
		field("subject_kind", model.KindText, false),
		field("subject_id", model.KindUUID, false),
		field("score", model.KindFloat, false),
		field("passed", model.KindBool, false),
		field("occurred_at", model.KindTimestamp, false),
		field("metrics", model.KindJSON, true),
		field("metadata", model.KindJSON, true),
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
		indexedField("kind", model.KindText, false),
		field("severity", model.KindText, false),
		indexedField("status", model.KindText, false),
		field("source", model.KindText, true),
		field("subject_kind", model.KindText, true),
		field("subject_id", model.KindUUID, true),
		field("title", model.KindText, false),
		field("detail_hash", model.KindBytes, true),
		field("occurred_at", model.KindTimestamp, false),
		field("metadata", model.KindJSON, true),
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
		field("subject_kind", model.KindText, false),
		field("subject_id", model.KindUUID, false),
		field("state", model.KindText, false),
		field("checked_at", model.KindTimestamp, false),
		field("latency_ms", model.KindInt, false),
		field("detail", model.KindText, true),
		field("metadata", model.KindJSON, true),
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
		field("subject_kind", model.KindText, false),
		field("subject_id", model.KindUUID, false),
		field("target", model.KindText, true),
		indexedField("environment", model.KindText, true),
		field("status", model.KindText, false),
		field("release_version", model.KindText, true),
		field("deployed_at", model.KindTimestamp, false),
		field("config_hash", model.KindBytes, true),
		field("metadata", model.KindJSON, true),
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
		field("origin_kind", model.KindText, false),
		field("origin_id", model.KindUUID, false),
		field("resource_id", model.KindUUID, false),
		field("mode", model.KindText, false),
		field("signal_source", model.KindText, false),
		field("confidence", model.KindText, false),
		field("permitted", model.KindBool, false),
		field("observed", model.KindBool, false),
		field("tool_id", model.KindUUID, true),
		field("session_id", model.KindUUID, true),
		field("first_seen", model.KindTimestamp, false),
		field("last_seen", model.KindTimestamp, false),
		field("occurrence_count", model.KindInt, false),
		field("metadata", model.KindJSON, true),
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
