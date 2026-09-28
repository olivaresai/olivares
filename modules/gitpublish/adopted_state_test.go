// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package gitpublish

// State-machine tests adopted from the independent construction read, with
// their helpers renamed so the reader's overlay files still load beside them.

import (
	"context"
	"sync"
	"testing"
	"time"

	gp "github.com/olivaresai/olivares/connectors/gitpublish"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// heldGit holds a push IGNORING the dispatcher's context, as a host that is
// still processing the request after the client stopped waiting would.
type heldGit struct {
	*fakeGit
	mu      sync.Mutex
	n       int
	release chan struct{}
	started chan struct{}
	result  *gp.Result
}

func (b *heldGit) Push(_ context.Context, r gp.PushRequest) (gp.Result, error) {
	b.mu.Lock()
	b.n++
	b.mu.Unlock()
	b.started <- struct{}{}
	<-b.release
	res := b.fakeGit.apply(r)
	if b.result != nil {
		return *b.result, nil
	}
	return res, nil
}

func (b *heldGit) count() int { b.mu.Lock(); defer b.mu.Unlock(); return b.n }

func intentByOperation(t *testing.T, h *harness, effect, op string) Intent {
	t.Helper()
	var in Intent
	var found bool
	if err := h.m.data.View(context.Background(), h.tenant, func(sc store.Scope) error {
		var err error
		in, found, err = findByOperation(context.Background(), sc, h.target.ID, effect, op)
		return err
	}); err != nil || !found {
		t.Fatalf("intent %s: found=%v err=%v", op, found, err)
	}
	return in
}

// observations reads the stored observations of an intent directly.
func storedObservations(t *testing.T, h *harness, id model.ID) []Observation {
	t.Helper()
	var out []Observation
	if err := h.m.data.View(context.Background(), h.tenant, func(sc store.Scope) error {
		recs, err := listAll(context.Background(), sc, kindObservation, eq("intent_id", id.String()))
		for _, r := range recs {
			out = append(out, Observation{Attempt: r.Int("attempt"), Source: r.String("source"), Result: r.String("result"), HostObject: r.String("host_object")})
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// reboundCustody rebinds repository binding rb1 to ANOTHER host repository
// (version 2, new RepoID) after the intent was admitted.
type reboundCustody struct {
	*fakeCustody
	rebound bool
	other   *fakeHost
}

func (c *reboundCustody) RepositoryBinding(ctx context.Context, tn model.TenantID, ws model.ID, id string) (RepositoryBinding, error) {
	rb, err := c.fakeCustody.RepositoryBinding(ctx, tn, ws, id)
	if c.rebound {
		rb.Version, rb.RepoID, rb.Name = 2, "R_OTHER", "other"
	}
	return rb, err
}

func (c *reboundCustody) OpenHost(ctx context.Context, tn model.TenantID, cb CredentialBinding, rb RepositoryBinding) (gp.Host, error) {
	if rb.RepoID == "R_OTHER" {
		return c.other, nil
	}
	return c.fakeCustody.host, nil
}

// Root §4 pins the repository identity on the intent. An observation of a
// DIFFERENT repository (after a rebind) must not settle the intent.
func TestAdoptedObservationOfReboundRepositoryDoesNotAdopt(t *testing.T) {
	h := newHarness(t)
	rc := &reboundCustody{fakeCustody: h.custody, other: newFakeHost()}
	h.m.opts.Custody = rc
	h.m.opts.DispatchTimeout = 50 * time.Millisecond
	h.git.hold = make(chan struct{})
	defer close(h.git.hold)
	r, _ := h.push(h.user(), "op-rb", "refs/heads/olivares/rb", "")
	if r.Intent.State != StateUncertain || r.Intent.RepoID != "R_1" {
		t.Fatalf("setup: %+v", r.Intent)
	}
	rc.rebound = true
	rc.other.refs["olivares/rb"] = shaCommit // the other repository happens to carry the commit
	rec, _ := h.m.Reconcile(context.Background(), h.user(), r.Intent.ID)
	t.Logf("after rebind to R_OTHER: state=%s receipt=%s observed=%+v (intent pins RepoID %s)", rec.Intent.State, rec.Intent.Receipt, rec.Intent.Observed, rec.Intent.RepoID)
	if rec.Intent.State == StateAdopted {
		t.Fatal("an observation of another repository adopted the intent and released its conflict scope")
	}
}

// A W2 loser that got a documented non-application for THIS attempt while
// the sweep had marked it uncertain.
func TestAdoptedW2LoserRejectionResolvesUncertain(t *testing.T) {
	h := newHarness(t)
	h.host.refs["olivares/w"] = shaBase
	rej := gp.Result{Class: gp.Rejected, Reason: "stale_lease"}
	bg := &heldGit{fakeGit: h.git, release: make(chan struct{}), started: make(chan struct{}, 2), result: &rej}
	h.m.opts.Git = bg
	h.m.opts.DispatchTimeout, h.m.opts.Skew = 50*time.Millisecond, 10*time.Millisecond
	done := make(chan struct{})
	go func() { _, _ = h.push(h.user(), "op-w", "refs/heads/olivares/w", shaBase); close(done) }()
	<-bg.started
	h.host.mu.Lock()
	h.host.refs["olivares/w"] = shaOther // the lease will fail at the host
	h.host.mu.Unlock()
	time.Sleep(120 * time.Millisecond)
	_ = h.m.SweepDue(context.Background(), h.tenant)
	close(bg.release)
	<-done
	got := intentByOperation(t, h, effectPush, "op-w")
	t.Logf("state after W2 loser with a documented rejection: %s", got.State)
	if got.State == StateUncertain {
		t.Fatal("a documented non-application of this attempt left the intent uncertain (scope held until abandoned)")
	}
}

// Root §2 "do not overwrite an earlier attempt": attempt 1's refusal must
// survive the re-arm somewhere (intent or observation).
func TestAdoptedRearmKeepsEarlierAttemptRecord(t *testing.T) {
	h := newHarness(t)
	h.authz.recheck = func() error { return auth.ErrRouteDenied }
	r1, _ := h.push(h.user(), "op-re", "refs/heads/olivares/re", "")
	h.authz.recheck = nil
	r2, err := h.push(h.user(), "op-re", "refs/heads/olivares/re", "")
	if err != nil || r2.Intent.Attempt != 2 {
		t.Fatalf("re-arm = %+v %v", r2.Intent, err)
	}
	for _, o := range storedObservations(t, h, r1.Intent.ID) {
		if o.Attempt == 1 {
			return
		}
	}
	t.Fatalf("attempt 1 (not_dispatched, reason %q) left no record after the re-arm", r1.Intent.Reason)
}

// A release failure on the A4-refusal path is recorded as a bounded field.
func TestAdoptedReleaseFailureRecordedOnA4Refusal(t *testing.T) {
	h := newHarness(t)
	h.host.release = &gp.HostError{Status: 502, Code: "server_error"}
	h.authz.recheck = func() error { return auth.ErrRouteDenied }
	r, _ := h.push(h.user(), "op-rf", "refs/heads/olivares/rf", "")
	got := intentByOperation(t, h, effectPush, "op-rf")
	t.Logf("A4 refusal: state=%s release_failure=%q (answer %s)", got.State, got.ReleaseFailure, r.Answer)
	if got.ReleaseFailure == "" {
		t.Fatal("release failure on the A4-refusal path was not recorded")
	}
}

// Abandonment's reason is recorded (Root §2: it records responsibility).
func TestAdoptedAbandonReasonRecorded(t *testing.T) {
	h := newHarness(t)
	h.m.opts.DispatchTimeout = 50 * time.Millisecond
	h.git.hold = make(chan struct{})
	defer close(h.git.hold)
	r, _ := h.push(h.user(), "op-ab", "refs/heads/olivares/ab", "")
	a, err := h.m.Abandon(context.Background(), h.admin(), r.Intent.ID, "host outage INC-42")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("abandoned: authorized_by=%q reason=%q", a.Intent.AuthorizedBy, a.Intent.Reason)
	if a.Intent.Reason != "host outage INC-42" {
		for _, o := range storedObservations(t, h, r.Intent.ID) {
			if o.HostObject == "host outage INC-42" {
				return
			}
		}
		t.Fatal("the abandonment reason sent by the administrator is discarded")
	}
}

// m7: acknowledging an abandoned intent carries at least the abandonment's
// authority (target:admin at AAL3) and is audited; an AAL1 caller with push
// access cannot unlock the scope while the first write may still land.
func TestAcknowledgeIntentRequiresAdminAtAAL3(t *testing.T) {
	h := newHarness(t)
	h.m.opts.DispatchTimeout = 50 * time.Millisecond
	h.git.hold = make(chan struct{})
	defer close(h.git.hold)
	r, _ := h.push(h.user(), "op-o", "refs/heads/olivares/o", "")
	if _, err := h.m.Abandon(context.Background(), h.admin(), r.Intent.ID, "outage"); err != nil {
		t.Fatal(err)
	}
	editor := h.caller(auth.KindUser, model.NewID(), model.NewID(), 1)
	if _, err := h.m.Push(context.Background(), editor, PushInput{Target: h.target.ID, OperationID: "op-o2", Ref: "refs/heads/olivares/o", Commit: shaCommit, Tree: shaTree, AcknowledgeIntent: r.Intent.ID}); codeOf(err) != "step_up_required" {
		t.Fatalf("AAL1 acknowledgment = %v, want step_up_required", err)
	}
	if h.git.count() != 1 {
		t.Fatalf("dispatches in the scope = %d, want 1", h.git.count())
	}
	if _, err := h.m.Push(context.Background(), h.admin(), PushInput{Target: h.target.ID, OperationID: "op-o3", Ref: "refs/heads/olivares/o", Commit: shaCommit, Tree: shaTree, AcknowledgeIntent: r.Intent.ID}); err != nil {
		t.Fatalf("admin acknowledgment = %v", err)
	}
	var acked bool
	for _, e := range auditEvents(t, h) {
		acked = acked || e.Action == "gitpublish.intent.acknowledged"
	}
	if !acked {
		t.Fatal("the acknowledgment was not audited")
	}
}

// m5: a release failure on a path that never created an intent (a post-mint
// preflight refusal) is still kept as a bounded field.
func TestReleaseFailureWithoutIntentIsLogged(t *testing.T) {
	h := newHarness(t)
	var logged []string
	h.m.opts.OnReleaseFailure = func(target model.ID, code string) { logged = append(logged, target.String()+" "+code) }
	h.host.release = &gp.HostError{Status: 502, Code: "server_error"}
	h.host.refs["olivares/stale"] = shaOther
	if _, err := h.push(h.user(), "op-rl", "refs/heads/olivares/stale", shaBase); codeOf(err) != "stale_lease" {
		t.Fatalf("preflight = %v", err)
	}
	if h.host.mints != 1 || len(logged) != 1 || logged[0] != h.target.ID.String()+" server_error" {
		t.Fatalf("mints = %d logged = %v", h.host.mints, logged)
	}
	_ = store.ErrConflict
	_ = sync.Mutex{}
}
