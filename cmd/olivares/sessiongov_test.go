// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/finops"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/modules/knowledge"
	"github.com/olivaresai/olivares/modules/sessions"
)

// sessiongov_test.go exercises the cmd-side governance adapters that wire an
// operated session through the existing controls: the budget/HITL/PEP LaunchGate, the
// kill-switch StopGate, and the ledger-anchored I/O Recorder.

// --- fakes ------------------------------------------------------------------

type fakeBudget struct {
	chk finops.BudgetCheck
	err error
}

func (f fakeBudget) CheckBudget(context.Context, model.TenantID, finops.SpendDims) (finops.BudgetCheck, error) {
	return f.chk, f.err
}

func (f fakeBudget) CheckSpendLimit(context.Context, model.TenantID, string, []string) (finops.SpendLimitCheck, error) {
	return finops.SpendLimitCheck{Allowed: true}, nil
}

type fakeSessionContextPolicy struct {
	pol   knowledge.EffectivePolicy
	err   error
	calls int
	last  knowledge.ContextPolicyQuery
}

func (f *fakeSessionContextPolicy) Apply(_ context.Context, _ model.TenantID, q knowledge.ContextPolicyQuery) (knowledge.EffectivePolicy, error) {
	f.calls++
	f.last = q
	return f.pol, f.err
}

type fakeOpener struct {
	status string
	err    error
	calls  int
	// consume* back the single-use spend. The zero value grants (granted=true,
	// no replay, no error), so a fake reporting nbApproved still allows — tests that
	// need a replay/deny set these explicitly.
	consumeReplay bool
	consumeErr    error
	consumeCalls  int
}

func (f *fakeOpener) gateOnce(_ context.Context, _ model.TenantID, _, _, _, _, _, _ string) (string, string, string, error) {
	f.calls++
	return "appr-1", f.status, "ph", f.err
}

func (f *fakeOpener) consumeApproval(_ context.Context, _ model.TenantID, _, _, _ string) (bool, bool, error) {
	f.consumeCalls++
	if f.consumeErr != nil {
		return false, false, f.consumeErr
	}
	if f.consumeReplay {
		return false, true, nil
	}
	return true, false, nil
}

type fakeKillSwitch struct {
	st  governance.StopState
	err error
}

func (f fakeKillSwitch) KillSwitchState(context.Context, model.TenantID) (governance.StopState, error) {
	return f.st, f.err
}

// --- LaunchGate: budget ------------------------------------------------------

func TestSessionLaunchGate_BudgetDeniesWithStatus(t *testing.T) {
	cases := []struct {
		action     string
		wantStatus int
	}{
		{"block", 402},
		{"throttle", 429},
	}
	for _, tc := range cases {
		g := &sessionLaunchGate{fin: fakeBudget{chk: finops.BudgetCheck{Allowed: false, Action: tc.action}}, recordAvailable: true, log: slog.Default()}
		dec, err := g.Authorize(context.Background(), "t1", sessions.LaunchIntent{Transport: sessions.TransportStreamJSON, PermissionMode: "default"})
		if err != nil {
			t.Fatalf("Authorize(%s): %v", tc.action, err)
		}
		if dec.Allowed {
			t.Fatalf("%s: budget over cap must deny the launch", tc.action)
		}
		if dec.DeniedStatus != tc.wantStatus {
			t.Fatalf("%s: want status %d, got %d", tc.action, tc.wantStatus, dec.DeniedStatus)
		}
		if bytes.Contains([]byte(dec.Reason), []byte("$")) {
			t.Fatalf("%s: deny reason must be money-free, got %q", tc.action, dec.Reason)
		}
	}
}

func TestSessionLaunchGate_BudgetFailsOpen(t *testing.T) {
	g := &sessionLaunchGate{fin: fakeBudget{err: errors.New("finops down")}, recordAvailable: true, log: slog.Default()}
	dec, err := g.Authorize(context.Background(), "t1", sessions.LaunchIntent{Transport: sessions.TransportStreamJSON, PermissionMode: "default"})
	if err != nil || !dec.Allowed {
		t.Fatalf("a FinOps read error must fail OPEN (allow), got allowed=%v err=%v", dec.Allowed, err)
	}
}

// --- LaunchGate: CRITICAL determination + HITL -------------------------------

