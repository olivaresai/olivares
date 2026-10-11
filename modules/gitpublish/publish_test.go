// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package gitpublish

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	gp "github.com/olivaresai/olivares/connectors/gitpublish"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

func TestPushAppliedRecordsRequestedObservedAcknowledged(t *testing.T) {
	h := newHarness(t)
	r, err := h.push(h.user(), "op-1", "refs/heads/olivares/a", "")
	if err != nil {
		t.Fatal(err)
	}
	in := r.Intent
	if in.State != StateApplied || in.Receipt != ReceiptCaused || !in.Acknowledged.Acknowledged {
		t.Fatalf("intent = %+v", in)
	}
	if in.Requested.Commit != shaCommit || in.Observed.SHA != shaCommit || !in.Observed.Present {
		t.Fatalf("requested/observed = %+v / %+v", in.Requested, in.Observed)
	}
	if in.TargetVersion != h.target.Version || in.SubjectActor != "user:"+userA.String() {
		t.Fatalf("pins = %+v", in)
	}
	if h.git.lastReq.RepoPath != "/srv/olivares/gitpublish/r1.git" || h.git.lastReq.URL != "https://git.example/acme/widgets.git" {
		t.Fatalf("push request = %+v", h.git.lastReq)
	}
	h.balanced()
}

// The discriminating test of Root 4f50c87d §2: the write is held, GET reports
// the old state, a retry is requested, then the original write completes.
// No second dispatch may occur, and uncertainty is kept until the effect is
// observed.
func TestHeldWriteThenGetAbsentThenRetryNeverDispatchesTwice(t *testing.T) {
	h := newHarness(t)
	h.m.opts.DispatchTimeout = 150 * time.Millisecond
	h.git.hold = make(chan struct{})
	h.host.refs["olivares/held"] = shaBase
	r, err := h.push(h.user(), "op-held", "refs/heads/olivares/held", shaBase)
	if err != nil || r.Intent.State != StateUncertain {
		t.Fatalf("held write = %+v %v, want uncertain", r.Intent, err)
	}
	id := r.Intent.ID
	// GET reports the old value: an observation, never a completion barrier.
	rc, err := h.m.Reconcile(context.Background(), h.user(), id)
	if err != nil || rc.Intent.State != StateUncertain {
		t.Fatalf("reconcile on old ref = %+v %v", rc.Intent, err)
	}
	// A retry of the same operation and a new operation in the same scope.
	if r2, err := h.push(h.user(), "op-held", "refs/heads/olivares/held", shaBase); err != nil || r2.Intent.State != StateUncertain {
		t.Fatalf("retry = %+v %v", r2.Intent, err)
	}
	if _, err := h.push(h.user(), "op-new", "refs/heads/olivares/held", shaBase); codeOf(err) != "unresolved_intent" {
		t.Fatalf("new operation id in the same scope = %v, want unresolved_intent", err)
	}
	close(h.git.hold) // the original write completes late
	deadline := time.Now().Add(2 * time.Second)
	for h.host.ref("olivares/held") != shaCommit && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	rc, err = h.m.Reconcile(context.Background(), h.user(), id)
	if err != nil || rc.Intent.State != StateAdopted || rc.Intent.Receipt != ReceiptExistingEffect || rc.Intent.Acknowledged.Acknowledged {
		t.Fatalf("after late landing = %+v %v, want adopted existing_effect without acknowledgment", rc.Intent, err)
	}
	if n := h.git.count(); n != 1 {
		t.Fatalf("dispatches = %d, want exactly 1", n)
	}
	h.balanced()
}

