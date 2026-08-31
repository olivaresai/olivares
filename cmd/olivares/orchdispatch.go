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
	"time"

	a2a "github.com/olivaresai/olivares/connectors/a2a"
	executor "github.com/olivaresai/olivares/core/runtime/executor"
	"github.com/olivaresai/olivares/modules/orchestration"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// orchdispatch.go is the IV↔actuation seam adapter: it implements the
// orchestration module's Dispatcher port (modules/orchestration/ports.go) so an
// APPROVED scheduled fire stops being "declared, not fired" and actually actuates.
// Like deployexec.go / notifydispatch.go / approvalbridge.go it lives in the
// composition root (cmd, AGPL) because it bridges an AGPL module port to the AGPL
// executor engine AND the Apache a2a connector — neither of which the module may
// import directly.
//
// The module only reaches Fire AFTER its own two-phase HITL gate approved the fire
// and the plan still matches (schedules.go: decision.Allowed() && PlanHash==planHash).
// This adapter therefore ASSUMES prior approval, but re-checks the plan_hash is
// non-empty (defense in depth) and treats every failure as a failure — never a
// pretend success (a faked Ref would let the control plane believe an agent ran).
//
// It routes a fire by subject, deny-closed, by one of two operator-provisioned paths:
//
//  1. RUNTIME — the subject maps to an operator-declared runtime target: the fire
//     reconciles that agent's desired deployment by delegating to the Executor
//     engine (executor.Apply, idempotent — a fire ENSURES the agent is deployed/
//     running to its declared spec). The op-ref is the backend + subject. This reuses
//     the ONLY plane that mutates infra; it does not reimplement actuation. Note the
//     deploy.Executor SEAM cannot be used from here (its ExecRequest carries the
//     module's unexported deploySpec), so we delegate to the shared core engine the
//     same engine backs the deploy module with.
//  2. A2A — the subject is a verified remote A2A agent: the fire emits ONE signed-
//     card-verified A2A v1.0 Task (a2a.Client.SendMessage) and returns the task id.
//     Card verification happens INSIDE the connector before anything is sent
//     (deny-closed); credentials are out-of-band HTTP headers, never in the payload.
//
// No route for a subject is an EXPLICIT error (op_status=failed), never a faked Ref.
// MINIMAL DATA (docs/SECURITY-HARDENING.md): FireRequest is references only; the runtime Desired and
// the A2A auth headers are resolved server-side from operator provisioning, never
// from the request.

// runtimeEngine is the narrow slice of the executor the runtime fire route uses:
// reconcile a desired deployment via the idempotent Apply (a fire ENSURES the agent
// is deployed/running to its declared spec). *executor.Executor satisfies it; depending
// on the capability (not the concrete engine) keeps this seam honestly testable.
type runtimeEngine interface {
	Apply(ctx context.Context, d executor.Desired) (executor.Result, error)
}

// defaultOrchestratorRef is the comm-graph identity of the governed scheduler when an
// A2A fire is delegated to a remote agent: the control plane's orchestration plane acts
// as the delegating agent (origin) toward the remote agent (resource). It is a stable,
// honest reference (not a real remote agent), overridable via the dispatch config.
const defaultOrchestratorRef = "olivares.orchestrator"

// a2aObservationSink late-binds the in-process producer (liveingest) that lifts a
// governed A2A delegation onto the bus as observability: an edge.observed (the comm
// graph, module IV) and a finding.reported (the SOC/audit feed). It is bound after the
// module set is built (the producer's Host exists only at Init); nil means no emission.
// *liveingest.Module satisfies it (PublishEdge / PublishFinding).
type a2aObservationSink interface {
	PublishEdge(ctx context.Context, tenant string, e sdkmodel.EdgeObservation) error
	PublishFinding(ctx context.Context, tenant string, f sdkmodel.FindingReport) error
}

// orchestrationDispatcher actuates approved fires by routing to a runtime target or
// a verified A2A agent. Safe for concurrent use after construction.
type orchestrationDispatcher struct {
	// exec is the shared actuation engine (the same instance backing the deploy
	// module). A nil interface => no runtime backend is provisioned and the runtime
	// route fails closed with an explicit error.
	exec     runtimeEngine
	runtimes map[string]runtimeTarget
	agents   map[string]a2aTarget
	// orchestratorRef is the comm-graph origin identity for an A2A delegation edge.
	orchestratorRef string
	// obs is the in-process observability producer (liveingest); nil until late-bound,
	// and emission is fail-open (it never blocks or fails a fire).
	obs a2aObservationSink
	log *slog.Logger
}

