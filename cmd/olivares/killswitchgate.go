// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/deploy"
	"github.com/olivaresai/olivares/modules/eventing"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/modules/models"
	"github.com/olivaresai/olivares/modules/orchestration"
	"github.com/olivaresai/olivares/modules/voice"
	"github.com/olivaresai/olivares/sdk/event"
)

// killswitchgate.go is the estate kill-switch seam adapter: it implements
// the per-module StopGate ports (orchestration fire / voice open / deploy
// apply+retire / models execute), the eventing DeliveryGate and the hooks-PEP /
// MCP-gateway consults over the governance module's live stop state
// (KillSwitchState). Like budgetgate.go it lives in the composition root and is
// ALWAYS wired (governance is in-process, no operator config).
//
// FAIL CLOSED — the deliberate inverse of budgetgate.go's documented fail-open
// contract: a stop is positive enforcement, so an UNREADABLE stop state denies
// the actuation (the module ports document the same contract). The only
// softening is the eventing adapter, which absorbs a read error into
// "paused with the static governance exemptions" so a state outage can never
// silence the approval/finding rail that recovery itself depends on.
//
// MINIMAL DATA (docs/SECURITY-HARDENING.md): only tenant + the surface's own subject reference
// cross this seam; deny evidence carries identifiers and counts, never tool
// arguments or payloads.

// killSwitchGuard is the narrow slice of the governance module the gates
// depend on. *governance.Module satisfies it.
type killSwitchGuard interface {
	KillSwitchState(ctx context.Context, tenant model.TenantID) (governance.StopState, error)
}

var _ killSwitchGuard = (*governance.Module)(nil)

// stopScopeOf labels which graduation matched for operator-facing messages.
func stopScopeOf(st governance.StopState, id model.ID) string {
	if st.EstateStopped && id == st.EstateStopID {
		return "estate"
	}
	return "agent"
}

// orchStopGate adapts the kill-switch state to the orchestration fire seam.
type orchStopGate struct {
	guard killSwitchGuard
}

var _ orchestration.StopGate = orchStopGate{}

func (g orchStopGate) Check(ctx context.Context, tenant model.TenantID, dims orchestration.StopDims) (orchestration.StopDecision, error) {
	st, err := g.guard.KillSwitchState(ctx, tenant)
	if err != nil {
		return orchestration.StopDecision{}, err // the module fails CLOSED on error
	}
	if id, stopped := st.Stopped(strings.TrimSpace(dims.AgentRef)); stopped {
		return orchestration.StopDecision{Stopped: true, StopRef: id.String(), Scope: stopScopeOf(st, id)}, nil
	}
	return orchestration.StopDecision{}, nil
}

// voiceStopGate adapts the kill-switch state to the voice open seam.
type voiceStopGate struct {
	guard killSwitchGuard
}

var _ voice.StopGate = voiceStopGate{}

func (g voiceStopGate) Check(ctx context.Context, tenant model.TenantID, dims voice.StopDims) (voice.StopDecision, error) {
	st, err := g.guard.KillSwitchState(ctx, tenant)
	if err != nil {
		return voice.StopDecision{}, err
	}
	if id, stopped := st.Stopped(strings.TrimSpace(dims.AgentRef)); stopped {
		return voice.StopDecision{Stopped: true, StopRef: id.String(), Scope: stopScopeOf(st, id)}, nil
	}
	return voice.StopDecision{}, nil
}

// deployStopGate adapts the kill-switch state to the deploy apply/retire seam.
type deployStopGate struct {
	guard killSwitchGuard
}

var _ deploy.StopGate = deployStopGate{}

func (g deployStopGate) Check(ctx context.Context, tenant model.TenantID, dims deploy.StopDims) (deploy.StopDecision, error) {
	st, err := g.guard.KillSwitchState(ctx, tenant)
	if err != nil {
		return deploy.StopDecision{}, err
	}
	if id, stopped := st.Stopped(strings.TrimSpace(dims.AgentRef)); stopped {
		return deploy.StopDecision{Stopped: true, StopRef: id.String(), Scope: stopScopeOf(st, id)}, nil
	}
	return deploy.StopDecision{}, nil
}

