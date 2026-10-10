// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/driverfacts"
)

// What each tool says about its own login, read through the tool's own read-only
// commands (docs: claude `auth status --json` and the stream-json `get_usage`
// control request, codex `app-server` account/read, account/rateLimits/read and
// model/list, grok `models`, opencode `auth list` and `models`). `get_usage` is
// marked experimental by Claude Code, so only the documented keys are read and a
// miss becomes a note, never a failure of the whole snapshot.

const (
	providerProbeTimeout = time.Minute
	versionTimeout       = 4 * time.Second
	listTimeout          = 15 * time.Second
	claudeUsageTimeout   = 25 * time.Second
	codexServerTimeout   = 10 * time.Second
	maxToolLine          = 16 << 20 // Claude's initialize answer lists every command
	maxProviderModels    = 200
	maxNoteLength        = 300
)

// probeError names the command that failed and what it said.
type probeError struct {
	command string
	err     error
}

func (e *probeError) Error() string { return e.command + ": " + e.err.Error() }
func (e *probeError) Unwrap() error { return e.err }

// prober runs the read-only commands of one instance and fills its snapshot.
type prober struct {
	ctx     context.Context
	program string
	env     []string
	own     bool
	cfgDir  string
	snap    *ProviderSnapshot
	sources []string
}

func (m *Module) probeProvider(ctx context.Context, in providerInstance) (ProviderSnapshot, error) {
	snap := ProviderSnapshot{Instance: in.id, Driver: in.driver, Default: in.own, ConfigDir: in.configDir,
		Limits: []ProviderLimit{}, Models: []ProviderModel{}, CheckedAt: time.Now().UTC()}
	if in.err != nil {
		return snap, in.err
	}
	program := m.program(in.driver)
	if program == "" {
		snap.State, snap.NextCommand = stateNotInstalled, "olivares tool install "+in.driver
		return snap, nil
	}
	snap.Installed = true
	p := &prober{ctx: ctx, program: program, env: in.env, own: in.own, cfgDir: in.configDir, snap: &snap}
	err := p.run(in.driver)
	snap.Source = strings.Join(p.sources, "; ")
	return snap, err
}

func (p *prober) run(driver string) error {
	out, err := p.output(versionTimeout, "--version")
	if err != nil {
		return err
	}
	p.snap.Version = oneLine(firstLine(out), 80)
	if driver == "gemini-cli" {
		st, err := geminiSignInStatus(SignInStatus{}, p.cfgDir)
		if err != nil {
			return err
		}
		if !st.SignedIn {
			p.snap.State, p.snap.NextCommand = stateNotSignedIn, p.loginCommand(driver)
			return nil
		}
		p.snap.State, p.snap.AuthMethod = stateUnknown, st.Method
		p.note("Gemini CLI's native Google login is present; token validity, plan, usage and models are not reported by a status command.")
		return nil
	}
	if driver == "grok" {
		// Grok Build has no status command. A login made through Olivares is the
		// session file `grok login` wrote, whose presence is the module's own rule
		// (readStatus) and which is never opened; the user's own login is not judged.
		if _, err := os.Lstat(filepath.Join(p.cfgDir, "auth.json")); !p.own && err != nil {
			p.snap.State, p.snap.NextCommand = stateNotSignedIn, p.loginCommand(driver)
			return nil
		}
		p.snap.State = stateUnknown
		p.note("Grok Build reports no sign-in state, plan or usage.")
		p.models("Available models:")
		return nil
	}
	st, err := p.status(driver)
	if err != nil {
		return err
	}
	if !st.SignedIn {
		p.snap.State, p.snap.NextCommand = stateNotSignedIn, p.loginCommand(driver)
		return nil
	}
	p.snap.State, p.snap.AuthMethod, p.snap.Plan = stateReady, st.Method, st.Plan
	if st.Account != "" {
		p.snap.Email = maskEmail(st.Account)
	}
	switch driver {
	case "claude":
		p.claude()
	case "codex":
		p.codex()
	case "opencode":
		p.models("")
	}
	return nil
}

// loginCommand is the tool's own sign-in command for this instance.
func (p *prober) loginCommand(driver string) string {
	if !p.own {
		return "olivares tool login " + driver
	}
	facts, _ := driverfacts.Lookup(driver)
	parts := []string{facts.Program}
	for _, arg := range facts.LoginArgs {
		if strings.ContainsRune(arg, ' ') {
			arg = strconv.Quote(arg)
		}
		parts = append(parts, arg)
	}
	return strings.Join(parts, " ")
}

