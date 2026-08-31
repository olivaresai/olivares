// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package pgtest

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestForeignFixtureObject pins the attribution predicate a cluster-state audit
// relies on. It needs no server: the whole point is that provenance is decidable
// from the NAME, so a package can skip a sibling process's live objects without
// querying anything about them.
func TestForeignFixtureObject(t *testing.T) {
	me := strconv.Itoa(os.Getpid())
	for _, c := range []struct {
		name string
		want bool
		why  string
	}{
		{"olv_tx_p" + me + "_deadbeef", false, "this process's admin role is ours to account for"},
		{"olv_t_p" + me + "_deadbeef", false, "this process's database is ours to account for"},
		{"olv_to_p" + me + "_deadbeef", false, "this process's owner role is ours to account for"},
		{"olv_tx_p999999_deadbeef", true, "another process's admin role"},
		{"olv_t_p999999_deadbeef", true, "another process's database"},
		{"olv_evfence_p999999_ab", true, "any tagged prefix, another process"},
		// Untagged names are hand-written fixtures. They must NEVER be skipped, or
		// making the audit concurrency-safe would quietly stop it auditing them.
		{"olivares_app_mid", false, "hand-named fixture role, no tag"},
		{"esc_app_thing", false, "hand-named escalation fixture, no tag"},
		{"olv_evfence_notagged", false, "prefix without a process tag"},
		{"olivares_app", false, "the shared application role"},
		{"olv_tx_pnotanumber_ab", false, "unparseable tag is not evidence of another process"},
		{"olv_tx_p", false, "truncated tag"},
		{"", false, "empty name"},
	} {
		if got := ForeignFixtureObject(c.name); got != c.want {
			t.Errorf("ForeignFixtureObject(%q) = %v, want %v (%s)", c.name, got, c.want, c.why)
		}
	}
}

// TestSuffixCarriesThisProcessTag proves the two halves agree: what Suffix mints,
// ForeignFixtureObject must recognize as ours, under every identifier prefix. A tag
// that the predicate could not parse would make a package skip its OWN leaks.
func TestSuffixCarriesThisProcessTag(t *testing.T) {
	s := Suffix(t)
	if !strings.HasPrefix(s, "p"+strconv.Itoa(os.Getpid())+"_") {
		t.Fatalf("Suffix() = %q, want a leading process tag", s)
	}
	ids := newIdentifiers(s)
	for _, name := range []string{ids.database, ids.tempOwner, ids.tempAdmin} {
		if ForeignFixtureObject(name) {
			t.Errorf("ForeignFixtureObject(%q) = true for a name this process just minted", name)
		}
		if len(name) > 63 {
			t.Errorf("identifier %q is %d chars, over the 63-char Postgres limit", name, len(name))
		}
		if !safeIdent.MatchString(name) {
			t.Errorf("identifier %q does not satisfy the provisioning guard", name)
		}
	}
}
