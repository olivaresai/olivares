// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"sync"
)

// observer.go —: the runtime observation seam surface.go reserved ("the
// authoritative runtime observation of an actual elicitation/sampling request is
// a separate signal"). The transports already SKIP server-initiated messages
// while scanning for a response (stdio.go readResponse, http.go responseFromSSE);
// the requestObserver records the governance-relevant ones as they fly by, so
// posture can grade a server that ACTIVELY drives deprecated/undeclared features
// against a zero-capability client (deprecation.go). Nothing is answered, nothing
// is fabricated — only what the server itself sent is recorded, minimal-data
// (method + the closed includeContext enum, never params).
//
// Logging-channel posture, verified: this connector never consumed the
// MCP logging channel. notifications/message is skipped by design in the response
// scanners, and the 2026-07-28 RC says servers MUST NOT emit it unless the request
// opts in with `_meta.io.modelcontextprotocol/logLevel`; the stateless client never
// sets that key. Monitoring remains on the OTel and audit seams.

// maxObservedRequests caps the recorded observations per transport. The REAL
// memory bound is the dedup over the closed key space (4 methods × the 4-value
// includeContext enum on one of them = 7 possible keys); this cap is
// defense-in-depth should the allow-list or enum ever widen.
const maxObservedRequests = 16

// requestObserver accumulates deduplicated server-initiated observations.
// It is safe for concurrent use (transports hold their own locks, but listen
// streams may run concurrently with nothing else guaranteed).
type requestObserver struct {
	mu   sync.Mutex
	seen map[string]struct{}
	obs  []serverRequestObservation
}

// observe records msg when it is a governance-relevant server-initiated request
// or notification (a message WITH a method; responses have none). Unknown methods
// are ignored — the allow-list is closed so a server cannot smuggle free-form
// method strings into findings.
func (o *requestObserver) observe(msg rpcMessage) {
	if msg.Method == "" {
		return // a response, not a server-initiated message
	}
	switch msg.Method {
	case methodSamplingCreate, methodRootsList, methodElicitationCreate, notifRootsListChanged:
	default:
		return
	}
	obs := serverRequestObservation{method: msg.Method}
	if msg.Method == methodSamplingCreate {
		obs.includeContext = includeContextOf(msg.Params)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.obs) >= maxObservedRequests {
		return
	}
	key := obs.method + "|" + obs.includeContext
	if o.seen == nil {
		o.seen = map[string]struct{}{}
	}
	if _, dup := o.seen[key]; dup {
		return
	}
	o.seen[key] = struct{}{}
	o.obs = append(o.obs, obs)
}

// observations returns a copy of what was observed.
func (o *requestObserver) observations() []serverRequestObservation {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.obs) == 0 {
		return nil
	}
	out := make([]serverRequestObservation, len(o.obs))
	copy(out, o.obs)
	return out
}

// includeContextOf extracts the includeContext value of a sampling/createMessage
// request's params. Only the closed enum values are returned — anything else is
// reported as "" (unrecognized values never ride into findings).
func includeContextOf(params json.RawMessage) string {
	var p struct {
		IncludeContext string `json:"includeContext"`
	}
	if json.Unmarshal(params, &p) != nil {
		return ""
	}
	switch p.IncludeContext {
	case "none", "thisServer", "allServers":
		return p.IncludeContext
	}
	return ""
}
