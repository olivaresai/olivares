// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/store"
)

type emailFallbackFed struct {
	fakeFed
	identity auth.FederatedIdentity
}

func (f *emailFallbackFed) ValidateAssertion(context.Context, auth.Assertion) (auth.FederatedIdentity, error) {
	return f.identity, nil
}

func completeFallbackCallback(t *testing.T, h *harness, protocol string) *httptest.ResponseRecorder {
	t.Helper()
	start := h.raw("GET", "/v1/auth/federation/start", nil)
	if start.Code != http.StatusFound {
		t.Fatal("start refused", start.Code)
	}
	cookie := findCookie(start.Result().Cookies(), "olv_sso")
	if cookie == nil {
		t.Fatal("missing flow cookie")
	}
	location, err := url.Parse(start.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	query := url.Values{"code": {"fixture-code"}, "state": {location.Query().Get("state")}}
	if protocol == auth.ProtocolSAML {
		query = url.Values{"SAMLResponse": {"fixture-signed-assertion"}}
	}
	return h.raw("GET", "/v1/auth/federation/callback?"+query.Encode(), []*http.Cookie{cookie})
}

func TestSSOEmailFallbackCallbackRefusesBoundAccountAndUnverifiedOIDC(t *testing.T) {
	for _, tc := range []struct {
		name, protocol string
		bound          bool
	}{
		{"bound OIDC", auth.ProtocolOIDC, true},
		{"bound SAML", auth.ProtocolSAML, true},
		{"OIDC verification omitted", auth.ProtocolOIDC, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fed := &emailFallbackFed{fakeFed: fakeFed{proto: tc.protocol}, identity: auth.FederatedIdentity{Issuer: "https://issuer-b.example", Subject: "subject-b", Email: "victim@corp.example"}}
			fed.identity.EmailVerified = tc.bound && tc.protocol == auth.ProtocolOIDC
			// Completion must pin the selected provider protocol even if its
			// optional result metadata does not match that protocol.
			fed.identity.Protocol = auth.ProtocolSAML
			h := newFedHarness(t, fed)
			token := h.adminLogin()
			p, err := h.authr.Authenticate(context.Background(), token)
			if err != nil {
				t.Fatal(err)
			}
			u, err := h.authr.CreateUser(context.Background(), p, auth.NewUser{Email: fed.identity.Email, Password: "fixture-local-password"})
			if err != nil {
				t.Fatal(err)
			}
			if tc.bound {
				if err := h.st.AuthMutate(context.Background(), func(as store.AuthScope) error {
					row, err := as.Users().Get(context.Background(), u.ID)
					if err != nil {
						return err
					}
					row.SsoSubject = (auth.FederatedIdentity{Issuer: "https://issuer-a.example", Subject: "subject-a"}).QualifiedSubject()
					_, err = as.Users().Update(context.Background(), row)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			callback := completeFallbackCallback(t, h, tc.protocol)
			if callback.Code != http.StatusUnauthorized || strings.Contains(callback.Body.String(), `"token"`) {
				t.Fatalf("callback admitted email adoption: status=%d", callback.Code)
			}
		})
	}
}

func TestSSOEmailFallbackCallbackVerifiedAndSAMLBootstrap(t *testing.T) {
	for _, protocol := range []string{auth.ProtocolOIDC, auth.ProtocolSAML} {
		t.Run(protocol, func(t *testing.T) {
			fed := &emailFallbackFed{fakeFed: fakeFed{proto: protocol}, identity: auth.FederatedIdentity{Issuer: "https://issuer.example", Subject: "subject", Email: "bootstrap@corp.example", EmailVerified: protocol == auth.ProtocolOIDC}}
			h := newFedHarness(t, fed)
			p, err := h.authr.Authenticate(context.Background(), h.adminLogin())
			if err != nil {
				t.Fatal(err)
			}
			u, err := h.authr.CreateUser(context.Background(), p, auth.NewUser{Email: fed.identity.Email, Password: "fixture-local-password"})
			if err != nil {
				t.Fatal(err)
			}
			callback := completeFallbackCallback(t, h, protocol)
			if callback.Code != http.StatusOK || !strings.Contains(callback.Body.String(), `"token"`) {
				t.Fatal("permitted bootstrap callback refused", callback.Code)
			}
			if err := h.st.AuthView(context.Background(), func(as store.AuthScope) error {
				row, err := as.Users().Get(context.Background(), u.ID)
				if err == nil && row.SsoSubject != fed.identity.QualifiedSubject() {
					t.Fatal("bootstrap callback did not bind the exact subject")
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
