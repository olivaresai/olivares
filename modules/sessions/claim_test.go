// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// SG-02 (core) DoD. The central assertion is the last one: a session that never
// claimed cannot act in silence — and it must fail for ITS OWN reason, with the
// signal emitted, not merely with a non-zero exit.

// claimed is a helper that resolves an identity and claims it in one step.
func claimed(t *testing.T, m *Module, tenant model.TenantID, ext, holder string) (string, Lease) {
	t.Helper()
	ctx := context.Background()
	sid, err := m.ResolveSession(ctx, tenant, SessionBinding{Provider: "claude", ExternalID: ext, At: baseTime})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	l, err := m.Claim(ctx, tenant, sid, holder, time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	return sid, l
}

// An expired lease loses authority even though its holder is still alive and
// still believes it is in charge. Liveness is asserted by renewal, never assumed
// from the absence of bad news.
func TestClaim_ExpiredLeaseLosesAuthority(t *testing.T) {
	t.Parallel()

	m, _, tenant, clk := newSess(t)
	ctx := context.Background()
	sid, lease := claimed(t, m, tenant, "sess-expire", "session-A")

	if err := m.Authority(ctx, tenant, sid, "session-A", lease.Fence); err != nil {
		t.Fatalf("authority while live: %v", err)
	}
	// Walk past the lease. The holder does nothing: it does not crash, it does
	// not notice, it simply stops renewing.
	clk.advance(2 * time.Minute)
	if err := m.Authority(ctx, tenant, sid, "session-A", lease.Fence); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("authority after lapse = %v, want ErrLeaseLost", err)
	}
	// A second session takes over, and the fence MOVES.
	taken, err := m.Claim(ctx, tenant, sid, "session-B", time.Minute)
	if err != nil {
		t.Fatalf("takeover: %v", err)
	}
	if taken.Fence <= lease.Fence {
		t.Errorf("fence %d did not advance past %d on takeover", taken.Fence, lease.Fence)
	}
	// The ORIGINAL holder's late write is rejected by its stale token.
	if err := m.Authority(ctx, tenant, sid, "session-A", lease.Fence); !errors.Is(err, ErrLeaseLost) {
		t.Errorf("stale-fence write = %v, want ErrLeaseLost", err)
	}
	// And the same holder presenting the NEW fence is still refused: authority
	// belongs to the holder, not to whoever can read a number.
	if err := m.Authority(ctx, tenant, sid, "session-A", taken.Fence); !errors.Is(err, ErrLeaseLost) {
		t.Errorf("old holder with new fence = %v, want ErrLeaseLost", err)
	}
}

// A live claim is exclusive: a second holder is refused while the lease holds.
func TestClaim_LiveClaimIsExclusive(t *testing.T) {
	t.Parallel()

	m, _, tenant, _ := newSess(t)
	ctx := context.Background()
	sid, _ := claimed(t, m, tenant, "sess-excl", "session-A")

	if _, err := m.Claim(ctx, tenant, sid, "session-B", time.Minute); !errors.Is(err, ErrClaimHeld) {
		t.Fatalf("second holder = %v, want ErrClaimHeld", err)
	}
}

// Concurrent claims on a fresh session: exactly one wins.
func TestClaim_ConcurrentClaim_OnlyOneWins(t *testing.T) {
	t.Parallel()

	m, _, tenant, _ := newSess(t)
	ctx := context.Background()
	sid, err := m.ResolveSession(ctx, tenant, SessionBinding{Provider: "claude", ExternalID: "sess-conc", At: baseTime})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	const n = 8
	wins := make([]bool, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			holder := "session-" + string(rune('A'+i))
			if _, err := m.Claim(ctx, tenant, sid, holder, time.Minute); err == nil {
				wins[i] = true
			}
		}(i)
	}
	wg.Wait()

	got := 0
	for _, w := range wins {
		if w {
			got++
		}
	}
	if got != 1 {
		t.Fatalf("%d holders claimed the same session, want exactly 1", got)
	}
	if rows := countRows(t, m, tenant, claimKind); rows != 1 {
		t.Errorf("claim rows = %d, want 1", rows)
	}
}

