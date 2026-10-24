// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/cmd/olivares/internal/tui"
)

// cmd_ui_session.go is the session shell of CLI-CONCEPT §5.4. It adds no
// engine call of its own: the output is the stream `session follow` reads, a
// message is the call `session send` makes, and start, interrupt and stop run
// the `session` commands themselves, so the audit ledger records what it
// records for the commands.

const (
	uiSessionNew = "new"
	// uiRecheck is how often the shell reads the session again, which also
	// reopens an output stream that ended while the session is still live.
	uiRecheck = 5 * time.Second
	// uiLeaveWait is how long leaving waits for a request that is still running.
	uiLeaveWait = 3 * time.Second
	// uiQueue bounds the requests waiting behind the one in flight.
	uiQueue = 64
)

func newUISessionCmd() *cobra.Command {
	var cfg agentClientConfig
	cmd := &cobra.Command{
		Annotations: map[string]string{"changes_data": "write"},
		Use:         "session <name|new>",
		Short:       "Follow, message, interrupt and stop one session on a terminal",
		Long: "session opens one session as a conversation: its output streams in, you type the\n" +
			"next message at the bottom, and the slash commands act on it: /interrupt cancels the\n" +
			"turn, /stop ends the tool after you confirm, /facts says what the session is, /help\n" +
			"lists the keys. `new` starts a session in this folder with the defaults, as\n" +
			"olivares session start does. Leaving the shell keeps the session running.\n\n" +
			"It needs a terminal of at least 80 x 24. Steering a session another person\n" +
			"started requires Olivares Business; Community shows that and keeps the output.",
		Example: "  olivares ui session new\n  olivares ui session my-app",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
				return sentence(exitcode.Usage, "Name the session, or start one: olivares ui session new")
			}
			in, out, err := uiTerminal(cmd)
			if err != nil {
				return err
			}
			// Every request would read the same standard input again, and it is the
			// terminal the shell is drawing on.
			if cfg.credentialFlags.tokenFile == "-" {
				return sentence(exitcode.Usage, "ui session cannot read the token from standard input. Pass a token file instead.")
			}
			if err := cfg.resolve(); err != nil {
				return err
			}
			// The TLS warning of --insecure would be written over the screen the
			// shell draws; the heading says it instead.
			transportWarnings = io.Discard
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			u := newUISession(ctx, &cfg, forwardedFlags(cmd.Flags()))
			if strings.TrimSpace(args[0]) == uiSessionNew {
				err = u.start()
			} else {
				err = u.open(args[0])
			}
			if err != nil {
				return err
			}
			go u.watch()
			code, err := tui.RunShell(in, out, u.sh)
			u.waitIdle(uiLeaveWait)
			cancel()
			if err == nil {
				name := shellWord(sessionLabel(u.current()))
				_, _ = fmt.Fprintf(out, "Left %s. Open it again: olivares ui session %s\n", name, name)
			}
			return uiExit(code, err)
		},
	}
	cfg.addFlags(cmd)
	return cmd
}

// uiSession is one open session shell.
type uiSession struct {
	ctx      context.Context
	cfg      *agentClientConfig
	flags    []string
	insecure bool
	sh       *tui.Shell

	// jobs runs sends and stops one at a time, in the order they were asked.
	jobs    chan func()
	pending sync.WaitGroup

	refreshMu  sync.Mutex
	mu         sync.Mutex
	run        map[string]any
	ref        string
	following  bool
	refreshing atomic.Bool
	seq        int64
	down       bool
	lastNote   string
}

func newUISession(ctx context.Context, cfg *agentClientConfig, flags []string) *uiSession {
	u := &uiSession{ctx: ctx, cfg: cfg, flags: flags, insecure: cfg.insecure, sh: tui.NewShell(), jobs: make(chan func(), uiQueue)}
	u.sh.Submit = u.submit
	u.sh.Interrupt = func() { u.background(u.interrupt) }
	go u.work()
	return u
}

