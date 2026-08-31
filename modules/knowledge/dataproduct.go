// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	dpStatusDraft      = "draft"
	dpStatusPublished  = "published"
	dpStatusDeprecated = "deprecated"
	dpStatusArchived   = "archived"

	dpModeEnforce = "enforce"
	dpModeWarn    = "warn"
	dpModeObserve = "observe"

	contractModeStrict  = "strict"
	contractModeLenient = "lenient"
	contractModeNone    = "none"

	contractStatusActive     = "active"
	contractStatusSuperseded = "superseded"

	dpEventContractViolation   = "contract_violation"
	dpEventQualityGateDeny     = "quality_gate_deny"
	dpEventQualityGateWarn     = "quality_gate_warn"
	dpEventFreshnessBreach     = "freshness_breach"
	dpEventLifecycleTransition = "lifecycle_transition"
	dpEventHealthCheck         = "health_check"

	dpSeverityLow      = "low"
	dpSeverityMedium   = "medium"
	dpSeverityHigh     = "high"
	dpSeverityCritical = "critical"

	subjectKindKB       = "kb"
	subjectKindDocument = "document"
	subjectKindQuery    = "query"
)

type dataProductRequest struct {
	Name                 *string        `json:"name,omitempty"`
	Description          *string        `json:"description,omitempty"`
	OwnerRef             *string        `json:"owner_ref,omitempty"`
	KBRef                *string        `json:"kb_ref,omitempty"`
	KBID                 *string        `json:"kb_id,omitempty"`
	Tags                 map[string]any `json:"tags,omitempty"`
	FreshnessSLASeconds  *int64         `json:"freshness_sla_seconds,omitempty"`
	AvailabilityTarget   *string        `json:"availability_target,omitempty"`
	EnforcementMode      *string        `json:"enforcement_mode,omitempty"`
	QualityScoreOverride *int64         `json:"quality_score,omitempty"`
}

type dataProductDTO struct {
	ID                  string         `json:"id"`
	Name                string         `json:"name"`
	Description         string         `json:"description,omitempty"`
	OwnerRef            string         `json:"owner_ref"`
	Status              string         `json:"status"`
	KBRef               string         `json:"kb_ref,omitempty"`
	Tags                map[string]any `json:"tags,omitempty"`
	FreshnessSLASeconds int64          `json:"freshness_sla_seconds"`
	AvailabilityTarget  string         `json:"availability_target,omitempty"`
	QualityScore        int64          `json:"quality_score"`
	UsageCount          int64          `json:"usage_count"`
	EnforcementMode     string         `json:"enforcement_mode"`
	LastIngestAt        string         `json:"last_ingest_at,omitempty"`
	LastHealthAt        string         `json:"last_health_at,omitempty"`
}

type dataContractRequest struct {
	SchemaDefinition         json.RawMessage `json:"schema_definition,omitempty"`
	ValidationMode           string          `json:"validation_mode,omitempty"`
	CompletenessThreshold    int64           `json:"completeness_threshold,omitempty"`
	FreshnessOverrideSeconds int64           `json:"freshness_override_seconds,omitempty"`
	Note                     string          `json:"note,omitempty"`
}

type dataContractDTO struct {
	ID                       string `json:"id"`
	ProductRef               string `json:"product_ref"`
	Version                  int64  `json:"version"`
	SchemaDefinition         any    `json:"schema_definition,omitempty"`
	ValidationMode           string `json:"validation_mode"`
	CompletenessThreshold    int64  `json:"completeness_threshold"`
	FreshnessOverrideSeconds int64  `json:"freshness_override_seconds"`
	Status                   string `json:"status"`
	CreatedBy                string `json:"created_by"`
	Note                     string `json:"note,omitempty"`
}

type dataProductEventDTO struct {
	ID          string `json:"id"`
	ProductRef  string `json:"product_ref"`
	ContractRef string `json:"contract_ref,omitempty"`
	EventType   string `json:"event_type"`
	Severity    string `json:"severity"`
	SubjectKind string `json:"subject_kind,omitempty"`
	SubjectRef  string `json:"subject_ref,omitempty"`
	Details     any    `json:"details,omitempty"`
	OccurredAt  string `json:"occurred_at"`
}

type dataProductValidateRequest struct {
	Payload  json.RawMessage `json:"payload,omitempty"`
	Metadata map[string]any  `json:"metadata,omitempty"`
}

type dataProductValidateResponse struct {
	Valid           bool     `json:"valid"`
	Errors          []string `json:"errors,omitempty"`
	ContractVersion int64    `json:"contract_version,omitempty"`
	ValidationMode  string   `json:"validation_mode,omitempty"`
}

type dataProductHealthDTO struct {
	ProductID string `json:"product_id"`
	Freshness struct {
		Status     string `json:"status"`
		AgeSeconds int64  `json:"age_seconds"`
		SLASeconds int64  `json:"sla_seconds"`
	} `json:"freshness"`
	Quality struct {
		Score     int64  `json:"score"`
		Threshold int64  `json:"threshold"`
		Status    string `json:"status"`
	} `json:"quality"`
	Usage struct {
		Total  int64 `json:"total"`
		Last30 int64 `json:"last_30d"`
	} `json:"usage"`
	Contract struct {
		Version             int64  `json:"version"`
		ValidationMode      string `json:"validation_mode"`
		ViolationsLast30Day int64  `json:"violations_last_30d"`
	} `json:"contract"`
	KB struct {
		ID         string `json:"id,omitempty"`
		Name       string `json:"name,omitempty"`
		DocCount   int64  `json:"doc_count"`
		ChunkCount int64  `json:"chunk_count"`
	} `json:"kb"`
	OverallHealth string `json:"overall_health"`
	CheckedAt     string `json:"checked_at"`
}

type dataProductHTTPError struct {
	status int
	msg    string
	body   any
}

func (e *dataProductHTTPError) Error() string { return e.msg }

type contractViolation struct {
	DocID  string   `json:"doc_id"`
	Errors []string `json:"errors"`
}

