// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// managed_stop_observation.go (P2 / W2) — Phases F5 and G: cross the process
// boundary, then report and settle only what was actually observed.
//
// ⛔ A CLOSED finalizedCh IS NOT A PROCESS FACT. It means the finalizer returned,
// and the finalizer also returns after a Wait error, after a terminal transition
// that failed to persist, and after an incarnation check that skipped the
// transition entirely. The process fact is P1: the committed terminal observation
// for EXACTLY the expected launch. Only P1 process_exit_observed, with a Stop that
// returned nil or reaped the child, is an observed exit, and only an observed exit
// settles completed (correction 1 §12.3). Everything else is uncertainty, and
// uncertainty is unknown — never stopped.
//
// ⛔ AND EVERY STAGE PAST THE DISPATCH FENCE OWNS ITS BOUND (correction 1 §12.2,
// correction 2 §7). The stop and finalize wait ignore the caller's cancellation and
// are bounded by the runtime's own waits; the revocation phase is created after
// Stop; the observation read and the settlement each get a FRESH T. None reuses
// the admission context, which may already be over.

// The three ratified process outcomes.
const (
	managedStopExitObserved = "exit_observed"
	managedStopNotReaped    = "not_reaped"
	managedStopUnverified   = "unverified"
)

// The three ratified credential-revocation results.
const (
	managedStopRevoked       = "revoked"
	managedStopRevokeFailed  = "failed"
	managedStopRevokeNotUsed = "not_applicable"
)

// errManagedStopObservationAmbiguous refuses more than one terminal observation
// for a single launch: two answers to one question is not evidence.
var errManagedStopObservationAmbiguous = errors.New(
	"sessions: more than one terminal observation names the expected launch")

// managedStopStageContext is one post-dispatch stage's own bound: detached from
// the admission context's cancellation and deadline, bounded by T from the moment
// the stage starts.
func managedStopStageContext(actx context.Context, ports *managedStopPorts) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(actx), ports.admissionTimeout)
}

// stopAndObserveManagedRun is Phases F5 and G for the originating call.
func (m *Module) stopAndObserveManagedRun(
	actx context.Context,
	ports *managedStopPorts,
	tenant model.TenantID,
	req ManagedStopRequest,
	authority managedStopAuthority,
	admission managedStopAdmission,
	lr *liveRun,
	operationRef, semantic string,
) ManagedStopResult {
	var revocationStarted, revocationEnded time.Time
	phase := managedRevocationPhase(actx, ports.admissionTimeout)
	timedPhase := func() (context.Context, context.CancelFunc) {
		revocationStarted = time.Now()
		rctx, cancel := phase()
		return rctx, func() {
			revocationEnded = time.Now()
			cancel()
		}
	}
	effectStarted := time.Now()
	// The stop and the finalize wait ignore the caller's cancellation: past the
	// dispatch fence a walked-away caller must not cut the wait short. The wait is
	// still bounded, by 2×waitDelay inside stopLiveEffect.
	finalize, stopErr, revokeErr := m.stopLiveEffect(context.WithoutCancel(actx), lr, req.Reason, timedPhase)
	effectEnded := time.Now()
	m.logManagedStopStage(operationRef, "stop", effectStarted, revocationStarted)
	m.logManagedStopStage(operationRef, "revocation", revocationStarted, revocationEnded,
		"failed", revokeErr != nil)
	m.logManagedStopStage(operationRef, "finalize_wait", revocationEnded, effectEnded,
		"finalizer_returned", finalize == stopEffectFinalized)

	observationStarted := time.Now()
	observation, obsErr := m.observeManagedStopExit(actx, ports, tenant, req)
	m.logManagedStopStage(operationRef, "observation", observationStarted, time.Now(),
		"read", obsErr == nil)

	result := ManagedStopResult{
		Outcome:              ManagedStopUnknown,
		OperationRef:         operationRef,
		Attempted:            true,
		ProcessOutcome:       managedStopProcessOutcome(observation, stopErr),
		Observation:          observation,
		CredentialRevocation: managedStopRevocationWord(revokeErr, admission.hadCredentials),
		Detail:               "the expected launch's exit was not observed",
	}
	state := model.EvidenceOpUnknown
	if result.ProcessOutcome == managedStopExitObserved {
		state, result.Outcome, result.Detail = model.EvidenceOpCompleted, ManagedStopStopped, ""
	}
	settlementStarted := time.Now()
	result.Settlement = m.settleManagedStop(actx, ports, tenant, authority.workspace, req, operationRef, semantic,
		state, managedStopResultDigest(result), "launch:"+req.ExpectedLaunch.String())
	m.logManagedStopStage(operationRef, "settlement", settlementStarted, time.Now(),
		"recorded", result.Settlement != "")
	return result
}

