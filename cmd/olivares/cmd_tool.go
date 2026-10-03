// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
)

// cmd_tool.go is `olivares tool`: install the agent tools sessions run, see them,
// and sign them in. It calls the engine routes the console's AI tools page calls
// (/v1/m/agenttools/*), so a tool lands where the engine runs and under the
// engine's user, and the sign-in is the tool's own login run on the engine's host.
// `agent tool` stays the local, server-free installer for this host.

const agentToolsPath = "/v1/m/agenttools"

// toolNames are the names people know the drivers by.
var toolNames = map[string]string{
	"claude": "Claude Code", "codex": "Codex", "grok": "Grok Build", "opencode": "OpenCode", "ollama": "Ollama",
}

func toolName(driver string) string {
	if n := toolNames[driver]; n != "" {
		return n
	}
	return driver
}

// toolSignsIn reports whether the engine can run this tool's own login.
func toolSignsIn(driver string) bool {
	return driver == "claude" || driver == "codex" || driver == "grok"
}

func newToolCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "tool",
		Aliases: []string{"tools"},
		Short:   "Install and sign in the agent tools sessions run (Claude Code, Codex, Grok Build, OpenCode)",
		Long: "Install an agent tool on the engine's host from its official signed release, sign it in\n" +
			"with its own login (your Claude or ChatGPT subscription), and see what is installed.",
		Example: "  olivares tool install claude\n  olivares tool login claude\n  olivares tool ls",
	}
	cmd.AddCommand(newToolListCmd(), newToolInstallCmd(), newToolLoginCmd(), newToolStartCmd())
	return cmd
}

// --- ls ------------------------------------------------------------------------

type toolRow struct {
	Driver      string `json:"driver"`
	AccountName string `json:"account_name,omitempty"`
	// Installed is what the engine's one resolver finds (the program a session
	// launch runs: a pin, a release Olivares installed, or the tool on PATH);
	// Version is set only for a release Olivares installed.
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
	SignedIn  *bool  `json:"signed_in,omitempty"`
	Account   string `json:"account,omitempty"`
}

func newToolListCmd() *cobra.Command {
	var cfg agentClientConfig
	var accountName string
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list", "status"},
		Short:   "List the agent tools on the engine's host and whether each is signed in",
		Long: "ls prints one row per agent tool: the version sessions run (or \"on this host\" when it\n" +
			"was installed outside Olivares), and whether it is installed and signed in. When one is\n" +
			"missing, the last line names the next step. Use it before you start a session.",
		Example: "  olivares tool ls\n  olivares tool ls -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			var rows []toolRow
			var err error
			if cmd.Flags().Changed("account") {
				account, findErr := cfg.findToolAccount(cmd.Context(), accountName)
				if findErr != nil {
					return findErr
				}
				st, readErr := cfg.readToolSignInStatus(cmd.Context(), account.Driver, account.Ref)
				if readErr != nil {
					return readErr
				}
				rows = []toolRow{{Driver: account.Driver, AccountName: account.Name, Installed: st.Installed, SignedIn: &st.SignedIn, Account: st.Account}}
			} else {
				rows, err = cfg.toolRows(cmd.Context())
			}
			if err != nil {
				return err
			}
			table := termrender.Table{Header: []string{"tool", "version", "status"}}
			next := ""
			for _, r := range rows {
				status, role := "installed", termrender.RoleOK
				version := r.Version
				if r.Installed && version == "" {
					version = "(on this host)"
				}
				switch {
				case !r.Installed:
					status, role = "not installed", termrender.RoleMuted
				case r.SignedIn != nil && *r.SignedIn && r.Account != "":
					status = "signed in as " + r.Account
				case r.SignedIn != nil && *r.SignedIn:
					status = "signed in"
				case r.SignedIn != nil:
					status, role = "not signed in", termrender.RoleWarn
					if next == "" {
						next = "olivares tool login " + r.Driver
					}
				}
				if !r.Installed && next == "" && r.Driver == "claude" {
					next = "olivares tool install claude"
				}
				label := toolName(r.Driver)
				if r.AccountName != "" {
					label += " (" + r.AccountName + ")"
					if strings.HasPrefix(next, "olivares tool login ") {
						next += " --account " + r.AccountName
					}
				}
				table.Rows = append(table.Rows, []string{label, version, status})
				table.Roles = append(table.Roles, []termrender.Role{termrender.RoleNone, termrender.RoleNone, role})
			}
			// A tool that can run a session now makes the session the next step, as in
			// `olivares` and `session start` (an OpenCode on a local model needs no login).
			if !cmd.Flags().Changed("account") {
				if _, ok := cfg.firstReadyTool(cmd.Context()); ok {
					next = "olivares session start <folder>"
				}
			}
			return renderOut(cmd, func(w io.Writer) error {
				rr := renderTo(w)
				rr.Table(table)
				rr.Next(next)
				return nil
			}, rows)
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().StringVar(&accountName, "account", "", "read an existing provider account's own login (name or profile reference)")
	return cmd
}