func toDataProductDTO(rec model.Record) dataProductDTO {
	return dataProductDTO{
		ID:                  rec.String(model.ColID),
		Name:                rec.String(colName),
		Description:         rec.String(colDescription),
		OwnerRef:            rec.String(colOwnerRef),
		Status:              rec.String(colStatus),
		KBRef:               rec.String(colKBRef),
		Tags:                jsonMap(rec.String(colTags)),
		FreshnessSLASeconds: rec.Int(colFreshnessSLASeconds),
		AvailabilityTarget:  rec.String(colAvailabilityTarget),
		QualityScore:        rec.Int(colQualityScore),
		UsageCount:          rec.Int(colUsageCount),
		EnforcementMode:     rec.String(colEnforcementMode),
		LastIngestAt:        rec.String(colLastIngestAt),
		LastHealthAt:        rec.String(colLastHealthAt),
	}
}

func toDataContractDTO(rec model.Record) dataContractDTO {
	return dataContractDTO{
		ID:                       rec.String(model.ColID),
		ProductRef:               rec.String(colProductRef),
		Version:                  rec.Int(colContractVersion),
		SchemaDefinition:         jsonAny(rec.String(colSchemaDefinition)),
		ValidationMode:           rec.String(colValidationMode),
		CompletenessThreshold:    rec.Int(colCompletenessThreshold),
		FreshnessOverrideSeconds: rec.Int(colFreshnessOverrideSeconds),
		Status:                   rec.String(colStatus),
		CreatedBy:                rec.String(colCreatedBy),
		Note:                     rec.String(colNote),
	}
}

func toDataProductEventDTO(rec model.Record) dataProductEventDTO {
	return dataProductEventDTO{
		ID:          rec.String(model.ColID),
		ProductRef:  rec.String(colProductRef),
		ContractRef: rec.String(colContractRef),
		EventType:   rec.String(colEventType),
		Severity:    rec.String(colSeverity),
		SubjectKind: rec.String(colSubjectKind),
		SubjectRef:  rec.String(colSubjectRef),
		Details:     jsonAny(rec.String(colDetails)),
		OccurredAt:  rec.String(colOccurredAt),
	}
}

func jsonMap(s string) map[string]any {
	if strings.TrimSpace(s) == "" || strings.TrimSpace(s) == "null" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

func jsonAny(s string) any {
	if strings.TrimSpace(s) == "" || strings.TrimSpace(s) == "null" {
		return nil
	}
	var out any
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

func jsonFromMap(m map[string]any) string {
	if len(m) == 0 {
		return "{}"
	}
	return marshalJSON(m)
}

func schemaJSON(raw json.RawMessage) string {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	return string(raw)
}

func hasSchema(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s != "" && s != "null" && s != "{}"
}

func ptrString(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
}

func normalizeDataProductMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		return dpModeEnforce
	}
	return mode
}

func validateDataProductMode(mode string) bool {
	return mode == dpModeEnforce || mode == dpModeWarn || mode == dpModeObserve
}

func validateProductStatus(status string) bool {
	switch status {
	case dpStatusDraft, dpStatusPublished, dpStatusDeprecated, dpStatusArchived:
		return true
	default:
		return false
	}
}

func normalizeProductKBRef(req dataProductRequest, current string) (string, bool, string) {
	seen := false
	out := strings.TrimSpace(current)
	if req.KBRef != nil {
		out = ptrString(req.KBRef)
		seen = true
	}
	if req.KBID != nil {
		kbID := ptrString(req.KBID)
		if seen && out != kbID {
			return "", true, "kb_ref and kb_id must match when both are provided"
		}
		out = kbID
		seen = true
	}
	if out == "" {
		return "", seen, ""
	}
	id, err := model.ParseID(out)
	if err != nil || id.IsZero() {
		return "", seen, "kb_ref must be a valid id"
	}
	return id.String(), seen, ""
}

func productKBValue(kbRef string) any {
	if strings.TrimSpace(kbRef) == "" {
		return nil
	}
	return strings.TrimSpace(kbRef)
}

func normalizeDataProductCreate(req dataProductRequest, actor string) (model.Record, string) {
	name := ptrString(req.Name)
	if name == "" {
		return nil, "name is required"
	}
	if len(name) > maxNameLen || containsSecret(name) {
		return nil, "name invalid (too long or contains a secret)"
	}
	desc := ptrString(req.Description)
	if len(desc) > maxContentLen || containsSecret(desc) {
		return nil, "description invalid (too long or contains a secret)"
	}
	owner := ptrString(req.OwnerRef)
	if owner == "" {
		owner = actor
	}
	if len(owner) > maxRefLen || containsSecret(owner) {
		return nil, "owner_ref invalid (too long or contains a secret)"
	}
	kbRef, _, msg := normalizeProductKBRef(req, "")
	if msg != "" {
		return nil, msg
	}
	freshness := int64(0)
	if req.FreshnessSLASeconds != nil {
		freshness = *req.FreshnessSLASeconds
	}
	if freshness < 0 {
		return nil, "freshness_sla_seconds must be >= 0"
	}
	availability := ptrString(req.AvailabilityTarget)
	if len(availability) > maxNameLen || containsSecret(availability) {
		return nil, "availability_target invalid (too long or contains a secret)"
	}
	mode := dpModeEnforce
	if req.EnforcementMode != nil {
		mode = normalizeDataProductMode(*req.EnforcementMode)
	}
	if !validateDataProductMode(mode) {
		return nil, "enforcement_mode must be enforce, warn or observe"
	}
	score := int64(100)
	if req.QualityScoreOverride != nil {
		score = *req.QualityScoreOverride
	}
	if score < 0 || score > 100 {
		return nil, "quality_score must be between 0 and 100"
	}
	return model.Record{
		colName: name, colDescription: nullableString(desc), colOwnerRef: owner,
		colStatus: dpStatusDraft, colKBRef: productKBValue(kbRef), colTags: jsonFromMap(req.Tags),
		colFreshnessSLASeconds: freshness, colAvailabilityTarget: nullableString(availability),
		colQualityScore: score, colUsageCount: int64(0), colEnforcementMode: mode,
		colLastIngestAt: nil, colLastHealthAt: nil,
	}, ""
}

