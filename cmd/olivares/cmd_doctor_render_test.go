// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
)

// doctor is the first command of the first hour that reports a VERDICT, so it is
// the one the terminal contract judges hardest: R8 (ASCII status tokens, never a
// bare word and never a glyph), R9 (a one-line summary and the next command) and
// the rule that a column block must still be a column block on an ordinary
// terminal.
//
// The form it replaced put five columns in one tabwriter, the widest of them a
// 96-column sentence. That row is about 140 columns wide: on any ordinary
// terminal the columns collapse and REMEDIATION wraps under DETAIL, which is the
// defect measured on `step-05-doctor.png` on 2026-09-18 and recorded as
// "below the bar, both sides".

// doctorRenderFixture is a report with one check of every role, a check whose
// detail is longer than any terminal, and a check with no detail at all.
func doctorRenderFixture() doctorReport {
	return doctorReport{
		Schema:  doctorSchema,
		Overall: "unhealthy",
		Mode:    "user",
		Init:    "unknown",
		Build:   doctorBuild{Version: "v26.10", Commit: "0123456789ab"},
		Checks: []doctorCheck{
			{Name: "binary", Status: "pass", Required: true, Detail: "/usr/local/bin/olivares mode=0755"},
			{Name: "data-directory", Status: "fail", Required: true,
				Detail:      "/var/lib/olivares mode=0777",
				Remediation: "remove group/world write access; user mode requires 0700 and system mode 0700 or 0750"},
			{Name: "service-unit", Status: "unknown", Required: true,
				Detail:      "no supported init adapter was detected",
				Remediation: "pass --init after confirming systemd, OpenRC or launchd"},
			{Name: "license", Status: "not_applicable", Required: false, Detail: "source=none"},
			{Name: "service-account", Status: "pass", Required: true},
		},
	}
}

// doctorPlainGolden is the report at EIGHTY columns. The width is in the name of
// the helper below and not left to the environment: since the DETAIL block folds,
// the plain form is a function of the width, and a golden that inherited COLUMNS
// would pass or fail on the shell that ran it.
const doctorPlainGolden = `OVERALL  unhealthy
MODE     user
INIT     unknown
VERSION  v26.10
COMMIT   0123456789ab

CHECK            STATUS  REQUIRED
binary           [ok]    true
data-directory   [fail]  true
service-unit     [--]    true
license          [--]    false
service-account  [ok]    true

DETAIL
BINARY           /usr/local/bin/olivares mode=0755
DATA-DIRECTORY   /var/lib/olivares mode=0777 | fix: remove group/world write
                 access; user mode requires 0700 and system mode 0700 or 0750
SERVICE-UNIT     no supported init adapter was detected | fix: pass --init after
                 confirming systemd, OpenRC or launchd
LICENSE          source=none
SERVICE-ACCOUNT  -

2 ok, 1 fail - 5 checks, 2 not measured, 2 required and not passing
next: olivares doctor
`

// plainDoctorAt renders the fixture with colour off at a stated width.
func plainDoctorAt(width int) string {
	var b strings.Builder
	off := false
	drawDoctor(termrender.New(&b, termrender.Options{Color: &off, Width: width}), doctorRenderFixture())
	return b.String()
}

// TestDoctorPlainGolden pins the bytes at eighty columns.
func TestDoctorPlainGolden(t *testing.T) {
	if got := plainDoctorAt(80); got != doctorPlainGolden {
		t.Errorf("the doctor report's plain form changed.\n got:\n%s\nwant:\n%s", got, doctorPlainGolden)
	}
	// And a PIPE gets the same bytes. A stream with no width reads as "unbounded"
	// for a table, and folding a sentence has to pick something: it picks 80, so
	// the form a script receives is one set of bytes and not the shell's guess.
	if piped := plainOf(func(r *termrender.Renderer) { drawDoctor(r, doctorRenderFixture()) }); piped != doctorPlainGolden {
		t.Errorf("a pipe does not receive the eighty-column form:\n%s", piped)
	}
}

// doctorVerdictTable is the verdict rows of a rendered report, header included.
func doctorVerdictTable(report string) []string {
	const header = "CHECK            STATUS  REQUIRED"
	var out []string
	inTable := false
	for _, line := range strings.Split(report, "\n") {
		if line == header {
			inTable = true
		}
		if !inTable {
			continue
		}
		if strings.TrimSpace(line) == "" {
			break
		}
		out = append(out, line)
	}
	return out
}

