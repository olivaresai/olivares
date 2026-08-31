// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package capabilities

import (
	"context"
	"encoding/hex"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// annotationTrustUntrusted is the constant trust label every tool projection
// carries. The MCP spec says clients MUST treat tool annotations as untrusted
// (ARCHITECTURE.md); this module surfaces readOnlyHint/destructiveHint as a signal
// flagged untrusted, never as truth. The label is loud and machine-readable so
// the UI cannot accidentally render a hint as authoritative.
const annotationTrustUntrusted = "untrusted"

// serverDTO is one MCP server in the live management catalog: its core identity
// plus the management overlays (derived connection health, managed config marker,
// tool count).
type serverDTO struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Transport  string `json:"transport"`
	Endpoint   string `json:"endpoint,omitempty"`
	Version    string `json:"version,omitempty"`
	Status     string `json:"status"`
	Connection string `json:"connection"` // derived: connected/degraded/down/unknown
	ToolCount  int    `json:"tool_count"`
	HasConfig  bool   `json:"has_config"`
	ConfigRev  int64  `json:"config_revision,omitempty"`
}

// toolDTO is a tool exposed to agents. Its annotation hints are surfaced with an
// explicit UNTRUSTED trust label (ARCHITECTURE.md) — a declared signal, never truth.
type toolDTO struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Kind            string `json:"kind,omitempty"`
	MCPServerID     string `json:"mcp_server_id,omitempty"`
	ReadOnlyHint    bool   `json:"read_only_hint"`
	DestructiveHint bool   `json:"destructive_hint"`
	AnnotationTrust string `json:"annotation_trust"`
	SchemaHash      string `json:"schema_hash,omitempty"`
}

func toToolDTO(t model.Tool) toolDTO {
	return toolDTO{
		ID: t.ID.String(), Name: t.Name, Kind: t.Kind,
		MCPServerID:     optID(t.MCPServerID),
		ReadOnlyHint:    t.ReadOnlyHint,
		DestructiveHint: t.DestructiveHint,
		AnnotationTrust: annotationTrustUntrusted,
		SchemaHash:      hex.EncodeToString(t.SchemaHash),
	}
}

// skillDTO is a reusable skill/prompt: the JSON surface of the skill catalog.
// Skills are materialized in the inventory; the live feeder is MCP server
// introspection (the rkMCPPrompt edges the mcp connector emits). A Claude-Code
// config feeder (subagents/Agent-Skills/plugins/output-styles, Source="claude-
// code:*") is available on-demand (configure the claude-config source); the
// catalog reads whatever the store holds and does not itself assume any
// particular feeder is wired.
type skillDTO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Source      string `json:"source,omitempty"`
	Version     string `json:"version,omitempty"`
	MCPServerID string `json:"mcp_server_id,omitempty"`
	Status      string `json:"status"`
	Description string `json:"description,omitempty"`
}

func toSkillDTO(s model.Skill) skillDTO {
	return skillDTO{
		ID: s.ID.String(), Name: s.Name, Source: s.Source, Version: s.Version,
		MCPServerID: optID(s.MCPServerID), Status: string(s.Status), Description: s.Description,
	}
}

// healthDTO is the basic connection-health overlay of a capability. The formal
// health/SLA module is this is the last observed connection signal.
type healthDTO struct {
	Status          string `json:"status"`
	Severity        string `json:"severity,omitempty"`
	LastTitle       string `json:"last_title,omitempty"`
	DetailHash      string `json:"detail_hash,omitempty"`
	StatusAt        string `json:"status_at"`
	OccurrenceCount int64  `json:"occurrence_count"`
}

func toHealthDTO(rec model.Record) healthDTO {
	return healthDTO{
		Status: rec.String(colStatus), Severity: rec.String(colSeverity),
		LastTitle: rec.String(colLastTitle), DetailHash: rec.String(colDetailHash),
		StatusAt: rec.String(colStatusAt), OccurrenceCount: rec.Int(colOccurrence),
	}
}

// serverDetailDTO is the full management view of one MCP server: core identity,
// managed config (secrets masked), health, the tools/skills/resources it exposes
// (with UNTRUSTED annotations) and the origins wired to it.
type serverDetailDTO struct {
	serverDTO
	Config    *configDTO      `json:"config,omitempty"`
	Health    *healthDTO      `json:"health,omitempty"`
	Tools     []toolDTO       `json:"tools"`
	Skills    []skillDTO      `json:"skills"`
	Resources []string        `json:"resources"`
	Consumers []wiringPeerDTO `json:"consumers"`
	Notes     map[string]any  `json:"notes,omitempty"`
}

