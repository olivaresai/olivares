// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package recording

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// Name is the module's globally unique identifier (the runtime registry key).
const Name = "olivares.recording"

// Namespace is the module's store and API namespace: its entities are
// "recording.<entity>" and its routes mount under /v1/m/recording/.
const Namespace = "recording"

// The module's permissions. Viewing a recording is at LEAST as sensitive as the
// access graph (it is the operators' own activity), so every session read is
// deliberately admin-verb — the module verb tiers would hand a ":read" to the
// lowest viewer role. The notice/ack pair is read-verb on purpose: it must be
// reachable by EVERY role that can touch any recorded surface (viewers can read
// governance lists), it only discloses the caller's own recording posture, and
// the ack writes nothing but the caller's own consent instant (self-scoped,
// self-audited).
const (
	permNoticeRead   auth.Permission = "recording:notice:read"
	permSessionAdmin auth.Permission = "recording:session:admin"
	permConfigAdmin  auth.Permission = "recording:config:admin"
)

// breakGlassPermPrefix marks the MANDATORY recording floor: every route guarded
// by a break-glass permission is recorded for every principal kind, regardless
// of tenant configuration (an emergency window without its recording is
// exactly what SEC-G5 forbids).
const breakGlassPermPrefix = "governance:breakglass:"

// defaultNamespaces is the default recorded surface set for HUMAN operators:
// governance admin (approvals, roster, policy, break-glass, NHI), the policy-
// authoring consoles, the identity/WIF viewer, the access-graph viewer (the
// most recon-sensitive read surface, docs/SECURITY-HARDENING.md) and this module itself —
// watching a recording is a privileged act that is itself recorded. Tenant
// admins may narrow or extend the set (minimal scope); the break-glass floor
// is not configurable. Deliberately NOT "record everything": scoping recording
// to privileged surfaces is the documented PAM practice.
var defaultNamespaces = []string{
	"governance", "claude-policy", "claude-agents", "identity", "accessmap", "recording",
}

// Consent modes (NIST SP 800-53 AC-8 system-use notification).
const (
	// consentNotice (default): the console shows the recording notice; the first
	// privileged action is itself the acknowledgement (use-implies-consent, the
	// AC-8 banner pattern commercial PAM ships).
	consentNotice = "notice"
	// consentRequired: deny-closed — the operator must explicitly POST /ack
	// before any privileged action (403 recording_consent_required until then).
	// Stricter than commercial PAM defaults; the high-assurance dial.
	consentRequired = "required"
)

// Session lifecycle.
const (
	statusActive = "active"
	statusSealed = "sealed"
)

// Seal reasons.
const (
	sealReasonIdle    = "idle"
	sealReasonClosed  = "closed"
	sealReasonReview  = "breakglass_review"
	sealReasonSweep   = "sweep"
	sealReasonConsent = "consent_change"
)

// retentionClass tags every session for the future retention/legal-hold
// engine (this module implements no purge and no hold).
const retentionClass = "privileged-session-recording"

// Defaults for the per-tenant config (no row = these).
const (
	defaultIdleSeconds   = int64(30 * 60)
	defaultRetentionDays = int64(180) // the documented commercial PAM default
	// anchorEvery is the periodic ledger-anchor cadence in frames: long sessions
	// stay anchored even before their seal, without one ledger append per frame
	// (ledger appends serialize per tenant).
	anchorEvery = int64(25)
	// maxAppendRetries bounds the optimistic retry of session-row updates when
	// concurrent requests race on the version counter. Paired with retrySleep's
	// capped backoff so a benign parallel burst on one credential's hot row (the
	// Bridge fanning out break-glass consumes) resolves instead of 503ing.
	maxAppendRetries = 10
)

// Finding kinds emitted on the notification rail.
const (
	findingRecordingGap         = "recording_gap"
	findingRecordingUnavailable = "recording_unavailable"
	findingRecordingSealFailed  = "recording_seal_failed"
	findingRoutineFired         = "routine_fired"
)

