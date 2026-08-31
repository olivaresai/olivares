// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// bedrockFixture is a Bedrock control-plane fixture server. It serves raw JSON per
// path and records every request so a test can assert the reads are GETs.
type bedrockFixture struct {
	list    string            // ListGuardrails response JSON
	get     map[string]string // guardrail id → GetGuardrail response JSON
	logging string            // GetModelInvocationLoggingConfiguration response JSON

	mu   sync.Mutex
	reqs []*http.Request
}

func (b *bedrockFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	b.reqs = append(b.reqs, r)
	b.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.URL.Path == "/guardrails":
		_, _ = w.Write([]byte(b.list))
	case strings.HasPrefix(r.URL.Path, "/guardrails/"):
		id := strings.TrimPrefix(r.URL.Path, "/guardrails/")
		body, ok := b.get[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(body))
	case r.URL.Path == "/logging/modelinvocations":
		_, _ = w.Write([]byte(b.logging))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (b *bedrockFixture) methods() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, r := range b.reqs {
		out = append(out, r.Method)
	}
	return out
}

// newBedrockSource opens a Source pointed at the fixture server, with only Bedrock
// enabled.
func newBedrockSource(t *testing.T, srvURL string) *Source {
	t.Helper()
	s := New()
	settings := map[string]string{
		cfgEnableIAM:        "false",
		cfgEnableCloudTrail: "false",
		cfgEnableBedrock:    "true",
		cfgBedrockEndpoint:  srvURL,
		cfgAccountID:        "123456789012",
		cfgRegion:           "us-east-1",
	}
	for k, v := range testCreds {
		settings[k] = v
	}
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func postureFindings(fs []model.FindingReport) []model.FindingReport {
	var out []model.FindingReport
	for _, f := range fs {
		if f.Kind == safetyPostureKind {
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

func TestBedrock_HealthyGuardrailAndLogging(t *testing.T) {
	fx := &bedrockFixture{
		list: `{"guardrails":[{"id":"gr-1","arn":"arn:aws:bedrock:us-east-1:123456789012:guardrail/gr-1","name":"prod-guard","status":"READY","version":"DRAFT"}]}`,
		get: map[string]string{
			"gr-1": `{"guardrailId":"gr-1","name":"prod-guard","status":"READY","version":"DRAFT",
				"contentPolicy":{"filters":[{"type":"HATE"},{"type":"VIOLENCE"},{"type":"PROMPT_ATTACK"}]},
				"sensitiveInformationPolicy":{"piiEntities":[{},{}]},
				"contextualGroundingPolicy":{"filters":[{}]}}`,
		},
		logging: `{"loggingConfig":{"s3Config":{"bucketName":"audit-bucket"},"textDataDeliveryEnabled":true}}`,
	}
	srv := httptest.NewServer(fx)
	defer srv.Close()

	s := newBedrockSource(t, srv.URL)
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	fs := postureFindings(sink.findings())
	if len(fs) != 2 {
		t.Fatalf("emitted %d posture findings, want 2 (guardrail + logging): %+v", len(fs), fs)
	}
	g, ok := findingBySubject(fs, subjectBedrockGuardrail)
	if !ok || g.Severity != model.SeverityInfo {
		t.Fatalf("healthy guardrail finding = %+v ok=%v", g, ok)
	}
	lg, ok := findingBySubject(fs, subjectBedrockLogging)
	if !ok || lg.Severity != model.SeverityInfo {
		t.Fatalf("logging-enabled finding = %+v ok=%v", lg, ok)
	}
	// Read-only: every Bedrock call is a GET.
	for _, m := range fx.methods() {
		if m != http.MethodGet {
			t.Fatalf("non-GET Bedrock request: %s", m)
		}
	}
}

func TestBedrock_GapsAndLoggingDisabled(t *testing.T) {
	fx := &bedrockFixture{
		// Two guardrails: one missing PROMPT_ATTACK, one not yet READY.
		list: `{"guardrails":[
			{"id":"gr-weak","name":"weak","status":"READY","version":"DRAFT"},
			{"id":"gr-creating","name":"new","status":"CREATING","version":"DRAFT"}]}`,
		get: map[string]string{
			"gr-weak":     `{"guardrailId":"gr-weak","name":"weak","status":"READY","contentPolicy":{"filters":[{"type":"HATE"}]}}`,
			"gr-creating": `{"guardrailId":"gr-creating","name":"new","status":"CREATING","contentPolicy":{"filters":[{"type":"PROMPT_ATTACK"}]}}`,
		},
		logging: `{}`, // loggingConfig absent ⇒ OFF
	}
	srv := httptest.NewServer(fx)
	defer srv.Close()

	s := newBedrockSource(t, srv.URL)
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	fs := postureFindings(sink.findings())

	mediumGuardrails := 0
	for _, f := range fs {
		if f.SubjectKind == subjectBedrockGuardrail && f.Severity == model.SeverityMedium {
			mediumGuardrails++
		}
	}
	if mediumGuardrails != 2 {
		t.Fatalf("medium guardrail findings = %d, want 2 (no-prompt-attack + not-READY): %+v", mediumGuardrails, fs)
	}
	lg, ok := findingBySubject(fs, subjectBedrockLogging)
	if !ok || lg.Severity != model.SeverityMedium {
		t.Fatalf("logging-disabled finding = %+v ok=%v, want Medium", lg, ok)
	}
}

func TestBedrock_ZeroGuardrails(t *testing.T) {
	fx := &bedrockFixture{
		list:    `{"guardrails":[]}`,
		logging: `{"loggingConfig":{"cloudWatchConfig":{"logGroupName":"/bedrock"},"textDataDeliveryEnabled":true}}`,
	}
	srv := httptest.NewServer(fx)
	defer srv.Close()

	s := newBedrockSource(t, srv.URL)
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	fs := postureFindings(sink.findings())
	g, ok := findingBySubject(fs, subjectBedrockGuardrail)
	if !ok || g.Severity != model.SeverityMedium || !strings.Contains(g.Title, "No Bedrock guardrails") {
		t.Fatalf("zero-guardrail finding = %+v ok=%v", g, ok)
	}
}

// TestBedrock_TruncationSignal proves that when the per-guardrail config reads are
// bounded below the discovered count, an honest Low partial-coverage finding is
// emitted (the "no silent caps" discipline) rather than a posture presented as full.
func TestBedrock_TruncationSignal(t *testing.T) {
	fx := &bedrockFixture{
		list: `{"guardrails":[{"id":"gr-a","name":"a","status":"READY"},{"id":"gr-b","name":"b","status":"READY"}]}`,
		get: map[string]string{
			"gr-a": `{"status":"READY","contentPolicy":{"filters":[{"type":"PROMPT_ATTACK"}]}}`,
		},
		logging: `{}`,
	}
	srv := httptest.NewServer(fx)
	defer srv.Close()

	s := New()
	settings := map[string]string{
		cfgEnableIAM:        "false",
		cfgEnableCloudTrail: "false",
		cfgEnableBedrock:    "true",
		cfgBedrockEndpoint:  srv.URL,
		cfgAccountID:        "123456789012",
		cfgRegion:           "us-east-1",
		cfgMaxGuardrails:    "1", // discovered 2, read 1 ⇒ partial
	}
	for k, v := range testCreds {
		settings[k] = v
	}
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	partial := false
	for _, f := range postureFindings(sink.findings()) {
		if f.Severity == model.SeverityLow && strings.Contains(f.Title, "PARTIAL") {
			partial = true
		}
	}
	if !partial {
		t.Fatalf("expected a Low partial-coverage finding when read < discovered, got %+v", sink.findings())
	}
}

// TestBedrock_HealthFindingOnReadFailure proves a Bedrock read failure yields exactly
// one health finding (hashed detail), never a fabricated empty posture.
func TestBedrock_HealthFindingOnReadFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	s := newBedrockSource(t, srv.URL)
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	fs := sink.findings()
	if len(fs) != 1 || fs[0].Kind != "health" || fs[0].SubjectKind != subjectBedrock {
		t.Fatalf("expected one bedrock health finding, got %+v", fs)
	}
	// No safety-posture finding was fabricated on a failed read.
	if len(postureFindings(fs)) != 0 {
		t.Fatal("a read failure must not fabricate a safety posture")
	}
}

// TestBedrock_DedupStable proves a guardrail's posture DetailHash depends only on its
// config state (not the per-pass timestamp), so an unchanged guardrail dedups.
func TestBedrock_DedupStable(t *testing.T) {
	t1 := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC) // a different pass timestamp
	f1 := bedrockPostureFinding(model.SeverityMedium, subjectBedrockGuardrail, "acct/us-east-1", "t", "bedrock.guardrail id=gr-1 status=READY content=1", t1)
	f2 := bedrockPostureFinding(model.SeverityMedium, subjectBedrockGuardrail, "acct/us-east-1", "t", "bedrock.guardrail id=gr-1 status=READY content=1", t2)
	if f1.DetailHash != f2.DetailHash {
		t.Fatal("identical config across passes must hash identically (dedup would break)")
	}
	f3 := bedrockPostureFinding(model.SeverityMedium, subjectBedrockGuardrail, "acct/us-east-1", "t", "bedrock.guardrail id=gr-1 status=READY content=2", t1)
	if f3.DetailHash == f1.DetailHash {
		t.Fatal("a config change must produce a different hash")
	}
}
