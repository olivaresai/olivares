// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudemanagedagents

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

// capture is an emit sink for the receiver that records observations and can be made to fail.
type capture struct {
	mu   sync.Mutex
	obs  []model.Observation
	fail bool
}

func (c *capture) emit(_ context.Context, o model.Observation) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail {
		return errors.New("downstream unavailable")
	}
	c.obs = append(c.obs, o)
	return nil
}

func (c *capture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.obs)
}

// envelopeBody builds a webhook envelope JSON with the given data.type/id at created_at.
func envelopeBody(eventID, dataType, dataID string, created time.Time) []byte {
	return []byte(fmt.Sprintf(`{"type":"event","id":%q,"created_at":%q,"data":{"type":%q,"id":%q,"workspace_id":"ws_1"}}`,
		eventID, created.UTC().Format(time.RFC3339), dataType, dataID))
}

func newTestReceiver(t *testing.T, c *capture) *webhookReceiver {
	t.Helper()
	r, err := newWebhookReceiver(testSecret, defaultWebhookMaxSkew, fixedClock(testTime), c.emit, nil)
	if err != nil {
		t.Fatalf("newWebhookReceiver: %v", err)
	}
	return r
}

func signedRequest(body []byte) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/cma/webhooks", strings.NewReader(string(body)))
	req.Header.Set(webhookSigHeader, signWebhookBody(testSecret, body))
	return req
}

func TestWebhookValidDeliveryEmits(t *testing.T) {
	c := &capture{}
	r := newTestReceiver(t, c)
	body := envelopeBody("event_1", "session.status_idled", "sesn_1", testTime)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, signedRequest(body))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if c.count() != 1 {
		t.Fatalf("emitted %d observations, want 1", c.count())
	}
	fr, ok := c.obs[0].(model.FindingReport)
	if !ok || fr.SubjectKind != kindManagedAgent || fr.Kind != findingForensic {
		t.Errorf("unexpected observation %+v", c.obs[0])
	}
}

func TestWebhookRefreshFailedIsGovernanceFinding(t *testing.T) {
	c := &capture{}
	r := newTestReceiver(t, c)
	body := envelopeBody("event_rf", evtRefreshFailed, "vcrd_9", testTime)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, signedRequest(body))
	if w.Code != http.StatusOK || c.count() != 1 {
		t.Fatalf("status=%d count=%d, want 200/1", w.Code, c.count())
	}
	fr := c.obs[0].(model.FindingReport)
	if fr.Kind != findingGovernance || fr.Severity != model.SeverityMedium || fr.SubjectKind != kindVaultCred {
		t.Errorf("refresh_failed should be a governance/Medium/vault_credential finding, got %+v", fr)
	}
}

func TestWebhookRejectsBadSignature(t *testing.T) {
	c := &capture{}
	r := newTestReceiver(t, c)
	body := envelopeBody("event_2", "session.status_idled", "sesn_2", testTime)
	req := httptest.NewRequest(http.MethodPost, "/cma/webhooks", strings.NewReader(string(body)))
	req.Header.Set(webhookSigHeader, "v1,"+"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if c.count() != 0 {
		t.Error("a bad-signature delivery must emit nothing")
	}
}

func TestWebhookRejectsMissingSignature(t *testing.T) {
	c := &capture{}
	r := newTestReceiver(t, c)
	body := envelopeBody("event_3", "session.status_idled", "sesn_3", testTime)
	req := httptest.NewRequest(http.MethodPost, "/cma/webhooks", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized || c.count() != 0 {
		t.Fatalf("missing signature: status=%d count=%d, want 401/0", w.Code, c.count())
	}
}

func TestWebhookRejectsStale(t *testing.T) {
	c := &capture{}
	r := newTestReceiver(t, c)
	// created_at 10 minutes before the receiver clock; skew window is 5 minutes.
	body := envelopeBody("event_4", "session.status_idled", "sesn_4", testTime.Add(-10*time.Minute))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, signedRequest(body))
	if w.Code != http.StatusUnauthorized || c.count() != 0 {
		t.Fatalf("stale: status=%d count=%d, want 401/0", w.Code, c.count())
	}
}

func TestWebhookRejectsFuture(t *testing.T) {
	c := &capture{}
	r := newTestReceiver(t, c)
	body := envelopeBody("event_5", "session.status_idled", "sesn_5", testTime.Add(10*time.Minute))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, signedRequest(body))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("future-dated: status = %d, want 401", w.Code)
	}
}

func TestWebhookReplayIsAckedNotReEmitted(t *testing.T) {
	c := &capture{}
	r := newTestReceiver(t, c)
	body := envelopeBody("event_dup", "vault.created", "vlt_1", testTime)

	w1 := httptest.NewRecorder()
	r.ServeHTTP(w1, signedRequest(body))
	if w1.Code != http.StatusOK || c.count() != 1 {
		t.Fatalf("first delivery: status=%d count=%d, want 200/1", w1.Code, c.count())
	}
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, signedRequest(body))
	if w2.Code != http.StatusOK {
		t.Fatalf("replay status = %d, want 200 (ack the retry)", w2.Code)
	}
	if c.count() != 1 {
		t.Errorf("a replayed event.id must NOT be re-emitted; count=%d want 1", c.count())
	}
}

func TestWebhookRejectsMethodAndMalformed(t *testing.T) {
	c := &capture{}
	r := newTestReceiver(t, c)

	get := httptest.NewRequest(http.MethodGet, "/cma/webhooks", nil)
	wg := httptest.NewRecorder()
	r.ServeHTTP(wg, get)
	if wg.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", wg.Code)
	}

	bad := []byte(`{not json`)
	wb := httptest.NewRecorder()
	r.ServeHTTP(wb, signedRequest(bad))
	if wb.Code != http.StatusBadRequest {
		t.Errorf("malformed body status = %d, want 400", wb.Code)
	}
	if c.count() != 0 {
		t.Error("neither a wrong method nor malformed body may emit")
	}
}