func TestOldRefABAObservationKeepsUncertainty(t *testing.T) {
	h := newHarness(t)
	h.git.localErr = nil
	h.m.opts.DispatchTimeout = 100 * time.Millisecond
	h.git.hold = make(chan struct{})
	h.host.refs["olivares/aba"] = shaBase
	r, _ := h.push(h.user(), "op-aba", "refs/heads/olivares/aba", shaBase)
	// A -> B -> A: the ref leaves and returns to the expected old value.
	h.host.mu.Lock()
	h.host.refs["olivares/aba"] = shaOther
	h.host.mu.Unlock()
	if rc, _ := h.m.Reconcile(context.Background(), h.user(), r.Intent.ID); rc.Intent.State != StateUncertain {
		t.Fatalf("B observed: %s", rc.Intent.State)
	}
	h.host.mu.Lock()
	h.host.refs["olivares/aba"] = shaBase
	h.host.mu.Unlock()
	rc, _ := h.m.Reconcile(context.Background(), h.user(), r.Intent.ID)
	if rc.Intent.State != StateUncertain || rc.Intent.Observed.SHA != shaBase {
		t.Fatalf("A again: %+v", rc.Intent)
	}
	// Both retry forms after the ABA observation: still one dispatch.
	if r2, err := h.push(h.user(), "op-aba", "refs/heads/olivares/aba", shaBase); err != nil || r2.Intent.State != StateUncertain {
		t.Fatalf("same-op retry = %+v %v", r2.Intent, err)
	}
	if _, err := h.push(h.user(), "op-aba2", "refs/heads/olivares/aba", shaBase); codeOf(err) != "unresolved_intent" {
		t.Fatalf("new-op retry = %v", err)
	}
	if h.git.count() != 1 {
		t.Fatal("an old-ref observation re-armed the write")
	}
	close(h.git.hold)
}

func TestPermissionHidden404KeepsUncertainty(t *testing.T) {
	h := newHarness(t)
	h.m.opts.DispatchTimeout = 100 * time.Millisecond
	h.git.hold = make(chan struct{})
	r, _ := h.push(h.user(), "op-404", "refs/heads/olivares/h", "")
	h.host.mu.Lock()
	h.host.hidden["olivares/h"] = true
	h.host.mu.Unlock()
	rc, err := h.m.Reconcile(context.Background(), h.user(), r.Intent.ID)
	if err != nil || rc.Intent.State != StateUncertain || rc.Intent.Observed.Present {
		t.Fatalf("404 = %+v %v", rc.Intent, err)
	}
	obs, err := h.m.Observations(context.Background(), h.user(), r.Intent.ID)
	if err != nil {
		t.Fatalf("observations: %v", err)
	}
	if len(obs) == 0 || obs[len(obs)-1].Result != "unknown" {
		t.Fatalf("observations = %+v", obs)
	}
	close(h.git.hold)
}

func TestIncompleteLookupRefusesCreate(t *testing.T) {
	h := newHarness(t)
	h.host.refs["olivares/pr"] = shaCommit
	h.host.lookupErr = gp.ErrLookupIncomplete
	_, err := h.m.OpenPullRequest(context.Background(), h.user(), PullRequestInput{Target: h.target.ID, OperationID: "op-pr", HeadRef: "olivares/pr", Base: "main", Commit: shaCommit, Title: "t"})
	if codeOf(err) != "lookup_incomplete" || h.host.creates != 0 {
		t.Fatalf("err = %v creates = %d", err, h.host.creates)
	}
	h.balanced()
}

