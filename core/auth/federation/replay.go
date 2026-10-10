// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package federation

import (
	"sync"
	"time"
)

// replayFloor is the shortest time a consumed assertion id is remembered, so a tiny
// NotOnOrAfter cannot disable replay protection.
const replayFloor = 10 * time.Minute

// replayStore tracks consumed SAML assertion IDs until their bearer
// SubjectConfirmationData NotOnOrAfter passes, which gosaml2 does NOT do
// (SAML 2.0 §4.1.4.5). Without it a captured POST body can be replayed within the
// assertion validity window. Single-node in-memory is sufficient: assertions are
// short-lived and a restart only narrows the window.
type replayStore struct {
	mu   sync.Mutex
	seen map[string]time.Time // assertion id -> expiry (NotOnOrAfter)
	// now is the clock; it is time.Now in production and overridable in tests so the
	// expiry-sweep of this anti-replay control can be exercised deterministically.
	now func() time.Time
}

func newReplayStore() *replayStore {
	return &replayStore{seen: map[string]time.Time{}, now: time.Now}
}

// admit records an assertion id as consumed and reports whether it is fresh (true) or
// a replay (false). The id is retained until notOnOrAfter, but at least replayFloor
// (a zero notOnOrAfter uses the floor alone). An empty id cannot be deduplicated and is
// refused.
func (r *replayStore) admit(id string, notOnOrAfter time.Time) bool {
	if id == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.now()
	// Sweep expired entries.
	for seen, exp := range r.seen {
		if now.After(exp) {
			delete(r.seen, seen)
		}
	}
	if _, ok := r.seen[id]; ok {
		return false // already consumed within its validity window
	}
	r.seen[id] = assertionExpiry(notOnOrAfter, now)
	return true
}

// assertionExpiry returns the later of the bearer NotOnOrAfter and now+replayFloor.
func assertionExpiry(notOnOrAfter, now time.Time) time.Time {
	if floor := now.Add(replayFloor); notOnOrAfter.Before(floor) {
		return floor
	}
	return notOnOrAfter
}
