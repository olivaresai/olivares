// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package replay drops repeated webhook deliveries by the sender's delivery ID
// (GitHub X-GitHub-Delivery, GitLab webhook-id / Idempotency-Key). A receiver
// calls Once only after the delivery authenticated, so an unauthenticated
// request can never use up an ID.
//
// It filters the sender's redeliveries. Where the ID is not signed (GitHub,
// GitLab without a signing token) a holder of a captured delivery can change
// the ID, so it is not replay protection on its own; GitLab's signed webhook-id
// with its timestamp window is.
//
// ponytail: in memory, per receiver, the newest Capacity IDs; after a restart or
// once an ID is older than that, a redelivery is processed again. Upgrade to a
// store row when a replay across restarts matters.
package replay

import (
	"crypto/sha256"
	"net/http"
	"sync"
)

// Capacity is how many recent delivery IDs a Guard remembers.
const Capacity = 4096

type key [sha256.Size]byte

type entry struct {
	slot int
	done bool // false while the first delivery is still being served
}

// Guard remembers the newest Capacity delivery IDs.
type Guard struct {
	mu   sync.Mutex
	seen map[key]entry
	ring []key
	next int
}

// New returns an empty Guard.
func New() *Guard {
	return &Guard{seen: make(map[key]entry), ring: make([]key, Capacity)}
}

// Once runs serve for the first delivery of (event, id). A repeat of a served
// delivery gets 200 and no effect, so the sender stops retrying; a repeat while
// the first is still being served gets 409, so the sender retries later. A
// delivery that serve fails (status 300 or above, or a panic) is forgotten, so
// the sender's retry is processed. With an unsigned ID the caller passes the
// unsigned event header too, so a copy with another event cannot use up the
// genuine delivery's ID; with a signed ID it passes "", so a copy with another
// event is still a repeat. An empty id (a sender that sends none) is always
// served.
func (g *Guard) Once(w http.ResponseWriter, event, id string, serve func(http.ResponseWriter)) {
	if id == "" {
		serve(w)
		return
	}
	k := sha256.Sum256([]byte(event + "\x00" + id))
	switch e, dup := g.claim(k); {
	case dup && e.done:
		w.WriteHeader(http.StatusOK)
		return
	case dup:
		http.Error(w, "delivery in progress", http.StatusConflict)
		return
	}
	ok := false
	defer func() { g.finish(k, ok) }()
	sw := &statusWriter{ResponseWriter: w, code: http.StatusOK}
	serve(sw)
	ok = sw.code < http.StatusMultipleChoices
}

// claim records k as in flight unless it is already known.
func (g *Guard) claim(k key) (entry, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if e, dup := g.seen[k]; dup {
		return e, true
	}
	// An unused slot holds the zero key, which no SHA-256 digest equals.
	if e, ok := g.seen[g.ring[g.next]]; ok && e.slot == g.next {
		delete(g.seen, g.ring[g.next])
	}
	g.ring[g.next] = k
	g.seen[k] = entry{slot: g.next}
	g.next = (g.next + 1) % Capacity
	return entry{}, false
}

// finish marks k served, or forgets it when serving failed.
func (g *Guard) finish(k key, ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	e, known := g.seen[k]
	switch {
	case !known:
	case ok:
		e.done = true
		g.seen[k] = e
	default:
		delete(g.seen, k)
	}
}

type statusWriter struct {
	http.ResponseWriter
	code    int
	written bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.written {
		w.code, w.written = code, true
	}
	w.ResponseWriter.WriteHeader(code)
}
