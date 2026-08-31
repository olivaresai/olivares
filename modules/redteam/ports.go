// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package redteam

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// This file defines the adversarial-test vocabulary and the SANDBOX seam. The
// module owns the battery and the scoring; the sandbox is the execution environment
// (README.md §XVIII). Until a sandbox is wired the default sandbox does NOT
// execute (it never silently "passes" a target): a run is reported DEGRADED.

// Probe is one adversarial robustness test case. It is a DEFENSIVE test, not a
// weapon (docs/SECURITY-HARDENING.md): a well-defended agent is EXPECTED to refuse it or have its
// guardrail block it. Each probe is mapped to an OWASP/ATLAS reference.
type Probe struct {
	// ID is the stable probe identifier (family-scoped, e.g. "inj-01").
	ID string
	// Family groups the probe: injection | jailbreak | exfil | tool_poisoning.
	Family string
	// Title is a short, non-sensitive description.
	Title string
	// OWASP / ATLAS are the framework references (e.g. "ASI01", "AML.T0051.000").
	OWASP string
	ATLAS string
	// Severity is the finding severity IF the target FAILS this probe.
	Severity sdkmodel.Severity
	// Surface is which agent input the payload targets: "input" | "tool" | "output".
	Surface string
	// Payload is the adversarial input delivered to the target (in the sandbox).
	Payload string
	// MustNotContain are markers that, if echoed in the target's response, prove a
	// compliance/leak (a failure). They are the deterministic part of the judgement
	// the sandbox or the default judge applies.
	MustNotContain []string
}

// Target is a client-governed agent under test: the CONSENT record. The red-team
// runs ONLY against a registered, authorized target in the client's own perimeter
// (docs/SECURITY-HARDENING.md). Endpoint is opaque to this module — the sandbox knows how to reach
// the target; this module never connects to anything itself.
type Target struct {
	ID         model.ID
	AgentRef   string
	Name       string
	Endpoint   string
	Scope      string
	Authorized bool
}

// Outcome is the result of one probe against a target.
type Outcome string

// The probe outcomes. blocked/refused are PASSES (the defense held); complied/leaked
// are FAILURES (the agent was vulnerable); error is an execution fault; skipped means
// the probe was not executed (no sandbox wired) and is excluded from the score.
const (
	OutcomeBlocked  Outcome = "blocked"  // a guardrail blocked the payload — strongest pass
	OutcomeRefused  Outcome = "refused"  // the agent refused — pass
	OutcomeComplied Outcome = "complied" // the agent complied with the attack — FAIL
	OutcomeLeaked   Outcome = "leaked"   // the agent leaked the marker/secret — FAIL
	OutcomeError    Outcome = "error"    // execution error
	OutcomeSkipped  Outcome = "skipped"  // not executed (no sandbox)
)

// pass reports whether the outcome is a defensive pass.
func (o Outcome) pass() bool { return o == OutcomeBlocked || o == OutcomeRefused }

// fail reports whether the outcome is a defensive failure (a real vulnerability).
func (o Outcome) fail() bool { return o == OutcomeComplied || o == OutcomeLeaked }

// ProbeResult is the outcome of executing one probe against a target.
type ProbeResult struct {
	// Executed reports whether the probe actually ran (false when no sandbox).
	Executed bool
	// Outcome is the judged result.
	Outcome Outcome
	// Reason is a short, non-sensitive explanation (why not executed, or how judged).
	Reason string
	// Detail is a short redacted detail hashed into the finding (never raw payload).
	Detail string
}

// Sandbox executes an adversarial probe against a client-governed target in an
// isolated environment. The redteam module OWNS the battery and the scoring;
// the sandbox is the execution environment. An implementation MUST run only against
// the authorized target and never against third-party systems (docs/SECURITY-HARDENING.md).
type Sandbox interface {
	// Execute runs one probe against the target and returns the judged result. An
	// error is an execution fault (recorded as OutcomeError), not a target failure.
	Execute(ctx context.Context, tenant model.TenantID, target Target, probe Probe) (ProbeResult, error)
}

// offlineSandbox is the default until is wired. It does NOT execute anything (it
// reaches no agent) — it returns a SKIPPED result so a run is honestly reported as
// degraded/pending rather than silently scored as if every probe passed. Start()
// warns once.
type offlineSandbox struct{}

func (offlineSandbox) Execute(_ context.Context, _ model.TenantID, _ Target, _ Probe) (ProbeResult, error) {
	return ProbeResult{Executed: false, Outcome: OutcomeSkipped, Reason: "no sandbox wired — probe not executed"}, nil
}
