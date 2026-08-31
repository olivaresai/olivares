// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

var workItemEntity = api.EntityRef{Kind: workItemKind, IDParam: "id", WorkspaceColumn: colWorkWorkspaceID}

func (m *Module) workRoutes(reg api.RouteRegistrar) {
	reg.Handle("POST", "/work-items", permWorkWrite, m.handleWorkCreate)
	reg.Handle("GET", "/work-items", permWorkRead, m.handleWorkList)
	reg.HandleEntity("GET", "/work-items/{id}", permWorkRead, workItemEntity, m.handleWorkGet)
	reg.HandleEntity("PATCH", "/work-items/{id}", permWorkWrite, workItemEntity, m.handleWorkUpdate)
	reg.HandleEntity("POST", "/work-items/{id}/transitions", permWorkWrite, workItemEntity, m.handleWorkTransition)
	reg.HandleEntity("POST", "/work-items/{id}/assignments", permWorkAdmin, workItemEntity, m.handleWorkAssignment)
	reg.HandleEntity("GET", "/work-items/{id}/dependencies", permWorkRead, workItemEntity, m.handleWorkDependencies)
	reg.HandleEntity("POST", "/work-items/{id}/dependencies", permWorkWrite, workItemEntity, m.handleWorkDependencyAdd)
	reg.HandleEntity("DELETE", "/work-items/{id}/dependencies/{dep_id}", permWorkWrite, workItemEntity, m.handleWorkDependencyRemove)
	reg.HandleEntity("GET", "/work-items/{id}/acceptance", permWorkRead, workItemEntity, m.handleWorkAcceptance)
	reg.HandleEntity("POST", "/work-items/{id}/acceptance", permWorkWrite, workItemEntity, m.handleWorkAcceptanceAdd)
	reg.HandleEntity("PATCH", "/work-items/{id}/acceptance/{criterion_id}", permWorkWrite, workItemEntity, m.handleWorkAcceptanceEvaluate)
	reg.HandleEntity("GET", "/work-items/{id}/events", permWorkRead, workItemEntity, m.handleWorkEvents)
	reg.HandleEntity("GET", "/work-items/{id}/lease", permLeaseRead, workItemEntity, m.handleWorkLeaseGet)
	reg.Handle("GET", "/leases", permLeaseRead, m.handleWorkLeaseList)
	reg.HandleEntity("POST", "/work-items/{id}/lease/acquire", permLeaseWrite, workItemEntity, m.handleWorkLeaseAcquire)
	reg.HandleEntity("POST", "/work-items/{id}/lease/renew", permLeaseWrite, workItemEntity, m.handleWorkLeaseRenew)
	reg.HandleEntity("POST", "/work-items/{id}/lease/release", permLeaseWrite, workItemEntity, m.handleWorkLeaseRelease)
	reg.HandleEntity("POST", "/work-items/{id}/lease/takeover", permLeaseAdmin, workItemEntity, m.handleWorkLeaseTakeover)
	reg.HandleEntity("POST", "/work-items/{id}/lease/revoke", permLeaseAdmin, workItemEntity, m.handleWorkLeaseRevoke)
	reg.HandleEntity("POST", "/work-items/{id}/lease/clock-rebase", permLeaseAdmin, workItemEntity, m.handleWorkLeaseClockRebase)
	reg.Handle("POST", "/work-events/{event_id}/replay", permWorkAdmin, m.handleWorkOutboxReplay)
	reg.Handle("POST", "/decisions", permDecisionWrite, m.handleWorkDecision)
	reg.Handle("GET", "/decisions", permDecisionRead, m.handleWorkDecisionList)
	decisionEntity := api.EntityRef{Kind: workDecisionKind, IDParam: "id", WorkspaceColumn: colWorkWorkspaceID}
	reg.HandleEntity("GET", "/decisions/{id}", permDecisionRead, decisionEntity, m.handleWorkDecisionGet)
	reg.HandleEntity("POST", "/decisions/{id}/revoke", permDecisionAdmin, decisionEntity, m.handleWorkDecisionRevoke)
	reg.Handle("GET", "/work-stream", permWorkRead, m.handleWorkStream)
}

func (m *Module) handleWorkCreate(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.handleWorkMutation(w, r, mc, "item.create", "")
}

func (m *Module) handleWorkUpdate(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.handleWorkMutation(w, r, mc, "item.update", chi.URLParam(r, "id"))
}

func (m *Module) handleWorkTransition(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.handleWorkMutation(w, r, mc, "", chi.URLParam(r, "id"))
}

func (m *Module) handleWorkAssignment(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.handleWorkMutation(w, r, mc, "item.assign", chi.URLParam(r, "id"))
}

func (m *Module) handleWorkDependencyAdd(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.handleWorkMutation(w, r, mc, "dependency.add", chi.URLParam(r, "id"))
}

func (m *Module) handleWorkDependencyRemove(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.handleWorkMutationTarget(w, r, mc, "dependency.remove", chi.URLParam(r, "id"), chi.URLParam(r, "dep_id"), "")
}

func (m *Module) handleWorkAcceptanceAdd(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.handleWorkMutation(w, r, mc, "acceptance.add", chi.URLParam(r, "id"))
}

func (m *Module) handleWorkAcceptanceEvaluate(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var cmd WorkCommand
	if !decodeWorkJSON(w, r, &cmd) {
		return
	}
	cmd.WorkItemID = model.ID(chi.URLParam(r, "id"))
	cmd.CriterionID = model.ID(chi.URLParam(r, "criterion_id"))
	command := cmd.Command
	if command == "" {
		normalized := normalizeWorkCommand(cmd)
		if len(normalized.Acceptance) == 1 && normalized.Acceptance[0].State != "" {
			command = "acceptance.evaluate"
		} else {
			command = "acceptance.update"
		}
	}
	if command != "acceptance.update" && command != "acceptance.evaluate" {
		writeWorkError(w, broken(http.StatusBadRequest, "invalid_command"))
		return
	}
	m.dispatchWorkMutation(w, r, mc, command, cmd)
}

func (m *Module) handleWorkLeaseAcquire(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.handleWorkMutation(w, r, mc, "lease.acquire", chi.URLParam(r, "id"))
}

func (m *Module) handleWorkLeaseRenew(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.handleWorkMutation(w, r, mc, "lease.renew", chi.URLParam(r, "id"))
}

func (m *Module) handleWorkLeaseRelease(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.handleWorkMutation(w, r, mc, "lease.release", chi.URLParam(r, "id"))
}

