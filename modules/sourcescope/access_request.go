// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

type accessRequestInput struct {
	SourceType   string `json:"source_type"`
	SourceRef    string `json:"source_ref"`
	WorkspaceRef string `json:"workspace_ref"`
	ExpiresAt    string `json:"expires_at,omitempty"`
	Note         string `json:"note,omitempty"`
}

// handleCreateAccessRequest records a self-service request for a time-boxed
// source binding. It does not grant access: the responsible department admin or
// owner must approve the stored request before the binding is created.
func (m *Module) handleCreateAccessRequest(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in accessRequestInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if mc.Principal.UserID.IsZero() {
		writeJSON(w, http.StatusForbidden, errorBody("access requests require a person"))
		return
	}
	proposed := bindingDTO{
		SourceType: in.SourceType, SourceRef: in.SourceRef,
		ScopeTree: scopeUser, ScopeRef: mc.Principal.UserID.String(),
		Effect: effectAllow, Enabled: true, Note: in.Note, ExpiresAt: in.ExpiresAt,
	}
	if msg := proposed.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	if m.profileAssignment == nil {
		writeProfileAssignmentBusiness(w)
		return
	}
	if err := m.mayWriteSource(in.SourceType); err != nil {
		writeStoreError(w, err) // the licence refusal: 403 addon_requires_license
		return
	}
	workspaceRef := in.WorkspaceRef
	requesterWorkspace, err := m.requesterDepartment(r.Context(), mc.Tenant, mc.Principal.UserID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var out postureRequestDTO
	err = mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		if !requesterWorkspace.IsZero() {
			slug, err := store.WorkspaceSlug(r.Context(), sc, requesterWorkspace)
			if err != nil {
				return err
			}
			if strings.TrimSpace(workspaceRef) == "" {
				workspaceRef = slug
			}
			if workspaceRef != slug {
				return errAccessWorkspace
			}
		}
		workspaceID, _, err := resolveScope(r.Context(), sc, scopeWorkspace, &workspaceRef)
		if err != nil {
			return err
		}
		if !requesterWorkspace.IsZero() && workspaceID != requesterWorkspace {
			return errAccessWorkspace
		}
		// A department member cannot enumerate tenant source bindings or policy.
		// Its own pending proposal grants nothing; the approver checks those facts.
		targetID := ""
		if _, confined := mc.Principal.ConfinedWorkspaceIn(mc.Tenant); !confined {
			confined, err := sourceConfined(r.Context(), sc, proposed.SourceType, proposed.SourceRef)
			if err != nil {
				return err
			}
			if !confined {
				return store.ErrConflict
			}
			duplicate, _, _, err := createPreflight(r.Context(), sc, proposed.SourceType, proposed.SourceRef, scopeOf(proposed))
			if err != nil {
				return err
			}
			if duplicate {
				targetID, err = m.expiredBindingForRenewal(r.Context(), sc, proposed)
				if err != nil {
					return err
				}
				if targetID == "" {
					return store.ErrConflict
				}
			}
		}
		resolved, err := auth.ResolveGrantExpiry(proposed.ExpiresAt, m.clock.Now().Time(), auth.GrantExpiryPolicy{})
		if err != nil {
			return expiryFailure(err)
		}
		proposal := accessProposal{bindingDTO: proposed}
		if strings.TrimSpace(in.ExpiresAt) != "" {
			at, err := time.Parse(time.RFC3339Nano, resolved)
			if err != nil {
				return err
			}
			proposal.Lifetime = at.Sub(m.clock.Now().Time())
			if proposal.Lifetime <= 0 {
				return validationError("expires_at must be in the future")
			}
		}
		raw, err := json.Marshal(proposal)
		if err != nil {
			return err
		}
		out, err = m.createPostureRequestJSONAt(r.Context(), sc, mc, postureOpAccessRequest, proposed.SourceType, proposed.SourceRef, targetID, "self-service access request", string(raw), workspaceID.String())
		if err != nil {
			return err
		}
		event, err := sc.Audit().Append(r.Context(), model.AuditDraft{
			Actor: mc.Principal.Actor(), ActorKind: mc.Principal.ActorKind(),
			Action: "sourcescope.access.request", TargetKind: postureRequestKind, TargetID: model.ID(out.ID),
			Meta: map[string]any{
				"source_type": proposed.SourceType, "source_ref": proposed.SourceRef,
				"workspace_id": workspaceID.String(), "expires_at": proposed.ExpiresAt,
			},
		})
		if err != nil {
			return err
		}
		if event.Seq == 0 {
			return errors.New("sourcescope: access request audit was not sealed")
		}
		return nil
	})
	if errors.Is(err, errAccessWorkspace) {
		writeJSON(w, http.StatusForbidden, errorBody(err.Error()))
		return
	}
	if verr, ok := err.(validationError); ok {
		writeJSON(w, http.StatusBadRequest, errorBody(string(verr)))
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	m.emitAccessRequest(r.Context(), mc.Tenant, out)
	writeJSON(w, http.StatusAccepted, out)
}

