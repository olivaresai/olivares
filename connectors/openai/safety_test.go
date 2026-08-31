// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package openai

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// findingsOf returns the safety_posture findings captured by the sink.
func findingsOf(t *testing.T, obs []model.Observation) []model.FindingReport {
	t.Helper()
	var out []model.FindingReport
	for _, o := range obs {
		if f, ok := o.(model.FindingReport); ok {
			out = append(out, f)
		}
	}
	return out
}

func findingBySubject(fs []model.FindingReport, subjectKind string) (model.FindingReport, bool) {
	for _, f := range fs {
		if f.SubjectKind == subjectKind {
			return f, true
		}
	}
	return model.FindingReport{}, false
}

func TestSafetyPosture_ModerationInUse(t *testing.T) {
	s, doer := newLive(t) // usage_moderations.json has num_model_requests=42
	sink := &captureSink{}
	if err := s.gatherSafetyPosture(context.Background(), sink); err != nil {
		t.Fatalf("gatherSafetyPosture: %v", err)
	}
	fs := findingsOf(t, sink.obs)
	if len(fs) != 2 {
		t.Fatalf("emitted %d posture findings, want 2", len(fs))
	}
	f, ok := findingBySubject(fs, subjectModeration)
	if !ok {
		t.Fatalf("missing moderation finding: %+v", fs)
	}
	if f.Kind != "safety_posture" || f.SubjectKind != "openai.moderation" || f.SubjectRef != "organization" {
		t.Fatalf("posture finding shape = %+v", f)
	}
	if f.Severity != model.SeverityInfo {
		t.Fatalf("in-use severity = %q, want info", f.Severity)
	}
	if f.DetailHash == "" {
		t.Fatal("posture finding has no DetailHash")
	}
	// Minimal-data: the title must not carry the fluctuating request count (the
	// count rides only the hashed detail's present/absent state, so dedup is stable).
	if strings.Contains(f.Title, "42") {
		t.Fatalf("title leaks the request count: %q", f.Title)
	}
	// Read-only: the moderation usage read is a GET on the usage/moderations bucket.
	if len(doer.reqs) != 1 || doer.reqs[0].Method != http.MethodGet {
		t.Fatalf("expected one GET, got %d reqs", len(doer.reqs))
	}
	if doer.reqs[0].URL.Path != usageModerationsPath {
		t.Fatalf("moderation read path = %q", doer.reqs[0].URL.Path)
	}
	// The usage limit must be the per-bucket-width cap (1d→31), not the list-endpoint
	// 100 that the Usage API rejects with 400.
	if got := doer.reqs[0].URL.Query().Get("limit"); got != "31" {
		t.Fatalf("moderation usage limit = %q, want 31 (1d bucket-width cap)", got)
	}
}

func TestSafetyPosture_SafetyDashboardCoverageCaveat(t *testing.T) {
	s, _ := newLive(t)
	sink := &captureSink{}
	if err := s.gatherSafetyPosture(context.Background(), sink); err != nil {
		t.Fatalf("gatherSafetyPosture: %v", err)
	}
	fs := findingsOf(t, sink.obs)
	f, ok := findingBySubject(fs, subjectSafetyDashboard)
	if !ok {
		t.Fatalf("missing dashboard-only safety coverage finding: %+v", fs)
	}
	if f.Kind != safetyPostureKind || f.Severity != model.SeverityInfo {
		t.Fatalf("dashboard coverage finding shape = %+v", f)
	}
	if !strings.Contains(f.Title, "dashboard-only") || !strings.Contains(f.Title, "no org-level API") {
		t.Fatalf("dashboard coverage title = %q", f.Title)
	}
}

// errModerationDoer returns an upstream 500 for the moderation usage read.
type errModerationDoer struct{}

func (d *errModerationDoer) Do(req *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
}

// TestSafetyPosture_UnreadableOnError proves a credentialed moderation-read failure
// emits an honest Medium "unreadable" finding and does NOT fail the Gather (so it
// neither fabricates a green nor poisons the cost samples).
func TestSafetyPosture_UnreadableOnError(t *testing.T) {
	s := New()
	s.doer = &errModerationDoer{}
	s.now = fixedClock
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"api_key": "sk-x"}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &captureSink{}
	if err := s.gatherSafetyPosture(context.Background(), sink); err != nil {
		t.Fatalf("gatherSafetyPosture must not fail the pass on a read error: %v", err)
	}
	fs := findingsOf(t, sink.obs)
	f, ok := findingBySubject(fs, subjectModeration)
	if !ok || f.Severity != model.SeverityMedium || !strings.Contains(f.Title, "unreadable") {
		t.Fatalf("expected one Medium 'unreadable' posture finding, got %+v", fs)
	}
	if _, ok := findingBySubject(fs, subjectSafetyDashboard); !ok {
		t.Fatalf("missing dashboard-only coverage finding on unreadable moderation: %+v", fs)
	}
}

