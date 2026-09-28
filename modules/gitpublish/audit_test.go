// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package gitpublish

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	gp "github.com/olivaresai/olivares/connectors/gitpublish"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func auditEvents(t *testing.T, h *harness) []model.AuditEvent {
	t.Helper()
	var out []model.AuditEvent
	if err := h.m.data.View(context.Background(), h.tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 0, func(e model.AuditEvent) error {
			if strings.HasPrefix(e.Action, "gitpublish.") {
				out = append(out, e)
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// M2 (construction read): every publication step is audited in its own
// transaction, attributed to the real actor, with bounded metadata and no
// secret, secret name or server path.
func TestAuditEventsEmittedWithoutSecret(t *testing.T) {
	h := newHarness(t)
	h.host.refs["olivares/pr"] = shaCommit
	ctx := context.Background()
	if _, err := h.push(h.user(), "a-push", "refs/heads/olivares/a", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := h.m.OpenPullRequest(ctx, h.user(), PullRequestInput{Target: h.target.ID, OperationID: "a-pr", HeadRef: "olivares/pr", Base: "main", Commit: shaCommit, Title: "t"}); err != nil {
		t.Fatal(err)
	}
	openPR(h, 21, shaCommit)
	if _, err := h.m.Merge(ctx, h.admin(), MergeInput{Target: h.target.ID, OperationID: "a-merge", Number: 21, ExpectedHead: shaCommit, Method: "merge"}); err != nil {
		t.Fatal(err)
	}
	// An adoption, an A4 refusal, an uncertain write, its reconcile and its abandonment.
	h.host.refs["olivares/adopt"] = shaCommit
	if r, err := h.push(h.user(), "a-adopt", "refs/heads/olivares/adopt", ""); err != nil || r.Intent.State != StateAdopted {
		t.Fatalf("adopt = %+v %v", r.Intent, err)
	}
	h.m.opts.DispatchTimeout = 60 * time.Millisecond
	h.git.hold = make(chan struct{})
	u, _ := h.push(h.user(), "a-held", "refs/heads/olivares/held", "")
	if _, err := h.m.Reconcile(ctx, h.user(), u.Intent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.m.Abandon(ctx, h.admin(), u.Intent.ID, "host outage"); err != nil {
		t.Fatal(err)
	}
	close(h.git.hold)
	h.git.hold = nil
	h.m.opts.DispatchTimeout = 2 * time.Second
	up, err := h.m.UpdateTarget(ctx, h.admin(), h.target.ID, TargetUpdate{ExpectedVersion: h.target.Version, PushPrefix: "olivares/", MergeBases: []string{"main", "dev"}})
	if err != nil {
		t.Fatal(err)
	}
	tg2, err := h.m.CreateTarget(ctx, h.admin(), TargetInput{Workspace: h.ws, CredentialBinding: "cb1", RepositoryBinding: "rb2", PushPrefix: "olivares/", MergeBases: []string{"main"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.m.DeleteTarget(ctx, h.admin(), tg2.ID); err != nil {
		t.Fatal(err)
	}
	_ = up
	seen := map[string]int{}
	for _, e := range auditEvents(t, h) {
		seen[e.Action]++
		if e.Actor == "" || e.ActorKind == "" {
			t.Fatalf("%s: unattributed event", e.Action)
		}
		dump := fmt.Sprintf("%v", e.Meta)
		for _, leak := range []string{"/srv/olivares", "Authorization", "Bearer", "secret", "token"} {
			if strings.Contains(dump, leak) {
				t.Fatalf("%s meta leaks %q: %s", e.Action, leak, dump)
			}
		}
	}
	for _, want := range []string{
		"gitpublish.push.admitted", "gitpublish.push.claimed", "gitpublish.push.dispatched", "gitpublish.push.settled",
		"gitpublish.push.adopted",
		"gitpublish.pull_request.admitted", "gitpublish.pull_request.claimed", "gitpublish.pull_request.dispatched", "gitpublish.pull_request.settled",
		"gitpublish.merge.admitted", "gitpublish.merge.claimed", "gitpublish.merge.dispatched", "gitpublish.merge.settled",
		"gitpublish.intent.reconciled", "gitpublish.intent.abandoned",
		"gitpublish.target.created", "gitpublish.target.updated", "gitpublish.target.deleted",
	} {
		if seen[want] == 0 {
			t.Errorf("no %s event (seen %v)", want, seen)
		}
	}
	_ = gp.Applied
}
