// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	jwt "github.com/go-jose/go-jose/v4/jwt"

	"github.com/olivaresai/olivares/connectors/teams"
)

const (
	teamsAppID   = "app-11112222"
	teamsAADID   = "aad-object-id-of-alice"
	teamsSvcURL  = "https://smba.trafficmanager.net/amer/"
	teamsKID     = "bf-key-1"
	teamsMetaURL = teams.DefaultMetadataURL
	teamsJWKSURL = "https://login.botframework.com/v1/.well-known/keys"
)

// botJWKSDoer serves the Bot Framework OpenID metadata + JWKS from an in-memory key set.
type botJWKSDoer struct{ jwks string }

func (d *botJWKSDoer) Do(req *http.Request) (*http.Response, error) {
	switch req.URL.String() {
	case teamsMetaURL:
		return mkResp(200, `{"issuer":"`+teams.DefaultIssuer+`","jwks_uri":"`+teamsJWKSURL+`"}`), nil
	case teamsJWKSURL:
		return mkResp(200, d.jwks), nil
	default:
		return mkResp(404, `{}`), nil
	}
}

func mkResp(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

type teamsFullClaims struct {
	jwt.Claims
	ServiceURL string `json:"serviceurl,omitempty"`
}

// teamsHarness wires a receiver with a single native-JWT teams provider whose verifier uses
// an injected JWKS transport, plus a signer for the trust key.
type teamsHarness struct {
	r      *hitlReceiver
	signer jose.Signer
	now    time.Time
}

func newTeamsHarness(t *testing.T, dec approvalDecider) *teamsHarness {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwk := jose.JSONWebKey{Key: &priv.PublicKey, KeyID: teamsKID, Algorithm: "RS256", Use: "sig"}
	setBytes, _ := json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}})
	ver, err := teams.NewVerifier(teams.VerifierConfig{AppID: teamsAppID, Doer: &botJWKSDoer{jwks: string(setBytes)}})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: priv, KeyID: teamsKID}},
		(&jose.SignerOptions{}).WithType("JWT"),
	)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	r := &hitlReceiver{
		providers: map[string]*hitlProvider{
			"corp-teams": {
				name: "corp-teams", kind: hitlKindTeams, teams: ver,
				approvers: map[string]hitlApprover{
					teamsAADID: {ExternalID: teamsAADID, Tenant: "t-acme", Token: "tok-teams"},
				},
			},
		},
		decider: dec,
		clock:   func() time.Time { return now },
		log:     discardLog(),
	}
	return &teamsHarness{r: r, signer: signer, now: now}
}

func (h *teamsHarness) token(t *testing.T, svc string) string {
	t.Helper()
	tok, err := jwt.Signed(h.signer).Claims(teamsFullClaims{
		Claims: jwt.Claims{
			Issuer:   teams.DefaultIssuer,
			Audience: jwt.Audience{teamsAppID},
			Expiry:   jwt.NewNumericDate(h.now.Add(time.Hour)),
		},
		ServiceURL: svc,
	}).Serialize()
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return tok
}

func teamsActivity(aadID, svc, decision, approval string) []byte {
	body := map[string]any{
		"type": "invoke", "name": "adaptiveCard/action", "serviceUrl": svc,
		"from": map[string]any{"id": "29:channel-scoped", "aadObjectId": aadID},
		"value": map[string]any{"action": map[string]any{
			"type": "Action.Execute", "verb": "olivares.hitl.decision",
			"data": map[string]any{"decision": decision, "approval_id": approval},
		}},
	}
	b, _ := json.Marshal(body)
	return b
}

func teamsRequest(token string, body []byte) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/hitl/corp-teams", strings.NewReader(string(body)))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestTeamsValidJWTRecordsDecision(t *testing.T) {
	spy := &spyDecider{}
	h := newTeamsHarness(t, spy)
	body := teamsActivity(teamsAADID, teamsSvcURL, "approve", "appr-7")
	rec := httptest.NewRecorder()
	h.r.handler().ServeHTTP(rec, teamsRequest(h.token(t, teamsSvcURL), body))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (Teams Invoke response)", rec.Code)
	}
	if spy.called != 1 {
		t.Fatalf("decider called %d times, want 1", spy.called)
	}
	if spy.token != "tok-teams" || spy.tenant != "t-acme" {
		t.Fatalf("acted as %q/%q, want the aadObjectId-mapped approver", spy.token, spy.tenant)
	}
	if spy.approval != "appr-7" || spy.decision != "approve" {
		t.Fatalf("decision = %q on %q", spy.decision, spy.approval)
	}
	// Teams Invoke response shape: { statusCode, type, value }.
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["type"] != "application/vnd.microsoft.activity.message" {
		t.Errorf("response type = %v, want the Teams activity.message type", resp["type"])
	}
}

func TestTeamsNoBearerNeverTouchesEngine(t *testing.T) {
	spy := &spyDecider{}
	h := newTeamsHarness(t, spy)
	body := teamsActivity(teamsAADID, teamsSvcURL, "approve", "appr-7")
	rec := httptest.NewRecorder()
	h.r.handler().ServeHTTP(rec, teamsRequest("", body)) // no Authorization header

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if spy.called != 0 {
		t.Fatal("SECURITY: an unauthenticated Teams callback reached the engine")
	}
}

func TestTeamsServiceURLSpoofRejected(t *testing.T) {
	spy := &spyDecider{}
	h := newTeamsHarness(t, spy)
	// Token bound to the real serviceUrl, but the activity claims a different one.
	body := teamsActivity(teamsAADID, "https://attacker.example.com/", "approve", "appr-7")
	rec := httptest.NewRecorder()
	h.r.handler().ServeHTTP(rec, teamsRequest(h.token(t, teamsSvcURL), body))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (serviceUrl binding)", rec.Code)
	}
	if spy.called != 0 {
		t.Fatal("SECURITY: a serviceUrl-spoofed callback reached the engine")
	}
}

func TestTeamsUnmappedApproverRejected(t *testing.T) {
	spy := &spyDecider{}
	h := newTeamsHarness(t, spy)
	body := teamsActivity("aad-object-id-of-stranger", teamsSvcURL, "approve", "appr-7")
	rec := httptest.NewRecorder()
	h.r.handler().ServeHTTP(rec, teamsRequest(h.token(t, teamsSvcURL), body))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (unmapped aadObjectId)", rec.Code)
	}
	if spy.called != 0 {
		t.Fatal("an unmapped approver must not reach the engine")
	}
}

func TestParseTeamsFailsClosedWithoutAadObjectId(t *testing.T) {
	// A well-formed envelope but no aadObjectId must fail (never fall back to from.id).
	body := []byte(`{"type":"invoke","name":"adaptiveCard/action","from":{"id":"29:x"},"value":{"action":{"data":{"decision":"approve","approval_id":"a"}}}}`)
	if _, err := parseTeams(body); err == nil {
		t.Fatal("parseTeams must reject an activity with no aadObjectId")
	}
	// The wrong invoke envelope is rejected too.
	if _, err := parseTeams([]byte(`{"type":"message"}`)); err == nil {
		t.Fatal("parseTeams must reject a non-adaptiveCard/action invoke")
	}
}

func TestTeamsProviderSkippedWithoutAppID(t *testing.T) {
	cfg := hitlConfig{Providers: []hitlProviderSpec{{Name: "x", Kind: hitlKindTeams}}}
	if newHITLReceiver(cfg, &spyDecider{}, discardLog()) != nil {
		t.Fatal("a teams provider with no bot_app_id must be skipped (no usable provider => nil receiver)")
	}
}
