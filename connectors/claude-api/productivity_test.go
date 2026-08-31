// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// TestClaudeCodeProductivityFinding_EmptyIsNoOp proves a record with no productivity
// signal is not ledgered as a fabricated zero.
func TestClaudeCodeProductivityFinding_EmptyIsNoOp(t *testing.T) {
	if _, ok := claudeCodeProductivityFinding(claudeCodeRecord{Date: "2026-06-02"}, fixedClock()); ok {
		t.Fatal("an empty record must not produce a productivity finding")
	}
}

// TestClaudeCodeProductivityFinding_Populated proves the per-tool accept/reject tally is
// summed correctly, the title carries the ROI counts, and the finding is Info evidence.
func TestClaudeCodeProductivityFinding_Populated(t *testing.T) {
	rec := claudeCodeRecord{
		Date:         "2026-06-02",
		CustomerType: "subscription",
		Actor:        claudeCodeActor{Type: "user_actor", EmailAddress: "dev@corp.example"},
		CoreMetrics: claudeCodeCoreMetrics{
			NumSessions: 3, LinesOfCode: claudeCodeLOCSpan{Added: 200, Removed: 40}, Commits: 5, PullRequests: 2,
		},
		ToolActions: claudeCodeToolActions{
			Edit:      claudeCodeToolTally{Accepted: 10, Rejected: 2},
			MultiEdit: claudeCodeToolTally{Accepted: 3, Rejected: 1},
			Write:     claudeCodeToolTally{Accepted: 4, Rejected: 0},
		},
	}
	f, ok := claudeCodeProductivityFinding(rec, fixedClock())
	if !ok {
		t.Fatal("a populated record must produce a finding")
	}
	if f.Kind != "analytics" || f.SubjectKind != subjectClaudeCodeDeveloper || f.Severity != model.SeverityInfo {
		t.Errorf("finding shape = %s/%s/%s", f.Kind, f.SubjectKind, f.Severity)
	}
	if f.SubjectRef != "dev@corp.example" {
		t.Errorf("subject ref = %q, want the developer attribution identity", f.SubjectRef)
	}
	// 17 accepted (10+3+4), 3 rejected (2+1+0) => "17 of 20".
	if !strings.Contains(f.Title, "17 of 20 tool action(s) accepted") {
		t.Errorf("title %q missing the accept tally", f.Title)
	}
	if !strings.Contains(f.Title, "3 session(s)") || !strings.Contains(f.Title, "+200/-40 LOC") {
		t.Errorf("title %q missing the ROI counts", f.Title)
	}
	if f.DetailHash == "" {
		t.Error("missing DetailHash")
	}
}

// TestClaudeCodeMetricSamples_EmptyOrNoActor proves an empty record and an actor-less
// record both yield no metric samples (never a fabricated zero / subjectless sample).
func TestClaudeCodeMetricSamples_EmptyOrNoActor(t *testing.T) {
	if s := claudeCodeMetricSamples(claudeCodeRecord{Date: "2026-06-02"}, fixedClock()); len(s) != 0 {
		t.Fatalf("empty record must yield no samples, got %d", len(s))
	}
	rec := claudeCodeRecord{CoreMetrics: claudeCodeCoreMetrics{NumSessions: 3}} // no actor
	if s := claudeCodeMetricSamples(rec, fixedClock()); len(s) != 0 {
		t.Fatalf("actor-less record must yield no samples, got %d", len(s))
	}
}

