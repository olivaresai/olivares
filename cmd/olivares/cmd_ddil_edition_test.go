// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

func runDDILThroughRoot(args ...string) (stdout string, err error) {
	root := newRootCmd()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(append([]string{"ddil"}, args...))
	err = root.Execute()
	return out.String(), err
}

// DDIL air-gap bundles are a Business capability. Community refuses every verb with
// the Business sentence and exit code 9 before it validates a flag or touches a file:
// the required-flag errors below would otherwise answer first with exit code 2.
func TestDDILCommunityRefusesEveryVerbBeforeFlagsOrWrites(t *testing.T) {
	asEdition(t, "community")
	assertDDILRefusedEverywhere(t)
}

// The bare group is not a verb: like every other group it prints its help in every edition.
func TestDDILBareGroupStillPrintsHelpInCommunity(t *testing.T) {
	asEdition(t, "community")
	stdout, err := runDDILThroughRoot()
	if err != nil || !strings.Contains(stdout, "Usage:") {
		t.Fatalf("bare `ddil` = %q, %v; want the usage block and no error", stdout, err)
	}
}

// The gate fails closed: an edition that is not the Business build, named or not, is refused too.
func TestDDILUnknownEditionIsRefused(t *testing.T) {
	for _, name := range []string{"", "business-preview"} {
		t.Run("edition "+name, func(t *testing.T) {
			asEdition(t, name)
			assertDDILRefusedEverywhere(t)
		})
	}
}

func assertDDILRefusedEverywhere(t *testing.T) {
	t.Helper()
	keyPath := filepath.Join(t.TempDir(), "ddil.key")
	for _, tc := range []struct {
		verb string
		args []string
	}{
		{"keygen", []string{"keygen", "--out", keyPath}},
		{"verify without its required flags", []string{"verify"}},
		{"import without its required flags", []string{"import"}},
		{"export without its required flags", []string{"export"}},
	} {
		t.Run(tc.verb, func(t *testing.T) {
			stdout, err := runDDILThroughRoot(tc.args...)
			if err == nil {
				t.Fatalf("Community ran ddil %v: %q", tc.args, stdout)
			}
			if code := exitcode.From(err); code != exitcode.Edition {
				t.Errorf("exit code = %d, want %d (edition): %v", code, exitcode.Edition, err)
			}
			if want := "DDIL air-gap transfer is a Business feature: " + pricingURL; err.Error() != want {
				t.Errorf("error = %q, want %q", err.Error(), want)
			}
			if stdout != "" {
				t.Errorf("a refused verb printed %q", stdout)
			}
		})
	}
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Fatalf("the refused keygen wrote its key file: %v", err)
	}
}

// The Business edition keeps every verb: keygen runs, and verify, import and export
// reach their own flag validation instead of the edition refusal.
func TestDDILBusinessKeepsEveryVerb(t *testing.T) {
	asEdition(t, "enterprise")
	keyPath := filepath.Join(t.TempDir(), "ddil.key")
	stdout, err := runDDILThroughRoot("keygen", "--out", keyPath)
	if err != nil || strings.TrimSpace(stdout) == "" {
		t.Fatalf("Business ddil keygen = %q, %v", stdout, err)
	}
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("Business ddil keygen wrote no key file: %v", err)
	}
	for _, verb := range []string{"verify", "import", "export"} {
		_, err := runDDILThroughRoot(verb)
		if err == nil || exitcode.From(err) == exitcode.Edition || !strings.Contains(err.Error(), "required flag") {
			t.Errorf("Business ddil %s without flags = %v, want the required-flag error", verb, err)
		}
	}
}
