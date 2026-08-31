// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"sync"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

// toolSignal is one side of a correlated tool invocation: the OTEL tool_result span/event
// or the PostToolUse hook for the same call. The resource is resolved (and redacted) at
// ingest, so the correlator holds only a safe reference, never the raw tool input.
// hasResource is false when only a generic tool-usage edge could be derived (no hook,
// OTEL_LOG_TOOL_DETAILS off). toolUseID is the model's tool_use block id — VERIFIED
// 2026-06-09 to be present on BOTH the OTEL tool_result/tool_decision events AND in the
// hook payload ("Matches the tool_use_id passed to hooks"), so it is the PRECISE
// correlation join. promptID (`prompt.id`) groups every event of one user prompt/turn;
// it rides the OTEL side only (the hook payload does not carry it) and is a forward-safe
// disambiguator, never the sole join.
type toolSignal struct {
	sessionID   string
	toolName    string
	toolUseID   string
	promptID    string
	at          time.Time
	fromHook    bool
	resKind     string
	resRef      string
	mode        model.AccessMode
	hasResource bool
}

// pendingCall holds the two sides of one tool call while they are being joined.
type pendingCall struct {
	otel *toolSignal
	hook *toolSignal
}

// buffered is a single unmatched signal awaiting its counterpart, with the time it was
// buffered (for window expiry).
type buffered struct {
	sig toolSignal
	at  time.Time
}

// correlator joins the OTEL tool_result span/event and the PostToolUse hook for the same
// tool call into exactly one access edge (ARCHITECTURE.md, the cooperative high-fidelity
// path). It keys a BUCKET on session+tool, then MATCHES within the bucket by tool_use_id:
//
//   - Both sides carry tool_use_id (the VERIFIED current norm): a hook pairs with the
//     OTEL signal of the SAME id, so two calls of the same tool that INTERLEAVE within the
//     window each pair correctly (the old single-slot session+tool join mis-handled this —
//     it could leave both calls unpaired). An exact id match is preferred.
//   - One side lacks the id (an older/degraded client): the signals still pair on
//     session+tool (an empty id conflicts with nothing) — the asymmetric fallback.
//   - Two DIFFERENT calls whose ids (or prompt.ids) are both present and DIFFER are kept
//     apart (never merged), even when they share the session+tool bucket.
//
// A buffered signal lives at most `window`; the janitor sweeps expired ones to a
// best-effort single-side edge (a hook alone still yields a precise resource edge; an
// OTEL span alone a usage edge). It is concurrency-safe; emission happens outside the
// lock so a slow sink never blocks ingestion.
type correlator struct {
	window time.Duration
	emit   func(model.Observation)

	mu      sync.Mutex
	pending map[string][]buffered
}

// newCorrelator returns a correlator that flushes a buffered signal after window and
// emits each resulting observation through emit.
func newCorrelator(window time.Duration, emit func(model.Observation)) *correlator {
	return &correlator{window: window, emit: emit, pending: map[string][]buffered{}}
}

// bucketKey is the correlation bucket: session+tool. Keeping the OTEL and hook sides in
// the same bucket (independent of which side carries an id) is what lets the asymmetric
// case still pair; the per-signal id then matches the precise counterpart inside it.
func bucketKey(s toolSignal) string {
	return "st:" + s.sessionID + "\x00" + s.toolName
}

// offer ingests one tool signal at time now. If a compatible counterpart is already
// buffered in the same session+tool bucket, the pair completes and one edge is emitted;
// otherwise the signal is buffered until its counterpart arrives or the window expires.
// now is passed in so the caller's clock (and tests) drive expiry deterministically.
func (c *correlator) offer(sig toolSignal, now time.Time) {
	c.mu.Lock()
	var ready []model.Observation
	k := bucketKey(sig)
	list := c.pending[k]
	if i := findMatch(list, sig); i >= 0 {
		match := list[i].sig
		c.pending[k] = append(list[:i], list[i+1:]...)
		if len(c.pending[k]) == 0 {
			delete(c.pending, k)
		}
		ready = append(ready, edgeForPair(match, sig))
	} else {
		c.pending[k] = append(list, buffered{sig: sig, at: now})
	}
	c.mu.Unlock()
	c.flush(ready)
}

// findMatch returns the index in list of the best counterpart for sig (opposite side,
// non-conflicting call), or -1. It tries, in order:
//
//  1. an EXACT non-empty tool_use_id match (the precise join, so interleaved id'd calls
//     pair with their own counterpart, not whichever arrived);
//  2. an id-LESS opposite-side counterpart (FIFO) — so an id-less signal does NOT consume
//     a precisely-id'd buffered span that may still pair with its own exact counterpart
//     still in flight;
//  3. ANY id-compatible opposite-side counterpart (FIFO) — the genuine asymmetric case
//     where the only counterpart available carries an id the incoming signal lacks.
//
// A counterpart whose tool_use_id OR prompt.id is present on both sides and DIFFERS is
// never matched (a genuinely different call).
func findMatch(list []buffered, sig toolSignal) int {
	if sig.toolUseID != "" {
		for i, b := range list {
			if b.sig.fromHook != sig.fromHook && b.sig.toolUseID == sig.toolUseID {
				return i
			}
		}
	}
	if i := fifoMatch(list, sig, true); i >= 0 {
		return i
	}
	return fifoMatch(list, sig, false)
}

