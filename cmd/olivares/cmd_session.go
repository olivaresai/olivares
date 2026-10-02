// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
)

// cmd_session.go is `olivares session`: the one path a person takes to work with an
// agent session. It is a client of the SAME endpoints `agent session` calls
// (/v1/m/sessions/runs*, /workspaces, /provider-profiles); what it adds is the part a
// person should not have to do by hand:
//
//   - a session is named by its NAME or its id, everywhere;
//   - `start` takes a folder and an optional first prompt, registers the folder and
//     the tool's profile when they are missing, and gives the session a unique name;
//   - `send` takes plain text for every tool: a Claude run gets the user frame the
//     console sends (web/src/features/agentops/session-turn.ts), any other driver
//     gets `text`, by the same rule (provider-contract.ts runInputMode);
//   - `follow` renders the stream the way the console does, and `-o json` keeps the
//     raw frames.
//
// Measured on 2026-10-01 before this file existed (CLX/CLI-MAP.md): `olivares session`
// was an unknown command, `--text` to a Claude run was a 400, attach printed raw
// NDJSON, get/stop/resume printed the whole run JSON, and a session could only be
// named by its UUID.

// sessionRunsPath and sessionWorkspacesPath are the endpoints this file speaks to.
const (
	sessionRunsPath       = "/v1/m/sessions/runs"
	sessionWorkspacesPath = "/v1/m/sessions/workspaces"
	// sessionListLimit bounds the one list read a name lookup makes. The API sorts
	// runs newest first, so a name resolves to its newest session.
	sessionListLimit = 500
)

// runRefPattern is the shape of a run reference (a UUID). Anything else is a name.
var runRefPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func newSessionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "session",
		Aliases: []string{"sessions"},
		Short:   "Start, follow, send to, stop and resume agent sessions",
		Long: "Run an agent tool (Claude Code, Codex, Grok Build, OpenCode) in a folder on the engine's\n" +
			"host and talk to it from here. Sessions keep running when this terminal closes. Refer to\n" +
			"a session by its name or its id.",
		Example: "  olivares session start . \"fix the failing test\"\n" +
			"  olivares session send my-app \"now run the linter\"\n" +
			"  olivares session follow my-app\n" +
			"  olivares session stop my-app",
	}
	cmd.AddCommand(
		newSessionStartCmd(),
		newSessionSendCmd(),
		newSessionFollowCmd(),
		newSessionListCmd(),
		newSessionShowCmd(),
		newSessionLifecycleCmd("stop", "Stop a session", "/stop", "Stopped"),
		newSessionLifecycleCmd("resume", "Resume a stopped session", "/resume", "Resumed"),
		newSessionInterruptCmd(),
		newSessionRemoveCmd(),
		newSessionEventsCmd(),
	)
	return cmd
}

// --- start -------------------------------------------------------------------

