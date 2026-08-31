// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package runtime is the engine's module/connector host. It registers in-process
// components and loads out-of-process plugins (hashicorp/go-plugin over gRPC),
// then wires them through the event bus: a source's observations become events,
// modules and output connectors react, and the whole graph starts and stops as
// one. A faulty component is isolated — a panicking in-process Gather or a
// crashing plugin is logged and marked failed, never allowed to take down the
// engine (ARCHITECTURE.md).
//
// The runtime imports the Apache SDK (and the Apache sdk/plugin transport) and
// the engine's store interfaces. Connectors never see it; they see only ./sdk.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	goplugin "github.com/hashicorp/go-plugin"

	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	"github.com/olivaresai/olivares/sdk/model"
)

// Status is the lifecycle state of a registered component.
type Status string

// The component statuses.
const (
	// StatusPending means registered but not yet started.
	StatusPending Status = "pending"
	// StatusRunning means started and healthy.
	StatusRunning Status = "running"
	// StatusStopped means cleanly stopped.
	StatusStopped Status = "stopped"
	// StatusFailed means it errored or panicked; it is isolated, not retried (S02).
	StatusFailed Status = "failed"
)

// ComponentStatus is a snapshot of one component's state, surfaced for health
// views and tests.
type ComponentStatus struct {
	// Name is the component's Descriptor name.
	Name string
	// Type is the component kind.
	Type sdk.ComponentType
	// Status is the lifecycle state.
	Status Status
	// Err is the failure detail when Status is StatusFailed, else "".
	Err string
}

// Options configures a Runtime.
type Options struct {
	// Logger is the base logger; nil uses slog.Default(). Each component gets a
	// child logger with its name attached.
	Logger *slog.Logger
	// Bus is the event bus to wire components through. nil makes the runtime
	// create an in-process bus that it owns and closes on Stop.
	Bus eventbus.Bus
	// SinkFactory, when set, overrides where a source's observations go: it builds
	// the sdk.Sink handed to each source's Gather, keyed by (tenant, source name).
	// nil (the default) lifts observations onto the local event bus. A COLLECTOR
	// process sets this to a factory that PUSHES observations to a remote core over
	// gRPC (CB-1 option C, sdk/plugin.IngestSink), so the very same gatherLoop,
	// scheduler and failure isolation drive a single-node engine and a distributed
	// collector identically — B is the substrate of C.
	SinkFactory func(tenant, source string) sdk.Sink
}

