// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package chronicle

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// verifyAssertion checks that the JWT-bearer assertion is a well-formed, correctly
// signed RS256 token with the expected service-account claims.
func verifyAssertion(t *testing.T, assertion string, pub *rsa.PublicKey, wantAud string) {
	t.Helper()
	parts := strings.SplitN(assertion, ".", 3)
	if len(parts) != 3 {
		t.Fatalf("assertion is not a 3-part JWT: %q", assertion)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("signature not base64url: %v", err)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatalf("assertion RS256 signature invalid: %v", err)
	}
	hdr := decodeJWTSegment(t, parts[0])
	claims := decodeJWTSegment(t, parts[1])
	if hdr["alg"] != "RS256" || hdr["typ"] != "JWT" || hdr["kid"] != "kid-1" {
		t.Errorf("assertion header = %v, want RS256/JWT/kid-1", hdr)
	}
	if claims["iss"] != "svc@proj.iam.gserviceaccount.com" {
		t.Errorf("iss = %v", claims["iss"])
	}
	if claims["scope"] != "https://www.googleapis.com/auth/cloud-platform" {
		t.Errorf("scope = %v", claims["scope"])
	}
	if claims["aud"] != wantAud {
		t.Errorf("aud = %v, want %v", claims["aud"], wantAud)
	}
	iat, _ := claims["iat"].(float64)
	exp, _ := claims["exp"].(float64)
	if !(exp > iat) {
		t.Errorf("exp %v not after iat %v", exp, iat)
	}
}

func decodeJWTSegment(t *testing.T, seg string) map[string]any {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		t.Fatalf("JWT segment not base64url: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("JWT segment not JSON: %v", err)
	}
	return m
}

func sampleNotification() sdk.Notification {
	return sdk.Notification{
		Type:     "finding.reported",
		Title:    "agent denied write",
		Body:     "claude-1 blocked",
		Severity: model.SeverityHigh,
		Tenant:   "acme",
		Fields:   map[string]string{"agent": "claude-1", "decision": "deny", "resource": "vault.path/db"},
		Time:     time.Date(2026, 6, 6, 10, 0, 0, 0, time.UTC),
	}
}

func extractUDM(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var req importRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("body is not an events:import request: %v", err)
	}
	if len(req.InlineSource.Events) != 1 {
		t.Fatalf("want 1 event, got %d", len(req.InlineSource.Events))
	}
	var udm map[string]any
	if err := json.Unmarshal(req.InlineSource.Events[0].UDM, &udm); err != nil {
		t.Fatalf("udm is not a JSON object: %v", err)
	}
	return udm
}

func TestNotifyStaticBearer(t *testing.T) {
	var path, auth string
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	o := New()
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"project": "proj", "instance": "inst-1", "region": "us",
		"endpoint": srv.URL, "token": "static-tok",
	}}); err != nil {
		t.Fatal(err)
	}
	defer o.Close(context.Background())
	if err := o.Notify(context.Background(), sampleNotification()); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	if path != "/v1alpha/projects/proj/locations/us/instances/inst-1/events:import" {
		t.Errorf("path = %q", path)
	}
	if auth != "Bearer static-tok" {
		t.Errorf("auth = %q, want 'Bearer static-tok'", auth)
	}
	udm := extractUDM(t, body)
	meta, _ := udm["metadata"].(map[string]any)
	if meta["eventType"] != "GENERIC_EVENT" {
		t.Errorf("eventType = %v, want GENERIC_EVENT", meta["eventType"])
	}
	if meta["eventTimestamp"] != "2026-06-06T10:00:00Z" {
		t.Errorf("eventTimestamp = %v", meta["eventTimestamp"])
	}
}

func TestDefaultRegionHostAndPath(t *testing.T) {
	o := New()
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"project": "p", "instance": "i", "region": "europe", "token": "t",
	}}); err != nil {
		t.Fatal(err)
	}
	want := "https://europe-chronicle.googleapis.com/v1alpha/projects/p/locations/europe/instances/i/events:import"
	if o.url != want {
		t.Errorf("url = %q, want %q", o.url, want)
	}
}

func TestOpenRejectsBadConfig(t *testing.T) {
	for i, cfg := range []map[string]string{
		{"instance": "i", "token": "t"},   // missing project
		{"project": "p", "token": "t"},    // missing instance
		{"project": "p", "instance": "i"}, // missing credential
	} {
		o := New()
		if err := o.Open(context.Background(), sdk.Config{Settings: cfg}); err == nil {
			t.Errorf("case %d: Open(%v) = nil, want error", i, cfg)
		}
	}
}

// TestNotifyServiceAccountMintsAndCaches exercises the full JWT-bearer flow: the
// connector signs an assertion with the service-account key, exchanges it for an
// access token at the (fake) token endpoint, uses that token on events:import, and
// caches it across notifications.
func TestNotifyServiceAccountMintsAndCaches(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})

	var tokenHits, importHits int32
	var importAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			atomic.AddInt32(&tokenHits, 1)
			_ = r.ParseForm()
			if r.FormValue("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
				t.Errorf("grant_type = %q", r.FormValue("grant_type"))
			}
			// Verify the RS256 assertion cryptographically + its claims, so a regression
			// in assertion() (wrong aud/scope/iss/sig) is caught, not just "non-empty".
			verifyAssertion(t, r.FormValue("assertion"), &key.PublicKey, "http://"+r.Host+"/token")
			_, _ = io.WriteString(w, `{"access_token":"minted-123","expires_in":3600,"token_type":"Bearer"}`)
		case strings.HasSuffix(r.URL.Path, "events:import"):
			atomic.AddInt32(&importHits, 1)
			importAuth = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	sa := map[string]string{
		"type":           "service_account",
		"client_email":   "svc@proj.iam.gserviceaccount.com",
		"private_key":    string(keyPEM),
		"token_uri":      srv.URL + "/token",
		"private_key_id": "kid-1",
	}
	saJSON, _ := json.Marshal(sa)
	dir := t.TempDir()
	credFile := filepath.Join(dir, "sa.json")
	if err := os.WriteFile(credFile, saJSON, 0o600); err != nil {
		t.Fatal(err)
	}

	o := New()
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"project": "p", "instance": "i", "endpoint": srv.URL, "credentials_file": credFile,
	}}); err != nil {
		t.Fatal(err)
	}
	defer o.Close(context.Background())

	for i := 0; i < 2; i++ {
		if err := o.Notify(context.Background(), sampleNotification()); err != nil {
			t.Fatalf("Notify #%d: %v", i, err)
		}
	}

	if importAuth != "Bearer minted-123" {
		t.Errorf("events:import auth = %q, want 'Bearer minted-123'", importAuth)
	}
	if got := atomic.LoadInt32(&importHits); got != 2 {
		t.Errorf("events:import hits = %d, want 2", got)
	}
	if got := atomic.LoadInt32(&tokenHits); got != 1 {
		t.Errorf("token minted %d times, want 1 (cached across notifications)", got)
	}
}
