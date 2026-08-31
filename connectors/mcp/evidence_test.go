// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

// Error-code note: the evidence-gate codes moved from -3201x to -3101x because MCP
// 2026-07-28 puts -32000..-32019 in a LEGACY sub-range new implementations SHOULD NOT
// use, and tells implementations to allocate their own codes OUTSIDE the reserved
// -32768..-32000 range entirely (basic/index.mdx:117-155). Trailing digits preserved.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/sdk"
)

// evidence_test.go — Stage 3 RED-first exploit suite for the q1-MCP
// evidence-mandatory tools/call gate. Written and observed FAILING against the
// historical fail-open code (post-hoc best-effort auditing, no claim, no
// canonicalization) BEFORE the enforcement was implemented; the assertions pin
// the frozen S5 contract (sdk/evidence.go): claim+anchor BEFORE effect,
// single-use OperationID, refusal wire shapes per the approved design §5.

// countingUpstream counts forwards and records every forwarded request
// (concurrency-safe, for the duplicate-claim race exploit). An injected err is
// classified errState (default DispatchUnknown — a post-transmit fault).
type countingUpstream struct {
	mu       sync.Mutex
	calls    int
	params   [][]byte
	reqs     []UpstreamRequest
	result   json.RawMessage
	err      error
	errState DispatchState
}

