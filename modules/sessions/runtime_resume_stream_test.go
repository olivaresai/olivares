// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

func TestAttachHTTPResumeKeepsTheRunCursorAcrossProcessGenerations(t *testing.T) {
	for _, reap := range []bool{false, true} {
		name := "retained_tail"
		if reap {
			name = "reaped_tail"
		}
		t.Run(name, func(t *testing.T) {
			h, runner, admin, tenant := attachHTTPHarness(t)
			ref := launchHTTPRun(t, h, admin, tenant, map[string]any{"transport": "stream-json", "name": "resume-stream"})
			base := "/v1/m/sessions/runs/" + ref
			emit := func(text string) {
				runner.lastProc().out <- OutputFrame{Stream: streamStdout, Data: []byte(text)}
			}
			stop := func() {
				t.Helper()
				if got := h.doJSON(http.MethodPost, base+"/stop", admin, nil, tenantHdr(tenant)); got.code != http.StatusOK {
					t.Fatalf("stop=%d %s", got.code, got.raw)
				}
			}
			attach := func(cursor int64) []attachFrame {
				t.Helper()
				got := h.do(http.MethodGet, fmt.Sprintf("%s/attach?from=%d", base, cursor), admin, tenantHdr(tenant))
				if got.code != http.StatusOK {
					t.Fatalf("attach=%d %s", got.code, got.raw)
				}
				var frames []attachFrame
				ended := false
				for _, event := range parseSSE(got.raw) {
					if event.Event == "output" {
						var frame attachFrame
						if err := json.Unmarshal([]byte(event.Data), &frame); err != nil {
							t.Fatal(err)
						}
						frames = append(frames, frame)
					}
					if event.Event == "end" {
						ended = true
					}
				}
				if !ended {
					t.Fatalf("closed attach did not end: %s", got.raw)
				}
				return frames
			}
			for i := 0; i < 3; i++ {
				emit(fmt.Sprintf(`{"type":"assistant","text":"before-%d"}`, i))
			}
			stop()
			before := attach(0)
			if len(before) != 3 {
				t.Fatalf("initial attach returned %d frames, want3", len(before))
			}
			cursor := before[len(before)-1].Seq + 1
			for generation := 1; generation <= 2; generation++ {
				if reap {
					// Execute the existing closed-tail reclamation, without sleeping
					// five minutes. Resume must not depend on retaining old output.
					old, ok := h.m.rt.getLive(tenant, ref)
					if !ok || !h.m.rt.dropLiveIf(old) {
						t.Fatal("could not reclaim the closed tail")
					}
				}
				if got := h.doJSON(http.MethodPost, base+"/resume", admin, nil, tenantHdr(tenant)); got.code != http.StatusOK {
					t.Fatalf("resume=%d %s", got.code, got.raw)
				}
				answer := fmt.Sprintf(`{"type":"assistant","text":"after-resume-%d"}`, generation)
				emit(answer)
				stop()
				fresh := attach(0)
				if len(fresh) != 1 || fresh[0].Line != answer {
					t.Fatalf("fresh attach after resume=%+v, want only the new answer", fresh)
				}
				next := attach(cursor)
				if len(next) != 1 || next[0].Line != answer || next[0].Seq < cursor {
					t.Fatalf("pre-resume cursor %d hid the new answer: got%+v; fresh attach received seq%d %q", cursor, next, fresh[0].Seq, fresh[0].Line)
				}
				if strings.Contains(next[0].Line, "before-") {
					t.Fatal("resume replayed old-generation output")
				}
				cursor = next[0].Seq + 1
			}
		})
	}
}

func TestResumedOutputSequenceHasSafeBoundsAndHonestGaps(t *testing.T) {
	t.Run("generation_gap_is_not_loss", func(t *testing.T) {
		r, err := newResumedOutputRing(2, 1024, 0)
		if err != nil {
			t.Fatal(err)
		}
		if seq := r.append(streamStdout, []byte("answer"), time.Now()); seq != 4294967297 {
			t.Fatalf("historical event zero reused an old sequence: %d", seq)
		}
		rd := r.readFrom(4)
		if rd.gap || rd.dropped != 0 || len(rd.frames) != 1 {
			t.Fatalf("unused generation range reported loss: %+v", rd)
		}
		r.append(streamStdout, []byte("second"), time.Now())
		r.append(streamStdout, []byte("third"), time.Now())
		rd = r.readFrom(4)
		if !rd.gap || rd.dropped != 1 || len(rd.frames) != 2 {
			t.Fatalf("real eviction must report exactly one lost frame: %+v", rd)
		}
		if fresh := r.readFrom(0); fresh.gap || len(fresh.frames) != 2 {
			t.Fatalf("fresh attach must still start at the retained tail: %+v", fresh)
		}
	})
	t.Run("javascript_exact_limit", func(t *testing.T) {
		r, err := newResumedOutputRing(2, 1024, 2097149)
		if err != nil {
			t.Fatal(err)
		}
		// Exercise the last frame without emitting billions of frames. These
		// literals are the protocol's exact integer ceiling, not a float cast.
		r.nextSeq = 9007194959773695
		if seq := r.append(streamStdout, []byte("last"), time.Now()); seq != 9007194959773695 {
			t.Fatalf("last exact frame sequence: %d", seq)
		}
		wake := r.wait()
		if seq := r.append(streamStdout, []byte("overflow"), time.Now()); seq != 0 {
			t.Fatalf("published a sequence outside this generation: %d", seq)
		}
		select {
		case <-wake:
		default:
			t.Fatal("range exhaustion did not wake attach")
		}
		rd := r.readFrom(0)
		if !rd.closed || len(rd.frames) != 1 || rd.next != 9007194959773696 {
			t.Fatalf("unsafe cursor or overflow frame emitted: %+v", rd)
		}
		for _, eventSeq := range []int64{-1, 2097150, 9223372036854775807} {
			if _, err := newResumedOutputRing(2, 1024, eventSeq); err == nil {
				t.Fatalf("unsafe generation admitted: %d", eventSeq)
			}
		}
	})
}

