// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package webpush

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

func b64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("base64url %q: %v", s, err)
	}
	return b
}

// The worked example of RFC 8291 section 5 and Appendix A: one fixed application
// server key, user-agent key, auth secret and salt must give exactly the published body.
func TestEncryptMatchesRFC8291Example(t *testing.T) {
	asPriv, err := ecdh.P256().NewPrivateKey(b64(t, "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"))
	if err != nil {
		t.Fatal(err)
	}
	sub := Subscription{
		Endpoint: "https://push.example.net/push/JzLQ3raZJfFBR0aqvOMsLrt54w4rJUsV",
		P256dh:   "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4",
		Auth:     "BTBZMqHH6r4Tts7J_aSIgg",
	}
	got, err := encrypt(sub, []byte("When I grow up, I want to be a watermelon"), asPriv, b64(t, "DGv6ra1nlYgDCS1FRnbzlw"))
	if err != nil {
		t.Fatal(err)
	}
	// The request body of RFC 8291 section 5, joined from its three printed lines.
	want := b64(t, "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27ml"+
		"mlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPT"+
		"pK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN")
	if !bytes.Equal(got, want) {
		t.Fatalf("body differs from the RFC 8291 example\n got %s\nwant %s",
			base64.RawURLEncoding.EncodeToString(got), base64.RawURLEncoding.EncodeToString(want))
	}
}

// decrypt is the user agent's half of RFC 8291, written apart from encrypt so the
// round trip below is not the encryptor agreeing with itself.
func decrypt(t *testing.T, uaPriv *ecdh.PrivateKey, auth, body []byte) []byte {
	t.Helper()
	if len(body) < 21 {
		t.Fatalf("body too short: %d", len(body))
	}
	salt, rs, idlen := body[:16], binary.BigEndian.Uint32(body[16:20]), int(body[20])
	asPub, record := body[21:21+idlen], body[21+idlen:]
	if rs != 4096 || len(record) > int(rs) {
		t.Fatalf("record size %d, record %d", rs, len(record))
	}
	peer, err := ecdh.P256().NewPublicKey(asPub)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := uaPriv.ECDH(peer)
	if err != nil {
		t.Fatal(err)
	}
	prkKey, _ := hkdf.Extract(sha256.New, secret, auth)
	keyInfo := append(append([]byte("WebPush: info\x00"), uaPriv.PublicKey().Bytes()...), asPub...)
	ikm, _ := hkdf.Expand(sha256.New, prkKey, string(keyInfo), 32)
	prk, _ := hkdf.Extract(sha256.New, ikm, salt)
	cek, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, record, nil)
	if err != nil {
		t.Fatalf("record does not open: %v", err)
	}
	if plain[len(plain)-1] != 2 {
		t.Fatalf("last record must end with the 0x02 delimiter, got %x", plain[len(plain)-1])
	}
	return plain[:len(plain)-1]
}

func newUA(t *testing.T) (*ecdh.PrivateKey, []byte, Subscription) {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	return priv, auth, Subscription{
		P256dh: base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()),
		Auth:   base64.RawURLEncoding.EncodeToString(auth),
	}
}