func TestClosedPRNeverAdopted(t *testing.T) {
	h := newHarness(t)
	h.host.refs["olivares/pr"] = shaCommit
	h.host.changes = []gp.Change{{Number: 3, Open: false, HeadRef: "olivares/pr", HeadSHA: shaCommit, BaseRef: "main"}}
	r, err := h.m.OpenPullRequest(context.Background(), h.user(), PullRequestInput{Target: h.target.ID, OperationID: "op-closed", HeadRef: "olivares/pr", Base: "main", Commit: shaCommit, Title: "t"})
	if err != nil || r.Intent.State != StateApplied || r.Intent.Receipt != ReceiptCaused || r.Intent.Observed.Number == 3 {
		t.Fatalf("closed PR must neither block nor be adopted: %+v %v", r.Intent, err)
	}
	// Reconciling an uncertain create never adopts a closed PR either.
	h2 := newHarness(t)
	h2.host.refs["olivares/pr"] = shaCommit
	amb := gp.Result{Class: gp.Ambiguous, Reason: "transport"}
	h2.host.createRes = &amb
	r2, _ := h2.m.OpenPullRequest(context.Background(), h2.user(), PullRequestInput{Target: h2.target.ID, OperationID: "op-u", HeadRef: "olivares/pr", Base: "main", Commit: shaCommit, Title: "t"})
	h2.host.changes = []gp.Change{{Number: 8, Open: false, HeadRef: "olivares/pr", HeadSHA: shaCommit, BaseRef: "main"}}
	rc, _ := h2.m.Reconcile(context.Background(), h2.user(), r2.Intent.ID)
	if rc.Intent.State != StateUncertain {
		t.Fatalf("a closed PR was adopted: %+v", rc.Intent)
	}
}

func TestOpenPRWithOtherHeadRefusedAndMatchingAdopted(t *testing.T) {
	h := newHarness(t)
	h.host.refs["olivares/pr"] = shaCommit
	h.host.changes = []gp.Change{{Number: 4, Open: true, HeadRef: "olivares/pr", HeadSHA: shaOther, BaseRef: "main"}}
	if _, err := h.m.OpenPullRequest(context.Background(), h.user(), PullRequestInput{Target: h.target.ID, OperationID: "op-x", HeadRef: "olivares/pr", Base: "main", Commit: shaCommit, Title: "t"}); codeOf(err) != "head_mismatch" {
		t.Fatalf("err = %v", err)
	}
	h.host.changes[0].HeadSHA = shaCommit
	r, err := h.m.OpenPullRequest(context.Background(), h.user(), PullRequestInput{Target: h.target.ID, OperationID: "op-y", HeadRef: "olivares/pr", Base: "main", Commit: shaCommit, Title: "t"})
	if err != nil || r.Intent.State != StateAdopted || r.Intent.Receipt != ReceiptExistingEffect || r.Intent.Observed.Number != 4 || h.host.creates != 0 {
		t.Fatalf("adopt = %+v %v creates=%d", r.Intent, err, h.host.creates)
	}
	h.balanced()
}

func TestCreateRaceOutcomes(t *testing.T) {
	// (1) The host refuses because a PR appeared concurrently for our head:
	// adopted as an existing effect, never as caused.
	h := newHarness(t)
	h.host.refs["olivares/pr"] = shaCommit
	rej := gp.Result{Class: gp.Rejected, Reason: "validation_failed", Host: gp.HostError{Status: 422}}
	h.host.createRes = &rej
	h.host.onCreate = func(fh *fakeHost) {
		fh.changes = append(fh.changes, gp.Change{Number: 9, Open: true, HeadRef: "olivares/pr", HeadSHA: shaCommit, BaseRef: "main"})
	}
	r, err := h.m.OpenPullRequest(context.Background(), h.user(), PullRequestInput{Target: h.target.ID, OperationID: "op-race", HeadRef: "olivares/pr", Base: "main", Commit: shaCommit, Title: "t"})
	if err != nil || r.Intent.State != StateAdopted || r.Intent.Receipt != ReceiptExistingEffect || r.Intent.Observed.Number != 9 {
		t.Fatalf("create race = %+v %v", r.Intent, err)
	}
	// (2) The head branch moves while the PR is created: applied, with the
	// content mismatch recorded, never called an immutable publication.
	h2 := newHarness(t)
	h2.host.refs["olivares/pr"] = shaCommit
	h2.host.onCreate = func(fh *fakeHost) { fh.refs["olivares/pr"] = shaOther }
	r2, err := h2.m.OpenPullRequest(context.Background(), h2.user(), PullRequestInput{Target: h2.target.ID, OperationID: "op-move", HeadRef: "olivares/pr", Base: "main", Commit: shaCommit, Title: "t"})
	if err != nil || r2.Intent.State != StateApplied || r2.Intent.ContentMatch || r2.Intent.Observed.HeadSHA != shaOther || r2.Intent.Requested.Commit != shaCommit {
		t.Fatalf("moved head = %+v %v", r2.Intent, err)
	}
}