func (u *countingUpstream) Forward(_ context.Context, req UpstreamRequest) (UpstreamResult, error) {
	u.mu.Lock()
	u.calls++
	u.params = append(u.params, append([]byte(nil), req.Params...))
	u.reqs = append(u.reqs, req)
	u.mu.Unlock()
	if u.err != nil {
		state := u.errState
		if state == "" {
			state = DispatchUnknown
		}
		return UpstreamResult{State: state}, u.err
	}
	result := u.result
	if result == nil {
		result = json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`)
	}
	return UpstreamResult{Result: result, State: DispatchCompleted}, nil
}

func (u *countingUpstream) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.calls
}

func (u *countingUpstream) lastParams() []byte {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.params) == 0 {
		return nil
	}
	return u.params[len(u.params)-1]
}

// errAmbiguousTransport simulates a transport fault AFTER the request was
// handed to the network (classification: dispatch state unknown).
var errAmbiguousTransport = &dispatchError{"simulated transport fault after Do (outcome unknown)"}

// fakeOpRow is one in-memory journal row of fakeEvidenceJournal.
type fakeOpRow struct {
	binding sdk.EvidenceBinding
	receipt sdk.EvidenceReceipt
	settled bool
	outcome RecordedOutcome
	// release is the response-release child binding the settlement NAMED through
	// GateOutcome.ReleaseBinding (stage 5; zero when the settlement named none).
	release sdk.EvidenceBinding
}

// fakeEvidenceJournal is a contract-faithful in-memory GateAuditor: single-use
// claims, exact-replay/rebind semantics, fence + settlement — the connector-level
// stand-in for the durable journal the composition root wires (the real
// claim/settle semantics are proven against the real store in cmd/olivares).
// The zero value grants; the fault knobs turn individual legs hostile.
type fakeEvidenceJournal struct {
	mu  sync.Mutex
	seq int
	ops map[sdk.OperationID]*fakeOpRow

	recordFault     sdk.EvidenceFault // non-empty ⇒ every allow-claim refuses with this fault
	fenceFault      sdk.EvidenceFault // non-empty ⇒ BeforeEffect refuses (leader loss after claim)
	settleFail      bool              // true ⇒ Settle does not record (outcome withheld)
	mismatchReceipt bool              // true ⇒ Record returns a receipt for a DIFFERENT operation

	// recordFaultFn, when set, overrides recordFault PER allow-claim (task
	// exploits: turn only selected claims hostile — e.g. the compensation cancel —
	// while the surrounding claims anchor).
	recordFaultFn func(ToolDecision, sdk.EvidenceBinding) sdk.EvidenceFault

	// fenceFaultFn, when set, overrides fenceFault PER claim and receives the
	// journal ACTION the claim was recorded under (round-2: the F-09 exploit
	// must fail the fence of the mcp.task.track REGISTRATION only, while the
	// parent tools/call fence stays valid).
	fenceFaultFn func(action string, rec GateRecord) sdk.EvidenceFault

	// actions maps a claimed operation to its journal action (test observability
	// and the fenceFaultFn selector).
	actions map[sdk.OperationID]string

	lastRecorded sdk.EvidenceBinding // the most recent allow-claim binding (test observability)
}

// action returns the journal action a claimed operation was recorded under.
func (j *fakeEvidenceJournal) action(opID sdk.OperationID) string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.actions[opID]
}

// claimedOperations returns every claimed operation id (test observability).
func (j *fakeEvidenceJournal) claimedOperations() []sdk.OperationID {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]sdk.OperationID, 0, len(j.ops))
	for id := range j.ops {
		out = append(out, id)
	}
	return out
}

// opCount returns the number of distinct claimed operations (test observability).
func (j *fakeEvidenceJournal) opCount() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.ops)
}

// settledCount returns how many claimed operations settled with state.
func (j *fakeEvidenceJournal) settledCount(state DispatchState) int {
	j.mu.Lock()
	defer j.mu.Unlock()
	n := 0
	for _, op := range j.ops {
		if op.settled && op.outcome.State == state {
			n++
		}
	}
	return n
}

// settledActionCount returns how many claimed operations whose journal action
// starts with prefix settled with state — the round-2 observability for the
// parent/child split: a parent op (e.g. "mcp.tool.call") and its release child
// ("mcp.release.<class>") settle independently, and a contract that asserts
// only settledCount can no longer tell WHICH operation carried the word.
func (j *fakeEvidenceJournal) settledActionCount(prefix string, state DispatchState) int {
	j.mu.Lock()
	defer j.mu.Unlock()
	n := 0
	for id, op := range j.ops {
		if op.settled && op.outcome.State == state && strings.HasPrefix(j.actions[id], prefix) {
			n++
		}
	}
	return n
}

// lastBinding returns the most recently recorded allow-claim binding.
func (j *fakeEvidenceJournal) lastBinding() sdk.EvidenceBinding {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.lastRecorded
}

func (j *fakeEvidenceJournal) refused(binding sdk.EvidenceBinding, fault sdk.EvidenceFault) GateRecord {
	return GateRecord{
		Binding: binding,
		Receipt: sdk.EvidenceReceipt{
			OperationID: binding.OperationID, EffectDigest: binding.EffectDigest, Fault: fault,
		},
		State:        GateRecordRefused,
		FailureClass: sdk.FailureEvidenceFault,
	}
}

func (j *fakeEvidenceJournal) Record(_ context.Context, dec ToolDecision, binding sdk.EvidenceBinding) GateRecord {
	if !dec.Allowed || !binding.Valid() {
		// Denials and legacy zero-binding audits: best-effort, result ignored.
		return j.refused(binding, sdk.EvidenceFaultLedgerUnwired)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.lastRecorded = binding
	fault := j.recordFault
	if j.recordFaultFn != nil {
		fault = j.recordFaultFn(dec, binding)
	}
	if fault != "" {
		return j.refused(binding, fault)
	}
	if j.ops == nil {
		j.ops = map[sdk.OperationID]*fakeOpRow{}
	}
	if op, ok := j.ops[binding.OperationID]; ok {
		if op.binding.EffectDigest != binding.EffectDigest {
			rec := j.refused(binding, "")
			rec.Receipt.Fault = sdk.EvidenceFaultWriteError
			rec.FailureClass = sdk.FailureReplay
			return rec
		}
		rec := GateRecord{Binding: op.binding, Receipt: op.receipt, State: GateRecordReplayPending}
		if op.settled {
			rec.State = GateRecordReplaySettled
			out := op.outcome
			rec.Recorded = &out
		}
		return rec
	}
	j.seq++
	receipt := sdk.EvidenceReceipt{
		OperationID:  binding.OperationID,
		EffectDigest: binding.EffectDigest,
		EvidenceRef:  fmt.Sprintf("fake-ev-%d", j.seq),
	}
	if j.mismatchReceipt {
		receipt.OperationID = "operation-of-someone-else" // exploit 2: wrong-binding receipt
	}
	j.ops[binding.OperationID] = &fakeOpRow{binding: binding, receipt: receipt}
	if j.actions == nil {
		j.actions = map[sdk.OperationID]string{}
	}
	j.actions[binding.OperationID] = dec.EffectAction
	return GateRecord{Binding: binding, Receipt: receipt, State: GateRecordFresh, FenceToken: "epoch-1"}
}

func (j *fakeEvidenceJournal) BeforeEffect(_ context.Context, rec GateRecord) sdk.EvidenceReceipt {
	j.mu.Lock()
	fault := j.fenceFault
	fn, action := j.fenceFaultFn, j.actions[rec.Binding.OperationID]
	j.mu.Unlock()
	if fn != nil {
		fault = fn(action, rec)
	}
	if fault != "" {
		return sdk.EvidenceReceipt{
			OperationID: rec.Binding.OperationID, EffectDigest: rec.Binding.EffectDigest,
			Fault: fault,
		}
	}
	return rec.Receipt
}

func (j *fakeEvidenceJournal) Settle(_ context.Context, out GateOutcome) GateSettlement {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.settleFail {
		return GateSettlement{FailureClass: sdk.FailureEvidenceFault}
	}
	op, ok := j.ops[out.Record.Binding.OperationID]
	if !ok {
		return GateSettlement{FailureClass: sdk.FailureEvidenceFault}
	}
	op.settled = true
	op.outcome = RecordedOutcome{
		State: out.State, ResultDigest: out.ResultDigest, OutcomeRef: op.receipt.EvidenceRef + "-settle",
	}
	op.release = out.ReleaseBinding
	return GateSettlement{Outcome: op.outcome, EvidenceRef: op.outcome.OutcomeRef}
}

// settledRelease returns the response-release child binding the operation's
// settlement named (zero when unsettled or when none was named).
func (j *fakeEvidenceJournal) settledRelease(opID sdk.OperationID) sdk.EvidenceBinding {
	j.mu.Lock()
	defer j.mu.Unlock()
	if op, ok := j.ops[opID]; ok && op.settled {
		return op.release
	}
	return sdk.EvidenceBinding{}
}

// settledState returns the recorded outcome state of an operation ("" when absent
// or unsettled).
func (j *fakeEvidenceJournal) settledState(opID sdk.OperationID) DispatchState {
	j.mu.Lock()
	defer j.mu.Unlock()
	if op, ok := j.ops[opID]; ok && op.settled {
		return op.outcome.State
	}
	return ""
}

// newEvidenceRSAud builds the RS under test with an explicit auditor (nil ⇒ the
// deny-closed default no-op).
func newEvidenceRSAud(t *testing.T, jwks []byte, up Upstream, aud GateAuditor) *ResourceServer {
	t.Helper()
	ts, err := NewToolset([]ToolPolicy{
		{Name: "search", RequiredScope: "tools:read"},
		{Name: "delete_db", RequiredScope: "tools:admin", Destructive: true},
	})
	if err != nil {
		t.Fatalf("toolset: %v", err)
	}
	rs, err := NewResourceServer(ResourceServerConfig{
		Resource:             rsResource,
		AuthorizationServers: []string{rsIssuer},
		Issuer:               rsIssuer,
		IssuerJWKS:           jwks,
		Toolset:              ts,
		Gate:                 fakeToolGate{StatusApproved},
		Upstream:             up,
		Auditor:              aud,
		Clock:                rsClock,
		// 2025-11-25-style requests: keep the focus on the evidence gate.
		DisableNextRevisionHeaders: true,
	})
	if err != nil {
		t.Fatalf("new rs: %v", err)
	}
	return rs
}

// newEvidenceRS builds the RS with a granting in-memory journal.
func newEvidenceRS(t *testing.T, jwks []byte, up Upstream) *ResourceServer {
	t.Helper()
	return newEvidenceRSAud(t, jwks, up, &fakeEvidenceJournal{})
}

// toolsCallBody builds a tools/call request body with raw params JSON.
func toolsCallBody(params string) string {
	return `{"jsonrpc":"2.0","id":17,"method":"tools/call","params":` + params + `}`
}

func postToolsCall(rs *ResourceServer, token, params string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, rsResource, strings.NewReader(toolsCallBody(params)))
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, req)
	return w
}

// rpcErrorEnvelope decodes the JSON-RPC error envelope of a refusal.
func rpcErrorEnvelope(t *testing.T, body string) (code int, message string, data map[string]any, id any) {
	t.Helper()
	var resp struct {
		ID    any `json:"id"`
		Error struct {
			Code    int             `json:"code"`
			Message string          `json:"message"`
			Data    json.RawMessage `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode rpc error envelope: %v; body=%s", err, body)
	}
	if len(resp.Error.Data) > 0 {
		if err := json.Unmarshal(resp.Error.Data, &data); err != nil {
			t.Fatalf("decode rpc error data: %v; body=%s", err, body)
		}
	}
	return resp.Error.Code, resp.Error.Message, data, resp.ID
}

