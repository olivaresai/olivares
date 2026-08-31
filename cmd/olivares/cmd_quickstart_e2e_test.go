// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/secure"
)

// cmd_quickstart_e2e_test.go RUNS `olivares quickstart`.
//
// ⛔ WHY THIS FILE EXISTS, measured 2026-08-27: the FIRST command README.md and INSTALL.md put
// in front of a community user had NOT ONE execution anywhere in the tree.
// an internal design note (not shipped) marks the `quickstart` row "1 verb, 0 with a test,
// NOT MEASURED" (deny-closed), and it was right: cmd_quickstart_test.go asserts the command's
// SHAPE (its flags, its secure-by-construction posture) and never starts it, and
// scripts/quickstart-smoke.sh boots the engine through `serve --seed-demo`, which is the docs
// page's path and NOT this verb. So the exact thing a first-time operator sees — the header,
// then the panel, then a one-time setup token they must paste — had never been watched by any
// gate. A wrapper is not "low risk" when nothing has ever run it: the panel is minted from live
// engine state (eng.setupTok.Ensure), and this very file's subject once printed a BLANK LINE
// where the token should be, on the second run of an unfinished data dir.
//
// It runs IN THE DEFAULT SUITE on purpose. The black-box binary smoke next door is behind
// `//go:build e2e`, so it is absent from every gate a lane or CI actually runs, and a row the
// matrix calls NOT MEASURED cannot be closed with a test nobody executes.
//
// WHAT IT ASSERTS, and each is a distinct way this can rot:
//   - the header arrives BEFORE the engine's startup checks (the ordering);
//   - the welcome panel arrives at all — i.e. announceQuickstart was reached, which means the
//     engine came up;
//   - the token is a REAL setup token by shape (olst_ + 52 base32 chars, core/secure/setup.go),
//     not merely a non-empty line — the defect this file guards against printed an empty one;
//   - the token the operator is shown VERIFIES against the store the engine wrote, so it is
//     the live credential and not an echo;
//   - the console URL printed is the loopback HTTPS one the secure defaults imply.

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
	t.Helper()
	out := &syncBuf{}
	cmd := newQuickstartCmd()
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
			t.Fatalf("quickstart exited before printing what was expected: %v\n%s", err, out.String())
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
			want, budget, out.String())
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

	// 1) The header is what an operator reads first, before any startup check.
	if !strings.Contains(got, "=== OLIVARES AI — FIRST RUN ===") {
		t.Fatalf("the first-run header is missing:\n%s", got)
	}
	// 2) The panel was reached, which means the engine came up.
	if !strings.Contains(got, "=== WELCOME TO OLIVARES AI ===") {
		t.Fatalf("the welcome panel is missing:\n%s", got)
	}
	// 3) The header comes BEFORE the panel (ordering: product first, checks after).
	if strings.Index(got, "=== OLIVARES AI — FIRST RUN ===") > strings.Index(got, "=== WELCOME TO OLIVARES AI ===") {
		t.Fatalf("the header must precede the welcome panel:\n%s", got)
	}
	// 4) The console URL is the loopback HTTPS one the secure defaults imply.
	if want := "https://" + listen; !strings.Contains(got, want) {
		t.Fatalf("the panel does not point at %s:\n%s", want, got)
	}
	// 5) The token is a real setup token BY SHAPE, and it VERIFIES against what the engine
	//    stored — the two halves of "this is a usable credential, not a decorative line".
	m := setupTokenShape.FindString(got)
	if m == "" {
		t.Fatalf("no setup token of the shape core/secure mints:\n%s", got)
	}
	if !strings.Contains(got, "one-time token") {
		t.Fatalf("the panel does not tell the operator what the token is for:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "setup.token")); err != nil {
		t.Fatalf("the engine did not persist a setup token store: %v", err)
	}
	if !secure.NewSetupToken(filepath.Join(dataDir, "setup.token")).Verify(m) {
		t.Fatal("the token printed to the operator does not verify against the stored one")
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
		t.Fatalf("the first run must mint a token:\n%s", first)
	}

	pending := regexp.MustCompile(`SETUP STILL PENDING`)
	second := runQuickstart(t, pending,
		"--data-dir", dataDir, "--listen", listen, "--grpc-listen", grpc, "--quiet")
	if !strings.Contains(second, "CANNOT be shown again") {
		t.Fatalf("the second run must explain that the token cannot be reshown:\n%s", second)
	}
	if strings.Contains(second, "setup.token") == false {
		t.Fatalf("the second run must name the file to delete to mint a fresh token:\n%s", second)
	}
	// NON-FIRING DIRECTION: it must NOT print a token-shaped string it cannot know.
	if setupTokenShape.MatchString(second) {
		t.Fatalf("the second run printed a token it cannot recover:\n%s", second)
	}
}
