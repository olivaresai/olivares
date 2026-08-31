// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"log/slog"
	"testing"
)

// THE LOG BACKFILL RETURNED THE OLDEST ENTRIES, NOT THE NEWEST (2026-08-06).
//
// logRing.snapshot walked FORWARD from the oldest entry and stopped as soon as it had
// `limit`, so `limit=N` handed back the N oldest surviving entries. Its only consumer is
// the console log viewer, which uses this as the seed before attaching the SSE stream and
// then takes `.slice(-MAX_ENTRIES)` — it already assumes the newest are at the END. On the
// shipped ring of 10 000, an operator opening the viewer during an incident was handed
// ancient history and lost precisely the errors immediately before they connected, with no
// cursor to recover the gap and nothing in the response saying a newer tail had been
// dropped.
//
// Two properties, and both matter: the page must be the NEWEST N (what the consumer needs)
// and it must stay in CHRONOLOGICAL order (what the consumer renders). A fix that returned
// the newest N reversed would satisfy the first and break the second silently.
func TestLogBufferReturnsTheNewestEntriesInChronologicalOrder(t *testing.T) {
	broker := testLogBroker(discardHandler{}, 100)
	logger := slog.New(broker)
	for i := 0; i < 50; i++ {
		logger.Info("msg", "i", i)
	}

	got, matched := broker.Buffer(LogFilter{}, 3)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	for pos, wantI := range []int64{47, 48, 49} {
		if got[pos].Attrs["i"] != wantI {
			t.Errorf("entry[%d].i = %v, want %d — the page must be the NEWEST three, oldest of them first",
				pos, got[pos].Attrs["i"], wantI)
		}
	}
	// `total` used to be len(entries): the size of the page, under a name that promises the
	// size of the set. A client could not tell a buffer holding exactly `limit` from one
	// holding fifty, so it could not know it was looking at a window at all.
	if matched != 50 {
		t.Errorf("matched = %d, want 50 (every entry in the ring, not the page size)", matched)
	}
}

// The filter must be applied across the WHOLE ring before the newest N are taken, or a page
// of N would be assembled from the newest N *entries* and then filtered down to fewer — a
// silently short page whose count nobody can question.
func TestLogBufferAppliesTheFilterBeforeTakingTheNewest(t *testing.T) {
	broker := testLogBroker(discardHandler{}, 100)
	logger := slog.New(broker)
	for i := 0; i < 20; i++ {
		logger.Info("info msg", "i", i)
	}
	for i := 0; i < 3; i++ {
		logger.Error("error msg", "i", i)
	}
	// Then bury the errors under newer INFO, so a naive "newest N then filter" returns none.
	for i := 100; i < 130; i++ {
		logger.Info("info msg", "i", i)
	}

	min := slog.LevelError
	got, matched := broker.Buffer(LogFilter{Min: &min}, 2)
	if matched != 3 {
		t.Fatalf("matched = %d, want 3 errors across the whole ring", matched)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 — the newest two of the three matching", len(got))
	}
	for pos, wantI := range []int64{1, 2} {
		if got[pos].Attrs["i"] != wantI {
			t.Errorf("entry[%d].i = %v, want %d", pos, got[pos].Attrs["i"], wantI)
		}
	}
}

// A limit of zero means "everything", and everything must still be chronological — the
// existing ring tests assert exactly that shape, so this pins the seam they rely on.
func TestLogBufferWithNoLimitIsStillOldestFirst(t *testing.T) {
	broker := testLogBroker(discardHandler{}, 5)
	logger := slog.New(broker)
	for i := 0; i < 10; i++ {
		logger.Info("msg", "i", i)
	}
	got, matched := broker.Buffer(LogFilter{}, 0)
	if len(got) != 5 || matched != 5 {
		t.Fatalf("len=%d matched=%d, want 5 and 5 (the ring capacity)", len(got), matched)
	}
	if got[0].Attrs["i"] != int64(5) || got[4].Attrs["i"] != int64(9) {
		t.Errorf("got[0].i=%v got[4].i=%v, want 5 and 9", got[0].Attrs["i"], got[4].Attrs["i"])
	}
}