// --- RED exploit 1: ledger unwired ⇒ refuse before forward ---------------------

// TestToolsCallEvidenceUnwiredRefusesBeforeForward: with NO auditor wired (the
// default no-op), an authorized tools/call MUST refuse 503/-31010 and the
// upstream MUST NOT be called. Historical fail-open: the nop auditor "recorded"
// nothing and the call forwarded anyway.
func TestToolsCallEvidenceUnwiredRefusesBeforeForward(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &countingUpstream{}
	rs := newEvidenceRSAud(t, jwks, up, nil) // no Auditor ⇒ deny-closed default no-op
	w := postToolsCall(rs, token, `{"name":"search","arguments":{"q":"x"}}`)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("ledger-unwired tools/call status = %d, want 503", w.Code)
	}
	code, msg, data, _ := rpcErrorEnvelope(t, w.Body.String())
	if code != -31010 {
		t.Errorf("ledger-unwired code = %d, want -31010", code)
	}
	if msg != "governance evidence unavailable; request was not forwarded" {
		t.Errorf("ledger-unwired message = %q", msg)
	}
	if data["failure_class"] != "evidence_fault" || data["retryable"] != true {
		t.Errorf("ledger-unwired data = %v, want failure_class=evidence_fault retryable=true", data)
	}
	if got := up.count(); got != 0 {
		t.Errorf("upstream calls = %d, want 0 (the effect must NEVER precede the anchor)", got)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	if ra := w.Header().Get("Retry-After"); ra != "1" {
		t.Errorf("Retry-After = %q, want 1", ra)
	}
}

// --- RED exploit 3: exact replay forwards exactly once -------------------------

const opKeyedSearch = `{"name":"search","arguments":{"q":"x"},"_meta":{"ai.olivares/operationId":"op-key-1"}}`

// TestToolsCallExactReplayForwardsOnce: the same client operation key with the
// same params must reach the upstream EXACTLY once across two requests; the
// second returns the recorded state (409/-31012), never a second effect.
func TestToolsCallExactReplayForwardsOnce(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &countingUpstream{}
	rs := newEvidenceRS(t, jwks, up)
	w1 := postToolsCall(rs, token, opKeyedSearch)
	if w1.Code != http.StatusOK {
		t.Fatalf("first keyed tools/call status = %d, want 200; body=%s", w1.Code, w1.Body.String())
	}
	w2 := postToolsCall(rs, token, opKeyedSearch)
	if got := up.count(); got != 1 {
		t.Errorf("upstream calls across exact replay = %d, want EXACTLY 1", got)
	}
	if w2.Code != http.StatusConflict {
		t.Errorf("exact replay status = %d, want 409; body=%s", w2.Code, w2.Body.String())
	}
	code, _, data, _ := rpcErrorEnvelope(t, w2.Body.String())
	if code != -31012 {
		t.Errorf("exact replay code = %d, want -31012", code)
	}
	if data["operation_id"] == nil || data["state"] == nil {
		t.Errorf("exact replay data = %v, want recorded state + operation_id", data)
	}
}

// --- RED exploit 4: rebind (same key, different params) refused ----------------

