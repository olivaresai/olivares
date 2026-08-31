// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package voice

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// telemetryOutcome carries the post-commit signals a telemetry fold produced, so the
// findings are emitted on the bus AFTER the transaction commits.
type telemetryOutcome struct {
	dto           sessionDTO
	policyMiss    bool // first telemetry for a session no policy allows
	latencyCross  bool // latency just crossed the matched policy's SLA bound
	agentRef      string
	modelRef      string
	providerRef   string
	latencyMS     int64
	maxLatencyBnd int64
}

// onTelemetry folds one voice-telemetry sample into the session's METADATA (turn
// counts, latency aggregates, duration, language, last activity) and surfaces voice
// governance findings. It stores NO audio or transcript text: a transcript locator
// is hashed; any field outside the allow-list was already dropped by parseTelemetry.
func (m *Module) onTelemetry(ctx context.Context, tenantRef string, vt Telemetry) error {
	tenant, ok := tenantOf(tenantRef)
	if !ok {
		return nil
	}
	at := nonZeroTime(parseTSOrZero(vt.OccurredAt), m.clock)

	var out telemetryOutcome
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		rec, isNew, prevMax, err := m.upsertSession(ctx, sc, vt, at)
		if err != nil {
			return err
		}
		agent := rec.String(colAgentRef)
		mdl := rec.String(colModelRef)
		prov := rec.String(colProviderRef)
		out = telemetryOutcome{dto: m.toSessionDTO(rec), agentRef: agent, modelRef: mdl, providerRef: prov, latencyMS: vt.LatencyMS}

		policy, matched, err := m.matchPolicy(ctx, sc, agent, mdl, prov)
		if err != nil {
			return err
		}
		// Policy violation is emitted once, on the first telemetry of a session that
		// no policy allows (an un-permitted voice interface in use).
		if !matched && isNew {
			out.policyMiss = true
			detail := fmt.Sprintf("session:%s agent:%s model:%s provider:%s not permitted by any voice policy", vt.SessionRef, agent, mdl, prov)
			if err := m.persistFinding(ctx, sc, finding{
				kind: busPolicyViolation, severity: sdkmodel.SeverityMedium, subjectKind: "agent", subjectRef: agent,
				title: "voice session in use without an allowing policy", detail: detail,
				meta: map[string]any{"session_ref": clamp(vt.SessionRef, maxRefLen)},
			}); err != nil {
				return err
			}
		}
		// Latency-degraded is emitted once per crossing of the matched policy's SLA.
		if matched {
			bound := policy.Int(colMaxLatencyMS)
			out.maxLatencyBnd = bound
			newMax := prevMax
			if vt.LatencyMS > newMax {
				newMax = vt.LatencyMS
			}
			if bound > 0 && newMax > bound && prevMax <= bound {
				out.latencyCross = true
				detail := fmt.Sprintf("session:%s latency %dms exceeds policy SLA %dms", vt.SessionRef, newMax, bound)
				if err := m.persistFinding(ctx, sc, finding{
					kind: busLatencyDegraded, severity: sdkmodel.SeverityLow, subjectKind: "agent", subjectRef: agent,
					title: "voice session latency exceeded the policy SLA", detail: detail,
					meta: map[string]any{"session_ref": clamp(vt.SessionRef, maxRefLen)},
				}); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	m.broker.publish(sessSnapshot{tenant: tenant, dto: out.dto})
	m.maybeReportUngovernedCall(ctx, tenant, vt.SessionRef, out.agentRef)
	if out.policyMiss {
		m.emitFinding(ctx, tenant, busPolicyViolation, sdkmodel.SeverityMedium, "agent", out.agentRef,
			"voice session in use without an allowing policy",
			fmt.Sprintf("agent:%s model:%s provider:%s", out.agentRef, out.modelRef, out.providerRef))
	}
	if out.latencyCross {
		m.emitFinding(ctx, tenant, busLatencyDegraded, sdkmodel.SeverityLow, "agent", out.agentRef,
			"voice session latency exceeded the policy SLA",
			fmt.Sprintf("latency %dms over SLA %dms", out.latencyMS, out.maxLatencyBnd))
	}
	return nil
}

// upsertSession find-or-creates the session row for a session_ref and folds the
// telemetry metadata into it, returning the record, whether it was newly created,
// and the latency-max BEFORE this fold (for once-per-crossing SLA detection).
func (m *Module) upsertSession(ctx context.Context, sc store.Scope, vt Telemetry, at time.Time) (model.Record, bool, int64, error) {
	repo, err := sc.Ext(sessionKind)
	if err != nil {
		return nil, false, 0, err
	}
	apply := func(rec model.Record) {
		setIf(rec, colAgentRef, clamp(vt.AgentRef, maxRefLen))
		setIf(rec, colModelRef, clamp(vt.ModelRef, maxRefLen))
		setIf(rec, colProviderRef, clamp(vt.ProviderRef, maxRefLen))
		setIf(rec, colLanguageCode, clamp(vt.LanguageCode, maxNameLen))
		setIf(rec, colClosedReason, clamp(vt.ClosedReason, maxNameLen))
		if vt.TranscriptLocatorRef != "" {
			rec[colTranscriptRef] = hashHex(vt.TranscriptLocatorRef) // hash of an EXTERNAL locator, never text
		}
		if vt.TurnDelta > 0 {
			switch vt.Role {
			case "user":
				rec[colUserTurns] = rec.Int(colUserTurns) + vt.TurnDelta
			case "agent":
				rec[colAgentTurns] = rec.Int(colAgentTurns) + vt.TurnDelta
			}
		}
		if vt.DurationMS > rec.Int(colDurationMS) {
			rec[colDurationMS] = vt.DurationMS
		}
		if vt.LatencyMS > 0 {
			rec[colLatencyCount] = rec.Int(colLatencyCount) + 1
			rec[colLatencySumMS] = rec.Int(colLatencySumMS) + vt.LatencyMS
			if vt.LatencyMS > rec.Int(colLatencyMaxMS) {
				rec[colLatencyMaxMS] = vt.LatencyMS
			}
		}
		advanceLast(rec, colLastEventAt, at)
	}

	if rec, ok, err := findOne(ctx, repo, eq(colSessionRef, clamp(vt.SessionRef, maxRefLen))); err != nil {
		return nil, false, 0, err
	} else if ok {
		prevMax := rec.Int(colLatencyMaxMS)
		apply(rec)
		updated, uerr := repo.Update(ctx, rec)
		return updated, false, prevMax, uerr
	}

	atTS := model.NewTimestamp(at).String()
	rec := model.Record{
		colSessionRef: clamp(vt.SessionRef, maxRefLen),
		colUserTurns:  int64(0), colAgentTurns: int64(0), colDurationMS: int64(0),
		colLatencyCount: int64(0), colLatencySumMS: int64(0), colLatencyMaxMS: int64(0),
		colGoverned: false, colFirstEventAt: atTS, colLastEventAt: atTS,
	}
	apply(rec)
	created, err := repo.Create(ctx, rec)
	if err == nil {
		return created, true, 0, nil
	}
	if errors.Is(err, store.ErrConflict) {
		if again, ok, lerr := findOne(ctx, repo, eq(colSessionRef, clamp(vt.SessionRef, maxRefLen))); lerr != nil {
			return nil, false, 0, lerr
		} else if ok {
			prevMax := again.Int(colLatencyMaxMS)
			apply(again)
			updated, uerr := repo.Update(ctx, again)
			return updated, false, prevMax, uerr
		}
	}
	return nil, false, 0, err
}

// handleListSessions lists voice sessions with read-time-derived state, metadata only.
func (m *Module) handleListSessions(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	out := listResponse[sessionDTO]{Items: []sessionDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(sessionKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, m.toSessionDTO(rec))
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

// handleGetSession returns one session's metadata by its reference.
func (m *Module) handleGetSession(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	ref := chi.URLParam(r, "ref")
	if ref == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("session ref required"))
		return
	}
	var dto sessionDTO
	found := false
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(sessionKind)
		if err != nil {
			return err
		}
		rec, ok, ferr := findOne(r.Context(), repo, eq(colSessionRef, ref))
		if ferr != nil {
			return ferr
		}
		if ok {
			dto = m.toSessionDTO(rec)
			found = true
		}
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
	writeJSON(w, http.StatusOK, dto)
}

// handleSessionDecisions lists the append-only open/close ledger for one session.
func (m *Module) handleSessionDecisions(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	ref := chi.URLParam(r, "ref")
	if ref == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("session ref required"))
		return
	}
	m.listDecisions(w, r, mc, nil, eq(colDecSessionRef, ref))
}

