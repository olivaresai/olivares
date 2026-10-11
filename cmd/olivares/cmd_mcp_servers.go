// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
	"github.com/olivaresai/olivares/core/auth"
)

// cmd_mcp_servers.go is `olivares mcp add|test|enable|disable|rm|ls|sessions`: the
// MCP servers sessions can use, through the roster the console's MCP page edits
// (/v1/console/mcp-gateway). A server is added disabled; a test lists its tools
// without calling them; enabling it is a separate, explicit step. Every write
// carries the roster version it read, so two editors cannot overwrite each other.
//
// A local server is a command (`mcp add files -- npx -y @modelcontextprotocol/server-
// filesystem .`), a remote one an HTTPS URL. Secrets never go in the command line:
// --secret-env NAME=store:mcp/<name> names a secret `mcp secret` stored in this
// organization's secrets.

const (
	mcpGatewayPath = "/v1/console/mcp-gateway"
	// mcpSecretsPath is the organization's own secrets, the only store an MCP
	// server's store:mcp/<name> resolves from (the deployment-wide store that
	// `olivares secrets` writes is never read for it).
	mcpSecretsPath = "/v1/console/secrets?scope=tenant"
)

type mcpSnapshot struct {
	Version      int64            `json:"version"`
	ReadOnly     bool             `json:"read_only"`
	SessionTools bool             `json:"session_tools"`
	Servers      []map[string]any `json:"servers"`
}

func newMCPServerCmds(flags *authClientFlags) []*cobra.Command {
	return []*cobra.Command{
		newMCPListCmd(flags), newMCPAddCmd(flags), newMCPSecretCmd(flags), newMCPTestCmd(flags),
		newMCPEnableCmd(flags, "enable", true), newMCPEnableCmd(flags, "disable", false),
		newMCPRemoveCmd(flags), newMCPSessionsCmd(flags),
	}
}

// mcpClient resolves the `mcp` group's persistent server/credential flags into the
// session client this file uses.
func mcpClient(cmd *cobra.Command, flags *authClientFlags) (agentClientConfig, error) {
	resolved, err := flags.resolve(cmd)
	if err != nil {
		return agentClientConfig{}, err
	}
	switch {
	case resolved.Server == "":
		return agentClientConfig{}, missingCLIValueError("server", "--server", "OLIVARES_SERVER_URL", resolved)
	case resolved.Token == "":
		return agentClientConfig{}, missingCLIValueError("token", "--token", "OLIVARES_TOKEN", resolved)
	case resolved.Tenant == "":
		return agentClientConfig{}, missingCLIValueError("tenant", "--tenant", "OLIVARES_TENANT", resolved)
	}
	return agentClientConfig{resolved: resolved, server: resolved.Server, token: resolved.Token, tenant: resolved.Tenant,
		insecure: flags.insecure, timeout: flags.timeout}, nil
}

func newMCPListCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List the MCP servers, whether each is on, and the tools its last test found",
		Long: "ls prints one row per MCP server: its name, on or off, how it runs (a command or a URL)\n" +
			"and the tools its last test listed. It says so when sessions do not use MCP servers yet.\n" +
			"Use it to see what sessions can reach before you start one.",
		Example: "  olivares mcp ls\n  olivares mcp ls -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := mcpClient(cmd, flags)
			if err != nil {
				return err
			}
			snap, err := cfg.mcpSnapshot(cmd.Context())
			if err != nil {
				return err
			}
			table := termrender.Table{
				Header: []string{"name", "state", "runs", "tools"},
				Empty:  "No MCP servers yet. Add one: olivares mcp add <name> -- <command> [args]",
			}
			for _, s := range snap.Servers {
				state, role := "off", termrender.RoleMuted
				if s["enabled"] == true {
					state, role = "on", termrender.RoleOK
					if len(mcpAllowedTools(s)) == 0 {
						// On with nothing callable is not usable; say so (HU 025).
						state, role = "on, no tool allowed", termrender.RoleWarn
					}
				}
				table.Rows = append(table.Rows, []string{termSafe(str(s, "name")), state, mcpTarget(s), mcpToolsCell(s)})
				table.Roles = append(table.Roles, []termrender.Role{termrender.RoleNone, role})
			}
			return renderOut(cmd, func(w io.Writer) error {
				r := renderTo(w)
				r.Table(table)
				if len(snap.Servers) > 0 && !snap.SessionTools {
					r.Line("Sessions do not use MCP servers yet. Turn it on: olivares mcp sessions on")
				}
				return nil
			}, snap)
		},
	}
	return cmd
}

