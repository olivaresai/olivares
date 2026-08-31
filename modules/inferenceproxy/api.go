// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inferenceproxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	maxClassLen = 64
	maxNoteLen  = 512
	listCap     = 1000

	// minTaskBudgetTokens mirrors claudeapi.MinTaskBudgetTokens without importing the
	// connector into this module; output_config.task_budget.total has a 20k API floor.
	minTaskBudgetTokens = 20000
)

// ---------------------------------------------------------------------------
// Config (singleton per tenant): viewer-tier read, admin-tier write.
// ---------------------------------------------------------------------------

// configDTO is the wire shape of a tenant's proxy governance config. Gate flags are
// pointers so an omitted gate defaults to ENABLED (the safe posture) while an explicit
// false disables it; fail_open defaults false (the safe posture).
//
// record_mandatory is a POINTER for the same reason the gates are, and it was a bare bool
// until this change. A bare bool cannot tell "the operator turned evidence off" from "this
// PUT was about the DLP mode and never mentioned evidence" — and Go's zero value made the
// second one silently mean the first. Every config write that omitted the field opted the
// tenant out of the product's evidence guarantee without anybody choosing it.
type configDTO struct {
	FailOpen          bool   `json:"fail_open"`
	ResponseDLPMode   string `json:"response_dlp_mode,omitempty"`
	RecordMandatory   *bool  `json:"record_mandatory,omitempty"`
	GateModelAccess   *bool  `json:"gate_model_access,omitempty"`
	GateBudget        *bool  `json:"gate_budget,omitempty"`
	GateResidency     *bool  `json:"gate_residency,omitempty"`
	GateContextWindow *bool  `json:"gate_context_window,omitempty"`
	GateDLPRequest    *bool  `json:"gate_dlp_request,omitempty"`
	GateDLPResponse   *bool  `json:"gate_dlp_response,omitempty"`

	CeilingsEnforce         *bool `json:"ceilings_enforce,omitempty"`
	CeilingMaxTokens        int64 `json:"ceiling_max_tokens,omitempty"`
	CeilingMaxToolUses      int64 `json:"ceiling_max_tool_uses,omitempty"`
	CeilingTaskBudgetTokens int64 `json:"ceiling_task_budget_tokens,omitempty"`
}

// boolOrTrue defaults an omitted gate flag to true (gates are on unless explicitly off).
func boolOrTrue(p *bool) bool { return p == nil || *p }

// boolOrFalse defaults an omitted opt-in enforcement flag to false.
func boolOrFalse(p *bool) bool { return p != nil && *p }

func policyToDTO(p ProxyPolicy) configDTO {
	t := func(b bool) *bool { return &b }
	return configDTO{
		FailOpen:          p.FailOpen,
		ResponseDLPMode:   p.ResponseDLPMode,
		RecordMandatory:   t(p.RecordMandatory),
		GateModelAccess:   t(p.GateModelAccess),
		GateBudget:        t(p.GateBudget),
		GateResidency:     t(p.GateResidency),
		GateContextWindow: t(p.GateContextWindow),
		GateDLPRequest:    t(p.GateDLPRequest),
		GateDLPResponse:   t(p.GateDLPResponse),

		CeilingsEnforce:         t(p.Ceilings.Enforce),
		CeilingMaxTokens:        p.Ceilings.MaxTokens,
		CeilingMaxToolUses:      p.Ceilings.MaxToolUses,
		CeilingTaskBudgetTokens: p.Ceilings.TaskBudgetTokens,
	}
}

