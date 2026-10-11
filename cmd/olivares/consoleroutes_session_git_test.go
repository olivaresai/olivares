// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"os"
	"strings"
	"testing"
)

func TestConsoleSessionGitActionRoutes(t *testing.T) {
	source, err := os.ReadFile("../../web/src/features/agentops/api.ts")
	if err != nil {
		t.Fatal(err)
	}
	calls, _, _ := parseTS(t, string(source))
	got := map[string]bool{}
	for _, call := range calls {
		if call.method == "POST" && strings.Contains(call.path, "/git/") {
			got[call.path] = true
		}
	}
	for _, action := range []string{"stage", "unstage", "commit", "branch"} {
		path := "/v1/m/sessions/runs/{}/git/" + action
		if !got[path] {
			t.Errorf("console Git action must expose its literal registered route: POST %s (got %v)", path, got)
		}
		delete(got, path)
	}
	if len(got) != 0 {
		t.Errorf("console Git actions expose unregistered routes: %v", got)
	}
}
