// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// External test package on purpose: it imports the module the way a consumer does, so
// it proves the generated capability DTO family is reachable through the PUBLIC import
// path and round-trips the schema 2 wire contract (docs/contracts/CAPABILITY-PROJECTION.md)
// with the generated types alone. No type is redeclared here.
package olivares_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	olivares "github.com/olivaresai/olivares/clients/go"
)

func TestCapabilityProjectionSchema2RoundTrip(t *testing.T) {
	const response = `{"schema_version":2,"results":[` +
		`{"id":"sheet","kind":"operation","state":"allowed","code":"authorized",` +
		`"observed_at":"2026-09-07T10:00:00.250Z","refresh_after_ms":30000},` +
		`{"id":"held","kind":"operation","state":"undisclosed","code":"not_disclosed",` +
		`"observed_at":"2026-09-07T10:00:00Z"},` +
		`{"id":"list","kind":"surface","state":"reachable","code":"admitted",` +
		`"observed_at":"2026-09-07T10:00:00.100Z","refresh_after_ms":12000}]}`

	var wire map[string]any
	var contentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/auth/capabilities" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		contentType = r.Header.Get("Content-Type")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		if err := json.Unmarshal(body, &wire); err != nil {
			t.Errorf("request body is not a JSON object: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(srv.Close)

	c, err := olivares.New(srv.URL, "olvk_test_secret")
	if err != nil {
		t.Fatal(err)
	}
	workspace := "00000000-0000-4000-8000-000000000010"
	questions := olivares.AuthCapabilityQuestions{
		SchemaVersion: 2,
		Questions: []olivares.AuthCapabilityQuestion{
			{ID: "sheet", Kind: "operation", Operation: "GET /v1/m/sessions/channels/{id}/grants",
				WorkspaceID: &workspace,
				Selectors:   &olivares.AuthCapabilitySelectors{Path: map[string]string{"id": "00000000-0000-4000-8000-000000000001"}}},
			{ID: "held", Kind: "operation", Operation: "PATCH /v1/m/sessions/channels",
				WorkspaceID: &workspace,
				Selectors:   &olivares.AuthCapabilitySelectors{Body: map[string]string{"channel_id": "00000000-0000-4000-8000-000000000002"}}},
			{ID: "list", Kind: "surface", Operation: "GET /v1/m/sessions/channels", WorkspaceID: &workspace},
		},
	}
	out, err := c.PostV1AuthCapabilities(context.Background(), olivares.PostV1AuthCapabilitiesInput{Body: questions})
	if err != nil {
		t.Fatal(err)
	}

	// The request the server saw is the schema 2 contract, field for field.
	if contentType != "application/json" {
		t.Errorf("Content-Type = %q", contentType)
	}
	if v, ok := wire["schema_version"].(float64); !ok || v != 2 {
		t.Errorf("wire schema_version = %v, want 2", wire["schema_version"])
	}
	sent, _ := wire["questions"].([]any)
	if len(sent) != 3 {
		t.Fatalf("wire questions = %v", wire["questions"])
	}
	first, _ := sent[0].(map[string]any)
	if first["workspace_id"] != workspace || first["kind"] != "operation" {
		t.Errorf("first question on the wire = %v", first)
	}
	if path, _ := first["selectors"].(map[string]any)["path"].(map[string]any); path["id"] != "00000000-0000-4000-8000-000000000001" {
		t.Errorf("first question selectors on the wire = %v", first["selectors"])
	}
	if _, present := first["selectors"].(map[string]any)["body"]; present {
		t.Errorf("an unset selector map must be absent, got %v", first["selectors"])
	}
	third, _ := sent[2].(map[string]any)
	if _, present := third["selectors"]; present {
		t.Errorf("a surface question without selectors must omit them, got %v", third)
	}

	// The typed result carries the schema 2 vocabulary; the budget is present only on
	// positives and absent — nil, not zero — on the non-verdict.
	if out.SchemaVersion != 2 {
		t.Errorf("result schema_version = %d, want 2", out.SchemaVersion)
	}
	if len(out.Results) != 3 {
		t.Fatalf("results = %+v", out.Results)
	}
	for index, want := range []olivares.AuthCapabilityResult{
		{ID: "sheet", Kind: "operation", State: "allowed", Code: "authorized", ObservedAt: "2026-09-07T10:00:00.250Z"},
		{ID: "held", Kind: "operation", State: "undisclosed", Code: "not_disclosed", ObservedAt: "2026-09-07T10:00:00Z"},
		{ID: "list", Kind: "surface", State: "reachable", Code: "admitted", ObservedAt: "2026-09-07T10:00:00.100Z"},
	} {
		got := out.Results[index]
		if got.ID != want.ID || got.Kind != want.Kind || got.State != want.State ||
			got.Code != want.Code || got.ObservedAt != want.ObservedAt {
			t.Errorf("results[%d] = %+v, want %+v", index, got, want)
		}
	}
	if out.Results[0].RefreshAfterMS == nil || *out.Results[0].RefreshAfterMS != 30000 {
		t.Errorf("positive budget = %v, want 30000", out.Results[0].RefreshAfterMS)
	}
	if out.Results[1].RefreshAfterMS != nil {
		t.Errorf("undisclosed carries a budget: %d", *out.Results[1].RefreshAfterMS)
	}
	if out.Results[2].RefreshAfterMS == nil || *out.Results[2].RefreshAfterMS != 12000 {
		t.Errorf("surface budget = %v, want 12000", out.Results[2].RefreshAfterMS)
	}

	// Re-serializing the decoded result reproduces the wire: the omitted budget stays
	// omitted, so a consumer that forwards the DTO cannot invent a budget for a non-verdict.
	again, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var canonical, roundTrip any
	if err := json.Unmarshal([]byte(response), &canonical); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(again, &roundTrip); err != nil {
		t.Fatal(err)
	}
	c1, _ := json.Marshal(canonical)
	c2, _ := json.Marshal(roundTrip)
	if string(c1) != string(c2) {
		t.Errorf("round trip differs:\n got %s\nwant %s", c2, c1)
	}
}