// fields renders the config row's columns from the DTO, applying the gate defaults.
func (d configDTO) fields(updatedBy string) model.Record {
	return model.Record{
		colFailOpen:        d.FailOpen,
		colResponseDLPMode: normalizeResponseMode(strings.TrimSpace(d.ResponseDLPMode)),
		colRecordMandatory: boolOrTrue(d.RecordMandatory),
		// Only an EXPLICIT value in the request is a choice. An omitted field leaves
		// this false, so the default that fills colRecordMandatory above cannot pass
		// itself off as a decision anybody made.
		colRecordMandatoryChosen: d.RecordMandatory != nil,
		colGateModelAccess:       boolOrTrue(d.GateModelAccess),
		colGateBudget:            boolOrTrue(d.GateBudget),
		colGateResidency:         boolOrTrue(d.GateResidency),
		colGateContextWin:        boolOrTrue(d.GateContextWindow),
		colGateDLPRequest:        boolOrTrue(d.GateDLPRequest),
		colGateDLPResponse:       boolOrTrue(d.GateDLPResponse),
		colUpdatedBy:             updatedBy,

		colCeilingsEnforce:         boolOrFalse(d.CeilingsEnforce),
		colCeilingMaxTokens:        d.CeilingMaxTokens,
		colCeilingMaxToolUses:      d.CeilingMaxToolUses,
		colCeilingTaskBudgetTokens: d.CeilingTaskBudgetTokens,
	}
}

// handleGetConfig returns the tenant's proxy config (the safe defaults when none is set).
func (m *Module) handleGetConfig(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	pol := defaultProxyPolicy()
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		rec, ok, err := singletonConfig(r.Context(), sc)
		if err != nil || !ok {
			return err
		}
		pol.Configured = true
		pol.FailOpen = rec.Bool(colFailOpen)
		pol.ResponseDLPMode = normalizeResponseMode(rec.String(colResponseDLPMode))
		pol.RecordMandatory = rec.Bool(colRecordMandatory)
		pol.GateModelAccess = rec.Bool(colGateModelAccess)
		pol.GateBudget = rec.Bool(colGateBudget)
		pol.GateResidency = rec.Bool(colGateResidency)
		pol.GateContextWindow = rec.Bool(colGateContextWin)
		pol.GateDLPRequest = rec.Bool(colGateDLPRequest)
		pol.GateDLPResponse = rec.Bool(colGateDLPResponse)
		pol.Ceilings = RequestCeilings{
			Enforce:          rec.Bool(colCeilingsEnforce),
			MaxTokens:        rec.Int(colCeilingMaxTokens),
			MaxToolUses:      rec.Int(colCeilingMaxToolUses),
			TaskBudgetTokens: rec.Int(colCeilingTaskBudgetTokens),
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, policyToDTO(pol))
}

