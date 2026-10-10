// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure"
)

func TestDecisionRequestHTTPResponseIsAuthorizedAndIdempotent(t *testing.T) {
	f := newDecisionResponseFixture(t)
	// The fixture evidence must fit inside the request deadline.
	ctx := decisionResponseTestContext(t)
	ready := &communicationReadinessStub{storeReady: true, sealerReady: true, pumpReady: true}
	f.m.CommunicationSessionCreds = ready
	func() { f.m.CommunicationSealer = ready; f.m.normalize() }()
	f.m.CommunicationStoreReadiness = ready
	f.m.CommunicationPumpReadiness = ready
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := audit.NewSigner(key)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := api.New(api.Options{Store: f.st, Authenticator: f.authr, Authorizer: auth.NewAuthorizer(nil), Signer: signer, SetupToken: secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token")), Clock: f.m.clock, Modules: []api.Module{f.m}})
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := f.authr.Login(context.Background(), "sender@direct-notice.test", "direct-notice-password", "127.0.0.2")
	if err != nil {
		t.Fatal(err)
	}
	cmd := decisionResolveCommand(f.request.Version, model.NewID(), "Proceed with the work")
	call := func(bearer string, command DecisionRequestResponseCommand) *httptest.ResponseRecorder {
		raw, err := json.Marshal(command)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/v1/m/sessions/decision-requests/"+f.request.ID.String()+"/responses", bytes.NewReader(raw))
		req = req.WithContext(ctx)
		req.RemoteAddr = "127.0.0.2:1234"
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("X-Olivares-Tenant", f.tenant.String())
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("If-Match", command.IfMatch)
		req.Header.Set("Idempotency-Key", command.IdempotencyKey)
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		return w
	}
	if got := call("invalid", cmd); got.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated response=%d", got.Code)
	}
	allowed := f.source.evidence
	f.source.evidence = auth.AuthorizationEvidence{
		Outcome:        auth.EvidenceDeny,
		CorePermission: auth.CheckEvidence{Verdict: auth.CheckBroken, Code: "credential_ceiling_denied"},
		ResourceGuard:  auth.CheckEvidence{Verdict: auth.CheckUnknown, Code: "not_evaluated"},
		ForbidAbsence:  auth.CheckEvidence{Verdict: auth.CheckUnknown, Code: "not_evaluated"},
	}
	if denied := call(token, cmd); denied.Code != http.StatusForbidden {
		t.Fatalf("denied response=%d %s", denied.Code, denied.Body.String())
	}
	if rows := communicationRowsForTest(t, f.directNoticeFixture, decisionResponseKind); len(rows) != 0 {
		t.Fatal("denied response produced an effect")
	}
	f.source.evidence = allowed
	first := call(token, cmd)
	if first.Code != http.StatusOK {
		t.Fatalf("respond=%d %s", first.Code, first.Body.String())
	}
	var result DecisionRequestResponseResult
	if err := json.Unmarshal(first.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.AuditSeq <= 0 || result.State != DecisionResolved || result.WorkDecisionID.IsZero() || first.Header().Get("ETag") != `"v2"` {
		t.Fatalf("response=%+v etag=%q", result, first.Header().Get("ETag"))
	}
	again := call(token, cmd)
	var replay DecisionRequestResponseResult
	if again.Code != http.StatusOK || json.Unmarshal(again.Body.Bytes(), &replay) != nil || replay.WorkDecisionID != result.WorkDecisionID || replay.ResponseID != result.ResponseID {
		t.Fatalf("retry=%d %s", again.Code, again.Body.String())
	}
	cmd.Response.ChoiceKey = "no"
	if changed := call(token, cmd); changed.Code != http.StatusConflict {
		t.Fatalf("changed retry=%d %s", changed.Code, changed.Body.String())
	}
	if rows := communicationRowsForTest(t, f.directNoticeFixture, decisionResponseKind); len(rows) != 1 {
		t.Fatalf("response effects=%d", len(rows))
	}
	ready.pumpReady = false
	if stopped := call(token, cmd); stopped.Code != http.StatusServiceUnavailable {
		t.Fatalf("not ready=%d %s", stopped.Code, stopped.Body.String())
	}
}
