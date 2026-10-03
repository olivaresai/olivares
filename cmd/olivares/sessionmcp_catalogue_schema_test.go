// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// HU030: Claude rejects the entire catalogue if one optional object or hint is
// null. Assert the Tool/ToolAnnotations field types from the negotiated schema:
// https://modelcontextprotocol.io/specification/2025-11-25/schema#tool
func TestSessionMCPAggregatedToolListKeepsOptionalHintsSchemaValid(t *testing.T) {
	a, tenant, _, launcher, _, _ := workSessionEdgePrincipals(t)
	issuer := auth.NewSessionCredentials(a, func(context.Context, auth.SessionScope) error { return nil })
	bearer, err := issuer.Mint(t.Context(), launcher, auth.SessionScope{
		TenantID: tenant, WorkspaceID: model.NewID(), FolderRef: "fixture", SessionRef: "osn_" + model.NewID().String(),
		RunRef: model.NewID().String(), Fence: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	var upstream []mcpc.Tool
	if err := json.Unmarshal([]byte(`[
		{"name":"no_annotations","inputSchema":{"type":"object"}},
		{"name":"empty_annotations","inputSchema":{"type":"object"},"annotations":{}},
		{"name":"partial_annotations","inputSchema":{"type":"object"},"annotations":{"readOnlyHint":false}},
		{"name":"optional_objects","inputSchema":{"type":"object"},"outputSchema":{"type":"object"},"_meta":{"fixture":"present"}},
		{"name":"null_optional_objects","inputSchema":{"type":"object"},"outputSchema":null,"_meta":null,"icons":null}
	]`), &upstream); err != nil {
		t.Fatal(err)
	}
	h := &sessionMCPHandler{authr: issuer, issuedSessionOnly: true, managedTools: func(context.Context, auth.Principal, model.TenantID) ([]mcpc.Tool, error) {
		return upstream, nil
	}}
	r := httptest.NewRequest(http.MethodPost, "/session/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	r.Header.Set("Authorization", "Bearer "+bearer)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var response struct {
		Result struct {
			Tools []map[string]any `json:"tools"`
		} `json:"result"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &response) != nil || len(response.Result.Tools) != 8 {
		t.Fatalf("aggregate did not return native and upstream tools: %d", w.Code)
	}
	for _, tool := range response.Result.Tools {
		name, _ := tool["name"].(string)
		t.Run(name, func(t *testing.T) {
			input, ok := tool["inputSchema"].(map[string]any)
			if name == "" || !ok || input["type"] != "object" {
				t.Fatal("Tool requires a name and an object inputSchema")
			}
			for _, field := range []string{"annotations", "outputSchema", "_meta"} {
				if value, present := tool[field]; present {
					if _, ok := value.(map[string]any); !ok {
						t.Fatalf("%s must be an object or absent, got %v", field, value)
					}
				}
			}
			if icons, present := tool["icons"]; present {
				if _, ok := icons.([]any); !ok {
					t.Fatalf("icons must be an array or absent, got %v", icons)
				}
			}
			if annotations, ok := tool["annotations"].(map[string]any); ok {
				for _, field := range []string{"readOnlyHint", "destructiveHint", "idempotentHint", "openWorldHint"} {
					if value, present := annotations[field]; present {
						if _, ok := value.(bool); !ok {
							t.Fatalf("%s must be a boolean or absent, got %v", field, value)
						}
					}
				}
			}
			if name == "partial_annotations" {
				annotations, ok := tool["annotations"].(map[string]any)
				if !ok || annotations["readOnlyHint"] != false {
					t.Fatal("an explicit false hint was lost")
				}
			}
			if name == "optional_objects" {
				if tool["outputSchema"].(map[string]any)["type"] != "object" || tool["_meta"].(map[string]any)["fixture"] != "present" {
					t.Fatal("a present optional object was lost")
				}
			}
		})
	}
}

func TestSessionMCPToolListingPreservesPreviouslyTestedCatalogueFingerprint(t *testing.T) {
	// Literal encoding from the already-shipped probe generation. A wire fix
	// must not require Test/enable again for an unchanged registered server.
	const legacy = `{"name":"echo","title":"","description":"","annotations":{"title":"","readOnlyHint":false,"destructiveHint":null,"idempotentHint":null,"openWorldHint":null},"inputSchema":{"type":"object"}}`
	var tool mcpc.Tool
	if err := json.Unmarshal([]byte(legacy), &tool); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(legacy))
	probe := auth.MCPGatewayProbe{State: "ok", Tools: []auth.MCPGatewayTool{{Name: "echo", Fingerprint: hex.EncodeToString(digest[:])}}}
	for i := 0; i < 2; i++ {
		if !managedCatalogueMatches(probe, []mcpc.Tool{tool}) {
			t.Fatal("unchanged upstream no longer matches its persisted successful probe")
		}
		w := httptest.NewRecorder()
		sessionRPCListTools(w, json.RawMessage(`1`), []mcpc.Tool{tool})
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"readOnlyHint":false`) || strings.Contains(w.Body.String(), `:null`) {
			t.Fatal("client listing lost an explicit false or contains an optional null")
		}
	}
}