// TestClaudeCodeMetricSamples_Populated proves the decomposition into queryable adoption
// MetricSamples: per-developer daily snapshots (Additive=false), only non-zero measures,
// with the right names/units/dimensions for LoC add/remove, per-tool accept/reject, and
// per-model token split.
func TestClaudeCodeMetricSamples_Populated(t *testing.T) {
	at := fixedClock()
	rec := claudeCodeRecord{
		Date:         "2026-06-02",
		CustomerType: "subscription",
		Actor:        claudeCodeActor{Type: "user_actor", EmailAddress: "dev@corp.example"},
		CoreMetrics: claudeCodeCoreMetrics{
			NumSessions: 3, LinesOfCode: claudeCodeLOCSpan{Added: 200, Removed: 40}, Commits: 5, PullRequests: 2,
		},
		ToolActions: claudeCodeToolActions{
			Edit:  claudeCodeToolTally{Accepted: 10, Rejected: 2},
			Write: claudeCodeToolTally{Accepted: 4, Rejected: 0}, // reject=0 must NOT emit
		},
		ModelBreakdown: []claudeCodeModelSpan{
			{Model: "claude-opus-4-8", Tokens: claudeCodeTokens{Input: 1000, Output: 500, CacheRead: 800, CacheCreation: 0}},
		},
	}
	samples := claudeCodeMetricSamples(rec, at)

	// Every sample is a per-developer daily snapshot for the right subject/time.
	for _, ms := range samples {
		if ms.SubjectKind != subjectDeveloper || ms.SubjectRef != "dev@corp.example" {
			t.Errorf("subject = %s/%s", ms.SubjectKind, ms.SubjectRef)
		}
		if ms.Additive {
			t.Errorf("Analytics daily totals must be snapshots (Additive=false): %+v", ms)
		}
		if !ms.OccurredAt.Equal(at) {
			t.Errorf("OccurredAt = %v, want %v", ms.OccurredAt, at)
		}
		if ms.Value <= 0 {
			t.Errorf("zero/negative measures must not be emitted: %+v", ms)
		}
	}

	find := func(name string, dims map[string]string) (model.MetricSample, bool) {
		for _, ms := range samples {
			if ms.Name != name {
				continue
			}
			ok := true
			for k, v := range dims {
				if ms.Dimensions[k] != v {
					ok = false
					break
				}
			}
			if ok && len(ms.Dimensions) == len(dims) {
				return ms, true
			}
		}
		return model.MetricSample{}, false
	}
	mustVal := func(name string, dims map[string]string, want int64, unit string) {
		ms, ok := find(name, dims)
		if !ok {
			t.Fatalf("missing sample %s %v", name, dims)
		}
		if ms.Value != want || ms.Unit != unit {
			t.Errorf("%s %v = value %d unit %q, want %d %q", name, dims, ms.Value, ms.Unit, want, unit)
		}
	}
	mustVal(metricSessionCount, nil, 3, "sessions")
	mustVal(metricLinesOfCode, map[string]string{"type": "added"}, 200, "lines")
	mustVal(metricLinesOfCode, map[string]string{"type": "removed"}, 40, "lines")
	mustVal(metricCommit, nil, 5, "commits")
	mustVal(metricPullRequest, nil, 2, "pull_requests")
	mustVal(metricCodeEditDecision, map[string]string{"tool": "Edit", "decision": "accept"}, 10, "decisions")
	mustVal(metricCodeEditDecision, map[string]string{"tool": "Edit", "decision": "reject"}, 2, "decisions")
	mustVal(metricCodeEditDecision, map[string]string{"tool": "Write", "decision": "accept"}, 4, "decisions")
	mustVal(metricTokenUsage, map[string]string{"type": "input", "model": "claude-opus-4-8"}, 1000, "tokens")
	mustVal(metricTokenUsage, map[string]string{"type": "cacheRead", "model": "claude-opus-4-8"}, 800, "tokens")

	// Zero measures are absent: Write reject (0) and cacheCreation (0) emit nothing.
	if _, ok := find(metricCodeEditDecision, map[string]string{"tool": "Write", "decision": "reject"}); ok {
		t.Error("a zero tool tally must not emit a sample")
	}
	if _, ok := find(metricTokenUsage, map[string]string{"type": "cacheCreation", "model": "claude-opus-4-8"}); ok {
		t.Error("a zero token bucket must not emit a sample")
	}
}

// usageSpeedDoer returns a one-row usage page for any usage call and records the speeds[]
// filter + the anthropic-beta header so the fast-mode split can be asserted.
type usageSpeedDoer struct {
	speeds []string
	betas  []string
}

func (d *usageSpeedDoer) Do(req *http.Request) (*http.Response, error) {
	d.speeds = append(d.speeds, req.URL.Query().Get("speeds[]"))
	d.betas = append(d.betas, req.Header.Get("anthropic-beta"))
	const body = `{"data":[{"starting_at":"2026-06-01T00:00:00Z","results":[{"model":"claude-opus-4-8","uncached_input_tokens":100,"output_tokens":50}]}],"has_more":false}`
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
}

