// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"errors"
	"fmt"

	"github.com/olivaresai/olivares/modules/sessions/cliruntime"
)

// The runner SELECTION seam: one declaration, one factory, and a read that says
// what the wired runner gives the child.
//
// ⛔ THE DECLARATION HAS TO GOVERN THE PATH PRODUCTION TAKES, OR IT GOVERNS
// NOTHING. cliruntime.LaunchTransport records, with the measurement beside it,
// that every owned operate form of the three official CLIs is a stdio protocol
// and that Claude Code's `--print` form REFUSES a terminal on stdin. Until r3
// the composition root wired NewProcRunner() by hand: the right runner, chosen
// for the right reason, by a line that did not read the declaration. An
// independent review named the cost exactly — the product worked by coincidence
// of the hand-wiring, and an edit of that one line would have broken every
// managed session with no test going red.
//
// So the selection lives here, both the driver (NewOfficialLocal) and the
// composition root call it, and the module can be ASKED what it got.

// errRunnerTransportDisagreement is the deny-closed refusal of a module that
// would need two different runners at once. It is deliberately not resolved by
// precedence: a wrong transport is a launch that dies before its first frame,
// and picking one silently would pick it for somebody.
var errRunnerTransportDisagreement = errors.New("sessions: the declared launch transports disagree, so no single runner can serve them")

// RunnerTransportReporter is the OPTIONAL half of a Runner that can name the
// child-side transport it provides.
//
// It is optional for the same reason RunnerInspector is: adding a required
// method would force every Runner — including one outside this repository — to
// change. A Runner that does not implement it reports the empty transport, and
// empty is honored as "it did not say", never as stdio.
type RunnerTransportReporter interface {
	ProvidesTransport() cliruntime.Transport
}

// ProvidesTransport is the native runner's answer: pipes on the child's
// standard streams, or a pseudo-terminal on stdin/stdout. It is the same bit
// Launch switches on, read instead of executed.
func (pr *procRunner) ProvidesTransport() cliruntime.Transport {
	if pr.usePTY {
		return cliruntime.TransportTerminal
	}
	return cliruntime.TransportStdio
}

// NewRunnerForTransport returns the native runner one DECLARED transport
// requires. It is the only place in this module that maps a transport to a
// runner, so the two constructors below cannot drift apart.
func NewRunnerForTransport(transport cliruntime.Transport) (Runner, error) {
	switch transport {
	case cliruntime.TransportStdio:
		return NewProcRunner(), nil
	case cliruntime.TransportTerminal:
		return NewPTYRunner(), nil
	default:
		return nil, fmt.Errorf("%w: transport %q", cliruntime.ErrUnknownKind, transport)
	}
}

// NewOfficialRunner returns the one native runner every named kind's DECLARED
// launch transport requires. It is the composition root's factory: wiring its
// result is what makes cliruntime.LaunchTransport decide what production
// launches.
//
// ⛔ IT REFUSES A DISAGREEMENT INSTEAD OF RESOLVING ONE, and that is the whole
// reason it takes the kinds rather than one transport. This module holds ONE
// Runner for every kind it launches (runtime.go: m.rt.runner), so if the
// declaration ever says stdio for one official CLI and terminal for another,
// there is no answer that serves both — and the failure mode of guessing is the
// one already measured: a `--print` child that exits 1 on stderr without a
// single protocol frame. A refusal here leaves the module deny-closed, which is
// loud at boot and red in the batteries; a guess would be silent until a
// customer launched.
//
// A kind the table does not declare is refused for the same reason: "I do not
// know what this needs" is not "stdio".
func NewOfficialRunner(kinds ...string) (Runner, error) {
	if len(kinds) == 0 {
		return nil, fmt.Errorf("%w: no kind named", cliruntime.ErrUnknownKind)
	}
	var agreed cliruntime.Transport
	var agreedBy string
	for _, kind := range kinds {
		declared := cliruntime.LaunchTransport(kind)
		if declared == "" {
			return nil, fmt.Errorf("%w: %q declares no launch transport", cliruntime.ErrUnknownKind, kind)
		}
		if agreed == "" {
			agreed, agreedBy = declared, kind
			continue
		}
		if declared != agreed {
			return nil, fmt.Errorf("%w: %q needs %q and %q needs %q",
				errRunnerTransportDisagreement, agreedBy, agreed, kind, declared)
		}
	}
	return NewRunnerForTransport(agreed)
}

// RunnerTransport reports the child-side transport the WIRED runner provides,
// or the empty transport when no runner is wired or the runner does not say.
//
// It exists for the composition root and for the operator's boot log, not for a
// test: a deployment that wired the wrong runner launches every managed session
// on a transport its official CLI refuses, and the only trace is a child that
// died on stderr. Naming the transport at boot turns that into something an
// operator can look up instead of deduce — and it is also what lets the
// composition root's own battery prove that the line which wires the runner
// agrees with the declaration, which the module's batteries cannot notice
// because they inject their own runner.
func (m *Module) RunnerTransport() cliruntime.Transport {
	if _, unwired := m.rt.runner.(unwiredRunner); unwired {
		return ""
	}
	reporter, ok := m.rt.runner.(RunnerTransportReporter)
	if !ok {
		return ""
	}
	return reporter.ProvidesTransport()
}
