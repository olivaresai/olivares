// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/internal/permcensus"
	"github.com/olivaresai/olivares/core/auth"
)

func TestPermissionInventoryUsesNativeComposition(t *testing.T) {
	out, _, err := execRoot(t, "openapi", "--permission-inventory")
	if err != nil {
		t.Fatal(err)
	}
	var inv struct {
		Schema   string   `json:"schema"`
		Roles    []string `json:"roles"`
		Declared map[string]struct {
			Forms  []string        `json:"forms"`
			Grants map[string]bool `json:"grants"`
		} `json:"declared"`
		Modules []struct {
			Namespace string                                                  `json:"namespace"`
			Routes    []struct{ Method, Pattern, Permission, Surface string } `json:"routes"`
		} `json:"modules"`
	}
	if err := json.Unmarshal([]byte(out), &inv); err != nil {
		t.Fatal(err)
	}
	if inv.Schema != "olivares.permissions.inventory/1" || len(inv.Modules) < 20 {
		t.Fatalf("incomplete native inventory: %s", out)
	}

	modules, err := moduleDocumentModules()
	if err != nil {
		t.Fatal(err)
	}
	if len(modules) != len(inv.Modules) {
		t.Errorf("native module set has %d entries, census has %d", len(modules), len(inv.Modules))
	}
	if len(inv.Roles) != 4 {
		t.Fatalf("incomplete role census: %v", inv.Roles)
	}
	for _, m := range modules {
		for _, p := range m.Permissions() {
			declaration, ok := inv.Declared[string(p)]
			if !ok {
				t.Errorf("native module declaration absent: %s", p)
				continue
			}
			for _, role := range inv.Roles {
				got, present := declaration.Grants[role]
				if !present || got != auth.RoleGrants(role, p) {
					t.Errorf("%s grant for %s disagrees with engine", p, role)
				}
			}
		}
	}
	doc, err := moduleOpenAPIDocument()
	if err != nil {
		t.Fatal(err)
	}
	paths := doc["paths"].(map[string]any)
	mounted := make(map[string]bool)
	surfaces := make(map[string]string)
	for _, m := range inv.Modules {
		for _, r := range m.Routes {
			mounted[r.Method+" /v1/m/"+m.Namespace+r.Pattern] = true
			surfaces[m.Namespace+" "+r.Method+" "+r.Pattern] = r.Surface
		}
	}
	count := 0
	for path, raw := range paths {
		for method := range raw.(map[string]any) {
			count++
			if !mounted[strings.ToUpper(method)+" "+path] {
				t.Errorf("native route absent from census: %s %s", method, path)
			}
		}
	}
	if permcensus.Business {
		for _, key := range []string{"finops POST /seats", "governance POST /breakglass/consume"} {
			if surfaces[key] != "api-only" {
				t.Errorf("native %s surface = %q, want api-only", key, surfaces[key])
			}
		}
	}
	t.Logf("native census: %d modules, %d declared permissions, %d mounted routes", len(inv.Modules), len(inv.Declared), count)
	if count != len(mounted) {
		t.Errorf("native census has %d routes, engine OpenAPI has %d", len(mounted), count)
	}
}
