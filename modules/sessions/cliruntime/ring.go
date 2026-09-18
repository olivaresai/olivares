// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package cliruntime

import (
	"sync"
	"time"
)

// seqRing is a bounded, sequenced output buffer. Attach reads by cursor.
// A slow subscriber never drops a frame silently: eviction below the cursor
// is reported as a gap on the next read.
type seqRing struct {
	mu       sync.Mutex
	frames   []Frame
	maxCount int
	maxBytes int
	curBytes int
	firstSeq int64
	nextSeq  int64
	closed   bool
	notify   chan struct{}
}

func newSeqRing(maxCount, maxBytes int) *seqRing {
	if maxCount <= 0 {
		maxCount = 4096
	}
	if maxBytes <= 0 {
		maxBytes = 8 << 20
	}
	return &seqRing{
		maxCount: maxCount,
		maxBytes: maxBytes,
		nextSeq:  1,
		notify:   make(chan struct{}),
	}
}

func (r *seqRing) append(stream string, data []byte, at time.Time) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	seq := r.nextSeq
	r.nextSeq++
	f := Frame{Seq: seq, Stream: stream, Data: append([]byte(nil), data...), At: at}
	if len(r.frames) == 0 {
		r.firstSeq = seq
	}
	r.frames = append(r.frames, f)
	r.curBytes += len(data)
	for len(r.frames) > 1 && (len(r.frames) > r.maxCount || r.curBytes > r.maxBytes) {
		r.curBytes -= len(r.frames[0].Data)
		r.frames = r.frames[1:]
		r.firstSeq = r.frames[0].Seq
	}
	close(r.notify)
	r.notify = make(chan struct{})
	return seq
}

func (r *seqRing) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
	close(r.notify)
	r.notify = make(chan struct{})
}

func (r *seqRing) wait() <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.notify
}

type ringRead struct {
	frames  []Frame
	next    int64
	gap     bool
	dropped int64
	closed  bool
}

func (r *seqRing) readFrom(cursor int64) ringRead {
	r.mu.Lock()
	defer r.mu.Unlock()
	res := ringRead{next: cursor, closed: r.closed}
	if len(r.frames) == 0 {
		res.next = r.nextSeq
		return res
	}
	start := cursor
	if cursor < r.firstSeq {
		if cursor >= 1 {
			res.gap = true
			res.dropped = r.firstSeq - cursor
		}
		start = r.firstSeq
	}
	for _, f := range r.frames {
		if f.Seq >= start {
			cp := f
			cp.Data = append([]byte(nil), f.Data...)
			res.frames = append(res.frames, cp)
		}
	}
	res.next = r.nextSeq
	return res
}

func followRing(ring *seqRing, fromSeq int64, stop <-chan struct{}) <-chan Frame {
	ch := make(chan Frame, 64)
	go func() {
		defer close(ch)
		cursor := fromSeq
		for {
			wake := ring.wait()
			rd := ring.readFrom(cursor)
			for _, f := range rd.frames {
				select {
				case ch <- f:
				case <-stop:
					return
				}
			}
			cursor = rd.next
			if rd.closed {
				return
			}
			select {
			case <-stop:
				return
			case <-wake:
			}
		}
	}()
	return ch
}
