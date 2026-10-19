// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// runRootForTest executes the root command with args and returns stdout.
func runRootForTest(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

// TestRootVersionWired proves the cobra root carries the build metadata, so
// `olivares --version` reports the SAME stamped version/commit/date the
// `version` subcommand does — the E2 traceability contract. A root with
// Version == "" silently drops the --version flag entirely.
func TestRootVersionWired(t *testing.T) {
	root := newRootCmd()
	if root.Version == "" {
		t.Fatal("root.Version is empty: cobra --version is not wired")
	}
	for _, want := range []string{version, commit, date} {
		if !strings.Contains(root.Version, want) {
			t.Errorf("root.Version %q does not carry build metadatum %q", root.Version, want)
		}
	}
}

// TestCommandsCmdPrintsSortedTree proves the hidden `commands` diagnostic prints
// the FULL cobra command tree, sorted and one path per line — the stable snapshot
// scripts/release-smoke.sh diffs between a packaged artifact and a binary built
// from the same source to catch a stale/divergent release binary (E2).
func TestCommandsCmdPrintsSortedTree(t *testing.T) {
	out, err := runRootForTest(t, "commands")
	if err != nil {
		t.Fatalf("commands: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 20 {
		t.Fatalf("suspiciously small command tree (%d lines):\n%s", len(lines), out)
	}
	for _, want := range []string{"olivares serve", "olivares version", "olivares dr drill", "olivares commands"} {
		if !strings.Contains(out, want+"\n") && !strings.HasSuffix(out, want) {
			t.Errorf("command tree is missing %q", want)
		}
	}
	if !sort.StringsAreSorted(lines) {
		t.Error("command tree is not sorted; the smoke diff would be order-flaky")
	}
}

// TestFirstPartyBinsCmdHonest proves the hidden `firstparty-bins` diagnostic
// never lies in either build state: it always prints an honest count (zero on a
// plain dev build, the extract-verified list when bins/ is populated), while
// --require of an absent plugin fails — that failure is exactly what the
// release smoke asserts against the artifact.
func TestFirstPartyBinsCmdHonest(t *testing.T) {
	out, err := runRootForTest(t, "firstparty-bins")
	if err != nil {
		t.Fatalf("firstparty-bins: %v", err)
	}
	if !strings.Contains(out, "embedded connector plugin(s)") {
		t.Errorf("missing count trailer in output:\n%s", out)
	}

	// A name that can never exist must fail closed under --require.
	if _, err := runRootForTest(t, "firstparty-bins", "--require", "no-such-plugin"); err == nil {
		t.Fatal("--require no-such-plugin succeeded; the release smoke would be vacuous")
	}
}

func TestCommandsJSONSurface(t *testing.T) {
	previous := thisEdition
	t.Cleanup(func() { thisEdition = previous })
	thisEdition.name, thisEdition.addOnsLinked = "community", false
	out, err := runRootForTest(t, "commands", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var dump struct {
		Schema   string `json:"schema"`
		Commands []struct {
			Path        string       `json:"path"`
			Short       string       `json:"short"`
			ChangesData string       `json:"changes_data"`
			Hidden      bool         `json:"hidden"`
			Flags       []cliRefFlag `json:"flags"`
		} `json:"commands"`
	}
	if err := json.Unmarshal([]byte(out), &dump); err != nil {
		t.Fatalf("commands -o json: %v", err)
	}
	if dump.Schema != "olivares.cli-ref/1" || len(dump.Commands) < 20 {
		t.Fatalf("incomplete dump: %s", out)
	}
	for _, c := range dump.Commands {
		if c.Path == "olivares" {
			found := false
			for _, f := range c.Flags {
				if f.Name == "version" {
					found = true
				}
			}
			if !found {
				t.Error("root --version flag is missing")
			}
		}
		if c.Hidden {
			t.Errorf("hidden command offered: %s", c.Path)
		}
		if strings.TrimSpace(c.Short) == "" {
			t.Errorf("summary missing: %s", c.Path)
		}
		if c.ChangesData != "read" && c.ChangesData != "write" && c.ChangesData != "delete" {
			t.Errorf("data effect missing: %s", c.Path)
		}
		for _, hidden := range []string{"olivares hooks", "olivares threatintel", "olivares governance breakglass", "olivares finops", "olivares redteam", "olivares reporting schedules", "olivares reporting enterprise", "olivares reporting branding", "olivares reporting templates", "olivares reporting signing"} {
			if c.Path == hidden || strings.HasPrefix(c.Path, hidden+" ") {
				t.Errorf("unavailable command offered: %s", c.Path)
			}
		}
	}
	for path, want := range map[string]string{"olivares help": "read", "olivares session ls": "read", "olivares session start": "write", "olivares session rm": "delete", "olivares tool install": "write", "olivares tool rm": "delete"} {
		found := false
		for _, c := range dump.Commands {
			if c.Path == path {
				found = true
				if c.ChangesData != want {
					t.Errorf("%s effect=%s, want %s", path, c.ChangesData, want)
				}
			}
		}
		if !found {
			t.Errorf("missing visible command: %s", path)
		}
	}
}

// Verbs whose name already says what they do with saved data. A command with
// one of these names that declares anything else is a mislabel, whatever its
// flags: a wrong value here would tell a script a destructive verb is safe.
var changesDataByVerb = map[string]string{
	"rm": "delete", "delete": "delete", "prune": "delete", "remove": "delete",
	"revoke": "delete", "purge": "delete", "destroy": "delete", "cleanup": "delete",
}

var verbsThatNeverRead = map[string]string{
	"create": "creates data", "add": "adds data", "update": "updates data", "put": "stores data",
	"import": "imports data", "mkdir": "creates a directory", "mv": "moves data",
}

func TestCLIChangesDataCensus(t *testing.T) {
	var walk func(*cobra.Command)
	walk = func(cmd *cobra.Command) {
		effect := cmd.Annotations["changes_data"]
		if effect != "read" && effect != "write" && effect != "delete" {
			t.Errorf("%s must declare read/write/delete, got %q", cmd.CommandPath(), effect)
		}
		for _, name := range append([]string{cmd.Name()}, cmd.Aliases...) {
			if want, ok := changesDataByVerb[name]; ok && effect != want {
				t.Errorf("%s (%s) must declare %s, got %s", cmd.CommandPath(), name, want, effect)
			}
			if notRead, ok := verbsThatNeverRead[name]; ok && effect == "read" {
				t.Errorf("%s (%s) %s so it cannot declare read", cmd.CommandPath(), name, notRead)
			}
		}
		for _, child := range cmd.Commands() {
			walk(child)
		}
	}
	walk(newRootCmd())
}

func TestCommandsOutputErrorsAndExitCodes(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			root := newRootCmd()
			root.SetOut(commandsFailWriter{})
			root.SetArgs([]string{"commands", "-o", format})
			cmd, err := root.ExecuteC()
			if !errors.Is(err, errCommandsWrite) {
				t.Fatalf("write error lost: %v", err)
			}
			code, _ := classifyOutcome(cmd, err)
			if code != 1 {
				t.Fatalf("write failure exit=%d, want 1", code)
			}
		})
	}
	for _, args := range [][]string{{"commands"}, {"commands", "-o", "text"}, {"commands", "-o", "json"}, {"commands", "extra"}, {"commands", "-o", "xml"}} {
		root := newRootCmd()
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		root.SetArgs(args)
		cmd, err := root.ExecuteC()
		code, _ := classifyOutcome(cmd, err)
		want := 0
		if args[len(args)-1] == "extra" || args[len(args)-1] == "xml" {
			want = 2
		}
		if code != want {
			t.Errorf("%v exit=%d, want %d (%v)", args, code, want, err)
		}
	}
}