func TestIsCriticalLaunch(t *testing.T) {
	cases := []struct {
		name string
		in   sessions.LaunchIntent
		want bool
	}{
		{"bypass", sessions.LaunchIntent{PermissionMode: "bypassPermissions"}, true},
		{"dontAsk", sessions.LaunchIntent{PermissionMode: "dontAsk"}, true},
		{"classified-rw", sessions.LaunchIntent{PermissionMode: "default", WorkspaceClassified: true, WorkspaceReadWrite: true}, true},
		{"classified-ro", sessions.LaunchIntent{PermissionMode: "default", WorkspaceClassified: true, WorkspaceReadWrite: false}, false},
		{"plain", sessions.LaunchIntent{PermissionMode: "default"}, false},
		{"unclassified-rw", sessions.LaunchIntent{PermissionMode: "acceptEdits", WorkspaceReadWrite: true}, false},
	}
	for _, tc := range cases {
		if got, _ := isCriticalLaunch(tc.in); got != tc.want {
			t.Errorf("%s: isCriticalLaunch=%v want %v", tc.name, got, tc.want)
		}
	}
}

func TestSessionLaunchGate_CriticalHITL(t *testing.T) {
	critical := sessions.LaunchIntent{Transport: sessions.TransportStreamJSON, PermissionMode: "bypassPermissions", Actor: "user:u1"}

	// Pending approval ⇒ the launch is DENIED (with the ref) until approved out-of-band.
	op := &fakeOpener{status: nbPending}
	g := &sessionLaunchGate{bridge: op, recordAvailable: true, log: slog.Default()}
	dec, _ := g.Authorize(context.Background(), "t1", critical)
	if dec.Allowed {
		t.Fatal("a pending approval must deny a CRITICAL launch")
	}
	if op.calls != 1 {
		t.Fatalf("the HITL bridge should be consulted once, got %d", op.calls)
	}

	// Approved ⇒ allowed, and the run is recorded (privileged ⇒ RecordIO).
	g2 := &sessionLaunchGate{bridge: &fakeOpener{status: nbApproved}, recordAvailable: true, log: slog.Default()}
	dec2, _ := g2.Authorize(context.Background(), "t1", critical)
	if !dec2.Allowed {
		t.Fatal("an approved CRITICAL launch must be allowed")
	}
	if !dec2.RecordIO {
		t.Fatal("a CRITICAL/privileged launch must be flagged for I/O recording")
	}

	// Rejected/other ⇒ denied.
	g3 := &sessionLaunchGate{bridge: &fakeOpener{status: nbRejected}, recordAvailable: true, log: slog.Default()}
	if dec3, _ := g3.Authorize(context.Background(), "t1", critical); dec3.Allowed {
		t.Fatal("a rejected approval must deny the launch")
	}
}

// TestSessionLaunchGate_CriticalApprovalIsSingleUse pins-FIX §F4: a human approval of
// a PRIVILEGED (bypassPermissions) launch authorizes ONE launch, not a 24h-reusable pass —
// the same replay root as the tool-call PEP on a MORE privileged surface. The gate SPENDS
// the approval single-use; a launch reusing an already-consumed grant is denied would-replay;
// break-glass is NOT double-consumed (the engine recorded its one-shot use at grant time).
// RED before the fix (nbApproved allowed without any consume), GREEN after.
func TestSessionLaunchGate_CriticalApprovalIsSingleUse(t *testing.T) {
	critical := sessions.LaunchIntent{Transport: sessions.TransportStreamJSON, PermissionMode: "bypassPermissions", Actor: "user:u1"}

	// Approved: the launch proceeds and SPENDS the approval exactly once.
	op := &fakeOpener{status: nbApproved}
	g := &sessionLaunchGate{bridge: op, recordAvailable: true, log: slog.Default()}
	if dec, _ := g.Authorize(context.Background(), "t1", critical); !dec.Allowed {
		t.Fatal("an approved privileged launch must be allowed on first use")
	}
	if op.consumeCalls != 1 {
		t.Fatalf("a privileged launch must SPEND the approval single-use, got %d consume calls", op.consumeCalls)
	}

	// Replay: the approval was already consumed ⇒ the launch is denied would-replay.
	opReplay := &fakeOpener{status: nbApproved, consumeReplay: true}
	g2 := &sessionLaunchGate{bridge: opReplay, recordAvailable: true, log: slog.Default()}
	if dec, _ := g2.Authorize(context.Background(), "t1", critical); dec.Allowed {
		t.Fatal("a privileged launch reusing an already-consumed approval must be denied (would-replay)")
	}

	// Break-glass: allowed, but NOT double-consumed (the engine already recorded the use).
	opBG := &fakeOpener{status: nbBreakGlass}
	g3 := &sessionLaunchGate{bridge: opBG, recordAvailable: true, log: slog.Default()}
	if dec, _ := g3.Authorize(context.Background(), "t1", critical); !dec.Allowed {
		t.Fatal("a break-glass privileged launch must be allowed")
	}
	if opBG.consumeCalls != 0 {
		t.Fatalf("break-glass must NOT double-consume the approval, got %d consume calls", opBG.consumeCalls)
	}
}

