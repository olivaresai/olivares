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
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// cmd_agent.go is the operator CLI for the governed session runtime:
// `olivares agent session {create|ls|get|events|attach|input|interrupt|stop|
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
		Short: "Operate governed provider sessions (launch, attach, interrupt, stop, resume, clean up)",
		Long: "agent is the operator surface for official provider sessions under governance: the\n" +
			"sessions themselves, the workspaces a session may read and write, and the\n" +
			"managed-settings.json that binds a launched Claude Code session to the PEP hook.\n\n" +
			"session and workspace act on a running control plane and need --server (or\n" +
			"OLIVARES_SERVER_URL) plus a token; managed-settings renders a file locally, and tool\n" +
			"installs and inventories official provider CLIs from signed releases on this host\n" +
			"without any server. Available session verbs depend on the driver and control plane;\n" +
			"this CLI does not claim that every provider supports every operation.",
		Example: "  olivares agent session ls\n" +
			"  olivares agent workspace ls -o json\n" +
			"  sudo olivares agent managed-settings --out /etc/claude-code/managed-settings.json\n" +
			"  olivares agent tool install --driver claude --version latest --yes",
	}
	cmd.AddCommand(newAgentSessionCmd())
	cmd.AddCommand(newAgentWorkspaceCmd())
	cmd.AddCommand(newAgentManagedSettingsCmd())
	cmd.AddCommand(newAgentToolCmd())
	return cmd
}

func newAgentSessionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "Manage the lifecycle of governed provider sessions",
		Long: "session covers a governed official-provider run end to end: create and stop it,\n" +
			"attach to its live output, send input, interrupt an active turn, read its lifecycle\n" +
			"ledger, and release its record once the work is done.\n\n" +
			"interrupt cancels the active provider turn and leaves the owned process running.\n" +
			"stop ends the session. interrupt does not stop, resume, or clean up a session.\n\n" +
			"Every verb here is a control-plane call, so a session outlives the terminal that\n" +
			"launched it and stays inspectable from any authenticated CLI. Which input form and\n" +
			"which interrupt a run accepts is decided by its driver; this CLI does not convert\n" +
			"payloads or claim that every provider supports every verb.",
		Example: "  olivares agent session ls\n" +
			"  olivares agent session create --name \"feature-work\" --workspace ws-123 --provider-profile prof-123\n" +
			"  olivares agent session attach run-123 --from 42\n" +
			"  olivares agent session interrupt run-123",
	}
	cmd.AddCommand(
		newAgentSessionCreateCmd(),
		newAgentSessionListCmd(),
		newAgentSessionGetCmd(),
		newAgentSessionEventsCmd(),
		newAgentSessionAttachCmd(),
		newAgentSessionInputCmd(),
		newAgentSessionInterruptCmd(),
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
		providerProfile                                     string
		envAllow                                            []string
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Launch a governed Claude Code session",
		Long: "create launches a Claude Code session through the Olivares sessions API, applying the\n" +
			"selected transport, permission mode, workspace, isolation, model, effort, environment\n" +
			"allowlist and, when given, provider profile. Current control planes require\n" +
			"--provider-profile; omitting it keeps the older request body, and the live API still\n" +
			"refuses the launch rather than selecting a profile, home or environment implicitly.",
		Example: `  # Create a governed session with stream-json transport under a selected profile
  olivares agent session create --name "feature-work" --workspace ws-123 --provider-profile prof-123

  # Create with full bypass for trusted automation
  olivares agent session create --name "ci-run" --permission-mode bypassPermissions --effort max --provider-profile prof-123`,
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
			// Only an explicit flag becomes provider_profile_ref. An omitted option
			// keeps the older transport; the current server still refuses it honestly.
			if cmd.Flags().Changed("provider-profile") {
				body["provider_profile_ref"] = providerProfile
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
	cmd.Flags().StringVar(&providerProfile, "provider-profile", "",
		"provider profile reference to launch under (current servers require it; omit to keep the older request body, which this API still refuses)")
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
		cfg            agentClientConfig
		line           string
		text           string
		workLeaseFence int64
	)
	cmd := &cobra.Command{
		Use:   "input <run-ref>",
		Short: "Send one line or driver text to a live session",
		Long: "input sends exactly one payload to a live governed session.\n\n" +
			"With neither --text nor --line, one NDJSON line is read from stdin (legacy).\n" +
			"--line sends a raw NDJSON line for stream-json stdin; an explicit empty value or\n" +
			"'-' still reads stdin, and trailing newlines are trimmed.\n" +
			"--text sends driver text as the session's turn input. '-' reads stdin; any other\n" +
			"value is included unchanged in the HTTP request, including multiline UTF-8 and a\n" +
			"trailing newline. Empty or whitespace-only text is refused locally. --text and\n" +
			"--line cannot be combined, including when either flag is explicitly empty.\n\n" +
			"--work-lease-fence, when set, must be a positive integer and is sent as\n" +
			"work_lease_fence. The control plane decides whether the session accepts line or\n" +
			"text; this command does not infer the provider, convert one form into the other,\n" +
			"or retry after refusal.",
		Example: `  printf '%s\n' '{"type":"user","message":"continue"}' | olivares agent session input run-123
  olivares agent session input run-123 --line '{"type":"user","message":"continue"}'
  olivares agent session input run-123 --text 'review the remaining tests'
  cat prompt.txt | olivares agent session input run-123 --text -
  olivares agent session input run-123 --text 'continue' --work-lease-fence 7`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			textSet := cmd.Flags().Changed("text")
			lineSet := cmd.Flags().Changed("line")
			if textSet && lineSet {
				return sessionCLIUsage("--text and --line are mutually exclusive")
			}
			fence, err := optionalPositiveWorkLeaseFence(cmd, workLeaseFence)
			if err != nil {
				return err
			}
			if err := cfg.resolve(); err != nil {
				return err
			}
			body := make(map[string]any, 2)
			if textSet {
				payload, err := sessionTextPayload(cmd, text)
				if err != nil {
					return err
				}
				body["text"] = payload
			} else {
				payload, err := sessionLinePayload(cmd, line)
				if err != nil {
					return err
				}
				body["line"] = payload
			}
			if fence != nil {
				// int64 in the map so encoding/json writes a JSON integer, including
				// values above 2^53; a float64 would silently change the fence.
				body["work_lease_fence"] = *fence
			}
			status, b, err := cfg.do(cmd.Context(), "POST",
				"/v1/m/sessions/runs/"+agentExecPathID(args[0])+"/input", body)
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
	cmd.Flags().StringVar(&line, "line", "", "raw NDJSON line (empty or '-' reads stdin; mutually exclusive with --text)")
	cmd.Flags().StringVar(&text, "text", "", "driver text (use '-' to read stdin; mutually exclusive with --line)")
	cmd.Flags().Int64Var(&workLeaseFence, "work-lease-fence", 0, "positive work-lease fence; omitted from the request when unset")
	return cmd
}

func newAgentSessionInterruptCmd() *cobra.Command {
	var (
		cfg            agentClientConfig
		workLeaseFence int64
	)
	cmd := &cobra.Command{
		Use:   "interrupt <run-ref>",
		Short: "Cancel the active provider turn without stopping the session",
		Long: "interrupt cancels the active provider turn of a live governed session and keeps\n" +
			"the owned process running. It is not stop: stop ends the session. interrupt does\n" +
			"not stop, resume, or clean up a session, and it does not retry another lifecycle\n" +
			"endpoint if the control plane refuses, reports the operation unsupported, or\n" +
			"conflicts.\n\n" +
			"An omitted --work-lease-fence sends no request body. A supplied fence must be a\n" +
			"positive integer and is sent as work_lease_fence. Support depends on the session's\n" +
			"driver; this command does not claim that every provider implements interrupt.",
		Example: `  olivares agent session interrupt run-123
  olivares agent session interrupt run-123 --work-lease-fence 7`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeSessions,
		RunE: func(cmd *cobra.Command, args []string) error {
			fence, err := optionalPositiveWorkLeaseFence(cmd, workLeaseFence)
			if err != nil {
				return err
			}
			if err := cfg.resolve(); err != nil {
				return err
			}
			var body any
			if fence != nil {
				body = map[string]any{"work_lease_fence": *fence}
			}
			status, b, err := cfg.do(cmd.Context(), "POST",
				"/v1/m/sessions/runs/"+agentExecPathID(args[0])+"/interrupt", body)
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
	cmd.Flags().Int64Var(&workLeaseFence, "work-lease-fence", 0, "positive work-lease fence; omitted from the request when unset")
	return cmd
}

// sessionInputMaxBytes is the local raw intake bound for session input. The
// reader takes at most one extra byte so overflow is refused before any HTTP
// call. Server work-text and JSON-expansion limits remain authoritative.
const sessionInputMaxBytes = 1 << 20

func sessionCLIUsage(msg string) error {
	return exitcode.New(exitcode.Usage, fmt.Errorf("%s", msg))
}

func optionalPositiveWorkLeaseFence(cmd *cobra.Command, value int64) (*int64, error) {
	if !cmd.Flags().Changed("work-lease-fence") {
		return nil, nil
	}
	if value <= 0 {
		return nil, sessionCLIUsage("--work-lease-fence must be a positive integer")
	}
	return &value, nil
}

func checkSessionInputBytes(b []byte) error {
	if len(b) > sessionInputMaxBytes {
		return sessionCLIUsage(fmt.Sprintf("input exceeds %d bytes", sessionInputMaxBytes))
	}
	if !utf8.Valid(b) {
		return sessionCLIUsage("input is not valid UTF-8")
	}
	return nil
}

func readSessionInputBytes(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, int64(sessionInputMaxBytes)+1))
	if err != nil {
		return nil, err
	}
	if err := checkSessionInputBytes(b); err != nil {
		return nil, err
	}
	return b, nil
}

func sessionLinePayload(cmd *cobra.Command, line string) (string, error) {
	if line == "" || line == "-" {
		b, err := readSessionInputBytes(cmd.InOrStdin())
		if err != nil {
			return "", err
		}
		return strings.TrimRight(string(b), "\n"), nil
	}
	if err := checkSessionInputBytes([]byte(line)); err != nil {
		return "", err
	}
	return line, nil
}

func sessionTextPayload(cmd *cobra.Command, text string) (string, error) {
	if text == "-" {
		b, err := readSessionInputBytes(cmd.InOrStdin())
		if err != nil {
			return "", err
		}
		text = string(b)
	} else if err := checkSessionInputBytes([]byte(text)); err != nil {
		return "", err
	}
	if strings.TrimSpace(text) == "" {
		return "", sessionCLIUsage("text input is empty")
	}
	return text, nil
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
