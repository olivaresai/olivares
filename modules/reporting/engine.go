// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package reporting

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"log/slog"
	"strings"
	"time"
)

//go:embed templates/*.html
var templateFS embed.FS

// Engine renders reports as HTML (and optionally PDF via chromedp).
type Engine struct {
	log   *slog.Logger
	tmpl  *template.Template
	funcs template.FuncMap
}

// reportFuncMap is the template function set shared by the built-in templates,
// operator-uploaded custom templates and template validation.
func reportFuncMap() template.FuncMap {
	return template.FuncMap{
		"t":              func(locale, key string) string { return T(locale, key) },
		"statusBadge":    statusBadgeClass,
		"budgetBar":      budgetBarClass,
		"formatDate":     formatDate,
		"formatDateTime": formatDateTime,
		"formatUSD":      formatMicroUSD,
		"formatPct":      formatPct,
		"formatNumber":   formatNumber,
		"upper":          strings.ToUpper,
		"lower":          strings.ToLower,
		"join":           strings.Join,
		"safeHTML":       func(s string) template.HTML { return template.HTML(s) }, // #nosec G203 -- safeHTML wraps only first-party report fragments, never user/tenant data
		"defaultCSS":     func() template.CSS { return template.CSS(defaultCSS) },  // #nosec G203 -- defaultCSS is a compile-time constant stylesheet
		"coveragePct": func(s AssessmentSummary) int {
			if s.Total == 0 {
				return 0
			}
			return (s.Satisfied + s.ByDesign) * 100 / s.Total
		},
	}
}

// NewEngine creates a rendering engine with embedded templates.
func NewEngine(log *slog.Logger) *Engine {
	funcMap := reportFuncMap()
	tmpl := template.Must(template.New("").Funcs(funcMap).ParseFS(templateFS, "templates/*.html"))
	return &Engine{log: log, tmpl: tmpl, funcs: funcMap}
}

// templateData is the envelope passed to every template.
type templateData struct {
	CSS      template.CSS
	Locale   string
	Report   any
	Branding BrandingConfig
}

// RenderHTML renders a report as HTML.
func (e *Engine) RenderHTML(reportType ReportType, data any, locale string, branding BrandingConfig) ([]byte, error) {
	tmplName := string(reportType) + ".html"
	t := e.tmpl.Lookup(tmplName)
	if t == nil {
		return nil, fmt.Errorf("template %q not found", tmplName)
	}

	td := templateData{
		CSS:      template.CSS(defaultCSS), // #nosec G203 -- defaultCSS is a compile-time constant stylesheet
		Locale:   locale,
		Report:   data,
		Branding: branding,
	}

	var buf bytes.Buffer
	if err := t.Execute(&buf, td); err != nil {
		return nil, fmt.Errorf("render %s: %w", reportType, err)
	}
	return buf.Bytes(), nil
}

// RenderCustomHTML renders a report through an OPERATOR-SUPPLIED template
// (custom templates) with the SAME data envelope and function set the
// built-in templates use. html/template contextual auto-escaping applies to the
// report data exactly as it does for the built-ins; the template text itself is
// tenant-admin-provided markup (validated at upload, see ValidateCustomTemplate).
func (e *Engine) RenderCustomHTML(reportType ReportType, tmplText string, data any, locale string, branding BrandingConfig) ([]byte, error) {
	t, err := template.New("custom-" + string(reportType)).Funcs(e.funcs).Parse(tmplText)
	if err != nil {
		return nil, fmt.Errorf("custom template for %s: %w", reportType, err)
	}
	td := templateData{
		CSS:      template.CSS(defaultCSS), // #nosec G203 -- defaultCSS is a compile-time constant stylesheet
		Locale:   locale,
		Report:   data,
		Branding: branding,
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, td); err != nil {
		return nil, fmt.Errorf("render custom %s: %w", reportType, err)
	}
	return buf.Bytes(), nil
}

// ValidateCustomTemplate parses a custom report template against the module's
// function set — the upload-time validation the template provider runs.
// It proves the template is executable syntax; runtime render errors (bad field
// references) still fall back to the built-in template, loudly.
func ValidateCustomTemplate(tmplText string) error {
	_, err := template.New("candidate").Funcs(reportFuncMap()).Parse(tmplText)
	return err
}

func formatDate(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Format("2006-01-02")
}

func formatDateTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Format("2006-01-02 15:04 UTC")
}

func formatMicroUSD(microUSD int64) string {
	dollars := float64(microUSD) / 1_000_000
	if dollars >= 1000 {
		return fmt.Sprintf("$%.0f", dollars)
	}
	if dollars >= 1 {
		return fmt.Sprintf("$%.2f", dollars)
	}
	return fmt.Sprintf("$%.4f", dollars)
}

func formatPct(pct int) string {
	return fmt.Sprintf("%d%%", pct)
}

func formatNumber(n int64) string {
	if n >= 1_000_000_000 {
		return fmt.Sprintf("%.1fB", float64(n)/1_000_000_000)
	}
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}
