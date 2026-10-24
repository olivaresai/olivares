// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
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
	"github.com/olivaresai/olivares/cmd/olivares/internal/agenttoolsapi"
	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
	"github.com/olivaresai/olivares/core/driverfacts"
)

// cmd_tool.go is `olivares tool`: install the agent tools sessions run, see them,
// and sign them in. It calls the engine routes the console's AI tools page calls
// (/v1/m/agenttools/*), so a tool lands where the engine runs and under the
// engine's user, and the sign-in is the tool's own login run on the engine's host.
// `agent tool` stays the local, server-free installer for this host.

const agentToolsPath = "/v1/m/agenttools"

func toolName(driver string) string {
	if facts, ok := driverfacts.Lookup(driver); ok {
		return facts.Name
	}
	return driver
}

// signInToolWords are the words a person types for the tools that can sign in:
// each tool's name stem (its alias when it has one, so gemini for gemini-cli).
func signInToolWords() []string {
	var words []string
	for _, f := range driverfacts.All() {
		if f.SignIn != "" {
			words = append(words, f.NameStem())
		}
	}
	return words
}

// toolSignsIn reports whether the engine can run this tool's own login.
func toolSignsIn(driver string) bool {
	facts, _ := driverfacts.Lookup(driver)
	return facts.SignIn != ""
}

