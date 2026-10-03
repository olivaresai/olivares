// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/models"
)

// The real authenticated module route must never hand a non-Anthropic target
// to the concrete legacy Anthropic transport, including a later fallback.
func TestLegacyModelsExecuteKeepsTheSelectedProvider(t *testing.T) {
	for _, tc := range []struct {
		name, provider               string
		key, dormant, gateway, mixed bool
		status, sends                int
	}{
		{name: "fresh_default_modules", provider: "openai", dormant: true, status: 404},
		{name: "enabled_without_key", provider: "openai", status: 503},
		{name: "non_anthropic", provider: "openai", key: true, status: 422},
		{name: "anthropic", provider: "anthropic", key: true, status: 200, sends: 1},
		{name: "non_anthropic_gateway", provider: "openai", key: true, gateway: true, status: 422},
		{name: "non_anthropic_fallback", provider: "anthropic", key: true, mixed: true, status: 422, sends: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newChatComposition(t, chatCompositionOptions{})
			var mu sync.Mutex
			var calls int
			var matched bool
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model    string `json:"model"`
					Messages []struct {
						Content []struct {
							Text string `json:"text"`
						} `json:"content"`
					} `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				mu.Lock()
				calls++
				matched = r.URL.Path == "/v1/messages" && len(body.Messages) == 1 && len(body.Messages[0].Content) == 1 && body.Messages[0].Content[0].Text == "selected-provider-privacy-canary"
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if tc.mixed && body.Model == "legacy-selected-model" {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"fixture primary failure"}}`))
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "msg_fixture", "type": "message", "role": "assistant", "model": body.Model, "content": []any{map[string]any{"type": "text", "text": "fixture response"}}, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1}})
			}))
			t.Cleanup(upstream.Close)
			env := map[string]string{"ANTHROPIC_BASE_URL": upstream.URL}
			if tc.key {
				env["ANTHROPIC_API_KEY"] = "disposable-legacy-fixture-key"
			}
			m := models.New()
			if ex := newModelsExecutor(func(k string) string { return env[k] }, upstream.Client(), nil, discardLog()); ex != nil {
				m = models.New(models.WithExecutor(ex))
			}
			m.UseData(api.NewModuleData(c.store))
			makeServer := func(dormant []string) *api.Server {
				srv, err := api.New(api.Options{Store: c.store, Authenticator: auth.NewAuthenticator(c.store, nil), Authorizer: auth.NewAuthorizer(nil), Signer: c.signer, SetupToken: secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token")), Version: "legacy-privacy-fixture", Modules: []api.Module{m}, NotEnabledModules: dormant})
				if err != nil {
					t.Fatal(err)
				}
				return srv
			}
			c.srv = makeServer(nil)
			if err := c.store.Mutate(context.Background(), c.tenant, func(sc store.Scope) error {
				p, err := sc.Providers().Create(context.Background(), model.Provider{Name: tc.provider, Kind: tc.provider, Status: model.StatusActive})
				if err != nil {
					return err
				}
				_, err = sc.Models().Create(context.Background(), model.Model{Name: "legacy-selected-model", ProviderID: p.ID, Status: model.StatusActive})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			policy := map[string]any{"name": "legacy-provider-privacy", "enabled": true, "strategy": "pinned", "pinned_model": "legacy-selected-model"}
			if !tc.mixed {
				policy["preferred_providers"] = []string{tc.provider}
			}
			if tc.gateway {
				policy["gateway_endpoint"] = upstream.URL
			}
			created := c.do("POST", "/v1/m/models/routing-policies", c.admin, policy)
			if created.code != http.StatusCreated {
				t.Fatalf("policy = %d %s", created.code, created.raw)
			}
			c.policyID = created.body["id"].(string)
			if tc.dormant {
				profile, err := resolveModuleProfile(standardModuleSelection())
				if err != nil || profile.Active("models") {
					t.Fatal("fresh standard selection unexpectedly runs models")
				}
				c.srv = makeServer(notEnabledNamespaces([]api.Module{m}, profile))
			} else {
				preview := c.do("POST", "/v1/m/models/routing-policies/"+c.policyID+"/resolve", c.admin, map[string]any{})
				primary, _ := preview.body["primary"].(map[string]any)
				if preview.code != http.StatusOK || primary["provider_ref"] != tc.provider {
					t.Fatalf("preview did not select %s: %d %s", tc.provider, preview.code, preview.raw)
				}
			}
			result := c.execute(map[string]any{"input": "selected-provider-privacy-canary", "max_tokens": 32})
			mu.Lock()
			sends, promptMatched := calls, matched
			mu.Unlock()
			t.Logf("selected=%s status=%d configured_anthropic_requests=%d prompt_reached_messages_endpoint=%t", tc.provider, result.code, sends, promptMatched)
			if result.code != tc.status || sends != tc.sends {
				t.Fatalf("execute = %d with %d Anthropic sends; want %d/%d; response=%s", result.code, sends, tc.status, tc.sends, result.raw)
			}
			if tc.dormant && result.errorCode() != "module_not_enabled" {
				t.Fatalf("default module refusal = %s", result.raw)
			}
			if tc.status == 422 && (!strings.Contains(result.raw, "supports only Anthropic targets") || strings.Contains(result.raw, "selected-provider-privacy-canary") || strings.Contains(result.raw, env["ANTHROPIC_API_KEY"])) {
				t.Fatal("unsupported-provider refusal lost its safe one-sentence remedy")
			}
			if tc.sends == 1 && !promptMatched {
				t.Fatal("positive Anthropic control never reached its selected transport")
			}
		})
	}
}
