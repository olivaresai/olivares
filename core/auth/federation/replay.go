// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package federation

import (
	"sync"
	"time"

	"github.com/crewjam/saml"
)

// replayStore tracks consumed SAML assertion IDs until their bearer
// SubjectConfirmationData NotOnOrAfter passes, which crewjam/saml does NOT do
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

// admit records an assertion as consumed and reports whether it is fresh (true) or
// a replay (false). An assertion with no usable NotOnOrAfter is admitted once and
// retained for a conservative default window.
func (r *replayStore) admit(a *saml.Assertion) bool {
	if a == nil || a.ID == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.now()
	// Sweep expired entries.
	for id, exp := range r.seen {
		if now.After(exp) {
			delete(r.seen, id)
		}
	}
	if _, ok := r.seen[a.ID]; ok {
		return false // already consumed within its validity window
	}
	r.seen[a.ID] = assertionExpiry(a, now)
	return true
}

// assertionExpiry returns the latest bearer SubjectConfirmationData NotOnOrAfter,
// or a conservative default when none is present.
func assertionExpiry(a *saml.Assertion, now time.Time) time.Time {
	exp := now.Add(10 * time.Minute) // conservative default
	if a.Subject != nil {
		for _, sc := range a.Subject.SubjectConfirmations {
			if sc.SubjectConfirmationData != nil && !sc.SubjectConfirmationData.NotOnOrAfter.IsZero() {
				if t := sc.SubjectConfirmationData.NotOnOrAfter; t.After(exp) {
					exp = t
				}
			}
		}
	}
	return exp
}
