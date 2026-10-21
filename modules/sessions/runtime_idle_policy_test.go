// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func idlePolicyFixture(t *testing.T) (*moveFixture, *testClock, *liveRun) {
	t.Helper()
	clk := &testClock{now: time.Now().UTC()}
	f := newMoveFixture(t, WithClock(clk))
	lr, _ := f.m.rt.getLive(f.tenant, f.runRef)
	f.m.SetIdleAutoStopPolicy(func(context.Context) (time.Duration, error) { return 6 * time.Minute, nil })
	f.fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: []byte(`{"type":"result","subtype":"success"}`)}
	waitFor(t, "completed turn", lr.waitingForInput)
	for range 3 {
		clk.advance(4 * time.Minute)
		if _, err := f.m.Heartbeat(t.Context(), f.tenant, lr.claim.SID, lr.claim.Holder, lr.claim.Fence, 0); err != nil {
			t.Fatal(err)
		}
	}
	return f, clk, lr
}

func TestIdlePolicyStopsAndResumesTheSameConversation(t *testing.T) {
	f, _, _ := idlePolicyFixture(t)
	before, _ := f.m.getRun(t.Context(), f.tenant, f.runRef)
	// Meta is nil on every audit read path, so the cause is observed as appended.
	var drafts []model.AuditDraft
	original := f.m.Data
	f.m.Data = idlePolicyAuditFaultData{ModuleData: original, seen: &drafts}
	f.m.sweepIdleAutoStop(t.Context())
	f.m.Data = original
	after, err := f.m.getRun(t.Context(), f.tenant, f.runRef)
	if err != nil || after.State != stateStopped || after.Reason != idlePolicyReason {
		t.Fatalf("idle policy result: state=%q reason=%q err=%v", after.State, after.Reason, err)
	}
	audited := false
	for _, event := range f.events(t) {
		audited = audited || event["event"] == "stopping" && event["actor_kind"] == model.ActorSystem && strings.Contains(event["detail"].(string), "idle policy")
	}
	if !audited {
		t.Fatal("no system-attributed idle-policy stop in ledger")
	}
	policyStops := 0
	for _, draft := range drafts {
		if draft.Action == "sessions.run.stopping" && draft.ActorKind == model.ActorSystem && draft.Meta["stop_cause"] == idlePolicyReason {
			policyStops++
		}
	}
	if policyStops != 1 {
		t.Fatalf("audit drafts carrying stop_cause=%s: %d, want 1", idlePolicyReason, policyStops)
	}
	if got := f.post(t, "resume", nil); got.code != 200 {
		t.Fatalf("resume: %d %s", got.code, got.raw)
	}
	resumed, _ := f.m.getRun(t.Context(), f.tenant, f.runRef)
	if resumed.WorkspacePath != before.WorkspacePath || resumed.ClaudeSessionID != before.ClaudeSessionID {
		t.Fatalf("resume lost folder or conversation: before=%+v after=%+v", before, resumed)
	}
	if sid, found := resumeFlagValue(f.fr.lastSpec()); !found || sid != moveTestSID {
		t.Fatalf("resume conversation=%q found=%v", sid, found)
	}
	if data, err := os.ReadFile(filepath.Join(f.homeA, "projects", moveTestProject, moveTestSID+".jsonl")); err != nil || !strings.Contains(string(data), "BLUE-HERON") {
		t.Fatalf("history lost: %v", err)
	}
	// A person stops the resumed session: it must not still read as stopped to free memory.
	if resumed.Reason != "" {
		t.Fatalf("resumed session kept the policy reason %q", resumed.Reason)
	}
	f.stop(t)
	byPerson, _ := f.m.getRun(t.Context(), f.tenant, f.runRef)
	if byPerson.State != stateStopped || byPerson.Reason == idlePolicyReason {
		t.Fatalf("a person's stop after a policy stop: state=%q reason=%q", byPerson.State, byPerson.Reason)
	}
}

func TestIdlePolicyIdlePeersDoNotHoldEachOtherOpen(t *testing.T) {
	f, clk, _ := idlePolicyFixture(t)
	peer := f.h.doJSON("POST", "/v1/m/sessions/runs", f.admin, map[string]any{"provider_profile_ref": f.b}, f.hdr)
	if peer.code != 201 {
		t.Fatal(peer.raw)
	}
	other, _ := f.m.loadRun(t.Context(), f.tenant, peer.body["run_ref"].(string))
	setRunColumn(t, f, colRunPeers, `["`+other.String(colRunClaimSID)+`"]`)
	// While the peer is recent it protects this session.
	f.m.sweepIdleAutoStop(t.Context())
	if now, _ := f.m.getRun(t.Context(), f.tenant, f.runRef); now.State == stateStopped {
		t.Fatalf("a recently active peer did not hold the session: %q", now.State)
	}
	// Once the peer has been silent for the projection window it no longer does,
	// and the recheck delay has passed.
	clk.advance(6 * time.Minute)
	f.m.sweepIdleAutoStop(t.Context())
	if after, _ := f.m.getRun(t.Context(), f.tenant, f.runRef); after.State != stateStopped || after.Reason != idlePolicyReason {
		t.Fatalf("an idle peer held the session: state=%q reason=%q", after.State, after.Reason)
	}
}

