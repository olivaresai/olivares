// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudewif

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// exchangeDoer captures the exchange request body and returns a scripted response.
type exchangeDoer struct {
	status   int
	body     string
	header   http.Header
	gotBody  []byte
	gotPath  string
	gotCType string
}

func (d *exchangeDoer) Do(req *http.Request) (*http.Response, error) {
	d.gotPath = req.URL.Path
	d.gotCType = req.Header.Get("content-type")
	if req.Body != nil {
		d.gotBody, _ = io.ReadAll(req.Body)
	}
	h := d.header
	if h == nil {
		h = make(http.Header)
	}
	return &http.Response{StatusCode: d.status, Body: io.NopCloser(strings.NewReader(d.body)), Header: h}, nil
}

const testAssertion = "eyJ.test.svid"

func testParams() ExchangeParams {
	return ExchangeParams{
		FederationRuleID: "fdrl_1", OrganizationID: "11111111-1111-1111-1111-111111111111",
		ServiceAccountID: "svac_1", WorkspaceID: "wrkspc_1",
	}
}

func TestExchangeSuccess(t *testing.T) {
	doer := &exchangeDoer{status: 200, body: `{"access_token":"sk-ant-oat01-abc123","token_type":"Bearer","expires_in":3600,"scope":"workspace:developer"}`}
	ex := NewExchanger("https://api.test", doer)
	ex.now = func() time.Time { return time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC) }

	tok, err := ex.Exchange(context.Background(), testAssertion, testParams())
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if tok.AccessToken != "sk-ant-oat01-abc123" || !hasOATPrefix(tok.AccessToken) {
		t.Errorf("access token = %q", tok.AccessToken)
	}
	if tok.TokenType != "Bearer" || tok.ExpiresIn != 3600 || tok.Scope != scopeWorkspaceDeveloper {
		t.Errorf("token = %+v", tok)
	}
	if got := tok.ExpiresAt(); !got.Equal(time.Date(2026, 6, 5, 13, 0, 0, 0, time.UTC)) {
		t.Errorf("ExpiresAt = %v", got)
	}

	// The request must use the RFC 7523 jwt-bearer grant and carry the assertion + ids.
	if doer.gotPath != oauthTokenPath {
		t.Errorf("path = %q", doer.gotPath)
	}
	if !strings.HasPrefix(doer.gotCType, "application/json") {
		t.Errorf("content-type = %q", doer.gotCType)
	}
	var req exchangeRequest
	if err := json.Unmarshal(doer.gotBody, &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if req.GrantType != grantJWTBearer || req.Assertion != testAssertion ||
		req.FederationRuleID != "fdrl_1" || req.ServiceAccountID != "svac_1" || req.WorkspaceID != "wrkspc_1" {
		t.Errorf("request = %+v", req)
	}

	// The audit record carries provenance but NO token.
	aud := tok.Audit()
	if aud.ServiceAccountID != "svac_1" || aud.Scope != scopeWorkspaceDeveloper {
		t.Errorf("audit = %+v", aud)
	}
	blob, _ := json.Marshal(aud)
	if strings.Contains(string(blob), tok.AccessToken) {
		t.Error("audit record must not contain the minted token")
	}
}

func TestExchangeOmitsEmptyWorkspace(t *testing.T) {
	doer := &exchangeDoer{status: 200, body: `{"access_token":"sk-ant-oat01-x","token_type":"Bearer","expires_in":60}`}
	ex := NewExchanger("", doer)
	p := testParams()
	p.WorkspaceID = ""
	if _, err := ex.Exchange(context.Background(), testAssertion, p); err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if strings.Contains(string(doer.gotBody), "workspace_id") {
		t.Errorf("empty workspace_id must be omitted, body = %s", doer.gotBody)
	}
}

func TestExchangeRejected(t *testing.T) {
	h := make(http.Header)
	h.Set("request-id", "req_abc")
	doer := &exchangeDoer{status: 400, body: `{"error":"invalid_grant"}`, header: h}
	ex := NewExchanger("https://api.test", doer)

	_, err := ex.Exchange(context.Background(), testAssertion, testParams())
	if err == nil {
		t.Fatal("expected error for 400")
	}
	msg := err.Error()
	if !strings.Contains(msg, "invalid_grant") || !strings.Contains(msg, "req_abc") {
		t.Errorf("error = %q (want invalid_grant + request id)", msg)
	}
	if strings.Contains(msg, testAssertion) {
		t.Error("error must never echo the assertion")
	}
}

func TestFederationExchangeParams(t *testing.T) {
	s := &Source{
		orgID: "11111111-1111-1111-1111-111111111111",
		federation: []FederationRule{
			{RuleID: "fdrl_a", ServiceAccountID: "svac_a", WorkspaceID: "wrkspc_a"},
			{RuleID: "fdrl_b", ServiceAccountID: "svac_b"}, // no workspace (single-workspace rule)
		},
	}
	got := s.FederationExchangeParams()
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	// Order mirrors the declared federation; each rule pairs with the Source's org id.
	if got[0].FederationRuleID != "fdrl_a" || got[0].ServiceAccountID != "svac_a" ||
		got[0].WorkspaceID != "wrkspc_a" || got[0].OrganizationID != s.orgID {
		t.Errorf("params[0] = %+v", got[0])
	}
	if got[1].FederationRuleID != "fdrl_b" || got[1].WorkspaceID != "" || got[1].OrganizationID != s.orgID {
		t.Errorf("params[1] = %+v", got[1])
	}
	// Each projected target must be a valid exchange target (id prefixes, org present).
	for i, p := range got {
		if err := p.validate(); err != nil {
			t.Errorf("params[%d].validate: %v", i, err)
		}
	}
	// No federation declared => no targets.
	if n := len((&Source{}).FederationExchangeParams()); n != 0 {
		t.Errorf("empty source: len = %d, want 0", n)
	}
}

func TestExchangeValidation(t *testing.T) {
	ex := NewExchanger("", &exchangeDoer{status: 200, body: `{}`})
	if _, err := ex.Exchange(context.Background(), "", testParams()); err == nil {
		t.Error("empty assertion must error")
	}
	bad := testParams()
	bad.FederationRuleID = "nope_1"
	if _, err := ex.Exchange(context.Background(), testAssertion, bad); err == nil {
		t.Error("bad rule prefix must error")
	}
	bad2 := testParams()
	bad2.OrganizationID = ""
	if _, err := ex.Exchange(context.Background(), testAssertion, bad2); err == nil {
		t.Error("missing organization_id must error")
	}
}