// toolRows is the engine's inventory (newest installed version per tool) joined
// with each tool's own answer about its login.
func (c *agentClientConfig) toolRows(ctx context.Context) ([]toolRow, error) {
	status, b, err := c.do(ctx, "GET", agentToolsPath+"/inventory", nil, http.StatusOK, http.StatusNotFound)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, toolHTTPErr(status, b)
	}
	var inv struct {
		Drivers   []string `json:"drivers"`
		Inventory struct {
			Installed []struct {
				Driver      string    `json:"driver"`
				Version     string    `json:"version"`
				State       string    `json:"state"`
				InstalledAt time.Time `json:"installed_at"`
			} `json:"installed"`
		} `json:"inventory"`
	}
	if err := json.Unmarshal(b, &inv); err != nil {
		return nil, err
	}
	newest := map[string]time.Time{}
	rows := map[string]*toolRow{}
	for _, d := range inv.Drivers {
		rows[d] = &toolRow{Driver: d}
	}
	for _, it := range inv.Inventory.Installed {
		if it.State != "installed" {
			continue
		}
		if _, ok := rows[it.Driver]; !ok {
			rows[it.Driver] = &toolRow{Driver: it.Driver}
		}
		if it.InstalledAt.After(newest[it.Driver]) || rows[it.Driver].Version == "" {
			newest[it.Driver] = it.InstalledAt
			rows[it.Driver].Version = it.Version
		}
		rows[it.Driver].Installed = true
	}
	// For Claude Code, Codex and Grok Build the engine's sign-in status asks the program a
	// session would run, so it also finds a tool on PATH (HU, R1 refresh 01).
	for d, r := range rows {
		if !toolSignsIn(d) {
			continue
		}
		if st, ok := c.toolSignInStatus(ctx, d); ok {
			r.Installed = r.Installed || st.Installed
			if st.Installed {
				signed := st.SignedIn
				r.SignedIn, r.Account = &signed, st.Account
			}
		}
	}
	out := make([]toolRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, *r)
	}
	order := map[string]int{"claude": 0, "codex": 1, "grok": 2, "opencode": 3, "ollama": 4}
	sort.Slice(out, func(i, j int) bool {
		oi, iok := order[out[i].Driver]
		oj, jok := order[out[j].Driver]
		if iok != jok {
			return iok
		}
		if oi != oj {
			return oi < oj
		}
		return out[i].Driver < out[j].Driver
	})
	return out, nil
}

type toolSignInState struct {
	Installed bool   `json:"installed"`
	SignedIn  bool   `json:"signed_in"`
	Method    string `json:"method"`
	Account   string `json:"account"`
}