func (m *Module) handleWorkLeaseTakeover(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.handleWorkMutation(w, r, mc, "lease.takeover", chi.URLParam(r, "id"))
}

func (m *Module) handleWorkLeaseRevoke(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.handleWorkMutation(w, r, mc, "lease.revoke", chi.URLParam(r, "id"))
}

func (m *Module) handleWorkLeaseClockRebase(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.handleWorkMutation(w, r, mc, "lease.clock_rebase", chi.URLParam(r, "id"))
}

func (m *Module) handleWorkOutboxReplay(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	eventID, err := model.ParseID(chi.URLParam(r, "event_id"))
	if err != nil || eventID.IsZero() {
		writeWorkError(w, broken(http.StatusNotFound, "not_found"))
		return
	}
	// Dead-letter replay is tenant-wide recovery: its receipt is anchored to the
	// tenant audit chain and releasing a predecessor can unblock delivery beyond
	// the caller's selected row. A workspace-confined membership cannot acquire
	// that authority merely because its local role is admin.
	if _, confined := mc.Principal.ConfinedWorkspaceIn(mc.Tenant); confined {
		writeWorkError(w, broken(http.StatusForbidden, "forbidden"))
		return
	}
	mode := ExecutionMode(r.URL.Query().Get("mode"))
	if mode != ModeValidate && mode != ModePlan && mode != ModeApply {
		writeWorkError(w, broken(http.StatusBadRequest, "mode_required"))
		return
	}
	var body struct {
		PlanHash string `json:"plan_hash"`
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeWorkError(w, broken(http.StatusBadRequest, "invalid_command"))
		return
	}
	if trimmed := strings.TrimSpace(string(raw)); trimmed != "" {
		dec := json.NewDecoder(strings.NewReader(trimmed))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil {
			writeWorkError(w, broken(http.StatusBadRequest, "invalid_command"))
			return
		}
		var extra any
		if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
			writeWorkError(w, broken(http.StatusBadRequest, "invalid_command"))
			return
		}
	}
	headerPlan := r.Header.Get("If-Plan-Hash")
	if headerPlan != "" {
		if body.PlanHash != "" && body.PlanHash != headerPlan {
			writeWorkError(w, broken(http.StatusBadRequest, "invalid_command"))
			return
		}
		if _, err := decodeHash(headerPlan, true); err != nil {
			writeWorkError(w, broken(http.StatusBadRequest, "invalid_command"))
			return
		}
	}
	if mode == ModeApply && headerPlan == "" {
		writeWorkError(w, broken(http.StatusPreconditionFailed, "plan_changed"))
		return
	}
	expectedVersion, hasVersion, err := parseWorkETag(r.Header.Get("If-Match"))
	if err != nil {
		writeWorkError(w, err)
		return
	}
	if mode == ModeApply && !hasVersion {
		writeWorkError(w, broken(http.StatusPreconditionRequired, "version_required"))
		return
	}
	principal, err := workPrincipalFromAuth(mc.Principal, mc.Tenant)
	if err != nil {
		writeWorkError(w, unknown("evidence_unavailable", err))
		return
	}
	// Reaching this route is the authoritative work:admin decision, including a
	// future policy grant that is narrower than the tenant role cached in the
	// Principal. ReplayWorkOutbox still requires this bit for in-process callers.
	principal.Admin = true
	cmd := WorkOutboxReplayCommand{
		Command: "outbox.replay", EventID: eventID, PlanHash: body.PlanHash,
		ExpectedVersion: expectedVersion, ExpectedPlanHash: headerPlan,
		IdempotencyKey: r.Header.Get("Idempotency-Key"), HTTPMethod: r.Method,
		CommandScope: r.Method + " " + canonicalWorkPath(r.URL.Path),
	}
	switch mode {
	case ModeValidate, ModePlan:
		plan, err := m.planWorkOutboxReplayWithData(r.Context(), mc.Data, mc.Tenant, principal, cmd)
		if err != nil {
			if assessment, ok := assessmentFromError(
				m.clock.Now().String(), classifyWorkStoreError(err),
			); ok {
				if mode == ModeValidate {
					writeJSON(w, http.StatusOK, assessment)
				} else {
					writeJSON(w, http.StatusOK, Plan{
						Assessment: assessment, Command: "outbox.replay",
						RowEffects: []string{}, ExternalCalls: []string{},
					})
				}
				return
			}
			writeWorkError(w, err)
			return
		}
		if plan.ExpectedETag != "" {
			w.Header().Set("ETag", plan.ExpectedETag)
		}
		if mode == ModeValidate {
			plan.Assessment.PlanHash = ""
			writeJSON(w, http.StatusOK, plan.Assessment)
		} else {
			writeJSON(w, http.StatusOK, plan)
		}
	case ModeApply:
		result, err := m.replayWorkOutboxWithData(
			r.Context(), mc.Data, mc.Tenant, principal, cmd,
		)
		if err != nil {
			writeWorkError(w, err)
			return
		}
		if result.Replayed {
			w.Header().Set("Idempotency-Replayed", "true")
		}
		writeWorkOutboxReplay(w, http.StatusAccepted, result)
	}
}

// writeWorkOutboxReplay emits the exact durable response projection. This is
// important across upgrades: a K1 receipt may contain the legacy work_item_id
// spelling, and an exact retry must not silently rewrite it to the K3 dual-
// aggregate shape after the receipt has already committed.
func writeWorkOutboxReplay(w http.ResponseWriter, status int, result WorkOutboxReplay) {
	if result.responseJSON == "" {
		writeJSON(w, status, result)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, result.responseJSON)
}

func (m *Module) handleWorkDecision(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.handleWorkMutation(w, r, mc, "", "")
}

func (m *Module) handleWorkDecisionRevoke(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.handleWorkMutationDecision(w, r, mc, "decision.revoke", chi.URLParam(r, "id"))
}

func (m *Module) handleWorkMutation(w http.ResponseWriter, r *http.Request, mc api.ModuleContext, command, itemID string) {
	m.handleWorkMutationTarget(w, r, mc, command, itemID, "", "")
}

func (m *Module) handleWorkMutationDecision(w http.ResponseWriter, r *http.Request, mc api.ModuleContext, command, decisionID string) {
	var cmd WorkCommand
	if !decodeWorkJSON(w, r, &cmd) {
		return
	}
	cmd.DecisionID = model.ID(decisionID)
	m.dispatchWorkMutation(w, r, mc, command, cmd)
}

