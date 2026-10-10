// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"path/filepath"
	"testing"
)

func TestOrchestrationAvailabilityDescriptions(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	// Read all production registrations, including the two operations that
	// previously carried catalog descriptions for unavailable paid behavior.
	routes, err := routesInPackage(filepath.Join(root, "modules", "orchestration"), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 26 {
		t.Fatalf("got %d orchestration registrations, want 26", len(routes))
	}
	catalog, err := readCatalog(filepath.Join(root, catalogRel))
	if err != nil {
		t.Fatal(err)
	}
	const want = "Returns HTTP 501 with orchestration_unavailable because orchestration is a Business capability; no orchestration action is performed."
	for _, route := range routes {
		t.Run(route.key(), func(t *testing.T) {
			description, err := deriveFromDoc(route.handlerName, route.doc)
			if err != nil {
				description = catalog[route.key()].description
			}
			if description != want {
				t.Errorf("description = %q, want %q", description, want)
			}
			if err := validateDescription(description); err != nil {
				t.Errorf("unpublishable description: %v", err)
			}
		})
	}
}
