// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

const (
	workOutboxPumpJobName     = "sessions-work-outbox"
	workOutboxPumpIntervalEnv = "OLIVARES_WORK_OUTBOX_INTERVAL"
	defaultWorkOutboxInterval = 15 * time.Second
)

// workOutboxPump is the local durable pump for the sessions outbox. It is one
// registered lifecycle object with two lanes:
//
//   - K1/K2 (work items, leases, ownership, protocol bindings) recover
//     unconditionally on every leader tick, exactly as before K3 existed.
//   - K3 (messages, deliveries, handoff carriers, decision requests) is claimed
//     only when the module-bound outbox authority below allows it NOW: the
//     communication witness is live (attached, registered, not stopped, this
//     node leads), the module's EFFECTIVE readiness is on, and the durable
//     leadership fence (store.EpochFencer.FencedEpoch) verifies. The authority
//     is sampled per candidate at the claim boundary and again at the effect
//     boundary; a per-tick sample or a cached Active() flag never authorizes
//     a K3 effect.
//
// The authority is bound on the sessions module by the composition, so the
// Apply nudge and the public drain run under the same gate as this pump; the
// pump only adds accounting for the operator's log. Both lanes share the sink:
// the real Eventing durable intake bound at boot. There is no stub notifier
// and no constant witness anywhere on this path.
type workOutboxPump struct {
	st       store.Store
	sessions *sessions.Module
	interval time.Duration
	log      *slog.Logger

	communication *communicationPumpWitness
	authority     *communicationOutboxAuthority

	mu         sync.Mutex
	lastReason string
}

func newWorkOutboxPump(getenv func(string) string, st store.Store, sm *sessions.Module, log *slog.Logger) *workOutboxPump {
	if sm == nil {
		return nil
	}
	interval := defaultWorkOutboxInterval
	if raw := strings.TrimSpace(getenv(workOutboxPumpIntervalEnv)); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			log.Warn("sessions-work-outbox: invalid interval; using default", "value", raw, "default", interval.String())
		} else {
			interval = parsed
		}
	}
	return &workOutboxPump{st: st, sessions: sm, interval: interval, log: log}
}

// useCommunication attaches the K3 lane the composition decided on: the
// module-bound authority this pump reports on, and the witness when the lane
// was composed (nil when activation was not requested or custody is missing).
// Until a witness is attached AND registered, every K3 family stays unclaimed
// and K1/K2 keep draining.
func (p *workOutboxPump) useCommunication(witness *communicationPumpWitness, authority *communicationOutboxAuthority) {
	if p == nil {
		return
	}
	p.authority = authority
	if witness == nil {
		return
	}
	p.communication = witness
	witness.attach(p.interval)
}

func (p *workOutboxPump) register(rt *runtime.Runtime) error {
	if err := rt.SchedulePeriodic(workOutboxPumpJobName, p.interval, false, p.runOnce); err != nil {
		return err
	}
	if p.communication != nil {
		p.communication.markRegistered()
	}
	return nil
}

// stop is called by engine.Close before the runtime stops. It removes pump
// readiness immediately so a readiness sample racing shutdown never reports a
// pump that will not tick again, and so a K3 candidate later in a tick that is
// still running is refused at its own claim boundary.
func (p *workOutboxPump) stop() {
	if p != nil && p.communication != nil {
		p.communication.markStopped()
	}
}

