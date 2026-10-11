// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"reflect"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// OrchestrationWorkScopeSource checks operator-owned authority using the raw
// tenant transaction, then supplies only a workspace-confined work scope. It
// never gives a request handler the profile or an unconstrained store handle.
type OrchestrationWorkScopeSource interface {
	WithScope(context.Context, auth.Principal, model.TenantID, bool, func(store.Scope) error) error
}

type orchestrationLaunchProfileKey struct{}

// This launch term stays outside ProviderHomeSnapshot and its historical digest.
type orchestrationLaunchProfile struct {
	ProfileID        string
	SessionWorkGrant string
}

func withOrchestrationLaunchProfile(ctx context.Context, snapshot *ProviderHomeSnapshot, grant string) context.Context {
	if snapshot == nil || grant == "" {
		return ctx
	}
	return context.WithValue(ctx, orchestrationLaunchProfileKey{}, orchestrationLaunchProfile{ProfileID: snapshot.ProfileID, SessionWorkGrant: grant})
}

func lockOrchestrationProfile(ctx context.Context, sc store.Scope, profile string) error {
	locker, ok := sc.(store.TransactionLocker)
	if !ok {
		return forbiddenErr("session work grant serialization is unavailable")
	}
	return locker.LockTransaction(ctx, "sessions-work-grant:"+profile)
}

// OrchestrationCredentialSpec is a composition-root-only mint/renew seam. Mint
// checks the pre-reservation snapshot; renew resolves the exact stored run. A
// missing grant selects the unchanged worker issuer. A malformed or changed
// grant refuses rather than substituting either worker or operator authority.
func (m *Module) OrchestrationCredentialSpec(ctx context.Context, req WorkSessionCredentialRequest, mint bool) (*auth.OrchestrationSessionCredentialSpec, error) {
	if m.Data == nil {
		return nil, errNoData
	}
	if mint && req.OrchestrationGrant == "" {
		return nil, nil
	}
	var result *auth.OrchestrationSessionCredentialSpec
	err := m.Data.View(ctx, req.Tenant, func(sc store.Scope) error {
		ref := req.OrchestrationProfileRef
		if !mint {
			repo, err := sc.Ext(runKind)
			if err != nil {
				return err
			}
			run, err := findRunRec(ctx, repo, req.RunRef)
			if err != nil {
				return err
			}
			if run.String(colRunClaimSID) != req.SessionRef || run.Int(colClaimFence) != req.ClaimFence || run.String(colRunAgentRef) != req.AgentRef {
				return forbiddenErr("runtime work binding changed")
			}
			ref = run.String(colRunProfileID)
		}
		if ref == "" {
			return nil
		}
		rec, err := findProfileRec(ctx, sc, ref)
		if err != nil {
			return err
		}
		grant, err := decodeProfileWorkGrant(rec.String(colPPSessionWorkGrant))
		if err != nil {
			return err
		}
		if grant == nil {
			if mint {
				return forbiddenErr("session work grant was withdrawn")
			}
			return nil
		}
		if rec.String(colPPState) != ProfileActive || req.AgentRef == "" {
			return forbiddenErr("orchestration requires an active profile and authenticated agent")
		}
		if mint && rec.String(colPPSessionWorkGrant) != req.OrchestrationGrant {
			return forbiddenErr("session work grant changed since launch preflight")
		}
		workspace, err := communicationWorkspaceWithin(ctx, sc, req.RunRef, req.SessionRef)
		if err != nil {
			return err
		}
		if ws, err := sc.Workspaces().Get(ctx, workspace); err != nil || ws.Status != model.StatusActive {
			return forbiddenErr("orchestration workspace is inactive")
		}
		claim, found, err := findClaim(ctx, sc, req.SessionRef)
		if err != nil {
			return err
		}
		if !found || !claimIsLive(claim, m.now()) || claim.Int(colFence) != req.ClaimFence || workspace != grant.WorkspaceID {
			return forbiddenErr("orchestration requires its workspace and live Claim")
		}
		result = &auth.OrchestrationSessionCredentialSpec{
			Tenant: req.Tenant, WorkspaceID: workspace, SessionRef: req.SessionRef, RunRef: req.RunRef,
			AgentRef: req.AgentRef, ClaimFence: req.ClaimFence, ProfileRef: ref, GrantID: grant.GrantID, Capabilities: grant.Capabilities,
		}
		return nil
	})
	return result, err
}

