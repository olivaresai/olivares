// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"errors"
	"testing"

	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/modules/sessions/cliruntime"
)

// The composition root's half of the launch TRANSPORT.
//
// ⛔ THE MODULE'S OWN BATTERIES CANNOT NOTICE THIS, which is the same reason
// TestSessionRuntimeCompositionWiresAnInspectableRunner exists: they inject
// their own runner. An independent review measured on 2026-09-18 that
// cliruntime.LaunchTransport — which records that Claude Code's `--print` form
// exits 1 on a terminal without one protocol frame — governed only the
// cliruntime driver, and that driver has no production caller. The line that
// decides what every managed session launches on named its runner by hand, so
// the declaration and production could disagree for ever with nothing red.
//
// This test is what that costs now: the transport the composition root wires is
// compared against the declaration, for every kind the declaration covers.
func TestSessionRuntimeCompositionWiresTheDeclaredTransport(t *testing.T) {
	t.Parallel()
	m := sessions.New(buildSessionRuntimeOptions(func(string) string { return "" }, nil, "", nil)...)
	got := m.RunnerTransport()
	if got == "" {
		t.Fatal("the composition root wired a runner that does not report its transport, or wired none at all; " +
			"every managed launch would either be denied or run on a transport nobody declared")
	}
	for _, kind := range cliruntime.Kinds() {
		if want := cliruntime.LaunchTransport(kind); got != want {
			t.Fatalf("the composition root wires the %q transport, and %q declares it needs %q: "+
				"that launch form dies before its first protocol frame", got, kind, want)
		}
	}

	// TWO controls, because the assertion above is an equality and an equality is
	// satisfiable by accident.
	//
	// 1. The terminal runner is a DIFFERENT answer to the same question, so the
	//    read is not a constant.
	if got := sessions.New(sessions.WithRunner(sessions.NewPTYRunner())).RunnerTransport(); got != cliruntime.TransportTerminal {
		t.Fatalf("the terminal runner reports %q, want %q; the transport read cannot tell the two runners apart", got, cliruntime.TransportTerminal)
	}
	// 2. An unwired module reports nothing, so "" is the honest absence and not a
	//    default that would make the first assertion vacuous.
	if got := sessions.New().RunnerTransport(); got != "" {
		t.Fatalf("an unwired module reports transport %q, want the empty transport", got)
	}
}

// TestOfficialRunnerRefusesWhatOneRunnerCannotServe pins the deny-closed arm the
// composition root depends on: this module holds ONE runner, so a declaration
// that asked for two transports at once has no answer that serves both, and
// guessing would pick the measured failure for somebody.
func TestOfficialRunnerRefusesWhatOneRunnerCannotServe(t *testing.T) {
	t.Parallel()
	if _, err := sessions.NewOfficialRunner(); err == nil {
		t.Fatal("a factory asked about no kind at all returned a runner")
	}
	if _, err := sessions.NewOfficialRunner("mystery"); !errors.Is(err, cliruntime.ErrUnknownKind) {
		t.Fatalf("an undeclared kind = %v, want ErrUnknownKind (not a silent stdio default)", err)
	}
	// The positive control: the kinds the table declares DO resolve, so the
	// refusals above are about the input and not about the factory.
	runner, err := sessions.NewOfficialRunner(cliruntime.Kinds()...)
	if err != nil {
		t.Fatalf("the declared kinds do not resolve to a runner: %v", err)
	}
	reporter, ok := runner.(sessions.RunnerTransportReporter)
	if !ok {
		t.Fatal("the factory returned a runner that cannot report its transport")
	}
	if got := reporter.ProvidesTransport(); got != cliruntime.LaunchTransport(cliruntime.KindClaude) {
		t.Fatalf("the factory returned the %q runner for a declaration of %q", got, cliruntime.LaunchTransport(cliruntime.KindClaude))
	}
}
