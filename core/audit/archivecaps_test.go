// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package audit

import "testing"

func TestParseSegmentKeyRoundTrip(t *testing.T) {
	// The inverse of SegmentKey recovers the tenant and the seq range.
	key := SegmentKey("acme", 1, 10)
	tenant, from, to, ok := ParseSegmentKey(key)
	if !ok || tenant != "acme" || from != 1 || to != 10 {
		t.Fatalf("ParseSegmentKey(%q) = %q %d %d %v", key, tenant, from, to, ok)
	}
	// A tenant containing a slash-free id with a dash survives (LastIndex of /seg-).
	key = SegmentKey("acme-eu", 42, 99)
	tenant, from, to, ok = ParseSegmentKey(key)
	if !ok || tenant != "acme-eu" || from != 42 || to != 99 {
		t.Fatalf("ParseSegmentKey(%q) = %q %d %d %v", key, tenant, from, to, ok)
	}
}

func TestParseSegmentKeyRejectsNonSegments(t *testing.T) {
	cases := []string{
		SegmentManifestKey("acme", 1, 10),          // the manifest sidecar (.manifest.json)
		"acme/keys.json",                           // the advisory keys file
		"acme/notifications/x.json",                // a notification object
		"seg-000000000001-000000000010.jsonl",      // no tenant prefix
		"acme/seg-bad-range.jsonl",                 // non-numeric range
		"acme/seg-000000000010-000000000001.jsonl", // to < from
		"",
	}
	for _, c := range cases {
		if _, _, _, ok := ParseSegmentKey(c); ok {
			t.Fatalf("ParseSegmentKey(%q) must be rejected", c)
		}
	}
}
