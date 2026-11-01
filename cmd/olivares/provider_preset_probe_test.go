// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/modules/sessions"
)

const presetFixtureKey = "fixture-preset-key-0001"

type presetProbeCall struct{ method, url, bearer, xAPIKey, version string }

// presetProbe answers every request with status and body and records what it was asked.
func presetProbe(status int, body string, calls *[]presetProbeCall) providerProbe {
	return providerProbe{client: &http.Client{Transport: diagRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		*calls = append(*calls, presetProbeCall{r.Method, r.URL.String(), r.Header.Get("Authorization"), r.Header.Get("x-api-key"), r.Header.Get("anthropic-version")})
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}}
}

// Each row's own model list is asked, with the header the row says its vendor takes.
func TestPresetProbeAsksTheRowsModelListWithTheRowsAuth(t *testing.T) {
	for _, row := range modelprovider.Presets() {
		t.Run(row.Ref, func(t *testing.T) {
			var calls []presetProbeCall
			p := presetProbe(http.StatusOK, `{"data":[{"id":"model-a"}]}`, &calls)
			got, err := p.ProbeService(t.Context(), sessions.ProviderProbeRequest{Kind: row.Kind, BaseURL: row.BaseURL, APIKey: presetFixtureKey}, row.Ref)
			if err != nil || len(got.Models) != 1 {
				t.Fatalf("probe: %+v, %v", got, err)
			}
			want := row.ModelsURL
			if want == "" {
				var err error
				if want, err = modelsURL(row.Kind, row.BaseURL); err != nil {
					t.Fatal(err)
				}
			}
			wantCalls := 1
			if row.KeyCheckURL != "" {
				wantCalls++
			}
			if len(calls) != wantCalls || calls[0].method != http.MethodGet || calls[0].url != want {
				t.Fatalf("calls %+v, want %d requests starting with GET %s", calls, wantCalls, want)
			}
			if wantVersion := map[bool]string{true: anthropicVersionHeader}[row.Kind == modelprovider.KindAnthropic]; calls[0].version != wantVersion {
				t.Fatalf("anthropic-version = %q, want %q", calls[0].version, wantVersion)
			}
			switch row.Auth {
			case modelprovider.AuthBearer:
				if calls[0].bearer != "Bearer "+presetFixtureKey || calls[0].xAPIKey != "" {
					t.Fatalf("a bearer row sent %+v", calls[0])
				}
			case modelprovider.AuthAnthropicKey:
				if calls[0].xAPIKey != presetFixtureKey || calls[0].bearer != "" {
					t.Fatalf("an x-api-key row sent %+v", calls[0])
				}
			}
		})
	}
}

// The URLs are literal, so a wrong base or models_url in a row cannot pass by being
// computed from itself.
func TestPresetProbeAsksTheVendorsPinnedListURL(t *testing.T) {
	for ref, want := range map[string]string{
		"deepseek-anthropic":   "https://api.deepseek.com/models",
		"zai-anthropic":        "https://api.z.ai/api/anthropic/v1/models",
		"kimi-code":            "https://api.kimi.ai/coding/v1/models",
		"moonshot-anthropic":   "https://api.moonshot.ai/v1/models",
		"openrouter-anthropic": "https://openrouter.ai/api/v1/models",
		"opencode-zen":         "https://opencode.ai/inference/v1/models",
		"opencode-go":          "https://opencode.ai/inference/v1/models",
		"deepseek":             "https://api.deepseek.com/models",
		"mistral":              "https://api.mistral.ai/v1/models",
	} {
		row, _ := modelprovider.LookupPreset(ref)
		var calls []presetProbeCall
		p := presetProbe(http.StatusOK, `{"data":[]}`, &calls)
		if _, err := p.ProbeService(t.Context(), sessions.ProviderProbeRequest{Kind: row.Kind, BaseURL: row.BaseURL, APIKey: presetFixtureKey}, ref); err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
		wantCalls := 1
		if row.KeyCheckURL != "" {
			wantCalls++
		}
		if len(calls) != wantCalls || calls[0].url != want {
			t.Errorf("%s asked %+v, want %s", ref, calls, want)
		}
	}
}