// TestSafetyPosture_MaxPagesFloor proves a zero/negative max_pages is floored, so the
// moderation read still runs (never a fabricated "absent" posture with zero requests).
func TestSafetyPosture_MaxPagesFloor(t *testing.T) {
	s := New()
	doer := &fixtureDoer{t: t} // usage_moderations.json has num_model_requests=42
	s.doer = doer
	s.now = fixedClock
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"api_key": "sk-x", "max_pages": "0"}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &captureSink{}
	if err := s.gatherSafetyPosture(context.Background(), sink); err != nil {
		t.Fatalf("gatherSafetyPosture: %v", err)
	}
	fs := findingsOf(t, sink.obs)
	f, ok := findingBySubject(fs, subjectModeration)
	if !ok || f.Severity != model.SeverityInfo {
		t.Fatalf("max_pages=0 must still read (Info in-use), got %+v", fs)
	}
	if len(doer.reqs) == 0 {
		t.Fatal("max_pages=0 floored: the moderation read must still issue a request")
	}
}

// zeroModerationDoer returns an empty moderations usage page (no calls observed).
type zeroModerationDoer struct{ t *testing.T }

func (d *zeroModerationDoer) Do(req *http.Request) (*http.Response, error) {
	if req.URL.Path != usageModerationsPath {
		d.t.Fatalf("unexpected path %q", req.URL.Path)
	}
	const empty = `{"object":"page","data":[],"has_more":false,"next_page":null}`
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(empty)), Header: make(http.Header)}, nil
}

func TestSafetyPosture_NoModerationObserved(t *testing.T) {
	s := New()
	s.doer = &zeroModerationDoer{t: t}
	s.now = fixedClock
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"api_key": "sk-openai-admin-test"}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &captureSink{}
	if err := s.gatherSafetyPosture(context.Background(), sink); err != nil {
		t.Fatalf("gatherSafetyPosture: %v", err)
	}
	fs := findingsOf(t, sink.obs)
	if len(fs) != 2 {
		t.Fatalf("emitted %d posture findings, want 2", len(fs))
	}
	// Absence of moderation usage is a Low governance gap, never High: OpenAI's
	// platform safety still applies, so this is "no application-level evidence",
	// not "unsafe".
	f, ok := findingBySubject(fs, subjectModeration)
	if !ok || f.Severity != model.SeverityLow {
		t.Fatalf("no-usage moderation finding = %+v, want low", fs)
	}
}

// TestSafetyPosture_AzureOpenAISkips proves that in azure-openai mode the connector
// does NOT call the OpenAI-platform moderation usage endpoint (it does not exist on
// Azure; Azure safety = RAI, read by the azure connector) and emits no posture.
func TestSafetyPosture_AzureOpenAISkips(t *testing.T) {
	s := New()
	s.doer = &zeroModerationDoer{t: t} // Do() would Fatal if the endpoint were called
	s.now = fixedClock
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"api_key": "sk-x", "provider": "azure-openai"}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &captureSink{}
	if err := s.gatherSafetyPosture(context.Background(), sink); err != nil {
		t.Fatalf("gatherSafetyPosture: %v", err)
	}
	if len(sink.obs) != 0 {
		t.Fatalf("azure-openai mode must emit no OpenAI moderation posture, got %d", len(sink.obs))
	}
}

// TestSafetyPosture_DedupStable proves the posture DetailHash is state-deterministic:
// two pulls of the same posture produce the SAME hash (so modules/security dedups),
// independent of how many requests were counted within the same present/absent state.
func TestSafetyPosture_DedupStable(t *testing.T) {
	s := New()
	s.now = fixedClock
	hi := s.moderationPostureFinding(42)
	lo := s.moderationPostureFinding(7)
	if hi.DetailHash != lo.DetailHash {
		t.Fatal("present-state DetailHash must not depend on the request count (dedup would break)")
	}
	absent := s.moderationPostureFinding(0)
	if absent.DetailHash == hi.DetailHash {
		t.Fatal("absent-state and present-state must hash differently (a posture change must surface)")
	}
}
