// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"
)

// newCompletionCmd builds the `olivares completion` command group that generates
// shell autocompletion scripts for bash, zsh, fish and powershell. Each
// subcommand's Long description contains installation instructions.
func newCompletionCmd(root *cobra.Command) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "completion",
		Short: "Generate shell autocompletion scripts",
		Example: "  source <(olivares completion bash)\n" +
			"  source <(olivares completion zsh)\n" +
			"  olivares completion fish | source",
		Long: "Generate shell autocompletion scripts for olivares.\n\n" +
			"Run `olivares completion <shell> --help` for installation instructions\n" +
			"for your shell.",
	}
	cmd.AddCommand(
		completionBashCmd(root),
		completionZshCmd(root),
		completionFishCmd(root),
		completionPowershellCmd(root),
	)
	return cmd
}

// completionWantsHelp reports whether the raw args ask for help. The four shell
// leafs set DisableFlagParsing (the generated script must not be perturbed by
// flag machinery), which ALSO swallows --help: cobra never sees the flag, so
// `olivares completion bash --help` used to print the script instead of the
// installation instructions. The leafs detect the request manually.
func completionWantsHelp(args []string) bool {
	for _, a := range args {
		if a == "--help" || a == "-h" {
			return true
		}
	}
	return false
}

// completionShellArgs validates a shell leaf's raw args: nothing is accepted
// except the manually-handled help flags (DisableFlagParsing delivers flags as
// positional args, so plain cobra.NoArgs would reject --help with a confusing
// error).
func completionShellArgs(_ *cobra.Command, args []string) error {
	for _, a := range args {
		if a != "--help" && a != "-h" {
			return fmt.Errorf("unknown argument %q (this command takes none)", a)
		}
	}
	return nil
}

// ⛔ LOS CUATRO GENERADORES ESCRIBEN EN `cmd.OutOrStdout()`, NO EN `os.Stdout`, y no es estilo.
//
// Para el usuario no cambia NADA: cobra devuelve `os.Stdout` cuando nadie ha fijado otra salida,
// así que `olivares completion bash > /etc/bash_completion.d/olivares` hace exactamente lo mismo
// que antes. Lo que cambia es que la salida SE PUEDE CAPTURAR.
//
// Medido el 2026-08-25 al escribir el testigo que la matriz de release pedía: con `os.Stdout` el
// script salía por el stdout del BINARIO DE TEST —visible en el log, invisible para el test— y
// cualquier aserción sobre su contenido leía CERO bytes. Un verbo cuya salida no se puede capturar
// sólo admite testigos que comprueban que no revienta, y eso es exactamente la clase de testigo
// que promete más de lo que mira. El resto de esta CLI ya usa el escritor del comando; éste era la
// excepción, y la excepción es la que no se podía probar.
func completionBashCmd(root *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:   "bash",
		Short: "Generate bash autocompletion script",
		Long: "Generate the autocompletion script for bash.\n\n" +
			"To load completions in the current session:\n\n" +
			"  source <(olivares completion bash)\n\n" +
			"To install permanently (requires shell restart):\n\n" +
			"  # Linux:\n" +
			"  olivares completion bash > /etc/bash_completion.d/olivares\n\n" +
			"  # macOS (requires bash-completion@2):\n" +
			"  olivares completion bash > $(brew --prefix)/etc/bash_completion.d/olivares",
		Example: `  # Source completions in the current shell
  source <(olivares completion bash)

  # Install permanently on Linux
  olivares completion bash | sudo tee /etc/bash_completion.d/olivares >/dev/null`,
		Args:               completionShellArgs,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if completionWantsHelp(args) {
				return cmd.Help()
			}
			return root.GenBashCompletionV2(cmd.OutOrStdout(), true)
		},
	}
}

func completionZshCmd(root *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:   "zsh",
		Short: "Generate zsh autocompletion script",
		Long: "Generate the autocompletion script for zsh.\n\n" +
			"To load completions in the current session:\n\n" +
			"  source <(olivares completion zsh)\n\n" +
			"To install permanently (requires shell restart):\n\n" +
			"  olivares completion zsh > \"${fpath[1]}/_olivares\"\n\n" +
			"If shell completion is not already enabled, add to your ~/.zshrc:\n\n" +
			"  autoload -Uz compinit && compinit",
		Example: `  # Load completions in the current shell
  source <(olivares completion zsh)

  # Install into the first directory on fpath
  olivares completion zsh > "${fpath[1]}/_olivares"`,
		Args:               completionShellArgs,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if completionWantsHelp(args) {
				return cmd.Help()
			}
			return root.GenZshCompletion(cmd.OutOrStdout())
		},
	}
}

