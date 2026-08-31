// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package fscontent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/sdk"
)

func cfg(m map[string]string) sdk.Config { return sdk.Config{Settings: m} }

func TestDescriptorAndKind(t *testing.T) {
	d := New().Descriptor()
	if d.Name != Name || d.Type != sdk.TypeContentSource {
		t.Fatalf("descriptor = %+v", d)
	}
	if New().Kind() != contentsource.ClassDocument {
		t.Fatal("kind must be document")
	}
}

// buildTree lays out a fixture directory tree plus an OUTSIDE directory with a secret,
// and a symlink inside the root that points at the outside secret (the escape attempt).
func buildTree(t *testing.T) (root, outsideSecret string) {
	t.Helper()
	root = t.TempDir()
	outside := t.TempDir()
	outsideSecret = filepath.Join(outside, "secret.txt")
	write := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mkdir := func(p string) {
		if err := os.Mkdir(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(root, "readme.md"), "hello world")
	write(filepath.Join(root, "notes.txt"), "some notes")
	write(filepath.Join(root, "image.png"), "\x89PNGbinary")
	mkdir(filepath.Join(root, "docs"))
	write(filepath.Join(root, "docs", "guide.md"), "the guide")
	mkdir(filepath.Join(root, "private"))
	write(filepath.Join(root, "private", "keys.txt"), "sensitive")
	write(outsideSecret, "TOP SECRET OUTSIDE THE ROOT")
	// The escape: a symlink inside the root pointing outside it.
	if err := os.Symlink(outsideSecret, filepath.Join(root, "escape.txt")); err != nil {
		t.Fatal(err)
	}
	return root, outsideSecret
}

func openTree(t *testing.T, extra map[string]string) *Source {
	t.Helper()
	root, _ := buildTree(t)
	m := map[string]string{fRoot: root}
	for k, v := range extra {
		m[k] = v
	}
	s := New()
	if err := s.Open(context.Background(), cfg(m)); err != nil {
		t.Fatalf("open: %v", err)
	}
	return s
}

func listAll(t *testing.T, s *Source) []contentsource.DocRef {
	t.Helper()
	var all []contentsource.DocRef
	cursor := ""
	for {
		refs, next, err := s.List(context.Background(), cursor)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		all = append(all, refs...)
		if next == "" {
			break
		}
		cursor = next
	}
	return all
}

// TestSymlinkEscapeRefused is THE read-security proof: a symlink inside the root that
// points OUTSIDE it is never followed, so the outside secret is not listed and can
// never be fetched — the os.Root confinement, proven adversarially.
func TestSymlinkEscapeRefused(t *testing.T) {
	s := openTree(t, nil)
	defer func() { _ = s.Close(context.Background()) }()

	for _, r := range listAll(t, s) {
		if strings.Contains(r.DocID, "escape") {
			t.Fatalf("the escaping symlink was listed: %q", r.DocID)
		}
		doc, err := s.Fetch(context.Background(), r.DocID)
		if err != nil {
			continue
		}
		if strings.Contains(doc.Body, "TOP SECRET") {
			t.Fatalf("ingested content escaped the root via a symlink: %q", r.DocID)
		}
	}
	// The symlink was counted as skipped, not silently ignored.
	if s.stats.symlinks == 0 {
		t.Error("expected the escaping symlink to be counted as skipped")
	}
}

// TestTraversalFetchRefused proves a hostile DocID cannot read a file outside the root:
// an id not in the index is not found, and even reaching readDocument with an escaping
// relative path is refused by os.Root.
func TestTraversalFetchRefused(t *testing.T) {
	s := openTree(t, nil)
	defer func() { _ = s.Close(context.Background()) }()

	for _, id := range []string{"../secret.txt", "../../etc/passwd", "/etc/passwd"} {
		if _, err := s.Fetch(context.Background(), id); err == nil {
			t.Errorf("Fetch(%q) must fail (not found / refused)", id)
		}
	}
	// Even bypassing the index, os.Root refuses an escaping path.
	if _, err := readDocument(context.Background(), s.root, s.sc, fileEntry{rel: "../../etc/passwd"}); err == nil {
		t.Error("readDocument with an escaping rel must be refused by os.Root")
	}
}

func TestBinaryExtensionSkipped(t *testing.T) {
	s := openTree(t, nil)
	defer func() { _ = s.Close(context.Background()) }()
	for _, r := range listAll(t, s) {
		if strings.HasSuffix(r.DocID, ".png") {
			t.Fatalf("a binary (.png) file was ingested: %q", r.DocID)
		}
	}
	if s.stats.binaries == 0 {
		t.Error("expected the .png to be counted as a skipped binary")
	}
}

func TestBinaryContentRefused(t *testing.T) {
	root := t.TempDir()
	// A .txt (text extension) whose bytes are actually binary (contain a NUL).
	if err := os.WriteFile(filepath.Join(root, "fake.txt"), []byte("ok\x00binary"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := New()
	if err := s.Open(context.Background(), cfg(map[string]string{fRoot: root})); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close(context.Background()) }()
	if _, err := s.Fetch(context.Background(), "fake.txt"); err == nil {
		t.Error("a file with NUL bytes must be refused as non-text")
	}
}

func TestSizeCapTruncates(t *testing.T) {
	root := t.TempDir()
	big := strings.Repeat("x", 5000)
	if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	s := New()
	if err := s.Open(context.Background(), cfg(map[string]string{fRoot: root, fMaxFileBytes: "1000"})); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close(context.Background()) }()
	doc, err := s.Fetch(context.Background(), "big.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Body) != 1000 {
		t.Errorf("body len = %d, want 1000 (capped)", len(doc.Body))
	}
	if doc.Attributes["truncated"] != "true" {
		t.Error("a truncated file must be marked truncated")
	}
}

func TestIncludeExcludeGlobs(t *testing.T) {
	s := openTree(t, map[string]string{fInclude: "*.md", fExclude: "private/*"})
	defer func() { _ = s.Close(context.Background()) }()
	got := map[string]bool{}
	for _, r := range listAll(t, s) {
		got[r.DocID] = true
	}
	if !got["readme.md"] || !got["docs/guide.md"] {
		t.Errorf("include *.md should keep the md files; got %v", got)
	}
	if got["notes.txt"] {
		t.Error("include *.md should have excluded notes.txt")
	}
}

func TestFetchBodyAndProvenance(t *testing.T) {
	s := openTree(t, nil)
	defer func() { _ = s.Close(context.Background()) }()
	doc, err := s.Fetch(context.Background(), "readme.md")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Body != "hello world" {
		t.Errorf("body = %q", doc.Body)
	}
	if doc.Source != SourceFilesystem {
		t.Errorf("source = %q", doc.Source)
	}
	if doc.ContentType != "text/markdown" {
		t.Errorf("content type = %q", doc.ContentType)
	}
	if doc.Attributes["path"] != "readme.md" {
		t.Errorf("path attr = %q", doc.Attributes["path"])
	}
}

func TestOpenEmptyWithoutRoot(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), cfg(map[string]string{})); err != nil {
		t.Fatalf("no root should open empty: %v", err)
	}
	refs, _, err := s.List(context.Background(), "")
	if err != nil || len(refs) != 0 {
		t.Fatalf("empty source: refs=%d err=%v", len(refs), err)
	}
}
