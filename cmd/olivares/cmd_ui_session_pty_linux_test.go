// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
	"github.com/olivaresai/olivares/cmd/olivares/internal/tui"
)

// These tests run the real CLI on a pty against the fake session engine, so the
// stream, the composer, the confirmation and the commands the shell runs are the
// ones a person gets.

const uiRunRef = "01a0f6dc-0000-7000-8000-0000000000a1"

// uiSessionEngine is the fake engine with one running session. Fields a test
// changes go in setup, which runs before the server starts (see newFakeSessionEngine).
func uiSessionEngine(t *testing.T, setup ...func(*fakeSessionEngine)) *fakeSessionEngine {
	t.Helper()
	return newFakeSessionEngine(t, append([]func(*fakeSessionEngine){oneRunningSession}, setup...)...)
}

func oneRunningSession(f *fakeSessionEngine) {
	f.runs = []map[string]any{{
		"run_ref": uiRunRef, "name": "demo", "state": "running", "provider_driver": "claude",
		"transport": "stream-json", "workspace_path": "/srv/demo",
	}}
	f.replay = []string{claudeInitFrame, `{"type":"assistant","message":{"content":[{"type":"text","text":"An OLD answer from before."}]}}`}
	f.reply = []string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"ls -la"}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Two files: README and main.go."}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"Two files: README and main.go.","total_cost_usd":0.0123,"duration_ms":3200}`,
	}
}

// uiSessionRun is one shell under test: the pty to type on, the engine and the CLI.
// A pump reads everything the CLI draws, so the CLI never blocks on a full pty
// buffer while a test is not looking at the screen.
type uiSessionRun struct {
	t      *testing.T
	master *os.File
	slave  *os.File
	cmd    *exec.Cmd
	f      *fakeSessionEngine

	mu   sync.Mutex
	out  []byte
	from int // where the last key press began; screen looks only at output after it
}

// openUISession starts `olivares ui session <name>` on a pty against f.
func openUISession(t *testing.T, f *fakeSessionEngine, name string) *uiSessionRun {
	t.Helper()
	master, slave := openPTY(t, tui.MinWidth, tui.MinHeight)
	cmd := startCLI(t, cliProcessEnv(t), slave, slave, slave,
		append([]string{"ui", "session", name}, sessionCreds(f.URL)...)...)
	r := &uiSessionRun{t: t, master: master, slave: slave, cmd: cmd, f: f}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			r.mu.Lock()
			r.out = append(r.out, buf[:n]...)
			r.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	return r
}

// wait returns the CLI's exit code, and fails with the last screen when it does not exit.
func (r *uiSessionRun) wait() int {
	r.t.Helper()
	done := make(chan error, 1)
	go func() { done <- r.cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			return 0
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		r.t.Fatalf("wait for the CLI: %v", err)
	case <-time.After(ptyWait):
		_ = r.cmd.Process.Kill()
		<-done
		r.t.Fatalf("the CLI did not exit within %s; the screen was:\n%s", ptyWait, lastFrame(r.since(0)))
	}
	return -1
}

func (r *uiSessionRun) since(from int) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.out[min(from, len(r.out)):])
}

// raw waits until the output after the last key press shows want and returns it, escapes included.
func (r *uiSessionRun) raw(want string) string {
	r.t.Helper()
	r.mu.Lock()
	from := r.from
	r.mu.Unlock()
	deadline := time.Now().Add(ptyWait)
	for time.Now().Before(deadline) {
		text := r.since(from)
		if strings.Contains(termrender.StripANSI(text), want) {
			return text
		}
		time.Sleep(20 * time.Millisecond)
	}
	r.t.Fatalf("the screen never showed %q; the last frame was:\n%s", want, lastFrame(r.since(from)))
	return ""
}

// screen waits for want and returns the plain text of everything drawn since the last key press.
func (r *uiSessionRun) screen(want string) string {
	r.t.Helper()
	return termrender.StripANSI(r.raw(want))
}

// lastFrame is the last full repaint in raw output, as plain text.
func lastFrame(raw string) string {
	frames := strings.Split(raw, "\x1b[H")
	return termrender.StripANSI(frames[len(frames)-1])
}

