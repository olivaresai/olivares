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
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
)

const protocolBindingModeTest ExecutionMode = "test"

var (
	protocolBindingEntity = api.EntityRef{
		Kind: protocolBindingKind, IDParam: "id", WorkspaceColumn: colWorkWorkspaceID,
	}
	// ErrProtocolBindingRemoteReconcilerUnwired is returned only by the public
	// HTTP boundary. Persistence and in-process adapters remain independently
	// usable while the peer reader is OFF.
	ErrProtocolBindingRemoteReconcilerUnwired = errors.New(
		"sessions: protocol binding remote reconciler is not wired",
	)
)

// ProtocolBindingRemoteCheck is a bounded, payload-free witness returned by a
// composed peer adapter. EvidenceRef is an opaque digest or durable reference,
// never a credential or a remote response body.
type ProtocolBindingRemoteCheck struct {
	Name        string                     `json:"name"`
	Verdict     ProtocolObservationVerdict `json:"verdict"`
	EvidenceRef string                     `json:"evidence_ref,omitempty"`
}

// ProtocolBindingReconcileRequest binds a peer read to the exact durable
// generation and version authorized by the REST request. Binding is a trusted
// local snapshot; no corresponding fields are decoded from the HTTP body.
type ProtocolBindingReconcileRequest struct {
	Binding          ProtocolBinding `json:"-"`
	ExpectedVersion  int64           `json:"-"`
	SemanticKey      string          `json:"-"`
	ExpectedPlanHash string          `json:"-"`
}

// ProtocolBindingReconcileResult is the connector-neutral peer observation.
// TestProtocolBinding must return the unchanged durable Binding snapshot;
// ReconcileProtocolBinding returns the binding after its observation commit.
type ProtocolBindingReconcileResult struct {
	Verdict    ProtocolObservationVerdict   `json:"verdict"`
	Code       string                       `json:"code"`
	ObservedAt time.Time                    `json:"observed_at"`
	Checks     []ProtocolBindingRemoteCheck `json:"checks"`
	Binding    ProtocolBinding              `json:"resource"`
	Replayed   bool                         `json:"-"`
}

// ProtocolBindingRemoteReconciler is implemented at the composition root.
// TestProtocolBinding may perform an authenticated remote read but MUST NOT
// write local state. ReconcileProtocolBinding is the sole REST apply path and
// must enforce ExpectedVersion before committing the exact-generation result.
type ProtocolBindingRemoteReconciler interface {
	TestProtocolBinding(
		context.Context,
		model.TenantID,
		ProtocolBindingReconcileRequest,
	) (ProtocolBindingReconcileResult, error)
	ReconcileProtocolBinding(
		context.Context,
		model.TenantID,
		ProtocolBindingReconcileRequest,
	) (ProtocolBindingReconcileResult, error)
}

// WithProtocolBindingRemoteReconciler wires authenticated protocol reads for
// the K5 REST surface. A nil implementation preserves the deny-closed default.
func WithProtocolBindingRemoteReconciler(reconciler ProtocolBindingRemoteReconciler) Option {
	return func(m *Module) { m.UseProtocolBindingRemoteReconciler(reconciler) }
}

// UseProtocolBindingRemoteReconciler late-binds the composition adapter after
// the sessions module and the remote executor have both been constructed.
// Passing nil explicitly returns the HTTP reconciliation surface to OFF.
func (m *Module) UseProtocolBindingRemoteReconciler(reconciler ProtocolBindingRemoteReconciler) {
	m.protocolBindingReconciler = reconciler
}

func (m *Module) protocolBindingRoutes(reg api.RouteRegistrar) {
	m.protocolBindingSpecRoutes(reg)
	reg.Handle("GET", "/protocol-bindings", permProtocolBindingRead, m.handleProtocolBindingList)
	reg.HandleEntity(
		"GET", "/protocol-bindings/{id}", permProtocolBindingRead,
		protocolBindingEntity, m.handleProtocolBindingGet,
	)
	reg.HandleEntity(
		"POST", "/protocol-bindings/{id}/reconcile", permProtocolBindingWrite,
		protocolBindingEntity, m.handleProtocolBindingReconcile,
	)
}