func TestPresetProbeRefusesWhatTheRowDoesNotSay(t *testing.T) {
	deepseek, _ := modelprovider.LookupPreset("deepseek-anthropic")
	for _, tc := range []struct {
		name, service string
		req           sessions.ProviderProbeRequest
	}{
		{"unknown ref", "not-a-vendor", sessions.ProviderProbeRequest{Kind: deepseek.Kind, BaseURL: deepseek.BaseURL}},
		{"another kind", deepseek.Ref, sessions.ProviderProbeRequest{Kind: sessions.ProviderKindOpenAICompatible, BaseURL: deepseek.BaseURL}},
		{"another endpoint", deepseek.Ref, sessions.ProviderProbeRequest{Kind: deepseek.Kind, BaseURL: "https://other.example.com"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []presetProbeCall
			tc.req.APIKey = presetFixtureKey
			if _, err := presetProbe(http.StatusOK, `{"data":[]}`, &calls).ProbeService(t.Context(), tc.req, tc.service); err == nil || len(calls) != 0 {
				t.Fatalf("err=%v calls=%d, want a refusal before any request", err, len(calls))
			}
		})
	}
}

// A redirect would carry the credential to a host the row does not name: Go forwards
// an x-api-key header across hosts, so the answer carries a Location to another host
// and the probe must stop at the first response.
func TestPresetProbeRefusesRedirects(t *testing.T) {
	row, _ := modelprovider.LookupPreset("kimi-code")
	var asked []string
	p := providerProbe{client: &http.Client{Transport: diagRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		asked = append(asked, r.URL.String())
		h := make(http.Header)
		h.Set("Location", "https://collector.example/keys")
		return &http.Response{StatusCode: http.StatusFound, Body: io.NopCloser(strings.NewReader("")), Header: h, Request: r}, nil
	})}}
	_, err := p.ProbeService(t.Context(), sessions.ProviderProbeRequest{Kind: row.Kind, BaseURL: row.BaseURL, APIKey: presetFixtureKey}, row.Ref)
	if err == nil || len(asked) != 1 {
		t.Fatalf("err=%v asked=%v, want the first answer refused and no second request", err, asked)
	}
}

// A vendor that answers 401 is the vendor's verdict on the key, as for any provider.
func TestPresetProbeReportsARefusedKey(t *testing.T) {
	row, _ := modelprovider.LookupPreset("openrouter-anthropic")
	var calls []presetProbeCall
	_, err := presetProbe(http.StatusUnauthorized, `{}`, &calls).ProbeService(t.Context(),
		sessions.ProviderProbeRequest{Kind: row.Kind, BaseURL: row.BaseURL, APIKey: presetFixtureKey}, row.Ref)
	if !errors.Is(err, sessions.ErrProviderRefused) || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v, want the provider's refusal with its 401", err)
	}
}

// A public model list is never a key verdict. Both rows ask the authenticated
// endpoint on the same pinned host; only its 200 lets the catalog be retained.
func TestPresetProbeOpenRouterVerifiesKeyBeforeSavingPublicModels(t *testing.T) {
	for _, ref := range []string{"openrouter", "openrouter-anthropic"} {
		for _, status := range []int{0, http.StatusOK, http.StatusUnauthorized, http.StatusForbidden, http.StatusFound, http.StatusInternalServerError} {
			t.Run(fmt.Sprintf("%s/%d", ref, status), func(t *testing.T) {
				row, _ := modelprovider.LookupPreset(ref)
				var calls []string
				p := providerProbe{client: &http.Client{Transport: diagRoundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls = append(calls, r.URL.String())
					if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+presetFixtureKey || r.Header.Get("x-api-key") != "" {
						t.Error("wrong key-check auth")
					}
					code, body := http.StatusOK, `{"data":[{"id":"model-a"}]}`
					if r.URL.String() == "https://openrouter.ai/api/v1/key" {
						if status == 0 {
							return nil, errors.New("fixture unavailable")
						}
						code, body = status, `{"data":{}}`
					}
					h := make(http.Header)
					h.Set("Location", "https://collector.example/key")
					return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: h, Request: r}, nil
				})}}
				got, err := p.ProbeService(t.Context(), sessions.ProviderProbeRequest{Kind: row.Kind, BaseURL: row.BaseURL, APIKey: presetFixtureKey}, ref)
				if len(calls) != 2 || calls[0] != "https://openrouter.ai/api/v1/models" || calls[1] != "https://openrouter.ai/api/v1/key" {
					t.Fatalf("calls=%v", calls)
				}
				switch status {
				case http.StatusOK:
					if err != nil || len(got.Models) != 1 || got.Models[0] != "model-a" {
						t.Fatalf("result=%+v err=%v", got, err)
					}
				case http.StatusUnauthorized, http.StatusForbidden:
					if !errors.Is(err, sessions.ErrProviderRefused) {
						t.Fatalf("refused key: %v", err)
					}
				default:
					if err == nil || errors.Is(err, sessions.ErrProviderRefused) {
						t.Fatalf("indeterminate key: %v", err)
					}
				}
			})
		}
	}
}