// toolSignInStatus asks the tool itself, through the engine, whether it is signed
// in. ok is false when this engine cannot answer (an engine without the sign-in
// routes, or a tool it cannot read).
func (c *agentClientConfig) toolSignInStatus(ctx context.Context, driver string) (toolSignInState, bool) {
	st, err := c.readToolSignInStatus(ctx, driver, "")
	return st, err == nil
}

func (c *agentClientConfig) readToolSignInStatus(ctx context.Context, driver, accountRef string) (toolSignInState, error) {
	// The own login is the organization's (FH 036): the status is asked for the
	// tenant of the saved sign-in, by name, because the route ignores the selection.
	path := agentToolsPath + "/sign-in?driver=" + url.QueryEscape(driver) + "&tenant_id=" + url.QueryEscape(c.tenant)
	if accountRef != "" {
		path += "&account_ref=" + url.QueryEscape(accountRef)
	}
	status, b, err := c.do(ctx, "GET", path, nil)
	if err != nil {
		return toolSignInState{}, err
	}
	if status != http.StatusOK {
		return toolSignInState{}, toolHTTPErr(status, b)
	}
	var st toolSignInState
	err = json.Unmarshal(b, &st)
	return st, err
}

// --- install -------------------------------------------------------------------

func newToolInstallCmd() *cobra.Command {
	var (
		cfg     agentClientConfig
		version string
	)
	cmd := &cobra.Command{
		Use:   "install <claude|codex|grok|opencode|ollama>",
		Short: "Install an agent tool on the engine's host from its official signed release",
		Long: "install asks the engine to resolve the tool's official release, verify it (signature or\n" +
			"published digest, per tool) and place it where sessions find it. Sessions use it\n" +
			"without any setting or restart.",
		Example: "  olivares tool install claude\n  olivares tool install codex --version 0.159.3",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			driver := strings.ToLower(strings.TrimSpace(args[0]))
			requestedVersion := version
			if driver == "grok" && !cmd.Flags().Changed("version") {
				requestedVersion = "stable"
			}
			if err := cfg.resolve(); err != nil {
				return err
			}
			ctx := cmd.Context()
			w := cmd.ErrOrStderr()
			status, b, err := cfg.do(ctx, "POST", agentToolsPath+"/plans", map[string]any{"driver": driver, "version": requestedVersion}, http.StatusOK, http.StatusNotFound)
			if err != nil {
				return err
			}
			if status != http.StatusOK {
				return toolHTTPErr(status, b)
			}
			var plan struct {
				Digest       string `json:"digest"`
				Version      string `json:"version"`
				Verification string `json:"verification"`
			}
			if err := json.Unmarshal(b, &plan); err != nil {
				return err
			}
			fmt.Fprintf(w, "Installing %s %s (verified: %s)…\n", toolName(driver), plan.Version, plan.Verification)
			reqID, err := uuid.NewV7()
			if err != nil {
				return err
			}
			status, b, err = cfg.do(ctx, "POST", agentToolsPath+"/installs",
				map[string]any{"plan_digest": plan.Digest, "request_id": reqID.String()}, http.StatusAccepted)
			if err != nil {
				return err
			}
			if status != http.StatusAccepted {
				return toolHTTPErr(status, b)
			}
			job, err := cfg.waitToolJob(ctx, b)
			if err != nil {
				return err
			}
			if job.State != "succeeded" {
				msg := job.Error
				if msg == "" {
					msg = "the installation ended as " + job.State
				}
				return sentence(exitcode.Err, "%s was not installed: %s", toolName(driver), msg)
			}
			return renderOut(cmd, func(out io.Writer) error {
				_, err := fmt.Fprintf(out, "Installed %s %s.\n", toolName(driver), job.Version)
				switch {
				case err != nil:
				case toolSignsIn(driver):
					renderTo(out).Next("olivares tool login " + driver)
				case driver == "ollama":
					renderTo(out).Next("olivares tool start ollama")
				}
				return err
			}, job)
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().StringVar(&version, "version", "latest", "release version, or the vendor's channel (latest; stable for Grok Build)")
	// Resolving and verifying a release takes the engine up to 45 seconds.
	cfg.timeout = 2 * time.Minute
	cmd.Flags().Lookup("timeout").DefValue = cfg.timeout.String()
	return cmd
}

type toolJob struct {
	ID       string `json:"id"`
	Driver   string `json:"driver"`
	Version  string `json:"version"`
	State    string `json:"state"`
	Progress string `json:"progress"`
	Error    string `json:"error,omitempty"`
}

// waitToolJob polls an install job until it is no longer queued or running.
func (c *agentClientConfig) waitToolJob(ctx context.Context, first []byte) (toolJob, error) {
	var job toolJob
	if err := json.Unmarshal(first, &job); err != nil {
		return job, err
	}
	for job.State == "queued" || job.State == "running" || job.State == "" {
		select {
		case <-ctx.Done():
			return job, ctx.Err()
		case <-time.After(time.Second):
		}
		status, b, err := c.do(ctx, "GET", agentToolsPath+"/jobs/"+url.PathEscape(job.ID), nil)
		if err != nil {
			return job, err
		}
		if status != http.StatusOK {
			return job, toolHTTPErr(status, b)
		}
		if err := json.Unmarshal(b, &job); err != nil {
			return job, err
		}
	}
	return job, nil
}

// --- login ---------------------------------------------------------------------

type toolSignIn struct {
	ID       string `json:"id"`
	Driver   string `json:"driver"`
	State    string `json:"state"`
	URL      string `json:"url"`
	UserCode string `json:"user_code"`
	Message  string `json:"message"`
}

func newToolLoginCmd() *cobra.Command {
	var cfg agentClientConfig
	var accountName string
	cmd := &cobra.Command{
		Use:   "login <claude|codex|grok>",
		Short: "Sign an agent tool in with its own login (Claude, ChatGPT or xAI account)",
		Long: "login runs the tool's own sign-in on the engine's host and shows you its link. For\n" +
			"Claude Code, open the link, sign in, and paste the code the page shows. For Codex and\n" +
			"Grok Build, open the link and enter the code shown here. The login is stored where the\n" +
			"tool keeps it.",
		Example: "  olivares tool login claude\n  olivares tool login codex\n  olivares tool login grok",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			driver := strings.ToLower(strings.TrimSpace(args[0]))
			if !toolSignsIn(driver) {
				return sentence(exitcode.Usage, "%s has no sign-in here. Use claude, codex or grok.", toolName(driver))
			}
			if err := cfg.resolve(); err != nil {
				return err
			}
			accountRef := ""
			if cmd.Flags().Changed("account") {
				account, err := cfg.findToolAccount(cmd.Context(), accountName)
				if err != nil {
					return err
				}
				if account.Driver != driver {
					return sentence(exitcode.Conflict, "This account uses %s. Run olivares tool login %s --account %s.", toolName(account.Driver), account.Driver, termSafe(accountName))
				}
				accountRef = account.Ref
			}
			// Ctrl-C or TERM must also end the login on the engine, or the tool's own login
			// process keeps waiting there: the signal cancels this context instead of
			// killing the CLI, and the deferred cancel below runs.
			ctx, stop := toolLoginSignals(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			out := cmd.OutOrStdout()
			st, statusErr := cfg.readToolSignInStatus(ctx, driver, accountRef)
			if accountRef != "" && statusErr != nil {
				return statusErr
			}
			if statusErr == nil && st.SignedIn {
				who := ""
				if st.Account != "" {
					who = " as " + st.Account
				}
				_, err := fmt.Fprintf(out, "%s is already signed in%s.\n", toolName(driver), who)
				return err
			}
			// The login is the organization's own (FH 036), named because the route
			// ignores the tenant selection.
			body := map[string]any{"driver": driver, "tenant_id": cfg.tenant}
			if accountRef != "" {
				body["account_ref"] = accountRef
			}
			status, b, err := cfg.do(ctx, "POST", agentToolsPath+"/sign-in", body, http.StatusAccepted, http.StatusNotFound)
			if err != nil && ctx.Err() != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "\nCancelled. A sign-in the engine had started ends by itself within 15 minutes.\n")
				return exitcode.New(exitcode.Err, nil)
			}
			if err != nil {
				return err
			}
			if status != http.StatusAccepted {
				return toolHTTPErr(status, b)
			}
			var s toolSignIn
			if err := json.Unmarshal(b, &s); err != nil {
				return err
			}
			// Ctrl-C, TERM or any failure below ends the login on the engine too.
			defer func() {
				if s.State == "signed_in" {
					return
				}
				dctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				status, _, derr := cfg.do(dctx, "DELETE", agentToolsPath+"/sign-in/"+url.PathEscape(s.ID), nil,
					http.StatusOK, http.StatusNotFound)
				if ctx.Err() == nil {
					return // not interrupted: the error already says what happened
				}
				if derr == nil && (status == http.StatusOK || status == http.StatusNotFound) {
					fmt.Fprintf(cmd.ErrOrStderr(), "\nCancelled the sign-in of %s.\n", toolName(driver))
				} else {
					fmt.Fprintf(cmd.ErrOrStderr(), "\nCould not cancel the sign-in of %s on the engine; it ends by itself within 15 minutes.\n",
						toolName(driver))
				}
				err = exitcode.New(exitcode.Err, nil)
			}()
			if s.URL == "" {
				return toolSignInFailed(driver, s)
			}
			fmt.Fprintf(out, "Open this link and sign in:\n\n  %s\n\n", termSafe(s.URL))
			if driver != "claude" && s.UserCode != "" {
				fmt.Fprintf(out, "Enter this code there: %s\n\n", termSafe(s.UserCode))
			}
			lines := toolLoginLines(ctx, cmd.InOrStdin())
			for {
				switch s.State {
				case "signed_in":
					fmt.Fprintf(out, "%s is signed in.\n", toolName(driver))
					return nil
				case "failed":
					return toolSignInFailed(driver, s)
				case "needs_code":
					if s.Message != "" {
						fmt.Fprintln(out, termSafe(s.Message))
					}
					fmt.Fprint(out, "Paste the code from the page: ")
					var line toolLoginLine
					select {
					case <-ctx.Done():
						return ctx.Err()
					case line = <-lines:
					}
					// A terminal echoes the Enter that ends the code; a pipe does not, and the
					// next sentence would share the prompt's line (CLI audit of 09b, item 13).
					if !interactiveStdin(cmd.InOrStdin()) {
						fmt.Fprintln(out)
					}
					code, rerr := strings.TrimSpace(line.text), line.err
					if code == "" {
						if rerr != nil {
							return sentence(exitcode.Usage, "No code was entered. Run olivares tool login %s again.", driver)
						}
						continue
					}
					status, b, err = cfg.do(ctx, "POST", agentToolsPath+"/sign-in/"+url.PathEscape(s.ID)+"/code", map[string]any{"code": code}, http.StatusOK, http.StatusAccepted)
				default:
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-time.After(2 * time.Second):
					}
					status, b, err = cfg.do(ctx, "GET", agentToolsPath+"/sign-in/"+url.PathEscape(s.ID), nil)
				}
				if err != nil {
					return err
				}
				if status != http.StatusOK && status != http.StatusAccepted {
					return toolHTTPErr(status, b)
				}
				if err := json.Unmarshal(b, &s); err != nil {
					return err
				}
			}
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().StringVar(&accountName, "account", "", "sign in an existing provider account (name or profile reference)")
	return cmd
}

