// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	jwt "github.com/go-jose/go-jose/v4/jwt"
)

func pushClock() time.Time { return time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC) }

const (
	pushAud = "https://webhook.olivares.example/a2a/push"
	pushIss = "https://billing.example"
)

// mintPushJWT signs a push-authentication JWT with a fresh EC key, returning the token
// plus the matching public JWKS. alg/kid/claims are overridable for negative tests.
func mintPushJWT(t *testing.T, alg jose.SignatureAlgorithm, kid string, claims jwt.Claims) (token string, jwks []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: alg, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", kid))
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	ks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: key.Public(), KeyID: kid, Algorithm: string(alg), Use: "sig"}}}
	blob, err := json.Marshal(ks)
	if err != nil {
		t.Fatalf("marshal jwks: %v", err)
	}
	return raw, blob
}

func validPushClaims(jti string) jwt.Claims {
	now := pushClock()
	return jwt.Claims{
		Issuer:   pushIss,
		Subject:  "billing-agent",
		Audience: jwt.Audience{pushAud},
		IssuedAt: jwt.NewNumericDate(now.Add(-time.Minute)),
		Expiry:   jwt.NewNumericDate(now.Add(5 * time.Minute)),
		ID:       jti,
	}
}

func newPushReceiver(t *testing.T, jwks []byte, onUpdate func(context.Context, TaskUpdate)) *PushReceiver {
	t.Helper()
	r, err := NewPushReceiver(PushReceiverConfig{
		Audience:       pushAud,
		IssuerJWKS:     jwks,
		AllowedIssuers: []string{pushIss},
		OnUpdate:       onUpdate,
		Clock:          pushClock,
	})
	if err != nil {
		t.Fatalf("new push receiver: %v", err)
	}
	return r
}

func pushRequest(token, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, pushAud, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

// validPushBody is the LEGACY (v0.3-style) bare-event webhook body, parsed as
// lenient fallback; the v1.0 body is a StreamResponse object (validPushBodyV1).
const validPushBody = `{"taskId":"t1","contextId":"c","status":{"state":"TASK_STATE_COMPLETED"}}`

// validPushBodyV1 is the v1.0 webhook payload (§4.3.3): a StreamResponse with
// exactly one member — here a statusUpdate, as in the spec's §6.6 worked example.
const validPushBodyV1 = `{"statusUpdate":{"taskId":"t1","contextId":"c","status":{"state":"TASK_STATE_COMPLETED","timestamp":"2026-06-08T11:59:00.000Z"}}}`

// TestPushValid: a correctly signed, audience-bound, fresh token → 204 + onUpdate.
func TestPushValid(t *testing.T) {
	token, jwks := mintPushJWT(t, jose.ES256, "k1", validPushClaims("jti-1"))
	var got TaskUpdate
	rec := newPushReceiver(t, jwks, func(_ context.Context, u TaskUpdate) { got = u })
	w := httptest.NewRecorder()
	rec.ServeHTTP(w, pushRequest(token, validPushBody))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
	if got.TaskID != "t1" || got.State != TaskStateCompleted || got.Sender != pushIss {
		t.Errorf("update = %+v, want t1/COMPLETED/%s", got, pushIss)
	}
}

// TestPushValidV1StreamResponse: the v1.0 StreamResponse-wrapped statusUpdate body
// is parsed (taskId/contextId/status) and delivered to onUpdate.
func TestPushValidV1StreamResponse(t *testing.T) {
	token, jwks := mintPushJWT(t, jose.ES256, "k1", validPushClaims("jti-v1"))
	var got TaskUpdate
	rec := newPushReceiver(t, jwks, func(_ context.Context, u TaskUpdate) { got = u })
	w := httptest.NewRecorder()
	rec.ServeHTTP(w, pushRequest(token, validPushBodyV1))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
	if got.TaskID != "t1" || got.State != TaskStateCompleted || !got.Terminal {
		t.Errorf("v1.0 statusUpdate mishandled: %+v", got)
	}
}

func TestPushAcknowledgesOnlyAfterDurableSettlement(t *testing.T) {
	token, jwks := mintPushJWT(t, jose.ES256, "k1", validPushClaims("jti-durable-fail"))
	observed := false
	rec, err := NewPushReceiver(PushReceiverConfig{
		Audience: pushAud, IssuerJWKS: jwks, AllowedIssuers: []string{pushIss}, Clock: pushClock,
		OnUpdateDurable: func(context.Context, TaskUpdate) error { return errors.New("store unavailable") },
		OnUpdate:        func(context.Context, TaskUpdate) { observed = true },
	})
	if err != nil {
		t.Fatalf("new push receiver: %v", err)
	}
	w := httptest.NewRecorder()
	rec.ServeHTTP(w, pushRequest(token, validPushBodyV1))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 until durable settlement succeeds", w.Code)
	}
	if observed {
		t.Fatal("post-settlement observer ran after durable settlement failed")
	}

	token, jwks = mintPushJWT(t, jose.ES256, "k2", validPushClaims("jti-durable-ok"))
	durable := false
	rec, err = NewPushReceiver(PushReceiverConfig{
		Audience: pushAud, IssuerJWKS: jwks, AllowedIssuers: []string{pushIss}, Clock: pushClock,
		OnUpdateDurable: func(context.Context, TaskUpdate) error { durable = true; return nil },
		OnUpdate: func(context.Context, TaskUpdate) {
			if !durable {
				t.Error("observer ran before durable settlement")
			}
			observed = true
		},
	})
	if err != nil {
		t.Fatalf("new push receiver: %v", err)
	}
	observed = false
	w = httptest.NewRecorder()
	rec.ServeHTTP(w, pushRequest(token, validPushBodyV1))
	if w.Code != http.StatusNoContent || !durable || !observed {
		t.Fatalf("status/durable/observed = %d/%v/%v, want 204/true/true", w.Code, durable, observed)
	}
}