func TestToolsCallRebindRefused(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &countingUpstream{}
	rs := newEvidenceRS(t, jwks, up)
	w1 := postToolsCall(rs, token, opKeyedSearch)
	if w1.Code != http.StatusOK {
		t.Fatalf("first keyed tools/call status = %d, want 200; body=%s", w1.Code, w1.Body.String())
	}
	// Same operation key, DIFFERENT arguments: a rebind of the single-use claim.
	w2 := postToolsCall(rs, token, `{"name":"search","arguments":{"q":"CHANGED"},"_meta":{"ai.olivares/operationId":"op-key-1"}}`)
	if got := up.count(); got != 1 {
		t.Errorf("upstream calls after rebind attempt = %d, want 1 (no second effect)", got)
	}
	if w2.Code != http.StatusConflict {
		t.Errorf("rebind status = %d, want 409; body=%s", w2.Code, w2.Body.String())
	}
	code, _, data, _ := rpcErrorEnvelope(t, w2.Body.String())
	if code != -31011 {
		t.Errorf("rebind code = %d, want -31011", code)
	}
	if data["failure_class"] != "replay" || data["retryable"] != false {
		t.Errorf("rebind data = %v, want failure_class=replay retryable=false", data)
	}
}

// --- RED exploit 5: concurrent duplicate claims ⇒ exactly one forward ----------

func TestToolsCallConcurrentDuplicateClaimsForwardOnce(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &countingUpstream{}
	rs := newEvidenceRS(t, jwks, up)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			postToolsCall(rs, token, opKeyedSearch)
		}()
	}
	wg.Wait()
	if got := up.count(); got != 1 {
		t.Errorf("concurrent duplicate claims produced %d upstream dispatches, want EXACTLY 1", got)
	}
}

// --- RED exploit 7: post-transmit ambiguity is never re-dispatched -------------

func TestToolsCallPostTransmitAmbiguityNeverRedispatches(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &countingUpstream{err: errAmbiguousTransport}
	rs := newEvidenceRS(t, jwks, up)
	w1 := postToolsCall(rs, token, opKeyedSearch)
	if w1.Code != http.StatusServiceUnavailable {
		t.Errorf("ambiguous dispatch status = %d, want 503; body=%s", w1.Code, w1.Body.String())
	}
	code, msg, _, _ := rpcErrorEnvelope(t, w1.Body.String())
	if code != -31012 {
		t.Errorf("ambiguous dispatch code = %d, want -31012", code)
	}
	if msg != "operation outcome is indeterminate; it will not be forwarded again" {
		t.Errorf("ambiguous dispatch message = %q", msg)
	}
	// The retry with the SAME operation key must NOT re-dispatch: the claim is
	// burned (at-most-once) and the recorded state is returned.
	w2 := postToolsCall(rs, token, opKeyedSearch)
	if got := up.count(); got != 1 {
		t.Errorf("upstream calls after ambiguous outcome retry = %d, want EXACTLY 1 (never re-dispatch an unknown)", got)
	}
	if w2.Code != http.StatusConflict {
		t.Errorf("retry-after-ambiguity status = %d, want 409; body=%s", w2.Code, w2.Body.String())
	}
}

// --- RED exploit 9 (guard): ledger-down policy DENIAL still stands -------------

// TestToolsCallPolicyDenyStandsWithoutLedger: a policy deny must never depend on
// evidence success — deny-by-default keeps answering 403 with no auditor wired.
// (This passed BEFORE the enforcement too; it is the regression guard that the
// inversion did not couple denials to the ledger.)
func TestToolsCallPolicyDenyStandsWithoutLedger(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &countingUpstream{}
	rs := newEvidenceRSAud(t, jwks, up, nil) // nop auditor (unwired ledger)
	w := postToolsCall(rs, token, `{"name":"not_in_toolset","arguments":{}}`)
	if w.Code != http.StatusForbidden {
		t.Errorf("policy deny status = %d, want 403 (denial must NEVER be blocked by evidence)", w.Code)
	}
	if got := up.count(); got != 0 {
		t.Errorf("upstream calls = %d, want 0", got)
	}
}

// --- RED exploit 11: duplicate JSON keys refused pre-claim ---------------------

