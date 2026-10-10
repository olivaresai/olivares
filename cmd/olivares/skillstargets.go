// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/api"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/modules/skills"
)

// Composition only: sessions owns the template and session rows and the core
// owns the workspace, agent group and agent rows; skills receives their stored
// version and lineage and never reads them itself. The request's own scope
// supplies the tenant and any workspace confinement; the authorizer adds the
// target's native permission, so holding skills:assignment:write alone never
// reaches a workspace, agent group, agent, template or session the caller could
// not read or write natively.
type skillsTargetAuthority struct {
	principals *auth.Authenticator
	st         store.Store
	authz      *auth.Authorizer
	sessions   *sessions.Module
}

var _ skills.TargetAuthority = skillsTargetAuthority{}

const (
	skillsWorkspaceRead  auth.Permission = "tenant:read"
	skillsWorkspaceWrite auth.Permission = "tenant:admin"
	skillsAgentRead      auth.Permission = "agent:read"
	skillsAgentWrite     auth.Permission = "agent:write"
)

// lineageWorkspace is the workspace an agent or group belongs to. An unset one
// means the tenant's default workspace, and the assignment table hides a row
// with no workspace from every confined principal, so the pin carries the real
// default workspace ID instead of none.
func lineageWorkspace(ctx context.Context, sc store.Scope, id model.ID) (model.ID, error) {
	if !id.IsZero() {
		return id, nil
	}
	def, err := sc.DefaultWorkspace(ctx)
	if err != nil {
		return model.ID(""), err
	}
	return def.ID, nil
}

// agentPermission is the native permission over an agent or an agent group:
// core gates both with agent:read and agent:write.
func agentPermission(write bool) auth.Permission {
	if write {
		return skillsAgentWrite
	}
	return skillsAgentRead
}

type skillsTargetFact struct {
	stored     skills.StoredTarget
	permission auth.Permission
	resource   auth.ResourceAttrs
}
type skillsPreparedTargetKey struct{}

// Only the composition root wraps a scope after acquiring its complete barrier.
type skillsAssignmentScope struct{ store.Scope }
type skillsPreparedTarget struct {
	tenant model.TenantID
	actor  string
	target skills.StoredTarget
	write  bool
	orphan bool
	// Session authorization uses the launch-bounded principal while its source
	// bundle pins the exact native launcher, directory and policy generation.
	plain               bool
	sourceBundle        store.AuthoritySnapshotBundle
	observedAt          time.Time
	freshUntil          time.Time
	epoch               store.AuthorizationFactRef
	principal           auth.Principal
	request             auth.Request
	authority           auth.RouteMutationAuthorization
	assignmentRequest   auth.Request
	assignmentAuthority auth.RouteMutationAuthorization
}

func (p skillsPreparedTarget) authorityBundle(now time.Time) (store.AuthoritySnapshotBundle, error) {
	if p.plain {
		if now.Before(p.observedAt) || !now.Before(p.freshUntil) {
			return store.AuthoritySnapshotBundle{}, auth.ErrRouteUndecided
		}
		if err := p.principal.ValidateSessionLauncher(now, p.tenant); err != nil {
			return store.AuthoritySnapshotBundle{}, err
		}
		return store.AuthoritySnapshotBundle{Facts: slices.Clone(p.sourceBundle.Facts), UserAuthorities: slices.Clone(p.sourceBundle.UserAuthorities)}, nil
	}
	if p.orphan {
		return p.assignmentAuthority.AuthorityFor(now, p.assignmentRequest)
	}
	native, err := p.authority.AuthorityFor(now, p.request)
	if err != nil {
		return store.AuthoritySnapshotBundle{}, err
	}
	assignment, err := p.assignmentAuthority.AuthorityFor(now, p.assignmentRequest)
	if err != nil {
		return store.AuthoritySnapshotBundle{}, err
	}
	return auth.MergeAuthoritySnapshotBundles(native, assignment)
}

