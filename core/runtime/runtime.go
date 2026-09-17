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
	"strings"
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
	// Name is the component's REGISTRATION name — the identity it was registered
	// under. For an output or a module that is still its Descriptor name; for a
	// SOURCE it is the name the composition root supplied (the roster row's name),
	// which is what makes two sources of one connector kind two distinct sources.
	Name string
	// Component is the connector's Descriptor name, retained SEPARATELY for
	// inspection and diagnosis. It is the component's type identity, never its
	// instance identity: two sources of one kind share it.
	Component string
	// Type is the component kind.
	Type sdk.ComponentType
	// Status is the lifecycle state.
	Status Status
	// Err is the failure detail when Status is StatusFailed, else "".
	Err string
}

// SourceRegistrationAdmission is a host-only decision before one observation
// enters the bus. It must return an immutable decision, not defer it to replay.
type SourceRegistrationAdmission func(context.Context, string, event.SourceRegistration) (event.SourceRegistration, error)

// Options configures a Runtime.
type Options struct {
	SourceRegistrationAdmission SourceRegistrationAdmission
	// Logger is the base logger; nil uses slog.Default(). Each component gets a
	// child logger with its name attached.
	Logger *slog.Logger
	// Bus is the event bus to wire components through. nil makes the runtime
	// create an in-process bus that it owns and closes on Stop.
	Bus eventbus.Bus
	// SinkFactory, when set, overrides where a source's observations go: it builds
	// the sdk.Sink handed to each source's Gather, keyed by (tenant, source name).
	// `source` is the source's REGISTRATION name — the operator's roster name for a
	// named registration, the connector's Descriptor name for a legacy one — so a
	// collector transports the same provenance the local bus would stamp.
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

	sinkFactory     func(tenant, source string) sdk.Sink
	sourceAdmission SourceRegistrationAdmission

	mu sync.Mutex
	// names is the ONE registration-name namespace shared by sources, outputs and
	// modules. It is deliberately shared: a source registered under a name a module
	// or an output already owns is REFUSED, never allowed to replace that owner.
	names    map[string]struct{}
	sources  []*sourceReg
	srcIndex map[string]*sourceReg // REGISTRATION name → source reg, for O(1) live lookup
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
	// name is the REGISTRATION name: the identity this source is keyed, reserved,
	// removed, rotated, reported and (for a named registration) ATTRIBUTED by. The
	// composition root supplies it; a legacy caller gets the Descriptor name.
	name string
	// component is the connector's Descriptor name — its TYPE identity, kept apart
	// from name so two registrations of one connector kind are two sources. It is
	// carried for inspection, diagnosis and plugin admission, never for keying.
	component string
	// registration is the durable roster snapshot (row id, applied revision,
	// environment) a REGISTERED source was wired with (B1). The host stamps it on
	// every event this source emits; nil for a legacy or merely named registration,
	// whose events stay unattributed. Immutable for the life of the registration:
	// a rotation is a new sourceReg with its own snapshot.
	registration *event.SourceRegistration
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
		sinkFactory: opts.SinkFactory, sourceAdmission: opts.SourceRegistrationAdmission,
		names:    make(map[string]struct{}),
		srcIndex: make(map[string]*sourceReg),

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

// ErrEmptyRegistrationName is returned by the explicitly-NAMED registration
// variants when the composition root supplies no name. It is deliberately an
// error and not a fallback: a CONFIGURED source registered under its connector's
// descriptor because its own name went missing would silently become "the one
// instance of that kind" and collide with — or be collided with by — its sibling.
// The legacy, name-less entry points are the only ones that may use the
// descriptor as the registration identity, and they say so at the call site.
var ErrEmptyRegistrationName = errors.New("runtime: source registration name is empty (a configured source is never registered under its connector's descriptor by fallback)")

// validComponentDescriptor rejects a connector that does not name ITSELF.
//
// ⛔ THIS IS THE CHECK THE REGISTRATION-NAME SEPARATION LOST, AND LOSING IT WAS A
// REGRESSION, NOT A SIMPLIFICATION. Before the separation, every source path
// reserved the connector's Descriptor name, so `Name == ""` was refused there —
// before Open, before any reservation, before anything was wired. Afterwards the
// paths reserved the REGISTRATION name instead, and "" was simply a name nobody had
// taken yet: a connector with no descriptor name was Opened and registered with an
// empty Name and an empty Component. Astra's independent overlay reproduced exactly
// that (it passes on the baseline and failed on the first commit of this lot).
//
// The two identities stay separate — the registration name is the operator's and is
// never derived from the descriptor — but the CONNECTOR must still identify itself.
// That is the SDK's own requirement (sdk.Descriptor.Name is the component's unique
// id), and nothing stronger is imposed here: no charset, length or shape rule that
// the SDK does not already ask for.
//
// The wording is the historical one, verbatim, because callers and tests read it.
func validComponentDescriptor(d sdk.Descriptor) error {
	if d.Name == "" {
		return errors.New("runtime: component descriptor has empty Name")
	}
	return nil
}

// reserveDescriptorName reserves a component under its own Descriptor name — the
// identity the LEGACY (name-less) registration paths use. Its errors are the
// historical ones, verbatim, because callers and tests read them.
func (r *Runtime) reserveDescriptorName(d sdk.Descriptor) error {
	if err := validComponentDescriptor(d); err != nil {
		return err
	}
	return r.reserveNameLocked(d.Name)
}

// reserveNameLocked reserves one registration name in the runtime's single shared
// namespace. r.mu MUST be held. The namespace is shared across sources, outputs
// and modules ON PURPOSE: a source whose registration name is already owned by a
// module or an output is REFUSED here, so it can never replace that other
// component — the refusal leaves the owner completely untouched.
func (r *Runtime) reserveNameLocked(name string) error {
	if _, dup := r.names[name]; dup {
		return fmt.Errorf("runtime: duplicate component name %q", name)
	}
	r.names[name] = struct{}{}
	return nil
}

// validRegistrationName rejects an absent registration name WITHOUT rewriting a
// present one. The composition root passes the operator's validated, trimmed
// SourceDef.Name and the runtime uses it byte for byte: it is never truncated,
// lower-cased, or derived from the kind, the config or a secret.
// ErrInvalidSourceRegistration refuses a registration snapshot with a missing
// component: a partial snapshot would attribute events to a source nobody can
// resolve, so it is not accepted and the caller's prepared source is discarded.
var ErrInvalidSourceRegistration = errors.New("runtime: source registration snapshot requires source id, applied revision and environment")

func validRegistrationName(name string) error {
	if strings.TrimSpace(name) == "" {
		return ErrEmptyRegistrationName
	}
	return nil
}

// cloneConfig copies a config's settings so each registration owns its OWN map.
// Two sources of one kind must not share a mutable map: mutating one's settings
// can then never reach the other, and a caller that reuses a map for a second
// registration cannot retroactively change the first.
func cloneConfig(cfg sdk.Config) sdk.Config {
	if cfg.Settings == nil {
		return sdk.Config{}
	}
	out := make(map[string]string, len(cfg.Settings))
	for k, v := range cfg.Settings {
		out[k] = v
	}
	return sdk.Config{Settings: out}
}

// AddSource registers an in-process source connector that the engine runs once:
// a streaming source blocks in Gather until Stop; a batch source runs to
// completion and is not re-run. cfg is the connector's configuration; tenant is
// the string tenant reference stamped onto the events its observations produce.
// It is equivalent to AddPollSource with a zero interval.
//
// It is the LEGACY, name-less registration: the connector's Descriptor name IS
// the registration identity, so only one source per connector kind can exist.
// A composition root that registers CONFIGURED sources uses AddSourceNamed /
// AddPollSourceNamed instead and supplies the operator's name.
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
	if err := r.reserveDescriptorName(d); err != nil {
		return err
	}
	r.wireSourceLocked(d.Name, conn, cfg, tenant, interval)
	return nil
}