// leaseWorkCommandRequest is the closed HTTP projection shared by the six lease
// routes. WorkCommand is deliberately wider because REST, CLI and in-process
// callers share it; decoding a lease body straight into that type silently
// accepted unrelated fields (for example title) that the lease operation ignored.
// Keep this projection in lockstep with the beta OpenAPI declaration.
type leaseWorkCommandRequest struct {
	Command          string   `json:"command"`
	HolderSID        string   `json:"holder_sid"`
	HolderRunRef     string   `json:"holder_run_ref"`
	HolderAgentRef   string   `json:"holder_agent_ref"`
	TTLSeconds       int64    `json:"ttl_seconds"`
	Fence            int64    `json:"fence"`
	Force            bool     `json:"force"`
	Unblock          bool     `json:"unblock"`
	ChangesRequested bool     `json:"changes_requested"`
	Reason           string   `json:"reason"`
	DecisionID       model.ID `json:"decision_id"`
	EvidenceRef      string   `json:"evidence_ref"`
	PlanHash         string   `json:"plan_hash"`
}

func (in leaseWorkCommandRequest) workCommand() WorkCommand {
	return WorkCommand{
		Command:          in.Command,
		HolderSID:        in.HolderSID,
		HolderRunRef:     in.HolderRunRef,
		HolderAgentRef:   in.HolderAgentRef,
		TTLSeconds:       in.TTLSeconds,
		Fence:            in.Fence,
		Force:            in.Force,
		Unblock:          in.Unblock,
		ChangesRequested: in.ChangesRequested,
		Reason:           in.Reason,
		DecisionID:       in.DecisionID,
		EvidenceRef:      in.EvidenceRef,
		PlanHash:         in.PlanHash,
	}
}

func (m *Module) handleWorkMutationTarget(w http.ResponseWriter, r *http.Request, mc api.ModuleContext, command, itemID, targetID, criterionID string) {
	var cmd WorkCommand
	if strings.HasPrefix(command, "lease.") {
		var body leaseWorkCommandRequest
		if !decodeWorkJSON(w, r, &body) {
			return
		}
		cmd = body.workCommand()
	} else {
		if !decodeWorkJSON(w, r, &cmd) {
			return
		}
	}
	if itemID != "" {
		cmd.WorkItemID = model.ID(itemID)
	}
	if targetID != "" {
		cmd.TargetID = model.ID(targetID)
	}
	if criterionID != "" {
		cmd.CriterionID = model.ID(criterionID)
	}
	m.dispatchWorkMutation(w, r, mc, command, cmd)
}

func (m *Module) dispatchWorkMutation(w http.ResponseWriter, r *http.Request, mc api.ModuleContext, routeCommand string, cmd WorkCommand) {
	mode := ExecutionMode(r.URL.Query().Get("mode"))
	if mode != ModeValidate && mode != ModePlan && mode != ModeApply {
		writeWorkError(w, broken(http.StatusBadRequest, "mode_required"))
		return
	}
	if routeCommand != "" {
		if cmd.Command != "" && cmd.Command != routeCommand {
			writeWorkError(w, broken(http.StatusBadRequest, "invalid_command"))
			return
		}
		cmd.Command = routeCommand
	} else if !validRouteBodyCommand(r.URL.Path, cmd.Command) {
		writeWorkError(w, broken(http.StatusBadRequest, "invalid_command"))
		return
	}
	if planHash := r.Header.Get("If-Plan-Hash"); planHash != "" {
		if cmd.PlanHash != "" && cmd.PlanHash != planHash {
			writeWorkError(w, broken(http.StatusBadRequest, "invalid_command"))
			return
		}
		if _, err := decodeHash(planHash, true); err != nil {
			writeWorkError(w, broken(http.StatusBadRequest, "invalid_command"))
			return
		}
		cmd.ExpectedPlanHash = planHash
	}
	cmd = normalizeWorkCommand(cmd)
	// The server-issued work-session bearer holds work:write only so the shared
	// route wrapper can admit fenced execution mutations. Keep that permission a
	// hard route-local subset: it is not authority to author backlog, dependencies,
	// decisions, assignments, or terminal review actions. Lease admin routes are
	// already outside the credential's core/auth ceiling.
	if mc.Principal.IsWorkSessionCredential() && !workSessionCommandAllowed(cmd.Command) {
		writeWorkError(w, broken(http.StatusForbidden, "forbidden"))
		return
	}
	cmd.HTTPMethod, cmd.CommandScope = r.Method, canonicalWorkCommandScope(r.Method, r.URL.Path, cmd)
	if v, present, err := parseWorkETag(r.Header.Get("If-Match")); err != nil {
		writeWorkError(w, err)
		return
	} else if present {
		cmd.ExpectedVersion = v
	}
	cmd.IdempotencyKey = r.Header.Get("Idempotency-Key")
	principal, err := workPrincipalFromAuth(mc.Principal, mc.Tenant)
	if err != nil {
		writeWorkError(w, unknown("evidence_unavailable", err))
		return
	}
	// The assignment route itself is mounted at work:admin, so reaching this
	// handler is authoritative even for a workspace-scoped Cedar grant that is
	// not reflected in Principal.RoleIn. Archive shares the transition route
	// with write-tier verbs; fail also admits the current owner, so those two
	// require a resource-scoped second authorization question.
	if cmd.Command == "item.assign" {
		principal.Admin = true
	} else if !principal.Admin && (cmd.Command == "item.archive" || cmd.Command == "item.fail") {
		principal.Admin, err = m.authorizeWorkAdmin(r, mc, cmd.WorkItemID)
		if err != nil {
			writeWorkError(w, err)
			return
		}
	}

	switch mode {
	case ModeValidate, ModePlan:
		plan, err := m.planWithData(r.Context(), mc.Data, mc.Tenant, principal, cmd)
		if err != nil {
			if assessment, ok := assessmentFromError(m.clock.Now().String(), classifyWorkStoreError(err)); ok {
				if mode == ModeValidate {
					writeJSON(w, http.StatusOK, assessment)
				} else {
					writeJSON(w, http.StatusOK, Plan{Assessment: assessment, Command: cmd.Command, RowEffects: []string{}, ExternalCalls: []string{}})
				}
				return
			}
			writeWorkError(w, err)
			return
		}
		if plan.ExpectedETag != "" {
			w.Header().Set("ETag", plan.ExpectedETag)
		}
		if mode == ModeValidate {
			plan.Assessment.PlanHash = ""
			writeJSON(w, http.StatusOK, plan.Assessment)
		} else {
			writeJSON(w, http.StatusOK, plan)
		}
	case ModeApply:
		result, err := m.applyWithData(r.Context(), mc.Data, mc.Tenant, principal, cmd)
		if err != nil {
			writeWorkError(w, err)
			return
		}
		if result.Version > 0 {
			w.Header().Set("ETag", fmt.Sprintf("\"v%d\"", result.Version))
		}
		if result.Replayed {
			w.Header().Set("Idempotency-Replayed", "true")
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func workSessionCommandAllowed(command string) bool {
	switch command {
	case "lease.acquire", "lease.renew", "lease.release",
		"item.block", "item.submit", "item.fail", "acceptance.evaluate":
		return true
	default:
		return false
	}
}

func (m *Module) authorizeWorkAdmin(r *http.Request, mc api.ModuleContext, itemID model.ID) (bool, error) {
	if m.workAuthz == nil || itemID.IsZero() {
		return false, nil
	}
	var workspace model.ID
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(workItemKind)
		if err != nil {
			return err
		}
		item, err := repo.Get(r.Context(), itemID)
		if err != nil {
			return err
		}
		workspace = model.ID(item.String(colWorkWorkspaceID))
		return nil
	})
	if err != nil {
		return false, classifyWorkStoreError(err)
	}
	resource := auth.ResourceFor(permWorkAdmin)
	resource.ID, resource.WorkspaceID = itemID.String(), workspace
	decision := m.workAuthz.Authorize(r.Context(), auth.Request{
		Principal: mc.Principal, Permission: permWorkAdmin, Tenant: mc.Tenant, Resource: resource,
	})
	return decision.Allow, nil
}

func validRouteBodyCommand(path, command string) bool {
	if strings.HasSuffix(path, "/transitions") {
		switch command {
		case "item.ready", "item.block", "item.unblock", "item.submit", "item.complete", "item.fail", "item.cancel", "item.archive":
			return true
		}
		return false
	}
	if strings.HasSuffix(path, "/decisions") || path == "/v1/m/sessions/decisions" {
		return command == "decision.set" || command == "decision.supersede"
	}
	return command != ""
}

func canonicalWorkPath(path string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if _, err := model.ParseID(part); err == nil {
			parts[i] = "{id}"
		}
	}
	return strings.Join(parts, "/")
}

