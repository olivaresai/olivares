// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func writeLoopbackEgressPolicy(t *testing.T, endpoint string) string {
	t.Helper()
	u, err := url.Parse(endpoint)
	if err != nil {
		t.Fatalf("parse the collector URL: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("the collector URL carries no port: %v", err)
	}
	doc := map[string]any{
		"default": map[string]any{
			"allow": []map[string]any{{
				"host":  u.Hostname(),
				"ports": []map[string]int{{"low": port, "high": port}},
			}},
		},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal the policy: %v", err)
	}
	path := filepath.Join(t.TempDir(), "egress-policy.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatalf("write the policy: %v", err)
	}
	return path
}
