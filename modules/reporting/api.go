// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package reporting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

var (
	permReportRead  auth.Permission = "reporting:report:read"
	permReportWrite auth.Permission = "reporting:report:write"
)

func (m *Module) APIRoutes(reg api.RouteRegistrar) {
	reg.Handle("GET", "/reports", permReportRead, m.handleListReports)
	reg.Handle("GET", "/reports/{type}", permReportRead, m.handleGenerateReport)
	// the enterprise report engine + schedules/branding/templates.
	// Every route answers 501 until its seam is wired (enterprise build).
	m.registerEnterpriseRoutes(reg)
}

// reportTypes is the catalog of available reports.
var reportTypes = []ReportMeta{
	{
		Type:        ReportComplianceEvidence,
		Title:       "Compliance Evidence Report",
		Description: "Compliance posture by framework with per-control status and evidence.",
		Formats:     []Format{FormatHTML, FormatPDF},
	},
	{
		Type:        ReportAuditSummary,
		Title:       "Audit Summary Report",
		Description: "Audit event summary with ledger integrity verification.",
		Formats:     []Format{FormatHTML, FormatPDF},
	},
	{
		Type:        ReportFinOps,
		Title:       "FinOps Report",
		Description: "AI spend breakdown by model, provider and team with budget status.",
		Formats:     []Format{FormatHTML, FormatPDF},
	},
	{
		Type:        ReportAccessReview,
		Title:       "Access Review Report",
		Description: "User access review with roles, permissions and last access for SOX/SOC2.",
		Formats:     []Format{FormatHTML, FormatPDF},
	},
	{
		Type:        ReportExecutiveSummary,
		Title:       "Executive Summary Report",
		Description: "One-page overview of governance posture, risk, cost and adoption.",
		Formats:     []Format{FormatHTML, FormatPDF},
	},
}

func (m *Module) handleListReports(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusOK, map[string]any{"items": reportTypes})
}

func (m *Module) handleGenerateReport(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	reportType := ReportType(chi.URLParam(r, "type"))
	if !validReportType(reportType) {
		writeError(w, http.StatusNotFound, "unknown report type")
		return
	}

	params := parseReportParams(r, reportType)
	if params.Format == FormatPDF && !PDFAvailable() {
		writeError(w, http.StatusNotImplemented, ErrPDFUnavailable.Error())
		return
	}

	ck := cacheKey(string(mc.Tenant), string(params.Type), string(params.Format),
		params.From.Format(time.RFC3339), params.To.Format(time.RFC3339),
		params.Framework+"|"+params.Team+"|"+params.Locale)

	if cached, ok := m.cache.Get(ck); ok {
		// A WARM CACHE MUST NOT OUTLIVE THE TENANT'S SERVICE. This entry lives an
		// hour by default and is invalidated only by TTL, error or shutdown — never
		// by a change of service state. Because the hit is answered straight from
		// memory, it never touches the store, and the store is where withdrawal of
		// service is enforced (core/suspension). So a tenant whose service had been
		// withdrawn kept being served its compliance reports for up to an hour, and
		// a report is the product.
		//
		// Re-enter the guarded store for the same verdict a cold request would get.
		// It costs one indexed read on a cache hit, on an endpoint that renders a
		// report — not a hot path.
		if mc.Data == nil {
			// Fail closed. "I could not establish service state" is never "service is
			// fine" — that confusion is the one exists to end, and here it would
			// hand a withdrawn tenant the product straight from memory.
			m.log.Error("report cache service-state check: no data handle on the module context", "type", reportType)
			writeError(w, http.StatusInternalServerError, "cannot establish tenant service state")
			return
		}
		if err := mc.Data.View(r.Context(), func(store.Scope) error { return nil }); err != nil {
			// The three arms that used to be spelled out here are the shared mapping's
			// now, wording included; what this path had right, it keeps.
			m.reportErr(w, err, "report cache service-state check", "failed to gather report data")
			return
		}
		writeReport(w, cached, params.Format)
		return
	}

	data, err := m.gatherData(r.Context(), mc, params)
	if err != nil {
		// THE COLD PATH. This is where a withdrawn tenant used to be answered 500 while
		// the warm-cache branch above answered it 423 — same tenant, same route, the
		// answer decided by whether an entry happened to be in memory.
		m.reportErr(w, err, "gather report data", "failed to gather report data")
		return
	}

	branding := BrandingConfig{}
	if m.branding != nil {
		if b, err := m.branding.GetBranding(r.Context(), mc.Tenant); err == nil {
			branding = b
		}
	}

	html, err := m.renderWithCustomTemplate(r.Context(), mc.Tenant, reportType, data, params.Locale, branding)
	if err != nil {
		m.log.Error("render report", "type", reportType, "err", err)
		writeError(w, http.StatusInternalServerError, "failed to render report")
		return
	}

	var output []byte
	if params.Format == FormatPDF {
		pdf, err := RenderPDF(r.Context(), html)
		if err != nil {
			if errors.Is(err, ErrPDFUnavailable) {
				writeError(w, http.StatusNotImplemented, err.Error())
				return
			}
			m.log.Error("render pdf", "type", reportType, "err", err)
			writeError(w, http.StatusInternalServerError, "failed to generate PDF")
			return
		}
		output = pdf
	} else {
		output = html
	}

	m.cache.Put(ck, output)
	writeReport(w, output, params.Format)
}

