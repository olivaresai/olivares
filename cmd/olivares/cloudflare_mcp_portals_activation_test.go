// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

type cloudflareMCPInventorySink []model.Observation

func (s *cloudflareMCPInventorySink) Emit(_ context.Context, observation model.Observation) error {
	*s = append(*s, observation)
	return nil
}

func TestCloudflareMCPPortalsCatalogAndGather(t *testing.T) {
	ctx := context.Background()
	catalog, err := (&sourceReconciler{}).ListConnectors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range catalog {
		if entry.Kind != "cloudflare-mcp-portals" {
			continue
		}
		for _, field := range entry.Fields {
			if field.Key == "api_token" && field.Required && field.Secret {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("Cloudflare MCP Portals missing from catalog or missing required secret field")
	}
	source, ok := buildInProcSource("cloudflare-mcp-portals")
	if !ok {
		t.Fatal("catalog source cannot be constructed")
	}
	const token = "synthetic-portals-activation-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("source must use authenticated read-only requests")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/accounts/test-account/access/ai-controls/mcp/servers":
			fmt.Fprint(w, `{"success":true,"result":[{"id":"server-1","name":"unapproved","http_url":"https://mcp.example/tools?token=synthetic-query-secret"}]}`)
		case "/accounts/test-account/access/ai-controls/mcp/portals":
			fmt.Fprint(w, `{"success":true,"result":[{"id":"portal-1","name":"Engineering","hostname":"mcp.example"}]}`)
		default:
			t.Error("unexpected inventory endpoint")
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	if err := source.Open(ctx, sdk.Config{Settings: map[string]string{
		"api_token": token, "account_id": "test-account", "api_base": server.URL,
		"approved_servers": `["approved"]`,
	}}); err != nil {
		t.Fatal(err)
	}
	defer source.Close(ctx)
	var observations cloudflareMCPInventorySink
	if err := source.Gather(ctx, &observations); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, observation := range observations {
		switch value := observation.(type) {
		case model.EdgeObservation:
			if value.OriginRef != "test-account" {
				t.Error("inventory lost account identity")
			}
			kinds[value.ResourceKind]++
		case model.FindingReport:
			kinds[value.Kind]++
		}
	}
	if len(observations) != 3 || kinds["cf.mcp_server"] != 1 || kinds["cf.mcp_portal"] != 1 || kinds["shadow_mcp"] != 1 {
		t.Fatalf("expected server, portal and shadow finding; got kinds %v", kinds)
	}
	payload, err := json.Marshal(observations)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), token) || strings.Contains(string(payload), "synthetic-query-secret") {
		t.Error("inventory exposed synthetic credential material")
	}
}
