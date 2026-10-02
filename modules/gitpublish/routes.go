// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package gitpublish

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// sealedRegistrar is the governed door. A registrar without it mounts no
// mutation route: a governed route never falls back to Handle.
type sealedRegistrar interface {
	HandleSealed(method, pattern string, perm auth.Permission, s api.SealedRoute, h api.ModuleHandler)
}

func sealed(action auth.CedarAction, aal int) api.SealedRoute {
	s, err := api.SealRoute(api.RouteMetadata{RouteMetadata: auth.RouteMetadata{CedarAction: string(action), MinimumAAL: aal}})
	if err != nil {
		panic("gitpublish: route metadata: " + err.Error())
	}
	return s
}

// APIRoutes implements api.Module. Reads mount on the ordinary doors;
// every mutation mounts through HandleSealed with its Cedar action and AAL
// floor. The door authorizes the collection; the stored target and its
// workspace enter the in-handler admission (A1), because the sealed door
// carries entity refs only for core kinds.
func (m *Module) APIRoutes(reg api.RouteRegistrar) {
	reg.Handle("GET", "/targets", permTargetRead, m.handleListTargets)
	reg.HandleEntity("GET", "/targets/{id}", permTargetRead, api.EntityRef{
		Kind: kindTarget, IDParam: "id", WorkspaceColumn: "workspace_id", ConcealDeniedAsNotFound: true,
	}, m.handleGetTarget)
	reg.Handle("GET", "/intents", permTargetRead, m.handleListIntents)
	reg.Handle("GET", "/intents/{id}", permTargetRead, m.handleGetIntent)
	reg.Handle("GET", "/intents/{id}/observations", permTargetRead, m.handleObservations)

	door, ok := reg.(sealedRegistrar)
	if !ok {
		return
	}
	door.HandleSealed("POST", "/targets", permTargetAdmin, sealed(actionTarget, auth.AAL3), m.handleCreateTarget)
	door.HandleSealed("PUT", "/targets/{id}", permTargetAdmin, sealed(actionTarget, auth.AAL3), m.handleUpdateTarget)
	door.HandleSealed("DELETE", "/targets/{id}", permTargetAdmin, sealed(actionTarget, auth.AAL3), m.handleDeleteTarget)
	door.HandleSealed("POST", "/targets/{id}/pushes", permPush, sealed(actionPush, 0), m.handlePush)
	door.HandleSealed("POST", "/targets/{id}/pull-requests", permPullRequest, sealed(actionPullRequest, 0), m.handlePullRequest)
	door.HandleSealed("POST", "/targets/{id}/merges", permMerge, sealed(actionMerge, auth.AAL3), m.handleMerge)
	door.HandleSealed("POST", "/intents/{id}/reconcile", permTargetRead, sealed(actionReconcile, 0), m.handleReconcile)
	door.HandleSealed("POST", "/intents/{id}/abandon", permTargetAdmin, sealed(actionAbandon, auth.AAL3), m.handleAbandon)
}

func callerOf(mc api.ModuleContext) Caller { return Caller{Principal: mc.Principal, Tenant: mc.Tenant} }

// decode reads a bounded body strictly. A field the contract does not
// accept (a secret reference, an endpoint, an installation id, a path) is
// field_not_accepted.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := api.DecodeRequestBody(w, r, v, api.RequestBodySpec{MaxBytes: 1 << 20}); err != nil {
		code := "invalid_request"
		if strings.Contains(err.Error(), "unknown field") {
			code = "field_not_accepted"
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": code})
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	var e *Error
	if !errors.As(err, &e) {
		e = errUnavailable
	}
	body := map[string]any{"error": e.Code}
	if e.Intent != "" {
		body["intent_id"] = e.Intent.String()
	}
	writeJSON(w, e.Status, body)
}

