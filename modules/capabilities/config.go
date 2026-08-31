// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package capabilities

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// maxHintLen bounds a masked-hint field. A hint is a short masked partial (e.g.
// "ghp_…aB12"); anything longer is rejected as a defensive guard against an
// operator pasting a full credential into a field meant only for display.
const maxHintLen = 64

// maxSecretRefs bounds the number of secret references on one config.
const maxSecretRefs = 64

// Valid MCP transports (README.md: stdio/HTTP/SSE/WS).
var validTransports = map[string]bool{"stdio": true, "http": true, "sse": true, "ws": true}

// Valid secret-reference kinds (where the credential actually lives — the config
// stores only the reference, never the value, docs/SECURITY-HARDENING.md).
var validRefKinds = map[string]bool{"env": true, "vault": true, "secret_manager": true, "file": true, "other": true}

// secretRefDTO is a REFERENCE to a credential an MCP server needs — its logical
// name, where it lives (ref_kind + ref locator) and an optional masked hint. It
// is MINIMAL-DATA by construction: there is deliberately no value field, so a
// usable credential cannot be persisted, and the decoder rejects unknown fields
// (so a client cannot smuggle one in).
type secretRefDTO struct {
	Name    string `json:"name"`
	RefKind string `json:"ref_kind"`
	Ref     string `json:"ref"`
	Hint    string `json:"hint,omitempty"`
}

// configDTO is the managed configuration of an MCP server. The endpoint is a
// reference the handler refuses to accept with inline credentials; secrets are
// referenced, never stored.
type configDTO struct {
	ID         string         `json:"id,omitempty"`
	ServerRef  string         `json:"server_ref"`
	Transport  string         `json:"transport"`
	Endpoint   string         `json:"endpoint,omitempty"`
	Scope      string         `json:"scope,omitempty"`
	SecretRefs []secretRefDTO `json:"secret_refs"`
	Enabled    bool           `json:"enabled"`
	Note       string         `json:"note,omitempty"`
	Revision   int64          `json:"revision,omitempty"`
}

// validate normalizes and checks an incoming config, enforcing the minimal-data
// invariant (no inline credential in the endpoint, secrets by reference only).
func (d *configDTO) validate() string {
	d.ServerRef = strings.TrimSpace(d.ServerRef)
	if d.ServerRef == "" {
		return "server_ref is required"
	}
	d.Transport = strings.TrimSpace(strings.ToLower(d.Transport))
	if !validTransports[d.Transport] {
		return "transport must be one of stdio, http, sse, ws"
	}
	d.Endpoint = strings.TrimSpace(d.Endpoint)
	if containsInlineCredential(d.Endpoint) {
		return "endpoint must not contain inline credentials; reference the secret via secret_refs instead"
	}
	if len(d.SecretRefs) > maxSecretRefs {
		return "too many secret references"
	}
	for i := range d.SecretRefs {
		s := &d.SecretRefs[i]
		s.Name = strings.TrimSpace(s.Name)
		s.RefKind = strings.TrimSpace(strings.ToLower(s.RefKind))
		s.Ref = strings.TrimSpace(s.Ref)
		if s.Name == "" {
			return "each secret reference needs a name"
		}
		if !validRefKinds[s.RefKind] {
			return "secret reference ref_kind must be one of env, vault, secret_manager, file, other"
		}
		if s.Ref == "" {
			return "each secret reference needs a ref locator"
		}
		if len(s.Hint) > maxHintLen {
			return "secret reference hint must be a short masked partial, never a full credential"
		}
		if containsInlineCredential(s.Ref) {
			return "secret reference ref must be a locator, never the credential value"
		}
	}
	return ""
}