func TestSessionLaunchGate_CriticalDenyClosed(t *testing.T) {
	critical := sessions.LaunchIntent{Transport: sessions.TransportStreamJSON, PermissionMode: "bypassPermissions"}

	// No HITL bridge ⇒ a CRITICAL launch is denied (deny-closed).
	g := &sessionLaunchGate{bridge: nil, recordAvailable: true, log: slog.Default()}
	if dec, _ := g.Authorize(context.Background(), "t1", critical); dec.Allowed {
		t.Fatal("a CRITICAL launch with no HITL bridge must be denied (deny-closed)")
	}

	// No recorder ⇒ a CRITICAL launch is denied (privileged sessions must be recordable).
	g2 := &sessionLaunchGate{bridge: &fakeOpener{status: nbApproved}, recordAvailable: false, log: slog.Default()}
	if dec, _ := g2.Authorize(context.Background(), "t1", critical); dec.Allowed {
		t.Fatal("a CRITICAL launch that cannot be recorded must be denied (deny-closed)")
	}
}

// --- LaunchGate: non-critical allow + PEP env --------------------------------

func TestSessionLaunchGate_AllowsWithPEPEnv(t *testing.T) {
	prov := &sessionPEPProvisioner{url: "http://127.0.0.1:8447/", mint: func(context.Context, model.TenantID, string) (string, error) {
		return "bearer-xyz", nil
	}}
	g := &sessionLaunchGate{pep: prov, recordAvailable: true, log: slog.Default()}
	dec, err := g.Authorize(context.Background(), "t1", sessions.LaunchIntent{
		Transport: sessions.TransportStreamJSON, PermissionMode: "default", AgentRef: "agent:a1",
	})
	if err != nil || !dec.Allowed {
		t.Fatalf("a non-critical launch must be allowed, got allowed=%v err=%v", dec.Allowed, err)
	}
	if dec.RecordIO {
		t.Fatal("a non-critical, non-opted-in launch must not be flagged for recording")
	}
	want := map[string]string{
		envHookPEPURL:    "http://127.0.0.1:8447/",
		envHookPEPTenant: "t1",
		envHookPEPAgent:  "agent:a1",
		envHookPEPToken:  "bearer-xyz",
	}
	got := map[string]string{}
	for _, e := range dec.InjectEnv {
		got[e.Name] = e.Value
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("injected PEP env %s = %q, want %q", k, got[k], v)
		}
	}

	// Opt-in recording on a non-critical run.
	dec2, _ := g.Authorize(context.Background(), "t1", sessions.LaunchIntent{
		Transport: sessions.TransportStreamJSON, PermissionMode: "default", RecordRequested: true,
	})
	if !dec2.RecordIO {
		t.Fatal("RecordRequested must flag a non-critical run for recording")
	}
}

func TestSessionLaunchGate_ContextPolicyInjectsAndSummarizes(t *testing.T) {
	cp := &fakeSessionContextPolicy{pol: knowledge.EffectivePolicy{
		MaxContextTokens: 2048,
		Strategy:         "summarize",
		WinningScope:     "agent:agent:a1",
	}}
	g := &sessionLaunchGate{contextPolicy: cp, recordAvailable: true, log: slog.Default()}
	dec, err := g.Authorize(context.Background(), "t1", sessions.LaunchIntent{
		Transport: sessions.TransportStreamJSON, PermissionMode: "default",
		AgentRef: "agent:a1", WorkspaceRef: "workspace:w1", Model: "claude-opus-4-8",
	})
	if err != nil || !dec.Allowed {
		t.Fatalf("context-allowed launch must be allowed, got allowed=%v err=%v", dec.Allowed, err)
	}
	got := map[string]string{}
	for _, e := range dec.InjectEnv {
		got[e.Name] = e.Value
	}
	if got[envContextMaxTokens] != "2048" {
		t.Fatalf("%s = %q, want 2048", envContextMaxTokens, got[envContextMaxTokens])
	}
	if got[envContextStrategy] != "summarize" {
		t.Fatalf("%s = %q, want summarize", envContextStrategy, got[envContextStrategy])
	}
	wantSummary := "ctx max=2048 strategy=summarize scope=agent:agent:a1"
	if dec.ContextPolicySummary != wantSummary {
		t.Fatalf("ContextPolicySummary = %q, want %q", dec.ContextPolicySummary, wantSummary)
	}
	if cp.calls != 1 {
		t.Fatalf("context policy resolver calls = %d, want 1", cp.calls)
	}
	if cp.last.AgentRef != "agent:a1" || cp.last.WorkspaceRef != "workspace:w1" || cp.last.Model != "claude-opus-4-8" {
		t.Fatalf("context policy query = %+v, want agent/workspace/model refs", cp.last)
	}
	if cp.last.Principal.UserID != "" || cp.last.SessionRef != "" || cp.last.KBRef != "" {
		t.Fatalf("launch-time context policy query must not infer unavailable scopes, got %+v", cp.last)
	}
}