func TestPushDurableFailureDoesNotBurnRetry(t *testing.T) {
	token, jwks := mintPushJWT(t, jose.ES256, "k1", validPushClaims("jti-rollback-retry"))
	calls := 0
	rec, err := NewPushReceiver(PushReceiverConfig{
		Audience: pushAud, IssuerJWKS: jwks, AllowedIssuers: []string{pushIss}, Clock: pushClock,
		OnUpdateDurable: func(context.Context, TaskUpdate) error {
			calls++
			if calls == 1 {
				return errors.New("rolled back")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("new push receiver: %v", err)
	}
	first := httptest.NewRecorder()
	rec.ServeHTTP(first, pushRequest(token, validPushBodyV1))
	second := httptest.NewRecorder()
	rec.ServeHTTP(second, pushRequest(token, validPushBodyV1))
	if first.Code != http.StatusServiceUnavailable || second.Code != http.StatusNoContent || calls != 2 {
		t.Fatalf("rollback retry statuses/calls = %d/%d/%d, want 503/204/2",
			first.Code, second.Code, calls)
	}
}

func TestPushProjectsMessageAndArtifactRepliesDurably(t *testing.T) {
	cases := []struct {
		name string
		body string
		want ReplyEventKind
	}{
		{
			name: "message",
			body: `{"message":{"messageId":"m1","contextId":"c","role":"ROLE_AGENT","parts":[{"text":"done"},{"data":{"score":1}}]}}`,
			want: ReplyEventMessage,
		},
		{
			name: "artifact",
			body: `{"artifactUpdate":{"taskId":"t1","contextId":"c","artifact":{"artifactId":"a1","parts":[{"file":{"uri":"https://example.test/result?q=private"}}]},"lastChunk":true}}`,
			want: ReplyEventArtifact,
		},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token, jwks := mintPushJWT(t, jose.ES256, "k1", validPushClaims(fmt.Sprintf("jti-reply-%d", i)))
			var got ReplyEvent
			observed := false
			rec, err := NewPushReceiver(PushReceiverConfig{
				Audience: pushAud, IssuerJWKS: jwks, AllowedIssuers: []string{pushIss}, Clock: pushClock,
				OnReplyDurable: func(_ context.Context, reply ReplyEvent) error {
					got = reply
					return nil
				},
				OnReply: func(context.Context, ReplyEvent) { observed = true },
			})
			if err != nil {
				t.Fatalf("new push receiver: %v", err)
			}
			w := httptest.NewRecorder()
			rec.ServeHTTP(w, pushRequest(token, tc.body))
			if w.Code != http.StatusNoContent || !observed {
				t.Fatalf("status/observed = %d/%v, want 204/true", w.Code, observed)
			}
			if got.Kind != tc.want || got.Sender != pushIss || got.ContextID != "c" ||
				got.ReplayID != fmt.Sprintf("jti-reply-%d", i) || len(got.Digest) != 64 ||
				len(got.Parts) == 0 {
				t.Fatalf("reply projection = %+v", got)
			}
			for _, part := range got.Parts {
				if part.Digest == "" || (part.Kind != "text" && part.Text != "") {
					t.Fatalf("unsafe reply part = %+v", part)
				}
			}
		})
	}
}

func TestPushRejectsInvalidReplyBeforeCallback(t *testing.T) {
	token, jwks := mintPushJWT(t, jose.ES256, "k1", validPushClaims("jti-invalid-reply"))
	called := false
	rec, err := NewPushReceiver(PushReceiverConfig{
		Audience: pushAud, IssuerJWKS: jwks, AllowedIssuers: []string{pushIss}, Clock: pushClock,
		OnReplyDurable: func(context.Context, ReplyEvent) error { called = true; return nil },
	})
	if err != nil {
		t.Fatalf("new push receiver: %v", err)
	}
	w := httptest.NewRecorder()
	rec.ServeHTTP(w, pushRequest(token,
		`{"message":{"messageId":"m1","contextId":"c","role":"ROLE_USER","parts":[{"text":"done"}]}}`))
	if w.Code != http.StatusBadRequest || called {
		t.Fatalf("invalid reply status/callback = %d/%v, want 400/false", w.Code, called)
	}
}

// TestPushStatelessVariantsAcked: message / artifactUpdate StreamResponse variants
// carry no lifecycle state — the receiver MUST still ACK 2xx (§4.3.3, else a
// conformant sender retries forever) but delivers no update.
func TestPushStatelessVariantsAcked(t *testing.T) {
	for _, body := range []string{
		`{"message":{"messageId":"m1","contextId":"c","role":"ROLE_AGENT","parts":[{"text":"x"}]}}`,
		`{"artifactUpdate":{"taskId":"t1","contextId":"c","artifact":{"artifactId":"a1","parts":[{"text":"x"}]},"lastChunk":true}}`,
	} {
		token, jwks := mintPushJWT(t, jose.ES256, "k1", validPushClaims("jti-"+body[3:9]))
		called := false
		rec := newPushReceiver(t, jwks, func(context.Context, TaskUpdate) { called = true })
		w := httptest.NewRecorder()
		rec.ServeHTTP(w, pushRequest(token, body))
		if w.Code != http.StatusNoContent {
			t.Errorf("stateless variant must be ACKED 204, got %d for %s", w.Code, body)
		}
		if called {
			t.Errorf("stateless variant must not call onUpdate: %s", body)
		}
	}
}

// TestPushWrongAudience: a token minted for another audience is the confused-deputy
// case → 401, onUpdate never called.
func TestPushWrongAudience(t *testing.T) {
	cl := validPushClaims("jti-2")
	cl.Audience = jwt.Audience{"https://someone-else.example/push"}
	token, jwks := mintPushJWT(t, jose.ES256, "k1", cl)
	called := false
	rec := newPushReceiver(t, jwks, func(context.Context, TaskUpdate) { called = true })
	w := httptest.NewRecorder()
	rec.ServeHTTP(w, pushRequest(token, validPushBody))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong-audience status = %d, want 401", w.Code)
	}
	if called {
		t.Error("onUpdate must NOT be called for a wrong-audience token")
	}
}