// The handler owns this cancellation and the transaction inherits the earliest
// credential/evidence expiry, including while a catalog row or audit waits.
func (p skillsPreparedTarget) requestContext(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if !p.write {
		return context.WithValue(ctx, skillsPreparedTargetKey{}, p), func() {}, nil
	}
	if p.plain {
		if _, err := p.authorityBundle(time.Now()); err != nil {
			return ctx, func() {}, err
		}
		bounded, cancel := context.WithDeadline(ctx, p.freshUntil)
		return context.WithValue(bounded, skillsPreparedTargetKey{}, p), cancel, nil
	}
	metadata, err := p.assignmentAuthority.MetadataFor(time.Now(), p.assignmentRequest)
	if err != nil {
		return ctx, func() {}, err
	}
	expiry := metadata.FreshUntil
	if !p.orphan {
		metadata, err = p.authority.MetadataFor(time.Now(), p.request)
		if err != nil {
			return ctx, func() {}, err
		}
		if metadata.FreshUntil.Before(expiry) {
			expiry = metadata.FreshUntil
		}
	}
	bounded, cancel := context.WithDeadline(ctx, expiry)
	return context.WithValue(bounded, skillsPreparedTargetKey{}, p), cancel, nil
}

// AssignmentTargetRefs maps every assignment target kind to its stored entity.
// The core target kinds also name their own native read: a caller the route
// denies may learn the refusal itself (403) when that caller could read the
// named target anyway AND the caller's own role never granted the assignment
// action — that refusal follows the caller's grants alone. A caller a scope or
// a policy refused, and every foreign, hidden or absent target, keeps the same
// concealed 404. The template and session kinds declare no disclosure read:
// their native read questions run over the sessions kinds, and this route's
// disclosure resource carries the assignment kind, so a kind-conditioned policy
// could not be honored exactly there. They stay fully concealed.
func (a skillsTargetAuthority) AssignmentTargetRefs() map[string]api.EntityRef {
	return map[string]api.EntityRef{
		"workspace":   {CoreKind: api.CoreKindWorkspace, BodyIDField: "target_id", ResourceKind: "workspace", ConcealDeniedAsNotFound: true, DeniedReadPermission: skillsWorkspaceRead},
		"agent_group": {CoreKind: api.CoreKindAgentGroup, BodyIDField: "target_id", ResourceKind: "agent_group", ConcealDeniedAsNotFound: true, DeniedReadPermission: skillsAgentRead},
		"agent":       {CoreKind: api.CoreKindAgent, BodyIDField: "target_id", ResourceKind: "agent", ConcealDeniedAsNotFound: true, DeniedReadPermission: skillsAgentRead},
		"template":    {Kind: "sessions.template", BodyIDField: "target_id", ConcealDeniedAsNotFound: true},
		"session":     {Kind: "sessions.run", BodyIDField: "target_id", LookupColumn: "run_ref", WorkspaceColumn: "authz_workspace_id", ConcealDeniedAsNotFound: true},
	}
}

