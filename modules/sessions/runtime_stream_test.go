// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"testing"
	"time"
)

func TestOutputRing_SequenceAndReplay(t *testing.T) {
	t.Parallel()

	r := newOutputRing(100, 1<<20)
	at := time.Now()
	for i := 0; i < 5; i++ {
		seq := r.append(streamStdout, []byte("line"), at)
		if want := int64(i + 1); seq != want {
			t.Fatalf("append %d: seq=%d want %d", i, seq, want)
		}
	}
	// Replay from the start.
	rd := r.readFrom(0)
	if rd.gap {
		t.Fatalf("unexpected gap reading from 0 with no eviction")
	}
	if len(rd.frames) != 5 {
		t.Fatalf("readFrom(0): got %d frames want 5", len(rd.frames))
	}
	if rd.next != 6 {
		t.Fatalf("next cursor = %d want 6", rd.next)
	}
	// Replay from a mid cursor.
	rd = r.readFrom(4)
	if len(rd.frames) != 2 || rd.frames[0].Seq != 4 {
		t.Fatalf("readFrom(4): got %d frames starting at %d", len(rd.frames), seqOf(rd))
	}
}

func TestOutputRing_EvictionReportsGap(t *testing.T) {
	t.Parallel()

	r := newOutputRing(3, 1<<20) // keep at most 3 frames
	at := time.Now()
	for i := 0; i < 6; i++ {
		r.append(streamStdout, []byte("x"), at)
	}
	// firstSeq should have advanced past the evicted frames.
	rd := r.readFrom(1) // cursor below the floor → gap
	if !rd.gap {
		t.Fatalf("expected a gap when reading below the evicted floor")
	}
	if rd.dropped == 0 {
		t.Fatalf("expected a non-zero dropped count")
	}
	// Only the last 3 frames remain.
	if len(rd.frames) != 3 {
		t.Fatalf("expected 3 retained frames, got %d", len(rd.frames))
	}
	if rd.frames[0].Seq != 4 {
		t.Fatalf("retained floor seq = %d want 4", rd.frames[0].Seq)
	}
	// A cursor at/above the floor sees no gap.
	if r.readFrom(4).gap {
		t.Fatalf("reading from the floor must not report a gap")
	}
}

func TestOutputRing_WaitWakesOnAppend(t *testing.T) {
	t.Parallel()

	r := newOutputRing(10, 1<<20)
	wake := r.wait()
	done := make(chan struct{})
	go func() {
		<-wake
		close(done)
	}()
	r.append(streamStdout, []byte("hi"), time.Now())
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("wait() was not woken by append")
	}
}

func TestOutputRing_CloseSignalsClosed(t *testing.T) {
	t.Parallel()

	r := newOutputRing(10, 1<<20)
	r.append(streamStdout, []byte("a"), time.Now())
	r.close()
	rd := r.readFrom(0)
	if !rd.closed {
		t.Fatalf("readFrom after close must report closed")
	}
	if len(rd.frames) != 1 {
		t.Fatalf("closed ring still replays its buffered tail (got %d)", len(rd.frames))
	}
}

func TestOutputRing_KeepsOversizeSingleFrame(t *testing.T) {
	t.Parallel()

	r := newOutputRing(10, 4) // 4-byte budget
	r.append(streamStdout, []byte("way-too-big-frame"), time.Now())
	rd := r.readFrom(0)
	if len(rd.frames) != 1 {
		t.Fatalf("a single oversize frame must be retained, got %d", len(rd.frames))
	}
}

func seqOf(rd ringRead) int64 {
	if len(rd.frames) == 0 {
		return -1
	}
	return rd.frames[0].Seq
}

func TestParseStreamJSON(t *testing.T) {
	t.Parallel()

	cases := []struct {
		line   string
		ok     bool
		isInit bool
		sid    string
	}{
		{`{"type":"system","subtype":"init","session_id":"abc"}`, true, true, "abc"},
		{`{"type":"assistant","text":"hi"}`, true, false, ""},
		{`not json at all`, false, false, ""},
		{`{"type":"system","subtype":"init"}`, true, false, ""}, // no session id ⇒ not a usable init
	}
	for _, c := range cases {
		f, ok := parseStreamJSON([]byte(c.line))
		if ok != c.ok {
			t.Fatalf("%q: ok=%v want %v", c.line, ok, c.ok)
		}
		if ok {
			if f.isInit() != c.isInit {
				t.Fatalf("%q: isInit=%v want %v", c.line, f.isInit(), c.isInit)
			}
			if f.SessionID != c.sid {
				t.Fatalf("%q: sid=%q want %q", c.line, f.SessionID, c.sid)
			}
		}
	}
}