func newSessionStartCmd() *cobra.Command {
	var (
		cfg                       agentClientConfig
		tool, name, profile       string
		model, effort, permission string
		dlp                       string
		detach                    bool
	)
	cmd := &cobra.Command{
		Use:   "start [folder] [prompt]",
		Short: "Start an agent session in a folder, optionally with a first prompt",
		Long: "start runs the agent tool in the folder (default: the current folder) and names the\n" +
			"session after it. With a prompt, it sends the prompt and shows the reply.\n\n" +
			"The first time, it registers the folder for sessions; a folder it registers carries no\n" +
			"DLP label unless you pass --dlp. Without --profile the engine picks how the tool runs,\n" +
			"as the console does (its own login, or a key or local model from Providers), and one\n" +
			"line says which.",
		Example: "  olivares session start\n" +
			"  olivares session start ~/code/my-app \"explain this repository\"\n" +
			"  olivares session start . --tool codex --name review",
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			folder := "."
			if len(args) > 0 {
				folder = args[0]
			}
			root, err := sessionFolder(folder)
			if err != nil {
				return err
			}
			tool = strings.ToLower(strings.TrimSpace(tool))
			if driverConfigHome(tool, "/") == "" {
				return sentence(exitcode.Usage, "Unknown tool %q. Use claude, codex, grok or opencode.", tool)
			}
			if _, ok := permissionModes[permission]; !ok {
				return sentence(exitcode.Usage, "Unknown permission %q. Use edits-and-commands, edits-only, read-only or ask.", permission)
			}
			if err := cfg.resolve(); err != nil {
				return err
			}
			ctx := cmd.Context()
			// The profile first: a start the engine refuses registers no folder.
			profileRef, err := cfg.toolProfile(ctx, cmd.ErrOrStderr(), tool, profile)
			if err != nil {
				return err
			}
			// The preset next, for the same reason: edits-and-commands is the engine's
			// built-in template, and a start without it could run under the profile's
			// stored mode instead of the one the person chose (SR2 report 194).
			templateID := ""
			if permission == "edits-and-commands" {
				if templateID, err = cfg.builtinTemplate(ctx, editsAndCommandsTemplate); err != nil {
					return err
				}
			}
			ws, err := cfg.folderWorkspace(ctx, root, dlp)
			if err != nil {
				return err
			}
			base := strings.TrimSpace(name)
			if base == "" {
				base = filepath.Base(root)
			}
			runName, err := cfg.freeSessionName(ctx, base)
			if err != nil {
				return err
			}
			body := map[string]any{
				"name": runName, "transport": "stream-json", "permission_mode": "",
				"effort": effort, "model": model, "workspace_ref": str(ws, "workspace_ref"),
				"isolation": "native", "provider_profile_ref": profileRef,
			}
			// The preset goes with every tool (HU 043): the engine applies it in the tool's
			// own settings, or refuses one the tool cannot honour with a sentence printed as
			// is. Sent only for Claude Code, Codex, Grok and OpenCode ran with "default".
			body["permission_mode"] = permissionModes[permission]
			if templateID != "" {
				body["template_id"] = templateID
			}
			// 202: the launch needs a person's approval; the session exists and waits.
			status, b, err := cfg.do(ctx, "POST", sessionRunsPath, body, http.StatusCreated, http.StatusAccepted)
			if err != nil {
				return err
			}
			if status != http.StatusCreated && status != http.StatusAccepted {
				return httpErr(status, b)
			}
			run := map[string]any{}
			if err := json.Unmarshal(b, &run); err != nil {
				return err
			}
			if str(run, "state") == sessionWaitingApproval {
				prompted := len(args) > 1 && strings.TrimSpace(args[1]) != ""
				return renderOut(cmd, func(w io.Writer) error {
					return printSessionWaitsForApproval(w, run, cfg.approvalsPage(), prompted)
				}, run)
			}
			if err := renderOut(cmd, func(w io.Writer) error {
				return printSessionStarted(w, run)
			}, run); err != nil {
				return err
			}
			if len(args) < 2 || strings.TrimSpace(args[1]) == "" {
				return nil
			}
			if outputIsJSON(cmd) {
				return cfg.postTurn(cmd.Context(), run, args[1])
			}
			return cfg.sendTurn(cmd, run, args[1], detach)
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().StringVar(&tool, "tool", "claude", "agent tool: claude, codex, grok or opencode")
	cmd.Flags().StringVar(&name, "name", "", "session name (default: the folder's name; -2, -3 … when taken)")
	cmd.Flags().StringVar(&profile, "profile", "", "provider profile to launch under (default: the one the engine picks, as in the console)")
	cmd.Flags().StringVar(&model, "model", "", "model alias or id (default: the tool's own)")
	cmd.Flags().StringVar(&effort, "effort", "", "low|medium|high|xhigh|max")
	cmd.Flags().StringVar(&permission, "permission", "edits-and-commands",
		"what the agent may do without asking: edits-and-commands, edits-only, read-only, or ask")
	cmd.Flags().StringVar(&dlp, "dlp", "off", "DLP posture for a folder this command registers: off, label or deny")
	cmd.Flags().BoolVar(&detach, "detach", false, "with a prompt, send it and return without showing the reply")
	_ = cmd.RegisterFlagCompletionFunc("tool", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"claude", "codex", "grok", "opencode"}, cobra.ShellCompDirectiveNoFileComp
	})
	_ = cmd.RegisterFlagCompletionFunc("permission", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"edits-and-commands", "edits-only", "read-only", "ask"}, cobra.ShellCompDirectiveNoFileComp
	})
	_ = cmd.RegisterFlagCompletionFunc("dlp", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"off", "label", "deny"}, cobra.ShellCompDirectiveNoFileComp
	})
	return cmd
}

// sessionFolder turns the folder argument into the absolute path the engine
// registers. A relative path only means something on this host, so it must exist
// here; an absolute path that does not exist here is passed on, because the engine
// may run on another host and it checks the path itself.
func sessionFolder(arg string) (string, error) {
	p := strings.TrimSpace(arg)
	if p == "" {
		p = "."
	}
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p[2:])
		}
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", exitcode.New(exitcode.Usage, err)
	}
	info, statErr := os.Stat(abs)
	switch {
	case statErr == nil && !info.IsDir():
		return "", sentence(exitcode.Usage, "%s is a file, not a folder.", abs)
	case statErr != nil && !filepath.IsAbs(p):
		return "", sentence(exitcode.Usage,
			"No folder named %q here. Give the folder first, then the prompt: olivares session start . %q", arg, arg)
	}
	return abs, nil
}

// folderWorkspace returns the active workspace registered at root, registering it
// when there is none.
func (c *agentClientConfig) folderWorkspace(ctx context.Context, root, dlp string) (map[string]any, error) {
	status, b, err := c.do(ctx, "GET", sessionWorkspacesPath, nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, httpErr(status, b)
	}
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(b, &page); err != nil {
		return nil, err
	}
	for _, ws := range page.Items {
		if str(ws, "root_path") == root && str(ws, "state") == "active" {
			return ws, nil
		}
	}
	body := map[string]any{
		"root_path": root, "name": filepath.Base(root), "mount_mode": "rw",
		"container_target": "/workspace", "dlp_mode": dlp,
	}
	status, b, err = c.do(ctx, "POST", sessionWorkspacesPath, body, http.StatusCreated)
	if err != nil {
		return nil, err
	}
	if status != http.StatusCreated {
		return nil, httpErr(status, b)
	}
	ws := map[string]any{}
	return ws, json.Unmarshal(b, &ws)
}