func newMCPAddCmd(flags *authClientFlags) *cobra.Command {
	var (
		envs, secrets []string
		noTest        bool
	)
	cmd := &cobra.Command{
		Use:   "add <name> (-- <command> [args...] | <https-url>)",
		Short: "Add an MCP server (off until you enable it) and list its tools",
		Long: "add registers a local MCP server (a command the engine runs next to each session) or a\n" +
			"remote one (an HTTPS URL). It is added off; add then tests it, which lists its tools\n" +
			"without calling any. Turn it on with `olivares mcp enable <name>`.\n\n" +
			"Secrets never go in the command line: store each one with\n" +
			"`olivares mcp secret <name> --value-file <file>` and pass --secret-env NAME=store:mcp/<name>.",
		Example: "  olivares mcp add files -- npx -y @modelcontextprotocol/server-filesystem .\n" +
			"  olivares mcp secret github --value-file ./github-token\n" +
			"  olivares mcp add github --secret-env GITHUB_TOKEN=store:mcp/github -- github-mcp-server stdio\n" +
			"  olivares mcp add docs https://mcp.example.com/mcp",
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			server := map[string]any{
				"name": args[0], "egress_cidrs": []string{}, "allowed_tools": []any{},
				"trust": map[string]any{}, "enabled": false,
			}
			switch dash := cmd.ArgsLenAtDash(); {
			case dash == 1:
				server["command"], server["args"] = args[1], append([]string{}, args[2:]...)
				warnEnvFlagsAfterTerminator(cmd, args[1:])
			case dash < 0 && len(args) == 2 && strings.HasPrefix(args[1], "https://"):
				server["url"] = args[1]
			default:
				return sentence(exitcode.Usage, "Give a command after -- or an HTTPS URL: olivares mcp add %s -- <command> [args]", shellWord(args[0]))
			}
			if len(envs)+len(secrets) > 0 {
				if _, local := server["command"]; !local {
					return sentence(exitcode.Usage, "--env and --secret-env are for a local server (a command after --).")
				}
				env, err := keyValues(envs, "--env")
				if err != nil {
					return err
				}
				refs, err := keyValues(secrets, "--secret-env")
				if err != nil {
					return err
				}
				server["env"], server["env_secret_refs"] = env, refs
			}
			cfg, err := mcpClient(cmd, flags)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			snap, err := cfg.mcpSnapshot(ctx)
			if err != nil {
				return err
			}
			if _, found := mcpFind(snap, args[0]); found {
				return sentence(exitcode.Conflict, "An MCP server named %q exists. See it: olivares mcp ls", args[0])
			}
			snap, err = cfg.mcpWrite(ctx, http.MethodPost, mcpGatewayPath+"/servers", map[string]any{"version": snap.Version, "server": server})
			if err != nil && len(secrets) > 0 && exitcode.From(err) == exitcode.NotFound {
				// The engine names the reference it could not find in this organization's
				// secrets; the CLI names the command that stores it there.
				return sentence(exitcode.NotFound, "%w Store it: olivares mcp secret <name> --value-file <file>", err)
			}
			if err != nil {
				return err
			}
			added, _ := mcpFind(snap, args[0])
			if noTest || outputIsJSON(cmd) {
				return renderOut(cmd, func(w io.Writer) error {
					_, err := fmt.Fprintf(w, "Added %s (off). Test it: olivares mcp test %s\n", termSafe(args[0]), shellWord(args[0]))
					return err
				}, added)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Added %s (off). Testing it…\n", termSafe(args[0]))
			return cfg.mcpTestAndShow(cmd, snap, added)
		},
	}
	cmd.Flags().StringArrayVar(&envs, "env", nil, "NAME=value for a local server's environment (public values only; repeatable)")
	cmd.Flags().StringArrayVar(&secrets, "secret-env", nil, "NAME=store:mcp/<secret> for a local server's secret environment (repeatable)")
	cmd.Flags().BoolVar(&noTest, "no-test", false, "add without testing it")
	return cmd
}