func TestSessionLaunchGate_ContextPolicyDenyBlocksLaunch(t *testing.T) {
	cp := &fakeSessionContextPolicy{pol: knowledge.EffectivePolicy{Deny: true}}
	g := &sessionLaunchGate{contextPolicy: cp, recordAvailable: true, log: slog.Default()}
	dec, err := g.Authorize(context.Background(), "t1", sessions.LaunchIntent{
		Transport: sessions.TransportStreamJSON, PermissionMode: "default", AgentRef: "agent:a1",
	})
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if dec.Allowed {
		t.Fatal("a denying context policy must block the launch")
	}
	if dec.DeniedStatus != http.StatusForbidden {
		t.Fatalf("DeniedStatus = %d, want %d", dec.DeniedStatus, http.StatusForbidden)
	}
	if dec.Reason != "context policy forbids this agent" {
		t.Fatalf("Reason = %q", dec.Reason)
	}
}

func TestSessionLaunchGate_ContextPolicyFailsOpen(t *testing.T) {
	cp := &fakeSessionContextPolicy{err: errors.New("knowledge unavailable")}
	g := &sessionLaunchGate{contextPolicy: cp, recordAvailable: true, log: slog.Default()}
	dec, err := g.Authorize(context.Background(), "t1", sessions.LaunchIntent{
		Transport: sessions.TransportStreamJSON, PermissionMode: "default", AgentRef: "agent:a1",
	})
	if err != nil || !dec.Allowed {
		t.Fatalf("a context-policy read error must fail OPEN, got allowed=%v err=%v", dec.Allowed, err)
	}
	for _, e := range dec.InjectEnv {
		if e.Name == envContextMaxTokens || e.Name == envContextStrategy {
			t.Fatalf("context env must not be injected after resolver error, got %+v", dec.InjectEnv)
		}
	}
	if dec.ContextPolicySummary != "" {
		t.Fatalf("ContextPolicySummary after resolver error = %q, want empty", dec.ContextPolicySummary)
	}
}

// --- StopGate ----------------------------------------------------------------

func TestSessionStopGate(t *testing.T) {
	_, st, tenant := newSessionsStore(t)
	rec := newStopDenyRecorder(st, slog.Default())

	// Estate stop ⇒ stopped (records deny evidence).
	g := sessionStopGate{guard: fakeKillSwitch{st: governance.StopState{EstateStopped: true, EstateStopID: model.NewID()}}, rec: rec}
	dec, err := g.Check(context.Background(), tenant, sessions.StopDims{RunRef: "run-1", AgentRef: "agent:a1"})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !dec.Stopped {
		t.Fatal("an estate stop must stop the launch")
	}

	// Not stopped ⇒ allowed.
	g2 := sessionStopGate{guard: fakeKillSwitch{}, rec: rec}
	if dec2, _ := g2.Check(context.Background(), tenant, sessions.StopDims{}); dec2.Stopped {
		t.Fatal("no stop state must allow the launch")
	}

	// State read error ⇒ error returned (the module fails CLOSED on it).
	g3 := sessionStopGate{guard: fakeKillSwitch{err: errors.New("state unreadable")}, rec: rec}
	if _, err := g3.Check(context.Background(), tenant, sessions.StopDims{}); err == nil {
		t.Fatal("an unreadable stop state must return an error (deny-closed at the module)")
	}
}

// --- I/O recorder: ledger anchoring + verify --------------------------------