// A linked peer in the middle of a turn protects the session even when it has
// been silent past the projection window (a long foreground tool call). The same
// peer, once its turn ended, no longer does.
func TestIdlePolicyPeerMidTurnStaysProtectiveWhileSilent(t *testing.T) {
	f, clk, _ := idlePolicyFixture(t)
	peer := f.h.doJSON("POST", "/v1/m/sessions/runs", f.admin, map[string]any{"provider_profile_ref": f.b}, f.hdr)
	if peer.code != 201 {
		t.Fatal(peer.raw)
	}
	peerRef := peer.body["run_ref"].(string)
	other, _ := f.m.loadRun(t.Context(), f.tenant, peerRef)
	setRunColumn(t, f, colRunPeers, `["`+other.String(colRunClaimSID)+`"]`)
	peerLive, ok := f.m.rt.getLive(f.tenant, peerRef)
	if !ok {
		t.Fatal("peer is not live")
	}
	// One finished turn, then a second input whose turn has not ended.
	peerLive.mu.Lock()
	peerLive.claudeTurnsEnded, peerLive.claudePendingTurns = 1, 1
	peerLive.mu.Unlock()
	clk.advance(6 * time.Minute)
	f.m.sweepIdleAutoStop(t.Context())
	if now, _ := f.m.getRun(t.Context(), f.tenant, f.runRef); now.State == stateStopped {
		t.Fatalf("a peer with a turn in flight did not hold the session: state=%q reason=%q", now.State, now.Reason)
	}
	// The peer's turn ends: it is finished and silent, so it no longer protects.
	peerLive.mu.Lock()
	peerLive.claudePendingTurns = 0
	peerLive.mu.Unlock()
	clk.advance(idlePolicyRecheck)
	f.m.sweepIdleAutoStop(t.Context())
	if after, _ := f.m.getRun(t.Context(), f.tenant, f.runRef); after.State != stateStopped || after.Reason != idlePolicyReason {
		t.Fatalf("a finished silent peer held the session: state=%q reason=%q", after.State, after.Reason)
	}
}

// What counts as work in flight for a linked peer, independent of how long it has
// been silent.
func TestIdlePolicyWorkInFlight(t *testing.T) {
	now := time.Now().UTC()
	window := 5 * time.Minute
	for _, tc := range []struct {
		name string
		run  *liveRun
		want bool
	}{
		{"claude never prompted", &liveRun{}, false},
		{"claude turn finished", &liveRun{claudeTurnsEnded: 1}, false},
		{"claude input accepted, no result yet", &liveRun{claudeTurnsEnded: 1, claudePendingTurns: 1}, true},
		{"claude output since the last result", &liveRun{claudeTurnsEnded: 1, claudeOutputOpen: true}, true},
		{"claude interrupted", &liveRun{claudeTurnsEnded: 1, claudePendingTurns: 1, turnInterrupted: true}, false},
		{"claude interrupted with approval open", &liveRun{turnInterrupted: true, pendingApprovals: []*runApprovalWait{{}}}, true},
		{"session tool call open", &liveRun{claudeTurnsEnded: 1, sessionCalls: []*runtimeSessionCall{{}}}, true},
		{"driver turn named", &liveRun{session: &fakeTurnSession{turn: "t1"}}, true},
		{"driver turn ended", &liveRun{session: &fakeTurnSession{}, driverTurnEnded: true, lastIOAt: now.Add(-time.Minute)}, false},
		{"driver input accepted, turn not named, inside window", &liveRun{session: &fakeTurnSession{}, lastIOAt: now.Add(-time.Minute)}, true},
		{"driver input accepted, turn not named, past window", &liveRun{session: &fakeTurnSession{}, lastIOAt: now.Add(-6 * time.Minute)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.run.workInFlightLocked(now, window); got != tc.want {
				t.Fatalf("workInFlightLocked=%v, want %v", got, tc.want)
			}
		})
	}
}