func TestToolsCallDuplicateJSONKeysRefusedPreClaim(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	for _, tc := range []struct{ name, params string }{
		{"duplicate argument key", `{"name":"search","arguments":{"a":1,"a":2}}`},
		{"duplicate nested key", `{"name":"search","arguments":{"o":{"x":1,"x":2}}}`},
		{"duplicate top-level params key", `{"name":"search","arguments":{},"arguments":{"b":1}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := &countingUpstream{}
			rs := newEvidenceRS(t, jwks, up)
			w := postToolsCall(rs, token, tc.params)
			if w.Code != http.StatusBadRequest {
				t.Errorf("duplicate-key params status = %d, want 400; body=%s", w.Code, w.Body.String())
			}
			if got := up.count(); got != 0 {
				t.Errorf("upstream calls = %d, want 0 (ambiguous params must never be forwarded)", got)
			}
		})
	}
}

// --- RED exploit 12: the _meta operation key never reaches the upstream --------

func TestToolsCallStripsOperationKeyFromForwardedParams(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &countingUpstream{}
	rs := newEvidenceRS(t, jwks, up)
	w := postToolsCall(rs, token, `{"name":"search","arguments":{"b":2,"a":1},"_meta":{"ai.olivares/operationId":"op-strip-1","traceparent":"00-11111111111111111111111111111111-2222222222222222-01"}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("keyed tools/call status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	got := up.lastParams()
	if strings.Contains(string(got), "ai.olivares/operationId") {
		t.Errorf("forwarded params leak the operation-key extension: %s", got)
	}
	// The bytes governed are the bytes sent: the forwarded params are OUR
	// canonical form (recursively sorted keys, number literals preserved, the
	// operation key stripped, trace correlation kept for propagation).
	want := `{"_meta":{"traceparent":"00-11111111111111111111111111111111-2222222222222222-01"},"arguments":{"a":1,"b":2},"name":"search"}`
	if string(got) != want {
		t.Errorf("forwarded params are not the canonical governed bytes:\n got %s\nwant %s", got, want)
	}
}

// --- exploit 2: a receipt for the WRONG binding never green-lights the effect ---
//
// (Seam-dependent: inexpressible against the pre void Record interface — the
// RED for this and the two below is the syntactic impossibility, documented in
// sessions-q1-mcp-evidence.md.)
func TestToolsCallWrongBindingReceiptRefused(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &countingUpstream{}
	rs := newEvidenceRSAud(t, jwks, up, &fakeEvidenceJournal{mismatchReceipt: true})
	w := postToolsCall(rs, token, `{"name":"search","arguments":{"q":"x"}}`)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("wrong-binding receipt status = %d, want 503; body=%s", w.Code, w.Body.String())
	}
	if code, _, _, _ := rpcErrorEnvelope(t, w.Body.String()); code != -31010 {
		t.Errorf("wrong-binding receipt code = %d, want -31010", code)
	}
	if got := up.count(); got != 0 {
		t.Errorf("upstream calls = %d, want 0 (a receipt minted for another operation authorizes nothing)", got)
	}
}

// --- exploit 6: leader loss between Record and BeforeEffect --------------------

func TestToolsCallLeaderLossBeforeEffectRefused(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &countingUpstream{}
	rs := newEvidenceRSAud(t, jwks, up, &fakeEvidenceJournal{fenceFault: sdk.EvidenceFaultLedgerUnavailable})
	w := postToolsCall(rs, token, `{"name":"search","arguments":{"q":"x"}}`)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("fence-refused status = %d, want 503; body=%s", w.Code, w.Body.String())
	}
	if code, _, _, _ := rpcErrorEnvelope(t, w.Body.String()); code != -31010 {
		t.Errorf("fence-refused code = %d, want -31010", code)
	}
	if got := up.count(); got != 0 {
		t.Errorf("upstream calls = %d, want 0 (the fence runs IMMEDIATELY before dispatch)", got)
	}
}

// --- exploit 8: settlement write failure withholds the response ----------------

func TestToolsCallSettlementFailureWithholdsResponse(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &countingUpstream{}
	journal := &fakeEvidenceJournal{settleFail: true}
	rs := newEvidenceRSAud(t, jwks, up, journal)
	w1 := postToolsCall(rs, token, opKeyedSearch)
	if got := up.count(); got != 1 {
		t.Fatalf("upstream calls = %d, want 1 (the forward happened; the settlement failed)", got)
	}
	if w1.Code != http.StatusServiceUnavailable {
		t.Errorf("unsettled outcome status = %d, want 503 (response withheld, no false success); body=%s", w1.Code, w1.Body.String())
	}
	code, msg, _, _ := rpcErrorEnvelope(t, w1.Body.String())
	if code != -31012 {
		t.Errorf("unsettled outcome code = %d, want -31012", code)
	}
	if msg != "operation outcome is indeterminate; it will not be forwarded again" {
		t.Errorf("unsettled outcome message = %q", msg)
	}
	// The operation is burned claimed/ambiguous: a same-operation retry is a
	// status replay only — never a re-dispatch, never a fabricated success.
	journal.settleFail = false
	w2 := postToolsCall(rs, token, opKeyedSearch)
	if got := up.count(); got != 1 {
		t.Errorf("upstream calls after retry = %d, want EXACTLY 1 (claimed-never-settled is non-replayable)", got)
	}
	if w2.Code != http.StatusConflict {
		t.Errorf("retry status = %d, want 409; body=%s", w2.Code, w2.Body.String())
	}
	code, _, data, _ := rpcErrorEnvelope(t, w2.Body.String())
	if code != -31012 || data["state"] != "claimed" {
		t.Errorf("retry envelope = code %d data %v, want -31012 state=claimed", code, data)
	}
}

// --- evidence-fault refusals never leak infrastructure detail ------------------

func TestToolsCallEvidenceFaultDetailNeverLeaks(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	for _, fault := range []sdk.EvidenceFault{
		sdk.EvidenceFaultSpoolFull, sdk.EvidenceFaultSpoolDegraded,
		sdk.EvidenceFaultLedgerUnavailable, sdk.EvidenceFaultTenantUnresolved,
		sdk.EvidenceFaultWriteError,
	} {
		t.Run(string(fault), func(t *testing.T) {
			up := &countingUpstream{}
			rs := newEvidenceRSAud(t, jwks, up, &fakeEvidenceJournal{recordFault: fault})
			w := postToolsCall(rs, token, `{"name":"search","arguments":{}}`)
			if w.Code != http.StatusServiceUnavailable {
				t.Errorf("fault %s status = %d, want 503", fault, w.Code)
			}
			body := w.Body.String()
			if strings.Contains(body, string(fault)) {
				t.Errorf("refusal leaks the specific evidence fault %q: %s", fault, body)
			}
			code, _, data, id := rpcErrorEnvelope(t, body)
			if code != -31010 || data["failure_class"] != "evidence_fault" {
				t.Errorf("fault %s envelope = code %d data %v", fault, code, data)
			}
			if idNum, ok := id.(float64); !ok || idNum != 17 {
				t.Errorf("refusal id = %v, want the verbatim request id 17", id)
			}
			if got := up.count(); got != 0 {
				t.Errorf("upstream calls = %d, want 0", got)
			}
		})
	}
}

// --- request_instance semantics: no transport-retry dedup claim ----------------

// TestToolsCallWithoutOperationKeyEachRequestIsANewOperation: a legacy client that
// supplies no operation key gets a fresh server-minted OperationID per request —
// evidence is enforced but an identical resend is a NEW operation (documented: no
// transport-retry dedup for legacy clients).
func TestToolsCallWithoutOperationKeyEachRequestIsANewOperation(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &countingUpstream{}
	rs := newEvidenceRS(t, jwks, up)
	body := `{"name":"search","arguments":{"q":"x"}}`
	if w := postToolsCall(rs, token, body); w.Code != http.StatusOK {
		t.Fatalf("first call status = %d; body=%s", w.Code, w.Body.String())
	}
	if w := postToolsCall(rs, token, body); w.Code != http.StatusOK {
		t.Fatalf("second call status = %d; body=%s", w.Code, w.Body.String())
	}
	if got := up.count(); got != 2 {
		t.Errorf("upstream calls = %d, want 2 (request_instance operations never dedup)", got)
	}
	if up.reqs[0].OperationID == up.reqs[1].OperationID {
		t.Error("request_instance OperationIDs must be unique per received request")
	}
}

// --- the upstream request carries the evidence identity ------------------------

func TestToolsCallUpstreamRequestCarriesEvidenceIdentity(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &countingUpstream{}
	journal := &fakeEvidenceJournal{}
	rs := newEvidenceRSAud(t, jwks, up, journal)
	if w := postToolsCall(rs, token, opKeyedSearch); w.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", w.Code, w.Body.String())
	}
	req := up.reqs[0]
	if req.OperationID == "" || req.EffectDigest == "" {
		t.Errorf("upstream request lacks the operation identity: %+v", req)
	}
	if req.FenceToken != "epoch-1" {
		t.Errorf("upstream request fence token = %q, want the claim's token", req.FenceToken)
	}
	if got := journal.settledState(req.OperationID); got != DispatchCompleted {
		t.Errorf("settled state = %q, want completed", got)
	}
}

// --- canonicalization unit pins ------------------------------------------------

// TestCanonicalizeToolCallParams pins the canonical form: presence distinctions,
// duplicate rejection, op-key extraction/strip, sorted keys, preserved literals.
func TestCanonicalizeToolCallParams(t *testing.T) {
	t.Run("absent vs null vs present are distinct", func(t *testing.T) {
		absent, err := canonicalizeToolCallParams(nil)
		if err != nil || absent.Presence != paramsAbsent || absent.Forward != nil {
			t.Fatalf("absent = %+v err=%v", absent, err)
		}
		null, err := canonicalizeToolCallParams(json.RawMessage("null"))
		if err != nil || null.Presence != paramsNull || string(null.Forward) != "null" {
			t.Fatalf("null = %+v err=%v", null, err)
		}
		present, err := canonicalizeToolCallParams(json.RawMessage(`{"name":"x"}`))
		if err != nil || present.Presence != paramsPresent {
			t.Fatalf("present = %+v err=%v", present, err)
		}
		if string(absent.Effect) == string(null.Effect) {
			t.Error("absent and null params must have distinct effect views")
		}
	})

	t.Run("duplicate keys rejected at every depth", func(t *testing.T) {
		for _, raw := range []string{
			`{"a":1,"a":2}`,
			`{"o":{"deep":{"x":1,"x":2}}}`,
			`{"arr":[{"k":1,"k":1}]}`,
		} {
			if _, err := canonicalizeToolCallParams(json.RawMessage(raw)); err == nil {
				t.Errorf("duplicate keys accepted: %s", raw)
			}
		}
	})

	t.Run("trailing data rejected", func(t *testing.T) {
		if _, err := canonicalizeToolCallParams(json.RawMessage(`{"a":1} {"b":2}`)); err == nil {
			t.Error("trailing JSON value accepted")
		}
		if _, err := canonicalizeToolCallParams(json.RawMessage(`{"a":1} x`)); err == nil {
			t.Error("trailing garbage accepted")
		}
	})

	t.Run("op key must be a non-empty string", func(t *testing.T) {
		for _, raw := range []string{
			`{"_meta":{"ai.olivares/operationId":42}}`,
			`{"_meta":{"ai.olivares/operationId":""}}`,
			`{"_meta":{"ai.olivares/operationId":"  "}}`,
			`{"_meta":{"ai.olivares/operationId":null}}`,
		} {
			if _, err := canonicalizeToolCallParams(json.RawMessage(raw)); err == nil {
				t.Errorf("invalid op key accepted: %s", raw)
			}
		}
	})

	t.Run("canonical form sorts keys and preserves number literals", func(t *testing.T) {
		canon, err := canonicalizeToolCallParams(json.RawMessage(
			`{"name":"t","arguments":{"z":1.0,"a":{"y":2e3,"b":[1,"x",null,true]}}}`))
		if err != nil {
			t.Fatal(err)
		}
		want := `{"arguments":{"a":{"b":[1,"x",null,true],"y":2e3},"z":1.0},"name":"t"}`
		if string(canon.Forward) != want {
			t.Errorf("canonical forward:\n got %s\nwant %s", canon.Forward, want)
		}
		if string(canon.Args) != `{"a":{"b":[1,"x",null,true],"y":2e3},"z":1.0}` {
			t.Errorf("canonical args = %s", canon.Args)
		}
	})

	t.Run("trace members excluded from the effect view only", func(t *testing.T) {
		canon, err := canonicalizeToolCallParams(json.RawMessage(
			`{"name":"t","_meta":{"traceparent":"00-aa-bb-01","tracestate":"x=1","baggage":"k=v","keep":"me"}}`))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(canon.Forward), "traceparent") {
			t.Error("forward view must KEEP trace members (propagation)")
		}
		if strings.Contains(string(canon.Effect), "traceparent") ||
			strings.Contains(string(canon.Effect), "tracestate") ||
			strings.Contains(string(canon.Effect), "baggage") {
			t.Errorf("effect view must EXCLUDE trace members: %s", canon.Effect)
		}
		if !strings.Contains(string(canon.Effect), `"keep":"me"`) {
			t.Errorf("effect view dropped a non-trace _meta member: %s", canon.Effect)
		}
	})
}

