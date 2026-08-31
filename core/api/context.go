// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

type ctxKey int

const (
	ctxKeyPrincipal ctxKey = iota
	ctxKeyRequestID
	ctxKeyActor
	ctxKeyModuleBoundary
)

// moduleRequestBoundary is the workspace confinement that applies to the module
// data reached DURING one authorized request (B-03). It travels in the
// context, not only in ModuleContext.Data, because a module also holds the
// boot-time ModuleData handle from UseData: 48 of the 610 module HTTP handlers
// reach the store through a method that uses that handle instead of mc.Data
// (e.g. sessions handleGetRun → getRun → loadRun), so confining only mc.Data
// would close the List class and leave the Get-by-id class open.
//
// It is installed AFTER authorization, so the authorization machinery's own
// reads keep engine authority, and it is keyed by tenant so it can never be
// mistaken for a confinement in another tenant's unit of work.
type moduleRequestBoundary struct {
	tenant    model.TenantID
	workspace model.ID
}

// withModuleRequestBoundary marks ctx with p's workspace confinement in tenant,
// if any. A principal that is not confined (or is superadmin) returns ctx
// unchanged, so every existing path stays byte-identical.
func withModuleRequestBoundary(ctx context.Context, tenant model.TenantID, p auth.Principal) context.Context {
	ws, confined := p.ConfinedWorkspaceIn(tenant)
	if !confined || p.Superadmin {
		return ctx
	}
	return context.WithValue(ctx, ctxKeyModuleBoundary, moduleRequestBoundary{tenant: tenant, workspace: ws})
}

// moduleBoundaryFrom returns the confined workspace marked on ctx for tenant.
// A boundary recorded for a DIFFERENT tenant does not apply: the caller is in
// another unit of work, and carrying it over would confine (or fail to confine)
// on the strength of an unrelated membership.
func moduleBoundaryFrom(ctx context.Context, tenant model.TenantID) (model.ID, bool) {
	b, ok := ctx.Value(ctxKeyModuleBoundary).(moduleRequestBoundary)
	if !ok || b.tenant != tenant || b.workspace.IsZero() {
		return "", false
	}
	return b.workspace, true
}

// actorHolder is a request-scoped mutable cell: the access-log middleware (which
// wraps everything) installs it, and the authenticate middleware (which runs
// inside) fills it, so the access log can attribute the request to the real
// principal even though it logs after the handler returns. One request, one
// goroutine — no synchronization needed.
type actorHolder struct{ actor string }

func withActorHolder(ctx context.Context) (context.Context, *actorHolder) {
	h := &actorHolder{actor: "anonymous"}
	return context.WithValue(ctx, ctxKeyActor, h), h
}

func actorHolderFrom(ctx context.Context) *actorHolder {
	h, _ := ctx.Value(ctxKeyActor).(*actorHolder)
	return h
}

// withPrincipal returns a context carrying the authenticated principal (zero
// value means anonymous).
func withPrincipal(ctx context.Context, p auth.Principal) context.Context {
	return context.WithValue(ctx, ctxKeyPrincipal, p)
}

// principalFrom returns the principal in ctx and whether one is present (non-anonymous).
func principalFrom(ctx context.Context) (auth.Principal, bool) {
	p, ok := ctx.Value(ctxKeyPrincipal).(auth.Principal)
	if !ok || p.Kind == "" {
		return auth.Principal{}, false
	}
	return p, true
}

// DetachRequestContext strips this package's REQUEST-SCOPED authority from ctx —
// the authenticated principal and the access-log actor cell — while preserving
// everything else, including cancellation, deadline and the request id (so a
// loopback's log line still correlates with the request that caused it).
//
// It exists for the composition root's in-process loopbacks (the approval
// bridge and friends), which call the engine's own handler with the LIVE
// request context. Those callers all present their own service credential, and
// the authenticate middleware overwrites the principal from that header, so
// today nothing inherits ambient authority. But the middleware passes a request
// through UNCHANGED when it carries no Authorization header — so a future
// loopback that forgot its credential would silently execute as whoever
// happened to be making the outer request. Stripping the authority at the seam
// makes that impossible rather than merely unlikely: a loopback authenticates
// on its own credential, or it is anonymous.
func DetachRequestContext(ctx context.Context) context.Context {
	if ctx == nil {
		return nil
	}
	return context.WithValue(context.WithValue(ctx, ctxKeyPrincipal, nil), ctxKeyActor, nil)
}

// withRequestID returns a context carrying the request id.
func withRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKeyRequestID, id)
}

// requestID returns the request id in ctx, or "".
func requestID(ctx context.Context) string {
	id, _ := ctx.Value(ctxKeyRequestID).(string)
	return id
}