// A heartbeat renews the LEASE without regenerating the IDENTITY: the sid and
// the fence both survive, so a long session keeps one token end to end.
func TestClaim_HeartbeatRenewsWithoutNewIdentity(t *testing.T) {
	t.Parallel()

	m, _, tenant, clk := newSess(t)
	ctx := context.Background()
	sid, lease := claimed(t, m, tenant, "sess-hb", "session-A")

	for i := 0; i < 4; i++ {
		clk.advance(30 * time.Second)
		renewed, err := m.Heartbeat(ctx, tenant, sid, "session-A", lease.Fence, time.Minute)
		if err != nil {
			t.Fatalf("heartbeat %d: %v", i, err)
		}
		if renewed.Fence != lease.Fence {
			t.Fatalf("heartbeat %d moved the fence %d -> %d; a renewal is not a change of authority",
				i, lease.Fence, renewed.Fence)
		}
		if renewed.SID != sid {
			t.Fatalf("heartbeat %d changed the identity %q -> %q", i, sid, renewed.SID)
		}
	}
	// Two minutes of heartbeats later, the original fence still has authority.
	if err := m.Authority(ctx, tenant, sid, "session-A", lease.Fence); err != nil {
		t.Errorf("authority after renewals: %v", err)
	}
	// A heartbeat with a stale fence is refused, not silently accepted.
	if _, err := m.Heartbeat(ctx, tenant, sid, "session-A", lease.Fence-1, time.Minute); !errors.Is(err, ErrLeaseLost) {
		t.Errorf("stale-fence heartbeat = %v, want ErrLeaseLost", err)
	}
}

// Release gives authority back, and the next acquirer is fenced ahead.
func TestClaim_ReleaseThenReacquireBumpsFence(t *testing.T) {
	t.Parallel()

	m, _, tenant, _ := newSess(t)
	ctx := context.Background()
	sid, lease := claimed(t, m, tenant, "sess-rel", "session-A")

	if err := m.Release(ctx, tenant, sid, "session-A", lease.Fence); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := m.Authority(ctx, tenant, sid, "session-A", lease.Fence); !errors.Is(err, ErrLeaseLost) {
		t.Errorf("authority after release = %v, want ErrLeaseLost", err)
	}
	next, err := m.Claim(ctx, tenant, sid, "session-B", time.Minute)
	if err != nil {
		t.Fatalf("reacquire: %v", err)
	}
	if next.Fence <= lease.Fence {
		t.Errorf("fence %d did not advance past %d after release", next.Fence, lease.Fence)
	}
}

// THE CENTRAL ONE. A session that never claimed cannot act in silence. It must
// be denied, it must be denied for the RIGHT reason, and the attempt must leave
// a visible signal — a deny with no signal is indistinguishable from a session
// that never tried.
func TestClaim_UnclaimedSessionCannotActInSilence(t *testing.T) {
	t.Parallel()

	m, st, tenant, _ := newSess(t)
	ctx := context.Background()
	sid, err := m.ResolveSession(ctx, tenant, SessionBinding{Provider: "claude", ExternalID: "sess-orphan", At: baseTime})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	// 1. It is denied, and by ITS reason: no claim at all, not a stale fence and
	//    not a wrong holder.
	err = m.Authority(ctx, tenant, sid, "ghost", 1)
	if !errors.Is(err, ErrNoClaim) {
		t.Fatalf("unclaimed authority = %v, want ErrNoClaim", err)
	}
	if errors.Is(err, ErrLeaseLost) {
		t.Error("unclaimed session reported as a lost lease; the two are different failures")
	}
	// 2. There genuinely is no live claim to be found.
	if _, live, aerr := m.ActiveClaim(ctx, tenant, sid); aerr != nil || live {
		t.Fatalf("ActiveClaim = live:%v err:%v, want no live claim", live, aerr)
	}
	// 3. The attempt is Signaled, not swallowed. The signal reuses the module's
	//    existing finding machinery (live.go onFinding -> timeline + sticky
	//    marker), so an operator sees it where they already look.
	if err := m.SignalUnclaimedActivity(ctx, tenant, "sess-orphan", baseTime); err != nil {
		t.Fatalf("signal: %v", err)
	}
	rec, found := getLive(t, m, st, tenant, "sess-orphan")
	if !found {
		t.Fatal("no live row after signaling unclaimed activity")
	}
	if rec.String(colUnclaimedAt) == "" {
		t.Error("unclaimed_at empty: activity without a claim must leave a visible mark")
	}
	dto := m.toLiveDTO(rec)
	if !dto.Unclaimed {
		t.Error("liveDTO.Unclaimed false: the signal must reach the surface an operator reads")
	}
}

// A claim with no holder is refused: an anonymous claim cannot be fenced out,
// renewed or attributed to anyone.
func TestClaim_AnonymousClaimRefused(t *testing.T) {
	t.Parallel()

	m, _, tenant, _ := newSess(t)
	if _, err := m.Claim(context.Background(), tenant, "osn_x", "", time.Minute); !errors.Is(err, ErrNoHolder) {
		t.Errorf("anonymous claim = %v, want ErrNoHolder", err)
	}
}

