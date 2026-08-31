// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package ssf

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

const (
	testIssuer    = "https://idp.corp.example"
	testReceiver  = "https://olivares.corp.example/ssf"
	testAgentSub  = "svac_agent_42"
	fixedUnixTime = 1780000000
)

func fixedNow() time.Time { return time.Unix(fixedUnixTime, 0).UTC() }

// testKeys returns an RSA signing key, the transmitter's signing JWK, and the
// public JWKS JSON the receiver verifies against.
func testKeys(t *testing.T) (jose.JSONWebKey, string) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	signJWK := jose.JSONWebKey{Key: priv, KeyID: "k1", Algorithm: "RS256", Use: "sig"}
	pubSet := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &priv.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}}
	b, err := json.Marshal(pubSet)
	if err != nil {
		t.Fatalf("marshal jwks: %v", err)
	}
	return signJWK, string(b)
}

func newReceiver(t *testing.T, jwks, audience, issuer string) *Source {
	t.Helper()
	s := New()
	s.now = fixedNow
	cfg := map[string]string{"listen_addr": "127.0.0.1:0", "audience": audience, "jwks": jwks}
	if issuer != "" {
		cfg["issuer"] = issuer
	}
	if err := s.Open(context.Background(), sdk.Config{Settings: cfg}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestVerifyAndKillSwitch(t *testing.T) {
	signJWK, jwks := testKeys(t)
	s := newReceiver(t, jwks, testReceiver, testIssuer)
	tx := Transmitter{Issuer: testIssuer, Audience: testReceiver, Key: signJWK}
	token, err := tx.RevokeSession(subjectID{Format: "opaque", ID: testAgentSub}, fixedNow(), "jti-1")
	if err != nil {
		t.Fatalf("build SET: %v", err)
	}

	set, err := s.verifySET(context.Background(), token)
	if err != nil {
		t.Fatalf("verifySET (valid) rejected: %v", err)
	}
	if _, ok := set.Events[evtSessionRevoked]; !ok {
		t.Fatalf("SET missing session-revoked event")
	}

	ev, _ := decodeCAEPEvent(set.Events[evtSessionRevoked])
	f, ok := deriveFinding(evtSessionRevoked, ev, set.SubID, set.Iss, set.Jti, fixedNow())
	if !ok {
		t.Fatal("session-revoked produced no finding")
	}
	if f.Kind != "caep_session_revoked" || f.Severity != model.SeverityHigh {
		t.Errorf("finding = (%s, %s), want (caep_session_revoked, high)", f.Kind, f.Severity)
	}
	if f.SubjectRef != testAgentSub {
		t.Errorf("SubjectRef = %q, want %q (the NHI it converges on)", f.SubjectRef, testAgentSub)
	}
}

func TestRejectTamperedSET(t *testing.T) {
	signJWK, jwks := testKeys(t)
	s := newReceiver(t, jwks, testReceiver, testIssuer)
	tx := Transmitter{Issuer: testIssuer, Audience: testReceiver, Key: signJWK}
	token, _ := tx.RevokeSession(subjectID{ID: testAgentSub}, fixedNow(), "jti-2")

	// Flip a character in the payload segment so the signature no longer verifies.
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("unexpected JWS shape: %d parts", len(parts))
	}
	payload := []byte(parts[1])
	payload[len(payload)/2] ^= 0x01
	parts[1] = string(payload)
	tampered := strings.Join(parts, ".")

	if _, err := s.verifySET(context.Background(), tampered); err == nil {
		t.Fatal("verifySET accepted a tampered SET — the kill-switch must never fire on an unverified signal")
	}
}

func TestRejectWrongAudience(t *testing.T) {
	signJWK, jwks := testKeys(t)
	s := newReceiver(t, jwks, testReceiver, testIssuer)
	// Transmitter targets a DIFFERENT receiver's audience.
	tx := Transmitter{Issuer: testIssuer, Audience: "https://someone-else.example/ssf", Key: signJWK}
	token, _ := tx.RevokeSession(subjectID{ID: testAgentSub}, fixedNow(), "jti-3")

	if _, err := s.verifySET(context.Background(), token); err == nil {
		t.Fatal("verifySET accepted a SET bound to a different audience (confused-deputy)")
	}
}

func signSET(t *testing.T, key jose.JSONWebKey, set setToken) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("secevent+jwt").WithHeader("kid", key.KeyID))
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	payload, _ := json.Marshal(set)
	obj, err := signer.Sign(payload)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	s, _ := obj.CompactSerialize()
	return s
}

func TestRejectExpiredSET(t *testing.T) {
	signJWK, jwks := testKeys(t)
	s := newReceiver(t, jwks, testReceiver, testIssuer)
	// A correctly-signed, correctly-audienced SET whose exp is an hour in the past:
	// a replayed revocation must NOT fire the kill-switch.
	set := setToken{
		Iss: testIssuer, Aud: audience{testReceiver},
		Iat: fixedUnixTime - 7200, Exp: fixedUnixTime - 3600,
		Events: map[string]json.RawMessage{
			evtSessionRevoked: json.RawMessage(`{"subject":{"format":"opaque","id":"` + testAgentSub + `"}}`),
		},
	}
	token := signSET(t, signJWK, set)
	if _, err := s.verifySET(context.Background(), token); err == nil {
		t.Fatal("verifySET accepted an expired SET — a replayed revocation must not fire the kill-switch")
	}
}

