// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
)

// cmd_agent_profile.go closes a measured gap: the provider-profile plane has had a
// full HTTP surface and a console screen since its first release, and NO CLI verb. `agent session
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
			"  olivares agent profile update ppf_01J8ABCDEF --tools Read,Grep,Edit\n" +
			"  olivares agent profile get ppf_01J8ABCDEF -o json\n" +
			"  olivares agent profile rm ppf_01J8ABCDEF --yes",
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(
		newAgentProfileListCmd(), newAgentProfileGetCmd(), newAgentProfileCreateCmd(),
		newAgentProfileUpdateCmd(), newAgentProfileRemoveCmd(),
	)
	return cmd
}

func newAgentProfileUpdateCmd() *cobra.Command {
	var (
		cfg                         agentClientConfig
		name, state, authSource     string
		providerRef, permissionMode string
		tools                       []string
		unbind                      bool
	)
	cmd := &cobra.Command{
		Use:   "update <profile-ref>",
		Short: "Change a profile's label, state, authorization or session policy",
		Long: "update changes what a profile AUTHORIZES, never what it IS. The driver, the\n" +
			"execution environment and the two homes are the profile's identity and are\n" +
			"immutable: changing one of those is creating another profile.\n\n" +
			"Everything else is repairable in place — the label, active/disabled, the\n" +
			"authorized authentication source, the bound provider, and the session policy\n" +
			"(--tools and --permission-mode). A running child keeps what its own launch\n" +
			"resolved; the change governs the next launch and the next resume.\n\n" +
			"Use `agent profile rm` to retire a profile for good.",
		Example: "  olivares agent profile update ppf_01J8ABCDEF --name \"Claude (prod)\"\n" +
			"  olivares agent profile update ppf_01J8ABCDEF --tools Read,Grep,Edit\n" +
			"  olivares agent profile update ppf_01J8ABCDEF --tools \"\"   # declare NO tools\n" +
			"  olivares agent profile update ppf_01J8ABCDEF --state disabled\n" +
			"  olivares agent profile update ppf_01J8ABCDEF --unbind-provider",
		Args: exactRef("profile-ref"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			body := map[string]any{}
			// Each field travels only when the operator named it: a PATCH that sent
			// every flag would withdraw an authorization nobody asked to withdraw.
			if cmd.Flags().Changed("name") {
				body["display_name"] = name
			}
			if cmd.Flags().Changed("state") {
				switch state {
				case "active", "disabled":
				case "retired":
					return sessionCLIUsage("retiring is irreversible and has its own verb: olivares agent profile rm")
				default:
					return sessionCLIUsage("--state must be active or disabled")
				}
				body["state"] = state
			}
			if cmd.Flags().Changed("auth-source") {
				body["auth_source"] = authSource
			}
			switch {
			case unbind && cmd.Flags().Changed("provider"):
				return sessionCLIUsage("--unbind-provider and --provider ask for opposite things")
			case unbind:
				body["provider_record_ref"] = ""
			case cmd.Flags().Changed("provider"):
				body["provider_record_ref"] = providerRef
			}
			if cmd.Flags().Changed("tools") {
				body["session_tools"] = tools
			}
			if cmd.Flags().Changed("permission-mode") {
				body["session_permission_mode"] = permissionMode
			}
			if len(body) == 0 {
				return sessionCLIUsage("name what to change: --name, --state, --auth-source, --provider, --unbind-provider, --tools or --permission-mode")
			}
			status, b, err := cfg.do(cmd.Context(), "PATCH", profilesPath+"/"+args[0], body)
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
	cmd.Flags().StringVar(&name, "name", "", "your own label for this profile")
	cmd.Flags().StringVar(&state, "state", "", "active or disabled (a disabled profile refuses new launches; running children keep theirs)")
	cmd.Flags().StringVar(&authSource, "auth-source", "",
		"provider_account_home, managed_injection, or \"\" to withdraw the authorization")
	cmd.Flags().StringVar(&providerRef, "provider", "", "registered provider reference this profile's managed launches use")
	cmd.Flags().BoolVar(&unbind, "unbind-provider", false, "clear the bound provider and return to the host-wide credential")
	cmd.Flags().StringSliceVar(&tools, "tools", nil,
		"the built-in tools sessions under this profile may use (repeatable or comma-separated; --tools \"\" declares NONE)")
	cmd.Flags().StringVar(&permissionMode, "permission-mode", "",
		"permission mode those sessions run under, or \"\" to withdraw the declaration")
	_ = cmd.RegisterFlagCompletionFunc("state", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{
			"active\taccepts launches, resumes and bindings",
			"disabled\trefuses NEW launches; reversible",
		}, cobra.ShellCompDirectiveNoFileComp
	})
	_ = cmd.RegisterFlagCompletionFunc("permission-mode", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return permissionModeChoices, cobra.ShellCompDirectiveNoFileComp
	})
	addDeprecatedJSONFlag(cmd)
	return cmd
}