// containsInlineCredential is a defensive heuristic that rejects the obvious ways
// a credential could end up persisted in a reference/endpoint field: basic-auth
// userinfo in a URL, or a query/param that assigns a secret-like key. It is a
// guardrail, not a secret scanner — the real guarantee is structural (no value
// column exists). It never inspects or stores the matched value.
func containsInlineCredential(s string) bool {
	if s == "" {
		return false
	}
	low := strings.ToLower(s)
	// basic-auth userinfo: scheme://user:pass@host
	if i := strings.Index(low, "://"); i >= 0 {
		rest := low[i+3:]
		if at := strings.IndexByte(rest, '@'); at >= 0 {
			if strings.IndexByte(rest[:at], ':') >= 0 {
				return true
			}
		}
	}
	for _, kw := range []string{"token=", "secret=", "password=", "passwd=", "api_key=", "apikey=", "access_key=", "client_secret="} {
		if strings.Contains(low, kw) {
			return true
		}
	}
	return false
}

// sanitizeRefs re-marshals the secret references through the typed struct so only
// the allow-listed fields (name/ref_kind/ref/hint) are persisted — a value can
// never reach storage even if a future caller path bypassed validation.
func sanitizeRefs(refs []secretRefDTO) string {
	clean := make([]secretRefDTO, 0, len(refs))
	for _, r := range refs {
		clean = append(clean, secretRefDTO{Name: r.Name, RefKind: r.RefKind, Ref: r.Ref, Hint: r.Hint})
	}
	b, err := json.Marshal(clean)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// parseRefs decodes the stored secret references.
func parseRefs(s string) []secretRefDTO {
	if s == "" {
		return []secretRefDTO{}
	}
	var out []secretRefDTO
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return []secretRefDTO{}
	}
	if out == nil {
		return []secretRefDTO{}
	}
	return out
}

// configFields renders the config fields shared by the config and its revision
// snapshot (entity columns only; the engine stamps the base columns).
func (d configDTO) configFields() model.Record {
	return model.Record{
		colServerRef:   d.ServerRef,
		colTransport:   d.Transport,
		colEndpointRef: d.Endpoint,
		colScope:       d.Scope,
		colSecretRefs:  sanitizeRefs(d.SecretRefs),
		colEnabled:     d.Enabled,
		colNote:        d.Note,
	}
}

// toConfigDTO renders a stored config record to the DTO.
func toConfigDTO(rec model.Record) configDTO {
	return configDTO{
		ID: rec.String(model.ColID), ServerRef: rec.String(colServerRef),
		Transport: rec.String(colTransport), Endpoint: rec.String(colEndpointRef),
		Scope: rec.String(colScope), SecretRefs: parseRefs(rec.String(colSecretRefs)),
		Enabled: rec.Bool(colEnabled), Note: rec.String(colNote), Revision: rec.Int(colRevision),
	}
}