// forwardedFlags are the connection flags (connectionFlags) the person set, as arguments for the
// `session` commands this shell runs: the same server, token and tenant reach
// them.
func forwardedFlags(set *pflag.FlagSet) []string {
	var args []string
	for _, name := range connectionFlags {
		f := set.Lookup(name)
		if f == nil || !f.Changed {
			continue
		}
		if f.Value.Type() == "stringArray" {
			values, _ := set.GetStringArray(name)
			for _, v := range values {
				args = append(args, "--"+name+"="+v)
			}
			continue
		}
		args = append(args, "--"+name+"="+f.Value.String())
	}
	return args
}

func (u *uiSession) current() map[string]any {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.run
}

// runCLI runs `olivares <args>` in this process and returns what it printed. The
// positional arguments follow a `--`: they come from the engine and must never
// read as flags.
func (u *uiSession) runCLI(args []string, positional ...string) (stdout, stderr string, err error) {
	argv := append(append([]string(nil), args...), u.flags...)
	if len(positional) > 0 {
		argv = append(append(argv, "--"), positional...)
	}
	return runOlivares(u.ctx, argv)
}

// start is `olivares session start` with nothing typed.
func (u *uiSession) start() error {
	text, notice, err := u.runCLI([]string{"session", "start", "-o", "json"})
	if err != nil {
		return err
	}
	run := map[string]any{}
	if err := json.Unmarshal([]byte(text), &run); err != nil {
		return fmt.Errorf("the engine started a session but its answer was not readable: %w", err)
	}
	if err := u.adopt(run); err != nil {
		return err
	}
	u.begin()
	for _, line := range strings.Split(notice, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			u.sh.Say("· " + line)
		}
	}
	return nil
}

// open reads the named session.
func (u *uiSession) open(name string) error {
	run, err := u.cfg.findSession(u.ctx, name)
	if err != nil {
		return err
	}
	if err := u.adopt(run); err != nil {
		return err
	}
	u.begin()
	return nil
}

// adopt takes the session the shell is about. Its reference comes from the
// engine and is passed to commands, so it must be the shape of a reference.
func (u *uiSession) adopt(run map[string]any) error {
	ref := str(run, "run_ref")
	if !runRefPattern.MatchString(ref) {
		return sentence(exitcode.Err, "The engine named a session by a reference this CLI cannot use.")
	}
	u.mu.Lock()
	u.run, u.ref = run, ref
	u.mu.Unlock()
	return nil
}

// begin fills the heading and starts following the output.
func (u *uiSession) begin() {
	u.retitle()
	u.sh.Say("Following " + sessionLabel(u.current()) + ". Type a message, or /help.")
	if u.insecure {
		u.sh.Say("· TLS certificate verification is off (--insecure).")
	}
	if str(u.current(), "state") == sessionWaitingApproval {
		u.sh.Say("It waits for approval before it starts: " + u.cfg.approvalsPage())
	}
	u.startFollow()
}

func (u *uiSession) retitle() {
	u.mu.Lock()
	run, down := u.run, u.down
	u.mu.Unlock()
	title := sessionLabel(run) + " · " + sessionDriver(run)
	if folder := termSafe(str(run, "workspace_path")); folder != "" {
		title += " · " + filepath.Base(folder)
	}
	if u.insecure {
		title += " · TLS not verified"
	}
	state := uiStateWord(str(run, "state"))
	if down {
		state = "Engine not answering"
	}
	u.sh.SetTitle(title, state)
}

// uiStateWord is a run state as the heading words it.
func uiStateWord(state string) string {
	switch state {
	case "":
		return ""
	case sessionWaitingApproval:
		return "Needs approval"
	case "pending":
		return "Starting"
	}
	return strings.ToUpper(state[:1]) + strings.ReplaceAll(state[1:], "_", " ")
}

// uiLive reports a state in which the session has output to follow and takes input.
func uiLive(state string) bool { return state == "running" || state == "idle" || state == "pending" }

// refresh reads the session again, so the heading and the next send see its
// state now and not the state it had when the shell opened. It also reopens the
// output stream when the session is live and nothing is following it.
func (u *uiSession) refresh() (map[string]any, error) {
	u.refreshMu.Lock()
	defer u.refreshMu.Unlock()
	run, err := u.cfg.findSession(u.ctx, u.ref)
	if err == nil && str(run, "run_ref") != u.ref {
		err = sentence(exitcode.Err, "The engine answered with a different session.")
	}
	u.mu.Lock()
	u.down = err != nil
	if err == nil {
		u.run = run
	}
	u.mu.Unlock()
	u.retitle()
	if err != nil {
		return u.current(), err
	}
	if uiLive(str(run, "state")) {
		u.startFollow()
	}
	return run, nil
}