func openPR(h *harness, n int, head string) {
	h.host.changes = append(h.host.changes, gp.Change{Number: n, Open: true, HeadRef: "olivares/pr", HeadSHA: head, BaseRef: "main"})
}

func TestMergeSourceRaceAndTargetRace(t *testing.T) {
	admin := func(h *harness) Caller { return h.admin() }
	// Source race: the head moves after preflight; the host's sha check refuses.
	h := newHarness(t)
	openPR(h, 5, shaCommit)
	h.host.onMerge = func(fh *fakeHost) { fh.changes[0].HeadSHA = shaOther }
	r, err := h.m.Merge(context.Background(), admin(h), MergeInput{Target: h.target.ID, OperationID: "op-m1", Number: 5, ExpectedHead: shaCommit, Method: "merge"})
	if err != nil || r.Intent.State != StateRejected || r.Intent.Reason != "head_mismatch" {
		t.Fatalf("source race = %+v %v", r.Intent, err)
	}
	// Target race: the base moves; the host merges the reviewed head into the
	// new base and the actual result is recorded.
	h2 := newHarness(t)
	openPR(h2, 6, shaCommit)
	h2.host.onMerge = func(fh *fakeHost) { fh.refs["main"] = shaOther }
	r2, err := h2.m.Merge(context.Background(), admin(h2), MergeInput{Target: h2.target.ID, OperationID: "op-m2", Number: 6, ExpectedHead: shaCommit, Method: "merge"})
	if err != nil || r2.Intent.State != StateApplied || r2.Intent.Observed.MergeCommitSHA != shaMerged {
		t.Fatalf("target race = %+v %v", r2.Intent, err)
	}
	// An exact result or base requirement is unsupported, not weakened.
	if _, err := h2.m.Merge(context.Background(), admin(h2), MergeInput{Target: h2.target.ID, OperationID: "op-m3", Number: 6, ExpectedHead: shaCommit, Method: "merge", ExpectedResultTree: shaTree}); codeOf(err) != "unsupported_requirement" {
		t.Fatalf("exact tree = %v", err)
	}
	// Merge requires AAL3: a token (AAL 0) or AAL1 session steps up.
	if _, err := h2.m.Merge(context.Background(), h2.user(), MergeInput{Target: h2.target.ID, OperationID: "op-m4", Number: 6, ExpectedHead: shaCommit, Method: "merge"}); codeOf(err) != "step_up_required" {
		t.Fatalf("merge at AAL1 = %v", err)
	}
}

func TestTargetEditOrCredentialRetargetBetweenAdmissionAndEffect(t *testing.T) {
	for name, edit := range map[string]func(h *harness){
		"target edited": func(h *harness) {
			if _, err := h.m.UpdateTarget(context.Background(), h.admin(), h.target.ID, TargetUpdate{ExpectedVersion: h.target.Version, PushPrefix: "olivares/", MergeBases: []string{"main", "dev"}}); err != nil {
				h.t.Fatal(err)
			}
		},
		"credential retargeted": func(h *harness) { h.custody.mu.Lock(); h.custody.cbVer = 2; h.custody.mu.Unlock() },
		"repository rebound":    func(h *harness) { h.custody.mu.Lock(); h.custody.rbVer = 2; h.custody.mu.Unlock() },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.authz.recheck = func() error { edit(h); return nil }
			r, err := h.push(h.user(), "op-edit", "refs/heads/olivares/e", "")
			if codeOf(err) != "target_changed" || h.git.count() != 0 {
				t.Fatalf("err = %v pushes = %d", err, h.git.count())
			}
			_ = r
			h.balanced()
		})
	}
}