// handleProtocolBindingGet returns one protocol binding in the caller's tenant.
// The response carries an ETag derived from the binding's version, so a later
// conditional write can be rejected instead of silently overwriting a row that
// moved underneath the reader.
func (m *Module) handleProtocolBindingGet(
	w http.ResponseWriter,
	r *http.Request,
	mc api.ModuleContext,
) {
	id, err := model.ParseID(chi.URLParam(r, "id"))
	if err != nil || id.IsZero() {
		writeProtocolBindingAPIError(w, protocolBindingNotFound("binding_not_found"))
		return
	}
	binding, err := m.GetProtocolBinding(r.Context(), mc.Tenant, ProtocolBindingRef{ID: id})
	if err != nil {
		writeProtocolBindingAPIError(w, err)
		return
	}
	w.Header().Set("ETag", protocolBindingETag(binding.Version))
	writeJSON(w, http.StatusOK, binding)
}

// handleProtocolBindingList lists the protocol bindings visible to the caller,
// filtered and paginated by the query. An empty page is returned as an empty
// array rather than null, so a client cannot confuse "none" with "absent".
func (m *Module) handleProtocolBindingList(
	w http.ResponseWriter,
	r *http.Request,
	mc api.ModuleContext,
) {
	query, err := protocolBindingQueryFromRequest(r, mc)
	if err != nil {
		writeProtocolBindingAPIError(w, err)
		return
	}
	page, err := m.ListProtocolBindings(r.Context(), mc.Tenant, query)
	if err != nil {
		writeProtocolBindingAPIError(w, err)
		return
	}
	if page.Items == nil {
		page.Items = []ProtocolBinding{}
	}
	writeJSON(w, http.StatusOK, page)
}

type protocolBindingReconcileBody struct {
	PlanHash string `json:"plan_hash,omitempty"`
}

