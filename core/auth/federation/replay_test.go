// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package federation

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewjam/saml"
)

// assertionWithExpiry builds a minimal *saml.Assertion carrying one bearer
// SubjectConfirmationData with the given NotOnOrAfter (zero = none).
func assertionWithExpiry(id string, notOnOrAfter time.Time) *saml.Assertion {
	a := &saml.Assertion{ID: id, Subject: &saml.Subject{}}
	scd := &saml.SubjectConfirmationData{}
	if !notOnOrAfter.IsZero() {
		scd.NotOnOrAfter = notOnOrAfter
	}
	a.Subject.SubjectConfirmations = []saml.SubjectConfirmation{
		{Method: "urn:oasis:names:tc:SAML:2.0:cm:bearer", SubjectConfirmationData: scd},
	}
	return a
}

func TestReplayStore_RejectsNilAndEmptyID(t *testing.T) {
	r := newReplayStore()
	if r.admit(nil) {
		t.Error("a nil assertion must not be admitted")
	}
	if r.admit(&saml.Assertion{ID: ""}) {
		t.Error("an assertion with no ID must not be admitted (cannot dedup it — fail closed)")
	}
}

func TestReplayStore_SingleUseWithinWindow(t *testing.T) {
	r := newReplayStore()
	a := assertionWithExpiry("assert-1", time.Now().Add(5*time.Minute))
	if !r.admit(a) {
		t.Fatal("first use of a fresh assertion must be admitted")
	}
	if r.admit(a) {
		t.Error("the SAME assertion replayed within its window must be rejected (single-use)")
	}
	// A distinct ID is independent.
	if !r.admit(assertionWithExpiry("assert-2", time.Now().Add(5*time.Minute))) {
		t.Error("a different assertion id must be admitted")
	}
}

func TestReplayStore_ReadmitAfterExpirySweep(t *testing.T) {
	r := newReplayStore()
	base := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	clock := base
	r.now = func() time.Time { return clock }

	// NotOnOrAfter beyond the 10-minute floor so the entry's expiry IS its
	// NotOnOrAfter (a near value would be clamped up to the floor — see TestAssertionExpiry).
	a := assertionWithExpiry("assert-x", base.Add(30*time.Minute)) // expires at base+30m
	if !r.admit(a) {
		t.Fatal("first admit must succeed")
	}
	if r.admit(a) {
		t.Fatal("immediate replay must be rejected")
	}
	// Still within the window: replay is still rejected.
	clock = base.Add(20 * time.Minute)
	if r.admit(a) {
		t.Error("within the validity window the id must still be rejected as a replay")
	}
	// Advance past the entry's expiry: the sweep evicts it, so the id is admittable
	// again (the validity window — not the store — bounds single-use).
	clock = base.Add(31 * time.Minute)
	if !r.admit(a) {
		t.Error("after the assertion's NotOnOrAfter passes, the swept id may be admitted again")
	}
}

func TestReplayStore_Concurrent(t *testing.T) {
	r := newReplayStore()
	a := assertionWithExpiry("assert-race", time.Now().Add(5*time.Minute))
	var admitted int32
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r.admit(a) {
				atomic.AddInt32(&admitted, 1)
			}
		}()
	}
	wg.Wait()
	if admitted != 1 {
		t.Errorf("exactly one concurrent admit of the same assertion may win; got %d", admitted)
	}
}

func TestAssertionExpiry(t *testing.T) {
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)

	// A bearer NotOnOrAfter beyond the conservative floor is honored.
	far := now.Add(30 * time.Minute)
	if got := assertionExpiry(assertionWithExpiry("a", far), now); !got.Equal(far) {
		t.Errorf("expiry with a far NotOnOrAfter = %v, want %v", got, far)
	}
	// No NotOnOrAfter → the conservative 10-minute default floor.
	if got := assertionExpiry(assertionWithExpiry("b", time.Time{}), now); !got.Equal(now.Add(10 * time.Minute)) {
		t.Errorf("expiry with no NotOnOrAfter = %v, want now+10m", got)
	}
	// A NotOnOrAfter EARLIER than the floor does not shorten the window below the
	// floor (the floor dominates) — so a tiny window cannot disable replay protection.
	near := now.Add(1 * time.Minute)
	if got := assertionExpiry(assertionWithExpiry("c", near), now); !got.Equal(now.Add(10 * time.Minute)) {
		t.Errorf("expiry with a near NotOnOrAfter = %v, want the now+10m floor", got)
	}
}
