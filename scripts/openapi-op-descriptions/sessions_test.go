// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"path/filepath"
	"testing"
)

func TestSessionGitActionRoutes(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	// Read the production registrations, including the helper reached from APIRoutes.
	routes, err := routesInPackage(filepath.Join(root, "modules", "sessions"), root)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := readCatalog(filepath.Join(root, catalogRel))
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"stage", "unstage", "commit", "branch"} {
		t.Run(action, func(t *testing.T) {
			key := "POST /v1/m/sessions/runs/{ref}/git/" + action
			count := 0
			for _, route := range routes {
				if route.key() == key {
					count++
					if route.handlerName != "handleRunGit" || route.handler != "(*Module).handleRunGit" {
						t.Errorf("%s mounts %q, want (*Module).handleRunGit", key, route.handler)
					}
				}
			}
			if count != 1 {
				t.Errorf("%s: got %d registrations, want 1", key, count)
			}
			if err := validateDescription(catalog[key].description); err != nil {
				t.Errorf("%s: catalog description: %v", key, err)
			}
		})
	}
}

func TestSessionGitPublishedContract(t *testing.T) {
	root := filepath.Join("..", "..")
	published, err := readPublishedOps(filepath.Join(root, betaSpecRel))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := readCatalog(filepath.Join(root, catalogRel))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"GET /v1/m/sessions/runs/{ref}/git",
		"POST /v1/m/sessions/runs/{ref}/git/branch",
		"POST /v1/m/sessions/runs/{ref}/git/commit",
		"POST /v1/m/sessions/runs/{ref}/git/stage",
		"POST /v1/m/sessions/runs/{ref}/git/unstage",
	} {
		t.Run(key, func(t *testing.T) {
			op, ok := published[key]
			if !ok {
				t.Fatalf("registered route %s missing from committed beta contract", key)
			}
			if err := validateDescription(op.description); err != nil {
				t.Fatal(err)
			}
			if op.description != catalog[key].description {
				t.Errorf("published description %q differs from catalog %q", op.description, catalog[key].description)
			}
		})
	}
}
