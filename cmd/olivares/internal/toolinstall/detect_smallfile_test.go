// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type statedInfo struct {
	size int64
	mode os.FileMode
}

func (s statedInfo) Name() string       { return stagingMarker }
func (s statedInfo) Size() int64        { return s.size }
func (s statedInfo) Mode() os.FileMode  { return s.mode }
func (s statedInfo) ModTime() time.Time { return time.Time{} }
func (s statedInfo) IsDir() bool        { return false }
func (s statedInfo) Sys() any           { return nil }

type countReader struct {
	r io.Reader
	n *int64
}

func (c countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	*c.n += int64(n)
	return n, err
}

// statThenBody Lstats as size bytes and then yields body. It is the
// deterministic stand-in for a file that grew between the pre-stat and the
// read, without a racing writer.
type statThenBody struct {
	size   int64
	mode   os.FileMode
	body   []byte
	opened *bool
	read   *int64
}

func (s statThenBody) Lstat(string) (os.FileInfo, error) {
	mode := s.mode
	if mode == 0 {
		mode = 0o600
	}
	return statedInfo{size: s.size, mode: mode}, nil
}

func (s statThenBody) Open(string) (io.ReadCloser, error) {
	if s.opened != nil {
		*s.opened = true
	}
	var n *int64
	if s.read != nil {
		n = s.read
	} else {
		n = new(int64)
	}
	return io.NopCloser(countReader{r: bytes.NewReader(s.body), n: n}), nil
}

type panicOpen struct{ size int64 }

func (p panicOpen) Lstat(string) (os.FileInfo, error) {
	return statedInfo{size: p.size, mode: 0o600}, nil
}

func (panicOpen) Open(string) (io.ReadCloser, error) {
	panic("Open called despite Lstat already exceeding the cap")
}

// TestReadSmallContentsBoundsGrowthIndependentlyOfStat is the N2 discriminator:
// a mutant that keeps the Lstat cap and reverts the reader to an unbounded
// whole-file read passes TestListBoundsStagingMarkerRead (the planted file is
// already oversize) and fails this test.
func TestReadSmallContentsBoundsGrowthIndependentlyOfStat(t *testing.T) {
	const limit = stagingMarkerCap
	t.Run("stated small, body past cap", func(t *testing.T) {
		var read int64
		opened := false
		body := bytes.Repeat([]byte("x"), int(limit)+2)
		_, err := readSmallContents(statThenBody{size: 64, body: body, opened: &opened, read: &read}, stagingMarker, limit)
		if err == nil || !strings.Contains(err.Error(), "grew past") {
			t.Fatalf("want grew-past refusal, got %v", err)
		}
		if !opened {
			t.Fatal("stat-only mutant: Open was never reached")
		}
		if read > limit+1 {
			t.Fatalf("reader consumed %d bytes; LimitReader must stop at %d", read, limit+1)
		}
	})
	t.Run("stated small, huge body still reads at most limit+1", func(t *testing.T) {
		var read int64
		body := bytes.Repeat([]byte("y"), 32<<20)
		_, err := readSmallContents(statThenBody{size: 100, body: body, read: &read}, stagingMarker, limit)
		if err == nil || !strings.Contains(err.Error(), "grew past") {
			t.Fatalf("want grew-past refusal, got %v", err)
		}
		if read > limit+1 {
			t.Fatalf("unbounded read consumed %d bytes of a %d-byte body", read, len(body))
		}
	})
	t.Run("pre-stat still refuses without opening", func(t *testing.T) {
		defer func() {
			if rec := recover(); rec != nil {
				t.Fatalf("pre-stat did not bound the open: %v", rec)
			}
		}()
		_, err := readSmallContents(panicOpen{size: 256 << 20}, stagingMarker, limit)
		if err == nil || !strings.Contains(err.Error(), "larger than") {
			t.Fatalf("want pre-stat refusal, got %v", err)
		}
	})
	t.Run("within cap succeeds", func(t *testing.T) {
		body := []byte(`{"pid":1}`)
		got, err := readSmallContents(statThenBody{size: int64(len(body)), body: body}, stagingMarker, limit)
		if err != nil || !bytes.Equal(got, body) {
			t.Fatalf("got %q %v", got, err)
		}
	})
	t.Run("os.Root grow after stat", func(t *testing.T) {
		dir := t.TempDir()
		root, err := os.OpenRoot(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = root.Close() })
		if err := os.WriteFile(filepath.Join(dir, stagingMarker), []byte(`{"pid":1}`), 0o600); err != nil {
			t.Fatal(err)
		}
		extra := bytes.Repeat([]byte("z"), int(limit)+1)
		_, err = readSmallContents(growAfterStat{root: root, extra: extra}, stagingMarker, limit)
		if err == nil || !strings.Contains(err.Error(), "grew past") {
			t.Fatalf("os.Root growth: %v", err)
		}
	})
}

// growAfterStat is a real os.Root that appends extra bytes at Open, after the
// production code has already taken Lstat. Containment stays with os.Root.
type growAfterStat struct {
	root  *os.Root
	extra []byte
}

func (g growAfterStat) Lstat(name string) (os.FileInfo, error) { return g.root.Lstat(name) }

func (g growAfterStat) Open(name string) (io.ReadCloser, error) {
	f, err := g.root.OpenFile(name, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return nil, err
	}
	_, werr := f.Write(g.extra)
	cerr := f.Close()
	if werr != nil {
		return nil, werr
	}
	if cerr != nil {
		return nil, cerr
	}
	return g.root.Open(name)
}
