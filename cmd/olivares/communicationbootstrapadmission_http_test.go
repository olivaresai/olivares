// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
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
	path := "/v1/m/sessions/channels"
	catalogPath := path + "?workspace_id=" + estate.workspace.String()
	assertHiddenCatalog := func(t *testing.T) {
		t.Helper()
		response := communicationHTTPTestRequest(t, eng, http.MethodGet, catalogPath, estate.adminToken, estate.tenant, nil, nil)
		if response.status != http.StatusOK {
			t.Fatalf("unjoined superadmin catalog = %d: %s", response.status, response.raw)
		}
		catalog := communicationHTTPTestDecode[sessions.ChannelCatalogPage](t, response)
		if len(catalog.Items) != 0 || catalog.HasMore || catalog.Continuation != "" {
			t.Fatalf("tenant owner admission disclosed channels without membership: %+v", catalog)
		}
	}
	t.Run("global session", func(t *testing.T) {
		// Human superadmins enter an explicitly selected tenant as its owner.
		// That current, tenant-bound authority is not K3 directory membership.
		principal, err := eng.authr.Authenticate(t.Context(), estate.adminToken)
		if err != nil {
			t.Fatal(err)
		}
		ref, ok := principal.Ref()
		if !ok {
			t.Fatal("global session has no current principal reference")
		}
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		scoped, err := eng.authr.ResolvePrincipalScope(ctx, ref, estate.tenant)
		if err != nil {
			t.Fatal(err)
		}
		if role, member := scoped.RoleIn(estate.tenant); !member || role != auth.RoleOwner || scoped.SessionScope() != estate.tenant {
			t.Fatal("explicit superadmin entry did not bind owner authority to the selected tenant")
		}
		az := auth.NewAuthorizer(nil)
		if az.Allowed(ctx, scoped, "agent:write", estate.other) || az.Allowed(ctx, scoped, auth.PermSystemAdmin, model.SystemTenantID) {
			t.Fatal("selected-tenant owner authority escaped its tenant")
		}
		response := communicationHTTPTestRequest(t, eng, http.MethodPost, path, estate.adminToken, estate.tenant, body, nil)
		if response.status != http.StatusForbidden || !strings.Contains(string(response.raw), `"code":"forbidden"`) {
			t.Fatalf("unjoined superadmin create = %d: %s", response.status, response.raw)
		}
		assertHiddenCatalog(t)
	})
	t.Run("global API token", func(t *testing.T) {
		for _, method := range []string{http.MethodPost, http.MethodGet} {
			requestPath, command := path, any(body)
			if method == http.MethodGet {
				requestPath, command = catalogPath, nil
			}
			response := communicationHTTPTestRequest(t, eng, method, requestPath, credential.Token, estate.tenant, command, nil)
			if response.status != http.StatusForbidden || !strings.Contains(string(response.raw), `"code":"tenant_admission_required"`) || !strings.Contains(string(response.raw), "tenant-admitted") {
				t.Fatalf("global API token %s = %d: %s", method, response.status, response.raw)
			}
		}
	})
	assertCommunicationHTTPTestNoEffects(t, eng, estate.tenant, before, "global bootstrap scope refusal")
	// A real tenant member still creates the same command. The refusal never
	// creates membership, substitutes that member or mints a scoped credential.
	response := communicationHTTPTestRequest(t, eng, http.MethodPost, "/v1/m/sessions/channels", estate.owner.token, estate.tenant, body, nil)
	if response.status != http.StatusCreated {
		t.Fatalf("admitted owner create = %d: %s", response.status, response.raw)
	}
	created := communicationHTTPTestDecode[sessions.ChannelMutationResult](t, response)
	response = communicationHTTPTestRequest(t, eng, http.MethodGet, catalogPath, estate.owner.token, estate.tenant, nil, nil)
	if response.status != http.StatusOK {
		t.Fatalf("member catalog = %d: %s", response.status, response.raw)
	}
	catalog := communicationHTTPTestDecode[sessions.ChannelCatalogPage](t, response)
	if len(catalog.Items) != 1 || catalog.Items[0].ID != created.Channel.ID {
		t.Fatal("positive control channel was not visible to its real member")
	}
	before = communicationHTTPTestEffects(t, eng, estate.tenant)
	assertHiddenCatalog(t) // Prove concealment against a populated, healthy catalog.
	assertCommunicationHTTPTestNoEffects(t, eng, estate.tenant, before, "unjoined superadmin catalog read")
}
