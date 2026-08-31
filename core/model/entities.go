// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package model

// This file declares the core entity types of the control plane (ARCHITECTURE.md).
// Every entity embeds BaseFields (id, tenant_id, created_at, updated_at,
// version, deleted_at) which the store manages. Fields kept here are the
// identifying, relational and status attributes the engine needs; modules
// extend the model with their own entities via the registry rather than by
// growing these structs. Free-form context lives in Metadata (stored as JSON),
// never raw payloads/secrets (docs/SECURITY-HARDENING.md).

// Org is a tenant: the isolation boundary itself (ARCHITECTURE.md, module XX). Its
// TenantID equals its ID — an org owns its own row — so tenant RLS lets an org
// read only itself; provisioning and listing orgs is a System-path operation.
type Org struct {
	BaseFields
	// Name is the human-readable organization name.
	Name string
	// Slug is a short, unique, URL-safe handle.
	Slug string
	// Status is the org lifecycle state.
	Status LifecycleStatus
	// Settings is free-form tenant configuration (no secrets).
	Settings map[string]any
	// DataRegion is the residency pin: the region this tenant's control-plane
	// data must reside and be processed in (gap OPS-4). Empty means the
	// tenant is unpinned (no residency requirement). When set, a region-scoped
	// deployment denies — fail closed — any access to this tenant from an
	// instance whose home region differs (core/residency). It is a governance
	// fact, not free-form Settings, so it is a first-class column.
	DataRegion string
}

// Agent is an AI agent operating in the estate (ARCHITECTURE.md, module I).
type Agent struct {
	BaseFields
	// Name is the human-readable agent name.
	Name string
	// Kind classifies the agent (e.g. "claude-code", "mcp", "api").
	Kind string
	// ExternalID is the agent's identifier in its source system.
	ExternalID string
	// Status is the agent lifecycle state.
	Status LifecycleStatus
	// IdentityID is the non-human identity the agent runs as (may be zero).
	IdentityID ID
	// WorkspaceID is the workspace this agent is scoped to (FASE X). It is
	// OPTIONAL: a zero value means the tenant's default workspace, so an agent
	// discovered by an ingest path that does not know about workspaces is never
	// orphaned (back-compat — see Workspace). Soft-isolation: it is a scoping
	// dimension the access engine reads, not a tenancy boundary.
	WorkspaceID ID
	// RiskTier is the agent's effective governance risk tier (the hot-read
	// column for gates that scale controls by tier). It is the EFFECTIVE tier:
	// the operator's declared tier when one exists, else the governance
	// module's heuristic suggestion. The governance module is the sole writer;
	// the profile entity (agent_risk_profile) carries the full lifecycle. Empty
	// means unclassified (no tier floor applies). Valid values mirror the OWASP
	// 4-tier vocabulary: low, medium, high, critical.
	RiskTier string
	// Labels are free-form key/value tags.
	Labels map[string]any
	// Metadata is free-form, non-sensitive context.
	Metadata map[string]any
}

// Session is one live operation/run of an agent (ARCHITECTURE.md, module II).
type Session struct {
	BaseFields
	// AgentID is the agent that owns the session.
	AgentID ID
	// ExternalID is the session's identifier in its source system.
	ExternalID string
	// State is the session lifecycle state.
	State SessionState
	// Goal is the session's stated objective.
	Goal string
	// Summary is a short, non-sensitive summary of the session.
	Summary string
	// WorkspaceID is the workspace this session is scoped to (FASE X).
	// OPTIONAL: zero means the tenant's default workspace. A session normally
	// inherits its agent's workspace, but it carries its own column so an
	// agentless session (discovered from cooperative telemetry) can still be
	// scoped. Soft-isolation, like Agent.WorkspaceID.
	WorkspaceID ID
	// ModelID is the primary model used (may be zero).
	ModelID ID
	// StartedAt is when the session began.
	StartedAt Timestamp
	// EndedAt is when the session ended (nil while running).
	EndedAt *Timestamp
	// Metadata is free-form, non-sensitive context.
	Metadata map[string]any
}

// Provider is a model/inference provider (ARCHITECTURE.md, module X).
type Provider struct {
	BaseFields
	// Name is the human-readable provider name.
	Name string
	// Kind classifies the provider (e.g. "anthropic", "openai", "ollama").
	Kind string
	// BaseURL is the provider API endpoint.
	BaseURL string
	// Status is the provider lifecycle state.
	Status LifecycleStatus
	// Config is non-secret provider configuration.
	Config map[string]any
}

