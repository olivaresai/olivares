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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// evidence_review_test.go — Stage-3 Codex round-1 MUST-FIX exploits
// (RED-first): case-fold key smuggling (P0 finding 1) and outcome laundering
// (P1 finding 4). Observed FAILING against the pre-fix code before the fixes.

// --- Finding 1 (P0): case-fold key smuggling authorizes one tool, forwards another.
//
// encoding/json matches struct fields case-INSENSITIVELY, so a body carrying
// BOTH "name" and "Name" (or "arguments"/"Arguments", "_meta"/"_Meta", or a
// case-variant of the operation key) lets the gate authorize one value while a
// case-insensitive Go upstream consuming the canonical bytes executes the other.
// Every such request MUST be refused 400/-32602 pre-claim; the upstream must be 0.
func TestToolsCallCaseFoldKeySmugglingRefused(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	for _, tc := range []struct{ name, params string }{
		{"name/Name", `{"name":"delete_db","Name":"search","arguments":{}}`},
		{"Name only (capital reserved alias)", `{"Name":"search","arguments":{}}`},
		{"arguments/Arguments", `{"name":"search","arguments":{},"Arguments":{"smuggled":1}}`},
		{"_meta/_Meta", `{"name":"search","arguments":{},"_Meta":{"x":1}}`},
		{"op-key case alias", `{"name":"search","arguments":{},"_meta":{"AI.olivares/operationId":"k1"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := &countingUpstream{}
			rs := newEvidenceRS(t, jwks, up)
			w := postToolsCall(rs, token, tc.params)
			if w.Code != http.StatusBadRequest {
				t.Errorf("case-fold alias status = %d, want 400 (pre-claim protocol refusal); body=%s", w.Code, w.Body.String())
			}
			if code, _, _, _ := rpcErrorEnvelope(t, w.Body.String()); code != rpcInvalidParams {
				t.Errorf("case-fold alias code = %d, want %d", code, rpcInvalidParams)
			}
			if got := up.count(); got != 0 {
				t.Errorf("upstream calls = %d, want 0 (a smuggled alias must never be forwarded)", got)
			}
		})
	}
}

// TestToolsCallForwardsExactCasedName: the authorized tool name and the forwarded
// bytes agree on exact casing — a Go upstream unmarshaling the forwarded body
// case-insensitively can only see the authorized name.
func TestToolsCallForwardsExactCasedName(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &countingUpstream{}
	rs := newEvidenceRS(t, jwks, up)
	if w := postToolsCall(rs, token, `{"name":"search","arguments":{"q":"x"}}`); w.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", w.Code, w.Body.String())
	}
	fwd := string(up.lastParams())
	if strings.Contains(fwd, `"Name"`) || strings.Count(fwd, `"name"`) != 1 {
		t.Errorf("forwarded body must carry exactly one exact-cased name key: %s", fwd)
	}
}

// --- Finding 4 (P1): outcomes must not be laundered as completed.
//
// A local fake that returns a nil error with a NON-completed state must never be
// written as a success: the gate treats {nil error, not completed} as ambiguous,
// settles unknown and WITHHOLDS the response (503/-31012).
func TestToolsCallInconsistentNilErrorStateWithheld(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &statedUpstream{state: DispatchNotSent, result: []byte(`{"content":[]}`)}
	rs := newEvidenceRS(t, jwks, up)
	w := postToolsCall(rs, token, `{"name":"search","arguments":{}}`)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("inconsistent nil-error/not-completed status = %d, want 503 (withheld, never laundered); body=%s", w.Code, w.Body.String())
	}
	if code, _, _, _ := rpcErrorEnvelope(t, w.Body.String()); code != rpcOperationRecorded {
		t.Errorf("inconsistent outcome code = %d, want %d", code, rpcOperationRecorded)
	}
}

// statedUpstream returns a caller-chosen State with a nil error — the adapter
// misclassification the gate must not trust.
type statedUpstream struct {
	state  DispatchState
	result json.RawMessage
	calls  int
}

func (u *statedUpstream) Forward(_ context.Context, _ UpstreamRequest) (UpstreamResult, error) {
	u.calls++
	return UpstreamResult{Result: u.result, State: u.state}, nil
}

// TestToolsCallCaseFoldSmugglingRefusedRCPath: the same case-fold smuggling on
// the 2026-07-28 RC header path (Mcp-Name header + body) is refused at strict
// canonicalization (the header pre-check authorizes the header name, but the body
// carries the smuggled alias). The upstream is never reached.
func TestToolsCallCaseFoldSmugglingRefusedRCPath(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &countingUpstream{}
	rs := newRSTestMode(t, jwks, up, &fakeEvidenceJournal{}, revisionModeRCStrict)
	// Header authorizes "search"; body smuggles a case-variant "Name".
	req := nextReq(token, "tools/call", "search",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search","Name":"delete_db","arguments":{}}}`)
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("RC case-fold smuggling status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	if got := up.count(); got != 0 {
		t.Errorf("upstream calls = %d, want 0", got)
	}
}

// --- Round-2 NEW-1: strict JSON-RPC response parser unit pins ------------------

func TestParseStrictJSONRPCResponse(t *testing.T) {
	t.Run("valid result preserves verbatim bytes", func(t *testing.T) {
		res, rpcErr, err := ParseStrictJSONRPCResponse(
			[]byte(`{"jsonrpc":"2.0","id":1,"result":{"b":1,"a":2}}`), 1)
		if err != nil || rpcErr != nil {
			t.Fatalf("valid result: err=%v rpcErr=%+v", err, rpcErr)
		}
		if string(res) != `{"b":1,"a":2}` {
			t.Errorf("result bytes = %s, want verbatim {\"b\":1,\"a\":2} (key order intact)", res)
		}
	})

	t.Run("valid error object", func(t *testing.T) {
		res, rpcErr, err := ParseStrictJSONRPCResponse(
			[]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"tool failed","data":{"x":1}}}`), 1)
		if err != nil || res != nil || rpcErr == nil || rpcErr.Code != -32000 || rpcErr.Message != "tool failed" {
			t.Fatalf("valid error: res=%s rpcErr=%+v err=%v", res, rpcErr, err)
		}
	})

	for _, tc := range []struct{ name, body string }{
		{"alt-cased members", `{"JSONRPC":"2.0","ID":1,"RESULT":{}}`},
		{"case-variant alias beside exact", `{"jsonrpc":"2.0","Id":2,"id":1,"result":{}}`},
		{"duplicate member", `{"jsonrpc":"2.0","id":1,"result":{},"result":{}}`},
		{"nested duplicate", `{"jsonrpc":"2.0","id":1,"result":{"a":1,"a":2}}`},
		{"both result and error", `{"jsonrpc":"2.0","id":1,"result":{},"error":{"code":-1,"message":"x"}}`},
		{"neither result nor error", `{"jsonrpc":"2.0","id":1}`},
		{"id mismatch", `{"jsonrpc":"2.0","id":2,"result":{}}`},
		{"string id", `{"jsonrpc":"2.0","id":"1","result":{}}`},
		{"fractional id", `{"jsonrpc":"2.0","id":1.0,"result":{}}`},
		{"missing jsonrpc", `{"id":1,"result":{}}`},
		{"wrong version", `{"jsonrpc":"1.0","id":1,"result":{}}`},
		{"empty error", `{"jsonrpc":"2.0","id":1,"error":{}}`},
		{"error code string", `{"jsonrpc":"2.0","id":1,"error":{"code":"x","message":"m"}}`},
		{"error code fractional", `{"jsonrpc":"2.0","id":1,"error":{"code":1.5,"message":"m"}}`},
		{"error message non-string", `{"jsonrpc":"2.0","id":1,"error":{"code":-1,"message":5}}`},
		{"error non-object", `{"jsonrpc":"2.0","id":1,"error":"boom"}`},
		{"trailing data", `{"jsonrpc":"2.0","id":1,"result":{}} {}`},
		{"non-object response", `[1,2]`},
	} {
		t.Run(tc.name+" rejected", func(t *testing.T) {
			if _, _, err := ParseStrictJSONRPCResponse([]byte(tc.body), 1); err == nil {
				t.Errorf("strict parser accepted %s", tc.body)
			}
		})
	}
}

// --- Round-2 finding 3: pin attestation + descriptor bind the effect identity --

// attestingPinVerifier is a ToolPinVerifier + ToolPinVerifyAttestor fake: it
// returns the decision AND the approved-pin identity together (round-3
// atomic capability). A test that swaps `att` between calls models an operator
// re-pin landing BETWEEN calls (each call sees a single consistent snapshot).
type attestingPinVerifier struct {
	att    ToolPinAttestation
	hasPin bool
	attErr error
}

func (v *attestingPinVerifier) Verify(context.Context, string, string, string) (bool, string, error) {
	return true, "", nil
}
func (v *attestingPinVerifier) RecordPin(context.Context, string, string, string) error { return nil }
func (v *attestingPinVerifier) VerifyAndAttest(context.Context, string, string, string) (ToolPinVerifyAttestation, error) {
	if v.attErr != nil {
		return ToolPinVerifyAttestation{}, v.attErr
	}
	return ToolPinVerifyAttestation{Allowed: true, Attested: v.hasPin, Pin: v.att}, nil
}

func newEvidenceRSPin(t *testing.T, jwks []byte, up Upstream, aud GateAuditor, pin ToolPinVerifier) *ResourceServer {
	t.Helper()
	ts, err := NewToolset([]ToolPolicy{{Name: "search", RequiredScope: "tools:read"}})
	if err != nil {
		t.Fatalf("toolset: %v", err)
	}
	rs, err := NewResourceServer(ResourceServerConfig{
		Resource:                   rsResource,
		AuthorizationServers:       []string{rsIssuer},
		Issuer:                     rsIssuer,
		IssuerJWKS:                 jwks,
		Toolset:                    ts,
		Gate:                       fakeToolGate{StatusApproved},
		Upstream:                   up,
		Auditor:                    aud,
		PinVerifier:                pin,
		Clock:                      rsClock,
		DisableNextRevisionHeaders: true,
	})
	if err != nil {
		t.Fatalf("new rs: %v", err)
	}
	return rs
}

// TestToolsCallRepinRebindsKeyedOperation: an operator re-pin (approved pin
// version bump) changes the effect identity — a keyed retry REBINDS (-31011)
// instead of silently replaying; the upstream never runs twice.
func TestToolsCallRepinRebindsKeyedOperation(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &countingUpstream{}
	verifier := &attestingPinVerifier{att: ToolPinAttestation{Fingerprint: "fp-a", Version: "1"}, hasPin: true}
	rs := newEvidenceRSPin(t, jwks, up, &fakeEvidenceJournal{}, verifier)
	if w := postToolsCall(rs, token, opKeyedSearch); w.Code != http.StatusOK {
		t.Fatalf("first call status = %d; body=%s", w.Code, w.Body.String())
	}
	verifier.att = ToolPinAttestation{Fingerprint: "fp-b", Version: "2"} // operator re-pin
	w2 := postToolsCall(rs, token, opKeyedSearch)
	if w2.Code != http.StatusConflict {
		t.Fatalf("post-re-pin status = %d, want 409; body=%s", w2.Code, w2.Body.String())
	}
	if code, _, _, _ := rpcErrorEnvelope(t, w2.Body.String()); code != rpcEvidenceRebind {
		t.Errorf("post-re-pin code = %d, want %d (rebind: approved pin identity changed)", code, rpcEvidenceRebind)
	}
	if got := up.count(); got != 1 {
		t.Errorf("upstream calls = %d, want 1", got)
	}
}

// TestToolsCallPinAttestationErrorFailsClosed: an attestation lookup error after
// a Verify allow refuses the call (the binding must never misstate the pin).
func TestToolsCallPinAttestationErrorFailsClosed(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &countingUpstream{}
	verifier := &attestingPinVerifier{attErr: context.DeadlineExceeded}
	rs := newEvidenceRSPin(t, jwks, up, &fakeEvidenceJournal{}, verifier)
	w := postToolsCall(rs, token, `{"name":"search","arguments":{}}`)
	if w.Code != http.StatusForbidden {
		t.Errorf("attestation-error status = %d, want 403 (fail-closed); body=%s", w.Code, w.Body.String())
	}
	if got := up.count(); got != 0 {
		t.Errorf("upstream calls = %d, want 0", got)
	}
}

// adminOnlyPinVerifier implements ToolPinVerifier + ToolPinAdmin (the enterprise
// verifier's shape TODAY) but NOT the atomic ToolPinVerifyAttestor. It records
// whether the RS ever RE-READ pin state (Pins) after Verify — the round-3 TOCTOU
// the atomic capability abolished. The RS must bind pin POSTURE only for such a
// verifier, never re-read.
type adminOnlyPinVerifier struct {
	snaps     []PinSnapshot
	pinsReads int
}

func (v *adminOnlyPinVerifier) Verify(context.Context, string, string, string) (bool, string, error) {
	return true, "", nil
}
func (v *adminOnlyPinVerifier) RecordPin(context.Context, string, string, string) error { return nil }
func (v *adminOnlyPinVerifier) Pins() []PinSnapshot {
	v.pinsReads++
	return v.snaps
}
func (v *adminOnlyPinVerifier) Unpin(context.Context, string, string) error { return nil }
func (v *adminOnlyPinVerifier) ApplyPinChange(context.Context, ToolPinChange) (ToolPinApplyResult, error) {
	return ToolPinApplyResult{}, nil
}

// TestToolsCallAdminOnlyVerifierBindsPostureNoReread: a verifier exposing only
// the ToolPinAdmin operator surface (no atomic VerifyAndAttest) authorizes the
// call, binds pin POSTURE ("verified", explicit-absent identity), forwards once,
// and — critically — the RS NEVER re-reads Pins() after the decision (the removed
// two-step bridge was the TOCTOU: a re-pin between Verify and a Pins() read could
// bind an identity that never authorized). Honest absence beats a racy re-read.
func TestToolsCallAdminOnlyVerifierBindsPostureNoReread(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &countingUpstream{}
	verifier := &adminOnlyPinVerifier{snaps: []PinSnapshot{
		{Server: "", Tool: "search", Fingerprint: "fp-admin", PinCount: 3},
	}}
	rs := newEvidenceRSPin(t, jwks, up, &fakeEvidenceJournal{}, verifier)
	if w := postToolsCall(rs, token, `{"name":"search","arguments":{}}`); w.Code != http.StatusOK {
		t.Fatalf("admin-only verifier call status = %d; body=%s", w.Code, w.Body.String())
	}
	if got := up.count(); got != 1 {
		t.Errorf("upstream calls = %d, want 1", got)
	}
	if verifier.pinsReads != 0 {
		t.Errorf("RS re-read Pins() %d time(s) after Verify — a TOCTOU re-read; posture-only binding must never re-read", verifier.pinsReads)
	}
}

// TestToolsCallUpstreamDescriptorRebindsKeyedOperation: connector-level pin of
// the descriptor axis — two RS with different UpstreamDescriptor values sharing
// one journal: the same keyed request REBINDS on the second (different effect).
func TestToolsCallUpstreamDescriptorRebindsKeyedOperation(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	journal := &fakeEvidenceJournal{}
	up := &countingUpstream{}
	build := func(descriptor string) *ResourceServer {
		ts, err := NewToolset([]ToolPolicy{{Name: "search", RequiredScope: "tools:read"}})
		if err != nil {
			t.Fatalf("toolset: %v", err)
		}
		rs, err := NewResourceServer(ResourceServerConfig{
			Resource:                   rsResource,
			AuthorizationServers:       []string{rsIssuer},
			Issuer:                     rsIssuer,
			IssuerJWKS:                 jwks,
			Toolset:                    ts,
			Gate:                       fakeToolGate{StatusApproved},
			Upstream:                   up,
			Auditor:                    journal,
			UpstreamDescriptor:         descriptor,
			Clock:                      rsClock,
			DisableNextRevisionHeaders: true,
		})
		if err != nil {
			t.Fatalf("new rs: %v", err)
		}
		return rs
	}
	if w := postToolsCall(build("https-forward:https://a.example|cred-provider:static"), token, opKeyedSearch); w.Code != http.StatusOK {
		t.Fatalf("backend A status = %d; body=%s", w.Code, w.Body.String())
	}
	w2 := postToolsCall(build("https-forward:https://b.example|cred-provider:static"), token, opKeyedSearch)
	if w2.Code != http.StatusConflict {
		t.Fatalf("backend B status = %d, want 409; body=%s", w2.Code, w2.Body.String())
	}
	if code, _, _, _ := rpcErrorEnvelope(t, w2.Body.String()); code != rpcEvidenceRebind {
		t.Errorf("backend B code = %d, want %d (rebind: descriptor is part of the effect identity)", code, rpcEvidenceRebind)
	}
	if got := up.count(); got != 1 {
		t.Errorf("upstream calls = %d, want 1", got)
	}
}

// atomicRepinPinVerifier models the atomic capability under a concurrent re-pin:
// VerifyAndAttest snapshots the pin it decides against (`authorized`) and, in the
// SAME step, the operator re-pin to `repinTo` lands. Because the decision and the
// attestation are produced together (one lock/snapshot), the returned identity is
// exactly `authorized` — a later `repinTo` can NEVER be the identity that
// authorized. This is the structural fix for the round-3 TOCTOU: with the removed
// two-step Verify+ApprovedPin seam, the re-pin between the two reads bound B.
type atomicRepinPinVerifier struct {
	authorized ToolPinAttestation
	repinTo    ToolPinAttestation
	current    ToolPinAttestation
}

func (v *atomicRepinPinVerifier) Verify(context.Context, string, string, string) (bool, string, error) {
	return true, "", nil
}
func (v *atomicRepinPinVerifier) RecordPin(context.Context, string, string, string) error { return nil }
func (v *atomicRepinPinVerifier) VerifyAndAttest(context.Context, string, string, string) (ToolPinVerifyAttestation, error) {
	// Decide + attest against the CURRENT snapshot atomically...
	decided := v.authorized
	// ...and the operator re-pin lands the instant after (the atomicity guarantees
	// the returned identity is `decided`, never the post-decision `repinTo`).
	v.current = v.repinTo
	return ToolPinVerifyAttestation{Allowed: true, Attested: true, Pin: decided}, nil
}

// --- Round-3 P1: attestation TOCTOU — a re-pin racing the decision -------------
//
// VerifyAndAttest authorizes under pin A while an operator re-pin to B lands; the
// evidence must bind A (what authorized) or DENY — never B (false evidence). The
// atomic capability makes this structural: the fake flips to B the instant after
// deciding, and the RS must still bind A.
func TestToolsCallRepinInsideVerifyWindowNeverBindsStalePin(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &countingUpstream{}
	journal := &fakeEvidenceJournal{}
	pinA := ToolPinAttestation{Fingerprint: "fp-A", Version: "1"}
	pinB := ToolPinAttestation{Fingerprint: "fp-B", Version: "2"}
	verifier := &atomicRepinPinVerifier{authorized: pinA, repinTo: pinB}
	rs := newEvidenceRSPin(t, jwks, up, journal, verifier)

	params := `{"name":"search","arguments":{"q":"x"}}`
	w := postToolsCall(rs, token, params)

	// Recompute the two candidate digests with the SAME inputs the handler used.
	canon, err := canonicalizeToolCallParams(json.RawMessage(params))
	if err != nil {
		t.Fatal(err)
	}
	tok := validatedToken{Subject: "agent:claude", Issuer: rsIssuer}
	policy := ToolPolicy{Name: "search", RequiredScope: "tools:read"}
	scopes := sortedScopeSet(map[string]struct{}{"tools:read": {}})
	digestFor := func(p ToolPinAttestation) string {
		pd := toolCallPolicyDigest(policy,
			pinBinding{State: "attested", Fingerprint: p.Fingerprint, Version: p.Version},
			coazBinding{State: "unwired"})
		return deriveToolCallEffectDigest(rs.tenant, rs.resource, "tools/call", tok,
			"mcp.tool", "search", rs.upstreamDescriptor, scopes, canon, pd, "", "")
	}
	recorded := journal.lastBinding()
	switch {
	case w.Code == http.StatusForbidden || w.Code == http.StatusServiceUnavailable:
		// DENY under the racing re-pin is acceptable (fail-closed).
	case w.Code == http.StatusOK:
		if string(recorded.EffectDigest) == digestFor(pinB) {
			t.Fatalf("TOCTOU: the evidence bound re-pinned pin B (%s) while authorization ran under pin A — false evidence", pinB.Fingerprint)
		}
		if string(recorded.EffectDigest) != digestFor(pinA) {
			t.Fatalf("allowed call bound neither pin A nor pin B: digest %s", recorded.EffectDigest)
		}
	default:
		t.Fatalf("unexpected status %d; body=%s", w.Code, w.Body.String())
	}
}
