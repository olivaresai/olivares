// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

var protocolBindingSpecEntity = api.EntityRef{
	Kind: protocolBindingSpecKind, IDParam: "id", WorkspaceColumn: colWorkWorkspaceID,
}

// ProtocolBindingSpecQuery is the bounded read projection used by the K5
// protocol composer. A workspace is always explicit or derived from the
// authenticated principal's confinement.
type ProtocolBindingSpecQuery struct {
	WorkspaceID   model.ID                 `json:"workspace_id"`
	BindingKey    string                   `json:"binding_key,omitempty"`
	Generation    int64                    `json:"generation,omitempty"`
	Protocol      BindingProtocol          `json:"protocol,omitempty"`
	Direction     BindingDirection         `json:"direction,omitempty"`
	LocalKind     BindingLocalKind         `json:"local_kind,omitempty"`
	PeerAuthority string                   `json:"peer_authority,omitempty"`
	State         ProtocolBindingSpecState `json:"state,omitempty"`
	Limit         int                      `json:"limit,omitempty"`
	Cursor        string                   `json:"cursor,omitempty"`
}

type ProtocolBindingSpecPage struct {
	Items      api.JSONArray[ProtocolBindingSpec] `json:"items"`
	NextCursor string                             `json:"next_cursor,omitempty"`
	HasMore    bool                               `json:"has_more"`
}

func (m *Module) GetProtocolBindingSpec(
	ctx context.Context,
	tenant model.TenantID,
	id model.ID,
) (ProtocolBindingSpec, error) {
	if tenant.IsZero() || tenant.IsSystem() || !validCanonicalCommunicationID(id) {
		return ProtocolBindingSpec{}, protocolBindingInvalid("invalid_spec_ref")
	}
	var result ProtocolBindingSpec
	err := m.workData(tenant).View(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(protocolBindingSpecKind)
		if err != nil {
			return err
		}
		record, err := repo.Get(ctx, id)
		if err != nil {
			return err
		}
		stored, err := decodeProtocolBindingSpec(record)
		if err == nil {
			result = stored.ProtocolBindingSpec
		}
		return err
	})
	if err != nil {
		return ProtocolBindingSpec{}, classifyProtocolBindingStoreError(err)
	}
	return result, nil
}

func (m *Module) ListProtocolBindingSpecs(
	ctx context.Context,
	tenant model.TenantID,
	query ProtocolBindingSpecQuery,
) (ProtocolBindingSpecPage, error) {
	normalized, err := normalizeProtocolBindingSpecQuery(query)
	if err != nil {
		return ProtocolBindingSpecPage{}, err
	}
	var result ProtocolBindingSpecPage
	err = m.workData(tenant).View(ctx, func(sc store.Scope) error {
		if _, err := sc.Workspaces().Get(ctx, normalized.WorkspaceID); err != nil {
			return err
		}
		repo, err := sc.Ext(protocolBindingSpecKind)
		if err != nil {
			return err
		}
		filters := []model.Filter{{
			Column: colWorkWorkspaceID, Op: model.OpEq, Value: normalized.WorkspaceID.String(),
		}}
		for _, filter := range []struct {
			column string
			value  any
			set    bool
		}{
			{colBindingKey, normalized.BindingKey, normalized.BindingKey != ""},
			{colCommGeneration, normalized.Generation, normalized.Generation != 0},
			{colBindingProtocol, string(normalized.Protocol), normalized.Protocol != ""},
			{colBindingDirection, string(normalized.Direction), normalized.Direction != ""},
			{colBindingLocalKind, string(normalized.LocalKind), normalized.LocalKind != ""},
			{colBindingPeerAuthority, normalized.PeerAuthority, normalized.PeerAuthority != ""},
			{colBindingState, string(normalized.State), normalized.State != ""},
		} {
			if filter.set {
				filters = append(filters, model.Filter{Column: filter.column, Op: model.OpEq, Value: filter.value})
			}
		}
		rows, page, err := repo.List(ctx, model.Query{
			Filters: filters, Limit: normalized.Limit, Cursor: normalized.Cursor,
		})
		if err != nil {
			return err
		}
		result.Items = make([]ProtocolBindingSpec, 0, len(rows))
		for _, row := range rows {
			stored, err := decodeProtocolBindingSpec(row)
			if err != nil {
				return err
			}
			result.Items = append(result.Items, stored.ProtocolBindingSpec)
		}
		result.NextCursor, result.HasMore = page.Cursor, page.HasMore
		return nil
	})
	if err != nil {
		return ProtocolBindingSpecPage{}, classifyProtocolBindingStoreError(err)
	}
	return result, nil
}