func applyDataProductUpdate(rec model.Record, req dataProductRequest) string {
	if req.Name != nil {
		name := ptrString(req.Name)
		if name == "" || len(name) > maxNameLen || containsSecret(name) {
			return "name invalid (required, too long or contains a secret)"
		}
		rec[colName] = name
	}
	if req.Description != nil {
		desc := ptrString(req.Description)
		if len(desc) > maxContentLen || containsSecret(desc) {
			return "description invalid (too long or contains a secret)"
		}
		rec[colDescription] = nullableString(desc)
	}
	if req.OwnerRef != nil {
		owner := ptrString(req.OwnerRef)
		if owner == "" || len(owner) > maxRefLen || containsSecret(owner) {
			return "owner_ref invalid (required, too long or contains a secret)"
		}
		rec[colOwnerRef] = owner
	}
	if kbRef, seen, msg := normalizeProductKBRef(req, rec.String(colKBRef)); msg != "" {
		return msg
	} else if seen {
		rec[colKBRef] = productKBValue(kbRef)
	}
	if req.Tags != nil {
		rec[colTags] = jsonFromMap(req.Tags)
	}
	if req.FreshnessSLASeconds != nil {
		if *req.FreshnessSLASeconds < 0 {
			return "freshness_sla_seconds must be >= 0"
		}
		rec[colFreshnessSLASeconds] = *req.FreshnessSLASeconds
	}
	if req.AvailabilityTarget != nil {
		availability := ptrString(req.AvailabilityTarget)
		if len(availability) > maxNameLen || containsSecret(availability) {
			return "availability_target invalid (too long or contains a secret)"
		}
		rec[colAvailabilityTarget] = nullableString(availability)
	}
	if req.EnforcementMode != nil {
		mode := normalizeDataProductMode(*req.EnforcementMode)
		if !validateDataProductMode(mode) {
			return "enforcement_mode must be enforce, warn or observe"
		}
		rec[colEnforcementMode] = mode
	}
	if req.QualityScoreOverride != nil {
		score := *req.QualityScoreOverride
		if score < 0 || score > 100 {
			return "quality_score must be between 0 and 100"
		}
		rec[colQualityScore] = score
	}
	return ""
}

func nullableString(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.TrimSpace(s)
}

func handleDataProductErr(w http.ResponseWriter, err error) {
	if ce, ok := err.(*clientError); ok {
		writeJSON(w, http.StatusBadRequest, errorBody(ce.msg))
		return
	}
	writeStoreError(w, err)
}

