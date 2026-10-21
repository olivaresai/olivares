// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/modules/sessions"
)

// An unrelated runtime ABAC deny must leave channel creation and both
// administrative reads decidable on the same engine, without a restart.
func TestCommunicationChannelsSurviveAbacDenyPolicyHTTP(t *testing.T) {
	e := bootChannelCatalogHTTPEstate(t)
	grants := []map[string]any{
		channelCatalogGrant(channelCatalogSubject("user", e.owner.id), true, true, true),
	}
	channel := e.create(t, "before-abac", grants)
	administrationPath := "/v1/m/sessions/channels/administration?workspace_id=" + e.workspace.String()
	sheetPath := "/v1/m/sessions/channels/" + channel.Channel.ID.String() +
		"/grants?workspace_id=" + e.workspace.String() + "&state=all"

	for _, stage := range []string{"before", "after"} {
		if stage == "after" {
			response := communicationHTTPTestRequest(t, e.eng, http.MethodPost,
				"/v1/m/governance/policies", e.owner.token, e.tenant, map[string]any{
					"name": "deny-secret-admin", "kind": "abac", "enabled": true,
					"spec": map[string]any{"rules": []any{map[string]any{
						"deny": true, "principal_kind": "token", "resource": "secret", "verb": "admin",
					}}},
				}, nil)
			if response.status != http.StatusCreated {
				t.Fatalf("create unrelated ABAC deny = %d: %s", response.status, response.raw)
			}
		}
		t.Run(stage+"/create", func(t *testing.T) {
			e.create(t, stage+"-channel", grants)
		})
		t.Run(stage+"/administration", func(t *testing.T) {
			response := communicationHTTPTestRequest(t, e.eng, http.MethodGet,
				administrationPath, e.owner.token, e.tenant, nil, nil)
			if response.status != http.StatusOK {
				t.Fatalf("channel administration = %d: %s", response.status, response.raw)
			}
			page := communicationHTTPTestDecode[sessions.ChannelAdministrationPage](t, response)
			for _, item := range page.Items {
				if item.Channel.ID == channel.Channel.ID {
					return
				}
			}
			t.Fatalf("administration omitted the existing channel %s", channel.Channel.ID)
		})
		t.Run(stage+"/grants", func(t *testing.T) {
			response := communicationHTTPTestRequest(t, e.eng, http.MethodGet,
				sheetPath, e.owner.token, e.tenant, nil, nil)
			if response.status != http.StatusOK {
				t.Fatalf("channel grant sheet = %d: %s", response.status, response.raw)
			}
			page := communicationHTTPTestDecode[sessions.ChannelGrantAdministrationPage](t, response)
			if page.Channel.ID != channel.Channel.ID || page.ETag == "" {
				t.Fatalf("grant sheet channel/etag = %s / %q", page.Channel.ID, page.ETag)
			}
			for _, item := range page.Items {
				if item.Grant.Subject.Ref == e.owner.id.String() && item.Grant.CanAdmin {
					return
				}
			}
			t.Fatal("grant sheet omitted the owner's administrative grant")
		})
		t.Run(stage+"/patch", func(t *testing.T) {
			response := communicationHTTPTestRequest(t, e.eng, http.MethodPatch,
				"/v1/m/sessions/channels", e.owner.token, e.tenant,
				map[string]any{"channel_id": channel.Channel.ID, "name": stage + " policy"},
				map[string]string{"If-Match": channel.ETag})
			if response.status != http.StatusOK {
				t.Fatalf("channel administration mutation = %d: %s", response.status, response.raw)
			}
			channel = communicationHTTPTestDecode[sessions.ChannelMutationResult](t, response)
		})
		t.Run(stage+"/nonadmin", func(t *testing.T) {
			response := communicationHTTPTestRequest(t, e.eng, http.MethodGet,
				administrationPath, e.reader.token, e.tenant, nil, nil)
			if response.status != http.StatusForbidden {
				t.Fatalf("nonadministrator = %d: %s, want 403", response.status, response.raw)
			}
		})
	}
}
