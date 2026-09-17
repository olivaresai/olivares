// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// cmd_agent_profile.go closes a measured gap: the provider-profile plane has had a
// full HTTP surface and a console screen since B1, and NO CLI verb. `agent session
// create` requires a profile reference, and the only ways to obtain one were the
// console or curl.
//
// These are thin HTTP clients against /v1/m/sessions/provider-profiles*. Nothing
// here validates a home: the SERVER canonicalises and validates the paths, on the
// node that owns them, and refuses one that does not already exist. A CLI that
// pre-checked would be checking the wrong machine.

const profilesPath = "/v1/m/sessions/provider-profiles"

func newAgentProfileCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Register and inspect provider profiles (which home an official CLI runs under)",
		Long: "profile is the identity a session launches under: a driver, this node's execution\n" +
			"environment, and the two home directories the official CLI reads — its configuration\n" +
			"home and the child's user home.\n\n" +
			"A profile is NOT a credential. `olivares provider` registers the credential and\n" +
			"`olivares provider bind` joins the two.\n\n" +
			"The homes must already exist on the machine that runs the control plane. The server\n" +
			"validates them there and never creates a missing one: an empty fallback home would\n" +
			"give a session a fresh provider identity nobody configured.",
		Example: "  olivares agent profile ls\n" +
			"  olivares agent profile create --driver claude --config-home ~/.claude --user-home ~ --name \"Claude (work)\"\n" +
			"  olivares agent profile get ppf_01J8ABCDEF -o json",
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(newAgentProfileListCmd(), newAgentProfileGetCmd(), newAgentProfileCreateCmd())
	return cmd
}

func newAgentProfileListCmd() *cobra.Command {
	var (
		cfg   agentClientConfig
		state string
	)
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List the provider profiles this tenant has registered",
		Long: "ls shows each profile's reference, driver, state and bound provider. It shows no\n" +
			"path: the homes are an authorized read of their own, so a list an operator leaves on\n" +
			"screen does not publish the filesystem layout of the control-plane host.",
		Example: "  olivares agent profile ls\n  olivares agent profile ls --state active -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			path := profilesPath
			if s := strings.TrimSpace(state); s != "" {
				path += "?state=" + s
			}
			status, b, err := cfg.do(cmd.Context(), "GET", path, nil)
			if err != nil {
				return err
			}
			if status != 200 {
				return httpErr(status, b)
			}
			var page struct {
				Items []map[string]any `json:"items"`
			}
			if uerr := json.Unmarshal(b, &page); uerr != nil {
				return uerr
			}
			return renderOut(cmd, func(w io.Writer) error {
				return printProfileTable(w, page.Items)
			}, page.Items)
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().StringVar(&state, "state", "", "only profiles in this state (active, disabled or retired)")
	addDeprecatedJSONFlag(cmd)
	return cmd
}

func newAgentProfileGetCmd() *cobra.Command {
	var cfg agentClientConfig
	cmd := &cobra.Command{
		Use:     "get <profile-ref>",
		Short:   "Show one provider profile",
		Long:    "get prints one profile's driver, environment, state, authorized source and bound provider.",
		Example: "  olivares agent profile get ppf_01J8ABCDEF -o json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			status, b, err := cfg.do(cmd.Context(), "GET", profilesPath+"/"+args[0], nil)
			if err != nil {
				return err
			}
			if status != 200 {
				return httpErr(status, b)
			}
			var rec map[string]any
			if uerr := json.Unmarshal(b, &rec); uerr != nil {
				return uerr
			}
			return renderOut(cmd, func(w io.Writer) error {
				return printProfileRecord(w, rec)
			}, rec)
		},
	}
	cfg.addFlags(cmd)
	addDeprecatedJSONFlag(cmd)
	return cmd
}

