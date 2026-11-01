// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"sync"
	"sync/atomic"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/modules/sessions/hookpep"
)

// editionLive holds an edition gate that settings an administrator saves can replace while
// the engine runs. A consumer calls Get for each request and keeps no copy. Get answers the
// zero value when nothing is configured: for a gate, the nil interface its consumer already
// checks for, so "not configured" keeps the meaning it had when the gate was built once at
// boot.
type editionLive[T any] struct {
	boot sync.Once
	cur  atomic.Pointer[T]
}

// bootFrom builds the boot gate once, however many consumers ask, and returns the getter
// each of them keeps. Every consumer shares the one gate, so a change reaches all of them.
func (l *editionLive[T]) bootFrom(build func() T) func() T {
	l.boot.Do(func() {
		v := build()
		// A setting applied before the first consumer asked (the edition binds its settings
		// when the store opens, before the servers are built) is newer than the boot value.
		l.cur.CompareAndSwap(nil, &v)
	})
	return l.Get
}

func (l *editionLive[T]) Get() (zero T) {
	if p := l.cur.Load(); p != nil {
		return *p
	}
	return zero
}

// Set puts v in force from the next request on.
func (l *editionLive[T]) Set(v T) { l.cur.Store(&v) }

// Clear puts nothing in force: the consumer's "not configured" path.
func (l *editionLive[T]) Clear() {
	var zero T
	l.cur.Store(&zero)
}

// editionLiveGates are the gates of one engine that an edition's settings can change
// without a restart. They belong to the engine, not the process: tests and `dr drill`
// boot several engines in one process, and a gate saved on one must never reach another.
type editionLiveGates struct {
	serverToolEgress editionLive[serverToolEgressGate]
	contentFirewall  editionLive[contentInspector]
	hookFirewall     editionLive[contentInspector]
	computerUse      editionLive[computerUseGate]
	renderInspector  editionLive[mcpc.RenderInspector]
	threatIntel      editionLive[threatIntelSource]
}

// gates answers this engine's live gates. An engine built without a module set (a test)
// gets an empty set that nothing changes, so a consumer never checks for nil.
func (e *engine) gates() *editionLiveGates {
	if e == nil || e.editionGates == nil {
		return &editionLiveGates{}
	}
	return e.editionGates
}

// hookInspectorSource gives the hook PEP its own interface type over the same live answer:
// a func's result type must match exactly, and a nil answer stays nil.
func hookInspectorSource(live func() contentInspector) func() hookpep.ContentInspector {
	return func() hookpep.ContentInspector { return live() }
}

// liveInspectorChain is chainInspectors over what is in force now: the edition's live
// inspector and the engine's decoys. With neither it answers nil, as chainInspectors does.
func liveInspectorChain(gate func() contentInspector, decoys sessionDecoyEngine) func() contentInspector {
	return func() contentInspector { return chainInspectors(gate(), decoys) }
}