// fifoMatch returns the first opposite-side, non-different counterpart in list. When
// idlessOnly is true it considers only a buffered signal that ALSO lacks a tool_use_id,
// so an id-less incoming signal prefers an id-less partner over hijacking a precisely-id'd
// span whose exact counterpart may still arrive.
func fifoMatch(list []buffered, sig toolSignal, idlessOnly bool) int {
	for i, b := range list {
		if b.sig.fromHook == sig.fromHook {
			continue
		}
		if idlessOnly && b.sig.toolUseID != "" {
			continue
		}
		if differentCall(b.sig, sig) {
			continue
		}
		return i
	}
	return -1
}

// differentCall reports whether two signals are provably DIFFERENT tool calls: their
// tool_use_id (or prompt.id) is present on BOTH sides and differs. When a discriminator
// is absent on either side it cannot prove a difference (the asymmetric/fallback case),
// so the signals remain joinable. prompt.id participates as a forward-safe guard — the
// hook payload does not currently carry it, so it is a no-op for the OTEL↔hook join, but
// it keeps two genuinely different prompts' calls apart should that ever change.
func differentCall(a, b toolSignal) bool {
	return idsDiffer(a.toolUseID, b.toolUseID) || idsDiffer(a.promptID, b.promptID)
}

// idsDiffer reports whether two ids are both non-empty and unequal.
func idsDiffer(a, b string) bool { return a != "" && b != "" && a != b }

// sweep flushes every buffered signal older than the window at time now, emitting its
// best-effort single-side edge. The runtime's janitor calls it on a ticker; a test calls
// it directly. It bounds memory: no signal lives longer than the window.
func (c *correlator) sweep(now time.Time) {
	c.mu.Lock()
	var ready []model.Observation
	for k, list := range c.pending {
		kept := list[:0]
		for _, b := range list {
			if now.Sub(b.at) >= c.window {
				ready = append(ready, edgeForSingle(b.sig))
			} else {
				kept = append(kept, b)
			}
		}
		if len(kept) == 0 {
			delete(c.pending, k)
		} else {
			c.pending[k] = kept
		}
	}
	c.mu.Unlock()
	c.flush(ready)
}

// drain flushes every remaining buffered signal regardless of age, used at shutdown so
// in-flight observations are not lost.
func (c *correlator) drain() {
	c.mu.Lock()
	var ready []model.Observation
	for k, list := range c.pending {
		for _, b := range list {
			ready = append(ready, edgeForSingle(b.sig))
		}
		delete(c.pending, k)
	}
	c.mu.Unlock()
	c.flush(ready)
}

// flush emits the collected observations outside the lock.
func (c *correlator) flush(obs []model.Observation) {
	for _, o := range obs {
		if o != nil {
			c.emit(o)
		}
	}
}

// edgeForPair builds the single access edge for a matched (otel, hook) pair, placing each
// signal on its side and choosing the richest resource (the hook breaks ties — it carries
// the real tool_input). a and b are opposite sides (guaranteed by findMatch).
func edgeForPair(a, b toolSignal) model.Observation {
	pc := &pendingCall{}
	place(pc, a)
	place(pc, b)
	return edgeFor(pc)
}

// edgeForSingle builds the best-effort edge for a half-complete call (one side only).
func edgeForSingle(s toolSignal) model.Observation {
	pc := &pendingCall{}
	place(pc, s)
	return edgeFor(pc)
}

// place stores sig on the otel or hook side of pc.
func place(pc *pendingCall, sig toolSignal) {
	s := sig
	if sig.fromHook {
		pc.hook = &s
	} else {
		pc.otel = &s
	}
}

// edgeFor builds the single access edge for a (possibly half-complete) call, choosing the
// richest resource available: the side that resolved a specific resource wins (the hook
// breaks ties, as it carries the real tool_input), and the OTEL span supplies the event
// time when present. It returns nil only if neither side can be attributed.
func edgeFor(pc *pendingCall) model.Observation {
	chosen, when := chooseSignal(pc)
	if chosen == nil {
		return nil
	}
	edge, ok := edgeFromTool(chosen.sessionID, chosen.toolName, nil, when, model.ConfidenceAttributed)
	if !ok {
		return nil
	}
	// resourceFromTool already ran at ingest; reuse its result rather than re-deriving
	// from a (discarded) input map.
	edge.ResourceKind = chosen.resKind
	edge.ResourceRef = chosen.resRef
	edge.Mode = chosen.mode
	return edge
}

// chooseSignal picks which buffered signal supplies the edge's resource and returns the
// event time to stamp. Preference: a side with a concrete resource over a generic one;
// the hook over the OTEL span on a tie. The time prefers the OTEL span (the authoritative
// event clock), falling back to the hook's receive time.
func chooseSignal(pc *pendingCall) (*toolSignal, time.Time) {
	otel, hook := pc.otel, pc.hook
	when := time.Time{}
	if otel != nil {
		when = otel.at
	} else if hook != nil {
		when = hook.at
	}
	switch {
	case hook != nil && hook.hasResource:
		return hook, when
	case otel != nil && otel.hasResource:
		return otel, when
	case hook != nil:
		return hook, when
	case otel != nil:
		return otel, when
	default:
		return nil, when
	}
}