// warnEnvFlagsAfterTerminator names the trap #572 reported: an --env or
// --secret-env token written after the -- terminator is the wrapped command or
// one of its arguments, so the environment (and the secret) is never wired and
// the engine sees the reference where it expects public values. The wrapped
// command may own such a flag itself, so this stays a warning; the server is
// added as given.
func warnEnvFlagsAfterTerminator(cmd *cobra.Command, serverArgs []string) {
	for _, a := range serverArgs {
		name, ok := strings.CutPrefix(a, "--")
		if !ok || name == "" {
			continue
		}
		name = strings.SplitN(name, "=", 2)[0]
		if name != "env" && name != "secret-env" {
			continue
		}
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
			"WARNING: --%s after -- is an argument of the server command, not an olivares flag; move it before the -- so it takes effect\n", name)
	}
}

// newMCPSecretCmd stores a credential where `mcp add --secret-env` finds it: this
// organization's secrets, through the same authorized route the console's MCP page
// uses (an organization admin, with step-up when the deployment asks for it).
func newMCPSecretCmd(flags *authClientFlags) *cobra.Command {
	var valueFile, description string
	cmd := &cobra.Command{
		Use:   "secret <name> --value-file <file>",
		Short: "Store a credential for MCP servers, referenced as store:mcp/<name>",
		Long: "secret seals a credential in this organization's secrets, where a local MCP server's\n" +
			"--secret-env NAME=store:mcp/<name> finds it. Storing a name again replaces its value and\n" +
			"description.\n" +
			"The value comes from a file, or - for stdin; never from the command line. It needs an\n" +
			"organization admin. `olivares secrets put` stores deployment-wide secrets, which MCP\n" +
			"servers do not read.",
		Example: "  olivares mcp secret github --value-file ./github-token\n" +
			"  olivares mcp add github --secret-env GITHUB_TOKEN=store:mcp/github -- github-mcp-server stdio",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			short := strings.TrimPrefix(args[0], "mcp/")
			if short == "" {
				return sentence(exitcode.Usage, "Name the secret: olivares mcp secret <name> --value-file <file>")
			}
			name := "mcp/" + short
			value, err := readSecretValue(cmd, "", valueFile)
			if err != nil {
				return err
			}
			if value == "" {
				return sentence(exitcode.Usage, "The value is empty. Give it in a file: olivares mcp secret %s --value-file <file>",
					shellWord(short))
			}
			cfg, err := mcpClient(cmd, flags)
			if err != nil {
				return err
			}
			_, b, err := cfg.do(cmd.Context(), http.MethodPut, mcpSecretsPath,
				map[string]string{"name": name, "value": value, "description": description})
			if err != nil {
				// A proxy in front of the engine may echo the request body in its error.
				return redactCoded(err, value)
			}
			var stored secretMutationResult
			if err := json.Unmarshal(b, &stored); err != nil {
				return exitcode.New(exitcode.Server, fmt.Errorf("decode the stored secret: %w", err))
			}
			return renderOut(cmd, func(w io.Writer) error {
				_, err := fmt.Fprintf(w, "Stored %s (hint %s). Use it: --secret-env NAME=store:%s\n",
					termSafe(stored.Name), termSafe(stored.Hint), termSafe(stored.Name))
				return err
			}, stored)
		},
	}
	cmd.Flags().StringVar(&valueFile, "value-file", "", "read the value from a file, or - for stdin")
	cmd.Flags().StringVar(&description, "description", "", "optional non-secret note")
	_ = cmd.MarkFlagRequired("value-file")
	return cmd
}

func newMCPTestCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "test <name>",
		Short: "Start an MCP server and list its tools without calling any",
		Long: "test starts the server the way a session would, asks it for its tools, and stops it. It\n" +
			"prints the tool names, or why the server did not answer. No tool is called. Run it after\n" +
			"you add or change a server, and before you turn it on.",
		Example: "  olivares mcp test files",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := mcpClient(cmd, flags)
			if err != nil {
				return err
			}
			snap, err := cfg.mcpSnapshot(cmd.Context())
			if err != nil {
				return err
			}
			s, err := mcpMustFind(snap, args[0])
			if err != nil {
				return err
			}
			return cfg.mcpTestAndShow(cmd, snap, s)
		},
	}
	return cmd
}

// mcpTestAndShow runs the engine's test of one server and prints what it found.
func (c *agentClientConfig) mcpTestAndShow(cmd *cobra.Command, snap mcpSnapshot, s map[string]any) error {
	snap, err := c.mcpWrite(cmd.Context(), http.MethodPost, mcpGatewayPath+"/servers/"+url.PathEscape(str(s, "id"))+"/test",
		map[string]any{"version": snap.Version})
	if err != nil {
		return err
	}
	tested, _ := mcpFind(snap, str(s, "id"))
	return renderOut(cmd, func(w io.Writer) error {
		name := termSafe(str(tested, "name"))
		probe, _ := tested["probe"].(map[string]any)
		if state := str(probe, "state"); state != "ok" {
			return sentence(exitcode.Err, "The test of %s failed: %s. Check its command or URL, then: olivares mcp test %s",
				name, mcpProbeFailure(probe), shellWord(name))
		}
		tools, _ := probe["tools"].([]any)
		fmt.Fprintf(w, "%s works: %d tools.\n", name, len(tools))
		for _, t := range tools {
			if tm, ok := t.(map[string]any); ok {
				fmt.Fprintf(w, "  %s\n", termSafe(str(tm, "name")))
			}
		}
		if tested["enabled"] != true {
			renderTo(w).Next("olivares mcp enable " + shellWord(name))
		}
		return nil
	}, tested)
}

