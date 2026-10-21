// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// denialFixture is one live Claude run with the session credential the hook PEP
// resolves, so every call goes through the same principal checks as a real hook.
type denialFixture struct {
	t         *testing.T
	h         *harness
	admin     string
	tenant    model.TenantID
	run       string
	principal auth.Principal
	proc      *fakeProc
	budget    DenialBudget
	detach    func()
}

func newDenialFixture(t *testing.T, viewer bool) *denialFixture {
	t.Helper()
	return newDenialFixtureWith(t, viewer, true)
}

// newDenialFixtureWith can leave the interrupt unanswered, as a Claude Code that is wedged.
func newDenialFixtureWith(t *testing.T, viewer, answersInterrupts bool) *denialFixture {
	t.Helper()
	old := claudeInterruptWait
	claudeInterruptWait = 2 * time.Second
	t.Cleanup(func() { claudeInterruptWait = old })
	fr := &fakeRunner{initSID: dupID}
	h, admin, tenant, profile, _ := profiledHTTP(t, fr, &testClock{now: baseTime})
	run, _ := launchProfiledHTTP(t, h, admin, tenant, profile)
	a := auth.NewAuthenticator(h.st, nil)
	human, err := a.Authenticate(t.Context(), admin)
	if err != nil {
		t.Fatal(err)
	}
	issuer := auth.NewSessionCredentials(a, func(ctx context.Context, scope auth.SessionScope) error {
		return h.m.Authority(ctx, scope.TenantID, scope.SessionRef, scope.Holder, scope.Fence)
	})
	lr, ok := h.m.rt.getLive(tenant, run)
	if !ok {
		t.Fatal("launched run is not supervised")
	}
	var workspace model.ID
	if err := h.st.View(t.Context(), tenant, func(sc store.Scope) error {
		ws, err := sc.DefaultWorkspace(t.Context())
		workspace = ws.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	token, err := issuer.Mint(t.Context(), human, auth.SessionScope{TenantID: tenant, WorkspaceID: workspace, FolderRef: run, SessionRef: lr.claim.SID, RunRef: run, Holder: lr.claim.Holder, Fence: lr.claim.Fence})
	if err != nil {
		t.Fatal(err)
	}
	p, err := issuer.Authenticate(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	f := &denialFixture{t: t, h: h, admin: admin, tenant: tenant, run: run, principal: p, proc: fr.lastProc()}
	if answersInterrupts {
		f.answerInterrupts()
	}
	if viewer {
		f.detach = f.attach()
	}
	return f
}

// answerInterrupts answers every interrupt control request the way Claude Code does.
func (f *denialFixture) answerInterrupts() {
	answered := 0
	go func() {
		for f.t.Context().Err() == nil {
			time.Sleep(5 * time.Millisecond)
			f.proc.mu.Lock()
			sent := append([][]byte(nil), f.proc.sent...)
			f.proc.mu.Unlock()
			for ; answered < len(sent); answered++ {
				var req map[string]any
				if json.Unmarshal(sent[answered], &req) != nil || req["type"] != "control_request" {
					continue
				}
				id, _ := req["request_id"].(string)
				b, _ := json.Marshal(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": id}})
				select {
				case f.proc.out <- OutputFrame{Stream: streamStdout, Data: b}:
				case <-f.t.Context().Done():
					return
				}
			}
		}
	}()
}

// attach opens the run's live output stream, the viewer the console and the CLI hold.
func (f *denialFixture) attach() func() {
	ctx, cancel := context.WithCancel(f.t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		req := httptest.NewRequest(http.MethodGet, "/v1/m/sessions/runs/"+f.run+"/attach", nil).WithContext(ctx)
		req.RemoteAddr = "10.0.0.1:1234"
		req.Header.Set("Authorization", "Bearer "+f.admin)
		req.Header.Set("X-Olivares-Tenant", f.tenant.String())
		f.h.srv.Handler().ServeHTTP(httptest.NewRecorder(), req)
	}()
	stop := func() { cancel(); <-done }
	f.t.Cleanup(stop)
	lr := f.live()
	waitFor(f.t, "the attach stream to register as a viewer", func() bool { return lr.viewers.Load() == 1 })
	return func() {
		stop()
		waitFor(f.t, "the detached stream to stop counting as a viewer", func() bool { return lr.viewers.Load() == 0 })
	}
}

func (f *denialFixture) live() *liveRun {
	f.t.Helper()
	lr, ok := f.h.m.rt.getLive(f.tenant, f.run)
	if !ok {
		f.t.Fatal("run is not supervised")
	}
	return lr
}

// tally reads the run's denial counts. They change synchronously with each decision,
// so a negative assertion on them needs no sleep.
func (f *denialFixture) tally() (consecutive, total int, breached bool) {
	lr := f.live()
	lr.mu.Lock()
	defer lr.mu.Unlock()
	return lr.denials.consecutive, lr.denials.total, lr.denials.breach != nil
}

func (f *denialFixture) listed() map[string]any {
	f.t.Helper()
	for _, row := range itemsOf(f.t, f.h.do(http.MethodGet, "/v1/m/sessions/runs", f.admin, tenantHdr(f.tenant))) {
		if row["run_ref"] == f.run {
			return row
		}
	}
	f.t.Fatal("run is missing from the list")
	return nil
}

func (f *denialFixture) record(denied bool, reason string) {
	f.t.Helper()
	if err := f.h.m.RecordToolDecision(f.principal, denied, reason, f.budget); err != nil {
		f.t.Fatalf("RecordToolDecision(denied=%v): %v", denied, err)
	}
}

func (f *denialFixture) deny(n int) {
	f.t.Helper()
	for i := 0; i < n; i++ {
		f.record(true, fmt.Sprintf("policy forbids tool-call %d", i+1))
	}
}

func (f *denialFixture) allow() { f.t.Helper(); f.record(false, "permitted") }

func (f *denialFixture) interrupts() int {
	f.proc.mu.Lock()
	defer f.proc.mu.Unlock()
	n := 0
	for _, line := range f.proc.sent {
		var req map[string]any
		if json.Unmarshal(line, &req) == nil && req["type"] == "control_request" {
			n++
		}
	}
	return n
}

// breaches returns the meta of every denial-budget entry on the tenant's ledger.
func (f *denialFixture) breaches() []map[string]any {
	f.t.Helper()
	var out []map[string]any
	err := f.h.st.View(f.t.Context(), f.tenant, func(sc store.Scope) error {
		// Walk hides metadata once the ledger blinds it; the canonical walk carries it.
		return sc.Audit().(store.CanonicalWalker).WalkCanonical(f.t.Context(), 0, func(ev model.AuditEvent, meta string, _ []byte) error {
			if ev.Action != denialBudgetAction {
				return nil
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(meta), &m); err != nil {
				return err
			}
			out = append(out, m)
			return nil
		})
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

func (f *denialFixture) dto() map[string]any {
	f.t.Helper()
	r := f.h.do(http.MethodGet, "/v1/m/sessions/runs/"+f.run, f.admin, tenantHdr(f.tenant))
	if r.code != http.StatusOK {
		f.t.Fatalf("run detail = %d %s", r.code, r.raw)
	}
	return r.body
}

// settle gives an escalation that must NOT happen the time a wrong one would need.
func settle() { time.Sleep(150 * time.Millisecond) }

func (f *denialFixture) waitBreaches(n int) {
	f.t.Helper()
	waitFor(f.t, fmt.Sprintf("%d denial-budget ledger entries", n), func() bool { return len(f.breaches()) == n })
}

func TestDenialBudget_ThreeConsecutiveDenialsInterruptOnce(t *testing.T) {
	f := newDenialFixture(t, true)
	f.deny(3)
	f.waitBreaches(1)
	if got := f.interrupts(); got != 1 {
		t.Fatalf("interrupts after 3 consecutive denials = %d, want 1", got)
	}
	d := f.dto()
	need, _ := d["needs_person"].(map[string]any)
	if need == nil {
		t.Fatalf("an interrupted session does not show needs_person: %v", d)
	}
	if row, _ := f.listed()["needs_person"].(map[string]any); row == nil || row["code"] != "repeated_policy_denials" || row["consecutive_denials"] != float64(3) {
		t.Fatalf("the run list does not show needs_person with the counts: %v", f.listed())
	}
	reasons, _ := need["reasons"].([]any)
	if len(reasons) != 3 || reasons[0] != "policy forbids tool-call 1" || reasons[2] != "policy forbids tool-call 3" {
		t.Fatalf("needs_person.reasons = %v, want the 3 denial reasons", need["reasons"])
	}
	// An interrupted turn leaves the process alive and the session idle: it awaits input.
	if d["state"] != "idle" || d["process_state"] != "running" {
		t.Fatalf("an interrupt must leave the session idle on a live process: state=%v process_state=%v", d["state"], d["process_state"])
	}
	meta := f.breaches()[0]
	if meta["action"] != "interrupt" || meta["trigger"] != "consecutive" {
		t.Fatalf("ledger meta = %v, want action=interrupt trigger=consecutive", meta)
	}
	// Further denials of the same stretch neither interrupt again nor write again.
	f.deny(2)
	settle()
	if got := f.interrupts(); got != 1 {
		t.Fatalf("interrupts after 5 denials = %d, want still 1", got)
	}
	if got := len(f.breaches()); got != 1 {
		t.Fatalf("ledger entries after 5 denials = %d, want one per breach", got)
	}
}

// Removing the reset (consecutive never goes back to 0) turns this test red: the
// 4th call, a denial, would already be the third counted one.
func TestDenialBudget_AnAllowInBetweenResetsTheConsecutiveCount(t *testing.T) {
	f := newDenialFixture(t, true)
	f.deny(2)
	f.allow()
	f.deny(2)
	if consecutive, total, breached := f.tally(); consecutive != 2 || total != 4 || breached {
		t.Fatalf("after deny,deny,allow,deny,deny the tally is consecutive=%d total=%d breached=%v, want 2, 4, false", consecutive, total, breached)
	}
	if _, shown := f.dto()["needs_person"]; shown || len(f.breaches()) != 0 || f.interrupts() != 0 {
		t.Fatal("no breach happened, yet the session was interrupted, needs a person or the ledger has an entry")
	}
	f.deny(1) // the third in a row since the allow
	f.waitBreaches(1)
	if got := f.interrupts(); got != 1 {
		t.Fatalf("interrupts after 3 denials since the allow = %d, want 1", got)
	}
}

func TestDenialBudget_TwentyDenialsInterruptEvenWhenAllowsKeepResettingTheStreak(t *testing.T) {
	f := newDenialFixture(t, true)
	for i := 0; i < 9; i++ { // deny,deny,allow never reaches 3 in a row
		f.deny(2)
		f.allow()
	}
	f.deny(1)
	if consecutive, total, breached := f.tally(); consecutive != 1 || total != 19 || breached {
		t.Fatalf("after 19 denials the tally is consecutive=%d total=%d breached=%v, want 1, 19, false", consecutive, total, breached)
	}
	f.allow()
	f.deny(1)
	f.waitBreaches(1)
	if meta := f.breaches()[0]; meta["trigger"] != "total" {
		t.Fatalf("20th denial tripped on %v, want total", meta["trigger"])
	}
	if got := f.interrupts(); got != 1 {
		t.Fatalf("interrupts after 20 total denials = %d, want 1", got)
	}
}

func TestDenialBudget_TheTenantBudgetReplacesTheDefaults(t *testing.T) {
	f := newDenialFixture(t, true)
	f.budget = DenialBudget{Consecutive: 2, Total: 5}
	f.deny(1)
	if _, _, breached := f.tally(); breached {
		t.Fatal("one denial tripped a budget of 2")
	}
	f.deny(1)
	f.waitBreaches(1)
	if meta := f.breaches()[0]; meta["limit_consecutive"] != float64(2) || meta["limit_total"] != float64(5) || meta["trigger"] != "consecutive" {
		t.Fatalf("ledger limits = %v/%v, want 2/5", meta["limit_consecutive"], meta["limit_total"])
	}
}

func TestDenialBudget_ASessionNobodyWatchesIsStoppedNotLeftWaiting(t *testing.T) {
	f := newDenialFixture(t, false)
	f.deny(3)
	f.waitBreaches(1)
	waitFor(t, "the unwatched session to stop", func() bool {
		d, err := f.h.m.getRun(t.Context(), f.tenant, f.run)
		return err == nil && d.State == stateStopped
	})
	if got := f.interrupts(); got != 0 {
		t.Fatalf("an unwatched session was interrupted %d times; nobody could answer, it must stop", got)
	}
	if meta := f.breaches()[0]; meta["action"] != "stop" {
		t.Fatalf("ledger action = %v, want stop", meta["action"])
	}
}

func TestDenialBudget_APersonsInputStartsANewBudget(t *testing.T) {
	f := newDenialFixture(t, true)
	f.deny(3)
	f.waitBreaches(1)
	// Work, handoff and retained-authority input is not a person's answer.
	rec, err := f.h.m.loadRun(t.Context(), f.tenant, f.run)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.m.sendTextInputLoaded(t.Context(), f.tenant, f.run, "automated follow-up", nil, rec); err != nil {
		t.Fatalf("automated input: %v", err)
	}
	if _, shown := f.dto()["needs_person"]; !shown {
		t.Fatal("automated input answered a breach that only a person can answer")
	}
	if err := f.h.m.sendTextInput(t.Context(), f.tenant, f.run, "go on, but read only"); err != nil {
		t.Fatalf("input after the interrupt: %v", err)
	}
	if _, shown := f.dto()["needs_person"]; shown {
		t.Fatal("the session still needs a person after one answered")
	}
	if row := f.listed(); row["needs_person"] != nil {
		t.Fatalf("the run list still shows needs_person after the answer: %v", row["needs_person"])
	}
	f.deny(3)
	f.waitBreaches(2)
	if got := f.interrupts(); got != 2 {
		t.Fatalf("interrupts over two breaches = %d, want 2", got)
	}
}

func TestDenialBudget_OnlyTheRunsOwnCurrentCredentialCounts(t *testing.T) {
	f := newDenialFixture(t, true)
	human, err := auth.NewAuthenticator(f.h.st, nil).Authenticate(t.Context(), f.admin)
	if err != nil {
		t.Fatal(err)
	}
	stale, other := f.principal, f.principal
	stale.SessionFence++
	other.SessionIdentity = "another-session"
	for name, p := range map[string]auth.Principal{"human": human, "stale fence": stale, "other session": other} {
		for i := 0; i < 3; i++ {
			if err := f.h.m.RecordToolDecision(p, true, "denied", f.budget); err == nil {
				t.Fatalf("%s: a decision was counted without this run's current session authority", name)
			}
		}
	}
	settle()
	if f.interrupts() != 0 || len(f.breaches()) != 0 {
		t.Fatal("refused principals still tripped the budget of the run")
	}
}

// An interrupt Claude Code never confirms must not leave the breached session running.
func TestDenialBudget_AnInterruptThatFailsFallsBackToStop(t *testing.T) {
	old := claudeInterruptWait
	claudeInterruptWait = 100 * time.Millisecond
	t.Cleanup(func() { claudeInterruptWait = old })
	f := newDenialFixtureWith(t, true, false)
	f.deny(3)
	f.waitBreaches(1)
	waitFor(t, "the session to stop after the failed interrupt", func() bool {
		d, err := f.h.m.getRun(t.Context(), f.tenant, f.run)
		return err == nil && d.State == stateStopped
	})
	if f.interrupts() != 1 {
		t.Fatalf("interrupt requests = %d, want the one that failed", f.interrupts())
	}
	if meta := f.breaches()[0]; meta["action"] != "stop" {
		t.Fatalf("ledger action = %v, want stop", meta["action"])
	}
}

// A person using the session's app in a browser preview is not watching the
// session: nobody can answer the breach, so the session still stops.
func TestDenialBudget_APreviewInUseIsNotAWatcher(t *testing.T) {
	f := newDenialFixture(t, false)
	holdPreviewRequest(t, f.h.m, f.live(), previewTTL)
	f.deny(3)
	f.waitBreaches(1)
	waitFor(t, "the session with only a preview open to stop", func() bool {
		d, err := f.h.m.getRun(t.Context(), f.tenant, f.run)
		return err == nil && d.State == stateStopped
	})
	if meta := f.breaches()[0]; meta["action"] != "stop" || f.interrupts() != 0 {
		t.Fatalf("ledger action = %v with %d interrupts, want stop and none", meta["action"], f.interrupts())
	}
}

// A console tab that was closed no longer watches the session.
func TestDenialBudget_ADetachedViewerNoLongerCountsAsWatching(t *testing.T) {
	f := newDenialFixture(t, true)
	f.detach()
	f.deny(3)
	f.waitBreaches(1)
	waitFor(t, "the unwatched session to stop", func() bool {
		d, err := f.h.m.getRun(t.Context(), f.tenant, f.run)
		return err == nil && d.State == stateStopped
	})
	if meta := f.breaches()[0]; meta["action"] != "stop" || f.interrupts() != 0 {
		t.Fatalf("ledger action = %v with %d interrupts, want stop and none", meta["action"], f.interrupts())
	}
	if _, shown := f.dto()["needs_person"]; shown {
		t.Fatal("a stopped session still reports needs_person")
	}
}

// An escalation that a person's answer or an operator's stop overtook acts on nothing.
func TestDenialBudget_AnOvertakenEscalationDoesNothing(t *testing.T) {
	f := newDenialFixture(t, true)
	stale := &denialBreach{trigger: denialByConsecutive, consecutive: 3, total: 3, limitConsecutive: 3, limitTotal: 20}
	f.h.m.escalateDenials(f.live(), stale) // the run holds no such breach: it was answered
	if f.interrupts() != 0 || len(f.breaches()) != 0 {
		t.Fatalf("an answered breach still interrupted (%d) or was recorded (%d)", f.interrupts(), len(f.breaches()))
	}
	f.deny(3)
	f.waitBreaches(1)
	lr := f.live()
	lr.mu.Lock()
	current := lr.denials.breach
	lr.mu.Unlock()
	if _, err := f.h.m.stopRun(t.Context(), f.tenant, f.run, actorU, actorKindU); err != nil {
		t.Fatalf("operator stop: %v", err)
	}
	f.h.m.escalateDenials(lr, current) // the run is already ending
	settle()
	if got := len(f.breaches()); got != 1 {
		t.Fatalf("ledger entries after an operator stop = %d, want only the first breach", got)
	}
}

// The total counts across ordinary prompts: only answering a spent budget starts it again.
func TestDenialBudget_OrdinaryInputDoesNotRestartTheTotal(t *testing.T) {
	f := newDenialFixture(t, true)
	f.budget = DenialBudget{Consecutive: 50, Total: 4}
	f.deny(2)
	if err := f.h.m.sendTextInput(t.Context(), f.tenant, f.run, "keep going"); err != nil {
		t.Fatal(err)
	}
	if _, total, _ := f.tally(); total != 2 {
		t.Fatalf("total after an ordinary prompt = %d, want 2", total)
	}
	f.deny(2)
	f.waitBreaches(1)
	if meta := f.breaches()[0]; meta["trigger"] != "total" || meta["total"] != float64(4) {
		t.Fatalf("breach meta = %v, want a total breach at 4", meta)
	}
}

// Only the last few reasons are kept, each clipped to valid UTF-8.
func TestDenialBudget_KeepsTheLastReasonsClipped(t *testing.T) {
	f := newDenialFixture(t, true)
	f.budget = DenialBudget{Consecutive: 7}
	long := strings.Repeat("\u00f1", 300) // 600 bytes, cut inside a character when clipped
	for i := 1; i <= 6; i++ {
		f.record(true, fmt.Sprintf("reason %d", i))
	}
	f.record(true, long)
	f.waitBreaches(1)
	reasons, _ := f.breaches()[0]["reasons"].([]any)
	if len(reasons) != denialReasonsKept || reasons[0] != "reason 3" {
		t.Fatalf("reasons = %v, want the last %d starting at reason 3", reasons, denialReasonsKept)
	}
	last, _ := reasons[len(reasons)-1].(string)
	if !utf8.ValidString(last) || len(last) > denialReasonBytes+len("…") || !strings.HasSuffix(last, "…") {
		t.Fatalf("clipped reason = %d bytes, valid=%v: want at most %d bytes plus an ellipsis", len(last), utf8.ValidString(last), denialReasonBytes)
	}
}