// toolProfile returns the profile a session of this tool launches under: the one
// named by --profile, else the one the engine resolves for the tool (the same choice
// the console makes), and says which in one line.
func (c *agentClientConfig) toolProfile(ctx context.Context, stderr io.Writer, tool, explicit string) (string, error) {
	if ref := strings.TrimSpace(explicit); ref != "" {
		return ref, nil
	}
	if driverConfigHome(tool, "/") == "" {
		return "", sentence(exitcode.Usage, "Unknown tool %q. Use claude, codex, grok or opencode.", tool)
	}
	// HU 030: the engine chooses, as for the console (provider-profiles/resolve, FH 026):
	// the tool's own login when it is signed in, else a Providers key or local model it
	// can use; it reuses or creates the profile. The CLI keeps no rule of its own.
	status, b, err := c.do(ctx, "POST", profilesPath+"/resolve", map[string]any{"driver": tool},
		http.StatusOK, http.StatusConflict, http.StatusServiceUnavailable, http.StatusNotFound)
	if err != nil {
		return "", err
	}
	switch {
	case status == http.StatusNotFound:
		return "", toolHTTPErr(status, b)
	case status != http.StatusOK:
		// 409: nothing can run the tool; 503: the node cannot read its sign-in now
		// (FH 032). The engine's sentence is printed as it is (exit 5 or 6), and with
		// -o json it keeps its status and code, like every engine refusal.
		return "", httpErr(status, b)
	}
	var res struct {
		Profile  map[string]any `json:"profile"`
		Reason   string         `json:"reason"`
		Provider struct {
			Kind        string `json:"kind"`
			DisplayName string `json:"display_name"`
		} `json:"provider"`
	}
	if err := json.Unmarshal(b, &res); err != nil {
		return "", err
	}
	ref := str(res.Profile, "profile_ref")
	if ref == "" {
		return "", sentence(exitcode.Server, "The engine chose no profile for %s. Name one: olivares session start --profile <profile>", toolName(tool))
	}
	name := termSafe(str(res.Profile, "display_name"))
	if name == "" {
		name = toolName(tool)
	}
	provider := termSafe(res.Provider.DisplayName)
	if res.Reason == "api_key" && provider != "" {
		// The engine names a profile it creates after its provider ("Claude Code
		// (Anthropic test)"); the line names the provider once.
		name = strings.TrimSuffix(name, " ("+provider+")")
	}
	switch {
	case res.Reason == "api_key" && res.Provider.Kind == "ollama":
		fmt.Fprintf(stderr, "Using %s (local model %s)\n", name, provider)
	case res.Reason == "api_key":
		fmt.Fprintf(stderr, "Using %s (API key %s)\n", name, provider)
	default:
		fmt.Fprintf(stderr, "Using %s (own login)\n", name)
	}
	return ref, nil
}

// The permission choices of a Claude Code session, as the console's New session
// offers them (FH, e4f53b2a): the default is the engine's built-in "Edits and
// commands" template (an allowlist under dontAsk, so the agent can edit and run
// commands and every other tool is refused); the others are plain modes.
var permissionModes = map[string]string{
	"edits-and-commands": "", "edits-only": "acceptEdits", "read-only": "plan", "ask": "default",
}

const editsAndCommandsTemplate = "Edits and commands"

// builtinTemplate returns the id of the engine's built-in session template with this
// name. A list the engine refuses, or one without the template, is an error that names
// the preset: the start must not go on without it (SR2 report 194).
func (c *agentClientConfig) builtinTemplate(ctx context.Context, name string) (string, error) {
	// do refuses anything but 200 with the engine's own sentence; the preset is named
	// around it, and its exit code is kept.
	_, b, err := c.do(ctx, "GET", "/v1/m/sessions/templates?builtin=true&limit=100", nil)
	if err != nil {
		return "", fmt.Errorf("--permission edits-and-commands needs the engine's built-in %q template, "+
			"and the engine did not list its templates, so the session was not started: %w", name, err)
	}
	var page struct {
		Items []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Builtin bool   `json:"builtin"`
		} `json:"items"`
	}
	if err := json.Unmarshal(b, &page); err != nil {
		return "", fmt.Errorf("read the engine's session templates: %w", err)
	}
	for _, t := range page.Items {
		if t.Builtin && t.Name == name {
			return t.ID, nil
		}
	}
	return "", sentence(exitcode.NotFound, "--permission edits-and-commands needs the engine's built-in %q template, "+
		"and this engine has none, so the session was not started. Choose --permission edits-only, read-only or ask.", name)
}

// freeSessionName returns base, or base-2, base-3 … when a session that is not
// cleaned already carries the name: a name must pick one session.
func (c *agentClientConfig) freeSessionName(ctx context.Context, base string) (string, error) {
	runs, err := c.listRuns(ctx)
	if err != nil {
		return "", err
	}
	taken := map[string]bool{}
	for _, r := range runs {
		if str(r, "state") != "cleaned" {
			taken[str(r, "name")] = true
		}
	}
	name := base
	for i := 2; taken[name]; i++ {
		name = fmt.Sprintf("%s-%d", base, i)
	}
	return name, nil
}

// sessionWaitingApproval is the state of a run whose launch waits for a person.
const sessionWaitingApproval = "waiting_approval"

// approvalsPage is the console page where an approver accepts or rejects a launch;
// the console is served by the engine itself.
func (c *agentClientConfig) approvalsPage() string {
	return strings.TrimRight(c.server, "/") + "/permissions?tab=approvals"
}

func printSessionWaitsForApproval(w io.Writer, run map[string]any, page string, prompted bool) error {
	name := sessionLabel(run)
	_, err := fmt.Fprintf(w, "%s (%s) waits for approval before it starts, in %s.\n  An approver decides in the console: %s\n"+
		"  then: olivares session send %s \"<text>\"\n",
		name, sessionDriver(run), termSafe(str(run, "workspace_path")), page, shellWord(name))
	if err == nil && prompted {
		_, err = fmt.Fprintf(w, "Your message was not sent: send it once %s is running.\n", name)
	}
	return err
}

