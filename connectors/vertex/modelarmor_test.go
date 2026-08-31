// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package vertex

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

const (
	armorProjectFloorPath = "/v1/projects/test-proj/locations/global/floorSetting"
	armorOrgFloorPath     = "/v1/organizations/123/locations/global/floorSetting"
	armorFolderFloorPath  = "/v1/folders/456/locations/global/floorSetting"
	armorTemplatesPath    = "/v1/projects/test-proj/locations/us-central1/templates"
)

type armorRoute struct {
	fixture string
	status  int
}

type armorRouteServer struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	reqs   []string
	routes map[string]armorRoute
}

func newArmorRouteServer(t *testing.T, routes map[string]armorRoute) *armorRouteServer {
	t.Helper()
	rs := &armorRouteServer{t: t, routes: routes}
	rs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.mu.Lock()
		rs.reqs = append(rs.reqs, r.Method+" "+r.URL.Path)
		rs.mu.Unlock()

		route, ok := rs.routes[r.URL.Path]
		if !ok {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		if route.status != 0 && route.status != http.StatusOK {
			http.Error(w, http.StatusText(route.status), route.status)
			return
		}
		writeFixture(w, route.fixture)
	}))
	t.Cleanup(rs.srv.Close)
	return rs
}

func (rs *armorRouteServer) requests() []string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return append([]string(nil), rs.reqs...)
}

