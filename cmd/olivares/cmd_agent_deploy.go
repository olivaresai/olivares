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

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall"
)

// cmd_agent_deploy.go is the one verb behind "deploy Claude Code": find the
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
			// AUTH SOURCE IS DECLARED IN BOTH BRANCHES, and the branch without a
			// provider is the one that was missing.
			//
			// MEASURED 2026-09-18, walking the first hour on a clean machine:
			// `olivares agent deploy grok` created a profile, reported "PROFILE
			// ready" and exited 0 — and that profile could never launch. The
			// server refuses a launch whose profile declares no auth source
			// ("set auth_source to provider_account_home or managed_injection"),
			// the home is then OCCUPIED so `agent profile create` answers 409,
			// and there is no `agent profile update` or `rm`. The shortest
			// documented path to a first session ended in a state the CLI could
			// not leave.
			//
			// The value is not a new decision: this command's own help already
			// says what the no-provider case means — "the host's own credential
			// variables decide" — and that is exactly provider_account_home.
			// Leaving the field out did not keep the choice open; it made the
			// profile unusable and said nothing.
			body := map[string]any{
				"driver": driver, "config_home": homes.config, "user_home": homes.user,
				"display_name": deployProfileName(name, driver),
				"auth_source":  "provider_account_home",
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

// preferRegistered picks which candidate `deploy` reports as "installed".
//
// MEASURED 2026-09-18: after `olivares agent tool install --driver grok --root R`
// placed a verified release in R and recorded its receipt, `olivares agent deploy
// grok --root R` reported the operator's OWN ~/.grok binary — classified
// "unregistered-observed" — and hid the release it had just installed under
// "1 more candidate(s)". Detection returns PATH candidates first, and this
// command printed found[0].
//
// A REGISTERED release is one this product placed, whose bytes it verified
// against a signed manifest and whose receipt it wrote. An observed path is a
// file that happens to be there. When both exist, the answer to "which tool is
// installed" is the one the product can account for.
//
// Order is otherwise preserved: with no registered candidate, found[0] is still
// the answer, so a host that only has a PATH binary reads exactly as before.
func preferRegistered(found []toolinstall.Candidate) int {
	for i, c := range found {
		// The CONSTANT, not a substring: "unregistered-observed" contains
		// "registered", and a substring test here would have preferred exactly
		// the candidate this function exists to stop preferring. MatchDamaged and
		// MatchUnverified are deliberately not eligible either — a release whose
		// bytes did not verify is not the one to report as installed.
		if c.Match == toolinstall.MatchRegistered {
			return i
		}
	}
	return 0
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

func deployFields(driver string, found []toolinstall.Candidate, profile map[string]any, homes deployHomes) []termrender.Field {
	fields := []termrender.Field{{Key: "driver", Value: driver}}
	if len(found) == 0 {
		fields = append(fields, termrender.Field{
			Key:   "tool",
			Value: "not installed (no executable found on this host)",
			Role:  termrender.RoleWarn,
		})
	} else {
		pick := preferRegistered(found)
		tool := fmt.Sprintf("installed: %s (%s)", found[pick].Resolved, found[pick].Match)
		if len(found) > 1 {
			tool += fmt.Sprintf("; %d more candidate(s), 'olivares agent tool detect' lists them", len(found)-1)
		}
		fields = append(fields, termrender.Field{Key: "tool", Value: tool, Role: termrender.RoleOK})
	}
	provider := str(profile, "provider_record_ref")
	if provider == "" {
		provider = "none (the host's own credential variables decide)"
	}
	fields = append(fields,
		termrender.Field{Key: "profile", Value: str(profile, "profile_ref")},
		termrender.Field{Key: "config home", Value: homes.config},
		termrender.Field{Key: "user home", Value: homes.user},
		termrender.Field{Key: "provider", Value: provider},
	)
	return fields
}

func printDeploy(w io.Writer, driver string, found []toolinstall.Candidate, profile map[string]any, homes deployHomes) error {
	r := renderTo(w)
	r.Fields(deployFields(driver, found, profile, homes))
	// The next action, decided by which of the four states this actually reached.
	r.Blank()
	switch {
	case len(found) == 0:
		r.Next("olivares agent tool install --driver " + driver + " --version latest --yes")
		r.Line("      (install the official CLI, then run deploy again)")
	case str(profile, "provider_record_ref") == "":
		// This profile IS launchable: it authenticates from the driver's own
		// configuration home. Naming `provider bind` first said the opposite —
		// that something was still missing — when the operator's next real step
		// is the session. Binding a registered credential stays available and is
		// named second, as the option it is.
		r.Next("olivares agent session create --provider-profile " + str(profile, "profile_ref") + " --workspace <workspace-ref>")
		r.Line("      (to launch with a credential this product owns instead of the driver's own:")
		r.Line("       olivares provider bind <provider-ref> --profile " + str(profile, "profile_ref") + ")")
	default:
		r.Next("olivares agent session create --provider-profile " + str(profile, "profile_ref") + " --workspace <workspace-ref>")
	}
	return nil
}
