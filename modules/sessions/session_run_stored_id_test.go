// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
)

// The native run routes name a run by its public run_ref but authorize the
// resource {kind "run", its stored row ID} (runtimeRoutes). Every other question
// about a run must name that same resource, or a policy written against it does
// not reach the question.

// storedRunIDForTest reads the stored row ID of the run runRef names.
func storedRunIDForTest(t *testing.T, st store.Store, tenant model.TenantID, runRef string) string {
	t.Helper()
	var id string
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		runs, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rec, err := findRunRec(context.Background(), runs, runRef)
		id = rec.String(model.ColID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if id == "" || id == runRef {
		t.Fatal("fixture run reference must name a distinct stored row")
	}
	return id
}

// runForbid is a Cedar forbid on one run's native resource for one permission,
// answered as deny-overlay evidence so a route mutation can rely on it.
func runForbid(t *testing.T, st store.Store, tenant model.TenantID, id string, perm auth.Permission) auth.PolicyEvaluator {
	t.Helper()
	eval, err := governance.NewCedarEvaluator(`forbid(principal, action, resource == Resource::"`+id+`") when { context.permission == "`+string(perm)+`" && resource.kind == "run" };`, nil)
	if err != nil {
		t.Fatal(err)
	}
	var fact store.AuthorizationFactRef
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		var err error
		fact, err = sc.(store.AuthorizationEpochReader).ReadAuthorizationEpoch(context.Background())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return cedarEvidence{eval, fact}
}

// cedarEvidence reports a Cedar verdict through the deny-overlay evidence seam,
// fresh for a minute against the store's current authorization epoch.
type cedarEvidence struct {
	*governance.CedarEvaluator
	fact store.AuthorizationFactRef
}

func (c cedarEvidence) EvaluateEvidence(ctx context.Context, req auth.Request) (auth.PolicyEvidenceDecision, error) {
	d, err := c.Evaluate(ctx, req)
	if err != nil {
		return auth.PolicyEvidenceDecision{}, err
	}
	forbid := auth.CheckEvidence{Verdict: auth.CheckClean, Code: "cedar_forbid_absent"}
	if !d.Allow {
		forbid = auth.CheckEvidence{Verdict: auth.CheckBroken, Code: "cedar_forbid_matched"}
	}
	now := time.Now()
	return auth.PolicyEvidenceDecision{ForbidAbsence: forbid, Facts: []store.AuthorizationFactRef{c.fact}, ObservedAt: now.Add(-time.Second), FreshUntil: now.Add(time.Minute)}, nil
}

func TestStoredRunIDReadsTheRunByItsReference(t *testing.T) {
	f := newManagedStopFixtureWith(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, WithRunner(&workControlRunner{}), WithCredentialSource(staticCred()))
	dto, err := createProfiledTestRun(t, f.m, context.Background(), f.tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:launch", ActorKind: model.ActorUser})
	if err != nil {
		t.Fatal(err)
	}
	if id, err := StoredRunID(context.Background(), f.st, f.tenant, dto.RunRef); err != nil || id.String() != storedRunIDForTest(t, f.st, f.tenant, dto.RunRef) {
		t.Errorf("stored run ID = %q %v", id, err)
	}
	if id, err := StoredRunID(context.Background(), f.st, f.tenant, "no-such-run"); err == nil || !id.IsZero() {
		t.Errorf("unknown run = %q %v, want an error", id, err)
	}
	if id, err := StoredRunID(context.Background(), nil, f.tenant, dto.RunRef); err == nil || !id.IsZero() {
		t.Errorf("no store = %q %v, want an error", id, err)
	}
}

func TestManagedStopRunQuestionAuthorizesTheStoredRunID(t *testing.T) {
	f := newManagedStopFixture(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true})
	op := f.operator("stored-id@w2.test", auth.RoleEditor, true, 2*time.Minute)
	denied, deniedLive := f.launch("thread-stored-denied")
	sibling, siblingLive := f.launch("thread-stored-sibling")
	f.m.ManagedStopAuthority = NewManagedStopAuthority(f.authr, auth.NewAuthorizer(runForbid(t, f.st, f.tenant, storedRunIDForTest(t, f.st, f.tenant, denied.RunRef), permRunWrite)), f.st.Leader())

	res, err := f.call(f.request(op, denied, deniedLive.launchID, "stored-denied"))
	f.refuseWithNoJournalRow(res, err, "stored-denied", ManagedStopForbidden)
	if res.Detail != "the run question was not answered with a permit" {
		t.Fatalf("refusal detail = %q, want the run question's", res.Detail)
	}
	if res, err := f.call(f.request(op, sibling, siblingLive.launchID, "stored-sibling")); err != nil || res.Outcome != ManagedStopStopped {
		t.Fatalf("stop the sibling session = %+v %v", res, err)
	}
}

func TestSessionPeerChoiceAuthorizesTheNativeRunResource(t *testing.T) {
	f, _, _ := newSessionPeersFixture(t, false)
	denied, deniedRun := newSessionPeer(t, f, "", "")
	sibling, _ := newSessionPeer(t, f, "", "")
	WithWorkAuthorizer(auth.NewAuthorizer(runForbid(t, f.h.st, f.tenant, storedRunIDForTest(t, f.h.st, f.tenant, deniedRun), permRunRead)))(f.h.m)
	for peer, want := range map[string]int{denied: http.StatusUnprocessableEntity, sibling: http.StatusOK} {
		got := f.h.doJSON("PUT", "/v1/m/sessions/runs/"+f.run+"/peers", f.admin, map[string]any{"peers": []string{peer}}, tenantHdr(f.tenant))
		if got.code != want {
			t.Errorf("choose peer %s = %d %s, want %d", peer, got.code, got.raw, want)
		}
	}
}