// Target DTO (CONTRACT-J10-S3 §3.3).
type targetDTO struct {
	ID                  string   `json:"id"`
	WorkspaceID         string   `json:"workspace_id"`
	CredentialBindingID string   `json:"credential_binding_id,omitempty"`
	RepositoryBindingID string   `json:"repository_binding_id,omitempty"`
	PushPrefix          string   `json:"push_prefix"`
	MergeBases          []string `json:"merge_bases"`
	Version             int64    `json:"version"`
}

func toTargetDTO(t Target, admin bool) targetDTO {
	d := targetDTO{ID: t.ID.String(), WorkspaceID: t.Workspace.String(), PushPrefix: t.PushPrefix, MergeBases: t.MergeBases, Version: t.Version}
	if d.MergeBases == nil {
		d.MergeBases = []string{}
	}
	if admin {
		d.CredentialBindingID, d.RepositoryBindingID = t.CredentialBinding, t.RepositoryBinding
	}
	return d
}

type requestedDTO struct {
	Ref          string `json:"ref,omitempty"`
	ExpectedOld  string `json:"expected_old,omitempty"`
	Commit       string `json:"commit,omitempty"`
	Tree         string `json:"tree,omitempty"`
	HeadRef      string `json:"head_ref,omitempty"`
	Base         string `json:"base,omitempty"`
	Number       int    `json:"number,omitempty"`
	ExpectedHead string `json:"expected_head,omitempty"`
	Method       string `json:"method,omitempty"`
	Title        string `json:"title,omitempty"`
}

type observedDTO struct {
	Present        bool   `json:"present"`
	SHA            string `json:"sha,omitempty"`
	HeadSHA        string `json:"head_sha,omitempty"`
	Number         int    `json:"number,omitempty"`
	Merged         bool   `json:"merged"`
	MergeCommitSHA string `json:"merge_commit_sha,omitempty"`
	MergeTree      string `json:"merge_tree,omitempty"`
	Source         string `json:"source,omitempty"`
	At             string `json:"at,omitempty"`
}

type acknowledgedDTO struct {
	Acknowledged bool   `json:"acknowledged"`
	Status       int    `json:"status,omitempty"`
	RequestID    string `json:"request_id,omitempty"`
	At           string `json:"at,omitempty"`
}

type intentDTO struct {
	ID               string          `json:"id"`
	TargetID         string          `json:"target_id"`
	TargetVersion    int64           `json:"target_version"`
	Effect           string          `json:"effect"`
	OperationID      string          `json:"operation_id"`
	Attempt          int64           `json:"attempt"`
	State            string          `json:"state"`
	Answer           string          `json:"answer,omitempty"`
	Reason           string          `json:"reason,omitempty"`
	Receipt          string          `json:"receipt"`
	Requested        requestedDTO    `json:"requested"`
	Observed         observedDTO     `json:"observed"`
	ContentMatch     *bool           `json:"content_match,omitempty"`
	Acknowledged     acknowledgedDTO `json:"acknowledged"`
	AuthorizedBy     string          `json:"authorized_by,omitempty"`
	DispatchDeadline string          `json:"dispatch_deadline,omitempty"`
	ReleaseFailure   string          `json:"release_failure,omitempty"`
}

func toIntentDTO(in Intent, answer string) intentDTO {
	q := in.Requested
	d := intentDTO{
		ID: in.ID.String(), TargetID: in.Target.String(), TargetVersion: in.TargetVersion, Effect: in.Effect, OperationID: in.OperationID,
		Attempt: in.Attempt, State: in.State, Reason: in.Reason, Receipt: in.Receipt,
		Requested: requestedDTO{Ref: q.Ref, ExpectedOld: q.ExpectedOld, Commit: q.Commit, Tree: q.Tree, HeadRef: q.HeadRef, Base: q.Base, Number: q.Number, ExpectedHead: q.ExpectedHead, Method: q.Method, Title: q.Title},
		Observed: observedDTO{Present: in.Observed.Present, SHA: in.Observed.SHA, HeadSHA: in.Observed.HeadSHA, Number: in.Observed.Number,
			Merged: in.Observed.Merged, MergeCommitSHA: in.Observed.MergeCommitSHA, MergeTree: in.Observed.MergeTree, Source: in.Observed.Source, At: ts(in.Observed.At)},
		Acknowledged:     acknowledgedDTO{Acknowledged: in.Acknowledged.Acknowledged, Status: in.Acknowledged.Status, RequestID: in.Acknowledged.RequestID, At: ts(in.Acknowledged.At)},
		AuthorizedBy:     in.AuthorizedBy,
		DispatchDeadline: ts(in.DispatchDeadline),
		ReleaseFailure:   in.ReleaseFailure,
	}
	if answer != in.State {
		d.Answer = answer
	}
	if in.Effect == effectPullRequest && in.State == StateApplied {
		cm := in.ContentMatch
		d.ContentMatch = &cm
	}
	return d
}