// handleListServers lists the live MCP-server catalog: every discovered MCPServer
// core entity enriched with its derived connection health, managed-config marker
// and tool count. It computes the overlays with a few bulk reads (the store has
// no join), grouping in memory over the bounded page.
func (m *Module) handleListServers(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	out := listResponse[serverDTO]{Items: []serverDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		servers, page, err := sc.MCPServers().List(r.Context(), listQuery(r))
		if err != nil {
			return err
		}
		toolsByServer, err := toolCountByServer(r.Context(), sc)
		if err != nil {
			return err
		}
		healthByName, err := m.healthByRef(r.Context(), sc, subjMCPServer)
		if err != nil {
			return err
		}
		configByName, err := m.configByServer(r.Context(), sc)
		if err != nil {
			return err
		}
		for _, s := range servers {
			d := serverDTO{
				ID: s.ID.String(), Name: s.Name, Transport: s.Transport,
				Endpoint: s.Endpoint, Version: s.Version, Status: string(s.Status),
				Connection: connFromHealth(healthByName[s.Name]),
				ToolCount:  toolsByServer[s.ID.String()],
			}
			if cfg, ok := configByName[s.Name]; ok {
				d.HasConfig = true
				d.ConfigRev = cfg.Int(colRevision)
			}
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

// handleGetServer returns the full management view of one MCP server.
func (m *Module) handleGetServer(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var (
		out   serverDetailDTO
		found bool
	)
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		s, err := sc.MCPServers().Get(r.Context(), id)
		if err != nil {
			return err
		}
		found = true
		out.serverDTO = serverDTO{
			ID: s.ID.String(), Name: s.Name, Transport: s.Transport,
			Endpoint: s.Endpoint, Version: s.Version, Status: string(s.Status),
		}
		out.Tools, out.Skills = []toolDTO{}, []skillDTO{}
		out.Resources, out.Consumers = []string{}, []wiringPeerDTO{}

		// Tools and skills the server exposes (authoritative core entities, linked
		// by mcp_server_id which inventory stamps).
		tools, _, err := sc.Tools().List(r.Context(), model.Query{Filters: []model.Filter{eq("mcp_server_id", id.String())}, Limit: listCap})
		if err != nil {
			return err
		}
		for _, t := range tools {
			out.Tools = append(out.Tools, toToolDTO(t))
		}
		out.ToolCount = len(out.Tools)
		skills, _, err := sc.Skills().List(r.Context(), model.Query{Filters: []model.Filter{eq("mcp_server_id", id.String())}, Limit: listCap})
		if err != nil {
			return err
		}
		for _, sk := range skills {
			out.Skills = append(out.Skills, toSkillDTO(sk))
		}

		// Resources the server exposes + the origins wired to it (the capability
		// connection graph; min-data natural refs).
		out.Resources, err = m.serverResources(r.Context(), sc, s.Name)
		if err != nil {
			return err
		}
		out.Consumers, err = m.serverConsumers(r.Context(), sc, s.Name)
		if err != nil {
			return err
		}

		// Managed config (secrets masked) and connection health.
		if cfg, ok, err := m.lookupConfig(r.Context(), sc, s.Name); err != nil {
			return err
		} else if ok {
			d := toConfigDTO(cfg)
			out.Config = &d
			out.HasConfig = true
			out.ConfigRev = cfg.Int(colRevision)
		}
		if hrec, ok, err := m.lookupHealth(r.Context(), sc, subjMCPServer, s.Name); err != nil {
			return err
		} else if ok {
			h := toHealthDTO(hrec)
			out.Health = &h
			out.Connection = h.Status
		} else {
			out.Connection = connUnknown
		}
		out.Notes = map[string]any{
			"annotations": "tool readOnlyHint/destructiveHint are UNTRUSTED declared hints (docs/05 §6), shown as signal, never as truth",
		}
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

// handleListSkills lists the discovered skills (and the surface through which
// plugins/subagents appear), optionally filtered by ?server_id.
func (m *Module) handleListSkills(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if sid := r.URL.Query().Get("server_id"); sid != "" {
		q.Filters = append(q.Filters, eq("mcp_server_id", sid))
	}
	out := listResponse[skillDTO]{Items: []skillDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		recs, page, err := sc.Skills().List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, s := range recs {
			out.Items = append(out.Items, toSkillDTO(s))
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

// handleListTools lists the discovered tools with their UNTRUSTED annotation
// hints, optionally filtered by ?server_id.
func (m *Module) handleListTools(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if sid := r.URL.Query().Get("server_id"); sid != "" {
		q.Filters = append(q.Filters, eq("mcp_server_id", sid))
	}
	out := listResponse[toolDTO]{Items: []toolDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		recs, page, err := sc.Tools().List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, t := range recs {
			out.Items = append(out.Items, toToolDTO(t))
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

// --- overlay lookups ---------------------------------------------------------

// toolCountByServer returns an EXACT count of tools per mcp_server_id. It pages
// the tool table to completion (the store has no COUNT primitive and caps a page
// at listCap) so a tenant with more than one page of tools still gets exact
// per-server counts — no silent truncation.
func toolCountByServer(ctx context.Context, sc store.Scope) (map[string]int, error) {
	out := map[string]int{}
	q := model.Query{Limit: listCap}
	for {
		tools, page, err := sc.Tools().List(ctx, q)
		if err != nil {
			return nil, err
		}
		for _, t := range tools {
			if !t.MCPServerID.IsZero() {
				out[t.MCPServerID.String()]++
			}
		}
		if !page.HasMore {
			return out, nil
		}
		q.Cursor = page.Cursor
	}
}

// healthByRef returns the health overlay rows of one subject kind, keyed by
// subject ref. It pages to completion so the catalog never shows a truncated
// connection state as authoritative.
func (m *Module) healthByRef(ctx context.Context, sc store.Scope, subjKind string) (map[string]model.Record, error) {
	out := map[string]model.Record{}
	recs, err := allExt(ctx, sc, healthKind, eq(colSubjectKind, subjKind))
	if err != nil {
		return nil, err
	}
	for _, rec := range recs {
		out[rec.String(colSubjectRef)] = rec
	}
	return out, nil
}

// configByServer returns the managed-config rows keyed by server ref, paging to
// completion.
func (m *Module) configByServer(ctx context.Context, sc store.Scope) (map[string]model.Record, error) {
	out := map[string]model.Record{}
	recs, err := allExt(ctx, sc, configKind)
	if err != nil {
		return nil, err
	}
	for _, rec := range recs {
		out[rec.String(colServerRef)] = rec
	}
	return out, nil
}

// allExt reads every row of an owned entity matching the AND of filters, paging
// the store to completion (each page is capped at listCap). Use it where a count
// or a complete keyed map must be exact rather than first-page-only.
func allExt(ctx context.Context, sc store.Scope, kind model.Kind, filters ...model.Filter) ([]model.Record, error) {
	repo, err := sc.Ext(kind)
	if err != nil {
		return nil, err
	}
	var out []model.Record
	q := model.Query{Filters: filters, Limit: listCap}
	for {
		recs, page, err := repo.List(ctx, q)
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
		if !page.HasMore {
			return out, nil
		}
		q.Cursor = page.Cursor
	}
}

// lookupConfig returns the managed config for one server, or ok=false.
func (m *Module) lookupConfig(ctx context.Context, sc store.Scope, serverRef string) (model.Record, bool, error) {
	return firstExt(ctx, sc, configKind, eq(colServerRef, serverRef))
}

// lookupHealth returns the health overlay for one subject, or ok=false.
func (m *Module) lookupHealth(ctx context.Context, sc store.Scope, subjKind, subjRef string) (model.Record, bool, error) {
	return firstExt(ctx, sc, healthKind, eq(colSubjectKind, subjKind), eq(colSubjectRef, subjRef))
}

// firstExt returns the first row of an owned entity matching the AND of filters.
func firstExt(ctx context.Context, sc store.Scope, kind model.Kind, filters ...model.Filter) (model.Record, bool, error) {
	repo, err := sc.Ext(kind)
	if err != nil {
		return nil, false, err
	}
	recs, _, err := repo.List(ctx, model.Query{Filters: filters, Limit: 1})
	if err != nil {
		return nil, false, err
	}
	if len(recs) == 0 {
		return nil, false, nil
	}
	return recs[0], true, nil
}

// connFromHealth derives the display connection state from a server's health row:
// the recorded status, or unknown when no connection signal has been seen.
func connFromHealth(rec model.Record) string {
	if rec == nil {
		return connUnknown
	}
	if s := rec.String(colStatus); s != "" {
		return s
	}
	return connUnknown
}

// optID renders a possibly-zero id as a string or "".
func optID(id model.ID) string {
	if id.IsZero() {
		return ""
	}
	return id.String()
}
