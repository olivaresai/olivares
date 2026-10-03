// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package main

import (
	"context"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

type toolLoginWaitingInput struct {
	io.Reader
	ready func()
}

func (in toolLoginWaitingInput) Read(p []byte) (int, error) {
	in.ready()
	return in.Reader.Read(p)
}

// TestToolLoginCancelsTheSignInOnASignal: Ctrl-C (SIGINT) or SIGTERM during `tool
// login` cancels the sign-in on the engine before the CLI exits, so the tool's own
// login process does not keep waiting there, and the CLI says so in one line. Claude
// is interrupted while it waits for a pasted code, Codex while it polls.
func TestToolLoginCancelsTheSignInOnASignal(t *testing.T) {
	for _, tc := range []struct {
		driver, id, name string
		sig              syscall.Signal
	}{
		{"claude", "s1", "Claude Code", syscall.SIGINT},
		{"codex", "s2", "Codex", syscall.SIGTERM},
	} {
		t.Run(tc.driver, func(t *testing.T) {
			f := newFakeToolEngine(t)
			f.pending = true
			listening, done := make(chan struct{}), make(chan struct{})
			prev := toolLoginSignals
			t.Cleanup(func() { toolLoginSignals = prev })
			toolLoginSignals = func(ctx context.Context, sig ...os.Signal) (context.Context, context.CancelFunc) {
				c, stop := prev(ctx, sig...)
				close(listening)
				return c, stop
			}
			stdin, w := io.Pipe() // nothing is ever pasted
			t.Cleanup(func() { _ = w.Close() })
			input := io.Reader(stdin)
			if tc.driver == "claude" {
				input = toolLoginWaitingInput{Reader: stdin, ready: func() {
					f.startedOnce.Do(func() { close(f.started) })
				}}
			}
			go func() {
				for _, ch := range []chan struct{}{listening, f.started} {
					select {
					case <-ch:
					case <-done:
						return
					}
				}
				_ = syscall.Kill(os.Getpid(), tc.sig)
			}()
			_, errb, err := execSessionCLI(t, input, append([]string{"tool", "login", tc.driver}, sessionCreds(f.URL)...)...)
			close(done)
			if exitcode.From(err) != exitcode.Err || !exitcode.Silent(err) {
				t.Fatalf("err = %v, want a silent exit 1", err)
			}
			if !strings.Contains(f.allHits(), "DELETE "+agentToolsPath+"/sign-in/"+tc.id) {
				t.Fatalf("the sign-in was not cancelled on the engine:\n%s", f.allHits())
			}
			if strings.Count(errb, "Cancelled the sign-in of "+tc.name+".") != 1 {
				t.Fatalf("stderr = %q, want one line that the sign-in was cancelled", errb)
			}
		})
	}
}