// PrepareSkillsTarget closes the read transaction before consulting the PDP,
// whose scope resolver reads the store too. The write later checks and fences
// this exact version and lineage rather than treating the preflight as current.
// A principal a session credential narrowed is served by prepareSessionTarget:
// the session configuration tools act as their launcher, and that launcher's
// answer must be the console's.
func (a skillsTargetAuthority) PrepareSkillsTarget(ctx context.Context, mc api.ModuleContext, t skills.Target, write bool) (context.Context, context.CancelFunc, error) {
	noop := func() {}
	tenant, p := mc.Tenant, mc.Principal
	if a.st == nil || a.authz == nil || a.principals == nil {
		return ctx, noop, store.ErrStoreUnavailable
	}
	// Bind body-target writes to the engine's admitted selector and locator,
	// including a session's public run reference. Path-only PUT/DELETE retain
	// their existing assignment-ID authorization resource.
	if write && mc.BodyEntityFields != nil && (mc.BodyEntityFields["target_kind"] != t.Kind || mc.BodyEntityFields["target_id"] != t.ID) {
		return ctx, noop, auth.ErrRouteDenied
	}
	bounded, cancel := context.WithTimeout(api.DetachRequestContext(ctx), 30*time.Second)
	defer cancel()
	if p.IsSessionCredential() {
		return a.prepareSessionTarget(ctx, bounded, tenant, p, mc, t, write)
	}
	ref, ok := p.Ref()
	if !ok {
		return ctx, noop, auth.ErrRouteDenied
	}
	resolved, err := a.principals.ResolvePrincipalScope(bounded, ref, tenant)
	if err != nil {
		return ctx, noop, err
	}
	p = resolved
	var assignmentRequest auth.Request
	var assignmentAuthority auth.RouteMutationAuthorization
	if write {
		assignmentRequest = auth.Request{Principal: p, Tenant: tenant, Permission: "skills:assignment:write", Resource: mc.Resource}
		assignmentAuthority, err = a.authz.AuthorizeRouteMutation(bounded, assignmentRequest)
		if err != nil {
			return ctx, noop, err
		}
	}
	fact, epoch, err := a.loadTargetFact(ctx, bounded, tenant, p, t)
	if err != nil {
		if write && errors.Is(err, store.ErrNotFound) && (t.Kind == "agent" || t.Kind == "agent_group") && mc.Resource.Kind == "skills:assignment" && mc.Resource.ID != "" {
			prepared := skillsPreparedTarget{tenant: tenant, actor: p.Actor(), target: skills.StoredTarget{Target: t}, write: true, orphan: true, principal: p, assignmentRequest: assignmentRequest, assignmentAuthority: assignmentAuthority}
			preparedCtx, release, prepareErr := prepared.requestContext(ctx)
			if prepareErr != nil {
				return ctx, noop, prepareErr
			}
			return preparedCtx, release, err
		}
		return ctx, noop, err
	}
	request := auth.Request{Principal: p, Tenant: tenant, Permission: targetWritePermission(t, fact.permission, write), Resource: fact.resource}
	var authority auth.RouteMutationAuthorization
	if write {
		authority, err = a.authz.AuthorizeRouteMutation(bounded, request)
		if err != nil {
			return ctx, noop, err
		}
	} else if !a.authz.Authorize(bounded, request).Allow {
		return ctx, noop, auth.ErrRouteDenied
	}

	prepared := skillsPreparedTarget{assignmentRequest: assignmentRequest, assignmentAuthority: assignmentAuthority, tenant: tenant, actor: p.Actor(), target: fact.stored, write: write, epoch: epoch, principal: p, request: request, authority: authority}
	return prepared.requestContext(ctx)
}

// prepareSessionTarget refreshes the issuer-owned launcher before asking the
// assignment and native permission questions on its launch-bounded principal.
// The source evidence and policy epoch are captured before those decisions and
// acquired together before the assignment, target or pack is written.
func (a skillsTargetAuthority) prepareSessionTarget(ctx, bounded context.Context, tenant model.TenantID, p auth.Principal, mc api.ModuleContext, t skills.Target, write bool) (context.Context, context.CancelFunc, error) {
	noop := func() {}
	p, source, expiry, err := a.principals.ResolveSessionLauncherScope(bounded, p, tenant)
	if err != nil {
		return ctx, noop, err
	}
	fact, epoch, err := a.loadTargetFact(ctx, bounded, tenant, p, t)
	if err != nil {
		return ctx, noop, err
	}
	source, err = auth.MergeAuthoritySnapshotBundles(source, store.AuthoritySnapshotBundle{Facts: []store.AuthorizationFactRef{epoch}})
	if err != nil {
		return ctx, noop, err
	}
	request := auth.Request{Principal: p, Tenant: tenant, Permission: targetWritePermission(t, fact.permission, write), Resource: fact.resource}
	prepared := skillsPreparedTarget{plain: true, sourceBundle: source, freshUntil: expiry, tenant: tenant, actor: p.Actor(), target: fact.stored, write: write, epoch: epoch, principal: p, request: request}
	if write {
		for _, question := range []auth.Request{{Principal: p, Tenant: tenant, Permission: "skills:assignment:write", Resource: mc.Resource}, request} {
			if err := a.retainSessionDecision(bounded, &prepared, question); err != nil {
				return ctx, noop, err
			}
		}
	} else if err := refusedBy(a.authz.Authorize(bounded, request)); err != nil {
		return ctx, noop, err
	}
	return prepared.requestContext(ctx)
}

func (a skillsTargetAuthority) retainSessionDecision(ctx context.Context, p *skillsPreparedTarget, request auth.Request) error {
	decision, bundle, err := a.principals.AuthorizeSessionLauncher(ctx, a.authz, request)
	if err != nil {
		return err
	}
	p.sourceBundle, err = auth.MergeAuthoritySnapshotBundles(p.sourceBundle, bundle)
	if err != nil {
		return err
	}
	if decision.ObservedAt.After(p.observedAt) {
		p.observedAt = decision.ObservedAt
	}
	if decision.FreshUntil.Before(p.freshUntil) {
		p.freshUntil = decision.FreshUntil
	}
	return nil
}

