// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

var ErrRuntimeAttemptNotCurrent = errors.New("sessions: runtime attempt is not current")
var ErrRuntimeOutputUnavailable = errors.New("sessions: output is unavailable for this runtime transport")

// RuntimeOutputEvent is a value copied from one exact attempt's bounded output ring.
// A lag names the first available sequence; it never silently skips missing output.
type RuntimeOutputEvent struct {
	Type    string `json:"type"`
	Seq     int64  `json:"seq,omitempty"`
	Stream  string `json:"stream,omitempty"`
	Line    string `json:"line,omitempty"`
	Dropped int64  `json:"dropped,omitempty"`
	NextSeq int64  `json:"next_seq,omitempty"`
}

// InterruptRuntimeAttempt cancels a turn on the named current attempt. The owner
// must authorize the human caller and the target before entering this port; an
// actor string and a launch identity are not authorization. The run lock covers
// both the identity check and native interruption, including its fenced audit.
func (m *Module) InterruptRuntimeAttempt(ctx context.Context, target RuntimeInputTarget,
	actor, actorKind, reason string,
) (RuntimeInputResult, error) {
	result := RuntimeInputResult{Outcome: RuntimeInputRefused}
	if ctx == nil || !validAttemptReason(reason) || actor == "" || actorKind != model.ActorUser {
		return result, ErrRuntimeInputAuthority
	}
	if err := validateRuntimeInputTarget(target); err != nil {
		return result, err
	}
	release, err := m.rt.lockRunContext(ctx, liveKey(target.Tenant, target.RunRef))
	if err != nil {
		return result, err
	}
	defer release()
	err = m.runtimeData(ctx).Mutate(ctx, target.Tenant, func(sc store.Scope) error {
		return m.ValidateRuntimeInputTargetInScope(ctx, sc, target)
	})
	if err != nil {
		return result, err
	}
	rec, err := m.loadRun(ctx, target.Tenant, target.RunRef)
	if err != nil {
		return result, err
	}
	if _, err = m.currentOutputAttempt(ctx, RuntimeLaunchIdentity{Tenant: target.Tenant, WorkspaceID: target.WorkspaceID, RunRef: target.RunRef, RuntimeLaunchID: target.ExpectedLaunch, SessionSID: target.ExpectedSID}); err != nil {
		return result, err
	}
	_, attempted, err := m.interruptRunLoadedWithReason(ctx, target.Tenant, target.RunRef, actor, actorKind, reason, rec)
	result.Attempted = attempted
	if err != nil {
		if attempted {
			result.Outcome = RuntimeInputUnknown
		}
		return result, err
	}
	result.Outcome = RuntimeInputAccepted
	return result, nil
}

func validAttemptReason(reason string) bool {
	return reason != "" && len(reason) <= 2048 && utf8.ValidString(reason) && strings.TrimSpace(reason) == reason && !strings.ContainsAny(reason, "\r\n\x00")
}
func interruptionDetail(detail, reason string) string {
	if reason == "" {
		return detail
	}
	return detail + "; reason: " + reason
}

// currentOutputAttempt is called with the run lock held. It compares the stored
// workspace, launch and SID and the exact owned live handle, never just RunRef.
func (m *Module) currentOutputAttempt(ctx context.Context, target RuntimeLaunchIdentity) (*liveRun, error) {
	if target.Tenant.IsZero() || target.WorkspaceID.IsZero() || target.RunRef == "" || !validRuntimeUUIDv7(target.RuntimeLaunchID.String()) || !validCanonicalSID(target.SessionSID) {
		return nil, ErrRuntimeAttemptNotCurrent
	}
	rec, err := m.loadRun(ctx, target.Tenant, target.RunRef)
	if err != nil {
		return nil, err
	}
	if rec.String(colRunAuthzWorkspaceID) != target.WorkspaceID.String() || rec.String(colRuntimeLaunchID) != target.RuntimeLaunchID.String() || rec.String(colRunClaimSID) != target.SessionSID || rec.String(colState) != stateRunning {
		return nil, ErrRuntimeAttemptNotCurrent
	}
	live, ok := m.rt.getLive(target.Tenant, target.RunRef)
	if !ok || live.launchID != target.RuntimeLaunchID || live.claim.SID != target.SessionSID {
		return nil, ErrRuntimeAttemptNotCurrent
	}
	return live, nil
}