// TestFastModeOff_SingleUntaggedPull proves the default is one usage pull with no speed
// filter and no speed tag (unchanged behavior).
func TestFastModeOff_SingleUntaggedPull(t *testing.T) {
	doer := &usageSpeedDoer{}
	s := New()
	s.doer = doer
	s.now = fixedClock
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"admin_key": "sk-ant-admin-test"}}); err != nil {
		t.Fatal(err)
	}
	sink := &captureSink{}
	if err := s.gatherUsage(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	if len(doer.speeds) != 1 {
		t.Fatalf("fast_mode off must do 1 usage pull, got %d", len(doer.speeds))
	}
	if doer.speeds[0] != "" || doer.betas[0] != "" {
		t.Errorf("default pull must send no speeds[]/beta, got speeds=%q beta=%q", doer.speeds[0], doer.betas[0])
	}
	for _, o := range sink.obs {
		if cs, ok := o.(model.CostSample); ok && cs.Speed != "" {
			t.Errorf("default pull must not tag Speed, got %q", cs.Speed)
		}
	}
}

// TestFastModeOn_SplitsBySpeed proves fast-mode issues one beta-gated, speed-filtered
// pull per band and tags each emitted CostSample with the requested speed.
func TestFastModeOn_SplitsBySpeed(t *testing.T) {
	doer := &usageSpeedDoer{}
	s := New()
	s.doer = doer
	s.now = fixedClock
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"admin_key": "sk-ant-admin-test", "fast_mode": "true"}}); err != nil {
		t.Fatal(err)
	}
	sink := &captureSink{}
	if err := s.gatherUsage(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	if len(doer.speeds) != 2 {
		t.Fatalf("fast_mode on must do 2 usage pulls (standard, fast), got %d", len(doer.speeds))
	}
	gotSpeeds := map[string]bool{doer.speeds[0]: true, doer.speeds[1]: true}
	if !gotSpeeds[speedStandard] || !gotSpeeds[speedFast] {
		t.Errorf("speeds = %v, want both standard and fast", doer.speeds)
	}
	for i, b := range doer.betas {
		if b != fastModeBeta {
			t.Errorf("call %d beta header = %q, want %q", i, b, fastModeBeta)
		}
	}
	tagged := map[string]bool{}
	for _, o := range sink.obs {
		if cs, ok := o.(model.CostSample); ok {
			tagged[cs.Speed] = true
		}
	}
	if !tagged[speedStandard] || !tagged[speedFast] {
		t.Errorf("emitted speed tags = %v, want both standard and fast", tagged)
	}
}

// keyDoer returns a fixed api_keys page for the lifecycle test.
type keyDoer struct{ body string }

func (d *keyDoer) Do(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(d.body)), Header: make(http.Header)}, nil
}

// TestKeyLifecycle_FlagsHygieneGaps proves an active key with no expiry, and one expiring
// within the warn window, are flagged Medium — while an inactive key and a comfortably-
// future key are not.
func TestKeyLifecycle_FlagsHygieneGaps(t *testing.T) {
	// Clock is 2026-06-02. apikey_2 expires in 3 days (within 14d window); apikey_4 in
	// 2030 (healthy). apikey_3 is inactive (skipped). apikey_1 has no expiry.
	doer := &keyDoer{body: `{"data":[
		{"id":"apikey_1","status":"active","name":"no-expiry"},
		{"id":"apikey_2","status":"active","name":"soon","expires_at":"2026-06-05T00:00:00Z"},
		{"id":"apikey_3","status":"inactive","name":"off"},
		{"id":"apikey_4","status":"active","name":"healthy","expires_at":"2030-01-01T00:00:00Z"}
	],"has_more":false}`}
	s := New()
	s.doer = doer
	s.now = fixedClock
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"admin_key": "sk-ant-admin-test"}}); err != nil {
		t.Fatal(err)
	}
	sink := &captureSink{}
	if err := s.gatherKeyLifecycle(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	bySubject := map[string]model.FindingReport{}
	for _, o := range sink.obs {
		f := o.(model.FindingReport)
		if f.SubjectKind != subjectAPIKey || f.Severity != model.SeverityMedium {
			t.Errorf("unexpected finding %s/%s", f.SubjectKind, f.Severity)
		}
		bySubject[f.SubjectRef] = f
	}
	if _, ok := bySubject["apikey_1"]; !ok {
		t.Error("active key with no expiry must be flagged")
	}
	if _, ok := bySubject["apikey_2"]; !ok {
		t.Error("active key expiring within the window must be flagged")
	}
	if _, ok := bySubject["apikey_3"]; ok {
		t.Error("inactive key must NOT be flagged (not a live rotation surface)")
	}
	if _, ok := bySubject["apikey_4"]; ok {
		t.Error("healthy key with comfortably-future expiry must NOT be flagged")
	}
}