func canonicalWorkCommandScope(method, path string, cmd WorkCommand) string {
	scope := method + " " + canonicalWorkPath(path)
	for _, target := range []struct {
		name string
		id   model.ID
	}{
		{name: "workspace", id: cmd.WorkspaceID},
		{name: "work_item", id: cmd.WorkItemID},
		{name: "target", id: cmd.TargetID},
		{name: "criterion", id: cmd.CriterionID},
		{name: "decision", id: cmd.DecisionID},
	} {
		if !target.id.IsZero() {
			scope += ";" + target.name + "=" + target.id.String()
		}
	}
	return scope
}

func decodeWorkJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeWorkError(w, broken(http.StatusBadRequest, "invalid_command"))
		return false
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		writeWorkError(w, broken(http.StatusBadRequest, "invalid_command"))
		return false
	}
	return true
}

func parseWorkETag(value string) (int64, bool, error) {
	if value == "" {
		return 0, false, nil
	}
	if len(value) < 4 || value[0:2] != "\"v" || value[len(value)-1] != '"' || strings.HasPrefix(value, "W/") {
		return 0, false, broken(http.StatusBadRequest, "invalid_command")
	}
	n, err := strconv.ParseInt(value[2:len(value)-1], 10, 64)
	if err != nil || n < 1 {
		return 0, false, broken(http.StatusBadRequest, "invalid_command")
	}
	return n, true, nil
}

func workPrincipalFromAuth(p auth.Principal, tenant model.TenantID) (WorkPrincipal, error) {
	actor, err := p.AttributableActor()
	if err != nil {
		return WorkPrincipal{}, err
	}
	role, _ := p.RoleIn(tenant)
	admin := p.Superadmin || auth.RoleRank(role) >= auth.RoleRank(auth.RoleAdmin)
	if p.AgentIdentity != "" {
		return WorkPrincipal{ActorKind: model.ActorAgent, ActorRef: p.AgentIdentity, Actor: actor,
			Admin: admin, SessionID: p.SessionIdentity, SessionRunRef: p.SessionRunRef,
			SessionFence:      p.SessionFence,
			PurposeRestricted: p.IsPurposeRestricted()}, nil
	}
	if !p.UserID.IsZero() {
		return WorkPrincipal{ActorKind: model.ActorUser, ActorRef: p.UserID.String(), Actor: actor,
			Admin: admin, SessionID: p.SessionIdentity, SessionRunRef: p.SessionRunRef,
			SessionFence:      p.SessionFence,
			PurposeRestricted: p.IsPurposeRestricted()}, nil
	}
	if p.SessionIdentity != "" {
		// ⛔ UNA SESION CONDUCIDA SE ATRIBUYE COMO SESION, y sin esto NO PUEDE ESCRIBIR.
		//
		// La credencial que el propio motor acuña para una sesión operada es un token
		// (`core/auth/worksession.go`), y `Principal.ActorKind()` devuelve "token" para
		// KindToken. Ese valor va tal cual a `sessions_work_event.actor_kind`, y el
		// trigger del propio esquema sólo admite `user|agent|session|system`
		// (`migrations/sqlite/0084_…:12`, y su gemelo Postgres `0016_…:23`).
		//
		// Medido contra un motor real el 2026-08-24: `lease.acquire` en `validate` y
		// `plan` sale LIMPIO —no escriben— y en `apply` muere con
		// `constraint failed: olivares: invalid sessions communication event
		// vocabulary, payload or evidence hash (1811)`. Es decir: la mitad de sesión
		// del lease es INALCANZABLE, que es exactamente la asimetría que
		// `principalIsWorkOwner` (work_service.go:1074-1082) ya nombró y cerró en el
		// otro sitio donde un WorkItem pregunta quién es su dueño. Esta es la mitad
		// que quedó viva, y aquí no hay una prueba que leer: hay una columna con un
		// vocabulario cerrado.
		//
		// El `ActorRef` pasa a ser el SID canónico por la misma razón: quien actuó fue
		// la sesión, no el identificador de su credencial, y el SID es lo que el resto
		// del plano usa para hablar de ella (owner_ref de un item de sesión lo es).
		//
		// ⚠ POR QUE ESTO NO MUEVE EL FALLO A OTRA TABLA, comprobado y no supuesto: de
		// los vocabularios de `*_by_kind` del esquema, los ÚNICOS que no admiten
		// `session` son los de `sessions_work_decision` (`0007_…:16`, `0008_…:18`), y
		// una credencial de sesión de trabajo NO PUEDE ejecutar `decision.set` ni
		// `decision.supersede`: `workSessionCommandAllowed` no los lista. Dos controles
		// independientes coinciden, que es lo que hace segura esta línea.
		// `string(ActorSession)` y no un literal: la constante tipada ya existe en este
		// mismo paquete (`communication_model.go:325-337`) y su validador exige un SID
		// canónico, que es exactamente lo que se pone en el ref. Señalado por el
		// contraste `sol max` del 2026-08-24.
		return WorkPrincipal{ActorKind: string(ActorSession), ActorRef: p.SessionIdentity, Actor: actor,
			Admin: admin, SessionID: p.SessionIdentity, SessionRunRef: p.SessionRunRef,
			SessionFence:      p.SessionFence,
			PurposeRestricted: p.IsPurposeRestricted()}, nil
	}
	return WorkPrincipal{ActorKind: p.ActorKind(), ActorRef: p.CredID.String(), Actor: actor,
		Admin: admin, SessionID: p.SessionIdentity, SessionRunRef: p.SessionRunRef,
		SessionFence:      p.SessionFence,
		PurposeRestricted: p.IsPurposeRestricted()}, nil
}

