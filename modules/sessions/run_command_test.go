// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func userFrame(text string) map[string]any {
	return map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": text}}
}

// endTurn delivers the tool's result for the open turn and waits until its row closed,
// so the next user frame is not held behind it.
func endTurn(t *testing.T, h *harness, fr *fakeRunner, tenant model.TenantID, run string) {
	t.Helper()
	fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: []byte(`{"type":"result","subtype":"success"}`)}
	waitFor(t, "turn closed on "+run, func() bool { return lastTurnOf(t, h.m, tenant, run).State == "completed" })
}

// A retried send carries the same command_id: the agent runs once and the retry gets
// the first receipt. The same id with another input is a conflict, and a malformed or
// oversized id is refused before anything is sent.
func TestRunInputCommandIDRunsTheAgentOnce(t *testing.T) {
	fr := &fakeRunner{initSID: dupID}
	h, admin, tenant, profile, _ := profiledHTTP(t, fr, &testClock{now: baseTime})
	run, _ := launchProfiledHTTP(t, h, admin, tenant, profile)
	path := "/v1/m/sessions/runs/" + run + "/input"
	id := model.NewID().String()
	sent := fr.lastProc().sentCount()

	first := h.doJSON("POST", path, admin, map[string]any{"message": userFrame("hello"), "command_id": id}, tenantHdr(tenant))
	if first.code != http.StatusAccepted || first.body["command_id"] != id {
		t.Fatalf("first send = %d %s", first.code, first.raw)
	}
	retry := h.doJSON("POST", path, admin, map[string]any{"message": userFrame("hello"), "command_id": id}, tenantHdr(tenant))
	if retry.code != http.StatusAccepted || retry.raw != first.raw {
		t.Fatalf("retry = %d %s, want the first receipt %s", retry.code, retry.raw, first.raw)
	}
	if got := fr.lastProc().sentCount() - sent; got != 1 {
		t.Fatalf("frames sent for one command = %d, want 1", got)
	}
	if other := h.doJSON("POST", path, admin, map[string]any{"message": userFrame("something else"), "command_id": id}, tenantHdr(tenant)); other.code != http.StatusConflict {
		t.Fatalf("same command_id, other input = %d %s, want 409", other.code, other.raw)
	}
	for _, bad := range []string{"not-a-uuid", strings.ToUpper(id), id + "0", strings.Repeat("a", 4096)} {
		if r := h.doJSON("POST", path, admin, map[string]any{"message": userFrame("hello"), "command_id": bad}, tenantHdr(tenant)); r.code != http.StatusBadRequest {
			t.Fatalf("command_id %.40q = %d %s, want 400", bad, r.code, r.raw)
		}
	}
	if got := fr.lastProc().sentCount() - sent; got != 1 {
		t.Fatalf("frames sent after refusals = %d, want 1", got)
	}
}

// The dedupe is per tenant, per run and per person: another person who sends the same
// command_id on the same run runs their own turn and never receives, or suppresses,
// the first person's.
func TestRunInputCommandIDOfAnotherPersonCannotSuppressATurn(t *testing.T) {
	fr := &fakeRunner{initSID: dupID}
	h, admin, tenant, profile, _ := profiledHTTP(t, fr, &testClock{now: baseTime})
	h.m.rt.SteerOthers = steerOthersLicensed
	run, _ := launchProfiledHTTP(t, h, admin, tenant, profile)
	other := h.userToken(admin, tenant, "colleague@command.io", "admin")
	path := "/v1/m/sessions/runs/" + run + "/input"
	id := model.NewID().String()
	sent := fr.lastProc().sentCount()

	if r := h.doJSON("POST", path, other, map[string]any{"message": userFrame("hello"), "command_id": id}, tenantHdr(tenant)); r.code != http.StatusAccepted {
		t.Fatalf("colleague's send = %d %s", r.code, r.raw)
	}
	endTurn(t, h, fr, tenant, run)
	if r := h.doJSON("POST", path, admin, map[string]any{"message": userFrame("hello"), "command_id": id}, tenantHdr(tenant)); r.code != http.StatusAccepted {
		t.Fatalf("launcher's send with the colleague's id = %d %s", r.code, r.raw)
	}
	if got := fr.lastProc().sentCount() - sent; got != 2 {
		t.Fatalf("frames sent = %d, want 2: the launcher's turn was suppressed by another person's command_id", got)
	}
}

// unsentInput refuses before a byte moves (attempted false), as a driver does when it
// declines an input outright.
type unsentInput struct {
	completingSession
	refuse bool
}

func (s *unsentInput) Input(ctx context.Context, text string) (bool, error) {
	if s.refuse {
		return false, errors.New("test: refused before a byte moved")
	}
	return s.completingSession.Input(ctx, text)
}

// A send refused before anything reached the tool reserves nothing: the caller retries
// the same input under the same command_id and it is sent, once. Only then is a repeat
// the duplicate.
func TestARefusedSendDoesNotReserveItsCommandID(t *testing.T) {
	ctx := context.Background()
	session := &unsentInput{completingSession: completingSession{newFakeTurnSession()}, refuse: true}
	m, tenant, ref, lr := driverRun(t, session)
	command := newRunCommand("actor", model.NewID().String(), []byte("hello"))
	cctx := withRunCommand(ctx, command)

	if attempted, err := m.driverInputAdmitted(cctx, lr, "hello", lr.launchID, lr.claim.SID); err == nil || attempted {
		t.Fatalf("the refused send = %v, %v, want a refusal before any byte moved", attempted, err)
	}
	if err := m.commandSeen(ctx, tenant, ref, command); err != nil {
		t.Fatalf("a retry after a refused send is answered %v, want it sent: nothing reached the tool", err)
	}
	session.refuse = false
	if attempted, err := m.driverInputAdmitted(cctx, lr, "hello", lr.launchID, lr.claim.SID); err != nil || !attempted {
		t.Fatalf("the retry = %v, %v, want it sent", attempted, err)
	}
	session.mu.Lock()
	sent := session.inputs
	session.mu.Unlock()
	if sent != 1 {
		t.Fatalf("inputs that reached the tool = %d, want 1", sent)
	}
	if err := m.commandSeen(ctx, tenant, ref, command); !errors.Is(err, errCommandDuplicate) {
		t.Fatalf("a repeat after the sent retry = %v, want the duplicate", err)
	}
}

// The row a checked input commits with its admission is settled when no send takes it
// up (the input is refused after the admission committed); it drops its command_id
// with it, so the retry of that input is sent, not answered as accepted.
func TestAnAdmittedTurnNoSendTookUpDoesNotReserveItsCommandID(t *testing.T) {
	ctx := context.Background()
	m, tenant, ref, lr := driverRun(t, completingSession{newFakeTurnSession()})
	command := newRunCommand("actor", model.NewID().String(), []byte("hello"))
	cctx := withRunCommand(ctx, command)

	if err := m.Data.Mutate(cctx, tenant, func(sc store.Scope) error {
		return m.admitRunTurn(cctx, sc, lr, RuntimeInputText, nil)
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.commandSeen(ctx, tenant, ref, command); !errors.Is(err, errCommandDuplicate) {
		t.Fatalf("the admitted row = %v, want it to carry the command", err)
	}
	m.releaseAdmittedTurn(lr)
	if turn := lastTurnOf(t, m, tenant, ref); turn.State != "failed" {
		t.Fatalf("the released turn = %+v, want failed", turn)
	}
	if err := m.commandSeen(ctx, tenant, ref, command); err != nil {
		t.Fatalf("a retry after a refused admitted send is answered %v, want it sent", err)
	}
}