// A caller-supplied TTL is bounded: a lease long enough to outlive the process
// holding it is a lock, not a lease.
func TestClaim_TTLIsBounded(t *testing.T) {
	t.Parallel()

	m, _, tenant, clk := newSess(t)
	ctx := context.Background()
	sid, _ := claimed(t, m, tenant, "sess-ttl", "session-A")

	l, err := m.Claim(ctx, tenant, sid, "session-A", 999*time.Hour)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if want := clk.get().Add(maxLeaseTTL); l.ExpiresAt.After(want) {
		t.Errorf("lease expires %v, capped at %v", l.ExpiresAt, want)
	}
}

// Admission, after the adversarial contrast. The first cut authorized on the
// mere EXISTENCE of a live claim (it literally discarded the lease), so any
// launcher could ride somebody else's. These assert the corrected rule: the
// LAUNCHER must be the holder, at the current fence.
func TestClaimAdmission_LauncherMustBeTheHolder(t *testing.T) {
	t.Parallel()

	m, st, tenant, _ := newSess(t)
	ctx := context.Background()

	innerCalls := 0
	inner := launchGateFunc(func(context.Context, model.TenantID, LaunchIntent) (LaunchDecision, error) {
		innerCalls++
		return LaunchDecision{Allowed: true}, nil
	})
	// The launcher names itself through the seam; here, from the intent's agent ref.
	var presentHolder string
	var presentFence int64
	gate := NewClaimAdmission(inner, m, "claude", func(LaunchIntent) (string, int64, bool) {
		return presentHolder, presentFence, presentHolder != ""
	})

	// 1. Unclaimed: denied, inner gate never reached, refusal signaled.
	presentHolder, presentFence = "session-A", 1
	dec, err := gate.Authorize(ctx, tenant, LaunchIntent{RunRef: "run-x"})
	if err != nil || dec.Allowed {
		t.Fatalf("unclaimed launch = allowed:%v err:%v, want denied", dec.Allowed, err)
	}
	if innerCalls != 0 {
		t.Errorf("inner gate ran %d times for a launch that was never admissible", innerCalls)
	}
	if rec, found := getLive(t, m, st, tenant, "run-x"); !found || rec.String(colUnclaimedAt) == "" {
		t.Error("refusal left no visible signal")
	}

	// 2. Session-A claims it.
	sid, err := m.ResolveSession(ctx, tenant, SessionBinding{Provider: "claude", ExternalID: "run-x", At: baseTime})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	lease, err := m.Claim(ctx, tenant, sid, "session-A", time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	// 3. THE REFUTED CASE. Session-B launches while A holds the claim. A live
	//    claim EXISTS, so the first cut allowed this. It must be denied.
	presentHolder, presentFence = "session-B", lease.Fence
	dec, err = gate.Authorize(ctx, tenant, LaunchIntent{RunRef: "run-x"})
	if err != nil || dec.Allowed {
		t.Fatalf("launcher riding another session's claim = allowed:%v, want denied", dec.Allowed)
	}
	if innerCalls != 0 {
		t.Errorf("inner gate reached by a launcher that holds nothing")
	}

	// 4. The right holder with a STALE fence is denied too.
	presentHolder, presentFence = "session-A", lease.Fence-1
	if dec, _ := gate.Authorize(ctx, tenant, LaunchIntent{RunRef: "run-x"}); dec.Allowed {
		t.Error("stale fence allowed; the fence must be part of the answer")
	}

	// 5. The holder, at the current fence, is admitted through.
	presentHolder, presentFence = "session-A", lease.Fence
	dec, err = gate.Authorize(ctx, tenant, LaunchIntent{RunRef: "run-x"})
	if err != nil || !dec.Allowed {
		t.Fatalf("the actual holder = allowed:%v err:%v, want allowed", dec.Allowed, err)
	}
	if innerCalls != 1 {
		t.Errorf("inner gate ran %d times, want 1", innerCalls)
	}
}

// A launcher that cannot name itself is denied, and so is a decorator built
// without an identity seam: a control that does not know who is calling cannot
// admit anyone.
func TestClaimAdmission_UnidentifiableLauncherDenied(t *testing.T) {
	t.Parallel()

	m, _, tenant, _ := newSess(t)
	ctx := context.Background()

	if dec, _ := NewClaimAdmission(nil, m, "claude", nil).
		Authorize(ctx, tenant, LaunchIntent{RunRef: "r"}); dec.Allowed {
		t.Error("a decorator with no identity seam allowed a launch")
	}
	anon := NewClaimAdmission(nil, m, "claude", func(LaunchIntent) (string, int64, bool) {
		return "", 0, false
	})
	if dec, _ := anon.Authorize(ctx, tenant, LaunchIntent{RunRef: "r2"}); dec.Allowed {
		t.Error("a launcher that did not identify itself was allowed")
	}
	// And a launch with no reference at all stays denied.
	if dec, _ := anon.Authorize(ctx, tenant, LaunchIntent{}); dec.Allowed {
		t.Error("anonymous launch allowed")
	}
}

// launchGateFunc adapts a func to the LaunchGate seam.
type launchGateFunc func(context.Context, model.TenantID, LaunchIntent) (LaunchDecision, error)

func (f launchGateFunc) Authorize(ctx context.Context, t model.TenantID, i LaunchIntent) (LaunchDecision, error) {
	return f(ctx, t, i)
}

// F5. A clock that moves BACKWARDS must not resurrect authority that was already
// observed dead. Expiry used to be a pure computation over the clock, so winding
// the clock back below lease_expires_at made a lapsed lease read as live again
// and let its old holder heartbeat with the old fence.
func TestClaim_F5_ClockRollbackCannotResurrectAuthority(t *testing.T) {
	t.Parallel()

	m, _, tenant, clk := newSess(t)
	ctx := context.Background()
	sid, lease := claimed(t, m, tenant, "sess-rollback", "session-A")

	// The lease lapses and SOMEBODY OBSERVES IT. That observation is the event
	// the durable state records.
	clk.advance(2 * time.Minute)
	if err := m.Authority(ctx, tenant, sid, "session-A", lease.Fence); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("authority after lapse = %v, want ErrLeaseLost", err)
	}

	// Now the clock goes BACKWARDS, to an instant the lease still covered.
	clk.advance(-90 * time.Second)

	if err := m.Authority(ctx, tenant, sid, "session-A", lease.Fence); !errors.Is(err, ErrLeaseLost) {
		t.Errorf("a clock rollback resurrected authority: %v", err)
	}
	if _, err := m.Heartbeat(ctx, tenant, sid, "session-A", lease.Fence, time.Minute); !errors.Is(err, ErrLeaseLost) {
		t.Errorf("a rolled-back clock let the old holder renew with the old fence: %v", err)
	}
	if _, live, err := m.ActiveClaim(ctx, tenant, sid); err != nil || live {
		t.Errorf("ActiveClaim = live:%v after a rollback, want dead", live)
	}
	// A takeover is still the ONLY way out, and it mints a new fence.
	taken, err := m.Claim(ctx, tenant, sid, "session-B", time.Minute)
	if err != nil {
		t.Fatalf("takeover after rollback: %v", err)
	}
	if taken.Fence <= lease.Fence {
		t.Errorf("takeover fence %d did not advance past %d", taken.Fence, lease.Fence)
	}
}

