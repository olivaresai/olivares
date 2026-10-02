// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/secure"
	"github.com/spf13/cobra"
)

// These tests execute the real quickstart command with an isolated SQLite store.
// They verify the console address, one-time token, setup-state guidance and logging.

// setupTokenShape is the token as core/secure/setup.go mints it: the operator-facing prefix
// plus unpadded base32 over 32 bytes of entropy (52 characters).
var setupTokenShape = regexp.MustCompile(`\bolst_[A-Z2-7]{52}\b`)

// syncBuf is a Writer the test can read while the command is still writing to it.
type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

// runQuickstart drives the real command by argv until `want` appears in its output (or the
// deadline passes), then cancels it and returns everything it printed.
func runQuickstart(t *testing.T, want *regexp.Regexp, args ...string) string {
	return runEngineCommand(t, newQuickstartCmd(), want, args...)
}

func runEngineCommand(t *testing.T, cmd *cobra.Command, want *regexp.Regexp, args ...string) string {
	t.Helper()
	out := &syncBuf{}
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetArgs(args)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()

	// ⛔ THE BUDGET IS FOR A FIRST BOOT ON A LOADED BOX, AND IT IS NOT DECORATIVE. The first
	// run of a fresh data directory creates the whole module schema before announce can mint
	// anything, and on a shared machine that is minutes, not seconds. A 90-second budget
	// expired mid-schema here: the cancel then surfaced as `create module table …: context
	// canceled`, and the assertion that ran next reported "the welcome panel is missing" — a
	// TEST timeout wearing the costume of a product defect. The budget is generous on purpose;
	// a run that genuinely never reaches the panel still fails, only later and with the right
	// sentence.
	const budget = 5 * time.Minute
	deadline := time.Now().Add(budget)
	reached := false
	for time.Now().Before(deadline) {
		if want.MatchString(out.String()) {
			reached = true
			break
		}
		select {
		case err := <-done:
			cancel()
			t.Fatalf("quickstart exited before printing what was expected: %v\n%s", err, setupTokenShape.ReplaceAllString(out.String(), "[setup token redacted]"))
		case <-time.After(100 * time.Millisecond):
		}
	}
	if !reached {
		// Say WHICH of the two happened. Falling through to the content assertions turns
		// "it was still booting" into "the panel is missing", which sends the reader after
		// the wrong defect.
		cancel()
		<-done
		t.Fatalf("quickstart did not print /%s/ within %s — it was still starting, or it never gets there:\n%s",
			want, budget, setupTokenShape.ReplaceAllString(out.String(), "[setup token redacted]"))
	}
	cancel()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("quickstart did not stop after its context was cancelled")
	}
	return out.String()
}

func TestQuickstartByArgvPrintsBannerAndSetupToken(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	listen := fmt.Sprintf("127.0.0.1:%d", freeLoopbackPort(t))
	grpc := fmt.Sprintf("127.0.0.1:%d", freeLoopbackPort(t))

	got := runQuickstart(t, setupTokenShape,
		"--data-dir", dataDir, "--listen", listen, "--grpc-listen", grpc, "--quiet")
	safeOutput := setupTokenShape.ReplaceAllString(got, "[setup token redacted]")

	if strings.Contains(got, "FIRST RUN") {
		t.Fatalf("quickstart must use state-aware guidance instead of an unconditional first-run header")
	}
	// 4) The console URL is the loopback HTTPS one the secure defaults imply.
	if want := "https://" + listen; !strings.Contains(got, want) {
		t.Fatalf("the panel does not point at %s:\n%s", want, safeOutput)
	}
	// 5) The token is a real setup token BY SHAPE, and it VERIFIES against what the engine
	//    stored — the two halves of "this is a usable credential, not a decorative line".
	m := setupTokenShape.FindString(got)
	if m == "" {
		t.Fatalf("no setup token of the shape core/secure mints:\n%s", safeOutput)
	}
	if !strings.Contains(got, "one-time token") {
		t.Fatalf("the panel does not tell the operator what the token is for:\n%s", safeOutput)
	}
	if !strings.Contains(got, firstHourWelcomeNextSteps) {
		t.Fatalf("the panel must point to the guided console flow:\n%s", safeOutput)
	}
	for _, unwanted := range []string{"passkey", "POST /v1/agents", "olivares doctor", "Privileged login"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("the panel contains an obsolete onboarding instruction %q:\n%s", unwanted, safeOutput)
		}
	}
	if _, err := os.Stat(filepath.Join(dataDir, "setup.token")); err != nil {
		t.Fatalf("the engine did not persist a setup token store: %v", err)
	}
	if !secure.NewSetupToken(filepath.Join(dataDir, "setup.token")).Verify(m) {
		t.Fatal("the token printed to the operator does not verify against the stored one")
	}
}

