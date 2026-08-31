// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package reporting

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
)

// enterprise.go is the OPEN half of the enterprise reporting surface: the
// seams the commercial add-on implements (the enterprise report engine and the
// three providers already declared in types.go) plus the HTTP routes that
// serve them. Every route answers 501 when its seam is nil — the community
// build keeps the five built-in on-demand reports byte-identically and never
// pretends an enterprise capability exists (no rug-pull, no theater).

// EnterpriseReportSource is the commercial report engine seam: live
// compliance posture, executive risk summary and the signed audit evidence
// bundle, synthesized from the control plane's own governance data. The results
// are JSON-marshalable DTOs owned by the implementation; the module serves them
// verbatim (they carry their own honest "source unavailable" section markers
// when an upstream source is not wired).
type EnterpriseReportSource interface {
	PostureReport(ctx context.Context, tenant model.TenantID) (any, error)
	RiskSummary(ctx context.Context, tenant model.TenantID) (any, error)
	EvidenceBundle(ctx context.Context, tenant model.TenantID) (any, error)
	// DueDigests reports the operator-configured digest reports due at now,
	// given the last completed run per cadence ("daily"/"weekly"/"monthly").
	// The schedule pump drives it; an empty config yields nothing.
	DueDigests(now time.Time, lastRun map[string]time.Time) []DigestDue
}

// DigestDue is one operator-cadence digest report that is due.
type DigestDue struct {
	// ReportType is the enterprise report kind: "posture" | "risk" | "bundle".
	ReportType string `json:"report_type"`
	// Cadence is the schedule tier: "daily" | "weekly" | "monthly".
	Cadence string `json:"cadence"`
}

// ScheduleRun is one recorded execution of a scheduled report. Output
// carries the rendered artifact for download; providers cap its size and prune
// old runs — the run history is an operational convenience, not an archive.
type ScheduleRun struct {
	ID         string `json:"id"`
	ScheduleID string `json:"schedule_id"`
	ReportType string `json:"report_type"`
	Format     string `json:"format"`
	RanAt      string `json:"ran_at"` // RFC 3339 UTC
	Status     string `json:"status"` // ok | failed
	Error      string `json:"error,omitempty"`
	Output     []byte `json:"output,omitempty"`
}

// WithEnterpriseReports wires the commercial report engine (nil = community:
// the /enterprise/* routes answer 501).
func WithEnterpriseReports(s EnterpriseReportSource) Option {
	return func(m *Module) { m.enterprise = s }
}

// SchedulerWired reports whether the scheduler seam is active (the
// composition root's schedule pump stays inert without it).
func (m *Module) SchedulerWired() bool { return m.scheduler != nil }

// maxTemplateBytes caps an uploaded custom template (defense in depth; a
// report template is prose + markup, never megabytes).
const maxTemplateBytes = 512 * 1024

// maxScheduleRunOutput caps a stored run artifact. Larger renders are recorded
// as failed with an explicit reason — never silently truncated.
const maxScheduleRunOutput = 2 * 1024 * 1024

// registerEnterpriseRoutes mounts the enterprise surface. Called from
// APIRoutes; every handler is 501 (honest not-implemented) when its seam is nil.
func (m *Module) registerEnterpriseRoutes(reg api.RouteRegistrar) {
	// The commercial report engine.
	reg.Handle("GET", "/enterprise/posture", permReportRead, m.handleEnterprisePosture)
	reg.Handle("GET", "/enterprise/risk", permReportRead, m.handleEnterpriseRisk)
	reg.Handle("GET", "/enterprise/bundle", permReportRead, m.handleEnterpriseBundle)

	// scheduled reports.
	reg.Handle("GET", "/schedules", permReportRead, m.handleListSchedules)
	reg.Handle("POST", "/schedules", permReportWrite, m.handleCreateSchedule)
	reg.Handle("DELETE", "/schedules/{id}", permReportWrite, m.handleDeleteSchedule)
	reg.Handle("GET", "/schedules/{id}/runs", permReportRead, m.handleListScheduleRuns)
	reg.Handle("GET", "/schedules/{id}/runs/{rid}", permReportRead, m.handleGetScheduleRun)

	// tenant branding.
	reg.Handle("GET", "/branding", permReportRead, m.handleGetBranding)
	reg.Handle("PUT", "/branding", permReportWrite, m.handleSetBranding)

	// custom templates.
	reg.Handle("GET", "/templates/{type}", permReportRead, m.handleGetTemplate)
	reg.Handle("PUT", "/templates/{type}", permReportWrite, m.handleSetTemplate)
	reg.Handle("DELETE", "/templates/{type}", permReportWrite, m.handleDeleteTemplate)
}