// modelsStopGate adapts the kill-switch state to the models execute seam
// (estate graduation only — routed execution has no agent dimension).
type modelsStopGate struct {
	guard killSwitchGuard
}

var _ models.StopGate = modelsStopGate{}

func (g modelsStopGate) Check(ctx context.Context, tenant model.TenantID) (models.StopDecision, error) {
	st, err := g.guard.KillSwitchState(ctx, tenant)
	if err != nil {
		return models.StopDecision{}, err
	}
	if st.EstateStopped {
		return models.StopDecision{Stopped: true, StopRef: st.EstateStopID.String()}, nil
	}
	return models.StopDecision{}, nil
}

// eventingGovernanceExempt is the STATIC governance-channel allowlist: the
// event types an estate stop never parks, because the dual-control re-enable
// and the incident alerting ride them (the control loop must not disable its
// own controller). Deliberately small and code-declared, not configurable —
// an operator cannot accidentally exempt business automations.
var eventingGovernanceExempt = map[string]struct{}{
	string(event.TypeApprovalRequested): {},
	string(event.TypeFindingReported):   {},
}

// eventingKillSwitchGate adapts the kill-switch state to the eventing delivery
// pass. It owns the deny-closed posture for this surface: an unreadable stop
// state parks everything EXCEPT the static governance channel (so a state
// outage can never silence the rail recovery depends on). Only the ESTATE
// graduation parks deliveries — agent-scoped stops do not (deliveries carry no
// agent dimension).
type eventingKillSwitchGate struct {
	guard killSwitchGuard
	log   *slog.Logger
}

var _ eventing.DeliveryGate = eventingKillSwitchGate{}

func (g eventingKillSwitchGate) Check(ctx context.Context, tenant model.TenantID) (eventing.DeliveryPause, error) {
	st, err := g.guard.KillSwitchState(ctx, tenant)
	if err != nil {
		if g.log != nil {
			g.log.Error("eventing kill-switch gate: stop state unreadable; parking non-governance deliveries (deny-closed)", "tenant", tenant.String(), "err", err)
		}
		return eventing.DeliveryPause{Paused: true, Exempt: eventingGovernanceExempt}, nil
	}
	if !st.EstateStopped {
		return eventing.DeliveryPause{}, nil
	}
	return eventing.DeliveryPause{Paused: true, Exempt: eventingGovernanceExempt}, nil
}

// ----------------------------------------------------------------------------
// Deny evidence for the cmd-side surfaces (hooks PEP, MCP gateway). The module
// seams record their own denials in their own ledgers/transactions; these two
// surfaces have no module transaction, so the recorder appends the
// tamper-evident "security.killswitch.deny" event directly — THROTTLED per
// (tenant, surface, subject) so an agent hammering a stopped estate cannot
// flood the ledger: the first deny in each window lands immediately, the
// window's suppressed count rides the next append (honest aggregation, never
// silent loss).
// ----------------------------------------------------------------------------

// stopDenyThrottle is the per-key append window.
const stopDenyThrottle = time.Minute

// stopDenyMaxKeys bounds the in-memory throttle map (a full map resets — the
// cost is one extra append per live key, never lost evidence).
const stopDenyMaxKeys = 4096

type stopDenyRecorder struct {
	st  store.Store
	log *slog.Logger

	mu   sync.Mutex
	last map[string]*stopDenyEntry
}

type stopDenyEntry struct {
	at         time.Time
	suppressed int
	stopID     model.ID // the stop this key's denials were recorded against (flush attribution)
}

func newStopDenyRecorder(st store.Store, log *slog.Logger) *stopDenyRecorder {
	return &stopDenyRecorder{st: st, log: log, last: map[string]*stopDenyEntry{}}
}

