// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestHelpDoesNotAdvertiseUnavailableAddOns pins the E6 gate: the root help of
// the artifact that is actually distributed must not offer a command group whose
// every verb refuses in that artifact.
func TestHelpDoesNotAdvertiseUnavailableAddOns(t *testing.T) {
	root := newRootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs([]string{"--help"})
	if _, err := root.ExecuteC(); err != nil {
		t.Fatalf("--help failed: %v", err)
	}
	help := out.String()
	for _, name := range addOnOnlyCommands {
		listed := strings.Contains(help, "\n  "+name+" ")
		if enterpriseAddOnsLinked && !listed {
			t.Errorf("the enterprise build must list %q in the root help", name)
		}
		if !enterpriseAddOnsLinked && listed {
			t.Errorf("the root help offers %q, but every verb under it refuses in this build", name)
		}
	}
}

// TestUnavailableAddOnsStayInvocable is the other half, and the reason the fix
// hides rather than removes: an operator's existing script must keep getting the
// add-on's own explanation, not "unknown command".
func TestUnavailableAddOnsStayInvocable(t *testing.T) {
	root := newRootCmd()
	for _, name := range addOnOnlyCommands {
		cmd, _, err := root.Find([]string{name})
		if err != nil || cmd == nil || cmd.Name() != name {
			t.Fatalf("%q is no longer invocable: %v", name, err)
		}
		if strings.TrimSpace(cmd.Long) == "" {
			t.Errorf("%q is hidden from the help, so its own --help is the only "+
				"explanation left; it must have a Long description", name)
		}
	}
}

// TestNoSourceOffersTheEnterpriseBuildTag stops the repaired instruction coming
// back. `go build -tags enterprise ./cmd/olivares/` fails on this repository
// with undefined symbols — the commercial tree moved to its own distribution in
// so any user-facing text that tells an operator to build with that tag
// is sending them to a compile error.
//
// Comments are exempt: the tag is still the real name of the seam, and the
// wiring files legitimately describe it. This scans STRING LITERALS.
func TestNoSourceOffersTheEnterpriseBuildTag(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	var offenders []string
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, rerr := os.ReadFile(name) //nolint:gosec // fixed package-local path
		if rerr != nil {
			t.Fatalf("read %s: %v", name, rerr)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") || !strings.Contains(line, "-tags enterprise") {
				continue
			}
			// Only a quoted occurrence can reach an operator.
			if !strings.Contains(line, `"`) {
				continue
			}
			offenders = append(offenders, name+" line "+strings.TrimSpace(line[:min(len(line), 60)])+
				" (line "+strconv.Itoa(i+1)+")")
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("%d user-facing string(s) tell an operator to build with -tags enterprise, "+
			"which does not compile from this repository:\n  %s",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}
