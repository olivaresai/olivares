// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package vertex

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

type sanitizationLoggedRequest struct {
	Method string
	Path   string
	Auth   string
	Body   sanitizationEntriesListRequest
}

type sanitizationLogServer struct {
	t      *testing.T
	srv    *httptest.Server
	status int
	bodies []string
	mu     sync.Mutex
	reqs   []sanitizationLoggedRequest
}

func newSanitizationLogServer(t *testing.T, bodies []string, status int) *sanitizationLogServer {
	t.Helper()
	ls := &sanitizationLogServer{t: t, bodies: bodies, status: status}
	ls.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var body sanitizationEntriesListRequest
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}

		ls.mu.Lock()
		ls.reqs = append(ls.reqs, sanitizationLoggedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Auth:   r.Header.Get("Authorization"),
			Body:   body,
		})
		idx := len(ls.reqs) - 1
		ls.mu.Unlock()

		if ls.status != 0 && ls.status != http.StatusOK {
			http.Error(w, http.StatusText(ls.status), ls.status)
			return
		}
		response := `{}`
		if len(ls.bodies) > 0 {
			if idx >= len(ls.bodies) {
				idx = len(ls.bodies) - 1
			}
			response = ls.bodies[idx]
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(ls.srv.Close)
	return ls
}

func (ls *sanitizationLogServer) requests() []sanitizationLoggedRequest {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	return append([]sanitizationLoggedRequest(nil), ls.reqs...)
}

func openSanitizationSource(t *testing.T, endpoint string, extra map[string]string) *Source {
	t.Helper()
	settings := map[string]string{
		cfgAccessToken:              "test-token",
		cfgProject:                  "test-proj",
		cfgEnableUsage:              "false",
		cfgEnableSanitizationIngest: "true",
		cfgLoggingEndpoint:          endpoint,
		cfgAIPlatformEndpoint:       endpoint,
		cfgMonitoringEndpoint:       endpoint,
		cfgModelArmorEndpoint:       endpoint,
		cfgModelArmorGlobalURL:      endpoint,
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

func (f *fakeSink) metrics() []model.MetricSample {
	var out []model.MetricSample
	for _, o := range f.obs {
		if m, ok := o.(model.MetricSample); ok {
			out = append(out, m)
		}
	}
	return out
}

func (f *fakeSink) observations() []model.Observation {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]model.Observation(nil), f.obs...)
}

func TestSanitizationIngestFixtureAndRequest(t *testing.T) {
	srv := newSanitizationLogServer(t, []string{string(mustFixtureBytes(t, "sanitization_entries_page.json"))}, 0)
	s := openSanitizationSource(t, srv.srv.URL, nil)
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}

	reqs := srv.requests()
	if len(reqs) != 1 {
		t.Fatalf("logging requests = %d, want 1: %+v", len(reqs), reqs)
	}
	req := reqs[0]
	if req.Method != http.MethodPost || req.Path != "/v2/entries:list" {
		t.Fatalf("request = %s %s, want POST /v2/entries:list", req.Method, req.Path)
	}
	if req.Auth != "Bearer test-token" {
		t.Fatalf("authorization = %q, want bearer token", req.Auth)
	}
	if !reflect.DeepEqual(req.Body.ResourceNames, []string{"projects/test-proj"}) {
		t.Fatalf("resourceNames = %+v, want [projects/test-proj]", req.Body.ResourceNames)
	}
	if req.Body.OrderBy != "timestamp desc" || req.Body.PageSize != 1000 {
		t.Fatalf("order/pageSize = %q/%d, want timestamp desc/1000", req.Body.OrderBy, req.Body.PageSize)
	}
	if !strings.Contains(req.Body.Filter, `jsonPayload.@type="`+sanitizationLogPayloadType+`"`) ||
		!strings.Contains(req.Body.Filter, `timestamp>="`) {
		t.Fatalf("filter = %q, want @type literal and timestamp lower bound", req.Body.Filter)
	}

	findings := sink.findings()
	if len(findings) != 3 {
		t.Fatalf("findings = %d, want 3 guardrail findings: %+v", len(findings), findings)
	}
	blocked := findingByRef(findings, "tmpl-block")
	if blocked == nil || blocked.Kind != guardrailFindingKind || blocked.Severity != model.SeverityHigh ||
		!strings.Contains(blocked.Title, "blocked") ||
		!reflect.DeepEqual(blocked.OWASPLLM, []string{"LLM01:2025"}) ||
		!reflect.DeepEqual(blocked.OWASPASI, []string{"ASI01"}) {
		t.Fatalf("blocked PI finding = %+v, want High guardrail with LLM01/ASI01", blocked)
	}
	sdp := findingByRef(findings, "tmpl-sdp")
	if sdp == nil || sdp.Severity != model.SeverityMedium ||
		!reflect.DeepEqual(sdp.OWASPLLM, []string{"LLM02:2025"}) ||
		len(sdp.OWASPASI) != 0 {
		t.Fatalf("SDP finding = %+v, want Medium with LLM02 only", sdp)
	}
	if clean := findingByRef(findings, "tmpl-clean"); clean != nil {
		t.Fatalf("clean NO_MATCH entry emitted finding %+v, want none", clean)
	}

	metrics := sink.metrics()
	wantMetrics := []struct {
		verdict   string
		operation string
		value     int64
	}{
		{"block", "sanitize_user_prompt", 1},
		{"match", "sanitize_model_response", 1},
		{"match", "sanitize_user_prompt", 1},
		{"no_match", "sanitize_model_response", 1},
	}
	if len(metrics) != len(wantMetrics) {
		t.Fatalf("metrics = %d, want %d: %+v", len(metrics), len(wantMetrics), metrics)
	}
	for i, want := range wantMetrics {
		got := metrics[i]
		if got.Name != sanitizationMetricName || !got.Additive || got.Unit != "1" ||
			got.SubjectKind != "project" || got.SubjectRef != "projects/test-proj" ||
			got.Value != want.value ||
			got.Dimensions["verdict"] != want.verdict ||
			got.Dimensions["operation"] != want.operation {
			t.Fatalf("metric[%d] = %+v, want (%s,%s)=%d additive unit 1", i, got, want.verdict, want.operation, want.value)
		}
	}
	assertNoSanitizationSentinel(t, sink.observations())
}