func (m *Module) gatherData(ctx context.Context, mc api.ModuleContext, params ReportParams) (any, error) {
	switch params.Type {
	case ReportComplianceEvidence:
		if m.compliance == nil {
			return ComplianceData{Generated: time.Now(), Disclaimer: "Data source not configured."}, nil
		}
		return m.compliance.GatherComplianceData(ctx, mc.Tenant, params.Framework)
	case ReportAuditSummary:
		return m.gatherAuditData(ctx, mc.Data, params)
	case ReportFinOps:
		return m.gatherFinOpsData(ctx, mc.Data, params)
	case ReportAccessReview:
		return m.gatherAccessData(ctx, mc.Data)
	case ReportExecutiveSummary:
		return m.gatherExecutiveData(ctx, mc, params)
	default:
		return nil, fmt.Errorf("unsupported report type: %s", params.Type)
	}
}

func (m *Module) gatherAuditData(ctx context.Context, sd api.ScopedData, params ReportParams) (AuditData, error) {
	d := AuditData{From: params.From, To: params.To}
	err := sd.View(ctx, func(sc store.Scope) error {
		head, ok, herr := sc.Audit().Head(ctx)
		if herr != nil {
			return herr
		}
		if ok {
			d.LedgerHead = head.Seq
		}
		rep, verr := sc.Audit().Verify(ctx, 0)
		if verr != nil {
			return verr
		}
		d.CheckpointOK = rep.OK
		d.CheckpointCount = int(rep.Checked)
		d.FirstBadSeq = rep.BreakAt
		d.CheckpointReason = rep.Reason

		actionCounts := make(map[string]int64)
		werr := sc.Audit().Walk(ctx, 1, func(ev model.AuditEvent) error {
			ts := ev.OccurredAt.Time()
			if (!params.From.IsZero() && ts.Before(params.From)) || (!params.To.IsZero() && ts.After(params.To)) {
				return nil
			}
			d.TotalEvents++
			actionCounts[ev.Action]++
			return nil
		})
		if werr != nil {
			return werr
		}
		for action, count := range actionCounts {
			d.EventsByAction = append(d.EventsByAction, ActionCount{Action: action, Count: count})
		}
		return nil
	})
	return d, err
}