// TestPushExpired: an expired token → 401.
func TestPushExpired(t *testing.T) {
	cl := validPushClaims("jti-3")
	cl.Expiry = jwt.NewNumericDate(pushClock().Add(-time.Hour))
	token, jwks := mintPushJWT(t, jose.ES256, "k1", cl)
	rec := newPushReceiver(t, jwks, nil)
	w := httptest.NewRecorder()
	rec.ServeHTTP(w, pushRequest(token, validPushBody))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expired status = %d, want 401", w.Code)
	}
}

// TestPushReplay: re-presenting the same jti is rejected.
func TestPushReplay(t *testing.T) {
	token, jwks := mintPushJWT(t, jose.ES256, "k1", validPushClaims("jti-replay"))
	rec := newPushReceiver(t, jwks, nil)
	w1 := httptest.NewRecorder()
	rec.ServeHTTP(w1, pushRequest(token, validPushBody))
	if w1.Code != http.StatusNoContent {
		t.Fatalf("first push status = %d, want 204", w1.Code)
	}
	w2 := httptest.NewRecorder()
	rec.ServeHTTP(w2, pushRequest(token, validPushBody))
	if w2.Code != http.StatusUnauthorized {
		t.Errorf("replayed push status = %d, want 401", w2.Code)
	}
}