// Model is a model offered by a provider, with pricing for FinOps (module XI).
type Model struct {
	BaseFields
	// ProviderID is the owning provider.
	ProviderID ID
	// Name is the model name (e.g. "claude-opus-4-8").
	Name string
	// Family groups related models (e.g. "claude-opus").
	Family string
	// ContextWindow is the model's context length in tokens.
	ContextWindow int64
	// InputCostMicroUSD is the cost per input token in millionths of a USD.
	InputCostMicroUSD int64
	// OutputCostMicroUSD is the cost per output token in millionths of a USD.
	OutputCostMicroUSD int64
	// Modality describes the model's modality (e.g. "text", "vision").
	Modality string
	// Status is the model lifecycle state.
	Status LifecycleStatus
	// Metadata is free-form, non-sensitive context.
	Metadata map[string]any
}

// MCPServer is a Model Context Protocol server (ARCHITECTURE.md, module V).
type MCPServer struct {
	BaseFields
	// Name is the human-readable server name.
	Name string
	// Transport is the MCP transport (e.g. "stdio", "http").
	Transport string
	// Endpoint is the server address or command.
	Endpoint string
	// Version is the reported server version.
	Version string
	// Status is the server lifecycle state.
	Status LifecycleStatus
	// Metadata is free-form, non-sensitive context.
	Metadata map[string]any
}

// Skill is a reusable agent skill (ARCHITECTURE.md, module V).
type Skill struct {
	BaseFields
	// Name is the human-readable skill name.
	Name string
	// Source is where the skill is defined (e.g. a repo or server).
	Source string
	// Version is the skill version.
	Version string
	// MCPServerID is the server that provides the skill (may be zero).
	MCPServerID ID
	// Description is a short, non-sensitive description.
	Description string
	// Status is the skill lifecycle state.
	Status LifecycleStatus
	// Metadata is free-form, non-sensitive context.
	Metadata map[string]any
}

// Tool is a callable tool exposed to agents (ARCHITECTURE.md). The MCP annotation
// hints are stored but UNTRUSTED (ARCHITECTURE.md) — corroborated, never relied on.
type Tool struct {
	BaseFields
	// Name is the tool name.
	Name string
	// MCPServerID is the server that exposes the tool (may be zero).
	MCPServerID ID
	// Kind classifies the tool.
	Kind string
	// ReadOnlyHint is the MCP readOnlyHint annotation (untrusted).
	ReadOnlyHint bool
	// DestructiveHint is the MCP destructiveHint annotation (untrusted).
	DestructiveHint bool
	// SchemaHash is a hash of the tool input schema (for change detection).
	SchemaHash []byte
	// Description is a short, non-sensitive description.
	Description string
	// Metadata is free-form, non-sensitive context.
	Metadata map[string]any
}

// Resource is something an agent can access: a DB, server, store or API
// (ARCHITECTURE.md, module III). The R/RW map records edges to resources. A resource
// can also be a FOLDER — a container with no leaf semantics of its own (Kind
// "folder") — so resources form a tree the enterprise organizes and scopes by
// subtree (FASE X).
type Resource struct {
	BaseFields
	// Name is the human-readable resource name.
	Name string
	// Kind classifies the resource (e.g. "postgres.table", "s3.bucket",
	// "folder").
	Kind string
	// URI is the resource's natural identifier.
	URI string
	// Sensitivity is an operator-assigned sensitivity label.
	Sensitivity string
	// Owner is the responsible team/person.
	Owner string
	// WorkspaceID is the workspace this resource is scoped to (FASE X).
	// OPTIONAL: zero means the tenant's default workspace (back-compat — see
	// Workspace). Soft-isolation, like Agent.WorkspaceID.
	WorkspaceID ID
	// ParentID is the direct parent in the resource tree; zero for a root
	// resource (FASE X). Direct children are listed by ParentID; the whole
	// subtree by Path prefix.
	ParentID ID
	// Path is the MATERIALIZED path of this resource: the slash-delimited chain
	// of ancestor ids ending with the resource's own id ("/<root>/…/<self>"),
	// maintained by the store. It makes a subtree query a single indexed prefix
	// scan ("Path LIKE '<ancestor.Path>/%'") instead of a recursive walk. It is
	// store-managed: set on create from the parent and rewritten on move; a
	// caller never edits it directly (Resources().Update preserves the tree
	// structure — reparent through Move).
	Path string
	// Metadata is free-form, non-sensitive context.
	Metadata map[string]any
}

