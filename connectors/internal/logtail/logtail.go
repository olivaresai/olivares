// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package logtail reads an append-only audit log file line by line, read-only,
// for the data-store connectors that tail a growing log (pg-audit jsonlog,
// mysql-audit). It is deliberately minimal and safe by construction:
//
//   - It opens the file O_RDONLY and never writes to it (docs/SECURITY-HARDENING.md: the
//     collector is read-only over its sources and never modifies the host).
//   - In batch mode it reads to EOF and returns (the natural model for a
//     completed, rotated log file or a test fixture).
//   - In follow mode it blocks, polling for appended lines and surviving log
//     rotation (a new file at the same path) and truncation (copytruncate), until
//     the context is canceled.
//
// A line is delivered without its trailing newline (and without a trailing
// carriage return); blank lines are skipped. An incomplete trailing line (bytes
// after the last newline) is held back in follow mode until its newline arrives,
// so a record is never split mid-write; in batch mode it is delivered as the
// final line at EOF.
package logtail

import (
	"bytes"
	"context"
	"io"
	"os"
	"time"
)

// DefaultPollInterval is the wait between end-of-file polls in follow mode.
const DefaultPollInterval = time.Second

// Options configures a Tail run.
type Options struct {
	// Follow keeps reading appended lines after EOF (tail -f). When false, Tail
	// returns nil once the current end of file is reached.
	Follow bool
	// PollInterval is the wait between EOF polls in follow mode. Zero means
	// DefaultPollInterval.
	PollInterval time.Duration
}

// LineFunc receives one complete log line (no trailing newline). Returning a
// non-nil error stops the tail and is propagated out of Tail.
type LineFunc func(line []byte) error

// Tail reads the file at path line by line and calls fn for each non-blank line.
//
// In batch mode (opts.Follow == false) it returns nil at end of file. In follow
// mode it blocks until ctx is done — returning ctx.Err() — or until fn returns an
// error, polling for appended data and re-opening the path on rotation or
// truncation. The file is opened read-only and is never modified.
func Tail(ctx context.Context, path string, opts Options, fn LineFunc) error {
	if opts.PollInterval <= 0 {
		opts.PollInterval = DefaultPollInterval
	}

	f, err := os.Open(path) // read-only
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	var (
		offset  int64  // bytes consumed from the current file
		pending []byte // incomplete trailing line, awaiting its newline
	)

	// deliver splits buf into complete lines, calls fn for each non-blank line,
	// and returns any incomplete trailing remainder.
	deliver := func(buf []byte) ([]byte, error) {
		for {
			i := bytes.IndexByte(buf, '\n')
			if i < 0 {
				return buf, nil
			}
			line := bytes.TrimSuffix(buf[:i], []byte{'\r'})
			buf = buf[i+1:]
			if len(line) == 0 {
				continue // skip blank lines
			}
			if err := fn(line); err != nil {
				return nil, err
			}
		}
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		fi, err := f.Stat()
		if err != nil {
			return err
		}
		size := fi.Size()

		switch {
		case size < offset:
			// Truncated in place (copytruncate) — restart from the beginning of
			// the same fd. Any held partial line is from the old content; drop it.
			offset, pending = 0, nil
			continue

		case size > offset:
			buf := make([]byte, size-offset)
			n, rerr := f.ReadAt(buf, offset)
			if n > 0 {
				offset += int64(n)
				rest, derr := deliver(append(pending, buf[:n]...))
				if derr != nil {
					return derr
				}
				pending = append(pending[:0:0], rest...) // own the bytes
			}
			if rerr != nil && rerr != io.EOF {
				return rerr
			}
			continue
		}

		// size == offset: caught up to end of file.
		if !opts.Follow {
			if len(pending) > 0 {
				// Deliver the final, newline-less line.
				if err := emitFinal(pending, fn); err != nil {
					return err
				}
			}
			return nil
		}

		// Follow mode: detect rotation (a different file now lives at path), then
		// wait before polling again.
		rotated, rerr := rotatedAway(path, fi)
		if rerr == nil && rotated {
			if len(pending) > 0 {
				if err := emitFinal(pending, fn); err != nil {
					return err
				}
			}
			nf, oerr := os.Open(path)
			if oerr != nil {
				// New file not readable yet; try again next poll on the old fd.
				if werr := wait(ctx, opts.PollInterval); werr != nil {
					return werr
				}
				continue
			}
			_ = f.Close()
			f = nf
			offset, pending = 0, nil
			continue
		}
		if err := wait(ctx, opts.PollInterval); err != nil {
			return err
		}
	}
}

// emitFinal delivers a trailing line that has no newline (trimming a stray CR and
// skipping it if blank).
func emitFinal(pending []byte, fn LineFunc) error {
	line := bytes.TrimSuffix(pending, []byte{'\r'})
	if len(line) == 0 {
		return nil
	}
	return fn(line)
}

// rotatedAway reports whether the path now resolves to a different file than the
// open fd described by cur (log rotation by rename + recreate).
func rotatedAway(path string, cur os.FileInfo) (bool, error) {
	pfi, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return !os.SameFile(cur, pfi), nil
}

// wait sleeps for d, returning early with ctx.Err() if the context is canceled.
func wait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