// loadTargetFact reads the policy authorization epoch, the caller's confined view and the
// target's stored fact in one read transaction. Both prepare paths ask it, so
// the target they authorize is the same row.
func (a skillsTargetAuthority) loadTargetFact(ctx, bounded context.Context, tenant model.TenantID, p auth.Principal, t skills.Target) (skillsTargetFact, store.AuthorizationFactRef, error) {
	var fact skillsTargetFact
	var epoch store.AuthorizationFactRef
	err := a.st.View(bounded, tenant, func(raw store.Scope) error {
		reader, ok := raw.(store.AuthorizationEpochReader)
		if !ok {
			return store.ErrStoreUnavailable
		}
		var err error
		epoch, err = reader.ReadAuthorizationEpoch(ctx)
		if err != nil {
			return err
		}
		sc, err := confineSkillsPrincipal(ctx, raw, p)
		if err != nil {
			return err
		}
		fact, err = a.targetFact(ctx, sc, t, false)
		return err
	})
	return fact, epoch, err
}

// targetWritePermission derives the native permission a target's write asks
// from the read permission its stored fact carries, workspace targets excepted:
// a workspace is written with the tenant admin permission.
func targetWritePermission(t skills.Target, read auth.Permission, write bool) auth.Permission {
	if !write {
		return read
	}
	if t.Kind == "workspace" {
		return skillsWorkspaceWrite
	}
	return auth.Permission(strings.TrimSuffix(string(read), ":read") + ":write")
}

// refusedBy maps a boolean authorization decision for a session credential's
// launcher: a denial is the route denial, and an evaluation that could not be
// established is unavailability, never a clean not-found. The reasons are the
// authorizer's own failure labels (core/auth/authorizer.go).
func refusedBy(decision auth.Decision) error {
	if decision.Allow {
		return nil
	}
	if decision.Reason == "scoped: evaluation error" || decision.Reason == "policy: evaluation error" {
		return store.ErrStoreUnavailable
	}
	return auth.ErrRouteDenied
}

func confineSkillsPrincipal(ctx context.Context, raw store.Scope, p auth.Principal) (store.Scope, error) {
	if ws, confined := p.ConfinedWorkspaceIn(raw.Tenant()); confined {
		return store.ConfineWorkspace(ctx, raw, ws)
	}
	return raw, nil
}

func (a skillsTargetAuthority) ResolveSkillsTarget(ctx context.Context, sc store.Scope, p auth.Principal, t skills.Target, write bool) (skills.StoredTarget, error) {
	prepared, ready := ctx.Value(skillsPreparedTargetKey{}).(skillsPreparedTarget)
	if ready {
		if prepared.tenant != sc.Tenant() || prepared.actor != p.Actor() || prepared.target.Target != t || prepared.write != write {
			return skills.StoredTarget{}, store.ErrConflict
		}
		if _, alreadyFenced := sc.(skillsAssignmentScope); write && !alreadyFenced {
			locker, ok := sc.(store.DirectoryAuthoritySnapshotLocker)
			if !ok {
				return skills.StoredTarget{}, store.ErrStoreUnavailable
			}
			bundle, err := prepared.authorityBundle(time.Now())
			if err != nil {
				return skills.StoredTarget{}, err
			}
			if err := locker.LockDirectoryAuthoritySnapshot(ctx, bundle); err != nil {
				return skills.StoredTarget{}, err
			}
			// A lock wait cannot extend the credential or evidence lifetime.
			if _, err := prepared.authorityBundle(time.Now()); err != nil {
				return skills.StoredTarget{}, err
			}
		} else if !write {
			reader, ok := sc.(store.AuthorizationEpochReader)
			if !ok {
				return skills.StoredTarget{}, store.ErrStoreUnavailable
			}
			epoch, err := reader.ReadAuthorizationEpoch(ctx)
			if err != nil {
				return skills.StoredTarget{}, err
			}
			if epoch != prepared.epoch {
				return skills.StoredTarget{}, store.ErrConflict
			}
		}
		if prepared.plain {
			if _, err := prepared.authorityBundle(time.Now()); err != nil {
				return skills.StoredTarget{}, err
			}
		}
	}
	fact, err := a.targetFact(ctx, sc, t, write)
	if err != nil {
		return skills.StoredTarget{}, err
	}
	if ready {
		version := prepared.target.Version
		if write {
			version++
		}
		if prepared.tenant != sc.Tenant() || prepared.actor != p.Actor() || prepared.target.Target != t || prepared.write != write || fact.stored.Version != version || fact.stored.WorkspaceID != prepared.target.WorkspaceID {
			return skills.StoredTarget{}, store.ErrConflict
		}
		return fact.stored, nil
	}
	// Runtime consumers must prepare their target too; no production launch calls
	// ResolveSelection yet. Standalone authorities retain their existing contract.
	if a.st != nil || a.authz == nil {
		return skills.StoredTarget{}, auth.ErrRouteDenied
	}
	if !a.authz.Authorize(ctx, auth.Request{Principal: p, Tenant: sc.Tenant(), Permission: fact.permission, Resource: fact.resource}).Allow {
		return skills.StoredTarget{}, auth.ErrRouteDenied
	}
	return fact.stored, nil
}

