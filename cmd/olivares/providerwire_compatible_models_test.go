// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/olivaresai/olivares/modules/sessions"
)

// The endpoint a session is launched on is the registered base_url as given (Grok Build takes its
// xAI base with the /v1, like the vendor default), so the model-list probe asks the same address.
func TestModelsURLCompatibleBaseMatchesLaunchEndpoint(t *testing.T) {
	for _, tc := range []struct {
		name, kind, base, want string
	}{
		{"compatible_v1", sessions.ProviderKindOpenAICompatible, "http://127.0.0.1:18797/v1", "http://127.0.0.1:18797/v1/models"},
		{"compatible_custom_path", sessions.ProviderKindOpenAICompatible, "https://gateway.example.test/proxy/v1", "https://gateway.example.test/proxy/v1/models"},
		{"compatible_trailing_slashes", sessions.ProviderKindOpenAICompatible, " https://gateway.example.test/v1/// ", "https://gateway.example.test/v1/models"},
		{"compatible_unversioned", sessions.ProviderKindOpenAICompatible, "https://gateway.example.test", "https://gateway.example.test/models"},
		{"anthropic_default", sessions.ProviderKindAnthropic, "", "https://api.anthropic.com/v1/models"},
		{"openai_default", sessions.ProviderKindOpenAI, "", "https://api.openai.com/v1/models"},
		{"xai_default", sessions.ProviderKindXAI, "", "https://api.x.ai/v1/models"},
		{"xai_custom_v1", sessions.ProviderKindXAI, "http://127.0.0.1:18797/v1", "http://127.0.0.1:18797/v1/models"},
		{"xai_custom_trailing_slash", sessions.ProviderKindXAI, " https://gateway.example.test/proxy/v1/ ", "https://gateway.example.test/proxy/v1/models"},
		{"vendor_custom_path", sessions.ProviderKindAnthropic, "https://gateway.example.test/proxy", "https://gateway.example.test/proxy/v1/models"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := modelsURL(tc.kind, tc.base)
			if err != nil || got != tc.want {
				t.Fatalf("model-list URL = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	for _, tc := range []struct{ kind, base string }{
		{sessions.ProviderKindOpenAICompatible, " "},
		{"unknown", "https://gateway.example.test/v1"},
	} {
		if got, err := modelsURL(tc.kind, tc.base); err == nil || got != "" {
			t.Fatalf("missing or unknown provider = %q, %v; want refusal", got, err)
		}
	}
}

// A local xAI-compatible server over plain http is probed for real: GET <base_url>/models with
// the key as a bearer, and the models it lists are the ones a session discovers.
func TestProviderProbeXAILocalHTTPServer(t *testing.T) {
	const key = "xai-synthetic-not-a-real-key"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer "+key {
			t.Errorf("probe asked %s %s with Authorization %q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"local-grok"}]}`)
	}))
	defer srv.Close()
	got, err := providerProbe{client: srv.Client()}.Probe(t.Context(),
		sessions.ProviderProbeRequest{Kind: sessions.ProviderKindXAI, BaseURL: srv.URL + "/v1", APIKey: key})
	if err != nil || len(got.Models) != 1 || got.Models[0] != "local-grok" {
		t.Fatalf("probe = %+v, %v; want the one local model", got, err)
	}
}
