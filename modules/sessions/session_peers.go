// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	sameTemplatePeerRule = "same-template"
	maxRunPeers          = 128
)

type setRunPeersRequest struct {
	Peers     *[]string `json:"peers,omitempty"`
	PeersRule *string   `json:"peers_rule,omitempty"`
}

// handleSetRunPeers stores one operator choice on the existing run row. The
// route supplies run-write authorization in the run's recorded workspace.
func (m *Module) handleSetRunPeers(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var body setRunPeersRequest
	if !decodeJSONBody(w, r, &body) {
		return
	}
	if (body.Peers == nil) == (body.PeersRule == nil) || (body.PeersRule != nil && *body.PeersRule != sameTemplatePeerRule) {
		writeJSON(w, http.StatusBadRequest, errorBody("choose peers or peers_rule: same-template"))
		return
	}
	peers, rule := []string{}, ""
	if body.Peers != nil {
		peers = *body.Peers
		if !validRunPeers(peers) {
			writeJSON(w, http.StatusBadRequest, errorBody("peers must be distinct canonical session IDs (at most 128)"))
			return
		}
	} else {
		rule = *body.PeersRule
	}
	ref := chi.URLParam(r, "ref")
	release, err := m.rt.lockRunContext(r.Context(), liveKey(mc.Tenant, ref))
	if err != nil {
		writeRunErr(w, err)
		return
	}
	defer release()
	// Authorize after releasing the read transaction: the scoped-grant engine
	// may read the same store, and SQLite has only one connection. Keep the run
	// operation lock and revalidate the authorized peer's identity before writing.
	selected := make(map[string]model.Record, len(peers))
	if len(peers) > 0 {
		err = mc.Data.View(r.Context(), func(sc store.Scope) error {
			repo, err := sc.Ext(runKind)
			if err != nil {
				return err
			}
			rec, err := findRunRec(r.Context(), repo, ref)
			if err != nil {
				return err
			}
			if rec.String(colRunWorkspacePath) == "" || rec.String(colRunAuthzWorkspaceID) == "" {
				return &runErr{http.StatusUnprocessableEntity, "run folder is not recorded"}
			}
			for _, sid := range peers {
				peer, err := m.findSessionPeer(r.Context(), mc.Tenant, repo, rec, sid)
				if err != nil {
					return err
				}
				if peer == nil || sid == rec.String(colRunClaimSID) {
					return &runErr{http.StatusUnprocessableEntity, "peer must be another readable live session in this authorization workspace"}
				}
				selected[sid] = peer
			}
			return nil
		})
		if err != nil {
			writeRunErr(w, err)
			return
		}
		for _, sid := range peers {
			if !m.sessionPeerReadable(r.Context(), mc.Tenant, mc.Principal, selected[sid]) {
				writeRunErr(w, &runErr{http.StatusUnprocessableEntity, "peer must be another readable live session in this authorization workspace"})
				return
			}
		}
	}
	var dto runDTO
	err = mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rec, err := findRunRec(r.Context(), repo, ref)
		if err != nil {
			return err
		}
		if rec.String(colRunWorkspacePath) == "" || rec.String(colRunAuthzWorkspaceID) == "" {
			return &runErr{http.StatusUnprocessableEntity, "run folder is not recorded"}
		}
		if rule != "" && rec.String(colTemplateID) == "" {
			return &runErr{http.StatusUnprocessableEntity, "same-template requires a run launched from a template"}
		}
		for _, sid := range peers {
			peer, err := m.findSessionPeer(r.Context(), mc.Tenant, repo, rec, sid)
			if err != nil {
				return err
			}
			before := selected[sid]
			if peer == nil || sid == rec.String(colRunClaimSID) ||
				peer.String(model.ColID) != before.String(model.ColID) ||
				peer.String(colRunAuthzWorkspaceID) != before.String(colRunAuthzWorkspaceID) ||
				peer.String(colRunWorkspacePath) != before.String(colRunWorkspacePath) ||
				peer.Int(colClaimFence) != before.Int(colClaimFence) {
				return &runErr{http.StatusUnprocessableEntity, "peer must be another readable live session in this authorization workspace"}
			}
		}
		raw, _ := json.Marshal(peers)
		rec[colRunPeers], rec[colRunPeersRule] = string(raw), rule
		rec, err = repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		_, err = sc.Audit().Append(r.Context(), model.AuditDraft{
			Actor: mc.Principal.Actor(), ActorKind: mc.Principal.ActorKind(),
			Action: "sessions.run.peers", TargetKind: runKind, TargetID: model.ID(rec.String(model.ColID)),
			Meta: map[string]any{"run_ref": ref, "peers": peers, "peers_rule": rule},
		})
		dto = m.toRunDTO(rec)
		return err
	})
	if err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func validRunPeers(peers []string) bool {
	if len(peers) > maxRunPeers {
		return false
	}
	seen := map[string]bool{}
	for _, sid := range peers {
		if !validCanonicalSID(sid) || seen[sid] {
			return false
		}
		seen[sid] = true
	}
	return true
}