func (a skillsTargetAuthority) MutateSkillsAssignment(ctx context.Context, mc api.ModuleContext, revisionID string, fn func(store.Scope, skills.AssignmentRevisionReader) error) error {
	prepared, ok := ctx.Value(skillsPreparedTargetKey{}).(skillsPreparedTarget)
	if a.st == nil || a.authz == nil || !ok || prepared.tenant != mc.Tenant || prepared.actor != mc.Principal.Actor() || !prepared.write {
		return auth.ErrRouteDenied
	}
	// Read only the requested immutable revision before asking about its stored
	// pack ID. The catalog bridge exposes no repository to the confined handler.
	var selected skills.Revision
	err := a.st.View(ctx, mc.Tenant, func(raw store.Scope) error {
		var err error
		selected, err = skills.ReadAssignmentRevision(ctx, raw, revisionID)
		return err
	})
	if err != nil {
		return err
	}
	// The session principal has no native mutation witness. Retain the catalog's
	// typed facts and window alongside the assignment and native decisions.
	if prepared.plain {
		resource := auth.ResourceFor("skills:catalog:read")
		resource.ID, resource.WorkspaceID = selected.PackID, prepared.target.WorkspaceID
		if err := a.retainSessionDecision(ctx, &prepared, auth.Request{Principal: prepared.principal, Tenant: mc.Tenant, Permission: "skills:catalog:read", Resource: resource}); err != nil {
			return err
		}
		ctx, release := context.WithDeadline(ctx, prepared.freshUntil)
		defer release()
		ctx = context.WithValue(ctx, skillsPreparedTargetKey{}, prepared)
		return a.st.Mutate(ctx, mc.Tenant, func(sc store.Scope) error {
			bundle, err := prepared.authorityBundle(time.Now())
			if err != nil {
				return err
			}
			locker, ok := sc.(store.DirectoryAuthoritySnapshotLocker)
			if !ok {
				return store.ErrStoreUnavailable
			}
			if err := locker.LockDirectoryAuthoritySnapshot(ctx, bundle); err != nil {
				return err
			}
			if _, err := prepared.authorityBundle(time.Now()); err != nil {
				return err
			}
			confined, err := confineSkillsPrincipal(ctx, sc, prepared.principal)
			if err != nil {
				return err
			}
			return fn(skillsAssignmentScope{confined}, func(ctx context.Context, id string) (skills.Revision, error) {
				if id != revisionID {
					return skills.Revision{}, auth.ErrRouteDenied
				}
				revision, err := skills.FenceAssignmentRevision(ctx, sc, id)
				if err == nil && revision.PackID != selected.PackID {
					return skills.Revision{}, store.ErrConflict
				}
				return revision, err
			})
		})
	}
	resource := auth.ResourceFor("skills:catalog:read")
	resource.ID, resource.WorkspaceID = selected.PackID, prepared.target.WorkspaceID
	request := auth.Request{Principal: prepared.principal, Tenant: mc.Tenant, Permission: "skills:catalog:read", Resource: resource}
	bounded, cancel := context.WithTimeout(api.DetachRequestContext(ctx), 30*time.Second)
	defer cancel()
	catalogAuthority, err := a.authz.AuthorizeRouteMutation(bounded, request)
	if err != nil {
		return err
	}
	metadata, err := catalogAuthority.MetadataFor(time.Now(), request)
	if err != nil {
		return err
	}
	ctx, release := context.WithDeadline(ctx, metadata.FreshUntil)
	defer release()
	return a.st.Mutate(ctx, mc.Tenant, func(raw store.Scope) error {
		catalogBundle, err := catalogAuthority.AuthorityFor(time.Now(), request)
		if err != nil {
			return err
		}
		targetBundle, err := prepared.authorityBundle(time.Now())
		if err != nil {
			return err
		}
		bundle, err := auth.MergeAuthoritySnapshotBundles(targetBundle, catalogBundle)
		if err != nil {
			return err
		}
		locker, ok := raw.(store.DirectoryAuthoritySnapshotLocker)
		if !ok {
			return store.ErrStoreUnavailable
		}
		if err := locker.LockDirectoryAuthoritySnapshot(ctx, bundle); err != nil {
			return err
		}
		if _, err := prepared.authorityBundle(time.Now()); err != nil {
			return err
		}
		if _, err := catalogAuthority.AuthorityFor(time.Now(), request); err != nil {
			return err
		}
		sc, err := confineSkillsPrincipal(ctx, raw, prepared.principal)
		if err != nil {
			return err
		}
		return fn(skillsAssignmentScope{sc}, func(ctx context.Context, id string) (skills.Revision, error) {
			if id != revisionID {
				return skills.Revision{}, auth.ErrRouteDenied
			}
			revision, err := skills.FenceAssignmentRevision(ctx, raw, id)
			if err == nil && revision.PackID != selected.PackID {
				return skills.Revision{}, store.ErrConflict
			}
			return revision, err
		})
	})
}