// writeWorkError answers on the WORK vocabulary, not the module one: a verdict and
// a stable code alongside the status, which is why this module has its own
// classifier (classifyWorkStoreError) instead of a copy of writeStoreError. That
// shape is deliberate and is kept exactly.
//
// What the StoreErrorStatus call adds is ONLY the status, and only for a sentinel
// the work classifier does not name. Before it, store.ErrTenantSuspended,
// store.ErrTenantNotInService, store.ErrResidencyViolation and
// store.ErrCursorWithSort all fell past the classifier and were answered 500 —
// the same drift core/api/moduleerrors.go was written for, in the one member of
// the family that could not simply delegate.
//
// THE CODE AND THE VERDICT ARE NOT TOUCHED, and that is a decision rather than an
// omission. They are the work vocabulary, the console switches on them
// (web/src/features/work/work-section.tsx:55 reads the code and falls back to
// observation_unavailable), and inventing a name for a state this vocabulary has
// never had is a semantic call for whoever owns K1 — not a side effect of
// centralizing a status. So these four now answer 423/423/403/400 with the code
// still internal_error: honest about the refusal, honest that the work layer has
// no name for it yet. Naming them is left declared, not done.
func writeWorkError(w http.ResponseWriter, err error) {
	err = classifyWorkStoreError(err)
	status, code, verdict := http.StatusInternalServerError, "internal_error", VerdictUnknown
	// ⛔ `field` SE PERDIA AQUI, y con el la mitad del arreglo. `validate` y `plan` salen por
	// `assessmentFromError`, que si lo pone en `evidence_ref`; `apply` sale por AQUI, y esta
	// funcion leia status/code/verdict y TIRABA el campo. Resultado: el mismo comando que en
	// `validate` te decia «blocked_code (o code)» te contestaba MUDO en `apply` — la mitad
	// del camino de tres fases seguia obligando a adivinar. Lo destapo un contraste; mi
	// testigo no lo veia porque sólo ejercitaba el camino de validate.
	campo := ""
	if we := asWorkError(err); we != nil {
		status, code, verdict = we.status, we.code, we.verdict
		campo = we.field
	} else if shared, _, ok := api.StoreErrorStatus(err); ok {
		status = shared
	} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		// ⛔ UN CLIENTE QUE SE VA NO ES UN INCIDENTE, y sin esta rama lo era.
		//
		// `classifyWorkStoreError` no conoce estos dos (work_service.go), asi que caian en
		// el brazo de abajo y CADA navegacion que abandona una peticion del plano de trabajo
		// escribia una linea ERROR. Es el mismo fallo de ruido que la tercera asercion de mi
		// propio test existe para impedir en los errores clasificados — y lo introduje yo en
		// el commit anterior. Lo cazo una revision sobre mi diff.
		//
		// El modulo ya trata estos dos aparte en otro sitio (work_outbox.go:971-973), asi que
		// esto sigue su precedente en vez de inventar uno.
		slog.Default().Debug("sessions: work-plane request abandoned by its caller",
			"err", redactErr(err))
	} else {
		// ⛔ AQUI SE DESTRUIA EL ERROR, Y ES LA UNICA COPIA QUE HABIA.
		//
		// Un error que no se clasifica sale como `internal_error` — y eso hacia el
		// CLIENTE es correcto y deliberado: el cuerpo no filtra internos. Lo que no
		// puede ser es que tampoco quede en el SERVIDOR. Medido el 2026-08-24 contra
		// un motor real: `POST …/lease/acquire?mode=apply` respondio 500 y el log de
		// peticiones dijo `status=500 dur_ms=7` y nada mas. El operador no tiene por
		// donde empezar, y el que escribio el codigo tampoco.
		//
		// Son 86 llamadores en cuatro ficheros del plano de trabajo: la clase entera
		// era ciega. `redactErr` acota el mensaje igual que en el resto del modulo.
		// `err.Error()` y NO `redactErr`: este destino es el LOG DEL SERVIDOR, que no tiene
		// limite de divulgacion. `redactErr` corta a 160 bytes y en la primera linea —un
		// presupuesto escrito para un mensaje que ademas viaja en un cuerpo de API—, y una
		// cadena envuelta del store (`work item …: mutate …: exec …: <causa>`) lo pasa de
		// largo: lo que se perdia era justo la causa raiz. El cuerpo de la respuesta sigue
		// sin llevar nada, que es donde el limite SI aplica.
		slog.Default().Error("sessions: unclassified work-plane error surfaced as internal_error",
			"err", err.Error())
	}
	cuerpo := map[string]any{
		"verdict": verdict, "code": code,
		"error": map[string]string{"code": code, "message": code},
	}
	if campo != "" {
		// Mismo nombre de clave que en `validate`/`plan`, para que el llamante no tenga que
		// mirar en dos sitios distintos segun la fase.
		cuerpo["evidence_ref"] = campo
	}
	writeJSON(w, status, cuerpo)
}

func (m *Module) handleWorkGet(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, err := model.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		writeWorkError(w, broken(http.StatusNotFound, "not_found"))
		return
	}
	out, err := m.getWorkWithData(r.Context(), mc.Data, id)
	if err != nil {
		writeWorkError(w, err)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf("\"v%d\"", out.Item.Version))
	writeJSON(w, http.StatusOK, out)
}