// writeReceipt maps a receipt to its HTTP answer.
func writeReceipt(w http.ResponseWriter, r Receipt, err error) {
	if err != nil {
		writeErr(w, err)
		return
	}
	status := http.StatusOK
	switch r.Answer {
	case StateUncertain, StateDispatching:
		status = http.StatusAccepted
	case StateRejected:
		status = http.StatusConflict
	}
	writeJSON(w, status, toIntentDTO(r.Intent, r.Answer))
}

func idParam(r *http.Request) model.ID { return model.ID(chi.URLParam(r, "id")) }

type targetBody struct {
	WorkspaceID         string   `json:"workspace_id"`
	CredentialBindingID string   `json:"credential_binding_id"`
	RepositoryBindingID string   `json:"repository_binding_id"`
	PushPrefix          string   `json:"push_prefix"`
	MergeBases          []string `json:"merge_bases"`
	ExpectedVersion     int64    `json:"expected_version"`
}

// handleCreateTarget records a publication target in a workspace: an approved
// credential binding, an approved repository binding, the branch prefix pushes
// must stay under and the allowed merge bases. It requires target
// administration at AAL3.
func (m *Module) handleCreateTarget(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var b targetBody
	if !decode(w, r, &b) {
		return
	}
	t, err := m.CreateTarget(r.Context(), callerOf(mc), TargetInput{Workspace: model.ID(b.WorkspaceID), CredentialBinding: b.CredentialBindingID, RepositoryBinding: b.RepositoryBindingID, PushPrefix: b.PushPrefix, MergeBases: b.MergeBases})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toTargetDTO(t, true))
}

// handleUpdateTarget replaces a publication target's bindings, push prefix and
// merge bases under optimistic concurrency, refusing a stale expected_version.
func (m *Module) handleUpdateTarget(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var b targetBody
	if !decode(w, r, &b) {
		return
	}
	if b.WorkspaceID != "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "field_not_accepted"})
		return
	}
	t, err := m.UpdateTarget(r.Context(), callerOf(mc), idParam(r), TargetUpdate{ExpectedVersion: b.ExpectedVersion, CredentialBinding: b.CredentialBindingID, RepositoryBinding: b.RepositoryBindingID, PushPrefix: b.PushPrefix, MergeBases: b.MergeBases})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTargetDTO(t, true))
}

// handleDeleteTarget deletes a publication target that no dispatching, uncertain
// or abandoned intent still holds.
func (m *Module) handleDeleteTarget(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if err := m.DeleteTarget(r.Context(), callerOf(mc), idParam(r)); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// canAdmin reports whether the caller holds target:admin on t, for DTO
// exposure only; it authorizes nothing.
func (m *Module) canAdmin(ctx context.Context, c Caller, t Target) bool {
	if m.opts.Authority == nil {
		return false
	}
	_, err := m.opts.Authority.Admit(ctx, c.Principal, c.Tenant, Question{Permission: permTargetAdmin, Target: t.ID, Workspace: t.Workspace})
	return err == nil
}

// handleListTargets lists the publication targets the caller can read, without
// their credential and repository binding ids.
func (m *Module) handleListTargets(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var out []targetDTO
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		recs, err := listAll(r.Context(), sc, kindTarget)
		for _, rec := range recs {
			out = append(out, toTargetDTO(targetFrom(rec), false))
		}
		return err
	})
	if err != nil {
		writeErr(w, storeError(err))
		return
	}
	if out == nil {
		out = []targetDTO{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// handleGetTarget returns one publication target. Its credential and repository
// binding ids are included only for a caller who holds target administration on
// it.
func (m *Module) handleGetTarget(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var t Target
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		var err error
		t, err = loadTarget(r.Context(), sc, idParam(r))
		return err
	})
	if err != nil {
		writeErr(w, storeError(err))
		return
	}
	writeJSON(w, http.StatusOK, toTargetDTO(t, m.canAdmin(r.Context(), callerOf(mc), t)))
}

