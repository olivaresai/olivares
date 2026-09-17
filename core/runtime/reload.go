// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	goplugin "github.com/hashicorp/go-plugin"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	sdkplugin "github.com/olivaresai/olivares/sdk/plugin"
)

// Live source reconfiguration. The runtime is single-threaded by contract
// (Start once, Stop once); these methods add EXACTLY ONE serialized mutation path
// on top of it, so an operator can add / remove / rotate an individual source
// connector while the engine runs — no process restart. They never touch
// outputs, modules or schedules (those are sealed at Start and cannot be
// hot-applied), and they preserve every runtime invariant:
//
//   - Deny-closed: a new/replacement source is Open()ed (the SDK's validation
//     point) BEFORE it is wired; a failure leaves the other sources — and, on a
//     rotate, the OLD source — running untouched. One bad connector never takes
//     down the engine, exactly as at boot (startSources).
//   - Quiesce via the SDK contract, not a new method: stopping one source is
//     cancel(its child ctx) → wait for Gather to return (bounded) → Close once.
//     The SourceConnector interface is frozen at v1 (no Drain/Quiesce method);
//     ctx-cancel-then-Close is the contract-sanctioned stop (sdk/connector.go).
//   - Stops as one: each source's ctx is a child of r.runCtx, so Stop's single
//     cancel still cascades to every live-added source.
//   - Serialized: reloadMu single-flights these against each other and against
//     Stop, so there is never a concurrent graph edit; the quick slice/index
//     edits hold r.mu, but the (possibly slow) Open and drain happen OUTSIDE
//     r.mu so a gather goroutine's status write never deadlocks the mutation.

// SourceInfo is a non-secret snapshot of one live source, for diffing desired
// configuration against what is actually running (the reconciler's input).
type SourceInfo struct {
	// Name is the source's REGISTRATION name — the runtime's identity for THIS
	// source. For a source registered by the composition root it is the operator's
	// roster name (so two rows of one kind are two entries here); for a legacy,
	// name-less registration it is still the connector's Descriptor name.
	Name string
	// Component is the connector's Descriptor name, kept SEPARATELY: it says which
	// connector serves this source, never which source this is. Two registrations
	// of one kind share it. Inspection and diagnosis read it; nothing keys on it.
	Component string
	// Tenant is the business tenant its observations are stamped with.
	Tenant string
	// Poll is the re-run interval (0 = one-shot/streaming).
	Poll time.Duration
	// Status is the current lifecycle state.
	Status Status
	// Registration is the roster snapshot the source was REGISTERED with (B1):
	// nil for a legacy or merely named registration. It is what the composition
	// root reports as "the revision this node applied" — the fact a binding is
	// validated against — so it comes from the runtime, not from bookkeeping.
	Registration *event.SourceRegistration
}

// LiveSourceInventory returns a snapshot of every source currently registered,
// for a reconciler to diff against its desired set. Safe at any time.
func (r *Runtime) LiveSourceInventory() []SourceInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]SourceInfo, 0, len(r.sources))
	for _, s := range r.sources {
		out = append(out, SourceInfo{Name: s.name, Component: s.component, Tenant: s.tenant, Poll: s.poll, Status: s.status, Registration: s.registration.Clone()})
	}
	return out
}

// SourceIsRegistered reports whether a source is currently registered under this
// REGISTRATION name. A reconciler needs it because a prepared source (and, for a
// plugin, its subprocess) is consumed by whichever of add/replace it is handed to:
// the add-or-rotate decision has to be made before the call, from what is really
// wired, not from the caller's own bookkeeping — which a failed remove can leave
// pointing at nothing.
func (r *Runtime) SourceIsRegistered(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.srcIndex[name]
	return ok
}

