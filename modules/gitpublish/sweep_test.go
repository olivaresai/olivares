// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package gitpublish

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type leader bool

func (l leader) Active() bool { return bool(l) }

// crashClaim leaves an intent exactly as a process that died after W1 would:
// dispatching, with its deadline at the given offset from now.
func crashClaim(t *testing.T, h *harness, op string, deadlineIn time.Duration) Intent {
	t.Helper()
	var out Intent
	err := h.m.data.Mutate(context.Background(), h.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(kindIntent)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		in := Intent{Target: h.target.ID, Workspace: h.ws, TargetVersion: h.target.Version, CredentialBinding: "cb1", CredentialVersion: 1,
			RepositoryBinding: "rb1", RepositoryVersion: 1, RepoID: "R_1", Effect: effectPush, OperationID: op, ScopeKey: "push:refs/heads/olivares/" + op,
			SubjectActor: "user:" + userA.String(), Attempt: 1, State: StateDispatching, Receipt: ReceiptNone,
			Requested: Requested{Ref: "refs/heads/olivares/" + op, Commit: shaCommit, Tree: shaTree}, ClaimedAt: now.Add(-time.Hour), DispatchDeadline: now.Add(deadlineIn)}
		rec, err := repo.Create(context.Background(), in.record())
		out = intentFrom(rec)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func stateOf(t *testing.T, h *harness, id model.ID) Intent {
	t.Helper()
	var in Intent
	if err := h.m.data.View(context.Background(), h.tenant, func(sc store.Scope) error {
		var err error
		in, err = loadIntent(context.Background(), sc, id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return in
}

func TestSweepPumpActiveWriterOnlyDBTime(t *testing.T) {
	h := newHarness(t)
	h.m.opts.Skew = time.Minute
	h.m.opts.SweepObserveInterval = time.Nanosecond     // re-observe on every pass here
	stale := crashClaim(t, h, "stale", -2*time.Minute)  // past deadline + skew
	fresh := crashClaim(t, h, "fresh", -30*time.Second) // past deadline, inside skew
	tenants := func(context.Context) ([]model.TenantID, error) { return []model.TenantID{h.tenant}, nil }

	// A standby never writes.
	if err := h.m.SweepPump(leader(false), tenants)(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := stateOf(t, h, stale.ID).State; s != StateDispatching {
		t.Fatalf("standby changed an intent to %s", s)
	}
	// The active writer marks only the intent past deadline + skew uncertain,
	// observes it, and never dispatches.
	if err := h.m.SweepPump(leader(true), tenants)(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := stateOf(t, h, stale.ID).State; s != StateUncertain {
		t.Fatalf("stale crash-claimed intent = %s, want uncertain", s)
	}
	if s := stateOf(t, h, fresh.ID).State; s != StateDispatching {
		t.Fatalf("an intent inside the skew was settled: %s", s)
	}
	if h.git.count() != 0 {
		t.Fatal("the sweep dispatched a write")
	}
	// The effect later appears on the host: the sweep adopts it, still
	// without dispatch and without claiming acknowledgment.
	h.host.mu.Lock()
	h.host.refs["olivares/stale"] = shaCommit
	h.host.mu.Unlock()
	if err := h.m.SweepPump(leader(true), tenants)(context.Background()); err != nil {
		t.Fatal(err)
	}
	in := stateOf(t, h, stale.ID)
	if in.State != StateAdopted || in.Receipt != ReceiptExistingEffect || in.Acknowledged.Acknowledged {
		t.Fatalf("after the late effect = %+v", in)
	}
	if h.git.count() != 0 {
		t.Fatal("the sweep dispatched a write")
	}
	h.balanced()
}