// status asks the tool whether it is signed in: the module's one reader, the same
// answer a sign-in status read gives. An answer it cannot recognize is an error,
// never "not signed in".
func (p *prober) status(driver string) (SignInStatus, error) {
	facts, _ := driverfacts.Lookup(driver)
	command := cmdLine(p.program, facts.StatusArgs...)
	p.sources = append(p.sources, command)
	st, err := readNativeStatus(p.ctx, SignInStatus{Driver: driver, Installed: true}, p.program, p.env)
	if err == nil && st.unrecognized {
		err = errors.New("the tool's answer was not recognized")
	}
	if err != nil {
		return st, &probeError{command, err}
	}
	return st, nil
}

// output runs one read-only command of the tool and returns its stdout.
func (p *prober) output(timeout time.Duration, args ...string) (string, error) {
	command := cmdLine(p.program, args...)
	p.sources = append(p.sources, command)
	ctx, cancel := context.WithTimeout(p.ctx, timeout)
	defer cancel()
	cmd := toolCommand(ctx, p.program, args...)
	cmd.Env, cmd.Dir, cmd.WaitDelay = p.env, envValue(p.env, "HOME"), 200*time.Millisecond
	raw, err := cmd.Output()
	if err != nil {
		return "", &probeError{command, commandFailure(ctx, err)}
	}
	if len(raw) > maxSignInOutput {
		raw = raw[:maxSignInOutput]
	}
	return ansiEscape.ReplaceAllString(string(raw), ""), nil
}

// models lists what the tool's `models` command prints: one model per line,
// after the heading when the tool prints one.
func (p *prober) models(heading string) {
	out, err := p.output(listTimeout, "models")
	if err != nil {
		p.note(err.Error())
		return
	}
	lines := strings.Split(out, "\n")
	if heading != "" {
		at := slices.IndexFunc(lines, func(l string) bool { return strings.TrimSpace(l) == heading })
		if at < 0 {
			p.note("`models` printed no list under " + strconv.Quote(heading))
			return
		}
		lines = lines[at+1:]
	}
	for _, line := range lines {
		// A bullet ("*" marks the default, "-" the others) is not part of the name.
		if fields := strings.Fields(strings.TrimLeft(strings.TrimSpace(line), "*- ")); len(fields) > 0 && len(p.snap.Models) < maxProviderModels {
			p.snap.Models = append(p.snap.Models, ProviderModel{ID: fields[0]})
		}
	}
}

func (p *prober) note(msg string) { p.snap.Notes = append(p.snap.Notes, oneLine(msg, maxNoteLength)) }

// --- Claude Code ---------------------------------------------------------------

func claudeReplyID(line []byte) string {
	var r struct {
		Response struct {
			ID string `json:"request_id"`
		} `json:"response"`
	}
	_ = json.Unmarshal(line, &r)
	return r.Response.ID
}