func normalizeProtocolBindingSpecQuery(value ProtocolBindingSpecQuery) (ProtocolBindingSpecQuery, error) {
	if !validCanonicalCommunicationID(value.WorkspaceID) || value.Generation < 0 ||
		(value.Protocol != "" && !value.Protocol.valid()) ||
		(value.Direction != "" && !value.Direction.valid()) ||
		(value.LocalKind != "" && !value.LocalKind.valid()) ||
		(value.State != "" && !value.State.valid()) || value.Limit < 0 ||
		value.Limit > maxProtocolBindingPage || !validWorkCursor(value.Cursor) {
		return value, protocolBindingInvalid("invalid_spec_query")
	}
	value.BindingKey = strings.ToLower(strings.TrimSpace(value.BindingKey))
	if value.BindingKey != "" && !boundedToken(value.BindingKey, 128) {
		return value, protocolBindingInvalid("invalid_spec_query")
	}
	value.PeerAuthority = strings.TrimSpace(value.PeerAuthority)
	if value.PeerAuthority != "" {
		var err error
		value.PeerAuthority, err = normalizeProtocolAuthority(value.PeerAuthority)
		if err != nil {
			return value, err
		}
	}
	if value.Limit == 0 {
		value.Limit = 100
	}
	return value, nil
}

func (m *Module) protocolBindingSpecRoutes(reg api.RouteRegistrar) {
	reg.Handle("GET", "/protocol-binding-specs", permProtocolBindingRead, m.handleProtocolBindingSpecList)
	reg.Handle("POST", "/protocol-binding-specs", permProtocolBindingWrite, m.handleProtocolBindingSpecCreate)
	reg.HandleEntity(
		"GET", "/protocol-binding-specs/{id}", permProtocolBindingRead,
		protocolBindingSpecEntity, m.handleProtocolBindingSpecGet,
	)
	reg.HandleEntity(
		"POST", "/protocol-binding-specs/{id}/activate", permProtocolBindingAdmin,
		protocolBindingSpecEntity, m.handleProtocolBindingSpecActivate,
	)
	reg.HandleEntity(
		"POST", "/protocol-binding-specs/{id}/disable", permProtocolBindingAdmin,
		protocolBindingSpecEntity, m.handleProtocolBindingSpecDisable,
	)
}

// handleProtocolBindingSpecGet returns one immutable protocol binding spec
// generation. Tenancy is already bound by the module middleware and the row's
// workspace by the entity middleware, so this handler resolves the id and no
// more. The ETag carries the generation's version.
func (m *Module) handleProtocolBindingSpecGet(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, err := model.ParseID(chi.URLParam(r, "id"))
	if err != nil || id.IsZero() {
		writeProtocolBindingAPIError(w, protocolBindingNotFound("spec_not_found"))
		return
	}
	// The tenant was already bound by the module middleware and the entity
	// middleware already confined the row's workspace.
	spec, err := m.GetProtocolBindingSpec(r.Context(), mc.Tenant, id)
	if err != nil {
		writeProtocolBindingAPIError(w, err)
		return
	}
	w.Header().Set("ETag", protocolBindingETag(spec.Version))
	writeJSON(w, http.StatusOK, protocolBindingSpecAPI(spec))
}

// handleProtocolBindingSpecList lists protocol binding spec generations in one
// workspace. An empty page is returned as an empty array rather than null.
func (m *Module) handleProtocolBindingSpecList(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	query, err := protocolBindingSpecQueryFromRequest(r, mc)
	if err != nil {
		writeProtocolBindingAPIError(w, err)
		return
	}
	page, err := m.ListProtocolBindingSpecs(r.Context(), mc.Tenant, query)
	if err != nil {
		writeProtocolBindingAPIError(w, err)
		return
	}
	if page.Items == nil {
		page.Items = []ProtocolBindingSpec{}
	}
	for index := range page.Items {
		page.Items[index] = protocolBindingSpecAPI(page.Items[index])
	}
	writeJSON(w, http.StatusOK, page)
}

// protocolBindingSpecAPI keeps the public JSON collection contract without
// changing the durable canonical form used for hashes and plan preconditions.
func protocolBindingSpecAPI(spec ProtocolBindingSpec) ProtocolBindingSpec {
	if spec.KnownLosses == nil {
		spec.KnownLosses = []ProtocolBindingLoss{}
	}
	return spec
}

