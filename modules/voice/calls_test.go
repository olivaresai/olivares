// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package voice

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	voiceconn "github.com/olivaresai/olivares/connectors/voice"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

const testWebhookSecret = "whsec_" + "dGVzdC1jYWxsLXdlYmhvb2sta2V5"

type fakeCallController struct {
	mu      sync.Mutex
	accepts []CallAccept
	rejects []int
	hangups []string
	err     error
}

func (f *fakeCallController) Accept(_ context.Context, _ string, cfg CallAccept) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.accepts = append(f.accepts, cfg)
	return f.err
}

func (f *fakeCallController) Reject(_ context.Context, _ string, statusCode int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rejects = append(f.rejects, statusCode)
	return f.err
}

func (f *fakeCallController) Hangup(_ context.Context, callID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hangups = append(f.hangups, callID)
	return f.err
}

type fakeSideband struct {
	mu       sync.Mutex
	messages [][]byte
	writes   [][]byte
	closed   bool
	closeCh  chan struct{}
}

type mutableStopGate struct {
	mu       sync.Mutex
	decision StopDecision
}

func (g *mutableStopGate) Check(context.Context, model.TenantID, StopDims) (StopDecision, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.decision, nil
}

func (g *mutableStopGate) set(dec StopDecision) {
	g.mu.Lock()
	g.decision = dec
	g.mu.Unlock()
}

func newFakeSideband(messages ...string) *fakeSideband {
	out := &fakeSideband{closeCh: make(chan struct{})}
	for _, msg := range messages {
		out.messages = append(out.messages, []byte(msg))
	}
	return out
}

func (s *fakeSideband) ReadMessage(ctx context.Context) ([]byte, error) {
	for {
		s.mu.Lock()
		if len(s.messages) > 0 {
			msg := append([]byte(nil), s.messages[0]...)
			s.messages = s.messages[1:]
			s.mu.Unlock()
			return msg, nil
		}
		if s.closed {
			s.mu.Unlock()
			return nil, io.EOF
		}
		ch := s.closeCh
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ch:
			return nil, io.EOF
		}
	}
}

func (s *fakeSideband) WriteText(_ context.Context, p []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes = append(s.writes, append([]byte(nil), p...))
	return nil
}

func (s *fakeSideband) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.closeCh)
	}
	return nil
}