// handleDecisions lists the whole append-only open/close ledger for the tenant, newest
// first: the page limit keeps the most recent decisions, not the oldest ones.
func (m *Module) handleDecisions(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	// ⛔ EL ORDEN NO ES PRESENTACIÓN AQUÍ: DECIDE QUÉ FILAS SOBREVIVEN AL RECORTE. Sin
	// Sort el store ordena por `id ASC` (sqlstore/generic.go, orderClause) y los ids son
	// UUIDv7, ordenados por tiempo: la respuesta empezaba por las decisiones MÁS
	// ANTIGUAS. Con más filas que el límite, la página devuelta era la de las más viejas
	// — un operador que abriera el ledger de un tenant con historia veía una tabla llena
	// y CERO denegaciones recientes. Ordenar en el navegador no lo arregla: el recorte ya
	// se hizo sobre la porción equivocada.
	//
	// ⛔ Y ESTA PROSA VA DENTRO DE LA FUNCIÓN, NO EN SU DOC COMMENT. La descripción
	// publicada del endpoint se COMPONE del doc comment
	// (scripts/check-openapi-op-descriptions.sh), así que dejarla arriba metía este aviso
	// interno, en español, dentro de `openapi.beta.json` — un documento que se publica.
	//
	// La ruta POR SESIÓN se deja como estaba a propósito: ahí la lectura natural es la
	// cronología de esa sesión, y cambiarla movería una superficie que esta unidad no
	// mide.
	m.listDecisions(w, r, mc, []model.Sort{{Column: colOccurredAt, Desc: true}})
}

// listDecisions serves one page of the ledger. `sorts` is the ORDER BY the caller
// wants; se ignora si la petición trae cursor, porque un Sort personalizado desactiva
// el cursor keyset y el store RECHAZA los dos juntos (core/model/filter.go, doc de
// Page). Se pasa explícito y no se deduce de si hay filtros: deducirlo ataría el orden
// a un detalle —«la ruta del tenant no filtra»— que el día que alguien añada un filtro
// cambiaría el orden sin que nadie lo pidiera.
func (m *Module) listDecisions(w http.ResponseWriter, r *http.Request, mc api.ModuleContext, sorts []model.Sort, filters ...model.Filter) {
	q := listQuery(r)
	q.Filters = append(q.Filters, filters...)
	if q.Cursor == "" {
		q.Sort = sorts
	}
	out := listResponse[decisionDTO]{Items: []decisionDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(decisionKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toDecisionDTO(rec))
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

// parseTSOrZero parses an RFC3339 timestamp or returns the zero time.
func parseTSOrZero(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := model.ParseTimestamp(s); err == nil {
		return t.Time()
	}
	return time.Time{}
}