// handleProtocolBindingSpecCreate records a new draft generation of a protocol
// binding spec. Two properties are load-bearing and deliberate. The capability
// verdict is ALWAYS server-derived: a browser or CLI may carry the validation
// field for schema compatibility, but it cannot assert a clean capability
// witness, so the submitted value is overwritten before anything is persisted.
// And an apply REQUIRES an Idempotency-Key that is a well-formed id, so a retried
// create settles on the first generation instead of minting a second.
func (m *Module) handleProtocolBindingSpecCreate(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	mode, ok := protocolBindingSpecMode(r)
	if !ok {
		writeWorkError(w, broken(http.StatusBadRequest, "mode_required"))
		return
	}
	var input ProtocolBindingSpecInput
	if err := decodeProtocolBindingSpecJSON(r, &input); err != nil {
		writeWorkError(w, broken(http.StatusBadRequest, "invalid_command"))
		return
	}
	if confined, confinedOK := mc.Principal.ConfinedWorkspaceIn(mc.Tenant); confinedOK && input.WorkspaceID != confined {
		writeProtocolBindingAPIError(w, protocolBindingNotFound("workspace_not_found"))
		return
	}
	// Normalize and validate the complete local shape before asking a protocol
	// peer for a capability observation. The placeholder is never persisted;
	// it only lets the shared desired-state validator run while keeping the
	// browser-supplied validation field non-authoritative.
	input.Validation = ProtocolBindingValidation{
		Verdict: ProtocolObservationUnknown, Code: "capability_pending",
	}
	normalizedInput, err := normalizeProtocolSpecInput(input)
	if err != nil {
		writeProtocolBindingAPIError(w, err)
		return
	}
	input = normalizedInput
	// Validation is always server-derived. A browser or CLI may carry the field
	// for schema compatibility, but it cannot assert a CLEAN capability witness.
	input.Validation = m.validateProtocolBindingSpecCapability(r.Context(), mc.Tenant, input)
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if mode == ModeApply {
		if id, err := model.ParseID(key); err != nil || id.IsZero() || id.String() != key {
			writeWorkError(w, broken(http.StatusBadRequest, "idempotency_key_required"))
			return
		}
	} else if key == "" {
		digest, err := protocolBindingHash(input)
		if err != nil {
			writeWorkError(w, broken(http.StatusBadRequest, "invalid_command"))
			return
		}
		key = "protocol-spec-read-" + protocolBindingHex(digest)
	}
	planHash, err := reconcilePlanPrecondition(r.Header.Get("If-Plan-Hash"), "")
	if err != nil {
		writeWorkError(w, err)
		return
	}
	command := ProtocolBindingSpecCommand{
		Operation: ProtocolBindingSpecCreateDraft, WorkspaceID: input.WorkspaceID,
		Input: &input, IdempotencyKey: key, ExpectedPlanHash: planHash,
	}
	m.handleProtocolBindingSpecCommand(w, r, mc.Tenant, mode, command, http.StatusCreated)
}

// handleProtocolBindingSpecActivate makes one spec generation the active desired
// state for its binding. Generations are immutable: activating never edits the
// row, it moves which generation is in force.
func (m *Module) handleProtocolBindingSpecActivate(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.handleProtocolBindingSpecState(w, r, mc, ProtocolBindingSpecActivate)
}

// handleProtocolBindingSpecDisable stops one spec generation from being the
// active desired state. Like activation it is a state move, not an edit, and the
// generation stays readable afterwards as evidence of what was once in force.
func (m *Module) handleProtocolBindingSpecDisable(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.handleProtocolBindingSpecState(w, r, mc, ProtocolBindingSpecDisable)
}