// AddSourceLive registers and starts an in-process source connector while the
// engine is running. It mirrors a single iteration of startSources behind
// the reconfigure lock: reserve the name, Open the connector (deny-closed — a
// failed Open is NOT wired and the engine is undisturbed), then launch its gather
// goroutine under a fresh child of runCtx. ctx bounds the Open call only.
func (r *Runtime) AddSourceLive(ctx context.Context, conn sdk.SourceConnector, cfg sdk.Config, tenant string, interval time.Duration) error {
	r.reloadMu.Lock()
	defer r.reloadMu.Unlock()
	return r.addSourceLiveLocked(ctx, conn.Descriptor().Name, conn, cfg, tenant, interval, nil, nil)
}

// AddSourceLiveNamed is AddSourceLive with the registration name supplied by the
// composition root, so a second source of an already-running connector kind is a
// NEW source rather than a duplicate-identity refusal.
func (r *Runtime) AddSourceLiveNamed(ctx context.Context, name string, conn sdk.SourceConnector, cfg sdk.Config, tenant string, interval time.Duration) error {
	if err := validRegistrationName(name); err != nil {
		return err
	}
	r.reloadMu.Lock()
	defer r.reloadMu.Unlock()
	return r.addSourceLiveLocked(ctx, name, conn, cfg, tenant, interval, nil, nil)
}

// ReplaceSourceLive rotates a running source IN PLACE: the connector whose
// Descriptor name matches an existing source is Open()ed first; only if that
// succeeds is the old instance quiesced, closed and replaced — so a bad new
// configuration leaves the old source running (deny-closed). There is a brief
// ingest gap while the old source drains; at-least-once delivery plus idempotent
// natural-key upserts make any duplicate observations during the swap harmless.
func (r *Runtime) ReplaceSourceLive(ctx context.Context, conn sdk.SourceConnector, cfg sdk.Config, tenant string, interval time.Duration) error {
	r.reloadMu.Lock()
	defer r.reloadMu.Unlock()
	return r.replaceSourceLiveLocked(ctx, conn.Descriptor().Name, conn, cfg, tenant, interval, nil, nil)
}

// ReplaceSourceLiveNamed rotates the source REGISTERED UNDER name in place. The
// replacement connector need not be of the same kind: a roster row that changed
// its kind rotates exactly one source — its own — because the registration name,
// not the descriptor, is the identity. Deny-closed as ever: the candidate is
// Open()ed first and a failure leaves the running source untouched.
func (r *Runtime) ReplaceSourceLiveNamed(ctx context.Context, name string, conn sdk.SourceConnector, cfg sdk.Config, tenant string, interval time.Duration) error {
	if err := validRegistrationName(name); err != nil {
		return err
	}
	r.reloadMu.Lock()
	defer r.reloadMu.Unlock()
	return r.replaceSourceLiveLocked(ctx, name, conn, cfg, tenant, interval, nil, nil)
}

// RemoveSourceLive quiesces, closes and unregisters a single running source by
// its REGISTRATION name — the operator's roster name for a named source,
// the connector's Descriptor name for a legacy one. It cancels only that source's gather goroutine,
// waits for it to drain (bounded by ctx — on timeout it closes anyway, matching
// Stop), Closes the connector once, Kills its plugin subprocess if any, and frees
// the name so a source can be re-added under it. Its siblings — including other
// sources of the SAME connector kind — are untouched.
func (r *Runtime) RemoveSourceLive(ctx context.Context, name string) error {
	r.reloadMu.Lock()
	defer r.reloadMu.Unlock()

	r.mu.Lock()
	if !r.started || r.stopped {
		r.mu.Unlock()
		return ErrNotRunning
	}
	reg, ok := r.srcIndex[name]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("%w: %q", ErrSourceNotFound, name)
	}
	// Detach from the live set under the lock so Status()/inventory and a same-name
	// re-add see it gone immediately, and Stop will not also close it.
	r.removeRegLocked(reg)
	r.mu.Unlock()

	r.quiesceAndClose(ctx, reg)
	return nil
}

