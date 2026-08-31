// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package clauderoutines

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

var testTime = time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)

func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

// --- routineEdge ---

func TestRoutineEdgeShape(t *testing.T) {
	tr := trigger{
		ID:        "trig_abc",
		Name:      "daily audit",
		CreatedAt: testTime.Add(-24 * time.Hour).Format(time.RFC3339),
	}
	e := routineEdge(tr, "org_42", testTime)
	if e.OriginKind != originWorkspace || e.OriginRef != "org_42" {
		t.Errorf("origin = %s/%s, want workspace/org_42", e.OriginKind, e.OriginRef)
	}
	if e.ResourceKind != kindRoutine || e.ResourceRef != "trig_abc" {
		t.Errorf("resource = %s/%s, want %s/trig_abc", e.ResourceKind, e.ResourceRef, kindRoutine)
	}
	if e.Mode != model.ModeRead {
		t.Errorf("mode = %v, want read", e.Mode)
	}
	if e.Source != model.SignalCMA {
		t.Errorf("source = %v, want SignalCMA", e.Source)
	}
	if e.ToolRef != "daily audit" {
		t.Errorf("ToolRef = %q, want %q", e.ToolRef, "daily audit")
	}
}

func TestRoutineEdgeDefaultOrg(t *testing.T) {
	e := routineEdge(trigger{ID: "trig_x"}, "", testTime)
	if e.OriginRef != "organization" {
		t.Errorf("empty orgID should default to %q, got %q", "organization", e.OriginRef)
	}
}

func TestRoutineEdgeCreatedAtFallback(t *testing.T) {
	e := routineEdge(trigger{ID: "trig_x"}, "", testTime)
	if !e.ObservedAt.Equal(testTime) {
		t.Errorf("a trigger with no created_at should use the observation time as fallback, got %v", e.ObservedAt)
	}
}

// --- cadenceFinding ---

func TestCadenceFindingTriggersOnExcessiveCadence(t *testing.T) {
	tr := trigger{
		ID:             "trig_fast",
		Name:           "too fast",
		Enabled:        true,
		CronExpression: "*/5 * * * *", // every 5 minutes = 300s
	}
	f, ok := cadenceFinding(tr, 3600, testTime)
	if !ok {
		t.Fatal("a 5-minute cron should trigger against a 1h floor")
	}
	if f.Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High", f.Severity)
	}
	if f.SubjectKind != kindRoutine || f.SubjectRef != "trig_fast" {
		t.Errorf("subject = %s/%s", f.SubjectKind, f.SubjectRef)
	}
	if f.DetailHash == "" {
		t.Error("finding must carry a hashed detail, never raw")
	}
}

func TestCadenceFindingDoesNotTriggerOnCompliant(t *testing.T) {
	tr := trigger{
		ID:             "trig_ok",
		Enabled:        true,
		CronExpression: "0 */2 * * *", // every 2 hours = 7200s
	}
	if _, ok := cadenceFinding(tr, 3600, testTime); ok {
		t.Error("a 2-hour cron should not trigger against a 1h floor")
	}
}

func TestCadenceFindingDisabledTrigger(t *testing.T) {
	tr := trigger{
		ID:             "trig_dis",
		Enabled:        false,
		CronExpression: "*/1 * * * *",
	}
	if _, ok := cadenceFinding(tr, 3600, testTime); ok {
		t.Error("a disabled trigger should not raise a cadence finding")
	}
}

func TestCadenceFindingNoCron(t *testing.T) {
	tr := trigger{
		ID:      "trig_once",
		Enabled: true,
		// RunOnceAt trigger — no cron expression
	}
	if _, ok := cadenceFinding(tr, 3600, testTime); ok {
		t.Error("a trigger with no cron expression should not raise a cadence finding")
	}
}

// --- reviewFinding ---