func TestSessionIORecorder_VerifyChain(t *testing.T) {
	_, st, tenant := newSessionsStore(t)
	ctx := context.Background()
	rec := newSessionIORecorder(st, slog.Default())
	const runRef = "run-rec-1"

	// Feed enough frames to cross a periodic anchor (64) plus a tail batch.
	const n = 70
	frames := make([]sessions.RecordedFrame, n)
	for i := 0; i < n; i++ {
		stream := streamStdoutT(i)
		data := []byte("frame-" + itoa(i))
		frames[i] = sessions.RecordedFrame{Seq: int64(i + 1), Stream: stream, Data: data}
		if err := rec.Record(ctx, tenant, runRef, frames[i]); err != nil {
			t.Fatalf("Record %d: %v", i, err)
		}
	}
	if err := rec.Finalize(ctx, tenant, runRef); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	// Recompute the expected chain tip from the SAME frames (an auditor holding the I/O
	// can prove it is the exact, unaltered stream).
	expected := make([]byte, sha256.Size)
	for _, f := range frames {
		sha := sha256.Sum256(f.Data)
		expected = sessionIOFrameHash(expected, runRef, f.Seq, f.Stream, int64(len(f.Data)), sha[:])
	}

	// WalkCanonical the ledger: collect the run's I/O anchors, assert the sealed anchor's
	// PayloadHash equals the recomputed tip and its canonical meta carries the run + seal.
	var sealedHash []byte
	var sealedMeta string
	anchors := 0
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		cw, ok := sc.Audit().(store.CanonicalWalker)
		if !ok {
			t.Fatal("audit log does not expose WalkCanonical")
		}
		return cw.WalkCanonical(ctx, 0, func(ev model.AuditEvent, metaCanonical string, _ []byte) error {
			if ev.Action != sessionIOAction || ev.TargetID != model.ID(runRef) {
				return nil
			}
			anchors++
			if bytes.Contains([]byte(metaCanonical), []byte("\"sealed\"")) {
				sealedHash = ev.PayloadHash
				sealedMeta = metaCanonical
			}
			return nil
		})
	}); err != nil {
		t.Fatalf("WalkCanonical: %v", err)
	}
	if anchors < 2 {
		t.Fatalf("expected at least a periodic + a seal anchor, got %d", anchors)
	}
	if sealedHash == nil {
		t.Fatal("no sealed I/O anchor found")
	}
	if !bytes.Equal(sealedHash, expected) {
		t.Fatalf("sealed anchor PayloadHash does not match the recomputed I/O chain tip\n got %x\nwant %x", sealedHash, expected)
	}
	if !bytes.Contains([]byte(sealedMeta), []byte(runRef)) {
		t.Fatalf("sealed anchor meta missing run_ref: %s", sealedMeta)
	}

	// The ledger chain itself verifies intact (hash/link/seq/sig).
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		rep, err := sc.Audit().Verify(ctx, 0)
		if err != nil {
			return err
		}
		if !rep.OK {
			t.Fatalf("ledger Verify reported a break at seq %d: %s", rep.BreakAt, rep.Reason)
		}
		return nil
	}); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