func TestTokenReleasedOnEveryExitPath(t *testing.T) {
	cases := map[string]func(h *harness) error{
		"preflight refusal": func(h *harness) error {
			_, err := h.m.Push(context.Background(), h.user(), PushInput{Target: h.target.ID, OperationID: "p1", Ref: "refs/heads/olivares/x", Commit: shaCommit, Tree: shaOther})
			return err
		},
		"post-mint preflight refusal": func(h *harness) error {
			h.host.refs["olivares/moved"] = shaOther
			_, err := h.push(h.user(), "p0", "refs/heads/olivares/moved", shaBase)
			if h.host.mints != 1 {
				return errors.New("the preflight refusal did not follow a mint")
			}
			return err
		},
		"A4 refusal": func(h *harness) error {
			h.authz.recheck = func() error { return auth.ErrRouteDenied }
			_, err := h.push(h.user(), "p2", "refs/heads/olivares/x", "")
			return err
		},
		"lost W1 claim": func(h *harness) error {
			h.m.beforeClaim = func() {
				h.m.beforeClaim = nil
				_, _ = h.push(h.user(), "p3", "refs/heads/olivares/x", "")
			}
			_, err := h.push(h.user(), "p3", "refs/heads/olivares/x", "")
			h.m.beforeClaim = nil
			return err
		},
		"completion": func(h *harness) error {
			_, err := h.push(h.user(), "p4", "refs/heads/olivares/x", "")
			return err
		},
		"host rejection": func(h *harness) error {
			h.host.refs["olivares/rej"] = shaBase
			_, err := h.push(h.user(), "p5", "refs/heads/olivares/rej", shaOther)
			return err
		},
		"release failure is bounded": func(h *harness) error {
			h.host.release = &gp.HostError{Status: 502, Code: "server_error"}
			r, err := h.push(h.user(), "p6", "refs/heads/olivares/y", "")
			if err == nil && r.Intent.ReleaseFailure != "server_error" {
				return errors.New("release failure not recorded as a bounded field")
			}
			if again, _ := h.push(h.user(), "p6", "refs/heads/olivares/y", ""); h.git.count() != 1 || again.Intent.State != StateApplied {
				return errors.New("a failed release authorized a replay")
			}
			if err != nil {
				return err
			}
			return errCheckPassed
		},
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			err := fn(h)
			if name == "release failure is bounded" && !errors.Is(err, errCheckPassed) {
				t.Fatal(err)
			}
			h.balanced()
			if h.host.mints == 0 && name != "preflight refusal" {
				t.Fatalf("no token was minted on path %q", name)
			}
		})
	}
}

var errCheckPassed = errors.New("check passed")

func TestSecondUserlessTokenRefused(t *testing.T) {
	h := newHarness(t)
	base := h.authz.admits
	t1 := h.caller(auth.KindToken, "", model.NewID(), 0)
	t2 := h.caller(auth.KindToken, "", model.NewID(), 0)
	h.authz.recheck = func() error { return auth.ErrRouteDenied } // leave the intent not_dispatched
	if _, err := h.push(t1, "op-tok", "refs/heads/olivares/t", ""); err == nil {
		t.Fatal("setup: expected an A4 refusal")
	}
	h.authz.recheck = nil
	if _, err := h.push(t2, "op-tok", "refs/heads/olivares/t", ""); codeOf(err) != "subject_mismatch" {
		t.Fatalf("second userless token = %v, want subject_mismatch", err)
	}
	if r, err := h.push(t1, "op-tok", "refs/heads/olivares/t", ""); err != nil || r.Intent.State != StateApplied || r.Intent.Attempt != 2 {
		t.Fatalf("same token re-arm under fresh authority = %+v %v", r.Intent, err)
	}
	if h.authz.admits-base != 3 {
		t.Fatalf("admissions = %d, want 3 (each attempt is admitted afresh)", h.authz.admits-base)
	}
}