func (m *Module) gatherFinOpsData(ctx context.Context, sd api.ScopedData, params ReportParams) (FinOpsData, error) {
	d := FinOpsData{From: params.From, To: params.To}
	err := sd.View(ctx, func(sc store.Scope) error {
		costs, _, cerr := sc.Costs().List(ctx, model.Query{Limit: 10000})
		if cerr != nil {
			return cerr
		}
		modelSpend := make(map[string]*SpendBucket)
		providerSpend := make(map[string]*SpendBucket)
		for _, c := range costs {
			ts := c.OccurredAt.Time()
			if (!params.From.IsZero() && ts.Before(params.From)) || (!params.To.IsZero() && ts.After(params.To)) {
				continue
			}
			d.TotalMicroUSD += c.CostMicroUSD
			d.InputTokens += c.InputTokens
			d.OutputTokens += c.OutputTokens
			d.Samples++
			accumBucket(modelSpend, string(c.ModelID), c.CostMicroUSD, c.InputTokens, c.OutputTokens)
			accumBucket(providerSpend, string(c.ProviderID), c.CostMicroUSD, c.InputTokens, c.OutputTokens)
		}
		d.ByModel = toBucketSlice(modelSpend)
		d.ByProvider = toBucketSlice(providerSpend)
		return nil
	})
	return d, err
}

func (m *Module) gatherAccessData(ctx context.Context, sd api.ScopedData) (AccessData, error) {
	d := AccessData{Generated: time.Now().UTC()}
	err := sd.View(ctx, func(sc store.Scope) error {
		identities, _, ierr := sc.Identities().List(ctx, model.Query{Limit: 10000})
		if ierr != nil {
			return ierr
		}
		for _, id := range identities {
			d.Users = append(d.Users, UserAccess{
				UserID:      string(id.ID),
				DisplayName: id.Name,
			})
		}
		return nil
	})
	return d, err
}

func (m *Module) gatherExecutiveData(ctx context.Context, mc api.ModuleContext, params ReportParams) (ExecutiveData, error) {
	d := ExecutiveData{Generated: time.Now().UTC(), From: params.From, To: params.To}

	if m.compliance != nil {
		cd, err := m.compliance.GatherComplianceData(ctx, mc.Tenant, "")
		if err == nil {
			for _, fw := range cd.Frameworks {
				pct := 0
				if fw.Summary.Total > 0 {
					pct = (fw.Summary.Satisfied + fw.Summary.ByDesign) * 100 / fw.Summary.Total
				}
				d.ComplianceSummary = append(d.ComplianceSummary, FrameworkBrief{
					Name:         fw.Name,
					SatisfiedPct: pct,
					GapCount:     fw.Summary.Gap,
				})
			}
		}
	}

	_ = mc.Data.View(ctx, func(sc store.Scope) error {
		agents, _, _ := sc.Agents().List(ctx, model.Query{Limit: 10000})
		d.ActiveAgents = int64(len(agents))
		sessions, _, _ := sc.Sessions().List(ctx, model.Query{Limit: 10000})
		d.ActiveSessions = int64(len(sessions))
		identities, _, _ := sc.Identities().List(ctx, model.Query{Limit: 10000})
		d.ActiveUsers = int64(len(identities))
		findings, _, _ := sc.Findings().List(ctx, model.Query{Limit: 10000})
		for _, f := range findings {
			if f.Status == model.FindingOpen || f.Status == "" {
				d.FindingsOpen++
				if f.Severity == model.SeverityCritical {
					d.FindingsCritical++
				}
			}
		}
		costs, _, _ := sc.Costs().List(ctx, model.Query{Limit: 10000})
		for _, c := range costs {
			ts := c.CreatedAt.Time()
			if (!params.From.IsZero() && ts.Before(params.From)) || (!params.To.IsZero() && ts.After(params.To)) {
				continue
			}
			d.TotalSpendUSD += float64(c.CostMicroUSD) / 1_000_000
		}
		rep, verr := sc.Audit().Verify(ctx, 0)
		if verr == nil {
			d.AuditIntegrityOK = rep.OK
		}
		return nil
	})
	return d, nil
}

func accumBucket(m map[string]*SpendBucket, key string, cost, in, out int64) {
	if key == "" {
		key = "(unknown)"
	}
	b, ok := m[key]
	if !ok {
		b = &SpendBucket{Key: key}
		m[key] = b
	}
	b.CostMicroUSD += cost
	b.InputTokens += in
	b.OutputTokens += out
	b.Samples++
}

func toBucketSlice(m map[string]*SpendBucket) []SpendBucket {
	s := make([]SpendBucket, 0, len(m))
	for _, b := range m {
		s = append(s, *b)
	}
	return s
}

