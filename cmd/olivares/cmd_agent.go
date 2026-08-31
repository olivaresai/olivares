// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// cmd_agent.go is the operator CLI for the governed Claude Code session
// runtime: `olivares agent session {create|ls|get|events|attach|input|stop|
// resume|cleanup|delete}`. Every subcommand is a THIN HTTP client (Bearer token +
// tenant header) against /v1/m/sessions/runs* — ALL lifecycle/runtime logic lives
// server-side in module II; the CLI never spawns a process itself.

// agentClientConfig is the shared server/credential flag set.
//
// It resolves and connects through the SAME two functions as the rest of the CLI
// — resolveCLIConfig and cliTransport (E4). Before that it did neither: it
// read only flags and environment, so `olivares auth login` had no effect on any
// agent command, and it built a bare http.Client, so --ca-cert and --pin-sha256
// did not exist here and --insecure disabled verification WITHOUT printing the
// warning clitransport.go emits. This is the largest of the four ad-hoc paths:
// every session and every workspace verb went through it.
type agentClientConfig struct {
	server   string
	token    string
	tenant   string
	caCert   string
	pins     []string
	insecure bool
	timeout  time.Duration
	// flags is captured at declaration time so resolve() can tell an explicitly
	// passed empty value from an omitted one, which is what gives the active
	// client context its correct precedence.
	flags *pflag.FlagSet
	// resolved is the outcome of resolve(), reused by every request.
	resolved cliResolvedConfig
}

func (c *agentClientConfig) addFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&c.server, "server", "", "control-plane base URL (default $OLIVARES_SERVER_URL or the active client context)")
	cmd.Flags().StringVar(&c.token, "token", "", "API bearer token (default $OLIVARES_TOKEN or the active client context)")
	cmd.Flags().StringVar(&c.tenant, "tenant", "", "tenant id (default $OLIVARES_TENANT or the active client context)")
	cmd.Flags().StringVar(&c.caCert, "ca-cert", "", "PEM CA bundle used to verify the control plane (default: the active client context)")
	cmd.Flags().StringArrayVar(&c.pins, "pin-sha256", nil, "pinned leaf SPKI SHA-256, base64 or hex (repeatable) — the engine prints it as pin_sha256 on the line reporting its certificate; default: the active client context")
	cmd.Flags().BoolVar(&c.insecure, "insecure", false, "skip TLS certificate verification (self-signed dev planes only)")
	cmd.Flags().DurationVar(&c.timeout, "timeout", 30*time.Second, "request timeout")
	c.flags = cmd.Flags()
}

func (c *agentClientConfig) changed(name string) bool {
	return c.flags != nil && c.flags.Changed(name)
}

func (c *agentClientConfig) resolve() error {
	resolved, err := resolveCLIConfig(cliResolutionOptions{
		Server: c.server, Token: c.token, Tenant: c.tenant,
		CACert: c.caCert, PinSHA256: append([]string(nil), c.pins...),
		ServerExplicit: c.changed("server") || c.server != "",
		TokenExplicit:  c.changed("token") || c.token != "",
		TenantExplicit: c.changed("tenant") || c.tenant != "",
		CACertExplicit: c.changed("ca-cert") || c.caCert != "",
		PinsExplicit:   c.changed("pin-sha256") || len(c.pins) > 0,
	})
	if err != nil {
		return err
	}
	c.resolved = resolved
	switch {
	case resolved.Server == "":
		return missingCLIValueError("server", "--server", "OLIVARES_SERVER_URL", resolved)
	case resolved.Token == "":
		return missingCLIValueError("token", "--token", "OLIVARES_TOKEN", resolved)
	case resolved.Tenant == "":
		return missingCLIValueError("tenant", "--tenant", "OLIVARES_TENANT", resolved)
	}
	// Keep the plain fields in step for the code that reads them directly.
	c.server, c.token, c.tenant = resolved.Server, resolved.Token, resolved.Tenant
	return nil
}