// TestWebhookRejectsMissingTimestamp proves a signed-but-undated delivery is rejected
// (deny-closed) rather than silently skipping the freshness window.
func TestWebhookRejectsMissingTimestamp(t *testing.T) {
	c := &capture{}
	r := newTestReceiver(t, c)
	body := []byte(`{"type":"event","id":"event_nots","data":{"type":"session.status_idled","id":"sesn_x"}}`)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, signedRequest(body))
	if w.Code != http.StatusBadRequest || c.count() != 0 {
		t.Fatalf("missing created_at: status=%d count=%d, want 400/0", w.Code, c.count())
	}
}

// TestWebhookRejectsUnparseableTimestamp proves a present-but-garbage created_at is rejected.
func TestWebhookRejectsUnparseableTimestamp(t *testing.T) {
	c := &capture{}
	r := newTestReceiver(t, c)
	body := []byte(`{"type":"event","id":"event_badts","created_at":"not-a-date","data":{"type":"vault.created","id":"vlt_x"}}`)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, signedRequest(body))
	if w.Code != http.StatusBadRequest || c.count() != 0 {
		t.Fatalf("bad created_at: status=%d count=%d, want 400/0", w.Code, c.count())
	}
}

// TestWebhookRejectsMissingEventID proves an id-less delivery is rejected (deny-closed)
// rather than bypassing replay de-dup.
func TestWebhookRejectsMissingEventID(t *testing.T) {
	c := &capture{}
	r := newTestReceiver(t, c)
	body := []byte(`{"type":"event","created_at":"` + testTime.UTC().Format(time.RFC3339) + `","data":{"type":"session.status_idled","id":"sesn_x"}}`)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, signedRequest(body))
	if w.Code != http.StatusBadRequest || c.count() != 0 {
		t.Fatalf("missing event.id: status=%d count=%d, want 400/0", w.Code, c.count())
	}
}

func TestWebhookDownstreamErrorIs503(t *testing.T) {
	c := &capture{fail: true}
	r := newTestReceiver(t, c)
	body := envelopeBody("event_6", "vault.created", "vlt_6", testTime)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, signedRequest(body))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("downstream-unavailable status = %d, want 503 (so Anthropic retries)", w.Code)
	}
}

// TestWebhookRetryAfterSinkFailureReEmits proves the fix: a delivery whose sink
// failed (503) must NOT be remembered as seen — the retry of the SAME event.id has to
// re-emit, or the observation is silently lost between the 503 and the dedup window.
func TestWebhookRetryAfterSinkFailureReEmits(t *testing.T) {
	c := &capture{fail: true}
	r := newTestReceiver(t, c)
	body := envelopeBody("event_retry", "vault.created", "vlt_r", testTime)

	w1 := httptest.NewRecorder()
	r.ServeHTTP(w1, signedRequest(body))
	if w1.Code != http.StatusServiceUnavailable || c.count() != 0 {
		t.Fatalf("failed delivery: status=%d count=%d, want 503/0", w1.Code, c.count())
	}

	c.mu.Lock()
	c.fail = false
	c.mu.Unlock()
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, signedRequest(body))
	if w2.Code != http.StatusOK || c.count() != 1 {
		t.Fatalf("retry after sink recovery: status=%d count=%d, want 200/1 (the fact must not be lost)", w2.Code, c.count())
	}
}

// TestWebhookEnrichmentObservationsRideTheDelivery proves a verified session envelope
// triggers the GET-back enrichment and its observations are emitted alongside the
// envelope finding — and that enrichment runs only for first-seen deliveries.
func TestWebhookEnrichmentObservationsRideTheDelivery(t *testing.T) {
	c := &capture{}
	calls := 0
	enrich := func(_ context.Context, ev webhookEnvelope, at time.Time) []model.Observation {
		calls++
		return []model.Observation{permissionPolicyEdge(ev.Data.ID, at)}
	}
	r, err := newWebhookReceiver(testSecret, defaultWebhookMaxSkew, fixedClock(testTime), c.emit, enrich)
	if err != nil {
		t.Fatalf("newWebhookReceiver: %v", err)
	}
	body := envelopeBody("event_enr", "session.status_idled", "sesn_e", testTime)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, signedRequest(body))
	if w.Code != http.StatusOK || c.count() != 2 || calls != 1 {
		t.Fatalf("enriched delivery: status=%d count=%d calls=%d, want 200/2/1", w.Code, c.count(), calls)
	}
	if _, ok := c.obs[1].(model.EdgeObservation); !ok {
		t.Errorf("enrichment observation missing, got %+v", c.obs)
	}
	// Replay: acked, no enrichment re-run, nothing re-emitted.
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, signedRequest(body))
	if w2.Code != http.StatusOK || c.count() != 2 || calls != 1 {
		t.Errorf("replay must not re-enrich/re-emit: status=%d count=%d calls=%d", w2.Code, c.count(), calls)
	}
}

func TestNewWebhookReceiverRequiresSecret(t *testing.T) {
	if _, err := newWebhookReceiver("", defaultWebhookMaxSkew, nil, func(context.Context, model.Observation) error { return nil }, nil); err == nil {
		t.Error("a receiver with no signing secret must not be constructed")
	}
	if _, err := newWebhookReceiver("whsec_", defaultWebhookMaxSkew, nil, func(context.Context, model.Observation) error { return nil }, nil); err == nil {
		t.Error("a receiver with an empty secret body must not be constructed")
	}
}
