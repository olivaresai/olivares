// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

// read-only console seam — previewing what the runtime resolver would decide for
// an actor. Read-only, rides the existing binding:read tier, and adds no new decision
// logic — it only surfaces what the module already decides. (Resource-tree navigation
// for the folder-binding picker lives in resources.go: GET /resources.)

// resolvePreviewDTO is the baseline resolver verdict for one (actor, source) pair. The
// credential surfaces as name + masked hint only — the locator stays on the binding
// endpoints, where it already lives for binding:read holders.
type resolvePreviewDTO struct {
	Allowed  bool   `json:"allowed"`
	Reason   string `json:"reason"`
	Bound    bool   `json:"bound"`
	Baseline bool   `json:"baseline"`
	CredName string `json:"cred_name,omitempty"`
	CredHint string `json:"cred_hint,omitempty"`
}

// handleResolvePreview answers "what would the runtime resolver decide for this actor
// and source" — the console's binding-effect preview. It runs the REAL resolver with a
// zero-value principal ON PURPOSE: the preview reports the deny-closed BASELINE
// (containment, row effects, assignments), never the CALLER's own authority — a tenant
// admin previewing an out-of-scope agent must see the deny that agent would get, not
// the admin's own tenant-wide allow. What the runtime principal itself would add (its
// Grants, its RBAC) is therefore not simulated; the response says baseline:true
// so the console can label it honestly.
func (m *Module) handleResolvePreview(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	qp := r.URL.Query()
	sourceType, sourceRef := qp.Get("source_type"), qp.Get("source_ref")
	actorKind, actorRef := qp.Get("actor_kind"), qp.Get("actor_ref")
	if !validSourceTypes[sourceType] {
		writeJSON(w, http.StatusBadRequest, errorBody("source_type must be one of mcp|model|provider|knowledge|data"))
		return
	}
	if sourceRef == "" || actorRef == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("source_ref and actor_ref are required"))
		return
	}
	var (
		dec Decision
		err error
	)
	switch actorKind {
	case "session":
		dec, err = m.Resolver().ResolveForSession(r.Context(), mc.Tenant, auth.Principal{}, actorRef, sourceType, sourceRef)
	case "agent":
		dec, err = m.Resolver().ResolveForAgent(r.Context(), mc.Tenant, auth.Principal{}, actorRef, sourceType, sourceRef)
	default:
		writeJSON(w, http.StatusBadRequest, errorBody("actor_kind must be session or agent"))
		return
	}
	if err != nil {
		// The resolver already failed closed; report the failure without detail.
		writeJSON(w, http.StatusInternalServerError, errorBody("resolve preview failed"))
		return
	}
	out := resolvePreviewDTO{Allowed: dec.Allowed, Reason: dec.Reason, Bound: dec.Bound, Baseline: true}
	if dec.Cred != nil {
		out.CredName, out.CredHint = dec.Cred.Name, dec.Cred.Hint
	}
	writeJSON(w, http.StatusOK, out)
}