func writeNotWired(w http.ResponseWriter, what string) {
	writeError(w, http.StatusNotImplemented, what+" is an enterprise capability and is not wired in this build")
}

// ---- enterprise report engine routes -------------------------------------------

func (m *Module) handleEnterprisePosture(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.serveEnterpriseReport(w, r, mc, "posture report", func(ctx context.Context) (any, error) {
		return m.enterprise.PostureReport(ctx, mc.Tenant)
	})
}

func (m *Module) handleEnterpriseRisk(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.serveEnterpriseReport(w, r, mc, "risk summary", func(ctx context.Context) (any, error) {
		return m.enterprise.RiskSummary(ctx, mc.Tenant)
	})
}

func (m *Module) handleEnterpriseBundle(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.serveEnterpriseReport(w, r, mc, "evidence bundle", func(ctx context.Context) (any, error) {
		return m.enterprise.EvidenceBundle(ctx, mc.Tenant)
	})
}

func (m *Module) serveEnterpriseReport(w http.ResponseWriter, r *http.Request, _ api.ModuleContext, what string, build func(context.Context) (any, error)) {
	if m.enterprise == nil {
		writeNotWired(w, "the enterprise "+what)
		return
	}
	out, err := build(r.Context())
	if err != nil {
		// THE ROUTE THE ADD-ON GATE REFUSES. build() is a call on m.enterprise, and in
		// the closed overlay that engine is constructed behind an add-on gate — so a
		// lapsed entitlement arrives here as license.ErrAddonRequiresLicense and used
		// to leave as 500 "failed to build the <report>". A customer who has paid for
		// everything except this add-on was told the server was broken.
		m.reportErr(w, err, "enterprise report failed: "+what, "failed to build the "+what)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- schedules -------------------------------------------------------------

func (m *Module) handleListSchedules(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if m.scheduler == nil {
		writeNotWired(w, "report scheduling")
		return
	}
	items, err := m.scheduler.ListSchedules(r.Context(), mc.Tenant)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list schedules")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (m *Module) handleCreateSchedule(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if m.scheduler == nil {
		writeNotWired(w, "report scheduling")
		return
	}
	var cfg ScheduleConfig
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<16))
	if err := dec.Decode(&cfg); err != nil || dec.More() {
		writeError(w, http.StatusBadRequest, "invalid schedule JSON")
		return
	}
	if !validReportType(cfg.ReportType) {
		writeError(w, http.StatusBadRequest, "unknown report type")
		return
	}
	if cfg.Format != FormatPDF {
		cfg.Format = FormatHTML
	}
	if _, err := ParseCronSpec(cfg.Cron); err != nil {
		writeError(w, http.StatusBadRequest, "invalid cron spec: "+err.Error())
		return
	}
	if err := m.scheduler.ScheduleReport(r.Context(), mc.Tenant, cfg); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	items, _ := m.scheduler.ListSchedules(r.Context(), mc.Tenant)
	writeJSON(w, http.StatusCreated, map[string]any{"items": items})
}

