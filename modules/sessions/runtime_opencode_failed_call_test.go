// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// #2937: when OpenCode's model call fails upstream (measured on 1.19.3 with a used-up
// free tier: "Free usage limit reached. Subscribe to OpenCode Go to keep going"), it
// logs the error and never ends the prompt: no ACP update and no result for 300 s, so
// the session showed "Working" forever. The turn now ends, and the session says why in
// the tool's own words.
func TestOpenCodeRuntimeEndsATurnWhoseModelCallFailed(t *testing.T) {
	const vendor = "Free usage limit reached. Subscribe to OpenCode Go to keep going: https://opencode.ai/go"
	shortFailedCallGrace(t, 300*time.Millisecond)
	m, _, tenant, prof := openCodeHarness(t, AuthSourceAccountHome)
	record := setOpenCodeFixture(t, prof, openCodeFixture{SessionID: "ses-failed-call", FailCall: vendor})
	ctx := context.Background()
	dto, err := openCodeLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	t.Cleanup(func() { _, _ = m.stopRun(context.Background(), tenant, dto.RunRef, "user:u1", model.ActorUser) })
	if err := m.sendTextInput(ctx, tenant, dto.RunRef, "go"); err != nil {
		t.Fatalf("input: %v", err)
	}
	waitFor(t, "the failed turn cancelled at the tool", func() bool {
		return slices.Contains(readOpenCodeFixtureRecord(t, record).Methods, acpMethodSessionCancel)
	})
	lr, ok := m.rt.getLive(tenant, dto.RunRef)
	if !ok {
		t.Fatal("the run is not live")
	}
	waitFor(t, "the turn ended", func() bool { return lr.session.ActiveTurn() == "" })
	notices, prompt := 0, -1
	for i, f := range lr.ring.readFrom(0).frames {
		if strings.Contains(string(f.Data), "level=ERROR") {
			t.Fatalf("the tool's raw log line was published: %s", f.Data)
		}
		if text, ok := acpUpdateText(f.Data, "user_message_chunk"); ok && text == "go" {
			prompt = i
		}
		var n struct{ Type, Code, Message string }
		if json.Unmarshal(f.Data, &n) != nil || n.Type != "olivares_notice" {
			continue
		}
		if prompt < 0 {
			t.Fatal("the notice came before the person's message it is about")
		}
		notices++
		if n.Code != "provider_call_failed" || !strings.Contains(n.Message, vendor) || strings.Contains(n.Message, "Model access is disabled") {
			t.Fatalf("notice = %+v, want the conversation's own failure in the tool's words", n)
		}
	}
	if notices != 1 {
		t.Fatalf("%d notices, want one: the title helper's failure is not the turn's", notices)
	}
	if err := m.sendTextInput(ctx, tenant, dto.RunRef, "again"); err != nil {
		t.Fatalf("the next message after the failed turn: %v", err)
	}
	peer := readOpenCodeFixtureRecord(t, record)
	if !slices.Contains(peer.Argv, "--print-logs") || !slices.Contains(peer.Argv, "ERROR") {
		t.Fatalf("argv = %v, want the tool's own --print-logs --log-level ERROR", peer.Argv)
	}
}

// A turn whose model call failed but that goes on (OpenCode retried and answered) is not
// stopped and gets no notice.
func TestOpenCodeRuntimeLeavesATurnThatRecoversFromAFailedCall(t *testing.T) {
	shortFailedCallGrace(t, time.Second)
	m, _, tenant, prof := openCodeHarness(t, AuthSourceAccountHome)
	record := setOpenCodeFixture(t, prof, openCodeFixture{SessionID: "ses-retried", FailCall: "Overloaded", FailCallRecovers: true})
	ctx := context.Background()
	dto, err := openCodeLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	t.Cleanup(func() { _, _ = m.stopRun(context.Background(), tenant, dto.RunRef, "user:u1", model.ActorUser) })
	if err := m.sendTextInput(ctx, tenant, dto.RunRef, "go"); err != nil {
		t.Fatalf("input: %v", err)
	}
	lr, ok := m.rt.getLive(tenant, dto.RunRef)
	if !ok {
		t.Fatal("the run is not live")
	}
	waitFor(t, "the answered turn", func() bool { return countACPUpdates(lr, "agent_message_chunk", "after a retry") == 1 })
	time.Sleep(openCodeFailedCallGrace + 500*time.Millisecond)
	if slices.Contains(readOpenCodeFixtureRecord(t, record).Methods, acpMethodSessionCancel) {
		t.Fatal("a turn that went on was cancelled")
	}
	for _, f := range lr.ring.readFrom(0).frames {
		if strings.Contains(string(f.Data), "olivares_notice") {
			t.Fatalf("a turn that went on got a failure notice: %s", f.Data)
		}
	}
}

func shortFailedCallGrace(t *testing.T, grace time.Duration) {
	prev := openCodeFailedCallGrace
	openCodeFailedCallGrace = grace
	t.Cleanup(func() { openCodeFailedCallGrace = prev })
}

// A retry that fails again is waited for afresh, and that failure stops the turn.
func TestOpenCodeRuntimeStopsATurnWhoseRetryFailsAgain(t *testing.T) {
	shortFailedCallGrace(t, time.Second)
	m, _, tenant, prof := openCodeHarness(t, AuthSourceAccountHome)
	record := setOpenCodeFixture(t, prof, openCodeFixture{SessionID: "ses-failed-twice", FailCall: "Overloaded",
		FailCallRecovers: true, FailCallThen: "Free usage limit reached."})
	ctx := context.Background()
	dto, err := openCodeLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	t.Cleanup(func() { _, _ = m.stopRun(context.Background(), tenant, dto.RunRef, "user:u1", model.ActorUser) })
	if err := m.sendTextInput(ctx, tenant, dto.RunRef, "go"); err != nil {
		t.Fatalf("input: %v", err)
	}
	waitFor(t, "the turn cancelled after its second failure", func() bool {
		return slices.Contains(readOpenCodeFixtureRecord(t, record).Methods, acpMethodSessionCancel)
	})
	lr, ok := m.rt.getLive(tenant, dto.RunRef)
	if !ok {
		t.Fatal("the run is not live")
	}
	waitFor(t, "the second failure's notice", func() bool {
		for _, f := range lr.ring.readFrom(0).frames {
			if strings.Contains(string(f.Data), "olivares_notice") && strings.Contains(string(f.Data), "Free usage limit reached.") &&
				strings.Contains(string(f.Data), "Olivares cancelled the turn") {
				return true
			}
		}
		return false
	})
}
