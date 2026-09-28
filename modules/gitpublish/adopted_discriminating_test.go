// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package gitpublish

// Discriminating tests adopted from the construction read (they kill the
// reader's MUT03, MUT07 and MUT08), renamed beside the reader's overlays.
// The ABA test uses a prefixed branch: after B1 a push never targets main.

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	gp "github.com/olivaresai/olivares/connectors/gitpublish"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// The custody seam receives the caller's tenant and the STORED target's
// workspace (B1: bindings are selected in the caller's permitted scope).
type recordingCustody struct {
	*fakeCustody
	mu    sync.Mutex
	calls [][2]string
}

func (c *recordingCustody) CredentialBinding(ctx context.Context, tn model.TenantID, ws model.ID, id string) (CredentialBinding, error) {
	c.mu.Lock()
	c.calls = append(c.calls, [2]string{tn.String(), ws.String()})
	c.mu.Unlock()
	return c.fakeCustody.CredentialBinding(ctx, tn, ws, id)
}

func (c *recordingCustody) RepositoryBinding(ctx context.Context, tn model.TenantID, ws model.ID, id string) (RepositoryBinding, error) {
	c.mu.Lock()
	c.calls = append(c.calls, [2]string{tn.String(), ws.String()})
	c.mu.Unlock()
	return c.fakeCustody.RepositoryBinding(ctx, tn, ws, id)
}

// Root 4f50c87d §2 discriminating interleaving, with the ORIGINAL dispatcher
// still blocked: held write -> sweep marks it uncertain and GET shows the old
// ref -> retry (same and new operation id) -> the original write completes.
// Exactly one dispatch; the original acknowledgment settles the intent.
func TestAdoptedHeldWriteSweepGetOldRetryThenOriginalCompletes(t *testing.T) {
	h := newHarness(t)
	h.host.refs["olivares/h"] = shaBase
	bg := &heldGit{fakeGit: h.git, release: make(chan struct{}), started: make(chan struct{}, 4)}
	h.m.opts.Git = bg
	h.m.opts.DispatchTimeout = 60 * time.Millisecond
	h.m.opts.Skew = 20 * time.Millisecond
	done := make(chan Receipt, 1)
	go func() {
		r, _ := h.push(h.user(), "op-h", "refs/heads/olivares/h", shaBase)
		done <- r
	}()
	<-bg.started
	in := intentByOperation(t, h, effectPush, "op-h")
	if in.State != StateDispatching {
		t.Fatalf("while held: %s", in.State)
	}
	if rc, err := h.m.Reconcile(context.Background(), h.user(), in.ID); err != nil || rc.Intent.State != StateDispatching {
		t.Fatalf("reconcile while dispatching = %+v %v", rc.Intent, err)
	}
	time.Sleep(150 * time.Millisecond) // past dispatch_deadline + skew (database time)
	if err := h.m.SweepDue(context.Background(), h.tenant); err != nil {
		t.Fatal(err)
	}
	if s := intentByOperation(t, h, effectPush, "op-h"); s.State != StateUncertain {
		t.Fatalf("after sweep: %s", s.State)
	}
	if r, err := h.push(h.user(), "op-h", "refs/heads/olivares/h", shaBase); err != nil || r.Intent.State != StateUncertain {
		t.Fatalf("same-op retry = %+v %v", r.Intent, err)
	}
	if _, err := h.push(h.user(), "op-h2", "refs/heads/olivares/h", shaBase); codeOf(err) != "unresolved_intent" {
		t.Fatalf("new-op retry = %v", err)
	}
	if _, err := h.push(h.admin(), "op-h3", "refs/heads/olivares/h", shaBase); codeOf(err) != "unresolved_intent" {
		t.Fatalf("another subject, new op = %v", err)
	}
	close(bg.release)
	var final Receipt
	select {
	case final = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("dispatcher never returned")
	}
	if bg.count() != 1 {
		t.Fatalf("dispatches = %d, want 1", bg.count())
	}
	got := intentByOperation(t, h, effectPush, "op-h")
	t.Logf("dispatcher answer %s; stored state=%s receipt=%s ack=%v", final.Answer, got.State, got.Receipt, got.Acknowledged.Acknowledged)
	if got.State != StateApplied || got.Receipt != ReceiptCaused || !got.Acknowledged.Acknowledged {
		t.Fatalf("after the original completes = %+v", got)
	}
	var sawOld, sawLoser bool
	for _, o := range storedObservations(t, h, got.ID) {
		sawOld = sawOld || o.Result == "absent_or_old"
		sawLoser = sawLoser || o.Source == "w2_loser"
	}
	if !sawOld || !sawLoser {
		t.Fatalf("observations lost: old=%v w2_loser=%v", sawOld, sawLoser)
	}
	h.balanced()
}