// claudeResult decodes the body of a successful control response.
func claudeResult(raw []byte, into any) error {
	var r struct {
		Response struct {
			Subtype  string          `json:"subtype"`
			Error    string          `json:"error"`
			Response json.RawMessage `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return err
	}
	if r.Response.Subtype != "success" {
		return errors.New("the tool refused the request: " + oneLine(r.Response.Error, 120))
	}
	return json.Unmarshal(r.Response.Response, into)
}

// claude reads the plan windows and the models over one stream-json session: no
// prompt is sent and no model is called; hooks and MCP are off.
func (p *prober) claude() {
	args := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--no-session-persistence",
		"--settings", `{"disableAllHooks":true}`, "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`}
	command := cmdLine(p.program, "-p", "--input-format", "stream-json") + " (get_usage, initialize)"
	request := func(id, subtype string, extra map[string]any) any {
		body := map[string]any{"subtype": subtype}
		for k, v := range extra {
			body[k] = v
		}
		return map[string]any{"type": "control_request", "request_id": id, "request": body}
	}
	replies, err := p.converse(claudeUsageTimeout, args, command, claudeReplyID, exchange{
		send: []any{request("usage", "get_usage", map[string]any{"skip_behaviors": true}), request("models", "initialize", nil)},
		wait: []string{"usage", "models"},
	})
	if raw, ok := replies["usage"]; ok {
		p.claudeUsage(command, raw)
	} else {
		p.note("plan limits: " + err.Error())
	}
	raw, ok := replies["models"]
	if !ok {
		p.note("models: " + err.Error())
		return
	}
	var init struct {
		Models []struct {
			Value       string `json:"value"`
			DisplayName string `json:"displayName"`
		} `json:"models"`
	}
	if err := claudeResult(raw, &init); err != nil {
		p.note("models: " + (&probeError{command, err}).Error())
	}
	for _, mo := range init.Models {
		if mo.Value != "" && len(p.snap.Models) < maxProviderModels {
			p.snap.Models = append(p.snap.Models, ProviderModel{ID: mo.Value, Name: mo.DisplayName})
		}
	}
}

func (p *prober) claudeUsage(command string, raw []byte) {
	var u struct {
		Plan       string `json:"subscription_type"`
		Available  bool   `json:"rate_limits_available"`
		RateLimits struct {
			Limits []struct {
				Kind     string  `json:"kind"`
				Percent  float64 `json:"percent"`
				Severity string  `json:"severity"`
				ResetsAt string  `json:"resets_at"`
				Scope    *struct {
					Model struct {
						DisplayName string `json:"display_name"`
					} `json:"model"`
				} `json:"scope"`
			} `json:"limits"`
		} `json:"rate_limits"`
	}
	if err := claudeResult(raw, &u); err != nil {
		p.note("plan limits: " + (&probeError{command, err}).Error())
		return
	}
	if p.snap.Plan == "" {
		p.snap.Plan = u.Plan
	}
	if !u.Available {
		p.note("Claude Code reports no plan limits for this login (an API key, Bedrock or Vertex).")
		return
	}
	for _, l := range u.RateLimits.Limits {
		limit := ProviderLimit{Label: l.Kind, Percent: l.Percent, Severity: l.Severity}
		switch l.Kind {
		case "session":
			limit.Label = "5-hour"
		case "weekly_all":
			limit.Label = "Weekly"
		case "weekly_scoped":
			limit.Label = "Weekly"
			if l.Scope != nil && l.Scope.Model.DisplayName != "" {
				limit.Label += " (" + l.Scope.Model.DisplayName + ")"
			}
		}
		if t, err := time.Parse(time.RFC3339, l.ResetsAt); err == nil {
			limit.ResetsAt = &t
		}
		p.snap.Limits = append(p.snap.Limits, limit)
	}
}

// --- Codex ---------------------------------------------------------------------

func codexReplyID(line []byte) string {
	var r struct {
		ID     json.Number `json:"id"`
		Method string      `json:"method"`
	}
	if json.Unmarshal(line, &r) != nil || r.Method != "" {
		return ""
	}
	return r.ID.String()
}

func rpcResult(raw []byte, into any) error {
	var r struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return err
	}
	if r.Error != nil {
		return errors.New(oneLine(r.Error.Message, 120))
	}
	return json.Unmarshal(r.Result, into)
}

// codexWindow is one rate-limit window: its length in minutes and when it resets.
type codexWindow struct {
	UsedPercent float64 `json:"usedPercent"`
	Minutes     int     `json:"windowDurationMins"`
	ResetsAt    int64   `json:"resetsAt"`
}

func (w codexWindow) limit() ProviderLimit {
	l := ProviderLimit{Percent: w.UsedPercent}
	switch {
	case w.Minutes == 7*24*60:
		l.Label = "Weekly"
	case w.Minutes > 0 && w.Minutes%(24*60) == 0:
		l.Label = fmt.Sprintf("%d-day", w.Minutes/(24*60))
	case w.Minutes > 0 && w.Minutes%60 == 0:
		l.Label = fmt.Sprintf("%d-hour", w.Minutes/60)
	default:
		l.Label = fmt.Sprintf("%d-minute", w.Minutes)
	}
	if w.ResetsAt > 0 {
		t := time.Unix(w.ResetsAt, 0).UTC()
		l.ResetsAt = &t
	}
	return l
}