// Runtime hosts and wires components. It is safe for concurrent registration
// before Start. The lifecycle is single-threaded by contract: Start is called
// once, and Stop once and only after Start has returned (they must not overlap).
//
// Adds exactly ONE further mutation path on top of that contract: live
// source reconfiguration (AddSourceLive / ReplaceSourceLive / RemoveSourceLive
// and the *PluginLive twins). Those calls run AFTER Start, are single-flighted
// against each other and against Stop by reloadMu, and touch only the source
// set — outputs, modules and schedules stay sealed at Start. They never run
// concurrently with each other or with Stop, so the "no overlapping graph
// edits" guarantee holds; the graph still starts and stops as one.
type Runtime struct {
	log    *slog.Logger
	bus    eventbus.Bus
	ownBus bool

	sinkFactory func(tenant, source string) sdk.Sink

	mu       sync.Mutex
	names    map[string]struct{}
	sources  []*sourceReg
	srcIndex map[string]*sourceReg // name → source reg, for O(1) live lookup
	outputs  []*outputReg
	modules  []*moduleReg
	jobs     []*jobReg          // engine-owned periodic jobs (e.g. roster SyncRoster)
	clients  []*goplugin.Client // out-of-process plugin clients to Kill on Stop
	// pluginCleanupByClient releases per-plugin confinement resources (e.g. the cgroup
	// dir plugjail allocated) after the plugin's client is killed. Keyed by client so a
	// LIVE teardown (external-output reload/remove source live-remove) can
	// reclaim it immediately via RunPluginCleanup; whatever remains is drained at Stop.
	pluginCleanupByClient map[*goplugin.Client]func()
	// standaloneOutputs are output plugins opened by a composition-root caller
	// without registering them on the event bus (for example notify destinations).
	// Stop still owes them the SDK Close lifecycle before their clients are killed.
	standaloneOutputs []sdk.OutputConnector
	started           bool
	stopped           bool

	// reloadMu serializes live source reconfiguration: the post-Start
	// add/remove/rotate primitives single-flight against each other AND against
	// Stop, so the single-threaded-by-contract lifecycle gains exactly one extra,
	// serialized mutation path — never concurrent graph edits. It is held only by
	// the *Live methods; the running gather goroutines never touch it, so a live
	// remove can wait on a source's drain (which takes r.mu) without deadlock.
	reloadMu sync.Mutex

	// runCtx bounds the lifetime of source Gather goroutines; canceled on Stop.
	// Each source's gather goroutine runs under a PER-SOURCE child of runCtx
	// (sourceReg.ctx), so Stop's single cancel still cascades to every source
	// (the graph still "stops as one"), yet one source can be canceled alone for
	// a live remove/rotate.
	runCtx context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

type sourceReg struct {
	conn   sdk.SourceConnector
	cfg    sdk.Config
	tenant string
	name   string
	// poll is the re-run interval for a BATCH/polling source. 0 means run Gather
	// once (a one-shot or streaming source that blocks in Gather is never
	// re-polled — the scheduler owns the cadence, not the connector, S02 §5).
	poll   time.Duration
	status Status
	err    error
	// ctx/cancel bound THIS source's gather goroutine — a child of r.runCtx set at
	// Start (startSources) or at live-add. Canceling it stops only this
	// source; canceling the parent (Stop) stops every source. done is closed by
	// gatherLoop on exit, so a live remove can wait for exactly this goroutine to
	// drain (not the whole engine's WaitGroup). client is the out-of-process
	// plugin process backing this source (nil for an in-process source), Killed on
	// a live remove so a removed plugin source leaves no orphan subprocess.
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	client *goplugin.Client
}

// jobReg is an engine-owned periodic job: non-Gather work (e.g. the governance
// roster SyncRoster) the runtime re-runs on the same scheduler.
type jobReg struct {
	name      string
	interval  time.Duration
	immediate bool
	fn        func(context.Context) error
}

type outputReg struct {
	conn   sdk.OutputConnector
	cfg    sdk.Config
	types  []event.Type
	name   string
	sub    eventbus.Subscription
	status Status
	err    error
}

type moduleReg struct {
	mod    sdk.Module
	cfg    sdk.Config
	name   string
	host   *moduleHost
	status Status
	err    error
}

// New creates a runtime. If opts.Bus is nil it builds and owns an in-process bus.
func New(opts Options) *Runtime {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	bus := opts.Bus
	ownBus := false
	if bus == nil {
		bus = eventbus.NewInProc(eventbus.Options{Logger: log})
		ownBus = true
	}
	return &Runtime{
		log:         log,
		bus:         bus,
		ownBus:      ownBus,
		sinkFactory: opts.SinkFactory,
		names:       make(map[string]struct{}),
		srcIndex:    make(map[string]*sourceReg),

		pluginCleanupByClient: make(map[*goplugin.Client]func()),
	}
}

// ErrAlreadyStarted is returned by registration calls after Start.
var ErrAlreadyStarted = errors.New("runtime: already started")

// ErrNotRunning is returned by a live reconfiguration call when the
// runtime has not been started, or has already been stopped — live source
// mutation is legal only while the engine is running.
var ErrNotRunning = errors.New("runtime: not running")

// ErrSourceNotFound is returned by a live remove/rotate when no source is
// registered under the given name.
var ErrSourceNotFound = errors.New("runtime: no such source")

// ErrSourceOpenFailed wraps a connector's Open error on a live add/rotate.
// Open runs against the RESOLVED config (live secret values substituted in), so a
// connector that echoes a credential into its Open error would carry it; callers
// that surface a failure to a user/log MUST treat this case generically and never
// render the wrapped detail (the secret-resolver error is genericized the same
// way). Match with errors.Is.
var ErrSourceOpenFailed = errors.New("runtime: source open failed")

// reserveName validates and reserves a component's unique Descriptor name.
func (r *Runtime) reserveName(d sdk.Descriptor) error {
	if d.Name == "" {
		return errors.New("runtime: component descriptor has empty Name")
	}
	if _, dup := r.names[d.Name]; dup {
		return fmt.Errorf("runtime: duplicate component name %q", d.Name)
	}
	r.names[d.Name] = struct{}{}
	return nil
}

// AddSource registers an in-process source connector that the engine runs once:
// a streaming source blocks in Gather until Stop; a batch source runs to
// completion and is not re-run. cfg is the connector's configuration; tenant is
// the string tenant reference stamped onto the events its observations produce.
// It is equivalent to AddPollSource with a zero interval.
func (r *Runtime) AddSource(conn sdk.SourceConnector, cfg sdk.Config, tenant string) error {
	return r.AddPollSource(conn, cfg, tenant, 0)
}

// AddPollSource registers a BATCH/polling source the engine RE-RUNS every
// interval (the scheduler is the engine's, not the connector's — S02 §5). A
// positive interval is for sampling sources (admin-API pulls, audit polls) that
// return nil from each Gather pass; the engine waits interval and runs Gather
// again, with at-least-once delivery (consumers de-dup on the observation's
// natural key). On a Gather error a polling source is retried with exponential
// backoff (base = interval, capped) and stays Running with its last error
// recorded, rather than being left down. interval<=0 is identical to AddSource
// (run once); a streaming source must use interval<=0 — it owns its own blocking
// loop in Gather and is never re-polled.
func (r *Runtime) AddPollSource(conn sdk.SourceConnector, cfg sdk.Config, tenant string, interval time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return ErrAlreadyStarted
	}
	d := conn.Descriptor()
	if err := r.reserveName(d); err != nil {
		return err
	}
	reg := &sourceReg{conn: conn, cfg: cfg, tenant: tenant, name: d.Name, poll: interval, status: StatusPending}
	r.sources = append(r.sources, reg)
	r.srcIndex[d.Name] = reg
	return nil
}

