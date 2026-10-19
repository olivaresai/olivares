// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"net/http"
	"testing"
)

func (h *harness) catalogCreate(t *testing.T, path string, body map[string]any) string {
	t.Helper()
	var created struct {
		ID string `json:"id"`
	}
	if code := h.reqInto("POST", "/v1/m/catalog"+path, h.adminToken, h.tenantA, body, &created); code != http.StatusCreated || created.ID == "" {
		t.Fatalf("create %s = %d", path, code)
	}
	return created.ID
}