func completionFishCmd(root *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:   "fish",
		Short: "Generate fish autocompletion script",
		Long: "Generate the autocompletion script for fish.\n\n" +
			"To load completions in the current session:\n\n" +
			"  olivares completion fish | source\n\n" +
			"To install permanently (applied on next login):\n\n" +
			"  olivares completion fish > ~/.config/fish/completions/olivares.fish",
		Example: `  # Load completions in the current shell
  olivares completion fish | source

  # Install permanently
  olivares completion fish > ~/.config/fish/completions/olivares.fish`,
		Args:               completionShellArgs,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if completionWantsHelp(args) {
				return cmd.Help()
			}
			return root.GenFishCompletion(cmd.OutOrStdout(), true)
		},
	}
}

func completionPowershellCmd(root *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:   "powershell",
		Short: "Generate PowerShell autocompletion script",
		Long: "Generate the autocompletion script for PowerShell.\n\n" +
			"To load completions in the current session:\n\n" +
			"  olivares completion powershell | Out-String | Invoke-Expression\n\n" +
			"To install permanently, add the output to your PowerShell profile:\n\n" +
			"  olivares completion powershell >> $PROFILE",
		Example: `  # Load completions in the current PowerShell session
  olivares completion powershell | Out-String | Invoke-Expression

  # Install permanently
  olivares completion powershell >> $PROFILE`,
		Args:               completionShellArgs,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if completionWantsHelp(args) {
				return cmd.Help()
			}
			return root.GenPowerShellCompletionWithDesc(cmd.OutOrStdout())
		},
	}
}

// ---------------------------------------------------------------------------
// Dynamic completion helpers
// ---------------------------------------------------------------------------

// completeSessions queries the control plane for governed-session run refs to
// offer as positional-argument completions. It fails silently (empty list) when
// the env is not configured or the server is unreachable — tab-completion must
// never block or error-spam the operator. The agent commands take RUN REFS from
// the sessions module — /v1/agents items (agent ids) were never valid arguments
// here, so the old endpoint completed nothing useful.
func completeSessions(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	return completeFromAPI("/v1/m/sessions/runs", "run_ref", "name")
}

// completeWorkspaces queries the control plane for workspace names/IDs.
func completeWorkspaces(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	return completeFromAPI("/v1/workspaces", "name", "id")
}

// completeFromAPI is the shared HTTP fetch-and-parse for dynamic completions:
// it lists path and offers the given string fields of each item, deduplicated.
//
// It goes through cliTransport like every other network path (E4). It used
// to build its own http.Client with InsecureSkipVerify read straight from
// OLIVARES_INSECURE, which made this the worst of the four ad-hoc clients:
//
//   - It could not see --ca-cert or --pin-sha256, so an operator who had pinned
//     the control plane's certificate had that pin silently evaporate here.
//   - The opt-out was an environment variable, not a flag, so it appeared in no
//     help text and left no trace on the command line.
//   - It sent the OLIVARES_TOKEN bearer to real routes (/v1/m/sessions/runs,
//     /v1/workspaces) over that unverified channel.
//
// And it runs on the SHELL COMPLETION path — the code that fires on TAB, the
// most-executed and least-watched route in the whole binary.
//
// Two properties of completion are preserved deliberately: it stays silent (a
// warning written while the operator is mid-word would corrupt the prompt, so
// cliTransport's stderr goes to io.Discard) and it never blocks (3s, and any
// failure yields no completions rather than an error).
func completeFromAPI(path string, fields ...string) ([]string, cobra.ShellCompDirective) {
	resolved, err := resolveCLIConfig(cliResolutionOptions{})
	if err != nil || resolved.Server == "" || resolved.Token == "" {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	client, headers, err := cliTransport(cliTransportOptions{
		Resolved: resolved,
		// The env opt-in is kept so a self-signed dev plane still completes, but
		// it is now the ONLY thing it skips: the CA bundle and the SPKI pins from
		// the active context are honored on this path exactly as everywhere else.
		Insecure: os.Getenv("OLIVARES_INSECURE") == "1",
		Timeout:  3 * time.Second,
		Stderr:   io.Discard,
	})
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	req, err := http.NewRequest(http.MethodGet, resolved.Server+path, nil) //nolint:noctx // completion helper, no long-lived context
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	req.Header = headers.Clone()

	resp, err := client.Do(req)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	var comps []string
	seen := map[string]bool{}
	for _, item := range body.Items {
		for _, field := range fields {
			if v, _ := item[field].(string); v != "" && !seen[v] {
				comps = append(comps, v)
				seen[v] = true
			}
		}
	}
	return comps, cobra.ShellCompDirectiveNoFileComp
}