// TestEffectDigestBoundaryBehavior pins the digest-identity decisions of the
// canonical form (design §3 + the RED-first exploit matrix).
func TestEffectDigestBoundaryBehavior(t *testing.T) {
	tok := validatedToken{Subject: "agent:claude", Issuer: rsIssuer, ClientID: "client-1"}
	policy := ToolPolicy{Name: "search", RequiredScope: "tools:read"}
	digest := func(t *testing.T, params string) string {
		t.Helper()
		canon, err := canonicalizeToolCallParams(json.RawMessage(params))
		if err != nil {
			t.Fatalf("canonicalize %s: %v", params, err)
		}
		pd := toolCallPolicyDigest(policy, pinBinding{State: "unwired"}, coazBinding{State: "unwired"})
		return deriveToolCallEffectDigest("tenant-1", rsResource, "tools/call", tok,
			"mcp.tool", "search", "", nil, canon, pd, "", "")
	}

	// Number/string literal distinctions are PRESERVED (pinned, documented):
	// {"a":1}, {"a":1.0} and {"a":"1"} are three different effects.
	d1 := digest(t, `{"name":"search","arguments":{"a":1}}`)
	d2 := digest(t, `{"name":"search","arguments":{"a":1.0}}`)
	d3 := digest(t, `{"name":"search","arguments":{"a":"1"}}`)
	if d1 == d2 || d1 == d3 || d2 == d3 {
		t.Errorf("number/string literal spellings must be distinct effects: %s %s %s", d1, d2, d3)
	}

	// Tuple-boundary attack: ["ab","c"] vs ["a","bc"] must differ.
	if digest(t, `{"name":"search","arguments":{"l":["ab","c"]}}`) ==
		digest(t, `{"name":"search","arguments":{"l":["a","bc"]}}`) {
		t.Error(`["ab","c"] and ["a","bc"] must produce different digests`)
	}

	// Key-order spellings of the SAME object are the SAME effect.
	if digest(t, `{"name":"search","arguments":{"a":1,"b":2}}`) !=
		digest(t, `{"arguments":{"b":2,"a":1},"name":"search"}`) {
		t.Error("key order must not change the effect identity")
	}

	// A fresh traceparent is the SAME effect.
	if digest(t, `{"name":"search","arguments":{"a":1},"_meta":{"traceparent":"00-11-22-01"}}`) !=
		digest(t, `{"name":"search","arguments":{"a":1},"_meta":{"traceparent":"00-33-44-01"}}`) {
		t.Error("trace correlation must not change the effect identity")
	}

	// Absent params vs explicit null params differ.
	absent, _ := canonicalizeToolCallParams(nil)
	null, _ := canonicalizeToolCallParams(json.RawMessage("null"))
	pd := toolCallPolicyDigest(policy, pinBinding{State: "unwired"}, coazBinding{State: "unwired"})
	da := deriveToolCallEffectDigest("tenant-1", rsResource, "tools/call", tok, "mcp.tool", "search", "", nil, absent, pd, "", "")
	dn := deriveToolCallEffectDigest("tenant-1", rsResource, "tools/call", tok, "mcp.tool", "search", "", nil, null, pd, "", "")
	if da == dn {
		t.Error("absent and null params must be different effects")
	}

	// Review round-1 P1: normalized granted scopes are part of the effect identity;
	// a different granted-scope set changes the digest, and set ORDER does not.
	c, _ := canonicalizeToolCallParams(json.RawMessage(`{"name":"search","arguments":{"a":1}}`))
	base := deriveToolCallEffectDigest("tenant-1", rsResource, "tools/call", tok, "mcp.tool", "search", "", []string{"tools:read"}, c, pd, "", "")
	more := deriveToolCallEffectDigest("tenant-1", rsResource, "tools/call", tok, "mcp.tool", "search", "", []string{"tools:read", "tools:admin"}, c, pd, "", "")
	if base == more {
		t.Error("a different granted-scope set must change the EffectDigest")
	}
	ord1 := deriveToolCallEffectDigest("tenant-1", rsResource, "tools/call", tok, "mcp.tool", "search", "", sortedScopeSet(map[string]struct{}{"a": {}, "b": {}}), c, pd, "", "")
	ord2 := deriveToolCallEffectDigest("tenant-1", rsResource, "tools/call", tok, "mcp.tool", "search", "", sortedScopeSet(map[string]struct{}{"b": {}, "a": {}}), c, pd, "", "")
	if ord1 != ord2 {
		t.Error("granted-scope set order must not change the EffectDigest")
	}
}

