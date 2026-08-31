// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

func appTitles(fs []model.FindingReport) string {
	var b strings.Builder
	for _, f := range fs {
		b.WriteString("\n  - [" + string(f.Severity) + "] " + f.Title)
	}
	return b.String()
}

func appFindTitle(fs []model.FindingReport, sub string) (model.FindingReport, bool) {
	for _, f := range fs {
		if strings.Contains(f.Title, sub) {
			return f, true
		}
	}
	return model.FindingReport{}, false
}

// TestAppsUndeclaredTemplate: with a pre-declared inventory, an observed ui://
// template outside it is HIGH; a declared-but-absent one is the Info stale
// signal; the declared+observed one is silent.
func TestAppsUndeclaredTemplate(t *testing.T) {
	cat := catalog{
		resources: []Resource{
			{URI: "ui://srv/dashboard", Name: "dashboard", MimeType: appMimeType},
			{URI: "ui://srv/rogue-panel", Name: "rogue", MimeType: appMimeType},
		},
	}
	spec := serverSpec{Name: "srv", UITemplates: []string{"ui://srv/dashboard", "ui://srv/settings"}}
	fs := appsFindings(spec, cat, fixedTime())

	f, ok := appFindTitle(fs, "UNDECLARED ui:// template")
	if !ok || f.Severity != model.SeverityHigh || !strings.Contains(f.Title, "rogue-panel") {
		t.Errorf("missing/mis-shaped undeclared-template finding (%v): %s", ok, appTitles(fs))
	}
	if f.Kind != findingApp || f.SubjectRef != "srv" {
		t.Errorf("finding shape wrong: %+v", f)
	}
	if f, ok := appFindTitle(fs, "stale inventory entry"); !ok || f.Severity != model.SeverityInfo || !strings.Contains(f.Title, "settings") {
		t.Errorf("missing stale-declaration Info (%v): %s", ok, appTitles(fs))
	}
	if f, ok := appFindTitle(fs, `"ui://srv/dashboard" — not in`); ok {
		t.Errorf("declared+observed template must be silent: %+v", f)
	}
}

// TestAppsUngovernedSurface: ui:// templates with NO pre-declared inventory is
// the Medium ungoverned-surface signal; a tool-meta-only template counts (the
// spec lets servers omit UI resources from resources/list).
func TestAppsUngovernedSurface(t *testing.T) {
	cat := catalog{
		tools: []Tool{{
			Name: "show_chart", Description: "Renders a chart.",
			Meta: json.RawMessage(`{"ui":{"resourceUri":"ui://srv/chart"}}`),
		}},
	}
	fs := appsFindings(serverSpec{Name: "srv"}, cat, fixedTime())
	f, ok := appFindTitle(fs, "NO pre-declared inventory configured")
	if !ok || f.Severity != model.SeverityMedium || !strings.Contains(f.Title, "1 ui:// template(s)") {
		t.Errorf("missing ungoverned-surface finding (%v): %s", ok, appTitles(fs))
	}
}

// TestAppsSpecConformance: the pre-GA mimeType, a wrong mimeType, the
// deprecated flat meta key and a parameterized ui:// template are all flagged.
func TestAppsSpecConformance(t *testing.T) {
	cat := catalog{
		resources: []Resource{
			{URI: "ui://srv/old", Name: "old", MimeType: preGAMimeType},
			{URI: "ui://srv/wrong", Name: "wrong", MimeType: "text/plain"},
		},
		templates: []ResourceTemplate{{URITemplate: "ui://srv/{panel}", Name: "param"}},
		tools: []Tool{{
			Name: "open_old", Meta: json.RawMessage(`{"ui/resourceUri":"ui://srv/old"}`),
		}},
	}
	spec := serverSpec{Name: "srv", UITemplates: []string{"ui://srv/old", "ui://srv/wrong"}}
	fs := appsFindings(spec, cat, fixedTime())

	for _, sub := range []string{
		"proposal-era mimeType text/html+mcp",
		"does not declare the required mimeType",
		"deprecated pre-GA flat _meta",
		"parameterizes the reserved ui:// scheme",
	} {
		if _, ok := appFindTitle(fs, sub); !ok {
			t.Errorf("missing conformance finding %q: %s", sub, appTitles(fs))
		}
	}
}

// TestAppsAppOnlyAndSandboxPosture: app-only tools are inventoried (Medium when
// executional), wildcard CSP and sensitive permissions are flagged.
func TestAppsAppOnlyAndSandboxPosture(t *testing.T) {
	cat := catalog{
		resources: []Resource{{
			URI: "ui://srv/panel", Name: "panel", MimeType: appMimeType,
			Meta: json.RawMessage(`{"ui":{"csp":{"connectDomains":["*"]},"permissions":{"camera":true,"clipboardWrite":true}}}`),
		}},
		tools: []Tool{
			{Name: "refresh_panel", Description: "Refreshes the panel.", Meta: json.RawMessage(`{"ui":{"resourceUri":"ui://srv/panel","visibility":["app"]}}`)},
			{Name: "run_command", Description: "Runs a shell command.", Meta: json.RawMessage(`{"ui":{"visibility":["app"]}}`)},
		},
	}
	spec := serverSpec{Name: "srv", UITemplates: []string{"ui://srv/panel"}}
	fs := appsFindings(spec, cat, fixedTime())

	if f, ok := appFindTitle(fs, "tool refresh_panel is APP-ONLY"); !ok || f.Severity != model.SeverityInfo {
		t.Errorf("missing app-only Info inventory (%v): %s", ok, appTitles(fs))
	}
	if f, ok := appFindTitle(fs, "exposes an exec surface"); !ok || f.Severity != model.SeverityMedium || !strings.Contains(f.Title, "run_command") {
		t.Errorf("missing app-only-exec Medium (%v): %s", ok, appTitles(fs))
	}
	if f, ok := appFindTitle(fs, "WILDCARD connect CSP domain"); !ok || f.Severity != model.SeverityMedium {
		t.Errorf("missing wildcard-CSP finding (%v): %s", ok, appTitles(fs))
	}
	if f, ok := appFindTitle(fs, "sensitive device permission"); !ok || !strings.Contains(f.Title, "camera") || !strings.Contains(f.Title, "clipboardWrite") {
		t.Errorf("missing sensitive-permissions finding (%v): %s", ok, appTitles(fs))
	}
}

// TestAppsCleanServerSilent: a server with no UI surface and no declared
// inventory emits nothing from the apps detective.
func TestAppsCleanServerSilent(t *testing.T) {
	cat := catalog{
		resources: []Resource{{URI: "file:///etc/hosts", Name: "hosts", MimeType: "text/plain"}},
		tools:     []Tool{{Name: "read_file", Description: "Reads a file."}},
	}
	if fs := appsFindings(serverSpec{Name: "srv"}, cat, fixedTime()); len(fs) != 0 {
		t.Errorf("clean server must be silent, got: %s", appTitles(fs))
	}
}