func TestSanitizationFilterFoldDeterminism(t *testing.T) {
	s := &Source{cfg: config{project: "test-proj"}}
	entry := sanitizationLogEntry{
		Timestamp: "2026-06-20T11:59:00Z",
		InsertID:  "ins-multi",
		LogName:   "projects/test-proj/logs/modelarmor.googleapis.com%2Fsanitize",
		Labels: map[string]string{
			"modelarmor.googleapis.com/client_name":           "gemini",
			"modelarmor.googleapis.com/client_correlation_id": "corr-multi",
		},
		JSONPayload: sanitizationJSONPayload{
			OperationType:             "SANITIZE_USER_PROMPT",
			SanitizationVerdict:       "MODEL_ARMOR_SANITIZATION_VERDICT_BLOCK",
			SanitizationVerdictReason: "MULTI",
			SanitizationResult: sanitizationResult{
				FilterMatchState: "MATCH_FOUND",
				InvocationResult: "SUCCESS",
				FilterResults: map[string]sanitizationFilterResult{
					"sdpFilterResult": {
						SDPFilterResult: sanitizationSimpleResult{MatchState: "MATCH_FOUND"},
					},
					"piAndJailbreakFilterResult": {
						PIAndJailbreakFilterResult: sanitizationSimpleResult{MatchState: "MATCH_FOUND"},
					},
					"maliciousUriFilterResult": {
						MaliciousURIFilterResult: sanitizationSimpleResult{MatchState: "MATCH_FOUND"},
					},
				},
			},
		},
	}
	entry.Resource.Labels = map[string]string{"template_id": "tmpl-multi", "location": "us-central1", "resource_container": "projects/test-proj"}

	first, ok, _ := s.mapSanitizationEntry(entry, fixedClock())
	if !ok {
		t.Fatal("multi-match entry did not emit a finding")
	}
	second, ok, _ := s.mapSanitizationEntry(entry, fixedClock())
	if !ok {
		t.Fatal("multi-match entry did not emit a finding on second run")
	}
	wantFilters := "filters [maliciousUriFilterResult|piAndJailbreakFilterResult|sdpFilterResult]"
	if !strings.Contains(first.Title, wantFilters) {
		t.Fatalf("title = %q, want sorted %s", first.Title, wantFilters)
	}
	if first.DetailHash != second.DetailHash {
		t.Fatalf("detail hash not stable: %s != %s", first.DetailHash, second.DetailHash)
	}
}

func TestSanitizationPaginationAndPartialCoverage(t *testing.T) {
	t.Run("follows next page token", func(t *testing.T) {
		srv := newSanitizationLogServer(t, []string{
			`{"entries":[],"nextPageToken":"token-2"}`,
			`{"entries":[]}`,
		}, 0)
		s := openSanitizationSource(t, srv.srv.URL, nil)
		sink := &fakeSink{}
		if err := s.Gather(context.Background(), sink); err != nil {
			t.Fatalf("Gather: %v", err)
		}
		reqs := srv.requests()
		if len(reqs) != 2 {
			t.Fatalf("requests = %d, want 2: %+v", len(reqs), reqs)
		}
		if reqs[1].Body.PageToken != "token-2" {
			t.Fatalf("second page token = %q, want token-2", reqs[1].Body.PageToken)
		}
	})

	t.Run("max pages emits partial coverage", func(t *testing.T) {
		srv := newSanitizationLogServer(t, []string{`{"entries":[],"nextPageToken":"still-more"}`}, 0)
		s := openSanitizationSource(t, srv.srv.URL, map[string]string{cfgMaxPages: "1"})
		sink := &fakeSink{}
		if err := s.Gather(context.Background(), sink); err != nil {
			t.Fatalf("Gather: %v", err)
		}
		reqs := srv.requests()
		if len(reqs) != 1 {
			t.Fatalf("requests = %d, want 1: %+v", len(reqs), reqs)
		}
		findings := sink.findings()
		if len(findings) != 1 {
			t.Fatalf("findings = %d, want one partial coverage finding: %+v", len(findings), findings)
		}
		f := findings[0]
		if f.Kind != safetyPostureKind || f.Severity != model.SeverityLow ||
			f.SubjectKind != subjectArmorSanitization ||
			!strings.Contains(f.Title, "coverage partial") ||
			f.DetailHash == "" {
			t.Fatalf("partial finding = %+v, want Low safety_posture coverage", f)
		}
	})
}