// refreshSoon reads the session again in the background, once at a time: a
// replay of a long history ends many turns at once.
func (u *uiSession) refreshSoon() {
	if u.refreshing.CompareAndSwap(false, true) {
		go func() {
			defer u.refreshing.Store(false)
			_, _ = u.refresh()
		}()
	}
}

// background runs fn on its own goroutine, counted so leaving waits for it.
func (u *uiSession) background(fn func()) {
	u.pending.Add(1)
	go func() {
		defer u.pending.Done()
		fn()
	}()
}

// watch keeps the shell honest while nothing is typed: the heading follows the
// session's state, and a stream that ended is reopened once the session is live.
func (u *uiSession) watch() {
	tick := time.NewTicker(uiRecheck)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			_, _ = u.refresh()
		case <-u.ctx.Done():
			return
		}
	}
}

// startFollow follows the output unless a follower is already running.
func (u *uiSession) startFollow() {
	u.mu.Lock()
	if u.following || u.ctx.Err() != nil {
		u.mu.Unlock()
		return
	}
	u.following = true
	run, from := u.run, u.seq
	u.mu.Unlock()
	go u.follow(run, from)
}

// follow shows the session's output as it arrives, the way `session follow` does.
func (u *uiSession) follow(run map[string]any, from int64) {
	defer func() {
		u.mu.Lock()
		u.following = false
		u.mu.Unlock()
	}()
	frames, errs := u.cfg.openFrames(u.ctx, u.ref, from)
	view := newSessionView(u.sh)
	view.driver, view.engineSupplied = sessionDriver(run), engineSupplied(run)
	view.noOneAsked = noOneAsked(run)
	defer view.finish()
	for {
		select {
		case f, ok := <-frames:
			if !ok {
				select {
				case err := <-errs:
					u.sayOnce(followErr(run, err))
				default:
					u.noteOnce("The session's output ended.")
				}
				return
			}
			u.mu.Lock()
			seen := f.Seq != 0 && f.Seq <= u.seq
			if !seen {
				u.seq = max(u.seq, f.Seq)
				u.lastNote = ""
			}
			u.mu.Unlock()
			if !seen && view.render(f.Line) {
				u.refreshSoon()
			}
		case err := <-errs:
			u.sayOnce(followErr(run, err))
			return
		case <-u.ctx.Done():
			return
		}
	}
}

// shown is an error as the command line would print it, or "" for a silent one.
func shown(err error) string {
	if err == nil || exitcode.Silent(err) {
		return ""
	}
	var printed bytes.Buffer
	_ = printCLIErrorAs(&printed, err, false)
	return termSafe(strings.TrimSpace(strings.TrimPrefix(printed.String(), "Error:")))
}

// say shows an error the way the commands word it. The engine's refusal to let
// Community steer another person's session also turns the composer off.
func (u *uiSession) say(err error) {
	text := shown(err)
	if text == "" {
		return
	}
	var refusal *apiRefusal
	if errors.As(err, &refusal) && refusal.code == "business_required" {
		u.sh.Lock(text)
		u.sh.Say("· " + text)
		return
	}
	u.sh.Say("! " + text)
}

// sayOnce is say for a condition that can repeat while the stream reopens: the
// same sentence is shown once until output arrives again.
func (u *uiSession) sayOnce(err error) {
	if u.firstTime(shown(err)) {
		u.say(err)
	}
}

// noteOnce is sayOnce for a plain notice.
func (u *uiSession) noteOnce(text string) {
	if u.firstTime(text) {
		u.sh.Say("· " + text)
	}
}

// firstTime reports whether text differs from the last notice shown.
func (u *uiSession) firstTime(text string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	repeat := text == u.lastNote
	u.lastNote = text
	return !repeat
}

const uiSessionHelp = `Keys and slash commands
  Enter send            Alt-Enter new line            Ctrl-D leave (it keeps running)
  Ctrl-C or /interrupt  cancel the current turn, the session goes on
  /stop                 end the tool's process, after you confirm
  /facts                what the session acts as, reaches and costs
  Esc                   scroll the output with the arrow keys; Enter writes again
  //text                sends "/text" as a message`

