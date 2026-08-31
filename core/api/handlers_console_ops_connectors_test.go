// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/api"
)

// The health summary used to report ONE connector number: the size of the
// connector CATALOG (the kinds this build can wire, ~100 on every install),
// under the name `connectors`. The console rendered it as "N active", so a
// freshly installed deployment with an empty roster claimed a hundred live
// connectors while GET /v1/connectors/health reported total=0, running=0.
// The catalog and the fleet are different populations and must be counted, and
// named, separately.

// catalogOf builds a stub catalog of n kinds — a stand-in for the real ~100-kind
// build catalog, which exists whether or not anything is configured.
func catalogOf(kinds ...string) *stubConnectorOnboarding {
	infos := make([]api.ConnectorInfo, 0, len(kinds))
	for _, k := range kinds {
		infos = append(infos, api.ConnectorInfo{Kind: k, Transport: "in_process", FieldsKnown: true})
	}
	return &stubConnectorOnboarding{connectors: infos}
}

// A clean install: the catalog is full, the roster is empty. Every runtime
// number must be zero, and the catalog number must NOT be reported under a
// runtime-sounding name.
func TestHealthSummaryCleanInstallCountsNoRuntimeConnectors(t *testing.T) {
	h := newHarnessOpts(t, func(o *api.Options) {
		o.ConnectorOnboarding = catalogOf("aws", "gcp-audit", "azure-activity", "github")
		o.SourceRoster = &stubSourceRoster{} // nothing configured yet
	})
	admin := h.adminLogin()

	r := h.do("GET", "/v1/console/health-summary", admin, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("health-summary = %d %s", r.code, r.raw)
	}
	if r.body["connectors_available"] != float64(4) {
		t.Errorf("connectors_available = %v, want 4 (catalog kinds): %s", r.body["connectors_available"], r.raw)
	}
	for _, field := range []string{"connectors_configured", "connectors_running", "connectors_error"} {
		if r.body[field] != float64(0) {
			t.Errorf("%s = %v on an empty roster, want 0: %s", field, r.body[field], r.raw)
		}
	}
	// The ambiguous field is gone: a stale consumer must fail loudly rather than
	// keep reading the catalog and calling it a fleet.
	if _, present := r.body["connectors"]; present {
		t.Errorf("legacy `connectors` field still present (it carried the catalog size): %s", r.raw)
	}
}

// With a roster, each number counts its own population: catalog kinds,
// configured instances, running instances, and enabled+failed instances.
func TestHealthSummarySeparatesCatalogConfiguredAndRunning(t *testing.T) {
	roster := &stubSourceRoster{
		sources: []api.SourceRosterEntry{
			{Name: "aws-prod", Kind: "aws", Status: "running", Enabled: true},
			{Name: "gh-main", Kind: "github", Status: "running", Enabled: true},
			{Name: "gcp-stg", Kind: "gcp-audit", Status: "failed", Enabled: true},
			{Name: "az-old", Kind: "azure-activity", Status: "failed", Enabled: false}, // disabled → not an error
			{Name: "paused", Kind: "aws", Status: "stopped", Enabled: true},
		},
	}
	h := newHarnessOpts(t, func(o *api.Options) {
		o.ConnectorOnboarding = catalogOf("aws", "gcp-audit", "azure-activity", "github")
		o.SourceRoster = roster
	})
	admin := h.adminLogin()

	r := h.do("GET", "/v1/console/health-summary", admin, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("health-summary = %d %s", r.code, r.raw)
	}
	// Four distinct expected values, so no two counters can be swapped without
	// this test noticing.
	for field, want := range map[string]float64{
		"connectors_available":  4, // catalog kinds this build can wire
		"connectors_configured": 5, // roster entries, enabled or not
		"connectors_running":    2, // live status == running
		"connectors_error":      1, // ENABLED and failed
	} {
		if r.body[field] != want {
			t.Errorf("%s = %v, want %v: %s", field, r.body[field], want, r.raw)
		}
	}
}

// The health summary and GET /v1/connectors/health read the same roster with the
// same criterion, so the dashboard tile and the connector-health view cannot
// disagree about how many connectors are running.
func TestHealthSummaryRunningAgreesWithConnectorHealthSummary(t *testing.T) {
	roster := &stubSourceRoster{
		sources: []api.SourceRosterEntry{
			{Name: "aws-prod", Kind: "aws", Status: "running", Enabled: true},
			{Name: "gcp-stg", Kind: "gcp-audit", Status: "failed", Enabled: true},
			{Name: "paused", Kind: "github", Status: "stopped", Enabled: true},
		},
	}
	h := newHarnessOpts(t, func(o *api.Options) {
		o.ConnectorOnboarding = catalogOf("aws", "gcp-audit", "github")
		o.SourceRoster = roster
	})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	summary := h.do("GET", "/v1/console/health-summary", admin, nil, nil)
	if summary.code != http.StatusOK {
		t.Fatalf("health-summary = %d %s", summary.code, summary.raw)
	}
	fleet := h.do("GET", "/v1/connectors/health", admin, nil, tenantHdr(tenant))
	if fleet.code != http.StatusOK {
		t.Fatalf("connectors/health = %d %s", fleet.code, fleet.raw)
	}
	fleetSummary, _ := fleet.body["summary"].(map[string]any)
	if fleetSummary == nil {
		t.Fatalf("connectors/health carries no summary: %s", fleet.raw)
	}
	if summary.body["connectors_running"] != fleetSummary["running"] {
		t.Errorf("health-summary connectors_running = %v but connectors/health running = %v",
			summary.body["connectors_running"], fleetSummary["running"])
	}
	if summary.body["connectors_configured"] != fleetSummary["total"] {
		t.Errorf("health-summary connectors_configured = %v but connectors/health total = %v",
			summary.body["connectors_configured"], fleetSummary["total"])
	}
}

// The setup wizard's connectors step carried the same defect: it asked the
// catalog, which is never empty, so the step was completed before the operator
// configured anything. It must ask the roster.
func TestSetupStatusConnectorsStepNeedsAConfiguredInstance(t *testing.T) {
	roster := &stubSourceRoster{}
	h := newHarnessOpts(t, func(o *api.Options) {
		o.ConnectorOnboarding = catalogOf("aws", "gcp-audit", "github")
		o.SourceRoster = roster
	})
	admin := h.adminLogin()

	if got := setupStepCompleted(t, h, admin, "connectors"); got != false {
		t.Errorf("setup step connectors = %v with a full catalog and an empty roster, want false", got)
	}

	roster.sources = []api.SourceRosterEntry{{Name: "aws-prod", Kind: "aws", Status: "running", Enabled: true}}
	if got := setupStepCompleted(t, h, admin, "connectors"); got != true {
		t.Errorf("setup step connectors = %v with one configured source, want true", got)
	}
}

func setupStepCompleted(t *testing.T, h *harness, token, id string) any {
	t.Helper()
	r := h.do("GET", "/v1/console/setup-status", token, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("setup-status = %d %s", r.code, r.raw)
	}
	steps, _ := r.body["steps"].([]any)
	for _, s := range steps {
		step, _ := s.(map[string]any)
		if step["id"] == id {
			return step["completed"]
		}
	}
	t.Fatalf("setup-status has no %q step: %s", id, r.raw)
	return nil
}
