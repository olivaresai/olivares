// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package runtime

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"time"

	goplugin "github.com/hashicorp/go-plugin"

	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
)

// ErrStopped is returned when Start is called on an already-stopped runtime.
var ErrStopped = errors.New("runtime: stopped")

// maxRepollBackoff caps the exponential backoff between failed Gather passes of a
// polling source, so a persistently-unreachable sampling source retries forever
// without hammering the target.
const maxRepollBackoff = 5 * time.Minute

// jitterRepollBackoff keeps polling-source retry timing consistent with the
// ±20% jitter policy used by the event-delivery retry loop. It is a variable so
// the scheduler test can replace randomness with deterministic samples.
var jitterRepollBackoff = func(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	f := 0.8 + 0.4*rand.Float64() // #nosec G404 -- scheduling jitter, not key material
	return time.Duration(float64(d) * f)
}

// Start opens and wires every registered component, then begins running sources.
// Order matters: outputs subscribe and modules initialize (and subscribe) BEFORE
// any source emits, so no early event is missed. A component that fails to
// open/init is isolated — marked failed and skipped — so one bad connector does
// not stop the engine; inspect Status to see failures. ctx bounds the setup
// calls (Open/Init/Start), not the running lifetime.
func (r *Runtime) Start(ctx context.Context) error {
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return ErrAlreadyStarted
	}
	if r.stopped {
		r.mu.Unlock()
		return ErrStopped
	}
	r.started = true
	r.runCtx, r.cancel = context.WithCancel(context.Background())
	r.mu.Unlock()

	r.startOutputs(ctx)
	r.startModules(ctx)
	r.startSources(ctx)
	r.startJobs()
	return nil
}

func (r *Runtime) startOutputs(ctx context.Context) {
	for _, o := range r.outputs {
		if err := safe(func() error { return o.conn.Open(ctx, o.cfg) }); err != nil {
			r.fail(&o.status, &o.err, "output", o.name, "open", err)
			continue
		}
		// Name the subscription when the bus supports it, so the
		// per-subscriber queue-depth gauge can attribute a saturated output
		// instead of aggregating every output under "anonymous".
		var sub eventbus.Subscription
		var err error
		if named, ok := r.bus.(eventbus.NamedSubscriber); ok {
			sub, err = named.SubscribeNamed(o.name, o.types, r.outputHandler(o))
		} else {
			sub, err = r.bus.Subscribe(o.types, r.outputHandler(o))
		}
		if err != nil {
			r.fail(&o.status, &o.err, "output", o.name, "subscribe", err)
			continue
		}
		o.sub = sub
		r.setRunning(&o.status, &o.err)
	}
}

// outputHandler maps each delivered event to a Notification and delivers it,
// recovering panics so a faulty output is marked failed rather than crashing the
// bus goroutine.
func (r *Runtime) outputHandler(o *outputReg) event.Handler {
	return func(ctx context.Context, e event.Event) (err error) {
		defer func() {
			if rec := recover(); rec != nil {
				err = fmt.Errorf("output %q panicked: %v", o.name, rec)
				r.fail(&o.status, &o.err, "output", o.name, "notify", err)
			}
		}()
		if nErr := o.conn.Notify(ctx, notificationFromEvent(e)); nErr != nil {
			r.log.Warn("runtime: output notify failed", "output", o.name, "error", nErr)
			return nErr
		}
		return nil
	}
}

func (r *Runtime) startModules(ctx context.Context) {
	for _, m := range r.modules {
		m.host = &moduleHost{
			bus:   r.bus,
			log:   r.log.With("module", m.name),
			cfg:   m.cfg,
			name:  m.name,
			class: deliveryClassForModule(m.name),
		}
		if err := safe(func() error { return m.mod.Init(ctx, m.host) }); err != nil {
			r.fail(&m.status, &m.err, "module", m.name, "init", err)
			continue
		}
		if err := safe(func() error { return m.mod.Start(ctx) }); err != nil {
			m.host.unsubscribeAll()
			r.fail(&m.status, &m.err, "module", m.name, "start", err)
			continue
		}
		r.setRunning(&m.status, &m.err)
	}
}

func (r *Runtime) startSources(ctx context.Context) {
	for _, s := range r.sources {
		if err := safe(func() error { return s.conn.Open(ctx, s.cfg) }); err != nil {
			r.fail(&s.status, &s.err, "source", s.name, "open", err)
			continue
		}
		// Per-source ctx/done: a child of runCtx so Stop's single cancel
		// still cascades to every source, yet this one source can be canceled
		// alone for a live remove/rotate. done is closed by gatherLoop on exit.
		s.ctx, s.cancel = context.WithCancel(r.runCtx)
		s.done = make(chan struct{})
		r.setRunning(&s.status, &s.err)
		r.wg.Add(1)
		go r.gatherLoop(s)
	}
}