// handleProtocolBindingReconcile drives one binding towards its desired state.
// The execution mode is REQUIRED and explicit — validate, plan, test or apply —
// and an unrecognized one is refused rather than defaulted, so a caller can never
// reach the applying path by omitting a parameter. An If-Plan-Hash precondition
// ties an apply to the plan the caller actually saw.
func (m *Module) handleProtocolBindingReconcile(
	w http.ResponseWriter,
	r *http.Request,
	mc api.ModuleContext,
) {
	mode := ExecutionMode(r.URL.Query().Get("mode"))
	if mode != ModeValidate && mode != ModePlan && mode != protocolBindingModeTest && mode != ModeApply {
		writeWorkError(w, broken(http.StatusBadRequest, "mode_required"))
		return
	}
	id, err := model.ParseID(chi.URLParam(r, "id"))
	if err != nil || id.IsZero() {
		writeProtocolBindingAPIError(w, protocolBindingNotFound("binding_not_found"))
		return
	}
	body, err := decodeProtocolBindingReconcileBody(r)
	if err != nil {
		writeWorkError(w, broken(http.StatusBadRequest, "invalid_command"))
		return
	}
	expectedPlanHash, err := reconcilePlanPrecondition(r.Header.Get("If-Plan-Hash"), body.PlanHash)
	if err != nil {
		writeWorkError(w, err)
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

	binding, err := m.GetProtocolBinding(r.Context(), mc.Tenant, ProtocolBindingRef{ID: id})
	if err != nil {
		writeProtocolBindingAPIError(w, err)
		return
	}
	if hasVersion && binding.Version != expectedVersion {
		writeWorkError(w, broken(http.StatusPreconditionFailed, "version_mismatch"))
		return
	}
	plan, err := m.protocolBindingReconcilePlan(mc.Tenant, binding)
	if err != nil {
		writeWorkError(w, unknown("evidence_unavailable", err))
		return
	}
	if expectedPlanHash != "" && !strings.EqualFold(expectedPlanHash, plan.PlanHash) {
		writeWorkError(w, broken(http.StatusPreconditionFailed, "plan_changed"))
		return
	}
	w.Header().Set("ETag", plan.ExpectedETag)

	switch mode {
	case ModeValidate:
		assessment := plan.Assessment
		assessment.Code, assessment.PlanHash = "binding_reconcile_valid", ""
		writeJSON(w, http.StatusOK, assessment)
	case ModePlan:
		writeJSON(w, http.StatusOK, plan)
	case protocolBindingModeTest, ModeApply:
		if mode == ModeApply {
			key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
			if _, parseErr := model.ParseID(key); parseErr != nil || key == "" {
				writeWorkError(w, broken(http.StatusBadRequest, "idempotency_key_required"))
				return
			}
		}
		if m.protocolBindingReconciler == nil {
			writeWorkError(w, unknown(
				"observation_unavailable", ErrProtocolBindingRemoteReconcilerUnwired,
			))
			return
		}
		semanticKey, err := protocolBindingReconcileSemanticKey(mc, r, binding, mode)
		if err != nil {
			writeWorkError(w, unknown("evidence_unavailable", err))
			return
		}
		request := ProtocolBindingReconcileRequest{
			Binding: binding, ExpectedVersion: binding.Version,
			SemanticKey: semanticKey, ExpectedPlanHash: plan.PlanHash,
		}
		var result ProtocolBindingReconcileResult
		if mode == protocolBindingModeTest {
			result, err = m.protocolBindingReconciler.TestProtocolBinding(
				r.Context(), mc.Tenant, request,
			)
		} else {
			key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
			request.SemanticKey, err = protocolBindingReconcileApplyKey(mc, r, binding, key)
			if err == nil {
				result, err = m.protocolBindingReconciler.ReconcileProtocolBinding(
					r.Context(), mc.Tenant, request,
				)
			}
		}
		if err != nil {
			writeWorkError(w, unknown("observation_unavailable", err))
			return
		}
		if err := validateProtocolBindingReconcileResult(binding, result, mode == ModeApply); err != nil {
			writeWorkError(w, unknown("observation_unavailable", err))
			return
		}
		resource := result.Binding
		if mode == protocolBindingModeTest {
			unchanged, readErr := m.GetProtocolBinding(
				r.Context(), mc.Tenant, ProtocolBindingRef{ID: binding.ID},
			)
			if readErr != nil || unchanged.Version != binding.Version ||
				unchanged.LastCommandID != binding.LastCommandID ||
				unchanged.LastEventID != binding.LastEventID ||
				unchanged.LastEventSeq != binding.LastEventSeq {
				writeWorkError(w, unknown("observation_unavailable", readErr))
				return
			}
			// The test projection is deliberately the durable pre-read snapshot;
			// remote state is evidence in verdict/checks until apply commits it.
			resource = unchanged
		} else {
			committed, readErr := m.GetProtocolBinding(
				r.Context(), mc.Tenant, ProtocolBindingRef{ID: binding.ID},
			)
			if readErr != nil || !protocolBindingResultMatchesCommit(result, committed) {
				writeWorkError(w, unknown("observation_unavailable", readErr))
				return
			}
			resource = committed
		}
		assessment := protocolBindingRemoteAssessment(result, plan.PlanHash, resource)
		w.Header().Set("ETag", protocolBindingETag(resource.Version))
		if result.Replayed && mode == ModeApply {
			w.Header().Set("Idempotency-Replayed", "true")
		}
		writeJSON(w, http.StatusOK, assessment)
	}
}

func protocolBindingQueryFromRequest(
	r *http.Request,
	mc api.ModuleContext,
) (ProtocolBindingQuery, error) {
	allowed := map[string]bool{
		"workspace_id": true, "binding_spec_id": true, "work_item_id": true,
		"protocol": true, "peer_authority": true, "owner_kind": true,
		"owner_ref": true, "external_kind": true, "external_id": true,
		"verdict": true, "terminal": true, "limit": true, "cursor": true,
	}
	for key, values := range r.URL.Query() {
		if !allowed[key] || len(values) != 1 {
			return ProtocolBindingQuery{}, protocolBindingInvalid("invalid_binding_query")
		}
	}
	values := r.URL.Query()
	workspaceText := values.Get("workspace_id")
	if workspaceText == "" {
		if confined, ok := mc.Principal.ConfinedWorkspaceIn(mc.Tenant); ok {
			workspaceText = confined.String()
		}
	}
	workspace, err := model.ParseID(workspaceText)
	if err != nil || workspace.IsZero() {
		return ProtocolBindingQuery{}, protocolBindingInvalid("invalid_binding_query")
	}
	limit, err := queryLimit(r)
	if err != nil || !validWorkCursor(values.Get("cursor")) {
		return ProtocolBindingQuery{}, protocolBindingInvalid("invalid_binding_query")
	}
	query := ProtocolBindingQuery{
		WorkspaceID: workspace, Protocol: BindingProtocol(values.Get("protocol")),
		PeerAuthority: values.Get("peer_authority"), OwnerKind: values.Get("owner_kind"),
		OwnerRef: values.Get("owner_ref"), ExternalKind: values.Get("external_kind"),
		ExternalID: values.Get("external_id"), Verdict: ProtocolObservationVerdict(values.Get("verdict")),
		Limit: limit, Cursor: values.Get("cursor"),
	}
	for _, field := range []struct {
		raw    string
		target *model.ID
	}{
		{raw: values.Get("binding_spec_id"), target: &query.BindingSpecID},
		{raw: values.Get("work_item_id"), target: &query.WorkItemID},
	} {
		raw, target := field.raw, field.target
		if raw == "" {
			continue
		}
		parsed, parseErr := model.ParseID(raw)
		if parseErr != nil || parsed.IsZero() {
			return ProtocolBindingQuery{}, protocolBindingInvalid("invalid_binding_query")
		}
		*target = parsed
	}
	if raw := values.Get("terminal"); raw != "" {
		terminal, parseErr := strconv.ParseBool(raw)
		if parseErr != nil {
			return ProtocolBindingQuery{}, protocolBindingInvalid("invalid_binding_query")
		}
		query.Terminal = &terminal
	}
	if _, err := normalizeProtocolBindingQuery(query); err != nil {
		return ProtocolBindingQuery{}, err
	}
	return query, nil
}

func decodeProtocolBindingReconcileBody(r *http.Request) (protocolBindingReconcileBody, error) {
	var body protocolBindingReconcileBody
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return body, err
	}
	if strings.TrimSpace(string(raw)) == "" {
		return body, nil
	}
	decoder := jsonDecoder(strings.NewReader(string(raw)))
	if err := decoder.Decode(&body); err != nil {
		return body, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return body, errors.New("sessions: trailing protocol binding reconcile body")
	}
	return body, nil
}

