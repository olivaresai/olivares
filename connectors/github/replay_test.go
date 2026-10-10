// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

type failOnceSink struct {
	collectSink
	failed bool
}

func (f *failOnceSink) Emit(ctx context.Context, o model.Observation) error {
	if !f.failed {
		f.failed = true
		return errors.New("store down")
	}
	return f.collectSink.Emit(ctx, o)
}

func pushDelivery(t *testing.T, h http.HandlerFunc, payload []byte, sig, delivery string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Body = newReadCloser(payload)
	req.Header.Set("X-Hub-Signature-256", sig)
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-GitHub-Delivery", delivery)
	w := httptest.NewRecorder()
	h(w, req)
	return w.Code
}

func openSource(t *testing.T) *Source {
	t.Helper()
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: validConfig()}); err != nil {
		t.Fatal(err)
	}
	return s
}

// A replayed delivery (same X-GitHub-Delivery) is answered 200 and emits nothing.
func TestWebhookReplayedDeliveryDropped(t *testing.T) {
	payload, err := os.ReadFile("testdata/push.json")
	if err != nil {
		t.Fatal(err)
	}
	sig := signPayload(payload, "test-secret")
	sink := &collectSink{}
	h := openSource(t).handleWebhook(sink)

	for i := 0; i < 2; i++ {
		if code := pushDelivery(t, h, payload, sig, "72d3162e-cc78-11e3-81ab-4c9367dc0958"); code != http.StatusOK {
			t.Fatalf("delivery %d: status %d", i, code)
		}
	}
	if n := len(sink.edges()); n != 1 {
		t.Fatalf("edges = %d, want 1 (the replay must be dropped)", n)
	}
	if code := pushDelivery(t, h, payload, sig, "another-delivery"); code != http.StatusOK || len(sink.edges()) != 2 {
		t.Fatalf("a new delivery ID must be processed: status %d, edges %d", code, len(sink.edges()))
	}
}

// X-GitHub-Event is not signed: a copy of a delivery under another event cannot
// use up the genuine delivery's ID.
func TestWebhookOtherEventDoesNotClaimID(t *testing.T) {
	payload, err := os.ReadFile("testdata/push.json")
	if err != nil {
		t.Fatal(err)
	}
	sig := signPayload(payload, "test-secret")
	sink := &collectSink{}
	h := openSource(t).handleWebhook(sink)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Body = newReadCloser(payload)
	req.Header.Set("X-Hub-Signature-256", sig)
	req.Header.Set("X-GitHub-Event", "ping")
	req.Header.Set("X-GitHub-Delivery", "d-event")
	w := httptest.NewRecorder()
	h(w, req)
	if w.Code != http.StatusOK || len(sink.edges()) != 0 {
		t.Fatalf("other event: status %d, edges %d", w.Code, len(sink.edges()))
	}
	if code := pushDelivery(t, h, payload, sig, "d-event"); code != http.StatusOK || len(sink.edges()) != 1 {
		t.Fatalf("genuine: status %d, edges %d; want it processed", code, len(sink.edges()))
	}
}

// A delivery that failed is retried by GitHub with the same ID; the retry runs.
func TestWebhookFailedDeliveryRetried(t *testing.T) {
	payload, err := os.ReadFile("testdata/push.json")
	if err != nil {
		t.Fatal(err)
	}
	sig := signPayload(payload, "test-secret")
	sink := &failOnceSink{}
	h := openSource(t).handleWebhook(sink)

	if code := pushDelivery(t, h, payload, sig, "d1"); code != http.StatusInternalServerError {
		t.Fatalf("first: status %d, want 500", code)
	}
	if code := pushDelivery(t, h, payload, sig, "d1"); code != http.StatusOK || len(sink.edges()) != 1 {
		t.Fatalf("retry: status %d, edges %d; want 200 and 1", code, len(sink.edges()))
	}
}

// A forged request cannot use up a delivery ID: the genuine delivery still runs.
func TestWebhookForgedDeliveryDoesNotClaimID(t *testing.T) {
	payload, err := os.ReadFile("testdata/push.json")
	if err != nil {
		t.Fatal(err)
	}
	sink := &collectSink{}
	h := openSource(t).handleWebhook(sink)

	if code := pushDelivery(t, h, payload, "sha256=00", "d2"); code != http.StatusForbidden {
		t.Fatalf("forged: status %d, want 403", code)
	}
	if code := pushDelivery(t, h, payload, signPayload(payload, "test-secret"), "d2"); code != http.StatusOK || len(sink.edges()) != 1 {
		t.Fatalf("genuine: status %d, edges %d; want 200 and 1", code, len(sink.edges()))
	}
	// Authentication comes before the replay check: a forged copy of a seen
	// delivery is refused, not answered as a duplicate.
	if code := pushDelivery(t, h, payload, "sha256=00", "d2"); code != http.StatusForbidden {
		t.Fatalf("forged replay: status %d, want 403", code)
	}
}
