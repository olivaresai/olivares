// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package logtail_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/logtail"
)

// collectBatch reads the whole file in batch mode and returns the delivered lines.
func collectBatch(t *testing.T, path string) []string {
	t.Helper()
	var got []string
	err := logtail.Tail(context.Background(), path, logtail.Options{Follow: false}, func(b []byte) error {
		got = append(got, string(b))
		return nil
	})
	if err != nil {
		t.Fatalf("Tail batch: %v", err)
	}
	return got
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func appendFile(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open append %s: %v", path, err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("append %s: %v", path, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
}

func recv(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case s := <-ch:
		return s
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for a line")
		return ""
	}
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestBatchTrailingNewline(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.log")
	writeFile(t, p, "line1\nline2\nline3\n")
	if got := collectBatch(t, p); !equalSlices(got, []string{"line1", "line2", "line3"}) {
		t.Errorf("got %v", got)
	}
}

func TestBatchNoTrailingNewline(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.log")
	writeFile(t, p, "line1\nline2\nfinal")
	if got := collectBatch(t, p); !equalSlices(got, []string{"line1", "line2", "final"}) {
		t.Errorf("got %v (final newline-less line must be delivered)", got)
	}
}

func TestBatchSkipsBlankAndTrimsCR(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.log")
	writeFile(t, p, "a\r\n\n   \r\nb\n\n")
	got := collectBatch(t, p)
	// "   " is not blank (has spaces) so it survives, CR-trimmed; empty lines skipped.
	if !equalSlices(got, []string{"a", "   ", "b"}) {
		t.Errorf("got %v", got)
	}
}

func TestBatchEmptyFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.log")
	writeFile(t, p, "")
	if got := collectBatch(t, p); len(got) != 0 {
		t.Errorf("empty file should yield no lines, got %v", got)
	}
}

func TestMissingFileErrors(t *testing.T) {
	err := logtail.Tail(context.Background(), filepath.Join(t.TempDir(), "nope.log"),
		logtail.Options{}, func([]byte) error { return nil })
	if err == nil {
		t.Fatal("expected an error opening a missing file")
	}
}

func TestLineFuncErrorPropagates(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.log")
	writeFile(t, p, "a\nb\nc\n")
	sentinel := errors.New("stop")
	var seen []string
	err := logtail.Tail(context.Background(), p, logtail.Options{}, func(b []byte) error {
		seen = append(seen, string(b))
		if string(b) == "b" {
			return sentinel
		}
		return nil
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want sentinel", err)
	}
	if !equalSlices(seen, []string{"a", "b"}) {
		t.Errorf("seen = %v, want [a b] (must stop after the failing line)", seen)
	}
}

func TestCanceledContextReturnsEarly(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.log")
	writeFile(t, p, "a\nb\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := logtail.Tail(ctx, p, logtail.Options{Follow: true}, func([]byte) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestFollowAppendAndPartialLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.log")
	writeFile(t, p, "line1\n")

	lines := make(chan string, 16)
	errc := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		errc <- logtail.Tail(ctx, p, logtail.Options{Follow: true, PollInterval: 5 * time.Millisecond},
			func(b []byte) error { lines <- string(b); return nil })
	}()

	if got := recv(t, lines); got != "line1" {
		t.Fatalf("got %q, want line1", got)
	}
	appendFile(t, p, "line2\n")
	if got := recv(t, lines); got != "line2" {
		t.Fatalf("got %q, want line2", got)
	}
	// Write a partial line, then complete it — it must not be split.
	appendFile(t, p, "par")
	appendFile(t, p, "tial\n")
	if got := recv(t, lines); got != "partial" {
		t.Fatalf("got %q, want partial", got)
	}

	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("Tail returned %v, want context.Canceled", err)
	}
}

func TestFollowTruncation(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.log")
	writeFile(t, p, "aaaa\n")

	lines := make(chan string, 16)
	errc := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		errc <- logtail.Tail(ctx, p, logtail.Options{Follow: true, PollInterval: 2 * time.Millisecond},
			func(b []byte) error { lines <- string(b); return nil })
	}()
	if got := recv(t, lines); got != "aaaa" {
		t.Fatalf("got %q, want aaaa", got)
	}
	// copytruncate: shrink, let the tailer observe size<offset and reset, then
	// write fresh content. The sleep (>> the 2ms poll interval) makes the reset
	// observable rather than racing truncate+refill within one poll window — the
	// same edge GNU tail tolerates.
	if err := os.Truncate(p, 0); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	time.Sleep(60 * time.Millisecond)
	appendFile(t, p, "bbbb\n")
	if got := recv(t, lines); got != "bbbb" {
		t.Fatalf("got %q, want bbbb after truncation", got)
	}
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("Tail returned %v, want context.Canceled", err)
	}
}

func TestFollowRotation(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.log")
	writeFile(t, p, "old1\n")

	lines := make(chan string, 16)
	errc := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		errc <- logtail.Tail(ctx, p, logtail.Options{Follow: true, PollInterval: 5 * time.Millisecond},
			func(b []byte) error { lines <- string(b); return nil })
	}()
	if got := recv(t, lines); got != "old1" {
		t.Fatalf("got %q, want old1", got)
	}
	// Rotate: move the current file aside and create a fresh one at the path.
	if err := os.Rename(p, p+".1"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	writeFile(t, p, "new1\n")
	if got := recv(t, lines); got != "new1" {
		t.Fatalf("got %q, want new1 after rotation", got)
	}
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("Tail returned %v, want context.Canceled", err)
	}
}