func (r *uiSessionRun) press(keys string) {
	r.t.Helper()
	r.mu.Lock()
	r.from = len(r.out)
	r.mu.Unlock()
	if _, err := r.master.Write([]byte(keys)); err != nil {
		r.t.Fatalf("type %q: %v", keys, err)
	}
}

// posts counts the requests the engine saw for a verb on the session.
func (r *uiSessionRun) posts(verb string) int {
	return len(r.f.postsTo(sessionRunsPath + "/" + uiRunRef + "/" + verb))
}

func TestUISessionStreamsOneMessageAndItsReply(t *testing.T) {
	f := uiSessionEngine(t)
	r := openUISession(t, f, "demo")
	text := r.screen("An OLD answer from before.")
	for _, want := range []string{"demo · claude · demo", "Running", "Enter send", "/help"} {
		if !strings.Contains(text, want) {
			t.Errorf("the shell is missing %q:\n%s", want, text)
		}
	}
	r.press("list the files\r")
	text = r.screen("— turn finished")
	for _, want := range []string{"you  list the files", "→ Bash ls -la", "Two files: README and main.go."} {
		if !strings.Contains(text, want) {
			t.Errorf("the reply is missing %q:\n%s", want, text)
		}
	}
	input := f.postsTo(sessionRunsPath + "/" + uiRunRef + "/input")
	if len(input) != 1 || !strings.Contains(input[0]["line"].(string), `"content":"list the files"`) {
		t.Fatalf("input = %v, want the message once, as `session send` sends it", input)
	}
	r.press("\x04")
	if code := r.wait(); code != 0 {
		t.Fatalf("Ctrl-D exited %d, want 0", code)
	}
	if n := r.posts("stop"); n != 0 {
		t.Fatalf("leaving the shell stopped the session (%d stop requests)", n)
	}
}

func TestUISessionStopAsksFirstAndStopsOnlyOnY(t *testing.T) {
	r := openUISession(t, uiSessionEngine(t), "demo")
	r.screen("An OLD answer from before.")

	r.press("/stop\r")
	r.screen("Stop demo?")
	r.press("n")
	r.press("/help\r")
	r.screen("Ctrl-C or /interrupt")
	if r.posts("stop") != 0 {
		t.Fatal("a no stopped the session")
	}

	r.press("/stop\r")
	r.screen("y confirms")
	if r.posts("stop") != 0 {
		t.Fatal("the session stopped before the answer")
	}
	r.press("y")
	r.screen("Stopped demo.")
	if n := r.posts("stop"); n != 1 {
		t.Fatalf("stop requests = %d, want 1", n)
	}
	r.press("\x04")
	if code := r.wait(); code != 0 {
		t.Fatalf("Ctrl-D exited %d, want 0", code)
	}
}

func TestUISessionInterruptKeepsTheShellOpen(t *testing.T) {
	r := openUISession(t, uiSessionEngine(t), "demo")
	r.screen("An OLD answer from before.")

	r.press("\x03")
	r.screen("Interrupted the current turn of demo.")
	r.press("/interrupt\r")
	deadline := time.Now().Add(ptyWait)
	for r.posts("interrupt") < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := r.posts("interrupt"); n != 2 {
		t.Fatalf("interrupt requests = %d, want 2 (Ctrl-C and /interrupt)", n)
	}
	r.press("\x04")
	if code := r.wait(); code != 0 {
		t.Fatalf("Ctrl-D exited %d, want 0: Ctrl-C must not leave the shell", code)
	}
}

func TestUISessionShowsFactsAndAnUnknownCommand(t *testing.T) {
	r := openUISession(t, uiSessionEngine(t), "demo")
	r.screen("An OLD answer from before.")
	r.press("/facts\r")
	text := r.screen("Proof")
	for _, want := range []string{"Runs as    claude", "Reaches    /srv/demo", "Costs"} {
		if !strings.Contains(text, want) {
			t.Errorf("/facts is missing %q:\n%s", want, text)
		}
	}
	r.press("/nope\r")
	r.screen("Unknown command /nope.")
	if n := r.posts("input"); n != 0 {
		t.Fatalf("a slash command reached the session as %d messages", n)
	}
	r.press("\x04")
	r.wait()
}