// TestDoctorColumnsHoldAtHundredAndEighty is the assertion the old form could not
// pass. The verdict table is three narrow columns, so it neither falls back to
// records nor overflows: the same rows come out at 100 columns and at 80.
//
// It is the TABLE and no longer the whole report, and that is the change of
// 2026-09-19 rather than a weakening. The report is now width-aware ON PURPOSE:
// DETAIL folds, so 100 and 80 differ there by design, and the test that says so is
// TestDoctorDetailFoldsToTheTerminalWidth below. What must NOT change with width is
// the verdict table, because a table that collapses at 80 is the defect this whole
// family was written for.
func TestDoctorColumnsHoldAtHundredAndEighty(t *testing.T) {
	wide, narrow := doctorVerdictTable(plainDoctorAt(100)), doctorVerdictTable(plainDoctorAt(80))
	if len(narrow) == 0 {
		t.Fatal("the scan never found the verdict table, so it proved nothing")
	}
	if strings.Join(wide, "\n") != strings.Join(narrow, "\n") {
		t.Errorf("the verdict table differs between 100 and 80 columns, so something collapsed.\n100:\n%s\n80:\n%s",
			strings.Join(wide, "\n"), strings.Join(narrow, "\n"))
	}
	// Its header is a header and not a record key. A record fallback writes
	// "CHECK  <value>" per row, so the header line would be gone.
	if wide[0] != "CHECK            STATUS  REQUIRED" {
		t.Fatalf("the verdict table fell back to records at 100 columns: %q", wide[0])
	}
	// And every line of it fits in 80 columns. This is the measurable half of
	// "the columns hold": the old form's rows were about 140 wide.
	for _, line := range wide {
		if len(line) > 80 {
			t.Errorf("a verdict row is %d columns wide and will wrap at 80: %q", len(line), line)
		}
	}
}

// TestDoctorDetailFoldsToTheTerminalWidth is F6 of the second pass, and it asserts
// the two halves of the one defect.
//
// MEASURED 2026-09-19 on the head binary: fifteen DETAIL lines over 80 columns, the
// widest 158, and the report byte-identical at 80 and at 120. One block that
// overflows a narrow terminal AND wastes a wide one — the two failures a fixed
// width produces at once, which is why neither of them alone is the assertion.
func TestDoctorDetailFoldsToTheTerminalWidth(t *testing.T) {
	widest := func(report string) (int, string) {
		max, at := 0, ""
		for _, line := range strings.Split(report, "\n") {
			if n := len([]rune(line)); n > max {
				max, at = n, line
			}
		}
		return max, at
	}

	for _, width := range []int{80, 120} {
		got, line := widest(plainDoctorAt(width))
		if got > width {
			t.Errorf("at %d columns a line is %d wide, so it wraps where the report did not "+
				"choose to: %q", width, got, line)
		}
	}

	// The other direction, and the one a fixed width fails: a wide terminal is
	// USED. Without it, "no line over 80" is satisfied by folding everything at 40.
	if got, _ := widest(plainDoctorAt(120)); got <= 80 {
		t.Errorf("at 120 columns the widest line is %d, so the report folds at a width "+
			"nobody asked for and wastes the terminal it was given", got)
	}

	// The folds hang under the value column, which is what makes the block read as
	// one field per paragraph instead of as a second column of words.
	const indent = "                 " // len("DATA-DIRECTORY") padded to SERVICE-ACCOUNT, plus two
	folds := 0
	for _, line := range strings.Split(plainDoctorAt(80), "\n") {
		if strings.HasPrefix(line, indent) {
			folds++
		}
	}
	if folds < 2 {
		t.Errorf("the eighty-column form has %d hanging folds; the fixture carries two "+
			"sentences longer than eighty columns, so a count below two means nothing folded", folds)
	}
}

// TestDoctorJSONIsIndependentOfTheTerminal is the guard on the machine form. The
// text form is now a function of the width; `-o json` must not be, because it is
// what a script reads and a script has no terminal.
func TestDoctorJSONIsIndependentOfTheTerminal(t *testing.T) {
	at := func(columns string) string {
		t.Helper()
		t.Setenv("COLUMNS", columns)
		cmd := &cobra.Command{}
		cmd.Flags().String("output", "json", "")
		var out bytes.Buffer
		cmd.SetOut(&out)
		if err := renderDoctor(cmd, doctorRenderFixture()); err != nil {
			t.Fatalf("render at COLUMNS=%s: %v", columns, err)
		}
		return out.String()
	}
	narrow, wide := at("80"), at("120")
	if narrow != wide {
		t.Errorf("doctor's JSON differs between COLUMNS=80 and COLUMNS=120.\n80:\n%s\n120:\n%s", narrow, wide)
	}
	if !strings.Contains(narrow, `"overall"`) {
		t.Fatalf("this is not the JSON form, so the comparison above proved nothing:\n%s", narrow)
	}
}