func TestReviewFindingTriggersOnOldRoutine(t *testing.T) {
	tr := trigger{
		ID:        "trig_old",
		Name:      "ancient",
		Enabled:   true,
		CreatedAt: testTime.Add(-60 * 24 * time.Hour).Format(time.RFC3339), // 60 days ago
	}
	f, ok := reviewFinding(tr, 30, testTime)
	if !ok {
		t.Fatal("a 60-day-old routine should trigger against a 30-day review window")
	}
	if f.Severity != model.SeverityMedium {
		t.Errorf("severity = %v, want Medium", f.Severity)
	}
	if f.DetailHash == "" {
		t.Error("finding must carry a hashed detail, never raw")
	}
}

func TestReviewFindingDoesNotTriggerOnRecent(t *testing.T) {
	tr := trigger{
		ID:        "trig_new",
		Enabled:   true,
		CreatedAt: testTime.Add(-7 * 24 * time.Hour).Format(time.RFC3339), // 7 days ago
	}
	if _, ok := reviewFinding(tr, 30, testTime); ok {
		t.Error("a 7-day-old routine should not trigger against a 30-day window")
	}
}

func TestReviewFindingDisabledTrigger(t *testing.T) {
	tr := trigger{
		ID:        "trig_dis",
		Enabled:   false,
		CreatedAt: testTime.Add(-90 * 24 * time.Hour).Format(time.RFC3339),
	}
	if _, ok := reviewFinding(tr, 30, testTime); ok {
		t.Error("a disabled trigger should not raise a review finding")
	}
}

func TestReviewFindingEndedTrigger(t *testing.T) {
	tr := trigger{
		ID:          "trig_ended",
		Enabled:     true,
		EndedReason: "run_once_fired",
		CreatedAt:   testTime.Add(-90 * 24 * time.Hour).Format(time.RFC3339),
	}
	if _, ok := reviewFinding(tr, 30, testTime); ok {
		t.Error("a trigger with ended_reason should not raise a review finding")
	}
}

// --- nameFinding ---

func TestNameFindingTriggersOnAnonymous(t *testing.T) {
	tr := trigger{ID: "trig_anon"}
	f, ok := nameFinding(tr, testTime)
	if !ok {
		t.Fatal("a trigger with no name should produce a finding")
	}
	if f.Severity != model.SeverityLow || f.Kind != findingPosture {
		t.Errorf("severity/kind = %v/%s, want Low/posture", f.Severity, f.Kind)
	}
}

func TestNameFindingDoesNotTriggerOnNamed(t *testing.T) {
	tr := trigger{ID: "trig_named", Name: "weekly report"}
	if _, ok := nameFinding(tr, testTime); ok {
		t.Error("a named trigger should not produce a name finding")
	}
}

// --- estimateCronInterval ---

func TestEstimateCronInterval(t *testing.T) {
	cases := []struct {
		expr string
		want int
	}{
		{"*/5 * * * *", 300},  // every 5 minutes
		{"*/15 * * * *", 900}, // every 15 minutes
		{"*/1 * * * *", 60},   // every minute
		{"0 */2 * * *", 7200}, // every 2 hours
		{"0 */1 * * *", 3600}, // every hour
		{"30 * * * *", 3600},  // fixed minute, every hour
		{"0 9 * * *", 0},      // daily at 9am — no simple interval
		{"0 0 * * 0", 0},      // weekly — no simple interval
		{"invalid", 0},        // garbage
		{"* * * * * *", 0},    // 6 fields
	}
	for _, tc := range cases {
		if got := estimateCronInterval(tc.expr); got != tc.want {
			t.Errorf("estimateCronInterval(%q) = %d, want %d", tc.expr, got, tc.want)
		}
	}
}

// --- integration: refreshOnce ---

type fakeSink struct {
	mu  sync.Mutex
	obs []model.Observation
}

func (f *fakeSink) Emit(_ context.Context, o model.Observation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.obs = append(f.obs, o)
	return nil
}

