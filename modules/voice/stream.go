// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package voice

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const heartbeatInterval = 25 * time.Second
const streamWriteTimeout = 30 * time.Second

// sessSnapshot is one session-metadata update fanned out to connected clients.
type sessSnapshot struct {
	tenant model.TenantID
	dto    sessionDTO
}

// broker is the in-process pub/sub that fans session-metadata snapshots to connected
// SSE clients (single-process by design, v1).
type broker struct {
	mu     sync.Mutex
	next   int
	subs   map[int]subscriber
	closed bool
}

// subscriber is one connected client: its authorized tenant, an optional single
// session filter, and a buffered delivery channel.
type subscriber struct {
	tenant model.TenantID
	ref    string // "" = every session in the tenant
	ch     chan sessionDTO
}

func newBroker() *broker { return &broker{subs: make(map[int]subscriber)} }

func (b *broker) subscribe(tenant model.TenantID, ref string) (<-chan sessionDTO, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		ch := make(chan sessionDTO)
		close(ch)
		return ch, func() {}
	}
	id := b.next
	b.next++
	ch := make(chan sessionDTO, 16)
	b.subs[id] = subscriber{tenant: tenant, ref: ref, ch: ch}
	return ch, func() { b.unsubscribe(id) }
}

func (b *broker) unsubscribe(id int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if s, ok := b.subs[id]; ok {
		delete(b.subs, id)
		close(s.ch)
	}
}

// publish delivers a snapshot to every subscriber authorized for its tenant (and
// matching the session filter). Non-blocking: a slow client misses a frame rather
// than blocking the ingestion goroutine.
func (b *broker) publish(s sessSnapshot) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, sub := range b.subs {
		if sub.tenant != s.tenant {
			continue // tenant isolation: never deliver another tenant's session
		}
		if sub.ref != "" && sub.ref != s.dto.SessionRef {
			continue
		}
		select {
		case sub.ch <- s.dto:
		default:
		}
	}
}

func (b *broker) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for id, s := range b.subs {
		delete(b.subs, id)
		close(s.ch)
	}
}

// handleStream serves one session's live metadata as server-sent events, pinned to
// the request's single authorized tenant. The stream open is a privileged read and
// is audited.
func (m *Module) handleStream(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	ref := chi.URLParam(r, "ref")
	if ref == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("session ref required"))
		return
	}
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	m.auditStreamOpen(r, mc, ref)

	ch, cancel := m.broker.subscribe(mc.Tenant, ref)
	defer cancel()

	if writeFrame(rc, w, ": connected\n\n") != nil {
		return
	}
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case dto, ok := <-ch:
			if !ok {
				return
			}
			payload, err := json.Marshal(dto)
			if err != nil {
				continue
			}
			if writeFrame(rc, w, fmt.Sprintf("event: session\ndata: %s\n\n", payload)) != nil {
				return
			}
		case <-ticker.C:
			if writeFrame(rc, w, ": ping\n\n") != nil {
				return
			}
		}
	}
}

func writeFrame(rc *http.ResponseController, w io.Writer, frame string) error {
	_ = rc.SetWriteDeadline(time.Now().Add(streamWriteTimeout))
	if _, err := io.WriteString(w, frame); err != nil {
		return err
	}
	return rc.Flush()
}

// auditStreamOpen records that a principal opened a session metadata stream — a
// privileged read (docs/SECURITY-HARDENING.md). Best-effort.
func (m *Module) auditStreamOpen(r *http.Request, mc api.ModuleContext, ref string) {
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		_, e := sc.Audit().Append(r.Context(), model.AuditDraft{
			Actor: mc.Principal.Actor(), ActorKind: mc.Principal.ActorKind(),
			Action: "voice.stream.open", TargetKind: sessionKind, Meta: map[string]any{"session_ref": clamp(ref, maxRefLen)},
		})
		return e
	})
	if err != nil {
		m.debugf("voice: stream-open audit failed", "err", err)
	}
}