// Ledger actions (free-form by convention; constants so a typo cannot seal into
// the immutable chain). The readable binding rides TargetKind/TargetID and, for
// chain anchors, PayloadHash — never Meta (write-only on read paths).
const (
	actionSessionOpen   = "recording.session.open"
	actionSessionAnchor = "recording.session.anchor"
	actionSessionSeal   = "recording.session.seal"
	actionConsentAck    = "recording.consent.ack"
	actionSessionRead   = "recording.read"
	actionSessionReplay = "recording.session.replay"
	actionSessionVerify = "recording.session.verify"
	actionSummarize     = "recording.session.summarize"
	actionConfigUpdate  = "recording.config.update"
	actionGrantBind     = "recording.session.bind"
	actionSweep         = "recording.sweep"
	actionRoutineFire   = "recording.routine.fire"
)

// Summarizer is the module's OWN port for the optional Claude-backed session
// summary (the composition root wires an adapter over the inference seam;
// modules never import sibling modules). nil = honest 501. The output is a
// DERIVED artifact: stored beside the session, marked derived, never evidence.
type Summarizer interface {
	// Summarize produces a short reviewer summary of a human-readable session
	// transcript. It must never receive secrets (frames are redacted by
	// construction) and must treat the transcript as untrusted content.
	Summarize(ctx context.Context, tenant model.TenantID, transcript string) (string, error)
}

// TimelineResolver resolves the operational timeline correlated to a recording
// session's credential. The composition root wires it to the sessions module's
// TimelineByCredential seam. The default is a no-op resolver that returns no
// timeline (honest degradation when the sessions module is not wired). Cursor
// values are opaque to recording; an empty cursor starts the timeline.
type TimelineResolver interface {
	ResolveTimeline(ctx context.Context, tenant model.TenantID, cred string, limit int, cursor string) (sessionRef string, timeline []TimelineEntry, nextCursor string, hasMore bool, err error)
}

// TimelineEntry is a single operational event in the correlated timeline.
type TimelineEntry struct {
	At          time.Time `json:"at"`
	Kind        string    `json:"kind"`
	ToolRef     string    `json:"tool_ref,omitempty"`
	ResourceRef string    `json:"resource_ref,omitempty"`
	Mode        string    `json:"mode,omitempty"`
	Source      string    `json:"source,omitempty"`
	Title       string    `json:"title,omitempty"`
}

// noopTimelineResolver is the deny-closed default: no correlated timeline.
type noopTimelineResolver struct{}

func (noopTimelineResolver) ResolveTimeline(context.Context, model.TenantID, string, int, string) (string, []TimelineEntry, string, bool, error) {
	return "", nil, "", false, nil
}

// WithTimelineResolver wires the operational timeline cross-module seam.
func WithTimelineResolver(r TimelineResolver) Option {
	return func(m *Module) {
		if r == nil {
			return
		}
		m.timelineResolver = r
		m.timelineResolverConfigured = true
	}
}

// Option configures a Module at construction.
type Option func(*Module)

// WithClock overrides the module clock (tests inject a deterministic clock).
func WithClock(c model.Clock) Option {
	return func(m *Module) { m.clock = c }
}

// WithSummarizer wires the optional AI session summarizer.
func WithSummarizer(s Summarizer) Option {
	return func(m *Module) { m.summarizer = s }
}

// Module is the privileged-session recording subsystem (see doc.go).
type Module struct {
	log              *slog.Logger
	data             api.ModuleData
	host             sdk.Host
	clock            model.Clock
	summarizer       Summarizer
	timelineResolver TimelineResolver
	// Distinguishes a successfully empty wired timeline from the default noop.
	timelineResolverConfigured bool

	mu     sync.Mutex
	cancel func() // bus unsubscribe (break-glass post-review seal)

	// cfgMu guards the per-tenant config cache the hot Gate path reads. Entries
	// expire by clock so a PUT /config on another node converges within the TTL;
	// cfgEpoch invalidates in-flight loads so a local write is never re-cached
	// over by a racing stale read.
	cfgMu    sync.Mutex
	cfgCache map[model.TenantID]cachedConfig
	cfgEpoch map[model.TenantID]int64

	// knownNS is the set of module API namespaces actually mounted (wired by the
	// composition root via UseKnownNamespaces). PUT /config validates the
	// recorded-namespace list against it so a typo cannot silently un-record a
	// surface; empty = validation by shape only (embedders/tests).
	knownNS map[string]bool
}