func (p *workOutboxPump) runOnce(ctx context.Context) error {
	if !p.st.Leader().Active() {
		return nil
	}
	var tenants []model.TenantID
	if err := p.st.System(ctx, func(sc store.SystemScope) error {
		orgs, err := sc.ListOrgs(ctx)
		if err != nil {
			return err
		}
		for _, org := range orgs {
			if !org.TenantID.IsZero() && !org.TenantID.IsSystem() {
				tenants = append(tenants, org.TenantID)
			}
		}
		return nil
	}); err != nil {
		p.log.Warn("sessions-work-outbox: cannot enumerate tenants", "err", err)
		return fmt.Errorf("sessions-work-outbox: enumerate tenants: %w", err)
	}

	// The K3 authority is the module-bound one and applies inside the drain.
	// This caller restriction only guarantees the pump's own promise when no
	// composition ever bound an authority: such a pump never claims K3.
	restriction := sessions.WorkOutboxClaimPolicyFunc(func(_ context.Context, candidate sessions.WorkOutboxCandidate) (bool, error) {
		switch candidate.Family {
		case sessions.WorkEventFamilyWork, sessions.WorkEventFamilyProtocol:
			return true, nil
		default:
			return p.authority != nil, nil
		}
	})

	var failures []error
	for _, tenant := range tenants {
		if reaped, err := p.sessions.ReapWorkLeases(ctx, tenant, 200); err != nil {
			p.log.Warn("sessions-work-lease: tenant reap failed; expired authority stays deny-closed",
				"tenant", tenant.String(), "err", err)
			failures = append(failures, fmt.Errorf("tenant %s lease reap: %w", tenant, err))
		} else if reaped > 0 {
			p.log.Info("sessions-work-lease: recovered expired ownership",
				"tenant", tenant.String(), "reaped", reaped)
		}
		if err := p.sessions.DrainWorkOutboxWithPolicy(ctx, tenant, 200, restriction); err != nil {
			p.log.Warn("sessions-work-outbox: tenant drain failed; event remains durable", "tenant", tenant.String(), "err", err)
			failures = append(failures, fmt.Errorf("tenant %s: %w", tenant, err))
		}
	}
	p.reportLane(ctx)
	return errors.Join(failures...)
}

// reportLane logs the K3 lane verdict when it changes or when work was held
// back, so an operator sees WHY K3 events stay pending without log spam on
// every quiet tick. The counts come from the authority's own decisions during
// this tick; when no K3 candidate was seen, the verdict is a fresh sample.
func (p *workOutboxPump) reportLane(ctx context.Context) {
	var (
		held, fenced, unknown int
		unknownTypes          []string
		reason                string
	)
	if p.authority == nil {
		reason = "authority_unbound"
	} else {
		var report communicationAuthorityReport
		report, reason = p.authority.snapshot()
		held, fenced, unknown, unknownTypes = report.held, report.fenced, report.unknown, report.unknownTypes
		if reason == "" {
			reason = p.authority.current(ctx).reason
		}
	}
	p.mu.Lock()
	changed := reason != p.lastReason
	p.lastReason = reason
	p.mu.Unlock()
	if unknown > 0 {
		p.log.Warn("sessions-work-outbox: unclassified event family left unclaimed",
			"count", unknown, "types", unknownTypes)
	}
	if fenced > 0 {
		p.log.Warn("sessions-work-outbox: durable leadership fence refused K3 claims", "count", fenced)
	}
	if changed || held > 0 {
		p.log.Info("sessions-work-outbox: K3 lane verdict", "verdict", reason, "held_k3_events", held)
	}
}

// communicationClaimVerdict is one CURRENT sample of the K3 lane authority.
type communicationClaimVerdict struct {
	allow  bool
	reason string
	fenced bool
}

// communicationAuthorityReport is the accounting the authority hands the pump
// for one tick's log line.
type communicationAuthorityReport struct {
	held, fenced, unknown int
	unknownTypes          []string
}

// communicationOutboxAuthority is the module-bound MANDATORY authority for the
// outbox families that are not K1/K2 work facts. The composition binds it on
// the sessions module on every boot (sessions.UseWorkOutboxClaimAuthority), so
// the Apply post-commit nudge, the public DrainWorkOutbox and the periodic pump
// all consult it; none of them can bypass it with a nil policy.
//
// It decides NOW, per candidate, at both boundaries the module offers:
//   - claim: inside the claim transaction (the row is touched only on allow);
//   - effect: after the claim committed and before the sink is invoked.
//
// A communication candidate is allowed only when the composed lane's witness
// is live (attached, registered, not stopped, this node leads), the module's
// effective readiness is on, and the durable epoch fence verifies. Nothing is
// cached between candidates: a pump stopped after one delivery refuses the next
// candidate of the same drain. A refusal never aborts the drain, so K1/K2
// keep flowing behind it. Unknown families are always refused and counted.
type communicationOutboxAuthority struct {
	sessions *sessions.Module

	mu           sync.Mutex
	witness      *communicationPumpWitness
	blockers     []string
	held         int
	fenced       int
	unknown      int
	unknownTypes []string
	lastReason   string
}

