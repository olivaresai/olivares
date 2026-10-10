// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

var testSigningKey = []byte("0123456789abcdef0123456789abcdef")

func testSigningToken() string { return "whsec_" + base64.StdEncoding.EncodeToString(testSigningKey) }

// glSign computes the GitLab 19.x webhook-signature value for one delivery.
func glSign(key []byte, id, ts string, body []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id + "." + ts + "."))
	mac.Write(body)
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func openSigned(t *testing.T, signing string) *Source {
	t.Helper()
	cfg := validConfig()
	if signing != "" {
		cfg.Settings["webhook_signing_token"] = signing
	}
	s := New()
	if err := s.Open(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	return s
}

func signed(body []byte, id string, at time.Time) glDelivery {
	ts := strconv.FormatInt(at.Unix(), 10)
	return glDelivery{token: "whsec-test-secret", id: id, idem: id, ts: ts, sig: glSign(testSigningKey, id, ts, body)}
}

func TestOpenSigningTokenFormat(t *testing.T) {
	for _, bad := range []string{"plain-secret", "whsec_", "whsec_!!notbase64"} {
		cfg := validConfig()
		cfg.Settings["webhook_signing_token"] = bad
		if err := New().Open(context.Background(), cfg); err == nil {
			t.Errorf("signing token %q: Open accepted it", bad)
		}
	}
}

func TestDescriptorSigningTokenIsSecret(t *testing.T) {
	for _, f := range New().Descriptor().ConfigFields {
		if f.Key == "webhook_signing_token" {
			if !f.Secret || f.Required {
				t.Fatalf("webhook_signing_token: secret=%v required=%v, want secret optional", f.Secret, f.Required)
			}
			return
		}
	}
	t.Fatal("webhook_signing_token not declared")
}

// A GitLab 19.x signed delivery verifies.
func TestWebhookSignedDeliveryVerifies(t *testing.T) {
	body := loadFixture(t, "testdata/push.json")
	sink := &collectSink{}
	h := openSigned(t, testSigningToken()).handleWebhook(sink)
	d := signed(body, "msg_1", time.Now())
	d.sig = "v1,AAAA " + d.sig // GitLab may send several space-separated signatures.
	if code := postPush(t, h, body, d); code != http.StatusOK || len(sink.edges()) == 0 {
		t.Fatalf("status %d, edges %d; want 200 and edges", code, len(sink.edges()))
	}
}

// A bad, missing or stale signature is refused when a signing token is set.
func TestWebhookBadSignatureRefused(t *testing.T) {
	body := loadFixture(t, "testdata/push.json")
	now := time.Now()
	good := signed(body, "msg_2", now)
	cases := map[string]glDelivery{}
	d := good
	d.sig = glSign([]byte("another key, another key, 32 by."), d.id, d.ts, body)
	cases["wrong key"] = d
	d = good
	d.sig = ""
	cases["missing signature"] = d
	d = good
	d.id, d.idem = "msg_other", "msg_other" // signature no longer covers the id
	cases["id not signed"] = d
	cases["stale timestamp"] = signed(body, "msg_3", now.Add(-10*time.Minute))
	cases["future timestamp"] = signed(body, "msg_4", now.Add(10*time.Minute))
	d = good
	d.token = "wrong-legacy-token"
	cases["legacy token still checked"] = d

	for name, d := range cases {
		sink := &collectSink{}
		h := openSigned(t, testSigningToken()).handleWebhook(sink)
		if code := postPush(t, h, body, d); code != http.StatusForbidden || len(sink.obs) != 0 {
			t.Errorf("%s: status %d, observations %d; want 403 and none", name, code, len(sink.obs))
		}
	}

	tampered := append([]byte(nil), body...)
	tampered[len(tampered)-2] = ' '
	sink := &collectSink{}
	h := openSigned(t, testSigningToken()).handleWebhook(sink)
	if code := postPush(t, h, tampered, good); code != http.StatusForbidden || len(sink.obs) != 0 {
		t.Errorf("tampered body: status %d, observations %d; want 403 and none", code, len(sink.obs))
	}
}

// Without a signing token the legacy X-Gitlab-Token still works, signed headers or not.
func TestWebhookLegacyTokenStillWorks(t *testing.T) {
	body := loadFixture(t, "testdata/push.json")
	sink := &collectSink{}
	h := openSigned(t, "").handleWebhook(sink)
	if code := postPush(t, h, body, glDelivery{token: "whsec-test-secret"}); code != http.StatusOK || len(sink.edges()) == 0 {
		t.Fatalf("legacy: status %d, edges %d", code, len(sink.edges()))
	}
	if code := postPush(t, h, body, glDelivery{token: "nope"}); code != http.StatusForbidden {
		t.Fatalf("legacy wrong token: status %d, want 403", code)
	}
}

// A delivery with a bad signature cannot use up an ID: the genuine delivery
// still runs, and a forged copy of it is refused, not answered as a duplicate.
func TestWebhookForgedSignatureDoesNotClaimID(t *testing.T) {
	body := loadFixture(t, "testdata/push.json")
	sink := &collectSink{}
	h := openSigned(t, testSigningToken()).handleWebhook(sink)
	good := signed(body, "msg_f", time.Now())
	forged := good
	forged.sig = glSign([]byte("x"), good.id, good.ts, body)
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

// A replayed signed delivery is dropped by its signed webhook-id.
func TestWebhookSignedReplayDropped(t *testing.T) {
	body := loadFixture(t, "testdata/push.json")
	sink := &collectSink{}
	h := openSigned(t, testSigningToken()).handleWebhook(sink)
	d := signed(body, "msg_6", time.Now())
	postPush(t, h, body, d)
	first := len(sink.obs)
	if code := postPush(t, h, body, d); code != http.StatusOK || len(sink.obs) != first {
		t.Errorf("signed replay: status %d, observations %d -> %d", code, first, len(sink.obs))
	}
	// Idempotency-Key is not signed: a replayer who changes it is still dropped
	// by the signed webhook-id.
	d.idem = "forged-key"
	if code := postPush(t, h, body, d); code != http.StatusOK || len(sink.obs) != first {
		t.Errorf("signed replay with a new Idempotency-Key: status %d, observations %d -> %d", code, first, len(sink.obs))
	}
	// X-Gitlab-Event is not signed either, and Tag Push Hook builds the same
	// edges as Push Hook: a replayer who changes it is still dropped.
	d.event = "Tag Push Hook"
	if code := postPush(t, h, body, d); code != http.StatusOK || len(sink.obs) != first {
		t.Errorf("signed replay with a new X-Gitlab-Event: status %d, observations %d -> %d", code, first, len(sink.obs))
	}
}

// Signatures may arrive in more than one webhook-signature header line.
func TestWebhookSignatureHeaderLines(t *testing.T) {
	body := loadFixture(t, "testdata/push.json")
	d := signed(body, "msg_l", time.Now())
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body)))
	req.Header.Set("X-Gitlab-Event", "Push Hook")
	req.Header.Set("X-Gitlab-Token", d.token)
	req.Header.Set("webhook-id", d.id)
	req.Header.Set("webhook-timestamp", d.ts)
	req.Header.Add("webhook-signature", "v1,AAAA")
	req.Header.Add("webhook-signature", d.sig)
	w := httptest.NewRecorder()
	openSigned(t, testSigningToken()).handleWebhook(&collectSink{})(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", w.Code)
	}
}

// Reopening without a signing token turns signature checks off again.
func TestOpenResetsSigningKey(t *testing.T) {
	s := openSigned(t, testSigningToken())
	if err := s.Open(context.Background(), validConfig()); err != nil {
		t.Fatal(err)
	}
	if s.signingKey != nil {
		t.Fatal("signing key kept after reopening without one")
	}
}
