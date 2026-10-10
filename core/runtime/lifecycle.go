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
// without hammering the target. It also caps the delay between plugin restarts.
const maxRepollBackoff = 5 * time.Minute

// pluginRestartBackoff is the first delay before a dead source plugin is started
// again. It doubles with every further restart up to maxRepollBackoff, and drops
// back once the plugin has run for maxRepollBackoff, so a plugin that dies right
// after each start is not relaunched every second.
const pluginRestartBackoff = time.Second

// pluginPingTimeout bounds the health check of a plugin whose Gather failed; a
// plugin that does not answer in time is wedged and is treated as dead.
const pluginPingTimeout = 5 * time.Second

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
			bus:     r.bus,
			log:     r.log.With("module", m.name),
			cfg:     m.cfg,
			name:    m.name,
			class:   deliveryClassForModule(m.name),
			dormant: m.dormant,
		}
		if err := safe(func() error { return m.mod.Init(ctx, m.host) }); err != nil {
			m.host.unsubscribeAll()
			r.fail(&m.status, &m.err, "module", m.name, "init", err)
			continue
		}
		if m.dormant {
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
		// Each source gets its OWN cfg (cloned at registration), so two sources of
		// one connector kind cannot see each other's settings through a shared map.
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
//
// `source` is the source's REGISTRATION name, and that is the whole provenance
// contract: an event emitted by the roster row "grok-home-a" carries
// Source="grok-home-a", not the connector descriptor its sibling also has. A
// legacy, name-less registration still carries the descriptor, so the value those
// callers produced does not change. The connector never chooses this attribution —
// the host stamps it — and it is an INGESTION-INSTANCE label, not an assertion
// about a provider, a process, a home or a user (see event.Event.Source).
//
// A REGISTERED source (B1) additionally carries its roster snapshot, which the
// local bus sink stamps on every event. A collector's SinkFactory does not: the
// push envelope has no such field, and the receiving engine authenticates only
// the tenant, so those events arrive unattributed by construction.
func (r *Runtime) sinkFor(tenant, source string, registration *event.SourceRegistration) sdk.Sink {
	if r.sinkFactory != nil {
		return r.sinkFactory(tenant, source)
	}
	return &busSink{bus: r.bus, tenant: tenant, source: source, registration: registration, admit: r.sourceAdmission}
}

// gatherLoop runs a source according to its schedule. A one-shot/streaming source
// (poll<=0) runs Gather exactly once: a returned error or a panic marks it failed
// and is logged (left down, the original semantics); a clean return — or ctx
// canceled on Stop — marks it stopped. A polling source (poll>0) re-runs Gather
// every interval until Stop, staying Running with its last error recorded and
// retrying failed passes with exponential backoff (base = interval, capped). Every
// pass is panic-isolated so a faulty Gather never unwinds the engine. On either
// schedule, a failed pass of a plugin source whose process died starts the plugin
// again (restartDeadPlugin) and runs Gather at once.
func (r *Runtime) gatherLoop(s *sourceReg) {
	defer r.wg.Done()
	// Signal THIS source's drain so a live remove/rotate can wait for
	// exactly this goroutine to exit, rather than the engine-wide WaitGroup.
	defer close(s.done)
	sink := r.sinkFor(s.tenant, s.name, s.registration)

	if s.poll <= 0 {
		// Gather runs once, and again only after a dead plugin process was restarted.
		for !r.runGatherOnce(s, sink, false) && r.restartDeadPlugin(s) {
		}
		return
	}

	backoff := s.poll
	for {
		ok := r.runGatherOnce(s, sink, true)
		if !ok && r.restartDeadPlugin(s) {
			continue
		}
		if s.ctx.Err() != nil {
			r.setStopped(&s.status, &s.err)
			return
		}
		wait := s.poll
		if ok {
			backoff = s.poll
		} else {
			wait = jitterRepollBackoff(backoff)
			backoff = min(backoff*2, maxRepollBackoff)
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
	pass := r.collectionSink(s.ctx, s, sink)
	err := func() (err error) {
		returned := false
		defer func() {
			rec := recover()
			if !returned {
				err = fmt.Errorf("runtime: source Gather panic (%T)", rec)
			}
		}()
		err = s.conn.Gather(s.ctx, pass)
		returned = true
		return err
	}()
	pass.finish(s.ctx, err)

	switch {
	case s.ctx.Err() != nil:
		// Stopped by the runtime: not a failure. The caller sets stopped.
		return err == nil
	case err != nil:
		if keepRunning {
			r.log.Warn("runtime: source gather failed; will retry", "source", s.name, "component", s.component, "error", err)
			r.recordErr(&s.err, err)
		} else {
			msg := "runtime: source gather failed; left down"
			if s.client != nil {
				msg = "runtime: source gather failed; left down unless its plugin process exited"
			}
			r.log.Warn(msg, "source", s.name, "component", s.component, "error", err)
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

// restartDeadPlugin supervises a plugin source after a failed Gather pass. When the
// plugin process has died, it reaps it and starts the same binary again (same path,
// the pinned checksum checked again, fresh confinement), Opens it with the source's
// settings and swaps it in, retrying with backoff until it succeeds or the source is
// stopped. It reports whether Gather should run again: false for an in-process
// source, for a plugin that is still alive (the failure was the connector's own) and
// for a source stopped while waiting.
func (r *Runtime) restartDeadPlugin(s *sourceReg) bool {
	if s.client == nil || s.ctx.Err() != nil || !pluginDead(s.ctx, s.client) {
		return false
	}
	r.log.Warn("runtime: source plugin process exited; restarting it", "source", s.name, "component", s.component)
	// A polling source stays Running through a failed pass; with its plugin dead and a
	// restart waiting, it is not.
	r.set(&s.status, &s.err, StatusFailed, errPluginExited)
	r.reapPlugin(s.client)
	if s.restartWait == 0 || time.Since(s.restartedAt) > maxRepollBackoff {
		s.restartWait = pluginRestartBackoff
	}
	for {
		if !r.sleep(s.ctx, jitterRepollBackoff(s.restartWait)) {
			r.setStopped(&s.status, &s.err)
			return false
		}
		s.restartWait = min(s.restartWait*2, maxRepollBackoff)
		err := r.relaunchPlugin(s)
		if err == nil {
			s.restartedAt = time.Now()
			r.log.Info("runtime: source plugin restarted", "source", s.name, "component", s.component)
			return true
		}
		r.mu.Lock()
		stopping := r.stopped // Stop marks the runtime before it cancels the sources
		r.mu.Unlock()
		if stopping || s.ctx.Err() != nil {
			r.setStopped(&s.status, &s.err)
			return false
		}
		r.log.Warn("runtime: source plugin restart failed; will retry", "source", s.name, "component", s.component, "error", err, "retry_in", s.restartWait)
		r.set(&s.status, &s.err, StatusFailed, fmt.Errorf("restart: %w", err))
	}
}

// errPluginExited is the status error of a source whose plugin process died and
// waits to be restarted.
var errPluginExited = errors.New("runtime: plugin process exited")

// pluginDead reports whether a plugin's process is gone or wedged: go-plugin saw it
// exit, or its gRPC health check fails or does not answer within pluginPingTimeout.
// A canceled ctx reports false: the source is stopping, not restarting.
func pluginDead(ctx context.Context, c *goplugin.Client) bool {
	if c.Exited() {
		return true
	}
	rpc, err := c.Client()
	if err != nil {
		return true
	}
	answer := make(chan error, 1) // go-plugin's Ping has no deadline of its own
	go func() { answer <- rpc.Ping() }()
	select {
	case err := <-answer:
		return err != nil
	case <-time.After(pluginPingTimeout):
		return true
	case <-ctx.Done():
		return false
	}
}

// relaunchPlugin starts s's plugin binary again and swaps the new process in. The new
// process must name the same component and Open with the source's settings, exactly
// as at registration; otherwise it is reaped and nothing is swapped.
func (r *Runtime) relaunchPlugin(s *sourceReg) error {
	conn, client, err := r.launchSource(s.plugin)
	if err != nil {
		return err
	}
	if got := conn.Descriptor().Name; got != s.component {
		r.reapPlugin(client)
		return fmt.Errorf("runtime: plugin %q now names itself %q, not %q", s.plugin.path, got, s.component)
	}
	// Open sends the resolved settings to the new process: not to one a stopped
	// runtime or a removed source no longer wants.
	r.mu.Lock()
	gone := r.stopped || s.ctx.Err() != nil
	r.mu.Unlock()
	if gone {
		r.reapPlugin(client)
		return ErrStopped
	}
	if oerr := safe(func() error { return conn.Open(s.ctx, s.cfg) }); oerr != nil {
		r.reapPlugin(client)
		// Open ran on the RESOLVED settings, so its text may carry a secret: only the
		// debug log sees it, as the reconciler does (cmd/olivares reconcile.go).
		r.log.Debug("runtime: restarted source plugin refused its settings", "source", s.name, "error", oerr)
		return ErrSourceOpenFailed
	}
	r.mu.Lock()
	if r.stopped || s.ctx.Err() != nil {
		r.mu.Unlock()
		r.reapPlugin(client)
		return ErrStopped
	}
	s.conn, s.client = conn, client
	s.status, s.err = StatusRunning, nil
	r.clients = append(r.clients, client)
	r.mu.Unlock()
	return nil
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
		r.mu.Lock()
		conn := s.conn // a plugin restart swaps it under r.mu; Stop may arrive before it drained
		r.mu.Unlock()
		if err := safe(func() error { return conn.Close(ctx) }); err != nil {
			r.log.Warn("runtime: source close failed", "source", s.name, "component", s.component, "error", err)
		}
		r.markStoppedUnlessFailed(&s.status, &s.err)
	}

	// Stop modules in reverse registration order.
	for i := len(modules) - 1; i >= 0; i-- {
		m := modules[i]
		if m.dormant {
			continue
		}
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
