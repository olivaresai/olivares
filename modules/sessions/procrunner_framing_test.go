// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"testing"
)

type framingCountReader struct {
	io.Reader
	read int
}

func (r *framingCountReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.read += n
	return n, err
}

func TestNativeOutputFramingRejectsExcessBeforeReadingTheWholeLine(t *testing.T) {
	for _, tail := range []string{"\n", ""} {
		t.Run(map[bool]string{true: "newline", false: "unterminated"}[tail != ""], func(t *testing.T) {
			r := &framingCountReader{Reader: strings.NewReader(strings.Repeat("x", 1<<20) + tail)}
			br := bufio.NewReaderSize(r, 32)
			line, err := readBoundedLine(br, 64)
			if err == nil || errors.Is(err, io.EOF) || len(line) != 0 {
				t.Fatalf("excess returned %d bytes, error %v", len(line), err)
			}
			if r.read > 64+2+br.Size() {
				t.Fatalf("read %d bytes before refusing a 64-byte frame", r.read)
			}
		})
	}
}

func TestNativeOutputFramingCeilingAndCompletion(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
		eof, excess       bool
	}{
		{"below", "abc\n", "abc", false, false},
		{"exact", "abcd\n", "abcd", false, false},
		{"CRLF", "abcd\r\n", "abcd", false, false},
		{"EOF", "abcd", "abcd", true, false},
		{"empty EOF", "", "", true, false},
		{"excess", "abcde\n", "", false, true},
		{"excess EOF", "abcde", "", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line, err := readBoundedLine(bufio.NewReaderSize(strings.NewReader(tc.input), 16), 4)
			if cap(line) > 6 {
				t.Fatalf("retained capacity = %d, want at most 6", cap(line))
			}
			if string(line) != tc.want {
				t.Fatalf("line %q, want %q", line, tc.want)
			}
			if tc.excess {
				if err == nil || errors.Is(err, io.EOF) {
					t.Fatalf("excess error = %v", err)
				}
			} else if tc.eof {
				if !errors.Is(err, io.EOF) {
					t.Fatalf("EOF error = %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

type framingErrorReader struct {
	sent bool
	err  error
}

func (r *framingErrorReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, "abc"), r.err
	}
	return 0, r.err
}
func TestNativeOutputFramingPreservesReaderFailure(t *testing.T) {
	failure := errors.New("fixture read failure")
	line, err := readBoundedLine(bufio.NewReader(&framingErrorReader{err: failure}), 4)
	if !errors.Is(err, failure) {
		t.Fatalf("error = %v", err)
	}
	if len(line) != 0 {
		t.Fatalf("incomplete failed read returned a protocol frame %q", line)
	}
}

func TestNativeOutputFramingAcrossBufferFragments(t *testing.T) {
	for _, n := range []int{63, 64, 65} {
		for _, ending := range []string{"\n", "\r\n", ""} {
			line, err := readBoundedLine(bufio.NewReaderSize(strings.NewReader(strings.Repeat("x", n)+ending), 16), 64)
			if n > 64 {
				if len(line) != 0 || !errors.Is(err, ErrOutputLineTooLong) {
					t.Fatalf("excess returned %d bytes, %v", len(line), err)
				}
			} else {
				if string(line) != strings.Repeat("x", n) || cap(line) > 66 {
					t.Fatalf("fragmented frame length/capacity = %d/%d", len(line), cap(line))
				}
				if ending == "" {
					if !errors.Is(err, io.EOF) {
						t.Fatal(err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

func TestNativeOutputFramingStopKeepsTheCauseWhenDeliveryWasBlocked(t *testing.T) {
	p := &procProcess{waitErr: errors.Join(ErrOutputAbandoned, ErrOutputLineTooLong)}
	p.abandonedFrames.Store(1)
	err := p.forcedTeardownReport()
	if !errors.Is(err, ErrOutputLineTooLong) || !errors.Is(err, ErrOutputAbandoned) {
		t.Fatalf("blocked delivery erased the framing cause: %v", err)
	}
}
