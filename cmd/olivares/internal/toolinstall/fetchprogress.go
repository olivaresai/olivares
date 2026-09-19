// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// fetchProgress counts the bytes going into the staged object and reports them.
//
// MEASURED 2026-09-18 walking the first hour: `agent tool install --driver grok`
// printed "downloading from <url>" and then nothing at all while it moved
// 163,035,648 bytes. On this machine that was 6.98 s; on a slow link it is
// minutes of a terminal that looks hung, and the operator's only question —
// "is it doing anything?" — had no answer on screen.
//
// It wraps ONLY the writer handed to FetchV2. The file is still fsynced and
// closed through its own handle, and the provider still computes the digest from
// the bytes it writes: this observes the stream and does not stand between the
// file and its verification.
type fetchProgress struct {
	w       io.Writer
	out     io.Writer
	total   int64 // the expected size, or 0 when the vendor did not declare one
	written int64
	last    time.Time
	every   time.Duration
	mu      sync.Mutex
	// now is a seam for the test; nil means time.Now.
	now func() time.Time
}

// newFetchProgress reports at most every 500 ms. A download is not a frame
// buffer: more often would spend the operator's terminal on a number that has
// not meaningfully changed.
func newFetchProgress(w, out io.Writer, total int64) *fetchProgress {
	return &fetchProgress{w: w, out: out, total: total, every: 500 * time.Millisecond}
}

func (p *fetchProgress) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

// Write passes the bytes through and reports at most one line per interval.
//
// A short write or an error is returned EXACTLY as the underlying writer gave
// it. Progress reporting must never be able to change the outcome of a fetch.
func (p *fetchProgress) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.mu.Lock()
	p.written += int64(n)
	written, total := p.written, p.total
	report := false
	if now := p.clock(); p.last.IsZero() || now.Sub(p.last) >= p.every {
		p.last, report = now, true
	}
	p.mu.Unlock()
	if report && err == nil {
		p.line(written, total)
	}
	return n, err
}

// done reports the final count, so the last line an operator sees is the whole
// figure and not whatever the interval happened to land on.
func (p *fetchProgress) done() {
	p.mu.Lock()
	written, total := p.written, p.total
	p.mu.Unlock()
	p.line(written, total)
}

func (p *fetchProgress) line(written, total int64) {
	if p.out == nil {
		return
	}
	// One line per report, not a carriage return. This output is read from
	// journalctl and from CI logs as often as from a terminal, and a repainted
	// line becomes an unreadable smear in both.
	if total > 0 {
		pct := written * 100 / total
		fmt.Fprintf(p.out, "  downloaded %s of %s (%d%%)\n", humanBytes(written), humanBytes(total), pct)
		return
	}
	fmt.Fprintf(p.out, "  downloaded %s\n", humanBytes(written))
}

// humanBytes is MiB for anything large enough to wait for, and exact bytes
// below that. The exact size is printed on the "fetched" line either way, so
// this one is free to be readable rather than precise.
func humanBytes(n int64) string {
	const mib = 1 << 20
	if n >= mib {
		return fmt.Sprintf("%.1f MiB", float64(n)/float64(mib))
	}
	return fmt.Sprintf("%d bytes", n)
}