// An absent historical selection conveys no authority. Invalid stored lists
// also fail closed; they cannot make the same-template rule a fallback grant.
func readRunPeers(rec model.Record) ([]string, bool) {
	peers := []string{}
	if raw := rec.String(colRunPeers); raw != "" {
		if json.Unmarshal([]byte(raw), &peers) != nil || peers == nil || !validRunPeers(peers) {
			return []string{}, false
		}
	}
	return peers, true
}

func runPeers(rec model.Record) []string {
	peers, _ := readRunPeers(rec)
	return peers
}

func (m *Module) findSessionPeer(ctx context.Context, tenant model.TenantID, repo store.GenericRepo, sender model.Record, sid string) (model.Record, error) {
	if !validCanonicalSID(sid) || sender.String(colRunWorkspacePath) == "" || sender.String(colRunAuthzWorkspaceID) == "" {
		return nil, nil
	}
	recs, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{
		eq(colRunClaimSID, sid), eq(colRunAuthzWorkspaceID, sender.String(colRunAuthzWorkspaceID)),
	}, Limit: 2})
	if err != nil || len(recs) != 1 {
		return nil, err
	}
	peer := recs[0]
	if m.WorkAuthorizer == nil || peer.String(colRunWorkspacePath) == "" || !m.runPeerLive(tenant, peer) {
		return nil, nil
	}
	return peer, nil
}

func (m *Module) sessionPeerReadable(ctx context.Context, tenant model.TenantID, p auth.Principal, peer model.Record) bool {
	workspace, err := model.ParseID(peer.String(colRunAuthzWorkspaceID))
	if err != nil {
		return false
	}
	// The resource the native run routes authorize: kind "run" and the stored ID.
	resource := auth.ResourceFor(permRunRead)
	resource.ID, resource.WorkspaceID = peer.String(model.ColID), workspace
	return m.WorkAuthorizer.Authorize(ctx, auth.Request{Principal: p, Tenant: tenant, Permission: permRunRead, Resource: resource}).Allow
}

func (m *Module) runPeerLive(tenant model.TenantID, rec model.Record) bool {
	live, ok := m.rt.getLive(tenant, rec.String(colRunRef))
	if !ok {
		return false
	}
	live.mu.Lock()
	defer live.mu.Unlock()
	return !live.finalized && !live.stopRequested && live.context != nil && live.context.Err() == nil &&
		live.claim.SID == rec.String(colRunClaimSID) && live.claim.Fence == rec.Int(colClaimFence) &&
		live.companion.Dir == rec.String(colRunWorkspacePath)
}

// The caller holds the run operation lock through the work mutation to exclude revocation.
func (m *Module) sessionPeerAllowed(ctx context.Context, tenant model.TenantID, p auth.Principal, companion RuntimeCompanion, sid string) (bool, error) {
	allowed := false
	var peer model.Record
	err := m.Data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		sender, err := findRunRec(ctx, repo, p.SessionRunRef)
		if err != nil {
			return err
		}
		workspace, _ := p.ConfinedWorkspaceIn(tenant)
		live, ok := m.rt.getLive(tenant, p.SessionRunRef)
		if !ok || live.launchID != companion.LaunchID || !m.runPeerLive(tenant, sender) || sender.String(colRunAuthzWorkspaceID) != workspace.String() || sender.String(colRunWorkspacePath) != companion.Dir {
			return nil
		}
		peers, valid := readRunPeers(sender)
		rule := sender.String(colRunPeersRule)
		if !valid || (rule != "" && (rule != sameTemplatePeerRule || len(peers) != 0)) {
			return nil
		}
		peer, err = m.findSessionPeer(ctx, tenant, repo, sender, sid)
		if err != nil || peer == nil {
			return err
		}
		allowed = slices.Contains(peers, sid) || (rule == sameTemplatePeerRule && sender.String(colTemplateID) != "" && sender.String(colTemplateID) == peer.String(colTemplateID) && sid != p.SessionIdentity)
		return nil
	})
	// Authorize outside this View: SQLite's single connection cannot serve a nested View.
	if err == nil && peer != nil {
		allowed = m.sessionPeerReadable(ctx, tenant, p, peer) && allowed
	}
	return allowed, err
}