func openArmorSource(t *testing.T, baseURL string, extra map[string]string) *Source {
	t.Helper()
	settings := map[string]string{
		cfgAccessToken:         "test-token",
		cfgProject:             "test-proj",
		cfgEnableUsage:         "false",
		cfgEnableModelArmor:    "true",
		cfgAIPlatformEndpoint:  baseURL,
		cfgMonitoringEndpoint:  baseURL,
		cfgModelArmorEndpoint:  baseURL,
		cfgModelArmorGlobalURL: baseURL,
	}
	for k, v := range extra {
		settings[k] = v
	}
	s := New()
	s.now = fixedClock
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func mustFixtureBytes(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func mustFloorFixture(t *testing.T, name string) floorSetting {
	t.Helper()
	var fs floorSetting
	if err := json.Unmarshal(mustFixtureBytes(t, name), &fs); err != nil {
		t.Fatalf("decode fixture %s: %v", name, err)
	}
	return fs
}

func mustTemplatesFixture(t *testing.T, name string) templatesResponse {
	t.Helper()
	var resp templatesResponse
	if err := json.Unmarshal(mustFixtureBytes(t, name), &resp); err != nil {
		t.Fatalf("decode fixture %s: %v", name, err)
	}
	return resp
}

func gatherArmorReports(t *testing.T, s *Source) []model.FindingReport {
	t.Helper()
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return sink.findings()
}

func findingByRef(findings []model.FindingReport, ref string) *model.FindingReport {
	for i := range findings {
		if findings[i].SubjectRef == ref {
			return &findings[i]
		}
	}
	return nil
}

func driftReports(findings []model.FindingReport) []model.FindingReport {
	var out []model.FindingReport
	for _, f := range findings {
		if f.Kind == policyDriftKind {
			out = append(out, f)
		}
	}
	return out
}

func TestModelArmorProjectFloorMCPGapAndDetail(t *testing.T) {
	withMCP := projectArmorFloorFinding("test-proj", mustFloorFixture(t, "floor_setting_mcp_inspect_only.json"), fixedClock())
	if withMCP.Severity != model.SeverityLow {
		t.Fatalf("MCP floor severity = %s, want low: %+v", withMCP.Severity, withMCP)
	}
	if !strings.Contains(withMCP.Title, "MCP leg inspect-only (detect-but-allow)") {
		t.Fatalf("MCP floor title = %q, want MCP gap", withMCP.Title)
	}
	wantMCPDetail := "vertex.model_armor_floor project=test-proj enforced=true binds_ai_platform=true binds_mcp=true inspect_only=false inspect_and_block=true mcp_inspect_only=true mcp_inspect_and_block=false logging_ai_platform=true logging_mcp=true rai=[HATE_SPEECH] permissive_high=[HATE_SPEECH] prompt_injection=true gaps=MCP leg inspect-only (detect-but-allow)"
	if withMCP.DetailHash != redact.Hash(wantMCPDetail) {
		t.Fatalf("MCP floor detail hash = %s, want hash of %q", withMCP.DetailHash, wantMCPDetail)
	}

	withoutMCP := projectArmorFloorFinding("test-proj", mustFloorFixture(t, "floor_setting.json"), fixedClock())
	if withoutMCP.Severity != model.SeverityInfo {
		t.Fatalf("floor without MCP severity = %s, want info: %+v", withoutMCP.Severity, withoutMCP)
	}
	if strings.Contains(withoutMCP.Title, "MCP") {
		t.Fatalf("floor without MCP title = %q, want no MCP gap", withoutMCP.Title)
	}
	wantNoMCPDetail := "vertex.model_armor_floor project=test-proj enforced=true binds_ai_platform=true binds_mcp=false inspect_only=false inspect_and_block=true mcp_inspect_only=false mcp_inspect_and_block=false logging_ai_platform=true logging_mcp=false rai=[HATE_SPEECH] permissive_high=[HATE_SPEECH] prompt_injection=true gaps="
	if withoutMCP.DetailHash != redact.Hash(wantNoMCPDetail) {
		t.Fatalf("floor without MCP detail hash = %s, want hash of %q", withoutMCP.DetailHash, wantNoMCPDetail)
	}
}

func TestModelArmorTemplateMetadataPosture(t *testing.T) {
	resp := mustTemplatesFixture(t, "armor_templates_metadata.json")
	findings := map[string]model.FindingReport{}
	for _, tmpl := range resp.Templates {
		f := armorTemplateFinding("us-central1", tmpl, fixedClock())
		findings[templateLeaf(tmpl.Name)] = f
	}

	inspectOnly := findings["inspect-only-tmpl"]
	if inspectOnly.Severity != model.SeverityLow || !strings.Contains(inspectOnly.Title, "inspect-only enforcement (detect-but-allow)") {
		t.Fatalf("inspect-only template = %+v, want Low with inspect-only gap", inspectOnly)
	}
	wantInspectDetail := "vertex.model_armor_template location=us-central1 name=inspect-only-tmpl rai=[HATE_SPEECH] permissive_high=[] prompt_injection=true malicious_uri=true sdp=true enforcement=INSPECT_ONLY log_sanitize=true filter_version=FILTER_VERSION_ALIAS_STABLE gaps=inspect-only enforcement (detect-but-allow)"
	if inspectOnly.DetailHash != redact.Hash(wantInspectDetail) {
		t.Fatalf("inspect-only detail hash = %s, want hash of %q", inspectOnly.DetailHash, wantInspectDetail)
	}

	defaulted := findings["default-tmpl"]
	if defaulted.Severity != model.SeverityInfo || strings.Contains(defaulted.Title, "safety-config gaps") {
		t.Fatalf("defaulted template = %+v, want Info with no metadata gap", defaulted)
	}
	wantDefaultDetail := "vertex.model_armor_template location=us-central1 name=default-tmpl rai=[HATE_SPEECH] permissive_high=[] prompt_injection=true malicious_uri=true sdp=true enforcement=INSPECT_AND_BLOCK log_sanitize=false filter_version=stable-default gaps="
	if defaulted.DetailHash != redact.Hash(wantDefaultDetail) {
		t.Fatalf("default template detail hash = %s, want hash of %q", defaulted.DetailHash, wantDefaultDetail)
	}

	legacy := findings["legacy-tmpl"]
	if legacy.Severity != model.SeverityLow || !strings.Contains(legacy.Title, "legacy/retired filter version pinned") {
		t.Fatalf("legacy template = %+v, want Low with legacy gap", legacy)
	}

	stable := findings["stable-tmpl"]
	if stable.Severity != model.SeverityInfo || strings.Contains(stable.Title, "filter version pinned") {
		t.Fatalf("stable template = %+v, want Info with no filter-version gap", stable)
	}
}

func TestModelArmorOrgFolderConformanceFloors(t *testing.T) {
	srv := newArmorRouteServer(t, map[string]armorRoute{
		armorProjectFloorPath: {fixture: "floor_setting.json"},
		armorOrgFloorPath:     {fixture: "floor_setting_org_clean_no_binding.json"},
		armorFolderFloorPath:  {fixture: "floor_setting_org_clean_no_binding.json"},
	})
	s := openArmorSource(t, srv.srv.URL, map[string]string{
		cfgModelArmorOrg:     "123",
		cfgModelArmorFolders: "456",
	})
	findings := gatherArmorReports(t, s)

	org := findingByRef(findings, "organizations/123")
	if org == nil || org.Severity != model.SeverityInfo || !strings.Contains(org.Title, "organization conformance floor active") {
		t.Fatalf("org conformance finding = %+v, want Info active", org)
	}
	if strings.Contains(org.Title, "AI_PLATFORM") {
		t.Fatalf("org conformance title = %q, want no project runtime binding gap", org.Title)
	}
	folder := findingByRef(findings, "folders/456")
	if folder == nil || folder.Severity != model.SeverityInfo || !strings.Contains(folder.Title, "folder conformance floor active") {
		t.Fatalf("folder conformance finding = %+v, want Info active", folder)
	}
	if strings.Contains(folder.Title, "AI_PLATFORM") {
		t.Fatalf("folder conformance title = %q, want no project runtime binding gap", folder.Title)
	}

	srv404 := newArmorRouteServer(t, map[string]armorRoute{
		armorProjectFloorPath: {fixture: "floor_setting.json"},
		armorOrgFloorPath:     {status: http.StatusNotFound},
	})
	s404 := openArmorSource(t, srv404.srv.URL, map[string]string{cfgModelArmorOrg: "123"})
	missing := findingByRef(gatherArmorReports(t, s404), "organizations/123")
	if missing == nil || missing.Severity != model.SeverityMedium || !strings.Contains(missing.Title, "No Model Armor floor setting configured for organization 123") {
		t.Fatalf("org 404 finding = %+v, want Medium absence", missing)
	}

	srvUnset := newArmorRouteServer(t, map[string]armorRoute{
		armorProjectFloorPath: {fixture: "floor_setting.json"},
	})
	sUnset := openArmorSource(t, srvUnset.srv.URL, nil)
	_ = gatherArmorReports(t, sUnset)
	for _, req := range srvUnset.requests() {
		if strings.Contains(req, "/organizations/") || strings.Contains(req, "/folders/") {
			t.Fatalf("unset org/folder config made scoped request %q", req)
		}
	}
}

func TestModelArmorFloorDrift(t *testing.T) {
	cases := []struct {
		name        string
		route       armorRoute
		extra       map[string]string
		wantDrift   string
		wantPosture model.Severity
	}{
		{
			name:        "expect enforcement healthy floor",
			route:       armorRoute{fixture: "floor_setting.json"},
			extra:       map[string]string{cfgExpectFloorEnforce: "true"},
			wantPosture: model.SeverityInfo,
		},
		{
			name:        "expect enforcement missing floor",
			route:       armorRoute{status: http.StatusNotFound},
			extra:       map[string]string{cfgExpectFloorEnforce: "true"},
			wantDrift:   "floor absent",
			wantPosture: model.SeverityMedium,
		},
		{
			name:        "enforcement disabled",
			route:       armorRoute{fixture: "floor_setting_enforcement_disabled.json"},
			extra:       map[string]string{cfgExpectFloorEnforce: "true"},
			wantDrift:   "enforcement disabled",
			wantPosture: model.SeverityMedium,
		},
		{
			name:        "block expected",
			route:       armorRoute{fixture: "floor_setting_inspect_only.json"},
			extra:       map[string]string{cfgExpectFloorBlock: "true"},
			wantDrift:   "inspect-only, block expected",
			wantPosture: model.SeverityLow,
		},
		{
			name:        "logging expected",
			route:       armorRoute{fixture: "floor_setting_logging_disabled.json"},
			extra:       map[string]string{cfgExpectFloorLogging: "true"},
			wantDrift:   "cloud logging disabled, logging expected",
			wantPosture: model.SeverityInfo,
		},
		{
			name:        "no expectations missing floor",
			route:       armorRoute{status: http.StatusNotFound},
			wantPosture: model.SeverityMedium,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newArmorRouteServer(t, map[string]armorRoute{armorProjectFloorPath: tc.route})
			s := openArmorSource(t, srv.srv.URL, tc.extra)
			findings := gatherArmorReports(t, s)

			var posture *model.FindingReport
			for i := range findings {
				if findings[i].Kind == safetyPostureKind && findings[i].SubjectKind == subjectArmorFloor {
					posture = &findings[i]
					break
				}
			}
			if posture == nil || posture.Severity != tc.wantPosture {
				t.Fatalf("posture = %+v, want severity %s", posture, tc.wantPosture)
			}

			drifts := driftReports(findings)
			if tc.wantDrift == "" {
				if len(drifts) != 0 {
					t.Fatalf("drift findings = %+v, want none", drifts)
				}
				return
			}
			if len(drifts) != 1 {
				t.Fatalf("drift findings = %d, want 1: %+v", len(drifts), drifts)
			}
			if drifts[0].Severity != model.SeverityHigh || !strings.Contains(drifts[0].Title, tc.wantDrift) {
				t.Fatalf("drift finding = %+v, want High containing %q", drifts[0], tc.wantDrift)
			}
		})
	}
}

func TestModelArmorEmissionDeterminism(t *testing.T) {
	srv := newArmorRouteServer(t, map[string]armorRoute{
		armorTemplatesPath:    {fixture: "armor_templates_metadata.json"},
		armorProjectFloorPath: {fixture: "floor_setting.json"},
		armorOrgFloorPath:     {fixture: "floor_setting_org_clean_no_binding.json"},
		armorFolderFloorPath:  {fixture: "floor_setting_org_clean_no_binding.json"},
	})
	s := openArmorSource(t, srv.srv.URL, map[string]string{
		cfgModelArmorLocations: "us-central1",
		cfgModelArmorOrg:       "123",
		cfgModelArmorFolders:   "456",
	})

	var runs [][]model.FindingReport
	for i := 0; i < 2; i++ {
		sink := &fakeSink{}
		if err := s.gatherModelArmor(context.Background(), sink, fixedClock()); err != nil {
			t.Fatalf("gatherModelArmor run %d: %v", i, err)
		}
		runs = append(runs, sink.findings())
	}
	if !reflect.DeepEqual(runs[0], runs[1]) {
		t.Fatalf("findings not deterministic:\nrun1=%+v\nrun2=%+v", runs[0], runs[1])
	}
}
