// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// rs_s700_render_hitl_test.go — Slice B: the ui:// render channel gets the
// two fields its elicitation SIBLING already carried (HITL, ApprovalPlanHash)
// and the seam that makes them mean something.
//
// THE DEFECT THESE PIN, measured on the pre tree: RenderInspectionDecision
// carried only Allow/Reason/Findings/Meter (renderinspect.go), so the `hitl`
// action of the firewall policy — "bloquea + abre aprobación + finding", in the
// content-firewall contract's own wording — had NO CHANNEL on this surface
// and collapsed onto plain `deny`. It was not that the logic chose wrong: the
// field to say it with did not exist.
//
// Every cell here forces the CONTENT to be clean (Allow: true) so nothing below
// can pass through the pre-existing content-deny leg wearing a HITL costume: if
// a render is withheld in these tests, only the approval gate can have withheld
// it.

const s700RenderPayload = "s700-render-payload"

func s700RenderResult(marker string) json.RawMessage {
	return json.RawMessage(`{"contents":[{"uri":"ui://srv/dashboard",` +
		`"mimeType":"text/html;profile=mcp-app","text":"<html>` + marker + `</html>"}]}`)
}

// hitlRenderInspector requests HITL on every render and names the plan its
// approval must be bound to. An empty plan exercises the fallback binding.
type hitlRenderInspector struct {
	plan  string
	calls []RenderInspectionInput
}

func (i *hitlRenderInspector) InspectRender(_ context.Context, in RenderInspectionInput) RenderInspectionDecision {
	i.calls = append(i.calls, in)
	return RenderInspectionDecision{Allow: true, HITL: true, ApprovalPlanHash: i.plan}
}

// cleanRenderInspector is the NON-FIRING direction: it inspects, finds nothing,
// and requests no approval.
type cleanRenderInspector struct{ calls int }

func (i *cleanRenderInspector) InspectRender(_ context.Context, _ RenderInspectionInput) RenderInspectionDecision {
	i.calls++
	return RenderInspectionDecision{Allow: true}
}

// denyAndHITLRenderInspector answers the COMPOSITE verdict the three independent
// fields permit: a content deny that also carries the HITL bit.
type denyAndHITLRenderInspector struct{}

func (denyAndHITLRenderInspector) InspectRender(_ context.Context, _ RenderInspectionInput) RenderInspectionDecision {
	return RenderInspectionDecision{
		Allow: false, Reason: "prompt injection detected", HITL: true, ApprovalPlanHash: "plan-render-A",
	}
}

// TestS700RenderHITLPlanMismatchWithheld is the ANTI-TOCTOU cell, and it is the
// reason ApprovalPlanHash is not decoration: the gate answers APPROVED, but the
// approval it holds is bound to ANOTHER plan. An approval granted for one
// render's content may not release a different one, so the render is withheld —
// the same rule tools/call (rs.go:570) and the elicitation sibling already obey.
func TestS700RenderHITLPlanMismatchWithheld(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &fakeUpstream{result: s700RenderResult(s700RenderPayload)}
	aud := &recordingAuditor{}
	inspector := &hitlRenderInspector{plan: "plan-render-A"}
	gate := &planHashGate{plan: "plan-render-B"}
	rs := newS504R1RS(t, jwks, up, aud, inspector, nil, gate)

	rec := httptest.NewRecorder()
	rs.ServeHTTP(rec, resourcesReadReq(token, "ui://srv/dashboard"))

	if strings.Contains(rec.Body.String(), s700RenderPayload) {
		t.Fatalf("a render approved for ANOTHER plan was released: %s", rec.Body.String())
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (an approval bound to another plan is not an approval); body: %s",
			rec.Code, rec.Body.String())
	}
	if len(gate.requests) != 1 || gate.requests[0].PlanHash != "plan-render-A" {
		t.Fatalf("the gate must be asked about THE INSPECTOR'S plan, once: %+v", gate.requests)
	}
	if gate.requests[0].Tool != ChannelRender {
		t.Fatalf("approval Tool = %q, want the render channel %q", gate.requests[0].Tool, ChannelRender)
	}
	// The bytes were FETCHED before the inspector could look at them, so the
	// refusal is the release CHILD's disposition, never the parent's: the
	// dispatch happened and stays durable, the release did not.
	if got := aud.settledActionCount(s504ReleaseActionPrefix, DispatchWithheld); got != 1 {
		t.Fatalf("withheld RELEASE-CHILD settlements = %d, want exactly 1", got)
	}
	if got := aud.settledCount(DispatchCompleted); got != 1 {
		t.Fatalf("completed settlements = %d, want exactly 1 (the PARENT records the dispatch fact)", got)
	}
}

