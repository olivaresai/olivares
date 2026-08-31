// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package fscontent

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/sdk"
)

// fakeExtractor stands in for the engine's sandboxed extractor so the connector's
// sniff→extract→provenance wiring is testable without spawning a subprocess.
type fakeExtractor struct {
	text    string
	subtype string
	err     error
	calls   int
	gotKind contentsource.RichDocKind
}

func (f *fakeExtractor) Extract(_ context.Context, kind contentsource.RichDocKind, _ []byte) (string, string, error) {
	f.calls++
	f.gotKind = kind
	return f.text, f.subtype, f.err
}

// minimalDOCX builds a byte slice that sniffs as OOXML (PK\x03\x04) — the fake
// extractor decides the result, so the parts only need the zip magic.
func minimalDOCX(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("[Content_Types].xml")
	_, _ = w.Write([]byte("<Types/>"))
	w, _ = zw.Create("word/document.xml")
	_, _ = w.Write([]byte("<w:document/>"))
	_ = zw.Close()
	return buf.Bytes()
}

func openWith(t *testing.T, root string, ext contentsource.RichDocExtractor) *Source {
	t.Helper()
	s := New(WithExtractor(ext))
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{fRoot: root}}); err != nil {
		t.Fatalf("open: %v", err)
	}
	return s
}

func TestRichDoc_ExtractedWhenInjected(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "report.docx"), minimalDOCX(t), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := &fakeExtractor{text: "extracted body", subtype: "docx"}
	s := openWith(t, root, fake)

	// The .docx is indexed (walk counts it as a rich-doc candidate).
	if s.stats.richDocs != 1 {
		t.Fatalf("richDocs stat = %d, want 1", s.stats.richDocs)
	}
	refs := listAll(t, s)
	if len(refs) != 1 {
		t.Fatalf("listed %d refs, want 1", len(refs))
	}

	doc, err := s.Fetch(context.Background(), "report.docx")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if fake.calls != 1 || fake.gotKind != contentsource.RichDocOOXML {
		t.Fatalf("extractor calls=%d kind=%q, want 1 ooxml", fake.calls, fake.gotKind)
	}
	if doc.Body != "extracted body" {
		t.Errorf("body = %q, want extracted text", doc.Body)
	}
	if doc.ContentType != "text/plain" {
		t.Errorf("content type = %q, want text/plain (body is text)", doc.ContentType)
	}
	if doc.Attributes["extracted"] != "true" || doc.Attributes["source_content_type"] != "docx" {
		t.Errorf("provenance attrs = %v, want extracted/source_content_type", doc.Attributes)
	}
}

func TestRichDoc_SkippedWithoutExtractor(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "report.docx"), minimalDOCX(t), 0o644); err != nil {
		t.Fatal(err)
	}
	// No extractor injected: the .docx is a counted binary skip, never indexed.
	s := openWith(t, root, nil)
	if s.stats.richDocs != 0 {
		t.Fatalf("richDocs stat = %d, want 0 (extraction disabled)", s.stats.richDocs)
	}
	if s.stats.binaries != 1 {
		t.Fatalf("binaries stat = %d, want 1 (docx counted as skipped binary)", s.stats.binaries)
	}
	if refs := listAll(t, s); len(refs) != 0 {
		t.Fatalf("listed %d refs, want 0", len(refs))
	}
}

func TestRichDoc_NotExtractableIsSkipSignal(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "broken.docx"), minimalDOCX(t), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := &fakeExtractor{err: contentsource.ErrNotExtractable}
	s := openWith(t, root, fake)

	_, err := s.Fetch(context.Background(), "broken.docx")
	if !errors.Is(err, contentsource.ErrSkipDocument) {
		t.Fatalf("fetch err = %v, want wraps ErrSkipDocument", err)
	}
}

func TestRichDoc_BinaryContentIsSkipSignal(t *testing.T) {
	// A .txt whose bytes contain a NUL is skipped (not fatal) — the skip sentinel now
	// wraps ErrSkipDocument so the ingest loop counts it and continues.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "fake.txt"), []byte("ok\x00binary"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := openWith(t, root, nil)
	_, err := s.Fetch(context.Background(), "fake.txt")
	if !errors.Is(err, contentsource.ErrSkipDocument) {
		t.Fatalf("fetch err = %v, want wraps ErrSkipDocument", err)
	}
}