// TestSessionIORecorder_BoundaryAndInjectivity covers the exact-multiple-of-64 seal
// boundary (an empty tail batch must NOT emit a phantom from_seq=0/to_seq=stale anchor)
// and the hash chain's sensitivity to frame order/content (tamper-evidence).
func TestSessionIORecorder_BoundaryAndInjectivity(t *testing.T) {
	_, st, tenant := newSessionsStore(t)
	ctx := context.Background()
	rec := newSessionIORecorder(st, slog.Default())
	const runRef = "run-boundary"

	// Exactly two full periodic batches (128 = 2*64): Finalize lands on a boundary with
	// an EMPTY tail batch.
	const n = 2 * sessionIOAnchorEvery
	expected := make([]byte, sha256.Size)
	for i := 0; i < n; i++ {
		f := sessions.RecordedFrame{Seq: int64(i + 1), Stream: streamStdoutT(i), Data: []byte("f" + itoa(i))}
		if err := rec.Record(ctx, tenant, runRef, f); err != nil {
			t.Fatalf("Record %d: %v", i, err)
		}
		sha := sha256.Sum256(f.Data)
		expected = sessionIOFrameHash(expected, runRef, f.Seq, f.Stream, int64(len(f.Data)), sha[:])
	}
	if err := rec.Finalize(ctx, tenant, runRef); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	var sealedHash []byte
	var sealedMeta string
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		cw := sc.Audit().(store.CanonicalWalker)
		return cw.WalkCanonical(ctx, 0, func(ev model.AuditEvent, meta string, _ []byte) error {
			if ev.Action != sessionIOAction || ev.TargetID != model.ID(runRef) {
				return nil
			}
			// No anchor may claim from_seq=0 (the phantom-seal bug): an empty seal omits
			// the range entirely; a real batch starts at from_seq>=1.
			if bytes.Contains([]byte(meta), []byte("\"from_seq\":0")) {
				t.Fatalf("phantom anchor with from_seq=0: %s", meta)
			}
			if bytes.Contains([]byte(meta), []byte("\"sealed\"")) {
				sealedHash = ev.PayloadHash
				sealedMeta = meta
			}
			return nil
		})
	}); err != nil {
		t.Fatalf("WalkCanonical: %v", err)
	}
	if sealedHash == nil {
		t.Fatal("the end-of-recording seal anchor must always be emitted, even on a batch boundary")
	}
	if !bytes.Equal(sealedHash, expected) {
		t.Fatalf("seal PayloadHash must commit the full chain tip\n got %x\nwant %x", sealedHash, expected)
	}
	// The empty seal reports frames=0 and total_frames=n, with no stale range.
	if !bytes.Contains([]byte(sealedMeta), []byte("\"frames\":0")) || bytes.Contains([]byte(sealedMeta), []byte("\"to_seq\"")) {
		t.Fatalf("empty seal must report frames=0 and omit to_seq, got %s", sealedMeta)
	}

	// Injectivity / tamper-evidence: swapping two frames' content yields a different tip.
	tampered := make([]byte, sha256.Size)
	for i := 0; i < n; i++ {
		data := []byte("f" + itoa(i))
		if i == 10 { // swap the content of frame 10 with frame 11's
			data = []byte("f" + itoa(11))
		} else if i == 11 {
			data = []byte("f" + itoa(10))
		}
		sha := sha256.Sum256(data)
		tampered = sessionIOFrameHash(tampered, runRef, int64(i+1), streamStdoutT(i), int64(len(data)), sha[:])
	}
	if bytes.Equal(tampered, expected) {
		t.Fatal("a reordered/altered I/O stream must produce a different chain tip (tamper-evidence)")
	}

	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		rep, err := sc.Audit().Verify(ctx, 0)
		if err == nil && !rep.OK {
			t.Fatalf("ledger Verify break at %d: %s", rep.BreakAt, rep.Reason)
		}
		return err
	}); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

// --- managed-settings render -------------------------------------------------

func TestManagedSettingsRender(t *testing.T) {
	cmd := newAgentManagedSettingsCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--pep-command", "olivares claude-hook"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("render: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("render output is not JSON: %v\n%s", err, buf.String())
	}
	if doc["allowManagedHooksOnly"] != true {
		t.Fatal("rendered managed-settings must set allowManagedHooksOnly (anti-tamper)")
	}
	if !bytes.Contains(buf.Bytes(), []byte("PreToolUse")) || !bytes.Contains(buf.Bytes(), []byte("olivares claude-hook")) {
		t.Fatalf("rendered managed-settings must carry the PreToolUse PEP hook command:\n%s", buf.String())
	}
}

func TestManagedSettingsPinsGovernedGatewayBaseURL(t *testing.T) {
	cmd := newAgentManagedSettingsCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--gateway-base-url", "https://olivares-gateway.internal"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("render: %v", err)
	}
	var doc struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("render output is not JSON: %v", err)
	}
	if got := doc.Env["ANTHROPIC_BASE_URL"]; got != "https://olivares-gateway.internal" {
		t.Fatalf("managed ANTHROPIC_BASE_URL = %q; want governed gateway pin", got)
	}
}

func TestManagedSettingsWarnsOnUnmanagedBaseURLOverride(t *testing.T) {
	t.Setenv("ANTHROPIC_BASE_URL", "https://attacker.example/secret")
	cmd := newAgentManagedSettingsCmd()
	var out, stderr bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--gateway-base-url", "https://olivares-gateway.internal"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("WARNING")) || !bytes.Contains(stderr.Bytes(), []byte("ANTHROPIC_BASE_URL")) {
		t.Fatalf("unmanaged base URL override must emit a startup/deployment warning; stderr=%q", stderr.String())
	}
	if bytes.Contains(stderr.Bytes(), []byte("attacker.example")) {
		t.Fatal("warning must not disclose the override URL")
	}
}

// small helpers (avoid extra imports).
func streamStdoutT(i int) string {
	if i%2 == 0 {
		return "stdout"
	}
	return "stderr"
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
