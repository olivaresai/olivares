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

	timeout := 30 * time.Second
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
