// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package cliruntime

import (
	"context"
	"errors"
	"testing"
	"time"
)

// waitPump is a Pump whose Wait answers with the outcome the test names. It is
// the only way to reach the one branch a real child cannot be asked to take on
// demand: a wait that could not classify the child at all.
type waitPump struct {
	frames chan Frame
	code   int
	err    error
}

func (p *waitPump) Send(context.Context, []byte) error { return nil }
func (p *waitPump) Frames() <-chan Frame               { return p.frames }
func (p *waitPump) Wait() (int, error)                 { return p.code, p.err }
func (p *waitPump) Stop(context.Context) error         { close(p.frames); return nil }
func (p *waitPump) PID() int                           { return 4242 }

// TestHostedReportsAnUnreapedChildAsNotExited pins the sentence the Result type
// carries: "A stop that could not reap the child reports ProcessExited false
// rather than inventing an exit code." A -1 with an error is that case; a -1
// with no error is a child Go could classify (a signaled one), and that IS an
// exit.
func TestHostedReportsAnUnreapedChildAsNotExited(t *testing.T) {
	cases := []struct {
		name     string
		code     int
		err      error
		exited   bool
		wantCode int
	}{
		{name: "clean exit", code: 0, err: nil, exited: true, wantCode: 0},
		{name: "non-zero exit", code: 2, err: nil, exited: true, wantCode: 2},
		{name: "signaled child", code: -1, err: nil, exited: true, wantCode: -1},
		{name: "wait could not classify", code: -1, err: errors.New("wait failed"), exited: false, wantCode: -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &waitPump{frames: make(chan Frame), code: tc.code, err: tc.err}
			h := Host(p, HostedMeta{Ref: "r", Kind: KindClaude, Generation: 7})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			res, err := h.Stop(ctx)
			if err != nil {
				t.Fatalf("stop: %v", err)
			}
			if res.ProcessExited != tc.exited {
				t.Fatalf("ProcessExited = %v, want %v (wait answered %d, %v)", res.ProcessExited, tc.exited, tc.code, tc.err)
			}
			if res.ExitCode != tc.wantCode {
				t.Fatalf("ExitCode = %d, want %d", res.ExitCode, tc.wantCode)
			}
			if res.Generation != 7 {
				t.Fatalf("Generation = %d, want 7", res.Generation)
			}
		})
	}
}
