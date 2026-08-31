// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package capabilities

import (
	"context"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// Resource-kind labels carried on an edge's ResourceKind (the contract, §2.1
// and §3). They classify what capability an edge names.
const (
	rkMCPTool             = "mcp.tool"
	rkMCPServer           = "mcp.server"
	rkMCPResource         = "mcp.resource"
	rkMCPResourceTemplate = "mcp.resource_template"
	rkMCPPrompt           = "mcp.prompt"
	rkClaudeTool          = "claude.tool"
	// Declared-capability resource kinds emitted by the CLA-14 static-config feeder
	// (connectors/claude). They name a capability DECLARED in config (not observed at
	// runtime); the reactor maps them to the declared capability kinds with
	// signal_source=config so the console distinguishes declared from observed.
	rkConfigSubagent    = "config.subagent"
	rkConfigSkill       = "config.skill"
	rkConfigPlugin      = "config.plugin"
	rkConfigOutputStyle = "config.output_style"
	// settings-declared hooks + project-declared MCP servers (.mcp.json).
	rkConfigHook      = "config.hook"
	rkConfigMCPServer = "config.mcp_server"
)

// findingHealth is the connectors' health finding kind (§3): a server
// that could not be introspected, or an observed connection failure.
const findingHealth = "health"

// wEdge is one capability-connection edge to upsert: an origin connected to a
// capability. It carries only natural references (already redacted by the
// connector), never a core entity id — so it never races inventory's create.
type wEdge struct {
	originKind string
	originRef  string
	capKind    string
	capRef     string
	toolRef    string
}

// onEdge maintains the capability-connection graph (and refreshes a server's
// connection health on a fresh connection signal) from one access edge. It
// considers ONLY capability-relevant edges (MCP server/tool/resource/skill and
// Claude tools); file/http/shell/db edges are resource access and belong to
// module III's R/RW graph, not the capability wiring — this module does not
// duplicate that graph.
func (m *Module) onEdge(ctx context.Context, tenantRef string, edge sdkmodel.EdgeObservation) error {
	tenant, ok := tenantOf(tenantRef)
	if !ok {
		m.debugf("capabilities: edge for non-tenant ref; skipped", "tenant", tenantRef)
		return nil
	}
	edges, serverConnected := capabilityEdges(edge)
	if len(edges) == 0 && serverConnected == "" {
		return nil
	}
	at := edge.ObservedAt
	if at.IsZero() {
		at = m.clock.Now().Time()
	}
	source := string(edge.Source)

	return m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		for _, we := range edges {
			if err := m.upsertWiring(ctx, sc, we, source, at); err != nil {
				return err
			}
		}
		// A server connection signal recovers the server's health to connected,
		// advancing the status timestamp forward only (never fabricated: it is a
		// real observed connection). A reported problem (onFinding) sets it down.
		if serverConnected != "" {
			return m.markHealth(ctx, sc, subjMCPServer, serverConnected, connConnected, "", "", "", at)
		}
		return nil
	})
}

