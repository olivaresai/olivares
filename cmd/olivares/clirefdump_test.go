// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// The documentation generator writes the full dump via this test hook. The
// installed CLI exposes the same builder through `commands -o json`, so scripts
// need no third diagnostic subcommand. This test always asserts determinism and
// environment independence, even when OLIVARES_CLIREF_DUMP_OUT is unset.

package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// cliRefDumpEnv names the file TestCLIRefDump writes the tree to. Unset, the
// test still runs every assertion below and writes nothing.
const cliRefDumpEnv = "OLIVARES_CLIREF_DUMP_OUT"

func marshalCLIRefDump(t *testing.T, d cliRefDump) []byte {
	t.Helper()
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		t.Fatalf("marshal cli-ref dump: %v", err)
	}
	return append(b, '\n')
}

// TestCLIRefDump is the enumeration the generated CLI reference is built from,
// and the witness that the enumeration is stable.
//
// It asserts three things and then, optionally, writes the dump:
//
//  1. The walk is DETERMINISTIC — two builds of the tree marshal to identical
//     bytes. Without this the page churns on every regeneration.
//  2. The walk is ENVIRONMENT-INDEPENDENT. A flag whose DefValue is read from
//     the environment at construction time would publish the machine that ran
//     the generator, and the gate would flap between developers. `--data-dir`
//     shows the shape that is correct instead: its resolution order is PROSE in
//     the usage string and its DefValue is empty.
//  3. The tree is not empty and every command carries -h/--help, which is what
//     lets the reference state that rule once instead of on 700 rows.
func TestCLIRefDump(t *testing.T) {
	// Read the destination BEFORE the scrub below, which blanks every OLIVARES_*
	// variable and would otherwise blank this one too. That mistake was made and
	// caught here on 2026-08-16; it failed CLOSED (no file written, so the
	// generator reported CANNOT LOOK rather than "in sync"), which is the only
	// reason it was cheap.
	out := os.Getenv(cliRefDumpEnv)

	first := marshalCLIRefDump(t, buildCLIRefDump())
	if second := marshalCLIRefDump(t, buildCLIRefDump()); string(first) != string(second) {
		t.Fatalf("cli-ref dump is not deterministic: two walks of the same tree differ")
	}

	dump := buildCLIRefDump()
	if len(dump.Commands) < 2 {
		t.Fatalf("cli-ref dump found %d commands; the tree cannot be that small, so the walk is broken",
			len(dump.Commands))
	}
	for _, c := range dump.Commands {
		if !c.HasHelpFlag {
			t.Errorf("command %q has no -h/--help flag; the reference states that rule once for the whole tree", c.Path)
		}
	}

	// (2) Rebuild under a different environment. Every OLIVARES_* variable in the
	// ambient environment is blanked and the home/XDG pair is pointed somewhere
	// that does not exist, so any default derived from them changes visibly.
	for _, kv := range os.Environ() {
		if k, _, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, "OLIVARES_") {
			t.Setenv(k, "")
		}
	}
	t.Setenv("HOME", "/zz-s761-cliref-home")
	t.Setenv("XDG_DATA_HOME", "/zz-s761-cliref-xdg")
	t.Setenv("XDG_CONFIG_HOME", "/zz-s761-cliref-xdgcfg")
	scrubbed := buildCLIRefDump()
	for i, c := range scrubbed.Commands {
		if i >= len(dump.Commands) || dump.Commands[i].Path != c.Path {
			t.Fatalf("the command tree itself changed with the environment at %q; the reference cannot describe a tree that depends on who generates it", c.Path)
		}
		was := dump.Commands[i].Flags
		for j, f := range c.Flags {
			if j >= len(was) {
				t.Fatalf("%s grew flag %q when the environment changed", c.Path, f.Name)
			}
			if was[j].Default != f.Default {
				t.Errorf("%s --%s takes its default from the environment (%q with the ambient env, %q without): "+
					"publish the resolution order as prose in the flag's usage string, the way --data-dir does, "+
					"so the reference does not publish the machine that generated it",
					c.Path, f.Name, was[j].Default, f.Default)
			}
		}
	}

	if out == "" {
		return
	}
	if err := os.WriteFile(out, first, 0o600); err != nil {
		t.Fatalf("write cli-ref dump to %s: %v", out, err)
	}
}

func TestCLIRefJSONGolden(t *testing.T) {
	root := &cobra.Command{Use: "demo", Short: "Demo CLI", Annotations: map[string]string{"changes_data": "read"}}
	root.AddGroup(&cobra.Group{ID: "manage", Title: "Manage:"})
	root.PersistentFlags().String("config", "default.toml", "Configuration file")
	items := &cobra.Command{Use: "items", Short: "Manage items", GroupID: "manage", Annotations: map[string]string{"changes_data": "read"}}
	rm := &cobra.Command{Use: "rm <id>", Short: "Remove an item", Long: "Remove the saved item.", Example: "  demo items rm one --yes", Aliases: []string{"remove", "delete"}, Annotations: map[string]string{"changes_data": "delete"}, RunE: func(*cobra.Command, []string) error { return nil }}
	rm.Flags().Bool("yes", false, "Confirm removal")
	if err := rm.MarkFlagRequired("yes"); err != nil {
		t.Fatal(err)
	}
	rm.Flags().String("legacy", "", "Legacy input")
	if err := rm.Flags().MarkHidden("legacy"); err != nil {
		t.Fatal(err)
	}
	if err := rm.Flags().MarkDeprecated("legacy", "use the id argument"); err != nil {
		t.Fatal(err)
	}
	items.AddCommand(rm)
	root.AddCommand(items)
	hidden := &cobra.Command{Use: "paid", Short: "Unavailable", Hidden: true, Annotations: map[string]string{"changes_data": "read"}}
	hidden.AddCommand(&cobra.Command{Use: "child", Short: "Hidden by parent", Annotations: map[string]string{"changes_data": "write"}})
	root.AddCommand(hidden)
	dump := collectCLIRefDump(root, false)
	got := marshalCLIRefDump(t, dump)
	want, err := os.ReadFile("testdata/command-surface.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("command surface JSON differs from golden:\n%s", got)
	}
	if full := collectCLIRefDump(root, true); len(full.Commands) != 5 {
		t.Fatalf("full diagnostic lost hidden subtree: %d commands", len(full.Commands))
	}
}
