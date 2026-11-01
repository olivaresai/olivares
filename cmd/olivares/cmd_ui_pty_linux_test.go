// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
	"github.com/olivaresai/olivares/cmd/olivares/internal/tui"
)

// The pty tests drive the REAL process: the test binary re-executes itself as
// the CLI through TestMain (OLIVARES_CLI_TRAMPOLINE), so raw mode, the window
// size and the restore path are the ones an operator gets, not a simulation.

const ptyWait = 10 * time.Second

var sgr = regexp.MustCompile("\x1b\\[[0-9;]*m")

// openPTY returns both ends of a new pty with the requested window size, and
// skips when this host gives the test no pty.
func openPTY(t *testing.T, width, height int) (master, slave *os.File) {
	t.Helper()
	ptmx, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("this host gives the test no pty: %v", err)
	}
	t.Cleanup(func() { _ = ptmx.Close() })
	if err := unix.IoctlSetPointerInt(int(ptmx.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatalf("unlock the pty: %v", err)
	}
	number, err := unix.IoctlGetInt(int(ptmx.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatalf("read the pty number: %v", err)
	}
	slave, err = os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open the pty slave: %v", err)
	}
	t.Cleanup(func() { _ = slave.Close() })
	size := &unix.Winsize{Row: uint16(height), Col: uint16(width)}
	if err := unix.IoctlSetWinsize(int(ptmx.Fd()), unix.TIOCSWINSZ, size); err != nil {
		t.Fatalf("set the pty size: %v", err)
	}
	return ptmx, slave
}

// cliProcessEnv is what a child CLI gets: a re-exec of this test binary, a
// throwaway config and data directory, no engine URL, and no inherited
// NO_COLOR/TERM, so every run starts from the known frame.
func cliProcessEnv(t *testing.T, extra ...string) []string {
	t.Helper()
	home := t.TempDir()
	env := make([]string, 0, len(os.Environ())+10)
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case "OLIVARES_CLI_TRAMPOLINE", cliConfigOverrideEnv, "OLIVARES_DATA_DIR", "OLIVARES_SERVER_URL",
			"NO_COLOR", "TERM", "COLUMNS", "HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME":
			continue
		}
		env = append(env, kv)
	}
	env = append(env,
		"OLIVARES_CLI_TRAMPOLINE=1",
		cliConfigOverrideEnv+"="+filepath.Join(home, "config.yaml"),
		"OLIVARES_DATA_DIR="+filepath.Join(home, "data"),
		"OLIVARES_SERVER_URL=",
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"XDG_DATA_HOME="+filepath.Join(home, ".local", "share"),
		"TERM=xterm-256color",
	)
	return append(env, extra...)
}

func startCLI(t *testing.T, env []string, in *os.File, out, errOut io.Writer, args ...string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve the test binary: %v", err)
	}
	// #nosec G204 -- exe is this test binary; args are fixed test arguments.
	cmd := exec.Command(exe, args...)
	cmd.Env = env
	cmd.Stdin = in
	cmd.Stdout = out
	cmd.Stderr = errOut
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %v: %v", args, err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	return cmd
}

func waitCLI(t *testing.T, cmd *exec.Cmd) int {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			return 0
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		t.Fatalf("wait for the CLI: %v", err)
	case <-time.After(ptyWait):
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("the CLI did not exit within %s", ptyWait)
	}
	return -1
}

// waitRead reports whether the pty has a byte ready within timeout. A pty
// master does not honor os.File read deadlines on every kernel, so the wait
// goes through poll: a test that cannot see its screen must fail, not hang.
func waitRead(t *testing.T, f *os.File, timeout time.Duration) bool {
	t.Helper()
	fds := []unix.PollFd{{Fd: int32(f.Fd()), Events: unix.POLLIN}}
	for {
		n, err := unix.Poll(fds, int(timeout.Milliseconds()))
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			t.Fatalf("poll the pty: %v", err)
		}
		return n > 0 && fds[0].Revents&unix.POLLIN != 0
	}
}

// readScreen reads the pty until the visible screen contains want.
func readScreen(t *testing.T, master *os.File, want string) string {
	return readScreenWithin(t, master, want, ptyWait)
}