// codex asks one `app-server` for the account, the rate limits and the models.
func (p *prober) codex() {
	args := []string{"app-server", "--disable", "hooks", "-c", "mcp_servers={}"}
	command := cmdLine(p.program, "app-server") + " (initialize, account/read, account/rateLimits/read, model/list)"
	call := func(id int, method string, params any) any {
		return map[string]any{"id": id, "method": method, "params": params}
	}
	replies, err := p.converse(codexServerTimeout, args, command, codexReplyID,
		exchange{
			send: []any{call(1, "initialize", map[string]any{"clientInfo": map[string]any{"name": "olivares", "version": "1"}})},
			wait: []string{"1"},
		},
		exchange{
			send: []any{map[string]any{"method": "initialized"}, call(2, "account/read", map[string]any{"refreshToken": false}),
				call(3, "account/rateLimits/read", nil), call(4, "model/list", map[string]any{})},
			wait: []string{"2", "3", "4"},
		})
	if _, ok := replies["1"]; !ok {
		p.note("app-server: " + err.Error())
		return
	}
	// result decodes one reply into v, or notes why it is missing.
	result := func(id, what string, v any) bool {
		raw, ok := replies[id]
		if !ok {
			p.note(what + ": " + err.Error())
			return false
		}
		if err := rpcResult(raw, v); err != nil {
			p.note(what + ": " + (&probeError{command, err}).Error())
			return false
		}
		return true
	}
	var account struct {
		Account *struct {
			Type  string `json:"type"`
			Email string `json:"email"`
			Plan  string `json:"planType"`
		} `json:"account"`
	}
	if result("2", "account", &account) && account.Account != nil {
		a := account.Account
		p.snap.AuthMethod, p.snap.Plan = a.Type, a.Plan
		if a.Email != "" {
			p.snap.Email = maskEmail(a.Email)
		}
	}
	var limits struct {
		RateLimits struct {
			Primary   *codexWindow `json:"primary"`
			Secondary *codexWindow `json:"secondary"`
		} `json:"rateLimits"`
	}
	if result("3", "plan limits", &limits) {
		for _, w := range []*codexWindow{limits.RateLimits.Primary, limits.RateLimits.Secondary} {
			if w != nil {
				p.snap.Limits = append(p.snap.Limits, w.limit())
			}
		}
	}
	var list struct {
		Data []struct {
			Model       string `json:"model"`
			DisplayName string `json:"displayName"`
			Hidden      bool   `json:"hidden"`
		} `json:"data"`
	}
	if result("4", "models", &list) {
		for _, mo := range list.Data {
			if !mo.Hidden && mo.Model != "" && len(p.snap.Models) < maxProviderModels {
				p.snap.Models = append(p.snap.Models, ProviderModel{ID: mo.Model, Name: mo.DisplayName})
			}
		}
	}
}

// --- JSON lines over stdio -----------------------------------------------------

// exchange is one round of a stdio conversation: write the messages, then wait for
// the replies with these ids before the next round.
type exchange struct {
	send []any
	wait []string
}

// converse starts the tool, runs the rounds in order and returns the replies it
// got, by id, even when a later round failed. idOf names the id a line answers.
func (p *prober) converse(timeout time.Duration, args []string, command string, idOf func([]byte) string, rounds ...exchange) (map[string][]byte, error) {
	p.sources = append(p.sources, command)
	got := map[string][]byte{}
	ctx, cancel := context.WithTimeout(p.ctx, timeout)
	defer cancel()
	cmd := toolCommand(ctx, p.program, args...)
	cmd.Env, cmd.Dir, cmd.WaitDelay = p.env, envValue(p.env, "HOME"), 200*time.Millisecond
	in, err := cmd.StdinPipe()
	if err != nil {
		return got, &probeError{command, err}
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return got, &probeError{command, err}
	}
	if err := cmd.Start(); err != nil {
		return got, &probeError{command, err}
	}
	// A child that outlives the tool and holds its stdout must not keep the read blocked.
	stop := context.AfterFunc(ctx, func() { _ = out.Close() })
	defer func() { stop(); _ = in.Close(); cancel(); _ = cmd.Wait() }()
	lines := bufio.NewScanner(out)
	lines.Buffer(make([]byte, 0, 64<<10), maxToolLine)
	for _, round := range rounds {
		for _, msg := range round.send {
			b, _ := json.Marshal(msg)
			if _, err := in.Write(append(b, '\n')); err != nil {
				return got, &probeError{command, commandFailure(ctx, err)}
			}
		}
		for pending := len(round.wait); pending > 0 && lines.Scan(); {
			if id := idOf(lines.Bytes()); slices.Contains(round.wait, id) {
				got[id] = slices.Clone(lines.Bytes())
				pending--
			}
		}
		for _, id := range round.wait {
			if _, ok := got[id]; !ok {
				err := lines.Err()
				if err == nil {
					err = io.ErrUnexpectedEOF
				}
				return got, &probeError{command, commandFailure(ctx, err)}
			}
		}
	}
	return got, nil
}

// --- small helpers -------------------------------------------------------------

// cmdLine is the command as shown to the operator: the tool's name, not its path.
func cmdLine(program string, args ...string) string {
	return strings.Join(append([]string{filepath.Base(program)}, args...), " ")
}

func commandFailure(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return errors.New("timed out")
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		msg := "exit status " + strconv.Itoa(exit.ExitCode())
		if s := oneLine(string(exit.Stderr), 200); s != "" {
			msg += ": " + s
		}
		return errors.New(msg)
	}
	return err
}

func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// oneLine collapses whitespace and keeps at most n characters.
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
