// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build e2e

package main

// Real round-trip for the sandboxed rich-document extractor: build the engine binary
// and drive sandboxedRichDocExtractor.Extract, which re-execs `olivares __extract`
// under plugjail confinement (env-scoped child, cgroup ceilings where available) and
// streams the OOXML bytes in / extracted text out. plugjail strips the child's
// environment, so the in-process test trampoline cannot survive — a freshly built
// binary is injected as exePath. Gated behind `e2e` because it compiles + execs the
// binary; the in-process suite covers the child logic (extractOnce) and the connector
// wiring (fake extractor) hermetically.

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/connectors/contentsource"
)

func buildEngine(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "olivares")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build engine: %v\n%s", err, out)
	}
	return bin
}

func e2eDOCX(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("[Content_Types].xml")
	_, _ = w.Write([]byte(`<Types/>`))
	w, _ = zw.Create("word/document.xml")
	_, _ = w.Write([]byte(`<?xml version="1.0"?><w:document xmlns:w="w"><w:body>` +
		`<w:p><w:t>Confined</w:t></w:p><w:p><w:t>Extraction</w:t></w:p></w:body></w:document>`))
	_ = zw.Close()
	return buf.Bytes()
}

func TestE2EExtract_SandboxedRoundTrip(t *testing.T) {
	ext := &sandboxedRichDocExtractor{exePath: buildEngine(t)}
	text, subtype, err := ext.Extract(context.Background(), contentsource.RichDocOOXML, e2eDOCX(t))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if subtype != "docx" {
		t.Errorf("subtype = %q, want docx", subtype)
	}
	if text != "Confined\nExtraction" {
		t.Errorf("text = %q, want %q", text, "Confined\nExtraction")
	}
}

func TestE2EExtract_GarbageIsSkip(t *testing.T) {
	ext := &sandboxedRichDocExtractor{exePath: buildEngine(t)}
	_, _, err := ext.Extract(context.Background(), contentsource.RichDocOOXML, []byte("PK\x03\x04 not a real archive"))
	if !errors.Is(err, contentsource.ErrNotExtractable) {
		t.Fatalf("Extract(garbage) err = %v, want ErrNotExtractable", err)
	}
}

func TestE2EExtract_PDFKindSkipped(t *testing.T) {
	// PDF is not supported this release: the extractor short-circuits to ErrNotExtractable
	// without even spawning the child.
	ext := &sandboxedRichDocExtractor{exePath: buildEngine(t)}
	_, _, err := ext.Extract(context.Background(), contentsource.RichDocPDF, []byte("%PDF-1.7"))
	if !errors.Is(err, contentsource.ErrNotExtractable) {
		t.Fatalf("Extract(pdf) err = %v, want ErrNotExtractable", err)
	}
}