// Compile-time proof the module satisfies the SDK lifecycle, the engine-side
// schema seam, the API route/permission seam, the data-consumer seam and the
// core/api recording seam.
var (
	_ sdk.Module          = (*Module)(nil)
	_ api.Module          = (*Module)(nil)
	_ api.DataConsumer    = (*Module)(nil)
	_ api.SessionRecorder = (*Module)(nil)
)

// New returns a recording module with a system clock; the engine wires the data
// handle via UseData before Start.
func New(opts ...Option) *Module {
	m := &Module{
		clock:            model.SystemClock{},
		cfgCache:         map[model.TenantID]cachedConfig{},
		cfgEpoch:         map[model.TenantID]int64{},
		timelineResolver: noopTimelineResolver{},
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

// Descriptor returns the module's self-description.
func (m *Module) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeModule,
		Title:       "Privileged session recording",
		Description: "Immutable, ledger-anchored recording and forensic replay of privileged operator sessions (governance, policy authoring, access-graph viewing) with a mandatory floor for break-glass — hash-chained frames, AC-8 notice/consent, redaction by construction, deny-closed capture (SEC-G5).",
	}
}

// UseData receives the least-privilege, tenant-parameterized data handle from
// the engine boot (api.DataConsumer), before Start.
func (m *Module) UseData(d api.ModuleData) { m.data = d }

// UseKnownNamespaces wires the set of module API namespaces actually mounted,
// so the recorded-namespace config is validated against reality (a typo'd
// namespace would otherwise silently un-record a surface — fail-open on the
// watch layer). Called by the composition root after the module set is built.
func (m *Module) UseKnownNamespaces(ns []string) {
	set := make(map[string]bool, len(ns))
	for _, n := range ns {
		set[n] = true
	}
	m.knownNS = set
}

// Init keeps the host for findings and subscribes to finding.reported. A
// break-glass review now seals its recording in the review transaction; the
// reviewed finding is notification plus idempotent legacy reconciliation, not
// the mechanism that makes the review true.
func (m *Module) Init(_ context.Context, host sdk.Host) error {
	m.log = host.Logger()
	m.host = host
	cancel, err := host.Subscribe([]event.Type{event.TypeFindingReported}, func(ctx context.Context, e event.Event) error {
		m.onFinding(ctx, e)
		return nil
	})
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.cancel = cancel
	m.mu.Unlock()
	return nil
}

// Start has no background work (idle sessions seal lazily on the next gate, by
// the sweep endpoint, or at break-glass review). It only warns when the data
// handle was never wired, so a silently-broken deployment is visible.
func (m *Module) Start(context.Context) error {
	if m.data == nil && m.log != nil {
		m.log.Warn("recording: started without a data handle; privileged surfaces will be DENY-CLOSED (no evidence, no action)")
	}
	return nil
}