type toolAccount struct{ Ref, Name, Driver string }

// findToolAccount resolves an existing tenant account, never creates one. Names
// can repeat across execution environments, so a name must match exactly once.
// A profile reference also selects an existing profile nobody has named.
func (c *agentClientConfig) findToolAccount(ctx context.Context, name string) (toolAccount, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return toolAccount{}, sentence(exitcode.Usage, "Name an existing provider account with --account.")
	}
	if strings.HasPrefix(name, "ppf_") {
		status, b, err := c.do(ctx, "GET", "/v1/m/sessions/provider-profiles/"+url.PathEscape(name), nil)
		if err != nil {
			return toolAccount{}, err
		}
		if status != http.StatusOK {
			return toolAccount{}, httpErr(status, b)
		}
		var profile struct {
			Ref    string `json:"profile_ref"`
			Driver string `json:"driver"`
		}
		if err := json.Unmarshal(b, &profile); err != nil {
			return toolAccount{}, err
		}
		if profile.Ref != name || profile.Driver == "" {
			return toolAccount{}, sentence(exitcode.Server, "The engine did not return the selected provider profile.")
		}
		return toolAccount{Ref: profile.Ref, Name: name, Driver: profile.Driver}, nil
	}
	var found toolAccount
	cursor := ""
	seen := map[string]bool{}
	for {
		q := url.Values{"limit": []string{"200"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		status, b, err := c.do(ctx, "GET", providerAccountsPath+"?"+q.Encode(), nil)
		if err != nil {
			return toolAccount{}, err
		}
		if status != http.StatusOK {
			return toolAccount{}, httpErr(status, b)
		}
		var page struct {
			Items []struct {
				Ref    string `json:"account_ref"`
				Name   string `json:"name"`
				Driver string `json:"driver"`
			} `json:"items"`
			HasMore bool   `json:"has_more"`
			Cursor  string `json:"cursor"`
		}
		if err := json.Unmarshal(b, &page); err != nil {
			return toolAccount{}, err
		}
		for _, account := range page.Items {
			if account.Name != name {
				continue
			}
			if account.Ref == "" || account.Driver == "" {
				return toolAccount{}, sentence(exitcode.Server, "The engine did not return a complete provider account.")
			}
			if found.Ref != "" {
				return toolAccount{}, sentence(exitcode.Conflict, "More than one account is named %q. Use its profile reference with --account.", termSafe(name))
			}
			found = toolAccount{Ref: account.Ref, Name: account.Name, Driver: account.Driver}
		}
		if !page.HasMore {
			break
		}
		if page.Cursor == "" || seen[page.Cursor] {
			return toolAccount{}, sentence(exitcode.Server, "The account list could not be read completely. Use a profile reference with --account.")
		}
		cursor = page.Cursor
		seen[cursor] = true
	}
	if found.Ref == "" {
		return toolAccount{}, sentence(exitcode.NotFound, "No provider account is named %q. List them: olivares provider account ls", termSafe(name))
	}
	return found, nil
}

// toolLoginSignals is signal.NotifyContext; a test wraps it to know when the CLI
// listens.
var toolLoginSignals = signal.NotifyContext

type toolLoginLine struct {
	text string
	err  error
}

// toolLoginLines reads stdin line by line in the background, so a signal can end a
// login that is waiting for a pasted code. It stops sending when ctx ends.
func toolLoginLines(ctx context.Context, r io.Reader) <-chan toolLoginLine {
	ch := make(chan toolLoginLine)
	go func() {
		in := bufio.NewReader(r)
		for {
			text, err := in.ReadString('\n')
			select {
			case ch <- toolLoginLine{text: text, err: err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return ch
}

func toolSignInFailed(driver string, s toolSignIn) error {
	msg := strings.TrimSpace(termSafe(s.Message))
	if msg == "" {
		msg = "The tool did not finish its sign-in."
	}
	return sentence(exitcode.Err, "%s is not signed in. %s Try again: olivares tool login %s", toolName(driver), msg, driver)
}

// toolHTTPErr is httpErr with the two refusals a person meets here put plainly: an
// engine that has no such route yet, and a session that is not a system
// administrator's.
func toolHTTPErr(status int, b []byte) error {
	switch status {
	case http.StatusNotFound:
		return sentence(exitcode.NotFound, "This engine cannot do this yet (it is older than this CLI). Upgrade the engine: olivares upgrade")
	case http.StatusForbidden:
		var env apiErrorEnvelope
		if json.Unmarshal(b, &env) == nil && env.Error.Message != "" {
			return sentence(exitcode.Auth, "%s", env.Error.Message)
		}
	}
	return httpErr(status, b)
}

// ollamaStartWait bounds how long `tool start ollama` waits for the service to answer.
var ollamaStartWait = 75 * time.Second

// newToolStartCmd starts the installed Ollama as the engine's own service, the CLI
// side of AI tools › Ollama › Start (Root on FH 108: the CLI could not start it).
func newToolStartCmd() *cobra.Command {
	var cfg agentClientConfig
	cmd := &cobra.Command{
		Use:   "start <tool>",
		Short: "Start Ollama on the engine's host as the engine's own service",
		Long: "start runs the installed Ollama as a service of the engine, as AI tools › Ollama ›\n" +
			"Start does, and registers its endpoint in your organization's Providers once it\n" +
			"answers, so OpenCode sessions can run on its models. The engine starts it again after a\n" +
			"restart until it is stopped in AI tools. Ollama is the only tool that runs as a service.",
		Example: "  olivares tool start ollama",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			driver := strings.ToLower(strings.TrimSpace(args[0]))
			if driver != "ollama" {
				return sentence(exitcode.Usage, "Only Ollama runs as a service. Start a session instead: olivares session start <folder>")
			}
			if err := cfg.resolve(); err != nil {
				return err
			}
			ctx := cmd.Context()
			body := map[string]any{}
			if t := strings.TrimSpace(cfg.tenant); t != "" {
				body["tenant_id"] = t
			}
			status, b, err := cfg.do(ctx, "POST", agentToolsPath+"/ollama/start", body, http.StatusAccepted)
			if err != nil {
				return err
			}
			if status != http.StatusAccepted {
				return toolHTTPErr(status, b)
			}
			var st struct {
				State    string   `json:"state"`
				Message  string   `json:"message"`
				Endpoint string   `json:"endpoint"`
				Models   []string `json:"models"`
			}
			deadline := time.Now().Add(ollamaStartWait)
			for {
				status, b, err := cfg.do(ctx, "GET", agentToolsPath+"/ollama", nil, http.StatusOK)
				if err != nil {
					return err
				}
				if status != http.StatusOK || json.Unmarshal(b, &st) != nil {
					return toolHTTPErr(status, b)
				}
				if st.State == "running" {
					break
				}
				if st.State == "failed" || st.State == "stopped" {
					return sentence(exitcode.Err, "Ollama did not start: %s", termSafe(firstNonEmptyCLI(st.Message, st.State)))
				}
				if time.Now().After(deadline) {
					return sentence(exitcode.Err, "Ollama has not answered yet. See it: olivares tool ls")
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(500 * time.Millisecond):
				}
			}
			return renderOut(cmd, func(w io.Writer) error {
				r := renderTo(w)
				r.Line(fmt.Sprintf("Ollama is running at %s.", termSafe(st.Endpoint)))
				if msg := strings.TrimSpace(st.Message); msg != "" {
					r.Line(termSafe(msg))
				}
				if len(st.Models) == 0 {
					r.Line("It holds no model yet: download one in AI tools › Ollama, then start a session.")
				}
				r.Next("olivares session start <folder>")
				return nil
			}, st)
		},
	}
	cfg.addFlags(cmd)
	return cmd
}
