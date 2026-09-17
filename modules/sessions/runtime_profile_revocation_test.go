// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// The CURRENT provider profile at the effect boundary, through authenticated
// HTTP, the real store, the native Runner and an owned Codex JSON-RPC child.
//
// ⛔ WHY THESE EXIST. assertRunAuthority proved four things and all four are
// about identity or generation, none about the profile's CURRENT LIFECYCLE. An
// independent review retired a live profile over its own admin route and measured
// every holder effect still crossing: a pending approval answered `approved`, a
// fenced interrupt sent turn/interrupt, a fenced text sent turn/start, and a
// fenced stop ended the process. Retirement was enforced at launch and at resume
// and nowhere in between.
//
// The shape is the reviewer's and is kept on purpose: count the METHODS the child
// actually saw, not the errors a helper returned. A refusal that still writes to
// the process is the defect, and only the child can report it.

// retireProfileOverHTTP retires a profile through the operator's own admin route,
// so the state under test is a COMMITTED transition and not a fixture poke at a
// row. That distinction is the whole point of the check: a hand-mutated record
// could pass a test the real lifecycle would fail.
func retireProfileOverHTTP(t *testing.T, h *harness, admin string, tenant model.TenantID, ref string) {
	t.Helper()
	retired := h.do(http.MethodPost, "/v1/m/sessions/provider-profiles/"+ref+"/retire",
		admin, tenantHdr(tenant))
	if retired.code != http.StatusOK || retired.body["state"] != ProfileRetired {
		t.Fatalf("retire live profile = %d %s", retired.code, retired.raw)
	}
}

// awaitApprovalDecision reads the decision the child recorded for the server
// request it sent. It is the only place that can say what actually crossed.
func awaitApprovalDecision(t *testing.T, record string) any {
	t.Helper()
	var decision any
	waitFor(t, "the child records the approval reply", func() bool {
		rec := readFixtureRecord(t, record)
		if len(rec.Replies) == 0 {
			return false
		}
		var reply map[string]any
		if json.Unmarshal(rec.Replies[0], &reply) != nil {
			return false
		}
		decision = reply["decision"]
		return true
	})
	return decision
}