func (m *Module) handleWorkLeaseGet(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, err := model.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		writeWorkError(w, broken(http.StatusNotFound, "not_found"))
		return
	}
	out, workVersion, err := m.getLeaseAndWorkVersionWithData(r.Context(), mc.Data, id)
	if err != nil {
		writeWorkError(w, err)
		return
	}
	// Lease mutations use the parent WorkItem's If-Match. Returning the lease
	// row version here made the natural GET lease -> renew/release sequence fail
	// with 412 whenever an unrelated WorkItem write had advanced only the parent.
	w.Header().Set("ETag", fmt.Sprintf("\"v%d\"", workVersion))
	writeJSON(w, http.StatusOK, out)
}

func (m *Module) handleWorkLeaseList(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	limit, err := queryLimit(r)
	if err != nil {
		writeWorkError(w, err)
		return
	}
	allowed := map[string]bool{
		"limit": true, "cursor": true, "work_item_id": true,
		"holder_sid": true, "state": true, "expires_before": true,
	}
	for key, values := range r.URL.Query() {
		if !allowed[key] || len(values) != 1 {
			writeWorkError(w, broken(http.StatusBadRequest, "invalid_command"))
			return
		}
	}
	filters := map[string]string{}
	for _, key := range []string{"work_item_id", "holder_sid", "state", "expires_before"} {
		if value := r.URL.Query().Get(key); value != "" {
			filters[key] = value
		}
	}
	out, err := m.listLeasesWithData(r.Context(), mc.Data, WorkLeaseQuery{
		Limit: limit, Cursor: r.URL.Query().Get("cursor"), Filters: filters,
	})
	if err != nil {
		writeWorkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (m *Module) handleWorkList(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	limit, err := queryLimit(r)
	if err != nil {
		writeWorkError(w, err)
		return
	}
	allowed := map[string]bool{
		"limit": true, "cursor": true, "status": true, "priority": true,
		"work_kind": true, "owner_kind": true, "owner_ref": true,
		"provenance_kind": true, "provenance_ref": true, "parent_id": true,
		"archived": true, "due_before": true, "updated_after": true,
	}
	for key, values := range r.URL.Query() {
		if !allowed[key] || len(values) != 1 {
			writeWorkError(w, broken(http.StatusBadRequest, "invalid_command"))
			return
		}
	}
	filters := map[string]string{}
	for _, key := range []string{"status", "priority", "work_kind", "owner_kind", "owner_ref", "provenance_kind", "provenance_ref", "parent_id", "archived", "due_before", "updated_after"} {
		if value := r.URL.Query().Get(key); value != "" {
			filters[key] = value
		}
	}
	out, err := m.listWorkWithData(r.Context(), mc.Data, WorkQuery{Limit: limit, Cursor: r.URL.Query().Get("cursor"), Filters: filters})
	if err != nil {
		writeWorkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func queryLimit(r *http.Request) (int, error) {
	if r.URL.Query().Get("limit") == "" {
		return 100, nil
	}
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n < 1 || n > 200 {
		return 0, broken(http.StatusBadRequest, "invalid_command")
	}
	return n, nil
}

func (m *Module) handleWorkDependencies(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.handleWorkChildren(w, r, mc, workDependencyKind)
}

func (m *Module) handleWorkAcceptance(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.handleWorkChildren(w, r, mc, workAcceptanceKind)
}

func (m *Module) handleWorkEvents(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.handleWorkChildren(w, r, mc, workEventKind)
}

func (m *Module) handleWorkChildren(w http.ResponseWriter, r *http.Request, mc api.ModuleContext, kind model.Kind) {
	id, err := model.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		writeWorkError(w, broken(http.StatusNotFound, "not_found"))
		return
	}
	limit, cursor, err := workChildrenQuery(r)
	if err != nil {
		writeWorkError(w, err)
		return
	}
	leaseEventsReadable := true
	var eventParent model.Record
	if kind == workEventKind {
		err = mc.Data.View(r.Context(), func(sc store.Scope) error {
			items, viewErr := sc.Ext(workItemKind)
			if viewErr != nil {
				return viewErr
			}
			eventParent, viewErr = items.Get(r.Context(), id)
			return viewErr
		})
		if err != nil {
			writeWorkError(w, err)
			return
		}
		// Keep external/scoped PDP resolution outside the store transaction. A
		// scoped authorizer may resolve hierarchy from the store itself.
		leaseEventsReadable = m.canReadWorkLeaseEvents(r.Context(), mc, eventParent)
	}
	rows := []model.Record{}
	var page model.Page
	err = mc.Data.View(r.Context(), func(sc store.Scope) error {
		parent := eventParent
		if kind != workEventKind {
			items, viewErr := sc.Ext(workItemKind)
			if viewErr != nil {
				return viewErr
			}
			parent, viewErr = items.Get(r.Context(), id)
			if viewErr != nil {
				return viewErr
			}
		}
		repo, err := sc.Ext(kind)
		if err != nil {
			return err
		}
		parentColumn := colWorkItemID
		filters := []model.Filter{{
			Column: colWorkWorkspaceID, Op: model.OpEq, Value: parent.String(colWorkWorkspaceID),
		}}
		if kind == workEventKind {
			parentColumn = colEventAggregateID
			filters = append(filters, model.Filter{
				Column: colEventAggregateKind, Op: model.OpEq, Value: string(workItemKind),
			})
		}
		filters = append(filters, model.Filter{Column: parentColumn, Op: model.OpEq, Value: id.String()})
		rows, page, err = repo.List(r.Context(), model.Query{
			Filters: filters, Limit: limit, Cursor: cursor,
		})
		if err == nil && kind == workEventKind && !leaseEventsReadable {
			for i, row := range rows {
				rows[i] = projectWorkEventForRead(row, false)
			}
		}
		return err
	})
	if err != nil {
		writeWorkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": api.JSONArray[model.Record](rows), "next_cursor": page.Cursor, "has_more": page.HasMore,
	})
}

// canReadWorkLeaseEvents is the second authorization question required by the
// mixed WorkEvent surfaces. sessions:work:read grants the ordinary item timeline,
// but a work.lease.* row carries holder identity, run refs, fences and expiry. The
// latter is projected only when the caller also holds sessions:lease:read for the
// row's WorkItem/workspace. An unwired authorizer denies this more sensitive slice.
func (m *Module) canReadWorkLeaseEvents(
	ctx context.Context,
	mc api.ModuleContext,
	event model.Record,
) bool {
	if m.workAuthz == nil {
		return false
	}
	resource := auth.ResourceFor(permLeaseRead)
	resource.ID = event.String(colEventAggregateID)
	if resource.ID == "" {
		resource.ID = event.String(model.ColID)
	}
	resource.WorkspaceID = model.ID(event.String(colWorkWorkspaceID))
	return m.workAuthz.Authorize(ctx, auth.Request{
		Principal:  mc.Principal,
		Permission: permLeaseRead,
		Tenant:     mc.Tenant,
		Resource:   resource,
	}).Allow
}

func isWorkLeaseEvent(eventType string) bool {
	return strings.HasPrefix(eventType, "work.lease.")
}

const redactedWorkLeaseEventPayload = `{"redacted":true,"required_permission":"sessions:lease:read"}`

// projectWorkEventForRead preserves the stable WorkEvent envelope and sequence
// while withholding the lease authority payload. Replacing the whole row (rather
// than only payload_json) also removes actor/command/audit/payload hashes that can
// correlate the privileged document. Ordinary WorkEvents remain byte-for-byte
// unchanged, including for a caller without lease:read.
func projectWorkEventForRead(row model.Record, leaseReadable bool) model.Record {
	if leaseReadable || !isWorkLeaseEvent(row.String(colEventType)) {
		return row
	}
	return model.Record{
		colWorkWorkspaceID:    row.String(colWorkWorkspaceID),
		colEventID:            row.String(colEventID),
		colEventAggregateKind: row.String(colEventAggregateKind),
		colEventAggregateID:   row.String(colEventAggregateID),
		colEventSeq:           row.Int(colEventSeq),
		colEventType:          row.String(colEventType),
		colEventOccurredAt:    row.String(colEventOccurredAt),
		colEventPayload:       redactedWorkLeaseEventPayload,
	}
}

func workChildrenQuery(r *http.Request) (int, string, error) {
	limit, err := queryLimit(r)
	if err != nil {
		return 0, "", err
	}
	for key, values := range r.URL.Query() {
		if key != "limit" && key != "cursor" || len(values) != 1 {
			return 0, "", broken(http.StatusBadRequest, "invalid_command")
		}
	}
	cursor := r.URL.Query().Get("cursor")
	if !validWorkCursor(cursor) {
		return 0, "", broken(http.StatusBadRequest, "invalid_cursor")
	}
	return limit, cursor, nil
}

func (m *Module) handleWorkDecisionGet(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.handleGenericWorkGet(w, r, mc, workDecisionKind)
}

func (m *Module) handleGenericWorkGet(w http.ResponseWriter, r *http.Request, mc api.ModuleContext, kind model.Kind) {
	id, err := model.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		writeWorkError(w, broken(http.StatusNotFound, "not_found"))
		return
	}
	var row model.Record
	err = mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(kind)
		if err != nil {
			return err
		}
		row, err = repo.Get(r.Context(), id)
		return err
	})
	if err != nil {
		writeWorkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (m *Module) handleWorkDecisionList(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	limit, err := queryLimit(r)
	if err != nil || !validWorkCursor(r.URL.Query().Get("cursor")) {
		writeWorkError(w, broken(http.StatusBadRequest, "invalid_cursor"))
		return
	}
	headState, headView, err := requestedDecisionHeadState(r.URL.Query())
	if err != nil {
		writeWorkError(w, err)
		return
	}
	allowed := map[string]string{"work_item_id": colWorkItemID, "decision_key": colDecisionKey, "subject_kind": colDecisionSubjectKind, "subject_ref": colDecisionSubjectRef, "decided_by_kind": colDecisionByKind, "decided_by_ref": colDecisionByRef}
	filters := []model.Filter{}
	for key, values := range r.URL.Query() {
		if key == "limit" || key == "cursor" || key == "effective" || key == "revoked" {
			continue
		}
		column := allowed[key]
		if column == "" || len(values) != 1 {
			writeWorkError(w, broken(http.StatusBadRequest, "invalid_command"))
			return
		}
		filters = append(filters, model.Filter{Column: column, Op: model.OpEq, Value: values[0]})
	}
	var rows []model.Record
	var page model.Page
	err = mc.Data.View(r.Context(), func(sc store.Scope) error {
		if headView {
			rows, page, err = listCurrentWorkDecisions(
				r.Context(), sc, headState, filters, limit, r.URL.Query().Get("cursor"),
			)
			return err
		}
		repo, err := sc.Ext(workDecisionKind)
		if err != nil {
			return err
		}
		rows, page, err = repo.List(r.Context(), model.Query{Filters: filters, Limit: limit, Cursor: r.URL.Query().Get("cursor")})
		return err
	})
	if err != nil {
		writeWorkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": api.JSONArray[model.Record](rows), "next_cursor": page.Cursor, "has_more": page.HasMore,
	})
}