// Stop unsubscribes from the bus.
func (m *Module) Stop(context.Context) error {
	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

// APINamespace roots the module's routes at /v1/m/recording/.
func (m *Module) APINamespace() string { return Namespace }

// Permissions declares the module's permissions so roles grant them by verb
// tier (notice/ack at read so every operator can acknowledge; everything else
// admin — recordings are operator activity, at least as sensitive as the
// access graph).
func (m *Module) Permissions() []auth.Permission {
	return []auth.Permission{permNoticeRead, permSessionAdmin, permConfigAdmin}
}

// APIRoutes mounts the module's routes.
func (m *Module) APIRoutes(reg api.RouteRegistrar) {
	reg.Handle("GET", "/notice", permNoticeRead, m.handleNotice)
	reg.Handle("POST", "/ack", permNoticeRead, m.handleAck)
	reg.Handle("GET", "/sessions", permSessionAdmin, m.handleListSessions)
	reg.Handle("GET", "/sessions/{id}", permSessionAdmin, m.handleGetSession)
	reg.Handle("GET", "/sessions/{id}/replay", permSessionAdmin, m.handleReplay)
	reg.Handle("GET", "/sessions/{id}/unified", permSessionAdmin, m.handleUnified)
	reg.Handle("GET", "/sessions/{id}/verify", permSessionAdmin, m.handleVerify)
	reg.Handle("GET", "/sessions/{id}/export", permSessionAdmin, m.handleExport)
	reg.Handle("POST", "/sessions/{id}/seal", permSessionAdmin, m.handleSeal)
	reg.Handle("POST", "/sessions/{id}/summarize", permSessionAdmin, m.handleSummarize)
	reg.Handle("POST", "/sweep", permSessionAdmin, m.handleSweep)
	reg.Handle("GET", "/config", permConfigAdmin, m.handleGetConfig)
	reg.Handle("PUT", "/config", permConfigAdmin, m.handlePutConfig)
}

// onFinding reconciles a break-glass grant's bound recording when its
// post-review notification lands (governance emits
// governance_breakglass_reviewed with the grant id as SubjectRef). The normal
// atomic path is already sealed, so this is an idempotent no-op there.
func (m *Module) onFinding(ctx context.Context, e event.Event) {
	if m.data == nil {
		return
	}
	f, ok := event.FindingOf(e)
	if !ok {
		return
	}
	switch f.Kind {
	case "governance_breakglass_reviewed":
		tenant, ok := tenantOf(e.Tenant)
		if !ok {
			return
		}
		grant := strings.TrimSpace(f.SubjectRef)
		if grant == "" {
			return
		}
		if err := m.sealBoundSessions(ctx, tenant, grant); err != nil {
			m.errorf("recording: sealing break-glass-bound session failed", "grant", grant, "err", err)
		}
	case findingRoutineFired:
		m.onRoutineFired(ctx, e, f)
	}
}

// onRoutineFired appends a ledger event when a routine fires, so the
// tamper-evident audit carries evidence of autonomous execution events. The
// ledger event is append-only and carries only the routine ref (SubjectRef)
// and tenant — never the prompt or session content (minimal data, docs/SECURITY-HARDENING.md).
func (m *Module) onRoutineFired(ctx context.Context, e event.Event, f sdkmodel.FindingReport) {
	tenant, ok := tenantOf(e.Tenant)
	if !ok {
		return
	}
	routineRef := strings.TrimSpace(f.SubjectRef)
	if routineRef == "" {
		return
	}
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, aerr := sc.Audit().Append(ctx, model.AuditDraft{
			Actor: "system:recording", ActorKind: "system",
			Action:     actionRoutineFire,
			TargetKind: "routine",
			TargetID:   model.ID(routineRef),
		})
		return aerr
	})
	if err != nil {
		m.errorf("recording: ledger append for routine fire failed", "routine", routineRef, "err", err)
	}
}

// emitFinding publishes a recording finding on the notification rail (minimal
// data: ids and a fixed title; details hashed).
func (m *Module) emitFinding(ctx context.Context, tenant model.TenantID, kind, sessionID string, sev sdkmodel.Severity, title string) {
	if m.host == nil {
		return
	}
	sum := sha256.Sum256([]byte(kind + "|" + sessionID))
	finding := sdkmodel.FindingReport{
		Kind:        kind,
		Severity:    sev,
		SubjectKind: "recording_session",
		SubjectRef:  sessionID,
		Title:       title,
		DetailHash:  hex.EncodeToString(sum[:]),
		OccurredAt:  m.clock.Now().Time(),
	}
	if err := m.host.Publish(ctx, event.FromObservation(tenant.String(), Name, finding)); err != nil {
		m.errorf("recording: emit finding failed", "kind", kind, "err", err)
	}
}

// tenantOf parses a bus tenant ref, rejecting zero/system.
func tenantOf(ref string) (model.TenantID, bool) {
	t, err := model.ParseTenantID(ref)
	if err != nil || t.IsZero() || t.IsSystem() {
		return "", false
	}
	return t, true
}

// errorf logs at error level if a logger is set.
func (m *Module) errorf(msg string, args ...any) {
	if m.log != nil {
		m.log.Error(msg, args...)
	}
}
