// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall"
)

// The first hour's list commands go through one Table primitive. These are golden
// tests on the PLAIN (non-terminal) form, because that is what a script reads and
// what a capture records.

// render is the plain, non-terminal form: no colour, unbounded width, exactly
// what a pipe receives.
func render(t *testing.T, width int, f func(*termrender.Renderer)) string {
	t.Helper()
	var buf bytes.Buffer
	no := false
	f(termrender.New(&buf, termrender.Options{Color: &no, Width: width}))
	return buf.String()
}

// TestToolDetectTableGivesEveryCandidateTheSameColumns is the defect this table
// exists for.
//
// MEASURED 2026-09-18: two candidates on one host printed tab-separated rows of
// DIFFERENT lengths with no header, so the fourth field was a size on one line
// and a symlink target on the next. The assertion is not "it looks nicer": it is
// that every row answers every column, in the header's order.
func TestToolDetectTableGivesEveryCandidateTheSameColumns(t *testing.T) {
	cands := []toolinstall.Candidate{
		{
			Path: "/opt/olivares/tools/grok/1.0.34/grok", Origin: "release", Match: toolinstall.MatchRegistered,
			Version: "1.0.34", Size: 163035648, SHA256: strings.Repeat("a", 64),
		},
		{
			// The candidate that produced the defect: no version, no size, no
			// probe — three empty cells in the MIDDLE of the row, which is what
			// made the following values slide left under the wrong headers.
			Path: "/home/op/.local/bin/claude", Origin: "path", Match: toolinstall.MatchUnregisteredObserved,
			IsSymlink: true, Resolved: "/home/op/.local/share/claude/versions/2.1.276", Note: "not in the tools root",
		},
	}
	got := render(t, 0, func(r *termrender.Renderer) {
		r.Table(toolCandidateTable(cands, "no executable found"))
	})

	// The golden is the whole block, spaces included, because the defect WAS the
	// spacing: an assertion that only looks up a value would pass while the value
	// sits under another column's header. Compare it as bytes.
	want := strings.Join([]string{
		"PATH                                  ORIGIN   MATCH                  VERSION  SIZE             SHA256                                                            RESOLVES TO                                    PROBE  NOTE",
		"/opt/olivares/tools/grok/1.0.34/grok  release  registered             1.0.34   163035648 bytes  aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"/home/op/.local/bin/claude            path     unregistered-observed                                                                                              /home/op/.local/share/claude/versions/2.1.276         not in the tools root",
	}, "\n") + "\n"
	if got != want {
		t.Fatalf("the plain form changed.\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}

	// And the property the golden encodes, asserted so a future re-golden cannot
	// quietly accept a shifted row: the second candidate's LAST two values sit at
	// the offsets its header names, even though three cells before them are empty.
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	for _, col := range []struct {
		header, value string
	}{
		{"RESOLVES TO", "/home/op/.local/share/claude/versions/2.1.276"},
		{"NOTE", "not in the tools root"},
	} {
		at := strings.Index(lines[0], col.header)
		if at < 0 {
			t.Fatalf("no %s column in the header", col.header)
		}
		if !strings.HasPrefix(lines[2][at:], col.value) {
			t.Fatalf("%s does not start at its header's offset %d: %q", col.header, at, lines[2])
		}
	}
}

// TestToolDetectTableKeepsTheProbeBesideItsCandidate pins that running the
// executable is reported, and reported as one of four mutually exclusive things.
func TestToolDetectTableKeepsTheProbeBesideItsCandidate(t *testing.T) {
	cases := []struct {
		name string
		cand toolinstall.Candidate
		want string
		role termrender.Role
	}{
		{"not probed", toolinstall.Candidate{}, "", termrender.RoleNone},
		{"refused", toolinstall.Candidate{ProbeSkipped: "unregistered path"}, "skipped: unregistered path", termrender.RoleWarn},
		{"ran and failed", toolinstall.Candidate{ProbeError: "exit 1"}, "failed: exit 1", termrender.RoleFail},
		{"ran", toolinstall.Candidate{Probe: &toolinstall.ProbeReport{Output: "grok 1.0.34\nmore", DurationMS: 67}}, "grok 1.0.34 (67 ms)", termrender.RoleOK},
		// A candidate can carry BOTH a run and its failure. The failure wins:
		// reporting the output of a run the engine classified as failed would
		// tell the operator the tool works.
		{"ran, failed, and printed something", toolinstall.Candidate{
			Probe: &toolinstall.ProbeReport{Output: "grok 1.0.34", DurationMS: 67}, ProbeError: "signature refused",
		}, "failed: signature refused", termrender.RoleFail},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, role := toolProbeCell(tc.cand)
			if got != tc.want || role != tc.role {
				t.Fatalf("toolProbeCell = (%q, %v), want (%q, %v)", got, role, tc.want, tc.role)
			}
		})
	}
}