// requestedDecisionHeadState turns the two boolean API filters into the one
// closed DecisionHead state. With no state filter the endpoint remains the
// append-only history view. Supplying either filter opts into the current-head
// projection; consistent pairs such as effective=true&revoked=false are valid.
func requestedDecisionHeadState(values map[string][]string) (string, bool, error) {
	state := ""
	filtered := false
	for _, key := range []string{"effective", "revoked"} {
		raw, present := values[key]
		if !present {
			continue
		}
		if len(raw) != 1 {
			return "", false, broken(http.StatusBadRequest, "invalid_command")
		}
		value, err := strconv.ParseBool(raw[0])
		if err != nil {
			return "", false, broken(http.StatusBadRequest, "invalid_command")
		}
		wanted := "effective"
		if key == "effective" && !value || key == "revoked" && value {
			wanted = "revoked"
		}
		if filtered && state != wanted {
			return "", false, broken(http.StatusBadRequest, "invalid_command")
		}
		state, filtered = wanted, true
	}
	return state, filtered, nil
}

// listCurrentWorkDecisions walks DecisionHead in its native UUIDv7 id order.
// Its continuation token is therefore a head-row id rather than the projected
// Decision id; the cursor is deliberately opaque to callers. Filters available
// on the head are pushed into SQL. Subject and actor filters are evaluated on
// the referenced current Decision while the scan continues until it has a full
// page, so post-filtering cannot deform has_more or skip a later matching head.
func listCurrentWorkDecisions(
	ctx context.Context,
	sc store.Scope,
	state string,
	filters []model.Filter,
	limit int,
	cursor string,
) ([]model.Record, model.Page, error) {
	heads, err := sc.Ext(workDecisionHeadKind)
	if err != nil {
		return nil, model.Page{}, err
	}
	decisions, err := sc.Ext(workDecisionKind)
	if err != nil {
		return nil, model.Page{}, err
	}
	headFilters := []model.Filter{{Column: colDecisionHeadState, Op: model.OpEq, Value: state}}
	decisionFilters := make([]model.Filter, 0, len(filters))
	for _, filter := range filters {
		switch filter.Column {
		case colWorkItemID, colDecisionKey:
			headFilters = append(headFilters, filter)
		default:
			decisionFilters = append(decisionFilters, filter)
		}
	}

	rows := make([]model.Record, 0, limit+1)
	rowCursors := make([]string, 0, limit+1)
	scanCursor := cursor
	for {
		headRows, headPage, err := heads.List(ctx, model.Query{
			Filters: headFilters, Limit: 200, Cursor: scanCursor,
		})
		if err != nil {
			return nil, model.Page{}, err
		}
		for _, head := range headRows {
			currentID, err := model.ParseID(head.String(colDecisionCurrentID))
			if err != nil || currentID.IsZero() {
				return nil, model.Page{}, unknown("evidence_unavailable", err)
			}
			decision, err := decisions.Get(ctx, currentID)
			if err != nil {
				return nil, model.Page{}, unknown("evidence_unavailable", err)
			}
			if !workDecisionMatches(decision, decisionFilters) {
				continue
			}
			projected := make(model.Record, len(decision)+1)
			for key, value := range decision {
				projected[key] = value
			}
			projected[colDecisionHeadState] = head.String(colDecisionHeadState)
			rows = append(rows, projected)
			rowCursors = append(rowCursors, head.String(model.ColID))
			if len(rows) > limit {
				return rows[:limit], model.Page{Cursor: rowCursors[limit-1], HasMore: true}, nil
			}
		}
		if !headPage.HasMore || headPage.Cursor == "" {
			return rows, model.Page{}, nil
		}
		scanCursor = headPage.Cursor
	}
}