// managedStopProcessOutcome classifies the process fact from P1 and the Stop
// error, exactly as correction 1 §12.3 states it.
func managedStopProcessOutcome(observation string, stopErr error) string {
	switch {
	case observation == obsProcessExitObserved && childWasReaped(stopErr):
		return managedStopExitObserved
	case errors.Is(stopErr, ErrChildNotReaped):
		return managedStopNotReaped
	default:
		return managedStopUnverified
	}
}

// managedStopRevocationWord reports the explicit phase's own result. A later
// successful cleanup by the finalizer does not rewrite it.
func managedStopRevocationWord(revokeErr error, hadCredentials bool) string {
	switch {
	case revokeErr != nil:
		return managedStopRevokeFailed
	case !hadCredentials:
		return managedStopRevokeNotUsed
	default:
		return managedStopRevoked
	}
}

// observeManagedStopExit reads the committed P1 for the expected launch under the
// observation stage's own bound. An empty string means no observation is
// committed, or none could be read; both are uncertainty, never an exit.
func (m *Module) observeManagedStopExit(
	actx context.Context,
	ports *managedStopPorts,
	tenant model.TenantID,
	req ManagedStopRequest,
) (string, error) {
	octx, cancel := managedStopStageContext(actx, ports)
	defer cancel()
	var observation string
	err := m.data.View(octx, tenant, func(raw store.Scope) error {
		var rerr error
		observation, rerr = readExactLaunchObservation(octx, raw, req.RunRef, req.ExpectedLaunch)
		return rerr
	})
	if err != nil {
		m.warnf("sessions: managed stop could not read the expected launch's process observation",
			"run_ref", req.RunRef)
		return "", err
	}
	return observation, nil
}

// readExactLaunchObservation returns the terminal observation recorded for
// exactly one retired launch of one run. It is reached raw from a run this scope's
// caller already proved visible, exactly like the claim and work rows.
func readExactLaunchObservation(ctx context.Context, sc store.Scope, runRef string, launch model.ID) (string, error) {
	repo, err := sc.Ext(runEventKind)
	if err != nil {
		return "", err
	}
	rows, _, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{eq(colEvRunRef, runRef), eq(colEvRetiredLaunchID, launch.String())},
		Limit:   2,
	})
	switch {
	case err != nil:
		return "", err
	case len(rows) == 0:
		return "", nil
	case len(rows) > 1:
		return "", errManagedStopObservationAmbiguous
	}
	return rows[0].String(colEvTerminalObservation), nil
}

// logManagedStopStage records one stage's duration at debug level. The durations
// are the measured stage timings the operation's bounds are checked against; they
// carry no credential, reason or policy content.
func (m *Module) logManagedStopStage(operationRef, stage string, start, end time.Time, extra ...any) {
	args := append([]any{"operation_ref", operationRef, "stage", stage,
		"elapsed_ms", end.Sub(start).Milliseconds()}, extra...)
	m.debugf("sessions: managed stop stage", args...)
}