// transport returns the hardened client for this config. Stderr is left nil so
// cliTransport writes its --insecure warning to os.Stderr — the warning is the
// point, and it was missing entirely from this path.
func (c *agentClientConfig) transport(timeout time.Duration) (*http.Client, error) {
	client, _, err := cliTransport(cliTransportOptions{
		Resolved: c.resolved, Insecure: c.insecure, Timeout: timeout,
	})
	return client, err
}

// streamTransport is transport for a long-lived stream: no overall deadline, so
// the caller's context (Ctrl-C) is what ends it. Asking for Timeout: 0 would NOT
// do that — cliTransport reads zero as "unspecified" and substitutes its default.
func (c *agentClientConfig) streamTransport() (*http.Client, error) {
	client, _, err := cliTransport(cliTransportOptions{
		Resolved: c.resolved, Insecure: c.insecure, Unbounded: true,
	})
	return client, err
}

func (c *agentClientConfig) newRequest(ctx context.Context, method, path string, body any) (*http.Request, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.server+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-Olivares-Tenant", c.tenant)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// do performs one buffered JSON request.
func (c *agentClientConfig) do(ctx context.Context, method, path string, body any) (int, []byte, error) {
	req, err := c.newRequest(ctx, method, path, body)
	if err != nil {
		return 0, nil, err
	}
	client, err := c.transport(c.timeout)
	if err != nil {
		return 0, nil, err
	}
	resp, err := cliDo(client, req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return resp.StatusCode, b, err
}

func newAgentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Operate governed Claude Code sessions (launch, attach, stop, resume, clean up)",
		Long: "agent is the operator surface for Claude Code under governance: the sessions\n" +
			"themselves, the workspaces a session may read and write, and the\n" +
			"managed-settings.json that binds a launched session to the PEP hook.\n\n" +
			"session and workspace act on a running control plane and need --server (or\n" +
			"OLIVARES_SERVER_URL) plus a token; managed-settings renders a file locally.",
		Example: "  olivares agent session ls\n" +
			"  olivares agent workspace ls -o json\n" +
			"  sudo olivares agent managed-settings --out /etc/claude-code/managed-settings.json",
	}
	cmd.AddCommand(newAgentSessionCmd())
	cmd.AddCommand(newAgentWorkspaceCmd())
	cmd.AddCommand(newAgentManagedSettingsCmd())
	return cmd
}

func newAgentSessionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "Manage the lifecycle of governed Claude Code sessions",
		Long: "session covers a governed Claude Code run end to end: create and stop it,\n" +
			"attach to its live output, feed it input, read its lifecycle ledger, and\n" +
			"release its record once the work is done.\n\n" +
			"Every verb here is a control-plane call, so a session outlives the terminal\n" +
			"that launched it and stays inspectable from any authenticated CLI.",
		Example: "  olivares agent session ls\n" +
			"  olivares agent session create --name \"feature-work\" --workspace /src/myproject\n" +
			"  olivares agent session attach run-123 --from 42",
	}
	cmd.AddCommand(
		newAgentSessionCreateCmd(),
		newAgentSessionListCmd(),
		newAgentSessionGetCmd(),
		newAgentSessionEventsCmd(),
		newAgentSessionAttachCmd(),
		newAgentSessionInputCmd(),
		newAgentSessionStopCmd(),
		newAgentSessionResumeCmd(),
		newAgentSessionCleanupCmd(),
		newAgentSessionDeleteCmd(),
	)
	return cmd
}