// readScreenWithin is readScreen with its own budget, for a step that waits on work the
// engine does, such as an install.
func readScreenWithin(t *testing.T, master *os.File, want string, wait time.Duration) string {
	t.Helper()
	var raw strings.Builder
	buf := make([]byte, 4096)
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if !waitRead(t, master, 200*time.Millisecond) {
			continue
		}
		n, err := master.Read(buf)
		if n > 0 {
			raw.Write(buf[:n])
			if strings.Contains(termrender.StripANSI(raw.String()), want) {
				return raw.String()
			}
		}
		if err != nil {
			t.Fatalf("read the pty: %v; saw:\n%s", err, termrender.StripANSI(raw.String()))
		}
	}
	t.Fatalf("the screen never showed %q; saw:\n%s", want, termrender.StripANSI(raw.String()))
	return ""
}

// readAvailable drains what the pty holds once the writer has exited, and
// stops after the output has been quiet for one poll period.
func readAvailable(t *testing.T, master *os.File) string {
	t.Helper()
	var raw strings.Builder
	buf := make([]byte, 4096)
	for {
		if !waitRead(t, master, 300*time.Millisecond) {
			return raw.String()
		}
		n, err := master.Read(buf)
		if n > 0 {
			raw.Write(buf[:n])
			continue
		}
		if err != nil {
			t.Fatalf("read the pty: %v", err)
		}
	}
}

func termiosOf(t *testing.T, f *os.File) *unix.Termios {
	t.Helper()
	state, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatalf("read the pty terminal state: %v", err)
	}
	return state
}

func TestUIOpensTheMenuOnATerminal(t *testing.T) {
	master, slave := openPTY(t, tui.MinWidth, tui.MinHeight)
	cmd := startCLI(t, cliProcessEnv(t), slave, slave, slave, "ui")
	screen := termrender.StripANSI(readScreen(t, master, "Get started"))
	for _, want := range []string{
		"Olivares AI", editionLabel(thisEdition.name),
		"Get started", "AI tools", "Sessions", "Security", "Settings", "All commands",
		"▸", "q quit",
	} {
		if !strings.Contains(screen, want) {
			t.Errorf("the menu is missing %q:\n%s", want, screen)
		}
	}
	if !strings.Contains(screen, "= olivares ") {
		t.Errorf("the selected row shows no equivalent command:\n%s", screen)
	}
	// A real arrow key travels through the pty as ESC [ B, so this also
	// exercises the split-sequence wait instead of a prepared buffer.
	if _, err := master.Write([]byte("\x1b[B")); err != nil {
		t.Fatalf("send the down arrow: %v", err)
	}
	screen = termrender.StripANSI(readScreen(t, master, "▸ AI tools"))
	if !strings.Contains(screen, "= olivares tool ls") {
		t.Errorf("AI tools shows no equivalent command:\n%s", screen)
	}
	if _, err := master.Write([]byte("q")); err != nil {
		t.Fatalf("write q: %v", err)
	}
	if code := waitCLI(t, cmd); code != 0 {
		t.Fatalf("q exited %d, want 0", code)
	}
}

func TestUIRefusesWhenStdoutIsNotATerminal(t *testing.T) {
	_, slave := openPTY(t, 100, 30)
	var out, errOut bytes.Buffer
	cmd := startCLI(t, cliProcessEnv(t), slave, &out, &errOut, "ui")
	if code := waitCLI(t, cmd); code != 2 {
		t.Fatalf("olivares ui on a pipe exited %d, want 2", code)
	}
	msg := errOut.String()
	for _, want := range []string{"olivares ui needs a terminal", "olivares status", "olivares tool ls", "olivares session start <folder>"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not name %q: %q", want, msg)
		}
	}
	if out.Len() != 0 {
		t.Fatalf("the refused frame wrote to stdout: %q", out.String())
	}
}

func TestUIRefusesWhenStdinIsNotATerminal(t *testing.T) {
	_, slave := openPTY(t, 100, 30)
	var errOut bytes.Buffer
	cmd := startCLI(t, cliProcessEnv(t), nil, slave, &errOut, "ui")
	if code := waitCLI(t, cmd); code != 2 {
		t.Fatalf("olivares ui without terminal stdin exited %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "olivares ui needs a terminal") {
		t.Fatalf("the refusal sentence is missing: %q", errOut.String())
	}
}

func TestUIRefusesATerminalBelow80x24(t *testing.T) {
	for _, tc := range []struct {
		name          string
		width, height int
	}{
		{"79 columns", tui.MinWidth - 1, tui.MinHeight},
		{"23 rows", tui.MinWidth, tui.MinHeight - 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, slave := openPTY(t, tc.width, tc.height)
			var errOut bytes.Buffer
			cmd := startCLI(t, cliProcessEnv(t), slave, slave, &errOut, "ui")
			if code := waitCLI(t, cmd); code != 2 {
				t.Fatalf("a %dx%d terminal exited %d, want 2", tc.width, tc.height, code)
			}
			if !strings.Contains(errOut.String(), "Make the terminal at least 80 x 24") {
				t.Fatalf("the size sentence is missing: %q", errOut.String())
			}
		})
	}
}