func newCommunicationOutboxAuthority(sm *sessions.Module) *communicationOutboxAuthority {
	return &communicationOutboxAuthority{sessions: sm}
}

// composeLane records the K3 lane the composition decided on. A nil witness
// with blockers is a REQUESTED activation whose custody is unavailable: the
// blockers become the visible refusal reason. A nil witness without blockers
// is an activation that was not requested.
func (a *communicationOutboxAuthority) composeLane(witness *communicationPumpWitness, blockers []string) {
	a.mu.Lock()
	a.witness = witness
	a.blockers = append([]string(nil), blockers...)
	a.mu.Unlock()
}

func (a *communicationOutboxAuthority) lane() (*communicationPumpWitness, []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.witness, append([]string(nil), a.blockers...)
}

// current samples the complete K3 authority NOW. Every term is a live read:
// the witness status, the module's readiness conjunction and the durable
// fence. It is the only place that decides, so the claim and effect
// boundaries cannot drift apart.
func (a *communicationOutboxAuthority) current(ctx context.Context) communicationClaimVerdict {
	if a == nil || a.sessions == nil {
		return communicationClaimVerdict{reason: "authority_unbound"}
	}
	witness, blockers := a.lane()
	if witness == nil {
		if len(blockers) > 0 {
			return communicationClaimVerdict{reason: "custody_unavailable: " + strings.Join(blockers, "; ")}
		}
		return communicationClaimVerdict{reason: "communication_pump_unbound"}
	}
	ready, err := witness.CommunicationPumpReady(ctx)
	if err != nil {
		return communicationClaimVerdict{reason: "pump_witness_error: " + err.Error()}
	}
	if !ready {
		return communicationClaimVerdict{reason: "pump_witness_off: " + witness.status().String()}
	}
	readiness, err := a.sessions.EvaluateCommunicationReadiness(ctx)
	if err != nil {
		return communicationClaimVerdict{reason: "readiness_error: " + err.Error()}
	}
	if !readiness.Effective {
		return communicationClaimVerdict{reason: fmt.Sprintf("readiness_off: %s missing=%v unavailable=%v",
			readiness.Code, readiness.Missing, readiness.Unavailable)}
	}
	fencer := witness.fencer
	if fencer == nil {
		return communicationClaimVerdict{reason: "epoch_fencer_unsupported"}
	}
	// Durable fence at the boundary: a paused or demoted node that still
	// believes it leads does not claim or emit a K3 effect.
	if _, err := fencer.FencedEpoch(ctx); err != nil {
		return communicationClaimVerdict{reason: "fence_refused: " + err.Error(), fenced: true}
	}
	return communicationClaimVerdict{allow: true, reason: "allowed"}
}

// AllowWorkOutboxClaim implements sessions.WorkOutboxClaimPolicy.
func (a *communicationOutboxAuthority) AllowWorkOutboxClaim(
	ctx context.Context, candidate sessions.WorkOutboxCandidate,
) (bool, error) {
	return a.decide(ctx, candidate)
}

// AllowWorkOutboxEffect implements sessions.WorkOutboxEffectPolicy with the
// same live sample: the claim's verdict is not reused.
func (a *communicationOutboxAuthority) AllowWorkOutboxEffect(
	ctx context.Context, candidate sessions.WorkOutboxCandidate,
) (bool, error) {
	return a.decide(ctx, candidate)
}

func (a *communicationOutboxAuthority) decide(ctx context.Context, candidate sessions.WorkOutboxCandidate) (bool, error) {
	switch candidate.Family {
	case sessions.WorkEventFamilyWork, sessions.WorkEventFamilyProtocol:
		return true, nil
	case sessions.WorkEventFamilyCommunication:
		verdict := a.current(ctx)
		a.mu.Lock()
		a.lastReason = verdict.reason
		if !verdict.allow {
			a.held++
			if verdict.fenced {
				a.fenced++
			}
		}
		a.mu.Unlock()
		// A refusal is a decision, not "could not look": returning an error
		// would abort the whole drain and stall K1/K2 behind a K3 hold.
		return verdict.allow, nil
	default:
		a.mu.Lock()
		a.unknown++
		if len(a.unknownTypes) < 8 {
			a.unknownTypes = append(a.unknownTypes, candidate.Type)
		}
		a.mu.Unlock()
		return false, nil
	}
}