// WithOrchestrationWorkScope must run inside the composition adapter's tenant
// transaction. Profile revocation and writes serialize on the same lock; the
// Claim joins the transaction after work locks and before commit. Callbacks see
// only confined rows; a failed Claim CAS rolls back their audit and domain writes.
func (m *Module) WithOrchestrationWorkScope(ctx context.Context, sc store.Scope, p auth.Principal, tenant model.TenantID, mutate bool, fn func(store.Scope) error) error {
	binding, ok := p.OrchestrationSessionGrant()
	workspace, confined := p.ConfinedWorkspaceIn(tenant)
	if !ok || !confined || workspace != p.SessionWorkspaceID {
		return broken(http.StatusForbidden, "forbidden")
	}
	if mutate {
		if err := lockOrchestrationProfile(ctx, sc, binding.ProfileRef); err != nil {
			return err
		}
	}
	profile, err := findProfileRec(ctx, sc, binding.ProfileRef)
	if err != nil {
		return broken(http.StatusForbidden, "forbidden")
	}
	grant, err := decodeProfileWorkGrant(profile.String(colPPSessionWorkGrant))
	if err != nil {
		return err
	}
	if profile.String(colPPState) != ProfileActive || grant == nil || grant.GrantID != binding.GrantID || grant.WorkspaceID != workspace || !reflect.DeepEqual(grant.Capabilities, binding.Capabilities) {
		return broken(http.StatusForbidden, "forbidden")
	}
	repo, err := sc.Ext(runKind)
	if err != nil {
		return err
	}
	run, err := findRunRec(ctx, repo, p.SessionRunRef)
	if err != nil {
		return broken(http.StatusForbidden, "forbidden")
	}
	if run.String(colState) != stateRunning || run.String(colRunProfileID) != binding.ProfileRef || run.String(colRunClaimSID) != p.SessionIdentity || run.Int(colClaimFence) != p.SessionFence || run.String(colRunAgentRef) != p.AgentIdentity {
		return broken(http.StatusForbidden, "forbidden")
	}
	resolved, err := communicationWorkspaceWithin(ctx, sc, p.SessionRunRef, p.SessionIdentity)
	if err != nil || resolved != workspace {
		return broken(http.StatusForbidden, "forbidden")
	}
	if ws, err := sc.Workspaces().Get(ctx, workspace); err != nil || ws.Status != model.StatusActive {
		return broken(http.StatusForbidden, "forbidden")
	}
	claim, found, err := findClaim(ctx, sc, p.SessionIdentity)
	if err != nil {
		return err
	}
	if !found || !claimIsLive(claim, m.now()) || claim.Int(colFence) != p.SessionFence || claim.String(colHolder) != run.String(colClaimHolder) {
		return broken(http.StatusForbidden, "forbidden")
	}
	scope, err := store.ConfineWorkspace(ctx, sc, workspace)
	if err != nil {
		return err
	}
	if err := fn(scope); err != nil {
		return err
	}
	if mutate {
		// Work commands have no external calls. Join Claim after their authority
		// locks, in the established identity-before-Claim order. Refusal rolls
		// back every callback effect in this same transaction.
		if err := fenceWithin(ctx, sc, p.SessionIdentity, run.String(colClaimHolder), p.SessionFence, m.now()); err != nil {
			return err
		}
	}
	return nil
}

type orchestrationWorkData struct {
	api.ScopedData
	source    OrchestrationWorkScopeSource
	principal auth.Principal
	tenant    model.TenantID
}

func (d orchestrationWorkData) View(ctx context.Context, fn func(store.Scope) error) error {
	return d.source.WithScope(ctx, d.principal, d.tenant, false, fn)
}
func (d orchestrationWorkData) Mutate(ctx context.Context, fn func(store.Scope) error) error {
	return d.source.WithScope(ctx, d.principal, d.tenant, true, fn)
}

func orchestrationHasCapability(p auth.Principal, capability string) bool {
	binding, ok := p.OrchestrationSessionGrant()
	if !ok {
		return false
	}
	for _, cap := range binding.Capabilities {
		if cap == capability {
			return true
		}
	}
	return false
}

func orchestrationCommandCapability(command string) string {
	switch command {
	case "item.create", "item.update", "item.ready", "dependency.add", "dependency.remove", "acceptance.add", "acceptance.update":
		return "work.create"
	case "item.assign":
		return "work.assign"
	case "item.complete", "item.block", "item.unblock", "item.fail", "item.cancel", "acceptance.evaluate":
		return "work.review"
	case "decision.set", "decision.supersede":
		return "decision.write"
	default:
		return ""
	}
}

type orchestrationWorkRegistrar struct {
	api.RouteRegistrar
	module *Module
}

func (r orchestrationWorkRegistrar) wrap(pattern string, handler api.ModuleHandler) api.ModuleHandler {
	return func(w http.ResponseWriter, req *http.Request, mc api.ModuleContext) {
		if mc.Principal.IsOrchestrationSessionCredential() {
			if pattern == "/work-events/{event_id}/replay" || r.module.OrchestrationScopes == nil {
				writeWorkError(w, broken(http.StatusForbidden, "forbidden"))
				return
			}
			mc.Data = orchestrationWorkData{ScopedData: mc.Data, source: r.module.OrchestrationScopes, principal: mc.Principal, tenant: mc.Tenant}
		}
		handler(w, req, mc)
	}
}
func (r orchestrationWorkRegistrar) Handle(method, pattern string, perm auth.Permission, handler api.ModuleHandler) {
	r.RouteRegistrar.Handle(method, pattern, perm, r.wrap(pattern, handler))
}
func (r orchestrationWorkRegistrar) HandleEntity(method, pattern string, perm auth.Permission, ref api.EntityRef, handler api.ModuleHandler) {
	r.RouteRegistrar.HandleEntity(method, pattern, perm, ref, r.wrap(pattern, handler))
}