// AttachRuntimeOutput follows one exact attempt. Callers authorize the read and
// retain their own cancellation/revocation guard. Replacement attempts are never
// followed. The callback receives value copies and may stop the stream with an error.
func (m *Module) AttachRuntimeOutput(ctx context.Context, target RuntimeLaunchIdentity, actor, actorKind, reason string, from int64, emit func(RuntimeOutputEvent) error) error {
	if ctx == nil || emit == nil || from < 0 || actor == "" || actorKind != model.ActorUser || !validAttemptReason(reason) {
		return ErrRuntimeInputAuthority
	}
	release, err := m.rt.lockRunContext(ctx, liveKey(target.Tenant, target.RunRef))
	if err != nil {
		return err
	}
	live, err := m.currentOutputAttempt(ctx, target)
	if err == nil && live.transport == TransportRemoteControl {
		err = ErrRuntimeOutputUnavailable
	}
	if err == nil {
		err = m.runtimeData(ctx).Mutate(ctx, target.Tenant, func(sc store.Scope) error {
			_, err := sc.Audit().Append(ctx, model.AuditDraft{Actor: actor, ActorKind: actorKind, Action: "sessions.run.attach", TargetKind: runKind, Meta: map[string]any{"run_ref": target.RunRef, "runtime_launch_id": target.RuntimeLaunchID.String(), "reason": reason}})
			return err
		})
	}
	release()
	if err != nil {
		return err
	}
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	cursor := from
	for {
		wake := live.ring.wait()
		release, err = m.rt.lockRunContext(ctx, liveKey(target.Tenant, target.RunRef))
		if err != nil {
			return err
		}
		current, checkErr := m.currentOutputAttempt(ctx, target)
		read := live.ring.readFrom(cursor)
		if errors.Is(checkErr, ErrRuntimeAttemptNotCurrent) && current == nil && read.closed {
			// Native finalization clears the launch column but retains this closed
			// handle. An already admitted reader may drain its original final tail.
			// A replacement live handle or a new nonempty launch always refuses.
			held, present := m.rt.getLive(target.Tenant, target.RunRef)
			row, rowErr := m.loadRun(ctx, target.Tenant, target.RunRef)
			if rowErr == nil && present && held == live && row.String(colRuntimeLaunchID) == "" &&
				row.String(colState) != stateRunning && row.String(colRunAuthzWorkspaceID) == target.WorkspaceID.String() && row.String(colRunClaimSID) == target.SessionSID {
				current, checkErr = live, nil
			}
		}
		if checkErr != nil || current != live {
			release()
			if checkErr != nil {
				return checkErr
			}
			return ErrRuntimeAttemptNotCurrent
		}
		release()
		if read.gap {
			next := read.next
			if len(read.frames) > 0 {
				next = read.frames[0].Seq
			}
			if err := emit(RuntimeOutputEvent{Type: "lag", Dropped: read.dropped, NextSeq: next}); err != nil {
				return err
			}
		}
		for _, frame := range read.frames {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := emit(RuntimeOutputEvent{Type: "output", Seq: frame.Seq, Stream: frame.Stream, Line: string(frame.Data)}); err != nil {
				return err
			}
		}
		cursor = read.next
		if read.closed {
			return emit(RuntimeOutputEvent{Type: "end"})
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wake:
		case <-ticker.C:
			if err := emit(RuntimeOutputEvent{Type: "heartbeat"}); err != nil {
				return err
			}
		}
	}
}