// capabilityEdges classifies an access edge into the capability-connection edges
// it implies, plus the server name when the edge is a server-connection signal
// (for health recovery). It is honest: an edge it cannot classify as a capability
// connection yields no wiring.
func capabilityEdges(edge sdkmodel.EdgeObservation) (edges []wEdge, serverConnected string) {
	origin, originRef := mapOrigin(edge.OriginKind, edge.OriginRef)
	if origin == "" || originRef == "" {
		return nil, ""
	}
	switch edge.ResourceKind {
	case rkMCPTool:
		server, tool := splitServerLeaf(edge.ResourceRef)
		if tool == "" {
			tool = edge.ToolRef
		}
		if tool != "" {
			// Qualify the capability ref with the server so two same-named tools on
			// different servers stay distinct nodes in the connection graph (the
			// uniqueness key is origin+capability_ref); keep the bare name as tool_ref
			// for display.
			capRef := tool
			if server != "" {
				capRef = server + "/" + tool
			}
			edges = append(edges, wEdge{origin, originRef, capTool, capRef, tool})
		}
		// The session also used the server the tool belongs to; and an mcp_server
		// origin successfully introspecting a tool is itself a connection signal.
		if server != "" && origin != originMCPServer {
			edges = append(edges, wEdge{origin, originRef, capMCPServer, server, ""})
		}
		if origin == originMCPServer {
			serverConnected = originRef
		}
	case rkMCPServer:
		if edge.ResourceRef != "" {
			edges = append(edges, wEdge{origin, originRef, capMCPServer, edge.ResourceRef, ""})
			serverConnected = edge.ResourceRef // an observed session→server connection
		}
	case rkMCPResource, rkMCPResourceTemplate:
		if edge.ResourceRef != "" {
			edges = append(edges, wEdge{origin, originRef, capResource, edge.ResourceRef, ""})
		}
		if origin == originMCPServer {
			serverConnected = originRef
		}
	case rkMCPPrompt:
		_, name := splitServerLeaf(edge.ResourceRef)
		if name == "" {
			name = edge.ResourceRef
		}
		if name != "" {
			edges = append(edges, wEdge{origin, originRef, capSkill, name, ""})
		}
		if origin == originMCPServer {
			serverConnected = originRef
		}
	case rkClaudeTool:
		tool := edge.ToolRef
		if tool == "" {
			tool = edge.ResourceRef
		}
		if tool != "" {
			edges = append(edges, wEdge{origin, originRef, capTool, tool, tool})
		}
	case rkConfigSubagent, rkConfigSkill, rkConfigPlugin, rkConfigOutputStyle,
		rkConfigHook, rkConfigMCPServer:
		// CLA-14: a capability DECLARED in static config (signal_source=config). The
		// connector classifies only the SURFACE (the ResourceKind); the reactor maps it
		// to the final capability kind (the AGPL classification boundary). The ref is the
		// capability's declared name — structural metadata only, never a prompt/body.
		if name := edge.ResourceRef; name != "" {
			edges = append(edges, wEdge{origin, originRef, declaredCapabilityKind(edge.ResourceKind), name, ""})
		}
	default:
		// file / http.url / shell / web.search / agent.task / unknown → resource
		// access, not a capability connection. Skipped (owns that graph).
	}
	return edges, serverConnected
}

// mapOrigin normalizes an edge's origin kind to a capability-wiring origin, or ""
// when the origin is not a capability consumer/owner.
func mapOrigin(kind, ref string) (string, string) {
	switch kind {
	case "session":
		return originSession, ref
	case "agent":
		return originAgent, ref
	case "mcp_server":
		return originMCPServer, ref
	case "workspace":
		// The config SCOPE that declares a capability (CLA-14): a workspace/project.
		return originWorkspace, ref
	default:
		return "", ""
	}
}

// declaredCapabilityKind maps a CLA-14 config ResourceKind to the capability kind it
// declares. Skills reuse the existing capSkill so a Skill seen both DECLARED (config)
// and EXECUTING (an mcp.prompt at runtime) collapses onto one capability node with two
// signal sources, exactly the declared-vs-observed view asks for.
func declaredCapabilityKind(resourceKind string) string {
	switch resourceKind {
	case rkConfigSubagent:
		return capSubagent
	case rkConfigPlugin:
		return capPlugin
	case rkConfigOutputStyle:
		return capOutputStyle
	case rkConfigHook:
		return capHook
	case rkConfigMCPServer:
		// Collapses onto the runtime mcp_server kind: a server DECLARED in
		// .mcp.json and one OBSERVED connecting are the same capability node,
		// distinguished by signal_source (config vs otel) —.
		return capMCPServer
	default: // rkConfigSkill
		return capSkill
	}
}

// splitServerLeaf splits an MCP "server/leaf" reference on the first slash; a
// reference with no slash has no server.
func splitServerLeaf(ref string) (server, leaf string) {
	if i := strings.IndexByte(ref, '/'); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	return "", ref
}

