// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package reporting

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// chromiumBinary returns the first found chromium/chrome binary, or "".
func chromiumBinary() string {
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "chrome"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

// PDFAvailable reports whether PDF generation is available (chromium found).
func PDFAvailable() bool {
	return chromiumBinary() != ""
}

// RenderPDF converts HTML bytes to PDF via headless chromium --print-to-pdf.
// Returns ErrPDFUnavailable if no chromium is found.
func RenderPDF(ctx context.Context, html []byte) ([]byte, error) {
	// The budget is read first: a malformed OLIVARES_PDF_RENDER_TIMEOUT is refused on every box,
	// with or without a browser, before anything is written or spawned.
	timeout, err := pdfRenderTimeout()
	if err != nil {
		return nil, err
	}
	bin := chromiumBinary()
	if bin == "" {
		return nil, ErrPDFUnavailable
	}

	dir, err := os.MkdirTemp("", "olivares-report-*")
	if err != nil {
		return nil, fmt.Errorf("pdf: create temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	inPath := filepath.Join(dir, "report.html")
	outPath := filepath.Join(dir, "report.pdf")

	if err := os.WriteFile(inPath, html, 0o600); err != nil {
		return nil, fmt.Errorf("pdf: write html: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// #nosec G204 -- bin is resolved via exec.LookPath (fixed chromium/edge allow-list); all args are fixed flags
	cmd := exec.CommandContext(ctx, bin,
		"--headless",
		"--disable-gpu",
		"--no-sandbox",
		"--disable-dev-shm-usage",
		"--print-to-pdf="+outPath,
		"--no-pdf-header-footer",
		"--run-all-compositor-stages-before-draw",
		"file://"+inPath,
	)
	cmd.Stderr = nil
	cmd.Stdout = nil

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("pdf: chromium: %w", err)
	}

	pdf, err := os.ReadFile(outPath)
	if err != nil {
		return nil, fmt.Errorf("pdf: read output: %w", err)
	}
	return pdf, nil
}

// ErrPDFUnavailable is returned when PDF generation is requested but no
// chromium binary is found on the system.
var ErrPDFUnavailable = fmt.Errorf("PDF generation requires chromium or google-chrome in PATH")

// pdfRenderTimeoutEnv names the wall-clock budget for one Chromium render. The default,
// 30 s, is the product's posture for a live request. The budget is a fact of the machine
// that runs the render, not of the code: measured 2026-09-16 on a GitHub-hosted 4-vCPU
// runner under the race detector, the structural test's render was killed at 30.05 s
// ("pdf: chromium: signal: killed") while the same render fits on an 8-CPU host. A test
// that inherits a live-request budget on a slower, instrumented box measures the clock,
// not the renderer (08 §A row 2), so the budget is declared where the box is known.
const pdfRenderTimeoutEnv = "OLIVARES_PDF_RENDER_TIMEOUT"

const pdfRenderTimeoutDefault = 30 * time.Second

// pdfRenderTimeout returns the render budget: the default unless OLIVARES_PDF_RENDER_TIMEOUT
// carries a Go duration. A value that is not a duration, or not positive, is refused by name
// rather than replaced by the default: a mistyped budget must not silently become 30 s.
func pdfRenderTimeout() (time.Duration, error) {
	raw, ok := os.LookupEnv(pdfRenderTimeoutEnv)
	if !ok || raw == "" {
		return pdfRenderTimeoutDefault, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("pdf: %s=%q is not a duration: %w", pdfRenderTimeoutEnv, raw, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("pdf: %s=%q must be positive", pdfRenderTimeoutEnv, raw)
	}
	return d, nil
}
