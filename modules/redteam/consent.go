// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package redteam

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// This file is the CONSENT gate (docs/SECURITY-HARDENING.md, the dual-use red line). A red-team run
// may only target an agent that has been explicitly REGISTERED and AUTHORIZED as a
// target by a tenant admin — the in-code enforcement that the battery is run against
// the client's OWN governed agents, with permission, and audited. Registration and
// authorization are admin-tier and self-audited.

// targetDTO is the wire shape of a red-team target (a consent record).
type targetDTO struct {
	ID           string `json:"id"`
	AgentRef     string `json:"agent_ref"`
	Name         string `json:"name"`
	Endpoint     string `json:"endpoint,omitempty"`
	Scope        string `json:"scope,omitempty"`
	Authorized   bool   `json:"authorized"`
	AuthorizedBy string `json:"authorized_by,omitempty"`
	AuthorizedAt string `json:"authorized_at,omitempty"`
	Status       string `json:"status"`
	CreatedBy    string `json:"created_by,omitempty"`
}

func toTargetDTO(rec model.Record) targetDTO {
	return targetDTO{
		ID: rec.String(model.ColID), AgentRef: rec.String(colAgentRef), Name: rec.String(colName),
		Endpoint: rec.String(colEndpoint), Scope: rec.String(colScope), Authorized: rec.Bool(colAuthorized),
		AuthorizedBy: rec.String(colAuthorizedBy), AuthorizedAt: rec.String(colAuthorizedAt),
		Status: rec.String(colTargetStatus), CreatedBy: rec.String(colTargetCreated),
	}
}

func recToTarget(rec model.Record) Target {
	return Target{
		ID: model.ID(rec.String(model.ColID)), AgentRef: rec.String(colAgentRef), Name: rec.String(colName),
		Endpoint: rec.String(colEndpoint), Scope: rec.String(colScope), Authorized: rec.Bool(colAuthorized),
	}
}

type registerTargetRequest struct {
	AgentRef string `json:"agent_ref"`
	Name     string `json:"name"`
	Endpoint string `json:"endpoint,omitempty"`
	Scope    string `json:"scope,omitempty"`
}

// handleRegisterTarget registers a client-governed agent as a candidate target. It
// starts UNAUTHORIZED — registration is not consent; a separate authorize step is
// the explicit grant. Admin-tier + self-audited.
func (m *Module) handleRegisterTarget(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var req registerTargetRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	agentRef := clamp(strings.TrimSpace(req.AgentRef), maxRefLen)
	if agentRef == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("agent_ref is required"))
		return
	}
	name := clamp(strings.TrimSpace(req.Name), maxNameLen)
	if name == "" {
		name = agentRef
	}
	var out targetDTO
	conflict := false
	unowned := false
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		// OWNERSHIP gate (docs/SECURITY-HARDENING.md): the agent_ref must resolve to an agent in
		// THIS tenant's inventory. You cannot register a target for an arbitrary
		// string or another tenant's agent — only your own governed estate.
		if _, ok, rerr := resolveOwnedAgent(r.Context(), sc, agentRef); rerr != nil {
			return rerr
		} else if !ok {
			unowned = true
			return nil
		}
		repo, err := sc.Ext(targetKind)
		if err != nil {
			return err
		}
		created, err := repo.Create(r.Context(), model.Record{
			colAgentRef: agentRef, colName: name, colEndpoint: clamp(strings.TrimSpace(req.Endpoint), maxRefLen),
			colScope: clamp(strings.TrimSpace(req.Scope), maxReasonLen), colAuthorized: false,
			colTargetStatus: "registered", colTargetCreated: mc.Principal.Actor(),
		})
		if err != nil {
			if isConflict(err) {
				conflict = true
				return nil
			}
			return err
		}
		out = toTargetDTO(created)
		return auditEvent(r.Context(), sc, mc, "redteam.target.register", targetKind, model.ID(created.String(model.ColID)), map[string]any{"agent_ref": agentRef})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if unowned {
		writeJSON(w, http.StatusUnprocessableEntity, errorBody("agent_ref does not resolve to an agent in this tenant's inventory; only governed agents in your own estate can be red-teamed (docs/08 §8)"))
		return
	}
	if conflict {
		writeJSON(w, http.StatusConflict, errorBody("a target for this agent_ref already exists"))
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

type authorizeTargetRequest struct {
	Authorized bool   `json:"authorized"`
	Scope      string `json:"scope,omitempty"`
}

// handleAuthorizeTarget grants or revokes CONSENT to red-team a target. This is the
// dual-use boundary: only an authorized target may be run against.
// Admin-tier + self-audited; revocation is always allowed.
func (m *Module) handleAuthorizeTarget(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var req authorizeTargetRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	var out targetDTO
	found := false
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(targetKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			if isNotFound(err) {
				return nil
			}
			return err
		}
		now := m.clock.Now()
		rec[colAuthorized] = req.Authorized
		if req.Authorized {
			rec[colTargetStatus] = "authorized"
			rec[colAuthorizedBy] = mc.Principal.Actor()
			rec[colAuthorizedAt] = now.String()
		} else {
			rec[colTargetStatus] = "revoked"
		}
		if s := strings.TrimSpace(req.Scope); s != "" {
			rec[colScope] = clamp(s, maxReasonLen)
		}
		updated, err := repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		found, out = true, toTargetDTO(updated)
		return auditEvent(r.Context(), sc, mc, "redteam.target.authorize", targetKind, id, map[string]any{"authorized": req.Authorized})
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

// handleListTargets lists the tenant's registered targets.
func (m *Module) handleListTargets(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := strings.TrimSpace(r.URL.Query().Get("status")); v != "" {
		q.Filters = append(q.Filters, eq(colTargetStatus, v))
	}
	out := listResponse[targetDTO]{Items: []targetDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(targetKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toTargetDTO(rec))
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

// handleGetTarget returns one target.
func (m *Module) handleGetTarget(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var out targetDTO
	found := false
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(targetKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			if isNotFound(err) {
				return nil
			}
			return err
		}
		found, out = true, toTargetDTO(rec)
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

// loadAuthorizedTarget loads a target by id and confirms it is AUTHORIZED. It is the
// in-code consent check a run launch must pass (docs/SECURITY-HARDENING.md).
func loadAuthorizedTarget(ctx context.Context, sc store.Scope, id model.ID) (Target, bool, bool, error) {
	repo, err := sc.Ext(targetKind)
	if err != nil {
		return Target{}, false, false, err
	}
	rec, err := repo.Get(ctx, id)
	if err != nil {
		if isNotFound(err) {
			return Target{}, false, false, nil
		}
		return Target{}, false, false, err
	}
	t := recToTarget(rec)
	return t, true, t.Authorized, nil
}
