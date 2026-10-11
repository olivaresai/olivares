// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/inferenceproxy"
	"github.com/olivaresai/olivares/modules/models"
	"github.com/olivaresai/olivares/modules/sessions"
)

const deepseekFixtureKey = "deepseek-fixture-key-only-0001"

// Only this fixture redirects an exact service origin to its trusted loopback
// TLS server. Production validates the documented URL and never redirects.
type deepseekFixtureTransport struct {
	target string
	base   http.RoundTripper
}

func (d deepseekFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" || r.URL.Host != "api.deepseek.com" {
		return d.base.RoundTrip(r)
	}
	u, err := url.Parse(d.target)
	if err != nil {
		return nil, err
	}
	copy := r.Clone(r.Context())
	copy.URL.Scheme, copy.URL.Host = u.Scheme, u.Host
	return d.base.RoundTrip(copy)
}

type deepseekFixtureVault struct {
	store *auth.SecretStore
	opens int
}

func (v *deepseekFixtureVault) Seal(ctx context.Context, a auth.Principal, t model.TenantID, n string, b []byte) (string, error) {
	return (providerSecretVault{v.store}).Seal(ctx, a, t, n, b)
}
func (v *deepseekFixtureVault) Open(ctx context.Context, t model.TenantID, n string) ([]byte, error) {
	v.opens++
	return (providerSecretVault{v.store}).Open(ctx, t, n)
}
func (v *deepseekFixtureVault) Revoke(ctx context.Context, a auth.Principal, t model.TenantID, n string) error {
	return (providerSecretVault{v.store}).Revoke(ctx, a, t, n)
}

func TestDeepSeekGovernedTextRegistrationTurnDenialAndProbeFailure(t *testing.T) {
	backends := []store.Config{{Engine: store.EngineSQLite, DSN: ":memory:"}}
	if enginetest.PostgresAvailable(t) {
		d := enginetest.IsolatedPostgres(t)
		backends = append(backends, store.Config{Engine: store.EnginePostgres, DSN: d.App, OwnerDSN: d.Owner, AdminDSN: d.Admin})
	} else {
		t.Log("PostgreSQL pending its configured runtime")
	}
	for _, cfg := range backends {
		t.Run(string(cfg.Engine), func(t *testing.T) {
			c := newChatComposition(t, chatCompositionOptions{deepseek: true, backend: &cfg})
			beforeCalls, beforeOpens := c.transport.count(), c.providerVault.opens
			response := c.do("POST", "/v1/m/models/routing-policies/"+c.policyID+"/execute", c.admin, map[string]any{"input": "hello", "max_tokens": 64})
			if response.code != http.StatusOK {
				t.Fatalf("turn = %d %s", response.code, response.raw)
			}
			calls := c.transport.calls()[beforeCalls:]
			if len(calls) != 1 || calls[0].method != http.MethodPost || calls[0].url != modelprovider.DeepSeekChatURL || calls[0].authorization != "Bearer "+deepseekFixtureKey {
				t.Fatal("wrong service dispatch or credential")
			}
			var body map[string]json.RawMessage
			if err := json.Unmarshal(calls[0].body, &body); err != nil {
				t.Fatal(err)
			}
			if string(body["max_tokens"]) != "64" || body["max_completion_tokens"] != nil || body["store"] != nil {
				t.Fatal("wrong codec")
			}
			denied := c.do("POST", "/v1/m/models/routing-policies/"+c.policyID+"/execute", "", map[string]any{"input": "denied"})
			if denied.code != http.StatusUnauthorized || c.transport.count() != beforeCalls+1 || c.providerVault.opens != beforeOpens+1 {
				t.Fatal("unauthorized use reached provider")
			}
			for _, action := range []string{chatIntentAction, chatOutcomeAction} {
				records := c.records(action)
				if len(records) != 1 {
					t.Fatalf("missing retained %s", action)
				}
				for _, rec := range records {
					if strings.Contains(rec.raw, deepseekFixtureKey) || strings.Contains(rec.raw, "provider:") {
						t.Fatal("credential or private locator in audit")
					}
				}
			}
			c.upstream.mu.Lock()
			c.upstream.status = http.StatusUnauthorized
			c.upstream.mu.Unlock()
			probe := c.do("POST", providersPath+"/"+c.profile.ProviderRef+"/test", c.admin, map[string]any{})
			if probe.code != http.StatusOK || probe.body["probe_state"] != sessions.ProbeRefused {
				t.Fatalf("probe refusal = %d %s", probe.code, probe.raw)
			}
			calls = c.transport.calls()
			if calls[len(calls)-1].method != http.MethodGet || calls[len(calls)-1].url != modelprovider.DeepSeekModelsURL {
				t.Fatal("wrong probe path")
			}
		})
	}
}

func TestDeepSeekContentDenialReachesNoSealedCredentialOrProvider(t *testing.T) {
	pol := inferenceproxy.PolicyWithDLPRules(chatDefaultPolicy(), map[string]string{"secret.credential": "deny", "*": "allow", "unscanned": "allow"})
	c := newChatComposition(t, chatCompositionOptions{deepseek: true, policy: &pol})
	beforeCalls, beforeOpens := c.transport.count(), c.providerVault.opens
	r := c.execute(map[string]any{"input": "please use AKIAIOSFODNN7EXAMPLE for this"})
	if r.code != http.StatusForbidden || r.errorCode() != models.ChatErrContentDenied || c.providerVault.opens != beforeOpens || c.transport.count() != beforeCalls {
		t.Fatalf("content denial = %d %s, opens=%d, dispatches=%d", r.code, r.raw, c.providerVault.opens, c.transport.count())
	}
	if len(c.records(chatIntentAction))+len(c.records(chatOutcomeAction)) != 0 {
		t.Fatal("denial recorded an authorized effect")
	}
}

func TestDeepSeekProbeValidatesDiscoveryWithoutChangingLegacyURLs(t *testing.T) {
	for _, tc := range []struct {
		body    string
		status  int
		wantErr bool
	}{
		{`{"data":[{"id":"deepseek-flash"}]}`, 200, false},
		{`{"data":[]}`, 200, false},
		{`{"choices":[]}`, 200, true},
		{`invalid`, 200, true},
		{`{}`, 404, true},
		{`{}`, 302, true},
	} {
		p := providerProbe{client: &http.Client{Transport: diagRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method != "GET" || r.URL.String() != modelprovider.DeepSeekModelsURL {
				t.Fatal("probe operation drift")
			}
			return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
		})}}
		_, err := p.ProbeService(t.Context(), sessions.ProviderProbeRequest{Kind: "openai_compatible", BaseURL: modelprovider.DeepSeekBaseURL, APIKey: deepseekFixtureKey}, "deepseek")
		if (err != nil) != tc.wantErr {
			t.Fatalf("probe status %d: %v", tc.status, err)
		}
	}
	// An untagged compatible provider keeps its registered base path.
	legacy, err := modelsURL("openai_compatible", modelprovider.DeepSeekBaseURL)
	if err != nil || legacy != "https://api.deepseek.com/models" {
		t.Fatal("untagged compatible endpoint changed")
	}
}
