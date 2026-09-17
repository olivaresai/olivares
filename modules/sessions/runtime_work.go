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
	workInputAccepted = "work_input_accepted"
	// workInterruptAccepted is a SEPARATE settlement name from the input one, not a
	// reuse. Both leave the run active and settle under the same postcondition, so
	// sharing a name would have compiled and worked; it would also have made the
	// durable ledger unable to answer "was this generation spoken to, or cancelled?"
	// — and the ledger is the only place that question can be asked afterwards.
	workInterruptAccepted  = "work_interrupt_accepted"
	workInputAmbiguous     = "work_input_ambiguous"
	workInterruptAmbiguous = "work_interrupt_ambiguous"
	workStopConfirmed      = "work_stop_confirmed"
	workStopAmbiguous      = "work_stop_ambiguous"
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

// InputForWork writes one RAW NDJSON line under the exact durable WorkLease
// generation stamped on the run. Authority is checked before the external effect
// and the success event is settled only if that authority still holds afterwards.
//
// The transport is unchanged and deliberately so: this is the Claude stream-json
// contract, one line on the child's stdin, and sendInputLoaded still REFUSES it
// for a protocol-driven run — an owned JSON-RPC peer would read an arbitrary line
// as a method call, approvals included. Text for such a run goes through
// TextForWork, which is a different method because it is a different contract.
//
// It takes the per-run operation lock at this OUTER entry, exactly like
// TextForWork and StopForWork. The lock is NOT in sendInputLoaded, which this
// method and the legacy route both call while already holding it: taking a
// non-reentrant mutex twice on the same key deadlocks the caller.
func (m *Module) InputForWork(
	ctx context.Context,
	tenant model.TenantID,
	runRef string,
	presentedFence int64,
	data []byte,
) error {
	// Admission is cancellation-aware: a fenced caller that has gone away stops
	// waiting for the key instead of queueing ahead of its own request.
	release, err := m.rt.lockRunContext(ctx, liveKey(tenant, runRef))
	if err != nil {
		return err
	}
	defer release()
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

// TextForWork writes one operator TURN under the exact durable WorkLease
// generation stamped on the run.
//
// ⛔ IT EXISTS BECAUSE A WORK-BOUND DRIVER RUN HAD NO ROUTE AT ALL. The durable
// work stamp permanently selects the fenced control plane (refuseLegacyControlUnderWork),
// so the unfenced text route refuses it; and the only fenced route was
// InputForWork, which carries RAW BYTES — which a driver run must refuse, because
// an owned JSON-RPC peer would read an arbitrary line as a method call. The review
// showed both refusing the same run: it could be launched and never spoken to.
//
// The answer is a second TYPED route, not a reinterpretation of the first. Raw
// bytes stay raw bytes for the Claude stream-json child; text stays text for a
// protocol driver; neither is ever guessed from the other's shape.
//
// It carries the same three-part authority as StopForWork — the WorkLease fence
// here, and the launch generation plus the durable Claim inside driverInput — and
// the same uncertainty contract: a refusal before the frame is a refusal, and a
// failure after it is recorded as ambiguous and returned as UNKNOWN.
func (m *Module) TextForWork(
	ctx context.Context,
	tenant model.TenantID,
	runRef string,
	presentedFence int64,
	text string,
) error {
	if !boundedText(text, 1, maxWorkTextInputBytes) {
		return broken(400, "invalid_command")
	}
	// Admission is cancellation-aware: a fenced caller that has gone away stops
	// waiting for the key instead of queueing ahead of its own request.
	release, err := m.rt.lockRunContext(ctx, liveKey(tenant, runRef))
	if err != nil {
		return err
	}
	defer release()
	lease, err := m.assertRunWorkLease(ctx, tenant, runRef, presentedFence)
	if err != nil {
		return err
	}
	generation := runtimeWorkGenerationFromLease(lease)
	rec, err := m.loadRun(ctx, tenant, runRef)
	if err != nil {
		return err
	}
	attempted, err := m.sendTextInputLoaded(ctx, tenant, runRef, text, rec)
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

// maxWorkTextInputBytes bounds the text of one work-fenced turn at 128 KiB, measured
// on the text as submitted. It is a different subject from the HTTP intake limit,
// which bounds the whole request document at 1 MiB in decodeJSONBody
// (runtime_dto.go), and it applies to this fenced route and its in-process callers
// rather than to provider input in general.
const maxWorkTextInputBytes = 128 * 1024

// InterruptForWork cancels the ACTIVE provider turn of a work-bound run under the
// exact durable WorkLease generation stamped on it, and leaves the owned process,
// the conversation and the run usable for the next input.
//
// ⛔ IT EXISTS BECAUSE THE FENCED PLANE HAD TWO CONTROLS AND NEEDED THREE. A
// durable work stamp selects the fenced plane for the life of the run
// (refuseLegacyControlUnderWork), and that plane had input and stop but no
// interrupt — so a work-bound driver run could be spoken to and it could be
// killed, and the only way to take back a turn already in flight was to end the
// process, the conversation and the claim generation with it. That is not a
// harsher interrupt; it is a different control wearing its name.
//
// So it NEVER falls back to stop. A provider with no turn interruption is told so
// by interruptRunLoaded and nothing is stopped, and every refusal below happens
// before any provider effect:
//
//   - a fence that is missing, moved, released or expired, or a session Claim that
//     has moved or lapsed, is refused by assertRunWorkLease;
//   - a partially written work stamp never falls through to a legacy control: it
//     is UNKNOWN/evidence_unavailable here and a conflict on the legacy route,
//     because runHasWorkBinding counts any non-NULL member as work-bound;
//   - the launch generation, the registered live handle, the persisted profile and
//     the Claim holder/fence are re-proved by assertRunAuthority INSIDE
//     interruptDriverTurn, in the transaction that authorises the effect.
//
// It keeps the uncertainty contract of TextForWork and StopForWork exactly: a
// refusal before the frame is a refusal with its own taxonomy, and a failure once
// something may have crossed is recorded ambiguous and returned UNKNOWN. The
// distinction is only as good as what the driver reports, which is why
// DriverSession.Interrupt returns the attempted flag rather than an error alone.
func (m *Module) InterruptForWork(
	ctx context.Context,
	tenant model.TenantID,
	runRef string,
	presentedFence int64,
) error {
	return m.interruptForWork(ctx, tenant, runRef, presentedFence, model.ActorSystem, model.ActorSystem)
}

// interruptForWork keeps the dispatch fence as authority while recording the
// caller who initiated the control. HTTP callers come from the authenticated
// principal; internal dispatch callers retain the system identity above.
func (m *Module) interruptForWork(
	ctx context.Context,
	tenant model.TenantID,
	runRef string,
	presentedFence int64,
	actor, actorKind string,
) error {
	// Admission is cancellation-aware: a fenced caller that has gone away stops
	// waiting for the key instead of queueing ahead of its own request.
	release, err := m.rt.lockRunContext(ctx, liveKey(tenant, runRef))
	if err != nil {
		return err
	}
	defer release()
	lease, err := m.assertRunWorkLease(ctx, tenant, runRef, presentedFence)
	if err != nil {
		return err
	}
	generation := runtimeWorkGenerationFromLease(lease)
	rec, err := m.loadRun(ctx, tenant, runRef)
	if err != nil {
		return err
	}
	// The work lease and launch claim still authorize the effect. The initiating
	// principal is audit attribution, never an alternative source of authority.
	_, attempted, err := m.interruptRunLoaded(
		ctx, tenant, runRef, actor, actorKind, rec,
	)
	if err != nil {
		if !attempted {
			return err
		}
		return m.recordAmbiguousWorkAction(
			context.WithoutCancel(ctx), tenant, runRef, generation, workInterruptAmbiguous, err,
		)
	}
	settleCtx := context.WithoutCancel(ctx)
	if err := m.settleRunWorkAction(settleCtx, tenant, runRef, generation, workInterruptAccepted); err != nil {
		return m.recordAmbiguousWorkAction(
			settleCtx, tenant, runRef, generation, workInterruptAmbiguous, err,
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
	// Admission is cancellation-aware: a fenced caller that has gone away stops
	// waiting for the key instead of queueing ahead of its own request.
	release, err := m.rt.lockRunContext(ctx, liveKey(tenant, runRef))
	if err != nil {
		return err
	}
	defer release()
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
