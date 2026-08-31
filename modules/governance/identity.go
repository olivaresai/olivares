// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// agentKind is the core Agent entity kind, used as the audit/binding target kind.
const agentKind model.Kind = "core.agent"

// bindRequest binds an agent to an NHI identity (the access-map attribution
// bridge). Exactly one of mint / identity_id / identity_ref selects the target.
type bindRequest struct {
	// IdentityID binds to an existing core Identity by its internal id — the
	// canonical anchor reconcileDrift keys on.
	IdentityID string `json:"identity_id,omitempty"`
	// IdentityRef binds to (find-or-creating) the identity whose external_id is the
	// directory ref the agent's credential presents — the SAME ref Vault's Gather
	// emits as a SignalPolicy edge origin, so the binding and the permitted grant
	// resolve to one row.
	IdentityRef string `json:"identity_ref,omitempty"`
	// Mint provisions a fresh, stable per-agent NHI identity and binds it. Use it
	// for a genuinely-new per-agent NHI the operator will also register in the
	// directory; it does NOT retroactively reconcile an agent that runs as a
	// pre-existing shared entity (that needs identity_id/identity_ref).
	Mint bool `json:"mint,omitempty"`
	// AllowUnknown permits binding to an identity whose principal type the source
	// never revealed (PrincipalUnknown); without it such a bind is refused so an
	// unknown is never silently treated as an NHI.
	AllowUnknown bool `json:"allow_unknown,omitempty"`
}

// bindResponse reports the resulting binding and whether the identity is shared
// across agents (which collapses per-agent attribution — surfaced, not faked).
type bindResponse struct {
	AgentID     string `json:"agent_id"`
	IdentityID  string `json:"identity_id"`
	IdentityRef string `json:"identity_ref,omitempty"`
	Minted      bool   `json:"minted,omitempty"`
	Shared      bool   `json:"shared"`
	AgentCount  int    `json:"agent_count"`
}

