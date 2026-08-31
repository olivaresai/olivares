// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Machine-readable dump of the cobra tree, for the generated CLI reference
// (C09-03).
//
// WHY THIS IS A TEST AND NOT A SUBCOMMAND. The tree is built by RUNNING code:
// newRootCmd() adds command groups conditionally (enterpriseRootCommands,
// hideUnavailableAddOns), so no parse of the sources can enumerate it and no
// grep can. The data the reference needs — every flag's name, shorthand, type,
// default, usage string and hidden/deprecated/required status — lives in pflag
// structs, not in text. `olivares commands` already prints the tree, but only
// the PATHS: it drops every flag, which is most of a reference page.
//
// The alternative was a third hidden diagnostic beside `commands` and
// `firstparty-bins`. It was rejected: a test hook costs the shipped binary
// nothing, and measured 2026-08-16 the two routes cost the same anyway
// (go build ./cmd/olivares 2.4s, go test -run this 2.6s, both warm).
//
// NO t.Skip. A skip is how a montage failure becomes a silent pass: this test
// always walks the tree and always asserts, and merely ALSO writes the dump when
// OLIVARES_CLIREF_DUMP_OUT names a destination. The generator that consumes it
// verifies the file exists and is non-empty, so "the test did not run" can never
// read as "the docs are in sync".

package main

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// cliRefDumpEnv names the file TestCLIRefDump writes the tree to. Unset, the
// test still runs every assertion below and writes nothing.
const cliRefDumpEnv = "OLIVARES_CLIREF_DUMP_OUT"

// cliRefSchema is the dump's contract with scripts/cli-ref-docs. Bump it when a
// field changes meaning; the generator refuses a schema it does not know rather
// than silently reading a field that no longer means what it meant.
const cliRefSchema = "olivares.cli-ref/1"

type cliRefFlag struct {
	Name       string `json:"name"`
	Shorthand  string `json:"shorthand,omitempty"`
	Type       string `json:"type"`
	Default    string `json:"default"`
	Usage      string `json:"usage"`
	Persistent bool   `json:"persistent,omitempty"`
	Hidden     bool   `json:"hidden,omitempty"`
	Required   bool   `json:"required,omitempty"`
	Deprecated string `json:"deprecated,omitempty"`
}

type cliRefCommand struct {
	Path           string       `json:"path"`
	Name           string       `json:"name"`
	Parent         string       `json:"parent,omitempty"`
	Use            string       `json:"use"`
	Short          string       `json:"short"`
	Long           string       `json:"long,omitempty"`
	Example        string       `json:"example,omitempty"`
	Aliases        []string     `json:"aliases,omitempty"`
	GroupID        string       `json:"group_id,omitempty"`
	Depth          int          `json:"depth"`
	Hidden         bool         `json:"hidden,omitempty"`
	Deprecated     string       `json:"deprecated,omitempty"`
	Runnable       bool         `json:"runnable"`
	HasSubcommands bool         `json:"has_subcommands"`
	HasHelpFlag    bool         `json:"has_help_flag"`
	Flags          []cliRefFlag `json:"flags,omitempty"`
}

type cliRefGroup struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type cliRefDump struct {
	Schema   string          `json:"schema"`
	Root     string          `json:"root"`
	Groups   []cliRefGroup   `json:"groups"`
	Commands []cliRefCommand `json:"commands"`
}

// collectCLIRefFlags returns the command's OWN flags — local plus the persistent
// ones it declares for its children — and never the ones it inherits. Inherited
// flags are published once, at the ancestor that declares them; repeating the
// root's -o on all 699 descendants would be the page's single largest section
// and would say nothing.
func collectCLIRefFlags(cmd *cobra.Command) []cliRefFlag {
	persistent := map[string]bool{}
	cmd.PersistentFlags().VisitAll(func(f *pflag.Flag) { persistent[f.Name] = true })

	var out []cliRefFlag
	cmd.LocalFlags().VisitAll(func(f *pflag.Flag) {
		// cobra marks a required flag with an annotation rather than a field.
		_, required := f.Annotations[cobra.BashCompOneRequiredFlag]
		out = append(out, cliRefFlag{
			Name:       f.Name,
			Shorthand:  f.Shorthand,
			Type:       f.Value.Type(),
			Default:    f.DefValue,
			Usage:      f.Usage,
			Persistent: persistent[f.Name],
			Hidden:     f.Hidden,
			Required:   required,
			Deprecated: f.Deprecated,
		})
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// buildCLIRefDump walks a freshly built tree and returns it in a canonical
// order: commands by path, flags by name, groups as declared. Nothing here reads
// a clock, a filesystem listing or a map iteration, so two calls on one tree
// produce identical bytes — the property TestCLIRefDump asserts below and the
// generated page depends on.
func buildCLIRefDump() cliRefDump {
	root := newRootCmd()
	dump := cliRefDump{Schema: cliRefSchema, Root: root.Name()}
	for _, g := range root.Groups() {
		dump.Groups = append(dump.Groups, cliRefGroup{ID: g.ID, Title: g.Title})
	}

	var walk func(cmd *cobra.Command, depth int)
	walk = func(cmd *cobra.Command, depth int) {
		// Register -h/--help exactly as cobra does before it renders help, so the
		// dump describes the command a user meets rather than a half-built one.
		cmd.InitDefaultHelpFlag()
		parent := ""
		if cmd.HasParent() {
			parent = cmd.Parent().CommandPath()
		}
		rec := cliRefCommand{
			Path:           cmd.CommandPath(),
			Name:           cmd.Name(),
			Parent:         parent,
			Use:            cmd.Use,
			Short:          cmd.Short,
			Long:           cmd.Long,
			Example:        cmd.Example,
			Aliases:        append([]string(nil), cmd.Aliases...),
			GroupID:        cmd.GroupID,
			Depth:          depth,
			Hidden:         cmd.Hidden,
			Deprecated:     cmd.Deprecated,
			Runnable:       cmd.Runnable(),
			HasSubcommands: cmd.HasSubCommands(),
			HasHelpFlag:    cmd.Flags().Lookup("help") != nil,
			Flags:          collectCLIRefFlags(cmd),
		}
		sort.Strings(rec.Aliases)
		dump.Commands = append(dump.Commands, rec)
		for _, child := range cmd.Commands() {
			walk(child, depth+1)
		}
	}
	walk(root, 0)
	sort.Slice(dump.Commands, func(i, j int) bool { return dump.Commands[i].Path < dump.Commands[j].Path })
	return dump
}

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