func TestSanitizationGating(t *testing.T) {
	srv := newSanitizationLogServer(t, []string{`{}`}, 0)

	s := New()
	s.now = fixedClock
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{cfgLoggingEndpoint: srv.srv.URL}}); err != nil {
		t.Fatalf("Open default/offline: %v", err)
	}
	if err := s.Gather(context.Background(), &fakeSink{}); err != nil {
		t.Fatalf("Gather default/offline: %v", err)
	}
	if got := len(srv.requests()); got != 0 {
		t.Fatalf("default config logging calls = %d, want 0", got)
	}

	offline := New()
	offline.now = fixedClock
	if err := offline.Open(context.Background(), sdk.Config{Settings: map[string]string{
		cfgProject:                  "test-proj",
		cfgEnableUsage:              "false",
		cfgEnableSanitizationIngest: "true",
		cfgLoggingEndpoint:          srv.srv.URL,
	}}); err != nil {
		t.Fatalf("Open enabled/offline: %v", err)
	}
	if err := offline.Gather(context.Background(), &fakeSink{}); err != nil {
		t.Fatalf("Gather enabled/offline: %v", err)
	}
	if got := len(srv.requests()); got != 0 {
		t.Fatalf("enabled offline logging calls = %d, want 0", got)
	}
}

func TestSanitizationReadFailureHealthFinding(t *testing.T) {
	srv := newSanitizationLogServer(t, nil, http.StatusInternalServerError)
	s := openSanitizationSource(t, srv.srv.URL, nil)
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	findings := sink.findings()
	if len(findings) != 1 {
		t.Fatalf("findings = %d, want one health finding: %+v", len(findings), findings)
	}
	f := findings[0]
	if f.Kind != "health" || f.SubjectKind != subjectArmorSanitization ||
		f.Title != "Vertex Model Armor sanitization-log read failed" {
		t.Fatalf("health finding = %+v, want sanitization read failure", f)
	}
}

func TestSanitizationObservationDeterminism(t *testing.T) {
	srv := newSanitizationLogServer(t, []string{string(mustFixtureBytes(t, "sanitization_entries_page.json"))}, 0)
	s := openSanitizationSource(t, srv.srv.URL, nil)

	var runs [][]model.Observation
	for i := 0; i < 2; i++ {
		sink := &fakeSink{}
		if err := s.gatherSanitization(context.Background(), sink, fixedClock()); err != nil {
			t.Fatalf("gatherSanitization run %d: %v", i, err)
		}
		runs = append(runs, sink.observations())
	}
	if !reflect.DeepEqual(runs[0], runs[1]) {
		t.Fatalf("observations not deterministic:\nrun1=%+v\nrun2=%+v", runs[0], runs[1])
	}
}

func assertNoSanitizationSentinel(t *testing.T, obs []model.Observation) {
	t.Helper()
	sentinels := []string{"SENTINEL-PROMPT-TEXT", "SENTINEL-SDP-QUOTE"}
	check := func(field, value string) {
		for _, sentinel := range sentinels {
			if strings.Contains(value, sentinel) {
				t.Fatalf("%s contains sentinel %q in %q", field, sentinel, value)
			}
		}
	}
	for _, o := range obs {
		switch v := o.(type) {
		case model.FindingReport:
			check("finding.kind", v.Kind)
			check("finding.subject_kind", v.SubjectKind)
			check("finding.subject_ref", v.SubjectRef)
			check("finding.title", v.Title)
			check("finding.detail_hash", v.DetailHash)
			for _, ref := range v.OWASPLLM {
				check("finding.owasp_llm", ref)
			}
			for _, ref := range v.OWASPASI {
				check("finding.owasp_asi", ref)
			}
			for _, ref := range v.ATLAS {
				check("finding.atlas", ref)
			}
		case model.MetricSample:
			check("metric.name", v.Name)
			check("metric.unit", v.Unit)
			check("metric.subject_kind", v.SubjectKind)
			check("metric.subject_ref", v.SubjectRef)
			for k, val := range v.Dimensions {
				check("metric.dimension.key", k)
				check("metric.dimension.value", val)
			}
			for k, val := range v.Labels {
				check("metric.label.key", k)
				check("metric.label.value", val)
			}
		}
	}
}