// TestDoctorRichIsPlainPlusColour is the property every converted family gets.
func TestDoctorRichIsPlainPlusColour(t *testing.T) {
	for _, width := range []int{100, 80} {
		assertRichIsPlainPlusColour(t, "doctor", width, func(r *termrender.Renderer) {
			drawDoctor(r, doctorRenderFixture())
		})
	}
}

// TestDoctorStatusesAreTheClosedTokenSet is R8: no bare `pass`/`fail`/`unknown`
// word in the STATUS column, no Unicode glyph, and the four ASCII tokens only.
//
// It asserts BOTH directions. A test that only checked "contains [ok]" would pass
// on the old output too, because the DETAIL sentences never mention brackets — so
// it also asserts that the words the old form printed are gone from the column.
func TestDoctorStatusesAreTheClosedTokenSet(t *testing.T) {
	out := plainOf(func(r *termrender.Renderer) { drawDoctor(r, doctorRenderFixture()) })
	for _, want := range []string{"[ok]", "[fail]", "[--]"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %s token in the report:\n%s", want, out)
		}
	}
	for _, glyph := range []string{"✓", "✗", "✔", "✘"} {
		if strings.Contains(out, glyph) {
			t.Errorf("the report draws %q, which is a replacement character on a host without the font", glyph)
		}
	}
	for _, row := range []string{"binary           pass", "data-directory   fail", "service-unit     unknown", "license          not_applicable"} {
		if strings.Contains(out, row) {
			t.Errorf("the STATUS column still carries a bare word: %q", row)
		}
	}
	// The mapping itself, in both directions, so a status this binary grows later
	// cannot quietly render as a pass.
	for status, want := range map[string]string{
		"pass": "[ok]", "warn": "[warn]", "fail": "[fail]",
		"unknown": "[--]", "not_applicable": "[--]", "": "[--]", "healthy": "[--]",
	} {
		if got := termrender.StatusToken(doctorStatusRole(status)); got != want {
			t.Errorf("status %q renders %s, want %s", status, got, want)
		}
	}
}

// TestDoctorEndsWithSummaryAndNextCommand is R9: one summary line, then the exact
// command to run. The old form ended with the last row of a collapsed table.
func TestDoctorEndsWithSummaryAndNextCommand(t *testing.T) {
	out := plainOf(func(r *termrender.Renderer) { drawDoctor(r, doctorRenderFixture()) })
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("the report is %d lines", len(lines))
	}
	summary, next := lines[len(lines)-2], lines[len(lines)-1]
	if summary != "2 ok, 1 fail - 5 checks, 2 not measured, 2 required and not passing" {
		t.Errorf("summary line = %q", summary)
	}
	if next != "next: olivares doctor" {
		t.Errorf("next line = %q", next)
	}

	// A healthy install is pointed at the FIRST HOUR, from the same table the
	// help section reads, so the terminal and the help cannot disagree.
	healthy := doctorRenderFixture()
	healthy.Overall = "healthy"
	for i := range healthy.Checks {
		healthy.Checks[i].Status = "pass"
		healthy.Checks[i].Remediation = ""
	}
	out = plainOf(func(r *termrender.Renderer) { drawDoctor(r, healthy) })
	want := "next: " + firstHourNextCommands["doctor"]
	if !strings.HasSuffix(strings.TrimRight(out, "\n"), want) {
		t.Errorf("a healthy report does not end with %q:\n%s", want, out)
	}
	if want == "next: " {
		t.Fatal("the first-hour table has no entry for doctor, so the assertion above proved nothing")
	}
}

// TestDoctorRendersNoCheckHonestly: an empty check list is a defect in doctor, not
// an empty table. The renderer's Empty sentence is what says so.
func TestDoctorRendersNoChecksHonestly(t *testing.T) {
	report := doctorRenderFixture()
	report.Checks = nil
	out := plainOf(func(r *termrender.Renderer) { drawDoctor(r, report) })
	if !strings.Contains(out, "doctor ran no checks") {
		t.Errorf("an empty report says nothing about being empty:\n%s", out)
	}
	if !strings.Contains(out, "nothing to report") {
		t.Errorf("an empty report has no summary line:\n%s", out)
	}
}