func TestOutputSequenceExhaustionStopsTheOwnedProcess(t *testing.T) {
	h, runner, admin, tenant := attachHTTPHarness(t)
	ref := launchHTTPRun(t, h, admin, tenant, map[string]any{"transport": "stream-json", "name": "sequence-limit"})
	lr, ok := h.m.rt.getLive(tenant, ref)
	if !ok {
		t.Fatal("launch has no supervised output")
	}
	lr.ring.mu.Lock()
	lr.ring.nextSeq = lr.ring.limitSeq - 1
	lr.ring.mu.Unlock()
	proc := runner.lastProc()
	proc.out <- OutputFrame{Stream: streamStdout, Data: []byte("last safe frame")}
	proc.out <- OutputFrame{Stream: streamStdout, Data: []byte("must never appear")}
	select {
	case <-lr.finalizedCh:
	case <-time.After(3 * time.Second):
		t.Fatal("sequence exhaustion left the supervised process running")
	}
	got := h.do(http.MethodGet, "/v1/m/sessions/runs/"+ref, admin, tenantHdr(tenant))
	if got.code != http.StatusOK || !strings.Contains(got.raw, `"state":"failed"`) {
		t.Fatalf("range exhaustion was not recorded as failed: %d %s", got.code, got.raw)
	}
	got = h.do(http.MethodGet, "/v1/m/sessions/runs/"+ref+"/attach?from=0", admin, tenantHdr(tenant))
	if got.code != http.StatusOK || !strings.Contains(got.raw, "last safe frame") || strings.Contains(got.raw, "must never appear") {
		t.Fatalf("attach published overflow or lost the safe tail: %d %s", got.code, got.raw)
	}
}

func TestOutputSequenceExhaustionWithdrawsAuthorityAfterUnconfirmedStop(t *testing.T) {
	probe := &dualCredentialProbe{}
	proc := &retryStopProcess{
		fakeProc: &fakeProc{out: make(chan OutputFrame, 2), stopped: make(chan struct{})},
		log:      probe,
	}
	m, _, tenant, clk := newRuntimeHarness(t, WithRunner(&fixedProcessRunner{proc: proc}), WithCredentialSource(staticCred()))
	probe.now = clk.get
	wireDualCredentialProbe(m, probe)
	created, err := createProfiledTestRun(t, m, context.Background(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "agent:sequence-limit", ActorKind: model.ActorAgent, AgentRef: "agent:sequence-limit",
	})
	if err != nil {
		t.Fatal(err)
	}
	lr, ok := m.rt.getLive(tenant, created.RunRef)
	if !ok {
		t.Fatal("launch has no supervised output")
	}
	lr.ring.mu.Lock()
	lr.ring.nextSeq = lr.ring.limitSeq
	lr.ring.mu.Unlock()
	proc.out <- OutputFrame{Stream: streamStdout, Data: []byte("overflow")}
	waitFor(t, "durable authority withdrawn after unconfirmed sequence-limit stop", func() bool {
		calls := probe.snapshot()
		if calls.workRevoke == 0 || calls.commRevoke == 0 {
			return false
		}
		rec, err := m.loadRun(context.Background(), tenant, created.RunRef)
		return err == nil && rec.String(colWorkCredentialID) == "" && rec.String(colCommunicationCredentialID) == ""
	})
	lr.mu.Lock()
	stopping, heartbeatStopped := lr.stopRequested, lr.credentialHeartbeatCancel == nil
	lr.mu.Unlock()
	if !stopping || !heartbeatStopped {
		t.Fatal("unconfirmed stop left a live authority heartbeat")
	}
	before := probe.snapshot()
	m.renewDualRuntimeCredentials(context.Background(), lr)
	after := probe.snapshot()
	if after.workRenew != before.workRenew || after.commRenew != before.commRenew {
		t.Fatal("sequence-limit stop renewed a revoked credential")
	}
	select {
	case <-lr.finalizedCh:
		t.Fatal("unconfirmed stop fabricated a process exit")
	default:
	}
	if _, err := m.resumeRun(context.Background(), tenant, created.RunRef, "agent:sequence-limit", model.ActorAgent, "agent:sequence-limit"); !isRunConflict(err) {
		t.Fatalf("uncollected process admitted a successor: %v", err)
	}
	if _, err := m.stopRun(context.Background(), tenant, created.RunRef, "test", model.ActorUser); err != nil {
		t.Fatalf("later stop could not collect the retained process: %v", err)
	}
}