// A finished, silent session that is bound to work is refused after its transcript
// is read; it must then wait out the recheck delay instead of being read again on
// every 30 s sweep.
func TestIdlePolicyWorkBoundSessionBacksOff(t *testing.T) {
	f, clk, lr := idlePolicyFixture(t)
	setRunColumn(t, f, colRunWorkItemID, model.NewID().String())
	f.m.sweepIdleAutoStop(t.Context())
	lr.mu.Lock()
	recheck := lr.idleRecheckAt
	lr.mu.Unlock()
	if want := clk.get().Add(idlePolicyRecheck); !recheck.Equal(want) {
		t.Fatalf("recheck at %v, want %v: a work-bound session would be read on every sweep", recheck, want)
	}
	if now, _ := f.m.getRun(t.Context(), f.tenant, f.runRef); now.State == stateStopped {
		t.Fatalf("work-bound session stopped by the policy: reason=%q", now.Reason)
	}
}

// setPeerColumn writes one column of another run in one transaction. The peer
// starts on its own goroutine and writes its record while it does; a record loaded
// before and written back whole would lose that race with a version conflict.
func setPeerColumn(t *testing.T, f *moveFixture, ref, col string, value any) {
	t.Helper()
	ctx := t.Context()
	if err := f.m.Data.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rec, err := findRunRec(ctx, repo, ref)
		if err != nil {
			return err
		}
		rec[col] = value
		_, err = repo.Update(ctx, rec)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIdlePolicyBlockRules(t *testing.T) {
	for _, name := range []string{"unset", "too soon", "unfinished", "approval", "open", "child", "background task", "peer", "new message", "unreadable policy", "missing transcript", "partial transcript", "unreadable transcript", "work-managed", "scheduled", "default zero", "child background", "outgoing peer", "incoming peer", "pending call", "codex child background", "window equals projection"} {
		t.Run(name, func(t *testing.T) {
			f, clk, lr := idlePolicyFixture(t)
			switch name {
			case "default zero":
				f.m.SetIdleAutoStopPolicy(func(context.Context) (time.Duration, error) { return 0, nil })
			case "pending call":
				lr.mu.Lock()
				lr.sessionCalls = append(lr.sessionCalls, &runtimeSessionCall{cancel: func() {}, done: make(chan struct{})})
				lr.mu.Unlock()
				defer func() { lr.mu.Lock(); lr.sessionCalls = nil; lr.mu.Unlock() }()
			case "unset":
				f.m.SetIdleAutoStopPolicy(nil)
			case "window equals projection":
				// The setting must exceed the five-minute projection; an equal value
				// stops nothing even though the session is far past it.
				f.m.SetIdleAutoStopPolicy(func(context.Context) (time.Duration, error) { return f.m.rt.idleWindow, nil })
			case "too soon":
				clk.advance(-2*time.Minute - time.Second)
			case "unfinished":
				lr.mu.Lock()
				lr.claudePendingTurns++
				lr.mu.Unlock()
			case "approval":
				lr.mu.Lock()
				lr.pendingApprovals = append(lr.pendingApprovals, &runApprovalWait{})
				lr.mu.Unlock()
			case "open":
				_, release := f.m.holdRunOpen(f.tenant, f.runRef)
				defer release()
			case "child", "background task":
				tool := "Agent"
				if name == "background task" {
					tool = "Bash"
				}
				writeFile(t, filepath.Join(f.homeA, "projects", moveTestProject, moveTestSID+".jsonl"), `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"child1","name":"`+tool+`","input":{"description":"work","command":"sleep 999","run_in_background":true}}]}}`+"\n")
			case "peer", "outgoing peer", "incoming peer":
				peer := f.h.doJSON("POST", "/v1/m/sessions/runs", f.admin, map[string]any{"provider_profile_ref": f.b}, f.hdr)
				if peer.code != 201 {
					t.Fatal(peer.raw)
				}
				peerRef := peer.body["run_ref"].(string)
				other, _ := f.m.loadRun(t.Context(), f.tenant, peerRef)
				switch name {
				case "peer":
					setRunColumn(t, f, colTemplateID, "idle-template")
					setRunColumn(t, f, colRunPeersRule, sameTemplatePeerRule)
					setPeerColumn(t, f, peerRef, colTemplateID, "idle-template")
				case "outgoing peer":
					setRunColumn(t, f, colRunPeers, `["`+other.String(colRunClaimSID)+`"]`)
				default:
					own, _ := f.m.loadRun(t.Context(), f.tenant, f.runRef)
					setPeerColumn(t, f, peerRef, colRunPeers, `["`+own.String(colRunClaimSID)+`"]`)
				}
			case "new message":
				// A raw provider message is accepted without opening a user turn. Its
				// bytes alone must reset reclamation even when the tool stays silent.
				if r := f.post(t, "input", map[string]any{"message": map[string]any{"type": "control_response", "response": map[string]any{"request_id": "idle-policy-probe", "subtype": "success"}}}); r.code != http.StatusAccepted {
					t.Fatal(r.raw)
				}
				if f.fr.lastProc().sentCount() != 1 {
					t.Fatal("message did not reach the stub tool")
				}

			case "unreadable policy":
				f.m.SetIdleAutoStopPolicy(func(context.Context) (time.Duration, error) { return 6 * time.Minute, errors.New("unavailable") })
			case "codex child background":
				home, user := t.TempDir(), t.TempDir()
				profile := mustCreateProfile(t, f.m, f.tenant, CreateProfileInput{Driver: "codex", ConfigHome: home, UserHome: user, AuthSource: AuthSourceAccountHome})
				for col, val := range map[string]any{colRunProfileID: profile.Ref, colRunProfileDriver: "codex", colRunProfileConfigHome: home, colRunProfileUserHome: user} {
					setRunColumn(t, f, col, val)
				}
				root := `{"type":"session_meta","payload":{"id":"` + moveTestSID + `","source":"cli"}}` + "\n" +
					`{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"CollabAgentToolCall","id":"spawn","tool":"spawn_agent","receiver_thread_ids":["child"],"agents_states":{"child":{"completed":"Done"}}}}}` + "\n"
				writeFile(t, filepath.Join(home, "sessions", "2026", "10", "14", "rollout-test-"+moveTestSID+".jsonl"), root)
				writeFile(t, filepath.Join(home, "sessions", "2026", "10", "14", "rollout-test-child.jsonl"), `{"type":"session_meta","payload":{"id":"child","source":{"subagent":{}}}}`+"\n"+`{"type":"event_msg","payload":{"type":"exec_command_begin","call_id":"exec","command":["sleep","999"],"process_id":"77"}}`+"\n")
				activity, err := f.m.runActivity(t.Context(), f.tenant, f.runRef)
				if err != nil || len(activity.Agents) != 1 || activity.Agents[0].State != activityCompleted || len(activity.Tasks) != 0 || activity.Unreadable != 0 {
					t.Fatalf("completed parent child fixture: %+v err=%v", activity, err)
				}
			case "child background":
				writeFile(t, filepath.Join(f.homeA, "projects", moveTestProject, moveTestSID, "subagents", "agent-a1.jsonl"), `{"type":"assistant","isSidechain":true,"message":{"content":[{"type":"tool_use","id":"child-bash","name":"Bash","input":{"command":"sleep 999","run_in_background":true}}]}}`+"\n")
			case "scheduled":
				writeFile(t, filepath.Join(f.homeA, "projects", moveTestProject, moveTestSID+".jsonl"), `{"type":"assistant","timestamp":"2026-10-14T06:00:00Z","message":{"content":[{"type":"tool_use","id":"wake1","name":"ScheduleWakeup","input":{"delaySeconds":3600,"reason":"check later"}}]}}`+"\n"+`{"type":"user","timestamp":"2026-10-14T06:00:01Z","message":{"content":[{"type":"tool_result","tool_use_id":"wake1","content":"scheduled"}]},"toolUseResult":{"id":"alarm1","wakeupAt":"2026-10-14T12:00:00Z"}}`+"\n")
			case "partial transcript":
				writeFile(t, filepath.Join(f.homeA, "projects", moveTestProject, moveTestSID+".jsonl"), `{"type":"assistant","message":`)
			case "unreadable transcript":
				writeFile(t, filepath.Join(f.homeA, "projects", moveTestProject, moveTestSID+".jsonl"), "broken-json\n")
			case "work-managed":
				setRunColumn(t, f, colRunWorkItemID, model.NewID().String())
			case "missing transcript":
				if err := os.Remove(filepath.Join(f.homeA, "projects", moveTestProject, moveTestSID+".jsonl")); err != nil {
					t.Fatal(err)
				}
			}
			f.m.sweepIdleAutoStop(t.Context())
			select {
			case <-f.fr.procs[0].stopped:
				t.Fatalf("policy stopped a session blocked by %s", name)
			default:
			}
		})
	}
}

func TestIdlePolicyHTTPViewsProtectUntilTheLastDisconnect(t *testing.T) {
	f, _, lr := idlePolicyFixture(t)
	server := httptest.NewServer(f.h.srv.Handler())
	defer server.Close()
	closeAttach := func(c attachConn) { c.cancel(); _ = c.res.Body.Close() }
	first, _ := openAttach(t, server, f.admin, f.tenant.String(), f.runRef, 0)
	defer closeAttach(first)
	second, _ := openAttach(t, server, f.admin, f.tenant.String(), f.runRef, 0)
	defer closeAttach(second)
	views := func() int { return int(lr.viewers.Load()) }
	if views() != 2 {
		t.Fatalf("HTTP views=%d", views())
	}
	f.m.sweepIdleAutoStop(t.Context())
	select {
	case <-f.fr.lastProc().stopped:
		t.Fatal("stopped with two HTTP views")
	default:
	}
	closeAttach(first)
	waitFor(t, "one HTTP view", func() bool { return views() == 1 })
	f.m.sweepIdleAutoStop(t.Context())
	select {
	case <-f.fr.lastProc().stopped:
		t.Fatal("stopped with one HTTP view")
	default:
	}
	closeAttach(second)
	waitFor(t, "last view disconnected", func() bool { return views() == 0 })
	f.m.sweepIdleAutoStop(t.Context())
	after, _ := f.m.getRun(t.Context(), f.tenant, f.runRef)
	if after.State != stateStopped {
		t.Fatalf("after last disconnect=%s", after.State)
	}
}

// A person can also follow a live run through AttachRuntimeOutput (the Business
// session cockpit does). That stream is a view like the HTTP attach: the policy
// must not stop the session under it, and must stop it once the stream ends.
func TestIdlePolicyRuntimeOutputStreamProtectsUntilItEnds(t *testing.T) {
	f, _, lr := idlePolicyFixture(t)
	row, err := f.m.loadRun(t.Context(), f.tenant, f.runRef)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := model.ParseID(row.String(colRunAuthzWorkspaceID))
	if err != nil {
		t.Fatal(err)
	}
	identity := RuntimeLaunchIdentity{Tenant: f.tenant, WorkspaceID: workspace, RunRef: f.runRef, RuntimeLaunchID: lr.launchID, SessionSID: lr.claim.SID}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	first := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- f.m.AttachRuntimeOutput(ctx, identity, "user:operator", model.ActorUser, "follow output", 0, func(ev RuntimeOutputEvent) error {
			if ev.Type == "output" {
				select {
				case first <- struct{}{}:
				default:
				}
			}
			return nil
		})
	}()
	select {
	case <-first:
	case err := <-done:
		t.Fatalf("stream ended before its first event: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("no first event on the output stream")
	}
	f.m.sweepIdleAutoStop(t.Context())
	if r, _ := f.m.getRun(t.Context(), f.tenant, f.runRef); r.State == stateStopped {
		t.Fatal("the idle policy stopped a session while a person streamed its output through AttachRuntimeOutput")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the output stream did not end after its context was canceled")
	}
	if lr.viewers.Load() != 0 {
		t.Fatalf("viewers=%d after the stream ended", lr.viewers.Load())
	}
	f.m.sweepIdleAutoStop(t.Context())
	if r, _ := f.m.getRun(t.Context(), f.tenant, f.runRef); r.State != stateStopped || r.Reason != idlePolicyReason {
		t.Fatalf("after the stream ended: state=%q reason=%q", r.State, r.Reason)
	}
}

// A preview URL is a person using the session's own app, and a dev server's
// live-reload stream outlives any sweep. A request in flight holds the run open,
// and the request's end restarts the window for a page that stays open but quiet.
func TestIdlePolicyPreviewRequestProtectsUntilItEnds(t *testing.T) {
	f, clk, lr := idlePolicyFixture(t)
	stopped := func() bool {
		t.Helper()
		r, err := f.m.getRun(t.Context(), f.tenant, f.runRef)
		if err != nil {
			t.Fatal(err)
		}
		return r.State == stateStopped
	}
	age := func() {
		for range 3 {
			clk.advance(4 * time.Minute)
			if _, err := f.m.Heartbeat(t.Context(), f.tenant, lr.claim.SID, lr.claim.Holder, lr.claim.Fence, 0); err != nil {
				t.Fatal(err)
			}
		}
	}
	sweepRunning := func(why string) {
		t.Helper()
		f.m.sweepIdleAutoStop(t.Context())
		if stopped() {
			t.Fatalf("the idle policy stopped a session %s", why)
		}
	}
	// Two requests are two holds: a short asset request ending must not release
	// the live-reload stream's. Each sweep below runs with a hold still open, so
	// the eligibility read is the only thing that keeps the session running.
	stream := holdPreviewRequest(t, f.m, lr, previewTTL)
	asset := holdPreviewRequest(t, f.m, lr, previewTTL)
	if got := lr.previewRequests.Load(); got != 2 {
		t.Fatalf("in-flight preview requests=%d, want 2", got)
	}
	age() // the page stays open past the whole window
	sweepRunning("while a person's preview requests were open")
	asset()
	if got := lr.previewRequests.Load(); got != 1 {
		t.Fatalf("in-flight preview requests=%d after the asset request ended, want 1 (the stream)", got)
	}
	age() // the asset's end restarted the window; a whole window passes again
	sweepRunning("while a person's preview stream was open")
	if rec := stream(); rec.Code != http.StatusOK || lr.previewRequests.Load() != 0 {
		t.Fatalf("preview response=%d in-flight=%d after the requests ended", rec.Code, lr.previewRequests.Load())
	}
	sweepRunning("right after its preview request ended; the window must restart")
	age()
	f.m.sweepIdleAutoStop(t.Context())
	if r, _ := f.m.getRun(t.Context(), f.tenant, f.runRef); r.State != stateStopped || r.Reason != idlePolicyReason {
		t.Fatalf("after the restarted window: state=%q reason=%q", r.State, r.Reason)
	}
}

// The URL's expiry bounds the hold: a stream a client keeps open after its
// preview URL expired no longer keeps a finished session in memory.
func TestIdlePolicyPreviewHoldEndsWithTheURL(t *testing.T) {
	f, clk, lr := idlePolicyFixture(t)
	holdPreviewRequest(t, f.m, lr, 50*time.Millisecond)
	waitFor(t, "the preview hold to end with its URL", func() bool { return lr.previewRequests.Load() == 0 })
	for range 3 {
		clk.advance(4 * time.Minute)
		if _, err := f.m.Heartbeat(t.Context(), f.tenant, lr.claim.SID, lr.claim.Holder, lr.claim.Fence, 0); err != nil {
			t.Fatal(err)
		}
	}
	f.m.sweepIdleAutoStop(t.Context())
	if r, _ := f.m.getRun(t.Context(), f.tenant, f.runRef); r.State != stateStopped || r.Reason != idlePolicyReason {
		t.Fatalf("an expired preview's stream still held the session: state=%q reason=%q", r.State, r.Reason)
	}
}

// An unreadable setting is a persistent condition of the instance: it is logged
// once per cause, not on every 30 s sweep, and again after a successful read.
func TestIdlePolicyUnreadableSettingWarnsOncePerCause(t *testing.T) {
	f, _, _ := idlePolicyFixture(t)
	logs := &runtimeObserverLogs{}
	f.m.log = slog.New(logs)
	failing := true
	f.m.SetIdleAutoStopPolicy(func(context.Context) (time.Duration, error) {
		if failing {
			return 0, errors.New("settings store unavailable")
		}
		return 0, nil
	})
	warns := func() int {
		logs.mu.Lock()
		defer logs.mu.Unlock()
		n := 0
		for _, r := range logs.records {
			if strings.Contains(r.Message, "idle policy setting could not be read") {
				n++
			}
		}
		return n
	}
	for range 3 {
		f.m.sweepIdleAutoStop(t.Context())
	}
	if n := warns(); n != 1 {
		t.Fatalf("warnings after 3 failing sweeps=%d, want 1", n)
	}
	if r, _ := f.m.getRun(t.Context(), f.tenant, f.runRef); r.State == stateStopped {
		t.Fatal("a session was stopped while the setting could not be read")
	}
	failing = false
	f.m.sweepIdleAutoStop(t.Context())
	failing = true
	f.m.sweepIdleAutoStop(t.Context())
	if n := warns(); n != 2 {
		t.Fatalf("warnings after a successful read and a new failure=%d, want 2", n)
	}
}

func TestIdlePolicyOpenViewKeepsItsGenerationAcrossResume(t *testing.T) {
	f, _, old := idlePolicyFixture(t)
	held, release := f.m.holdRunOpen(f.tenant, f.runRef)
	defer release()
	if held != old {
		t.Fatal("view was not bound to the captured process")
	}
	f.stop(t)
	if r := f.post(t, "resume", nil); r.code != 200 {
		t.Fatal(r.raw)
	}
	current, _ := f.m.rt.getLive(f.tenant, f.runRef)
	if held == current {
		t.Fatal("a view silently moved to a successor")
	}
	release()
	if views := current.viewers.Load(); views != 0 {
		t.Fatal("old view changed successor presence")
	}
}

// The history replay of an attach can outlast a stop and a resume. The stream
// must be counted as a viewer on the run it ends up serving: the denial budget
// interrupts a watched run and stops an unwatched one, and the idle policy skips it.
func TestIdlePolicyAttachRepinsTheRunLiveAfterTheReplay(t *testing.T) {
	cases := []struct {
		name       string
		liveAtHold bool
	}{
		{"run started during the replay", false},
		{"run replaced during the replay", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, _, first := idlePolicyFixture(t)
			var held *liveRun
			var release func()
			if tc.liveAtHold {
				held, release = f.m.holdRunOpen(f.tenant, f.runRef)
				if held != first || first.viewers.Load() != 1 {
					t.Fatal("setup: the view did not pin the live run")
				}
			}
			f.stop(t)
			if !tc.liveAtHold {
				f.m.rt.dropLive(f.tenant, f.runRef) // the reaped handle, as reapClosed leaves it
				held, release = f.m.holdRunOpen(f.tenant, f.runRef)
				if held != nil {
					t.Fatal("setup: a stopped run was reported live")
				}
			}
			if r := f.post(t, "resume", nil); r.code != 200 {
				t.Fatal(r.raw)
			}
			current, _ := f.m.rt.getLive(f.tenant, f.runRef)
			if current == nil || current == held {
				t.Fatal("setup: no successor run is live")
			}
			served, releaseServed := f.m.repinLive(f.tenant, f.runRef, held, release)
			defer releaseServed()
			if served != current {
				t.Fatal("the attach would stream a run that is no longer live")
			}
			if got := current.viewers.Load(); got != 1 {
				t.Fatalf("viewers on the served run = %d, want 1", got)
			}
			if tc.liveAtHold && first.viewers.Load() != 0 {
				t.Fatal("the replaced run kept the attach's pin")
			}
			again, releaseAgain := f.m.repinLive(f.tenant, f.runRef, served, releaseServed)
			if again != served || current.viewers.Load() != 1 {
				t.Fatalf("a repin of the current run changed the count to %d", current.viewers.Load())
			}
			_ = releaseAgain
		})
	}
}

