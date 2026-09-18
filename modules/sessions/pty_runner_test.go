// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ptyPeerScript is the fixture that reports what it got and continues either
// way. That makes it the right peer for exactly two questions — "does the
// terminal runner give the child a terminal?" and "does the pipe runner not?" —
// and the WRONG peer for anything that stands for a real vendor binary, because
// a peer that accepts both transports is green on both. An independent review
// measured that on 2026-09-18: 21 conformance leaf cases and seven HTTP
// journeys passed on the transport production had retired. See
// printFormPeerScript (refuses a terminal, as the real `--print` form does) and
// terminalClientPeerScript (requires one, as a terminal client does).
const ptyPeerScript = `#!/bin/sh
trap 'exit 0' TERM
if [ -t 0 ]; then TTY=true; else TTY=false; fi
SID="sess-pty-1"
while [ $# -gt 0 ]; do
  case "$1" in
    --resume) SID="$2"; shift 2 ;;
    *) shift ;;
  esac
done
printf '{"type":"system","subtype":"init","session_id":"%s","tty":%s}\n' "$SID" "$TTY"
printf 'peer-stderr\n' >&2
while IFS= read -r line; do
  printf '{"type":"assistant","echo":true}\n'
done
exit 0
`

func writePTYPeer(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pty-peer.sh")
	if err := os.WriteFile(path, []byte(ptyPeerScript), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// terminalClientPeerScript is the mirror of printFormPeerScript: it needs a
// terminal on stdin and refuses without one. It is the peer of everything that
// stands for a TERMINAL client — the journey that opens the environment's
// terminal (J05) and the terminal runner's own conformance — so those batteries
// fail if they are ever moved onto pipes.
//
// ⛔ ITS REFUSAL LINE IS OURS, NOT A VENDOR'S, AND THAT IS STATED BECAUSE IT
// MATTERS. printFormPeerScript quotes a measured vendor sentence; this
// one
// cannot, because no official form measured on 2026-09-18 refuses a pipe for
// lack of a terminal: claude 2.1.276 with no `--print` and a pipe on stdin
// answered `Not logged in · Please run /login` and exited 1 — the
// unauthenticated answer, not a transport refusal. What the fixture reproduces
// is the DEFINITIONAL requirement of a terminal client, and the wording is
// labelled as a fixture's.
const terminalClientPeerScript = `#!/bin/sh
trap 'exit 0' TERM
if [ ! -t 0 ]; then
  printf 'olivares-fixture: this form is a terminal client and has no terminal on stdin\n' >&2
  exit 1
fi
SID="sess-terminal-1"
while [ $# -gt 0 ]; do
  case "$1" in
    --resume) SID="$2"; shift 2 ;;
    *) shift ;;
  esac
done
printf '{"type":"system","subtype":"init","session_id":"%s","tty":true}\n' "$SID"
printf 'peer-stderr\n' >&2
while IFS= read -r line; do
  printf '{"type":"assistant","echo":true}\n'
done
exit 0
`

func writeTerminalClientPeer(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "terminal-client-peer.sh")
	if err := os.WriteFile(path, []byte(terminalClientPeerScript), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestTheTwoTransportPeersDisagree is the control that gives every battery
// below its meaning: the two fixtures refuse OPPOSITE transports, so a battery
// that names one of them cannot pass on both.
func TestTheTwoTransportPeersDisagree(t *testing.T) {
	t.Parallel()
	stdioPeer := writePrintFormPeer(t)
	terminalPeer := writeTerminalClientPeer(t)
	for _, tc := range []struct {
		name    string
		runner  Runner
		program string
		refused bool
	}{
		{"stdio peer on pipes", NewProcRunner(), stdioPeer, false},
		{"stdio peer on a terminal", NewPTYRunner(), stdioPeer, true},
		{"terminal peer on a terminal", NewPTYRunner(), terminalPeer, false},
		{"terminal peer on pipes", NewProcRunner(), terminalPeer, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			proc, err := tc.runner.Launch(ctx, LaunchSpec{
				Program: tc.program, Dir: t.TempDir(), WaitDelay: 2 * time.Second,
			})
			if err != nil {
				t.Fatalf("launch: %v", err)
			}
			defer func() { _ = proc.Stop(context.Background()) }()
			gotInit := waitProcFrame(t, proc, 5*time.Second, func(f OutputFrame) bool {
				return strings.Contains(string(f.Data), `"subtype":"init"`)
			})
			if tc.refused && gotInit {
				t.Fatal("the peer accepted a transport it must refuse, so a battery using it would be green on either")
			}
			if !tc.refused && !gotInit {
				t.Fatal("the peer refused the transport it is meant to accept, so it proves nothing about the other")
			}
		})
	}
}

func TestPTYRunner_ChildSeesTTYAndBridgesNDJSON(t *testing.T) {
	t.Parallel()
	script := writePTYPeer(t)
	r := NewPTYRunner()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	proc, err := r.Launch(ctx, LaunchSpec{
		Program: script, Dir: t.TempDir(), WaitDelay: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	var sawTTY, sawEcho, sawStderr bool
	if err := proc.Send(ctx, []byte(`{"type":"user"}`)); err != nil {
		t.Fatalf("send: %v", err)
	}
	deadline := time.After(4 * time.Second)
	for !sawTTY || !sawEcho || !sawStderr {
		select {
		case f, open := <-proc.Output():
			if !open {
				t.Fatalf("output closed tty=%v echo=%v stderr=%v", sawTTY, sawEcho, sawStderr)
			}
			if strings.Contains(string(f.Data), `"tty":true`) {
				sawTTY = true
			}
			if strings.Contains(string(f.Data), `"echo":true`) {
				sawEcho = true
			}
			if f.Stream == streamStderr && strings.Contains(string(f.Data), "peer-stderr") {
				sawStderr = true
			}
		case <-deadline:
			t.Fatalf("tty=%v echo=%v stderr=%v (stderr must stay a distinct pipe)", sawTTY, sawEcho, sawStderr)
		}
	}
	if err := proc.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
	code, err := proc.Wait()
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
}

func TestPTYRunner_RefusesContainerIsolation(t *testing.T) {
	r := NewPTYRunner()
	_, err := r.Launch(context.Background(), LaunchSpec{
		Program: writePTYPeer(t), Isolation: IsolationContainer,
	})
	if err == nil {
		t.Fatal("container isolation must be refused")
	}
}

func TestProcRunner_ChildDoesNotSeeTTY(t *testing.T) {
	t.Parallel()
	script := writePTYPeer(t)
	r := NewProcRunner()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	proc, err := r.Launch(ctx, LaunchSpec{
		Program: script, Dir: t.TempDir(), WaitDelay: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	defer func() { _ = proc.Stop(ctx) }()
	if !waitProcFrame(t, proc, 3*time.Second, func(f OutputFrame) bool {
		return strings.Contains(string(f.Data), `"tty":false`)
	}) {
		t.Fatal("pipe child must not see a terminal")
	}
}

func waitProcFrame(t *testing.T, proc Process, d time.Duration, ok func(OutputFrame) bool) bool {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case f, open := <-proc.Output():
			if !open {
				return false
			}
			if ok(f) {
				return true
			}
		case <-deadline:
			return false
		}
	}
}
