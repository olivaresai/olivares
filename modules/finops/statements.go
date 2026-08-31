// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// statements.go implements periodic chargeback statement generation,
// listing, detail retrieval and CSV export. A statement is a per-cost-center,
// per-period (monthly/weekly) snapshot with line items grouped by
// (model, provider, agent) — the artifact finance consumes for internal billing.

// --- DTOs -------------------------------------------------------------------

type generateStatementRequest struct {
	Period      string `json:"period"`
	PeriodStart string `json:"period_start"`
}

type statementDTO struct {
	ID                       string             `json:"id"`
	CostCenterID             string             `json:"cost_center_id"`
	CostCenterCode           string             `json:"cost_center_code"`
	CostCenterName           string             `json:"cost_center_name"`
	Period                   string             `json:"period"`
	PeriodStart              string             `json:"period_start"`
	PeriodEnd                string             `json:"period_end"`
	TotalMicroUSD            int64              `json:"total_micro_usd"`
	LineCount                int                `json:"line_count"`
	PriorPeriodTotalMicroUSD int64              `json:"prior_period_total_micro_usd"`
	DeltaPct                 int                `json:"delta_pct"`
	Status                   string             `json:"status"`
	GeneratedAt              string             `json:"generated_at"`
	Lines                    []statementLineDTO `json:"lines,omitempty"`
	CreatedAt                string             `json:"created_at,omitempty"`
	UpdatedAt                string             `json:"updated_at,omitempty"`
}