// handleListConfigs lists managed MCP-server configs, optionally filtered.
func (m *Module) handleListConfigs(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := r.URL.Query().Get("server_ref"); v != "" {
		q.Filters = append(q.Filters, eq(colServerRef, v))
	}
	if v := r.URL.Query().Get("transport"); v != "" {
		q.Filters = append(q.Filters, eq(colTransport, v))
	}
	out := listResponse[configDTO]{Items: []configDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(configKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toConfigDTO(rec))
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

// handleGetConfig returns one managed config.
func (m *Module) handleGetConfig(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var (
		out   configDTO
		found bool
	)
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(configKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		found, out = true, toConfigDTO(rec)
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

// handleCreateConfig registers a new managed config for an MCP server, recording
// the first revision snapshot and a self-audit attributed to the real principal.
func (m *Module) handleCreateConfig(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in configDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := in.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	var out configDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(configKind)
		if err != nil {
			return err
		}
		// The revision is a per-server monotonic counter that spans the config's
		// whole lifetime, including re-creations: a deleted config leaves its
		// append-only history behind, so a fresh config must continue past it
		// (revision 1 would collide with the immutable history's unique index and
		// make the server permanently unconfigurable).
		next, err := m.nextRevision(r.Context(), sc, in.ServerRef)
		if err != nil {
			return err
		}
		fields := in.configFields()
		fields[colRevision] = next
		rec, err := repo.Create(r.Context(), fields)
		if err != nil {
			return err
		}
		out = toConfigDTO(rec)
		if err := m.appendRevision(r.Context(), sc, in, next, mc.Principal.Actor(), "create"); err != nil {
			return err
		}
		return auditConfig(r.Context(), sc, mc, "create", in.ServerRef)
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// handleUpdateConfig updates a managed config in place, bumping its revision and
// recording an immutable revision snapshot plus a self-audit.
func (m *Module) handleUpdateConfig(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var in configDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	var out configDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(configKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		// server_ref is the immutable natural key; the URL targets a specific
		// config, so the incoming server_ref is forced to the stored one.
		in.ServerRef = rec.String(colServerRef)
		if msg := in.validate(); msg != "" {
			return validationError(msg)
		}
		next := rec.Int(colRevision) + 1
		for k, v := range in.configFields() {
			rec[k] = v
		}
		rec[colRevision] = next
		rec, err = repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		out = toConfigDTO(rec)
		if err := m.appendRevision(r.Context(), sc, in, next, mc.Principal.Actor(), "update"); err != nil {
			return err
		}
		return auditConfig(r.Context(), sc, mc, "update", in.ServerRef)
	})
	if verr, ok := err.(validationError); ok {
		writeJSON(w, http.StatusBadRequest, errorBody(string(verr)))
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDeleteConfig removes a managed config, recording a final revision snapshot
// (the immutable history survives the deletion) and a self-audit.
func (m *Module) handleDeleteConfig(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(configKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		serverRef := rec.String(colServerRef)
		snapshot := toConfigDTO(rec)
		if err := m.appendRevision(r.Context(), sc, snapshot, rec.Int(colRevision)+1, mc.Principal.Actor(), "delete"); err != nil {
			return err
		}
		if err := repo.Delete(r.Context(), id); err != nil {
			return err
		}
		return auditConfig(r.Context(), sc, mc, "delete", serverRef)
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// revisionDTO is one immutable config-version snapshot in the history.
type revisionDTO struct {
	ServerRef    string         `json:"server_ref"`
	Revision     int64          `json:"revision"`
	Transport    string         `json:"transport"`
	Endpoint     string         `json:"endpoint,omitempty"`
	Scope        string         `json:"scope,omitempty"`
	SecretRefs   []secretRefDTO `json:"secret_refs"`
	Enabled      bool           `json:"enabled"`
	Note         string         `json:"note,omitempty"`
	ChangeActor  string         `json:"change_actor"`
	ChangeAction string         `json:"change_action"`
	ChangedAt    string         `json:"changed_at"`
}

// handleListRevisions returns the immutable version history of a config, newest
// revision first.
func (m *Module) handleListRevisions(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var (
		out   listResponse[revisionDTO]
		found bool
	)
	out.Items = []revisionDTO{}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		cfgRepo, err := sc.Ext(configKind)
		if err != nil {
			return err
		}
		cfg, err := cfgRepo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		found = true
		serverRef := cfg.String(colServerRef)
		revRepo, err := sc.Ext(revisionKind)
		if err != nil {
			return err
		}
		recs, page, err := revRepo.List(r.Context(), model.Query{
			Filters: []model.Filter{eq(colServerRef, serverRef)},
			Limit:   listCap,
		})
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toRevisionDTO(rec))
		}
		// Say so when the page is not the whole history. This discarded the page with `_`,
		// and the evidence that it was an oversight rather than a decision is in this very
		// file: handleListConfigs (:207) propagates HasMore, and nextRevision (:496-510)
		// walks `q.Cursor` in a LOOP until `!page.HasMore` -- over the SAME kind and the
		// SAME server_ref filter as this call. The module already knew listCap is not the
		// whole history; only this handler forgot to say so, and it did it by staying
		// quiet: a client reading `has_more: false` concluded it held every revision,
		// which above listCap was simply false.
		//
		// It DECLARES rather than draining like nextRevision does, and that is deliberate:
		// nextRevision walks everything because it needs one maximum and answers to no
		// client, while an HTTP handler that drains an append-only history has no bound on
		// what it will hold in memory or how long it will take. An honest ceiling beats an
		// unbounded response.
		//
		// HasMore only, deliberately, and NOT Cursor: this endpoint never reads a cursor
		// from the query (unlike handleListConfigs, which takes one through listQuery), so
		// returning one would advertise a pagination the handler cannot honour. Trading a
		// silent truncation for an unusable cursor is not an improvement.
		out.HasMore = page.HasMore
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
	// Sort newest-revision-first; the store orders by id (ingest order), which for
	// monotonic revisions equals revision order, but sort explicitly to be safe.
	sortRevisionsDesc(out.Items)
	writeJSON(w, http.StatusOK, out)
}

func toRevisionDTO(rec model.Record) revisionDTO {
	return revisionDTO{
		ServerRef: rec.String(colServerRef), Revision: rec.Int(colRevision),
		Transport: rec.String(colTransport), Endpoint: rec.String(colEndpointRef),
		Scope: rec.String(colScope), SecretRefs: parseRefs(rec.String(colSecretRefs)),
		Enabled: rec.Bool(colEnabled), Note: rec.String(colNote),
		ChangeActor: rec.String(colChangeActor), ChangeAction: rec.String(colChangeAction),
		ChangedAt: rec.String(colChangedAt),
	}
}

// sortRevisionsDesc orders revisions newest first (descending revision number).
func sortRevisionsDesc(rs []revisionDTO) {
	for i := 1; i < len(rs); i++ {
		for j := i; j > 0 && rs[j-1].Revision < rs[j].Revision; j-- {
			rs[j-1], rs[j] = rs[j], rs[j-1]
		}
	}
}

// nextRevision returns the next monotonic revision for a server's config: one
// past the highest revision ever recorded in the append-only history for that
// server_ref (1 when there is none). Because the history outlives the live config
// (a delete leaves it behind), this keeps revisions gap-tolerant and collision-
// free across delete/re-create cycles.
func (m *Module) nextRevision(ctx context.Context, sc store.Scope, serverRef string) (int64, error) {
	repo, err := sc.Ext(revisionKind)
	if err != nil {
		return 0, err
	}
	maxRev := int64(0)
	q := model.Query{Filters: []model.Filter{eq(colServerRef, serverRef)}, Limit: listCap}
	for {
		recs, page, err := repo.List(ctx, q)
		if err != nil {
			return 0, err
		}
		for _, rec := range recs {
			if v := rec.Int(colRevision); v > maxRev {
				maxRev = v
			}
		}
		if !page.HasMore {
			break
		}
		q.Cursor = page.Cursor
	}
	return maxRev + 1, nil
}

// appendRevision writes one immutable revision snapshot (the append-only history).
func (m *Module) appendRevision(ctx context.Context, sc store.Scope, cfg configDTO, revision int64, actor, action string) error {
	repo, err := sc.Ext(revisionKind)
	if err != nil {
		return err
	}
	rec := cfg.configFields()
	rec[colRevision] = revision
	rec[colChangeActor] = actor
	rec[colChangeAction] = action
	rec[colChangedAt] = model.NewTimestamp(m.clock.Now().Time()).String()
	_, err = repo.Create(ctx, rec)
	return err
}

// auditConfig appends a config-governance audit event attributed to the real
// principal, in the caller's transaction — so the ledger records WHO changed which
// MCP server's configuration (docs/SECURITY-HARDENING.md self-audit), not the system actor.
func auditConfig(ctx context.Context, sc store.Scope, mc api.ModuleContext, verb, serverRef string) error {
	_, err := sc.Audit().Append(ctx, model.AuditDraft{
		Actor:      mc.Principal.Actor(),
		ActorKind:  mc.Principal.ActorKind(),
		Action:     "capabilities.mcp_config." + verb,
		TargetKind: configKind,
		Meta:       map[string]any{"server_ref": serverRef},
	})
	return err
}

// validationError is a deferred validation failure raised from inside a Mutate
// closure (where the request body is validated against the loaded record), mapped
// to a 400 by the caller.
type validationError string

func (e validationError) Error() string { return string(e) }