func TestUICtrlCRestoresTheTerminal(t *testing.T) {
	master, slave := openPTY(t, 100, 30)
	before := termiosOf(t, slave)
	cmd := startCLI(t, cliProcessEnv(t), slave, slave, slave, "ui")
	readScreen(t, master, "Get started")
	if _, err := master.Write([]byte{0x03}); err != nil {
		t.Fatalf("send Ctrl-C: %v", err)
	}
	if code := waitCLI(t, cmd); code != 130 {
		t.Fatalf("Ctrl-C exited %d, want 130", code)
	}
	if after := termiosOf(t, slave); *after != *before {
		t.Fatalf("the terminal was not restored after Ctrl-C:\nbefore: %+v\nafter:  %+v", before, after)
	}
	assertLeftScreen(t, readAvailable(t, master), "Ctrl-C")
}

// assertLeftScreen checks the whole exit, not just termios: the alternate
// screen is gone and the cursor is visible again.
func assertLeftScreen(t *testing.T, screen, how string) {
	t.Helper()
	for _, want := range []string{"\x1b[?25h", "\x1b[?1049l"} {
		if !strings.Contains(screen, want) {
			t.Fatalf("after %s the frame did not write %q: %q", how, want, screen)
		}
	}
}

func TestUISignalRestoresTheTerminal(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			master, slave := openPTY(t, 100, 30)
			before := termiosOf(t, slave)
			cmd := startCLI(t, cliProcessEnv(t), slave, slave, slave, "ui")
			readScreen(t, master, "Get started")
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatalf("send %v: %v", sig, err)
			}
			if code := waitCLI(t, cmd); code != 128+int(sig) {
				t.Fatalf("%v exited %d, want %d", sig, code, 128+int(sig))
			}
			if after := termiosOf(t, slave); *after != *before {
				t.Fatalf("the terminal was not restored after %v:\nbefore: %+v\nafter:  %+v", sig, before, after)
			}
			assertLeftScreen(t, readAvailable(t, master), sig.String())
		})
	}
}

func TestUINOCOLOREmitsNoColour(t *testing.T) {
	master, slave := openPTY(t, 100, 30)
	cmd := startCLI(t, cliProcessEnv(t, "NO_COLOR=1"), slave, slave, slave, "ui")
	screen := readScreen(t, master, "Get started")
	if _, err := master.Write([]byte("q")); err != nil {
		t.Fatalf("write q: %v", err)
	}
	if code := waitCLI(t, cmd); code != 0 {
		t.Fatalf("q exited %d, want 0", code)
	}
	// Scan the whole stream, not only the bytes drawn before the first
	// assertion: a colour emitted on a redraw or on the way out counts too.
	screen += readAvailable(t, master)
	if sgr.MatchString(screen) {
		t.Fatalf("NO_COLOR still emitted SGR %q", sgr.FindString(screen))
	}
}

func TestUIColorIsAdditive(t *testing.T) {
	master, slave := openPTY(t, 100, 30)
	cmd := startCLI(t, cliProcessEnv(t), slave, slave, slave, "ui")
	screen := readScreen(t, master, "Get started")
	if !strings.Contains(screen, "\x1b[32m") {
		t.Fatalf("the selected row carried no colour: %q", screen)
	}
	if _, err := master.Write([]byte("q")); err != nil {
		t.Fatalf("write q: %v", err)
	}
	if code := waitCLI(t, cmd); code != 0 {
		t.Fatalf("q exited %d, want 0", code)
	}
}

func TestBareOlivaresHintsTheUIOnATerminal(t *testing.T) {
	master, slave := openPTY(t, 100, 30)
	cmd := startCLI(t, cliProcessEnv(t), slave, slave, slave)
	if code := waitCLI(t, cmd); code != 0 {
		t.Fatalf("bare olivares exited %d, want 0", code)
	}
	screen := termrender.StripANSI(readAvailable(t, master))
	if !strings.Contains(screen, "Interactive: olivares ui") {
		t.Fatalf("bare olivares on a terminal did not hint the menu:\n%s", screen)
	}
	for _, line := range strings.Split(screen, "\n") {
		if strings.HasPrefix(strings.TrimSuffix(line, "\r"), "  ui ") {
			t.Fatalf("bare olivares repeats the ui row:\n%s", screen)
		}
	}
}