func newMCPEnableCmd(flags *authClientFlags, use string, on bool) *cobra.Command {
	short := "Turn an MCP server on, so sessions can use its tools"
	long := "enable turns the server on and prints what each tested tool may do: run without\n" +
		"approval, ask first, or nothing. Alone, every tool asks first. The tools the server itself\n" +
		"calls read-only are only a proposal: enable prints them and --allow-proposed accepts\n" +
		"them; --allow names the tools yourself. Every other tool asks. Sessions get its tools\n" +
		"while sessions use MCP servers (olivares mcp sessions on). Test the server first:\n" +
		"olivares mcp test <name>."
	if !on {
		short = "Turn an MCP server off; sessions stop using it"
		long = "disable turns the server off and prints its new state. Sessions no longer get its\n" +
			"tools; its settings are kept, so enable turns it back on as it was. Use it to stop\n" +
			"sessions from using a server without removing it."
	}
	example := "  olivares mcp " + use + " files"
	if on {
		example += "\n  olivares mcp enable files --allow-proposed\n  olivares mcp enable files --allow read_file,list_directory"
	}
	var (
		allowProposed bool
		allow         []string
	)
	cmd := &cobra.Command{
		Use:     use + " <name>",
		Short:   short,
		Long:    long,
		Example: example,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := mcpClient(cmd, flags)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			snap, err := cfg.mcpSnapshot(ctx)
			if err != nil {
				return err
			}
			s, err := mcpMustFind(snap, args[0])
			if err != nil {
				return err
			}
			input := map[string]any{}
			for k, v := range s {
				if mcpWritableFields[k] {
					input[k] = v
				}
			}
			input["enabled"] = on
			switch list, _ := input["allowed_tools"].([]any); {
			case on && (allowProposed || len(allow) > 0):
				// Run without approval only by the administrator's choice, made with the
				// tool list in view (Root 2026-10-02T01:42Z).
				policies, err := mcpChosenPolicies(s, allowProposed, allow)
				if err != nil {
					return err
				}
				input["allowed_tools"] = policies
			case on && len(list) == 0:
				// The engine stores [] for every new server, and an explicit [] on enable
				// allows no tool. Sent without a list, the engine applies its default:
				// every tool asks. A list someone set (non-empty) stays as set.
				delete(input, "allowed_tools")
			}
			snap, err = cfg.mcpWrite(ctx, http.MethodPut, mcpGatewayPath+"/servers/"+url.PathEscape(str(s, "id")),
				map[string]any{"version": snap.Version, "server": input})
			if err != nil {
				return err
			}
			updated, _ := mcpFind(snap, str(s, "id"))
			name := termSafe(str(s, "name"))
			if on && len(mcpAllowedTools(updated)) == 0 {
				return sentence(exitcode.Err, "%s is on, but no tool is allowed, so sessions cannot call any of its tools. "+
					"Test it to list its tools: olivares mcp test %s", name, shellWord(name))
			}
			return renderOut(cmd, func(w io.Writer) error {
				if !on {
					_, err := fmt.Fprintf(w, "%s is off.\n", name)
					return err
				}
				run, ask, none := mcpToolPolicies(updated)
				fmt.Fprintf(w, "%s is on.\n", name)
				for _, row := range []struct {
					label string
					tools []string
				}{{"Run without approval", run}, {"Ask first", ask}, {"Not allowed", none}} {
					if len(row.tools) > 0 {
						fmt.Fprintf(w, "  %s: %s\n", row.label, mcpToolList(row.tools))
					}
				}
				var pending []string
				for _, t := range mcpProposedTools(updated) {
					if !slices.Contains(run, t) {
						pending = append(pending, t)
					}
				}
				if len(pending) > 0 {
					fmt.Fprintf(w, "The server says these tools only read: %s. To let them run without approval: olivares mcp enable %s --allow-proposed\n",
						mcpToolList(pending), shellWord(name))
				}
				if !snap.SessionTools {
					fmt.Fprintln(w, "Sessions do not use MCP servers yet. Turn it on: olivares mcp sessions on")
				}
				return nil
			}, updated)
		},
	}
	if on {
		cmd.Flags().BoolVar(&allowProposed, "allow-proposed", false,
			"let the tools the server calls read-only run without approval (every other tool asks)")
		cmd.Flags().StringSliceVar(&allow, "allow", nil,
			"tools that run without approval (comma-separated or repeated; every other tool asks)")
	}
	return cmd
}

// mcpWritableFields is what a server write accepts: the fields of the engine's input
// (auth.MCPGatewayServerInput). A roster read also carries response-only fields (id,
// probe, proposed_allow), and the engine refuses a write that sends one back. On refresh
// 09 `mcp enable` copied proposed_allow and got 400 "provide version and a
// reference-only server configuration".
var mcpWritableFields = func() map[string]bool {
	fields := map[string]bool{}
	t := reflect.TypeOf(auth.MCPGatewayServerInput{})
	for i := range t.NumField() {
		if name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ","); name != "" && name != "-" {
			fields[name] = true
		}
	}
	return fields
}()

