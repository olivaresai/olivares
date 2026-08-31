// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// heartbeatInterval keeps an idle SSE connection alive and lets the server
// detect a dead peer (a failed write returns and ends the stream).
const heartbeatInterval = 25 * time.Second

// streamWriteTimeout bounds a single SSE write. The server's hardened
// WriteTimeout cannot apply to a long-lived stream, so instead each frame arms
// its own finite deadline: a stalled client (full receive window, no reset) makes
// the next write fail after this bound, the loop returns, and the deferred
// unsubscribe frees the goroutine and subscriber slot — rather than pinning them
// indefinitely. It is generous so a merely slow link is not severed.
const streamWriteTimeout = 30 * time.Second

// broker is the in-process pub/sub that fans live snapshots out to connected SSE
// clients. It is single-process by design (v1); a multi-instance deployment
// would fan out over the event bus / NATS, which the bus interface already
// allows without touching subscribers.
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
	ch     chan liveDTO
}

func newBroker() *broker { return &broker{subs: make(map[int]subscriber)} }

// subscribe registers a client for a tenant (and optional session ref) and
// returns its delivery channel plus an idempotent unsubscribe.
func (b *broker) subscribe(tenant model.TenantID, ref string) (<-chan liveDTO, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		ch := make(chan liveDTO)
		close(ch)
		return ch, func() {}
	}
	id := b.next
	b.next++
	ch := make(chan liveDTO, 16)
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
// matching the session filter). Delivery is non-blocking: a slow client that has
// filled its buffer simply misses this update and gets the next one — a live
// view tolerates a dropped intermediate frame, and a slow reader must never
// block the ingestion goroutine.
func (b *broker) publish(s liveSnapshot) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, sub := range b.subs {
		if sub.tenant != s.tenant {
			continue // tenant isolation: never deliver another tenant's operation
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

// close ends every stream and rejects new subscriptions.
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

// handleStream serves the live operation as server-sent events. It clears the
// server's hardened write timeout for the duration of the stream (via the
// ResponseController unwrap the engine exposes), flushes each frame, and
// heartbeats. The subscription is pinned to the request's single authorized
// tenant, so a client only ever sees its own tenant's sessions.
func (m *Module) handleStream(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // ask intermediaries not to buffer

	ref := r.URL.Query().Get("ref")
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
				return // broker closed (module stopping)
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

// writeFrame arms a finite per-write deadline (so a stalled client cannot pin the
// goroutine), writes the frame and flushes it. SetWriteDeadline is best-effort:
// if the wrapped writer does not support it (e.g. a test recorder), the write
// still proceeds under whatever deadline applies.
func writeFrame(rc *http.ResponseController, w io.Writer, frame string) error {
	_ = rc.SetWriteDeadline(time.Now().Add(streamWriteTimeout))
	if _, err := io.WriteString(w, frame); err != nil {
		return err
	}
	return rc.Flush()
}

// auditStreamOpen records that a principal opened a live stream — a privileged
// read of live operation (docs/SECURITY-HARDENING.md). It is best-effort: a failed audit logs
// but does not deny the stream (the per-request RBAC check already gated access).
func (m *Module) auditStreamOpen(r *http.Request, mc api.ModuleContext, ref string) {
	target := model.ID("")
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		_, e := sc.Audit().Append(r.Context(), model.AuditDraft{
			Actor: mc.Principal.Actor(), ActorKind: mc.Principal.ActorKind(),
			Action: "sessions.stream.open", TargetKind: liveKind, TargetID: target,
			Meta: streamMeta(ref),
		})
		return e
	})
	if err != nil {
		m.debugf("sessions: stream-open audit failed", "err", err)
	}
}

// streamMeta builds the non-sensitive audit meta for a stream open.
func streamMeta(ref string) map[string]any {
	if ref == "" {
		return map[string]any{"scope": "all"}
	}
	return map[string]any{"session_ref": ref}
}

// debugf logs at debug level if a logger is set.
func (m *Module) debugf(msg string, args ...any) {
	if m.log != nil {
		m.log.Debug(msg, args...)
	}
}
