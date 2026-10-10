// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/modules/sessions"
)

func TestGeminiProviderProbeNativeGooglePagination(t *testing.T) {
	const key = "gemini-synthetic-fixture"
	calls := 0
	p := providerProbe{client: &http.Client{Transport: diagRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "GET" || r.URL.Scheme != "https" || r.URL.Host != "generativelanguage.googleapis.com" || r.URL.Path != "/v1beta/models" || r.Header.Get("x-goog-api-key") != key || r.Header.Get("Authorization") != "" || strings.Contains(r.URL.String(), key) {
			t.Errorf("unsafe or incorrect native model-list request")
		}
		body := `{"models":[{"name":"models/gemini-fixture","supportedGenerationMethods":["generateContent"]},{"name":"models/embedding","supportedGenerationMethods":["embedContent"]}],"nextPageToken":"next/fixture"}`
		if calls == 2 {
			if r.URL.Query().Get("pageToken") != "next/fixture" || r.URL.Query().Get("pageSize") != "100" || r.URL.Query().Has("limit") {
				t.Errorf("incorrect Google pagination: %s", r.URL.RawQuery)
			}
			body = `{"models":[{"name":"models/gemini-second","supportedGenerationMethods":["generateContent"]}]}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}, nil
	})}}
	got, err := p.Probe(t.Context(), sessions.ProviderProbeRequest{Kind: sessions.ProviderKindGemini, APIKey: key})
	if err != nil || calls != 2 || strings.Join(got.Models, ",") != "gemini-fixture,gemini-second" {
		t.Fatalf("native probe = %+v %v calls=%d", got, err, calls)
	}
	if _, err := modelsURL(sessions.ProviderKindGemini, "https://another.example"); err == nil {
		t.Fatal("Gemini accepted another endpoint")
	}
}