// TestCodexRuntimeRetiredProfileRevokesApprovalAndEveryHolderControl is the
// reviewer's discriminator kept permanent.
//
// It opens a work-fenced turn whose LEGACY approval carries no wire `turnId` —
// the F1 positive, asserted first, so a retirement fix can never be built on top
// of a later `currentTurn` read — retires the profile while that approval waits
// in the governed gate, and then requires every holder effect to refuse without
// the child hearing anything more.
func TestCodexRuntimeRetiredProfileRevokesApprovalAndEveryHolderControl(t *testing.T) {
	gate := blockingApprovalGate{
		entered: make(chan ProviderApprovalRequest, 1),
		release: make(chan struct{}),
	}
	m, h, admin, tenant, prof := codexHTTPHarness(t, "retired-profile-revocation",
		WithProviderApprovalGate(gate))
	record := setCodexFixture(t, prof, codexFixture{
		ThreadID: "thread-retired-revocation", Account: "apikey",
		ApprovalOnTurn: codexReqLegacyExecApproval,
	})
	runRef, live := codexHTTPRun(t, m, h, admin, tenant, prof)
	fence := bindRunToFreshWorkLease(t, m, h, tenant, runRef, live.claim.SID)
	inputPath := "/v1/m/sessions/runs/" + runRef + "/input"
	interruptPath := "/v1/m/sessions/runs/" + runRef + "/interrupt"
	stopPath := "/v1/m/sessions/runs/" + runRef + "/stop"

	opened := h.doJSON(http.MethodPost, inputPath, admin, map[string]any{
		"text": "request the legacy approval", "work_lease_fence": fence,
	}, tenantHdr(tenant))
	if opened.code != http.StatusAccepted {
		t.Fatalf("open the approval turn = %d %s", opened.code, opened.raw)
	}
	// THE F1 POSITIVE. execCommandApproval carries no turnId on the wire, so the
	// only correct binding is the turn the pump observed on arrival. If a later fix
	// ever answered with "whatever turn is current now", this assertion is what
	// notices.
	select {
	case req := <-gate.entered:
		if req.TurnID != "turn-1" {
			t.Fatalf("a legacy approval with no wire turnId bound to %q, want the captured turn-1", req.TurnID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the approval never reached the governed authority")
	}

	retireProfileOverHTTP(t, h, admin, tenant, prof.Ref)
	close(gate.release)

	// The approval must be cancelled with THIS METHOD'S OWN codec. For the legacy
	// ReviewDecision pair that is `abort`; answering "cancel" would be the v2
	// vocabulary on a surface that does not have it.
	if decision := awaitApprovalDecision(t, record); decision != "abort" {
		t.Errorf("approval released after retirement crossed as decision=%v, want abort", decision)
	}

	interrupts := countMethod(readFixtureRecord(t, record).Methods, codexMethodTurnInterrupt)
	starts := countMethod(readFixtureRecord(t, record).Methods, codexMethodTurnStart)
	// Counted BEFORE, not asserted to be zero: the turn that opened this approval
	// was a legitimate accepted input under the then-current profile, and a test
	// that demanded zero would be measuring the setup instead of the revocation.
	settledBefore := map[string]int{}
	for _, event := range []string{workInputAccepted, workInterruptAccepted, workStopConfirmed} {
		settledBefore[event] = countNamedRunEvents(t, h.st, tenant, runRef, event)
	}

	interrupted := h.doJSON(http.MethodPost, interruptPath, admin, map[string]any{
		"work_lease_fence": fence,
	}, tenantHdr(tenant))
	if interrupted.code < 400 {
		t.Errorf("retired-profile fenced interrupt = %d %s, want a refusal", interrupted.code, interrupted.raw)
	}
	text := h.doJSON(http.MethodPost, inputPath, admin, map[string]any{
		"text": "must be revoked", "work_lease_fence": fence,
	}, tenantHdr(tenant))
	if text.code < 400 {
		t.Errorf("retired-profile fenced text = %d %s, want a refusal", text.code, text.raw)
	}
	pid := live.proc.PID()
	stopped := h.doJSON(http.MethodPost, stopPath, admin, map[string]any{
		"work_lease_fence": fence, "reason": "must be revoked",
	}, tenantHdr(tenant))
	if stopped.code < 400 {
		t.Errorf("retired-profile fenced stop = %d %s, want a refusal", stopped.code, stopped.raw)
	}

	// NOT ONE ADDED CHILD METHOD. This is the assertion that cannot be satisfied by
	// returning an error after the frame has gone out.
	after := readFixtureRecord(t, record)
	if got := countMethod(after.Methods, codexMethodTurnInterrupt); got != interrupts {
		t.Errorf("a revoked interrupt crossed the process boundary: turn/interrupt %d -> %d", interrupts, got)
	}
	if got := countMethod(after.Methods, codexMethodTurnStart); got != starts {
		t.Errorf("a revoked text crossed the process boundary: turn/start %d -> %d", starts, got)
	}
	if !processRunning(pid) {
		t.Error("a REFUSED stop ended the owned process")
	}
	// NOR ONE NEW SUCCESSFUL CONTROL EVENT. A refusal that still settled its work
	// generation would leave the ledger claiming an effect that never happened.
	for event, before := range settledBefore {
		if got := countNamedRunEvents(t, h.st, tenant, runRef, event); got != before {
			t.Errorf("a revoked control settled a new %s event: %d -> %d", event, before, got)
		}
	}

	// And the runtime's own cleanup is a different power: revoking the holder must
	// never leave a child nobody can reap.
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := m.Stop(cleanupCtx); err != nil {
		t.Fatalf("runtime shutdown after holder revocation: %v", err)
	}
	waitFor(t, "the runtime reaped its own child after revoking the holder", func() bool {
		return !processRunning(pid)
	})
}

// TestCodexRuntimeRetiredProfileRevokesTheUNFENCEDDriverControls is the other
// half of the control surface. The fenced plane is exercised above; a run with no
// durable work stamp reaches the same child through the LEGACY routes, and those
// answer to the same profile.
//
// It is a separate test because the two planes select different code paths above
// assertRunAuthority, and a correction that only reached one of them would look
// complete from either test alone.
func TestCodexRuntimeRetiredProfileRevokesTheUNFENCEDDriverControls(t *testing.T) {
	m, h, admin, tenant, prof := codexHTTPHarness(t, "retired-profile-unfenced")
	record := setCodexFixture(t, prof, codexFixture{
		ThreadID: "thread-retired-unfenced", Account: "apikey",
	})
	runRef, live := codexHTTPRun(t, m, h, admin, tenant, prof)
	inputPath := "/v1/m/sessions/runs/" + runRef + "/input"
	ctx := context.Background()

	// Positive first: with the profile current, the unfenced routes work.
	opened := h.doJSON(http.MethodPost, inputPath, admin, map[string]any{
		"text": "unfenced turn",
	}, tenantHdr(tenant))
	if opened.code != http.StatusAccepted {
		t.Fatalf("unfenced text on a CURRENT profile = %d %s", opened.code, opened.raw)
	}
	waitFor(t, "the turn is active on the child", func() bool { return live.session.ActiveTurn() != "" })
	starts := countMethod(readFixtureRecord(t, record).Methods, codexMethodTurnStart)

	retireProfileOverHTTP(t, h, admin, tenant, prof.Ref)

	if err := m.sendTextInput(ctx, tenant, runRef, "must be revoked"); err == nil {
		t.Error("the unfenced text route must refuse once the profile is retired")
	}
	if _, err := m.interruptRun(ctx, tenant, runRef, "user:operator", model.ActorUser); err == nil {
		t.Error("the unfenced interrupt must refuse once the profile is retired")
	}
	pid := live.proc.PID()
	if _, err := m.stopRun(ctx, tenant, runRef, "user:operator", model.ActorUser); err == nil {
		t.Error("the unfenced stop must refuse once the profile is retired")
	}

	after := readFixtureRecord(t, record)
	if got := countMethod(after.Methods, codexMethodTurnStart); got != starts {
		t.Errorf("a revoked unfenced text crossed the child: turn/start %d -> %d", starts, got)
	}
	if got := countMethod(after.Methods, codexMethodTurnInterrupt); got != 0 {
		t.Errorf("a revoked unfenced interrupt crossed the child: turn/interrupt = %d", got)
	}
	if !processRunning(pid) {
		t.Error("a refused unfenced stop ended the owned process")
	}
	// The turn the child is still executing was never cancelled, which is the
	// honest consequence: revoking the holder does not reach into the provider.
	if live.session.ActiveTurn() == "" {
		t.Error("a refused interrupt cancelled the turn anyway")
	}
}

// TestCodexRuntimeActiveProfileAllowsApprovalAndEveryHolderControl is the
// positive control the discriminator above needs to mean anything: with the
// profile left ACTIVE, exactly the same sequence grants the approval and every
// control reaches the child.
func TestCodexRuntimeActiveProfileAllowsApprovalAndEveryHolderControl(t *testing.T) {
	gate := blockingApprovalGate{
		entered: make(chan ProviderApprovalRequest, 1),
		release: make(chan struct{}),
	}
	m, h, admin, tenant, prof := codexHTTPHarness(t, "active-profile-positive",
		WithProviderApprovalGate(gate))
	record := setCodexFixture(t, prof, codexFixture{
		ThreadID: "thread-active-positive", Account: "apikey",
		ApprovalOnTurn: codexReqLegacyExecApproval,
	})
	runRef, live := codexHTTPRun(t, m, h, admin, tenant, prof)
	fence := bindRunToFreshWorkLease(t, m, h, tenant, runRef, live.claim.SID)
	inputPath := "/v1/m/sessions/runs/" + runRef + "/input"

	opened := h.doJSON(http.MethodPost, inputPath, admin, map[string]any{
		"text": "request the legacy approval", "work_lease_fence": fence,
	}, tenantHdr(tenant))
	if opened.code != http.StatusAccepted {
		t.Fatalf("open the approval turn = %d %s", opened.code, opened.raw)
	}
	select {
	case req := <-gate.entered:
		if req.TurnID != "turn-1" {
			t.Fatalf("legacy approval bound to %q, want turn-1", req.TurnID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the approval never reached the governed authority")
	}
	close(gate.release)

	// The gate allowed it and the profile is current, so the grant crosses.
	if decision := awaitApprovalDecision(t, record); decision != "approved" {
		t.Fatalf("approval under a CURRENT profile crossed as decision=%v, want approved", decision)
	}

	starts := countMethod(readFixtureRecord(t, record).Methods, codexMethodTurnStart)
	interrupted := h.doJSON(http.MethodPost,
		"/v1/m/sessions/runs/"+runRef+"/interrupt", admin,
		map[string]any{"work_lease_fence": fence}, tenantHdr(tenant))
	if interrupted.code != http.StatusOK {
		t.Fatalf("current-profile fenced interrupt = %d %s", interrupted.code, interrupted.raw)
	}
	waitFor(t, "the child cancelled its turn", func() bool {
		return countMethod(readFixtureRecord(t, record).Methods, codexMethodTurnInterrupt) == 1
	})
	text := h.doJSON(http.MethodPost, inputPath, admin, map[string]any{
		"text": "still speakable", "work_lease_fence": fence,
	}, tenantHdr(tenant))
	if text.code != http.StatusAccepted {
		t.Fatalf("current-profile fenced text = %d %s", text.code, text.raw)
	}
	waitFor(t, "a new turn started on the same child", func() bool {
		return countMethod(readFixtureRecord(t, record).Methods, codexMethodTurnStart) == starts+1
	})
	pid := live.proc.PID()
	stopped := h.doJSON(http.MethodPost, "/v1/m/sessions/runs/"+runRef+"/stop", admin,
		map[string]any{"work_lease_fence": fence, "reason": "positive control"}, tenantHdr(tenant))
	if stopped.code != http.StatusOK || stopped.body["state"] != stateStopped {
		t.Fatalf("current-profile fenced stop = %d %s", stopped.code, stopped.raw)
	}
	waitFor(t, "the fenced stop ended the owned process", func() bool { return !processRunning(pid) })
}

// TestCodexRuntimeDisabledLabelAndAuthSourceDoNotRevokeALiveChild is the
// compatibility half, and it is not decoration: the smallest wrong version of
// this correction is "refuse unless the profile is active", which would silently
// turn three unrelated lifecycle facts into revocation directions.
//
//   - `disabled` closes the door to a NEW launch or resume; it deliberately does
//     not kill what is already through it.
//   - a rename is a label.
//   - `auth_source` is re-decided for the NEXT launch; the running child keeps
//     the source its own launch was authorised under.
func TestCodexRuntimeDisabledLabelAndAuthSourceDoNotRevokeALiveChild(t *testing.T) {
	m, h, admin, tenant, prof := codexHTTPHarness(t, "profile-compat-directions")
	record := setCodexFixture(t, prof, codexFixture{
		ThreadID: "thread-profile-compat", Account: "apikey",
	})
	runRef, live := codexHTTPRun(t, m, h, admin, tenant, prof)
	fence := bindRunToFreshWorkLease(t, m, h, tenant, runRef, live.claim.SID)
	inputPath := "/v1/m/sessions/runs/" + runRef + "/input"

	disabled := ProfileDisabled
	renamed := "renamed while a child is live"
	patched := h.doJSON(http.MethodPatch, "/v1/m/sessions/provider-profiles/"+prof.Ref, admin,
		map[string]any{
			"state": disabled, "display_name": renamed,
			"auth_source": AuthSourceManagedInjection,
		}, tenantHdr(tenant))
	if patched.code != http.StatusOK || patched.body["state"] != ProfileDisabled {
		t.Fatalf("disable + rename + re-authorize = %d %s", patched.code, patched.raw)
	}

	// The live child answers to none of those three.
	starts := countMethod(readFixtureRecord(t, record).Methods, codexMethodTurnStart)
	text := h.doJSON(http.MethodPost, inputPath, admin, map[string]any{
		"text": "a disabled profile does not revoke me", "work_lease_fence": fence,
	}, tenantHdr(tenant))
	if text.code != http.StatusAccepted {
		t.Fatalf("fenced text under a DISABLED profile = %d %s, want it still accepted",
			text.code, text.raw)
	}
	waitFor(t, "the text reached the child", func() bool {
		return countMethod(readFixtureRecord(t, record).Methods, codexMethodTurnStart) == starts+1
	})
	interrupted := h.doJSON(http.MethodPost, "/v1/m/sessions/runs/"+runRef+"/interrupt", admin,
		map[string]any{"work_lease_fence": fence}, tenantHdr(tenant))
	if interrupted.code != http.StatusOK {
		t.Fatalf("fenced interrupt under a DISABLED profile = %d %s", interrupted.code, interrupted.raw)
	}
	// The persisted authorization snapshot is the launch's, not today's.
	if got := live.profile.AuthSource; got != AuthSourceAccountHome {
		t.Errorf("the live launch's auth source became %q; it must stay the one it launched under", got)
	}
	// And retirement, the one direction that IS revocation, still bites afterwards.
	retireProfileOverHTTP(t, h, admin, tenant, prof.Ref)
	revoked := h.doJSON(http.MethodPost, inputPath, admin, map[string]any{
		"text": "now revoked", "work_lease_fence": fence,
	}, tenantHdr(tenant))
	if revoked.code < 400 {
		t.Fatalf("fenced text after retirement = %d %s, want a refusal", revoked.code, revoked.raw)
	}
	_ = m
}