func parseReportParams(r *http.Request, rt ReportType) ReportParams {
	q := r.URL.Query()
	format := Format(q.Get("format"))
	if format != FormatPDF {
		format = FormatHTML
	}
	locale := q.Get("locale")
	if locale == "" {
		locale = "en"
	}
	now := time.Now().UTC()
	from := parseTime(q.Get("from"), now.AddDate(0, -1, 0))
	to := parseTime(q.Get("to"), now)
	return ReportParams{
		Type:      rt,
		Format:    format,
		From:      from,
		To:        to,
		Framework: q.Get("framework"),
		Team:      q.Get("team"),
		Locale:    locale,
	}
}

func parseTime(s string, fallback time.Time) time.Time {
	if s == "" {
		return fallback
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t, err = time.Parse("2006-01-02", s)
		if err != nil {
			return fallback
		}
	}
	return t
}

func validReportType(rt ReportType) bool {
	switch rt {
	case ReportComplianceEvidence, ReportAuditSummary, ReportFinOps, ReportAccessReview, ReportExecutiveSummary:
		return true
	}
	return false
}

func writeReport(w http.ResponseWriter, data []byte, format Format) {
	switch format {
	case FormatPDF:
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition", "attachment; filename=report.pdf")
	default:
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	}
	w.WriteHeader(http.StatusOK)
	// The status line is already on the wire, so a write failure here cannot change
	// the response; discarded explicitly rather than left to be read as an oversight.
	_, _ = w.Write(data)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": http.StatusText(status), "message": msg}})
}

// writeStoreError is this module's member of the product-wide error-mapper family,
// and it did not exist until 2026-08-12. Reporting classified inline, in one handler,
// and every other error path answered 500 — which had two measured consequences.
//
// THE ONE THAT REACHES A PAYING CUSTOMER. The enterprise report engine is wired
// behind an add-on gate in the closed overlay, so a lapsed entitlement returns
// license.ErrAddonRequiresLicense out of m.enterprise.*, and serveEnterpriseReport
// (enterprise.go) answered it 500 "failed to build the posture report". The operator
// is told their server is broken when in fact their license lapsed — the exact defect
// the addon_requires_license arm of core/api statusFor was written to prevent, one
// layer out. Nothing in the open tree constructs that error, so it cannot be shown
// end to end here; it is proven against the constructor in the tests beside this file.
//
// THE ONE VISIBLE FROM THE OPEN TREE. handleGenerateReport answered a withdrawn
// tenant 423 on a WARM cache (the service-state re-check above) and 500 on a COLD
// one, because the cold path let gatherData's error fall into the generic arm. Same
// tenant, same route, two answers, decided by cache warmth.
//
// The two local arms keep the wording reporting already put on the wire — the shared
// mapping agrees on both statuses and says "tenant suspended" / "residency violation"
// where these say something an operator can act on. Centralizing a mapping is not
// license to reword a response nothing in the tree tests.
func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrTenantSuspended), errors.Is(err, store.ErrTenantNotInService):
		writeError(w, http.StatusLocked, "tenant is not in service")
	case errors.Is(err, store.ErrResidencyViolation):
		writeError(w, http.StatusForbidden, "tenant is not resident in this region")
	default:
		status, msg, _ := api.StoreErrorStatus(err)
		writeError(w, status, msg)
	}
}

// reportErr answers err and decides, in ONE place, whether it was a fault worth a log
// line. A deliberate refusal must not be logged at ERROR next to real faults: that is
// how operators learn to ignore the log, and core/api writeError already draws the
// same line for the same reason. A genuine fault keeps its event and its call-site
// sentence, because "failed to render report" and "failed to build the risk summary"
// are not interchangeable.
func (m *Module) reportErr(w http.ResponseWriter, err error, event, fallback string) {
	if _, _, refused := api.StoreErrorStatus(err); refused {
		writeStoreError(w, err)
		return
	}
	m.log.Error(event, "err", err)
	writeError(w, http.StatusInternalServerError, fallback)
}