func (m *Module) handleDeleteSchedule(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if m.scheduler == nil {
		writeNotWired(w, "report scheduling")
		return
	}
	if err := m.scheduler.DeleteSchedule(r.Context(), mc.Tenant, chi.URLParam(r, "id")); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func (m *Module) handleListScheduleRuns(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if m.scheduler == nil {
		writeNotWired(w, "report scheduling")
		return
	}
	runs, err := m.scheduler.ListRuns(r.Context(), mc.Tenant, chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list runs")
		return
	}
	// List is metadata-only; the artifact downloads via the run route.
	metas := make([]ScheduleRun, 0, len(runs))
	for _, run := range runs {
		run.Output = nil
		metas = append(metas, run)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": metas})
}

func (m *Module) handleGetScheduleRun(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if m.scheduler == nil {
		writeNotWired(w, "report scheduling")
		return
	}
	runs, err := m.scheduler.ListRuns(r.Context(), mc.Tenant, chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load runs")
		return
	}
	rid := chi.URLParam(r, "rid")
	for _, run := range runs {
		if run.ID != rid {
			continue
		}
		if len(run.Output) == 0 {
			writeJSON(w, http.StatusOK, run)
			return
		}
		writeReport(w, run.Output, Format(run.Format))
		return
	}
	writeError(w, http.StatusNotFound, "unknown run")
}

// ---- branding ---------------------------------------------------------------

func (m *Module) handleGetBranding(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if m.branding == nil {
		writeNotWired(w, "report branding")
		return
	}
	cfg, err := m.branding.GetBranding(r.Context(), mc.Tenant)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load branding")
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

func (m *Module) handleSetBranding(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if m.branding == nil {
		writeNotWired(w, "report branding")
		return
	}
	var cfg BrandingConfig
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<16))
	if err := dec.Decode(&cfg); err != nil || dec.More() {
		writeError(w, http.StatusBadRequest, "invalid branding JSON")
		return
	}
	if err := m.branding.SetBranding(r.Context(), mc.Tenant, cfg); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

// ---- custom templates ---------------------------------------------------------

func (m *Module) handleGetTemplate(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if m.customTmpl == nil {
		writeNotWired(w, "custom report templates")
		return
	}
	rt := ReportType(chi.URLParam(r, "type"))
	if !validReportType(rt) {
		writeError(w, http.StatusNotFound, "unknown report type")
		return
	}
	html, ok, err := m.customTmpl.GetTemplate(r.Context(), mc.Tenant, rt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load template")
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "no custom template for this report type")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(html))
}

func (m *Module) handleSetTemplate(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if m.customTmpl == nil {
		writeNotWired(w, "custom report templates")
		return
	}
	rt := ReportType(chi.URLParam(r, "type"))
	if !validReportType(rt) {
		writeError(w, http.StatusNotFound, "unknown report type")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxTemplateBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read template body")
		return
	}
	if len(body) > maxTemplateBytes {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("template exceeds %d bytes", maxTemplateBytes))
		return
	}
	if strings.TrimSpace(string(body)) == "" {
		writeError(w, http.StatusBadRequest, "template body is empty")
		return
	}
	if err := m.customTmpl.SetTemplate(r.Context(), mc.Tenant, rt, string(body)); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stored": true, "report_type": rt})
}

