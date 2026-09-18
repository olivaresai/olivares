// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package cliruntime

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

// Pump is a live child process with multiplexed output. The sessions PTY
// runner adapts its Process to this interface.
type Pump interface {
	Send(ctx context.Context, line []byte) error
	Frames() <-chan Frame
	Wait() (int, error)
	Stop(ctx context.Context) error
	PID() int
}

// HostedMeta is the identity of one hosted generation.
type HostedMeta struct {
	Ref            string
	Kind           string
	ConversationID string
	Generation     int64
}

// Hosted wraps a Pump with sequenced attach/reconnect.
type Hosted struct {
	pump Pump
	meta HostedMeta
	ring *seqRing

	mu     sync.Mutex
	gone   bool
	result Result
	waitCh chan struct{}
}

// Host starts consuming pump.Frames into a sequenced ring and returns a Session.
func Host(pump Pump, meta HostedMeta) *Hosted {
	h := &Hosted{
		pump:   pump,
		meta:   meta,
		ring:   newSeqRing(4096, 8<<20),
		waitCh: make(chan struct{}),
		result: Result{Generation: meta.Generation},
	}
	go h.consume()
	return h
}

func (h *Hosted) consume() {
	for f := range h.pump.Frames() {
		if h.meta.ConversationID == "" {
			if id := sessionIDFromFrame(f.Data); id != "" {
				h.mu.Lock()
				if h.meta.ConversationID == "" {
					h.meta.ConversationID = id
				}
				h.mu.Unlock()
			}
		}
		stream := f.Stream
		if stream == "" {
			stream = StreamStdout
		}
		h.ring.append(stream, f.Data, time.Now())
	}
	code, err := h.pump.Wait()
	h.mu.Lock()
	h.gone = true
	// ⛔ A WAIT THAT FAILED IS NOT AN EXIT, and `code != 0` is exactly the term
	// that used to turn one into the other. The pump answers (-1, err) when it
	// could not classify the child's outcome at all — it was not reaped — and
	// (code, nil) for every outcome it did classify, a signal included (Go
	// reports a signaled child as -1). So the previous `err == nil || code != 0`
	// reported ProcessExited true for the ONE case the contract says must report
	// false, and handed the operator a -1 that no child ever returned.
	h.result = Result{ExitCode: code, ProcessExited: err == nil, Generation: h.meta.Generation}
	h.mu.Unlock()
	h.ring.close()
	close(h.waitCh)
}

func (h *Hosted) Ref() string { return h.meta.Ref }
func (h *Hosted) ConversationID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.meta.ConversationID
}
func (h *Hosted) Generation() int64 { return h.meta.Generation }
func (h *Hosted) PID() int          { return h.pump.PID() }

func (h *Hosted) Send(ctx context.Context, line []byte) error {
	h.mu.Lock()
	gone := h.gone
	h.mu.Unlock()
	if gone {
		return ErrProcessGone
	}
	return h.pump.Send(ctx, line)
}

func (h *Hosted) Attach(fromSeq int64) (<-chan Frame, func()) {
	stop := make(chan struct{})
	var once sync.Once
	cancel := func() { once.Do(func() { close(stop) }) }
	return followRing(h.ring, fromSeq, stop), cancel
}

func (h *Hosted) Reconnect(fromSeq int64) (<-chan Frame, func(), error) {
	h.mu.Lock()
	gone := h.gone
	h.mu.Unlock()
	if gone {
		return nil, func() {}, ErrProcessGone
	}
	ch, cancel := h.Attach(fromSeq)
	return ch, cancel, nil
}

func (h *Hosted) Stop(ctx context.Context) (Result, error) {
	err := h.pump.Stop(ctx)
	<-h.waitCh
	h.mu.Lock()
	res := h.result
	h.mu.Unlock()
	if err != nil && !res.ProcessExited {
		return res, err
	}
	return res, nil
}

func (h *Hosted) Wait() (Result, error) {
	<-h.waitCh
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.result, nil
}

func sessionIDFromFrame(data []byte) string {
	var obj struct {
		SessionID string `json:"session_id"`
		ThreadID  string `json:"thread_id"`
		ID        string `json:"id"`
		Type      string `json:"type"`
		Subtype   string `json:"subtype"`
		Result    struct {
			ThreadID string `json:"thread_id"`
		} `json:"result"`
	}
	if json.Unmarshal(data, &obj) != nil {
		return ""
	}
	if obj.SessionID != "" {
		return obj.SessionID
	}
	if obj.ThreadID != "" {
		return obj.ThreadID
	}
	if obj.Result.ThreadID != "" {
		return obj.Result.ThreadID
	}
	return ""
}

var _ Session = (*Hosted)(nil)
