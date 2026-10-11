// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"time"
)

// publishRuntimeOutput keeps the ring and governed recorder in the same order.
// Engine notices use this path without entering the provider's frame parser.
func (m *Module) publishRuntimeOutput(lr *liveRun, stream string, data []byte, at time.Time) int64 {
	lr.outputMu.Lock()
	defer lr.outputMu.Unlock()
	if lr.outputClosed {
		return 0
	}
	seq := lr.ring.append(stream, data, at)
	if seq != 0 && lr.recordIO {
		if err := m.rt.Recorder.Record(context.Background(), lr.tenant, lr.runRef, RecordedFrame{Seq: seq, Stream: stream, Data: data, At: at}); err != nil {
			m.failIOEvidence(lr, "session I/O evidence recording failed")
		}
	}
	return seq
}

// Stop outside the output loop so process teardown can drain its output. Retain
// a classified reason even when a concurrent operator stop already owns teardown.
// Recorder error text may contain secrets and never reaches the run or its log.
func (m *Module) failIOEvidence(lr *liveRun, reason string) {
	lr.mu.Lock()
	if lr.ioEvidenceFailure != "" {
		lr.mu.Unlock()
		return
	}
	lr.ioEvidenceFailure = reason
	lr.mu.Unlock()
	m.warnf("sessions: I/O evidence failed", "run_ref", lr.runRef, "reason", reason)
	if !lr.outputClosed {
		m.terminateForRuntimeAccessFailure(lr, reason, accessStopNone)
	}
}

// Publication completes before the codec can send its refusal to the child.
// Only classified errors reach followers; the error text can contain secrets.
func (m *Module) publishApprovalFailure(lr *liveRun, err error) {
	cause := errorCause(err, "unavailable")
	reason := "the approval service is unavailable"
	switch cause {
	case "access_ended":
		reason = "this session's access has ended"
	case "credential_expired":
		reason = "this session's credential has expired"
	case "deadline_exceeded":
		reason = "the approval check timed out"
	case "canceled":
		reason = "the approval check was canceled"
	}
	data, _ := json.Marshal(struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Cause   string `json:"cause"`
		Message string `json:"message"`
	}{"olivares_notice", "approval_refused", cause, "Olivares refused the tool request because " + reason + "."})
	m.publishRuntimeOutput(lr, streamStdout, lr.redact.apply(data), m.now())
}
