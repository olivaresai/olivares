// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/sdk/scaffold"
)

func newConnectorCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "connector",
		Short: "Scaffold out-of-tree connector projects",
		Example: "  olivares connector init acme.widget-audit \\\n" +
			"    --module github.com/acme/olivares-connector-widget-audit \\\n" +
			"    --template access-edge-source",
		Long: "Scaffold out-of-tree connector projects from the stable SDK archetypes.\n" +
			"The generated repository links only the Apache-2.0 SDK and can ship as a signed plugin.",
	}
	root.AddCommand(connectorInitCmd())
	return root
}

// connectorInitResult is what `connector init -o json` reports: the FACT the
// command produced, which is a repository on local disk.
//
// The two "next:" lines the text form prints are NOT in here, and that is the
// point of splitting them. They are advice to a human ("add the replace
// directive(s)", "cd there and run go test"), not a result, and a script that
// scaffolds twenty connectors has no use for twenty copies of a suggestion. What
// a script does need is the input the advice is CONDITIONED on, so SDKPath is a
// field: empty means the generated go.mod needs the replace directives added by
// hand, non-empty means they are already written. The advice is therefore
// derivable from the document, and it is still printed for the human — on stdout
// in text mode exactly as before, on stderr under -o json (commentaryOut).
type connectorInitResult struct {
	Name     string `json:"name"`
	Template string `json:"template"`
	Dir      string `json:"dir"`
	Module   string `json:"module"`
	SDKPath  string `json:"sdk_path"`
	Plugin   bool   `json:"plugin"`
}

func connectorInitCmd() *cobra.Command {
	var (
		dir        string
		module     string
		template   string
		withPlugin = true
		sdkPath    string
	)
	cmd := &cobra.Command{
		Use:   "init <name>",
		Short: "Generate a connector repository from an archetype template",
		Long: `Generate a complete, boundary-clean connector repository.

Templates:
  content-source      Serve documents and ACL refs to governed knowledge.
  access-edge-source  Observe who reached what and emit EdgeObservation facts.
  output-sink         Deliver engine notifications to an external system.
  agent-surface       Observe an agent runtime and emit edges plus findings.
  model-provider      Observe a model backend and emit cost plus usage edges.`,
		Example: `  olivares connector init acme.widget-audit \
    --module github.com/acme/olivares-connector-widget-audit \
    --template access-edge-source \
    --sdk-path ~/src/olivares/sdk`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			target := dir
			if target == "" {
				target = defaultConnectorInitDir(name)
			}
			opts := scaffold.Options{
				Dir:        target,
				Name:       name,
				Module:     module,
				Template:   template,
				WithPlugin: withPlugin,
				SDKPath:    sdkPath,
			}
			if err := scaffold.Generate(opts); err != nil {
				return fmt.Errorf("connector init: %w", err)
			}
			if err := renderOut(cmd, func(out io.Writer) error {
				_, werr := fmt.Fprintf(out, "generated %s connector %q in %s\n", template, name, target)
				return werr
			}, connectorInitResult{
				Name: name, Template: template, Dir: target,
				Module: module, SDKPath: sdkPath, Plugin: withPlugin,
			}); err != nil {
				return err
			}
			advice, err := commentaryOut(cmd)
			if err != nil {
				return err
			}
			if sdkPath == "" {
				fmt.Fprintln(advice, "next: add the SDK replace directive(s) from README.md, then run go test ./...")
			} else {
				fmt.Fprintln(advice, "next: cd "+target+" && go test ./... && ./scripts/check-boundary.sh")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "target directory (default ./<connector part>; non-empty dirs are refused)")
	cmd.Flags().StringVar(&module, "module", "", "Go module path of the generated repository")
	cmd.Flags().StringVar(&template, "template", "", "archetype template: content-source | access-edge-source | output-sink | agent-surface | model-provider")
	cmd.Flags().BoolVar(&withPlugin, "plugin", true, "emit cmd/<vendor-connector>/main.go and the sdk/plugin dependency")
	cmd.Flags().StringVar(&sdkPath, "sdk-path", "", "DEV: path to a local checkout of the upstream repo's sdk/ for replace directives")
	_ = cmd.MarkFlagRequired("module")
	_ = cmd.MarkFlagRequired("template")
	_ = cmd.RegisterFlagCompletionFunc("template", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return scaffold.Templates(), cobra.ShellCompDirectiveNoFileComp
	})
	return cmd
}

func defaultConnectorInitDir(name string) string {
	parts := strings.Split(name, ".")
	if len(parts) == 2 && strings.TrimSpace(parts[1]) != "" {
		return filepath.Join(".", parts[1])
	}
	return filepath.Join(".", name)
}
