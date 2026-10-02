// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// claudeInterruptWait bounds how long an interrupt waits for Claude Code to
// confirm it. A variable so tests stay fast.
var claudeInterruptWait = 10 * time.Second

// interruptClaudeTurn cancels the turn Claude Code is running and leaves the
// session live, the way the Agent SDK does it: a control request with subtype
// "interrupt" on the child's stream-json stdin, answered by a control response
// that carries the same request id on stdout. It is not a stop and never becomes
// one. Like DriverSession.Interrupt, the bool reports whether the request may have
// reached the child: false only when nothing was written.
func (m *Module) interruptClaudeTurn(ctx context.Context, lr *liveRun) (bool, error) {
	if err := m.assertRunAuthority(ctx, lr); err != nil {
		return false, err
	}
	callErr := m.endSessionCalls(lr, "")
	id := "olv-interrupt-" + string(model.NewID())
	line, err := json.Marshal(map[string]any{
		"type":       "control_request",
		"request_id": id,
		"request":    map[string]any{"subtype": "interrupt"},
	})
	if err != nil {
		return false, err
	}
	cursor := lr.ring.cursorNow()
	if err := lr.proc.Send(ctx, line); err != nil {
		m.warnf("session interrupt not delivered", "run_ref", lr.runRef)
		return true, &runErr{http.StatusBadGateway, "the interrupt could not be written to the session; it may still be running its turn"}
	}
	return true, errors.Join(callErr, m.awaitClaudeControlResponse(ctx, lr.ring, cursor, id))
}

// awaitClaudeControlResponse reads the session's output from cursor until Claude
// Code answers request id: success is nil, an error answer says what Claude said.
func (m *Module) awaitClaudeControlResponse(ctx context.Context, ring *outputRing, cursor int64, id string) error {
	timer := time.NewTimer(claudeInterruptWait)
	defer timer.Stop()
	for {
		wake := ring.wait() // snapshot BEFORE reading: no lost wake-up
		rd := ring.readFrom(cursor)
		for _, f := range rd.frames {
			if f.Stream != streamStdout || !bytes.Contains(f.Data, []byte(id)) {
				continue
			}
			var resp struct {
				Type     string `json:"type"`
				Response struct {
					Subtype   string `json:"subtype"`
					RequestID string `json:"request_id"`
					Error     string `json:"error"`
				} `json:"response"`
			}
			if json.Unmarshal(bytes.TrimSpace(f.Data), &resp) != nil || resp.Type != "control_response" || resp.Response.RequestID != id {
				continue
			}
			if resp.Response.Subtype == "success" {
				return nil
			}
			msg := "Claude Code refused the interrupt"
			if resp.Response.Error != "" {
				msg += ": " + clipCause(resp.Response.Error)
			}
			return &runErr{http.StatusConflict, msg}
		}
		cursor = rd.next
		if rd.closed {
			return &runErr{http.StatusConflict, "the session ended before Claude Code confirmed the interrupt"}
		}
		select {
		case <-wake:
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return &runErr{http.StatusGatewayTimeout, "Claude Code did not confirm the interrupt in time; the turn may still be running"}
		}
	}
}

// cursorNow is the sequence the next frame will get: a read from here sees only
// what the child writes after this instant.
func (r *outputRing) cursorNow() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.nextSeq
}
