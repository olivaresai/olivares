// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"errors"
	"math"
	"time"
)

// fenceLifecycle is the entity-neutral lifecycle understood by the fencing
// primitive. Claim and WorkLease deliberately keep their own rows, columns and
// public states; their adapters translate only the states they support.
type fenceLifecycle uint8

const (
	fenceVacant fenceLifecycle = iota
	fenceActive
	fenceReleased
	fenceExpired
	fenceRevoked
)

// fenceState is the pure value on which fencing decisions operate. It contains no
// record ID, entity kind, tenant or workspace: those remain the caller's domain and
// are loaded and persisted by that entity's transaction.
type fenceState struct {
	Holder       string
	Fence        int64
	Lifecycle    fenceLifecycle
	AcquiredAt   time.Time
	RenewedAt    time.Time
	ExpiresAt    time.Time
	EndedAt      time.Time
	EndReason    string
	RenewalCount int64
}

// fenceToken is the complete authority presented by a writer. Knowing either the
// holder or the number alone never grants authority.
type fenceToken struct {
	Holder string
	Fence  int64
}

// fenceTTLPolicy lets entities share the transition code without silently sharing
// policy. A zero Min or Max leaves that bound open.
type fenceTTLPolicy struct {
	Default time.Duration
	Min     time.Duration
	Max     time.Duration
}

// fenceEndPolicy captures the two intentional differences between Claim and
// WorkLease. Claim release is allowed after liveness has ended and is invalidated
// by the next acquire; WorkLease end transitions require live authority and bump
// immediately. Keeping that choice explicit prevents either entity from inheriting
// the other's semantics by accident.
type fenceEndPolicy struct {
	Lifecycle   fenceLifecycle
	Bump        bool
	RequireLive bool
}

var (
	// errFenceHeld means an acquire found authority that has not lapsed. The
	// entity maps this internal reason to its own public conflict.
	errFenceHeld = errors.New("sessions: fence held")
	// errFenceLost means a renewal, release or write no longer carries the exact
	// live authority required by its operation.
	errFenceLost = errors.New("sessions: fenced authority lost")
)

// clampFenceTTL applies an entity's explicit duration policy.
func clampFenceTTL(ttl time.Duration, policy fenceTTLPolicy) time.Duration {
	if ttl <= 0 {
		ttl = policy.Default
	}
	if policy.Min > 0 && ttl < policy.Min {
		ttl = policy.Min
	}
	if policy.Max > 0 && ttl > policy.Max {
		ttl = policy.Max
	}
	return ttl
}

// nextFence mints the next monotonic token and refuses the only operation that
// could make it move backwards.
func nextFence(current int64) (int64, error) {
	if current == math.MaxInt64 {
		return 0, ErrFenceExhausted
	}
	return current + 1, nil
}

// fenceIsLive reports authority at one supplied instant. The caller owns the time
// source; this helper never consults an application clock or invents a fallback.
func fenceIsLive(current fenceState, now time.Time) bool {
	return current.Lifecycle == fenceActive && !current.ExpiresAt.IsZero() && now.Before(current.ExpiresAt)
}

// fenceAcquire acquires a vacant/ended/lapsed state. A live state is never an
// implicit renewal: callers that intentionally support renewal use fenceRenew.
func fenceAcquire(
	current fenceState,
	holder string,
	now time.Time,
	ttl time.Duration,
	policy fenceTTLPolicy,
) (fenceState, error) {
	if fenceIsLive(current, now) {
		return current, errFenceHeld
	}
	next, err := nextFence(current.Fence)
	if err != nil {
		return current, err
	}
	return fenceState{
		Holder:     holder,
		Fence:      next,
		Lifecycle:  fenceActive,
		AcquiredAt: now,
		ExpiresAt:  now.Add(clampFenceTTL(ttl, policy)),
	}, nil
}

// fenceRenew extends from now under the same holder and fence. It never moves the
// fencing token.
func fenceRenew(
	current fenceState,
	token fenceToken,
	now time.Time,
	ttl time.Duration,
	policy fenceTTLPolicy,
) (fenceState, error) {
	if err := assertFence(current, token, now); err != nil {
		return current, err
	}
	current.RenewedAt = now
	current.ExpiresAt = now.Add(clampFenceTTL(ttl, policy))
	current.RenewalCount++
	return current, nil
}

// fenceRelease ends authority under an explicit entity policy. The end timestamp
// also closes the old expiry window; entity adapters may omit fields they do not
// persist.
func fenceRelease(
	current fenceState,
	token fenceToken,
	now time.Time,
	reason string,
	policy fenceEndPolicy,
) (fenceState, error) {
	if policy.RequireLive {
		if err := assertFence(current, token, now); err != nil {
			return current, err
		}
	} else if current.Holder != token.Holder || current.Fence != token.Fence {
		return current, errFenceLost
	}
	if policy.Bump {
		next, err := nextFence(current.Fence)
		if err != nil {
			return current, err
		}
		current.Fence = next
	}
	current.Lifecycle = policy.Lifecycle
	current.ExpiresAt = now
	current.EndedAt = now
	current.EndReason = reason
	return current, nil
}

// materializeExpiry turns the first observation of a lapse into a one-way state
// transition. If an invalidating bump is exhausted, the returned state still
// carries the observed expiry and changed=true; callers can persist that safe
// terminal fact and deliver ErrFenceExhausted only after the transaction commits.
func materializeExpiry(
	current fenceState,
	now time.Time,
	reason string,
	bump bool,
) (next fenceState, changed bool, err error) {
	if current.Lifecycle != fenceActive || fenceIsLive(current, now) {
		return current, false, nil
	}
	next = current
	next.Lifecycle = fenceExpired
	next.EndedAt = now
	next.EndReason = reason
	if bump {
		var fence int64
		fence, err = nextFence(current.Fence)
		if err == nil {
			next.Fence = fence
		}
	}
	return next, true, err
}

// assertFence is the deny-closed pure authority decision used by both read checks
// and transactional CAS checks. Entity code adds its own non-sensitive public
// error and performs the row touch inside the effect's transaction.
func assertFence(current fenceState, token fenceToken, now time.Time) error {
	if !fenceIsLive(current, now) || current.Holder != token.Holder || current.Fence != token.Fence {
		return errFenceLost
	}
	return nil
}