// PreparedSource is a source connector that has been constructed (and, for a
// plugin, its subprocess launched) but NOT yet wired into the engine. It
// lets a reconciler learn the CONNECTOR's identity (ComponentName) before wiring —
// which is what plugin diagnosis, admission and honest reporting need — while the
// SOURCE's identity stays the registration name the reconciler already holds. A
// prepared source MUST end up either passed to AddPreparedSourceNamed /
// ReplacePreparedSourceNamed (or their legacy twins) or released with Discard, so
// a launched plugin subprocess is never leaked.
type PreparedSource struct {
	rt     *Runtime
	conn   sdk.SourceConnector
	client *goplugin.Client // nil for an in-process source
}

// ComponentName is the connector's Descriptor name — WHICH CONNECTOR this is, not
// which source. An out-of-process plugin self-describes, so this is the first
// point at which the host learns it; it is kept for inspection and diagnosis and
// is never wrapped or faked, because plugin validation and admission depend on it.
func (p *PreparedSource) ComponentName() string { return p.conn.Descriptor().Name }

// Name is the LEGACY accessor for ComponentName, kept for existing callers.
//
// Deprecated: it reads as the source's identity and is not — it is the
// connector's. Use ComponentName for the descriptor; the source's identity is the
// registration name the caller supplies to AddPreparedSourceNamed.
func (p *PreparedSource) Name() string { return p.ComponentName() }

// Discard reaps a prepared source that will not be wired: it kills the plugin
// subprocess (if any) AND releases its confinement (cgroup subtree + dir), so an
// abandoned prepare — an identity collision (reconcile) — never orphans a forked child
// or leaks a cgroup dir until Stop. Safe to call on an in-process prepared source.
func (p *PreparedSource) Discard() {
	if p.client != nil {
		p.client.Kill()
		if p.rt != nil {
			p.rt.RunPluginCleanup(p.client)
		}
	}
}

// PrepareInProcSource wraps an already-constructed in-process connector as a
// PreparedSource (no subprocess; Discard is a no-op).
func (r *Runtime) PrepareInProcSource(conn sdk.SourceConnector) *PreparedSource {
	return &PreparedSource{rt: r, conn: conn}
}

// PrepareSourcePlugin launches a FIRST-PARTY embedded source-connector plugin and
// returns it prepared-but-unwired. The subprocess runs until the prepared
// source is added/replaced or Discarded.
func (r *Runtime) PrepareSourcePlugin(path string) (*PreparedSource, error) {
	conn, client, err := r.dispenseSource(path, nil)
	if err != nil {
		return nil, err
	}
	return &PreparedSource{rt: r, conn: conn, client: client}, nil
}

// PrepareSourcePluginVerified launches an EXTERNAL (third-party) source-connector
// plugin with its sha256 checksum-pinned at exec time and returns it
// prepared-but-unwired. A malformed digest is refused without touching the
// file (a supplied-but-unusable pin never degrades to an unpinned launch).
func (r *Runtime) PrepareSourcePluginVerified(path, sha256Hex string) (*PreparedSource, error) {
	secure, err := secureConfigFor(path, sha256Hex)
	if err != nil {
		return nil, err
	}
	conn, client, err := r.dispenseSource(path, secure)
	if err != nil {
		return nil, err
	}
	return &PreparedSource{rt: r, conn: conn, client: client}, nil
}