func workDecisionMatches(row model.Record, filters []model.Filter) bool {
	for _, filter := range filters {
		value, ok := filter.Value.(string)
		if filter.Op != model.OpEq || !ok || row.String(filter.Column) != value {
			return false
		}
	}
	return true
}

func (m *Module) handleWorkStream(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	cursor := r.URL.Query().Get("cursor")
	if header := r.Header.Get("Last-Event-ID"); header != "" {
		if cursor != "" && cursor != header {
			writeWorkError(w, broken(http.StatusBadRequest, "invalid_cursor"))
			return
		}
		cursor = header
	}
	if !validWorkCursor(cursor) {
		writeWorkError(w, broken(http.StatusBadRequest, "invalid_cursor"))
		return
	}
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	_ = rc.SetWriteDeadline(time.Time{})
	if err := rc.Flush(); err != nil {
		writeWorkError(w, unknown("observation_unavailable", nil))
		return
	}
	ticker := time.NewTicker(time.Second)
	heartbeat := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	defer heartbeat.Stop()
	for {
		emitted, next, err := m.streamWorkEvents(r, mc, w, cursor)
		if err != nil {
			writeWorkStreamFailure(w, err)
			_ = rc.Flush()
			return
		}
		cursor = next
		if emitted {
			if err := rc.Flush(); err != nil {
				return
			}
			continue
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		case <-heartbeat.C:
			_ = rc.SetWriteDeadline(time.Now().Add(streamWriteTimeout))
			if _, err := io.WriteString(w, ": heartbeat\n\n"); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		}
	}
}

func writeWorkStreamFailure(w io.Writer, err error) {
	classified := classifyWorkStoreError(err)
	code := "observation_unavailable"
	if we := asWorkError(classified); we != nil && we.verdict == VerdictUnknown {
		code = we.code
	}
	payload, marshalErr := json.Marshal(map[string]any{
		"verdict": VerdictUnknown,
		"code":    code,
		"checks":  []WorkCheck{{Name: code, Verdict: VerdictUnknown}},
	})
	if marshalErr != nil {
		payload = []byte(`{"verdict":"NO_HE_PODIDO_MIRAR","code":"observation_unavailable"}`)
	}
	_, _ = fmt.Fprintf(w, "event: olivares.error\ndata: %s\n\n", payload)
}

func (m *Module) streamWorkEvents(r *http.Request, mc api.ModuleContext, w io.Writer, cursor string) (bool, string, error) {
	var rows []model.Record
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(workEventKind)
		if err != nil {
			return err
		}
		// /work-stream is the K1 compatibility surface. The durable event table is
		// dual-aggregate in K3, but work:read must never disclose standalone Message
		// events; communication routes apply their own C5 authority gates.
		filters := []model.Filter{{
			Column: colEventAggregateKind, Op: model.OpEq, Value: string(workItemKind),
		}}
		if cursor != "" {
			filters = append(filters, model.Filter{Column: colEventID, Op: model.OpGt, Value: cursor})
		}
		rows, _, err = repo.List(r.Context(), model.Query{Filters: filters, Sort: []model.Sort{{Column: colEventID}}, Limit: 200})
		return err
	})
	if err != nil {
		return false, cursor, err
	}
	permissions := map[string]bool{}
	emitted := false
	for _, row := range rows {
		cursor = row.String(colEventID)
		payload := json.RawMessage(row.String(colEventPayload))
		if isWorkLeaseEvent(row.String(colEventType)) {
			key := row.String(colWorkWorkspaceID) + "\x00" + row.String(colEventAggregateID)
			allowed, decided := permissions[key]
			if !decided {
				allowed = m.canReadWorkLeaseEvents(r.Context(), mc, row)
				permissions[key] = allowed
			}
			if !allowed {
				payload = json.RawMessage(redactedWorkLeaseEventPayload)
			}
		}
		data, _ := json.Marshal(map[string]any{
			"event_id": row.String(colEventID), "workspace_id": row.String(colWorkWorkspaceID),
			"aggregate_kind": row.String(colEventAggregateKind), "aggregate_id": row.String(colEventAggregateID),
			"seq": row.Int(colEventSeq), "type": row.String(colEventType),
			"occurred_at": row.String(colEventOccurredAt), "payload": payload,
		})
		if _, err := fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", row.String(colEventID), row.String(colEventType), data); err != nil {
			return false, cursor, err
		}
		emitted = true
	}
	return emitted, cursor, nil
}