func newAgentProfileRemoveCmd() *cobra.Command {
	var (
		cfg agentClientConfig
		yes bool
	)
	cmd := &cobra.Command{
		Use:     "rm <profile-ref>",
		Aliases: []string{"retire"},
		Short:   "Retire a provider profile for good",
		Long: "rm retires a profile. It is irreversible for that reference: the id keeps its\n" +
			"history and its sessions, and the HOME becomes free for a new profile id.\n\n" +
			"Nothing else is rewritten — the runs, aliases and ledger entries that name this\n" +
			"profile keep naming it, which is what makes them still readable. To stop new\n" +
			"launches without losing the id, use `agent profile update --state disabled`.",
		Example: "  olivares agent profile rm ppf_01J8ABCDEF --yes",
		Args:    exactRef("profile-ref"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				return sessionCLIUsage("retiring a profile cannot be undone for that reference: pass --yes (or disable it instead with `agent profile update --state disabled`)")
			}
			if err := cfg.resolve(); err != nil {
				return err
			}
			status, b, err := cfg.do(cmd.Context(), "POST", profilesPath+"/"+args[0]+"/retire", nil)
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
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm the irreversible retirement")
	addDeprecatedJSONFlag(cmd)
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
		tools                                          []string
		permissionMode                                 string
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
			"step. The engine refuses a credential this driver cannot read.\n\n" +
			"--tools declares what the agent may USE. It is deny-closed: a profile that declares\n" +
			"no tools launches its sessions with no built-in tools at all, because the product\n" +
			"governs which profile launches and must not then hand the child everything. Declare\n" +
			"the surface you want (--tools Read,Grep,Edit) or declare none out loud (--tools \"\").\n" +
			"A driver that negotiates its tools in its own protocol (codex, grok, opencode)\n" +
			"refuses the flag rather than storing a policy nobody would apply.",
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
			// The DECLARATION travels only when the operator made one: a null field
			// means "nothing declared", which the engine reads as deny-closed, and an
			// empty array means "no tools" out loud. Sending [] for an unset flag would
			// erase that difference at the only place it can still be told.
			if cmd.Flags().Changed("tools") {
				body["session_tools"] = tools
			}
			if permissionMode != "" {
				body["session_permission_mode"] = permissionMode
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
	cmd.Flags().StringSliceVar(&tools, "tools", nil,
		"the built-in tools sessions under this profile may use (repeatable or comma-separated; --tools \"\" declares NONE). "+
			"Undeclared is deny-closed: the child launches with no built-in tools")
	cmd.Flags().StringVar(&permissionMode, "permission-mode", "",
		"permission mode those sessions run under: default | acceptEdits | plan | auto | dontAsk | bypassPermissions")
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
	_ = cmd.RegisterFlagCompletionFunc("permission-mode", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return permissionModeChoices, cobra.ShellCompDirectiveNoFileComp
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

func profileTable(items []map[string]any) termrender.Table {
	t := termrender.Table{
		Header: []string{"profile", "name", "driver", "state", "auth source", "provider"},
		Empty:  "No provider profiles registered.",
	}
	for _, rec := range items {
		provider := str(rec, "provider_record_ref")
		if provider == "" {
			provider = "-"
		}
		source := str(rec, "auth_source")
		if source == "" {
			source = "none authorized"
		}
		t.Rows = append(t.Rows, []string{
			str(rec, "profile_ref"), str(rec, "display_name"), str(rec, "driver"),
			str(rec, "state"), source, provider,
		})
	}
	return t
}

func printProfileTable(w io.Writer, items []map[string]any) error {
	r := renderTo(w)
	r.Table(profileTable(items))
	if len(items) == 0 {
		r.Next("olivares agent profile create --driver claude --config-home <dir> --user-home <dir> --name \"Claude\"")
	}
	return nil
}

func profileRecordFields(rec map[string]any) []termrender.Field {
	source := str(rec, "auth_source")
	if source == "" {
		source = "none authorized (a driver that needs one refuses the launch)"
	}
	provider := str(rec, "provider_record_ref")
	if provider == "" {
		provider = "none (the host's own credential variables decide)"
	}
	mode := str(rec, "session_permission_mode")
	if mode == "" {
		mode = "not declared (each launch keeps its own)"
	}
	return []termrender.Field{
		{Key: "profile", Value: str(rec, "profile_ref")},
		{Key: "name", Value: str(rec, "display_name")},
		{Key: "driver", Value: str(rec, "driver")},
		{Key: "environment", Value: str(rec, "environment_ref")},
		{Key: "state", Value: str(rec, "state")},
		{Key: "auth source", Value: source},
		{Key: "provider", Value: provider},
		{Key: "tools", Value: profileToolsSentence(rec)},
		{Key: "permission mode", Value: mode},
	}
}

func printProfileRecord(w io.Writer, rec map[string]any) error {
	r := renderTo(w)
	r.Fields(profileRecordFields(rec))
	ref := str(rec, "profile_ref")
	switch {
	case str(rec, "state") != "active":
		// Nothing to suggest: the next action is a lifecycle decision, not a step.
	case str(rec, "provider_record_ref") == "" && str(rec, "auth_source") == "managed_injection":
		r.Blank()
		r.Next("olivares provider bind <provider-ref> --profile " + ref)
	default:
		r.Blank()
		r.Next("olivares agent session create --provider-profile " + ref + " --workspace <workspace-ref>")
	}
	return nil
}

// permissionModeChoices is the completion for the declared session mode, with
// what each one means, so the choice does not need a docs round trip.
var permissionModeChoices = []string{
	"default\task before anything that is not already allowed",
	"acceptEdits\tauto-approve file edits, ask for the rest",
	"plan\tplan only: no edits, no commands",
	"auto\tthe CLI's own automatic classifier decides",
	"dontAsk\tnever ask; only what is explicitly allowed runs",
	"bypassPermissions\tapprove everything (the container is the only frontier)",
}

// profileToolsSentence says which of the three states this profile's tool
// declaration is in. The difference between "declared none" and "declared
// nothing" is invisible in the argv and decisive for the operator: one is a
// policy, the other is a gap the engine closes deny-closed.
func profileToolsSentence(rec map[string]any) string {
	declared, _ := rec["session_tools_declared"].(bool)
	if !declared {
		return "not declared — sessions launch with NO built-in tools (deny-closed); declare them with `agent profile update --tools`"
	}
	raw, _ := rec["session_tools"].([]any)
	names := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			names = append(names, s)
		}
	}
	if len(names) == 0 {
		return "declared NONE — sessions launch with no built-in tools"
	}
	return strings.Join(names, ", ")
}
