// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package reporting

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
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
	// The render budget is the box's, not the live request's: under the race detector on a
	// GitHub-hosted 4-vCPU runner this render was killed at the 30 s default (measured
	// 2026-09-16, race-full modules-rest); 120 s is the first re-measure, and the elapsed
	// time is logged so the next run can size it from a number instead of a kill.
	t.Setenv(pdfRenderTimeoutEnv, "120s")
	started := time.Now()
	pdf, err := RenderPDF(context.Background(), []byte("<html><head><title>Olivares Test</title></head><body><h1>Executive Summary</h1></body></html>"))
	t.Logf("chromium render took %s (budget %s)", time.Since(started).Round(time.Millisecond), "120s")
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

func TestPDFRenderTimeoutIsDeclaredByTheBox(t *testing.T) {
	t.Setenv(pdfRenderTimeoutEnv, "")
	if d, err := pdfRenderTimeout(); err != nil || d != pdfRenderTimeoutDefault {
		t.Fatalf("unset: got %v, %v; want %v, nil", d, err, pdfRenderTimeoutDefault)
	}
	t.Setenv(pdfRenderTimeoutEnv, "90s")
	if d, err := pdfRenderTimeout(); err != nil || d != 90*time.Second {
		t.Fatalf("90s: got %v, %v", d, err)
	}
	for _, bad := range []string{"soon", "-5s", "0"} {
		t.Setenv(pdfRenderTimeoutEnv, bad)
		_, err := pdfRenderTimeout()
		if err == nil || !strings.Contains(err.Error(), pdfRenderTimeoutEnv) {
			t.Fatalf("%q: want an error naming %s, got %v", bad, pdfRenderTimeoutEnv, err)
		}
	}
	// And the refusal reaches RenderPDF before any process is spawned.
	t.Setenv(pdfRenderTimeoutEnv, "soon")
	if _, err := RenderPDF(context.Background(), []byte("<html></html>")); err == nil || !strings.Contains(err.Error(), pdfRenderTimeoutEnv) {
		t.Fatalf("RenderPDF with a malformed budget: want an error naming the variable, got %v", err)
	}
}
