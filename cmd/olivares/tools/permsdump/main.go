// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Command permsdump emits the control plane's PERMISSION INVENTORY: every
// permission the engine declares, in which FORM it is declared, and — for each
// built-in role — whether the engine actually grants it. The output is the source
// of truth the console's permission STRINGS are checked against (reshaped by
// there is no console-side RBAC mirror left to compare against).
//
// It RUNS the engine rather than reading it. That distinction is the whole point:
//
//   - Core permissions are not written down anywhere. They are CONCATENATED at
//     init — "session:read" is `"session" + ":" + VerbRead` (auth.permission.go
//     buildCoreRolePerms). A grep for the literal finds nothing and concludes the
//     permission is invented; an earlier cross-check of the console against
//     grep-able declaration forms reported 48 candidates, of which 45 were core
//     permissions that exist perfectly well. A method that cannot see a
//     declaration form reports the permissions using it as fabricated, and a
//     guard with false positives gets switched off within a week.
//   - The grant decision itself is a function, not a table: RoleGrants dispatches
//     on privileged-read membership, then module-vs-core shape. Re-implementing
//     that dispatch anywhere else creates a second copy that drifts — which is
//     precisely the defect this inventory exists to detect in the console.
//
// FOUR declaration forms are collected, because any one of them missing puts real
// permissions in the "undeclared" bucket:
//
//	core        the per-role explicit sets (auth.PermissionsForRole over every role)
//	privileged  auth.PrivilegedReadPerms — gated above the read tier, deliberately
//	            absent from the per-role core sets
//	module      what each module's Permissions() DECLARES
//	route       what a mounted route actually REQUIRES — the form that produces the
//	            403, captured through a recording api.RouteRegistrar
//
// The module and route forms are distinct facts and are reported separately. A
// permission a route demands but Permissions() omits is a real declaration bug
// (main_test.go asserts the two are equal, in both directions), and collapsing the
// two forms into one bucket would hide it.
//
// It no longer emits a TypeScript RBAC table for the console. It used to, and the table
// was a real improvement over the hand-written mirror it replaced — but removed the
// mirror itself: the engine now hands each grant its EFFECTIVE PERMISSION SET in
// /v1/auth/whoami and the console does set membership, so there is no client-side rule
// left to generate. The inventory below is still the source of truth the console's
// permission STRINGS are checked against (scripts/check-console-perms.mjs).
//
// Usage:
//
//	go run ./tools/permsdump            # the JSON inventory, to stdout
//	go run ./tools/permsdump -o <file>  # write to a file instead
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime/debug"

	"github.com/olivaresai/olivares/cmd/olivares/internal/permcensus"
)

type Inventory = permcensus.Inventory
type Route = permcensus.Route

func build() *Inventory { return permcensus.Build(allModules()) }

func invocationInventory() (*Inventory, error) {
	if !permcensus.Business {
		return build(), nil
	}
	// The native composition owns private registrars and configured edition seams.
	// Its own compiler-reported tags keep this subprocess on exactly the same build.
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return nil, fmt.Errorf("cannot determine the compiled engine build tags")
	}
	var tags string
	for _, setting := range info.Settings {
		if setting.Key == "-tags" {
			tags = setting.Value
		}
	}
	if tags == "" {
		return nil, fmt.Errorf("Business inventory has no compiler-reported build tags")
	}
	cmd := exec.Command("go", "run", "-tags="+tags, ".", "openapi", "--permission-inventory")
	cmd.Stderr = os.Stderr
	b, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("native engine permission inventory: %w", err)
	}
	var inv Inventory
	if err := json.Unmarshal(b, &inv); err != nil {
		return nil, fmt.Errorf("native permission inventory JSON: %w", err)
	}
	if inv.Schema != "olivares.permissions.inventory/1" || len(inv.Modules) == 0 || len(inv.Declared) == 0 {
		return nil, fmt.Errorf("native engine emitted an incomplete permission inventory")
	}
	return &inv, nil
}

func main() {
	out := flag.String("o", "", "write to this file instead of stdout")
	flag.Parse()

	inv, err := invocationInventory()
	if err != nil {
		fmt.Fprintf(os.Stderr, "permsdump: %v\n", err)
		os.Exit(1)
	}
	b, err := json.MarshalIndent(inv, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "permsdump: %v\n", err)
		os.Exit(1)
	}
	buf := append(b, '\n')
	if *out == "" {
		os.Stdout.Write(buf)
		return
	}
	if err := os.WriteFile(*out, buf, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "permsdump: %v\n", err)
		os.Exit(1)
	}
}
