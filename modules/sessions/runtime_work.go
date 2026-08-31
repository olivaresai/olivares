// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/core/model"
)

const (
	workInputAccepted  = "work_input_accepted"
	workInputAmbiguous = "work_input_ambiguous"
	workStopConfirmed  = "work_stop_confirmed"
	workStopAmbiguous  = "work_stop_ambiguous"
)

// runHasWorkBinding is deliberately conservative: any non-NULL member of the
// durable launch stamp makes the run work-bound. A partially written/corrupt stamp
// must never fall through to an unfenced legacy control path.
func runHasWorkBinding(rec model.Record) bool {
	return !rec.IsNull(colRunWorkItemID) ||
		!rec.IsNull(colRunWorkLeaseFence) ||
		!rec.IsNull(colRunWorkDispatchKey) ||
		!rec.IsNull(colRunWorkOwnerEpoch) ||
		!rec.IsNull(colRunWorkLaunchSpecHash)
}

// refuseLegacyControlUnderWork is the gate the legacy /stop, /input and /resume
// paths ask before touching a run. A durable work stamp selects the fenced
// control plane for the lifetime of the run, even after one lease generation
// ends. Reopening legacy control after a read of the lease creates a TOCTOU:
// acquire can install a new generation between that read and Process.Send/Stop.
// The immutable stamp is therefore both evidence and the permanent selector.
func (m *Module) refuseLegacyControlUnderWork(
	_ context.Context,
	_ model.TenantID,
	rec model.Record,
) error {
	if runHasWorkBinding(rec) {
		return conflictErr("work-bound session requires fenced runtime control")
	}
	return nil
}

// InputForWork writes one line under the exact durable WorkLease generation
// stamped on the run. Authority is checked before the external effect and the
// success event is settled only if that authority still holds afterwards.
func (m *Module) InputForWork(
	ctx context.Context,
	tenant model.TenantID,
	runRef string,
	presentedFence int64,
	data []byte,
) error {
	lease, err := m.assertRunWorkLease(ctx, tenant, runRef, presentedFence)
	if err != nil {
		return err
	}
	generation := runtimeWorkGenerationFromLease(lease)
	rec, err := m.loadRun(ctx, tenant, runRef)
	if err != nil {
		return err
	}
	attempted, err := m.sendInputLoaded(ctx, tenant, runRef, data, rec)
	if err != nil {
		if !attempted {
			return err
		}
		return m.recordAmbiguousWorkAction(
			context.WithoutCancel(ctx), tenant, runRef, generation, workInputAmbiguous, err,
		)
	}
	settleCtx := context.WithoutCancel(ctx)
	if err := m.settleRunWorkAction(settleCtx, tenant, runRef, generation, workInputAccepted); err != nil {
		return m.recordAmbiguousWorkAction(
			settleCtx, tenant, runRef, generation, workInputAmbiguous, err,
		)
	}
	return nil
}

// StopForWork stops one supervised process under a durable WorkLease fence. A
// terminal run is not sufficient proof by itself: verifyStoppedWorkLease checks
// that the observed death callback settled the expected generation. Any failure
// after the stop attempt is durably marked ambiguous and returned as UNKNOWN.
func (m *Module) StopForWork(
	ctx context.Context,
	tenant model.TenantID,
	runRef string,
	presentedFence int64,
	reason string,
) error {
	if reason != "" && !boundedText(reason, 1, 512) {
		return broken(400, "invalid_command")
	}
	defer m.rt.lockRun(liveKey(tenant, runRef))()
	lease, err := m.assertRunWorkLease(ctx, tenant, runRef, presentedFence)
	if err != nil {
		return err
	}
	generation := runtimeWorkGenerationFromLease(lease)
	rec, err := m.loadRun(ctx, tenant, runRef)
	if err != nil {
		return err
	}
	_, attempted, err := m.stopRunLoaded(
		ctx, tenant, runRef, model.ActorSystem, model.ActorSystem, reason, rec,
	)
	if err != nil {
		if !attempted {
			return err
		}
		return m.recordAmbiguousWorkAction(
			context.WithoutCancel(ctx), tenant, runRef, generation, workStopAmbiguous, err,
		)
	}
	settleCtx := context.WithoutCancel(ctx)
	if err := m.verifyStoppedWorkLease(settleCtx, tenant, runRef, presentedFence); err != nil {
		if !attempted {
			return err
		}
		return m.recordAmbiguousWorkAction(
			settleCtx, tenant, runRef, generation, workStopAmbiguous, err,
		)
	}
	if err := m.settleRunWorkAction(settleCtx, tenant, runRef, generation, workStopConfirmed); err != nil {
		if !attempted {
			return err
		}
		return m.recordAmbiguousWorkAction(
			settleCtx, tenant, runRef, generation, workStopAmbiguous, err,
		)
	}
	return nil
}

// recordAmbiguousWorkAction persists the uncertainty without payload content.
// The original observation error is retained for errors.Is/errors.As; a failure
// to write the ambiguity is joined and logged, never collapsed into success.
func (m *Module) recordAmbiguousWorkAction(
	ctx context.Context,
	tenant model.TenantID,
	runRef string,
	generation runtimeWorkGeneration,
	event string,
	cause error,
) error {
	if err := m.settleRunWorkAction(ctx, tenant, runRef, generation, event); err != nil {
		m.warnf("sessions: could not persist ambiguous work runtime outcome",
			"run_ref", runRef, "event", event, "err", redactErr(err))
		cause = errors.Join(cause, err)
	}
	return unknown(event, cause)
}