// bindObservationSink late-binds the in-process producer so each governed A2A fire emits
// its delegation edge + finding (observability). The sink's Host is nil until Init
// and its publish methods are nil-safe, so binding the (stable) module pointer now is in
// time. A nil dispatcher (none provisioned) is a no-op.
func (d *orchestrationDispatcher) bindObservationSink(sink a2aObservationSink) {
	if d != nil {
		d.obs = sink
	}
}

// runtimeTarget is an operator-declared desired deployment a scheduled fire
// reconciles. It carries references only (image/command/resource refs, secret-store
// REFERENCES) — never a cleartext secret.
type runtimeTarget struct {
	runtime     string
	target      string
	environment string
	name        string
	image       string
	command     string
	replicas    int
	resources   map[string]string
	envRefs     []executor.SecretBinding
	wirings     []executor.Wiring
}

// a2aEmitter is the narrow A2A emission capability the dispatcher uses. It is the
// CAPABILITY-BOUND primitive: the connector performs the mandatory card verification AND
// refuses a skill the signed card does not declare, before sending. *a2a.Client satisfies
// it; depending on the capability keeps this route testable without minting signed cards.
type a2aEmitter interface {
	SendMessageCapable(ctx context.Context, spec a2a.SendSpec) (a2a.TaskResult, error)
}

// a2aTarget is a verified-before-use remote A2A agent a scheduled fire delegates to.
// The client embeds the operator trust anchor + out-of-band auth headers; text/skill
// are the governed task instruction.
type a2aTarget struct {
	name   string
	url    string
	skill  string
	text   string
	client a2aEmitter
}

var _ orchestration.Dispatcher = (*orchestrationDispatcher)(nil)

// Fire actuates an approved fire. It returns an explicit error on any failure (the
// module records op_status=failed/502 and does NOT advance last_fired_at); a real
// dispatch returns a non-empty opaque Ref (op_status=dispatched).
func (d *orchestrationDispatcher) Fire(ctx context.Context, req orchestration.FireRequest) (orchestration.DispatchResult, error) {
	// Defense in depth: the module never reaches Fire without a bound plan, but an
	// empty plan_hash would mean the approval binding is absent — refuse rather than act.
	if strings.TrimSpace(req.PlanHash) == "" {
		return orchestration.DispatchResult{}, fmt.Errorf("orchestration dispatch: empty plan_hash; refusing to fire (approval binding missing)")
	}
	key := subjectKey(req.SubjectKind, req.SubjectRef)
	if rt, ok := d.runtimes[key]; ok {
		return d.fireRuntime(ctx, req, rt)
	}
	if ag, ok := d.agents[key]; ok {
		return d.fireA2A(ctx, req, ag)
	}
	return orchestration.DispatchResult{}, fmt.Errorf("orchestration dispatch: no actuation route for subject %s/%s", req.SubjectKind, req.SubjectRef)
}

// fireRuntime reconciles the subject's desired deployment via the shared executor.
func (d *orchestrationDispatcher) fireRuntime(ctx context.Context, req orchestration.FireRequest, rt runtimeTarget) (orchestration.DispatchResult, error) {
	if d.exec == nil {
		return orchestration.DispatchResult{}, fmt.Errorf("orchestration dispatch: runtime route for %s but no executor engine wired (provision OLIVARES_DEPLOY_EXECUTOR_CONFIG)", req.SubjectRef)
	}
	name := rt.name
	if name == "" {
		name = req.SubjectRef
	}
	res, err := d.exec.Apply(ctx, executor.Desired{
		Tenant:      req.Tenant.String(),
		Environment: rt.environment,
		Target:      rt.target,
		Runtime:     rt.runtime,
		SubjectKind: req.SubjectKind,
		SubjectRef:  req.SubjectRef,
		Name:        name,
		Image:       rt.image,
		Command:     rt.command,
		Replicas:    rt.replicas,
		Resources:   rt.resources,
		EnvRefs:     rt.envRefs,
		Wirings:     rt.wirings,
	})
	if err != nil {
		return orchestration.DispatchResult{}, fmt.Errorf("orchestration dispatch: runtime apply failed: %w", err)
	}
	backend := res.BackendID
	if backend == "" {
		backend = rt.runtime
	}
	if d.log != nil {
		d.log.Info("orchestration: fire actuated on runtime", "subject", req.SubjectRef, "runtime", rt.runtime, "backend", res.BackendID, "credential", res.CredentialID)
	}
	return orchestration.DispatchResult{Ref: fmt.Sprintf("runtime:%s:%s", backend, req.SubjectRef)}, nil
}