func jsonDecoder(reader io.Reader) *json.Decoder {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	return decoder
}

func reconcilePlanPrecondition(header, body string) (string, error) {
	header, body = strings.TrimSpace(header), strings.TrimSpace(body)
	if header != "" && body != "" && !strings.EqualFold(header, body) {
		return "", broken(http.StatusBadRequest, "invalid_command")
	}
	value := header
	if value == "" {
		value = body
	}
	if value != "" {
		if _, err := decodeHash(value, true); err != nil {
			return "", broken(http.StatusBadRequest, "invalid_command")
		}
		value = strings.TrimPrefix(value, "sha256:")
	}
	return value, nil
}

func (m *Module) protocolBindingReconcilePlan(
	tenant model.TenantID,
	binding ProtocolBinding,
) (Plan, error) {
	anchor := struct {
		Command               string          `json:"command"`
		TenantID              model.TenantID  `json:"tenant_id"`
		BindingID             model.ID        `json:"binding_id"`
		WorkspaceID           model.ID        `json:"workspace_id"`
		Version               int64           `json:"version"`
		Generation            int64           `json:"generation"`
		BindingSpecID         model.ID        `json:"binding_spec_id"`
		BindingSpecGeneration int64           `json:"binding_spec_generation"`
		Protocol              BindingProtocol `json:"protocol"`
		ProtocolVersion       string          `json:"protocol_version"`
		PeerAuthority         string          `json:"peer_authority"`
		RemoteResourceRef     string          `json:"remote_resource_ref"`
		ExternalKind          string          `json:"external_kind"`
		ExternalID            string          `json:"external_id"`
		ContextID             string          `json:"context_id"`
		ExternalMessageID     string          `json:"external_message_id"`
		LocalState            string          `json:"local_state"`
		RemoteState           string          `json:"remote_state"`
		RemoteRevision        string          `json:"remote_revision"`
		Terminal              bool            `json:"terminal"`
		CancelRequested       bool            `json:"cancel_requested"`
		PinnedSpecHash        string          `json:"pinned_spec_hash"`
		PinnedMappingHash     string          `json:"pinned_mapping_hash"`
		PinnedLossesHash      string          `json:"pinned_losses_hash"`
	}{
		Command: "binding.reconcile", TenantID: tenant,
		BindingID: binding.ID, WorkspaceID: binding.WorkspaceID, Version: binding.Version,
		Generation: binding.Generation, BindingSpecID: binding.BindingSpecID,
		BindingSpecGeneration: binding.BindingSpecGeneration, Protocol: binding.Protocol,
		ProtocolVersion: binding.ProtocolVersion, PeerAuthority: binding.PeerAuthority,
		RemoteResourceRef: binding.RemoteResourceRef, ExternalKind: binding.ExternalKind,
		ExternalID: binding.ExternalID, ContextID: binding.ContextID,
		ExternalMessageID: binding.ExternalMessageID, LocalState: binding.LocalState,
		RemoteState: binding.RemoteState, RemoteRevision: binding.RemoteRevision,
		Terminal: binding.Terminal, CancelRequested: binding.CancelRequested,
		PinnedSpecHash:    protocolBindingHex(binding.PinnedSpecHash),
		PinnedMappingHash: protocolBindingHex(binding.PinnedMappingHash),
		PinnedLossesHash:  protocolBindingHex(binding.PinnedLossesHash),
	}
	digest, err := protocolBindingHash(anchor)
	if err != nil {
		return Plan{}, err
	}
	externalCalls := []string{"protocol_binding.get"}
	if binding.Terminal || binding.ExternalID == "" {
		externalCalls = []string{}
	}
	planHash := protocolBindingHex(digest)
	return Plan{
		Assessment: Assessment{
			Verdict: VerdictClean, Code: "binding_reconcile_planned",
			ObservedAt: m.clock.Now().String(),
			Checks: []WorkCheck{
				{Name: "binding_generation", Verdict: VerdictClean, EvidenceRef: binding.ID.String()},
				{Name: "protocol_currency", Verdict: VerdictClean, EvidenceRef: binding.ProtocolVersion},
			},
			PlanHash: planHash, Resource: binding,
		},
		Command: "binding.reconcile", ExpectedETag: protocolBindingETag(binding.Version),
		RowEffects: []string{"sessions.protocol_binding:update", "sessions.work_item:transition_if_legal"},
		EventType:  "work.binding.observed", AuditAction: "sessions.work.binding.reconcile",
		Permission: string(permProtocolBindingWrite), ExternalCalls: externalCalls,
	}, nil
}

