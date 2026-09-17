// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall"
)

// cmd_agent_deploy.go is the one verb behind "deploy Claude Code" (D19): find the
// official CLI on this host, register the profile it runs under, and bind the
// credential it runs with.
//
// ⛔ IT COMPOSES, IT DOES NOT REIMPLEMENT. The three steps already exist and each
// one keeps its own contract and its own refusals:
//
//	`agent tool detect`        finds the executable and classifies what it found
//	`agent profile create`     registers the homes, validated on the node that owns them
//	`provider bind`            joins the credential to the profile
//
// A composite that re-derived any of them would be a fourth opinion about the same
// facts, and the first one to drift would be the one nobody tested.
//
// ⛔ AND IT DOES NOT INSTALL BY DEFAULT. Installing is a separate decision with a
// separate blast radius — it downloads, verifies a signature and writes to a root
// Olivares owns — so `deploy` REPORTS that the tool is missing and names the install
// command rather than running it. `--install` opts in and hands over to the same
// engine `agent tool install` uses, approval and all.

func newAgentDeployCmd() *cobra.Command {
	var (
		cfg                        agentClientConfig
		configHome, userHome, name string
		providerRef                string
		root                       string
		install                    bool
		yes                        bool
	)
	cmd := &cobra.Command{
		Use:   "deploy <claude|codex|grok|opencode>",
		Short: "Find an official CLI on this host and register the profile a session launches under",
		Long: "deploy is the short path from an installed CLI to a session that can launch: it detects\n" +
			"the driver's executable, registers a provider profile for the homes it runs under, and —\n" +
			"with --provider — binds the credential that profile's launches use.\n\n" +
			"It reports FOUR states, and they are not the same problem:\n" +
			"  not installed      no executable was found; the install command is named\n" +
			"  installed          an executable was found and classified (registered, observed, …)\n" +
			"  profile ready      a profile exists for these homes on this node\n" +
			"  launchable         the profile also names a credential\n\n" +
			"What it never does: run a vendor login, read or copy the provider's configuration or\n" +
			"credentials, create a missing home, or install anything unless you pass --install.",
		Example: "  olivares agent deploy claude\n" +
			"  olivares agent deploy claude --provider prv_01J8ABCDEF\n" +
			"  olivares agent deploy claude --install --yes",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			driver := strings.ToLower(strings.TrimSpace(args[0]))
			if driver == "" {
				return sessionCLIUsage("name the driver to deploy: claude, codex, grok or opencode")
			}
			homes, err := resolveDeployHomes(driver, configHome, userHome)
			if err != nil {
				return err
			}
			// 1. LOCAL: what is on this host. No server is needed for this half, which
			// is why it runs first — an operator with no control plane reachable still
			// gets the honest answer about their own machine.
			rootPath, err := resolveAgentToolRoot(root)
			if err != nil {
				return err
			}
			engine := toolInstallEngine(cmd.Context())
			found, detectErr := deployDetect(cmd, engine, driver, rootPath)
			if detectErr != nil && !install {
				// A driver the installer does not know is still deployable: its
				// executable may be on the PATH and its profile is ordinary. The
				// detection is advisory; it never decides the registration.
				found = nil
			}
			if len(found) == 0 && install {
				return exitcode.New(exitcode.Usage, fmt.Errorf(
					"--install is not available for driver %s in this build: install it with its own vendor mechanism, then run deploy again", driver))
			}

			// 2. SERVER: the profile, and the binding when one was named.
			if err := cfg.resolve(); err != nil {
				return err
			}
			body := map[string]any{
				"driver": driver, "config_home": homes.config, "user_home": homes.user,
				"display_name": deployProfileName(name, driver),
			}
			if providerRef != "" {
				body["auth_source"] = "managed_injection"
				body["provider_record_ref"] = providerRef
			}
			status, b, err := cfg.do(cmd.Context(), "POST", profilesPath, body)
			if err != nil {
				return err
			}
			if status != 201 {
				return httpErr(status, b)
			}
			rec := map[string]any{}
			if uerr := json.Unmarshal(b, &rec); uerr != nil {
				return uerr
			}
			return renderOut(cmd, func(w io.Writer) error {
				return printDeploy(w, driver, found, rec, homes)
			}, map[string]any{"driver": driver, "candidates": found, "profile": rec})
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().StringVar(&configHome, "config-home", "", "the CLI's configuration home (default: this driver's own under $HOME)")
	cmd.Flags().StringVar(&userHome, "user-home", "", "the child's user home (default: $HOME)")
	cmd.Flags().StringVar(&name, "name", "", "label for the profile (default: the driver's name)")
	cmd.Flags().StringVar(&providerRef, "provider", "", "registered provider to bind; without it the host's own credential variables decide")
	addAgentToolRootFlag(cmd, &root)
	cmd.Flags().BoolVar(&install, "install", false, "install the official CLI from its signed release when none is found")
	cmd.Flags().BoolVar(&yes, "yes", false, "with --install, approve the installation plan without a second prompt")
	_ = cmd.RegisterFlagCompletionFunc("provider", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return nil, cobra.ShellCompDirectiveNoFileComp
	})
	return cmd
}

