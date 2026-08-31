// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md

package main

import (
	"bytes"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/core/model"
)

// TestEventingTruncationNoticeIsAPair pins both directions.
//
// ⛔ THE NON-TRIGGERING HALF IS THE POINT, and it goes first: a notice printed unconditionally
// would satisfy the truncated case on its own and tell the operator nothing. Only the pair says the
// line follows HasMore.
func TestEventingTruncationNoticeIsAPair(t *testing.T) {
	run := func(page model.Page) string {
		cmd := &cobra.Command{}
		var err bytes.Buffer
		cmd.SetErr(&err)
		cmd.SetOut(&bytes.Buffer{})
		warnEventingTruncated(cmd, 100, page, "deliveries")
		return err.String()
	}

	if got := run(model.Page{HasMore: false}); got != "" {
		t.Errorf("a complete listing said %q, want silence", got)
	}

	got := run(model.Page{HasMore: true})
	if !strings.Contains(got, "more exist") {
		t.Errorf("a truncated listing said %q, want it to say more exist", got)
	}
	// The number is what ARRIVED, not the ceiling asked for. Printing the ceiling would be a
	// measurement nobody made.
	if !strings.Contains(got, "first 100 deliveries") {
		t.Errorf("notice = %q, want it to name the 100 rows loaded and what they are", got)
	}
}

// TestEventingNoticeGoesToStderrNotStdout guards the reason the notice exists at all.
//
// ⛔ `renderOut` serves text AND json. If this line ever moved to stdout it would land inside the
// JSON document and break every script that pipes it -- which is precisely the failure the notice
// was meant to avoid causing.
func TestEventingNoticeGoesToStderrNotStdout(t *testing.T) {
	cmd := &cobra.Command{}
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	warnEventingTruncated(cmd, 7, model.Page{HasMore: true}, "events")
	if out.Len() != 0 {
		t.Errorf("the notice wrote %q to STDOUT; it would corrupt --output json", out.String())
	}
	if errb.Len() == 0 {
		t.Error("the notice wrote nothing to stderr")
	}
}

// TestEventingListingsDoNotDiscardThePage is a local ratchet over this file's own source.
//
// ⛔ WHY A SOURCE CHECK AND NOT A BEHAVIOURAL ONE. Reproducing the defect behaviourally needs more
// than a thousand rows in a store per listing, four times over; the defect itself is one character
// -- binding the page to `_`. This asserts the shape that cannot regress silently, and it is the
// same predicate that found the four in the first place. A behavioural test for the notice already
// exists above; this one guards that the notice keeps being REACHABLE.
func TestEventingListingsDoNotDiscardThePage(t *testing.T) {
	src, err := os.ReadFile("cmd_eventing.go")
	if err != nil {
		t.Fatalf("cannot read the subject: %v", err)
	}
	descarta := regexp.MustCompile(`,\s*_,\s*err\s*:?=\s*[\w.()]*\.List\(`)
	if hits := descarta.FindAllString(string(src), -1); len(hits) > 0 {
		t.Errorf("%d listing(s) bind the store page to `_`, so has_more can never be reported: %v",
			len(hits), hits)
	}
	// Positive control: the predicate must be able to FIND that shape, or an always-empty match
	// would make this test green for the wrong reason.
	if !descarta.MatchString("recs, _, err := repo.List(ctx, q)") {
		t.Fatal("the predicate cannot match the very shape it exists to forbid")
	}
}
