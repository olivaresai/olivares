// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

var supportBundleNameRe = regexp.MustCompile(`^olivares-support-\d{8}-\d{6}Z\.tar\.gz$`)

// TestSupportBundleDefaultOutIsClockFree guards the two halves of the fix
// together, because either one alone is a regression waiting to happen.
//
// WHAT WENT WRONG. `--out` defaulted to a string built with time.Now() at the
// moment the command was CONSTRUCTED. Two consequences, and the second is the
// expensive one: `olivares support bundle --help` advertised a different default
// on every invocation, and the generated CLI reference
// (scripts/check-cli-ref-docs.sh) could never be byte-stable — one flag out of
// 2209 keeping a whole published page permanently in drift. Measured 2026-08-16:
// two walks of the command tree nine seconds apart differed in this flag alone.
//
// So the first half asserts the flag advertises NO clock-derived default, and the
// second asserts the behavior that default used to provide still happens: run
// without --out and a timestamped archive is still what you get. Without the
// second half, "fix" and "delete the feature" would look identical to this file.
func TestSupportBundleDefaultOutIsClockFree(t *testing.T) {
	t.Run("advertised default does not move with the clock", func(t *testing.T) {
		f := supportBundleCmd().Flags().Lookup("out")
		if f == nil {
			t.Fatal("support bundle has no --out flag")
		}
		if f.DefValue != "" {
			t.Errorf("--out advertises the default %q; a default computed from the clock changes "+
				"every second, so --help and the generated reference can never agree", f.DefValue)
		}
		// The resolution has to be stated somewhere the operator reads, exactly as
		// --data-dir states its own: prose in the usage string, not a live value.
		if !strings.Contains(f.Usage, "olivares-support-") {
			t.Errorf("--out usage %q does not say what the default name is", f.Usage)
		}
	})

	t.Run("omitting --out still writes a timestamped archive", func(t *testing.T) {
		dir := t.TempDir()
		configPath := filepath.Join(dir, "olivares.env")
		writeSupportTestFile(t, configPath, "OLIVARES_LOG_LEVEL=info\n")

		t.Chdir(dir)
		cmd := supportBundleCmd()
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{"--offline", "--include", "config", "--config", configPath})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("support bundle without --out: %v\n%s", err, out.String())
		}

		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		var found []string
		for _, e := range entries {
			if supportBundleNameRe.MatchString(e.Name()) {
				found = append(found, e.Name())
			}
		}
		if len(found) != 1 {
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			t.Fatalf("want exactly one olivares-support-<stamp>.tar.gz written with no --out, got %v "+
				"(directory: %v)", found, names)
		}
		if !strings.Contains(out.String(), found[0]) {
			t.Errorf("the command reported %q but wrote %q; the operator is told the wrong path",
				out.String(), found[0])
		}
	})

	t.Run("the name is derived from the bundle's own timestamp", func(t *testing.T) {
		at := time.Date(2026, 8, 16, 20, 58, 51, 0, time.UTC)
		if got, want := defaultSupportBundleName(at), "olivares-support-20260816-205851Z.tar.gz"; got != want {
			t.Errorf("defaultSupportBundleName = %q, want %q", got, want)
		}
		// A non-UTC instant must still produce the UTC stamp the suffix claims.
		zone := time.FixedZone("UTC+2", 2*60*60)
		if got, want := defaultSupportBundleName(at.In(zone)), "olivares-support-20260816-205851Z.tar.gz"; got != want {
			t.Errorf("defaultSupportBundleName in %v = %q, want %q; the Z suffix would be a lie",
				zone, got, want)
		}
	})
}