// sourceConfined reports whether an enabled allow binding (expired or not) confines
// the source. Without one nobody is refused, and approving a request would create the
// first allow and lock everyone else out.
func sourceConfined(ctx context.Context, sc store.Scope, sourceType, sourceRef string) (bool, error) {
	recs, err := allExt(ctx, sc, bindingKind, eq(colSourceType, sourceType), eq(colSourceRef, sourceRef))
	if err != nil {
		return false, err
	}
	for _, rec := range recs {
		if rec.Bool(colEnabled) && normalizeEffect(rec.String(colEffect)) == effectAllow {
			return true, nil
		}
	}
	return false, nil
}

func (m *Module) expiredBindingForRenewal(ctx context.Context, sc store.Scope, proposed bindingDTO) (string, error) {
	recs, err := allExt(ctx, sc, bindingKind, eq(colSourceType, proposed.SourceType), eq(colSourceRef, proposed.SourceRef))
	if err != nil {
		return "", err
	}
	for _, rec := range recs {
		binding := toBindingDTO(rec)
		if !binding.Enabled || scopeOf(binding) != scopeOf(proposed) {
			continue
		}
		expired, err := bindingExpired(binding, m.clock.Now().Time())
		if err != nil {
			return "", err
		}
		if expired {
			return binding.ID, nil
		}
	}
	return "", nil
}

func bindingExpired(binding bindingDTO, now time.Time) (bool, error) {
	if binding.ExpiresAt == "" {
		return false, nil
	}
	expires, err := time.Parse(time.RFC3339Nano, binding.ExpiresAt)
	if err != nil {
		return false, fmt.Errorf("sourcescope: binding %s has an invalid expiry: %w", binding.ID, err)
	}
	return !now.Before(expires), nil
}

// handleApproveAccessRequest approves one pending access request when the caller is the
// department admin or owner it names and a different person from the requester, and creates
// the time-boxed binding it asked for.
func (m *Module) handleApproveAccessRequest(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	allowed, err := m.accessRequestDecisionAllowed(r.Context(), mc, id, true, true)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !allowed {
		writeJSON(w, http.StatusForbidden, errorBody("access requests require the department admin or owner for the requested workspace"))
		return
	}
	m.decidePostureRequest(w, r, mc, true)
}

func (m *Module) accessRequestDecisionAllowed(ctx context.Context, mc api.ModuleContext, id model.ID, accessRoute, approve bool) (bool, error) {
	var rec model.Record
	if err := mc.Data.View(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(postureRequestKind)
		if err != nil {
			return err
		}
		rec, err = repo.Get(ctx, id)
		return err
	}); err != nil {
		return false, err
	}
	if rec.String(colPROp) != postureOpAccessRequest {
		return !accessRoute, nil
	}
	if !m.mayApproveAccessRequest(ctx, mc, rec) {
		return false, nil
	}
	// Refusing a request grants nothing, even after a department transfer.
	if !approve {
		return true, nil
	}
	return m.accessRequestSourceAllowed(ctx, mc, rec)
}

func (m *Module) accessRequestSourceAllowed(ctx context.Context, mc api.ModuleContext, rec model.Record) (bool, error) {
	requesterWorkspace, err := m.requesterDepartment(ctx, mc.Tenant, model.ID(rec.String(colPRProposerUser)))
	if err != nil {
		return false, err
	}
	if !requesterWorkspace.IsZero() && requesterWorkspace != model.ID(rec.String(colPRWorkspaceID)) {
		return false, nil
	}
	hasOwnerWorkspace := false
	var otherWorkspaces []model.ID
	if err := mc.Data.View(ctx, func(sc store.Scope) error {
		bindings, err := allExt(ctx, sc, bindingKind,
			eq(colSourceType, rec.String(colPRSourceType)),
			eq(colSourceRef, rec.String(colPRSourceRef)),
		)
		if err != nil {
			return err
		}
		requestedWorkspace := model.ID(rec.String(colPRWorkspaceID))
		for _, binding := range bindings {
			if !binding.Bool(colEnabled) {
				continue
			}
			owner, err := bindingOwnerWorkspace(ctx, sc, binding)
			if err != nil {
				return err
			}
			// Only a permanent allow makes a department the source's owner. A forbid
			// restricts and owns nothing; an expired allow grants the department nothing;
			// a time-boxed one would let its admin approve grants that outlive it (a
			// delegation cannot, see canDelegateAt). All of those leave the tenant admin to
			// decide. The ceiling below still counts every enabled binding.
			// shortcut: a time-boxed department allow sends its requests to the tenant
			// admin; cap the approved expiry at the owner's instead once departments need
			// to approve under a time box.
			if !owner.IsZero() && normalizeEffect(binding.String(colEffect)) == effectAllow && binding.String(colExpiresAt) == "" {
				hasOwnerWorkspace = true
			}
			if !owner.IsZero() && owner != requestedWorkspace {
				otherWorkspaces = append(otherWorkspaces, owner)
			}
		}
		return nil
	}); err != nil {
		return false, err
	}
	if requesterWorkspace.IsZero() || !hasOwnerWorkspace {
		// A workspace-less Resource is mapped to a confined caller workspace by
		// Authorize, so explicitly reject confined callers before the tenant check.
		if _, confined := mc.Principal.ConfinedWorkspaceIn(mc.Tenant); confined {
			return false, nil
		}
		if !m.authorizer.Authorize(ctx, auth.Request{Principal: mc.Principal, Tenant: mc.Tenant, Permission: permPostureApprove, Resource: auth.ResourceAttrs{Kind: string(postureRequestKind)}}).Allow {
			return false, nil
		}
	}
	for _, workspaceID := range otherWorkspaces {
		if !m.authorizer.Authorize(ctx, auth.Request{
			Principal: mc.Principal, Tenant: mc.Tenant, Permission: permPostureApprove,
			Resource: auth.ResourceAttrs{Kind: string(postureRequestKind), ID: rec.String(model.ColID), WorkspaceID: workspaceID},
		}).Allow {
			return false, nil
		}
	}
	return true, nil
}

