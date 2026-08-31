// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/modules/orchestration"
)

// routinepolicygate.go is the routine-governance seam adapter: it
// implements modules/orchestration's RoutinePolicyGate over the governance
// module's live policy store, and its TargetEnvironmentResolver over the SAME
// operator dispatcher configuration the fire path actuates through.
//
// Like killswitchgate.go it lives in the composition root and is ALWAYS wired
// (governance is in-process, no operator config), and it carries the same
// FAIL-CLOSED posture: routine policy is positive enforcement, so an unreadable
// policy denies. A module never imports another module — the ports are stated
// in orchestration's own terms and translated here.
//
// MINIMAL DATA (docs/SECURITY-HARDENING.md): only the tenant, the routine's frozen owner scope
// and the subject reference cross this seam; what comes back is limits, opaque
// policy ids and a digest — never a policy body.

// routinePolicySource is the narrow slice of the governance module this gate
// depends on. *governance.Module satisfies it.
type routinePolicySource interface {
	ResolveRoutinePolicy(ctx context.Context, tenant model.TenantID, scope governance.RoutineScope) (governance.EffectiveRoutinePolicy, error)
}

var _ routinePolicySource = (*governance.Module)(nil)

// orchRoutinePolicyGate adapts governance's routine policy to module IV's port.
type orchRoutinePolicyGate struct {
	gov routinePolicySource
}

var _ orchestration.RoutinePolicyGate = orchRoutinePolicyGate{}

func (g orchRoutinePolicyGate) Resolve(ctx context.Context, tenant model.TenantID, scope orchestration.RoutineScope) (orchestration.RoutinePolicy, error) {
	eff, err := g.gov.ResolveRoutinePolicy(ctx, tenant, governance.RoutineScope{
		UserRef:      scope.UserRef,
		UserKnown:    scope.UserKnown,
		WorkspaceRef: scope.WorkspaceRef,
	})
	if err != nil {
		return orchestration.RoutinePolicy{}, err // the module fails CLOSED on error
	}
	out := orchestration.RoutinePolicy{
		InForce:               eff.InForce,
		Indeterminate:         eff.Indeterminate,
		IndeterminateAxis:     eff.IndeterminateAxis,
		MinIntervalSec:        eff.MinIntervalSec,
		RequireApproval:       eff.RequireApproval,
		CronAllowed:           eff.CronAllowed,
		CronAllowlistInForce:  eff.CronAllowlistInForce,
		BlockedEnvs:           eff.BlockedEnvs,
		EffectiveUserRef:      eff.EffectiveUserRef,
		EffectiveWorkspaceRef: eff.EffectiveWorkspaceRef,
		DefaultWorkspaceRef:   eff.DefaultWorkspaceRef,
		PolicyRefs:            eff.PolicyRefs,
		Digest:                eff.Digest,
	}
	for _, c := range eff.ActiveCaps {
		out.ActiveCaps = append(out.ActiveCaps, orchestration.RoutineActiveCap{
			ScopeKind: c.ScopeKind, ScopeRef: c.ScopeRef, Max: c.Max,
		})
	}
	return out, nil
}

// orchTargetEnvironment answers "which environment does this subject actually
// actuate in" from the operator dispatcher configuration.
//
// It is built by MIRRORING newOrchestrationDispatcher / newDispatcherGeneration:
// the same validity filters (a runtime entry with an empty subject_ref or
// runtime is skipped; an A2A agent with an empty subject_ref or url is skipped)
// and the SAME precedence (runtime after A2A, so a subject present in both
// resolves to the runtime route Fire picks). Deriving from a different snapshot
// would let an operator's invalid duplicate answer the policy question while
// the dispatcher acted on something else — the exact substitution closed
// for the target fingerprint.
//
// The A2A route deliberately contributes RouteFound with an EMPTY environment:
// that route carries no environment dimension, and an absence must never be
// read as an implicitly safe environment.
type orchTargetEnvironment struct {
	bySubject map[string]string // subjectKey -> environment ("" = route has none)
}

var _ orchestration.TargetEnvironmentResolver = (*orchTargetEnvironment)(nil)

func newOrchTargetEnvironment(cfg orchDispatchConfig) *orchTargetEnvironment {
	m := map[string]string{}
	// A2A FIRST so a subject present in BOTH is overwritten by its runtime
	// entry below — matching orchdispatch.Fire, which resolves runtimes[key]
	// before agents[key].
	for _, a := range cfg.A2A.Agents {
		if strings.TrimSpace(a.SubjectRef) == "" || strings.TrimSpace(a.URL) == "" {
			continue
		}
		m[subjectKey(orDefaultStr(a.SubjectKind, "agent"), a.SubjectRef)] = ""
	}
	for _, t := range cfg.Runtime.Targets {
		if strings.TrimSpace(t.SubjectRef) == "" || strings.TrimSpace(t.Runtime) == "" {
			continue
		}
		m[subjectKey(orDefaultStr(t.SubjectKind, "agent"), t.SubjectRef)] = strings.TrimSpace(t.Environment)
	}
	return &orchTargetEnvironment{bySubject: m}
}

func (r *orchTargetEnvironment) Resolve(_ context.Context, subjectKind, subjectRef string) (orchestration.TargetEnvironment, error) {
	env, ok := r.bySubject[subjectKey(subjectKind, subjectRef)]
	if !ok {
		return orchestration.TargetEnvironment{}, nil // no route: RouteFound=false
	}
	return orchestration.TargetEnvironment{RouteFound: true, Environment: env}, nil
}