func (u *uiSession) submit(text string) {
	command := ""
	if fields := strings.Fields(text); len(fields) > 0 {
		command = fields[0]
	}
	switch {
	case strings.HasPrefix(text, "//"):
		u.enqueue(func() { u.send(text[1:]) })
	case !strings.HasPrefix(text, "/"):
		u.enqueue(func() { u.send(text) })
	case command == "/help":
		u.sh.Say(uiSessionHelp)
	case command == "/facts":
		u.background(u.facts)
	case command == "/interrupt":
		u.background(u.interrupt)
	case command == "/stop":
		name := sessionLabel(u.current())
		u.sh.Ask("Stop "+name+"? It ends the tool; the conversation stays. [y/N]",
			func() { u.enqueue(u.stop) })
	default:
		u.sh.Say("Unknown command " + command + ". Type /help. To send text that starts with a slash, start it with //.")
	}
}

// enqueue runs job after the ones asked before it.
func (u *uiSession) enqueue(job func()) {
	u.pending.Add(1)
	select {
	case u.jobs <- job:
	default:
		u.pending.Done()
		u.sh.Say("! Too many requests are waiting. Try again in a moment.")
	}
}

func (u *uiSession) work() {
	for {
		select {
		case job := <-u.jobs:
			job()
			u.pending.Done()
		case <-u.ctx.Done():
			return
		}
	}
}

// waitIdle gives a request that is still running a moment to finish before the
// shell cancels it.
func (u *uiSession) waitIdle(limit time.Duration) {
	idle := make(chan struct{})
	go func() {
		u.pending.Wait()
		close(idle)
	}()
	select {
	case <-idle:
	case <-time.After(limit):
	}
}

// send is `olivares session send`: the same input check, the same state check,
// the same call. The reply arrives on the stream follow already reads.
func (u *uiSession) send(text string) {
	if err := checkSessionInputBytes([]byte(text)); err != nil {
		u.say(err)
		return
	}
	run, err := u.refresh()
	if err != nil {
		u.sh.Say("· Not sent. Press Up to bring the message back.")
		u.say(err)
		return
	}
	u.sh.Say("you  " + strings.ReplaceAll(text, "\n", "\n     "))
	if err := u.cfg.postTurn(u.ctx, run, text, nil); err != nil {
		u.sh.Say("· Not sent. Press Up to bring the message back.")
		u.say(err)
	}
}

// interrupt is `olivares session interrupt`. It does not wait behind a send.
func (u *uiSession) interrupt() { u.act("interrupt") }

func (u *uiSession) stop() { u.act("stop") }

// act runs `olivares session <verb> <session>` and shows its sentence.
func (u *uiSession) act(verb string) {
	text, _, err := u.runCLI([]string{"session", verb}, u.ref)
	if text != "" {
		u.sh.Say("· " + termSafe(text))
	}
	u.say(err)
	_, _ = u.refresh()
}

// facts says what the session read carries today of the six facts. ponytail:
// acts for, approved and proof are not in the session read yet; show them
// when the engine reports them.
func (u *uiSession) facts() {
	run, err := u.refresh()
	if err != nil {
		u.say(err)
	}
	runsAs := sessionDriver(run)
	if engineSupplied(run) {
		runsAs += " · with the engine's key"
	} else {
		runsAs += " · with its own sign-in"
	}
	if mode := termSafe(str(run, "tool_mode")); mode != "" {
		runsAs += " · " + mode
	}
	reaches := termSafe(str(run, "workspace_path"))
	if branch := termSafe(str(run, "worktree_branch")); branch != "" {
		reaches += " on branch " + branch
	}
	const unreported = "not reported by this engine yet"
	u.sh.Say("Acts for   " + unreported)
	u.sh.Say("Runs as    " + runsAs)
	u.sh.Say("Reaches    " + reaches)
	u.sh.Say("Costs      " + strings.TrimPrefix(runCostLine(run), "cost "))
	u.sh.Say("Approved   " + unreported)
	u.sh.Say("Proof      " + unreported)
}
