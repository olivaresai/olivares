// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package gitpublish

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	gp "github.com/olivaresai/olivares/connectors/gitpublish"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const approvalOne = "0199b0a1-0000-7000-8000-0000000000a1"

func (h *harness) proposal() Proposal {
	return Proposal{SessionRun: runOne, Workspace: h.ws, Approval: approvalOne}
}

// A session's approved request publishes as its launcher, and the intent, its
// receipt and every audit step name the session run and the approval.
func TestApprovedProposalRecordsSessionRunAndApproval(t *testing.T) {
	h, _ := sessionHarness(t)
	r, err := h.m.Push(context.Background(), h.user(), PushInput{Target: h.target.ID, OperationID: "proposal:" + approvalOne, Ref: "refs/heads/olivares/run-1",
		Commit: shaSession, Tree: shaTree, SessionRun: runOne, Proposal: h.proposal()})
	if err != nil || r.Intent.State != StateApplied {
		t.Fatalf("proposed push = %+v %v", r.Intent, err)
	}
	if r.Intent.Proposal != h.proposal() || r.Intent.SubjectUser != userA.String() {
		t.Fatalf("intent proposal = %+v subject %q", r.Intent.Proposal, r.Intent.SubjectUser)
	}
	dto := toIntentDTO(r.Intent, r.Answer)
	if dto.Proposal == nil || dto.Proposal.SessionRun != runOne || dto.Proposal.ApprovalID != approvalOne {
		t.Fatalf("receipt proposal = %+v", dto.Proposal)
	}
	stored, err := h.m.Intents(context.Background(), h.user(), h.target.ID)
	if err != nil || len(stored) != 1 || stored[0].Proposal != h.proposal() {
		t.Fatalf("stored = %+v %v", stored, err)
	}
	steps := 0
	if err := h.m.data.View(context.Background(), h.tenant, func(sc store.Scope) error {
		walker, ok := sc.Audit().(store.CanonicalWalker)
		if !ok {
			t.Fatal("canonical audit read unavailable")
		}
		return walker.WalkCanonical(context.Background(), 0, func(e model.AuditEvent, meta string, _ []byte) error {
			if !strings.HasPrefix(e.Action, "gitpublish.push.") {
				return nil
			}
			if err := json.Unmarshal([]byte(meta), &e.Meta); err != nil {
				return err
			}
			if e.Meta["session_run"] != runOne || e.Meta["approval_id"] != approvalOne {
				t.Fatalf("%s meta = %v", e.Action, e.Meta)
			}
			steps++
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if steps != 4 {
		t.Fatalf("push audit steps = %d, want admitted, claimed, dispatched and settled", steps)
	}
	// A direct push records no proposal.
	d, err := h.push(h.user(), "direct", "refs/heads/olivares/direct", "")
	if err != nil || d.Intent.Proposal != (Proposal{}) || toIntentDTO(d.Intent, d.Answer).Proposal != nil {
		t.Fatalf("direct push = %+v %v", d.Intent, err)
	}
}

// A proposal names a canonical run, a workspace and an approval, or nothing.
func TestInvalidProposalIsRefused(t *testing.T) {
	h, s := sessionHarness(t)
	for name, p := range map[string]Proposal{
		"no workspace":      {SessionRun: runOne, Approval: approvalOne},
		"run not an id":     {SessionRun: "run-1", Workspace: h.ws, Approval: approvalOne},
		"run not canonical": {SessionRun: strings.ToUpper(runOne), Workspace: h.ws, Approval: approvalOne},
		"no approval":       {SessionRun: runOne, Workspace: h.ws},
		"approval spaces":   {SessionRun: runOne, Workspace: h.ws, Approval: "a b"},
	} {
		_, err := h.m.Push(context.Background(), h.user(), PushInput{Target: h.target.ID, OperationID: "op-" + strings.ReplaceAll(name, " ", "-"), Ref: "refs/heads/olivares/run-1",
			Commit: shaSession, Tree: shaTree, SessionRun: runOne, Proposal: p})
		if codeOf(err) != "invalid_request" {
			t.Errorf("%s: err = %v, want invalid_request", name, err)
		}
	}
	if s.asked != 0 || h.host.mints != 0 {
		t.Fatalf("an invalid proposal reached the folder (%d) or the host (%d)", s.asked, h.host.mints)
	}
}

// The approved request publishes only to a target in the session's own
// workspace, and refuses before any host or folder is touched.
func TestProposalOutsideTheSessionWorkspaceIsRefused(t *testing.T) {
	h, s := sessionHarness(t)
	p := h.proposal()
	p.Workspace = model.NewID()
	_, err := h.m.Push(context.Background(), h.user(), PushInput{Target: h.target.ID, OperationID: "op-ws", Ref: "refs/heads/olivares/run-1",
		Commit: shaSession, Tree: shaTree, SessionRun: runOne, Proposal: p})
	if codeOf(err) != "not_found" {
		t.Fatalf("err = %v, want not_found", err)
	}
	_, err = h.m.OpenPullRequest(context.Background(), h.user(), PullRequestInput{Target: h.target.ID, OperationID: "op-ws-pr", HeadRef: "olivares/pr",
		Base: "main", Commit: shaCommit, Title: "t", Proposal: p})
	if codeOf(err) != "not_found" {
		t.Fatalf("pr err = %v, want not_found", err)
	}
	if s.asked != 0 || h.host.mints != 0 {
		t.Fatalf("refused proposal reached the folder (%d) or the host (%d)", s.asked, h.host.mints)
	}
}

// The person whose session published a pull request or its head commit
// cannot merge it; another administrator can, and so can the launcher for
// work no session of theirs proposed.
func TestSessionLauncherCannotMergeWhatTheirSessionPublished(t *testing.T) {
	// The launcher merges with another credential of the same person: a fresh
	// login at AAL3. Separation of duty is decided on the person.
	launcher := func(h *harness) Caller { return h.caller(auth.KindUser, userA, model.NewID(), 3) }
	t.Run("pull request", func(t *testing.T) {
		h := newHarness(t)
		h.host.refs["olivares/pr"] = shaCommit
		r, err := h.m.OpenPullRequest(context.Background(), h.user(), PullRequestInput{Target: h.target.ID, OperationID: "op-pr", HeadRef: "olivares/pr",
			Base: "main", Commit: shaCommit, Title: "t", Proposal: h.proposal()})
		if err != nil || r.Intent.Observed.Number == 0 {
			t.Fatalf("proposed pr = %+v %v", r.Intent, err)
		}
		n := r.Intent.Observed.Number
		if _, err := h.m.Merge(context.Background(), launcher(h), MergeInput{Target: h.target.ID, OperationID: "op-m", Number: n, ExpectedHead: shaCommit, Method: "merge"}); codeOf(err) != "separation_of_duty" {
			t.Fatalf("launcher merge err = %v, want separation_of_duty", err)
		}
		if h.host.merges != 0 {
			t.Fatalf("launcher merge dispatched")
		}
		m, err := h.m.Merge(context.Background(), h.admin(), MergeInput{Target: h.target.ID, OperationID: "op-m2", Number: n, ExpectedHead: shaCommit, Method: "merge"})
		if err != nil || m.Intent.State != StateApplied {
			t.Fatalf("other admin merge = %+v %v", m.Intent, err)
		}
	})
	t.Run("moved head", func(t *testing.T) {
		h := newHarness(t)
		h.host.refs["olivares/pr"] = shaCommit
		r, err := h.m.OpenPullRequest(context.Background(), h.user(), PullRequestInput{Target: h.target.ID, OperationID: "op-pr", HeadRef: "olivares/pr",
			Base: "main", Commit: shaCommit, Title: "t", Proposal: h.proposal()})
		if err != nil || r.Intent.Observed.Number == 0 {
			t.Fatalf("proposed pr = %+v %v", r.Intent, err)
		}
		h.host.changes[0].HeadSHA = shaOther
		if _, err := h.m.Merge(context.Background(), launcher(h), MergeInput{Target: h.target.ID, OperationID: "op-m", Number: r.Intent.Observed.Number, ExpectedHead: shaOther, Method: "merge"}); codeOf(err) != "separation_of_duty" {
			t.Fatalf("launcher merge of a moved head err = %v, want separation_of_duty", err)
		}
	})
	t.Run("unobserved number", func(t *testing.T) {
		h := newHarness(t)
		h.host.refs["olivares/pr"] = shaCommit
		h.host.createRes = &gp.Result{Class: gp.Ambiguous, Reason: "transport"}
		r, err := h.m.OpenPullRequest(context.Background(), h.user(), PullRequestInput{Target: h.target.ID, OperationID: "op-pr", HeadRef: "olivares/pr",
			Base: "main", Commit: shaCommit, Title: "t", Proposal: h.proposal()})
		if err != nil || r.Intent.Observed.Number != 0 {
			t.Fatalf("uncertain proposed pr = %+v %v", r.Intent, err)
		}
		h.host.createRes = nil
		openPR(h, 51, shaOther)
		if _, err := h.m.Merge(context.Background(), launcher(h), MergeInput{Target: h.target.ID, OperationID: "op-m", Number: 51, ExpectedHead: shaOther, Method: "merge"}); codeOf(err) != "separation_of_duty" {
			t.Fatalf("launcher merge of its session's branch err = %v, want separation_of_duty", err)
		}
	})
	t.Run("another target on the repository", func(t *testing.T) {
		h := newHarness(t)
		h.host.refs["olivares/pr"] = shaCommit
		r, err := h.m.OpenPullRequest(context.Background(), h.user(), PullRequestInput{Target: h.target.ID, OperationID: "op-pr", HeadRef: "olivares/pr",
			Base: "main", Commit: shaCommit, Title: "t", Proposal: h.proposal()})
		if err != nil {
			t.Fatal(err)
		}
		second, err := h.m.CreateTarget(context.Background(), h.admin(), TargetInput{Workspace: h.ws, CredentialBinding: "cb1", RepositoryBinding: "rb2", PushPrefix: "olivares/", MergeBases: []string{"main"}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.m.Merge(context.Background(), launcher(h), MergeInput{Target: second.ID, OperationID: "op-m", Number: r.Intent.Observed.Number, ExpectedHead: shaCommit, Method: "merge"}); codeOf(err) != "separation_of_duty" {
			t.Fatalf("launcher merge through a second target err = %v, want separation_of_duty", err)
		}
	})
	t.Run("pushed head", func(t *testing.T) {
		h, _ := sessionHarness(t)
		if _, err := h.m.Push(context.Background(), h.user(), PushInput{Target: h.target.ID, OperationID: "op-p", Ref: "refs/heads/olivares/run-1",
			Commit: shaSession, Tree: shaTree, SessionRun: runOne, Proposal: h.proposal()}); err != nil {
			t.Fatal(err)
		}
		openPR(h, 31, shaSession)
		if _, err := h.m.Merge(context.Background(), launcher(h), MergeInput{Target: h.target.ID, OperationID: "op-m", Number: 31, ExpectedHead: shaSession, Method: "merge"}); codeOf(err) != "separation_of_duty" {
			t.Fatalf("launcher merge err = %v, want separation_of_duty", err)
		}
	})
	t.Run("no proposal", func(t *testing.T) {
		h := newHarness(t)
		if _, err := h.push(h.user(), "op-p", "refs/heads/olivares/pr", ""); err != nil {
			t.Fatal(err)
		}
		openPR(h, 41, shaCommit)
		m, err := h.m.Merge(context.Background(), launcher(h), MergeInput{Target: h.target.ID, OperationID: "op-m", Number: 41, ExpectedHead: shaCommit, Method: "merge"})
		if err != nil || m.Intent.State != StateApplied {
			t.Fatalf("merge of unproposed work = %+v %v", m.Intent, err)
		}
	})
}
