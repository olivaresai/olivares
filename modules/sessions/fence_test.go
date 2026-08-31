// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"errors"
	"math"
	"testing"
	"time"
)

var fenceTestNow = time.Date(2026, time.August, 10, 12, 0, 0, 0, time.UTC)

func TestClampFenceTTLUsesEntityPolicy(t *testing.T) {
	t.Parallel()

	policy := fenceTTLPolicy{Default: time.Minute, Min: 10 * time.Second, Max: 5 * time.Minute}
	for _, tc := range []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{name: "default", in: 0, want: time.Minute},
		{name: "minimum", in: time.Second, want: 10 * time.Second},
		{name: "inside bounds", in: 30 * time.Second, want: 30 * time.Second},
		{name: "maximum", in: 10 * time.Minute, want: 5 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampFenceTTL(tc.in, policy); got != tc.want {
				t.Fatalf("clampFenceTTL(%s) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

func TestNextFenceIsMonotonicAndRefusesWrap(t *testing.T) {
	t.Parallel()

	if got, err := nextFence(41); err != nil || got != 42 {
		t.Fatalf("nextFence(41) = %d, %v; want 42, nil", got, err)
	}
	if got, err := nextFence(math.MaxInt64); got != 0 || !errors.Is(err, ErrFenceExhausted) {
		t.Fatalf("nextFence(MaxInt64) = %d, %v; want 0, ErrFenceExhausted", got, err)
	}
}

func TestFenceAcquireChangesHandsButNeverStealsLiveAuthority(t *testing.T) {
	t.Parallel()

	policy := fenceTTLPolicy{Default: time.Minute, Max: 5 * time.Minute}
	fresh, err := fenceAcquire(fenceState{}, "worker:a", fenceTestNow, 0, policy)
	if err != nil {
		t.Fatalf("fresh acquire: %v", err)
	}
	if fresh.Holder != "worker:a" || fresh.Fence != 1 || fresh.Lifecycle != fenceActive ||
		!fresh.AcquiredAt.Equal(fenceTestNow) || !fresh.ExpiresAt.Equal(fenceTestNow.Add(time.Minute)) {
		t.Fatalf("fresh acquire = %#v", fresh)
	}

	// Non-trigger direction: another holder cannot turn acquire into an implicit
	// takeover while the old token is live.
	if got, err := fenceAcquire(fresh, "worker:b", fenceTestNow.Add(time.Second), time.Minute, policy); !errors.Is(err, errFenceHeld) || got != fresh {
		t.Fatalf("live acquire = %#v, %v; want unchanged, errFenceHeld", got, err)
	}

	old := fenceState{
		Holder: "worker:a", Fence: 8, Lifecycle: fenceActive,
		AcquiredAt: fenceTestNow.Add(-time.Hour), RenewedAt: fenceTestNow.Add(-time.Minute),
		ExpiresAt: fenceTestNow, EndedAt: fenceTestNow, EndReason: "old", RenewalCount: 9,
	}
	taken, err := fenceAcquire(old, "worker:b", fenceTestNow, 2*time.Minute, policy)
	if err != nil {
		t.Fatalf("expired takeover: %v", err)
	}
	if taken.Holder != "worker:b" || taken.Fence != 9 || taken.Lifecycle != fenceActive {
		t.Fatalf("takeover = %#v, want worker:b at fence 9", taken)
	}
	if !taken.RenewedAt.IsZero() || !taken.EndedAt.IsZero() || taken.EndReason != "" || taken.RenewalCount != 0 {
		t.Fatalf("takeover retained former lifecycle metadata: %#v", taken)
	}
}

func TestFenceAcquireRefusesExhaustedTakeover(t *testing.T) {
	t.Parallel()

	current := fenceState{
		Holder: "worker:a", Fence: math.MaxInt64, Lifecycle: fenceExpired,
		ExpiresAt: fenceTestNow.Add(-time.Minute),
	}
	got, err := fenceAcquire(current, "worker:b", fenceTestNow, time.Minute, fenceTTLPolicy{})
	if !errors.Is(err, ErrFenceExhausted) || got != current {
		t.Fatalf("exhausted takeover = %#v, %v; want unchanged, ErrFenceExhausted", got, err)
	}
}

func TestFenceRenewKeepsFenceAndRequiresExactLiveToken(t *testing.T) {
	t.Parallel()

	current := fenceState{
		Holder: "worker:a", Fence: 17, Lifecycle: fenceActive,
		AcquiredAt: fenceTestNow.Add(-time.Minute), ExpiresAt: fenceTestNow.Add(time.Minute),
		RenewalCount: 2,
	}
	renewed, err := fenceRenew(current, fenceToken{Holder: "worker:a", Fence: 17},
		fenceTestNow, 30*time.Second, fenceTTLPolicy{Default: time.Minute})
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if renewed.Fence != current.Fence {
		t.Fatalf("renew moved fence %d -> %d", current.Fence, renewed.Fence)
	}
	if renewed.RenewalCount != 3 || !renewed.RenewedAt.Equal(fenceTestNow) ||
		!renewed.ExpiresAt.Equal(fenceTestNow.Add(30*time.Second)) {
		t.Fatalf("renewed = %#v", renewed)
	}

	for _, tc := range []struct {
		name  string
		state fenceState
		token fenceToken
	}{
		{name: "wrong holder", state: current, token: fenceToken{Holder: "worker:b", Fence: 17}},
		{name: "wrong fence", state: current, token: fenceToken{Holder: "worker:a", Fence: 16}},
		{name: "lapsed", state: func() fenceState {
			v := current
			v.ExpiresAt = fenceTestNow
			return v
		}(), token: fenceToken{Holder: "worker:a", Fence: 17}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, renewErr := fenceRenew(tc.state, tc.token, fenceTestNow, time.Minute, fenceTTLPolicy{})
			if !errors.Is(renewErr, errFenceLost) || got != tc.state {
				t.Fatalf("renew = %#v, %v; want unchanged, errFenceLost", got, renewErr)
			}
		})
	}
}

func TestFenceReleaseKeepsEntityPoliciesSeparate(t *testing.T) {
	t.Parallel()

	lapsed := fenceState{
		Holder: "worker:a", Fence: 23, Lifecycle: fenceActive,
		AcquiredAt: fenceTestNow.Add(-time.Minute), ExpiresAt: fenceTestNow,
	}
	// Claim policy: an exact token may release after lapse and the next acquire,
	// rather than release itself, performs the monotonic bump.
	claimEnded, err := fenceRelease(lapsed, fenceToken{Holder: "worker:a", Fence: 23},
		fenceTestNow, "", fenceEndPolicy{Lifecycle: fenceReleased})
	if err != nil {
		t.Fatalf("claim-style release: %v", err)
	}
	if claimEnded.Lifecycle != fenceReleased || claimEnded.Fence != 23 ||
		!claimEnded.EndedAt.Equal(fenceTestNow) {
		t.Fatalf("claim-style release = %#v", claimEnded)
	}

	live := lapsed
	live.ExpiresAt = fenceTestNow.Add(time.Minute)
	workEnded, err := fenceRelease(live, fenceToken{Holder: "worker:a", Fence: 23},
		fenceTestNow, "owner died", fenceEndPolicy{
			Lifecycle: fenceRevoked, Bump: true, RequireLive: true,
		})
	if err != nil {
		t.Fatalf("work-style end: %v", err)
	}
	if workEnded.Lifecycle != fenceRevoked || workEnded.Fence != 24 ||
		workEnded.EndReason != "owner died" || !workEnded.EndedAt.Equal(fenceTestNow) {
		t.Fatalf("work-style end = %#v", workEnded)
	}

	// Non-trigger direction: a WorkLease terminal transition cannot be performed
	// after liveness has already ended, even with the former exact token.
	if got, err := fenceRelease(lapsed, fenceToken{Holder: "worker:a", Fence: 23},
		fenceTestNow, "late", fenceEndPolicy{Lifecycle: fenceRevoked, Bump: true, RequireLive: true}); !errors.Is(err, errFenceLost) || got != lapsed {
		t.Fatalf("late work end = %#v, %v; want unchanged, errFenceLost", got, err)
	}
}

func TestFenceReleaseRefusesExhaustedInvalidation(t *testing.T) {
	t.Parallel()

	current := fenceState{
		Holder: "worker:a", Fence: math.MaxInt64, Lifecycle: fenceActive,
		ExpiresAt: fenceTestNow.Add(time.Minute),
	}
	got, err := fenceRelease(current, fenceToken{Holder: "worker:a", Fence: math.MaxInt64},
		fenceTestNow, "done", fenceEndPolicy{Lifecycle: fenceReleased, Bump: true, RequireLive: true})
	if !errors.Is(err, ErrFenceExhausted) || got != current {
		t.Fatalf("exhausted release = %#v, %v; want unchanged, ErrFenceExhausted", got, err)
	}
}

func TestMaterializeExpiryIsOneWayAndCanInvalidateImmediately(t *testing.T) {
	t.Parallel()

	current := fenceState{
		Holder: "worker:a", Fence: 31, Lifecycle: fenceActive,
		ExpiresAt: fenceTestNow,
	}
	if got, changed, err := materializeExpiry(current, fenceTestNow.Add(-time.Nanosecond), "reaped", true); err != nil || changed || got != current {
		t.Fatalf("pre-expiry materialize = %#v, %v, %v; want unchanged", got, changed, err)
	}

	got, changed, err := materializeExpiry(current, fenceTestNow, "reaped", true)
	if err != nil || !changed {
		t.Fatalf("at-expiry materialize changed=%v err=%v", changed, err)
	}
	if got.Lifecycle != fenceExpired || got.Fence != 32 || got.EndReason != "reaped" ||
		!got.EndedAt.Equal(fenceTestNow) {
		t.Fatalf("materialized expiry = %#v", got)
	}
	if again, changedAgain, errAgain := materializeExpiry(got, fenceTestNow.Add(time.Hour), "again", true); errAgain != nil || changedAgain || again != got {
		t.Fatalf("second materialize = %#v, %v, %v; want unchanged", again, changedAgain, errAgain)
	}
}

func TestMaterializeExpiryPersistsTerminalFactAtFenceExhaustion(t *testing.T) {
	t.Parallel()

	current := fenceState{
		Holder: "worker:a", Fence: math.MaxInt64, Lifecycle: fenceActive,
		ExpiresAt: fenceTestNow,
	}
	got, changed, err := materializeExpiry(current, fenceTestNow, "reaped", true)
	if !changed || !errors.Is(err, ErrFenceExhausted) {
		t.Fatalf("exhausted expiry changed=%v err=%v", changed, err)
	}
	if got.Lifecycle != fenceExpired || got.Fence != math.MaxInt64 || !got.EndedAt.Equal(fenceTestNow) {
		t.Fatalf("exhausted terminal state = %#v", got)
	}
}

func TestAssertFenceRequiresLiveHolderAndToken(t *testing.T) {
	t.Parallel()

	live := fenceState{
		Holder: "worker:a", Fence: 41, Lifecycle: fenceActive,
		ExpiresAt: fenceTestNow.Add(time.Minute),
	}
	if err := assertFence(live, fenceToken{Holder: "worker:a", Fence: 41}, fenceTestNow); err != nil {
		t.Fatalf("exact live authority: %v", err)
	}
	for _, tc := range []struct {
		name  string
		state fenceState
		token fenceToken
	}{
		{name: "wrong holder", state: live, token: fenceToken{Holder: "worker:b", Fence: 41}},
		{name: "wrong fence", state: live, token: fenceToken{Holder: "worker:a", Fence: 40}},
		{name: "at expiry", state: func() fenceState {
			v := live
			v.ExpiresAt = fenceTestNow
			return v
		}(), token: fenceToken{Holder: "worker:a", Fence: 41}},
		{name: "terminal state", state: func() fenceState {
			v := live
			v.Lifecycle = fenceReleased
			return v
		}(), token: fenceToken{Holder: "worker:a", Fence: 41}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := assertFence(tc.state, tc.token, fenceTestNow); !errors.Is(err, errFenceLost) {
				t.Fatalf("assertFence err = %v, want errFenceLost", err)
			}
		})
	}
}
