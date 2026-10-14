// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !enterprise

package api_test

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestCommunityOrganizationManagementUnavailable(t *testing.T) {
	for _, route := range []struct {
		method, suffix string
		body           any
	}{
		{http.MethodPost, "", map[string]any{"name": "Second organization", "slug": "second"}},
		{http.MethodGet, "", nil},
		{http.MethodDelete, "/{tenant}", nil},
		{http.MethodPut, "/{tenant}/region", map[string]any{"data_region": ""}},
		{http.MethodPut, "/{tenant}/status", map[string]any{"status": "suspended"}},
	} {
		t.Run(route.method+route.suffix, func(t *testing.T) {
			h := newHarness(t)
			admin := h.adminLogin()
			before := h.storedOrgs()
			path := "/v1/system/orgs" + strings.ReplaceAll(route.suffix, "{tenant}", before[0].TenantID.String())
			r := h.do(route.method, path, admin, route.body, nil)
			if r.code != http.StatusNotImplemented || errCode(r.body) != "organizations_unavailable" {
				t.Fatalf("%s %s = %d %s, want 501 organizations_unavailable", route.method, path, r.code, r.raw)
			}
			if after := h.storedOrgs(); !reflect.DeepEqual(after, before) {
				t.Fatalf("organization management changed stored organizations: before=%#v after=%#v", before, after)
			}
		})
	}
}