func TestUISessionSteeringAnotherPersonsSessionIsNotOffered(t *testing.T) {
	f := uiSessionEngine(t, func(f *fakeSessionEngine) {
		f.steerRefusal = "Steering a session another person started requires Olivares Business."
	})
	r := openUISession(t, f, "demo")
	r.screen("An OLD answer from before.")
	r.press("hello\r")
	text := r.screen("read-only")
	if !strings.Contains(text, "requires Olivares Business") {
		t.Errorf("the refusal is not on screen:\n%s", text)
	}
	if !strings.Contains(text, "An OLD answer from before.") {
		t.Errorf("the output stopped following after the refusal:\n%s", text)
	}
	r.press("again\r")
	time.Sleep(200 * time.Millisecond)
	if n := r.posts("input"); n != 1 {
		t.Fatalf("input requests = %d, want 1: the composer must stay off after the refusal", n)
	}
	r.press("q")
	if code := r.wait(); code != 0 {
		t.Fatalf("q exited %d, want 0", code)
	}
}

func TestUISessionNewStartsASessionWithTheDefaults(t *testing.T) {
	f := newFakeSessionEngine(t)
	r := openUISession(t, f, "new")
	text := r.screen("Following")
	if !strings.Contains(text, "claude") || !strings.Contains(text, "Enter send") || !strings.Contains(text, "· Using Claude Code (own login)") {
		t.Errorf("the new session's shell is incomplete:\n%s", text)
	}
	runs := f.postsTo(sessionRunsPath)
	if len(runs) != 1 || runs[0]["template_id"] != "tpl-edit-run" {
		t.Fatalf("launches = %v, want one with the same defaults as `session start`", runs)
	}
	r.press("\x04")
	if code := r.wait(); code != 0 {
		t.Fatalf("Ctrl-D exited %d, want 0", code)
	}
}