// Identity is a non-human identity (role/principal/service account) under which
// agents act (ARCHITECTURE.md, module VI). Per-agent identity is the dependency that
// makes R/RW attribution possible (ARCHITECTURE.md).
type Identity struct {
	BaseFields
	// Name is the identity name.
	Name string
	// Kind classifies the identity (e.g. "db_role", "iam_principal").
	Kind string
	// ExternalID is the identity's identifier in its source system.
	ExternalID string
	// Provider is the identity provider (e.g. "vault", "okta", "aws").
	Provider string
	// Metadata is free-form, non-sensitive context.
	Metadata map[string]any
}

// Policy is a governance/guardrail/budget policy (ARCHITECTURE.md, modules VI, IX).
type Policy struct {
	BaseFields
	// Name is the policy name.
	Name string
	// Kind classifies the policy (e.g. "rbac", "abac", "guardrail", "budget").
	Kind string
	// Spec is the policy definition.
	Spec map[string]any
	// Enabled reports whether the policy is in force.
	Enabled bool
}

// CostRecord is one model-usage cost event for FinOps (ARCHITECTURE.md, module XI).
// Money is integer micro-USD; never a float.
type CostRecord struct {
	BaseFields
	// SessionID, AgentID, ModelID, ProviderID tie the cost to its origin (any
	// may be zero).
	SessionID  ID
	AgentID    ID
	ModelID    ID
	ProviderID ID
	// OccurredAt is when the usage happened.
	OccurredAt Timestamp
	// InputTokens and OutputTokens are token counts.
	InputTokens  int64
	OutputTokens int64
	// CostMicroUSD is the cost in millionths of a USD.
	CostMicroUSD int64
	// Currency is the display currency (cost is always stored in USD micro).
	Currency string
	// Metadata is free-form, non-sensitive context.
	Metadata map[string]any
}

// EvalResult is the outcome of an eval/test run (ARCHITECTURE.md, module XII).
type EvalResult struct {
	BaseFields
	// Suite names the eval suite.
	Suite string
	// SubjectKind and SubjectID identify what was evaluated.
	SubjectKind string
	SubjectID   ID
	// Score is the numeric score (suite-defined scale).
	Score float64
	// Passed reports whether the eval passed.
	Passed bool
	// OccurredAt is when the eval ran.
	OccurredAt Timestamp
	// Metrics is free-form metric detail.
	Metrics map[string]any
	// Metadata is free-form, non-sensitive context.
	Metadata map[string]any
}

// Finding is a guardrail/red-team/forensic finding (ARCHITECTURE.md, modules IX,
// XVIII). The sensitive detail is hashed, never stored raw (docs/SECURITY-HARDENING.md).
type Finding struct {
	BaseFields
	// Kind classifies the finding (e.g. "guardrail", "redteam", "forensic").
	Kind string
	// Severity is the finding severity.
	Severity Severity
	// Status is the triage state.
	Status FindingStatus
	// Source names the detector/connector that produced it.
	Source string
	// SubjectKind and SubjectID identify what the finding is about.
	SubjectKind string
	SubjectID   ID
	// Title is a short, non-sensitive summary safe to display.
	Title string
	// DetailHash is a hash of the redacted detail (the raw detail is not kept).
	DetailHash []byte
	// OccurredAt is when the finding was produced.
	OccurredAt Timestamp
	// Metadata is free-form, non-sensitive context.
	Metadata map[string]any
}

// HealthStatus is the current health of a monitored subject (ARCHITECTURE.md,
// module XXII).
type HealthStatus struct {
	BaseFields
	// SubjectKind and SubjectID identify the monitored subject.
	SubjectKind string
	SubjectID   ID
	// State is the current health state.
	State HealthState
	// CheckedAt is when health was last checked.
	CheckedAt Timestamp
	// LatencyMS is the last observed latency in milliseconds.
	LatencyMS int64
	// Detail is a short, non-sensitive health detail.
	Detail string
	// Metadata is free-form, non-sensitive context.
	Metadata map[string]any
}

// Deployment is a deployment of an agent/MCP/module to infrastructure
// (ARCHITECTURE.md, module VII).
type Deployment struct {
	BaseFields
	// SubjectKind and SubjectID identify what was deployed.
	SubjectKind string
	SubjectID   ID
	// Target is where it was deployed (e.g. a host or cluster).
	Target string
	// Environment is the deployment environment (e.g. "prod", "staging").
	Environment string
	// Status is the deployment status.
	Status string
	// Version is the deployed version.
	Version string
	// DeployedAt is when the deployment happened.
	DeployedAt Timestamp
	// ConfigHash is a hash of the applied configuration.
	ConfigHash []byte
	// Metadata is free-form, non-sensitive context.
	Metadata map[string]any
}