func newToolCmd() *cobra.Command {
	cmd := &cobra.Command{
		Annotations: map[string]string{"changes_data": "read"},
		Use:         "tool",
		Aliases:     []string{"tools"},
		Short:       "Install and sign in the agent tools sessions run (Claude Code, Codex, Grok Build, OpenCode, Gemini CLI)",
		Long: "Install an agent tool on the engine's host from its official signed release, sign it in\n" +
			"with its own login (your Claude, ChatGPT or Google account), and see what is installed.",
		Example: "  olivares tool install claude\n  olivares tool login claude\n  olivares tool ls",
	}
	cmd.AddCommand(newToolListCmd(), newToolProvidersCmd(), newToolInstallCmd(), newToolUpdateCmd(), newToolRemoveCmd(), newToolPinCmd(), newToolLoginCmd(), newToolStartCmd(), newToolStopCmd(), newToolPullCmd())
	// Business adds `ring` (a candidate version tried on one agent group first);
	// Community's hook returns nil and the group is unchanged.
	if ring := toolRingCmd(); ring != nil {
		cmd.AddCommand(ring)
	}
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
		Annotations: map[string]string{"changes_data": "read"},
		Use:         "ls",
		Aliases:     []string{"list", "status"},
		Short:       "List the agent tools on the engine's host and whether each is signed in",
		Long: "ls prints one row per agent tool: the version sessions run (or \"on this host\" when it\n" +
			"was installed outside Olivares), and whether it is installed and signed in. When one is\n" +
			"missing, the last line names the next step. Use it before you start a session.\n\n" +
			"Use --account <name> to read one existing provider account's sign-in. Find account names\n" +
			"with olivares provider account ls. You can also pass a profile reference to read its login.",
		Example: "  olivares tool ls\n  olivares tool ls -o json\n  olivares tool ls --account work",
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
			// With none ready, a key its provider refused is named: it is why.
			var refused string
			if !cmd.Flags().Changed("account") {
				var ready string
				if ready, refused = cfg.firstReadyTool(cmd.Context()); ready != "" {
					next = "olivares session start <folder>"
				}
			}
			return renderOut(cmd, func(w io.Writer) error {
				rr := renderTo(w)
				rr.Table(table)
				if refused != "" {
					rr.Line(refused)
				}
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
		Drivers  []string `json:"drivers"`
		Presence map[string]struct {
			Present bool `json:"present"`
		} `json:"presence"`
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
	for driver, presence := range inv.Presence {
		if _, exists := rows[driver]; !exists {
			rows[driver] = &toolRow{Driver: driver, Installed: presence.Present}
		}
	}
	// For tools with native sign-in, the engine's status asks the program a
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
	order := map[string]int{"claude": 0, "codex": 1, "grok": 2, "opencode": 3, "gemini-cli": 4, "ollama": 5}
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
	// Methods are the tool's login methods in the order the engine offers them.
	Methods []struct {
		ID string `json:"id"`
	} `json:"methods"`
}

// firstSignInMethod is the login method to start when none was chosen: the one the
// engine lists first, or "" when it lists none or could not be asked.
func firstSignInMethod(st toolSignInState, statusErr error) string {
	if statusErr != nil || len(st.Methods) == 0 {
		return ""
	}
	return st.Methods[0].ID
}

// toolSignInStatus asks the tool itself, through the engine, whether it is signed
// in. ok is false when this engine cannot answer (an engine without the sign-in
// routes, or a tool it cannot read).
func (c *agentClientConfig) toolSignInStatus(ctx context.Context, driver string) (toolSignInState, bool) {
	st, err := c.readToolSignInStatus(ctx, driver, "")
	return st, err == nil
}

func (c *agentClientConfig) readToolSignInStatus(ctx context.Context, driver, accountRef string) (toolSignInState, error) {
	// The own login is the organization's: the status is asked for the
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
		from    string
	)
	cmd := &cobra.Command{
		Annotations: map[string]string{"changes_data": "write"},
		Use:         "install [tool] [--from URL --version VERSION]",
		Short:       "Install an official or catalog-admitted tool on the engine's host",
		Long: "install asks the engine to resolve the tool's official release, verify it (signature or\n" +
			"published digest, per tool) and place it where sessions find it. Official sessions use it\n" +
			"without any setting or restart. Use --from URL --version VERSION for an admitted\n" +
			"third-party tool. Catalog pins its archive digest and executable before installation.",
		Example: "  olivares tool install claude\n  olivares tool install codex --version 0.159.3\n  olivares tool install --from https://github.com/example/tool.git --version 1.0.0",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			driver := ""
			if len(args) > 0 {
				driver = strings.ToLower(strings.TrimSpace(args[0]))
			}
			if (from == "") == (driver == "") {
				return sentence(exitcode.Usage, "Choose a tool name or --from with an admitted source URL")
			}
			if from != "" && (!cmd.Flags().Changed("version") || version == "latest" || version == "stable") {
				return sentence(exitcode.Usage, "--from requires a pinned --version")
			}
			if driver == "gemini" {
				driver = "gemini-cli"
			}
			requestedVersion := installRequestVersion(driver, version, cmd.Flags().Changed("version"))
			if err := cfg.resolve(); err != nil {
				return err
			}
			w := cmd.ErrOrStderr()
			request := map[string]any{"driver": driver, "version": requestedVersion}
			if from != "" {
				delete(request, "driver")
				request["from"] = from
			}
			_, job, err := cfg.installTool(cmd.Context(), request, func(plan toolInstallPlan) {
				if from != "" {
					driver = plan.Driver
				}
				fmt.Fprintf(w, "Installing %s %s (verified: %s)…\n", toolName(driver), plan.Version, plan.Verification)
			})
			if err != nil {
				return err
			}
			if job.State != "succeeded" {
				return sentence(exitcode.Err, "%s was not installed: %s", toolName(driver), job.failure())
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
	cmd.Flags().StringVar(&from, "from", "", "Git or release URL of a catalog-admitted tool")
	cfg.addFlags(cmd)
	cmd.Flags().StringVar(&version, "version", "latest", "release version, or the vendor's channel (latest; stable for Grok Build)")
	cfg.timeout = toolInstallTimeout
	cmd.Flags().Lookup("timeout").DefValue = cfg.timeout.String()
	return cmd
}

// toolInstallTimeout is how long one request of an install may take: resolving
// and verifying a release takes the engine up to 45 seconds.
const toolInstallTimeout = 2 * time.Minute

// toolInstallPlan is what the engine will install and how it checked it.
type toolInstallPlan struct {
	Driver       string `json:"driver"`
	Digest       string `json:"digest"`
	Version      string `json:"version"`
	Verification string `json:"verification"`
}

// installRequestVersion is the release a request names: the one given, or the
// console's default, which is the vendor's stable channel for Grok Build.
func installRequestVersion(driver, version string, given bool) string {
	if driver == "grok" && !given {
		return "stable"
	}
	return version
}

// installTool asks the engine to plan one install, hands the plan to announce,
// runs it and waits for the job. `olivares tool install` and the AI tools screen
// both call it, so both leave the same events in the audit ledger. A job that
// did not succeed is returned for the caller to explain.
func (c *agentClientConfig) installTool(ctx context.Context, request map[string]any, announce func(toolInstallPlan)) (toolInstallPlan, toolJob, error) {
	var plan toolInstallPlan
	status, b, err := c.do(ctx, "POST", agentToolsPath+"/plans", request, http.StatusOK, http.StatusNotFound)
	if err != nil {
		return plan, toolJob{}, err
	}
	if status != http.StatusOK {
		return plan, toolJob{}, toolHTTPErr(status, b)
	}
	if err := json.Unmarshal(b, &plan); err != nil {
		return plan, toolJob{}, err
	}
	announce(plan)
	reqID, err := uuid.NewV7()
	if err != nil {
		return plan, toolJob{}, err
	}
	status, b, err = c.do(ctx, "POST", agentToolsPath+"/installs",
		map[string]any{"plan_digest": plan.Digest, "request_id": reqID.String()}, http.StatusAccepted)
	if err != nil {
		return plan, toolJob{}, err
	}
	if status != http.StatusAccepted {
		return plan, toolJob{}, toolHTTPErr(status, b)
	}
	job, err := c.waitToolJob(ctx, b)
	return plan, job, err
}

// failure is why a job that did not succeed ended, in the engine's words.
func (j toolJob) failure() string {
	if j.Error != "" {
		return j.Error
	}
	return "the installation ended as " + j.State
}

type toolJob struct {
	ID       string          `json:"id"`
	Driver   string          `json:"driver"`
	Version  string          `json:"version"`
	State    string          `json:"state"`
	Progress string          `json:"progress"`
	Error    string          `json:"error,omitempty"`
	Receipt  json.RawMessage `json:"receipt,omitempty"`
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
	var accountName, method string
	cmd := &cobra.Command{
		Annotations: map[string]string{"changes_data": "write"},
		Use:         "login <" + strings.Join(signInToolWords(), "|") + ">",
		Short:       "Sign an agent tool in with its own login (Claude, ChatGPT, xAI, Google or OpenCode account)",
		Long: "login runs the tool's own sign-in on the engine's host and shows you its link. For\n" +
			"Claude Code or Gemini CLI, open the link, sign in, and paste the code the page shows. For Codex,\n" +
			"Grok Build and OpenCode's ChatGPT method, open the link and enter the code shown here.\n" +
			"For OpenCode's own providers, create an API key on the page shown and paste it: at a terminal\n" +
			"it is not shown, and from a pipe it is read from standard input. The tool stores it; Olivares\n" +
			"does not. The login is stored where the tool keeps it.\n\n" +
			"Use --method <id> to choose among the official login methods a tool offers; without it\n" +
			"the first one the engine lists runs (OpenCode: opencode, then opencode-go and\n" +
			"chatgpt-headless). An unknown id is refused with the valid ones (exit 2).\n\n" +
			"Use --account <name> to sign in an existing provider account. Find account names with\n" +
			"olivares provider account ls. You can also pass a profile reference to sign in that profile.",
		Example: "  olivares tool login claude\n  olivares tool login codex\n  olivares tool login grok\n" +
			"  olivares tool login opencode\n  olivares tool login opencode --method opencode-go < go-key.txt\n" +
			"  olivares tool login claude --account work\n  olivares tool login gemini --account personal",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			driver := strings.ToLower(strings.TrimSpace(args[0]))
			if driver == "gemini" {
				driver = "gemini-cli"
			}
			if !toolSignsIn(driver) {
				return sentence(exitcode.Usage, "%s has no sign-in here. Use %s.", toolName(driver), driverfacts.JoinList(signInToolWords(), "or"))
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
			// The login is the organization's own, named because the route
			// ignores the tenant selection.
			body := map[string]any{"driver": driver, "tenant_id": cfg.tenant}
			if accountRef != "" {
				body["account_ref"] = accountRef
			}
			if cmd.Flags().Changed("method") {
				body["method"] = method
			} else if first := firstSignInMethod(st, statusErr); first != "" {
				body["method"] = first
			} else if facts, _ := driverfacts.Lookup(driver); statusErr != nil && len(facts.LoginMethods) > 1 {
				fmt.Fprintf(cmd.ErrOrStderr(), "Could not read %s's login methods, so its default login runs. Choose another with --method.\n", toolName(driver))
			}
			var s toolSignIn
			err = cfg.startToolSignIn(ctx, body, &s)
			if err != nil && ctx.Err() != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "\nCancelled. A sign-in the engine had started ends by itself within 15 minutes.\n")
				return exitcode.New(exitcode.Err, nil)
			}
			var refusal *apiRefusal
			if err != nil && errors.As(err, &refusal) && refusal.status == http.StatusBadRequest && cmd.Flags().Changed("method") {
				// The engine lists the valid ids in its sentence.
				return exitcode.New(exitcode.Usage, err)
			}
			if err != nil {
				return err
			}
			// Ctrl-C, TERM or any failure below ends the login on the engine too.
			defer func() {
				if s.State == "signed_in" {
					return
				}
				canceled := cfg.cancelToolSignIn(s.ID)
				if ctx.Err() == nil {
					return // not interrupted: the error already says what happened
				}
				if canceled {
					fmt.Fprintf(cmd.ErrOrStderr(), "\nCancelled the sign-in of %s.\n", toolName(driver))
				} else {
					fmt.Fprintf(cmd.ErrOrStderr(), "\nCould not cancel the sign-in of %s on the engine; it ends by itself within 15 minutes.\n",
						toolName(driver))
				}
				err = exitcode.New(exitcode.Err, nil)
			}()
			if s.State == "starting" {
				fmt.Fprintf(out, "Starting %s sign-in...\n", toolName(driver))
			}
			flowCtx, cancelFlow := context.WithTimeout(ctx, signInWindow)
			defer cancelFlow()
			lines := toolLoginLines(flowCtx, cmd.InOrStdin())
			linkShown := false
			for {
				if !linkShown && s.State == "needs_key" && s.URL != "" {
					fmt.Fprintf(out, "Create an API key here, then paste it below:\n\n  %s\n\n", termSafe(s.URL))
					linkShown = true
				}
				if !linkShown && s.State != "starting" && s.URL != "" {
					fmt.Fprintf(out, "Open this link and sign in:\n\n  %s\n\n", termSafe(s.URL))
					if driver != "claude" && s.UserCode != "" {
						fmt.Fprintf(out, "Enter this code there: %s\n\n", termSafe(s.UserCode))
					}
					linkShown = true
				}
				switch s.State {
				case "signed_in":
					fmt.Fprintf(out, "%s is signed in.\n", toolName(driver))
					return nil
				case "failed":
					return toolSignInFailed(driver, s)
				case "needs_code":
					if s.URL == "" {
						return toolSignInFailed(driver, s)
					}
					if s.Message != "" {
						fmt.Fprintln(out, termSafe(s.Message))
					}
					fmt.Fprint(out, "Paste the code from the page: ")
					var line toolLoginLine
					select {
					case <-flowCtx.Done():
						return flowCtx.Err()
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
					err = cfg.sendToolSignInCode(flowCtx, &s, code)
				case "needs_key":
					key, kerr := readToolLoginKey(flowCtx, cmd, lines, driver)
					if kerr != nil {
						return kerr
					}
					// The key goes to the tool's own login on the engine and nowhere else.
					err = cfg.sendToolSignInCode(flowCtx, &s, key)
				default:
					if s.State != "starting" && s.URL == "" {
						return toolSignInFailed(driver, s)
					}
					select {
					case <-flowCtx.Done():
						return flowCtx.Err()
					case <-time.After(2 * time.Second):
					}
					err = cfg.readToolSignIn(flowCtx, &s)
				}
				if err != nil {
					return err
				}
			}
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().StringVar(&accountName, "account", "", "sign in an existing provider account (name or profile reference)")
	cmd.Flags().StringVar(&method, "method", "", "the tool's official login method, when it offers several (the first one the engine lists otherwise)")
	return cmd
}

// signInWindow is how long the engine keeps a tool's own login open, and so how
// long `olivares tool login` and the AI tools screen wait for it.
const signInWindow = 15 * time.Minute

// startToolSignIn asks the engine to start the tool's own login and reads its
// first state into s.
func (c *agentClientConfig) startToolSignIn(ctx context.Context, body map[string]any, s *toolSignIn) error {
	status, b, err := c.do(ctx, "POST", agentToolsPath+"/sign-in", body, http.StatusAccepted, http.StatusNotFound)
	if err != nil {
		return err
	}
	if status != http.StatusAccepted {
		return toolHTTPErr(status, b)
	}
	return json.Unmarshal(b, s)
}

// readToolSignIn reads the login's state into s, keeping what the engine leaves out.
func (c *agentClientConfig) readToolSignIn(ctx context.Context, s *toolSignIn) error {
	status, b, err := c.do(ctx, "GET", agentToolsPath+"/sign-in/"+url.PathEscape(s.ID), nil)
	return readToolSignInAnswer(s, status, b, err)
}

// sendToolSignInCode gives the login the code a person pasted from the sign-in page.
func (c *agentClientConfig) sendToolSignInCode(ctx context.Context, s *toolSignIn, code string) error {
	status, b, err := c.do(ctx, "POST", agentToolsPath+"/sign-in/"+url.PathEscape(s.ID)+"/code",
		map[string]any{"code": code}, http.StatusOK, http.StatusAccepted)
	return readToolSignInAnswer(s, status, b, err)
}

func readToolSignInAnswer(s *toolSignIn, status int, b []byte, err error) error {
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusAccepted {
		return toolHTTPErr(status, b)
	}
	return json.Unmarshal(b, s)
}

// cancelToolSignIn ends a login on the engine, within five seconds, and reports
// whether the engine confirmed it (or no longer knew it). A login nobody ends
// ends by itself within 15 minutes.
func (c *agentClientConfig) cancelToolSignIn(id string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	status, _, err := c.do(ctx, "DELETE", agentToolsPath+"/sign-in/"+url.PathEscape(id), nil,
		http.StatusOK, http.StatusNotFound)
	return err == nil && (status == http.StatusOK || status == http.StatusNotFound)
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

// readToolLoginKey reads the API key a tool's own login waits for: at a terminal
// with echo off (echo comes back once it is read, or when the login ends), from
// a pipe as its next line. It never prints the key.
func readToolLoginKey(ctx context.Context, cmd *cobra.Command, lines <-chan toolLoginLine, driver string) (string, error) {
	out, in := cmd.OutOrStdout(), cmd.InOrStdin()
	terminal := interactiveStdin(in)
	if terminal {
		restore, err := hideTerminalEcho(in)
		if err != nil {
			return "", sentence(exitcode.Usage, "This terminal cannot hide the key. Pipe it instead: olivares tool login %s < key-file", driver)
		}
		defer restore()
	}
	fmt.Fprint(out, "Paste the API key (it is not shown): ")
	var line toolLoginLine
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case line = <-lines:
	}
	fmt.Fprintln(out) // neither a hidden terminal nor a pipe echoes the Enter
	key := strings.TrimSpace(line.text)
	if key == "" {
		return "", sentence(exitcode.Usage, "No key was entered. Run olivares tool login %s again.", driver)
	}
	return key, nil
}

func toolSignInFailed(driver string, s toolSignIn) error {
	// The engine's message is the tool's reason and its one next step (#470).
	if msg := strings.TrimSpace(termSafe(s.Message)); msg != "" {
		return sentence(exitcode.Err, "%s is not signed in. %s", toolName(driver), msg)
	}
	return sentence(exitcode.Err, "%s is not signed in. The tool did not finish its sign-in. Try again: olivares tool login %s", toolName(driver), driver)
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

// toolServices are the tools that run as a service of the engine, by the name the CLI takes,
// with the sentence's name for each and the route that stops it.
var toolServices = map[string]struct{ name, stop string }{
	"ollama":    {"Ollama", "/ollama/stop"},
	"llama-cpp": {"llama.cpp", "/model-servers/llama-cpp/stop"},
	"vllm":      {"vLLM", "/model-servers/vllm/stop"},
}

// modelServerStartWait bounds how long `tool start llama-cpp|vllm` waits for the server to
// answer: it may have to download its model first.
var modelServerStartWait = 10 * time.Minute

// newToolStartCmd starts the installed Ollama, llama.cpp or vLLM as the engine's own
// service, the CLI side of AI tools › Ollama › Start (the CLI could not start it before).
func newToolStartCmd() *cobra.Command {
	var cfg agentClientConfig
	var modelRef string
	cmd := &cobra.Command{
		Annotations: map[string]string{"changes_data": "write"},
		Use:         "start <ollama|llama-cpp|vllm>",
		Short:       "Start Ollama, llama.cpp or vLLM on the engine's host as the engine's own service",
		Long: "start runs the installed Ollama, llama.cpp (llama-server) or vLLM as a service of the\n" +
			"engine, as AI tools › Ollama › Start does, and registers its endpoint in your organization's\n" +
			"Providers once it answers, so OpenCode sessions can run on its models. The engine starts it\n" +
			"again after a restart until it is stopped (olivares tool stop). llama.cpp and vLLM must be\n" +
			"installed on the engine's PATH, run on loopback only and answer a key the engine makes for\n" +
			"them; name the model with --model: for llama-cpp an absolute .gguf path or a Hugging Face\n" +
			"user/repo[:quantization], for vllm an absolute model directory or a Hugging Face user/repo.",
		Example: "  olivares tool start ollama\n" +
			"  olivares tool start llama-cpp --model /srv/models/gemma-3-1b-it-Q4_K_M.gguf\n" +
			"  olivares tool start vllm --model Qwen/Qwen3-0.6B",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			driver := strings.ToLower(strings.TrimSpace(args[0]))
			if driver == "llama-cpp" || driver == "vllm" {
				if strings.TrimSpace(modelRef) == "" {
					return sentence(exitcode.Usage, "Name the model to serve: olivares tool start %s --model <model>", driver)
				}
				if err := cfg.resolve(); err != nil {
					return err
				}
				return startModelServer(cmd, &cfg, driver, strings.TrimSpace(modelRef))
			}
			if driver != "ollama" {
				return sentence(exitcode.Usage, "Only Ollama, llama-cpp and vllm run as services. Start a session instead: olivares session start <folder>")
			}
			if modelRef != "" {
				return sentence(exitcode.Usage, "Ollama downloads its models: olivares tool pull <model>")
			}
			if err := cfg.resolve(); err != nil {
				return err
			}
			ctx := cmd.Context()
			body := map[string]any{}
			if t := strings.TrimSpace(cfg.tenant); t != "" {
				body["tenant_id"] = t
			}
			if _, _, err := cfg.do(ctx, "POST", agentToolsPath+"/ollama/start", body, http.StatusAccepted); err != nil {
				return err
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
	cmd.Flags().StringVar(&modelRef, "model", "", "llama-cpp: an absolute .gguf path or a Hugging Face user/repo[:quantization]; vllm: an absolute model directory or a Hugging Face user/repo")
	cfg.addFlags(cmd)
	return cmd
}

// startModelServer starts llama.cpp or vLLM for the organization and waits until it answers.
// Starting the server that already runs this model answers with its state, so the same
// command is how to keep waiting for a model that is still loading.
func startModelServer(cmd *cobra.Command, cfg *agentClientConfig, kind, modelRef string) error {
	ctx := cmd.Context()
	name := toolServices[kind].name
	if _, _, err := cfg.do(ctx, "POST", agentToolsPath+"/model-servers/"+kind+"/start", map[string]any{"model": modelRef, "tenant_id": cfg.tenant}, http.StatusAccepted); err != nil {
		return err
	}
	var row agenttoolsapi.ModelServerStatus
	deadline := time.Now().Add(modelServerStartWait)
	for {
		status, b, err := cfg.do(ctx, "GET", agentToolsPath+"/model-servers", nil, http.StatusOK)
		if err != nil {
			return err
		}
		var list struct {
			Servers []agenttoolsapi.ModelServerStatus `json:"servers"`
		}
		if status != http.StatusOK || json.Unmarshal(b, &list) != nil {
			return toolHTTPErr(status, b)
		}
		found := false
		for _, s := range list.Servers {
			if s.Kind == kind {
				row, found = s, true
			}
		}
		if !found {
			return sentence(exitcode.Err, "The engine does not report %s.", name)
		}
		if row.State == "running" {
			break
		}
		if row.State == "failed" || row.State == "stopped" {
			return sentence(exitcode.Err, "%s did not start: %s", name, termSafe(firstNonEmptyCLI(row.Message, row.State)))
		}
		if time.Now().After(deadline) {
			return sentence(exitcode.Err, "%s has not answered yet; it may still be loading its model. Keep waiting: olivares tool start %s --model %s", name, kind, termSafe(modelRef))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	// Running with a message means the server answers but the engine could not register it: the
	// next step would not work, so this is a failure, with the cause.
	if msg := strings.TrimSpace(row.Message); msg != "" {
		return sentence(exitcode.Err, "%s is running at %s. %s", name, termSafe(row.Endpoint), termSafe(msg))
	}
	return renderOut(cmd, func(w io.Writer) error {
		r := renderTo(w)
		r.Line(fmt.Sprintf("%s is running at %s.", name, termSafe(row.Endpoint)))
		if len(row.Models) > 0 {
			r.Line("It serves: " + termSafe(strings.Join(row.Models, ", ")) + ".")
		}
		r.Next("olivares session start <folder> --tool opencode")
		return nil
	}, row)
}

// newToolStopCmd stops a service the engine runs: Ollama, llama.cpp or vLLM. Their models
// stay on disk, and the engine does not start them again after a restart.
func newToolStopCmd() *cobra.Command {
	var cfg agentClientConfig
	cmd := &cobra.Command{
		Annotations: map[string]string{"changes_data": "write"},
		Use:         "stop <ollama|llama-cpp|vllm>",
		Short:       "Stop Ollama, llama.cpp or vLLM, the engine's own service",
		Long: "stop ends a service started with olivares tool start. Its models stay on disk, and the\n" +
			"engine does not start it again after a restart until it is started again.",
		Example: "  olivares tool stop llama-cpp",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, ok := toolServices[strings.ToLower(strings.TrimSpace(args[0]))]
			if !ok {
				return sentence(exitcode.Usage, "Only Ollama, llama-cpp and vllm run as services.")
			}
			if err := cfg.resolve(); err != nil {
				return err
			}
			status, b, err := cfg.do(cmd.Context(), "POST", agentToolsPath+svc.stop, nil, http.StatusOK)
			if err != nil {
				return err
			}
			var st map[string]any
			if status != http.StatusOK || json.Unmarshal(b, &st) != nil {
				return toolHTTPErr(status, b)
			}
			if state, _ := st["state"].(string); state != "stopped" {
				return sentence(exitcode.Err, "%s did not stop (it is %s). Try again in a moment.", svc.name, termSafe(firstNonEmptyCLI(state, "in an unknown state")))
			}
			return renderOut(cmd, func(w io.Writer) error {
				renderTo(w).Line(svc.name + " stopped.")
				return nil
			}, st)
		},
	}
	cfg.addFlags(cmd)
	return cmd
}
