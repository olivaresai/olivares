// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// deprecatedOutputWarning is scoped ON PURPOSE (E8). `--format` means two
// different things in this CLI: on eventing/hookpep/config/superadmin it is a
// legacy spelling of -o/--output, and on `audit export` and `findings export` it
// is a REAL, supported flag that selects the export format (cef|leef|syslog|
// otlp|ocsf, and sarif). An unqualified "--format is deprecated" teaches an
// operator a rule that, applied to their SIEM export, breaks it.
const deprecatedOutputWarning = "is deprecated ON THIS COMMAND; use -o/--output"

// formatCollisionNote is appended to the --format warning ONLY. --json has no
// twin, so telling its user about `audit export` would be noise.
const formatCollisionNote = " (the --format of 'audit export' and 'findings export' is a " +
	"different, fully supported flag that selects the export format — this does not apply to it)"

// deprecationWarningFor renders the warning for one alias, scoped to it.
func deprecationWarningFor(name string) string {
	if name == "format" {
		return deprecatedOutputWarning + formatCollisionNote
	}
	return deprecatedOutputWarning
}

// outputFlagValue validates the global output mode while flags are parsed. A
// rejected value therefore flows through the root FlagErrorFunc and carries the
// documented usage exit code instead of becoming an ordinary runtime failure.
type outputFlagValue struct {
	value string
}

func (v *outputFlagValue) Set(value string) error {
	switch value {
	case "text", "json":
		v.value = value
		return nil
	default:
		return exitcode.New(exitcode.Usage, fmt.Errorf("invalid --output %q (use text or json)", value))
	}
}

func (v *outputFlagValue) String() string {
	if v == nil || v.value == "" {
		return "text"
	}
	return v.value
}

func (*outputFlagValue) Type() string { return "string" }

// deprecatedOutputAlias backs the legacy --format and --json flags. When the
// command is attached to the real root it writes through to the global flag, so
// mixed old/new invocations retain normal last-flag-wins parsing semantics. The
// local value remains available to constructor-level tests that execute a command
// group without the application root.
type deprecatedOutputAlias struct {
	cmd     *cobra.Command
	name    string
	value   string
	boolean bool
	warned  bool
}

func (v *deprecatedOutputAlias) Set(raw string) error {
	value := raw
	if v.boolean {
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			return err
		}
		value = "text"
		if enabled {
			value = "json"
		}
	}
	if value != "text" && value != "json" {
		return exitcode.New(exitcode.Usage, fmt.Errorf("invalid --%s %q (use text or json)", v.name, value))
	}
	v.value = value
	if !v.warned {
		fmt.Fprintf(v.cmd.ErrOrStderr(), "--%s %s\n", v.name, deprecationWarningFor(v.name))
		v.warned = true
	}
	rootFlags := v.cmd.Root().PersistentFlags()
	if rootFlags.Lookup("output") != nil {
		return rootFlags.Set("output", value)
	}
	return nil
}

func (v *deprecatedOutputAlias) String() string {
	if v.boolean {
		return strconv.FormatBool(v.value == "json")
	}
	if v.value == "" {
		return "text"
	}
	return v.value
}

func (v *deprecatedOutputAlias) Type() string {
	if v.boolean {
		return "bool"
	}
	return "string"
}

func (v *deprecatedOutputAlias) IsBoolFlag() bool { return v.boolean }

// deprecatedFormatFlagHelp names the collision in the help itself, where an
// operator reads it, not only in the warning they see after already using it.
//
// NO BACKTICKS. pflag reads a backtick PAIR in a usage string as the flag's
// metavariable, so quoting the command names the way the rest of this file does
// rendered the flag as `--format audit export` instead of `--format string` —
// found by the sol-max contrast, in the one unit whose whole purpose was to stop
// the help misleading anyone.
const deprecatedFormatFlagHelp = "deprecated alias for -o/--output on this command (text or json) — " +
	"NOT the export-format flag of 'audit export' / 'findings export'"

func addDeprecatedFormatFlag(cmd *cobra.Command, persistent bool) {
	alias := &deprecatedOutputAlias{cmd: cmd, name: "format", value: "text"}
	if persistent {
		cmd.PersistentFlags().Var(alias, "format", deprecatedFormatFlagHelp)
	} else {
		cmd.Flags().Var(alias, "format", deprecatedFormatFlagHelp)
	}
	_ = cmd.RegisterFlagCompletionFunc("format", completeOutput)
}

func addDeprecatedJSONFlag(cmd *cobra.Command) {
	cmd.Flags().Var(&deprecatedOutputAlias{cmd: cmd, name: "json", boolean: true}, "json",
		"deprecated alias for -o json")
	cmd.Flags().Lookup("json").NoOptDefVal = "true"
}

func completeOutput(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	return []string{"text", "json"}, cobra.ShellCompDirectiveNoFileComp
}

// renderOut is the single text/JSON rendering switch for CLI commands. JSON is
// always indented deterministically from the same DTO or raw API value that the
// command used to render its human form.
func renderOut(cmd *cobra.Command, textFn func(io.Writer) error, jsonVal any) error {
	format, err := selectedOutput(cmd)
	if err != nil {
		return err
	}
	if format == "text" {
		return textFn(cmd.OutOrStdout())
	}
	body, err := json.MarshalIndent(jsonVal, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), string(body))
	return err
}