// TestToolDetectTableColoursOnlyMeasuredFailure pins the match colouring.
//
// An unregistered path is the ORDINARY finding on a host where the operator
// installed the vendor's CLI themselves — the first hour's own case. Colouring it
// red teaches the operator to ignore red.
func TestToolDetectTableColoursOnlyMeasuredFailure(t *testing.T) {
	for match, want := range map[string]termrender.Role{
		toolinstall.MatchRegistered:           termrender.RoleOK,
		toolinstall.MatchManifestCorroborated: termrender.RoleOK,
		toolinstall.MatchUnverified:           termrender.RoleWarn,
		toolinstall.MatchDamaged:              termrender.RoleFail,
		toolinstall.MatchUnregisteredObserved: termrender.RoleNone,
		"a-class-a-newer-engine-reports":      termrender.RoleNone,
	} {
		if got := toolMatchRole(match); got != want {
			t.Fatalf("toolMatchRole(%q) = %v, want %v", match, got, want)
		}
	}
}

// TestSessionStateColouring pins the same argument for session states: `stopped`
// and `cleaned` are ordinary ends of a session's life, not failures.
func TestSessionStateColouring(t *testing.T) {
	for state, want := range map[string]termrender.Role{
		"running": termrender.RoleOK, "idle": termrender.RoleOK,
		"failed":  termrender.RoleFail,
		"stopped": termrender.RoleMuted, "cleaned": termrender.RoleMuted,
		"pending": termrender.RoleNone, "": termrender.RoleNone,
	} {
		if got := sessionStateRole(state); got != want {
			t.Fatalf("sessionStateRole(%q) = %v, want %v", state, got, want)
		}
	}
}

// TestFirstHourTablesFallBackToRecordsWhenNarrow pins that the record form keeps
// every byte. A truncated SHA-256 or a truncated path cannot be copied, and an
// operator copying an identifier is the whole point of printing it.
func TestFirstHourTablesFallBackToRecordsWhenNarrow(t *testing.T) {
	sha := strings.Repeat("a", 64)
	cands := []toolinstall.Candidate{{
		Path: "/opt/olivares/tools/grok/1.0.34/grok", Origin: "release",
		Match: toolinstall.MatchRegistered, Version: "1.0.34", Size: 163035648, SHA256: sha,
	}}
	got := render(t, 80, func(r *termrender.Renderer) {
		r.Table(toolCandidateTable(cands, "no executable found"))
	})
	if strings.Contains(got, "PATH  ORIGIN") {
		t.Fatalf("80 columns cannot hold this row; want the record form:\n%s", got)
	}
	for _, keep := range []string{sha, "/opt/olivares/tools/grok/1.0.34/grok", "163035648 bytes", "1.0.34"} {
		if !strings.Contains(got, keep) {
			t.Fatalf("the record form dropped %q:\n%s", keep, got)
		}
	}
}

// TestFirstHourTablesSayNothingRatherThanPrintNothing keeps the guarantee
// renderListOut was built for: an empty list is a sentence, not zero bytes.
func TestFirstHourTablesSayNothingRatherThanPrintNothing(t *testing.T) {
	got := render(t, 0, func(r *termrender.Renderer) {
		r.Table(toolCandidateTable(nil, "no grok executable found under /opt, the vendor default paths or PATH"))
	})
	if strings.TrimSpace(got) != "no grok executable found under /opt, the vendor default paths or PATH" {
		t.Fatalf("an empty list must say so, got %q", got)
	}
}