// Probe opens the prepared connector with cfg and closes it again WITHOUT wiring
// it into the engine — the "does this source actually answer?" check behind
// `olivares sources test`.
//
// It exercises Open, not a reachability ping, because Open IS the SDK's
// configuration-validation point (sdk.SourceConnector.Open: "a configuration
// error — missing required setting, unreachable target — should be returned
// here"). A probe that only dialed the endpoint would pass configurations the
// engine then refuses, which is the exact lie a test verb exists to prevent.
//
// It deliberately does NOT touch the engine: no name reservation, no schedule,
// no requirement that the runtime be started. And it does NOT Discard — the
// caller still owns the prepared source (for a plugin, a subprocess is running),
// exactly as it does with AddPreparedSource and ReplacePreparedSource.
//
// The returned error wraps ErrSourceOpenFailed on an Open failure so a caller can
// tell "the source refused us" from "the source answered but did not shut down
// cleanly". Both messages can carry connector detail derived from the RESOLVED
// config, so a caller that publishes them must genericize first — the rule
// AddPreparedSource's callers already follow.
func (p *PreparedSource) Probe(ctx context.Context, cfg sdk.Config) error {
	if oerr := safe(func() error { return p.conn.Open(ctx, cfg) }); oerr != nil {
		// Close even after a failed Open: the SDK contract requires Close to be safe
		// there, and a connector that acquired a handle before failing would leak it.
		// It gets the CALLER's context, not context.Background: a probe under a
		// deadline whose Close ignores it is a probe with no deadline.
		_ = safe(func() error { return p.conn.Close(ctx) })
		return fmt.Errorf("%w: %v", ErrSourceOpenFailed, oerr)
	}
	if cerr := safe(func() error { return p.conn.Close(ctx) }); cerr != nil {
		return fmt.Errorf("runtime: the source opened but did not close cleanly after the probe: %v", cerr)
	}
	return nil
}

// AddPreparedSource wires a prepared source into the running engine as a NEW
// source — the name must be free. On failure the prepared subprocess is
// reaped. Deny-closed: a failed Open is not wired and does not disturb the engine.
func (r *Runtime) AddPreparedSource(ctx context.Context, p *PreparedSource, cfg sdk.Config, tenant string, interval time.Duration) error {
	r.reloadMu.Lock()
	defer r.reloadMu.Unlock()
	return r.addSourceLiveLocked(ctx, p.ComponentName(), p.conn, cfg, tenant, interval, p.client, nil)
}

// AddPreparedSourceNamed wires a prepared source into the running engine under the
// registration name the composition root supplies — the roster row's name. The
// name must be free; the connector kind need not be. On failure the prepared
// subprocess is reaped and its confinement released. Deny-closed: a failed Open is
// not wired and does not disturb any other source.
func (r *Runtime) AddPreparedSourceNamed(ctx context.Context, name string, p *PreparedSource, cfg sdk.Config, tenant string, interval time.Duration) error {
	if err := validRegistrationName(name); err != nil {
		p.Discard()
		return err
	}
	r.reloadMu.Lock()
	defer r.reloadMu.Unlock()
	return r.addSourceLiveLocked(ctx, name, p.conn, cfg, tenant, interval, p.client, nil)
}

// AddPreparedSourceRegistered is AddPreparedSourceNamed for a source that comes
// from the DURABLE ROSTER (B1): besides its registration name it carries the
// roster snapshot — the row's persistent id, the revision being applied and the
// applying execution environment — which the host stamps on every event the
// source emits (event.Event.SourceRegistration). The snapshot is fixed at
// registration: the revision recorded is the one whose Open SUCCEEDS here, so a
// failed rotation leaves the previous revision running and stamped. A partial
// snapshot is refused and the prepared source discarded.
func (r *Runtime) AddPreparedSourceRegistered(ctx context.Context, name string, p *PreparedSource, cfg sdk.Config, tenant string, interval time.Duration, reg event.SourceRegistration) error {
	if err := validRegistrationName(name); err != nil {
		p.Discard()
		return err
	}
	if !reg.Valid() {
		p.Discard()
		return ErrInvalidSourceRegistration
	}
	r.reloadMu.Lock()
	defer r.reloadMu.Unlock()
	return r.addSourceLiveLocked(ctx, name, p.conn, cfg, tenant, interval, p.client, &reg)
}