// handlePutConfig upserts the tenant's singleton config (admin-tier; self-audited).
func (m *Module) handlePutConfig(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if !requireAAL3(w, mc) {
		return
	}
	var in configDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	if mode := strings.TrimSpace(in.ResponseDLPMode); mode != "" &&
		mode != ResponseDLPOff && mode != ResponseDLPFlag && mode != ResponseDLPBuffer {
		writeJSON(w, http.StatusBadRequest, errorBody("response_dlp_mode must be one of off, flag, buffer"))
		return
	}
	if msg := validateConfigCeilings(in); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(configKind)
		if err != nil {
			return err
		}
		fields := in.fields(mc.Principal.Actor())
		existing, ok, err := singletonConfig(r.Context(), sc)
		if err != nil {
			return err
		}
		if ok {
			// CAPTURED BEFORE THE MERGE, and the first attempt at this fix read it AFTER —
			// which returns the value the loop below had just overwritten, so the correction
			// corrected nothing. The handler-level regression is what caught that; the
			// version of it that re-implemented this rule inline had stayed green.
			// BOTH columns are captured here, and for one reason: the loop below
			// overwrites every key of `existing` with the incoming request's value.
			// The line that used to stand where prevChosen now does was
			// `_, _ = existing[colRecordMandatoryChosen]`, which discards both
			// results — it read like the capture and was not one.
			prevMandatory, hadMandatory := existing[colRecordMandatory]
			prevChosen, hadChosen := existing[colRecordMandatoryChosen]
			for k, v := range fields {
				existing[k] = v
			}
			// AN OMITTED record_mandatory MUST NOT REVOKE AN EXPLICIT OPT-OUT, and the first
			// version of this change created exactly that defect while fixing its mirror
			// image. `nil => true` is the right rule when a row is being CREATED: a
			// deployment that never said anything gets the safe posture. Applied to an
			// UPDATE it is the same silent overwrite in the other direction — a PUT about the
			// DLP mode would turn an operator's deliberate `false` back on, and this
			// repository already issues partial PUTs.
			//
			// So on update the omitted field KEEPS what the row says, and only an explicit
			// value changes it. The contrast measured the sequence that breaks otherwise:
			// PUT {record_mandatory:false} then PUT {fail_open:true} ended mandatory.
			if in.RecordMandatory == nil && hadMandatory {
				fields[colRecordMandatory] = prevMandatory
				existing[colRecordMandatory] = prevMandatory
				// AND THE PROVENANCE IS PRESERVED TOO: a PUT that never mentioned
				// evidence neither makes a choice nor unmakes one.
				//
				// This read used to sit AFTER the merge loop, which is the same
				// mistake the comment above says the first attempt made about the
				// value — repeated one column over, inside the correction itself. By
				// then the loop had written `d.RecordMandatory != nil`, false on a
				// partial PUT, so the "preservation" restored false over false and
				// changed nothing.
				//
				// What that cost is not bookkeeping. An operator who wrote
				// record_mandatory=true asked for evidence-or-refuse, and the matrix
				// declares `configured/spool_degraded` a DENY. Erase the provenance
				// and defaultMandatoryYieldsTo reads the tenant as "nobody decided",
				// so it YIELDS: the tenant that most explicitly demanded evidence gets
				// its call forwarded with a gap. The console re-issues a partial PUT
				// for this field on every save, so that was the common path.
				//
				// An absent key stays absent: a row written before this column existed
				// has nobody's decision in it, and false already carries that meaning.
				if hadChosen {
					fields[colRecordMandatoryChosen] = prevChosen
					existing[colRecordMandatoryChosen] = prevChosen
				}
			}
			if _, err := repo.Update(r.Context(), existing); err != nil {
				return err
			}
		} else if _, err := repo.Create(r.Context(), fields); err != nil {
			return err
		}
		// THE EVENT SEALS WHAT WAS WRITTEN, not what the request happened to carry, and the
		// difference was a falsified record. It used to seal boolOrTrue(in.RecordMandatory):
		// on a partial PUT that renders `true` while the row correctly keeps the operator's
		// `false`, so the ledger of an evidence product asserted a choice nobody made. In a
		// product whose whole claim is tamper-evident history, an event that disagrees with
		// the row it describes is worse than no event.
		return auditEvent(r.Context(), sc, mc, "inferenceproxy.config.put", configKind, "",
			map[string]any{
				// AND THE PROVENANCE IS SEALED TOO. It was left out at first, and
				// the omission mattered more than it looks: this column, not the
				// value, decides whether a spool `degrade` forwards the call or
				// denies it. A sequence that changes only the provenance —
				// `PUT {}` then `PUT {"record_mandatory":true}`, same value both
				// times — altered enforcement while sealing two identical events.
				// An evidence ledger that cannot show when enforcement changed is
				// not evidence of enforcement.
				"fail_open": in.FailOpen, "response_dlp_mode": normalizeResponseMode(in.ResponseDLPMode), "record_mandatory": fields[colRecordMandatory],
				"record_mandatory_chosen": fields[colRecordMandatoryChosen],
				"ceilings_enforce":        boolOrFalse(in.CeilingsEnforce), "ceiling_max_tokens": in.CeilingMaxTokens,
				"ceiling_max_tool_uses": in.CeilingMaxToolUses, "ceiling_task_budget_tokens": in.CeilingTaskBudgetTokens,
			})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	m.handleGetConfig(w, r, mc)
}

func validateConfigCeilings(in configDTO) string {
	if in.CeilingMaxTokens < 0 || in.CeilingMaxToolUses < 0 || in.CeilingTaskBudgetTokens < 0 {
		return "ceilings must be greater than or equal to zero"
	}
	if in.CeilingTaskBudgetTokens > 0 && in.CeilingTaskBudgetTokens < minTaskBudgetTokens {
		return "ceiling_task_budget_tokens must be zero or at least 20000"
	}
	if boolOrFalse(in.CeilingsEnforce) &&
		in.CeilingMaxTokens == 0 && in.CeilingMaxToolUses == 0 && in.CeilingTaskBudgetTokens == 0 {
		return "ceilings_enforce requires at least one ceiling"
	}
	return ""
}