// TestPolicyDigestBindsStablePostureNotText pins review round-1 P1: the policy
// digest binds STABLE posture bits, and its signature no longer accepts the COAZ
// reason text or a call-time pin fingerprint (a compile-time guarantee that
// unstable text can never enter the digest).
func TestPolicyDigestBindsStablePostureNotText(t *testing.T) {
	policy := ToolPolicy{Name: "search", RequiredScope: "tools:read"}
	unwired := toolCallPolicyDigest(policy, pinBinding{State: "unwired"}, coazBinding{State: "unwired"})
	if unwired == toolCallPolicyDigest(policy, pinBinding{State: "unwired"}, coazBinding{State: "allow"}) {
		t.Error("COAZ posture (unwired vs allow) must change the policy digest")
	}
	if unwired == toolCallPolicyDigest(policy, pinBinding{State: "verified"}, coazBinding{State: "unwired"}) {
		t.Error("pin posture (unwired vs verified) must change the policy digest")
	}
	// Round-2: the APPROVED pin identity and the COAZ stable refs bind; a version
	// bump or a different decision-ref/policy-version is a different effect.
	attV1 := toolCallPolicyDigest(policy, pinBinding{State: "attested", Fingerprint: "fp-a", Version: "1"}, coazBinding{State: "unwired"})
	attV2 := toolCallPolicyDigest(policy, pinBinding{State: "attested", Fingerprint: "fp-a", Version: "2"}, coazBinding{State: "unwired"})
	attFP := toolCallPolicyDigest(policy, pinBinding{State: "attested", Fingerprint: "fp-B", Version: "1"}, coazBinding{State: "unwired"})
	if attV1 == attV2 || attV1 == attFP {
		t.Error("approved pin fingerprint/version must change the policy digest")
	}
	coazRef1 := toolCallPolicyDigest(policy, pinBinding{State: "unwired"}, coazBinding{State: "allow", DecisionRef: "d-1", PolicyVersion: "pv-1"})
	coazRef2 := toolCallPolicyDigest(policy, pinBinding{State: "unwired"}, coazBinding{State: "allow", DecisionRef: "d-2", PolicyVersion: "pv-1"})
	coazPV2 := toolCallPolicyDigest(policy, pinBinding{State: "unwired"}, coazBinding{State: "allow", DecisionRef: "d-1", PolicyVersion: "pv-2"})
	if coazRef1 == coazRef2 || coazRef1 == coazPV2 {
		t.Error("COAZ decision-ref/policy-version must change the policy digest")
	}
}