func TestPushDurableReplayAuthorityReceivesVerifiedClaims(t *testing.T) {
	token, jwks := mintPushJWT(t, jose.ES256, "k1", validPushClaims("jti-durable-replay"))
	calls := 0
	rec, err := NewPushReceiver(PushReceiverConfig{
		Audience: pushAud, IssuerJWKS: jwks, AllowedIssuers: []string{pushIss}, Clock: pushClock,
		OnUpdateDurable: func(_ context.Context, update TaskUpdate) error {
			calls++
			if update.ReplayID != "jti-durable-replay" ||
				!update.ReplayExpiresAt.Equal(pushClock().Add(5*time.Minute)) {
				t.Fatalf("durable replay projection = %+v", update)
			}
			if calls > 1 {
				return ErrReplay
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("new durable receiver: %v", err)
	}
	first := httptest.NewRecorder()
	rec.ServeHTTP(first, pushRequest(token, validPushBodyV1))
	second := httptest.NewRecorder()
	rec.ServeHTTP(second, pushRequest(token, validPushBodyV1))
	if first.Code != http.StatusNoContent || second.Code != http.StatusUnauthorized || calls != 2 {
		t.Fatalf("durable replay statuses/calls = %d/%d/%d, want 204/401/2",
			first.Code, second.Code, calls)
	}
}

// TestPushSymmetricAlgRejected: an HS256 token must be rejected (algorithm-confusion
// defense — only asymmetric algs are accepted).
func TestPushSymmetricAlgRejected(t *testing.T) {
	// Mint an HS256 token directly; its key is symmetric and not in the trust anchor.
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: []byte("0123456789abcdef0123456789abcdef")},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	if err != nil {
		t.Fatalf("hs256 signer: %v", err)
	}
	token, err := jwt.Signed(signer).Claims(validPushClaims("jti-hs")).Serialize()
	if err != nil {
		t.Fatalf("hs256 sign: %v", err)
	}
	_, jwks := mintPushJWT(t, jose.ES256, "k1", validPushClaims("ignored"))
	rec := newPushReceiver(t, jwks, nil)
	w := httptest.NewRecorder()
	rec.ServeHTTP(w, pushRequest(token, validPushBody))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("HS256 token status = %d, want 401 (symmetric alg must be rejected)", w.Code)
	}
}

// TestPushNoExpiryRejected: a push token WITHOUT exp is rejected — its validity
// window must bound the replay window (a no-exp token would become replayable once
// its jti ages out of the replay cache).
func TestPushNoExpiryRejected(t *testing.T) {
	cl := validPushClaims("jti-noexp")
	cl.Expiry = nil
	token, jwks := mintPushJWT(t, jose.ES256, "k1", cl)
	rec := newPushReceiver(t, jwks, nil)
	w := httptest.NewRecorder()
	rec.ServeHTTP(w, pushRequest(token, validPushBody))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("no-exp token status = %d, want 401", w.Code)
	}
}