func protocolBindingReconcileSemanticKey(
	mc api.ModuleContext,
	r *http.Request,
	binding ProtocolBinding,
	mode ExecutionMode,
) (string, error) {
	actor, err := mc.Principal.AttributableActor()
	if err != nil {
		return "", err
	}
	digest, err := protocolBindingHash(struct {
		TenantID  model.TenantID `json:"tenant_id"`
		Actor     string         `json:"actor"`
		Method    string         `json:"method"`
		Path      string         `json:"path"`
		BindingID model.ID       `json:"binding_id"`
		Mode      ExecutionMode  `json:"mode"`
	}{mc.Tenant, actor, r.Method, canonicalWorkPath(r.URL.Path), binding.ID, mode})
	if err != nil {
		return "", err
	}
	return "http_binding_" + protocolBindingHex(digest), nil
}

func protocolBindingReconcileApplyKey(
	mc api.ModuleContext,
	r *http.Request,
	binding ProtocolBinding,
	idempotencyKey string,
) (string, error) {
	base, err := protocolBindingReconcileSemanticKey(mc, r, binding, ModeApply)
	if err != nil {
		return "", err
	}
	digest, err := protocolBindingHash(struct {
		Scope string `json:"scope"`
		Key   string `json:"key"`
	}{base, idempotencyKey})
	if err != nil {
		return "", err
	}
	return "http_binding_apply_" + protocolBindingHex(digest), nil
}

