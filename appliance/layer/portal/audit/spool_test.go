// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package audit

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// clock is an injected clock that advances one second per reading, so no case waits.
type clock struct{ now time.Time }

func (c *clock) read() time.Time {
	c.now = c.now.Add(time.Second)
	return c.now
}

// reopen opens the spool in dir as a restarted console would, with the injected clock.
func reopen(t *testing.T, dir string, c *clock) *Spool {
	t.Helper()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("the spool did not reopen: %v", err)
	}
	s.Now, s.Random = c.read, rand.Reader
	return s
}

func ids(records []Record) []string {
	var out []string
	for _, r := range records {
		out = append(out, r.ID)
	}
	return out
}

// taker is a product that takes every record it is offered, failing on the ids in refuse.
type taker struct {
	taken  []string
	refuse map[string]bool
}

func (p *taker) deliver(_ context.Context, r Record) error {
	if p.refuse[r.ID] {
		return errors.New("the product's ledger is unavailable")
	}
	p.taken = append(p.taken, r.ID)
	return nil
}

func TestAuditSpool_SurvivesRestartAndIngestsOnce(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	c := &clock{now: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)}
	records := filepath.Join(dir, "records.jsonl")
	grows := func(before []byte) []byte {
		t.Helper()
		after, err := os.ReadFile(records)
		if err != nil {
			t.Fatalf("the spool kept no records file: %v", err)
		}
		if !bytes.HasPrefix(after, before) {
			t.Fatalf("the records file was rewritten, not appended to")
		}
		return after
	}

	s := reopen(t, dir, c)
	var appended []string
	for _, verb := range []string{"power", "network", "roll-back"} {
		id, err := s.Append(Record{Mode: "tty1", Surface: "tty1", Actor: "olivares-repair-console.service", Verb: verb, Outcome: "performed"})
		if err != nil {
			t.Fatal(err)
		}
		appended = append(appended, id)
	}
	snapshot := grows(nil)

	// A restart finds every record still to ingest, in order, as recorded.
	s = reopen(t, dir, c)
	pending, err := s.Pending()
	if err != nil || !slices.Equal(ids(pending), appended) {
		t.Fatalf("after a restart pending %v (%v), want %v", ids(pending), err, appended)
	}
	if pending[0].Time != "2026-09-26T12:00:01Z" || pending[2].Verb != "roll-back" {
		t.Fatalf("the records changed across the restart: %+v", pending)
	}

	// The product takes the first and its ledger fails on the second: the first is never
	// offered again, and the second and third wait.
	product := &taker{refuse: map[string]bool{appended[1]: true}}
	if n, err := s.Ingest(context.Background(), product.deliver); n != 1 || err == nil {
		t.Fatalf("ingest took %d (%v), want 1 and the ledger's refusal", n, err)
	}
	s = reopen(t, dir, c)
	product.refuse = nil
	if n, err := s.Ingest(context.Background(), product.deliver); n != 2 || err != nil {
		t.Fatalf("after a restart ingest took %d (%v), want the two left", n, err)
	}
	if !slices.Equal(product.taken, appended) {
		t.Fatalf("the product took %v, want each record once, in order: %v", product.taken, appended)
	}

	// Nothing is ingested twice, across a restart too.
	s = reopen(t, dir, c)
	if n, err := s.Ingest(context.Background(), product.deliver); n != 0 || err != nil || len(product.taken) != 3 {
		t.Fatalf("a second ingest took %d (%v); the product holds %v", n, err, product.taken)
	}

	// A crash that cut an append short leaves a line that is never a record, and the next
	// record starts on a line of its own.
	snapshot = grows(snapshot)
	f, err := os.OpenFile(records, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"id":"cut-short","time":"2026-09-2`); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	s = reopen(t, dir, c)
	if pending, err := s.Pending(); err != nil || len(pending) != 0 {
		t.Fatalf("a cut line is pending: %v %v", ids(pending), err)
	}
	fourth, err := s.Append(Record{Mode: "tty1", Surface: "tty1", Actor: "olivares-repair-console.service", Verb: "power", Outcome: "refused"})
	if err != nil {
		t.Fatal(err)
	}
	grows(snapshot)
	s = reopen(t, dir, c)
	if n, err := s.Ingest(context.Background(), product.deliver); n != 1 || err != nil || product.taken[3] != fourth {
		t.Fatalf("the record after a cut line: took %d (%v), product holds %v", n, err, product.taken)
	}

	t.Run("the spool refuses a directory others can reach", func(t *testing.T) {
		open := t.TempDir()
		if err := os.Chmod(open, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(open); err == nil {
			t.Fatal("a spool readable by others was opened")
		}
		if _, err := Open(filepath.Join(open, "absent")); err == nil {
			t.Fatal("an absent spool was opened")
		}
	})
}
