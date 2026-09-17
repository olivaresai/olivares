// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package localinstall

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The allowlist that bounds the exec seam is only worth having if it CANNOT fall behind the call
// sites. So this test does not hold a second copy of the list: it PARSES uninstall.go, collects
// the literal name of every `run(...)` call, and requires each one to be in `uninstallCommands`.
//
// That is the mutant this pair exists for: adding a seventh command and forgetting the allowlist.
// A test that only checked `uninstallCommands["systemctl"]` would stay green through exactly that
// change, which is the failure the allowlist was added to prevent.
func TestUninstallRunNamesAreAllowlisted(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "uninstall.go", nil, 0)
	if err != nil {
		t.Fatalf("parse uninstall.go: %v", err)
	}

	seen := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		ident, ok := call.Fun.(*ast.Ident)
		if !ok || ident.Name != "run" || len(call.Args) < 2 {
			return true
		}
		// run(runner, "name", args...) — the SECOND argument is the program.
		lit, ok := call.Args[1].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			t.Errorf("run() called with a non-literal program name at %s: the allowlist can no longer be checked by reading, decide deliberately", fset.Position(call.Pos()))
			return true
		}
		seen[strings.Trim(lit.Value, `"`)] = true
		return true
	})

	if len(seen) == 0 {
		t.Fatal("found no run() call sites: this test would pass vacuously, which is worse than failing")
	}
	for name := range seen {
		if _, ok := uninstallCommands[name]; !ok {
			t.Errorf("uninstall.go executes %q and uninstallCommands does not allow it", name)
		}
	}
	// And the other direction, so the list cannot rot into a museum of commands nobody runs.
	for name := range uninstallCommands {
		if !seen[name] {
			t.Errorf("uninstallCommands allows %q and no call site uses it", name)
		}
	}
}

// The refusal itself: an unknown program never reaches exec. `runner` is nil on purpose — that is
// the branch that would otherwise call exec.Command — and the check returns before it, so this
// test starts no process.
func TestUninstallRunRefusesUnknownProgram(t *testing.T) {
	err := run(nil, "curl", "https://example.invalid")
	if err == nil {
		t.Fatal("run() accepted a program outside the allowlist")
	}
	if !strings.Contains(err.Error(), "refuses to execute") || !strings.Contains(err.Error(), "curl") {
		t.Fatalf("the refusal does not name what it refused: %v", err)
	}
}

// Exercise the production exec path: an injected runner returns before the allowlist.
// Inert executables record argv, so this catches both rejecting every allowed command
// and accidentally executing an unknown command that happens to exist on PATH.
func TestUninstallRunAllowsServiceManagers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native service-manager fixtures use POSIX shell scripts")
	}
	ports := t.TempDir()
	marker := filepath.Join(ports, "args")
	t.Setenv("PATH", ports)
	t.Setenv("OLIVARES_TEST_EXEC_ARGS", marker)
	names := []string{"systemctl", "launchctl", "rc-service", "rc-update", "userdel", "groupdel", "curl"}
	for _, name := range names {
		script := []byte("#!/bin/sh\nprintf '%s\\n' \"$0\" \"$@\" >> \"$OLIVARES_TEST_EXEC_ARGS\"\n")
		if err := os.WriteFile(filepath.Join(ports, name), script, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range names[:6] {
		if err := run(nil, name, "argument with spaces", "literal;argument"); err != nil {
			t.Fatalf("allowed %s refused: %v", name, err)
		}
	}
	before, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	var want strings.Builder
	for _, name := range names[:6] {
		want.WriteString(filepath.Join(ports, name) + "\nargument with spaces\nliteral;argument\n")
	}
	if string(before) != want.String() {
		t.Fatalf("command or literal argv changed: got %q, want %q", before, want.String())
	}
	if err := run(nil, "curl", "never-execute"); err == nil || !strings.Contains(err.Error(), "refuses to execute") {
		t.Fatalf("want named refusal despite existing PATH executable: %v", err)
	}
	after, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("unallowlisted program executed")
	}
}