// renderListOut is renderOut for a LIST, and it exists because the bare form of
// that loop cannot say "nothing".
//
// Measured 2026-08-05: `agent session ls`, `agent workspace ls` and `agent
// workspace files` each rendered their text form as a naked `for` over the items,
// so on an empty list they emitted ZERO BYTES and exited 0. On a fresh install —
// where every list is empty — the operator cannot tell "no sessions" from "the
// command did nothing" from "the output got swallowed". Sibling commands in the
// same binary do say something, which is the tell that this was an oversight and
// not a house style.
//
// emptyNote is the command's own words for its empty case, because "no sessions
// observed yet" and "no workspaces registered" are different facts and a generic
// "(none)" would flatten them. JSON is untouched: an empty list is already `[]`
// there, and `[]` was never ambiguous.
func renderListOut[T any](cmd *cobra.Command, items []T, emptyNote string, row func(io.Writer, T) error, jsonVal any) error {
	return renderOut(cmd, func(out io.Writer) error {
		if len(items) == 0 {
			_, err := fmt.Fprintln(out, emptyNote)
			return err
		}
		for _, it := range items {
			if err := row(out, it); err != nil {
				return err
			}
		}
		return nil
	}, jsonVal)
}

// renderReportOut is renderStatusOut for a command whose OWN default has always
// been JSON — the machine-readable reports: `audit verify`, `keys status`,
// `license status`/`verify`, `dr restore`/`inspect`, `ddil verify`,
// `threatintel status`.
//
// The defect E2 fixes is that `-o text` did nothing on these: they printed
// JSON whatever the operator asked. The fix is to honor the flag — NOT to flip
// the default. `audit verify --strict` is a CI gate whose own failure messages
// say "see the JSON report above", and several of these are parsed by scripts;
// changing what they emit when nobody asked would be exactly the rug-pull this
// codebase refuses elsewhere. So: an explicit -o wins, and silence keeps JSON.
//
// The asymmetry is deliberate and it is only here. Commands whose default was
// already text (`sources ls`, `dr ls`, `eventing egress status`) go through
// renderOut and keep the global default.
func renderReportOut(cmd *cobra.Command, value any) error {
	if outputExplicitlySelected(cmd) {
		return renderStatusOut(cmd, value)
	}
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), string(body))
	return err
}

// outputExplicitlySelected reports whether the caller ASKED for a format, by any
// of the three spellings. Absence — including a command constructed without the
// root, as the constructor-level tests do — means "no preference", which for a
// report command is its own JSON default.
func outputExplicitlySelected(cmd *cobra.Command) bool {
	for _, name := range []string{"output", "format", "json"} {
		if f := cmd.Flags().Lookup(name); f != nil && f.Changed {
			return true
		}
	}
	return false
}

// renderStatusOut renders a status-shaped payload that has no bespoke human
// form: the SAME value as aligned `key: value` lines for text, and as indented
// JSON for json. A generic renderer is the honest choice for a flat report —
// hand-writing a second form is how the two drift apart.
func renderStatusOut(cmd *cobra.Command, value any) error {
	return renderOut(cmd, func(out io.Writer) error {
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		var decoded any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return err
		}
		tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
		writeStatusLines(tw, "", decoded)
		return tw.Flush()
	}, value)
}

// writeStatusLines flattens a decoded JSON value into `path\tvalue` lines,
// deterministically: maps are walked in sorted key order so two runs of the same
// command produce byte-identical text.
func writeStatusLines(w io.Writer, prefix string, value any) {
	switch v := value.(type) {
	case map[string]any:
		// An EMPTY map has to print a line, exactly as an empty slice does below.
		// Without this branch the loop runs zero times and the field VANISHES from
		// -o text while -o json still carries it: the two outputs of one command
		// disagree about which fields exist, and the text reader concludes the
		// engine never reported something it did report as empty. "{}" is the
		// answer, and it is not the same answer as "absent".
		if len(v) == 0 {
			fmt.Fprintf(w, "%s\t{}\n", prefix)
			return
		}
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			writeStatusLines(w, joinStatusPath(prefix, k), v[k])
		}
	case []any:
		if len(v) == 0 {
			fmt.Fprintf(w, "%s\t(none)\n", prefix)
			return
		}
		for i, item := range v {
			writeStatusLines(w, fmt.Sprintf("%s[%d]", prefix, i), item)
		}
	case nil:
		fmt.Fprintf(w, "%s\t-\n", prefix)
	default:
		fmt.Fprintf(w, "%s\t%v\n", prefix, v)
	}
}

func joinStatusPath(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

func selectedOutput(cmd *cobra.Command) (string, error) {
	if cmd.Flags().Lookup("output") != nil {
		return cmd.Flags().GetString("output")
	}
	// Constructor-level tests often execute a command group directly, without
	// newRootCmd. Honor its deprecated alias there as well.
	if flag := cmd.Flags().Lookup("format"); flag != nil && flag.Changed {
		return cmd.Flags().GetString("format")
	}
	if flag := cmd.Flags().Lookup("json"); flag != nil && flag.Changed {
		asJSON, err := cmd.Flags().GetBool("json")
		if err != nil {
			return "", err
		}
		if asJSON {
			return "json", nil
		}
	}
	return "text", nil
}