func (a skillsTargetAuthority) targetFact(ctx context.Context, sc store.Scope, t skills.Target, write bool) (skillsTargetFact, error) {
	if a.sessions == nil || sc.Tenant().IsZero() {
		return skillsTargetFact{}, store.ErrStoreUnavailable
	}
	stored := skills.StoredTarget{Target: t}
	// resourceID is the stored ID the native routes authorize; a session is
	// named by its public run reference but authorized by its row ID.
	resourceID := t.ID
	var permission auth.Permission
	// entity names the stored kind when it differs from the permission's resource
	// (an agent group is gated by agent:*), so the scoped engine finds its workspace.
	var entity string
	switch t.Kind {
	case "workspace":
		workspace, err := sc.Workspaces().Get(ctx, model.ID(t.ID))
		if err != nil {
			return skillsTargetFact{}, err
		}
		permission = skillsWorkspaceRead
		if write {
			permission = skillsWorkspaceWrite
			if workspace, err = sc.Workspaces().Update(ctx, workspace); err != nil {
				return skillsTargetFact{}, err
			}
		}
		stored.Version, stored.WorkspaceID = workspace.Version, workspace.ID
	case "agent_group":
		group, err := sc.AgentGroups().Get(ctx, model.ID(t.ID))
		if err != nil {
			return skillsTargetFact{}, err
		}
		if write {
			if group, err = sc.AgentGroups().Update(ctx, group); err != nil {
				return skillsTargetFact{}, err
			}
		}
		permission, entity = agentPermission(write), "agent_group"
		stored.Version = group.Version
		if stored.WorkspaceID, err = lineageWorkspace(ctx, sc, group.WorkspaceID); err != nil {
			return skillsTargetFact{}, err
		}
	case "agent":
		agent, err := sc.Agents().Get(ctx, model.ID(t.ID))
		if err != nil {
			return skillsTargetFact{}, err
		}
		if write {
			if agent, err = sc.Agents().Update(ctx, agent); err != nil {
				return skillsTargetFact{}, err
			}
		}
		permission = agentPermission(write)
		stored.Version = agent.Version
		if stored.WorkspaceID, err = lineageWorkspace(ctx, sc, agent.WorkspaceID); err != nil {
			return skillsTargetFact{}, err
		}
	default:
		row, err := a.sessions.ReadSkillsTarget(ctx, sc, t.Kind, t.ID, write)
		if err != nil {
			return skillsTargetFact{}, err
		}
		permission, stored.Version, stored.WorkspaceID, resourceID = row.Permission, row.Version, row.WorkspaceID, row.ID.String()
	}
	resource := auth.ResourceFor(permission)
	if entity != "" {
		resource.Kind = entity
	}
	resource.ID, resource.WorkspaceID = resourceID, stored.WorkspaceID
	return skillsTargetFact{stored: stored, permission: permission, resource: resource}, nil
}
