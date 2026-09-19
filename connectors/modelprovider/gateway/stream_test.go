// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"bufio"
	"context"
	"io"
	"strings"
	"sync/atomic"
	"testing"
)

type countingBody struct {
	io.Reader
	closed atomic.Bool
}

func (b *countingBody) Close() error { b.closed.Store(true); return nil }

// TestPullStreamDoesNotReadAheadOfTheConsumer pins the backpressure contract at
// the seam where it is observable.
//
// The conformance case cannot see it: its fake holds the second upstream write
// behind a channel the test itself closes only AFTER asserting, so the write
// counter can never be above one and the assertion never fires. A driver that
// reads ahead in the background passes it. What backpressure means here is that
// the stream pulls exactly one upstream event per Recv and none before the
// first, so the parser call count is the thing to count.
func TestPullStreamDoesNotReadAheadOfTheConsumer(t *testing.T) {
	const lines = "a\nb\nc\n"
	body := &countingBody{Reader: strings.NewReader(lines)}
	var parses atomic.Int32
	parse := func(r *bufio.Reader) (Event, error) {
		parses.Add(1)
		line, err := r.ReadString('\n')
		if err != nil {
			return Event{}, io.EOF
		}
		return Event{Type: EventTextDelta, Text: strings.TrimSpace(line)}, nil
	}
	s := newPullStream(context.Background(), body, parse, NopCostHook{}, Reservation{Allowed: true})
	defer func() { _ = s.Close() }()

	if n := parses.Load(); n != 0 {
		t.Fatalf("parser calls before the first Recv = %d, want 0 (the stream must not prefetch)", n)
	}
	for i, want := range []string{"a", "b", "c"} {
		ev, err := s.Recv()
		if err != nil {
			t.Fatalf("Recv %d: %v", i, err)
		}
		if ev.Text != want {
			t.Fatalf("Recv %d text = %q, want %q", i, ev.Text, want)
		}
		if n := parses.Load(); n != int32(i+1) {
			t.Fatalf("after %d Recv calls the parser ran %d times, want %d (one upstream event per pull)", i+1, n, i+1)
		}
	}
	if _, err := s.Recv(); err != io.EOF {
		t.Fatalf("final Recv = %v, want io.EOF", err)
	}
}

// TestPullStreamCloseClosesTheBody pins that the caller's Close reaches the
// upstream body, which is what frees the connection when a consumer gives up
// part-way through a stream.
func TestPullStreamCloseClosesTheBody(t *testing.T) {
	body := &countingBody{Reader: strings.NewReader("a\nb\n")}
	parse := func(r *bufio.Reader) (Event, error) {
		line, err := r.ReadString('\n')
		if err != nil {
			return Event{}, io.EOF
		}
		return Event{Type: EventTextDelta, Text: strings.TrimSpace(line)}, nil
	}
	s := newPullStream(context.Background(), body, parse, NopCostHook{}, Reservation{Allowed: true})
	if _, err := s.Recv(); err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if body.closed.Load() {
		t.Fatal("body closed while the stream was still open")
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !body.closed.Load() {
		t.Fatal("Close did not close the upstream body")
	}
}