// mcpTestedTools is the tool names the server's last test found, in its order.
func mcpTestedTools(s map[string]any) []string {
	probe, _ := s["probe"].(map[string]any)
	if str(probe, "state") != "ok" {
		return nil
	}
	tools, _ := probe["tools"].([]any)
	var names []string
	for _, t := range tools {
		if tm, ok := t.(map[string]any); ok && str(tm, "name") != "" {
			names = append(names, str(tm, "name"))
		}
	}
	return names
}

// mcpProposedTools is the engine's proposal: the tested tools whose own hints say
// read-only (proposed_allow). An engine that does not send the proposal yet marks the
// same tools read_only in the probe.
func mcpProposedTools(s map[string]any) []string {
	var names []string
	if list, ok := s["proposed_allow"].([]any); ok {
		for _, n := range list {
			if name, ok := n.(string); ok && name != "" {
				names = append(names, termSafe(name))
			}
		}
		return names
	}
	probe, _ := s["probe"].(map[string]any)
	tools, _ := probe["tools"].([]any)
	for _, t := range tools {
		if tm, ok := t.(map[string]any); ok && tm["read_only"] == true && str(tm, "name") != "" {
			names = append(names, termSafe(str(tm, "name")))
		}
	}
	return names
}

// mcpChosenPolicies is the explicit list for the tools the administrator lets run
// without approval: the proposal (--allow-proposed) and the named tools (--allow). Every
// other tested tool asks (destructive). A name the test did not find is refused.
func mcpChosenPolicies(s map[string]any, proposed bool, allow []string) ([]any, error) {
	name := termSafe(str(s, "name"))
	tools := mcpTestedTools(s)
	if len(tools) == 0 {
		return nil, sentence(exitcode.Usage, "Test %s first, so its tools are known: olivares mcp test %s", name, shellWord(name))
	}
	run := map[string]bool{}
	if proposed {
		for _, t := range mcpProposedTools(s) {
			run[t] = true
		}
	}
	for _, a := range allow {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if !slices.Contains(tools, a) {
			shown := make([]string, len(tools))
			for i, t := range tools {
				shown[i] = termSafe(t)
			}
			return nil, sentence(exitcode.Usage, "%s has no tool named %q. Its tools: %s", name, termSafe(a), mcpToolList(shown))
		}
		run[a] = true
	}
	policies := make([]any, 0, len(tools))
	for _, t := range tools {
		policies = append(policies, map[string]any{"name": t, "required_scope": "tools:call", "destructive": !run[t]})
	}
	return policies, nil
}

// mcpToolPolicies sorts the server's tools by what they may do. A listed tool runs
// without approval unless it is marked destructive, which asks first; a tested tool the
// list does not name is not allowed.
func mcpToolPolicies(s map[string]any) (run, ask, none []string) {
	listed := map[string]bool{}
	var order []string
	list, _ := s["allowed_tools"].([]any)
	for _, t := range list {
		tm, ok := t.(map[string]any)
		if !ok || str(tm, "name") == "" {
			continue
		}
		listed[str(tm, "name")] = tm["destructive"] == true
		order = append(order, str(tm, "name"))
	}
	place := func(n string) {
		switch destructive, ok := listed[n]; {
		case !ok:
			none = append(none, termSafe(n))
		case destructive:
			ask = append(ask, termSafe(n))
		default:
			run = append(run, termSafe(n))
		}
	}
	tested := mcpTestedTools(s)
	for _, n := range tested {
		place(n)
	}
	for _, n := range order {
		if !slices.Contains(tested, n) {
			place(n)
		}
	}
	return run, ask, none
}