// Old-ref ABA followed by BOTH retry forms: still exactly one dispatch.
func TestAdoptedABAThenRetryNoSecondDispatch(t *testing.T) {
	h := newHarness(t)
	h.m.opts.DispatchTimeout = 80 * time.Millisecond
	h.git.hold = make(chan struct{})
	defer close(h.git.hold)
	h.host.refs["olivares/aba"] = shaBase // B1: a prefixed branch, not the merge base
	r, _ := h.push(h.user(), "op-aba", "refs/heads/olivares/aba", shaBase)
	for _, v := range []string{shaOther, shaBase} {
		h.host.mu.Lock()
		h.host.refs["olivares/aba"] = v
		h.host.mu.Unlock()
		_, _ = h.m.Reconcile(context.Background(), h.user(), r.Intent.ID)
	}
	if r2, _ := h.push(h.user(), "op-aba", "refs/heads/olivares/aba", shaBase); r2.Intent.State != StateUncertain {
		t.Fatalf("same op after ABA = %s", r2.Intent.State)
	}
	if _, err := h.push(h.user(), "op-aba-2", "refs/heads/olivares/aba", shaBase); codeOf(err) != "unresolved_intent" {
		t.Fatalf("new op after ABA = %v", err)
	}
	if h.git.count() != 1 {
		t.Fatalf("dispatches = %d", h.git.count())
	}
}

// Merge: an unreadable (permission-hidden) change keeps uncertainty; PR: an
// incomplete lookup and an empty lookup keep uncertainty; no second write.
func TestAdoptedMergeHiddenAndPRLookupKeepUncertainty(t *testing.T) {
	h := newHarness(t)
	openPR(h, 7, shaCommit)
	amb := gp.Result{Class: gp.Ambiguous, Reason: "transport"}
	h.host.mergeRes = &amb
	r, err := h.m.Merge(context.Background(), h.admin(), MergeInput{Target: h.target.ID, OperationID: "op-m", Number: 7, ExpectedHead: shaCommit, Method: "merge"})
	if err != nil || r.Intent.State != StateUncertain {
		t.Fatalf("ambiguous merge = %+v %v", r.Intent, err)
	}
	h.host.mu.Lock()
	h.host.changes = nil // GetChange now fails: 404/hidden stand-in
	h.host.mu.Unlock()
	if rc, _ := h.m.Reconcile(context.Background(), h.admin(), r.Intent.ID); rc.Intent.State != StateUncertain {
		t.Fatalf("hidden change: %s", rc.Intent.State)
	}
	openPR(h, 7, shaCommit) // visible again, still open and unmerged
	if rc, _ := h.m.Reconcile(context.Background(), h.admin(), r.Intent.ID); rc.Intent.State != StateUncertain {
		t.Fatalf("open unmerged: %s", rc.Intent.State)
	}
	if _, err := h.m.Merge(context.Background(), h.admin(), MergeInput{Target: h.target.ID, OperationID: "op-m2", Number: 7, ExpectedHead: shaCommit, Method: "merge"}); codeOf(err) != "unresolved_intent" {
		t.Fatalf("new merge op = %v", err)
	}
	if h.host.merges != 1 {
		t.Fatalf("merges = %d", h.host.merges)
	}

	h2 := newHarness(t)
	h2.host.refs["olivares/pr"] = shaCommit
	h2.host.createRes = &amb
	pr := PullRequestInput{Target: h2.target.ID, OperationID: "op-p", HeadRef: "olivares/pr", Base: "main", Commit: shaCommit, Title: "t"}
	r2, _ := h2.m.OpenPullRequest(context.Background(), h2.user(), pr)
	h2.host.lookupErr = gp.ErrLookupIncomplete
	if rc, _ := h2.m.Reconcile(context.Background(), h2.user(), r2.Intent.ID); rc.Intent.State != StateUncertain {
		t.Fatalf("incomplete lookup: %s", rc.Intent.State)
	}
	h2.host.lookupErr = nil
	if rc, _ := h2.m.Reconcile(context.Background(), h2.user(), r2.Intent.ID); rc.Intent.State != StateUncertain {
		t.Fatalf("empty lookup: %s", rc.Intent.State)
	}
	_, _ = h2.m.OpenPullRequest(context.Background(), h2.user(), pr)
	pr.OperationID = "op-p2"
	if _, err := h2.m.OpenPullRequest(context.Background(), h2.user(), pr); codeOf(err) != "unresolved_intent" {
		t.Fatalf("new PR op = %v", err)
	}
	if h2.host.creates != 1 {
		t.Fatalf("creates = %d", h2.host.creates)
	}
}