func (m *Module) handleDeleteTemplate(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if m.customTmpl == nil {
		writeNotWired(w, "custom report templates")
		return
	}
	rt := ReportType(chi.URLParam(r, "type"))
	if err := m.customTmpl.DeleteTemplate(r.Context(), mc.Tenant, rt); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

// ---- schedule execution (driven by the composition-root pump) ------------------------

// RunDueSchedules evaluates the tenant's cron schedules plus the enterprise
// digest cadences and generates every report that is due at now, recording one
// ScheduleRun per execution (ok or failed — a failed render is a recorded
// fact, never a silent skip). Returns the number of runs recorded. Inert (0,
// nil) when the scheduler seam is not wired.
func (m *Module) RunDueSchedules(ctx context.Context, mc api.ModuleContext, now time.Time) (int, error) {
	if m.scheduler == nil {
		return 0, nil
	}
	schedules, err := m.scheduler.ListSchedules(ctx, mc.Tenant)
	if err != nil {
		return 0, err
	}
	ran := 0
	for _, cfg := range schedules {
		if !cfg.Enabled {
			continue
		}
		spec, err := ParseCronSpec(cfg.Cron)
		if err != nil {
			continue // rejected at create; a stored-corrupt spec must not wedge the pump
		}
		last, err := m.lastRunAt(ctx, mc.Tenant, cfg.ID)
		if err != nil {
			return ran, err
		}
		if !spec.DueSince(last, now) {
			continue
		}
		m.executeSchedule(ctx, mc, cfg, now)
		ran++
	}
	n, err := m.runDueDigests(ctx, mc, now)
	return ran + n, err
}

// runDueDigests generates the operator-cadence enterprise digests (posture/
// risk/bundle JSON) due at now, recording runs under the reserved schedule ids
// "digest:<cadence>". Inert without the enterprise engine.
func (m *Module) runDueDigests(ctx context.Context, mc api.ModuleContext, now time.Time) (int, error) {
	if m.enterprise == nil {
		return 0, nil
	}
	lastRun := map[string]time.Time{}
	for _, cadence := range []string{"daily", "weekly", "monthly"} {
		last, err := m.lastRunAt(ctx, mc.Tenant, "digest:"+cadence)
		if err != nil {
			return 0, err
		}
		if !last.IsZero() {
			lastRun[cadence] = last
		}
	}
	ran := 0
	for _, due := range m.enterprise.DueDigests(now, lastRun) {
		var out any
		var err error
		switch due.ReportType {
		case "posture":
			out, err = m.enterprise.PostureReport(ctx, mc.Tenant)
		case "risk":
			out, err = m.enterprise.RiskSummary(ctx, mc.Tenant)
		case "bundle":
			out, err = m.enterprise.EvidenceBundle(ctx, mc.Tenant)
		default:
			continue
		}
		run := ScheduleRun{
			ScheduleID: "digest:" + due.Cadence,
			ReportType: due.ReportType,
			Format:     "json",
			RanAt:      now.UTC().Format(time.RFC3339),
		}
		if err != nil {
			run.Status, run.Error = "failed", err.Error()
		} else if body, jerr := json.Marshal(out); jerr != nil {
			run.Status, run.Error = "failed", jerr.Error()
		} else if len(body) > maxScheduleRunOutput {
			run.Status, run.Error = "failed", fmt.Sprintf("digest output exceeds %d bytes", maxScheduleRunOutput)
		} else {
			run.Status, run.Output = "ok", body
		}
		if rerr := m.scheduler.RecordRun(ctx, mc.Tenant, run); rerr != nil {
			return ran, rerr
		}
		ran++
	}
	return ran, nil
}

// executeSchedule renders one due schedule through the SAME gather+render path
// the on-demand route uses, and records the outcome.
func (m *Module) executeSchedule(ctx context.Context, mc api.ModuleContext, cfg ScheduleConfig, now time.Time) {
	run := ScheduleRun{
		ScheduleID: cfg.ID,
		ReportType: string(cfg.ReportType),
		Format:     string(cfg.Format),
		RanAt:      now.UTC().Format(time.RFC3339),
	}
	output, err := m.renderScheduled(ctx, mc, cfg, now)
	if err != nil {
		run.Status, run.Error = "failed", err.Error()
	} else if len(output) > maxScheduleRunOutput {
		run.Status, run.Error = "failed", fmt.Sprintf("report output exceeds %d bytes", maxScheduleRunOutput)
	} else {
		run.Status, run.Output = "ok", output
	}
	if err := m.scheduler.RecordRun(ctx, mc.Tenant, run); err != nil {
		m.log.Error("scheduled report: run record failed", "schedule", cfg.ID, "err", err)
	}
}

func (m *Module) renderScheduled(ctx context.Context, mc api.ModuleContext, cfg ScheduleConfig, now time.Time) ([]byte, error) {
	params := ReportParams{
		Type:      cfg.ReportType,
		Format:    cfg.Format,
		From:      now.AddDate(0, -1, 0),
		To:        now,
		Framework: cfg.Framework,
		Team:      cfg.Team,
		Locale:    cfg.Locale,
	}
	if params.Locale == "" {
		params.Locale = "en"
	}
	data, err := m.gatherData(ctx, mc, params)
	if err != nil {
		return nil, err
	}
	branding := BrandingConfig{}
	if m.branding != nil {
		if b, err := m.branding.GetBranding(ctx, mc.Tenant); err == nil {
			branding = b
		}
	}
	html, err := m.renderWithCustomTemplate(ctx, mc.Tenant, params.Type, data, params.Locale, branding)
	if err != nil {
		return nil, err
	}
	if cfg.Format == FormatPDF {
		return RenderPDF(ctx, html)
	}
	return html, nil
}

// lastRunAt returns the most recent recorded run instant for a schedule id
// (zero time = never ran).
func (m *Module) lastRunAt(ctx context.Context, tenant model.TenantID, scheduleID string) (time.Time, error) {
	runs, err := m.scheduler.ListRuns(ctx, tenant, scheduleID)
	if err != nil {
		return time.Time{}, err
	}
	var last time.Time
	for _, run := range runs {
		if t, err := time.Parse(time.RFC3339, run.RanAt); err == nil && t.After(last) {
			last = t
		}
	}
	return last, nil
}

// renderWithCustomTemplate renders through the tenant's custom template when
// one is stored, falling back to the built-in template set.
func (m *Module) renderWithCustomTemplate(ctx context.Context, tenant model.TenantID, rt ReportType, data any, locale string, branding BrandingConfig) ([]byte, error) {
	if m.customTmpl != nil {
		if tmpl, ok, err := m.customTmpl.GetTemplate(ctx, tenant, rt); err == nil && ok {
			html, rerr := m.engine.RenderCustomHTML(rt, tmpl, data, locale, branding)
			if rerr == nil {
				return html, nil
			}
			// A stored template that fails to render is a loud fallback, never
			// a broken report: the built-in template still serves the data.
			m.log.Error("custom template render failed; falling back to the built-in template", "type", rt, "err", rerr)
		}
	}
	return m.engine.RenderHTML(rt, data, locale, branding)
}
