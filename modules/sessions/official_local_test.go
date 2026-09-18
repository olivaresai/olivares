// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/modules/sessions/cliruntime"
)

func TestOfficialLocal_ConformanceClaudeAndCodexOnPTY(t *testing.T) {
	script := writeTerminalClientPeer(t)
	for _, kind := range []string{cliruntime.KindClaude, cliruntime.KindCodex} {
		t.Run(kind, func(t *testing.T) {
			// The terminal runner keeps its contract coverage here even though no
			// launch form selects it: newOfficialLocalOn is that seam. The peer is
			// one that REQUIRES a terminal, so this battery cannot be satisfied by
			// the pipe transport — the ambiguity r3 removed.
			d, err := newOfficialLocalOn(kind, script, NewPTYRunner())
			if err != nil {
				t.Fatal(err)
			}
			cliruntime.RunConformance(t, d)
		})
	}
}

func TestOfficialLocal_GrokConformanceOnPTY(t *testing.T) {
	d, err := newOfficialLocalOn(cliruntime.KindGrok, writeTerminalClientPeer(t), NewPTYRunner())
	if err != nil {
		t.Fatal(err)
	}
	cliruntime.RunConformance(t, d)
}

// TestOfficialLocal_ConformanceOnTheSelectedTransport is the contract battery on
// the transport the DECLARATION selects, against the peer that refuses a
// terminal exactly as the real binary does.
//
// ⛔ ITS PEER USED TO BE THE TERMINAL FIXTURE, WHICH MADE IT PASS ON EITHER
// TRANSPORT. An independent review measured that on 2026-09-18: ptyPeerScript
// continues whether or not it got a terminal, so 21 leaf cases were green on
// the transport production had retired. The peer is the assertion here.
func TestOfficialLocal_ConformanceOnTheSelectedTransport(t *testing.T) {
	script := writePrintFormPeer(t)
	for _, kind := range cliruntime.Kinds() {
		t.Run(kind, func(t *testing.T) {
			d, err := NewOfficialLocal(kind, script)
			if err != nil {
				t.Fatal(err)
			}
			cliruntime.RunConformance(t, d)
		})
	}
}

// printFormPeerScript is THE peer of everything in this package that stands for
// the real `--print` stream-json binary: it refuses a terminal on stdin exactly
// as that binary does, and otherwise speaks the same minimal protocol the
// terminal fixture speaks (an init frame that nominates a session, a distinct
// stderr line, one answer per input line, a graceful exit on SIGTERM), so a
// battery can be moved onto the production transport without losing coverage.
//
// ⛔ THE REFUSAL IS A MEASUREMENT, NOT A GUESS, and it was re-taken for r3.
// Against claude 2.1.276 on 2026-09-18, in a directory with no project hook:
//
//   - the argv this engine builds, with a TERMINAL on stdin (`script -qec … /dev/null`):
//     stdout/stderr carry exactly `Error: Input must be provided either through
//     stdin or as a prompt argument when using --print`, rc 1, and ZERO frames
//     with `"subtype":"init"`;
//   - the SAME argv with a PIPE on stdin: one `"subtype":"init"` frame, then one
//     `"subtype":"success"` result carrying `Not logged in · Please run /login`
//     — the unauthenticated answer, so no model turn is spent.
//
// A fixture is used instead of the binary so the regression reproduces on a host
// that has no vendor CLI at all; official_cli_live_test.go is where the binary
// itself is driven.
const printFormPeerScript = `#!/bin/sh
trap 'exit 0' TERM
if [ -t 0 ]; then
  printf 'Error: Input must be provided either through stdin or as a prompt argument when using --print\n' >&2
  exit 1
fi
SID="sess-stdio-1"
while [ $# -gt 0 ]; do
  case "$1" in
    --resume) SID="$2"; shift 2 ;;
    *) shift ;;
  esac
done
printf '{"type":"system","subtype":"init","session_id":"%s","tty":false}\n' "$SID"
printf 'peer-stderr\n' >&2
while IFS= read -r line; do
  printf '{"type":"assistant","echo":true}\n'
done
exit 0
`