func TestRejectWrongIssuer(t *testing.T) {
	signJWK, jwks := testKeys(t)
	s := newReceiver(t, jwks, testReceiver, testIssuer)
	tx := Transmitter{Issuer: "https://evil.example", Audience: testReceiver, Key: signJWK}
	token, _ := tx.RevokeSession(subjectID{ID: testAgentSub}, fixedNow(), "jti-4")
	if _, err := s.verifySET(context.Background(), token); err == nil {
		t.Fatal("verifySET accepted a SET from an unexpected issuer")
	}
}

func TestRejectSymmetricAlg(t *testing.T) {
	_, jwks := testKeys(t)
	s := newReceiver(t, jwks, testReceiver, testIssuer)
	// Forge an HS256-signed token (algorithm-confusion attempt).
	hsKey := []byte("0123456789abcdef0123456789abcdef")
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: hsKey}, (&jose.SignerOptions{}).WithType("secevent+jwt"))
	if err != nil {
		t.Fatalf("hs signer: %v", err)
	}
	payload, _ := json.Marshal(setToken{Iss: testIssuer, Aud: audience{testReceiver}, Events: map[string]json.RawMessage{evtSessionRevoked: json.RawMessage(`{}`)}})
	obj, _ := signer.Sign(payload)
	token, _ := obj.CompactSerialize()

	if _, err := s.verifySET(context.Background(), token); err == nil {
		t.Fatal("verifySET accepted an HS256 SET — alg-confusion downgrade not rejected")
	}
}

// --- inbound receiver integration ---

type chanSink struct{ ch chan model.FindingReport }

func (c chanSink) Emit(_ context.Context, o model.Observation) error {
	if f, ok := o.(model.FindingReport); ok {
		select {
		case c.ch <- f:
		default:
		}
	}
	return nil
}

func TestReceiverEndToEnd(t *testing.T) {
	signJWK, jwks := testKeys(t)
	s := newReceiver(t, jwks, testReceiver, testIssuer)
	defer func() { _ = s.Close(context.Background()) }()
	addr := s.lis.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan model.FindingReport, 4)
	go func() { _ = s.Gather(ctx, chanSink{results}) }()

	tx := Transmitter{Issuer: testIssuer, Audience: testReceiver, Key: signJWK}
	token, _ := tx.RevokeSession(subjectID{Format: "opaque", ID: testAgentSub}, fixedNow(), "jti-e2e")

	// Valid SET => 202 + kill-switch finding.
	resp := post(t, addr+defaultPath, token)
	if resp != http.StatusAccepted {
		t.Fatalf("valid SET status = %d, want 202", resp)
	}
	select {
	case f := <-results:
		if f.Kind != "caep_session_revoked" || f.SubjectRef != testAgentSub {
			t.Errorf("finding = (%s, %s), want kill-switch for the agent", f.Kind, f.SubjectRef)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no kill-switch finding from a valid SET within 3s")
	}

	// Tampered SET => 400 + NO finding.
	parts := strings.Split(token, ".")
	pb := []byte(parts[1])
	pb[len(pb)/2] ^= 0x01
	parts[1] = string(pb)
	if got := post(t, addr+defaultPath, strings.Join(parts, ".")); got != http.StatusBadRequest {
		t.Fatalf("tampered SET status = %d, want 400", got)
	}
	select {
	case f := <-results:
		t.Fatalf("a tampered SET produced a finding (%s) — kill-switch fired on an unverified signal", f.Kind)
	case <-time.After(300 * time.Millisecond):
		// good: nothing emitted
	}
}

func post(t *testing.T, url, body string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, "http://"+url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/secevent+jwt")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func TestRefusesNonLoopbackBind(t *testing.T) {
	_, jwks := testKeys(t)
	s := New()
	err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"listen_addr": "0.0.0.0:0", "audience": testReceiver, "jwks": jwks,
	}})
	if err == nil {
		_ = s.Close(context.Background())
		t.Fatal("Open accepted a non-loopback bind without allow_public_bind (weakened)")
	}
}

func TestRefusesUnverifiableReceiver(t *testing.T) {
	// No key source: a receiver that cannot verify a SET must not be mounted.
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"listen_addr": "127.0.0.1:0", "audience": testReceiver,
	}}); err == nil {
		_ = s.Close(context.Background())
		t.Fatal("Open mounted a receiver with no verification key source")
	}
	// No audience: an unbound receiver is a confused-deputy hole.
	_, jwks := testKeys(t)
	s2 := New()
	if err := s2.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"listen_addr": "127.0.0.1:0", "jwks": jwks,
	}}); err == nil {
		_ = s2.Close(context.Background())
		t.Fatal("Open mounted a receiver with no audience binding")
	}
}