type pushBody struct {
	OperationID       string `json:"operation_id"`
	Ref               string `json:"ref"`
	ExpectedOld       string `json:"expected_old"`
	Commit            string `json:"commit"`
	Tree              string `json:"tree"`
	AcknowledgeIntent string `json:"acknowledge_intent"`
}

// handlePush pushes one exact commit to a branch under the target's push
// prefix, leased on the branch's expected current value, and returns the
// publication intent with its receipt.
func (m *Module) handlePush(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var b pushBody
	if !decode(w, r, &b) {
		return
	}
	rc, err := m.Push(r.Context(), callerOf(mc), PushInput{Target: idParam(r), OperationID: b.OperationID, Ref: b.Ref, ExpectedOld: b.ExpectedOld, Commit: b.Commit, Tree: b.Tree, AcknowledgeIntent: model.ID(b.AcknowledgeIntent)})
	writeReceipt(w, rc, err)
}

type prBody struct {
	OperationID       string `json:"operation_id"`
	HeadRef           string `json:"head_ref"`
	Base              string `json:"base"`
	Commit            string `json:"commit"`
	Title             string `json:"title"`
	Body              string `json:"body"`
	Draft             bool   `json:"draft"`
	AcknowledgeIntent string `json:"acknowledge_intent"`
}

// handlePullRequest opens a pull request from a branch under the target's push
// prefix into an allowed merge base, or adopts the matching open one, and
// returns the publication intent with its receipt.
func (m *Module) handlePullRequest(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var b prBody
	if !decode(w, r, &b) {
		return
	}
	rc, err := m.OpenPullRequest(r.Context(), callerOf(mc), PullRequestInput{Target: idParam(r), OperationID: b.OperationID, HeadRef: b.HeadRef, Base: b.Base, Commit: b.Commit, Title: b.Title, Body: b.Body, Draft: b.Draft, AcknowledgeIntent: model.ID(b.AcknowledgeIntent)})
	writeReceipt(w, rc, err)
}

type mergeBody struct {
	OperationID        string `json:"operation_id"`
	Number             int    `json:"number"`
	ExpectedHead       string `json:"expected_head"`
	Method             string `json:"method"`
	ExpectedResultTree string `json:"expected_result_tree"`
	ExpectedBase       string `json:"expected_base"`
	AcknowledgeIntent  string `json:"acknowledge_intent"`
}

// handleMerge merges one pull request only while its head is still the reviewed
// expected_head, and returns the publication intent with the merge commit and
// tree it recorded.
func (m *Module) handleMerge(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var b mergeBody
	if !decode(w, r, &b) {
		return
	}
	rc, err := m.Merge(r.Context(), callerOf(mc), MergeInput{Target: idParam(r), OperationID: b.OperationID, Number: b.Number, ExpectedHead: b.ExpectedHead, Method: b.Method, ExpectedResultTree: b.ExpectedResultTree, ExpectedBase: b.ExpectedBase, AcknowledgeIntent: model.ID(b.AcknowledgeIntent)})
	writeReceipt(w, rc, err)
}