// ---------------------------------------------------------------------------
// DLP rules (per class): admin-tier writes, viewer-tier reads.
// ---------------------------------------------------------------------------

type dlpRuleDTO struct {
	ID        string `json:"id,omitempty"`
	Class     string `json:"class"`
	Action    string `json:"action"`
	Note      string `json:"note,omitempty"`
	CreatedBy string `json:"created_by,omitempty"`
}

func toDLPRuleDTO(rec model.Record) dlpRuleDTO {
	return dlpRuleDTO{
		ID: rec.String(model.ColID), Class: rec.String(colClass), Action: rec.String(colAction),
		Note: rec.String(colNote), CreatedBy: rec.String(colCreatedBy),
	}
}

// handleListDLPRules lists the tenant's inference-egress DLP rules.
func (m *Module) handleListDLPRules(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	out := listResponse[dlpRuleDTO]{Items: []dlpRuleDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(dlpRuleKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), listQuery(r))
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toDLPRuleDTO(rec))
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

// handlePutDLPRule upserts one DLP rule by class (admin-tier: authorizing egress is
// privileged governance; self-audited). Exact tenant rules override seeded defaults;
// deleting an override restores the secure default for that class.
func (m *Module) handlePutDLPRule(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if !requireAAL3(w, mc) {
		return
	}
	var req dlpRuleDTO
	if !decodeJSON(w, r, &req) {
		return
	}
	class := strings.ToLower(strings.TrimSpace(req.Class))
	action := strings.ToLower(strings.TrimSpace(req.Action))
	if class == "" || len(class) > maxClassLen {
		writeJSON(w, http.StatusBadRequest, errorBody("class is required and must be short"))
		return
	}
	if action != dlpAllow && action != dlpDeny {
		writeJSON(w, http.StatusBadRequest, errorBody("action must be allow or deny"))
		return
	}
	if len(req.Note) > maxNoteLen {
		writeJSON(w, http.StatusBadRequest, errorBody("note too long"))
		return
	}
	var out dlpRuleDTO
	created := false
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(dlpRuleKind)
		if err != nil {
			return err
		}
		fields := model.Record{colClass: class, colAction: action, colNote: req.Note, colCreatedBy: mc.Principal.Actor()}
		existing, ok, err := findRuleByClass(r.Context(), sc, class)
		if err != nil {
			return err
		}
		if ok {
			for k, v := range fields {
				existing[k] = v
			}
			updated, err := repo.Update(r.Context(), existing)
			if err != nil {
				return err
			}
			out = toDLPRuleDTO(updated)
		} else {
			rec, err := repo.Create(r.Context(), fields)
			if err != nil {
				return err
			}
			created = true
			out = toDLPRuleDTO(rec)
		}
		return auditEvent(r.Context(), sc, mc, "inferenceproxy.dlp.put", dlpRuleKind, model.ID(out.ID),
			map[string]any{"class": class, "action": action})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, out)
}

// handleDeleteDLPRule removes one DLP rule by id (admin-tier; self-audited).
func (m *Module) handleDeleteDLPRule(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if !requireAAL3(w, mc) {
		return
	}
	id := model.ID(strings.TrimSpace(chi.URLParam(r, "id")))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	found := false
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(dlpRuleKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil
			}
			return err
		}
		if err := repo.Delete(r.Context(), id); err != nil {
			return err
		}
		found = true
		return auditEvent(r.Context(), sc, mc, "inferenceproxy.dlp.delete", dlpRuleKind, id,
			map[string]any{"class": rec.String(colClass)})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// ---------------------------------------------------------------------------
// store / json helpers (each module owns a tiny copy — the core render helpers
// are unexported; mirrors modules/sourcescope/dto.go).
// ---------------------------------------------------------------------------

func findRuleByClass(ctx context.Context, sc store.Scope, class string) (model.Record, bool, error) {
	repo, err := sc.Ext(dlpRuleKind)
	if err != nil {
		return nil, false, err
	}
	recs, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{eq(colClass, class)}, Limit: 1})
	if err != nil {
		return nil, false, err
	}
	if len(recs) == 0 {
		return nil, false, nil
	}
	return recs[0], true, nil
}

