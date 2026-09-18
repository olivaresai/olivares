// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/modules/sessions/cliruntime"
)

// engineArgvFor is the argv the ENGINE launches for a kind: the built-in Claude
// path reads the table directly, a registered driver reads it through its own
// LaunchArgs. Both are the production call, not a restatement of it.
func engineArgvFor(t *testing.T, kind string) []string {
	t.Helper()
	launch := DriverLaunch{WorkDir: t.TempDir(), Model: "m", Effort: "high"}
	switch kind {
	case cliruntime.KindClaude:
		return cliruntime.ClaudeArgs(cliruntime.LaunchRequest{
			WorkDir: launch.WorkDir, Model: launch.Model, Effort: launch.Effort,
			PermissionMode: "default",
		})
	case cliruntime.KindCodex:
		return NewCodexDriver().LaunchArgs(launch)
	case cliruntime.KindGrok:
		return NewGrokDriver().LaunchArgs(launch)
	default:
		t.Fatalf("no engine launch path for kind %q", kind)
		return nil
	}
}

// TestEngineArgvLaunchesOnTheTransportTheFactorySelects is the regression the
// independent review of r2 asked for: for every declared kind, the argv the
// engine builds is launched through the runner the PRODUCTION factory selects,
// against a peer that refuses a terminal on stdin exactly as the real binary
// does (exit 1, the measured message, no protocol frame).
//
// It fails when the factory is bypassed with NewPTYRunner(): the peer refuses,
// the child leaves without a frame, and the launch is proved impossible instead
// of being asserted about.
func TestEngineArgvLaunchesOnTheTransportTheFactorySelects(t *testing.T) {
	t.Parallel()
	peer := writePrintFormPeer(t)
	for _, kind := range cliruntime.Kinds() {
		t.Run(kind, func(t *testing.T) {
			runner, err := NewOfficialRunner(kind)
			if err != nil {
				t.Fatalf("the production factory refuses the declared kind: %v", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			proc, err := runner.Launch(ctx, LaunchSpec{
				Program:   peer,
				Args:      engineArgvFor(t, kind),
				Dir:       t.TempDir(),
				Isolation: IsolationNative,
				WaitDelay: 2 * time.Second,
			})
			if err != nil {
				t.Fatalf("launch: %v", err)
			}
			defer func() { _ = proc.Stop(context.Background()) }()
			deadline := time.After(6 * time.Second)
			for {
				select {
				case f, open := <-proc.Output():
					if !open {
						t.Fatalf("%s: the child left without a protocol frame; the engine's argv cannot start on this transport", kind)
					}
					if strings.Contains(string(f.Data), "must be provided") {
						t.Fatalf("%s: the child was given a terminal on stdin and refused: %s", kind, f.Data)
					}
					if strings.Contains(string(f.Data), `"subtype":"init"`) {
						// It started AND it can be driven: a frame alone would not prove
						// the channel carries input in the other direction.
						if err := proc.Send(ctx, []byte(`{"type":"user"}`)); err != nil {
							t.Fatalf("%s send: %v", kind, err)
						}
						if !waitProcFrame(t, proc, 4*time.Second, func(f OutputFrame) bool {
							return strings.Contains(string(f.Data), `"echo":true`)
						}) {
							t.Fatalf("%s: the child took an input and never answered", kind)
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

// TestRunnerFactoryMapsEveryTransportAndNothingElse pins the one mapping this
// module has from a declared transport to a runner, including its refusal: an
// undeclared transport is not resolved to the stdio default, because "I do not
// know what this needs" is not "pipes".
func TestRunnerFactoryMapsEveryTransportAndNothingElse(t *testing.T) {
	t.Parallel()
	for transport, want := range map[cliruntime.Transport]cliruntime.Transport{
		cliruntime.TransportStdio:    cliruntime.TransportStdio,
		cliruntime.TransportTerminal: cliruntime.TransportTerminal,
	} {
		runner, err := NewRunnerForTransport(transport)
		if err != nil {
			t.Fatalf("NewRunnerForTransport(%q): %v", transport, err)
		}
		reporter, ok := runner.(RunnerTransportReporter)
		if !ok {
			t.Fatalf("the runner for %q cannot report its transport", transport)
		}
		if got := reporter.ProvidesTransport(); got != want {
			t.Fatalf("the runner for %q provides %q", transport, got)
		}
	}
	if _, err := NewRunnerForTransport(""); err == nil {
		t.Fatal("the empty transport resolved to a runner")
	}
	if _, err := NewRunnerForTransport(cliruntime.Transport("smoke-signals")); err == nil {
		t.Fatal("an undeclared transport resolved to a runner")
	}
}