// Inject only the policy's stopping audit; normal launch and later retry use the
// real transaction and ledger, so a failed audit cannot authorize a process effect.
// With seen set it only records the appended drafts and refuses nothing.
type idlePolicyAuditFaultData struct {
	api.ModuleData
	drop bool
	seen *[]model.AuditDraft
}
type idlePolicyAuditFaultScope struct {
	store.Scope
	drop bool
	seen *[]model.AuditDraft
}
type idlePolicyAuditFaultLog struct {
	store.AuditLog
	drop bool
	seen *[]model.AuditDraft
}

func (d idlePolicyAuditFaultData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error {
		return fn(idlePolicyAuditFaultScope{Scope: sc, drop: d.drop, seen: d.seen})
	})
}
func (s idlePolicyAuditFaultScope) Audit() store.AuditLog {
	return idlePolicyAuditFaultLog{AuditLog: s.Scope.Audit(), drop: s.drop, seen: s.seen}
}
func (l idlePolicyAuditFaultLog) Append(ctx context.Context, draft model.AuditDraft) (model.AuditEvent, error) {
	if l.seen != nil {
		*l.seen = append(*l.seen, draft)
		return l.AuditLog.Append(ctx, draft)
	}
	if draft.Action == "sessions.run.stopping" && draft.Meta["stop_cause"] == idlePolicyReason {
		if l.drop {
			return model.AuditEvent{}, nil
		}
		return model.AuditEvent{}, store.ErrAuditSpoolFull
	}
	return l.AuditLog.Append(ctx, draft)
}
func TestIdlePolicyAuditRefusalPreservesTheLiveSessionAndAllowsRetry(t *testing.T) {
	for _, drop := range []bool{false, true} {
		t.Run(fmt.Sprint(drop), func(t *testing.T) {
			f, clk, lr := idlePolicyFixture(t)
			probe := &dualCredentialProbe{}
			f.m.rt.WorkSessionCreds = dualWorkSource{p: probe}
			lr.mu.Lock()
			lr.workCredentialID = model.NewID()
			lr.mu.Unlock()
			original := f.m.Data
			f.m.Data = idlePolicyAuditFaultData{ModuleData: original, drop: drop}
			err := f.m.stopIdleRun(t.Context(), lr, 11*time.Minute)
			if !errors.Is(err, store.ErrAuditSpoolFull) {
				t.Fatalf("lost audit refusal: %v", err)
			}
			select {
			case <-f.fr.lastProc().stopped:
				t.Fatal("unaudited process stop")
			default:
			}
			lr.mu.Lock()
			reserved, reason := lr.stopRequested, lr.stopReason
			lr.mu.Unlock()
			if calls := probe.snapshot(); calls.workRevoke != 0 {
				t.Fatal("audit refusal revoked the live credential")
			}
			if reserved || reason != "" {
				t.Fatal("failed audit retained stop reservation")
			}
			after, _ := f.m.getRun(t.Context(), f.tenant, f.runRef)
			if after.ProcessState != stateRunning || after.Reason != "" {
				t.Fatalf("audit failure changed durable state: %+v", after)
			}
			f.m.Data = original
			lr.mu.Lock()
			lr.workCredentialID = model.ID("")
			lr.mu.Unlock()
			if r := f.post(t, "input", map[string]any{"message": map[string]any{"type": "control_response", "response": map[string]any{"request_id": "retry-probe", "subtype": "success"}}}); r.code != http.StatusAccepted {
				t.Fatalf("input after refusal: %d %s", r.code, r.raw)
			}
			for range 3 {
				clk.advance(4 * time.Minute)
				if _, err := f.m.Heartbeat(t.Context(), f.tenant, lr.claim.SID, lr.claim.Holder, lr.claim.Fence, 0); err != nil {
					t.Fatal(err)
				}
			}
			f.m.sweepIdleAutoStop(t.Context())
			after, _ = f.m.getRun(t.Context(), f.tenant, f.runRef)
			if after.State != stateStopped || after.Reason != idlePolicyReason {
				t.Fatalf("audited retry: %+v", after)
			}
		})
	}
}