// deployHomes is the pair a profile is identified by.
type deployHomes struct{ config, user string }

// driverConfigHome is each driver's own configuration home under a user's home. It is
// the vendor's documented default and nothing is guessed from it: an operator whose
// layout differs passes --config-home, and the SERVER refuses either one if it does
// not exist.
func driverConfigHome(driver, home string) string {
	switch driver {
	case "claude":
		return filepath.Join(home, ".claude")
	case "codex":
		return filepath.Join(home, ".codex")
	case "grok":
		return filepath.Join(home, ".grok")
	case "opencode":
		return filepath.Join(home, ".config", "opencode")
	}
	return ""
}

func resolveDeployHomes(driver, configHome, userHome string) (deployHomes, error) {
	out := deployHomes{config: strings.TrimSpace(configHome), user: strings.TrimSpace(userHome)}
	if out.config != "" && out.user != "" {
		return out, nil
	}
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(strings.TrimSpace(home)) {
		return deployHomes{}, exitcode.New(exitcode.Usage, fmt.Errorf(
			"this account has no usable home directory, so the profile's homes cannot be defaulted: pass --config-home and --user-home"))
	}
	home = filepath.Clean(strings.TrimSpace(home))
	if out.user == "" {
		out.user = home
	}
	if out.config == "" {
		out.config = driverConfigHome(driver, home)
	}
	if out.config == "" {
		return deployHomes{}, exitcode.New(exitcode.Usage, fmt.Errorf(
			"there is no default configuration home for driver %s: pass --config-home with the directory that CLI already uses", driver))
	}
	return out, nil
}

func deployProfileName(name, driver string) string {
	if n := strings.TrimSpace(name); n != "" {
		return n
	}
	return driver
}

// deployDetect reports the driver's executables without executing any of them.
// `deploy` is a registration step, and a registration step that ran somebody's binary
// would be the "diagnostic with effects" the product refuses elsewhere.
func deployDetect(cmd *cobra.Command, engine *toolinstall.Engine, driver, root string) ([]toolinstall.Candidate, error) {
	opts := toolinstall.DetectOptions{Driver: driver, Root: root, PathEnv: os.Getenv("PATH")}
	if home, err := os.UserHomeDir(); err == nil && filepath.IsAbs(strings.TrimSpace(home)) {
		opts.Home = filepath.Clean(strings.TrimSpace(home))
	}
	return engine.Detect(cmd.Context(), opts)
}

func printDeploy(w io.Writer, driver string, found []toolinstall.Candidate, profile map[string]any, homes deployHomes) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "DRIVER\t%s\n", driver)
	if len(found) == 0 {
		fmt.Fprintf(tw, "TOOL\tnot installed (no executable found on this host)\n")
	} else {
		fmt.Fprintf(tw, "TOOL\tinstalled: %s (%s)\n", found[0].Resolved, found[0].Match)
		if len(found) > 1 {
			fmt.Fprintf(tw, "\t%d more candidate(s); `olivares agent tool detect` lists them\n", len(found)-1)
		}
	}
	fmt.Fprintf(tw, "PROFILE\t%s\n", str(profile, "profile_ref"))
	fmt.Fprintf(tw, "CONFIG HOME\t%s\n", homes.config)
	fmt.Fprintf(tw, "USER HOME\t%s\n", homes.user)
	provider := str(profile, "provider_record_ref")
	if provider == "" {
		fmt.Fprintf(tw, "PROVIDER\tnone (the host's own credential variables decide)\n")
	} else {
		fmt.Fprintf(tw, "PROVIDER\t%s\n", provider)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	// The next action, decided by which of the four states this actually reached.
	switch {
	case len(found) == 0:
		fmt.Fprintf(w, "\nNext: install the official CLI, then run deploy again.\n")
		fmt.Fprintf(w, "      olivares agent tool install --driver %s --version latest --yes\n", driver)
	case provider == "":
		fmt.Fprintf(w, "\nNext: olivares provider bind <provider-ref> --profile %s\n", str(profile, "profile_ref"))
	default:
		fmt.Fprintf(w, "\nNext: olivares agent session create --provider-profile %s --workspace <workspace-ref>\n",
			str(profile, "profile_ref"))
	}
	return nil
}