func validateProtocolBindingReconcileResult(
	before ProtocolBinding,
	result ProtocolBindingReconcileResult,
	apply bool,
) error {
	if !result.Verdict.valid() || !boundedToken(strings.TrimSpace(result.Code), 128) ||
		result.ObservedAt.IsZero() || result.Binding.ID != before.ID ||
		result.Binding.WorkspaceID != before.WorkspaceID || result.Binding.Generation != before.Generation {
		return errors.New("sessions: invalid protocol binding reconcile result")
	}
	if apply {
		if result.Binding.Version < before.Version {
			return errors.New("sessions: protocol binding reconcile result regressed version")
		}
	} else if result.Binding.Version != before.Version {
		return errors.New("sessions: protocol binding test changed durable version")
	}
	for _, check := range result.Checks {
		if !boundedToken(strings.TrimSpace(check.Name), 128) || !check.Verdict.valid() ||
			(check.EvidenceRef != "" && !validateOpaqueRef(check.EvidenceRef)) {
			return errors.New("sessions: invalid protocol binding remote check")
		}
	}
	return nil
}

func protocolBindingResultMatchesCommit(
	result ProtocolBindingReconcileResult,
	committed ProtocolBinding,
) bool {
	if committed.ID != result.Binding.ID || committed.WorkspaceID != result.Binding.WorkspaceID ||
		committed.Generation != result.Binding.Generation || committed.Version != result.Binding.Version ||
		committed.ObservationVerdict != result.Verdict || committed.ObservationCode != result.Code {
		return false
	}
	if committed.LastObservedAt == nil {
		return false
	}
	return committed.LastObservedAt.UTC().Equal(result.ObservedAt.UTC())
}

func protocolBindingRemoteAssessment(
	result ProtocolBindingReconcileResult,
	planHash string,
	resource ProtocolBinding,
) Assessment {
	checks := make([]WorkCheck, 0, len(result.Checks))
	for _, check := range result.Checks {
		checks = append(checks, WorkCheck{
			Name: check.Name, Verdict: protocolBindingAssessmentVerdict(check.Verdict),
			EvidenceRef: check.EvidenceRef,
		})
	}
	if checks == nil {
		checks = []WorkCheck{}
	}
	return Assessment{
		Verdict:    protocolBindingAssessmentVerdict(result.Verdict),
		Code:       strings.TrimSpace(result.Code),
		ObservedAt: model.NewTimestamp(result.ObservedAt.UTC()).String(),
		Checks:     checks, PlanHash: planHash, Resource: resource,
	}
}

func protocolBindingAssessmentVerdict(verdict ProtocolObservationVerdict) AssessmentVerdict {
	switch verdict {
	case ProtocolObservationClean:
		return VerdictClean
	case ProtocolObservationBroken:
		return VerdictBroken
	default:
		return VerdictUnknown
	}
}

func protocolBindingETag(version int64) string { return fmt.Sprintf("\"v%d\"", version) }

func writeProtocolBindingAPIError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidProtocolBinding):
		writeWorkError(w, broken(http.StatusBadRequest, "invalid_command"))
	case errors.Is(err, ErrProtocolBindingConflict):
		writeWorkError(w, broken(http.StatusConflict, "state_conflict"))
	case errors.Is(err, ErrProtocolBindingNotFound):
		writeWorkError(w, broken(http.StatusNotFound, "not_found"))
	case errors.Is(err, ErrProtocolBindingUnknown):
		writeWorkError(w, unknown("observation_unavailable", err))
	default:
		writeWorkError(w, err)
	}
}
