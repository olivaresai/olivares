// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package gitpublish

import (
	"context"
	"sync"
	"testing"
	"time"

	gp "github.com/olivaresai/olivares/connectors/gitpublish"
	"github.com/olivaresai/olivares/core/model"
)

// openCountingCustody counts every host the module opens. The module reaches
// the Git host only through a host it opened, so no open means no host call.
type openCountingCustody struct {
	*fakeCustody
	mu    sync.Mutex
	opens int
}

func (c *openCountingCustody) OpenHost(ctx context.Context, tn model.TenantID, cb CredentialBinding, rb RepositoryBinding) (gp.Host, error) {
	c.mu.Lock()
	c.opens++
	c.mu.Unlock()
	return c.fakeCustody.OpenHost(ctx, tn, cb, rb)
}

func (c *openCountingCustody) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.opens
}

// A is proven not_dispatched and B, a new operation for the same target and
// ref, went uncertain, so B holds the conflict scope. A retry of A is refused
// at the early decision (A2), before the narrowed capability is minted: it
// opens no host, mints no token and dispatches nothing. The claim transaction
// would refuse it too, but only after a mint, a release and the host
// preflight.
func TestEarlyRetryRefusalMintsNothing(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	const branch = "olivares/retry-early"
	const ref = "refs/heads/" + branch
	h.host.refs[branch] = shaBase
	custody := &openCountingCustody{fakeCustody: h.custody}
	h.m.opts.Custody = custody
	a := seedNotDispatched(t, h, "op-a", ref, shaCommit)
	h.m.opts.DispatchTimeout = 50 * time.Millisecond
	hold := make(chan struct{}) // B's write is held at the host and lands only after the test
	defer close(hold)
	h.git.mu.Lock()
	h.git.hold = hold
	h.git.mu.Unlock()
	b, _ := pushInScope(ctx, h, "op-b", ref, shaOther)
	if b.Intent.State != StateUncertain {
		t.Fatalf("setup: B = %q, want uncertain", b.Intent.State)
	}
	h.git.mu.Lock()
	h.git.hold = nil // a new write would not be held
	h.git.mu.Unlock()

	h.host.mu.Lock()
	mints := h.host.mints
	h.host.mu.Unlock()
	opens, dispatches := custody.count(), h.git.count()
	r, err := pushInScope(ctx, h, "op-a", ref, shaCommit)
	h.host.mu.Lock()
	mintsAfter := h.host.mints
	h.host.mu.Unlock()
	t.Logf("retry of A=%s while B=%s is uncertain: state=%q err=%v; mints %d→%d, host opens %d→%d, dispatches %d→%d",
		a.ID, b.Intent.ID, r.Intent.State, err, mints, mintsAfter, opens, custody.count(), dispatches, h.git.count())
	if code := codeOf(err); code != "unresolved_intent" {
		t.Errorf("retry of A while B holds the scope = %q (state %q), want unresolved_intent", code, r.Intent.State)
	}
	if mintsAfter != mints {
		t.Errorf("tokens minted by the refused retry = %d, want none", mintsAfter-mints)
	}
	if n := custody.count() - opens; n != 0 {
		t.Errorf("hosts opened by the refused retry = %d, want none", n)
	}
	if n := h.git.count() - dispatches; n != 0 {
		t.Errorf("dispatches by the refused retry = %d, want none", n)
	}
}