// fireA2A emits one verified-card, CAPABILITY-BOUND A2A Task for the subject (the
// connector refuses a skill the signed card does not declare before sending). An input/
// auth-required interrupt is still a successful DISPATCH (the task was emitted and is
// waiting); the actionable state is encoded into the ref so the ledger records it. On a
// successful emit it publishes the governed delegation to module IV's communication graph
// (an a2a edge) and the SOC/audit feed (a finding), fail-open — so this governed
// inter-agent communication is VISIBLE ("who delegated what to whom"), never silent.
func (d *orchestrationDispatcher) fireA2A(ctx context.Context, req orchestration.FireRequest, ag a2aTarget) (orchestration.DispatchResult, error) {
	res, err := ag.client.SendMessageCapable(ctx, a2a.SendSpec{
		AgentName: ag.name,
		AgentURL:  ag.url,
		Text:      ag.text,
		Skill:     ag.skill,
	})
	if err != nil {
		// Item 8: a post-transmit failure (the remote may have processed the
		// message) is AMBIGUOUS — surface the sentinel so the operation settles
		// UNKNOWN (never a false "failed", never a blind re-emit), not a definitive
		// failure. A clean pre-transmit failure stays a definitive dispatch error.
		if errors.Is(err, a2a.ErrAfterTransmit) {
			return orchestration.DispatchResult{}, orchestration.ErrDispatchAmbiguous
		}
		return orchestration.DispatchResult{}, fmt.Errorf("orchestration dispatch: a2a emit to %s failed: %w", ag.name, err)
	}
	if d.log != nil && res.Interrupt {
		d.log.Info("orchestration: a2a task awaiting input/auth", "subject", req.SubjectRef, "task", res.TaskID, "state", string(res.State))
	}
	d.observeDelegation(ctx, req, ag, res)
	return orchestration.DispatchResult{Ref: fmt.Sprintf("a2a:%s:%s", res.TaskID, res.State)}, nil
}

// observeDelegation makes a governed A2A fire visible: it publishes the delegation as a
// module-IV communication edge (the orchestrator → the remote agent, skill as tool_ref)
// and as an a2a_delegation finding (the "with what objective" record; objective = the
// schedule subject). FAIL-OPEN: a nil sink (none bound) or a publish hiccup never affects
// the dispatch result. Minimal data: references + skill + a non-sensitive objective label
// only — never the task text (docs/SECURITY-HARDENING.md).
func (d *orchestrationDispatcher) observeDelegation(ctx context.Context, req orchestration.FireRequest, ag a2aTarget, res a2a.TaskResult) {
	if d.obs == nil {
		return
	}
	origin := d.orchestratorRef
	if origin == "" {
		origin = defaultOrchestratorRef
	}
	tenant := req.Tenant.String()
	at := time.Now().UTC()
	edge := a2a.AgentEdge(origin, ag.name, ag.skill, at)
	if err := d.obs.PublishEdge(ctx, tenant, edge); err != nil && d.log != nil {
		d.log.Debug("orchestration: a2a delegation edge publish failed (fail-open)", "err", err)
	}
	finding := a2a.DelegationFinding(a2a.DelegationDecision{
		AgentName: ag.name, Skill: ag.skill, Objective: req.SubjectRef,
		Allowed: true, Reason: "scheduled fire", State: res.State,
	}, at)
	if err := d.obs.PublishFinding(ctx, tenant, finding); err != nil && d.log != nil {
		d.log.Debug("orchestration: a2a delegation finding publish failed (fail-open)", "err", err)
	}
}

// subjectKey is the routing key: a fire's (kind, ref) tuple, normalized.
func subjectKey(kind, ref string) string {
	return strings.ToLower(strings.TrimSpace(kind)) + "/" + strings.TrimSpace(ref)
}
