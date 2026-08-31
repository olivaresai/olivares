// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"

	executor "github.com/olivaresai/olivares/core/runtime/executor"
	"github.com/olivaresai/olivares/modules/deploy"
)

// This file is the VII↔executor seam adapter: it implements the deploy module's
// Executor port (modules/deploy/ports.go) by delegating to the real, governed
// actuation engine (core/runtime/executor). It is the ONLY layer that imports both
// the AGPL deploy module and the AGPL executor engine — the composition root —
// exactly as notifydispatch.go bridges notify and approvalbridge.go bridges
// the four ApprovalGate seams. The module never knows which backend acts; the
// executor selects it by runtime and enforces credential-minting + the blast-radius
// gate. Here we only translate the module's typed deploySpec into the executor's
// neutral Desired and translate the neutral Diff/Result back into []deploy.Change.
//
// MINIMAL DATA: the translation copies only references (image/command/resource
// refs and secret-store REFERENCES) — never a cleartext secret. The executor's
// Result carries the non-sensitive credential id (for audit) but never the
// material; we surface it in the operation detail, never a token.

// deployExecutor adapts *executor.Executor to deploy.Executor.
type deployExecutor struct {
	e *executor.Executor
}

var _ deploy.Executor = (*deployExecutor)(nil)

// deployEngine returns the core executor this adapter delegates to, so the
// composition root can share the SAME governed actuation engine with the
// orchestration dispatcher: the runtime fire route reuses it rather than
// constructing a second engine. It returns nil for a nil adapter (no executor
// provisioned — the orchestration runtime route then fails closed per fire).
func deployEngine(a *deployExecutor) *executor.Executor {
	if a == nil {
		return nil
	}
	return a.e
}

// Plan maps the module's dry-run to the executor's read-only Plan.
func (a *deployExecutor) Plan(ctx context.Context, req deploy.ExecRequest) ([]deploy.Change, error) {
	diff, err := a.e.Plan(ctx, desiredFrom(req))
	if err != nil {
		return nil, err
	}
	return changesFromDiff(diff), nil
}

// Apply maps the governed apply: the executor re-plans, enforces the blast-radius
// gate (the second control over the module's HITL gate), mints a short-lived
// write credential and reconciles.
func (a *deployExecutor) Apply(ctx context.Context, req deploy.ExecRequest) (deploy.ExecResult, error) {
	res, err := a.e.Apply(ctx, desiredFrom(req))
	if err != nil {
		return deploy.ExecResult{}, err
	}
	return deploy.ExecResult{Changes: changesFromItems(res.Applied), Detail: applyDetail(res)}, nil
}

// Verify maps the read-only drift path. An unobservable unit is surfaced as a
// non-sync change so the module never records a silent "in sync" for a gap.
func (a *deployExecutor) Verify(ctx context.Context, req deploy.ExecRequest) (deploy.ExecResult, error) {
	rs, err := a.e.Verify(ctx, desiredFrom(req))
	if err != nil {
		return deploy.ExecResult{}, err
	}
	changes := changesFromItems(rs.Drift)
	switch {
	case !rs.Observable:
		// Honest gap (docs/SECURITY-HARDENING.md): a unit we cannot read is NOT reported as in-sync.
		detail := rs.Detail
		if detail == "" {
			detail = "unit is not observable"
		}
		changes = append(changes, deploy.Change{Kind: "unknown", Resource: "observability", Detail: detail})
	case !rs.InSync && len(changes) == 0:
		// The backend reports drift but enumerated no specific items: InSync is the
		// authoritative flag, so surface a generic non-sync change rather than let the
		// module derive a FALSE in-sync from an empty change set (lifecycle.go:383).
		detail := rs.Detail
		if detail == "" {
			detail = "real state differs from desired"
		}
		changes = append(changes, deploy.Change{Kind: "update", Resource: "state", Detail: detail})
	}
	return deploy.ExecResult{Changes: changes, Detail: rs.Detail}, nil
}

// Retire maps the governed teardown (gated by blast-radius + HITL).
func (a *deployExecutor) Retire(ctx context.Context, req deploy.ExecRequest) (deploy.ExecResult, error) {
	res, err := a.e.Retire(ctx, desiredFrom(req))
	if err != nil {
		return deploy.ExecResult{}, err
	}
	return deploy.ExecResult{Changes: changesFromItems(res.Applied), Detail: res.Detail}, nil
}

// desiredFrom translates the module's ExecRequest (carrying the typed, unexported
// deploySpec whose fields are exported) into the executor's neutral Desired. Only
// references travel — no cleartext secret (the module already guarantees the spec
// carries secret-store references only).
func desiredFrom(req deploy.ExecRequest) executor.Desired {
	d := executor.Desired{
		Tenant:      req.Tenant.String(),
		Environment: req.Environment,
		Target:      req.Target,
		Runtime:     req.Runtime,
		SubjectKind: req.SubjectKind,
		SubjectRef:  req.SubjectRef,
		Name:        req.SubjectRef,
		Image:       req.Spec.Image,
		Command:     req.Spec.Command,
		Replicas:    req.Spec.Replicas,
	}
	if len(req.Spec.Resources) > 0 {
		d.Resources = make(map[string]string, len(req.Spec.Resources))
		for k, v := range req.Spec.Resources {
			d.Resources[k] = v
		}
	}
	for _, er := range req.Spec.EnvRefs {
		d.EnvRefs = append(d.EnvRefs, executor.SecretBinding{Name: er.Name, SecretRef: er.SecretRef})
	}
	for _, w := range req.Spec.Wirings {
		d.Wirings = append(d.Wirings, executor.Wiring{
			ResourceKind: w.ResourceKind, ResourceRef: w.ResourceRef, Mode: w.Mode, SecretRef: w.SecretRef,
		})
	}
	return d
}

// changesFromDiff maps a neutral Diff to the module's []deploy.Change (minimal
// data: action kind + resource class + non-sensitive detail).
func changesFromDiff(d executor.Diff) []deploy.Change {
	return changesFromItems(d.Items())
}

// changesFromItems maps neutral change items to module changes.
func changesFromItems(items []executor.ChangeItem) []deploy.Change {
	if len(items) == 0 {
		return nil
	}
	out := make([]deploy.Change, 0, len(items))
	for _, it := range items {
		out = append(out, deploy.Change{Kind: it.Action, Resource: it.Kind, Detail: it.Detail})
	}
	return out
}

// applyDetail builds a short, non-sensitive apply summary that records the backend
// and the NON-SENSITIVE credential id (never the credential material) for the
// operation ledger meta (docs/SECURITY-HARDENING.md).
func applyDetail(res executor.Result) string {
	detail := res.Detail
	if res.BackendID != "" {
		detail += " [backend=" + res.BackendID + "]"
	}
	if res.CredentialID != "" {
		detail += " [credential=" + res.CredentialID + "]"
	}
	return detail
}