// sinkFor builds the Sink a source's Gather emits to: the configured SinkFactory
// when set (a collector pushes to a remote core), else the default that lifts each
// observation onto the local event bus.
func (r *Runtime) sinkFor(tenant, source string) sdk.Sink {
	if r.sinkFactory != nil {
		return r.sinkFactory(tenant, source)
	}
	return &busSink{bus: r.bus, tenant: tenant, source: source}
}

// gatherLoop runs a source according to its schedule. A one-shot/streaming source
// (poll<=0) runs Gather exactly once: a returned error or a panic marks it failed
// and is logged (left down, the original semantics); a clean return — or ctx
// canceled on Stop — marks it stopped. A polling source (poll>0) re-runs Gather
// every interval until Stop, staying Running with its last error recorded and
// retrying failed passes with exponential backoff (base = interval, capped). Every
// pass is panic-isolated so a faulty Gather never unwinds the engine.
func (r *Runtime) gatherLoop(s *sourceReg) {
	defer r.wg.Done()
	// Signal THIS source's drain so a live remove/rotate can wait for
	// exactly this goroutine to exit, rather than the engine-wide WaitGroup.
	defer close(s.done)
	sink := r.sinkFor(s.tenant, s.name)

	if s.poll <= 0 {
		r.runGatherOnce(s, sink, false)
		return
	}

	backoff := s.poll
	for {
		ok := r.runGatherOnce(s, sink, true)
		if s.ctx.Err() != nil {
			r.setStopped(&s.status, &s.err)
			return
		}
		wait := s.poll
		if ok {
			backoff = s.poll
		} else {
			wait = jitterRepollBackoff(backoff)
			backoff = minDuration(backoff*2, maxRepollBackoff)
		}
		if !r.sleep(s.ctx, wait) {
			r.setStopped(&s.status, &s.err)
			return
		}
	}
}

// runGatherOnce runs one panic-isolated Gather pass and records the outcome.
// keepRunning distinguishes a scheduled (polling) source — which stays Running
// across passes with its last error recorded, so the operator sees a live source —
// from a one-shot/streaming source, which transitions to stopped/failed exactly as
// before. It returns whether the pass succeeded (for backoff).
func (r *Runtime) runGatherOnce(s *sourceReg, sink sdk.Sink, keepRunning bool) bool {
	err := func() (err error) {
		defer func() {
			if rec := recover(); rec != nil {
				err = fmt.Errorf("panic: %v", rec)
			}
		}()
		return s.conn.Gather(s.ctx, sink)
	}()

	switch {
	case s.ctx.Err() != nil:
		// Stopped by the runtime: not a failure. The caller sets stopped.
		return err == nil
	case err != nil:
		if keepRunning {
			r.log.Warn("runtime: source gather failed; will retry", "source", s.name, "error", err)
			r.recordErr(&s.err, err)
		} else {
			r.log.Warn("runtime: source gather failed; left down", "source", s.name, "error", err)
			r.set(&s.status, &s.err, StatusFailed, err)
		}
		return false
	default:
		if keepRunning {
			r.recordErr(&s.err, nil)
		} else {
			r.setStopped(&s.status, &s.err)
		}
		return true
	}
}

// startJobs launches each registered periodic job on its own goroutine, tracked
// by the same WaitGroup as sources so Stop waits for them. Jobs start AFTER
// sources (so subscribers and any same-process state are up); a roster sync writes
// to the store, not the bus, so it has no ordering dependency on subscribers.
func (r *Runtime) startJobs() {
	for _, j := range r.jobs {
		r.wg.Add(1)
		go r.jobLoop(j)
	}
}

// jobLoop runs a periodic job every interval until the runtime stops, each pass
// panic-isolated and error-logged so a transient failure never kills the schedule.
func (r *Runtime) jobLoop(j *jobReg) {
	defer r.wg.Done()
	run := func() {
		defer func() {
			if rec := recover(); rec != nil {
				r.log.Warn("runtime: periodic job panicked; will run again next interval", "job", j.name, "panic", rec)
			}
		}()
		if err := j.fn(r.runCtx); err != nil && r.runCtx.Err() == nil {
			r.log.Warn("runtime: periodic job failed; will run again next interval", "job", j.name, "error", err)
		}
	}
	if j.immediate {
		run()
		if r.runCtx.Err() != nil {
			return
		}
	}
	for {
		if !r.sleep(r.runCtx, j.interval) {
			return
		}
		run()
	}
}