// ReplacePreparedSource rotates a running source IN PLACE with a prepared one of
// the SAME connector identity: the prepared connector is Open()ed first,
// and only on success is the old instance quiesced and replaced (deny-closed).
func (r *Runtime) ReplacePreparedSource(ctx context.Context, p *PreparedSource, cfg sdk.Config, tenant string, interval time.Duration) error {
	r.reloadMu.Lock()
	defer r.reloadMu.Unlock()
	return r.replaceSourceLiveLocked(ctx, p.ComponentName(), p.conn, cfg, tenant, interval, p.client, nil)
}

// ReplacePreparedSourceNamed rotates the source registered under name with a
// prepared one, which may be of a DIFFERENT connector kind (a roster row that
// changed kind rotates only itself). The prepared connector is Open()ed first and
// only on success is the old instance quiesced and replaced (deny-closed).
func (r *Runtime) ReplacePreparedSourceNamed(ctx context.Context, name string, p *PreparedSource, cfg sdk.Config, tenant string, interval time.Duration) error {
	if err := validRegistrationName(name); err != nil {
		p.Discard()
		return err
	}
	r.reloadMu.Lock()
	defer r.reloadMu.Unlock()
	return r.replaceSourceLiveLocked(ctx, name, p.conn, cfg, tenant, interval, p.client, nil)
}

// ReplacePreparedSourceRegistered is ReplacePreparedSourceNamed with the roster
// snapshot of the NEW revision (B1). The running source keeps its own snapshot
// until the replacement's Open succeeds: events it emits meanwhile still carry
// the revision they were produced under, and if the rotation fails they keep
// doing so. A partial snapshot is refused and the prepared source discarded.
func (r *Runtime) ReplacePreparedSourceRegistered(ctx context.Context, name string, p *PreparedSource, cfg sdk.Config, tenant string, interval time.Duration, reg event.SourceRegistration) error {
	if err := validRegistrationName(name); err != nil {
		p.Discard()
		return err
	}
	if !reg.Valid() {
		p.Discard()
		return ErrInvalidSourceRegistration
	}
	r.reloadMu.Lock()
	defer r.reloadMu.Unlock()
	return r.replaceSourceLiveLocked(ctx, name, p.conn, cfg, tenant, interval, p.client, &reg)
}

// --- internals (all run with reloadMu held) ----------------------------------

// dispenseSource launches a source plugin and returns its connector + client,
// reusing the same dispense path the boot loaders use. secure is the exec-time
// integrity pin for an external binary (nil for first-party).
func (r *Runtime) dispenseSource(path string, secure *goplugin.SecureConfig) (sdk.SourceConnector, *goplugin.Client, error) {
	raw, client, err := r.dispense(path, sdkplugin.SourcePluginMap(), sdkplugin.SourcePluginName, secure)
	if err != nil {
		return nil, nil, err
	}
	conn, ok := raw.(sdk.SourceConnector)
	if !ok {
		client.Kill()
		return nil, nil, fmt.Errorf("runtime: plugin %q did not dispense a SourceConnector (%T)", path, raw)
	}
	return conn, client, nil
}

// secureConfigFor decodes an operator-pinned sha256 digest into a go-plugin
// SecureConfig (exec-time re-hash). A malformed digest is refused outright — a
// supplied-but-unusable pin never degrades to an unpinned launch (S142 parity).
func secureConfigFor(path, sha256Hex string) (*goplugin.SecureConfig, error) {
	sum, err := hex.DecodeString(sha256Hex)
	if err != nil || len(sum) != sha256.Size {
		return nil, fmt.Errorf("runtime: external plugin %q: pinned digest is not a sha256 hex digest (a supplied-but-unusable pin refuses, never degrades to an unpinned launch)", path)
	}
	return &goplugin.SecureConfig{Checksum: sum, Hash: sha256.New()}, nil
}

