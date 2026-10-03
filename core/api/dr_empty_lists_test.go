// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package api_test

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestDRBackupListsAreArraysWithoutBundles(t *testing.T) {
	h, dir := drHarness(t)
	admin := h.adminLogin()
	for _, state := range []string{"absent", "empty", "unrelated file"} {
		if state == "empty" {
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
		}
		if state == "unrelated file" {
			if err := os.WriteFile(filepath.Join(dir, "README.txt"), []byte("no bundle"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		response := h.do("GET", "/v1/console/dr/backups", admin, nil, nil)
		entries, ok := response.body["items"].([]any)
		if response.code != http.StatusOK || !ok || len(entries) != 0 {
			t.Fatalf("%s backup list=%d %s", state, response.code, response.raw)
		}
	}
}