func TestEncryptRoundTripAndLimits(t *testing.T) {
	uaPriv, auth, sub := newUA(t)
	sub.Endpoint = "https://push.example.net/x"
	msg := []byte(`{"kind":"approval","url":"/sessions?s=run%3Aabc"}`)
	body, err := Encrypt(sub, msg)
	if err != nil {
		t.Fatal(err)
	}
	if got := decrypt(t, uaPriv, auth, body); !bytes.Equal(got, msg) {
		t.Fatalf("round trip = %q", got)
	}
	again, _ := Encrypt(sub, msg)
	if bytes.Equal(body, again) {
		t.Fatal("two encryptions of one message are identical: the salt and key are not fresh")
	}
	if _, err := Encrypt(sub, make([]byte, MaxPayload+1)); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("oversize payload err = %v", err)
	}
	for name, bad := range map[string]Subscription{
		"short key":    {Endpoint: sub.Endpoint, P256dh: "AAAA", Auth: sub.Auth},
		"off curve":    {Endpoint: sub.Endpoint, P256dh: base64.RawURLEncoding.EncodeToString(append([]byte{4}, make([]byte, 64)...)), Auth: sub.Auth},
		"short auth":   {Endpoint: sub.Endpoint, P256dh: sub.P256dh, Auth: "AAAA"},
		"not base64":   {Endpoint: sub.Endpoint, P256dh: "***", Auth: sub.Auth},
		"empty fields": {Endpoint: sub.Endpoint},
	} {
		if _, err := Encrypt(bad, msg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestKeyFromSeedIsStableAndFollowsTheSeed(t *testing.T) {
	a, err := KeyFromSeed([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := KeyFromSeed([]byte("0123456789abcdef0123456789abcdef"))
	c, _ := KeyFromSeed([]byte("0123456789abcdef0123456789abcdeX"))
	if !a.Equal(b) || a.Equal(c) {
		t.Fatal("the key must follow the seed and only the seed")
	}
	if a.Curve != elliptic.P256() {
		t.Fatal("VAPID keys are P-256")
	}
	if _, err := KeyFromSeed([]byte("short")); err == nil {
		t.Fatal("a short seed was accepted")
	}
	if got := PublicKey(a); len(b64(t, got)) != 65 {
		t.Fatalf("public key = %q", got)
	}
}

func TestVAPIDHeaderIsSignedForTheEndpointOrigin(t *testing.T) {
	key, _ := KeyFromSeed(bytes.Repeat([]byte{7}, 32))
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	h, err := vapidHeader(key, "https://olivares.ai", "https://push.example.net:8443/wpush/v2/abc", now)
	if err != nil {
		t.Fatal(err)
	}
	rest, ok := strings.CutPrefix(h, "vapid t=")
	if !ok {
		t.Fatalf("header = %q", h)
	}
	token, k, ok := strings.Cut(rest, ", k=")
	if !ok || k != PublicKey(key) {
		t.Fatalf("header does not carry the public key: %q", h)
	}
	parsed, err := jwt.ParseSigned(token, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		t.Fatal(err)
	}
	var claims jwt.Claims
	if err := parsed.Claims(&key.PublicKey, &claims); err != nil {
		t.Fatalf("signature does not verify: %v", err)
	}
	if claims.Audience[0] != "https://push.example.net:8443" || claims.Subject != "https://olivares.ai" {
		t.Fatalf("claims = %+v", claims)
	}
	if exp := claims.Expiry.Time(); exp.After(now.Add(24*time.Hour)) || !exp.After(now) {
		t.Fatalf("exp %v must be within 24h of %v", exp, now)
	}
}

// service is a push service of the test: TLS on loopback, and it records what it got.
type service struct {
	srv    *httptest.Server
	status int
	hits   atomic.Int32
	req    *http.Request
	body   []byte
}

func newService(t *testing.T, status int) *service {
	t.Helper()
	s := &service{status: status}
	s.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		s.req = r
		s.body, _ = io.ReadAll(r.Body)
		w.WriteHeader(s.status)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// loopbackSender trusts the test server's certificate and lifts the reserved-address
// refusal, the one thing a loopback server cannot satisfy.
func loopbackSender(t *testing.T, s *service) *Sender {
	t.Helper()
	key, _ := KeyFromSeed(bytes.Repeat([]byte{9}, 32))
	snd := NewSender(key)
	pool := x509.NewCertPool()
	pool.AddCert(s.srv.Certificate())
	snd.tls = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	snd.reserved = func(net.IP) bool { return false }
	snd.anyPort = true
	return snd
}

func TestSendDeliversAnEncryptedVAPIDRequest(t *testing.T) {
	svc := newService(t, http.StatusCreated)
	snd := loopbackSender(t, svc)
	uaPriv, auth, sub := newUA(t)
	sub.Endpoint = svc.srv.URL + "/push/abc"
	msg := []byte(`{"kind":"finished"}`)
	if err := snd.Send(context.Background(), sub, msg, Options{TTL: time.Hour, Urgency: UrgencyHigh}); err != nil {
		t.Fatal(err)
	}
	r := svc.req
	if r.Method != http.MethodPost || r.URL.Path != "/push/abc" {
		t.Fatalf("request = %s %s", r.Method, r.URL.Path)
	}
	for k, want := range map[string]string{"Content-Encoding": "aes128gcm", "Ttl": "3600", "Urgency": "high", "Content-Type": "application/octet-stream"} {
		if got := r.Header.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "vapid t=") {
		t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
	}
	if got := decrypt(t, uaPriv, auth, svc.body); !bytes.Equal(got, msg) {
		t.Fatalf("the service received %q, which does not decrypt to the message", got)
	}
	if bytes.Contains(svc.body, []byte("finished")) {
		t.Fatal("the message travelled in clear")
	}
}

func TestSendStatusMapping(t *testing.T) {
	for _, tc := range []struct {
		status int
		gone   bool
		fail   bool
	}{
		{http.StatusCreated, false, false},
		{http.StatusAccepted, false, false},
		{http.StatusNotFound, true, true},
		{http.StatusGone, true, true},
		{http.StatusTooManyRequests, false, true},
		{http.StatusInternalServerError, false, true},
		{http.StatusFound, false, true}, // a redirect is never followed
	} {
		svc := newService(t, tc.status)
		_, _, sub := newUA(t)
		sub.Endpoint = svc.srv.URL
		err := loopbackSender(t, svc).Send(context.Background(), sub, []byte("x"), Options{})
		if (err != nil) != tc.fail || errors.Is(err, ErrGone) != tc.gone {
			t.Errorf("status %d: err = %v (gone=%v fail=%v)", tc.status, err, tc.gone, tc.fail)
		}
		if svc.hits.Load() != 1 {
			t.Errorf("status %d: %d requests, want exactly 1", tc.status, svc.hits.Load())
		}
	}
}

// The endpoint is chosen by whoever registers a subscription, so the sender is the
// place that refuses an internal destination.
func TestSendRefusesInternalAndInsecureEndpoints(t *testing.T) {
	svc := newService(t, http.StatusCreated)
	key, _ := KeyFromSeed(bytes.Repeat([]byte{9}, 32))
	snd := NewSender(key) // production defaults
	_, _, sub := newUA(t)
	for _, endpoint := range []string{
		svc.srv.URL,                      // https on loopback
		"https://127.0.0.1:1/x",          // loopback literal
		"https://[::1]:1/x",              // v6 loopback
		"https://169.254.169.254/latest", // link-local metadata address
		"https://10.0.0.5/x",             // private
		"https://user:pw@push.example.net/x",
		"http://push.example.net/x",
		"ftp://push.example.net/x",
		"",
	} {
		sub.Endpoint = endpoint
		if err := snd.Send(context.Background(), sub, []byte("x"), Options{}); err == nil {
			t.Errorf("%q was accepted", endpoint)
		}
	}
	if svc.hits.Load() != 0 {
		t.Fatalf("%d requests reached the loopback service", svc.hits.Load())
	}
}

func TestSubscriptionValidateChecksEndpointAndBothKeys(t *testing.T) {
	_, _, sub := newUA(t)
	sub.Endpoint = "https://fcm.googleapis.com/fcm/send/abc"
	if err := sub.Validate(); err != nil {
		t.Fatalf("a good subscription: %v", err)
	}
	for name, bad := range map[string]Subscription{
		"http endpoint": {Endpoint: "http://fcm.googleapis.com/x", P256dh: sub.P256dh, Auth: sub.Auth},
		"loopback":      {Endpoint: "https://127.0.0.1/x", P256dh: sub.P256dh, Auth: sub.Auth},
		"no key":        {Endpoint: sub.Endpoint, Auth: sub.Auth},
		"no auth":       {Endpoint: sub.Endpoint, P256dh: sub.P256dh},
		"off curve key": {Endpoint: sub.Endpoint, P256dh: base64.RawURLEncoding.EncodeToString(append([]byte{4}, make([]byte, 64)...)), Auth: sub.Auth},
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestValidateEndpoint(t *testing.T) {
	for endpoint, ok := range map[string]bool{
		"https://fcm.googleapis.com/fcm/send/abc":         true,
		"https://updates.push.services.mozilla.com/wpush": true,
		"https://web.push.apple.com/Qabc":                 true,
		"http://fcm.googleapis.com/fcm/send/abc":          false,
		"https://":                                        false,
		"https://203.0.113.7/push":                        false, // a public literal: push services have names
		"https://x.example/" + strings.Repeat("a", 3000):  false,
		"not a url": false,
	} {
		if err := ValidateEndpoint(endpoint); (err == nil) != ok {
			t.Errorf("ValidateEndpoint(%.40q) = %v, want ok=%v", endpoint, err, ok)
		}
	}
}