// sleep waits d, returning true when the timer fires and false when ctx is
// canceled first. ctx is the engine-wide runCtx for a periodic job and the
// per-source child ctx for a polling source, so a live source remove
// wakes only that source's sleep, not every job's. A non-positive d returns true
// immediately.
func (r *Runtime) sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// minDuration returns the smaller of two durations.
func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// Stop tears the runtime down in reverse: it cancels source Gather goroutines and
// waits for them (bounded by ctx), closes sources, stops modules, unsubscribes
// and closes outputs, kills out-of-process plugins, and closes the bus if it owns
// it. Every component call is panic-isolated so one bad teardown does not abort
// the rest. It is safe to call once; a second call is a no-op.
//
// It takes reloadMu for its whole duration so a live source reconfiguration
// and teardown never overlap: an in-flight AddSource/Replace/Remove completes
// before Stop snapshots the source set (so a source is never closed twice — once
// by the live op's quiesce and once by Stop), and a live mutation that arrives
// during teardown blocks until Stop has marked the runtime stopped, then refuses
// with ErrNotRunning. Lock order is reloadMu→r.mu everywhere, so this cannot
// deadlock (no path holds r.mu while waiting on reloadMu).
func (r *Runtime) Stop(ctx context.Context) error {
	r.reloadMu.Lock()
	defer r.reloadMu.Unlock()

	r.mu.Lock()
	if !r.started || r.stopped {
		r.mu.Unlock()
		return nil
	}
	r.stopped = true
	cancel := r.cancel
	sources := append([]*sourceReg(nil), r.sources...)
	modules := append([]*moduleReg(nil), r.modules...)
	outputs := append([]*outputReg(nil), r.outputs...)
	standaloneOutputs := append([]sdk.OutputConnector(nil), r.standaloneOutputs...)
	clients := append([]*goplugin.Client(nil), r.clients...)
	r.mu.Unlock()

	// Signal sources to stop and wait for their goroutines (bounded by ctx).
	if cancel != nil {
		cancel()
	}
	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		r.log.Warn("runtime: timed out waiting for sources to stop", "error", ctx.Err())
	}

	for _, s := range sources {
		if err := safe(func() error { return s.conn.Close(ctx) }); err != nil {
			r.log.Warn("runtime: source close failed", "source", s.name, "error", err)
		}
		r.markStoppedUnlessFailed(&s.status, &s.err)
	}

	// Stop modules in reverse registration order.
	for i := len(modules) - 1; i >= 0; i-- {
		m := modules[i]
		if m.host != nil {
			m.host.unsubscribeAll()
		}
		if err := safe(func() error { return m.mod.Stop(ctx) }); err != nil {
			r.log.Warn("runtime: module stop failed", "module", m.name, "error", err)
		}
		r.markStoppedUnlessFailed(&m.status, &m.err)
	}

	for _, o := range outputs {
		if o.sub != nil {
			o.sub.Unsubscribe()
		}
		if err := safe(func() error { return o.conn.Close(ctx) }); err != nil {
			r.log.Warn("runtime: output close failed", "output", o.name, "error", err)
		}
		r.markStoppedUnlessFailed(&o.status, &o.err)
	}
	for _, o := range standaloneOutputs {
		if err := safe(func() error { return o.Close(ctx) }); err != nil {
			r.log.Warn("runtime: standalone output plugin close failed", "output", o.Descriptor().Name, "error", err)
		}
	}

	for _, c := range clients {
		c.Kill()
	}

	// release per-plugin confinement resources (cgroup dirs) AFTER the clients are
	// killed — the plugin process must be gone before its cgroup dir can be removed.
	r.mu.Lock()
	cleanups := r.pluginCleanupByClient
	r.pluginCleanupByClient = make(map[*goplugin.Client]func())
	r.mu.Unlock()
	for _, fn := range cleanups {
		fn()
	}

	if r.ownBus {
		_ = r.bus.Close()
	}
	return nil
}

// safe runs fn, converting a panic into an error so a misbehaving component
// cannot unwind the engine.
func safe(fn func() error) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("panic: %v", rec)
		}
	}()
	return fn()
}

// --- status helpers (all serialize on r.mu) ----------------------------------

func (r *Runtime) set(st *Status, errp *error, s Status, e error) {
	r.mu.Lock()
	*st = s
	*errp = e
	r.mu.Unlock()
}

func (r *Runtime) setRunning(st *Status, errp *error) { r.set(st, errp, StatusRunning, nil) }
func (r *Runtime) setStopped(st *Status, errp *error) { r.set(st, errp, StatusStopped, nil) }

// recordErr updates only a component's last error without touching its status —
// used by a polling source that stays Running across passes while surfacing the
// outcome of its most recent Gather (e is nil on a clean pass).
func (r *Runtime) recordErr(errp *error, e error) {
	r.mu.Lock()
	*errp = e
	r.mu.Unlock()
}

func (r *Runtime) markStoppedUnlessFailed(st *Status, errp *error) {
	r.mu.Lock()
	if *st != StatusFailed {
		*st = StatusStopped
		*errp = nil
	}
	r.mu.Unlock()
}

func (r *Runtime) fail(st *Status, errp *error, kind, name, phase string, e error) {
	r.log.Warn("runtime: component failed", "kind", kind, "name", name, "phase", phase, "error", e)
	r.set(st, errp, StatusFailed, fmt.Errorf("%s: %w", phase, e))
}
