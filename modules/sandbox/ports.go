// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sandbox

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/core/model"
)

// This file defines the integration SEAMS this module depends on but does not own,
// each expressed in the module's own terms so the module stays decoupled from its
// neighbors' packages (the same convention as redteam.Sandbox / orchestration's
// Dispatcher). The composition root injects real adapters; each seam defaults to a
// SAFE, honest behavior so an un-wired deployment can execute a portable, isolated
// run but can NEVER reach a real resource, silently score a pass, fabricate a
// replay, or generate synthetic data (docs/contracts/§3.3).

// ----------------------------------------------------------------------------
// Runner — the ISOLATED, ephemeral execution backend.
// ----------------------------------------------------------------------------

// Step is one input fed to the runner. Key identifies the step (stable, for the
// per-step output row); Input is the synthetic input / the resource it asks for.
type Step struct {
	Key   string
	Input string
}

// Mock is one simulated MCP/resource response. Resource is matched against a
// step's Input; Response is the synthetic text returned for a hit. No secrets.
type Mock struct {
	Resource string
	Response string
}

// RunSpec is the COMPLETE input to a run — the ONLY thing the isolated runner sees.
// It carries no store, network or secret handle by construction, which is what makes
// the default runner isolated (docs/contracts).
type RunSpec struct {
	Steps []Step
	Mocks []Mock
}

// StepOutput is the runner's result for one step. MockHit reports whether the step
// resolved against a mock (vs a deterministic mock-miss).
type StepOutput struct {
	Key     string
	Output  string
	MockHit bool
}

// RunOutcome is the full result of a run. Destroyed reports that the ephemeral
// execution state was discarded (true for any isolated, ephemeral runner).
type RunOutcome struct {
	Steps     []StepOutput
	Destroyed bool
}

// Runner executes a scenario's steps in an isolated, ephemeral environment and
// returns the per-step outputs. An implementation MUST be isolated: it may not reach
// production resources, the network or secrets except through the mocks in the spec.
// The OS-level backend (hardened container / microVM) implements this same interface.
type Runner interface {
	// Name identifies the backend ("inproc-mock" | "container" | "microvm" ...).
	Name() string
	// Isolated reports whether the runner guarantees isolation (no egress, no
	// secrets, no production). Recorded on every run so a portable/degraded backend
	// is visible and auditable, never hidden.
	Isolated() bool
	// Run executes the spec and returns the per-step outcome. An error is an
	// execution fault, not a step failure.
	Run(ctx context.Context, tenant model.TenantID, spec RunSpec) (RunOutcome, error)
}

// mockMissPrefix and mockMissSuffix bracket the DETERMINISTIC marker the in-proc
// runner emits for a step with no matching mock. It is a synthetic placeholder, NOT
// the output of a real resource (which the runner cannot reach).
const (
	mockMissPrefix = "[[mock-miss:"
	mockMissSuffix = "]]"
)

// mockMiss returns the deterministic mock-miss marker for an input.
func mockMiss(input string) string { return mockMissPrefix + input + mockMissSuffix }

// inprocMockRunner is the default runner: deterministic, in-memory, and isolated BY
// CONSTRUCTION — the struct has NO field carrying a store/network/secret handle, so
// Run physically cannot reach anything but its RunSpec. For each step it resolves the
// matching Mock by Resource == Step.Input (the deterministic resolution rule) and
// returns MockHit=true with the mock's Response; a step with no matching mock returns
// MockHit=false and a deterministic mock-miss marker, NEVER a real resource. It is
// ephemeral (state lives in the call, discarded on return) ⇒ Destroyed=true.
//
// Determinism: no time.Now, no rand, no map-iteration-order dependence — outputs are
// produced in Steps order; mocks are resolved through a built lookup map but the
// OUTPUT order follows the Steps slice, so the same spec always yields the same
// outputs in the same order.
type inprocMockRunner struct{}

func (inprocMockRunner) Name() string   { return "inproc-mock" }
func (inprocMockRunner) Isolated() bool { return true }

func (inprocMockRunner) Run(_ context.Context, _ model.TenantID, spec RunSpec) (RunOutcome, error) {
	// Build a deterministic resolution map: last write wins for a duplicated
	// resource (a stable, documented rule). The map is only a lookup; output order
	// follows the Steps slice, so iteration order never affects the result.
	resolve := make(map[string]string, len(spec.Mocks))
	for _, mk := range spec.Mocks {
		resolve[mk.Resource] = mk.Response
	}
	out := RunOutcome{Steps: make([]StepOutput, 0, len(spec.Steps)), Destroyed: true}
	for _, s := range spec.Steps {
		if resp, ok := resolve[s.Input]; ok {
			out.Steps = append(out.Steps, StepOutput{Key: s.Key, Output: resp, MockHit: true})
			continue
		}
		// No mock ⇒ deterministic mock-miss marker. The runner does NOT reach a real
		// resource; it returns a synthetic placeholder so the absence is visible.
		out.Steps = append(out.Steps, StepOutput{Key: s.Key, Output: mockMiss(s.Input), MockHit: false})
	}
	return out, nil
}