// The idle pass must be reachable from the lifecycle sweep itself, not only by
// calling the sweep function: a policy-only configuration has to start the timer.
func TestIdlePolicyRunsFromTheLifecycleSweep(t *testing.T) {
	f, _, _ := idlePolicyFixture(t)
	f.m.stopStopSweep()
	f.m.mu.Lock()
	f.m.rt.idleSweepInterval = 20 * time.Millisecond
	f.m.mu.Unlock()
	f.m.startStopSweep()
	t.Cleanup(f.m.stopStopSweep)
	waitFor(t, "policy stop from the lifecycle sweep", func() bool {
		r, err := f.m.getRun(t.Context(), f.tenant, f.runRef)
		return err == nil && r.State == stateStopped && r.Reason == idlePolicyReason
	})
}

// A running session keeps appending to its transcripts. The shared reader returns
// the consistent prefix and reports the change; it must not fail an existing read.
func TestReadActivityStateReportsALiveAppendWithoutFailing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-a.jsonl")
	writeFile(t, path, "{}\n")
	home, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer home.Close()
	changed, err := readActivityState(t.Context(), home, "agent-a.jsonl", func(_ context.Context, r io.Reader) error {
		_, _ = io.Copy(io.Discard, r)
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = f.WriteString("{}\n")
		return err
	})
	if err != nil || !changed {
		t.Fatalf("append during read: changed=%v err=%v", changed, err)
	}
	changed, err = readActivityState(t.Context(), home, "agent-a.jsonl", func(_ context.Context, r io.Reader) error {
		_, err := io.Copy(io.Discard, r)
		return err
	})
	if err != nil || changed {
		t.Fatalf("stable file: changed=%v err=%v", changed, err)
	}
}

