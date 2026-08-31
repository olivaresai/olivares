// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package bedrock

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

// bedrockFixture is a Bedrock control-plane fixture server. It serves raw JSON per path
// and records every request so a test can assert the reads are GETs.
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
	case r.URL.Path == loggingConfigPath:
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

func newGuardrailsSource(t *testing.T, srvURL string, maxGuardrails string) *Source {
	t.Helper()
	s := New()
	settings := map[string]string{
		cfgEnableGuardrails: "true",
		cfgBedrockEndpoint:  srvURL,
		cfgAccountID:        "123456789012",
		cfgRegion:           "us-east-1",
	}
	if maxGuardrails != "" {
		settings[cfgMaxGuardrails] = maxGuardrails
	}
	for k, v := range testCreds {
		settings[k] = v
	}
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestGuardrails_HealthyWithAutomatedReasoning(t *testing.T) {
	fx := &bedrockFixture{
		list: `{"guardrails":[{"id":"gr-1","arn":"arn:aws:bedrock:us-east-1:123456789012:guardrail/gr-1","name":"prod-guard","status":"READY","version":"DRAFT"}]}`,
		get: map[string]string{
			"gr-1": `{"guardrailId":"gr-1","name":"prod-guard","status":"READY","version":"DRAFT",
				"contentPolicy":{"filters":[{"type":"HATE"},{"type":"MISCONDUCT"},{"type":"PROMPT_ATTACK"}]},
				"sensitiveInformationPolicy":{"piiEntities":[{},{}]},
				"contextualGroundingPolicy":{"filters":[{}]},
				"automatedReasoningPolicy":{"policies":["arn:aws:bedrock:us-east-1:123456789012:automated-reasoning-policy/abcdef123456:1"],"confidenceThreshold":0.8}}`,
		},
		logging: `{"loggingConfig":{"s3Config":{"bucketName":"audit-bucket"},"textDataDeliveryEnabled":true}}`,
	}
	srv := httptest.NewServer(fx)
	defer srv.Close()

	s := newGuardrailsSource(t, srv.URL, "")
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

func TestGuardrailHasAutomatedReasoning(t *testing.T) {
	with := getGuardrailResponse{}
	with.AutomatedReasoningPolicy = &struct {
		Policies []string `json:"policies"`
	}{Policies: []string{"arn:...:automated-reasoning-policy/x"}}
	if !guardrailHasAutomatedReasoning(with) {
		t.Error("AR with a policy ARN must be detected as configured")
	}
	empty := getGuardrailResponse{}
	empty.AutomatedReasoningPolicy = &struct {
		Policies []string `json:"policies"`
	}{}
	if guardrailHasAutomatedReasoning(empty) {
		t.Error("AR present but with no policies must NOT count as configured")
	}
	if guardrailHasAutomatedReasoning(getGuardrailResponse{}) {
		t.Error("absent AR must not count as configured")
	}
}

// TestGuardrails_AutomatedReasoningInPosture proves the Automated Reasoning policy (the
// field the aws connector does not read) actually changes the emitted posture: an
// otherwise-identical guardrail with AR attached hashes differently from one without, so
// AR is genuinely captured in the finding (not silently dropped).
func TestGuardrails_AutomatedReasoningInPosture(t *testing.T) {
	getWith := `{"guardrailId":"gr-1","name":"g","status":"READY","contentPolicy":{"filters":[{"type":"PROMPT_ATTACK"}]},"automatedReasoningPolicy":{"policies":["arn:aws:bedrock:us-east-1:123456789012:automated-reasoning-policy/abcdef123456:1"]}}`
	getWithout := `{"guardrailId":"gr-1","name":"g","status":"READY","contentPolicy":{"filters":[{"type":"PROMPT_ATTACK"}]}}`

	hashOf := func(getBody string) string {
		fx := &bedrockFixture{
			list:    `{"guardrails":[{"id":"gr-1","name":"g","status":"READY","version":"DRAFT"}]}`,
			get:     map[string]string{"gr-1": getBody},
			logging: `{"loggingConfig":{"s3Config":{"bucketName":"b"},"textDataDeliveryEnabled":true}}`,
		}
		srv := httptest.NewServer(fx)
		defer srv.Close()
		s := newGuardrailsSource(t, srv.URL, "")
		sink := &fakeSink{}
		if err := s.Gather(context.Background(), sink); err != nil {
			t.Fatalf("Gather: %v", err)
		}
		g, ok := findingBySubject(postureFindings(sink.findings()), subjectBedrockGuardrail)
		if !ok {
			t.Fatal("no guardrail posture finding")
		}
		return g.DetailHash
	}

	if hashOf(getWith) == hashOf(getWithout) {
		t.Fatal("a guardrail WITH Automated Reasoning must produce a different posture than one WITHOUT (AR is dropped otherwise)")
	}
}

func TestGuardrails_GapsAndLoggingDisabled(t *testing.T) {
	fx := &bedrockFixture{
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

	s := newGuardrailsSource(t, srv.URL, "")
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

func TestGuardrails_ZeroGuardrails(t *testing.T) {
	fx := &bedrockFixture{
		list:    `{"guardrails":[]}`,
		logging: `{"loggingConfig":{"cloudWatchConfig":{"logGroupName":"/bedrock"},"textDataDeliveryEnabled":true}}`,
	}
	srv := httptest.NewServer(fx)
	defer srv.Close()

	s := newGuardrailsSource(t, srv.URL, "")
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	g, ok := findingBySubject(postureFindings(sink.findings()), subjectBedrockGuardrail)
	if !ok || g.Severity != model.SeverityMedium || !strings.Contains(g.Title, "No Bedrock guardrails") {
		t.Fatalf("zero-guardrail finding = %+v ok=%v", g, ok)
	}
}

func TestGuardrails_TruncationSignal(t *testing.T) {
	fx := &bedrockFixture{
		list: `{"guardrails":[{"id":"gr-a","name":"a","status":"READY"},{"id":"gr-b","name":"b","status":"READY"}]}`,
		get: map[string]string{
			"gr-a": `{"status":"READY","contentPolicy":{"filters":[{"type":"PROMPT_ATTACK"}]}}`,
		},
		logging: `{}`,
	}
	srv := httptest.NewServer(fx)
	defer srv.Close()

	s := newGuardrailsSource(t, srv.URL, "1") // discovered 2, read 1 ⇒ partial
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

func TestGuardrails_ReadFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	s := newGuardrailsSource(t, srv.URL, "")
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	fs := sink.findings()
	if len(fs) != 1 || fs[0].Kind != "health" || fs[0].SubjectKind != subjectGuardrails {
		t.Fatalf("expected one guardrails health finding, got %+v", fs)
	}
	if len(postureFindings(fs)) != 0 {
		t.Fatal("a read failure must not fabricate a safety posture")
	}
}

// TestGuardrails_DedupStable proves a guardrail's posture DetailHash depends only on its
// config state, not the per-pass timestamp, so an unchanged guardrail dedups.
func TestGuardrails_DedupStable(t *testing.T) {
	t1 := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)
	f1 := postureFinding(model.SeverityMedium, subjectBedrockGuardrail, "acct/us-east-1", "t", "bedrock.guardrail id=gr-1 status=READY content=1", t1)
	f2 := postureFinding(model.SeverityMedium, subjectBedrockGuardrail, "acct/us-east-1", "t", "bedrock.guardrail id=gr-1 status=READY content=1", t2)
	if f1.DetailHash != f2.DetailHash {
		t.Fatal("identical config across passes must hash identically (dedup would break)")
	}
	f3 := postureFinding(model.SeverityMedium, subjectBedrockGuardrail, "acct/us-east-1", "t", "bedrock.guardrail id=gr-1 status=READY content=2", t1)
	if f3.DetailHash == f1.DetailHash {
		t.Fatal("a config change must produce a different hash")
	}
}