func TestWorkflowFilePushRefused(t *testing.T) {
	h := newHarness(t)
	h.git.workflows = true
	_, err := h.push(h.user(), "op-wf", "refs/heads/olivares/wf", "")
	if codeOf(err) != "workflow_change_refused" || h.git.count() != 0 {
		t.Fatalf("err = %v pushes = %d", err, h.git.count())
	}
	h.balanced()
}

func TestCrossTenantAndCrossWorkspaceTargetsConcealed(t *testing.T) {
	h := newHarness(t)
	other := h.user()
	other.Tenant = h.other
	if _, err := h.push(other, "op-ct", "refs/heads/olivares/c", ""); codeOf(err) != "not_found" {
		t.Fatalf("cross-tenant = %v", err)
	}
	h.authz.deny[h.ws] = auth.ErrRouteDenied
	if _, err := h.push(h.user(), "op-cw", "refs/heads/olivares/c", ""); codeOf(err) != "not_found" {
		t.Fatalf("cross-workspace = %v", err)
	}
	if h.host.mints != 0 {
		t.Fatal("a concealed target minted a token")
	}
}

func TestRuntimeSessionCredentialRefused(t *testing.T) {
	h := newHarness(t)
	c := h.caller(auth.KindToken, "", model.NewID(), 0)
	c.Principal.SessionIdentity = "sid-1"
	if _, err := h.push(c, "op-rt", "refs/heads/olivares/r", ""); codeOf(err) != "runtime_credential_refused" {
		t.Fatalf("err = %v", err)
	}
}

