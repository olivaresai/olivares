// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package deploy

import (
	"context"
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// Wiring lifecycle statuses (wiring_status column).
const (
	wiringDeclared = "declared"
	wiringApplied  = "applied"
	wiringRevoked  = "revoked"
)

// Attribution levels for a wiring's origin (attribution column). Firm means a
// single per-agent NHI identity was bound via degraded means binding was
// unavailable or no per-agent identity was declared — surfaced, never faked.
const (
	attributionFirm     = "firm"
	attributionDegraded = "degraded"
)

// permittedEdge is a declared PERMITTED connection ready to be published as a
// policy-grant EdgeObservation. (the sole AccessEdge writer) ingests it from
// the bus and, because Source is SignalPolicy, records it on the PERMITTED side
// of the permitted-vs-observed diff (access-map reactor deriveObservedPermitted).
type permittedEdge struct {
	originKind   string // "identity" (firm NHI bound by the binder) or "agent" (degraded — attribute to the subject)
	originRef    string
	resourceKind string
	resourceRef  string
	mode         sdkmodel.AccessMode
	confidence   sdkmodel.Confidence
}

// Canonical graph origin kinds (the subset of EdgeObservation.OriginKind
// vocabulary this module emits). A firm per-agent identity is published as an
// "identity" origin (resolves the bound NHI credential); a degraded wiring is
// published as an "agent" origin against the subject — never an agent name
// masquerading as a credential identity (which would create a spurious Identity in
// bridge).
const (
	originKindIdentity = "identity"
	originKindAgent    = "agent"
)

// ensureIdentity asserts the deployment's per-agent identity through the
// binder BEFORE the persistence transaction (the binder may make an external
// call, which must not hold the store's single writer). It returns the identity
// reference the wirings attribute to and whether attribution is firm.
//
// Honesty rule (ARCHITECTURE.md, the session brief): a deployment with a declared
// per-agent identity that the binder firmly bound is "firm"; one with no declared
// identity, or whose binding was unavailable, is "degraded" — the origin still
// carries a usable reference (the declared id or the subject itself) so the
// PERMITTED edge is declarable, but the wiring records that the per-agent
// attribution needs is not firm. It is NEVER faked as firm.
func (m *Module) ensureIdentity(ctx context.Context, tenant model.TenantID, ec execContext) (ref string, firm bool) {
	subjectRef := ec.def.String(colSubjectRef)
	if ec.spec.Identity == nil {
		// No per-agent identity declared: attribution is degraded; the origin falls
		// back to the subject's own reference.
		return subjectRef, false
	}
	bound, err := m.binder.EnsureAgentIdentity(ctx, tenant, subjectRef, ec.spec.Identity.IdentityRef, ec.spec.Identity.Mint)
	if err != nil {
		m.debugf("deploy: identity binder error; attribution degraded", "err", err)
		return fallbackRef(ec.spec.Identity.IdentityRef, subjectRef), false
	}
	if bound.Firm && bound.IdentityRef != "" {
		return bound.IdentityRef, true
	}
	return fallbackRef(ec.spec.Identity.IdentityRef, subjectRef), false
}

// fallbackRef prefers the operator-declared identity ref, else the subject ref.
func fallbackRef(declared, subject string) string {
	if strings.TrimSpace(declared) != "" {
		return declared
	}
	return subject
}

// materializeWirings upserts the declared wirings for the definition's current
// spec and returns the PERMITTED edges to publish after commit. It runs inside
// the apply transaction (pure DB work; the external identity binding already
// happened in ensureIdentity). Upsert-by-natural-key makes re-applying the same
// spec idempotent rather than duplicating wirings.
func (m *Module) materializeWirings(ctx context.Context, sc store.Scope, tenant model.TenantID, ec execContext) ([]permittedEdge, int, error) {
	identityRef, firm := m.ensureIdentity(ctx, tenant, ec)
	subjectRef := ec.def.String(colSubjectRef)
	defID := ec.def.String(model.ColID)

	// The published edge's origin depends on attribution: a firm NHI is an
	// "identity" origin carrying the bound credential ref; a degraded wiring is an
	// "agent" origin against the subject itself (never the agent name dressed up as
	// a credential — that would mint a spurious Identity in bridge).
	attribution, confidence := attributionDegraded, sdkmodel.ConfidenceApproximate
	originKind, originRef := originKindAgent, subjectRef
	if firm {
		attribution, confidence = attributionFirm, sdkmodel.ConfidenceAttributed
		originKind, originRef = originKindIdentity, identityRef
	}

	repo, err := sc.Ext(wiringKind)
	if err != nil {
		return nil, 0, err
	}
	edges := make([]permittedEdge, 0, len(ec.spec.Wirings))
	for _, ws := range ec.spec.Wirings {
		rec, ok, err := findOne(ctx, repo,
			eq(colDefinitionRef, defID), eq(colAgentRef, subjectRef), eq(colResourceRef, ws.ResourceRef), eq(colMode, ws.Mode))
		if err != nil {
			return nil, 0, err
		}
		fields := model.Record{
			colDefinitionRef: defID, colAgentRef: subjectRef, colIdentityRef: identityRef,
			colResourceKind: ws.ResourceKind, colResourceRef: ws.ResourceRef, colMode: ws.Mode,
			colSecretRef: ws.SecretRef, colWiringStatus: wiringApplied, colAttribution: attribution,
			colRevNum: ec.currentVer,
		}
		if ok {
			for k, v := range fields {
				rec[k] = v
			}
			if _, err := repo.Update(ctx, rec); err != nil {
				return nil, 0, err
			}
		} else if _, err := repo.Create(ctx, fields); err != nil {
			return nil, 0, err
		}
		edges = append(edges, permittedEdge{
			originKind: originKind, originRef: originRef, resourceKind: ws.ResourceKind, resourceRef: ws.ResourceRef,
			mode: sdkmodel.AccessMode(ws.Mode), confidence: confidence,
		})
	}
	return edges, len(edges), nil
}

// revokeWirings marks every wiring of a definition revoked (on retire). It does
// not retract the PERMITTED edges already published: the EdgeObservation
// model has no retraction verb, so edge revocation is eventually reconciled by
// own staleness handling — a documented limitation, surfaced honestly in
// the wiring's revoked status rather than hidden.
func (m *Module) revokeWirings(ctx context.Context, sc store.Scope, defID model.ID) error {
	repo, err := sc.Ext(wiringKind)
	if err != nil {
		return err
	}
	recs, err := listAll(ctx, repo, eq(colDefinitionRef, defID.String()))
	if err != nil {
		return err
	}
	for _, rec := range recs {
		if rec.String(colWiringStatus) == wiringRevoked {
			continue
		}
		rec[colWiringStatus] = wiringRevoked
		if _, err := repo.Update(ctx, rec); err != nil {
			return err
		}
	}
	return nil
}

// publishPermittedEdges emits each declared wiring as a policy-grant
// EdgeObservation on the bus. Reconciles them into the PERMITTED side of the
// R/RW map; nothing else writes AccessEdge (decision A). It runs AFTER the apply
// transaction commits, so a rolled-back apply never signals a permitted edge.
// Minimal data (docs/SECURITY-HARDENING.md): only the origin/resource references and the access
// mode travel — never the secret_ref, never a payload.
func (m *Module) publishPermittedEdges(ctx context.Context, tenant model.TenantID, edges []permittedEdge) {
	if m.host == nil {
		return
	}
	now := m.clock.Now().Time()
	for _, e := range edges {
		obs := sdkmodel.EdgeObservation{
			OriginKind:   e.originKind,
			OriginRef:    e.originRef,
			ResourceKind: e.resourceKind,
			ResourceRef:  e.resourceRef,
			Mode:         e.mode,
			Source:       sdkmodel.SignalPolicy,
			Confidence:   e.confidence,
			ObservedAt:   now,
		}
		if err := m.host.Publish(ctx, event.FromObservation(tenant.String(), Name, obs)); err != nil {
			m.debugf("deploy: publish permitted edge failed", "resource", e.resourceRef, "err", err)
		}
	}
}

// wiringDTO is one declared PERMITTED connection — the contract (permitted-
// vs-observed) (change evidence) and (UI) consume. The secret_ref is a
// reference only; no secret material is ever present.
type wiringDTO struct {
	DefinitionID string `json:"definition_id"`
	AgentRef     string `json:"agent_ref"`
	IdentityRef  string `json:"identity_ref,omitempty"`
	ResourceKind string `json:"resource_kind"`
	ResourceRef  string `json:"resource_ref"`
	Mode         string `json:"mode"`
	SecretRef    string `json:"secret_ref,omitempty"`
	Status       string `json:"status"`
	Attribution  string `json:"attribution"`
	Version      int64  `json:"version"`
}

func toWiringDTO(rec model.Record) wiringDTO {
	return wiringDTO{
		DefinitionID: rec.String(colDefinitionRef), AgentRef: rec.String(colAgentRef), IdentityRef: rec.String(colIdentityRef),
		ResourceKind: rec.String(colResourceKind), ResourceRef: rec.String(colResourceRef), Mode: rec.String(colMode),
		SecretRef: rec.String(colSecretRef), Status: rec.String(colWiringStatus), Attribution: rec.String(colAttribution),
		Version: rec.Int(colRevNum),
	}
}

// handleListWirings lists the declared PERMITTED wirings, optionally filtered by
// definition_id or status. Read-tier (deploy:wiring:read).
func (m *Module) handleListWirings(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := strings.TrimSpace(r.URL.Query().Get("definition_id")); v != "" {
		q.Filters = append(q.Filters, eq(colDefinitionRef, v))
	}
	if v := strings.TrimSpace(r.URL.Query().Get("status")); v != "" {
		q.Filters = append(q.Filters, eq(colWiringStatus, v))
	}
	out := listResponse[wiringDTO]{Items: []wiringDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(wiringKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toWiringDTO(rec))
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