func newAgentSessionCreateCmd() *cobra.Command {
	var (
		cfg                                                 agentClientConfig
		name, transport, permMode, effort, model, workspace string
		isolation                                           string
		envAllow                                            []string
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Launch a governed Claude Code session",
		Long: "create launches a Claude Code session through the Olivares sessions API, applying the\n" +
			"selected transport, permission mode, workspace, isolation, model, effort and environment allowlist.",
		Example: `  # Create a governed session with stream-json transport
  olivares agent session create --name "feature-work" --workspace /src/myproject

  # Create with full bypass for trusted automation
  olivares agent session create --name "ci-run" --permission-mode bypassPermissions --effort max`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			body := map[string]any{
				"name": name, "transport": transport, "permission_mode": permMode,
				"effort": effort, "model": model, "workspace_ref": workspace, "isolation": isolation,
				"env_allow": envAllow,
			}
			status, b, err := cfg.do(cmd.Context(), "POST", "/v1/m/sessions/runs", body)
			if err != nil {
				return err
			}
			return printRun(cmd, status, b, http.StatusCreated)
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().StringVar(&name, "name", "", "display name for the session")
	cmd.Flags().StringVar(&transport, "transport", "stream-json", "transport: stream-json (governed) | remote-control (lifecycle-only)")
	cmd.Flags().StringVar(&permMode, "permission-mode", "default", "default|acceptEdits|plan|auto|dontAsk|bypassPermissions")
	cmd.Flags().StringVar(&effort, "effort", "", "low|medium|high|xhigh|max")
	cmd.Flags().StringVar(&model, "model", "", "model alias (opus) or id (claude-opus-4-8)")
	cmd.Flags().StringVar(&workspace, "workspace", "", "workspace reference (the session's working directory)")
	// E6: the accepted values and the WIRED values are not the same set, and
	// the help used to name all three as if they were. The only runner in this
	// release is the native one, and it refuses container/sandbox deny-closed
	// rather than run unisolated while the row claims otherwise
	// (modules/sessions/procrunner.go:52) — so copying the old example produced a
	// 502. The flag still accepts them, because the API and the row model do; the
	// help now says which one actually launches.
	cmd.Flags().StringVar(&isolation, "isolation", "native",
		"native (the only runner wired this release) | container | sandbox — "+
			"container and sandbox are accepted by the API but refused by the launcher until their runner ships")
	cmd.Flags().StringSliceVar(&envAllow, "env-allow", nil, "host env var NAMES to forward to the session (allowlist; nothing else is inherited)")
	addDeprecatedJSONFlag(cmd)
	_ = cmd.RegisterFlagCompletionFunc("transport", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"stream-json", "remote-control"}, cobra.ShellCompDirectiveNoFileComp
	})
	_ = cmd.RegisterFlagCompletionFunc("permission-mode", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"default", "acceptEdits", "plan", "auto", "dontAsk", "bypassPermissions"}, cobra.ShellCompDirectiveNoFileComp
	})
	_ = cmd.RegisterFlagCompletionFunc("effort", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"low", "medium", "high", "xhigh", "max"}, cobra.ShellCompDirectiveNoFileComp
	})
	_ = cmd.RegisterFlagCompletionFunc("isolation", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		// Descriptions, not a bare list: the shell shows them, so the completion
		// stops presenting three equally-valid-looking choices when one launches
		// and two are refused.
		return []string{
			"native\twired: runs as a governed host child",
			"container\tnot wired this release: the launcher refuses it",
			"sandbox\tnot wired this release: the launcher refuses it",
		}, cobra.ShellCompDirectiveNoFileComp
	})
	return cmd
}