// addSourceLiveLocked is the shared add path for AddSourceLive and the live plugin
// loaders. On any failure it reaps the plugin subprocess (client) so a rejected
// add leaves nothing running. reloadMu MUST be held.
func (r *Runtime) addSourceLiveLocked(ctx context.Context, name string, conn sdk.SourceConnector, cfg sdk.Config, tenant string, interval time.Duration, client *goplugin.Client, registration *event.SourceRegistration) (err error) {
	defer func() {
		if err != nil && client != nil {
			client.Kill()
			r.RunPluginCleanup(client) // release the confinement too, not just Kill
		}
	}()

	// Each registration owns its OWN settings map: a caller that reuses one map for
	// two sources cannot make them share mutable state, and mutating one afterwards
	// cannot reach the other.
	cfg = cloneConfig(cfg)

	r.mu.Lock()
	if !r.started || r.stopped {
		r.mu.Unlock()
		return ErrNotRunning
	}
	// The connector's own identity, checked in the SAME position the descriptor
	// reservation used to occupy: after "is the engine running", before the name is
	// reserved and before Open. A component that does not name itself is refused
	// here, so it is never Opened and never appears in the inventory — and the
	// deferred reap above returns a prepared plugin's subprocess and its confinement.
	if derr := validComponentDescriptor(conn.Descriptor()); derr != nil {
		r.mu.Unlock()
		return derr
	}
	if rerr := r.reserveNameLocked(name); rerr != nil {
		r.mu.Unlock()
		return rerr
	}
	runCtx := r.runCtx
	r.mu.Unlock()

	// Open OUTSIDE r.mu (it may block on the network). Per the SDK contract Open is
	// the configuration-validation point; a failure means the source is NOT wired
	// (deny-closed), and the reserved name is released so a later retry can reuse it.
	if oerr := safe(func() error { return conn.Open(ctx, cfg) }); oerr != nil {
		r.releaseName(name)
		_ = safe(func() error { return conn.Close(context.Background()) })
		return fmt.Errorf("%w: %v", ErrSourceOpenFailed, oerr)
	}

	sctx, scancel := context.WithCancel(runCtx)
	reg := &sourceReg{
		conn: conn, cfg: cfg, tenant: tenant, name: name, component: conn.Descriptor().Name, poll: interval,
		status: StatusRunning, ctx: sctx, cancel: scancel, done: make(chan struct{}), client: client,
		registration: registration.Clone(),
	}

	r.mu.Lock()
	if r.stopped {
		// Lost a race with Stop after Open succeeded: undo cleanly, do not launch.
		r.releaseNameLocked(name)
		r.mu.Unlock()
		scancel()
		_ = safe(func() error { return conn.Close(context.Background()) })
		return ErrStopped
	}
	r.sources = append(r.sources, reg)
	r.srcIndex[name] = reg
	if client != nil {
		r.clients = append(r.clients, client)
	}
	r.wg.Add(1)
	r.mu.Unlock()

	go r.gatherLoop(reg)
	return nil
}