// upsertWiring records or refreshes one capability-connection edge: it bumps
// last-seen and occurrence, unions the discovering signal source, and sets the
// tool ref when known. It is idempotent — a redelivered observation merges — which
// makes the graph safe under at-least-once delivery.
func (m *Module) upsertWiring(ctx context.Context, sc store.Scope, we wEdge, source string, at time.Time) error {
	repo, err := sc.Ext(wiringKind)
	if err != nil {
		return err
	}
	atTS := model.NewTimestamp(at).String()
	existing, _, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{
			eq(colOriginKind, we.originKind),
			eq(colOriginRef, we.originRef),
			eq(colCapabilityKind, we.capKind),
			eq(colCapabilityRef, we.capRef),
		},
		Limit: 1,
	})
	if err != nil {
		return err
	}
	if len(existing) == 0 {
		_, err := repo.Create(ctx, model.Record{
			colOriginKind:     we.originKind,
			colOriginRef:      we.originRef,
			colCapabilityKind: we.capKind,
			colCapabilityRef:  we.capRef,
			colToolRef:        we.toolRef,
			colSignalSources:  marshalSet(addToSet(nil, source)),
			colFirstSeen:      atTS,
			colLastSeen:       atTS,
			colOccurrence:     int64(1),
		})
		if err != nil && isConflict(err) {
			return nil // a redelivered create raced the unique index; merge next time
		}
		return err
	}
	rec := existing[0]
	if we.toolRef != "" {
		rec[colToolRef] = we.toolRef
	}
	rec[colSignalSources] = marshalSet(addToSet(parseSet(rec.String(colSignalSources)), source))
	if cur := rec.String(colLastSeen); cur == "" || cur < atTS {
		rec[colLastSeen] = atTS
	}
	rec[colOccurrence] = rec.Int(colOccurrence) + 1
	_, err = repo.Update(ctx, rec)
	return err
}

// onFinding records a capability's connection health from a connector health
// finding (§3). The connectors emit a health finding only on a PROBLEM
// (a server that cannot be introspected, an observed connection failure), so a
// health finding sets the subject down/degraded by severity. Anti-evasion
// findings are a security signal (module IX), not capability health, and are
// not handled here.
func (m *Module) onFinding(ctx context.Context, tenantRef string, f sdkmodel.FindingReport) error {
	if f.Kind != findingHealth {
		return nil
	}
	tenant, ok := tenantOf(tenantRef)
	if !ok {
		return nil
	}
	subjKind, subjRef := mapSubject(f.SubjectKind, f.SubjectRef)
	if subjRef == "" {
		return nil
	}
	at := f.OccurredAt
	if at.IsZero() {
		at = m.clock.Now().Time()
	}
	status := connDown
	if f.Severity == sdkmodel.SeverityLow || f.Severity == sdkmodel.SeverityInfo {
		status = connDegraded
	}
	return m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		return m.markHealth(ctx, sc, subjKind, subjRef, status, string(f.Severity), f.Title, f.DetailHash, at)
	})
}

// mapSubject normalizes a finding's subject to a capability-health subject. The
// connectors label an MCP server's subject "mcp.server"; a session subject
// is "session".
func mapSubject(kind, ref string) (string, string) {
	switch kind {
	case rkMCPServer, subjMCPServer:
		return subjMCPServer, ref
	case "session":
		return subjSession, ref
	case "skill":
		return subjSkill, ref
	default:
		return kind, ref
	}
}

// markHealth upserts the connection-health overlay for one subject, advancing the
// status timestamp FORWARD ONLY so a stale signal never overrides a newer state
// (a recovered "connected" edge and a "down" finding resolve to whichever is more
// recent — never fabricated). An empty status string means "no change to status"
// is not used; callers always pass a concrete status.
func (m *Module) markHealth(ctx context.Context, sc store.Scope, subjKind, subjRef, status, severity, title, detailHash string, at time.Time) error {
	repo, err := sc.Ext(healthKind)
	if err != nil {
		return err
	}
	atTS := model.NewTimestamp(at).String()
	existing, _, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{eq(colSubjectKind, subjKind), eq(colSubjectRef, subjRef)},
		Limit:   1,
	})
	if err != nil {
		return err
	}
	if len(existing) == 0 {
		_, err := repo.Create(ctx, model.Record{
			colSubjectKind: subjKind,
			colSubjectRef:  subjRef,
			colStatus:      status,
			colSeverity:    severity,
			colLastTitle:   title,
			colDetailHash:  detailHash,
			colStatusAt:    atTS,
			colOccurrence:  int64(1),
		})
		if err != nil && isConflict(err) {
			return nil
		}
		return err
	}
	rec := existing[0]
	rec[colOccurrence] = rec.Int(colOccurrence) + 1
	// Forward-only: only a signal at least as recent as the stored one changes the
	// status, so an out-of-order stale event cannot resurrect an old state.
	if cur := rec.String(colStatusAt); cur == "" || cur <= atTS {
		rec[colStatus] = status
		rec[colSeverity] = severity
		rec[colLastTitle] = title
		rec[colDetailHash] = detailHash
		rec[colStatusAt] = atTS
	}
	_, err = repo.Update(ctx, rec)
	return err
}
