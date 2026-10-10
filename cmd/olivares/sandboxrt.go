// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime/sandboxrt"
	"github.com/olivaresai/olivares/modules/sandbox"
)

// The Community sandbox Runner adapter delegates to the isolated runtime.
// Business red-team execution is provided by the private composition overlay.

// ---- sandbox.Runner adapter -----------------------------------------------------

// sandboxRunnerAdapter adapts the sandboxrt engine to sandbox.Runner. A synthetic
// scenario run carries a DENY-ALL egress scope (no Egress allowlist): the instance
// resolves steps against mocks and can reach nothing on the network.
type sandboxRunnerAdapter struct{ eng *sandboxrt.Engine }

var _ sandbox.Runner = sandboxRunnerAdapter{}

// Name reports the engine's effective backend ("gvisor"|"firecracker"|
// "unavailable"). When no backend passed preflight it is "unavailable" and a run
// fails closed — recorded honestly, never a faked microVM.
func (a sandboxRunnerAdapter) Name() string { n, _, _ := a.eng.Primary(); return n }

// Isolated reports the engine's effective isolation guarantee (false when no
// backend is available, so the module's Start() flags a degraded deployment).
func (a sandboxRunnerAdapter) Isolated() bool { _, iso, _ := a.eng.Primary(); return iso }

// Run executes the scenario steps in a fresh, hardened, egress-DENIED ephemeral
// instance and maps the neutral result back onto the module's RunOutcome. Even on
// a fault the destroyed flag is surfaced from the attestation (honest).
func (a sandboxRunnerAdapter) Run(ctx context.Context, tenant model.TenantID, spec sandbox.RunSpec) (sandbox.RunOutcome, error) {
	job := sandboxrt.Job{
		Tenant: tenant.String(),
		RunID:  "scenario:" + tenant.String(),
		Steps:  toRTSteps(spec.Steps),
		Mocks:  toRTMocks(spec.Mocks),
		// No Egress ⇒ the engine's egress proxy denies everything (synthetic only).
	}
	res, err := a.eng.Run(ctx, job)
	if err != nil {
		return sandbox.RunOutcome{Destroyed: res.Attestation.Destroyed}, err
	}
	return sandbox.RunOutcome{Steps: fromRTOutputs(res.Steps), Destroyed: res.Attestation.Destroyed}, nil
}

func toRTSteps(in []sandbox.Step) []sandboxrt.Step {
	out := make([]sandboxrt.Step, 0, len(in))
	for _, s := range in {
		out = append(out, sandboxrt.Step{Key: s.Key, Input: s.Input})
	}
	return out
}

func toRTMocks(in []sandbox.Mock) []sandboxrt.Mock {
	out := make([]sandboxrt.Mock, 0, len(in))
	for _, m := range in {
		out = append(out, sandboxrt.Mock{Resource: m.Resource, Response: m.Response})
	}
	return out
}

func fromRTOutputs(in []sandboxrt.StepOutput) []sandbox.StepOutput {
	out := make([]sandbox.StepOutput, 0, len(in))
	for _, s := range in {
		out = append(out, sandbox.StepOutput{Key: s.Key, Output: s.Output, MockHit: s.MockHit})
	}
	return out
}
