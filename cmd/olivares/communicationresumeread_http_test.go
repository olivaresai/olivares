// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

// The reader uses the same real tuple resolver, PDP, Claim barrier, sealer and
// HTTP handlers as the product. Only provider process creation is omitted.
func TestCommunicationResumeReadIsolationHTTP(t *testing.T) {
	e := bootIncomingHandoffHTTPEstate(t, communicationHTTPTestSQLiteStore(t))
	const canary = "resume history stays with its exact agent session"
	offer := e.offer(t, map[string]any{"kind": "session", "ref": e.targetSession.sid}, canary, time.Hour)
	notice := communicationHTTPTestRequest(t, e.eng, http.MethodPost, "/v1/m/sessions/messages/send", e.owner.token, e.tenant,
		map[string]any{"channel_id": e.channelID, "recipient": map[string]any{"kind": "session", "ref": e.targetSession.sid},
			"content": map[string]any{"subject": "Earlier generation notice", "blocks": []map[string]any{{"type": "text", "format": "plain", "text": canary}}}},
		map[string]string{"Idempotency-Key": model.NewID().String()})
	if notice.status != http.StatusCreated {
		t.Fatalf("send before resume = %d: %s", notice.status, notice.raw)
	}
	sent := communicationHTTPTestDecode[sessions.DirectNoticePublishResult](t, notice)
	accepted := communicationHTTPTestRequest(t, e.eng, http.MethodPost,
		"/v1/m/sessions/handoffs/"+offer.HandoffID.String()+"/responses", e.targetSession.communication.Token, e.tenant,
		map[string]any{"transition": "accept"}, map[string]string{"If-Match": offer.ETag, "Idempotency-Key": model.NewID().String()})
	if accepted.status != http.StatusOK {
		t.Fatalf("accept before resume = %d: %s", accepted.status, accepted.raw)
	}
	issuer, err := auth.NewSystemOperator("test:resume-read", "issue bounded successor fixture credentials")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var otherWorkspace model.ID
	if err := e.eng.store.Mutate(ctx, e.tenant, func(sc store.Scope) error {
		ws, err := sc.Workspaces().Create(ctx, model.Workspace{Name: "Other resume scope", Slug: "other-resume-scope", Status: model.StatusActive})
		otherWorkspace = ws.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	mintSuccessor := func(change func(*auth.CommunicationSessionCredentialSpec)) string {
		t.Helper()
		if err := e.eng.sessionsMod.Release(ctx, e.tenant, e.targetSession.sid, e.targetSession.agentRef, e.targetSession.claim.Fence); err != nil {
			t.Fatal(err)
		}
		claim, err := e.eng.sessionsMod.Claim(ctx, e.tenant, e.targetSession.sid, e.targetSession.agentRef, time.Hour)
		if err != nil || claim.Fence <= e.targetSession.claim.Fence {
			t.Fatalf("successor Claim = %+v, %v", claim, err)
		}
		e.targetSession.claim = claim
		spec := auth.CommunicationSessionCredentialSpec{Tenant: e.tenant, WorkspaceID: e.workspace,
			SessionRef: e.targetSession.sid, RunRef: e.targetSession.runRef, AgentRef: e.targetSession.agentRef, ClaimFence: claim.Fence}
		if change != nil {
			change(&spec)
		}
		credential, err := e.eng.authr.IssueCommunicationSessionCredential(ctx, issuer, spec)
		if err != nil {
			t.Fatal(err)
		}
		return credential.Token
	}
	assertDenied := func(label, token string) {
		t.Helper()
		got := e.detail(t, token, offer.DeliveryID)
		if (got.status != http.StatusUnauthorized && got.status != http.StatusForbidden &&
			got.status != http.StatusNotFound && got.status != http.StatusServiceUnavailable) ||
			strings.Contains(string(got.raw), canary) {
			t.Fatalf("%s disclosed earlier carrier: %d: %s", label, got.status, got.raw)
		}
	}
	// A valid other session is allowed on the same channel, but owns no carrier.
	assertDenied("other SID", offer.source.communication.Token)
	old := e.targetSession.communication.Token
	for _, tc := range []struct {
		name   string
		change func(*auth.CommunicationSessionCredentialSpec)
	}{
		{"other agent", func(s *auth.CommunicationSessionCredentialSpec) { s.AgentRef = "agent:foreign" }},
		{"other workspace", func(s *auth.CommunicationSessionCredentialSpec) { s.WorkspaceID = otherWorkspace }},
		{"forged future fence", func(s *auth.CommunicationSessionCredentialSpec) { s.ClaimFence++ }},
	} {
		// A private test issuer intentionally issues a bad tuple. This does not
		// forge public claims; the real resolver must still refuse that tuple.
		assertDenied(tc.name, mintSuccessor(tc.change))
	}
	assertDenied("old process after resume", old)
	current := mintSuccessor(nil)
	before := communicationHTTPTestEffects(t, e.eng, e.tenant)
	oldAct := communicationHTTPTestRequest(t, e.eng, http.MethodPost,
		"/v1/m/sessions/handoffs/"+offer.HandoffID.String()+"/responses", old, e.tenant,
		map[string]any{"transition": "accept"}, map[string]string{"If-Match": `"v2"`, "Idempotency-Key": model.NewID().String()})
	if oldAct.status != http.StatusUnauthorized {
		t.Fatalf("old process acted after resume: %d: %s", oldAct.status, oldAct.raw)
	}
	got := e.detail(t, current, offer.DeliveryID)
	if got.status != http.StatusOK || !strings.Contains(string(got.raw), canary) {
		t.Fatalf("same SID/agent/workspace successor read = %d: %s", got.status, got.raw)
	}
	detail := communicationHTTPTestDecode[sessions.IncomingHandoffReadResult](t, got)
	if detail.Handoff.State != sessions.HandoffAccepted {
		t.Fatalf("resume changed accepted state: %+v", detail.Handoff)
	}
	page := e.page(t, current, "accepted", 10, "")
	if len(page.Items) != 1 || page.Items[0].Carrier.DeliveryID != offer.DeliveryID {
		t.Fatalf("accepted resume inbox = %+v", page)
	}
	inbox := communicationHTTPTestRequest(t, e.eng, http.MethodGet,
		fmt.Sprintf("/v1/m/sessions/inbox?workspace_id=%s&limit=10", e.workspace), current, e.tenant, nil, nil)
	if inbox.status != http.StatusOK || !strings.Contains(string(inbox.raw), canary) {
		t.Fatalf("direct inbox after resume = %d: %s", inbox.status, inbox.raw)
	}
	items := communicationHTTPTestDecode[sessions.DirectNoticeInboxPage](t, inbox)
	if len(items.Items) != 1 || items.Items[0].Delivery.ID != sent.DeliveryID {
		t.Fatalf("successor inbox lost original notice: %+v", items)
	}
	opened := communicationHTTPTestRequest(t, e.eng, http.MethodGet,
		"/v1/m/sessions/deliveries/"+sent.DeliveryID.String(), current, e.tenant, nil, nil)
	if opened.status != http.StatusOK || !strings.Contains(string(opened.raw), canary) {
		t.Fatalf("successor direct carrier read = %d: %s", opened.status, opened.raw)
	}
	assertCommunicationHTTPTestNoEffects(t, e.eng, e.tenant, before, "successor carrier reads")
}