// F5, second half: the retirement is recorded even when the FIRST observer is a
// read. Authority is the path most likely to notice a lapse, and an observation
// nobody records is precisely what a rollback exploits.
func TestClaim_F5_ReadPathRecordsTheLapse(t *testing.T) {
	t.Parallel()

	m, _, tenant, clk := newSess(t)
	ctx := context.Background()
	sid, lease := claimed(t, m, tenant, "sess-record", "session-A")

	clk.advance(2 * time.Minute)
	_ = m.Authority(ctx, tenant, sid, "session-A", lease.Fence) // a READ observes it

	if err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		rec, found, err := findClaim(ctx, sc, sid)
		if err != nil || !found {
			t.Fatalf("claim missing: %v", err)
		}
		if got := rec.String(colClaimState); got != claimExpired {
			t.Errorf("claim_state = %q after a read observed the lapse, want %q — "+
				"an unrecorded observation is what a clock rollback exploits", got, claimExpired)
		}
		return nil
	}); err != nil {
		t.Fatalf("view: %v", err)
	}
}

// F6 — NO Behavioral TEST, AND THAT IS THE HONEST ANSWER.
//
// The fix is real and applied: Claim, Heartbeat, Release, Authority and
// ActiveClaim now read the clock INSIDE their transaction. Reading it before
// meant a wait for the write lock could exceed the TTL, so the transaction would
// commit — and RETURN — a lease already expired at the instant it was written.
//
// It is not covered by a test here, and a mutation check is why. Restoring the
// old shape (clock read before the Mutate) leaves every candidate assertion
// GREEN, because the defect is a LATENCY window: with an injected clock the
// transaction returns instantly, so the instant before it and the instant inside
// it are the same value. Making the clock advance on every read kills the fixed
// version too — the assertion stops discriminating instead of starting to.
// Reproducing F6 needs a real contended write lock, which is an integration
// concern, not a unit one.
//
// So this is recorded as VERIFIED BY CONSTRUCTION (the clock read is lexically
// inside the Mutate/View closure) and NOT by a discriminating test. Writing a
// green test here would assert nothing while looking like coverage — the exact
// failure the mutation method exists to catch, and the one it already caught
// once in this file's history.
//
// Owner of the integration-level proof: pack SG-02-b, with F1/F3.