// replaceSourceLiveLocked rotates a source in place: Open the new connector
// first; only on success quiesce+remove the old and launch the new. reloadMu MUST
// be held.
func (r *Runtime) replaceSourceLiveLocked(ctx context.Context, name string, conn sdk.SourceConnector, cfg sdk.Config, tenant string, interval time.Duration, client *goplugin.Client, registration *event.SourceRegistration) (err error) {
	defer func() {
		if err != nil && client != nil {
			client.Kill()
			r.RunPluginCleanup(client) // release the confinement too, not just Kill
		}
	}()

	cfg = cloneConfig(cfg) // per-registration settings, never a shared mutable map

	r.mu.Lock()
	if !r.started || r.stopped {
		r.mu.Unlock()
		return ErrNotRunning
	}
	old, ok := r.srcIndex[name]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("%w: %q", ErrSourceNotFound, name)
	}
	// A malformed CANDIDATE is refused here — after the target is found, before it is
	// Opened and long before the running instance is touched. The order matters twice
	// over: it keeps the legacy answer for a name-less connector (its descriptor is
	// the lookup key, so it misses and reports ErrSourceNotFound, exactly as before),
	// and it guarantees a rotate can never swap a healthy source out for a component
	// that does not name itself. The old instance and every sibling are untouched.
	if derr := validComponentDescriptor(conn.Descriptor()); derr != nil {
		r.mu.Unlock()
		return derr
	}
	runCtx := r.runCtx
	r.mu.Unlock()

	// Validate the NEW connector BEFORE tearing down the old (deny-closed): if Open
	// fails, the old source keeps running and the engine is undisturbed.
	if oerr := safe(func() error { return conn.Open(ctx, cfg) }); oerr != nil {
		_ = safe(func() error { return conn.Close(context.Background()) })
		return fmt.Errorf("%w: %v", ErrSourceOpenFailed, oerr)
	}

	// New is healthy → detach + quiesce the old (frees its name), then wire the new.
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		_ = safe(func() error { return conn.Close(context.Background()) })
		return ErrStopped
	}
	r.removeRegLocked(old)
	r.mu.Unlock()
	r.quiesceAndClose(ctx, old)

	sctx, scancel := context.WithCancel(runCtx)
	reg := &sourceReg{
		conn: conn, cfg: cfg, tenant: tenant, name: name, component: conn.Descriptor().Name, poll: interval,
		status: StatusRunning, ctx: sctx, cancel: scancel, done: make(chan struct{}), client: client,
		registration: registration.Clone(),
	}

	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		scancel()
		_ = safe(func() error { return conn.Close(context.Background()) })
		return ErrStopped
	}
	// The old reg's name was freed by removeRegLocked; re-reserve it for the new.
	if rerr := r.reserveNameLocked(name); rerr != nil {
		r.mu.Unlock()
		scancel()
		_ = safe(func() error { return conn.Close(context.Background()) })
		return rerr
	}
	r.sources = append(r.sources, reg)
	r.srcIndex[name] = reg
	if client != nil {
		r.clients = append(r.clients, client)
	}
	r.wg.Add(1)
	r.mu.Unlock()

	go r.gatherLoop(reg)
	return nil
}

// quiesceAndClose stops one source: cancel its ctx, wait for its gather goroutine
// to drain (bounded by ctx — on timeout close anyway, matching Stop's behavior),
// Close the connector once, and reap its plugin subprocess. Runs WITHOUT r.mu so
// the draining goroutine's status writes (which take r.mu) cannot deadlock it.
func (r *Runtime) quiesceAndClose(ctx context.Context, reg *sourceReg) {
	reg.cancel()
	select {
	case <-reg.done:
	case <-ctx.Done():
		r.log.Warn("runtime: timed out waiting for source to quiesce; closing anyway", "source", reg.name, "component", reg.component, "error", ctx.Err())
	}
	if cerr := safe(func() error { return reg.conn.Close(ctx) }); cerr != nil {
		r.log.Warn("runtime: source close failed during live reconfigure", "source", reg.name, "component", reg.component, "error", cerr)
	}
	if reg.client != nil {
		r.untrackClient(reg.client)
		reg.client.Kill()
		// release the plugin's confinement (cgroup subtree + dir) on live
		// remove too, not only at Stop — otherwise a forked child survives and the
		// cgroup dir leaks until the engine exits (the same gap the external-output
		// teardown closes).
		r.RunPluginCleanup(reg.client)
	}
}

// removeRegLocked unregisters a source from the slice, the index and the name
// reservation. r.mu MUST be held.
func (r *Runtime) removeRegLocked(reg *sourceReg) {
	for i, s := range r.sources {
		if s == reg {
			r.sources = append(r.sources[:i], r.sources[i+1:]...)
			break
		}
	}
	delete(r.srcIndex, reg.name)
	delete(r.names, reg.name)
}

// releaseName frees a reserved component name (used when a live Open fails after
// the name was reserved). releaseNameLocked is the r.mu-held variant.
func (r *Runtime) releaseName(name string) {
	r.mu.Lock()
	delete(r.names, name)
	r.mu.Unlock()
}

func (r *Runtime) releaseNameLocked(name string) { delete(r.names, name) }