// bindingOwnerWorkspace is the workspace whose admin owns a binding's source: the
// workspace itself, or the one an agent group or folder belongs to. Subject trees
// (user, group, role, session, agent) name no workspace and own nothing.
func bindingOwnerWorkspace(ctx context.Context, sc store.Scope, binding model.Record) (model.ID, error) {
	switch binding.String(colScopeTree) {
	case scopeWorkspace:
		workspace, found, err := findWorkspaceBySlug(ctx, sc, binding.String(colScopeRef))
		if err != nil {
			return "", err
		}
		if !found {
			return "", fmt.Errorf("sourcescope: binding %s names an unknown workspace", binding.String(model.ColID))
		}
		return workspace.ID, nil
	case scopeAgentGroup, scopeFolder:
		return model.ID(binding.String(colWorkspaceID)), nil
	}
	return "", nil
}

func (m *Module) mayApproveAccessRequest(ctx context.Context, mc api.ModuleContext, rec model.Record) bool {
	if m.authorizer == nil || rec.String(colPROp) != postureOpAccessRequest {
		return false
	}
	workspaceID := model.ID(rec.String(colPRWorkspaceID))
	if workspaceID.IsZero() {
		return false
	}
	return m.authorizer.Authorize(ctx, auth.Request{
		Principal: mc.Principal, Tenant: mc.Tenant, Permission: permPostureApprove,
		Resource: auth.ResourceAttrs{Kind: string(postureRequestKind), ID: rec.String(model.ColID), WorkspaceID: workspaceID},
	}).Allow
}

func (m *Module) emitAccessRequest(ctx context.Context, tenant model.TenantID, request postureRequestDTO) {
	host := m.host0()
	if host == nil {
		return
	}
	sum := sha256.Sum256([]byte("sourcescope_access_requested|" + request.ID))
	finding := sdkmodel.FindingReport{
		Kind: "sourcescope_access_requested", Severity: sdkmodel.SeverityMedium,
		SubjectKind: "access_request", SubjectRef: request.ID, Title: "Access requested",
		DetailHash: hex.EncodeToString(sum[:]), OccurredAt: m.clock.Now().Time(),
	}
	if err := host.Publish(ctx, event.FromObservation(tenant.String(), Name, finding)); err != nil && m.log != nil {
		m.log.Warn("sourcescope: could not notify approvers of an access request; it stays pending", "request", request.ID, "err", err)
	}
}

func validateAccessProposal(request model.Record, proposed bindingDTO) error {
	if request.String(colPROp) != postureOpAccessRequest {
		return fmt.Errorf("not an access request")
	}
	if proposed.ScopeTree != scopeUser || proposed.ScopeRef != request.String(colPRProposerUser) {
		return validationError("access proposal does not match its requester")
	}
	return nil
}

var errAccessWorkspace = errors.New("access request workspace must match the requester's department")

type accessProposal struct {
	bindingDTO
	Lifetime time.Duration `json:"requested_lifetime_ns,omitempty"`
}

func (m *Module) requesterDepartment(ctx context.Context, tenant model.TenantID, user model.ID) (model.ID, error) {
	if m.requesterWorkspace == nil {
		return "", errors.New("sourcescope: requester membership lookup is unavailable")
	}
	return m.requesterWorkspace(ctx, tenant, user)
}
