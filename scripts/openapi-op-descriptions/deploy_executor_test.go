// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestDeployExecutorConfigDescriptions reads the production deploy module: the three
// saved-executor routes must compose a description from their handlers' doc comments
// and the committed beta document must carry exactly that text.
func TestDeployExecutorConfigDescriptions(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	m, err := compose(root)
	if err != nil {
		t.Fatal(err)
	}
	routes, err := routesInPackage(filepath.Join(root, modulesDirRel, "deploy"), root)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"GET /v1/m/deploy/executor/config": "Returns the saved local Docker executor setup, empty when none is saved, and whether an operator override replaces it.",
		"PUT /v1/m/deploy/executor/config": "Saves the local Docker executor setup for the tenant as the local Docker socket path and a store: secret reference, then returns it; requires step-up, answers 400 for any other socket or reference and 409 while an operator override is in effect.",
		"POST /v1/m/deploy/executor/test":  "Checks the saved credential and the local Docker daemon read-only and returns connected; it plans, creates, starts, stops and removes nothing, and answers 409 while an operator override is in effect, 503 when no executor is saved and 502 when the check fails.",
	}
	seen := map[string]bool{}
	for _, route := range routes {
		if !strings.HasPrefix(route.handlerPos, filepath.Join(modulesDirRel, "deploy", "executor_config.go")+":") {
			continue
		}
		key := route.key()
		seen[key] = true
		t.Run(key, func(t *testing.T) {
			desc, ok := want[key]
			if !ok {
				t.Fatalf("unexpected executor_config.go registration %s; add its description here", key)
			}
			if got := m.entries[key].description; got != desc {
				t.Errorf("composed description = %q, want %q", got, desc)
			}
			if got := m.entries[key].source; got != sourceDoc {
				t.Errorf("description source = %q, want %q: the handler's doc comment, not a catalog row", got, sourceDoc)
			}
			if got := m.published[key]; got != desc {
				t.Errorf("published description = %q, want %q", got, desc)
			}
		})
	}
	for key := range want {
		if !seen[key] {
			t.Errorf("%s is not registered by executor_config.go handlers", key)
		}
	}
}

// TestSessionPreviewDescriptions pins the two preview routes the same way: their
// handlers' doc comments are the only source and the beta document carries them.
func TestSessionPreviewDescriptions(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	m, err := compose(root)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"GET /v1/m/sessions/runs/{ref}/preview":  "Lists the loopback ports the run's own processes listen on, which a browser preview can open; a run not live on this node has none.",
		"POST /v1/m/sessions/runs/{ref}/preview": "Opens a browser preview of one port the run listens on and returns its URL, which expires within the hour and when the session stops.",
	}
	for key, desc := range want {
		t.Run(key, func(t *testing.T) {
			if got := m.entries[key].description; got != desc {
				t.Errorf("composed description = %q, want %q", got, desc)
			}
			if got := m.entries[key].source; got != sourceDoc {
				t.Errorf("description source = %q, want %q: the handler's doc comment, not a catalog row", got, sourceDoc)
			}
			if got := m.published[key]; got != desc {
				t.Errorf("published description = %q, want %q", got, desc)
			}
		})
	}
}
