// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package gitpublish

import (
	"context"
	"errors"
	"reflect"
	"testing"

	gp "github.com/olivaresai/olivares/connectors/gitpublish"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

func (h *harness) pushChanges(c Caller, old string) ([]string, error) {
	return h.m.PushChanges(context.Background(), c, h.ws, PushInput{Target: h.target.ID, Ref: "refs/heads/olivares/run-1", ExpectedOld: old, Commit: shaCommit, Tree: shaTree})
}

// The change of a push is read against the base the publish compares with: the
// lease it names, else the head of the target's merge base on the host. Nothing
// is pushed, claimed or left minted.
func TestPushChangesReadsTheCommitAgainstThePublishBase(t *testing.T) {
	h := newHarness(t)
	h.git.changed = []string{"docs/a.md", "go.mod"}
	got, err := h.pushChanges(h.user(), "")
	if err != nil || !reflect.DeepEqual(got, []string{"docs/a.md", "go.mod"}) {
		t.Fatalf("paths = %q %v", got, err)
	}
	if !reflect.DeepEqual(h.git.changedAsked, []string{shaBase + "..." + shaCommit}) {
		t.Fatalf("compared %v, want what the commit adds since the merge base's head", h.git.changedAsked)
	}
	if h.git.count() != 0 || h.host.mints != 1 {
		t.Fatalf("pushes %d mints %d, want no push and one observation capability", h.git.count(), h.host.mints)
	}
	h.balanced()

	// A lease names its own base, asks the host for none and compares the
	// trees: a branch moved off its tip loses what only the tip had.
	h.git.changedAsked = nil
	if _, err := h.pushChanges(h.user(), shaOther); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(h.git.changedAsked, []string{shaOther + ".." + shaCommit}) {
		t.Fatalf("with a lease compared %v", h.git.changedAsked)
	}
	h.balanced()
}

func TestPullRequestChangesReadsTheCommitAgainstTheBaseHead(t *testing.T) {
	h := newHarness(t)
	h.git.changed = []string{"src/a.go"}
	got, err := h.m.PullRequestChanges(context.Background(), h.user(), h.ws, PullRequestInput{
		Target: h.target.ID, HeadRef: "olivares/run-1", Base: "main", Commit: shaCommit, Title: "t"})
	if err != nil || !reflect.DeepEqual(got, []string{"src/a.go"}) {
		t.Fatalf("paths = %q %v", got, err)
	}
	if !reflect.DeepEqual(h.git.changedAsked, []string{shaBase + "..." + shaCommit}) {
		t.Fatalf("compared %v", h.git.changedAsked)
	}
	h.balanced()
}

// Every refusal is the publish's own, comes before any host read where the
// publish's would, and leaves no capability behind.
func TestPushChangesRefusals(t *testing.T) {
	t.Run("a session credential", func(t *testing.T) {
		h := newHarness(t)
		c := h.user()
		c.Principal.SessionIdentity = "session-1"
		if _, err := h.pushChanges(c, ""); codeOf(err) != "runtime_credential_refused" || h.host.mints != 0 {
			t.Fatalf("err = %v mints %d", err, h.host.mints)
		}
	})
	t.Run("another workspace", func(t *testing.T) {
		h := newHarness(t)
		_, err := h.m.PushChanges(context.Background(), h.user(), model.NewID(), PushInput{Target: h.target.ID, Ref: "refs/heads/olivares/r", Commit: shaCommit, Tree: shaTree})
		if codeOf(err) != "not_found" || h.host.mints != 0 {
			t.Fatalf("err = %v mints %d", err, h.host.mints)
		}
	})
	t.Run("a ref outside the push prefix", func(t *testing.T) {
		h := newHarness(t)
		_, err := h.m.PushChanges(context.Background(), h.user(), h.ws, PushInput{Target: h.target.ID, Ref: "refs/heads/elsewhere", Commit: shaCommit, Tree: shaTree})
		if codeOf(err) != "ref_not_allowed" || h.host.mints != 0 {
			t.Fatalf("err = %v mints %d", err, h.host.mints)
		}
	})
	t.Run("authority denies", func(t *testing.T) {
		h := newHarness(t)
		h.authz.deny[h.ws] = auth.ErrRouteDenied
		if _, err := h.pushChanges(h.user(), ""); err == nil || h.host.mints != 0 {
			t.Fatalf("err = %v mints %d, want a refusal before the host", err, h.host.mints)
		}
	})
	t.Run("the commit is not in the managed repository", func(t *testing.T) {
		h := newHarness(t)
		delete(h.git.treeFor, shaCommit)
		if _, err := h.pushChanges(h.user(), ""); codeOf(err) != "content_mismatch" || h.host.mints != 0 {
			t.Fatalf("err = %v mints %d", err, h.host.mints)
		}
	})
	t.Run("the commit has another tree than the proposal names", func(t *testing.T) {
		h := newHarness(t)
		_, err := h.m.PushChanges(context.Background(), h.user(), h.ws, PushInput{Target: h.target.ID, Ref: "refs/heads/olivares/r", Commit: shaCommit, Tree: shaOther})
		if codeOf(err) != "content_mismatch" || h.host.mints != 0 {
			t.Fatalf("err = %v mints %d, want content_mismatch before the host", err, h.host.mints)
		}
	})
	t.Run("git fails for another reason than the content", func(t *testing.T) {
		h := newHarness(t)
		h.git.changedErr = errors.New("git did not start")
		if _, err := h.pushChanges(h.user(), ""); codeOf(err) != "session_source_unavailable" {
			t.Fatalf("err = %v, want session_source_unavailable", err)
		}
		h.balanced()
	})
	t.Run("the base cannot be read", func(t *testing.T) {
		h := newHarness(t)
		h.host.refErr["main"] = errors.New("host down")
		if _, err := h.pushChanges(h.user(), ""); codeOf(err) != "lookup_incomplete" {
			t.Fatalf("err = %v, want lookup_incomplete", err)
		}
		h.balanced()
	})
	t.Run("the paths cannot be listed", func(t *testing.T) {
		h := newHarness(t)
		h.git.changedErr = gp.ErrContent
		if _, err := h.pushChanges(h.user(), ""); codeOf(err) != "content_mismatch" {
			t.Fatalf("err = %v, want content_mismatch", err)
		}
		h.balanced()
	})
	t.Run("the capability cannot be released", func(t *testing.T) {
		h := newHarness(t)
		h.host.release = errors.New("release refused")
		h.git.changed = []string{"a.md"}
		if got, err := h.pushChanges(h.user(), ""); codeOf(err) != "host_unavailable" || got != nil {
			t.Fatalf("paths %q err = %v, want host_unavailable", got, err)
		}
	})
	t.Run("no capability", func(t *testing.T) {
		h := newHarness(t)
		h.host.mintErr = errors.New("no token")
		if _, err := h.pushChanges(h.user(), ""); codeOf(err) != "host_unavailable" {
			t.Fatalf("err = %v, want host_unavailable", err)
		}
	})
}
