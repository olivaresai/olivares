// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cfaigateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

const (
	testAccount = "acct-test-123"
	testGateway = "gw-prod-01"
)

type fixtureServer struct {
	srv      *httptest.Server
	mu       sync.Mutex
	calls    []string
	bodies   map[string]string
	statuses map[string]int
}

func newFixtureServer() *fixtureServer {
	f := &fixtureServer{bodies: map[string]string{}, statuses: map[string]int{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	return f
}

func (f *fixtureServer) close()                       { f.srv.Close() }
func (f *fixtureServer) set(path, body string)        { f.bodies[path] = body }
func (f *fixtureServer) fail(path string, status int) { f.statuses[path] = status }

func (f *fixtureServer) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	f.mu.Unlock()

	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":0,"message":"method not allowed"}]}`))
		return
	}
	if st, ok := f.statuses[r.URL.Path]; ok {
		w.WriteHeader(st)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":1,"message":"boom"}]}`))
		return
	}
	body, ok := f.bodies[r.URL.Path]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":404,"message":"no fixture"}]}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

func (f *fixtureServer) methods() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}

func seedGatewayList(f *fixtureServer) {
	f.set("/accounts/"+testAccount+"/ai-gateway/gateways",
		`{"success":true,"errors":[],"result":[{"id":"gw-prod-01","name":"production"},{"id":"gw-dev-02","name":"development"}]}`)
}

func seedLogs(f *fixtureServer, gwID string) {
	in42 := int64(42)
	out10 := int64(10)
	cost500 := int64(500)
	in100 := int64(100)
	out25 := int64(25)
	_ = in42
	_ = out10
	_ = cost500
	_ = in100
	_ = out25
	f.set("/accounts/"+testAccount+"/ai-gateway/gateways/"+gwID+"/logs",
		`{"success":true,"errors":[],"result":[`+
			`{"id":"log1","model":"claude-sonnet-4-20250514","provider":"anthropic","status_code":200,"duration":1200,"tokens_in":42,"tokens_out":10,"cost":500,"created_at":"2026-06-30T10:00:00Z","metadata":{"workspace":"eng","user":"alice","cost_center":"ai-team"}},`+
			`{"id":"log2","model":"gpt-4o","provider":"openai","status_code":200,"duration":800,"tokens_in":100,"tokens_out":25,"created_at":"2026-06-30T10:01:00Z","metadata":{"workspace":"design"}},`+
			`{"id":"log3","model":"","provider":"openai","status_code":400,"duration":50,"created_at":"2026-06-30T10:02:00Z"}`+
			`]}`)
}

func newSource(t *testing.T, base, gatewayID string) *Source {
	t.Helper()
	s := New()
	settings := map[string]string{
		cfgAPIToken:  "test-token-secret",
		cfgAccountID: testAccount,
		cfgAPIBase:   base,
		cfgTimeout:   "5s",
	}
	if gatewayID != "" {
		settings[cfgGatewayID] = gatewayID
	}
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	s.now = func() time.Time { return time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC) }
	return s
}