// handleBindAgent binds an agent to its NHI identity (Agent.IdentityID). Admin-tier
// and self-audited. Binding to an identity already bound to another agent emits the
// shared-identity finding immediately (not only at list time).
func (m *Module) handleBindAgent(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	agentID := model.ID(chi.URLParam(r, "agentID"))
	if agentID.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid agent id"))
		return
	}
	var in bindRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	var (
		resp       bindResponse
		identID    model.ID
		sharedN    int
		clientErr  string
		clientCode int
	)
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		agent, err := sc.Agents().Get(r.Context(), agentID)
		if err != nil {
			return err // ErrNotFound -> 404
		}
		var target model.Identity
		switch {
		case in.Mint:
			target, err = mintIdentity(r.Context(), sc, agent)
			if err != nil {
				return err
			}
		case strings.TrimSpace(in.IdentityID) != "":
			target, err = sc.Identities().Get(r.Context(), model.ID(strings.TrimSpace(in.IdentityID)))
			if err != nil {
				if isNotFound(err) {
					clientErr, clientCode = "identity not found", http.StatusNotFound
					return nil
				}
				return err
			}
			if msg := bindGate(target, in.AllowUnknown); msg != "" {
				clientErr, clientCode = msg, http.StatusBadRequest
				return nil
			}
		case strings.TrimSpace(in.IdentityRef) != "":
			target, err = foBindIdentity(r.Context(), sc, strings.TrimSpace(in.IdentityRef))
			if err != nil {
				return err
			}
			if msg := bindGate(target, in.AllowUnknown); msg != "" {
				clientErr, clientCode = msg, http.StatusBadRequest
				return nil
			}
		default:
			clientErr, clientCode = "one of identity_id, identity_ref or mint is required", http.StatusBadRequest
			return nil
		}

		agent.IdentityID = target.ID
		agent, err = sc.Agents().Update(r.Context(), agent)
		if err != nil {
			return err // ErrConflict -> 409 (concurrent agent update)
		}
		identID = target.ID
		n, err := countAgentsForIdentity(r.Context(), sc, target.ID)
		if err != nil {
			return err
		}
		sharedN = n
		resp = bindResponse{
			AgentID: agent.ID.String(), IdentityID: target.ID.String(), IdentityRef: target.ExternalID,
			Minted: in.Mint, Shared: n > 1, AgentCount: n,
		}
		return auditEvent(r.Context(), sc, mc, "governance.binding.bind", agentKind, agent.ID, map[string]any{
			"identity_id": target.ID.String(), "minted": in.Mint, "shared": n > 1,
		})
	})
	if clientErr != "" {
		writeJSON(w, clientCode, errorBody(clientErr))
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if sharedN > 1 { // emit AFTER commit, so a rolled-back bind never signals
		m.emitSharedIdentityFinding(r.Context(), mc.Tenant, identID, sharedN)
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleUnbindAgent clears an agent's identity binding. Admin-tier, self-audited.
func (m *Module) handleUnbindAgent(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	agentID := model.ID(chi.URLParam(r, "agentID"))
	if agentID.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid agent id"))
		return
	}
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		agent, err := sc.Agents().Get(r.Context(), agentID)
		if err != nil {
			return err
		}
		if agent.IdentityID.IsZero() {
			return nil // already unbound — idempotent
		}
		prev := agent.IdentityID
		agent.IdentityID = ""
		if _, err := sc.Agents().Update(r.Context(), agent); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "governance.binding.unbind", agentKind, agentID, map[string]any{"identity_id": prev.String()})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// bindGate refuses to bind an agent to a human identity, and to an
// unknown-principal-type identity unless the operator explicitly allows it — the
// honest-confidence rule (never coerce Unknown to NHI; ARCHITECTURE.md).
func bindGate(target model.Identity, allowUnknown bool) string {
	pt, _ := target.Metadata["principal_type"].(string)
	switch pt {
	case string(identitysource.PrincipalHuman):
		return "cannot bind an agent to a human identity"
	case "", string(identitysource.PrincipalUnknown):
		if !allowUnknown {
			return "identity principal type is unknown; pass allow_unknown=true to bind anyway"
		}
	}
	return ""
}

// mintIdentity find-or-creates a stable per-agent NHI identity (external_id
// "agent:<agentID>") so re-minting is idempotent. It asserts principal_type=nhi
// because the operator is provisioning a dedicated NHI for the agent.
func mintIdentity(ctx context.Context, sc store.Scope, agent model.Agent) (model.Identity, error) {
	ref := "agent:" + agent.ID.String()
	if cur, ok, err := identityByExternalID(ctx, sc, ref); err != nil {
		return model.Identity{}, err
	} else if ok {
		return cur, nil
	}
	name := agent.Name
	if name == "" {
		name = ref
	}
	return sc.Identities().Create(ctx, model.Identity{
		Name: "nhi:" + name, Kind: "agent_nhi", ExternalID: ref, Provider: "governance",
		Metadata: map[string]any{"principal_type": string(identitysource.PrincipalNHI), "minted": true},
	})
}

// foBindIdentity find-or-creates the identity an agent binds to by directory ref.
// On CREATE it asserts principal_type=nhi (the operator is declaring the agent runs
// as this NHI); an EXISTING row is returned unchanged so bindGate can judge its
// real, source-derived type.
func foBindIdentity(ctx context.Context, sc store.Scope, ref string) (model.Identity, error) {
	if cur, ok, err := identityByExternalID(ctx, sc, ref); err != nil {
		return model.Identity{}, err
	} else if ok {
		return cur, nil
	}
	return sc.Identities().Create(ctx, model.Identity{
		Name: ref, Kind: "nhi", ExternalID: ref,
		Metadata: map[string]any{"principal_type": string(identitysource.PrincipalNHI)},
	})
}

// countAgentsForIdentity returns the EXACT number of agents bound to identityID
// within the pinned tenant scope, paginating so the count is accurate for any
// fan-out (consistent with handleListBindings, which also reports the true count).
// The shared signal is count>1 — the same scoped semantics access-map's bridge
// uses to decide attribution collapses — but the count surfaced to clients and the
// finding must be the real number, not a capped one.
func countAgentsForIdentity(ctx context.Context, sc store.Scope, identityID model.ID) (int, error) {
	n := 0
	q := model.Query{Filters: []model.Filter{eq("identity_id", identityID.String())}, Limit: listCap}
	for {
		list, page, err := sc.Agents().List(ctx, q)
		if err != nil {
			return 0, err
		}
		n += len(list)
		if !page.HasMore || page.Cursor == "" {
			return n, nil
		}
		q.Cursor = page.Cursor
	}
}

// emitSharedIdentityFinding publishes a finding that an NHI identity is bound to
// more than one agent, so per-agent attribution collapses to the identity level
// (ARCHITECTURE.md — honest, never faked as a recoverable agent). The title carries the
// count only; the identity id rides SubjectRef; no DisplayName/email is ever
// interpolated (docs/SECURITY-HARDENING.md).
func (m *Module) emitSharedIdentityFinding(ctx context.Context, tenant model.TenantID, identityID model.ID, count int) {
	if m.host == nil {
		return
	}
	sum := sha256.Sum256([]byte("shared_identity|" + identityID.String() + "|" + strconv.Itoa(count)))
	finding := sdkmodel.FindingReport{
		Kind:        "governance_shared_identity",
		Severity:    sdkmodel.SeverityMedium,
		SubjectKind: "identity",
		SubjectRef:  identityID.String(),
		Title:       "Shared NHI identity bound to " + strconv.Itoa(count) + " agents — per-agent attribution unavailable",
		DetailHash:  hex.EncodeToString(sum[:]),
		OccurredAt:  m.clock.Now().Time(),
	}
	if err := m.host.Publish(ctx, event.FromObservation(tenant.String(), Name, finding)); err != nil {
		m.debugf("governance: emit shared-identity finding failed", "err", err)
	}
}

// bindingDTO is one agent↔identity binding in the governance view.
type bindingDTO struct {
	AgentID     string `json:"agent_id"`
	AgentName   string `json:"agent_name,omitempty"`
	IdentityID  string `json:"identity_id"`
	IdentityRef string `json:"identity_ref,omitempty"`
	Shared      bool   `json:"shared"`
	AgentCount  int    `json:"agent_count"`
}

// handleListBindings lists agent↔identity bindings, flagging identities shared
// across agents. The agent→identity topology is recon-relevant, so the
// read self-audits in a committed transaction (the access-map pattern).
func (m *Module) handleListBindings(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	out := listResponse[bindingDTO]{Items: []bindingDTO{}}
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		if err := auditEvent(r.Context(), sc, mc, "governance.binding.list", agentKind, "", nil); err != nil {
			return err
		}
		// Scan agents page by page; collect the bound ones and count per identity in
		// memory (one scoped scan, no per-agent identity query for the count).
		var bound []model.Agent
		counts := map[model.ID]int{}
		q := model.Query{Limit: listCap}
		for {
			agents, page, err := sc.Agents().List(r.Context(), q)
			if err != nil {
				return err
			}
			for _, a := range agents {
				if a.IdentityID.IsZero() {
					continue
				}
				bound = append(bound, a)
				counts[a.IdentityID]++
			}
			if !page.HasMore || page.Cursor == "" {
				break
			}
			q.Cursor = page.Cursor
		}
		refCache := map[model.ID]string{}
		for _, a := range bound {
			ref := refCache[a.IdentityID]
			if ref == "" {
				if id, err := sc.Identities().Get(r.Context(), a.IdentityID); err == nil {
					ref = id.ExternalID
				} else if !isNotFound(err) {
					return err
				}
				refCache[a.IdentityID] = ref
			}
			n := counts[a.IdentityID]
			out.Items = append(out.Items, bindingDTO{
				AgentID: a.ID.String(), AgentName: a.Name, IdentityID: a.IdentityID.String(),
				IdentityRef: ref, Shared: n > 1, AgentCount: n,
			})
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