// AddSourceNamed is AddSource with an EXPLICIT registration name supplied by the
// composition root — the identity this source is keyed, reported, removed and
// attributed by, independent of which connector kind serves it. It is
// AddPollSourceNamed with a zero interval.
func (r *Runtime) AddSourceNamed(name string, conn sdk.SourceConnector, cfg sdk.Config, tenant string) error {
	return r.AddPollSourceNamed(name, conn, cfg, tenant, 0)
}

// AddPollSourceNamed registers a source under the registration name the caller
// supplies instead of under its connector's Descriptor name. That separation is
// the whole point: two roster rows of ONE kind ("grok-home-a" and "grok-home-b")
// are two distinct sources with their own connector instance, their own config,
// their own lifecycle and their own event provenance, because the runtime keys
// them by the operator's name and keeps the descriptor beside it as type
// information. A duplicate registration name is still refused, and a name already
// owned by a module or an output is refused too — without disturbing that owner.
//
// The name is used verbatim (see validRegistrationName); an empty one is an error,
// never a silent fall back to the descriptor. The CONNECTOR must still name itself
// (validComponentDescriptor): an explicit registration identity does not admit a
// component that has none.
func (r *Runtime) AddPollSourceNamed(name string, conn sdk.SourceConnector, cfg sdk.Config, tenant string, interval time.Duration) error {
	if err := validRegistrationName(name); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return ErrAlreadyStarted
	}
	// The connector still has to identify itself: naming the SOURCE never excuses a
	// component with no descriptor name. Checked before the reservation, so a refused
	// connector leaves the namespace exactly as it found it.
	if err := validComponentDescriptor(conn.Descriptor()); err != nil {
		return err
	}
	if err := r.reserveNameLocked(name); err != nil {
		return err
	}
	r.wireSourceLocked(name, conn, cfg, tenant, interval)
	return nil
}

// wireSourceLocked appends a reserved pre-Start source registration. r.mu MUST be
// held and the name MUST already be reserved.
func (r *Runtime) wireSourceLocked(name string, conn sdk.SourceConnector, cfg sdk.Config, tenant string, interval time.Duration) {
	reg := &sourceReg{
		conn: conn, cfg: cloneConfig(cfg), tenant: tenant,
		name: name, component: conn.Descriptor().Name, poll: interval, status: StatusPending,
	}
	r.sources = append(r.sources, reg)
	r.srcIndex[name] = reg
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
	if err := r.reserveDescriptorName(d); err != nil {
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
	if err := r.reserveDescriptorName(d); err != nil {
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
		// A source reports BOTH identities: the registration name it answers to and
		// the connector descriptor that serves it. Two rows of one kind therefore
		// appear as two entries that share a Component and differ in Name.
		cs := statusOf(s.name, sdk.TypeSource, s.status, s.err)
		cs.Component = s.component
		out = append(out, cs)
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
	cs := ComponentStatus{Name: name, Component: name, Type: t, Status: st}
	if err != nil {
		cs.Err = err.Error()
	}
	return cs
}