// evictedDeny is a throttle-map entry flushed when the map hits its cap: its
// accrued suppressed count must still reach the ledger (never lost evidence).
type evictedDeny struct {
	tenant     model.TenantID
	key        string
	suppressed int
	stopID     model.ID
}

// record appends one throttled deny event. actor is the denied principal's
// audit-actor string when known ("" falls back to the system actor — the deny
// itself is a plane decision). Best-effort: the denial stands even if the
// evidence write fails (logged loudly, mirroring the module gates).
func (r *stopDenyRecorder) record(ctx context.Context, tenant model.TenantID, stopID model.ID, surface, subject, actor string) {
	if r == nil {
		return
	}
	key := tenant.String() + "|" + surface + "|" + subject
	now := time.Now()
	var evicted []evictedDeny
	r.mu.Lock()
	if len(r.last) >= stopDenyMaxKeys {
		// At the cap: do NOT wipe the map (that would silently drop the accrued
		// suppressed counts of every in-window key — the "never lost evidence"
		// contract). Reset the map but FLUSH each key that holds suppressed denials
		// as one aggregate event AFTER the lock is released. The cost is the
		// documented "one extra append per live key", and no count is lost.
		for k, e := range r.last {
			if e.suppressed > 0 {
				t := tenant
				if i := strings.IndexByte(k, '|'); i >= 0 {
					if parsed, perr := model.ParseTenantID(k[:i]); perr == nil {
						t = parsed
					}
				}
				evicted = append(evicted, evictedDeny{tenant: t, key: k, suppressed: e.suppressed, stopID: e.stopID})
			}
		}
		r.last = map[string]*stopDenyEntry{}
	}
	e := r.last[key]
	if e != nil && now.Sub(e.at) < stopDenyThrottle {
		e.suppressed++
		r.mu.Unlock()
		r.flushEvicted(ctx, evicted)
		return
	}
	suppressed := 0
	if e != nil {
		suppressed = e.suppressed
	}
	r.last[key] = &stopDenyEntry{at: now, stopID: stopID}
	r.mu.Unlock()
	r.flushEvicted(ctx, evicted)

	actorKind := model.ActorSystem
	if actor == "" {
		actor = model.ActorSystem
	} else {
		actorKind = model.ActorAgent
	}
	meta := map[string]any{"surface": surface, "subject": subject}
	if suppressed > 0 {
		meta["suppressed_since_last"] = suppressed
	}
	if err := r.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, aerr := sc.Audit().Append(ctx, model.AuditDraft{
			Actor: actor, ActorKind: actorKind,
			Action: "security.killswitch.deny", TargetKind: "governance.killswitch", TargetID: stopID,
			Meta: meta,
		})
		return aerr
	}); err != nil && r.log != nil {
		r.log.Error("kill-switch: failed to record deny evidence (denial stands)", "surface", surface, "err", err)
	}
}

// flushEvicted appends one aggregate deny event per cap-evicted key that held
// suppressed denials, OUTSIDE the throttle lock (never a DB write under r.mu).
// Best-effort, mirroring record(): a failed flush is logged, the count is not
// re-queued (the eviction already cleared it) but the denials themselves stood.
func (r *stopDenyRecorder) flushEvicted(ctx context.Context, evicted []evictedDeny) {
	for _, ev := range evicted {
		surface, subject := "", ""
		parts := strings.SplitN(ev.key, "|", 3)
		if len(parts) == 3 {
			surface, subject = parts[1], parts[2]
		}
		meta := map[string]any{"surface": surface, "subject": subject, "suppressed_since_last": ev.suppressed, "flushed_on_cap_evict": true}
		if err := r.st.Mutate(ctx, ev.tenant, func(sc store.Scope) error {
			_, aerr := sc.Audit().Append(ctx, model.AuditDraft{
				Actor: model.ActorSystem, ActorKind: model.ActorSystem,
				Action: "security.killswitch.deny", TargetKind: "governance.killswitch", TargetID: ev.stopID,
				Meta: meta,
			})
			return aerr
		}); err != nil && r.log != nil {
			r.log.Error("kill-switch: failed to flush evicted deny evidence", "surface", surface, "err", err)
		}
	}
}