func (m *Module) handleCreateDataProduct(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var req dataProductRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	fields, msg := normalizeDataProductCreate(req, mc.Principal.Actor())
	if msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	var out dataProductDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(dataProductKind)
		if err != nil {
			return err
		}
		if _, ok, err := findOne(r.Context(), repo, eq(colName, fields[colName])); err != nil {
			return err
		} else if ok {
			return store.ErrConflict
		}
		rec, err := repo.Create(r.Context(), fields)
		if err != nil {
			return err
		}
		out = toDataProductDTO(rec)
		return auditEvent(r.Context(), sc, mc, "knowledge.data_product.create", dataProductKind, model.ID(out.ID),
			map[string]any{"status": dpStatusDraft, "kb_ref": out.KBRef})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (m *Module) handleListDataProducts(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := strings.TrimSpace(r.URL.Query().Get("status")); v != "" {
		q.Filters = append(q.Filters, eq(colStatus, v))
	}
	if v := strings.TrimSpace(r.URL.Query().Get("owner_ref")); v != "" {
		q.Filters = append(q.Filters, eq(colOwnerRef, v))
	}
	if v := strings.TrimSpace(r.URL.Query().Get("kb_ref")); v != "" {
		q.Filters = append(q.Filters, eq(colKBRef, v))
	}
	out := listResponse[dataProductDTO]{Items: []dataProductDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(dataProductKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toDataProductDTO(rec))
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

func (m *Module) handleGetDataProduct(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var out dataProductDTO
	found := false
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		rec, ok, err := loadDataProduct(r.Context(), sc, id)
		if err != nil || !ok {
			return err
		}
		found, out = true, toDataProductDTO(rec)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (m *Module) handleUpdateDataProduct(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var req dataProductRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	var out dataProductDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(dataProductKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		if rec.String(colStatus) == dpStatusArchived {
			return &clientError{"archived data products cannot be updated"}
		}
		oldName := rec.String(colName)
		if msg := applyDataProductUpdate(rec, req); msg != "" {
			return &clientError{msg}
		}
		if rec.String(colName) != oldName {
			if existing, ok, err := findOne(r.Context(), repo, eq(colName, rec.String(colName))); err != nil {
				return err
			} else if ok && existing.String(model.ColID) != id.String() {
				return store.ErrConflict
			}
		}
		updated, err := repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		out = toDataProductDTO(updated)
		return auditEvent(r.Context(), sc, mc, "knowledge.data_product.update", dataProductKind, id,
			map[string]any{"status": out.Status, "kb_ref": out.KBRef})
	})
	if err != nil {
		handleDataProductErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (m *Module) handleDeleteDataProduct(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	found := false
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(dataProductKind)
		if err != nil {
			return err
		}
		if _, err := repo.Get(r.Context(), id); err != nil {
			if isNotFound(err) {
				return nil
			}
			return err
		}
		found = true
		cRepo, err := sc.Ext(dataContractKind)
		if err != nil {
			return err
		}
		contracts, err := listAll(r.Context(), cRepo, eq(colProductRef, id.String()))
		if err != nil {
			return err
		}
		for _, c := range contracts {
			if err := cRepo.Delete(r.Context(), model.ID(c.String(model.ColID))); err != nil {
				return err
			}
		}
		if err := repo.Delete(r.Context(), id); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "knowledge.data_product.delete", dataProductKind, id,
			map[string]any{"contracts_deleted": len(contracts), "events_retained": true})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func (m *Module) handlePublishDataProduct(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.handleDataProductTransition(w, r, mc, dpStatusPublished)
}

func (m *Module) handleDeprecateDataProduct(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.handleDataProductTransition(w, r, mc, dpStatusDeprecated)
}

func (m *Module) handleArchiveDataProduct(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.handleDataProductTransition(w, r, mc, dpStatusArchived)
}

func (m *Module) handleDataProductTransition(w http.ResponseWriter, r *http.Request, mc api.ModuleContext, to string) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var out dataProductDTO
	found := false
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(dataProductKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			if isNotFound(err) {
				return nil
			}
			return err
		}
		found = true
		from := rec.String(colStatus)
		if !validDataProductTransition(from, to) {
			return &clientError{"invalid lifecycle transition from " + from + " to " + to}
		}
		rec[colStatus] = to
		updated, err := repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		out = toDataProductDTO(updated)
		if err := m.writeDataProductEvent(r.Context(), sc, id.String(), "", dpEventLifecycleTransition, dpSeverityLow,
			"data_product", id.String(), map[string]any{"from": from, "to": to}); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "knowledge.data_product."+to, dataProductKind, id,
			map[string]any{"from": from, "to": to})
	})
	if err != nil {
		handleDataProductErr(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func validDataProductTransition(from, to string) bool {
	if from == to && to == dpStatusArchived {
		return true
	}
	switch to {
	case dpStatusPublished:
		return from == dpStatusDraft
	case dpStatusDeprecated:
		return from == dpStatusPublished
	case dpStatusArchived:
		return validateProductStatus(from)
	default:
		return false
	}
}

func (m *Module) handleCreateDataContract(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	productID, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var req dataContractRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if msg := normalizeDataContractRequest(&req); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	var out dataContractDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		if _, ok, err := loadDataProduct(r.Context(), sc, productID); err != nil || !ok {
			return err
		}
		repo, err := sc.Ext(dataContractKind)
		if err != nil {
			return err
		}
		contracts, err := listAll(r.Context(), repo, eq(colProductRef, productID.String()))
		if err != nil {
			return err
		}
		version := int64(1)
		for _, c := range contracts {
			if c.String(colStatus) == contractStatusActive {
				c[colStatus] = contractStatusSuperseded
				if _, err := repo.Update(r.Context(), c); err != nil {
					return err
				}
			}
			if c.Int(colContractVersion) >= version {
				version = c.Int(colContractVersion) + 1
			}
		}
		rec, err := repo.Create(r.Context(), model.Record{
			colProductRef: productID.String(), colContractVersion: version,
			colSchemaDefinition: nullableString(schemaJSON(req.SchemaDefinition)),
			colValidationMode:   req.ValidationMode, colCompletenessThreshold: req.CompletenessThreshold,
			colFreshnessOverrideSeconds: req.FreshnessOverrideSeconds, colStatus: contractStatusActive,
			colCreatedBy: mc.Principal.Actor(), colNote: nullableString(req.Note),
		})
		if err != nil {
			return err
		}
		out = toDataContractDTO(rec)
		return auditEvent(r.Context(), sc, mc, "knowledge.data_contract.create", dataContractKind, model.ID(out.ID),
			map[string]any{"product_ref": productID.String(), "version": version})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if out.ID == "" {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func normalizeDataContractRequest(req *dataContractRequest) string {
	req.ValidationMode = strings.ToLower(strings.TrimSpace(req.ValidationMode))
	if req.ValidationMode == "" {
		if hasSchema(req.SchemaDefinition) {
			req.ValidationMode = contractModeStrict
		} else {
			req.ValidationMode = contractModeNone
		}
	}
	if req.ValidationMode != contractModeStrict && req.ValidationMode != contractModeLenient && req.ValidationMode != contractModeNone {
		return "validation_mode must be strict, lenient or none"
	}
	if req.CompletenessThreshold < 0 || req.CompletenessThreshold > 100 {
		return "completeness_threshold must be between 0 and 100"
	}
	if req.FreshnessOverrideSeconds < 0 {
		return "freshness_override_seconds must be >= 0"
	}
	if len(req.Note) > maxNameLen || containsSecret(req.Note) {
		return "note invalid (too long or contains a secret)"
	}
	if len(req.SchemaDefinition) > maxContentLen {
		return "schema_definition too large"
	}
	if hasSchema(req.SchemaDefinition) {
		var schema any
		if err := json.Unmarshal(req.SchemaDefinition, &schema); err != nil {
			return "schema_definition must be valid JSON"
		}
		if _, ok := schema.(map[string]any); !ok {
			return "schema_definition must be a JSON object"
		}
	}
	return ""
}

func (m *Module) handleListDataContracts(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	productID, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	out := listResponse[dataContractDTO]{Items: []dataContractDTO{}}
	found := false
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		if _, ok, err := loadDataProduct(r.Context(), sc, productID); err != nil || !ok {
			return err
		}
		found = true
		repo, err := sc.Ext(dataContractKind)
		if err != nil {
			return err
		}
		q := listQuery(r)
		q.Filters = append(q.Filters, eq(colProductRef, productID.String()))
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toDataContractDTO(rec))
		}
		sort.Slice(out.Items, func(i, j int) bool { return out.Items[i].Version < out.Items[j].Version })
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (m *Module) handleGetActiveDataContract(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	productID, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	m.writeContractBySelector(w, r, mc, productID, 0, true)
}

func (m *Module) handleGetDataContract(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	productID, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	ver, err := strconv.ParseInt(strings.TrimSpace(chi.URLParam(r, "ver")), 10, 64)
	if err != nil || ver <= 0 {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid contract version"))
		return
	}
	m.writeContractBySelector(w, r, mc, productID, ver, false)
}

func (m *Module) writeContractBySelector(w http.ResponseWriter, r *http.Request, mc api.ModuleContext, productID model.ID, version int64, active bool) {
	var out dataContractDTO
	foundProduct, foundContract := false, false
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		if _, ok, err := loadDataProduct(r.Context(), sc, productID); err != nil || !ok {
			return err
		}
		foundProduct = true
		var rec model.Record
		var ok bool
		var err error
		if active {
			rec, ok, err = loadActiveDataContract(r.Context(), sc, productID)
		} else {
			repo, rerr := sc.Ext(dataContractKind)
			if rerr != nil {
				return rerr
			}
			rec, ok, err = findOne(r.Context(), repo, eq(colProductRef, productID.String()), eq(colContractVersion, version))
		}
		if err != nil || !ok {
			return err
		}
		foundContract, out = true, toDataContractDTO(rec)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !foundProduct || !foundContract {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (m *Module) handleListDataProductEvents(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	productID, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	out := listResponse[dataProductEventDTO]{Items: []dataProductEventDTO{}}
	found := false
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		if _, ok, err := loadDataProduct(r.Context(), sc, productID); err != nil || !ok {
			return err
		}
		found = true
		repo, err := sc.Ext(dpEventKind)
		if err != nil {
			return err
		}
		q := listQuery(r)
		q.Filters = append(q.Filters, eq(colProductRef, productID.String()))
		if v := strings.TrimSpace(r.URL.Query().Get("event_type")); v != "" {
			q.Filters = append(q.Filters, eq(colEventType, v))
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toDataProductEventDTO(rec))
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (m *Module) handleValidateDataProduct(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	productID, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var req dataProductValidateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(req.Payload) == 0 && req.Metadata == nil {
		writeJSON(w, http.StatusBadRequest, errorBody("payload or metadata is required"))
		return
	}
	var out dataProductValidateResponse
	found := false
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		if _, ok, err := loadDataProduct(r.Context(), sc, productID); err != nil || !ok {
			return err
		}
		found = true
		contract, ok, err := loadActiveDataContract(r.Context(), sc, productID)
		if err != nil || !ok {
			out.Valid = true
			return err
		}
		out.ContractVersion = contract.Int(colContractVersion)
		out.ValidationMode = contract.String(colValidationMode)
		if contract.String(colValidationMode) == contractModeNone || strings.TrimSpace(contract.String(colSchemaDefinition)) == "" {
			out.Valid = true
			return nil
		}
		data := any(req.Metadata)
		if len(req.Payload) > 0 {
			var payload any
			if err := json.Unmarshal(req.Payload, &payload); err != nil {
				out.Errors = []string{"payload must be valid JSON"}
				out.Valid = false
				return nil
			}
			data = payload
		}
		out.Errors = validateAgainstStoredSchema(data, contract.String(colSchemaDefinition))
		out.Valid = len(out.Errors) == 0
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (m *Module) handleDataProductHealth(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	productID, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var out dataProductHealthDTO
	found := false
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		productRepo, err := sc.Ext(dataProductKind)
		if err != nil {
			return err
		}
		product, err := productRepo.Get(r.Context(), productID)
		if err != nil {
			if isNotFound(err) {
				return nil
			}
			return err
		}
		found = true
		contract, hasContract, err := loadActiveDataContract(r.Context(), sc, productID)
		if err != nil {
			return err
		}
		var kb model.Record
		if kbID := product.String(colKBRef); kbID != "" {
			kbRec, ok, err := loadKB(r.Context(), sc, model.ID(kbID))
			if err != nil {
				return err
			}
			if ok {
				kb = kbRec
			}
		}
		checkedAt := m.clock.Now()
		out = computeDataProductHealth(product, contract, hasContract, kb, checkedAt)
		out.Usage.Last30, out.Contract.ViolationsLast30Day, err = m.dataProductLast30Stats(r.Context(), sc, product, checkedAt)
		if err != nil {
			return err
		}
		product[colQualityScore] = out.Quality.Score
		product[colLastHealthAt] = checkedAt.String()
		if _, err := productRepo.Update(r.Context(), product); err != nil {
			return err
		}
		if err := m.writeDataProductEvent(r.Context(), sc, productID.String(), contract.String(model.ColID), dpEventHealthCheck, dpSeverityLow,
			"data_product", productID.String(), map[string]any{
				"overall_health": out.OverallHealth, "freshness": out.Freshness.Status, "quality": out.Quality.Status,
			}); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "knowledge.data_product.health", dataProductKind, productID,
			map[string]any{"overall_health": out.OverallHealth})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func computeDataProductHealth(product, contract model.Record, hasContract bool, kb model.Record, checkedAt model.Timestamp) dataProductHealthDTO {
	out := dataProductHealthDTO{ProductID: product.String(model.ColID), CheckedAt: checkedAt.String()}
	sla := product.Int(colFreshnessSLASeconds)
	if hasContract && contract.Int(colFreshnessOverrideSeconds) > 0 {
		sla = contract.Int(colFreshnessOverrideSeconds)
	}
	out.Freshness.SLASeconds = sla
	if last := product.String(colLastIngestAt); last != "" {
		if ts, err := model.ParseTimestamp(last); err == nil {
			age := int64(checkedAt.Time().Sub(ts.Time()).Seconds())
			if age < 0 {
				age = 0
			}
			out.Freshness.AgeSeconds = age
			if sla > 0 && age > sla {
				out.Freshness.Status = "stale"
			} else {
				out.Freshness.Status = "fresh"
			}
		}
	}
	if out.Freshness.Status == "" {
		if sla > 0 {
			out.Freshness.Status = "unknown"
		} else {
			out.Freshness.Status = "fresh"
		}
	}
	out.Quality.Score = computeQualityScore(kb)
	if hasContract {
		out.Quality.Threshold = contract.Int(colCompletenessThreshold)
		out.Contract.Version = contract.Int(colContractVersion)
		out.Contract.ValidationMode = contract.String(colValidationMode)
	}
	switch {
	case out.Quality.Threshold <= 0:
		out.Quality.Status = "unconfigured"
	case out.Quality.Score >= out.Quality.Threshold:
		out.Quality.Status = "passing"
	default:
		out.Quality.Status = "failing"
	}
	out.Usage.Total = product.Int(colUsageCount)
	if kb != nil {
		out.KB.ID = kb.String(model.ColID)
		out.KB.Name = kb.String(colName)
		out.KB.DocCount = kb.Int(colDocCount)
		out.KB.ChunkCount = kb.Int(colChunkCount)
	}
	mode := normalizeDataProductMode(product.String(colEnforcementMode))
	out.OverallHealth = "healthy"
	if out.Freshness.Status == "stale" || out.Freshness.Status == "unknown" || out.Quality.Status == "failing" {
		out.OverallHealth = "degraded"
	}
	if mode == dpModeEnforce && (out.Freshness.Status == "stale" || out.Quality.Status == "failing") {
		out.OverallHealth = "unhealthy"
	}
	return out
}

func computeQualityScore(kb model.Record) int64 {
	if kb == nil {
		return 0
	}
	docs := kb.Int(colDocCount)
	chunks := kb.Int(colChunkCount)
	switch {
	case docs <= 0:
		return 0
	case chunks <= 0:
		return 50
	default:
		return 100
	}
}

func (m *Module) dataProductLast30Stats(ctx context.Context, sc store.Scope, product model.Record, now model.Timestamp) (usage, violations int64, err error) {
	cutoff := model.NewTimestamp(now.Time().Add(-30 * 24 * time.Hour)).String()
	if kbID := product.String(colKBRef); kbID != "" {
		repo, err := sc.Ext(lineageKind)
		if err != nil {
			return 0, 0, err
		}
		rows, err := listAll(ctx, repo, eq(colKBRef, kbID), eq(colDecision, decisionAllowed),
			model.Filter{Column: colOccurredAt, Op: model.OpGte, Value: cutoff})
		if err != nil {
			return 0, 0, err
		}
		usage = int64(len(rows))
	}
	eventRepo, err := sc.Ext(dpEventKind)
	if err != nil {
		return 0, 0, err
	}
	rows, err := listAll(ctx, eventRepo, eq(colProductRef, product.String(model.ColID)), eq(colEventType, dpEventContractViolation),
		model.Filter{Column: colOccurredAt, Op: model.OpGte, Value: cutoff})
	if err != nil {
		return 0, 0, err
	}
	return usage, int64(len(rows)), nil
}

func loadDataProduct(ctx context.Context, sc store.Scope, id model.ID) (model.Record, bool, error) {
	repo, err := sc.Ext(dataProductKind)
	if err != nil {
		return nil, false, err
	}
	rec, err := repo.Get(ctx, id)
	if err != nil {
		if isNotFound(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return rec, true, nil
}

func loadGoverningDataProductForKB(ctx context.Context, sc store.Scope, kbID model.ID) (model.Record, bool, error) {
	repo, err := sc.Ext(dataProductKind)
	if err != nil {
		return nil, false, err
	}
	recs, err := listAll(ctx, repo, eq(colKBRef, kbID.String()))
	if err != nil {
		return nil, false, err
	}
	var draft model.Record
	for _, rec := range recs {
		switch rec.String(colStatus) {
		case dpStatusArchived:
			return rec, true, nil
		case dpStatusPublished, dpStatusDeprecated:
			return rec, true, nil
		case dpStatusDraft:
			if draft == nil {
				draft = rec
			}
		}
	}
	if draft != nil {
		return draft, true, nil
	}
	return nil, false, nil
}

func loadActiveDataContract(ctx context.Context, sc store.Scope, productID model.ID) (model.Record, bool, error) {
	repo, err := sc.Ext(dataContractKind)
	if err != nil {
		return nil, false, err
	}
	return findOne(ctx, repo, eq(colProductRef, productID.String()), eq(colStatus, contractStatusActive))
}

func (m *Module) writeDataProductEvent(ctx context.Context, sc store.Scope, productRef, contractRef, eventType, severity, subjectKind, subjectRef string, details any) error {
	repo, err := sc.Ext(dpEventKind)
	if err != nil {
		return err
	}
	fields := model.Record{
		colProductRef: productRef, colContractRef: nullableString(contractRef),
		colEventType: eventType, colSeverity: severity, colSubjectKind: nullableString(subjectKind),
		colSubjectRef: nullableString(subjectRef), colOccurredAt: m.clock.Now().String(),
	}
	if details != nil {
		fields[colDetails] = marshalJSON(details)
	} else {
		fields[colDetails] = nil
	}
	_, err = repo.Create(ctx, fields)
	return err
}

func (m *Module) enforceDataProductIngest(ctx context.Context, mc api.ModuleContext, kb model.Record, docs []contentsource.Document) (model.ID, error) {
	var product model.Record
	var contract model.Record
	var hasContract bool
	err := mc.Data.View(ctx, func(sc store.Scope) error {
		p, ok, err := loadGoverningDataProductForKB(ctx, sc, model.ID(kb.String(model.ColID)))
		if err != nil || !ok {
			return err
		}
		product = p
		if p.String(colStatus) == dpStatusPublished || p.String(colStatus) == dpStatusDeprecated {
			contract, hasContract, err = loadActiveDataContract(ctx, sc, model.ID(p.String(model.ColID)))
			return err
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if product == nil {
		return "", nil
	}
	productID := model.ID(product.String(model.ColID))
	switch product.String(colStatus) {
	case dpStatusArchived:
		return productID, &dataProductHTTPError{status: http.StatusConflict, msg: "data product archived; ingest refused"}
	case dpStatusDraft:
		return "", nil
	}
	if !hasContract || contract.String(colValidationMode) == contractModeNone || strings.TrimSpace(contract.String(colSchemaDefinition)) == "" {
		return productID, nil
	}
	violations := validateDocsAgainstContract(docs, contract.String(colSchemaDefinition))
	if len(violations) == 0 {
		return productID, nil
	}
	mode := contract.String(colValidationMode)
	severity := dpSeverityMedium
	if mode == contractModeStrict {
		severity = dpSeverityHigh
	}
	if err := mc.Data.Mutate(ctx, func(sc store.Scope) error {
		for _, v := range violations {
			if err := m.writeDataProductEvent(ctx, sc, productID.String(), contract.String(model.ColID),
				dpEventContractViolation, severity, subjectKindDocument, v.DocID,
				map[string]any{"errors": v.Errors, "contract_version": contract.Int(colContractVersion)}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return productID, err
	}
	if mode == contractModeStrict {
		return productID, &dataProductHTTPError{
			status: http.StatusUnprocessableEntity,
			msg:    "data contract violation",
			body: map[string]any{"error": map[string]any{
				"message": "data contract violation",
				"details": violations,
			}},
		}
	}
	return productID, nil
}

func (m *Module) markDataProductIngested(ctx context.Context, sc store.Scope, productID model.ID) error {
	if productID.IsZero() {
		return nil
	}
	repo, err := sc.Ext(dataProductKind)
	if err != nil {
		return err
	}
	rec, err := repo.Get(ctx, productID)
	if err != nil {
		return err
	}
	rec[colLastIngestAt] = m.clock.Now().String()
	_, err = repo.Update(ctx, rec)
	return err
}

type dpQualityGateOutcome struct {
	productID model.ID
	denied    bool
	reason    string
	message   string
}

func (m *Module) enforceDataProductQualityGate(ctx context.Context, mc api.ModuleContext, kb model.Record, qr queryRequest, queryHash, region string) (model.ID, *QueryError) {
	var outcome dpQualityGateOutcome
	err := mc.Data.Mutate(ctx, func(sc store.Scope) error {
		product, ok, err := loadGoverningDataProductForKB(ctx, sc, model.ID(kb.String(model.ColID)))
		if err != nil || !ok {
			return err
		}
		productID := model.ID(product.String(model.ColID))
		outcome.productID = productID
		switch product.String(colStatus) {
		case dpStatusArchived:
			outcome.denied = true
			outcome.reason = "data product archived"
			outcome.message = "data product archived"
			return m.writeDataProductEvent(ctx, sc, productID.String(), "", dpEventQualityGateDeny, dpSeverityCritical,
				subjectKindQuery, queryHash, map[string]any{"reason": outcome.reason})
		case dpStatusDraft:
			outcome.productID = ""
			return nil
		}
		contract, hasContract, err := loadActiveDataContract(ctx, sc, productID)
		if err != nil {
			return err
		}
		if decision := m.evaluateFreshnessGate(product, contract, hasContract, queryHash); decision != nil {
			if err := m.writeDataProductEvent(ctx, sc, productID.String(), contract.String(model.ColID),
				decision.eventType, decision.severity, subjectKindQuery, queryHash, decision.details); err != nil {
				return err
			}
			if decision.deny {
				outcome.denied, outcome.reason, outcome.message = true, decision.reason, decision.message
				return nil
			}
		}
		if hasContract {
			if decision := m.evaluateCompletenessGate(product, contract, queryHash); decision != nil {
				if err := m.writeDataProductEvent(ctx, sc, productID.String(), contract.String(model.ColID),
					decision.eventType, decision.severity, subjectKindQuery, queryHash, decision.details); err != nil {
					return err
				}
				if decision.deny {
					outcome.denied, outcome.reason, outcome.message = true, decision.reason, decision.message
					return nil
				}
			}
		}
		return nil
	})
	if err != nil {
		return "", queryErr(QueryErrUnavailable, "store error")
	}
	if outcome.denied {
		m.denyQuery(ctx, mc, model.ID(kb.String(model.ColID)), qr, queryHash, region, outcome.reason, "", "")
		return outcome.productID, queryErr(QueryErrConflict, outcome.message)
	}
	return outcome.productID, nil
}

type dpGateDecision struct {
	deny      bool
	eventType string
	severity  string
	reason    string
	message   string
	details   map[string]any
}

func (m *Module) evaluateFreshnessGate(product, contract model.Record, hasContract bool, queryHash string) *dpGateDecision {
	sla := product.Int(colFreshnessSLASeconds)
	if hasContract && contract.Int(colFreshnessOverrideSeconds) > 0 {
		sla = contract.Int(colFreshnessOverrideSeconds)
	}
	if sla <= 0 {
		return nil
	}
	now := m.clock.Now()
	age := int64(math.MaxInt64)
	if last := product.String(colLastIngestAt); last != "" {
		if ts, err := model.ParseTimestamp(last); err == nil {
			age = int64(now.Time().Sub(ts.Time()).Seconds())
			if age < 0 {
				age = 0
			}
		}
	}
	if age <= sla {
		return nil
	}
	mode := normalizeDataProductMode(product.String(colEnforcementMode))
	details := map[string]any{"query_hash": queryHash, "age_seconds": age, "sla_seconds": sla, "mode": mode}
	reason := "data product freshness SLA breached"
	switch mode {
	case dpModeEnforce:
		return &dpGateDecision{deny: true, eventType: dpEventFreshnessBreach, severity: dpSeverityHigh,
			reason: reason, message: "data product freshness SLA breached", details: details}
	case dpModeWarn:
		return &dpGateDecision{eventType: dpEventQualityGateWarn, severity: dpSeverityMedium,
			reason: reason, message: reason, details: details}
	default:
		return &dpGateDecision{eventType: dpEventFreshnessBreach, severity: dpSeverityLow,
			reason: reason, message: reason, details: details}
	}
}

func (m *Module) evaluateCompletenessGate(product, contract model.Record, queryHash string) *dpGateDecision {
	threshold := contract.Int(colCompletenessThreshold)
	if threshold <= 0 {
		return nil
	}
	score := product.Int(colQualityScore)
	if score >= threshold {
		return nil
	}
	mode := normalizeDataProductMode(product.String(colEnforcementMode))
	details := map[string]any{"query_hash": queryHash, "quality_score": score, "threshold": threshold, "mode": mode}
	reason := "data product quality score below completeness threshold"
	switch mode {
	case dpModeEnforce:
		return &dpGateDecision{deny: true, eventType: dpEventQualityGateDeny, severity: dpSeverityHigh,
			reason: reason, message: "data product quality gate denied retrieval", details: details}
	case dpModeWarn:
		return &dpGateDecision{eventType: dpEventQualityGateWarn, severity: dpSeverityMedium,
			reason: reason, message: reason, details: details}
	default:
		return &dpGateDecision{eventType: dpEventQualityGateWarn, severity: dpSeverityLow,
			reason: reason, message: reason, details: details}
	}
}

func (m *Module) incrementDataProductUsage(ctx context.Context, mc api.ModuleContext, productID model.ID) error {
	if productID.IsZero() {
		return nil
	}
	return mc.Data.Mutate(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(dataProductKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, productID)
		if err != nil {
			return err
		}
		rec[colUsageCount] = rec.Int(colUsageCount) + 1
		_, err = repo.Update(ctx, rec)
		return err
	})
}

func validateDocsAgainstContract(docs []contentsource.Document, schema string) []contractViolation {
	var out []contractViolation
	for _, doc := range docs {
		target := validationTargetForDoc(doc)
		errs := validateAgainstStoredSchema(target, schema)
		if len(errs) > 0 {
			out = append(out, contractViolation{DocID: doc.DocID, Errors: errs})
		}
	}
	return out
}

func validationTargetForDoc(doc contentsource.Document) any {
	var body any
	if err := json.Unmarshal([]byte(strings.TrimSpace(doc.Body)), &body); err == nil {
		return body
	}
	return map[string]any{
		"source_kind":    string(doc.Source),
		"source_doc_id":  doc.DocID,
		"title":          doc.Title,
		"content_type":   doc.ContentType,
		"classification": doc.Classification,
		"space_ref":      doc.SpaceRef,
	}
}

func validateAgainstStoredSchema(data any, schemaText string) []string {
	if strings.TrimSpace(schemaText) == "" {
		return nil
	}
	var schema any
	if err := json.Unmarshal([]byte(schemaText), &schema); err != nil {
		return []string{"contract schema is invalid"}
	}
	return validateJSONSchemaValue(data, schema, "$")
}

func validateJSONSchemaValue(data, schema any, path string) []string {
	obj, ok := schema.(map[string]any)
	if !ok {
		return nil
	}
	var errs []string
	if types := schemaTypes(obj["type"]); len(types) > 0 && !jsonTypeMatches(data, types) {
		return []string{path + " must be " + strings.Join(types, " or ")}
	}
	if enumVals, ok := obj["enum"].([]any); ok {
		matched := false
		for _, ev := range enumVals {
			if reflect.DeepEqual(data, ev) {
				matched = true
				break
			}
		}
		if !matched {
			errs = append(errs, path+" must be one of the allowed enum values")
		}
	}
	if req, ok := obj["required"].([]any); ok {
		dataObj, isObj := data.(map[string]any)
		for _, r := range req {
			name, ok := r.(string)
			if !ok {
				continue
			}
			if !isObj {
				errs = append(errs, path+" must be an object with required fields")
				break
			}
			if _, exists := dataObj[name]; !exists {
				errs = append(errs, path+"."+name+" is required")
			}
		}
	}
	if props, ok := obj["properties"].(map[string]any); ok {
		if dataObj, isObj := data.(map[string]any); isObj {
			for name, sub := range props {
				if value, exists := dataObj[name]; exists {
					errs = append(errs, validateJSONSchemaValue(value, sub, path+"."+name)...)
				}
			}
		}
	}
	if itemSchema, ok := obj["items"]; ok {
		if arr, isArr := data.([]any); isArr {
			for i, item := range arr {
				errs = append(errs, validateJSONSchemaValue(item, itemSchema, path+"["+strconv.Itoa(i)+"]")...)
			}
		}
	}
	if min, ok := numericSchemaValue(obj["minimum"]); ok {
		if n, ok := numericSchemaValue(data); ok && n < min {
			errs = append(errs, path+" must be >= "+trimFloat(min))
		}
	}
	if max, ok := numericSchemaValue(obj["maximum"]); ok {
		if n, ok := numericSchemaValue(data); ok && n > max {
			errs = append(errs, path+" must be <= "+trimFloat(max))
		}
	}
	if min, ok := numericSchemaValue(obj["minLength"]); ok {
		if s, ok := data.(string); ok && len(s) < int(min) {
			errs = append(errs, path+" is shorter than minLength")
		}
	}
	if max, ok := numericSchemaValue(obj["maxLength"]); ok {
		if s, ok := data.(string); ok && len(s) > int(max) {
			errs = append(errs, path+" is longer than maxLength")
		}
	}
	return errs
}

func schemaTypes(v any) []string {
	switch t := v.(type) {
	case string:
		if strings.TrimSpace(t) == "" {
			return nil
		}
		return []string{strings.TrimSpace(t)}
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	default:
		return nil
	}
}

func jsonTypeMatches(data any, types []string) bool {
	for _, t := range types {
		switch t {
		case "object":
			_, ok := data.(map[string]any)
			if ok {
				return true
			}
		case "array":
			_, ok := data.([]any)
			if ok {
				return true
			}
		case "string":
			_, ok := data.(string)
			if ok {
				return true
			}
		case "number":
			if _, ok := numericSchemaValue(data); ok {
				return true
			}
		case "integer":
			if n, ok := numericSchemaValue(data); ok && math.Trunc(n) == n {
				return true
			}
		case "boolean":
			_, ok := data.(bool)
			if ok {
				return true
			}
		case "null":
			if data == nil {
				return true
			}
		}
	}
	return false
}

func numericSchemaValue(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int64:
		return float64(n), true
	case int:
		return float64(n), true
	default:
		return 0, false
	}
}

func trimFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}