// allRules pages the DLP rule table to completion (each page capped at listCap) so a
// policy load is never silently truncated. The rule set is small (one row per class).
func allRules(ctx context.Context, sc store.Scope) ([]model.Record, error) {
	repo, err := sc.Ext(dlpRuleKind)
	if err != nil {
		return nil, err
	}
	var out []model.Record
	q := model.Query{Limit: listCap}
	for {
		recs, page, err := repo.List(ctx, q)
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
		if !page.HasMore || page.Cursor == "" {
			return out, nil
		}
		q.Cursor = page.Cursor
	}
}

func auditEvent(ctx context.Context, sc store.Scope, mc api.ModuleContext, action string, kind model.Kind, id model.ID, meta map[string]any) error {
	_, err := sc.Audit().Append(ctx, model.AuditDraft{
		Actor: mc.Principal.Actor(), ActorKind: mc.Principal.ActorKind(),
		Action: action, TargetKind: kind, TargetID: id, Meta: meta,
	})
	return err
}

// listResponse is the paginated envelope every list endpoint returns: the ONE
// engine-wide shape (items + opaque cursor + has_more), aliased rather than
// re-declared so an empty page can never serialize as `{"items":null}` here
// while it serializes as `{"items":[]}` next door (core/api/listresponse.go).
type listResponse[T any] = api.ListResponse[T]

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func errorBody(msg string) map[string]any {
	return map[string]any{"error": map[string]string{"message": msg}}
}

// requireAAL3 is the second, assurance-only gate on proxy governance writes. Module
// routing applies RBAC but has no implicit assurance floor, so every handler that can
// relax an inference gate, authorize egress, or approve a device calls this before it
// decodes or mutates anything. Tokens carry AAL0 and therefore fail closed too.
func requireAAL3(w http.ResponseWriter, mc api.ModuleContext) bool {
	if mc.Principal.AAL < auth.AAL3 {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]string{
			"code":    "step_up_required",
			"message": "this inference-proxy governance write requires a hardware-verified (AAL3) session; complete the WebAuthn/PIV step-up and retry",
		}})
		return false
	}
	return true
}

// writeStoreError maps a store error to an HTTP status. THE MAPPING ITSELF IS NOT
// HERE: it is api.StoreErrorStatus (core/api/moduleerrors.go), which derives the
// status from the same statusFor that answers core/api's own routes. This module
// therefore cannot answer a sentinel differently from core, or from the other
// thirty-five copies of this function, and a sentinel added to statusFor tomorrow
// reaches this module without anyone editing it.
//
// That is not hypothetical: on 2026-08-12 four sentinels core/api had long mapped —
// tenant_suspended, tenant_not_in_service, not_leader and residency_violation —
// were absent from all but two of the thirty-six copies, so the same refusal was
// answered 423/503/403 by a core route and 500 "internal error" by every module
// route. The per-arm reasoning (ADR-0024 Q2 for the audit spool/B-03 for
// workspace confinement for the standby) now lives beside statusFor, once.
func writeStoreError(w http.ResponseWriter, err error) {
	if err == nil {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	status, msg, _ := api.StoreErrorStatus(err)
	writeJSON(w, status, errorBody(msg))
}

func listQuery(r *http.Request) model.Query {
	q := model.Query{}
	if c := r.URL.Query().Get("cursor"); c != "" {
		q.Cursor = c
	}
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			q.Limit = n
		}
	}
	return q
}

func eq(col, val string) model.Filter {
	return model.Filter{Column: col, Op: model.OpEq, Value: val}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid JSON body"))
		return false
	}
	// A BODY IS ONE JSON DOCUMENT (2026-08-06). Decode reads the FIRST value and stops,
	// so `{...}{...}` used to decode the first, silently discard the rest and perform a
	// durable mutation returning 201. Measured against a live engine on the models route,
	// with the created row read back by a separate GET; core/api/render.go has rejected
	// this since it was written, and 21 of the 22 copies of this helper had drifted from
	// it. A concatenation error becomes an apparently correct action, and two layers can
	// disagree about which document the request meant.
	if dec.More() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid JSON body"))
		return false
	}
	return true
}