// TestS700RenderHITLPlanMatchReleases is the cell that proves the channel is a
// CHANNEL and not a second deny: with the approval bound to THIS render's plan,
// the template is released. Mutating the HITL leg into the deny branch — which
// is the pre behavior — turns this red.
func TestS700RenderHITLPlanMatchReleases(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &fakeUpstream{result: s700RenderResult(s700RenderPayload)}
	inspector := &hitlRenderInspector{plan: "plan-render-A"}
	gate := &planHashGate{plan: "plan-render-A"}
	rs := newS504R1RS(t, jwks, up, &recordingAuditor{}, inspector, nil, gate)

	rec := httptest.NewRecorder()
	rs.ServeHTTP(rec, resourcesReadReq(token, "ui://srv/dashboard"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (approval bound to THIS plan releases the render); body: %s",
			rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), s700RenderPayload) {
		t.Fatalf("an approved render must reach the client: %s", rec.Body.String())
	}
	if len(gate.requests) != 1 {
		t.Fatalf("gate consultations = %d, want exactly 1", len(gate.requests))
	}
}

// TestS700RenderHITLDenyClosedWithNoGateWired: a deployment that asks for human
// approval without wiring an approval gate gets a REFUSAL, not a release.
// rs.gate defaults to denyApprovalGate (rsconfig.go:355), and this pins that the
// render leg inherits that default rather than skipping the check when no gate
// is configured.
func TestS700RenderHITLDenyClosedWithNoGateWired(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &fakeUpstream{result: s700RenderResult(s700RenderPayload)}
	inspector := &hitlRenderInspector{plan: "plan-render-A"}
	rs := newS504R1RS(t, jwks, up, &recordingAuditor{}, inspector, nil, nil)

	rec := httptest.NewRecorder()
	rs.ServeHTTP(rec, resourcesReadReq(token, "ui://srv/dashboard"))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (no gate wired ⇒ deny-closed); body: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), s700RenderPayload) {
		t.Fatalf("an unapproved render was released with no gate wired: %s", rec.Body.String())
	}
}

// TestS700RenderWithoutHITLNeverConsultsTheGate is the NON-FIRING DIRECTION, and
// without it the three cells above are satisfied by a leg that withholds
// everything. A clean inspector that requests no approval must release the
// render AND must not consult the gate at all — the HITL bit is what opens the
// approval, not the mere presence of an inspector.
func TestS700RenderWithoutHITLNeverConsultsTheGate(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &fakeUpstream{result: s700RenderResult(s700RenderPayload)}
	inspector := &cleanRenderInspector{}
	gate := &planHashGate{plan: "an-approval-nobody-should-need"}
	rs := newS504R1RS(t, jwks, up, &recordingAuditor{}, inspector, nil, gate)

	rec := httptest.NewRecorder()
	rs.ServeHTTP(rec, resourcesReadReq(token, "ui://srv/dashboard"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a clean render needs no approval); body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), s700RenderPayload) {
		t.Fatalf("a clean render must reach the client: %s", rec.Body.String())
	}
	if inspector.calls != 1 {
		t.Fatalf("inspector calls = %d, want 1 (the inspector DID run — this is not a green from a skipped gate)", inspector.calls)
	}
	if len(gate.requests) != 0 {
		t.Fatalf("gate consultations = %d, want 0: only a HITL decision opens an approval; %+v",
			len(gate.requests), gate.requests)
	}
}