func newMCPRemoveCmd(flags *authClientFlags) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "rm <name>",
		Aliases: []string{"remove", "delete"},
		Short:   "Remove an MCP server",
		Long: "rm deletes the server and its settings; sessions no longer get its tools. It asks\n" +
			"before it deletes (--yes skips the question). To stop using a server and keep its\n" +
			"settings, use olivares mcp disable instead.",
		Example: "  olivares mcp rm files --yes",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := mcpClient(cmd, flags)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			snap, err := cfg.mcpSnapshot(ctx)
			if err != nil {
				return err
			}
			s, err := mcpMustFind(snap, args[0])
			if err != nil {
				return err
			}
			if err := confirmDestructive(cmd, yes, "remove the MCP server "+termSafe(str(s, "name"))); err != nil {
				return err
			}
			if _, err := cfg.mcpWrite(ctx, http.MethodDelete, mcpGatewayPath+"/servers/"+url.PathEscape(str(s, "id")),
				map[string]any{"version": snap.Version}); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				_, err := fmt.Fprintf(w, "Removed %s.\n", termSafe(str(s, "name")))
				return err
			}, map[string]any{"id": str(s, "id"), "removed": true})
		},
	}
	addYesFlag(cmd, &yes)
	return cmd
}

func newMCPSessionsCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sessions <on|off>",
		Short: "Let sessions use the MCP servers that are on, or stop them (off by default)",
		Long: "sessions on lets sessions use the tools of every MCP server that is on; sessions off\n" +
			"stops that for all of them, and each server keeps its own on/off. It prints the new\n" +
			"setting. It is off on a new installation.",
		Example:   "  olivares mcp sessions on",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"on", "off"},
		RunE: func(cmd *cobra.Command, args []string) error {
			on := args[0] == "on"
			if !on && args[0] != "off" {
				return sentence(exitcode.Usage, "Say on or off: olivares mcp sessions on")
			}
			cfg, err := mcpClient(cmd, flags)
			if err != nil {
				return err
			}
			snap, err := cfg.mcpSnapshot(cmd.Context())
			if err != nil {
				return err
			}
			snap, err = cfg.mcpWrite(cmd.Context(), http.MethodPut, mcpGatewayPath+"/session-tools",
				map[string]any{"version": snap.Version, "enabled": on})
			if err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				msg := "Sessions use the MCP servers that are on."
				if !on {
					msg = "Sessions do not use MCP servers."
				}
				_, err := fmt.Fprintln(w, msg)
				return err
			}, map[string]any{"session_tools": snap.SessionTools})
		},
	}
	return cmd
}

// --- roster reads and writes ------------------------------------------------------

func (c *agentClientConfig) mcpSnapshot(ctx context.Context) (mcpSnapshot, error) {
	status, b, err := c.do(ctx, http.MethodGet, mcpGatewayPath, nil)
	if err != nil {
		return mcpSnapshot{}, err
	}
	if status != http.StatusOK {
		return mcpSnapshot{}, httpErr(status, b)
	}
	var snap mcpSnapshot
	return snap, json.Unmarshal(b, &snap)
}

// mcpWrite sends one versioned write and returns the roster the engine answers with.
func (c *agentClientConfig) mcpWrite(ctx context.Context, method, path string, body any) (mcpSnapshot, error) {
	// 409/412 are read here, never printed: the sentence below replaces the body. An
	// add answers 201 (J6 on refresh 02: it was refused as an error).
	status, b, err := c.do(ctx, method, path, body, http.StatusOK, http.StatusCreated, http.StatusConflict,
		http.StatusPreconditionFailed)
	if err != nil {
		return mcpSnapshot{}, err
	}
	switch {
	case status == http.StatusConflict || status == http.StatusPreconditionFailed:
		return mcpSnapshot{}, sentence(exitcode.Conflict, "The MCP servers changed while you were editing. Run the command again.")
	case status < 200 || status > 299:
		return mcpSnapshot{}, httpErr(status, b)
	}
	var snap mcpSnapshot
	return snap, json.Unmarshal(b, &snap)
}

// mcpFind returns the server whose id or name is key.
func mcpFind(snap mcpSnapshot, key string) (map[string]any, bool) {
	for _, s := range snap.Servers {
		if str(s, "id") == key {
			return s, true
		}
	}
	for _, s := range snap.Servers {
		if str(s, "name") == key {
			return s, true
		}
	}
	return nil, false
}

