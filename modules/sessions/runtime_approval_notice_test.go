// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

type noticeApprovalGate struct{ err error }

func (g noticeApprovalGate) Approve(context.Context, model.TenantID, ProviderApprovalRequest) (ProviderApprovalDecision, error) {
	return ProviderApprovalDecision{}, g.err
}

func TestRuntimeApprovalFailureNoticePrecedesInterruptionOnReplay(t *testing.T) {
	for _, tc := range []struct {
		cause string
		err   error
	}{
		{"access_ended", auth.ErrSessionAccessEnded},
		{"credential_expired", auth.ErrSessionCredentialExpired},
		{"deadline_exceeded", context.DeadlineExceeded},
		{"canceled", context.Canceled},
		{"unavailable", errors.New("store unavailable")},
		{"normal_denial", nil},
	} {
		t.Run(tc.cause, func(t *testing.T) {
			var failure error
			if tc.err != nil {
				failure = fmt.Errorf("private-path secret-value: %w", tc.err)
			}
			rec := &countingRecorder{}
			m, _, tenant, prof := codexHarness(t, AuthSourceAccountHome, WithProviderApprovalGate(noticeApprovalGate{failure}), WithLaunchGate(&spyGate{inner: LaunchDecision{Allowed: true, RecordIO: true}}))
			WithRecorder(rec)(m)
			path := setCodexFixture(t, prof, codexFixture{ThreadID: "notice-thread", Account: "apikey", ApprovalOnTurn: codexReqCommandApproval, CompleteApprovalTurn: true})
			dto, err := codexLaunch(t, m, tenant, prof)
			if err != nil {
				t.Fatal(err)
			}
			lr, _ := m.rt.getLive(tenant, dto.RunRef)
			reapOwnedChild(t, lr)
			if err := m.sendTextInput(context.Background(), tenant, dto.RunRef, "run command"); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "refusal reaches child", func() bool { return len(readFixtureRecord(t, path).Replies) > 0 })
			if reply := string(readFixtureRecord(t, path).Replies[0]); !strings.Contains(reply, `"decline"`) {
				t.Fatalf("refusal changed: %s", reply)
			}
			identity, ok := dto.Completion.Identity()
			if !ok {
				t.Fatal("no launch identity")
			}
			for replay := 0; replay < 2; replay++ {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				done := errors.New("observed interruption")
				var noticeSeq int64
				err := m.AttachRuntimeOutput(ctx, identity, "user:u1", model.ActorUser, "follow refusal", 0, func(ev RuntimeOutputEvent) error {
					if strings.Contains(ev.Line, "private-path") || strings.Contains(ev.Line, "secret-value") {
						t.Error("raw authority error leaked")
					}
					var frame struct{ Type, Code, Cause, Message, Method string }
					_ = json.Unmarshal([]byte(ev.Line), &frame)
					if frame.Type == "olivares_notice" {
						if noticeSeq != 0 || frame.Code != "approval_refused" || frame.Cause != tc.cause || !strings.HasPrefix(frame.Message, "Olivares refused the tool request") {
							t.Errorf("wrong notice: %s", ev.Line)
						}
						noticeSeq = ev.Seq
					}
					if frame.Method == codexNotifyTurnCompleted {
						if tc.err != nil && (noticeSeq == 0 || noticeSeq >= ev.Seq) {
							t.Errorf("interruption without preceding notice: notice=%d terminal=%d", noticeSeq, ev.Seq)
						}
						if tc.err == nil && noticeSeq != 0 {
							t.Error("normal denial emitted authority failure")
						}
						return done
					}
					return nil
				})
				cancel()
				if !errors.Is(err, done) {
					t.Fatalf("replay: %v", err)
				}
			}
			_, _ = m.stopRun(context.Background(), tenant, dto.RunRef, "user:u1", model.ActorUser)
			<-lr.finalizedCh
			seqs, finalized := rec.snapshot()
			if finalized != 1 {
				t.Errorf("recorder finalize=%d", finalized)
			}
			for i, seq := range seqs {
				if i > 0 && seq <= seqs[i-1] {
					t.Fatalf("recorder out of order: %v", seqs)
				}
			}
			if len(seqs) != len(lr.ring.readFrom(0).frames) {
				t.Fatal("ring and recording differ")
			}
		})
	}
}