func (m *Module) handleProtocolBindingSpecState(
	w http.ResponseWriter,
	r *http.Request,
	mc api.ModuleContext,
	operation ProtocolBindingSpecOperation,
) {
	mode, ok := protocolBindingSpecMode(r)
	if !ok {
		writeWorkError(w, broken(http.StatusBadRequest, "mode_required"))
		return
	}
	id, err := model.ParseID(chi.URLParam(r, "id"))
	if err != nil || id.IsZero() {
		writeProtocolBindingAPIError(w, protocolBindingNotFound("spec_not_found"))
		return
	}
	body := protocolBindingReconcileBody{}
	if err := decodeProtocolBindingSpecJSON(r, &body); err != nil && !errors.Is(err, errEmptyProtocolBindingSpecBody) {
		writeWorkError(w, broken(http.StatusBadRequest, "invalid_command"))
		return
	}
	spec, err := m.GetProtocolBindingSpec(r.Context(), mc.Tenant, id)
	if err != nil {
		writeProtocolBindingAPIError(w, err)
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
	if hasVersion && expectedVersion != spec.Version {
		writeWorkError(w, broken(http.StatusPreconditionFailed, "version_mismatch"))
		return
	}
	if !hasVersion {
		expectedVersion = spec.Version
	}
	if mode == ModeApply {
		key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		if parsed, parseErr := model.ParseID(key); parseErr != nil || parsed.IsZero() || parsed.String() != key {
			writeWorkError(w, broken(http.StatusBadRequest, "idempotency_key_required"))
			return
		}
	}
	planHash, err := reconcilePlanPrecondition(r.Header.Get("If-Plan-Hash"), body.PlanHash)
	if err != nil {
		writeWorkError(w, err)
		return
	}
	command := ProtocolBindingSpecCommand{
		Operation: operation, WorkspaceID: spec.WorkspaceID, SpecID: spec.ID,
		ExpectedVersion: expectedVersion, ExpectedPlanHash: planHash,
	}
	if operation == ProtocolBindingSpecActivate {
		input := protocolBindingSpecInput(spec)
		validation := m.validateProtocolBindingSpecCapability(r.Context(), mc.Tenant, input)
		command.validationOverride = &validation
	}
	m.handleProtocolBindingSpecCommand(w, r, mc.Tenant, mode, command, http.StatusOK)
}

func (m *Module) handleProtocolBindingSpecCommand(
	w http.ResponseWriter,
	r *http.Request,
	tenant model.TenantID,
	mode ExecutionMode,
	command ProtocolBindingSpecCommand,
	createdStatus int,
) {
	plan, err := m.PlanProtocolBindingSpec(r.Context(), tenant, command)
	if err != nil {
		writeProtocolBindingAPIError(w, err)
		return
	}
	if command.ExpectedVersion > 0 {
		w.Header().Set("ETag", protocolBindingETag(command.ExpectedVersion))
	}
	switch mode {
	case ModeValidate:
		plan.Code = string(command.Operation) + "_valid"
		plan.PlanHash = ""
		writeJSON(w, http.StatusOK, plan)
	case ModePlan:
		writeJSON(w, http.StatusOK, plan)
	case ModeApply:
		if command.ExpectedPlanHash == "" {
			writeWorkError(w, broken(http.StatusPreconditionRequired, "plan_hash_required"))
			return
		}
		result, err := m.ApplyProtocolBindingSpec(r.Context(), tenant, command)
		if err != nil {
			writeProtocolBindingAPIError(w, err)
			return
		}
		w.Header().Set("ETag", protocolBindingETag(result.Spec.Version))
		status := createdStatus
		if result.Replayed {
			status = http.StatusOK
			w.Header().Set("Idempotency-Replayed", "true")
		}
		result.Spec = protocolBindingSpecAPI(result.Spec)
		writeJSON(w, status, result)
	}
}

func protocolBindingSpecMode(r *http.Request) (ExecutionMode, bool) {
	if len(r.URL.Query()) != 1 || len(r.URL.Query()["mode"]) != 1 {
		return "", false
	}
	mode := ExecutionMode(r.URL.Query().Get("mode"))
	return mode, mode == ModeValidate || mode == ModePlan || mode == ModeApply
}

var errEmptyProtocolBindingSpecBody = errors.New("sessions: empty protocol binding spec body")

func decodeProtocolBindingSpecJSON(r *http.Request, target any) error {
	if r.Body == nil || r.ContentLength == 0 {
		return errEmptyProtocolBindingSpecBody
	}
	decoder := jsonDecoder(io.LimitReader(r.Body, 1<<20))
	if err := decoder.Decode(target); err != nil {
		if errors.Is(err, io.EOF) {
			return errEmptyProtocolBindingSpecBody
		}
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return errors.New("sessions: trailing protocol binding spec body")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func protocolBindingSpecQueryFromRequest(r *http.Request, mc api.ModuleContext) (ProtocolBindingSpecQuery, error) {
	allowed := map[string]bool{
		"workspace_id": true, "binding_key": true, "generation": true,
		"protocol": true, "direction": true, "local_kind": true,
		"peer_authority": true, "state": true, "limit": true, "cursor": true,
	}
	for key, values := range r.URL.Query() {
		if !allowed[key] || len(values) != 1 {
			return ProtocolBindingSpecQuery{}, protocolBindingInvalid("invalid_spec_query")
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
		return ProtocolBindingSpecQuery{}, protocolBindingInvalid("invalid_spec_query")
	}
	generation := int64(0)
	if raw := values.Get("generation"); raw != "" {
		generation, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || generation < 1 {
			return ProtocolBindingSpecQuery{}, protocolBindingInvalid("invalid_spec_query")
		}
	}
	limit, err := queryLimit(r)
	if err != nil {
		return ProtocolBindingSpecQuery{}, protocolBindingInvalid("invalid_spec_query")
	}
	query := ProtocolBindingSpecQuery{
		WorkspaceID: workspace, BindingKey: values.Get("binding_key"), Generation: generation,
		Protocol: BindingProtocol(values.Get("protocol")), Direction: BindingDirection(values.Get("direction")),
		LocalKind: BindingLocalKind(values.Get("local_kind")), PeerAuthority: values.Get("peer_authority"),
		State: ProtocolBindingSpecState(values.Get("state")), Limit: limit, Cursor: values.Get("cursor"),
	}
	return normalizeProtocolBindingSpecQuery(query)
}