func TestQuickstartDefaultKeepsStartupChecksInLogFile(t *testing.T) {
	dir := t.TempDir()
	got := runQuickstart(t, setupTokenShape, "--data-dir", dir,
		"--listen", fmt.Sprintf("127.0.0.1:%d", freeLoopbackPort(t)),
		"--grpc-listen", fmt.Sprintf("127.0.0.1:%d", freeLoopbackPort(t)))
	if strings.Contains(got, "level=") {
		t.Fatalf("default output includes engine logs:\n%s", setupTokenShape.ReplaceAllString(got, "[setup token redacted]"))
	}
	file := filepath.Join(dir, "olivares.log")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"WRITER FENCE", "ACTUATED", "level=INFO"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("the log file lost startup check %q", want)
		}
	}
	if setupTokenShape.Match(data) {
		t.Fatal("the one-time setup token was written to the log file")
	}
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("log file permissions: info=%v err=%v", info, err)
	}
}

func TestQuickstartVerbosePrintsAndRetainsStartupChecks(t *testing.T) {
	dir := t.TempDir()
	got := runQuickstart(t, setupTokenShape, "--verbose", "--data-dir", dir,
		"--listen", fmt.Sprintf("127.0.0.1:%d", freeLoopbackPort(t)),
		"--grpc-listen", fmt.Sprintf("127.0.0.1:%d", freeLoopbackPort(t)))
	data, err := os.ReadFile(filepath.Join(dir, "olivares.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"WRITER FENCE", "ACTUATED", "level=INFO"} {
		if !strings.Contains(got, want) || !strings.Contains(string(data), want) {
			t.Errorf("--verbose must print and retain %q", want)
		}
	}
}

func TestQuickstartFailedBootKeepsStartupDiagnostics(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "olivares.db"), []byte("not a SQLite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cmd := newQuickstartCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--data-dir", dir, "--public-url", "https://console.example.invalid"})
	if err := cmd.ExecuteContext(context.Background()); err == nil {
		t.Fatal("corrupt store must fail boot")
	}
	data, err := os.ReadFile(filepath.Join(dir, "olivares.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "console: public address declared") {
		t.Fatal("failed boot discarded its startup diagnostics")
	}
	if strings.Contains(out.String(), "level=") {
		t.Fatal("failed boot printed internal records by default")
	}
}

func TestQuickstartInvalidConfigurationCreatesNoLog(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "absent")
	cmd := newQuickstartCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--data-dir", dir, "--public-url", "not a URL"})
	if err := cmd.ExecuteContext(context.Background()); err == nil {
		t.Fatal("invalid configuration must fail")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("invalid configuration created a data directory or log: %v", err)
	}
}

// TestQuickstartByArgvSecondRunSaysTheTokenIsGone drives the SAME data directory a second time.
// This is the state that once printed a blank line where the token should be: the store keeps
// only a hash, so Ensure returns no plaintext, and a panel that still says "complete setup with
// this one-time token" followed by nothing reads as a broken product. It is also the direction
// the happy path cannot prove — a test that only ever sees a fresh data dir would pass with the
// blank-line defect fully present.
func TestQuickstartByArgvSecondRunSaysTheTokenIsGone(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	listen := fmt.Sprintf("127.0.0.1:%d", freeLoopbackPort(t))
	grpc := fmt.Sprintf("127.0.0.1:%d", freeLoopbackPort(t))

	first := runQuickstart(t, setupTokenShape,
		"--data-dir", dataDir, "--listen", listen, "--grpc-listen", grpc, "--quiet")
	if !setupTokenShape.MatchString(first) {
		t.Fatalf("the first run must mint a token:\n%s", setupTokenShape.ReplaceAllString(first, "[setup token redacted]"))
	}

	pending := regexp.MustCompile(`Setup is still pending`)
	second := runQuickstart(t, pending,
		"--data-dir", dataDir, "--listen", listen, "--grpc-listen", grpc, "--quiet")
	if !strings.Contains(second, "cannot be shown again") {
		t.Fatalf("the second run must explain that the token cannot be reshown:\n%s", second)
	}
	if !strings.Contains(second, "--new-token") {
		t.Fatalf("the second run must name the supported token recovery command:\n%s", second)
	}
	// NON-FIRING DIRECTION: it must NOT print a token-shaped string it cannot know.
	if setupTokenShape.MatchString(second) {
		t.Fatalf("the second run printed a token it cannot recover:\n%s", setupTokenShape.ReplaceAllString(second, "[setup token redacted]"))
	}
}
