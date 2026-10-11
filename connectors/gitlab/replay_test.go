// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

type glDelivery struct {
	token, id, idem, ts, sig, event string
}

func postPush(t *testing.T, h http.HandlerFunc, body []byte, d glDelivery) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body)))
	req.Header.Set("X-Gitlab-Event", "Push Hook")
	for k, v := range map[string]string{
		"X-Gitlab-Event": d.event,
		"X-Gitlab-Token": d.token, "webhook-id": d.id, "Idempotency-Key": d.idem,
		"webhook-timestamp": d.ts, "webhook-signature": d.sig,
	} {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
	w := httptest.NewRecorder()
	h(w, req)
	return w.Code
}

func openReceiver(t *testing.T) *Source {
	t.Helper()
	s := New()
	if err := s.Open(context.Background(), validConfig()); err != nil {
		t.Fatal(err)
	}
	return s
}

// A replayed delivery is dropped: by Idempotency-Key, or by webhook-id when it is the only ID.
func TestWebhookReplayedDeliveryDropped(t *testing.T) {
	body := loadFixture(t, "testdata/push.json")
	for name, d := range map[string]glDelivery{
		"Idempotency-Key": {token: "whsec-test-secret", idem: "k-1"},
		"webhook-id":      {token: "whsec-test-secret", id: "msg_5"},
	} {
		sink := &collectSink{}
		h := openReceiver(t).handleWebhook(sink)
		postPush(t, h, body, d)
		first := len(sink.obs)
		if code := postPush(t, h, body, d); code != http.StatusOK || first == 0 || len(sink.obs) != first {
			t.Errorf("%s: replay status %d, observations %d -> %d; want 200 and no new ones", name, code, first, len(sink.obs))
		}
	}

}

// Without a signing token the event is part of the key: a copy under another
// (unsigned) X-Gitlab-Event cannot use up the genuine delivery's ID.
func TestWebhookUnsignedOtherEventDoesNotClaimID(t *testing.T) {
	body := loadFixture(t, "testdata/push.json")
	sink := &collectSink{}
	h := openReceiver(t).handleWebhook(sink)
	d := glDelivery{token: "whsec-test-secret", idem: "k-e", event: "Unknown Hook"}
	if code := postPush(t, h, body, d); code != http.StatusOK || len(sink.obs) != 0 {
		t.Fatalf("other event: status %d, observations %d", code, len(sink.obs))
	}
	d.event = "Push Hook"
	if code := postPush(t, h, body, d); code != http.StatusOK || len(sink.obs) == 0 {
		t.Fatalf("genuine: status %d, observations %d; want it processed", code, len(sink.obs))
	}
}

// A request with a wrong X-Gitlab-Token cannot use up an ID: the genuine
// delivery still runs, and a forged copy of it is refused, not answered as a
// duplicate.
func TestWebhookForgedTokenDoesNotClaimID(t *testing.T) {
	body := loadFixture(t, "testdata/push.json")
	sink := &collectSink{}
	h := openReceiver(t).handleWebhook(sink)
	good := glDelivery{token: "whsec-test-secret", id: "msg_t", idem: "msg_t"}
	forged := good
	forged.token = "nope"
	if code := postPush(t, h, body, forged); code != http.StatusForbidden {
		t.Fatalf("forged: status %d, want 403", code)
	}
	if code := postPush(t, h, body, good); code != http.StatusOK || len(sink.obs) == 0 {
		t.Fatalf("genuine: status %d, observations %d", code, len(sink.obs))
	}
	if code := postPush(t, h, body, forged); code != http.StatusForbidden {
		t.Fatalf("forged replay: status %d, want 403", code)
	}
}

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

// A delivery that failed is retried by GitLab with the same ID; the retry runs.
func TestWebhookFailedDeliveryRetried(t *testing.T) {
	body := loadFixture(t, "testdata/push.json")
	sink := &failOnceSink{}
	h := openReceiver(t).handleWebhook(sink)
	d := glDelivery{token: "whsec-test-secret", id: "msg_r", idem: "msg_r"}
	if code := postPush(t, h, body, d); code != http.StatusInternalServerError {
		t.Fatalf("first: status %d, want 500", code)
	}
	if code := postPush(t, h, body, d); code != http.StatusOK || len(sink.obs) == 0 {
		t.Fatalf("retry: status %d, observations %d", code, len(sink.obs))
	}
}