type statementLineDTO struct {
	ID           string `json:"id"`
	StatementID  string `json:"statement_id"`
	ModelRef     string `json:"model_ref"`
	ProviderRef  string `json:"provider_ref"`
	AgentRef     string `json:"agent_ref,omitempty"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	CostMicroUSD int64  `json:"cost_micro_usd"`
	SampleCount  int    `json:"sample_count"`
}

func toStatementDTO(rec model.Record) statementDTO {
	return statementDTO{
		ID:                       rec.String(model.ColID),
		CostCenterID:             rec.String(colStmtCostCenterID),
		CostCenterCode:           rec.String(colStmtCostCenterCode),
		CostCenterName:           rec.String(colStmtCostCenterName),
		Period:                   rec.String(colStmtPeriod),
		PeriodStart:              rec.String(colStmtPeriodStart),
		PeriodEnd:                rec.String(colStmtPeriodEnd),
		TotalMicroUSD:            rec.Int(colStmtTotalMicroUSD),
		LineCount:                int(rec.Int(colStmtLineCount)),
		PriorPeriodTotalMicroUSD: rec.Int(colStmtPriorTotal),
		DeltaPct:                 int(rec.Int(colStmtDeltaPct)),
		Status:                   rec.String(colStmtStatus),
		GeneratedAt:              rec.String(colStmtGeneratedAt),
		CreatedAt:                rec.String(model.ColCreatedAt),
		UpdatedAt:                rec.String(model.ColUpdatedAt),
	}
}

func toStatementLineDTO(rec model.Record) statementLineDTO {
	return statementLineDTO{
		ID:           rec.String(model.ColID),
		StatementID:  rec.String(colLineStatementID),
		ModelRef:     rec.String(colLineModelRef),
		ProviderRef:  rec.String(colLineProviderRef),
		AgentRef:     rec.String(colLineAgentRef),
		InputTokens:  rec.Int(colLineInputTokens),
		OutputTokens: rec.Int(colLineOutputTokens),
		CostMicroUSD: rec.Int(colLineCostMicroUSD),
		SampleCount:  int(rec.Int(colLineSampleCount)),
	}
}

// --- Statement generation ---------------------------------------------------

type lineKey struct {
	Model    string
	Provider string
	Agent    string
}

type lineAgg struct {
	input, output, cost int64
	count               int
}

func stmtPeriodEnd(period string, start time.Time) time.Time {
	switch period {
	case "weekly":
		return start.AddDate(0, 0, 7)
	default: // monthly
		return start.AddDate(0, 1, 0)
	}
}

func stmtPriorPeriodStart(period string, start time.Time) time.Time {
	switch period {
	case "weekly":
		return start.AddDate(0, 0, -7)
	default:
		return start.AddDate(0, -1, 0)
	}
}

func generateStatements(ctx context.Context, sc store.Scope, period string, pStart, pEnd, now time.Time) ([]statementDTO, error) {
	ccRepo, err := sc.Ext(costCenterKind)
	if err != nil {
		return nil, err
	}
	ccs, _, err := ccRepo.List(ctx, model.Query{
		Filters: []model.Filter{eq(colCCStatus, "active")},
		Limit:   listCap,
	})
	if err != nil {
		return nil, err
	}
	if len(ccs) == 0 {
		return []statementDTO{}, nil
	}

	pStartStr := model.NewTimestamp(pStart).String()
	pEndStr := model.NewTimestamp(pEnd).String()

	stmtRepo, err := sc.Ext(chargebackStatementKind)
	if err != nil {
		return nil, err
	}
	lineRepo, err := sc.Ext(statementLineKind)
	if err != nil {
		return nil, err
	}

	var out []statementDTO
	for _, cc := range ccs {
		ccCode := cc.String(colCCCode)
		ccName := cc.String(colCCName)
		ccID := cc.String(model.ColID)
		// ⛔ LA CLAVE LLEVA EL PERIODO, y omitirlo NO era cosmético: `(tenant_id, statement_key)` es
		// ÚNICO (`schema.go:543`), así que un extracto semanal y uno mensual que empiezan el mismo día
		// —el 1 de mes que cae en lunes, ~1 de cada 7— colisionaban, y el segundo se descartaba por la
		// rama `ErrConflict` de abajo: **sin extracto y sin error**. Y aunque no colisionasen, el
		// `priorKey` de una semana que arranca un día 1 encontraba el extracto MENSUAL y calculaba el
		// delta contra el total del MES. Medido por un panel adversarial interno el 2026-08-18:
		// `weekly(2026-11-08): total=700 delta_pct=-7500 prior=2800` — 2800 era el mes.
		//
		// La clave sigue siendo una cadena compuesta con `|` y el código de centro es texto libre
		// (`costcenter.go:74` sólo exige no vacío), así que la falta de ambigüedad **descansa en que
		// `period` viene de un conjunto CERRADO y no vacío** —`handleGenerateStatements` rechaza todo
		// lo que no sea "monthly" o "weekly"— y en que el arranque es una marca de tiempo con formato.
		// Si algún día se admite un periodo libre, esta composición deja de ser inequívoca.
		stmtKey := ccCode + "|" + period + "|" + pStartStr

		// Aggregate cost_sample rows for this CC in the period.
		lines := map[lineKey]*lineAgg{}
		_, scanErr := scanSamples(ctx, sc, []model.Filter{
			estimatedFilter(),
			eq(colCostCenterRef, ccCode),
			{Column: colOccurredAt, Op: model.OpGte, Value: pStartStr},
			{Column: colOccurredAt, Op: model.OpLt, Value: pEndStr},
		}, func(r model.Record) {
			k := lineKey{
				Model:    r.String(colModelRef),
				Provider: r.String(colProviderRef),
				Agent:    r.String(colAgentRef),
			}
			a := lines[k]
			if a == nil {
				a = &lineAgg{}
				lines[k] = a
			}
			a.input += r.Int(colInputTokens)
			a.output += r.Int(colOutputTokens)
			a.cost += r.Int(colCostMicroUSD)
			a.count++
		})
		if scanErr != nil {
			return nil, scanErr
		}

		var total int64
		for _, a := range lines {
			total += a.cost
		}

		// Look up prior period statement for delta.
		priorKey := ccCode + "|" + period + "|" + model.NewTimestamp(stmtPriorPeriodStart(period, pStart)).String()
		priorTotal := int64(0)
		priors, _, _ := stmtRepo.List(ctx, model.Query{
			Filters: []model.Filter{eq(colStmtKey, priorKey)},
			Limit:   1,
		})
		if len(priors) > 0 {
			priorTotal = priors[0].Int(colStmtTotalMicroUSD)
		}

		deltaPct := 0
		if priorTotal > 0 {
			deltaPct = int((total - priorTotal) * 10000 / priorTotal)
		}

		// Create statement (idempotent: skip on conflict).
		stmtRec, createErr := stmtRepo.Create(ctx, model.Record{
			colStmtKey:            stmtKey,
			colStmtCostCenterID:   ccID,
			colStmtCostCenterCode: ccCode,
			colStmtCostCenterName: ccName,
			colStmtPeriod:         period,
			colStmtPeriodStart:    pStartStr,
			colStmtPeriodEnd:      pEndStr,
			colStmtTotalMicroUSD:  total,
			colStmtLineCount:      int64(len(lines)),
			colStmtPriorTotal:     priorTotal,
			colStmtDeltaPct:       int64(deltaPct),
			colStmtStatus:         "draft",
			colStmtGeneratedAt:    model.NewTimestamp(now).String(),
		})
		if createErr != nil {
			if errors.Is(createErr, store.ErrConflict) {
				continue
			}
			return nil, createErr
		}

		stmtID := stmtRec.String(model.ColID)

		// Create line items.
		sortedLines := make([]lineKey, 0, len(lines))
		for k := range lines {
			sortedLines = append(sortedLines, k)
		}
		sort.Slice(sortedLines, func(i, j int) bool {
			li, lj := lines[sortedLines[i]], lines[sortedLines[j]]
			return li.cost > lj.cost
		})

		var lineDTOs []statementLineDTO
		for _, k := range sortedLines {
			a := lines[k]
			lineRec, lineErr := lineRepo.Create(ctx, model.Record{
				colLineStatementID:  stmtID,
				colLineModelRef:     k.Model,
				colLineProviderRef:  k.Provider,
				colLineAgentRef:     k.Agent,
				colLineInputTokens:  a.input,
				colLineOutputTokens: a.output,
				colLineCostMicroUSD: a.cost,
				colLineSampleCount:  int64(a.count),
			})
			if lineErr != nil {
				return nil, lineErr
			}
			lineDTOs = append(lineDTOs, toStatementLineDTO(lineRec))
		}

		dto := toStatementDTO(stmtRec)
		dto.Lines = lineDTOs
		out = append(out, dto)
	}
	return out, nil
}

// --- HTTP Handlers ----------------------------------------------------------

func (m *Module) handleGenerateStatements(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in generateStatementRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Period != "monthly" && in.Period != "weekly" {
		writeJSON(w, http.StatusBadRequest, errorBody("period must be monthly or weekly"))
		return
	}
	pStart, err := time.Parse(time.RFC3339, in.PeriodStart)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("period_start must be valid RFC3339"))
		return
	}
	pEnd := stmtPeriodEnd(in.Period, pStart)
	now := m.clock.Now().Time()

	var out []statementDTO
	mutErr := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		var e error
		out, e = generateStatements(r.Context(), sc, in.Period, pStart, pEnd, now)
		return e
	})
	if mutErr != nil {
		writeStoreError(w, mutErr)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"statements": out})
}

func (m *Module) handleListStatements(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if ccID := r.URL.Query().Get("cost_center_id"); ccID != "" {
		q.Filters = append(q.Filters, eq(colStmtCostCenterID, ccID))
	}
	if period := r.URL.Query().Get("period"); period != "" {
		q.Filters = append(q.Filters, eq(colStmtPeriod, period))
	}
	if status := r.URL.Query().Get("status"); status != "" {
		q.Filters = append(q.Filters, eq(colStmtStatus, status))
	}
	q.Sort = []model.Sort{{Column: colStmtPeriodStart, Desc: true}}
	out := listResponse[statementDTO]{Items: []statementDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(chargebackStatementKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toStatementDTO(rec))
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

func (m *Module) handleGetStatement(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var out statementDTO
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		stmtRepo, err := sc.Ext(chargebackStatementKind)
		if err != nil {
			return err
		}
		rec, err := stmtRepo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		out = toStatementDTO(rec)

		lineRepo, err := sc.Ext(statementLineKind)
		if err != nil {
			return err
		}
		lineRecs, _, err := lineRepo.List(r.Context(), model.Query{
			Filters: []model.Filter{eq(colLineStatementID, id.String())},
			Limit:   listCap,
		})
		if err != nil {
			return err
		}
		out.Lines = make([]statementLineDTO, 0, len(lineRecs))
		for _, lr := range lineRecs {
			out.Lines = append(out.Lines, toStatementLineDTO(lr))
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (m *Module) handleExportStatement(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var stmt statementDTO
	var lines []statementLineDTO
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		stmtRepo, err := sc.Ext(chargebackStatementKind)
		if err != nil {
			return err
		}
		rec, err := stmtRepo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		stmt = toStatementDTO(rec)
		lineRepo, err := sc.Ext(statementLineKind)
		if err != nil {
			return err
		}
		lineRecs, _, err := lineRepo.List(r.Context(), model.Query{
			Filters: []model.Filter{eq(colLineStatementID, id.String())},
			Limit:   listCap,
		})
		if err != nil {
			return err
		}
		for _, lr := range lineRecs {
			lines = append(lines, toStatementLineDTO(lr))
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}

	filename := fmt.Sprintf("chargeback_%s_%s.csv", stmt.CostCenterCode, stmt.PeriodStart[:10])
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")

	cw := csv.NewWriter(w)
	_ = cw.Write([]string{
		"cost_center_code", "cost_center_name", "model", "provider", "agent",
		"input_tokens", "output_tokens", "cost_micro_usd", "sample_count",
	})
	for _, l := range lines {
		_ = cw.Write([]string{
			stmt.CostCenterCode, stmt.CostCenterName,
			l.ModelRef, l.ProviderRef, l.AgentRef,
			strconv.FormatInt(l.InputTokens, 10),
			strconv.FormatInt(l.OutputTokens, 10),
			strconv.FormatInt(l.CostMicroUSD, 10),
			strconv.Itoa(l.SampleCount),
		})
	}
	cw.Flush()
}