// TestS700RenderApprovalFallbackBindsTheContent: when the inspector requests
// HITL WITHOUT naming a plan, the fallback must still bind the anti-TOCTOU
// property — otherwise the empty-plan path silently reintroduces exactly the
// reuse the field exists to prevent.
//
// Two renders of the SAME template by the SAME subject differing ONLY in their
// HTML must produce DIFFERENT plan hashes. A fallback seeded on channel+subject
// alone (the elicitation seam's weaker seed, evaluateElicitationVerdict) makes
// these two identical and turns this cell red.
func TestS700RenderApprovalFallbackBindsTheContent(t *testing.T) {
	planFor := func(t *testing.T, marker string) string {
		t.Helper()
		token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
		up := &fakeUpstream{result: s700RenderResult(marker)}
		gate := &planHashGate{plan: "bound-to-nothing-here"}
		rs := newS504R1RS(t, jwks, up, &recordingAuditor{},
			&hitlRenderInspector{plan: ""}, nil, gate)

		rec := httptest.NewRecorder()
		rs.ServeHTTP(rec, resourcesReadReq(token, "ui://srv/dashboard"))

		if len(gate.requests) != 1 {
			t.Fatalf("gate consultations = %d, want exactly 1", len(gate.requests))
		}
		if gate.requests[0].PlanHash == "" {
			t.Fatal("the fallback must produce a NON-EMPTY plan: an empty plan is bound to nothing, " +
				"and the strict equality check would then accept an unbound approval")
		}
		return gate.requests[0].PlanHash
	}

	first := planFor(t, "s700-clean-template")
	second := planFor(t, "s700-poisoned-template")
	if first == second {
		t.Fatalf("both renders asked for the SAME plan %q: an approval granted for one template's "+
			"content would release the other — the anti-TOCTOU binding is not binding the content", first)
	}
	// A NON-DETERMINISTIC plan would satisfy the inequality above while being
	// useless: nobody can approve a plan that changes between the request and the
	// approval. The same render must always ask for the same plan.
	if again := planFor(t, "s700-clean-template"); again != first {
		t.Fatalf("the same render asked for two different plans (%q then %q): a plan nobody can "+
			"approve twice is not an approval binding", first, again)
	}
}

// TestS700RenderApprovalPlanIsInjective is the contrast's P1, pinned.
//
// The fallback used to be SHA256(channel + "|" + subject + "|" + uri + "|" + hash),
// and a delimiter join is not an injective encoding: nothing forbids `|` in a
// `sub` claim (tokenvalidate.go) or in an inventoried ui:// URI (apps.go), so the
// two DIFFERENT (subject, uri) pairs below hashed the SAME preimage. Because the
// production approval adapter keys an approval on (action, tool, plan) and NOT on
// the subject (cmd/olivares/mcpgateway.go, approvalbridge.go), that collision let
// ONE SUBJECT'S APPROVAL RELEASE ANOTHER SUBJECT'S RENDER — a fail-open the
// original cells could not see, because they only ever varied the content.
//
// evidenceLPDigest is length-prefixed ("never delimiter joins", evidence.go:751-760).
func TestS700RenderApprovalPlanIsInjective(t *testing.T) {
	// Each pair is TWO DISTINCT tuples that a `|` join flattens to one preimage.
	// Both must be real collisions under the old encoding, or the cell proves
	// nothing: a subtest that passes under the defect AND the fix is exactly the
	// "wrong implementation still green" the contrast criticised.
	for _, c := range []struct {
		name              string
		aSub, aURI, aHash string
		bSub, bURI, bHash string
	}{
		{
			name: "the boundary moves between subject and uri",
			aSub: "alice", aURI: "ui://srv/a|ui://srv/b", aHash: "H",
			bSub: "alice|ui://srv/a", bURI: "ui://srv/b", bHash: "H",
		},
		{
			name: "the boundary moves between uri and content hash",
			aSub: "alice", aURI: "ui://srv/a", aHash: "b|H",
			bSub: "alice", bURI: "ui://srv/a|b", bHash: "H",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			first := renderApprovalPlan("", c.aSub, c.aURI, c.aHash)
			second := renderApprovalPlan("", c.bSub, c.bURI, c.bHash)
			if first == second {
				t.Fatalf("two DIFFERENT tuples produced the same plan %q: an approval for "+
					"(%q, %q, %q) releases the render of (%q, %q, %q)",
					first, c.aSub, c.aURI, c.aHash, c.bSub, c.bURI, c.bHash)
			}
		})
	}

	// The direction of no-firing: an INJECTIVE encoding must still be stable for
	// the same tuple, or nothing is approvable.
	if a, b := renderApprovalPlan("", "alice", "ui://srv/a", "H"), renderApprovalPlan("", "alice", "ui://srv/a", "H"); a != b {
		t.Fatalf("the same tuple produced two plans: %q vs %q", a, b)
	}
	// And an inspector that NAMES its plan still wins outright.
	if got := renderApprovalPlan("named", "alice", "ui://srv/a", "H"); got != "named" {
		t.Fatalf("plan = %q, want the inspector's own %q", got, "named")
	}
}

