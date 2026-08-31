// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// TestInvariant_DRPassphraseFloor pins: the passphrase that encrypts a DR
// bundle at rest — its ONLY protection — must be at least minDRPassphraseLen runes
// at CREATION time. A regression that lowers the floor or drops the helper's check
// must fail here.
func TestInvariant_DRPassphraseFloor(t *testing.T) {
	if minDRPassphraseLen < 12 {
		t.Fatalf("DR passphrase floor regressed below 12 (got %d)", minDRPassphraseLen)
	}
	tooShort := strings.Repeat("a", minDRPassphraseLen-1)
	if msg := drPassphraseFloorError(tooShort); msg == "" {
		t.Errorf("a %d-rune passphrase must be rejected by the floor", minDRPassphraseLen-1)
	}
	atFloor := strings.Repeat("a", minDRPassphraseLen)
	if msg := drPassphraseFloorError(atFloor); msg != "" {
		t.Errorf("a %d-rune passphrase must be accepted, got error %q", minDRPassphraseLen, msg)
	}
	// The empty passphrase carries no floor here (the "encrypted vs unencrypted"
	// choice is enforced separately); the floor helper itself must not reject "".
	if msg := drPassphraseFloorError(""); msg != "" {
		t.Errorf("floor helper must not reject empty passphrase (handled elsewhere), got %q", msg)
	}
}

// TestInvariant_DRPassphraseFloor_CreateEnforcesRestoreExempt pins the ratified
// carve-out (dr_handler.go: "Restore deliberately does not apply this creation
// policy: legacy bundles encrypted with a shorter passphrase must remain
// recoverable"). The floor is enforced on the CREATE entrypoints and deliberately
// ABSENT on the RESTORE entrypoints. Two regressions must fail here:
//   - a create path that stops calling the floor (weakens new bundles), and
//   - a restore path that STARTS calling the floor (bricks legacy recovery).
//
// This is a source-level guard: it parses the DR handlers and checks which call
// drPassphraseFloorError, so the invariant survives a body refactor.
func TestInvariant_DRPassphraseFloor_CreateEnforcesRestoreExempt(t *testing.T) {
	callsFloor := func(file, fn string) bool {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		var seen, found bool
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Name.Name != fn {
				continue
			}
			seen = true
			ast.Inspect(fd, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && id.Name == "drPassphraseFloorError" {
					found = true
				}
				return true
			})
		}
		if !seen {
			t.Fatalf("function %s not found in %s — DR handlers were refactored; update this invariant", fn, file)
		}
		return found
	}

	// CREATE paths MUST enforce the floor.
	for _, c := range []struct{ file, fn string }{
		{"dr_handler.go", "handleTriggerBackup"},
		{"dr_startup.go", "RunStartupBackup"},
	} {
		if !callsFloor(c.file, c.fn) {
			t.Errorf("create path %s (%s) must enforce the passphrase floor", c.fn, c.file)
		}
	}

	// RESTORE paths MUST NOT enforce the floor (legacy-recovery carve-out).
	for _, c := range []struct{ file, fn string }{
		{"dr_handler.go", "handleRestoreApply"},
		{"dr_handler.go", "handleRestoreApprove"},
	} {
		if callsFloor(c.file, c.fn) {
			t.Errorf("restore path %s (%s) must NOT apply the create-time floor — it would brick legacy bundle recovery", c.fn, c.file)
		}
	}
}
