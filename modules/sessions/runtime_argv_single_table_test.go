// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"strings"
	"testing"

	"github.com/olivaresai/olivares/modules/sessions/cliruntime"
)

// The launch path the ENGINE takes builds its argv from the ONE table, and this
// file is what makes that checkable.
//
// ⛔ THE DEFECT THIS REPLACES WAS NOT A WRONG ARGV: IT WAS A SECOND ONE. Until
// r3 buildLaunchSpec held its own copy of the `--print` stream-json form beside
// cliruntime's, and the two agreed. An independent review measured the
// consequence: cliruntime.LaunchTransport — the declaration that says this form
// must NOT get a terminal — sat beside the copy production did not launch, so
// the declaration could be changed for a kind and nothing the engine launches
// would change, with no test going red.
//
// Byte-identical duplication is not what costs; DIVERGENCE is. So the assertion
// is equality against the table itself for a launch that exercises every
// governed term, which fails the moment one side grows a flag the other does
// not have.

// engineClaudeParams is one fully-loaded governed launch: every term the server
// can decide, so no flag of the form is left unexercised.
func engineClaudeParams() CreateRunParams {
	return CreateRunParams{
		Name:           "argv-table",
		Transport:      TransportStreamJSON,
		PermissionMode: "dontAsk",
		Effort:         "xhigh",
		Model:          "claude-opus-5",
		Isolation:      IsolationNative,
		AllowedTools:   []string{"Read", "Bash(git *)"},
		Instructions:   "stay inside the workspace",
	}
}

func TestEngineArgvComesFromTheOneTable(t *testing.T) {
	t.Parallel()
	m := New()
	p := engineClaudeParams()
	req := cliruntime.LaunchRequest{
		Model:          p.Model,
		Effort:         p.Effort,
		PermissionMode: p.PermissionMode,
		ResumeID:       "sess-77",
		AllowedTools:   p.AllowedTools,
		Instructions:   p.Instructions,
		Name:           p.Name,
	}

	spec := m.buildLaunchSpec(p, Credential{}, WorkSessionCredential{}, CommunicationSessionCredential{},
		"sess-77", nil, nil, nil)
	assertSameArgv(t, "stream-json", spec.Args, cliruntime.ClaudeArgs(req))

	p.Transport = TransportRemoteControl
	spec = m.buildLaunchSpec(p, Credential{}, WorkSessionCredential{}, CommunicationSessionCredential{},
		"sess-77", nil, nil, nil)
	assertSameArgv(t, "remote-control", spec.Args, cliruntime.ClaudeRemoteControlArgs(req))

	// The control positive: the two forms are NOT the same argv, so the equality
	// above is not being satisfied by a function that ignores its input.
	if strings.Join(cliruntime.ClaudeArgs(req), " ") == strings.Join(cliruntime.ClaudeRemoteControlArgs(req), " ") {
		t.Fatal("the print form and the remote-control form produced the same argv; this test would pass on either table")
	}
}

// TestEngineArgvOfARegisteredDriverComesFromTheOneTable is the same property for
// the kinds whose argv a registered driver owns: the driver consults the table
// instead of keeping its own copy of its official CLI's form.
func TestEngineArgvOfARegisteredDriverComesFromTheOneTable(t *testing.T) {
	t.Parallel()
	launch := DriverLaunch{WorkDir: "/w", Model: "grok-4", Effort: "high"}
	req := cliruntime.LaunchRequest{WorkDir: launch.WorkDir, Model: launch.Model, Effort: launch.Effort}
	for _, tc := range []struct {
		kind   string
		driver ProviderDriver
	}{
		{cliruntime.KindCodex, NewCodexDriver()},
		{cliruntime.KindGrok, NewGrokDriver()},
	} {
		want, err := cliruntime.LaunchArgs(tc.kind, req)
		if err != nil {
			t.Fatalf("%s: the table has no argv for a kind a driver launches: %v", tc.kind, err)
		}
		assertSameArgv(t, tc.kind, tc.driver.LaunchArgs(launch), want)
	}
}

// TestUnknownTransportStillLaunchesTheGovernedForm pins the deny-closed
// direction of the default arm. buildLaunchSpec is reachable only behind
// validateCreate, which normalizes an empty transport to stream-json — but a
// fall-through that emitted the governed TERMS with no FORM flag would start the
// vendor CLI interactively under a row that claims a bridged session, which is
// the same class of fault as launching the print form on a terminal.
func TestUnknownTransportStillLaunchesTheGovernedForm(t *testing.T) {
	t.Parallel()
	m := New()
	p := engineClaudeParams()
	p.Transport = Transport("not-a-transport")
	spec := m.buildLaunchSpec(p, Credential{}, WorkSessionCredential{}, CommunicationSessionCredential{},
		"", nil, nil, nil)
	if !argvHasFlag(spec.Args, "--print") || !argvHasFlag(spec.Args, "--input-format") {
		t.Fatalf("an unrecognized transport produced an argv with no governed form flag: %v", spec.Args)
	}
	if argvHasFlag(spec.Args, "--remote-control") {
		t.Fatalf("an unrecognized transport must not fall back to the lifecycle-only form: %v", spec.Args)
	}
}

func assertSameArgv(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: the engine built %d argv items and the table declares %d\n engine: %v\n  table: %v",
			what, len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s: argv item %d = %q, the table declares %q\n engine: %v\n  table: %v",
				what, i, got[i], want[i], got, want)
		}
	}
}

func argvHasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}