// ----------------------------------------------------------------------------
// MCP gateway wrap: the upstream forwarder covers EVERY forwarded method
// (tools/call, tools/list, resources/read, …), so wrapping it freezes the WHOLE
// MCP surface under a stop — not just the destructive-tool gate (which the
// connector consults ONLY for policy.Destructive tools, connectors/mcp/rs.go).
// The forwarder DOES carry the validated caller subject (UpstreamRequest.Subject
// = tok.Subject), so it enforces BOTH graduations: the estate stop and the
// per-agent stop of the calling agent. Without this, an agent-scoped kill switch
// would leave the agent free to drive every non-destructive call (recon via
// tools/list, exfiltration via resources/read) through the governed gateway.
// ----------------------------------------------------------------------------

// killSwitchUpstream wraps the gateway's Upstream with the live stop check,
// covering both the estate and the per-agent graduation off the caller subject.
type killSwitchUpstream struct {
	guard  killSwitchGuard
	tenant model.TenantID
	rec    *stopDenyRecorder
	inner  mcpc.Upstream
}

func (u killSwitchUpstream) Forward(ctx context.Context, req mcpc.UpstreamRequest) (mcpc.UpstreamResult, error) {
	st, err := u.guard.KillSwitchState(ctx, u.tenant)
	if err != nil {
		// Deny-closed before any transport write: a definitively not-sent dispatch
		// (classification), never an ambiguous one.
		return mcpc.UpstreamResult{State: mcpc.DispatchNotSent}, fmt.Errorf("mcp gateway: kill-switch state unreadable; forward denied (deny-closed)")
	}
	subject := strings.TrimSpace(req.Subject)
	if stopID, stopped := st.Stopped(subject); stopped {
		if req.Method == "tasks/cancel" {
			return u.inner.Forward(ctx, req)
		}
		// Record the agent subject (not the method) as the deny subject, attributed
		// to the agent; an empty subject under an estate stop falls back to system.
		u.rec.record(ctx, u.tenant, stopID, "mcp-forward", subject, subject)
		// A stop is a policy block AFTER the claim anchored: settlement state
		// "blocked" — the effect was stopped, nothing reached the upstream.
		return mcpc.UpstreamResult{State: mcpc.DispatchBlocked}, fmt.Errorf("mcp gateway: kill switch active (%s); the forward is denied until a dual-control re-enable", stopID.String())
	}
	return u.inner.Forward(ctx, req)
}

type killSwitchSubscriptionUpstream struct {
	guard  killSwitchGuard
	tenant model.TenantID
	rec    *stopDenyRecorder
	inner  mcpc.SubscriptionUpstream
}

var _ mcpc.SubscriptionUpstream = killSwitchSubscriptionUpstream{}

// Listen applies the same live stop check to long-lived MCP subscriptions. The
// connector has already authenticated the downstream subject; this wrapper is
// the final composition-root check before an upstream stream is opened.
func (u killSwitchSubscriptionUpstream) Listen(
	ctx context.Context,
	req mcpc.SubscriptionListenRequest,
	emit func(mcpc.SubscriptionEvent) error,
) error {
	st, err := u.guard.KillSwitchState(ctx, u.tenant)
	if err != nil {
		return fmt.Errorf("mcp gateway: kill-switch state unreadable; subscription denied (deny-closed)")
	}
	subject := strings.TrimSpace(req.Route.Subject)
	if stopID, stopped := st.Stopped(subject); stopped {
		u.rec.record(ctx, u.tenant, stopID, "mcp-subscription-listen", subject, subject)
		return fmt.Errorf(
			"mcp gateway: kill switch active (%s); the subscription is denied until a dual-control re-enable",
			stopID.String(),
		)
	}
	if u.inner == nil {
		return fmt.Errorf("mcp gateway: durable subscription upstream unavailable")
	}
	return u.inner.Listen(ctx, req, emit)
}
