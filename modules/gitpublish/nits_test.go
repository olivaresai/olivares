// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package gitpublish

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/store"
)

// N2: an applied merge records the host's result commit and its tree.
func TestMergeRecordsResultTree(t *testing.T) {
	h := newHarness(t)
	openPR(h, 41, shaCommit)
	r, err := h.m.Merge(context.Background(), h.admin(), MergeInput{Target: h.target.ID, OperationID: "m-tree", Number: 41, ExpectedHead: shaCommit, Method: "merge"})
	if err != nil || r.Intent.State != StateApplied || r.Intent.Observed.MergeCommitSHA != shaMerged || r.Intent.Observed.MergeTree != shaTree {
		t.Fatalf("merge = %+v %v", r.Intent.Observed, err)
	}
}

// N3: an acknowledged write whose evidence contradicts it stays applied (the
// host acknowledged it) but names the contradiction; missing evidence is
// named too.
func TestAppliedEvidenceContradictionIsNamed(t *testing.T) {
	h := newHarness(t)
	h.git.afterApply = func() { h.host.mu.Lock(); h.host.refs["olivares/c"] = shaOther; h.host.mu.Unlock() }
	r, err := h.push(h.user(), "p-contra", "refs/heads/olivares/c", "")
	if err != nil || r.Intent.State != StateApplied || r.Intent.Reason != "evidence_contradiction" || r.Intent.Observed.Present {
		t.Fatalf("contradiction = %+v %v", r.Intent, err)
	}
	h.git.afterApply = func() { h.host.mu.Lock(); h.host.hidden["olivares/d"] = true; h.host.mu.Unlock() }
	r, err = h.push(h.user(), "p-missing", "refs/heads/olivares/d", "")
	if err != nil || r.Intent.State != StateApplied || r.Intent.Reason != "evidence_unavailable" {
		t.Fatalf("missing evidence = %+v %v", r.Intent, err)
	}
}

// N4: a permanently uncertain intent is re-observed at a bounded rate.
func TestSweepBoundsReobservation(t *testing.T) {
	h := newHarness(t)
	h.m.opts.Skew = time.Minute
	crashClaim(t, h, "slow", -2*time.Minute)
	for i := 0; i < 3; i++ {
		if err := h.m.SweepDue(context.Background(), h.tenant); err != nil {
			t.Fatal(err)
		}
	}
	if h.host.mints != 1 {
		t.Fatalf("mints over three back-to-back sweeps = %d, want 1", h.host.mints)
	}
	h.balanced()
}

// N6: W1 takes the conflict scope's own row lock, so two claims in one
// scope serialize on that row (OCC on PostgreSQL), not only as a side effect
// of the authority lock.
func TestClaimTakesTheScopeRowLock(t *testing.T) {
	h := newHarness(t)
	if _, err := h.push(h.user(), "p-s1", "refs/heads/olivares/s", ""); err != nil {
		t.Fatal(err)
	}
	h.host.mu.Lock()
	h.host.refs["olivares/s"] = shaBase
	h.host.mu.Unlock()
	if _, err := h.push(h.user(), "p-s2", "refs/heads/olivares/s", shaBase); err != nil {
		t.Fatal(err)
	}
	var rows int
	var version int64
	if err := h.m.data.View(context.Background(), h.tenant, func(sc store.Scope) error {
		recs, err := listAll(context.Background(), sc, kindScope, eq("target_id", h.target.ID.String()), eq("scope_key", "push:refs/heads/olivares/s"))
		rows = len(recs)
		if rows == 1 {
			version = recs[0].Int("version")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || version < 2 {
		t.Fatalf("scope rows = %d version = %d, want one row taken by both claims", rows, version)
	}
}
