// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

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

// heartbeatInterval keeps an idle SSE connection alive and lets the server detect
// a dead peer (a failed write returns and ends the stream).
const heartbeatInterval = 25 * time.Second

// streamWriteTimeout bounds a single SSE write so a stalled client cannot pin the
// goroutine and subscriber slot indefinitely (the server's hardened WriteTimeout
// cannot apply to a long-lived stream). It is generous so a merely slow link is
// not severed.
const streamWriteTimeout = 30 * time.Second

// relSnapshot is one relation update fanned out to connected clients: the
// authorized tenant plus the projected edge.
type relSnapshot struct {
	tenant model.TenantID
	dto    edgeDTO
}

// broker is the in-process pub/sub that fans relation snapshots to connected SSE
// clients. Single-process by design (v1); a multi-instance deployment would fan
// out over the event bus / NATS, which the bus interface already allows.
type broker struct {
	mu     sync.Mutex
	next   int
	subs   map[int]subscriber
	closed bool
}

// subscriber is one connected client: its authorized tenant, an optional single
// node filter, and a buffered delivery channel.
type subscriber struct {
	tenant model.TenantID
	node   string // "" = every relation in the tenant
	ch     chan edgeDTO
}

func newBroker() *broker { return &broker{subs: make(map[int]subscriber)} }

// subscribe registers a client for a tenant (and optional node ref) and returns
// its delivery channel plus an idempotent unsubscribe.
func (b *broker) subscribe(tenant model.TenantID, node string) (<-chan edgeDTO, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		ch := make(chan edgeDTO)
		close(ch)
		return ch, func() {}
	}
	id := b.next
	b.next++
	ch := make(chan edgeDTO, 16)
	b.subs[id] = subscriber{tenant: tenant, node: node, ch: ch}
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
// matching the node filter). Delivery is non-blocking: a slow client that filled
// its buffer simply misses this update and gets the next one — a live view
// tolerates a dropped intermediate frame, and a slow reader must never block the
// ingestion goroutine.
func (b *broker) publish(s relSnapshot) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, sub := range b.subs {
		if sub.tenant != s.tenant {
			continue // tenant isolation: never deliver another tenant's relations
		}
		if sub.node != "" && sub.node != s.dto.Source && sub.node != s.dto.Target {
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

// handleStream serves the live comm graph as server-sent events, pinned to the
// request's single authorized tenant so a client only ever sees its own tenant's
// relations. The stream open is a privileged read and is audited.
func (m *Module) handleStream(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // ask intermediaries not to buffer

	node := r.URL.Query().Get("node")
	m.auditStreamOpen(r, mc, node)

	ch, cancel := m.broker.subscribe(mc.Tenant, node)
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
			if writeFrame(rc, w, fmt.Sprintf("event: relation\ndata: %s\n\n", payload)) != nil {
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
// goroutine), writes the frame and flushes it. SetWriteDeadline is best-effort.
func writeFrame(rc *http.ResponseController, w io.Writer, frame string) error {
	_ = rc.SetWriteDeadline(time.Now().Add(streamWriteTimeout))
	if _, err := io.WriteString(w, frame); err != nil {
		return err
	}
	return rc.Flush()
}

// auditStreamOpen records that a principal opened the live comm-graph stream — a
// privileged read (docs/SECURITY-HARDENING.md). Best-effort: a failed audit logs but does not deny
// the stream (the per-request RBAC check already gated access).
func (m *Module) auditStreamOpen(r *http.Request, mc api.ModuleContext, node string) {
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		meta := map[string]any{"scope": "all"}
		if node != "" {
			meta = map[string]any{"node": clamp(node, maxRefLen)}
		}
		_, e := sc.Audit().Append(r.Context(), model.AuditDraft{
			Actor: mc.Principal.Actor(), ActorKind: mc.Principal.ActorKind(),
			Action: "orchestration.stream.open", TargetKind: relationKind, Meta: meta,
		})
		return e
	})
	if err != nil {
		m.debugf("orchestration: stream-open audit failed", "err", err)
	}
}
