// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package release

import "testing"

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in                  string
		wantErr             bool
		maj, min, pat, nPre int
	}{
		{in: "26.7.0", maj: 26, min: 7, pat: 0},
		{in: "v26.7.1", maj: 26, min: 7, pat: 1},
		{in: "26.8.0-rc.1", maj: 26, min: 8, pat: 0, nPre: 2},
		{in: "1.2.3+build.5", maj: 1, min: 2, pat: 3}, // build metadata dropped
		{in: "dev"}, // zero version, no error
		{in: ""},    // zero version, no error
		{in: "26.7", wantErr: true},
		{in: "26.7.0.1", wantErr: true},
		{in: "26.x.0", wantErr: true},
		{in: "26.7.-1", wantErr: true},
		{in: "26.8.0-", wantErr: true},
	}
	for _, c := range cases {
		v, err := ParseVersion(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseVersion(%q): want error, got %+v", c.in, v)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseVersion(%q): unexpected error %v", c.in, err)
			continue
		}
		if v.Major != c.maj || v.Minor != c.min || v.Patch != c.pat || len(v.Pre) != c.nPre {
			t.Errorf("ParseVersion(%q) = %d.%d.%d pre=%v, want %d.%d.%d nPre=%d", c.in, v.Major, v.Minor, v.Patch, v.Pre, c.maj, c.min, c.pat, c.nPre)
		}
	}
}

func TestCompareOrder(t *testing.T) {
	// Strictly increasing precedence, incl. SemVer prerelease rules and dev-lowest.
	order := []string{
		"dev",
		"25.0.0",
		"26.6.9",
		"26.7.0-alpha",
		"26.7.0-alpha.1",
		"26.7.0-alpha.2",
		"26.7.0-beta",
		"26.7.0-rc.1",
		"26.7.0",
		"26.7.1",
		"26.8.0",
		"27.0.0",
	}
	vs := make([]Version, len(order))
	for i, s := range order {
		v, err := ParseVersion(s)
		if err != nil {
			t.Fatalf("ParseVersion(%q): %v", s, err)
		}
		vs[i] = v
	}
	for i := 0; i < len(vs); i++ {
		for j := 0; j < len(vs); j++ {
			got := Compare(vs[i], vs[j])
			want := cmpInt(i, j)
			if got != want {
				t.Errorf("Compare(%q,%q) = %d, want %d", order[i], order[j], got, want)
			}
		}
	}
	// Newer is the anti-rollback predicate.
	a, _ := ParseVersion("26.7.0")
	b, _ := ParseVersion("26.7.1")
	if !a.Newer(b) {
		t.Error("26.7.0.Newer(26.7.1) must be true")
	}
	if b.Newer(a) {
		t.Error("26.7.1.Newer(26.7.0) must be false (that is a rollback)")
	}
	if a.Newer(a) {
		t.Error("equal versions are not newer")
	}
	// An unstamped build parses to the zero Version, so raw PRECEDENCE puts every real
	// release above it — and this test used to stop there, under the heading "any real
	// release outranks a dev build". That framing is what the min-version gate read as
	// "too old to upgrade", inverting it. Both halves are pinned now: Compare
	// still answers the precedence question, and IsUnstamped is the predicate that says
	// the question should not have been asked. A future change that makes the upgrade
	// path refuse unstamped builds must not have to delete an assertion to do it.
	dev, _ := ParseVersion("dev")
	if !dev.Newer(a) {
		t.Error("raw precedence: the zero Version is below every real release")
	}
	if !IsUnstamped("dev") {
		t.Error("...but \"dev\" is unstamped, so that precedence is not a position anyone may act on")
	}
}