func TestRichDoc_TextExtensionWithMagicNotRerouted(t *testing.T) {
	// A TEXT-extension file whose bytes happen to begin with %PDF- (no NUL) must stay on
	// the text path and be ingested as text — the content sniff only reroutes rich-doc
	// EXTENSIONS, so a coincidental magic prefix cannot silently drop a real text file.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "doc.txt"), []byte("%PDF-1.7 this is actually a text note"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := &fakeExtractor{text: "should not be used"}
	s := openWith(t, root, fake)

	doc, err := s.Fetch(context.Background(), "doc.txt")
	if err != nil {
		t.Fatalf("fetch err = %v, want the text file ingested (not skipped)", err)
	}
	if fake.calls != 0 {
		t.Fatalf("extractor called %d times for a text-extension file, want 0", fake.calls)
	}
	if doc.ContentType != "text/plain" || doc.Body == "" {
		t.Errorf("doc = {ct:%q body:%q}, want text/plain non-empty", doc.ContentType, doc.Body)
	}
}

func TestRichDoc_RichExtensionSniffingPDFSkipped(t *testing.T) {
	// A rich-EXTENSION file (.pptx) whose bytes sniff as PDF is a skip (PDF disabled),
	// and the OOXML extractor is never invoked for it.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "deck.pptx"), []byte("%PDF-1.7 not a real deck"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := &fakeExtractor{text: "should not be used"}
	s := openWith(t, root, fake)
	_, err := s.Fetch(context.Background(), "deck.pptx")
	if !errors.Is(err, contentsource.ErrSkipDocument) {
		t.Fatalf("fetch err = %v, want wraps ErrSkipDocument (PDF disabled)", err)
	}
	if fake.calls != 0 {
		t.Fatalf("extractor called %d times for PDF, want 0 (short-circuited)", fake.calls)
	}
}

func TestRichDoc_RichExtensionPlainTextIsTextPlain(t *testing.T) {
	// A .docx-named file that is NOT actually an OOXML zip (plain text, no PK header) is
	// ingested on the text path with an HONEST text/plain content type — never stamped
	// with the Office MIME its extension implies.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notreally.docx"), []byte("just plain prose, not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := &fakeExtractor{err: contentsource.ErrNotExtractable}
	s := openWith(t, root, fake)

	doc, err := s.Fetch(context.Background(), "notreally.docx")
	if err != nil {
		t.Fatalf("fetch err = %v, want text ingest", err)
	}
	if fake.calls != 0 {
		t.Fatalf("extractor called %d times, want 0 (no PK header → text path)", fake.calls)
	}
	if doc.ContentType != "text/plain" {
		t.Errorf("content type = %q, want text/plain (honest — body is text)", doc.ContentType)
	}
}

func TestRichDoc_BudgetAccountsRealReadSize(t *testing.T) {
	// A rich doc is READ up to richDocMaxInputBytes at Fetch, so the walk must CHARGE it
	// at that ceiling, not at the ≤1 MiB text cap — otherwise max_total_bytes is
	// defeated. Build a >maxFileBytes .docx and assert the byte budget saw its real size.
	root := t.TempDir()
	big := oversizeDOCX(t, 1500*1024) // ~1.5 MiB on disk (> the 1 MiB maxFileBytes cap)
	if err := os.WriteFile(filepath.Join(root, "big.docx"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	fake := &fakeExtractor{text: "x", subtype: "docx"}
	s := openWith(t, root, fake)
	if s.stats.totalBytes <= int64(s.sc.maxFileBytes) {
		t.Fatalf("byte budget charged %d for a %d-byte rich doc, want > maxFileBytes %d (real read size, not the text cap)",
			s.stats.totalBytes, len(big), s.sc.maxFileBytes)
	}
}

// oversizeDOCX builds a valid-looking OOXML zip padded to at least size bytes on disk
// via a STORED (uncompressed) media part, so info.Size() reflects the padding.
func oversizeDOCX(t *testing.T, size int) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("[Content_Types].xml")
	_, _ = w.Write([]byte("<Types/>"))
	w, _ = zw.Create("word/document.xml")
	_, _ = w.Write([]byte("<w:document/>"))
	// A stored part of incompressible-enough bytes so the on-disk zip is ~size.
	sw, err := zw.CreateHeader(&zip.FileHeader{Name: "word/media/blob.bin", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	pad := make([]byte, size)
	for i := range pad {
		pad[i] = byte(i * 7)
	}
	_, _ = sw.Write(pad)
	_ = zw.Close()
	return buf.Bytes()
}

func TestSniffRichDoc(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want contentsource.RichDocKind
	}{
		{"ooxml", []byte("PK\x03\x04rest"), contentsource.RichDocOOXML},
		{"pdf", []byte("%PDF-1.7"), contentsource.RichDocPDF},
		{"empty-zip", []byte("PK\x05\x06"), contentsource.RichDocNone},
		{"text", []byte("hello world"), contentsource.RichDocNone},
		{"short", []byte("PK"), contentsource.RichDocNone},
	}
	for _, tc := range cases {
		if got := contentsource.SniffRichDoc(tc.in); got != tc.want {
			t.Errorf("%s: sniff = %q, want %q", tc.name, got, tc.want)
		}
	}
}