func TestIdlePolicyRechecksABlockedSessionOnlyAfterTheBackoff(t *testing.T) {
	f, clk, _ := idlePolicyFixture(t)
	transcript := filepath.Join(f.homeA, "projects", moveTestProject, moveTestSID+".jsonl")
	writeFile(t, transcript, `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"bg","name":"Bash","input":{"command":"sleep 999","run_in_background":true}}]}}`+"\n")
	f.m.sweepIdleAutoStop(t.Context())
	if r, _ := f.m.getRun(t.Context(), f.tenant, f.runRef); r.State == stateStopped {
		t.Fatalf("a session with a background task was stopped: %q", r.State)
	}
	// The task is gone, but the costly look is not repeated before the delay.
	writeFile(t, transcript, `{"type":"assistant","message":{"content":[{"type":"text","text":"done"}]}}`+"\n")
	clk.advance(time.Minute)
	f.m.sweepIdleAutoStop(t.Context())
	if r, _ := f.m.getRun(t.Context(), f.tenant, f.runRef); r.State == stateStopped {
		t.Fatalf("rechecked inside the backoff: %q", r.State)
	}
	clk.advance(idlePolicyRecheck)
	f.m.sweepIdleAutoStop(t.Context())
	if r, _ := f.m.getRun(t.Context(), f.tenant, f.runRef); r.State != stateStopped || r.Reason != idlePolicyReason {
		t.Fatalf("not reclaimed after the backoff: state=%q reason=%q", r.State, r.Reason)
	}
}