// The W1 re-check of the conflict scope (not only A2's view) refuses a new
// operation when a competing intent enters the scope between A2 and W1.
func TestAdoptedW1RecheckRefusesScopeRace(t *testing.T) {
	h := newHarness(t)
	h.m.beforeClaim = func() {
		h.m.beforeClaim = nil
		_ = crashClaim(t, h, "x", time.Hour) // a competing dispatching intent, scope push:refs/heads/olivares/x
	}
	if _, err := h.push(h.user(), "op-race", "refs/heads/olivares/x", ""); codeOf(err) != "unresolved_intent" {
		t.Fatalf("scope race at W1 = %v", err)
	}
	if h.git.count() != 0 {
		t.Fatal("dispatched into a held scope")
	}
	h.balanced()
}

func TestAdoptedCustodyGetsCallerTenantAndTargetWorkspace(t *testing.T) {
	h := newHarness(t)
	rc := &recordingCustody{fakeCustody: h.custody}
	h.m.opts.Custody = rc
	if _, err := h.push(h.user(), "op-c", "refs/heads/olivares/c", ""); err != nil {
		t.Fatal(err)
	}
	if len(rc.calls) == 0 {
		t.Fatal("custody never consulted")
	}
	for _, c := range rc.calls {
		if c[0] != h.tenant.String() || c[1] != h.ws.String() {
			t.Fatalf("custody consulted with tenant=%s workspace=%s, want %s/%s", c[0], c[1], h.tenant, h.ws)
		}
	}
}

// Intent point routes conceal a target outside the caller's workspace.
func TestAdoptedIntentRoutesConcealOtherWorkspace(t *testing.T) {
	h := newHarness(t)
	h.m.opts.DispatchTimeout = 50 * time.Millisecond
	h.git.hold = make(chan struct{})
	defer close(h.git.hold)
	r, _ := h.push(h.user(), "op-cw", "refs/heads/olivares/cw", "")
	h.authz.deny[h.ws] = auth.ErrRouteDenied
	get := call(handlerFor(t, h.m, http.MethodGet, "/intents/{id}"), h.user(), http.MethodGet, r.Intent.ID.String(), nil)
	rec := call(handlerFor(t, h.m, http.MethodPost, "/intents/{id}/reconcile"), h.user(), http.MethodPost, r.Intent.ID.String(), nil)
	missing := call(handlerFor(t, h.m, http.MethodGet, "/intents/{id}"), h.user(), http.MethodGet, model.NewID().String(), nil)
	t.Logf("other-workspace GET %d %s; reconcile %d %s; absent GET %d %s", get.Code, get.Body.String(), rec.Code, rec.Body.String(), missing.Code, missing.Body.String())
	if get.Code != http.StatusNotFound || rec.Code != http.StatusNotFound || get.Body.String() != missing.Body.String() {
		t.Fatal("an intent in another workspace is distinguishable from an absent one")
	}
}
