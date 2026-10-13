//go:build !enterprise || !addon_ids

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudewif

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/connectors/modelprovider"
)

// Valid parameters and a real local token endpoint cannot make the Community
// direct API exchange an assertion or issue a credential.
func TestCommunityWIFExchangeUnavailable(t *testing.T) {
	// Build the same synthetic JWT from its public claims and fake signature,
	// so source scanning needs no exception for a credential-shaped literal.
	assertion := strings.Join([]string{
		base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`)),
		base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"probe-workload"}`)),
		base64.RawURLEncoding.EncodeToString([]byte("probe-signature")),
	}, ".")
	params := ExchangeParams{
		FederationRuleID: "fdrl_edition_probe", OrganizationID: "11111111-1111-4111-8111-111111111111",
		ServiceAccountID: "svac_edition_probe", WorkspaceID: "wrkspc_edition_probe",
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/oauth/token" {
			http.Error(w, "unexpected endpoint", http.StatusNotFound)
			return
		}
		var body struct {
			GrantType        string `json:"grant_type"`
			Assertion        string `json:"assertion"`
			FederationRuleID string `json:"federation_rule_id"`
			OrganizationID   string `json:"organization_id"`
			ServiceAccountID string `json:"service_account_id"`
			WorkspaceID      string `json:"workspace_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil ||
			body.GrantType != "urn:ietf:params:oauth:grant-type:jwt-bearer" || body.Assertion != assertion ||
			body.FederationRuleID != params.FederationRuleID || body.OrganizationID != params.OrganizationID ||
			body.ServiceAccountID != params.ServiceAccountID || body.WorkspaceID != params.WorkspaceID {
			http.Error(w, "invalid exchange request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"sk-ant-oat01-edition-probe","token_type":"Bearer","expires_in":60,"scope":"workspace:developer"}`))
	}))
	defer server.Close()
	for _, tc := range []struct {
		name string
		doer modelprovider.Doer
	}{{"default-client", nil}, {"injected-client", server.Client()}} {
		t.Run(tc.name, func(t *testing.T) {
			calls.Store(0)
			exchanger := NewExchanger(server.URL, tc.doer)
			if exchanger == nil {
				t.Fatal("Community must return a nonnil refusing exchanger")
			}
			token, err := exchanger.Exchange(context.Background(), assertion, params)
			if err == nil || !strings.Contains(err.Error(), "Business") {
				t.Error("exchange must return an explicit Business-unavailable error")
			}
			if !reflect.DeepEqual(token, MintedToken{}) {
				t.Error("unavailable exchange returned credential data")
			}
			if got := calls.Load(); got != 0 {
				t.Errorf("Community performed %d HTTP exchanges, want none", got)
			}
		})
	}
}