func mcpMustFind(snap mcpSnapshot, key string) (map[string]any, error) {
	if s, ok := mcpFind(snap, key); ok {
		return s, nil
	}
	return nil, sentence(exitcode.NotFound, "No MCP server is named %q. List them: olivares mcp ls", key)
}

// mcpTarget is what a server runs: its command line, or its URL.
func mcpTarget(s map[string]any) string {
	if u := str(s, "url"); u != "" {
		return termSafe(u)
	}
	parts := []string{str(s, "command")}
	if args, ok := s["args"].([]any); ok {
		for _, a := range args {
			if as, ok := a.(string); ok {
				parts = append(parts, as)
			}
		}
	}
	return truncateSummary(termSafe(strings.Join(parts, " ")))
}

// mcpAllowedTools is the names of the tools sessions may call on a server.
func mcpAllowedTools(s map[string]any) []string {
	list, _ := s["allowed_tools"].([]any)
	var names []string
	for _, t := range list {
		if tool, ok := t.(map[string]any); ok && str(tool, "name") != "" {
			names = append(names, termSafe(str(tool, "name")))
		}
	}
	return names
}

// mcpToolList names up to ten tools and counts the rest.
func mcpToolList(names []string) string {
	if len(names) > 10 {
		return strings.Join(names[:10], ", ") + fmt.Sprintf(" and %d more", len(names)-10)
	}
	return strings.Join(names, ", ")
}

func mcpToolsCell(s map[string]any) string {
	probe, _ := s["probe"].(map[string]any)
	switch state := str(probe, "state"); state {
	case "", "never_tested":
		return "not tested"
	case "ok":
		tools, _ := probe["tools"].([]any)
		return fmt.Sprintf("%d", len(tools))
	default:
		return truncateSummary(mcpProbeFailure(probe))
	}
}

// mcpProbeFailure is a failed test in one line, with the engine's reason and its
// redacted detail (a local server's last stderr line or OS error): "did not start:
// <detail>", never a bare "unreachable".
func mcpProbeFailure(probe map[string]any) string {
	detail := strings.TrimRight(strings.TrimSpace(termSafe(str(probe, "detail"))), ".")
	with := func(s string) string {
		if detail != "" {
			return s + ": " + detail
		}
		return s
	}
	switch reason := str(probe, "reason"); reason {
	case "process_start":
		return with("did not start")
	case "process_exit":
		return with("exited")
	case "dns":
		return with("unreachable: the name does not resolve")
	case "tls":
		return with("unreachable: TLS failed")
	case "timeout":
		return with("unreachable: timed out")
	case "connection_refused":
		return with("unreachable: connection refused")
	case "http_status":
		if code, ok := probe["http_status"].(float64); ok && code > 0 {
			return with(fmt.Sprintf("unreachable: HTTP %.0f", code))
		}
		return with("unreachable: HTTP error")
	}
	switch state := str(probe, "state"); state {
	case "unreachable":
		if detail == "" {
			return "unreachable (the engine gave no reason)"
		}
		return with("unreachable")
	case "refused":
		return with("refused the test")
	case "invalid_response":
		return with("answered with an invalid response")
	case "egress_denied":
		return with("blocked by the egress policy")
	case "redirect_refused":
		return with("redirected (redirects are refused)")
	case "credential_unavailable":
		return with("its credential is unavailable")
	default:
		return with(strings.ReplaceAll(termSafe(state), "_", " "))
	}
}

// keyValues parses repeated NAME=value flags.
func keyValues(pairs []string, flag string) (map[string]string, error) {
	out := map[string]string{}
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, sentence(exitcode.Usage, "%s takes NAME=value, got %q.", flag, p)
		}
		out[k] = v
	}
	return out, nil
}