// writePrintFormPeer installs that peer as an executable fixture.
func writePrintFormPeer(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "print-form-peer.sh")
	if err := os.WriteFile(path, []byte(printFormPeerScript), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestOfficialLocal_LaunchesThePrintFormOnPipes is the regression row for the
// revision that wired the terminal runner under every official launch: the
// driver must give the stdio launch forms pipes, so a CLI that refuses a
// terminal on stdin still starts and still answers.
func TestOfficialLocal_LaunchesThePrintFormOnPipes(t *testing.T) {
	path := writePrintFormPeer(t)
	for _, kind := range cliruntime.Kinds() {
		t.Run(kind, func(t *testing.T) {
			if got := cliruntime.LaunchTransport(kind); got != cliruntime.TransportStdio {
				t.Fatalf("launch transport for %s = %q, want %q", kind, got, cliruntime.TransportStdio)
			}
			d, err := NewOfficialLocal(kind, path)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			sess, err := d.Launch(ctx, cliruntime.LaunchRequest{
				WorkDir: t.TempDir(), UserHome: t.TempDir(), ConfigHome: t.TempDir(),
			})
			if err != nil {
				t.Fatalf("launch %s: %v", kind, err)
			}
			defer func() { _, _ = sess.Stop(context.Background()) }()
			ch, unsub := sess.Attach(1)
			defer unsub()
			deadline := time.After(6 * time.Second)
			for {
				select {
				case f, open := <-ch:
					if !open {
						t.Fatalf("%s: the peer refused the transport and left without a protocol frame", kind)
					}
					if f.Stream == cliruntime.StreamStderr && strings.Contains(string(f.Data), "must be provided") {
						t.Fatalf("%s: the peer was given a terminal on stdin and refused: %s", kind, f.Data)
					}
					if strings.Contains(string(f.Data), `"subtype":"init"`) {
						if err := sess.Send(ctx, []byte(`{"type":"user"}`)); err != nil {
							t.Fatalf("%s send: %v", kind, err)
						}
						return
					}
				case <-deadline:
					t.Fatalf("%s: no protocol frame in 6s", kind)
				}
			}
		})
	}
}

func TestOfficialLocal_HomesDoNotCross(t *testing.T) {
	script := writeHomePeer(t)
	homeA := t.TempDir()
	cfgA := t.TempDir()
	homeB := t.TempDir()
	cfgB := t.TempDir()
	d, err := NewOfficialLocal(cliruntime.KindClaude, script)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	a, err := d.Launch(ctx, cliruntime.LaunchRequest{WorkDir: t.TempDir(), UserHome: homeA, ConfigHome: cfgA})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = a.Stop(ctx) }()
	b, err := d.Launch(ctx, cliruntime.LaunchRequest{WorkDir: t.TempDir(), UserHome: homeB, ConfigHome: cfgB})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = b.Stop(ctx) }()
	waitFile(t, filepath.Join(cfgA, "seen-env"), 3*time.Second)
	waitFile(t, filepath.Join(cfgB, "seen-env"), 3*time.Second)
	gotA, _ := os.ReadFile(filepath.Join(cfgA, "seen-env"))
	gotB, _ := os.ReadFile(filepath.Join(cfgB, "seen-env"))
	if !strings.Contains(string(gotA), "HOME="+homeA) || !strings.Contains(string(gotA), "CLAUDE_CONFIG_DIR="+cfgA) {
		t.Fatalf("home A not isolated: %s", gotA)
	}
	if !strings.Contains(string(gotB), "HOME="+homeB) || !strings.Contains(string(gotB), "CLAUDE_CONFIG_DIR="+cfgB) {
		t.Fatalf("home B not isolated: %s", gotB)
	}
	if strings.Contains(string(gotA), homeB) || strings.Contains(string(gotB), homeA) {
		t.Fatal("homes crossed")
	}
}

func writeHomePeer(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "home-peer.sh")
	// It launches through NewOfficialLocal, i.e. the production factory, so it
	// refuses a terminal like the binary it stands for: a peer that tolerated one
	// would keep this green if the factory ever handed it a pseudo-terminal.
	script := `#!/bin/sh
trap 'exit 0' TERM
if [ -t 0 ]; then
  printf 'Error: Input must be provided either through stdin or as a prompt argument when using --print\n' >&2
  exit 1
fi
printf '{"type":"system","subtype":"init","session_id":"sess-home"}\n'
printf 'HOME=%s\nCLAUDE_CONFIG_DIR=%s\n' "$HOME" "$CLAUDE_CONFIG_DIR" > "$CLAUDE_CONFIG_DIR/seen-env"
exec sleep 30
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func waitFile(t *testing.T, path string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("file not written: %s", path)
}

// TestOfficialLocal_RefusesWhatItCannotLaunch keeps the two refusals the
// constructor owes its caller, now that the transport table gates them.
func TestOfficialLocal_RefusesWhatItCannotLaunch(t *testing.T) {
	if _, err := NewOfficialLocal("mystery", "/bin/true"); !errors.Is(err, cliruntime.ErrUnknownKind) {
		t.Fatalf("unknown kind = %v, want ErrUnknownKind", err)
	}
	if _, err := NewOfficialLocal(cliruntime.KindClaude, ""); !errors.Is(err, cliruntime.ErrNoProgram) {
		t.Fatalf("empty program = %v, want ErrNoProgram", err)
	}
}
