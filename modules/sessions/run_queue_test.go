// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

const turnResultFrame = `{"type":"result","subtype":"success","is_error":false,"result":"done"}`

const usageLimitResultFrame = `{"type":"result","subtype":"success","is_error":true,"api_error_status":429,"api_error":"usage_limit_reached","result":"You've hit your session limit"}`

// queueRun is a Claude Code session in the middle of a turn: the person sent a first
// message and the tool has not answered it yet.
type queueRun struct {
	h      *harness
	fr     *fakeRunner
	admin  string
	tenant model.TenantID
	ref    string
	proc   *fakeProc
}

func newQueueRun(t *testing.T) queueRun {
	t.Helper()
	h, fr, admin, tenant := attachHTTPHarness(t)
	fr.initSID = "queue-session"
	ref := launchHTTPRun(t, h, admin, tenant, map[string]any{"transport": "stream-json", "name": "queue"})
	q := queueRun{h: h, fr: fr, admin: admin, tenant: tenant, ref: ref, proc: fr.lastProc()}
	if r := h.doJSON("POST", q.path("/input"), admin, map[string]any{"text": "first"}, tenantHdr(tenant)); r.code != http.StatusAccepted {
		t.Fatalf("first message = %d %s", r.code, r.raw)
	}
	waitFor(t, "the first message reached the child", func() bool { return q.proc.sentCount() == 1 })
	return q
}

func (q queueRun) path(suffix string) string { return "/v1/m/sessions/runs/" + q.ref + suffix }

func (q queueRun) endTurn(frame string) {
	q.proc.out <- OutputFrame{Stream: streamStdout, Data: []byte(frame)}
}

// sentTexts is what the child has been given, one user message per entry.
func (q queueRun) sentTexts(t *testing.T) []string {
	t.Helper()
	q.proc.mu.Lock()
	defer q.proc.mu.Unlock()
	var out []string
	for _, line := range q.proc.sent {
		var frame struct {
			Type    string `json:"type"`
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &frame) == nil && frame.Type == "user" {
			out = append(out, frame.Message.Content)
		}
	}
	return out
}

