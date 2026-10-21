// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// logSink is a slog destination the test reads while engine goroutines may still write.
type logSink struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *logSink) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logSink) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func captureLog(t *testing.T) *logSink {
	t.Helper()
	sink := &logSink{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(sink, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return sink
}

// statusLine is one status-read line of the engine log: which read, which tool, how it
// ended and how long it took.
type statusLine struct {
	msg, driver, outcome string
	elapsedMS            int
}

var statusLineRE = regexp.MustCompile(`msg="tool sign-in status (read|process)" driver=(\S+) elapsed_ms=(\d+) outcome=(\S+)`)

func statusLines(out string) (lines []statusLine) {
	for _, m := range statusLineRE.FindAllStringSubmatch(out, -1) {
		ms, _ := strconv.Atoi(m[3])
		lines = append(lines, statusLine{msg: m[1], driver: m[2], outcome: m[4], elapsedMS: ms})
	}
	return lines
}

// wantLines asserts the log holds exactly one line per message name given, each for the
// driver, with the outcome, and at least minMS long.
func wantLines(t *testing.T, logs *logSink, driver, outcome string, minMS int, msgs ...string) map[string]statusLine {
	t.Helper()
	got := map[string]statusLine{}
	lines := statusLines(logs.String())
	for _, l := range lines {
		if _, dup := got[l.msg]; dup {
			t.Errorf("two %q lines:\n%s", l.msg, logs.String())
		}
		got[l.msg] = l
	}
	if len(lines) != len(msgs) {
		t.Errorf("%d status lines, want %d (%v):\n%s", len(lines), len(msgs), msgs, logs.String())
	}
	for _, msg := range msgs {
		l, ok := got[msg]
		if !ok {
			t.Errorf("no %q line:\n%s", msg, logs.String())
			continue
		}
		if l.driver != driver || l.outcome != outcome || l.elapsedMS < minMS {
			t.Errorf("%q line = %+v, want driver=%s outcome=%s elapsed_ms>=%d", msg, l, driver, outcome, minMS)
		}
	}
	return got
}

// statusModule is a module whose native status process is the shell script given.
func statusModule(t *testing.T, script string) (*Module, model.TenantID) {
	t.Helper()
	var m *Module
	_, home := newSignInServer(t, func(module *Module, _ string) { m = module })
	previous := toolCommand
	toolCommand = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c", script)
	}
	t.Cleanup(func() { toolCommand = previous })
	return m, model.TenantID(filepath.Base(home))
}

func slowAfter(t *testing.T, d time.Duration) {
	t.Helper()
	previous := slowStatusRead
	slowStatusRead = d
	t.Cleanup(func() { slowStatusRead = previous })
}

// #2575: release-first-hour run 39114374757 returned 503 from GET sign-in after 15 s on
// the AI tools page, and the engine log named neither the tool nor where the time went.
// A status read that fails now says which tool and how long it took, with no output of
// the tool in the line.
func TestAFailedStatusReadNamesTheTool(t *testing.T) {
	m, tenant := statusModule(t, "echo 'secret-looking output sk-abcdefghijklmnopqrstuvwxyz' >&2; exit 1")
	logs := captureLog(t)
	if _, _, err := m.LoginStatus(t.Context(), tenant, "opencode"); err == nil {
		t.Fatal("a status process that exits 1 must fail the read")
	}
	wantLines(t, logs, "opencode", "failed", 0, "process", "read")
	if out := logs.String(); strings.Contains(out, "sk-abcdefghij") || strings.Contains(out, "secret-looking") {
		t.Errorf("the tool's own output reached the log:\n%s", out)
	}
}

// A read past the console's load target is logged even when it succeeds: the process
// line and the whole-read line both carry the tool and the time, and the whole read is
// at least as long as the process.
func TestASlowStatusReadIsLoggedWithItsTime(t *testing.T) {
	slowAfter(t, 50*time.Millisecond)
	m, tenant := statusModule(t, "sleep 0.2; printf 'OpenAI oauth\\n'")
	logs := captureLog(t)
	if installed, signedIn, err := m.LoginStatus(t.Context(), tenant, "opencode"); err != nil || !installed || !signedIn {
		t.Fatalf("status = %v %v %v", installed, signedIn, err)
	}
	got := wantLines(t, logs, "opencode", "ok", 200, "process", "read")
	if got["read"].elapsedMS < got["process"].elapsedMS {
		t.Errorf("the whole read (%d ms) is shorter than its process (%d ms)", got["read"].elapsedMS, got["process"].elapsedMS)
	}
}

// The deadline that answered 503 after 15 s in first hour is named as a timeout.
func TestATimedOutStatusReadIsLoggedAsATimeout(t *testing.T) {
	previous := statusTimeout
	statusTimeout = 200 * time.Millisecond
	t.Cleanup(func() { statusTimeout = previous })
	m, tenant := statusModule(t, "exec sleep 5")
	logs := captureLog(t)
	if _, _, err := m.LoginStatus(t.Context(), tenant, "opencode"); err == nil {
		t.Fatal("a status process that outlives its deadline must fail the read")
	}
	wantLines(t, logs, "opencode", "timeout", 200, "process", "read")
}

// A tool that exits but leaves a child holding its output is cut off after the wait delay
// and answers the same 503 as a deadline: the log says timeout too.
func TestAStatusReadCutOffByTheWaitDelayIsLoggedAsATimeout(t *testing.T) {
	m, tenant := statusModule(t, "sleep 2 & printf 'OpenAI oauth\\n'")
	logs := captureLog(t)
	if _, _, err := m.LoginStatus(t.Context(), tenant, "opencode"); err == nil {
		t.Fatal("a status process whose output stays open must fail the read")
	}
	wantLines(t, logs, "opencode", "timeout", 0, "process", "read")
}

// Claude and Codex answer "not signed in" when their status is unreadable, and no error
// says so: the log does.
func TestAnUnreadableStatusAnswerIsLogged(t *testing.T) {
	m, tenant := statusModule(t, "echo 'garbage, not json'; exit 1")
	logs := captureLog(t)
	installed, signedIn, err := m.LoginStatus(t.Context(), tenant, "claude")
	if err != nil || !installed || signedIn {
		t.Fatalf("status = %v %v %v, want installed and signed out without an error", installed, signedIn, err)
	}
	wantLines(t, logs, "claude", "unrecognized", 0, "process", "read")
}

// A page that closed while the read ran is not a finding when the read was still quick,
// whether it closed before the tool started or while it ran.
func TestAStatusReadItsCallerAbandonedQuicklyIsNotLogged(t *testing.T) {
	m, tenant := statusModule(t, "exec sleep 5")
	logs := captureLog(t)
	before, cancelBefore := context.WithCancel(t.Context())
	cancelBefore()
	if _, _, err := m.LoginStatus(before, tenant, "opencode"); err == nil {
		t.Fatal("a canceled read must return its error")
	}
	during, cancelDuring := context.WithCancel(t.Context())
	time.AfterFunc(100*time.Millisecond, cancelDuring)
	if _, _, err := m.LoginStatus(during, tenant, "opencode"); err == nil {
		t.Fatal("a read canceled while the tool ran must return its error")
	}
	if lines := statusLines(logs.String()); len(lines) != 0 {
		t.Errorf("a quick abandoned read was logged:\n%s", logs.String())
	}
}

// ...but a page that gave up after the load target is how a stall looks from the page,
// and is logged as canceled with its time.
func TestAStatusReadItsCallerAbandonedAfterTheLoadTargetIsLogged(t *testing.T) {
	slowAfter(t, 50*time.Millisecond)
	m, tenant := statusModule(t, "exec sleep 5")
	logs := captureLog(t)
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(200*time.Millisecond, cancel)
	if _, _, err := m.LoginStatus(ctx, tenant, "opencode"); err == nil {
		t.Fatal("a canceled read must return its error")
	}
	wantLines(t, logs, "opencode", "canceled", 200, "process", "read")
}

// The provider probe reads the process directly: it logs the process, not a whole read.
func TestTheProviderProbeLogsTheProcessAlone(t *testing.T) {
	m, _ := statusModule(t, "exit 1")
	logs := captureLog(t)
	home := t.TempDir()
	env := []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
	if _, err := m.readNativeStatus(t.Context(), SignInStatus{Driver: "opencode", Installed: true}, "/bin/sh", home, env); err == nil {
		t.Fatal("a status process that exits 1 must fail the read")
	}
	wantLines(t, logs, "opencode", "failed", 0, "process")
}

// A caller that joins a read already running waits for it and answers with its result: one
// process, and a whole-read line for each caller.
func TestACallerJoiningASharedFailingReadLogsItsOwnWait(t *testing.T) {
	m, tenant := statusModule(t, "sleep 1; exit 1")
	logs := captureLog(t)
	var wg sync.WaitGroup
	wg.Go(func() { _, _, _ = m.LoginStatus(t.Context(), tenant, "opencode") })
	time.Sleep(100 * time.Millisecond)
	wg.Go(func() { _, _, _ = m.LoginStatus(t.Context(), tenant, "opencode") })
	wg.Wait()
	var process, read int
	for _, l := range statusLines(logs.String()) {
		if l.outcome != "failed" || l.driver != "opencode" {
			t.Errorf("unexpected line %+v", l)
		}
		if l.msg == "process" {
			process++
		} else {
			read++
		}
	}
	if process != 1 || read != 2 {
		t.Errorf("process lines = %d, read lines = %d; want 1 and 2:\n%s", process, read, logs.String())
	}
}

// A read that answers quickly says nothing: the engine's log is not a per-request trace.
func TestAFastStatusReadIsNotLogged(t *testing.T) {
	m, tenant := statusModule(t, "printf 'OpenAI oauth\\n'")
	logs := captureLog(t)
	if installed, signedIn, err := m.LoginStatus(t.Context(), tenant, "opencode"); err != nil || !installed || !signedIn {
		t.Fatalf("status = %v %v %v", installed, signedIn, err)
	}
	if out := logs.String(); strings.Contains(out, "tool sign-in status") {
		t.Errorf("a fast, successful read was logged:\n%s", out)
	}
}