var errCommandsWrite = fmt.Errorf("commands output refused")

type commandsFailWriter struct{}

func (commandsFailWriter) Write([]byte) (int, error) { return 0, errCommandsWrite }

// Effects follow handlers rather than HTTP methods or names. Optional flags use
// the strongest effect; ordinary reads exclude incidental audit records.
func TestCLIChangesDataEffects(t *testing.T) {
	root := newRootCmd()
	for path, want := range map[string]string{
		"work validate": "read", "work plan": "write", "work apply": "delete",
		"skills enable": "write", "skills disable": "delete",
		"sourcescope posture-requests approve": "delete", "sourcescope posture-requests reject": "write",
		"sourcescope guard-postures set": "write", "sourcescope workspace-connectors set": "write",
		"tool pin": "delete", "users totp-reset": "delete", "tokens rotate": "write", "tokens revoke": "delete",
		"sandbox generate": "read", "recording sessions export": "read",
		"reporting reports get": "write", "reporting templates get": "write", "reporting schedules run": "write",
		"dr backup": "delete", "dr restore": "delete", "doctor": "read", "dr inspect": "read", "dr verify": "read",
		"deploy plan": "write", "deploy verify": "write",
		"knowledge kbs query": "read", "knowledge kbs sync": "delete",
		"agent session cleanup": "delete", "agent session stop": "write", "agent session resume": "write",
		"agent workspace project-config": "read", "agent workspace approve-project-config": "write",
	} {
		t.Run(path, func(t *testing.T) {
			cmd, remaining, err := root.Find(strings.Fields(path))
			if err != nil || len(remaining) != 0 {
				t.Fatalf("command does not exist: %v (%v)", err, remaining)
			}
			if got := cmd.Annotations["changes_data"]; got != want {
				t.Errorf("changes_data=%q, want %q", got, want)
			}
		})
	}
}

// A hidden parent hides its whole subtree. The JSON must never list a command
// whose parent is missing: that would be a Business child leaking out of a
// hidden group.
func TestCommandsJSONListsNoOrphan(t *testing.T) {
	out, err := runRootForTest(t, "commands", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var dump cliRefDump
	if err := json.Unmarshal([]byte(out), &dump); err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, c := range dump.Commands {
		listed[c.Path] = true
	}
	for _, c := range dump.Commands {
		if c.Parent != "" && !listed[c.Parent] {
			t.Errorf("%s is listed but its parent %s is not", c.Path, c.Parent)
		}
	}
}

func TestCommandsJSONBusinessVisibility(t *testing.T) {
	previous := thisEdition
	t.Cleanup(func() { thisEdition = previous })
	thisEdition.name, thisEdition.addOnsLinked = "enterprise", false
	out, err := runRootForTest(t, "commands", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var dump cliRefDump
	if err := json.Unmarshal([]byte(out), &dump); err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for _, c := range dump.Commands {
		paths[c.Path] = true
	}
	for _, path := range []string{"olivares finops", "olivares redteam", "olivares reporting schedules", "olivares reporting enterprise", "olivares reporting branding", "olivares reporting templates", "olivares reporting signing"} {
		if !paths[path] {
			t.Errorf("Business surface omits %s", path)
		}
	}
	for _, path := range []string{"olivares hooks", "olivares threatintel"} {
		if paths[path] {
			t.Errorf("unlinked add-on offered: %s", path)
		}
	}
}
