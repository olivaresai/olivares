// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// The CLI reference and `commands -o json` share this dump builder. Keep the
// existing schema additive: consumers ignore fields they do not use.
package main

import (
	"sort"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

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
	ChangesData    string       `json:"changes_data"`
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
	return collectCLIRefDump(newRootCmd(), true)
}

// collectCLIRefDump is the single walker for the reference and installed CLI.
// The runtime surface omits hidden subtrees; the reference keeps the full tree.
func collectCLIRefDump(root *cobra.Command, includeHidden bool) cliRefDump {
	dump := cliRefDump{Schema: cliRefSchema, Root: root.Name()}
	for _, g := range root.Groups() {
		dump.Groups = append(dump.Groups, cliRefGroup{ID: g.ID, Title: g.Title})
	}

	var walk func(cmd *cobra.Command, depth int)
	walk = func(cmd *cobra.Command, depth int) {
		if cmd.Hidden && !includeHidden {
			return
		}
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
			ChangesData:    cmd.Annotations["changes_data"],
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
