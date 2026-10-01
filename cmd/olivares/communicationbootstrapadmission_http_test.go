// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestCommunicationGlobalBootstrapAdmissionHTTP(t *testing.T) {
	estate := bootChannelAdministrationHTTPEstate(t, communicationHTTPTestSQLiteStore(t))
	eng := estate.eng
	credential, err := auth.NewCredential(auth.PrefixToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.store.AuthMutate(context.Background(), func(sc store.AuthScope) error {
		_, err := sc.Tokens().Create(context.Background(), model.APIToken{Name: "global bootstrap denial", Selector: credential.Selector, SecretHash: credential.SecretHash, IsSuperadmin: true})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"workspace_id": estate.workspace.String(), "slug": "bootstrap-admission", "name": "Bootstrap admission",
		"initial_grants": []map[string]any{channelAdministrationGrant(channelAdministrationSubject("user", estate.owner.id), true, true, true)}}
	before := communicationHTTPTestEffects(t, eng, estate.tenant)
	for name, token := range map[string]string{"global session": estate.adminToken, "global API token": credential.Token} {
		t.Run(name, func(t *testing.T) {
			for _, method := range []string{http.MethodPost, http.MethodGet} {
				path := "/v1/m/sessions/channels"
				var command any = body
				if method == http.MethodGet {
					path += "?workspace_id=" + estate.workspace.String()
					command = nil
				}
				response := communicationHTTPTestRequest(t, eng, method, path, token, estate.tenant, command, nil)
				if response.status != http.StatusForbidden || !strings.Contains(string(response.raw), `"code":"tenant_admission_required"`) || !strings.Contains(string(response.raw), "tenant-admitted") {
					t.Fatalf("%s %s = %d: %s", name, method, response.status, response.raw)
				}
			}
		})
	}
	assertCommunicationHTTPTestNoEffects(t, eng, estate.tenant, before, "global bootstrap scope refusal")
	// A real tenant member still creates the same command. The refusal never
	// creates membership, substitutes that member or mints a scoped credential.
	response := communicationHTTPTestRequest(t, eng, http.MethodPost, "/v1/m/sessions/channels", estate.owner.token, estate.tenant, body, nil)
	if response.status != http.StatusCreated {
		t.Fatalf("admitted owner create = %d: %s", response.status, response.raw)
	}
}