func (q queueRun) add(t *testing.T, text string) string {
	t.Helper()
	r := q.h.doJSON("POST", q.path("/queue"), q.admin, map[string]any{"text": text}, tenantHdr(q.tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("queue %q = %d %s", text, r.code, r.raw)
	}
	id, _ := r.body["id"].(string)
	if id == "" || r.body["text"] != text || r.body["queued_by"] == "" || r.body["queued_at"] == "" {
		t.Fatalf("queued item = %s, want id, text, queued_by and queued_at", r.raw)
	}
	return id
}

// list reads the queue as a person reloading the page does: the texts in order, and
// the line that says why the first one has not gone.
func (q queueRun) list(t *testing.T, token string) (texts []string, hold string, body map[string]any) {
	t.Helper()
	r := q.h.do("GET", q.path("/queue"), token, tenantHdr(q.tenant))
	if r.code != http.StatusOK {
		t.Fatalf("queue = %d %s", r.code, r.raw)
	}
	items, _ := r.body["items"].([]any)
	for _, it := range items {
		texts = append(texts, it.(map[string]any)["text"].(string))
	}
	hold, _ = r.body["hold"].(string)
	return texts, hold, r.body
}

func equalTexts(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Two messages queued during a turn are saved on the server, in order, and a reader
// that knows nothing of the first (a reload, a second device) sees both. Nothing
// reaches the tool while its turn runs.
func TestQueuedMessagesAreSavedOnTheServerInOrder(t *testing.T) {
	q := newQueueRun(t)
	q.add(t, "run the tests")
	q.add(t, "then the linter")

	second := q.h.userToken(q.admin, q.tenant, "other-device@queue.io", "admin")
	for name, token := range map[string]string{"the same person": q.admin, "a second device": second} {
		texts, hold, body := q.list(t, token)
		if !equalTexts(texts, []string{"run the tests", "then the linter"}) {
			t.Fatalf("%s sees %v, want both messages in order", name, texts)
		}
		if hold != "turn" {
			t.Fatalf("%s: hold = %q, want turn while the tool answers", name, hold)
		}
		if body["can_steer"] != true {
			t.Fatalf("%s: can_steer = %v, want true for Claude Code", name, body["can_steer"])
		}
	}
	if got := q.sentTexts(t); !equalTexts(got, []string{"first"}) {
		t.Fatalf("the tool was given %v while its turn ran, want only the first message", got)
	}
}

func TestQueuedMessagesCanBeEditedReorderedAndRemoved(t *testing.T) {
	q := newQueueRun(t)
	a := q.add(t, "a")
	b := q.add(t, "b")
	c := q.add(t, "c")

	if r := q.h.doJSON("PATCH", q.path("/queue/"+a), q.admin, map[string]any{"text": "a edited"}, tenantHdr(q.tenant)); r.code != http.StatusOK || r.body["text"] != "a edited" {
		t.Fatalf("edit = %d %s", r.code, r.raw)
	}
	if r := q.h.doJSON("PUT", q.path("/queue"), q.admin, map[string]any{"ids": []string{c, a, b}}, tenantHdr(q.tenant)); r.code != http.StatusOK {
		t.Fatalf("reorder = %d %s", r.code, r.raw)
	}
	if texts, _, _ := q.list(t, q.admin); !equalTexts(texts, []string{"c", "a edited", "b"}) {
		t.Fatalf("after edit and reorder: %v", texts)
	}
	if r := q.h.doJSON("DELETE", q.path("/queue/"+a), q.admin, nil, tenantHdr(q.tenant)); r.code != http.StatusOK {
		t.Fatalf("remove = %d %s", r.code, r.raw)
	}
	if texts, _, _ := q.list(t, q.admin); !equalTexts(texts, []string{"c", "b"}) {
		t.Fatalf("after remove: %v", texts)
	}
	// An order that is not the queue's own items refuses, and changes nothing.
	if r := q.h.doJSON("PUT", q.path("/queue"), q.admin, map[string]any{"ids": []string{b, b}}, tenantHdr(q.tenant)); r.code != http.StatusBadRequest {
		t.Fatalf("a reorder naming an item twice = %d %s, want 400", r.code, r.raw)
	}
	if r := q.h.doJSON("PUT", q.path("/queue"), q.admin, map[string]any{"ids": []string{b}}, tenantHdr(q.tenant)); r.code != http.StatusBadRequest {
		t.Fatalf("a reorder that leaves a message out = %d %s, want 400", r.code, r.raw)
	}
	if texts, _, _ := q.list(t, q.admin); !equalTexts(texts, []string{"c", "b"}) {
		t.Fatalf("a refused reorder changed the queue: %v", texts)
	}
	if r := q.h.doJSON("PATCH", q.path("/queue/"+b), q.admin, map[string]any{"text": "   "}, tenantHdr(q.tenant)); r.code != http.StatusBadRequest {
		t.Fatalf("an empty edit = %d %s, want 400", r.code, r.raw)
	}
}

// When the turn ends the first queued message is sent by itself, exactly once, and
// the next one waits for the turn that message starts.
func TestTheFirstQueuedMessageIsSentOnceWhenTheTurnEnds(t *testing.T) {
	q := newQueueRun(t)
	q.add(t, "one")
	q.add(t, "two")

	q.endTurn(turnResultFrame)
	waitFor(t, "the first queued message reached the child", func() bool { return q.proc.sentCount() == 2 })
	waitFor(t, "the sent message left the queue", func() bool {
		texts, _, _ := q.list(t, q.admin)
		return equalTexts(texts, []string{"two"})
	})
	if got := q.sentTexts(t); !equalTexts(got, []string{"first", "one"}) {
		t.Fatalf("the tool was given %v, want the first queued message once", got)
	}
	// The turn that message started is running: the next one still waits.
	if _, hold, _ := q.list(t, q.admin); hold != "turn" {
		t.Fatalf("hold = %q, want turn while the second turn runs", hold)
	}

	q.endTurn(turnResultFrame)
	waitFor(t, "the second queued message reached the child", func() bool { return q.proc.sentCount() == 3 })
	waitFor(t, "the queue emptied", func() bool {
		texts, _, _ := q.list(t, q.admin)
		return len(texts) == 0
	})
	if got := q.sentTexts(t); !equalTexts(got, []string{"first", "one", "two"}) {
		t.Fatalf("the tool was given %v, want each message once, in order", got)
	}
}

// A message queued after the turn already ended does not wait for a turn that will not
// come: it goes at once.
func TestAMessageQueuedWhileTheToolWaitsGoesAtOnce(t *testing.T) {
	q := newQueueRun(t)
	q.endTurn(turnResultFrame)
	waitFor(t, "the session waits for the person", func() bool {
		r := q.h.do("GET", q.path(""), q.admin, tenantHdr(q.tenant))
		return r.body["state"] == stateIdle
	})
	q.add(t, "late")
	waitFor(t, "the late message reached the child", func() bool { return q.proc.sentCount() == 2 })
	if texts, _, _ := q.list(t, q.admin); len(texts) != 0 {
		t.Fatalf("queue still holds %v after the send", texts)
	}
}

// A limit ends the turn too, but the account cannot take the next message: the queue
// stays and says why.
func TestALimitKeepsTheQueueAndSaysWhy(t *testing.T) {
	q := newQueueRun(t)
	q.add(t, "after the limit")
	q.endTurn(usageLimitResultFrame)
	waitFor(t, "the queue says it waits on the limit", func() bool {
		_, hold, _ := q.list(t, q.admin)
		return hold == "limit"
	})
	if q.proc.sentCount() != 1 {
		t.Fatalf("the child was given %d lines, want the first message only", q.proc.sentCount())
	}
	if texts, _, _ := q.list(t, q.admin); !equalTexts(texts, []string{"after the limit"}) {
		t.Fatalf("the queue after a limit: %v", texts)
	}
}

// A session that waits for the person to approve something does not take the next
// message either: the queue stays and says so.
func TestAnApprovalKeepsTheQueueAndSaysWhy(t *testing.T) {
	q := newQueueRun(t)
	q.add(t, "after the approval")
	lr, ok := q.h.m.rt.getLive(q.tenant, q.ref)
	if !ok {
		t.Fatal("the session has no live handle")
	}
	lr.mu.Lock()
	lr.pendingApprovals = append(lr.pendingApprovals, &runApprovalWait{})
	lr.mu.Unlock()
	q.endTurn(turnResultFrame)
	waitFor(t, "the bridge counted the turn as ended", func() bool {
		lr.mu.Lock()
		defer lr.mu.Unlock()
		return lr.claudeTurnsEnded == 1
	})
	// The send, if it were going to happen, runs off the bridge right after that count.
	time.Sleep(300 * time.Millisecond)
	texts, hold, _ := q.list(t, q.admin)
	if !equalTexts(texts, []string{"after the approval"}) || hold != "approval" || q.proc.sentCount() != 1 {
		t.Fatalf("while an approval waits: queue %v hold=%q, child given %d lines; want the message kept, hold=approval and the first message only", texts, hold, q.proc.sentCount())
	}
}

// The person interrupted the turn to stop the agent: the next queued message does not
// start another one behind their back. It stays, and the queue says the session is stopped.
func TestAnInterruptedTurnDoesNotSendTheNextQueuedMessage(t *testing.T) {
	q := newQueueRun(t)
	q.add(t, "after the interrupt")
	seen := claudeProtocolStub(t, q.proc, "success")
	if r := q.h.doJSON("POST", q.path("/interrupt"), q.admin, nil, tenantHdr(q.tenant)); r.code != http.StatusOK {
		t.Fatalf("interrupt = %d %s", r.code, r.raw)
	}
	<-seen
	// The tool's own result for the interrupted turn follows its acknowledgement.
	q.endTurn(turnResultFrame)
	time.Sleep(300 * time.Millisecond)
	texts, hold, _ := q.list(t, q.admin)
	if !equalTexts(texts, []string{"after the interrupt"}) || hold != "stopped" {
		t.Fatalf("after an interrupt: queue %v hold=%q, want the message kept and hold=stopped", texts, hold)
	}
	if got := q.sentTexts(t); !equalTexts(got, []string{"first"}) {
		t.Fatalf("the tool was given %v after an interrupt, want only the first message", got)
	}
}

// A send the session refuses before any byte reaches the tool puts the message back,
// where it was: nothing the person typed is lost to a refusal.
func TestARefusedSteerPutsTheMessageBack(t *testing.T) {
	q := newQueueRun(t)
	if r := q.h.doJSON("POST", q.path("/stop"), q.admin, nil, tenantHdr(q.tenant)); r.code != http.StatusOK {
		t.Fatalf("stop = %d %s", r.code, r.raw)
	}
	first := q.add(t, "one")
	q.add(t, "two")
	r := q.h.doJSON("POST", q.path("/queue/"+first+"/steer"), q.admin, nil, tenantHdr(q.tenant))
	if r.code < 400 || r.code >= 500 {
		t.Fatalf("steer into a stopped session = %d %s, want a 4xx refusal", r.code, r.raw)
	}
	if texts, _, _ := q.list(t, q.admin); !equalTexts(texts, []string{"one", "two"}) {
		t.Fatalf("after the refused steer the queue holds %v, want both messages in their places", texts)
	}
}

// A stopped session keeps its queue, and resuming it is the moment the queue goes: no turn
// ends to send it, and the queue would otherwise promise a send that never comes.
func TestResumingAStoppedSessionSendsItsQueue(t *testing.T) {
	q := newQueueRun(t)
	waitFor(t, "the tool's session id was captured, so the session can resume", func() bool {
		r := q.h.do("GET", q.path(""), q.admin, tenantHdr(q.tenant))
		return r.body["claude_session_id"] != nil && r.body["claude_session_id"] != ""
	})
	q.add(t, "after the resume")
	if r := q.h.doJSON("POST", q.path("/stop"), q.admin, nil, tenantHdr(q.tenant)); r.code != http.StatusOK {
		t.Fatalf("stop = %d %s", r.code, r.raw)
	}
	if texts, hold, _ := q.list(t, q.admin); !equalTexts(texts, []string{"after the resume"}) || hold != "stopped" {
		t.Fatalf("after stop: %v hold=%q", texts, hold)
	}
	if r := q.h.doJSON("POST", q.path("/resume"), q.admin, nil, tenantHdr(q.tenant)); r.code != http.StatusOK {
		t.Fatalf("resume = %d %s", r.code, r.raw)
	}
	waitFor(t, "the queued message reached the resumed child", func() bool {
		p := q.fr.lastProc()
		return p != q.proc && p.sentCount() >= 1
	})
	waitFor(t, "the queue emptied", func() bool {
		texts, _, _ := q.list(t, q.admin)
		return len(texts) == 0
	})
}

// Stop keeps the queue, and says the session stopped; releasing the session hands back
// what it discards, so the person can copy it.
func TestStopKeepsTheQueueAndReleaseReportsWhatItDiscarded(t *testing.T) {
	q := newQueueRun(t)
	q.add(t, "keep me")
	q.add(t, "and me")

	if r := q.h.doJSON("POST", q.path("/stop"), q.admin, nil, tenantHdr(q.tenant)); r.code != http.StatusOK {
		t.Fatalf("stop = %d %s", r.code, r.raw)
	}
	texts, hold, _ := q.list(t, q.admin)
	if !equalTexts(texts, []string{"keep me", "and me"}) || hold != "stopped" {
		t.Fatalf("after stop: %v hold=%q, want both messages and hold=stopped", texts, hold)
	}

	r := q.h.doJSON("POST", q.path("/cleanup"), q.admin, nil, tenantHdr(q.tenant))
	if r.code != http.StatusOK {
		t.Fatalf("cleanup = %d %s", r.code, r.raw)
	}
	discarded, _ := r.body["discarded_queue"].([]any)
	if len(discarded) != 2 || discarded[0].(map[string]any)["text"] != "keep me" || discarded[1].(map[string]any)["text"] != "and me" {
		t.Fatalf("cleanup discarded_queue = %s, want both messages in order", r.raw)
	}
	if texts, _, _ := q.list(t, q.admin); len(texts) != 0 {
		t.Fatalf("a released session still holds %v", texts)
	}
}

// A session can only be deleted once it is released, and the release is where the queue
// is shown and dropped: a released session takes no new message, so there is nothing
// left for a delete to discard unseen.
func TestAReleasedSessionTakesNoMessage(t *testing.T) {
	q := newQueueRun(t)
	if r := q.h.doJSON("POST", q.path("/stop"), q.admin, nil, tenantHdr(q.tenant)); r.code != http.StatusOK {
		t.Fatalf("stop = %d %s", r.code, r.raw)
	}
	if r := q.h.doJSON("POST", q.path("/cleanup"), q.admin, nil, tenantHdr(q.tenant)); r.code != http.StatusOK {
		t.Fatalf("cleanup = %d %s", r.code, r.raw)
	}
	if r := q.h.doJSON("POST", q.path("/queue"), q.admin, map[string]any{"text": "too late"}, tenantHdr(q.tenant)); r.code != http.StatusConflict {
		t.Fatalf("queueing on a released session = %d %s, want 409", r.code, r.raw)
	}
}

// Steer sends one queued message into the running turn through the tool's own input,
// and takes it out of the queue; the others stay.
func TestSteerSendsTheChosenMessageIntoTheRunningTurn(t *testing.T) {
	q := newQueueRun(t)
	q.add(t, "later")
	steer := q.add(t, "now")

	r := q.h.doJSON("POST", q.path("/queue/"+steer+"/steer"), q.admin, nil, tenantHdr(q.tenant))
	if r.code != http.StatusAccepted {
		t.Fatalf("steer = %d %s", r.code, r.raw)
	}
	waitFor(t, "the steered message reached the child", func() bool { return q.proc.sentCount() == 2 })
	if got := q.sentTexts(t); !equalTexts(got, []string{"first", "now"}) {
		t.Fatalf("the tool was given %v, want the steered message", got)
	}
	if texts, _, _ := q.list(t, q.admin); !equalTexts(texts, []string{"later"}) {
		t.Fatalf("after steer the queue holds %v, want the other message", texts)
	}
	if r := q.h.doJSON("POST", q.path("/queue/"+steer+"/steer"), q.admin, nil, tenantHdr(q.tenant)); r.code != http.StatusNotFound {
		t.Fatalf("steering a message that already went = %d %s, want 404", r.code, r.raw)
	}
}

// Steer is offered only where the tool takes input into a running turn.
func TestOnlyToolsThatTakeInputInATurnCanSteer(t *testing.T) {
	cases := map[string]bool{
		"":                   true, // Claude Code, the historical path
		providerDriverClaude: true,
		providerDriverCodex:  true,
		providerDriverGrok:   false, // ACP has no steer (driver_grok.go)
		providerDriverGemini: false,
	}
	for driver, want := range cases {
		rec := model.Record{colRunProfileDriver: driver, colTransport: string(TransportStreamJSON)}
		if got := queueCanSteer(rec); got != want {
			t.Errorf("queueCanSteer(driver %q) = %v, want %v", driver, got, want)
		}
	}
}

// Who may message a session is unchanged: a reader who may not write cannot queue, and
// another organization cannot see the queue at all.
func TestTheQueueFollowsTheSessionsOwnAccessRules(t *testing.T) {
	q := newQueueRun(t)
	q.add(t, "mine")
	viewer := q.h.viewerToken(q.admin, q.tenant, "v@queue.io")
	if r := q.h.doJSON("POST", q.path("/queue"), viewer, map[string]any{"text": "x"}, tenantHdr(q.tenant)); r.code != http.StatusForbidden {
		t.Fatalf("a viewer queueing = %d %s, want 403", r.code, r.raw)
	}
	other := q.h.createOrg(q.admin, "globex")
	if r := q.h.do("GET", q.path("/queue"), q.admin, tenantHdr(other)); r.code != http.StatusNotFound {
		t.Fatalf("another organization reading the queue = %d %s, want 404", r.code, r.raw)
	}
}