// ----------------------------------------------------------------------------
// Scorer — the XII (evals) integration seam.
// ----------------------------------------------------------------------------

// ScoreRequest asks XII to score a run's outputs against a suite. SubjectKind/Ref
// and Variant identify what produced the outputs; Outputs is the step_key→output
// map the scorer measures.
type ScoreRequest struct {
	SuiteRef    string
	SubjectKind string
	SubjectRef  string
	Variant     string
	Outputs     map[string]string
}

// ScoreVerdict is XII's aggregate answer for one scored run.
type ScoreVerdict struct {
	Score    float64
	PassRate float64
	Passed   bool
	Total    int
	PassedN  int
	FailedN  int
}

// Scorer scores the outputs a sandbox run produced against an evals suite
// (integration with XII). The real adapter — a tiny struct that calls evals'
// ScoreOutputs and translates the DTOs — lives in the composition root (the only
// site authorized to import both modules) or a test, never in evals production code
// (docs/contracts: zero sibling import).
type Scorer interface {
	Score(ctx context.Context, tenant model.TenantID, req ScoreRequest) (ScoreVerdict, error)
}

// errNoScorer is the honest fail-closed error the unwired scorer returns: a run with
// a suite_ref but no scorer is recorded "executed, not scored", NEVER a silent pass.
var errNoScorer = errors.New("sandbox: no scorer wired (XII); outputs executed, not scored")

// unscoredScorer is the default: it scores nothing and returns an explicit error so
// the caller records the run as executed-not-scored.
type unscoredScorer struct{}

func (unscoredScorer) Score(context.Context, model.TenantID, ScoreRequest) (ScoreVerdict, error) {
	return ScoreVerdict{}, errNoScorer
}

// ----------------------------------------------------------------------------
// HistorySource — the replay timeline seam.
// ----------------------------------------------------------------------------

// ReplayStep is one reconstructed input of a historical session, fed back to the
// runner for deterministic re-execution.
type ReplayStep struct {
	Key   string
	Input string
}

// HistorySource reconstructs the ordered input sequence of a historical session so
// replay can re-execute it deterministically. A richer adapter (the sessions
// timeline of module II) is wired with WithHistorySource. With no ordered timeline
// available it returns an empty slice ⇒ the replay is reported DEGRADED, never
// fabricated.
type HistorySource interface {
	Timeline(ctx context.Context, tenant model.TenantID, sessionRef string) ([]ReplayStep, error)
}

// coreHistorySource is the default: it reads what core exposes — a Session looked up
// by id or external id. Core has no per-message timeline (only refs/metadata are
// reliable; Goal/Summary are not populated by capture), so it cannot reconstruct an
// ordered list of input steps. It therefore returns ZERO steps so the replay is
// honestly degraded — it never invents inputs. A richer source replaces it via
// WithHistorySource. It holds no store handle: the module passes whatever it can read
// through its own scope, so this default is intentionally a no-op that yields nothing.
type coreHistorySource struct{}

func (coreHistorySource) Timeline(context.Context, model.TenantID, string) ([]ReplayStep, error) {
	// No ordered per-message timeline is reconstructable from the core Session alone.
	// Returning nil ⇒ replay degraded (zero steps), never fabricated input.
	return nil, nil
}

// ----------------------------------------------------------------------------
// SyntheticDataGenerator — POST-v1 EXTENSION POINT, NOT IMPLEMENTED.
// ----------------------------------------------------------------------------

// GenSpec describes a synthetic-data generation request. It is part of the POST-v1
// extension surface (README.mdbis · §6); v1 ships no generator.
type GenSpec struct {
	SubjectKind string
	Count       int
	Seed        string
}

// GenSample is one synthetic data sample. POST-v1.
type GenSample struct {
	Key   string
	Input string
}

// SyntheticDataGenerator is a POST-v1 EXTENSION POINT (README.mdbis · §6): synthetic
// / test-data generation is deliberately NOT implemented in v1. The interface is
// declared so a future backend can be wired, but the module ships NO real generator,
// there is NO WithSyntheticData option, and NO route generates data. The default
// produces ZERO samples and an explicit error — verified by a test asserting the
// absence.
type SyntheticDataGenerator interface {
	Generate(ctx context.Context, tenant model.TenantID, spec GenSpec) ([]GenSample, error)
}

// errSyntheticDataPostV1 is the explicit refusal of the default generator.
var errSyntheticDataPostV1 = errors.New("synthetic-data generation is POST-v1 and not wired")

// noSyntheticData is the default (and only) SyntheticDataGenerator: it generates
// ZERO samples and returns errSyntheticDataPostV1. It is not wired into any flow.
type noSyntheticData struct{}

func (noSyntheticData) Generate(context.Context, model.TenantID, GenSpec) ([]GenSample, error) {
	return nil, errSyntheticDataPostV1
}