var _ sdk.Sink = (*fakeSink)(nil)

func (f *fakeSink) all() []model.Observation {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]model.Observation(nil), f.obs...)
}

func (f *fakeSink) findings() []model.FindingReport {
	var out []model.FindingReport
	for _, o := range f.all() {
		if fr, ok := o.(model.FindingReport); ok {
			out = append(out, fr)
		}
	}
	return out
}

func (f *fakeSink) edges() []model.EdgeObservation {
	var out []model.EdgeObservation
	for _, o := range f.all() {
		if e, ok := o.(model.EdgeObservation); ok {
			out = append(out, e)
		}
	}
	return out
}

func openTestSource(t *testing.T, settings map[string]string) *Source {
	t.Helper()
	s := &Source{now: fixedClock(testTime)}
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestRefreshOnceEmitsInventory(t *testing.T) {
	created := testTime.Add(-3 * time.Hour).Format(time.RFC3339)
	old := testTime.Add(-60 * 24 * time.Hour).Format(time.RFC3339)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"triggers":[
			{"id":"trig_1","name":"hourly scan","enabled":true,"cron_expression":"*/5 * * * *","created_at":"` + created + `"},
			{"id":"trig_2","name":"","enabled":true,"cron_expression":"0 */2 * * *","created_at":"` + old + `"},
			{"id":"trig_3","name":"one-shot","enabled":true,"run_once_at":"2026-07-01T00:00:00Z","created_at":"` + created + `","ended_reason":"run_once_fired"}
		]}`))
	}))
	defer srv.Close()

	s := openTestSource(t, map[string]string{
		cfgAPIKey:  "sk-ant-test",
		cfgBaseURL: srv.URL,
	})
	sink := &fakeSink{}
	s.refreshOnce(context.Background(), sink)

	edges := sink.edges()
	if len(edges) != 3 {
		t.Fatalf("expected 3 edges (one per trigger), got %d", len(edges))
	}
	for _, e := range edges {
		if e.ResourceKind != kindRoutine || e.Source != model.SignalCMA {
			t.Errorf("unexpected edge %+v", e)
		}
	}

	findings := sink.findings()
	var cadence, review, name int
	for _, f := range findings {
		switch {
		case f.Severity == model.SeverityHigh && f.SubjectRef == "trig_1":
			cadence++
		case f.Severity == model.SeverityMedium && f.SubjectRef == "trig_2":
			review++
		case f.Severity == model.SeverityLow && f.SubjectRef == "trig_2" && f.Kind == findingPosture:
			name++
		}
	}
	if cadence != 1 {
		t.Errorf("expected 1 cadence finding for trig_1, got %d", cadence)
	}
	if review != 1 {
		t.Errorf("expected 1 review finding for trig_2, got %d", review)
	}
	if name != 1 {
		t.Errorf("expected 1 name finding for trig_2, got %d", name)
	}
}

func TestRefreshDegradesHonestly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	s := openTestSource(t, map[string]string{
		cfgAPIKey:  "sk-ant-test",
		cfgBaseURL: srv.URL,
	})
	sink := &fakeSink{}
	s.refreshOnce(context.Background(), sink)

	findings := sink.findings()
	if len(findings) != 1 || findings[0].Kind != findingSelfAudit || findings[0].SubjectKind != connectorSubject {
		t.Fatalf("a failed fetch must emit a self-audit degrade finding, got %+v", findings)
	}
}

func TestGatherReturnsOnCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"triggers":[]}`))
	}))
	defer srv.Close()

	s := openTestSource(t, map[string]string{
		cfgAPIKey:  "sk-ant-test",
		cfgBaseURL: srv.URL,
		cfgRefresh: "1h",
	})
	sink := &fakeSink{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Gather(ctx, sink) }()

	// Wait for the immediate first refresh pass
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("Gather returned %v, want context.Canceled/nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Gather did not return after ctx cancel")
	}
}