// TestPushMissingBearer: no Authorization header → 401.
func TestPushMissingBearer(t *testing.T) {
	_, jwks := mintPushJWT(t, jose.ES256, "k1", validPushClaims("x"))
	rec := newPushReceiver(t, jwks, nil)
	w := httptest.NewRecorder()
	rec.ServeHTTP(w, pushRequest("", validPushBody))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("missing bearer status = %d, want 401", w.Code)
	}
}

// TestPushWrongIssuer: an iss not on the allowlist → 401.
func TestPushWrongIssuer(t *testing.T) {
	cl := validPushClaims("jti-iss")
	cl.Issuer = "https://evil.example"
	token, jwks := mintPushJWT(t, jose.ES256, "k1", cl)
	rec := newPushReceiver(t, jwks, nil)
	w := httptest.NewRecorder()
	rec.ServeHTTP(w, pushRequest(token, validPushBody))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("wrong-issuer status = %d, want 401", w.Code)
	}
}

// TestPushReceiverRequiresAnchorAudienceAndIssuers: construction fails without a trust
// anchor, without an audience, or without an issuer allowlist (never a silently-open
// receiver — the issuer allowlist is a fail-closed requirement, not optional).
func TestPushReceiverRequiresAnchorAudienceAndIssuers(t *testing.T) {
	_, jwks := mintPushJWT(t, jose.ES256, "k1", validPushClaims("x"))
	if _, err := NewPushReceiver(PushReceiverConfig{Audience: pushAud, AllowedIssuers: []string{pushIss}}); err == nil {
		t.Error("a receiver with no trust anchor must not be constructed")
	}
	if _, err := NewPushReceiver(PushReceiverConfig{IssuerJWKS: jwks, AllowedIssuers: []string{pushIss}}); err == nil {
		t.Error("a receiver with no audience must not be constructed")
	}
	// No issuer allowlist (omitted) → must fail-closed (else any trusted-key issuer is accepted).
	if _, err := NewPushReceiver(PushReceiverConfig{Audience: pushAud, IssuerJWKS: jwks}); err == nil {
		t.Error("a receiver with no allowed_issuers must not be constructed (iss must be pinned)")
	}
	// An allowlist of only blank entries is effectively empty → must also fail.
	if _, err := NewPushReceiver(PushReceiverConfig{Audience: pushAud, IssuerJWKS: jwks, AllowedIssuers: []string{"", "  "}}); err == nil {
		t.Error("a receiver with a blank-only allowed_issuers must not be constructed")
	}
}

func TestRequireHTTPSLoopbackIPv6Exemption(t *testing.T) {
	tests := []struct {
		name    string
		rawURL  string
		wantErr bool
	}{
		{name: "https accepted", rawURL: "https://2001:db8::1/jwks"},
		{name: "localhost http accepted", rawURL: "http://localhost:8080/jwks"},
		{name: "ipv4 loopback http accepted", rawURL: "http://127.0.0.1:8080/jwks"},
		{name: "ipv6 loopback compressed accepted", rawURL: "http://[::1]:8080/jwks"},
		{name: "public ipv6 denied", rawURL: "http://[2001:db8::1]/jwks", wantErr: true},
		{name: "link local zone denied", rawURL: "http://[fe80::1%25eth0]:8080/jwks", wantErr: true},
		{name: "v4 mapped denied", rawURL: "http://[::ffff:192.0.2.1]:8080/jwks", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := requireHTTPS(tt.rawURL)
			if (err != nil) != tt.wantErr {
				t.Fatalf("requireHTTPS(%q) error = %v, wantErr %v", tt.rawURL, err, tt.wantErr)
			}
		})
	}
}