// TestOperationIDDerivation pins the supplied-key namespace and the rebind shape:
// the same key with changed params keeps the OperationID and changes the digest.
func TestOperationIDDerivation(t *testing.T) {
	tok := validatedToken{Subject: "agent:claude", Issuer: rsIssuer, ClientID: "client-1"}
	id1, kind1, err := deriveToolCallOperationID("tenant-1", rsResource, tok, "op-key")
	if err != nil || kind1 != opIDKindKeyed {
		t.Fatalf("keyed derivation: id=%q kind=%q err=%v", id1, kind1, err)
	}
	id2, _, _ := deriveToolCallOperationID("tenant-1", rsResource, tok, "op-key")
	if id1 != id2 {
		t.Error("the keyed OperationID must be deterministic")
	}

	// Namespace isolation: every identity axis changes the OperationID.
	for name, alt := range map[string]validatedToken{
		"different subject": {Subject: "agent:other", Issuer: rsIssuer, ClientID: "client-1"},
		"different client":  {Subject: "agent:claude", Issuer: rsIssuer, ClientID: "client-2"},
		"different issuer":  {Subject: "agent:claude", Issuer: "https://other-as.example", ClientID: "client-1"},
		"delegated act-as":  {Subject: "agent:claude", Issuer: rsIssuer, ClientID: "client-1", ActAs: "user:x"},
	} {
		altID, _, _ := deriveToolCallOperationID("tenant-1", rsResource, alt, "op-key")
		if altID == id1 {
			t.Errorf("%s must not share the operation namespace", name)
		}
	}

	// Same key + changed params ⇒ SAME OperationID, DIFFERENT EffectDigest — the
	// journal then refuses the rebind (FailureReplay).
	policy := ToolPolicy{Name: "search", RequiredScope: "tools:read"}
	pd := toolCallPolicyDigest(policy, pinBinding{State: "unwired"}, coazBinding{State: "unwired"})
	c1, _ := canonicalizeToolCallParams(json.RawMessage(`{"name":"search","arguments":{"q":"a"}}`))
	c2, _ := canonicalizeToolCallParams(json.RawMessage(`{"name":"search","arguments":{"q":"b"}}`))
	e1 := deriveToolCallEffectDigest("tenant-1", rsResource, "tools/call", tok, "mcp.tool", "search", "", nil, c1, pd, "", "")
	e2 := deriveToolCallEffectDigest("tenant-1", rsResource, "tools/call", tok, "mcp.tool", "search", "", nil, c2, pd, "", "")
	if e1 == e2 {
		t.Error("changed params must change the EffectDigest")
	}

	// request_instance ids are random and unique.
	r1, kindR, err := deriveToolCallOperationID("tenant-1", rsResource, tok, "")
	if err != nil || kindR != opIDKindRequestInstance {
		t.Fatalf("request_instance derivation: kind=%q err=%v", kindR, err)
	}
	r2, _, _ := deriveToolCallOperationID("tenant-1", rsResource, tok, "")
	if r1 == r2 {
		t.Error("request_instance OperationIDs must be unique")
	}
}

// TestToolsCallNotificationEvidenceRefusalHasEmptyBody: a notification-shaped
// tools/call (no id) refused by the evidence gate gets the HTTP status + cache/
// retry headers with an EMPTY body — a notification expects no JSON-RPC response
// object (design §5).
func TestToolsCallNotificationEvidenceRefusalHasEmptyBody(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &countingUpstream{}
	rs := newEvidenceRSAud(t, jwks, up, nil) // unwired ledger
	body := `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"search","arguments":{}}}`
	req := httptest.NewRequest(http.MethodPost, rsResource, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("notification refusal status = %d, want 503", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Errorf("notification refusal body = %q, want empty", w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Retry-After") != "1" {
		t.Errorf("notification refusal headers = %v, want no-store + Retry-After 1", w.Header())
	}
	if got := up.count(); got != 0 {
		t.Errorf("upstream calls = %d, want 0", got)
	}
}