// TestS700RenderContentDenyBeatsHITL pins the content-firewall contract's
// `deny > hitl` precedence on the COMPOSITE verdict the three independent
// fields permit.
//
// The contrast caught this: the first cut evaluated HITL first "like the
// sibling", so a decision that was BOTH a deny and a HITL opened an approval a
// human could grant — and the render was refused for content anyway once granted.
// Approving something the policy already refused is a workflow the contract
// forbids, and no cell forced the combination, so the wrong order stayed green.
func TestS700RenderContentDenyBeatsHITL(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &fakeUpstream{result: s700RenderResult(s700RenderPayload)}
	// A gate that WOULD approve the inspector's plan, so a green here cannot come
	// from the approval merely failing.
	gate := &planHashGate{plan: "plan-render-A"}
	rs := newS504R1RS(t, jwks, up, &recordingAuditor{}, denyAndHITLRenderInspector{}, nil, gate)

	rec := httptest.NewRecorder()
	rs.ServeHTTP(rec, resourcesReadReq(token, "ui://srv/dashboard"))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), s700RenderPayload) {
		t.Fatalf("a denied render was released: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "denied by inspection") {
		t.Fatalf("the client must be told the CONTENT was denied, not that it needs an approval "+
			"it can never usefully obtain; body: %s", rec.Body.String())
	}
	if len(gate.requests) != 0 {
		t.Fatalf("a deny must not open an approval: %d gate consultation(s), %+v",
			len(gate.requests), gate.requests)
	}
}

// TestS700ApprovedRenderNamesItsApproval pins the contrast's other P1: a HITL
// delivery whose evidence cannot say WHICH approval released the bytes is durable
// but not attributable. tools/call has carried its ApprovalRef since
// (rs.go:637-641, :679); the render release now does too.
func TestS700ApprovedRenderNamesItsApproval(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &fakeUpstream{result: s700RenderResult(s700RenderPayload)}
	aud := &recordingAuditor{}
	gate := &planHashGate{plan: "plan-render-A"} // answers ApprovalRef "appr-s504-r1"
	rs := newS504R1RS(t, jwks, up, aud, &hitlRenderInspector{plan: "plan-render-A"}, nil, gate)

	rec := httptest.NewRecorder()
	rs.ServeHTTP(rec, resourcesReadReq(token, "ui://srv/dashboard"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	named := false
	for _, d := range aud.decisions {
		if strings.HasPrefix(d.EffectAction, releaseActionPrefix) && d.ApprovalRef == "appr-s504-r1" {
			named = true
		}
	}
	if !named {
		t.Fatalf("no release decision named the approval that authorized it; decisions: %+v", aud.decisions)
	}
}