// SchedulePeriodic registers a named job the engine runs every interval after
// Start, each pass on a panic-isolated goroutine the runtime owns and cancels via
// ctx on Stop. It is the SAME scheduler the polling sources use, for non-Gather
// periodic work — the governance roster SyncRoster is the first caller. When
// runImmediately is set the engine runs one pass at Start before waiting the
// first interval (so the roster populates promptly), then every interval. A job
// error or panic is logged and the schedule continues — a transient directory
// outage must not kill the schedule. It must be called before Start.
func (r *Runtime) SchedulePeriodic(name string, interval time.Duration, runImmediately bool, job func(context.Context) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return ErrAlreadyStarted
	}
	if name == "" {
		return errors.New("runtime: periodic job has empty name")
	}
	if interval <= 0 {
		return fmt.Errorf("runtime: periodic job %q needs a positive interval", name)
	}
	if job == nil {
		return fmt.Errorf("runtime: periodic job %q has a nil func", name)
	}
	r.jobs = append(r.jobs, &jobReg{name: name, interval: interval, immediate: runImmediately, fn: job})
	return nil
}

// Ingest lifts one observation PUSHED by a remote collector onto the event bus,
// exactly as an in-process source's Sink would (CB-1 option C, ARCHITECTURE.md). It is
// the core side of the distributed ingest plane: the IngestService server decodes
// a pushed envelope and calls this, so a collector's stream and an in-host
// source's Gather converge on the same bus. It satisfies sdk/plugin.IngestHandler.
// Authorization of (tenant) happens in the gRPC layer before this is called; here
// the runtime only stamps and publishes. A source name is required for provenance;
// an empty one defaults to "collector".
func (r *Runtime) Ingest(ctx context.Context, tenant, source string, obs model.Observation) error {
	if source == "" {
		source = "collector"
	}
	return (&busSink{bus: r.bus, tenant: tenant, source: source}).Emit(ctx, obs)
}

// AddOutput registers an in-process output connector. types restricts which
// event types it is notified of; nil/empty means every event.
func (r *Runtime) AddOutput(conn sdk.OutputConnector, cfg sdk.Config, types []event.Type) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return ErrAlreadyStarted
	}
	d := conn.Descriptor()
	if err := r.reserveName(d); err != nil {
		return err
	}
	r.outputs = append(r.outputs, &outputReg{conn: conn, cfg: cfg, types: types, name: d.Name, status: StatusPending})
	return nil
}

// AddModule registers an in-process module.
func (r *Runtime) AddModule(mod sdk.Module, cfg sdk.Config) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return ErrAlreadyStarted
	}
	d := mod.Descriptor()
	if err := r.reserveName(d); err != nil {
		return err
	}
	r.modules = append(r.modules, &moduleReg{mod: mod, cfg: cfg, name: d.Name, status: StatusPending})
	return nil
}

// Status returns a snapshot of every registered component's state.
func (r *Runtime) Status() []ComponentStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ComponentStatus, 0, len(r.sources)+len(r.outputs)+len(r.modules))
	for _, s := range r.sources {
		out = append(out, statusOf(s.name, sdk.TypeSource, s.status, s.err))
	}
	for _, o := range r.outputs {
		out = append(out, statusOf(o.name, sdk.TypeOutput, o.status, o.err))
	}
	for _, m := range r.modules {
		out = append(out, statusOf(m.name, sdk.TypeModule, m.status, m.err))
	}
	return out
}

func statusOf(name string, t sdk.ComponentType, st Status, err error) ComponentStatus {
	cs := ComponentStatus{Name: name, Type: t, Status: st}
	if err != nil {
		cs.Err = err.Error()
	}
	return cs
}
