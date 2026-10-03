// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// CallSessionWork is the internal port for the session MCP's closed work
// envelopes. It receives an already resolved session principal, never a bearer.
// The existing work handlers retain validation, versions, idempotency and audit;
// this port supplies their authorization and mandatory workspace boundary.
// Communication, arbitrary module routes and exports are not admitted here.
func (m *Module) CallSessionWork(w http.ResponseWriter, r *http.Request, p auth.Principal, tenant model.TenantID) {
	companion, err := m.RuntimeCompanion(r.Context(), tenant, p)
	workspace, confined := p.ConfinedWorkspaceIn(tenant)
	if err != nil || !confined || workspace.IsZero() || m.workAuthz == nil || m.data == nil {
		writeWorkError(w, broken(http.StatusForbidden, "forbidden"))
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(companion.Context, cancel)
	defer cancel()
	defer stop()
	r = r.WithContext(ctx)
	data := sessionWorkData{data: m.data, tenant: tenant, workspace: workspace}
	mc := api.ModuleContext{Principal: p, Tenant: tenant, Data: data, Standing: m.standing}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/m/sessions/"), "/")
	if len(parts) == 0 || (parts[0] != "work-items" && parts[0] != "decisions") {
		writeWorkError(w, broken(http.StatusNotFound, "not_found"))
		return
	}
	permission, kind, id := permWorkRead, workItemKind, model.ID("")
	if parts[0] == "decisions" {
		permission, kind = permDecisionRead, workDecisionKind
	}
	var cmd WorkCommand
	if r.Method == http.MethodGet {
		if len(parts) > 2 {
			writeWorkError(w, broken(http.StatusNotFound, "not_found"))
			return
		}
		if len(parts) == 2 {
			id, err = model.ParseID(parts[1])
			if err != nil {
				writeWorkError(w, broken(http.StatusNotFound, "not_found"))
				return
			}
		}
	} else {
		if !decodeWorkJSON(w, r, &cmd) {
			return
		}
		permission, err = sessionWorkPermission(cmd.Command)
		if err != nil {
			writeWorkError(w, err)
			return
		}
		// The lease envelope omits its parent ID; it is supplied by the path.
		if strings.HasPrefix(cmd.Command, "lease.") && len(parts) == 4 && parts[2] == "lease" {
			cmd.WorkItemID, err = model.ParseID(parts[1])
			if err != nil {
				writeWorkError(w, broken(http.StatusBadRequest, "invalid_command"))
				return
			}
		}
		if !cmd.WorkspaceID.IsZero() && cmd.WorkspaceID != workspace {
			writeWorkError(w, broken(http.StatusForbidden, "forbidden"))
			return
		}
		if cmd.Command == "item.create" && cmd.WorkspaceID.IsZero() {
			cmd.WorkspaceID = workspace
		}
		id, kind = cmd.WorkItemID, workItemKind
	}
	if !id.IsZero() {
		// Resolve through the confined store before authorization: a foreign ID
		// is concealed even when the launcher was a superadmin.
		err = data.View(ctx, func(sc store.Scope) error {
			repo, err := sc.Ext(kind)
			if err != nil {
				return err
			}
			_, err = repo.Get(ctx, id)
			return err
		})
		if err != nil {
			writeWorkError(w, err)
			return
		}
	}
	resource := auth.ResourceFor(permission)
	resource.ID, resource.WorkspaceID = id.String(), workspace
	decision := m.workAuthz.Authorize(ctx, auth.Request{Principal: p, Tenant: tenant, Permission: permission, Resource: resource})
	if decision.Allow && cmd.OwnerKind == "session" && (cmd.Command == "item.create" || cmd.Command == "item.assign") {
		// Serialize peer changes with the send, using the existing run exclusion.
		// RuntimeCompanion released this same lock before returning above.
		release, err := m.rt.lockRunContext(ctx, liveKey(tenant, p.SessionRunRef))
		if err != nil {
			writeWorkError(w, err)
			return
		}
		defer release()
		decision.Allow, err = m.sessionPeerAllowed(ctx, tenant, p, companion, cmd.OwnerRef)
		if err != nil {
			writeWorkError(w, err)
			return
		}
	}
	if !decision.Allow {
		err := data.Mutate(ctx, func(sc store.Scope) error {
			_, err := sc.Audit().Append(ctx, model.AuditDraft{
				Actor: p.Actor(), ActorKind: p.ActorKind(), Action: "sessions.work.tool.denied",
				TargetKind: kind, TargetID: id,
				Meta: map[string]any{"permission": string(permission), "session_ref": p.SessionIdentity, "run_ref": p.SessionRunRef},
			})
			return err
		})
		if err != nil {
			writeWorkError(w, err)
			return
		}
		writeWorkError(w, broken(http.StatusForbidden, "forbidden"))
		return
	}
	mc.Resource = resource
	if r.Method != http.MethodGet {
		m.dispatchWorkMutation(w, r, mc, cmd.Command, cmd)
		return
	}
	route := chi.NewRouteContext()
	route.URLParams.Add("id", id.String())
	r = r.WithContext(context.WithValue(ctx, chi.RouteCtxKey, route))
	switch {
	case parts[0] == "work-items" && id.IsZero():
		m.handleWorkList(w, r, mc)
	case parts[0] == "work-items":
		m.handleWorkGet(w, r, mc)
	case id.IsZero():
		m.handleWorkDecisionList(w, r, mc)
	default:
		m.handleWorkDecisionGet(w, r, mc)
	}
}

func sessionWorkPermission(command string) (auth.Permission, error) {
	switch command {
	case "item.create", "item.update", "item.assign", "item.ready", "item.block", "item.unblock", "item.submit", "item.complete", "item.fail", "item.cancel", "dependency.add", "dependency.remove", "acceptance.add", "acceptance.update", "acceptance.evaluate", "lease.acquire", "lease.renew", "lease.release", "decision.set", "decision.supersede":
		return auth.Permission(workCommandPermission(command)), nil
	default:
		return "", broken(http.StatusBadRequest, "invalid_command")
	}
}

// This boundary also applies when a session was launched by a superadmin.
// It never relies on an HTTP middleware mark or on the caller's filters.
type sessionWorkData struct {
	data      api.ModuleData
	tenant    model.TenantID
	workspace model.ID
}

func (d sessionWorkData) inWorkspace(ctx context.Context, fn func(store.Scope) error) func(store.Scope) error {
	return func(sc store.Scope) error {
		bounded, err := store.ConfineWorkspace(ctx, sc, d.workspace)
		if err != nil {
			return err
		}
		return fn(bounded)
	}
}

func (d sessionWorkData) View(ctx context.Context, fn func(store.Scope) error) error {
	return d.data.View(ctx, d.tenant, d.inWorkspace(ctx, fn))
}

func (d sessionWorkData) Mutate(ctx context.Context, fn func(store.Scope) error) error {
	return d.data.Mutate(ctx, d.tenant, d.inWorkspace(ctx, fn))
}

func (d sessionWorkData) Export(context.Context, func(store.ExportScope) error) error {
	return store.ErrReadOnly
}