func newAgentProfileCreateCmd() *cobra.Command {
	var (
		cfg                                            agentClientConfig
		driver, configHome, userHome, name, authSource string
		providerRef                                    string
		errNoProfile                                   = "the control plane did not return a provider profile"
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Register a provider profile for homes that already exist on this node",
		Long: "create registers one profile. The server canonicalises both paths on the node that\n" +
			"owns them — absolute, symlinks resolved, existing, a directory — and refuses anything\n" +
			"else. It creates nothing, installs nothing and logs in to nothing.\n\n" +
			"--auth-source decides where the child's provider identity comes from:\n" +
			"  provider_account_home   the login already saved inside the profile's own homes;\n" +
			"                          Olivares injects nothing and never reads that file\n" +
			"  managed_injection       a credential the engine supplies — with --provider, the\n" +
			"                          registered provider named there\n\n" +
			"With --provider the profile is bound in the same call, so deploying an agent is one\n" +
			"step. The engine refuses a credential this driver cannot read.",
		Example: "  olivares agent profile create --driver claude --config-home /home/ops/.claude --user-home /home/ops --name \"Claude (work)\"\n" +
			"  olivares agent profile create --driver claude --config-home /home/ops/.claude --user-home /home/ops \\\n" +
			"      --name \"Claude (prod)\" --auth-source managed_injection --provider prv_01J8ABCDEF",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			if providerRef != "" && authSource == "" {
				// Naming a provider and not authorizing managed injection would register a
				// profile that carries a credential it is not allowed to use. Refusing is
				// better than choosing for the operator: the flag is one word away.
				return sessionCLIUsage("--provider needs --auth-source managed_injection: a bound credential is only used by a profile authorized to receive one")
			}
			body := map[string]any{
				"driver": driver, "config_home": configHome, "user_home": userHome,
				"display_name": name, "auth_source": authSource,
			}
			if providerRef != "" {
				body["provider_record_ref"] = providerRef
			}
			status, b, err := cfg.do(cmd.Context(), "POST", profilesPath, body)
			if err != nil {
				return err
			}
			if status != 201 {
				return httpErr(status, b)
			}
			var rec map[string]any
			if uerr := json.Unmarshal(b, &rec); uerr != nil {
				return fmt.Errorf("%s: %w", errNoProfile, uerr)
			}
			return renderOut(cmd, func(w io.Writer) error {
				return printProfileRecord(w, rec)
			}, rec)
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().StringVar(&driver, "driver", "", "official CLI this profile launches: claude, codex, grok or opencode")
	cmd.Flags().StringVar(&configHome, "config-home", "", "absolute path of the CLI's configuration home on the control-plane host")
	cmd.Flags().StringVar(&userHome, "user-home", "", "absolute path of the child's user home on the control-plane host")
	cmd.Flags().StringVar(&name, "name", "", "your own label for this profile")
	cmd.Flags().StringVar(&authSource, "auth-source", "",
		"provider_account_home (the login saved in the homes) or managed_injection (a credential the engine supplies)")
	cmd.Flags().StringVar(&providerRef, "provider", "", "registered provider reference to bind in the same call (needs --auth-source managed_injection)")
	_ = cmd.MarkFlagRequired("driver")
	_ = cmd.MarkFlagRequired("config-home")
	_ = cmd.MarkFlagRequired("user-home")
	_ = cmd.RegisterFlagCompletionFunc("driver", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{
			"claude\tClaude Code",
			"codex\tOpenAI Codex CLI",
			"grok\tGrok CLI",
			"opencode\tOpenCode (ACP)",
		}, cobra.ShellCompDirectiveNoFileComp
	})
	_ = cmd.RegisterFlagCompletionFunc("auth-source", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{
			"provider_account_home\tthe login already saved in this profile's homes; nothing is injected",
			"managed_injection\ta credential the engine supplies (bind one with --provider)",
		}, cobra.ShellCompDirectiveNoFileComp
	})
	addDeprecatedJSONFlag(cmd)
	return cmd
}

func printProfileTable(w io.Writer, items []map[string]any) error {
	if len(items) == 0 {
		fmt.Fprintln(w, "No provider profiles registered.")
		fmt.Fprintln(w, "Next: olivares agent profile create --driver claude --config-home <dir> --user-home <dir> --name \"Claude\"")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PROFILE\tNAME\tDRIVER\tSTATE\tAUTH SOURCE\tPROVIDER")
	for _, rec := range items {
		provider := str(rec, "provider_record_ref")
		if provider == "" {
			provider = "-"
		}
		source := str(rec, "auth_source")
		if source == "" {
			source = "none authorized"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			str(rec, "profile_ref"), str(rec, "display_name"), str(rec, "driver"),
			str(rec, "state"), source, provider)
	}
	return tw.Flush()
}

func printProfileRecord(w io.Writer, rec map[string]any) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "PROFILE\t%s\n", str(rec, "profile_ref"))
	fmt.Fprintf(tw, "NAME\t%s\n", str(rec, "display_name"))
	fmt.Fprintf(tw, "DRIVER\t%s\n", str(rec, "driver"))
	fmt.Fprintf(tw, "ENVIRONMENT\t%s\n", str(rec, "environment_ref"))
	fmt.Fprintf(tw, "STATE\t%s\n", str(rec, "state"))
	source := str(rec, "auth_source")
	if source == "" {
		source = "none authorized (a driver that needs one refuses the launch)"
	}
	fmt.Fprintf(tw, "AUTH SOURCE\t%s\n", source)
	provider := str(rec, "provider_record_ref")
	if provider == "" {
		provider = "none (the host's own credential variables decide)"
	}
	fmt.Fprintf(tw, "PROVIDER\t%s\n", provider)
	if err := tw.Flush(); err != nil {
		return err
	}
	ref := str(rec, "profile_ref")
	switch {
	case str(rec, "state") != "active":
		// Nothing to suggest: the next action is a lifecycle decision, not a step.
	case str(rec, "provider_record_ref") == "" && str(rec, "auth_source") == "managed_injection":
		fmt.Fprintf(w, "\nNext: olivares provider bind <provider-ref> --profile %s\n", ref)
	default:
		fmt.Fprintf(w, "\nNext: olivares agent session create --provider-profile %s --workspace <workspace-ref>\n", ref)
	}
	return nil
}