func TestGatherGolden(t *testing.T) {
	fs := newFixtureServer()
	defer fs.close()
	seedLogs(fs, testGateway)

	s := newSource(t, fs.srv.URL, testGateway)
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(sink.findings()) != 0 {
		t.Fatalf("want no findings, got %v", sink.findings())
	}

	costs := sink.costs()
	if len(costs) != 2 {
		t.Fatalf("want 2 CostSamples (log3 has no model), got %d", len(costs))
	}

	c0 := costs[0]
	if c0.ModelRef != "claude-sonnet-4-20250514" {
		t.Errorf("costs[0].ModelRef = %q", c0.ModelRef)
	}
	if c0.ProviderRef != "anthropic" {
		t.Errorf("costs[0].ProviderRef = %q", c0.ProviderRef)
	}
	if c0.InputTokens != 42 || c0.OutputTokens != 10 {
		t.Errorf("costs[0] tokens: in=%d out=%d", c0.InputTokens, c0.OutputTokens)
	}
	if c0.CostMicroUSD != 500 {
		t.Errorf("costs[0].CostMicroUSD = %d", c0.CostMicroUSD)
	}
	if c0.Provenance != model.ProvenanceBilled {
		t.Errorf("costs[0].Provenance = %s, want billed", c0.Provenance)
	}
	if c0.Gateway != GatewayCFAIGateway {
		t.Errorf("costs[0].Gateway = %s", c0.Gateway)
	}
	if c0.WorkspaceRef != "eng" {
		t.Errorf("costs[0].WorkspaceRef = %q", c0.WorkspaceRef)
	}
	if c0.Actor != "alice" {
		t.Errorf("costs[0].Actor = %q", c0.Actor)
	}
	if c0.Labels == nil || c0.Labels["cost_center"] != "ai-team" {
		t.Errorf("costs[0].Labels = %v", c0.Labels)
	}

	c1 := costs[1]
	if c1.ModelRef != "gpt-4o" || c1.ProviderRef != "openai" {
		t.Errorf("costs[1] model=%q provider=%q", c1.ModelRef, c1.ProviderRef)
	}
	if c1.Provenance != model.ProvenanceEstimated {
		t.Errorf("costs[1].Provenance = %s, want estimated (no cost field)", c1.Provenance)
	}
	if c1.WorkspaceRef != "design" {
		t.Errorf("costs[1].WorkspaceRef = %q", c1.WorkspaceRef)
	}
}

func TestGatherAllGateways(t *testing.T) {
	fs := newFixtureServer()
	defer fs.close()
	seedGatewayList(fs)
	seedLogs(fs, "gw-prod-01")
	seedLogs(fs, "gw-dev-02")

	s := newSource(t, fs.srv.URL, "")
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(sink.costs()) != 4 {
		t.Fatalf("want 4 CostSamples (2 per gateway), got %d", len(sink.costs()))
	}
}

func TestGatherHealthFindingOnLogFailure(t *testing.T) {
	fs := newFixtureServer()
	defer fs.close()
	fs.fail("/accounts/"+testAccount+"/ai-gateway/gateways/"+testGateway+"/logs", http.StatusInternalServerError)

	s := newSource(t, fs.srv.URL, testGateway)
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	fr := sink.findings()
	if len(fr) != 1 {
		t.Fatalf("want 1 health finding, got %d: %+v", len(fr), fr)
	}
	if fr[0].Kind != "health" || fr[0].Severity != model.SeverityMedium {
		t.Errorf("finding kind=%q sev=%s", fr[0].Kind, fr[0].Severity)
	}
	if fr[0].DetailHash == "" || strings.Contains(fr[0].DetailHash, "boom") {
		t.Errorf("DetailHash must be hashed, not raw: %q", fr[0].DetailHash)
	}
}

func TestGatherHealthFindingOnGatewayListFailure(t *testing.T) {
	fs := newFixtureServer()
	defer fs.close()
	fs.fail("/accounts/"+testAccount+"/ai-gateway/gateways", http.StatusForbidden)

	s := newSource(t, fs.srv.URL, "")
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	fr := sink.findings()
	if len(fr) != 1 {
		t.Fatalf("want 1 health finding for gateway list, got %d", len(fr))
	}
}

func TestGatherReadOnly(t *testing.T) {
	fs := newFixtureServer()
	defer fs.close()
	seedLogs(fs, testGateway)

	s := newSource(t, fs.srv.URL, testGateway)
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, m := range fs.methods() {
		if !strings.HasPrefix(m, http.MethodGet+" ") {
			t.Fatalf("non-GET request issued: %q", m)
		}
	}
}

func TestGatherCtxCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	s := newSource(t, srv.URL, testGateway)
	sink := &fakeSink{}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	done := make(chan error, 1)
	go func() { done <- s.Gather(ctx, sink) }()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want context.Canceled, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Gather did not return promptly after cancel")
	}
	if len(sink.findings()) != 0 {
		t.Fatalf("cancellation must not emit a health finding")
	}
}