// handleReconcile reads the host again for one publication intent and records
// what it observed. It never dispatches, and only the requested effect, once
// observed, ends an uncertain intent.
func (m *Module) handleReconcile(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	rc, err := m.Reconcile(r.Context(), callerOf(mc), idParam(r))
	writeReceipt(w, rc, err)
}

// handleAbandon records that an administrator takes responsibility for an
// unresolved publication intent, with a bounded reason. The intent keeps its
// conflict scope until a later request acknowledges it.
func (m *Module) handleAbandon(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var b struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	rc, err := m.Abandon(r.Context(), callerOf(mc), idParam(r), b.Reason)
	writeReceipt(w, rc, err)
}

// handleListIntents lists the publication intents of one target, named by
// ?target_id, with what each requested, what the host was last observed to
// hold and its current state.
func (m *Module) handleListIntents(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	target := model.ID(r.URL.Query().Get("target_id"))
	if target == "" {
		writeErr(w, errInvalid)
		return
	}
	list, err := m.Intents(r.Context(), callerOf(mc), target)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]intentDTO, 0, len(list))
	for _, in := range list {
		out = append(out, toIntentDTO(in, in.State))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// handleGetIntent returns one publication intent: what it requested, what the
// host was last observed to hold, whether the host acknowledged the request,
// and its state.
func (m *Module) handleGetIntent(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	in, e := m.admitIntent(r.Context(), callerOf(mc), idParam(r), "", 0, false)
	if e != nil {
		writeErr(w, e)
		return
	}
	writeJSON(w, http.StatusOK, toIntentDTO(in, in.State))
}

// handleObservations lists every observation recorded for one publication
// intent: each host read, dispatcher outcome and refusal, with its attempt,
// result and time.
func (m *Module) handleObservations(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	list, err := m.Observations(r.Context(), callerOf(mc), idParam(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	type obsDTO struct {
		Attempt    int64  `json:"attempt"`
		Source     string `json:"source"`
		Result     string `json:"result"`
		HostObject string `json:"host_object,omitempty"`
		Status     int    `json:"status,omitempty"`
		RequestID  string `json:"request_id,omitempty"`
		At         string `json:"at"`
	}
	out := make([]obsDTO, 0, len(list))
	for _, o := range list {
		out = append(out, obsDTO{Attempt: o.Attempt, Source: o.Source, Result: o.Result, HostObject: o.HostObject, Status: o.Status, RequestID: o.RequestID, At: o.At.UTC().Format(time.RFC3339Nano)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// DeleteTarget removes a target (target:admin, AAL3) that no unresolved
// intent still holds.
func (m *Module) DeleteTarget(ctx context.Context, c Caller, id model.ID) error {
	if !m.wired() {
		return errUnavailable
	}
	actx, cancel := m.admissionContext(ctx)
	defer cancel()
	var cur Target
	if err := m.data.View(actx, c.Tenant, func(sc store.Scope) error {
		var err error
		cur, err = loadTarget(actx, sc, id)
		return err
	}); err != nil {
		return storeError(err)
	}
	adm, e := m.admitTarget(actx, c, cur.ID, cur.Workspace)
	if e != nil {
		return e
	}
	err := m.data.Mutate(actx, c.Tenant, func(sc store.Scope) error {
		if err := adm.Lock(actx, sc, txNow(actx, sc, m.now())); err != nil {
			return err
		}
		recs, err := listAll(actx, sc, kindIntent, eq("target_id", id.String()))
		if err != nil {
			return err
		}
		for _, rec := range recs {
			switch rec.String("state") {
			case StateDispatching, StateUncertain, StateAbandoned:
				return refuse("target_in_use", http.StatusConflict)
			}
		}
		repo, err := sc.Ext(kindTarget)
		if err != nil {
			return err
		}
		if err := repo.Delete(actx, id); err != nil {
			return err
		}
		return appendAudit(actx, sc, adm.Subject().Actor, adm.Subject().ActorKind, "gitpublish.target.deleted", kindTarget, id, targetMeta(cur))
	})
	return storeError(err)
}
