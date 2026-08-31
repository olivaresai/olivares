// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package reporting

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestRenderPDFReturnsUnavailableWhenChromiumIsAbsent(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := RenderPDF(context.Background(), []byte("<html><body>Report</body></html>"))
	if !errors.Is(err, ErrPDFUnavailable) {
		t.Fatalf("RenderPDF error = %v, want ErrPDFUnavailable", err)
	}
}

func TestRenderPDFStructuralOutputWhenChromiumAvailable(t *testing.T) {
	if !PDFAvailable() {
		t.Skip("chromium/google-chrome not available in PATH")
	}
	pdf, err := RenderPDF(context.Background(), []byte("<html><head><title>Olivares Test</title></head><body><h1>Executive Summary</h1></body></html>"))
	if err != nil {
		t.Fatalf("RenderPDF: %v", err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF")) {
		t.Fatalf("PDF prefix = %q, want %%PDF", pdf[:min(len(pdf), 8)])
	}
	if !bytes.Contains(pdf, []byte("%%EOF")) {
		t.Fatal("PDF missing EOF marker")
	}
	if count := bytes.Count(pdf, []byte(" obj")); count == 0 {
		t.Fatal("PDF has no object markers")
	}
}
