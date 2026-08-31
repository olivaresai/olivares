// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"sync"
	"time"
)

// The observe overlay's broker (stream.go) is DROP-ON-FULL: a live view tolerates
// losing an intermediate snapshot. That is WRONG for an operated session, whose
// output is a SEQUENTIAL byte stream — a silently dropped frame corrupts it. So
// the attach path uses a sequenced, bounded ring buffer read by CURSOR:
//
//   - every output frame gets a monotonic sequence number and is appended to a
//     per-run ring bounded in both frame-count and bytes;
//   - an attach client reads from a cursor (?from=<seq>): it replays the buffered
//     tail from its cursor, then follows live frames;
//   - BACKPRESSURE is honest: a slow client never has a frame dropped silently —
//     the producer never blocks on it (it appends to the ring and evicts the
//     OLDEST), and if the client falls so far behind that the ring evicted frames
//     below its cursor, it receives an explicit GAP marker and resyncs from the
//     ring's current floor. Memory is bounded regardless of client speed.

const (
	// defaultRingFrames bounds a run's buffered frame count.
	defaultRingFrames = 4096
	// defaultRingBytes bounds a run's buffered output bytes (8 MiB).
	defaultRingBytes = 8 << 20
)

// seqFrame is one sequenced output frame in the ring.
type seqFrame struct {
	Seq    int64
	Stream string
	Data   []byte
	At     time.Time
}

// outputRing is a per-run bounded, sequenced output buffer with a cursor read
// API and a wake signal. It is the single source of truth for attach replay.
type outputRing struct {
	mu       sync.Mutex
	frames   []seqFrame
	maxCount int
	maxBytes int
	curBytes int
	firstSeq int64 // seq of frames[0]; 0 when empty
	nextSeq  int64 // next seq to assign (starts at 1)
	closed   bool
	notify   chan struct{} // closed+replaced on every append/close to wake readers
}

// newOutputRing builds a ring with the given (count, bytes) bounds.
func newOutputRing(maxCount, maxBytes int) *outputRing {
	if maxCount <= 0 {
		maxCount = defaultRingFrames
	}
	if maxBytes <= 0 {
		maxBytes = defaultRingBytes
	}
	return &outputRing{
		maxCount: maxCount,
		maxBytes: maxBytes,
		nextSeq:  1,
		notify:   make(chan struct{}),
	}
}

// append seals a frame with the next sequence number, appends it, evicts the
// oldest frames while over either bound, and wakes readers. It NEVER blocks on a
// consumer (the deny-of-silent-loss is handled at read time via the gap marker).
// Returns the assigned sequence number.
func (r *outputRing) append(stream string, data []byte, at time.Time) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	seq := r.nextSeq
	r.nextSeq++
	f := seqFrame{Seq: seq, Stream: stream, Data: data, At: at}
	if len(r.frames) == 0 {
		r.firstSeq = seq
	}
	r.frames = append(r.frames, f)
	r.curBytes += len(data)
	r.evictLocked()
	r.wakeLocked()
	return seq
}

// evictLocked drops the oldest frames while the ring exceeds either bound. It
// always keeps at least one frame (the just-appended one) so a single oversized
// frame is retained rather than vanishing.
func (r *outputRing) evictLocked() {
	for len(r.frames) > 1 && (len(r.frames) > r.maxCount || r.curBytes > r.maxBytes) {
		r.curBytes -= len(r.frames[0].Data)
		r.frames = r.frames[1:]
		r.firstSeq = r.frames[0].Seq
	}
}

// close marks the stream ended and wakes readers (so they emit the tail + an end).
func (r *outputRing) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
	r.wakeLocked()
}

// wait returns a channel that is closed on the next append or close. Callers
// SNAPSHOT this BEFORE reading, so an append racing between read and select is
// not a lost wake-up.
func (r *outputRing) wait() <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.notify
}

func (r *outputRing) wakeLocked() {
	close(r.notify)
	r.notify = make(chan struct{})
}

// ringRead is the result of a cursor read.
type ringRead struct {
	frames  []seqFrame // a COPY (safe to use after the lock is released)
	next    int64      // the cursor to use on the next read
	gap     bool       // true if frames below the cursor were evicted (honest loss)
	dropped int64      // how many frames were evicted past the cursor (when gap)
	closed  bool       // the stream has ended (no more frames will ever arrive)
}

// readFrom returns the buffered frames with Seq >= cursor (a copy), the next
// cursor, and whether an eviction created a gap below the cursor. A live stream
// is never corrupted silently: a gap is reported explicitly.
func (r *outputRing) readFrom(cursor int64) ringRead {
	r.mu.Lock()
	defer r.mu.Unlock()
	res := ringRead{next: cursor, closed: r.closed}
	if len(r.frames) == 0 {
		res.next = r.nextSeq
		return res
	}
	start := cursor
	if cursor < r.firstSeq {
		// Below the retained floor: serve from the floor. A FRESH attach (from=0)
		// is not a lag — only an explicit resume cursor (>=1, the first real seq)
		// that fell below the floor missed evicted frames and is an honest gap.
		if cursor >= 1 {
			res.gap = true
			res.dropped = r.firstSeq - cursor
		}
		start = r.firstSeq
	}
	for _, f := range r.frames {
		if f.Seq >= start {
			res.frames = append(res.frames, f)
		}
	}
	res.next = r.nextSeq
	return res
}