func TestUISessionRefusesWithoutAName(t *testing.T) {
	_, slave := openPTY(t, tui.MinWidth, tui.MinHeight)
	var errOut bytes.Buffer
	cmd := startCLI(t, cliProcessEnv(t), slave, slave, &errOut, "ui", "session")
	if code := waitCLI(t, cmd); code != 2 {
		t.Fatalf("ui session without a name exited %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "olivares ui session new") {
		t.Fatalf("the sentence does not name the way to start one: %q", errOut.String())
	}
}

func TestUISessionRefusesWhenStdoutIsNotATerminal(t *testing.T) {
	_, slave := openPTY(t, tui.MinWidth, tui.MinHeight)
	var out, errOut bytes.Buffer
	cmd := startCLI(t, cliProcessEnv(t), slave, &out, &errOut, "ui", "session", "demo")
	if code := waitCLI(t, cmd); code != 2 {
		t.Fatalf("ui session on a pipe exited %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "olivares ui needs a terminal") || out.Len() != 0 {
		t.Fatalf("the refusal = %q, stdout = %q", errOut.String(), out.String())
	}
}

// input waits for the engine to have seen n messages and returns the last body's line.
func (r *uiSessionRun) input(n int) string {
	r.t.Helper()
	deadline := time.Now().Add(ptyWait)
	for r.posts("input") < n && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	posts := r.f.postsTo(sessionRunsPath + "/" + uiRunRef + "/input")
	if len(posts) < n {
		r.t.Fatalf("the engine saw %d messages, want %d", len(posts), n)
	}
	line, _ := posts[n-1]["line"].(string)
	return line
}

func TestUISessionRestoresTheTerminalWhenItLeaves(t *testing.T) {
	r := openUISession(t, uiSessionEngine(t), "demo")
	r.screen("An OLD answer from before.")
	if termiosOf(t, r.slave).Lflag&unix.ICANON != 0 {
		t.Fatal("the terminal was not raw while the shell ran")
	}
	r.press("\x04")
	raw := r.raw("Left demo.")
	for _, want := range []string{"\x1b[?2004l", "\x1b[?25h", "\x1b[?1049l", "Open it again: olivares ui session demo"} {
		if !strings.Contains(raw, want) {
			t.Errorf("leaving did not write %q", want)
		}
	}
	if code := r.wait(); code != 0 {
		t.Fatalf("Ctrl-D exited %d, want 0", code)
	}
	if termiosOf(t, r.slave).Lflag&unix.ICANON == 0 {
		t.Fatal("the terminal was left raw")
	}
}

func TestUISessionPasteDoesNotSendUntilEnter(t *testing.T) {
	r := openUISession(t, uiSessionEngine(t), "demo")
	r.screen("An OLD answer from before.")
	r.press("\x1b[200~first\rsecond\x1b[201~")
	r.screen("> first")
	if n := r.posts("input"); n != 0 {
		t.Fatalf("a newline inside a paste sent %d messages", n)
	}
	r.press("\r")
	if line := r.input(1); !strings.Contains(line, `"content":"first\nsecond"`) {
		t.Fatalf("the message = %s, want both pasted lines", line)
	}
	r.press("\x04")
	r.wait()
}

func TestUISessionDoubleSlashSendsTheTextAfterOne(t *testing.T) {
	r := openUISession(t, uiSessionEngine(t), "demo")
	r.screen("An OLD answer from before.")
	r.press("//etc/hosts is wrong\r")
	if line := r.input(1); !strings.Contains(line, `"content":"/etc/hosts is wrong"`) {
		t.Fatalf("the message = %s, want /etc/hosts is wrong", line)
	}
	r.press("/stop now\r")
	r.screen("Stop demo?")
	r.press("n")
	r.press("\x04")
	r.wait()
}

func TestUISessionTooSmallAfterResizeThenBack(t *testing.T) {
	r := openUISession(t, uiSessionEngine(t), "demo")
	r.screen("An OLD answer from before.")
	resize := func(width, height uint16) {
		size := &unix.Winsize{Row: height, Col: width}
		if err := unix.IoctlSetWinsize(int(r.master.Fd()), unix.TIOCSWINSZ, size); err != nil {
			t.Fatal(err)
		}
		if err := r.cmd.Process.Signal(syscall.SIGWINCH); err != nil {
			t.Fatal(err)
		}
	}
	resize(60, 20)
	r.screen("Make the terminal at least 80 x 24")
	r.press("typed while hidden\r")
	// Nothing visible changes while the keys are ignored, so give the shell time
	// to read them before the size comes back.
	time.Sleep(300 * time.Millisecond)
	resize(100, 30)
	text := r.screen("Enter send")
	if n := r.posts("input"); n != 0 {
		t.Fatalf("keys acted on a hidden screen: %d messages", n)
	}
	if !strings.Contains(text, "An OLD answer from before.") {
		t.Errorf("the transcript is missing after the resize:\n%s", text)
	}
	r.press("\x04")
	r.wait()
}

func TestUISessionReopensAnEndedStreamAndShowsEachFrameOnce(t *testing.T) {
	f := uiSessionEngine(t, func(f *fakeSessionEngine) { f.endAfterInput = true })
	r := openUISession(t, f, "demo")
	r.screen("An OLD answer from before.")
	r.press("list the files\r")
	r.screen("The session's output ended.")
	attaches := func() int {
		f.mu.Lock()
		defer f.mu.Unlock()
		n := 0
		for _, hit := range f.hits {
			if strings.HasSuffix(hit, "/attach") {
				n++
			}
		}
		return n
	}
	deadline := time.Now().Add(2 * uiRecheck)
	for attaches() < 2 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if attaches() < 2 {
		t.Fatalf("the shell did not reopen the stream (%d attach requests)", attaches())
	}
	r.press("\x03")
	r.screen("Interrupted the current turn")
	text := lastFrame(r.since(0))
	if n := strings.Count(text, "→ Bash ls -la"); n != 1 {
		t.Fatalf("the tool call is on screen %d times after the stream reopened:\n%s", n, text)
	}
	if n := strings.Count(text, "The session's output ended."); n != 1 {
		t.Fatalf("the ended notice is on screen %d times:\n%s", n, text)
	}
	r.press("\x04")
	r.wait()
}