func printSessionStarted(w io.Writer, run map[string]any) error {
	name := sessionLabel(run)
	_, err := fmt.Fprintf(w, "Started %s (%s) in %s\n  send:    olivares session send %s \"<text>\"\n"+
		"  follow:  olivares session follow %s\n  stop:    olivares session stop %s\n",
		name, sessionDriver(run), termSafe(str(run, "workspace_path")), shellWord(name), shellWord(name), shellWord(name))
	return err
}

// --- send and follow ---------------------------------------------------------

func newSessionSendCmd() *cobra.Command {
	var (
		cfg    agentClientConfig
		detach bool
	)
	cmd := &cobra.Command{
		Use:   "send <session> [text]",
		Short: "Send a message to a session and show the reply",
		Long: "send gives the session one turn of input. Without text, it reads the message from\n" +
			"standard input. For Claude Code and Codex it shows the reply until the turn ends; for\n" +
			"other tools it returns once the engine accepts the message (follow shows the output).",
		Example: "  olivares session send my-app \"run the tests and fix what fails\"\n" +
			"  cat prompt.md | olivares session send my-app",
		Args:              cobra.RangeArgs(1, 2),
		ValidArgsFunction: completeSessions,
		RunE: func(cmd *cobra.Command, args []string) error {
			text := ""
			if len(args) > 1 {
				text = args[1]
			} else {
				b, err := readSessionInputBytes(cmd.InOrStdin())
				if err != nil {
					return err
				}
				text = string(b)
			}
			if strings.TrimSpace(text) == "" {
				return sentence(exitcode.Usage, "Say what to send: olivares session send %s \"<text>\"", shellWord(args[0]))
			}
			if err := checkSessionInputBytes([]byte(text)); err != nil {
				return err
			}
			if err := cfg.resolve(); err != nil {
				return err
			}
			run, err := cfg.findSession(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return cfg.sendTurn(cmd, run, text, detach || outputIsJSON(cmd))
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().BoolVar(&detach, "detach", false, "return once the message is accepted, without showing the reply")
	return cmd
}

// sessionTurnBody is the console's rule (web/src/features/agentops/session-turn.ts):
// a run whose driver is claude (or that has no driver, a legacy run) takes a raw
// stream-json line, so the sentence is wrapped as a user frame; any other driver
// owns a protocol and takes the sentence as `text`.
func sessionTurnBody(run map[string]any, text string) (map[string]any, error) {
	if d := strings.TrimSpace(str(run, "provider_driver")); d != "" && d != "claude" {
		return map[string]any{"text": text}, nil
	}
	line, err := json.Marshal(map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": text},
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"line": string(line)}, nil
}

// sendable says why a run cannot take input now, before anything is attached or sent:
// the engine refuses a waiting run's stream with its own wording (refresh 04).
func (c *agentClientConfig) sendable(run map[string]any) error {
	switch state := str(run, "state"); state {
	case "running", "idle", "pending":
		return nil
	case sessionWaitingApproval:
		return sentence(exitcode.Conflict, "%s is waiting for approval before it starts. Send once an approver accepts it: %s",
			sessionLabel(run), c.approvalsPage())
	case "failed":
		// A failed session says what the engine recorded. Resuming it before the cause is
		// fixed fails the same way (refresh 08b: "exit 1: Not logged in").
		reason := strings.TrimRight(strings.TrimSpace(termSafe(str(run, "reason"))), ".")
		if reason == "" {
			return sentence(exitcode.Conflict, "Session %s failed. See why: olivares session show %s",
				sessionLabel(run), shellWord(sessionLabel(run)))
		}
		return sentence(exitcode.Conflict, "Session %s failed: %s. Resume it once that is fixed: olivares session resume %s",
			sessionLabel(run), reason, shellWord(sessionLabel(run)))
	default:
		return sentence(exitcode.Conflict, "Session %s is %s. Resume it first: olivares session resume %s",
			sessionLabel(run), state, shellWord(sessionLabel(run)))
	}
}

// postTurn sends one turn of input to a live run.
func (c *agentClientConfig) postTurn(ctx context.Context, run map[string]any, text string) error {
	if err := c.sendable(run); err != nil {
		return err
	}
	body, err := sessionTurnBody(run, text)
	if err != nil {
		return err
	}
	status, b, err := c.do(ctx, "POST", sessionRunsPath+"/"+url.PathEscape(str(run, "run_ref"))+"/input", body, http.StatusAccepted)
	if err != nil {
		return err
	}
	if status != http.StatusAccepted {
		return httpErr(status, b)
	}
	return nil
}

// sendTurn sends one turn and, unless detached, shows the reply. It opens the
// output stream first and lets the replay settle, so only frames written after the
// turn was sent are shown.
func (c *agentClientConfig) sendTurn(cmd *cobra.Command, run map[string]any, text string, detach bool) error {
	if err := c.sendable(run); err != nil {
		return err
	}
	if detach || !sessionShowsTurns(run) {
		if err := c.postTurn(cmd.Context(), run, text); err != nil {
			return err
		}
		return renderOut(cmd, func(w io.Writer) error {
			_, err := fmt.Fprintf(w, "Sent to %s. See the reply: olivares session follow %s\n",
				sessionLabel(run), shellWord(sessionLabel(run)))
			return err
		}, map[string]any{"run_ref": str(run, "run_ref"), "sent": true})
	}
	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()
	frames, errs := c.openFrames(ctx, str(run, "run_ref"), 0)
	last, err := settleReplay(frames, errs)
	if errors.Is(err, errSessionEnded) {
		// The tool may have exited before its first turn; the engine records why.
		if now, rerr := c.findSession(ctx, str(run, "run_ref")); rerr == nil {
			if serr := c.sendable(now); serr != nil {
				return serr
			}
		}
	}
	if err != nil {
		return err
	}
	if err := c.postTurn(ctx, run, text); err != nil {
		return err
	}
	view := newSessionView(cmd.OutOrStdout())
	view.driver, view.readyShown = sessionDriver(run), true
	for {
		select {
		case f, ok := <-frames:
			if !ok {
				select {
				case err := <-errs:
					return err
				default:
				}
				return sentence(exitcode.Err, "The session's output ended before the turn finished. See it: olivares session show %s",
					shellWord(sessionLabel(run)))
			}
			if f.Seq <= last {
				continue
			}
			if view.render(f.Line) {
				if view.failed {
					// The turn's failure is already on screen; the code is for scripts.
					return exitcode.New(exitcode.Err, nil)
				}
				return nil
			}
		case err := <-errs:
			return err
		case <-ctx.Done():
			return nil
		}
	}
}

// sessionShowsTurns reports whether this CLI can tell when a turn of this run ends:
// a Claude stream-json run emits a `result` frame, a Codex run `turn/completed`.
// Other drivers' frames are their own protocol, so send returns once the engine
// accepts the message.
func sessionShowsTurns(run map[string]any) bool {
	d := strings.TrimSpace(str(run, "provider_driver"))
	return (d == "" || d == "claude" || d == "codex") && str(run, "transport") != "remote-control"
}

// sessionFrame is one output frame of the attach stream.
type sessionFrame struct {
	Seq  int64  `json:"seq"`
	Line string `json:"line"`
}

// errSessionNotLive is the attach `notice` for a run with no bridged I/O.
var errSessionNotLive = errors.New("session output is not available")

// errSessionEnded is a stream that ended before input could be sent.
var errSessionEnded = sentence(exitcode.Conflict, "The session's output has ended, so it cannot take a message. Resume it first.")

// followErr turns a refused or absent stream into the sentence follow prints.
func followErr(run map[string]any, err error) error {
	if errors.Is(err, errSessionNotLive) {
		return sentence(exitcode.Conflict, "Session %s is %s and has no live output. See it: olivares session show %s",
			sessionLabel(run), str(run, "state"), shellWord(sessionLabel(run)))
	}
	return err
}

// openFrames streams the run's output frames. frames closes when the stream ends;
// errs carries at most one error.
func (c *agentClientConfig) openFrames(ctx context.Context, ref string, from int64) (<-chan sessionFrame, <-chan error) {
	frames := make(chan sessionFrame, 256)
	errs := make(chan error, 1)
	fail := func(err error) {
		select {
		case errs <- err:
		default:
		}
	}
	go func() {
		defer close(frames)
		err := c.readAttach(ctx, ref, from, func(event, data string) bool {
			switch event {
			case "output":
				var f sessionFrame
				if json.Unmarshal([]byte(data), &f) == nil {
					select {
					case frames <- f:
					case <-ctx.Done():
						return false
					}
				}
			case "notice":
				var n struct {
					State, Detail string
					IOUnavailable string `json:"io_unavailable"`
				}
				_ = json.Unmarshal([]byte(data), &n)
				if n.IOUnavailable != "" {
					fail(exitcode.New(exitcode.Conflict, fmt.Errorf("%w: the session is %s (%s)", errSessionNotLive, n.State, n.Detail)))
					return false
				}
			case "end":
				return false
			}
			return true
		})
		if err != nil && ctx.Err() == nil {
			fail(err)
		}
	}()
	return frames, errs
}

// readAttach opens GET /runs/{ref}/attach and calls fn for every SSE event until fn
// returns false or the stream ends.
func (c *agentClientConfig) readAttach(ctx context.Context, ref string, from int64, fn func(event, data string) bool) error {
	path := fmt.Sprintf("%s/%s/attach?from=%d", sessionRunsPath, url.PathEscape(ref), from)
	req, err := c.newRequest(ctx, "GET", path, nil)
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
	return scanSSE(resp.Body, fn)
}

// settleReplay drains the frames the attach stream replays at once and returns
// the last sequence number it saw. The server writes the whole replay before it
// waits for new output, so a short quiet period marks its end.
func settleReplay(frames <-chan sessionFrame, errs <-chan error) (int64, error) {
	const quiet, ceiling = 400 * time.Millisecond, 3 * time.Second
	var last int64
	deadline := time.After(ceiling)
	for {
		select {
		case f, ok := <-frames:
			if !ok {
				// The stream ended during the replay: either it was refused (the error
				// is waiting) or the session's output is over. Never send into either.
				select {
				case err := <-errs:
					return 0, err
				default:
				}
				return 0, errSessionEnded
			}
			if f.Seq > last {
				last = f.Seq
			}
		case err := <-errs:
			return 0, err
		case <-time.After(quiet):
			return last, nil
		case <-deadline:
			return last, nil
		}
	}
}

func newSessionFollowCmd() *cobra.Command {
	var (
		cfg  agentClientConfig
		from int64
	)
	cmd := &cobra.Command{
		Use:   "follow <session>",
		Short: "Show a session's output as it happens",
		Long: "follow shows what the session has written so far and keeps showing new output\n" +
			"until the session ends or you press Ctrl-C. -o json prints the raw frames.",
		Example:           "  olivares session follow my-app\n  olivares session follow my-app -o json",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeSessions,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			run, err := cfg.findSession(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			raw := outputIsJSON(cmd)
			view := newSessionView(cmd.OutOrStdout())
			view.driver = sessionDriver(run)
			frames, errs := cfg.openFrames(cmd.Context(), str(run, "run_ref"), from)
			for {
				select {
				case f, ok := <-frames:
					if !ok {
						select {
						case err := <-errs:
							return followErr(run, err)
						default:
						}
						return nil
					}
					if raw {
						fmt.Fprintln(cmd.OutOrStdout(), f.Line)
						continue
					}
					view.render(f.Line)
				case err := <-errs:
					return followErr(run, err)
				case <-cmd.Context().Done():
					return nil
				}
			}
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().Int64Var(&from, "from", 0, "start from this output sequence number")
	return cmd
}

// --- ls, show, stop, resume, interrupt, rm, events ------------------------------

func newSessionListCmd() *cobra.Command {
	var (
		cfg   agentClientConfig
		all   bool
		state string
	)
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List sessions, newest first",
		Long: "ls prints one row per session: name, state, tool, folder and when it started, newest\n" +
			"first. Stopped and failed sessions are listed until you remove them. Use the name with\n" +
			"the other session commands.",
		Example: "  olivares session ls\n  olivares session ls --state running -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			runs, err := cfg.listRuns(cmd.Context())
			if err != nil {
				return err
			}
			shown := make([]map[string]any, 0, len(runs))
			for _, r := range runs {
				s := str(r, "state")
				if (state != "" && s != state) || (state == "" && !all && s == "cleaned") {
					continue
				}
				shown = append(shown, r)
			}
			table := termrender.Table{
				Header: []string{"name", "state", "tool", "folder", "started"},
				Empty:  "No sessions yet. Start one: olivares session start <folder>",
			}
			// A name that two rows carry gets the short tail of each row's id, as the
			// console does (sharedNames in web/src/features/sessions/provenance.ts).
			names := map[string]int{}
			for _, r := range shown {
				names[sessionLabel(r)]++
			}
			now := time.Now()
			waiting := 0
			for _, r := range shown {
				s := str(r, "state")
				if s == sessionWaitingApproval {
					waiting++
					s = "needs approval"
				}
				folder := homeShort(termSafe(str(r, "workspace_path")))
				if str(r, "workspace_ref") == "" && folder != "" {
					// The engine made this folder for the session; its path is an id.
					folder = "(its own folder)"
				}
				name := sessionLabel(r)
				if names[name] > 1 {
					name += " " + termSafe(refTail(str(r, "run_ref")))
				}
				table.Rows = append(table.Rows, []string{name, s, sessionDriver(r),
					folder, sinceText(str(r, "created_at"), now)})
				table.Roles = append(table.Roles, []termrender.Role{termrender.RoleNone, sessionStateRole(s)})
			}
			return renderOut(cmd, func(out io.Writer) error {
				r := renderTo(out)
				r.Table(table)
				if waiting > 0 {
					noun := "session needs"
					if waiting > 1 {
						noun = "sessions need"
					}
					r.Line(r.Paint(fmt.Sprintf("%d %s approval: %s", waiting, noun, cfg.approvalsPage()), termrender.RoleWarn))
				}
				return nil
			}, shown)
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().BoolVar(&all, "all", false, "include released (cleaned) sessions")
	cmd.Flags().StringVar(&state, "state", "", "only sessions in this state (pending|waiting_approval|running|idle|stopped|failed|cleaned)")
	return cmd
}

func newSessionShowCmd() *cobra.Command {
	var cfg agentClientConfig
	cmd := &cobra.Command{
		Use:     "show <session>",
		Aliases: []string{"get"},
		Short:   "Show one session",
		Long: "show prints one session's details: name, state, tool, folder, start time, cost so far\n" +
			"and its id. Use it to check a session before you send to it, stop it or remove it.",
		Example:           "  olivares session show my-app\n  olivares session show my-app -o json",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeSessions,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			run, err := cfg.findSession(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				printSessionFields(w, run)
				return nil
			}, run)
		},
	}
	cfg.addFlags(cmd)
	return cmd
}

func printSessionFields(w io.Writer, run map[string]any) {
	state := str(run, "state")
	fields := []termrender.Field{
		{Key: "name", Value: sessionLabel(run)},
		{Key: "state", Value: state, Role: sessionStateRole(state)},
	}
	// A failed session always says why; a stopped one says so when the engine gave a
	// reason a person acts on ("Access changed for <user>; resume to continue ...").
	if reason := termSafe(str(run, "reason")); state == "failed" || (state == "stopped" && strings.TrimSpace(reason) != "") {
		fields = append(fields, termrender.Field{Key: "reason", Value: reason})
	}
	fields = append(fields,
		termrender.Field{Key: "tool", Value: sessionDriver(run)},
		termrender.Field{Key: "folder", Value: termSafe(str(run, "workspace_path"))},
		termrender.Field{Key: "started", Value: str(run, "started_at")},
	)
	if stopped := str(run, "stopped_at"); stopped != "" {
		fields = append(fields, termrender.Field{Key: "stopped", Value: stopped})
	}
	fields = append(fields,
		termrender.Field{Key: "cost", Value: strings.TrimPrefix(runCostLine(run), "cost ")},
		termrender.Field{Key: "id", Value: str(run, "run_ref")},
	)
	renderTo(w).Fields(fields)
}

// newSessionLifecycleCmd is stop and resume: one POST, one sentence.
// sessionLifecycleLong is the Long of stop and resume.
var sessionLifecycleLong = map[string]string{
	"stop": "stop ends the tool's process and keeps the session, its folder and its conversation.\n" +
		"It prints \"Stopped <name>.\" Resume it later, or remove it: olivares session rm <name>.",
	"resume": "resume starts the tool again for a stopped session, in the same folder and with the\n" +
		"same conversation. It prints \"Resumed <name>.\" Then send to it or follow it.",
}

func newSessionLifecycleCmd(use, short, suffix, done string) *cobra.Command {
	var cfg agentClientConfig
	cmd := &cobra.Command{
		Use:               use + " <session>",
		Short:             short,
		Long:              sessionLifecycleLong[use],
		Example:           "  olivares session " + use + " my-app",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeSessions,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			run, err := cfg.findSession(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			status, b, err := cfg.do(cmd.Context(), "POST", sessionRunsPath+"/"+url.PathEscape(str(run, "run_ref"))+suffix, nil, http.StatusOK)
			if err != nil {
				return err
			}
			if status != http.StatusOK {
				return httpErr(status, b)
			}
			updated := map[string]any{}
			if err := json.Unmarshal(b, &updated); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				_, err := fmt.Fprintf(w, "%s %s.\n", done, sessionLabel(updated))
				return err
			}, updated)
		},
	}
	cfg.addFlags(cmd)
	return cmd
}

func newSessionInterruptCmd() *cobra.Command {
	var cfg agentClientConfig
	cmd := &cobra.Command{
		Use:   "interrupt <session>",
		Short: "Cancel the current turn and keep the session running",
		Long: "interrupt stops what the tool is doing now and keeps the session and its conversation.\n" +
			"It prints \"Interrupted the current turn of <name>.\" Use it when a turn goes the wrong\n" +
			"way; then send a new message. If the session's tool cannot interrupt a turn, it says so\n" +
			"and names the stop command instead.",
		Example:           "  olivares session interrupt my-app",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeSessions,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			run, err := cfg.findSession(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			status, b, err := cfg.do(cmd.Context(), "POST", sessionRunsPath+"/"+url.PathEscape(str(run, "run_ref"))+"/interrupt", nil,
				http.StatusOK, http.StatusConflict, http.StatusGatewayTimeout)
			if err != nil {
				return err
			}
			var refusal apiErrorEnvelope
			if (status == http.StatusConflict || status == http.StatusGatewayTimeout) && json.Unmarshal(b, &refusal) == nil {
				name := shellWord(sessionLabel(run))
				switch msg := strings.TrimRight(termSafe(refusal.Error.Message), ". "); {
				case strings.Contains(msg, "no turn interruption"):
					// The engine decides which runs interrupt (a relayed run does not); the
					// sentence names the session, never a tool that may be able to.
					return sentence(exitcode.Conflict, "Session %s cannot interrupt a turn. Stop it instead: olivares session stop %s",
						sessionLabel(run), name)
				case status == http.StatusGatewayTimeout && msg != "":
					// No answer in time: the turn may still be running, so the outcome is unknown.
					return sentence(exitcode.Indeterminate, "%s. See it: olivares session follow %s; to end it: olivares session stop %s",
						msg, name, name)
				case strings.HasPrefix(msg, "Claude Code refused the interrupt"):
					return sentence(exitcode.Conflict, "%s. The turn goes on; to end it: olivares session stop %s", msg, name)
				case strings.Contains(msg, "no active provider turn"):
					// Nothing is running: there is nothing to interrupt, which is not a failure.
					return renderOut(cmd, func(w io.Writer) error {
						_, err := fmt.Fprintf(w, "No turn is running in %s; nothing to interrupt.\n", sessionLabel(run))
						return err
					}, map[string]any{"interrupted": false, "session": sessionLabel(run)})
				case msg != "":
					// Any other refusal the engine explains (the session ended first, …).
					return sentence(exitcode.Conflict, "%s.", msg)
				}
			}
			if status != http.StatusOK {
				return httpErr(status, b)
			}
			return renderOut(cmd, func(w io.Writer) error {
				_, err := fmt.Fprintf(w, "Interrupted the current turn of %s.\n", sessionLabel(run))
				return err
			}, json.RawMessage(b))
		},
	}
	cfg.addFlags(cmd)
	return cmd
}

func newSessionRemoveCmd() *cobra.Command {
	var cfg agentClientConfig
	cmd := &cobra.Command{
		Use:     "rm <session>",
		Aliases: []string{"delete", "remove"},
		Short:   "Remove a stopped session",
		Long: "rm releases a session that stopped, failed or never started (its launch was rejected or\n" +
			"expired) and deletes its record. A folder you gave the session is kept; a folder the\n" +
			"engine created for it is removed.",
		Example:           "  olivares session rm my-app",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeSessions,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			run, err := cfg.findSession(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			name, ref := sessionLabel(run), url.PathEscape(str(run, "run_ref"))
			switch str(run, "state") {
			case "stopped", "failed", "declined", "expired":
				status, b, err := cfg.do(cmd.Context(), "POST", sessionRunsPath+"/"+ref+"/cleanup", nil, http.StatusOK)
				if err != nil {
					return err
				}
				if status != http.StatusOK {
					return httpErr(status, b)
				}
			case "cleaned":
			default:
				return sentence(exitcode.Conflict, "Session %s is %s. Stop it first: olivares session stop %s",
					name, str(run, "state"), shellWord(name))
			}
			status, b, err := cfg.do(cmd.Context(), "DELETE", sessionRunsPath+"/"+ref, nil, http.StatusOK)
			if err != nil {
				return err
			}
			if status != http.StatusOK {
				return httpErr(status, b)
			}
			return renderOut(cmd, func(w io.Writer) error {
				_, err := fmt.Fprintf(w, "Removed %s.\n", name)
				return err
			}, map[string]any{"run_ref": str(run, "run_ref"), "deleted": true})
		},
	}
	cfg.addFlags(cmd)
	return cmd
}

func newSessionEventsCmd() *cobra.Command {
	var cfg agentClientConfig
	cmd := &cobra.Command{
		Use:   "events <session>",
		Short: "Show a session's lifecycle record (start, stop, failures)",
		Long: "events prints the engine's record of the session as JSON: when it was launched, stopped\n" +
			"and resumed, and why it failed if it did. It does not show the conversation (use\n" +
			"olivares session follow). Use it to find out why a session stopped.",
		Example:           "  olivares session events my-app",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeSessions,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			run, err := cfg.findSession(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			status, b, err := cfg.do(cmd.Context(), "GET", sessionRunsPath+"/"+url.PathEscape(str(run, "run_ref"))+"/events", nil)
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

// --- lookup and formatting -----------------------------------------------------

// listRuns reads the newest runs (the API sorts by creation, newest first).
func (c *agentClientConfig) listRuns(ctx context.Context) ([]map[string]any, error) {
	status, b, err := c.do(ctx, "GET", fmt.Sprintf("%s?limit=%d", sessionRunsPath, sessionListLimit), nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, httpErr(status, b)
	}
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(b, &page); err != nil {
		return nil, err
	}
	return page.Items, nil
}

// findSession resolves a session by id or by name. A name resolves to its newest
// session; `start` keeps names unique among sessions that are not released.
func (c *agentClientConfig) findSession(ctx context.Context, arg string) (map[string]any, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return nil, sentence(exitcode.Usage, "Name the session. List them: olivares session ls")
	}
	if runRefPattern.MatchString(arg) {
		status, b, err := c.do(ctx, "GET", sessionRunsPath+"/"+url.PathEscape(arg), nil)
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, httpErr(status, b)
		}
		run := map[string]any{}
		return run, json.Unmarshal(b, &run)
	}
	runs, err := c.listRuns(ctx)
	if err != nil {
		return nil, err
	}
	matches := make([]map[string]any, 0, 1)
	for _, r := range runs {
		if str(r, "name") == arg {
			matches = append(matches, r)
		}
	}
	if len(matches) == 0 {
		return nil, sentence(exitcode.NotFound, "No session is named %q. List them: olivares session ls", arg)
	}
	sort.SliceStable(matches, func(i, j int) bool { return str(matches[i], "created_at") > str(matches[j], "created_at") })
	return matches[0], nil
}

// sessionLabel is how a session is named on screen: its name, else its id.
func sessionLabel(run map[string]any) string {
	if n := strings.TrimSpace(str(run, "name")); n != "" {
		return termSafe(n)
	}
	return termSafe(str(run, "run_ref"))
}

var (
	refTailPrefix = regexp.MustCompile(`(?i)^(ppf|osn|xenv|wsp|run|sess)[_-]`)
	refTailUUID   = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-([0-9a-f]{12})$`)
)

// refTail is the console's short form of a reference beside a name (refTail in
// web/src/features/shared/entity-names.ts): the type prefix goes, and a uuid keeps its
// last group, the random one (a uuid v7 starts with the time it was made, so two
// sessions started together share the head). A reference that is readable without its
// prefix stays whole.
func refTail(reference string) string {
	id := reference
	if i := strings.IndexByte(id, ':'); i >= 0 {
		id = id[i+1:]
	}
	id = refTailPrefix.ReplaceAllString(id, "")
	if m := refTailUUID.FindStringSubmatch(id); m != nil {
		return "…" + m[1]
	}
	return id
}

func sessionDriver(run map[string]any) string {
	if d := str(run, "provider_driver"); d != "" {
		return termSafe(d)
	}
	return "claude"
}

// shellWord quotes a session name for a command line the user may copy.
func shellWord(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$`*?[]{}()<>|&;#~!") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// homeShort writes a path under this account's home as ~/…, as a shell does.
func homeShort(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || p == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + p[len(home):]
	}
	return p
}

// sinceText is a timestamp as "5m ago", or the date when it is older than a week.
func sinceText(stamp string, now time.Time) string {
	t, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return stamp
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
	return t.Local().Format("2006-01-02")
}

// sentence is a message a person reads: a full sentence with its next command, in the
// copy Root sets for the CLI (CLX/COPY-NEEDS.md). It is not an error value for code
// to wrap, which is why it does not follow Go's lower-case error-string convention.
func sentence(code int, format string, args ...any) error {
	return exitcode.New(code, fmt.Errorf(format, args...))
}

// outputIsJSON reports whether -o json was selected.
func outputIsJSON(cmd *cobra.Command) bool {
	format, err := selectedOutput(cmd)
	return err == nil && format == "json"
}