func TestAbandonedBlocksUntilAcknowledged(t *testing.T) {
	h := newHarness(t)
	h.m.opts.DispatchTimeout = 100 * time.Millisecond
	h.git.hold = make(chan struct{})
	defer close(h.git.hold)
	r, _ := h.push(h.user(), "op-ab", "refs/heads/olivares/b", "")
	if _, err := h.m.Abandon(context.Background(), h.admin(), r.Intent.ID, "host outage"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.push(h.user(), "op-ab2", "refs/heads/olivares/b", ""); codeOf(err) != "unresolved_intent" {
		t.Fatalf("abandoned must still block: %v", err)
	}
	// The acknowledgment carries the abandonment's authority (m7): an admin at AAL3.
	r2, err := h.m.Push(context.Background(), h.admin(), PushInput{Target: h.target.ID, OperationID: "op-ab3", Ref: "refs/heads/olivares/b", Commit: shaCommit, Tree: shaTree, AcknowledgeIntent: r.Intent.ID})
	if err != nil && !strings.Contains(codeOf(err), "stale_lease") {
		t.Fatalf("explicit acknowledgment: %v", err)
	}
	_ = r2
}

func TestRefAndBaseRules(t *testing.T) {
	h := newHarness(t)
	if _, err := h.push(h.user(), "op-r", "refs/heads/release", ""); codeOf(err) != "ref_not_allowed" {
		t.Fatalf("outside push_prefix = %v", err)
	}
	if _, err := h.push(h.user(), "op-t", "refs/tags/v1", ""); codeOf(err) != "ref_not_allowed" {
		t.Fatalf("tag = %v", err)
	}
	h.host.refs["olivares/pr"] = shaCommit
	if _, err := h.m.OpenPullRequest(context.Background(), h.user(), PullRequestInput{Target: h.target.ID, OperationID: "op-b", HeadRef: "olivares/pr", Base: "prod", Commit: shaCommit, Title: "t"}); codeOf(err) != "base_not_allowed" {
		t.Fatalf("base = %v", err)
	}
	if _, err := h.m.CreateTarget(context.Background(), h.admin(), TargetInput{Workspace: h.ws, CredentialBinding: "cb-unknown", RepositoryBinding: "rb1", PushPrefix: "olivares/"}); codeOf(err) != "binding_not_approved" {
		t.Fatalf("unapproved binding = %v", err)
	}
	h.custody.owners = []string{"someone-else"}
	if _, err := h.m.CreateTarget(context.Background(), h.admin(), TargetInput{Workspace: h.ws, CredentialBinding: "cb1", RepositoryBinding: "rb1", PushPrefix: "olivares/"}); codeOf(err) != "repository_outside_binding" {
		t.Fatalf("repository outside binding = %v", err)
	}
}

// B1 (construction read): a push lands only under the target's push prefix;
// the default branch and any host-reported default or protected branch
// refuse by name before dispatch, whatever the caller's permission.
func TestPushRefusesMergeBaseDefaultAndProtectedBranches(t *testing.T) {
	h := newHarness(t)
	for _, c := range []struct {
		name, ref, code string
		setup           func()
	}{
		{"merge base main", "refs/heads/main", "ref_not_allowed", func() {}},
		{"outside prefix", "refs/heads/feature/x", "ref_not_allowed", func() {}},
		{"host default under prefix", "refs/heads/olivares/trunk", "default_branch_refused", func() { h.host.defaultBranch = "olivares/trunk" }},
		{"host protected under prefix", "refs/heads/olivares/rel", "protected_branch_refused", func() { h.host.protected["olivares/rel"] = true }},
	} {
		c.setup()
		_, err := h.push(h.admin(), "op-"+strings.ReplaceAll(c.name, " ", "-"), c.ref, "")
		if codeOf(err) != c.code {
			t.Fatalf("%s: err = %v, want %s", c.name, err, c.code)
		}
	}
	if h.git.count() != 0 {
		t.Fatalf("dispatches = %d, want 0", h.git.count())
	}
	h.balanced()
	// A target whose push prefix covers a merge base is refused.
	if _, err := h.m.CreateTarget(context.Background(), h.admin(), TargetInput{Workspace: h.ws, CredentialBinding: "cb1", RepositoryBinding: "rb1", PushPrefix: "rel/", MergeBases: []string{"main", "rel/2026"}}); codeOf(err) != "push_prefix_overlaps_base" {
		t.Fatalf("overlapping prefix = %v", err)
	}
}

// m8/N8: on a GitLab target a rebase merge cannot be expressed and is refused
// by name, and the CI rule compares .gitlab-ci.yml.
func TestGitLabTargetRefusesRebaseAndChecksCIConfig(t *testing.T) {
	h := newHarness(t)
	h.custody.hostKind = "gitlab"
	openPR(h, 31, shaCommit)
	if _, err := h.m.Merge(context.Background(), h.admin(), MergeInput{Target: h.target.ID, OperationID: "m-rb", Number: 31, ExpectedHead: shaCommit, Method: "rebase"}); codeOf(err) != "unsupported_requirement" {
		t.Fatalf("GitLab rebase = %v, want unsupported_requirement", err)
	}
	if h.host.merges != 0 {
		t.Fatal("a refused method reached the host")
	}
	h.git.workflows = true
	if _, err := h.push(h.user(), "p-ci", "refs/heads/olivares/ci", ""); codeOf(err) != "workflow_change_refused" {
		t.Fatalf("CI change = %v", err)
	}
	if len(h.git.checkedPaths) == 0 || h.git.checkedPaths[0] != ".gitlab-ci.yml" {
		t.Fatalf("GitLab CI paths checked = %v", h.git.checkedPaths)
	}
}

// Custody implementations outside the first-party roster historically use the
// GitHub defaults for a custom host kind. The kind extraction keeps that path.
func TestPublicationKindCustomCustodyKeepsGitHubDefaults(t *testing.T) {
	h := newHarness(t)
	h.custody.hostKind = "custom"
	if _, err := h.push(h.user(), "custom-host", "refs/heads/olivares/custom", ""); err != nil {
		t.Fatalf("custom custody push = %v", err)
	}
	if h.host.mints == 0 || len(h.git.checkedPaths) != 1 || h.git.checkedPaths[0] != ".github/workflows" {
		t.Fatalf("custom host mints %d, CI paths %v", h.host.mints, h.git.checkedPaths)
	}
	h.balanced()
}
