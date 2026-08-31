// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package plugjail

import "testing"

// TestUIDAllocatorDistinctAndSkipsReserved is the F8 regression: co-resident plugins must
// get DISTINCT uids (the old base+counter%uidRange wrapped, colliding after uidRange launches)
// and reserved/system uids (nobody=65534, nogroup=65535, root) must never be assigned.
func TestUIDAllocatorDistinctAndSkipsReserved(t *testing.T) {
	a := &uidAllocator{live: map[int]bool{}}
	const base = defaultPluginUID // 65533 → base+1=65534 (nobody), base+2=65535 (nogroup)

	seen := map[int]bool{}
	var uids []int
	for i := 0; i < 500; i++ {
		uid, ok := a.acquire(base)
		if !ok {
			t.Fatalf("acquire failed at co-resident #%d (range not exhausted)", i)
		}
		if seen[uid] {
			t.Fatalf("co-resident plugins share uid %d — isolation defeated (F8)", uid)
		}
		if reservedUIDs[uid] {
			t.Fatalf("assigned reserved uid %d", uid)
		}
		if uid < minPluginUID {
			t.Fatalf("assigned system uid %d", uid)
		}
		seen[uid] = true
		uids = append(uids, uid)
	}
	if seen[65534] || seen[65535] {
		t.Fatal("assigned the nobody/nogroup reserved uid (F8: the old scheme hit these at base+1/+2)")
	}

	// Releasing a uid returns it to the pool; a later launch may reuse it (the prior plugin is
	// gone, so distinctness among LIVE plugins still holds).
	a.release(uids[0])
	uid, ok := a.acquire(base)
	if !ok {
		t.Fatal("acquire after release failed")
	}
	if uid != uids[0] {
		t.Errorf("expected reuse of released uid %d, got %d", uids[0], uid)
	}
}

// TestUIDAllocatorExhaustionDegradesHonestly proves the pool is bounded and reports ok=false
// when exhausted (the caller then degrades the attestation instead of asserting false isolation).
func TestUIDAllocatorExhaustionDegradesHonestly(t *testing.T) {
	a := &uidAllocator{live: map[int]bool{}}
	const base = 100000 // no reserved uids in [base, base+uidRange)

	got := 0
	for {
		_, ok := a.acquire(base)
		if !ok {
			break
		}
		if got++; got > uidRange+10 {
			t.Fatal("allocator never exhausted — the pool is unbounded")
		}
	}
	if got != uidRange {
		t.Errorf("acquired %d distinct uids, want the full range %d", got, uidRange)
	}
	if _, ok := a.acquire(base); ok {
		t.Error("acquire past a fully-live range must return ok=false (honest degrade)")
	}
}
