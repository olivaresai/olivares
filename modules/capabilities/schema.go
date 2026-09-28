// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package capabilities

import (
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Owned entity kinds and their physical tables.
const (
	configKind    model.Kind = "capabilities.mcp_config"
	configTable              = "capabilities_mcp_config"
	revisionKind  model.Kind = "capabilities.config_revision"
	revisionTable            = "capabilities_config_revision"
	wiringKind    model.Kind = "capabilities.wiring"
	wiringTable              = "capabilities_wiring"
	healthKind    model.Kind = "capabilities.health"
	healthTable              = "capabilities_health"
)

// Capability-connection origin kinds (the "who/what is connected").
const (
	originSession   = "session"
	originAgent     = "agent"
	originMCPServer = "mcp_server"
	// originWorkspace is the config SCOPE that DECLARES a capability (CLA-14): the
	// workspace/project whose static config tree wires a subagent/Skill/plugin/output-
	// style. It is the origin of a declared (signal_source=config) edge, distinct from
	// a session/agent that EXECUTED a capability at runtime.
	originWorkspace = "workspace"
)

// Capability kinds (the "to what" of a wiring edge). The first four are populated by
// runtime observation (a session/agent using an MCP server/tool/skill/resource); the
// declared kinds below are populated by the CLA-14 static-config feeder (a workspace
// that DECLARES a subagent/plugin/output-style — skills reuse capSkill). The console
// distinguishes the two by signal_source (config vs otel/mcp_annotation/...), not by
// kind, so a capability seen both declared AND executing is one node with two sources.
const (
	capMCPServer = "mcp_server"
	capTool      = "tool"
	capSkill     = "skill"
	capResource  = "resource"
	// Declared-capability kinds (CLA-14). New VALUES in the existing capability_kind
	// column — no schema change; additive and coordinated with the data model.
	capSubagent    = "subagent"
	capPlugin      = "plugin"
	capOutputStyle = "output_style"
	// (2.1.17x parity): a hook EVENT declared in settings.json (arbitrary
	// command/prompt execution wired into the agent loop — supply-chain-relevant
	// declared capability). Declared MCP servers (.mcp.json) reuse capMCPServer so
	// a server seen both DECLARED and CONNECTED collapses onto one node with two
	// signal sources — the declared-vs-observed diff asks for.
	capHook = "hook"
)

// Health subject kinds.
const (
	subjMCPServer = "mcp_server"
	subjSkill     = "skill"
	subjSession   = "session"
)

// Derived connection states (never stored on a positive edge alone; the health
// row stores the last reported PROBLEM, and the catalog derives "connected" when
// a fresher connection signal exists — honest, read-time, never fabricated).
const (
	connConnected = "connected"
	connDegraded  = "degraded"
	connDown      = "down"
	connUnknown   = "unknown"
)

// mcp_config columns.
const (
	colServerRef   = "server_ref"
	colTransport   = "transport"
	colEndpointRef = "endpoint_ref"
	colScope       = "scope"
	colSecretRefs  = "secret_refs"
	colEnabled     = "enabled"
	colNote        = "note"
	colRevision    = "revision"
)

// config_revision columns (a snapshot plus who changed it; reuses the config
// column names above for the snapshot fields).
const (
	colChangeActor  = "change_actor"
	colChangeAction = "change_action"
	colChangedAt    = "changed_at"
)

// wiring columns.
const (
	colOriginKind     = "origin_kind"
	colOriginRef      = "origin_ref"
	colCapabilityKind = "capability_kind"
	colCapabilityRef  = "capability_ref"
	colToolRef        = "tool_ref"
	colSignalSources  = "signal_sources"
	colFirstSeen      = "first_seen"
	colLastSeen       = "last_seen"
	colOccurrence     = "occurrence_count"
)

// health columns.
const (
	colSubjectKind = "subject_kind"
	colSubjectRef  = "subject_ref"
	colStatus      = "status"
	colSeverity    = "severity"
	colLastTitle   = "last_title"
	colDetailHash  = "detail_hash"
	colStatusAt    = "status_at"
)

// Principal declarations of the text and JSON columns above: what each stored
// value says about accounts, with the reader lines that show it.
var (
	// Managed config and its revision snapshot.
	pdeclNoneServerRef = model.None("an MCP server name, matched only against inventory server names: servers.go:153, servers.go:223")
	pdeclNoneTransport = model.None("a transport from a closed set: config.go:29, config.go:70")
	pdeclNoneEndpoint  = model.None("an MCP endpoint locator, refused when it carries inline credentials and only rendered: config.go:74, config.go:179, config.go:476")
	pdeclNoneScope     = model.None("an operator-set scope label, stored and rendered only: config.go:180, config.go:477")
	pdeclNoneNote      = model.None("operator prose, rendered only: config.go:181, config.go:478")
	pdeclSecretRefs    = model.Nested([]secretRefDTO(nil), model.ClassEvidence,
		model.Leaf("[].name", model.None("the logical name of a referenced credential, decoded only for display: config.go:152, config.go:180")),
		model.Leaf("[].ref_kind", model.None("where a credential lives, from a closed set: config.go:33, config.go:88")),
		model.Leaf("[].ref", model.None("a locator of where a credential lives, refused when it holds the value and never resolved: config.go:97, config.go:180")),
		model.Leaf("[].hint", model.None("a short masked partial of a credential, length-bounded and rendered only: config.go:94, config.go:180")),
	)
	pdeclNoneChangeAction = model.None("the change verb, one of create, update or delete: config.go:284, config.go:334, config.go:369")

	// Wiring edges.
	pdeclNoneOriginKind     = model.None("an origin kind from a closed set: reactor.go:175")
	pdeclNoneOriginRef      = model.None("a session, agent, MCP server or workspace reference (identity origins are dropped), rendered as a graph node: reactor.go:175, wiring.go:150")
	pdeclNoneCapabilityKind = model.None("a capability kind from a closed set: reactor.go:101, reactor.go:195")
	pdeclNoneCapabilityRef  = model.None("an MCP server, tool, resource, skill or declared capability name: reactor.go:101, wiring.go:128")
	pdeclNoneToolRef        = model.None("a tool name, rendered only: reactor.go:116, reactor.go:155, wiring.go:35")
	pdeclSignalSources      = model.Nested([]string(nil), model.ClassEvidence,
		model.Leaf("[]", model.None("the name of the collector that produced an edge, rendered only: reactor.go:74, sdk/model/enums.go:47, wiring.go:36")),
	)

	// Health overlay.
	pdeclNoneSubjectKind = model.None("the kind label of a health finding's subject, used only as a lookup filter: servers.go:339, servers.go:393")
	// A health finding's subject kind is passed through unchanged (reactor.go:310),
	// so its reference may be any reported string and is matched against aliases.
	pdeclScanSubjectRef = model.Scan(model.ClassEvidence)
	pdeclNoneStatus     = model.None("a connection status from a closed set: reactor.go:86, reactor.go:298, servers.go:418")
	pdeclNoneSeverity   = model.None("a finding severity from a closed set: sdk/model/enums.go:153, reactor.go:303")
	pdeclNoneLastTitle  = model.None("a short display summary of a finding, rendered only: sdk/model/observation.go:239, servers.go:104")
	pdeclNoneDetailHash = model.None("a SHA-256 digest of a redacted finding detail, rendered only: connectors/mcp/mcp.go:251, servers.go:104")
)

// RegisterSchema declares the module's owned entities. It satisfies the
// engine-side runtime.SchemaProvider seam (structural — no runtime import) and is
// called once, at store construction, before any Scope exists (S02 §7 /).
// The engine creates the tables, injects the base columns and attaches the tenant
// guards; a module cannot opt out of isolation.
//
// mcp_config is MINIMAL-DATA by construction: it has no column that could hold a
// usable credential — secret_refs holds REFERENCES (a logical name + a locator +
// an optional masked hint), never values, and the endpoint is a reference the
// create/update handler refuses to accept with inline credentials (docs/SECURITY-HARDENING.md).
//
// config_revision is APPEND-ONLY: the version history is immutable by the engine's
// guards (docs/SECURITY-HARDENING.md), so a config's lineage cannot be silently rewritten.
//
// Neither wiring nor health is descriptor-audited: both are high-frequency
// automated ingestion (like inventory's catalog overlay), and the catalog reads
// are RBAC-gated. The privileged, self-audited mutations are the config handlers,
// which append a semantic audit attributed to the real principal (config.go).
func (m *Module) RegisterSchema(reg store.ExtensionRegistry) error {
	if err := reg.Register(model.EntityDescriptor{
		Kind:  configKind,
		Table: configTable,
		Fields: []model.FieldSpec{
			{Name: colServerRef, Kind: model.KindText, Indexed: true, Principal: pdeclNoneServerRef},
			{Name: colTransport, Kind: model.KindText, Principal: pdeclNoneTransport},
			{Name: colEndpointRef, Kind: model.KindText, Nullable: true, Principal: pdeclNoneEndpoint},
			{Name: colScope, Kind: model.KindText, Nullable: true, Principal: pdeclNoneScope},
			{Name: colSecretRefs, Kind: model.KindJSON, Nullable: true, Principal: pdeclSecretRefs},
			{Name: colEnabled, Kind: model.KindBool},
			{Name: colNote, Kind: model.KindText, Nullable: true, Principal: pdeclNoneNote},
			{Name: colRevision, Kind: model.KindInt},
		},
		Indexes: []model.IndexSpec{{
			// One managed config per MCP server. Unique index leads with tenant_id
			// so it cannot couple tenants or leak existence.
			Name:    "capabilities_mcp_config_uniq",
			Columns: []string{model.ColTenantID, colServerRef},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	if err := reg.Register(model.EntityDescriptor{
		Kind:       revisionKind,
		Table:      revisionTable,
		AppendOnly: true, // immutable version history (docs/SECURITY-HARDENING.md)
		Fields: []model.FieldSpec{
			{Name: colServerRef, Kind: model.KindText, Indexed: true, Principal: pdeclNoneServerRef},
			{Name: colRevision, Kind: model.KindInt},
			{Name: colTransport, Kind: model.KindText, Principal: pdeclNoneTransport},
			{Name: colEndpointRef, Kind: model.KindText, Nullable: true, Principal: pdeclNoneEndpoint},
			{Name: colScope, Kind: model.KindText, Nullable: true, Principal: pdeclNoneScope},
			{Name: colSecretRefs, Kind: model.KindJSON, Nullable: true, Principal: pdeclSecretRefs},
			{Name: colEnabled, Kind: model.KindBool},
			{Name: colNote, Kind: model.KindText, Nullable: true, Principal: pdeclNoneNote},
			{Name: colChangeActor, Kind: model.KindText, Principal: model.Ref(model.EncodeUserRef, model.ClassEvidence)},
			{Name: colChangeAction, Kind: model.KindText, Principal: pdeclNoneChangeAction},
			{Name: colChangedAt, Kind: model.KindTimestamp},
		},
		Indexes: []model.IndexSpec{{
			Name:    "capabilities_config_revision_uniq",
			Columns: []string{model.ColTenantID, colServerRef, colRevision},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	if err := reg.Register(model.EntityDescriptor{
		Kind:  wiringKind,
		Table: wiringTable,
		Fields: []model.FieldSpec{
			{Name: colOriginKind, Kind: model.KindText, Indexed: true, Principal: pdeclNoneOriginKind},
			{Name: colOriginRef, Kind: model.KindText, Indexed: true, Principal: pdeclNoneOriginRef},
			{Name: colCapabilityKind, Kind: model.KindText, Indexed: true, Principal: pdeclNoneCapabilityKind},
			{Name: colCapabilityRef, Kind: model.KindText, Indexed: true, Principal: pdeclNoneCapabilityRef},
			{Name: colToolRef, Kind: model.KindText, Nullable: true, Principal: pdeclNoneToolRef},
			{Name: colSignalSources, Kind: model.KindJSON, Nullable: true, Principal: pdeclSignalSources},
			{Name: colFirstSeen, Kind: model.KindTimestamp},
			{Name: colLastSeen, Kind: model.KindTimestamp, Indexed: true},
			{Name: colOccurrence, Kind: model.KindInt},
		},
		Indexes: []model.IndexSpec{{
			// One wiring edge per (origin, capability). Unique index leads with
			// tenant_id.
			Name:    "capabilities_wiring_uniq",
			Columns: []string{model.ColTenantID, colOriginKind, colOriginRef, colCapabilityKind, colCapabilityRef},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	return reg.Register(model.EntityDescriptor{
		Kind:  healthKind,
		Table: healthTable,
		Fields: []model.FieldSpec{
			{Name: colSubjectKind, Kind: model.KindText, Indexed: true, Principal: pdeclNoneSubjectKind},
			{Name: colSubjectRef, Kind: model.KindText, Indexed: true, Principal: pdeclScanSubjectRef},
			{Name: colStatus, Kind: model.KindText, Indexed: true, Principal: pdeclNoneStatus},
			{Name: colSeverity, Kind: model.KindText, Nullable: true, Principal: pdeclNoneSeverity},
			{Name: colLastTitle, Kind: model.KindText, Nullable: true, Principal: pdeclNoneLastTitle},
			{Name: colDetailHash, Kind: model.KindText, Nullable: true, Principal: pdeclNoneDetailHash},
			{Name: colStatusAt, Kind: model.KindTimestamp},
			{Name: colOccurrence, Kind: model.KindInt},
		},
		Indexes: []model.IndexSpec{{
			Name:    "capabilities_health_uniq",
			Columns: []string{model.ColTenantID, colSubjectKind, colSubjectRef},
			Unique:  true,
		}},
	})
}