// snapshot returns and resets this tick's accounting; the last reason is
// returned as the verdict and kept.
func (a *communicationOutboxAuthority) snapshot() (communicationAuthorityReport, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	report := communicationAuthorityReport{
		held: a.held, fenced: a.fenced, unknown: a.unknown, unknownTypes: a.unknownTypes,
	}
	a.held, a.fenced, a.unknown, a.unknownTypes = 0, 0, 0, nil
	return report, a.lastReason
}

var (
	_ sessions.WorkOutboxClaimPolicy  = (*communicationOutboxAuthority)(nil)
	_ sessions.WorkOutboxEffectPolicy = (*communicationOutboxAuthority)(nil)
)

// communicationPumpWitness implements sessions.CommunicationPumpReadinessWitness
// on the registered pump. It proves only its own facts: the pump is attached to
// a real scheduler registration, its capability is valid (positive interval,
// real sink, a store that exposes the directory status and a durable epoch
// fencer) and this node holds established leadership right now. It never
// evaluates the module's full readiness: that conjunction consumes THIS witness
// and would otherwise recurse.
type communicationPumpWitness struct {
	mu             sync.RWMutex
	attached       bool
	registered     bool
	stopped        bool
	interval       time.Duration
	sinkBound      bool
	storeSupported bool
	isLeader       func() bool
	fencer         store.EpochFencer
}

type communicationPumpStatus struct {
	Attached, Registered, Stopped, SinkBound, StoreSupported, FencerBound, Leader bool
	Interval                                                                      time.Duration
}

func (s communicationPumpStatus) String() string {
	return fmt.Sprintf("attached=%t registered=%t stopped=%t sink=%t store=%t fencer=%t leader=%t interval=%s",
		s.Attached, s.Registered, s.Stopped, s.SinkBound, s.StoreSupported, s.FencerBound, s.Leader, s.Interval)
}

func newCommunicationPumpWitness(ctx context.Context, st store.Store, sinkBound bool) *communicationPumpWitness {
	w := &communicationPumpWitness{sinkBound: sinkBound}
	if st == nil {
		return w
	}
	if statuser, ok := st.(store.DirectoryStatuser); ok {
		if _, supported, err := statuser.DirectoryStatus(ctx); err == nil && supported {
			w.storeSupported = true
		}
	}
	if leader := st.Leader(); leader != nil {
		w.isLeader = leader.IsLeader
		if fencer, ok := leader.(store.EpochFencer); ok {
			w.fencer = fencer
		}
	}
	return w
}

func (w *communicationPumpWitness) attach(interval time.Duration) {
	w.mu.Lock()
	w.attached, w.interval = true, interval
	w.mu.Unlock()
}

func (w *communicationPumpWitness) markRegistered() {
	w.mu.Lock()
	w.registered = true
	w.mu.Unlock()
}

func (w *communicationPumpWitness) markStopped() {
	w.mu.Lock()
	w.stopped = true
	w.mu.Unlock()
}

func (w *communicationPumpWitness) status() communicationPumpStatus {
	w.mu.RLock()
	defer w.mu.RUnlock()
	leader := w.isLeader != nil && w.isLeader()
	return communicationPumpStatus{
		Attached: w.attached, Registered: w.registered, Stopped: w.stopped, SinkBound: w.sinkBound,
		StoreSupported: w.storeSupported, FencerBound: w.fencer != nil, Leader: leader, Interval: w.interval,
	}
}

// CommunicationPumpReady implements sessions.CommunicationPumpReadinessWitness.
func (w *communicationPumpWitness) CommunicationPumpReady(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if w == nil {
		return false, nil
	}
	s := w.status()
	return s.Attached && s.Registered && !s.Stopped && s.SinkBound && s.StoreSupported &&
		s.FencerBound && s.Interval > 0 && s.Leader, nil
}

var _ sessions.CommunicationPumpReadinessWitness = (*communicationPumpWitness)(nil)