func newAgentSessionListCmd() *cobra.Command {
	var (
		cfg   agentClientConfig
		state string
	)
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List operated sessions",
		Long:    "ls lists the governed Claude Code sessions visible to the configured tenant, optionally filtered by lifecycle state.",
		Example: `  # List all sessions
  olivares agent session ls

  # List only running sessions as JSON
  olivares agent session ls --state running -o json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			path := "/v1/m/sessions/runs"
			if state != "" {
				path += "?state=" + state
			}
			status, b, err := cfg.do(cmd.Context(), "GET", path, nil)
			if err != nil {
				return err
			}
			if status != http.StatusOK {
				return httpErr(status, b)
			}
			var resp struct {
				Items []map[string]any `json:"items"`
			}
			if err := json.Unmarshal(b, &resp); err != nil {
				return err
			}
			return renderListOut(cmd, resp.Items, "no sessions", func(out io.Writer, it map[string]any) error {
				_, err := fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", str(it, "run_ref"), str(it, "state"), str(it, "transport"), str(it, "name"))
				return err
			}, json.RawMessage(b))
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().StringVar(&state, "state", "", "filter by state (pending|running|idle|stopped|failed|cleaned)")
	addDeprecatedJSONFlag(cmd)
	_ = cmd.RegisterFlagCompletionFunc("state", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"pending", "running", "idle", "stopped", "failed", "cleaned"}, cobra.ShellCompDirectiveNoFileComp
	})
	return cmd
}

func newAgentSessionGetCmd() *cobra.Command {
	var cfg agentClientConfig
	cmd := &cobra.Command{
		Use:               "get <run-ref>",
		Short:             "Show one session",
		Long:              "get retrieves the complete governed-session record for one run reference and prints it as JSON.",
		Example:           "  olivares agent session get run-123",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeSessions,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			status, b, err := cfg.do(cmd.Context(), "GET", "/v1/m/sessions/runs/"+args[0], nil)
			if err != nil {
				return err
			}
			if status != http.StatusOK {
				return httpErr(status, b)
			}
			return printRaw(cmd, b)
		},
	}
	cfg.addFlags(cmd)
	return cmd
}

func newAgentSessionEventsCmd() *cobra.Command {
	var cfg agentClientConfig
	cmd := &cobra.Command{
		Use:               "events <run-ref>",
		Short:             "Show a session's lifecycle ledger",
		Long:              "events retrieves the ordered lifecycle ledger for one governed session and prints the JSON response.",
		Example:           "  olivares agent session events run-123",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeSessions,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			status, b, err := cfg.do(cmd.Context(), "GET", "/v1/m/sessions/runs/"+args[0]+"/events", nil)
			if err != nil {
				return err
			}
			if status != http.StatusOK {
				return httpErr(status, b)
			}
			return printRaw(cmd, b)
		},
	}
	cfg.addFlags(cmd)
	return cmd
}

func newAgentSessionAttachCmd() *cobra.Command {
	var (
		cfg  agentClientConfig
		from int64
	)
	cmd := &cobra.Command{
		Use:               "attach <run-ref>",
		Short:             "Stream a live session's I/O (server-sent events) to stdout",
		Long:              "attach opens the server-sent-events stream for a live governed session and writes session output to stdout until the stream ends or is canceled.",
		Example:           "  olivares agent session attach run-123 --from 42",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeSessions,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			return cfg.streamAttach(cmd, args[0], from)
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().Int64Var(&from, "from", 0, "replay from this output sequence number")
	return cmd
}

// streamAttach opens the SSE attach stream and prints each output line. It uses no
// client timeout (a live attach is long-lived); Ctrl-C (context cancel) ends it.
func (c *agentClientConfig) streamAttach(cmd *cobra.Command, ref string, from int64) error {
	path := fmt.Sprintf("/v1/m/sessions/runs/%s/attach?from=%d", ref, from)
	req, err := c.newRequest(cmd.Context(), "GET", path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	client, err := c.streamTransport()
	if err != nil {
		return err
	}
	resp, err := cliDo(client, req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return httpErr(resp.StatusCode, b)
	}
	out := cmd.OutOrStdout()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	var event string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data := strings.TrimPrefix(line, "data: ")
			switch event {
			case "output":
				var f struct {
					Line string `json:"line"`
				}
				if json.Unmarshal([]byte(data), &f) == nil {
					fmt.Fprintln(out, f.Line)
				}
			case "lag":
				fmt.Fprintln(cmd.ErrOrStderr(), "[attach] lag: "+data)
			case "notice":
				fmt.Fprintln(cmd.ErrOrStderr(), "[attach] notice: "+data)
			case "end":
				return nil
			}
		}
	}
	return sc.Err()
}

func newAgentSessionInputCmd() *cobra.Command {
	var (
		cfg  agentClientConfig
		line string
	)
	cmd := &cobra.Command{
		Use:     "input <run-ref>",
		Short:   "Send one NDJSON line to a live session's stdin ('-' or empty reads stdin)",
		Long:    "input sends one NDJSON message to a live governed session. Supply it with --line or pipe it through stdin.",
		Example: `  printf '%s\n' '{"type":"user","message":"continue"}' | olivares agent session input run-123`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			if line == "" || line == "-" {
				b, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 1<<20))
				if err != nil {
					return err
				}
				line = strings.TrimRight(string(b), "\n")
			}
			status, b, err := cfg.do(cmd.Context(), "POST", "/v1/m/sessions/runs/"+args[0]+"/input", map[string]any{"line": line})
			if err != nil {
				return err
			}
			if status != http.StatusAccepted {
				return httpErr(status, b)
			}
			return nil
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().StringVar(&line, "line", "", "the NDJSON message to write (default: read from stdin)")
	return cmd
}

func newAgentSessionStopCmd() *cobra.Command {
	return newAgentSessionActionCmd("stop", "Stop a running session", "POST", "/stop")
}
func newAgentSessionResumeCmd() *cobra.Command {
	return newAgentSessionActionCmd("resume", "Resume a stopped session", "POST", "/resume")
}
func newAgentSessionCleanupCmd() *cobra.Command {
	return newAgentSessionActionCmd("cleanup", "Release a stopped session (mark cleaned)", "POST", "/cleanup")
}

// newAgentSessionActionCmd builds a simple ref-only lifecycle action command.
func newAgentSessionActionCmd(use, short, method, suffix string) *cobra.Command {
	var cfg agentClientConfig
	cmd := &cobra.Command{
		Use:               use + " <run-ref>",
		Short:             short,
		Long:              short + " through the governed sessions API and print the updated session record.",
		Example:           "  olivares agent session " + use + " run-123",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeSessions,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			status, b, err := cfg.do(cmd.Context(), method, "/v1/m/sessions/runs/"+args[0]+suffix, nil)
			if err != nil {
				return err
			}
			if status != http.StatusOK {
				return httpErr(status, b)
			}
			return printRaw(cmd, b)
		},
	}
	cfg.addFlags(cmd)
	return cmd
}

func newAgentSessionDeleteCmd() *cobra.Command {
	var cfg agentClientConfig
	cmd := &cobra.Command{
		// Canonical short verb first (`ls`/`rm` across the CLI); the old
		// names stay as aliases so nothing breaks.
		Use:               "rm <run-ref>",
		Aliases:           []string{"delete", "remove"},
		Short:             "Delete a cleaned session's record",
		Long:              "delete removes the persisted record for a governed session that has already reached the cleaned state.",
		Example:           "  olivares agent session delete run-123",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeSessions,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			status, b, err := cfg.do(cmd.Context(), "DELETE", "/v1/m/sessions/runs/"+args[0], nil)
			if err != nil {
				return err
			}
			if status != http.StatusOK {
				return httpErr(status, b)
			}
			return nil
		},
	}
	cfg.addFlags(cmd)
	return cmd
}

// printRun prints a run DTO response (human summary or raw JSON), mapping a
// non-success status to an error.
func printRun(cmd *cobra.Command, status int, b []byte, want int) error {
	if status != want {
		return httpErr(status, b)
	}
	var run map[string]any
	if err := json.Unmarshal(b, &run); err != nil {
		return err
	}
	return renderOut(cmd, func(out io.Writer) error {
		_, err := fmt.Fprintf(out, "run %s state=%s transport=%s isolation=%s\n",
			str(run, "run_ref"), str(run, "state"), str(run, "transport"), str(run, "isolation"))
		return err
	}, json.RawMessage(b))
}

func printRaw(cmd *cobra.Command, b []byte) error {
	_, err := cmd.OutOrStdout().Write(append(bytes.TrimRight(b, "\n"), '\n'))
	return err
}

func httpErr(status int, b []byte) error {
	err := fmt.Errorf("request failed: HTTP %d: %s", status, strings.TrimSpace(string(b)))
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return exitcode.New(exitcode.Auth, err)
	case status == http.StatusNotFound:
		return exitcode.New(exitcode.NotFound, err)
	case status == http.StatusConflict:
		return exitcode.New(exitcode.Conflict, err)
	case status >= http.StatusInternalServerError:
		return exitcode.New(exitcode.Server, err)
	default:
		return err
	}
}

func str(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}