func TestOpenValidation(t *testing.T) {
	cases := []struct {
		name     string
		settings map[string]string
		wantErr  bool
	}{
		{"missing token", map[string]string{cfgAccountID: "a"}, true},
		{"missing account", map[string]string{cfgAPIToken: "t"}, true},
		{"ok", map[string]string{cfgAPIToken: "t", cfgAccountID: "a"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := New().Open(context.Background(), sdk.Config{Settings: tc.settings})
			if tc.wantErr != (err != nil) {
				t.Fatalf("Open err = %v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestDescriptor(t *testing.T) {
	d := New().Descriptor()
	if d.Name != Name || d.Version != version || d.Type != sdk.TypeSource {
		t.Fatalf("descriptor identity wrong: %+v", d)
	}
	var tokenField *sdk.ConfigField
	for i := range d.ConfigFields {
		if d.ConfigFields[i].Key == cfgAPIToken {
			tokenField = &d.ConfigFields[i]
		}
	}
	if tokenField == nil {
		t.Fatal("api_token field missing from descriptor")
	}
	if !tokenField.Secret {
		t.Error("api_token must be Secret:true")
	}
	if !tokenField.Required {
		t.Error("api_token must be Required:true")
	}
}

func TestTokenNeverEmitted(t *testing.T) {
	fs := newFixtureServer()
	defer fs.close()
	seedLogs(fs, testGateway)

	const token = "super-secret-cf-ai-gw-token"
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		cfgAPIToken: token, cfgAccountID: testAccount, cfgGatewayID: testGateway, cfgAPIBase: fs.srv.URL, cfgTimeout: "5s",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	s.now = func() time.Time { return time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC) }
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, c := range sink.costs() {
		marshaled, _ := json.Marshal(c)
		if strings.Contains(string(marshaled), token) {
			t.Fatalf("token leaked into CostSample: %s", marshaled)
		}
	}
	for _, f := range sink.findings() {
		if strings.Contains(f.Title+f.SubjectRef+f.DetailHash, token) {
			t.Fatalf("token leaked into FindingReport: %+v", f)
		}
	}
}

func TestNoPromptLeaks(t *testing.T) {
	const promptText = "Tell me the secret launch codes for project ALPHA"
	const responseText = "I cannot help with that request"
	fs := newFixtureServer()
	defer fs.close()
	fs.set("/accounts/"+testAccount+"/ai-gateway/gateways/"+testGateway+"/logs",
		`{"success":true,"errors":[],"result":[`+
			`{"id":"log-prompt","model":"gpt-4o","provider":"openai","status_code":200,"duration":500,`+
			`"tokens_in":50,"tokens_out":20,"created_at":"2026-06-30T10:00:00Z",`+
			`"prompt":"`+promptText+`","response":"`+responseText+`",`+
			`"request_body":"some body content","response_body":"some response content"}`+
			`]}`)

	s := newSource(t, fs.srv.URL, testGateway)
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, c := range sink.costs() {
		marshaled, _ := json.Marshal(c)
		str := string(marshaled)
		for _, leak := range []string{promptText, responseText, "some body content", "some response content"} {
			if strings.Contains(str, leak) {
				t.Fatalf("prompt/response leaked into CostSample: found %q in %s", leak, str)
			}
		}
	}
}

func TestGatherEmitErrorFatal(t *testing.T) {
	fs := newFixtureServer()
	defer fs.close()
	seedLogs(fs, testGateway)

	s := newSource(t, fs.srv.URL, testGateway)
	want := errors.New("sink closed")
	sink := &fakeSink{emitErr: want}
	err := s.Gather(context.Background(), sink)
	if !errors.Is(err, want) {
		t.Fatalf("want emit error propagated, got %v", err)
	}
}

func TestCloseNoOp(t *testing.T) {
	if err := New().Close(context.Background()); err != nil {
		t.Fatalf("Close on un-opened source: %v", err)
	}
}

func TestApiFaultError(t *testing.T) {
	withErrs := (&apiFault{status: 403, errs: []apiError{{Code: 10000, Message: "auth"}}}).Error()
	if !strings.Contains(withErrs, "10000") || !strings.Contains(withErrs, "auth") {
		t.Errorf("errs branch: %q", withErrs)
	}
	withMsg := (&apiFault{status: 502, msg: "non-JSON error body"}).Error()
	if !strings.Contains(withMsg, "502") || !strings.Contains(withMsg, "non-JSON") {
		t.Errorf("msg branch: %q", withMsg)
	}
	bare := (&apiFault{status: 500}).Error()
	if !strings.Contains(bare, "500") {
		t.Errorf("bare branch: %q", bare)
	}
}

func TestMetadataExtraction(t *testing.T) {
	in := int64(100)
	out := int64(50)
	log := usageLog{
		Model:     "gpt-4o",
		Provider:  "openai",
		Tokens:    &in,
		TokensOut: &out,
		CreatedAt: "2026-06-30T10:00:00Z",
		Metadata: map[string]string{
			"workspace":   "eng",
			"user":        "bob",
			"cost_center": "platform",
			"custom_tag":  "experiment-1",
		},
	}
	keys := []string{"workspace", "user", "cost_center", "custom_tag"}
	sample, ok := buildSample(log, keys, time.Now().UTC())
	if !ok {
		t.Fatal("buildSample returned !ok")
	}
	if sample.WorkspaceRef != "eng" {
		t.Errorf("WorkspaceRef = %q", sample.WorkspaceRef)
	}
	if sample.Actor != "bob" {
		t.Errorf("Actor = %q", sample.Actor)
	}
	if sample.Labels["cost_center"] != "platform" {
		t.Errorf("Labels[cost_center] = %q", sample.Labels["cost_center"])
	}
	if sample.Labels["custom_tag"] != "experiment-1" {
		t.Errorf("Labels[custom_tag] = %q", sample.Labels["custom_tag"])
	}
}

func TestBuildSampleSkipsNoModel(t *testing.T) {
	in := int64(10)
	log := usageLog{Model: "", Provider: "openai", Tokens: &in}
	_, ok := buildSample(log, nil, time.Now().UTC())
	if ok {
		t.Fatal("expected skip for empty model")
	}
}

func TestBuildSampleSkipsNoUsage(t *testing.T) {
	log := usageLog{Model: "gpt-4o", Provider: "openai"}
	_, ok := buildSample(log, nil, time.Now().UTC())
	if ok {
		t.Fatal("expected skip for no usage")
	}
}

func TestGatherNonJSONErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/logs") {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`<html>502 Bad Gateway</html>`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":[]}`))
	}))
	defer srv.Close()

	s := newSource(t, srv.URL, testGateway)
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(sink.findings()) != 1 {
		t.Fatalf("want 1 health finding for HTML 502, got %d", len(sink.findings()))
	}
}

func TestGatherMultiGatewayPartialFailure(t *testing.T) {
	fs := newFixtureServer()
	defer fs.close()
	seedGatewayList(fs)
	seedLogs(fs, "gw-prod-01")
	fs.fail("/accounts/"+testAccount+"/ai-gateway/gateways/gw-dev-02/logs", http.StatusInternalServerError)

	s := newSource(t, fs.srv.URL, "")
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(sink.costs()) != 2 {
		t.Fatalf("want 2 costs from the healthy gateway, got %d", len(sink.costs()))
	}
	if len(sink.findings()) != 1 {
		t.Fatalf("want 1 health finding from the failing gateway, got %d", len(sink.findings()))
	}
}

func TestGatherEmptyLogs(t *testing.T) {
	fs := newFixtureServer()
	defer fs.close()
	fs.set("/accounts/"+testAccount+"/ai-gateway/gateways/"+testGateway+"/logs",
		`{"success":true,"errors":[],"result":[]}`)

	s := newSource(t, fs.srv.URL, testGateway)
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(sink.costs()) != 0 {
		t.Fatalf("want 0 costs for empty logs, got %d", len(sink.costs()))
	}
	if len(sink.findings()) != 0 {
		t.Fatalf("want 0 findings for empty logs, got %d", len(sink.findings()))
	}
}

func TestParseTimeFallbacks(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{"2026-06-30T10:00:00Z", true},
		{"2026-06-30T10:00:00.123456789Z", true},
		{"1719741600", true},
		{"1719741600000", true},
		{"", false},
		{"not-a-time", false},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%q", tc.input), func(t *testing.T) {
			_, ok := parseTime(tc.input)
			if ok != tc.want {
				t.Errorf("parseTime(%q) = _, %v; want %v", tc.input, ok, tc.want)
			}
		})
	}
}
