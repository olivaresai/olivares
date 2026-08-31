// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

// the stable core doc must annotate every secured operation with
// x-required-permission (or be in the small documented exempt set in
// openapi_permissions.go) — the console playground's tenant-admin filter is
// deny-closed on this metadata, so an unannotated route silently disappears
// for every tenant admin. This test makes that a build-time decision instead
// of a silent regression.
func TestCoreOpenAPIPermissionAnnotations(t *testing.T) {
	// The exempt set mirrored from openapi_permissions.go — duplicated here on
	// purpose: growing it must be a conscious two-file change.
	exempt := map[string]bool{
		"logout":        true,
		"refreshToken":  true,
		"whoami":        true,
		"searchConsole": true,
	}

	h := newHarness(t)
	r := h.do("GET", "/openapi.json", "", nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("openapi.json = %d", r.code)
	}
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal([]byte(r.raw), &doc); err != nil {
		t.Fatal(err)
	}

	type operation struct {
		OperationID string                `json:"operationId"`
		Security    []map[string][]string `json:"security"`
		Permission  string                `json:"x-required-permission"`
	}
	seen := 0
	for path, item := range doc.Paths {
		for method, raw := range item {
			if method == "parameters" {
				continue
			}
			var o operation
			if err := json.Unmarshal(raw, &o); err != nil || o.OperationID == "" {
				continue
			}
			seen++
			secured := false
			for _, s := range o.Security {
				if _, ok := s["bearerAuth"]; ok {
					secured = true
				}
			}
			switch {
			case !secured:
				if o.Permission != "" {
					t.Errorf("%s %s (%s): unsecured operation carries a permission %q", method, path, o.OperationID, o.Permission)
				}
			case exempt[o.OperationID]:
				if o.Permission != "" {
					t.Errorf("%s %s (%s): exempt operation must stay unannotated, got %q", method, path, o.OperationID, o.Permission)
				}
			default:
				if o.Permission == "" {
					t.Errorf("%s %s (%s): secured core operation without x-required-permission — annotate it in openapi_permissions.go or (rarely) add it to the documented exempt set", method, path, o.OperationID)
				}
			}
		}
	}
	if seen < 50 {
		t.Fatalf("walked only %d operations — the doc shape changed under this test", seen)
	}

	// Pinned samples: the annotation must be the handler's REAL permission.
	pin := map[string]string{
		"listAgents":            "agent:read",
		"listSystemAuditEvents": "system:admin",
		"createWorkspace":       "tenant:admin",
		"listMembers":           "user:read",
		"getConnectorHealth":    "health:status:read",
	}
	found := map[string]string{}
	for _, item := range doc.Paths {
		for method, raw := range item {
			if method == "parameters" {
				continue
			}
			var o operation
			if json.Unmarshal(raw, &o) == nil && o.OperationID != "" {
				found[o.OperationID] = o.Permission
			}
		}
	}
	for id, want := range pin {
		if found[id] != want {
			t.Errorf("%s: x-required-permission = %q, want %q", id, found[id], want)
		}
	}
}