func (s *fakeSideband) waitWrite(t *testing.T) []byte {
	t.Helper()
	for i := 0; i < 200; i++ {
		s.mu.Lock()
		if len(s.writes) > 0 {
			out := append([]byte(nil), s.writes[0]...)
			s.mu.Unlock()
			return out
		}
		s.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("sideband write never arrived")
	return nil
}

func (s *fakeSideband) waitClosed(t *testing.T) {
	t.Helper()
	for i := 0; i < 200; i++ {
		s.mu.Lock()
		closed := s.closed
		s.mu.Unlock()
		if closed {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("sideband was not closed")
}

func signedCallRequest(t *testing.T, eventID, typ, callID, from, to string) *http.Request {
	t.Helper()
	body := callWebhookBody(t, eventID, typ, callID, from, to)
	ts := time.Now().Unix()
	req := httptest.NewRequest(http.MethodPost, "/webhooks/openai-realtime", bytes.NewReader(body))
	req.Header.Set("webhook-id", eventID)
	req.Header.Set("webhook-timestamp", strconvFormatInt(ts))
	req.Header.Set("webhook-signature", "v1,"+callMAC(t, eventID, strconvFormatInt(ts), body))
	return req
}

func callWebhookBody(t *testing.T, eventID, typ, callID, from, to string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"object":     "event",
		"id":         eventID,
		"type":       typ,
		"created_at": time.Now().Unix(),
		"data": map[string]any{
			"call_id": callID,
			"sip_headers": []map[string]string{
				{"name": "From", "value": from},
				{"name": "To", "value": to},
				{"name": "Call-ID", "value": "sip-call-id"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func callMAC(t *testing.T, id, ts string, body []byte) string {
	t.Helper()
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(testWebhookSecret, "whsec_"))
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id))
	mac.Write([]byte("."))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(body)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func strconvFormatInt(n int64) string {
	return strconv.FormatInt(n, 10)
}

func (h *harness) setCallPolicy(t *testing.T, token string, tenant model.TenantID, calls map[string]any) {
	t.Helper()
	body := map[string]any{
		"agent_ref":            "voice-agent",
		"allowed_model_ref":    "*",
		"allowed_provider_ref": "openai",
		"calls":                calls,
	}
	if r := h.do("PUT", "/v1/m/voice/policies", token, body, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("set call policy = %d %s", r.code, r.raw)
	}
}

func serveCall(t *testing.T, m *Module, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	m.RealtimeWebhookHandler(testWebhookSecret).ServeHTTP(rec, req)
	return rec
}

func TestRealtimeWebhookHappyPath(t *testing.T) {
	ctrl := &fakeCallController{}
	h, mod := newHarness(t, WithCallController(ctrl))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	mod.callConfig.Tenant = tenant
	h.setCallPolicy(t, tok, tenant, map[string]any{
		"enabled": true, "to_patterns": []string{"+18005551212"}, "from_patterns": []string{"*0123"},
		"model": "gpt-realtime-2", "guardrail_instructions": "stay governed",
	})

	req := signedCallRequest(t, "evt-happy", "realtime.call.incoming", "call-123", "sip:+14155550123@example.com", "sip:+18005551212@voice.example.com")
	if rec := serveCall(t, mod, req); rec.Code != http.StatusOK {
		t.Fatalf("webhook = %d %s", rec.Code, rec.Body.String())
	}
	ctrl.mu.Lock()
	if len(ctrl.accepts) != 1 || ctrl.accepts[0].Model != "gpt-realtime-2" || ctrl.accepts[0].Instructions != "stay governed" {
		t.Fatalf("accepts = %+v", ctrl.accepts)
	}
	ctrl.mu.Unlock()

	s, code := h.getSession(tok, tenant, "call-123")
	if code != http.StatusOK || !s.Governed || s.Transport != callTransportSIP || s.CallRef != "call-123" {
		t.Fatalf("call session = code %d %+v", code, s)
	}
	raw, _ := json.Marshal(s)
	if strings.Contains(string(raw), "18005551212") || strings.Contains(string(raw), "14155550123") {
		t.Fatalf("stored session leaked raw SIP digits: %s", raw)
	}
	decisions := h.voiceDecisions(t, tenant)
	if len(decisions) != 1 || decisions[0].Op != opOpen || decisions[0].OpStatus != opStatusDispatched {
		t.Fatalf("decisions = %+v", decisions)
	}
	if !h.auditHas(t, tenant, "voice.call.accept") {
		t.Fatal("voice.call.accept audit event not appended")
	}
}

func TestRealtimeWebhookBadSignatureUnknownTypeAndReplay(t *testing.T) {
	ctrl := &fakeCallController{}
	h, mod := newHarness(t, WithCallController(ctrl))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	mod.callConfig.Tenant = tenant
	h.setCallPolicy(t, tok, tenant, map[string]any{"enabled": true, "to_patterns": []string{"*1212"}})

	bad := signedCallRequest(t, "evt-bad", "realtime.call.incoming", "call-bad", "sip:+1000@example.com", "sip:+18005551212@voice.example.com")
	bad.Header.Set("webhook-signature", "v1,bad")
	if rec := serveCall(t, mod, bad); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad signature = %d", rec.Code)
	}
	if got := len(h.voiceDecisions(t, tenant)); got != 0 {
		t.Fatalf("bad signature wrote %d decisions", got)
	}

	unknown := signedCallRequest(t, "evt-unknown", "other.event", "call-u", "sip:+1000@example.com", "sip:+18005551212@voice.example.com")
	if rec := serveCall(t, mod, unknown); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown type = %d", rec.Code)
	}

	req1 := signedCallRequest(t, "evt-replay", "realtime.call.incoming", "call-r", "sip:+1000@example.com", "sip:+18005551212@voice.example.com")
	req2 := signedCallRequest(t, "evt-replay", "realtime.call.incoming", "call-r", "sip:+1000@example.com", "sip:+18005551212@voice.example.com")
	if rec := serveCall(t, mod, req1); rec.Code != http.StatusOK {
		t.Fatalf("first replay fixture = %d", rec.Code)
	}
	if rec := serveCall(t, mod, req2); rec.Code != http.StatusOK {
		t.Fatalf("second replay fixture = %d", rec.Code)
	}
	if got := len(h.voiceDecisions(t, tenant)); got != 1 {
		t.Fatalf("replayed event wrote %d decisions, want 1", got)
	}
}

func TestRealtimeWebhookPolicyRejects(t *testing.T) {
	for _, tc := range []struct {
		name  string
		calls map[string]any
	}{
		{name: "disabled", calls: map[string]any{"enabled": false, "to_patterns": []string{"*1212"}}},
		{name: "empty-to", calls: map[string]any{"enabled": true}},
		{name: "no-match", calls: map[string]any{"enabled": true, "to_patterns": []string{"*9999"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := &fakeCallController{}
			h, mod := newHarness(t, WithCallController(ctrl))
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "acme"+tc.name)
			tok := h.roleToken(admin, tenant, "op"+tc.name+"@acme.io", "admin")
			mod.callConfig.Tenant = tenant
			h.setCallPolicy(t, tok, tenant, tc.calls)
			req := signedCallRequest(t, "evt-"+tc.name, "realtime.call.incoming", "call-"+tc.name, "sip:+1000@example.com", "sip:+18005551212@voice.example.com")
			if rec := serveCall(t, mod, req); rec.Code != http.StatusOK {
				t.Fatalf("webhook = %d", rec.Code)
			}
			ctrl.mu.Lock()
			rejects, accepts := append([]int(nil), ctrl.rejects...), len(ctrl.accepts)
			ctrl.mu.Unlock()
			if len(rejects) != 1 || rejects[0] != callRejectDecline || accepts != 0 {
				t.Fatalf("rejects=%v accepts=%d", rejects, accepts)
			}
			if !h.waitForFinding(busPolicyViolation) {
				t.Fatal("policy reject must emit voice_policy_violation")
			}
		})
	}
}

func TestRealtimeWebhookStopAndSweep(t *testing.T) {
	ctrl := &fakeCallController{}
	sb := newFakeSideband()
	h, mod := newHarness(t,
		WithCallController(ctrl),
		WithSidebandAttacher(func(context.Context, string) (CallSideband, error) { return sb, nil }),
		WithStopGate(fakeStopGate{decision: StopDecision{Stopped: true, StopRef: "stop-1", Scope: "estate"}}),
		WithCallConfig(CallConfig{StopSweepInterval: 10 * time.Millisecond}),
	)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	mod.callConfig.Tenant = tenant
	h.setCallPolicy(t, tok, tenant, map[string]any{"enabled": true, "to_patterns": []string{"*1212"}})

	req := signedCallRequest(t, "evt-stop", "realtime.call.incoming", "call-stop", "sip:+1000@example.com", "sip:+18005551212@voice.example.com")
	if rec := serveCall(t, mod, req); rec.Code != http.StatusOK {
		t.Fatalf("webhook stop = %d", rec.Code)
	}
	ctrl.mu.Lock()
	if len(ctrl.rejects) != 1 || ctrl.rejects[0] != callRejectDecline {
		t.Fatalf("incoming stop rejects = %v", ctrl.rejects)
	}
	ctrl.mu.Unlock()

	ctrl2 := &fakeCallController{}
	sb2 := newFakeSideband()
	stopGate := &mutableStopGate{}
	h2, mod2 := newHarness(t,
		WithCallController(ctrl2),
		WithSidebandAttacher(func(context.Context, string) (CallSideband, error) { return sb2, nil }),
		WithStopGate(stopGate),
		WithCallConfig(CallConfig{StopSweepInterval: 10 * time.Millisecond}),
	)
	admin2 := h2.adminLogin()
	tenant2 := h2.createOrg(admin2, "acme2")
	tok2 := h2.roleToken(admin2, tenant2, "op@acme2.io", "admin")
	mod2.callConfig.Tenant = tenant2
	h2.setCallPolicy(t, tok2, tenant2, map[string]any{"enabled": true, "to_patterns": []string{"*1212"}})
	req2 := signedCallRequest(t, "evt-live", "realtime.call.incoming", "call-live", "sip:+1000@example.com", "sip:+18005551212@voice.example.com")
	if rec := serveCall(t, mod2, req2); rec.Code != http.StatusOK {
		t.Fatalf("webhook live = %d", rec.Code)
	}
	stopGate.set(StopDecision{Stopped: true, StopRef: "stop-2", Scope: "estate"})
	for i := 0; i < 200; i++ {
		ctrl2.mu.Lock()
		hangups := append([]string(nil), ctrl2.hangups...)
		ctrl2.mu.Unlock()
		if len(hangups) == 1 && hangups[0] == "call-live" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	ctrl2.mu.Lock()
	if len(ctrl2.hangups) != 1 || ctrl2.hangups[0] != "call-live" {
		t.Fatalf("hangups = %v", ctrl2.hangups)
	}
	ctrl2.mu.Unlock()
	sb2.waitClosed(t)
	decisions := h2.voiceDecisions(t, tenant2)
	if row := findVoiceDecision(decisions, opClose); row == nil || row.OpStatus != opStatusDispatched {
		t.Fatalf("close/dispatched not recorded: %+v", decisions)
	}
}

func TestRealtimeSidebandCostAndGuardrail(t *testing.T) {
	ctrl := &fakeCallController{}
	sb := newFakeSideband(responseDone("r1", "gpt-realtime-2", 500, 1000, 50, 100, 75, 200), responseDone("r1", "gpt-realtime-2", 500, 1000, 50, 100, 75, 200), responseDone("r2", "unknown-model", 10, 0, 1, 0, 2, 0), responseDone("r3", "gpt-realtime-2", 0, 0, 0, 0, 0, 0))
	h, mod := newHarness(t,
		WithCallController(ctrl),
		WithSidebandAttacher(func(context.Context, string) (CallSideband, error) { return sb, nil }),
	)
	var costsMu sync.Mutex
	var costs []sdkmodel.CostSample
	if _, err := h.bus.Subscribe([]event.Type{event.TypeCostSampled}, func(_ context.Context, e event.Event) error {
		if c, ok := event.CostOf(e); ok {
			costsMu.Lock()
			costs = append(costs, c)
			costsMu.Unlock()
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	mod.callConfig.Tenant = tenant
	mod.callConfig.WorkspaceRef = "proj_123"
	h.setCallPolicy(t, tok, tenant, map[string]any{"enabled": true, "to_patterns": []string{"*1212"}, "model": "gpt-realtime-2", "guardrail_instructions": "stay governed"})
	req := signedCallRequest(t, "evt-cost", "realtime.call.incoming", "call-cost", "sip:+1000@example.com", "sip:+18005551212@voice.example.com")
	if rec := serveCall(t, mod, req); rec.Code != http.StatusOK {
		t.Fatalf("webhook = %d", rec.Code)
	}
	wantUpdate, err := voiceconn.GuardrailSessionUpdate("stay governed")
	if err != nil {
		t.Fatal(err)
	}
	if got := sb.waitWrite(t); !bytes.Equal(got, wantUpdate) {
		t.Fatalf("guardrail update = %s want %s", got, wantUpdate)
	}
	for i := 0; i < 200; i++ {
		costsMu.Lock()
		n := len(costs)
		costsMu.Unlock()
		if n == 3 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	costsMu.Lock()
	defer costsMu.Unlock()
	if len(costs) != 3 {
		t.Fatalf("cost samples = %d %+v", len(costs), costs)
	}
	byType := map[string]sdkmodel.CostSample{}
	for _, c := range costs {
		if c.ProviderRef != callProviderOpenAI || c.SessionRef != "call-cost" || c.WorkspaceRef != "proj_123" || c.Provenance != sdkmodel.ProvenanceEstimated {
			t.Fatalf("bad cost dimensions: %+v", c)
		}
		if c.ModelRef == "gpt-realtime-2" {
			byType[c.CostType] = c
		}
	}
	if byType[costTypeRealtimeAudio].CostMicroUSD != 41640 || byType[costTypeRealtimeAudio].InputTokens != 1000 || byType[costTypeRealtimeAudio].CacheReadTokens != 100 || byType[costTypeRealtimeAudio].OutputTokens != 200 {
		t.Fatalf("audio cost = %+v", byType[costTypeRealtimeAudio])
	}
	if byType[costTypeRealtimeText].CostMicroUSD != 3620 || byType[costTypeRealtimeText].InputTokens != 500 || byType[costTypeRealtimeText].CacheReadTokens != 50 || byType[costTypeRealtimeText].OutputTokens != 75 {
		t.Fatalf("text cost = %+v", byType[costTypeRealtimeText])
	}
	foundUnknown := false
	for _, c := range costs {
		if c.ModelRef == "unknown-model" {
			foundUnknown = c.CostMicroUSD == 0 && c.InputTokens == 10 && c.CacheReadTokens == 1 && c.OutputTokens == 2
		}
	}
	if !foundUnknown {
		t.Fatalf("unknown-model usage-only sample missing: %+v", costs)
	}
}

func TestRealtimeTranscriptDLPAndRecordingPosture(t *testing.T) {
	ctrl := &fakeCallController{}
	sb := newFakeSideband(transcriptDone("bank card 4111111111111111"), transcriptDone("bank card again"))
	h, mod := newHarness(t,
		WithCallController(ctrl),
		WithSidebandAttacher(func(context.Context, string) (CallSideband, error) { return sb, nil }),
	)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	mod.callConfig.Tenant = tenant
	h.setCallPolicy(t, tok, tenant, map[string]any{"enabled": true, "to_patterns": []string{"*1212"}, "recording": map[string]any{"active": true}})
	req := signedCallRequest(t, "evt-dlp-1", "realtime.call.incoming", "call-dlp-1", "sip:+1000@example.com", "sip:+18005551212@voice.example.com")
	if rec := serveCall(t, mod, req); rec.Code != http.StatusOK {
		t.Fatalf("webhook = %d", rec.Code)
	}
	if !h.waitForFinding(busTranscriptUnclassified) {
		t.Fatal("missing unclassified transcript finding")
	}
	if got := h.countFindings(t, tenant, busTranscriptUnclassified); got != 1 {
		t.Fatalf("unclassified persisted %d times, want 1", got)
	}

	ctrl2 := &fakeCallController{}
	sb2 := newFakeSideband(transcriptDone("card 4111111111111111"))
	h2, mod2 := newHarness(t,
		WithCallController(ctrl2),
		WithSidebandAttacher(func(context.Context, string) (CallSideband, error) { return sb2, nil }),
		WithTranscriptClassifier(TranscriptClassifierFunc(func(string) ([]SensitivityHit, error) {
			return []SensitivityHit{{Class: "pii.financial", Count: 2}}, nil
		})),
	)
	admin2 := h2.adminLogin()
	tenant2 := h2.createOrg(admin2, "acme2")
	tok2 := h2.roleToken(admin2, tenant2, "op@acme2.io", "admin")
	mod2.callConfig.Tenant = tenant2
	h2.setCallPolicy(t, tok2, tenant2, map[string]any{"enabled": true, "to_patterns": []string{"*1212"}, "recording": map[string]any{"active": true, "dtmf_masking": true}})
	req2 := signedCallRequest(t, "evt-dlp-2", "realtime.call.incoming", "call-dlp-2", "sip:+1000@example.com", "sip:+18005551212@voice.example.com")
	if rec := serveCall(t, mod2, req2); rec.Code != http.StatusOK {
		t.Fatalf("webhook2 = %d", rec.Code)
	}
	if !h2.waitForFinding(busRecordingSADRisk) {
		t.Fatal("missing recording SAD finding for financial data")
	}
	findings := h2.voiceFindings(t, tenant2, busRecordingSADRisk)
	var sawFinancial bool
	for _, f := range findings {
		if f.Metadata["reason"] == recordingReasonFinancialSAD {
			sawFinancial = true
			if bytes.Contains(f.DetailHash, []byte("4111111111111111")) {
				t.Fatal("detail hash contains transcript text")
			}
			raw, _ := json.Marshal(f.Metadata)
			if strings.Contains(string(raw), "4111111111111111") {
				t.Fatalf("finding metadata leaked transcript text: %s", raw)
			}
		}
	}
	if !sawFinancial {
		t.Fatalf("financial SAD reason not found: %+v", findings)
	}
}

func TestRealtimeRecordingPostureAndUngoverned(t *testing.T) {
	ctrl := &fakeCallController{}
	h, mod := newHarness(t, WithCallController(ctrl))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	mod.callConfig.Tenant = tenant
	h.setCallPolicy(t, tok, tenant, map[string]any{"enabled": true, "to_patterns": []string{"*1212"}, "recording": map[string]any{"active": true}})
	req := signedCallRequest(t, "evt-rec", "realtime.call.incoming", "call-rec", "sip:+1000@example.com", "sip:+18005551212@voice.example.com")
	if rec := serveCall(t, mod, req); rec.Code != http.StatusOK {
		t.Fatalf("webhook = %d", rec.Code)
	}
	if !h.waitForFinding(busRecordingSADRisk) {
		t.Fatal("missing recording posture finding")
	}
	findings := h.voiceFindings(t, tenant, busRecordingSADRisk)
	if len(findings) == 0 || findings[0].Metadata["reason"] != recordingReasonNoSADControls {
		t.Fatalf("recording posture metadata = %+v", findings)
	}

	h2, _ := newHarness(t)
	admin2 := h2.adminLogin()
	tenant2 := h2.createOrg(admin2, "acme2")
	h2.publishTelemetry(tenant2, map[string]any{"session_ref": "rogue-call", "agent_ref": "rogue", "model_ref": "m", "provider_ref": "openai", "role": "agent", "turn_delta": 1})
	if !h2.waitForFinding(busRealtimeUngoverned) {
		t.Fatal("missing realtime_session_ungoverned finding")
	}
	h2.publishTelemetry(tenant2, map[string]any{"session_ref": "rogue-call", "agent_ref": "rogue", "model_ref": "m", "provider_ref": "openai", "role": "agent", "turn_delta": 1})
	if got := h2.countFindings(t, tenant2, busRealtimeUngoverned); got != 1 {
		t.Fatalf("ungoverned finding persisted %d times, want 1", got)
	}
}

func TestTranscriptClassifierErrorIsUnclassified(t *testing.T) {
	ctrl := &fakeCallController{}
	sb := newFakeSideband(transcriptDone("text"))
	h, mod := newHarness(t,
		WithCallController(ctrl),
		WithSidebandAttacher(func(context.Context, string) (CallSideband, error) { return sb, nil }),
		WithTranscriptClassifier(TranscriptClassifierFunc(func(string) ([]SensitivityHit, error) {
			return nil, errors.New("classifier down")
		})),
	)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	mod.callConfig.Tenant = tenant
	h.setCallPolicy(t, tok, tenant, map[string]any{"enabled": true, "to_patterns": []string{"*1212"}})
	req := signedCallRequest(t, "evt-cls", "realtime.call.incoming", "call-cls", "sip:+1000@example.com", "sip:+18005551212@voice.example.com")
	if rec := serveCall(t, mod, req); rec.Code != http.StatusOK {
		t.Fatalf("webhook = %d", rec.Code)
	}
	if !h.waitForFinding(busTranscriptUnclassified) {
		t.Fatal("classifier errors must surface unclassified transcript")
	}
}

func responseDone(id, modelRef string, textIn, audioIn, cachedText, cachedAudio, textOut, audioOut int64) string {
	b, _ := json.Marshal(map[string]any{
		"type": "response.done",
		"response": map[string]any{
			"id":    id,
			"model": modelRef,
			"usage": map[string]any{
				"input_tokens":  textIn + audioIn,
				"output_tokens": textOut + audioOut,
				"input_token_details": map[string]any{
					"text_tokens":  textIn,
					"audio_tokens": audioIn,
					"cached_tokens_details": map[string]any{
						"text_tokens":  cachedText,
						"audio_tokens": cachedAudio,
					},
				},
				"output_token_details": map[string]any{
					"text_tokens":  textOut,
					"audio_tokens": audioOut,
				},
			},
		},
	})
	return string(b)
}

func transcriptDone(text string) string {
	b, _ := json.Marshal(map[string]any{"type": "conversation.item.input_audio_transcription.completed", "item_id": "item-1", "content_index": 0, "transcript": text})
	return string(b)
}

func (h *harness) voiceDecisions(t *testing.T, tenant model.TenantID) []decisionDTO {
	t.Helper()
	var out []decisionDTO
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(decisionKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(context.Background(), model.Query{Limit: 100})
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out = append(out, toDecisionDTO(rec))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func (h *harness) auditHas(t *testing.T, tenant model.TenantID, action string) bool {
	t.Helper()
	found := false
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 1, func(ev model.AuditEvent) error {
			if ev.Action == action {
				found = true
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	return found
}

func (h *harness) voiceFindings(t *testing.T, tenant model.TenantID, kind string) []model.Finding {
	t.Helper()
	var out []model.Finding
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		recs, _, err := sc.Findings().List(context.Background(), model.Query{Limit: 100})
		if err != nil {
			return err
		}
		for _, f := range recs {
			if f.Source == Name && f.Metadata["bus_kind"] == kind {
				out = append(out, f)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func (h *harness) countFindings(t *testing.T, tenant model.TenantID, kind string) int {
	t.Helper()
	return len(h.voiceFindings(t, tenant, kind))
}
