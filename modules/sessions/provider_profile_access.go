// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// Who may use which AI tool, account and model path.
//
// A provider profile is one tool on one account, and the provider record it names is the
// model path, so assigning the profile assigns all three. A tool (a driver key) can be
// assigned on its own. The assignments are source-scoping bindings (modules/sourcescope,
// source types "profile" and "tool"): a department, a group or a person, a department
// reaching its sub-departments, a forbid, and the inheritance filter. This module only asks
// and obeys. A profile or tool nobody was assigned stays open to everyone who holds the
// route's permission, exactly as before assignments existed.

// ProfileAccess answers whether the authenticated caller may use a tool and, when
// profileRef is not empty, one profile of it (MayUse), and whether any assignment limits who
// may (Limited). An error is a refusal: the module never treats an unreadable assignment as
// open.
type ProfileAccess interface {
	MayUse(ctx context.Context, tenant model.TenantID, principal auth.Principal, profileRef, driver string) (bool, error)
	Limited(ctx context.Context, tenant model.TenantID, principal auth.Principal, profileRef, driver string) (bool, error)
}

// ErrProfileNotAssigned is the refusal of a launch under a profile or tool the caller's
// department, groups and person were not assigned.
var ErrProfileNotAssigned = forbiddenErr("this provider profile or tool is not assigned to you; request access from a department admin or owner")

// errKeyLimited is the refusal to share a provider record some assignment limits.
var errKeyLimited = forbiddenErr("this provider key is limited to the departments its profiles were assigned to; only a profile administrator can use it for another profile")

// callerMayUse is the one question: nil when principal may use the tool (and profile),
// ErrProfileNotAssigned when not, a refusal when it cannot be told. No ProfileAccess wired
// is open: the engine without assignments.
func (m *Module) callerMayUse(ctx context.Context, tenant model.TenantID, principal auth.Principal, profileRef, driver string) error {
	if m.rt.ProfileAccess == nil {
		return nil
	}
	ok, err := m.rt.ProfileAccess.MayUse(ctx, tenant, principal, profileRef, driver)
	if err != nil {
		// 503 whatever the cause: a not-found or conflict inside the lookup is not the
		// caller's profile being absent.
		m.warnf("sessions: provider profile assignment could not be read", "err", redactErr(err))
		return &runErr{http.StatusServiceUnavailable, "provider profile assignment could not be read"}
	}
	if !ok {
		return ErrProfileNotAssigned
	}
	return nil
}

// askProfileAccess is the caller's launch question, bound to the authenticated principal: a
// login a person owns is theirs alone, then the assignments.
func (m *Module) askProfileAccess(ctx context.Context, tenant model.TenantID, principal auth.Principal) func(ProviderProfile) error {
	return func(prof ProviderProfile) error {
		if err := ownerRefuses(prof, principal); err != nil {
			return err
		}
		return m.callerMayUse(ctx, tenant, principal, prof.Ref, prof.Driver)
	}
}

// ownerRefuses refuses everyone but its owner a profile that is one person's own login (the
// vendors tie a subscription to one person). It sits under the assignments: an administrator
// or an assignment never hands one person's login to another.
func ownerRefuses(prof ProviderProfile, principal auth.Principal) error {
	if prof.OwnerRef == "" || auth.PersonRefOf(principal).User == prof.OwnerRef {
		return nil
	}
	return ErrProfileNotAssigned
}

// refuseAnotherPersonsRun is ownerRefuses for a session: one that runs on a person's own
// login is acted on by that person only, in every edition. A run with no profile, or whose
// profile is gone, runs on nobody's login.
func (m *Module) refuseAnotherPersonsRun(ctx context.Context, tenant model.TenantID, rec model.Record, principal auth.Principal) error {
	ref := storedProfileSnapshot(rec).ProfileID
	if ref == "" {
		return nil
	}
	prof, err := m.GetProfile(ctx, tenant, ref)
	if errors.Is(err, ErrProfileNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return ownerRefuses(prof, principal)
}

// ownedRunRoutes is the one owner gate of the run routes: every route on one run that acts on
// it (sessions:run:write, sessions:stop:write, sessions:run:admin) asks refuseAnotherPersonsRun
// before its handler runs, so a route added later is gated too. The engine has already
// answered an unknown or concealed run with its 404. Reads keep their own rules.
type ownedRunRoutes struct {
	api.RouteRegistrar
	m *Module
}

func (o ownedRunRoutes) HandleEntity(method, pattern string, perm auth.Permission, ref api.EntityRef, h api.ModuleHandler) {
	if ref.Kind == runKind && perm != permRunRead && perm != permRunOutput {
		next, param := h, ref.IDParam
		h = func(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
			rec, err := o.m.loadRun(r.Context(), mc.Tenant, chi.URLParam(r, param))
			if err == nil {
				err = o.m.refuseAnotherPersonsRun(r.Context(), mc.Tenant, rec, mc.Principal)
			}
			if err != nil {
				writeRunErr(w, err)
				return
			}
			next(w, r, mc)
		}
	}
	o.RouteRegistrar.HandleEntity(method, pattern, perm, ref, h)
}

// refuseAnotherPersonsInput is the owner gate of the input ports a channel delivers a
// person's message through (DeliverRuntimeInput, DeliverRuntimeInputChecked): it is asked
// of the person the entry authorized, before the run is locked or touched.
func (m *Module) refuseAnotherPersonsInput(ctx context.Context, request RuntimeInputRequest) error {
	rec, err := m.loadRun(ctx, request.Target.Tenant, request.Target.RunRef)
	if err != nil {
		// The port's own answer for a run it cannot read.
		return errors.Join(ErrRuntimeInputTarget, err)
	}
	return m.refuseAnotherPersonsRun(ctx, request.Target.Tenant, rec, request.Authority.Entry.Principal)
}

// requireProfileAccess answers the routes that act on one profile by its reference: a
// profile the caller is not assigned does not exist for it (the same 404 as an unknown
// reference). An unknown or malformed reference has nothing to hide, so it passes and the
// route gives the answer it always gave.
func (m *Module) requireProfileAccess(ctx context.Context, mc api.ModuleContext, ref string) error {
	if m.rt.ProfileAccess == nil {
		return nil
	}
	prof, err := m.GetProfile(ctx, mc.Tenant, ref)
	if errors.Is(err, ErrProfileNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	err = m.callerMayUse(ctx, mc.Tenant, mc.Principal, prof.Ref, prof.Driver)
	if errors.Is(err, ErrProfileNotAssigned) {
		return ErrProfileNotFound
	}
	return err
}

// RequireProfileAssigned is requireProfileAccess for the skills routes that pin, read or list
// the skills of one profile: a profile the principal is not assigned answers as a route the
// principal may not use, which those routes conceal as a missing target. An unknown reference
// passes and keeps the answer the target read gives it.
func (m *Module) RequireProfileAssigned(ctx context.Context, tenant model.TenantID, p auth.Principal, ref string) error {
	err := m.requireProfileAccess(ctx, api.ModuleContext{Tenant: tenant, Principal: p}, ref)
	if errors.Is(err, ErrProfileNotFound) {
		return auth.ErrRouteDenied
	}
	return err
}

// requireAccountAccess is requireProfileAccess for the account routes whose unknown
// reference answers "provider account not found": a profile the caller is not assigned
// answers the same.
func (m *Module) requireAccountAccess(ctx context.Context, mc api.ModuleContext, ref string) error {
	err := m.requireProfileAccess(ctx, mc, ref)
	if errors.Is(err, ErrProfileNotFound) {
		return ErrAccountNotFound
	}
	return err
}

// requireToolAccess refuses a tool the caller was not assigned.
func (m *Module) requireToolAccess(ctx context.Context, mc api.ModuleContext, driver string) error {
	return m.callerMayUse(ctx, mc.Tenant, mc.Principal, "", driver)
}

// requireNamedTool refuses the tool a request names (a driver key, as the resolve and
// preview routes take it), before a profile of it is chosen or made. Only a session tool can
// be assigned (sourcescope refuses any other key), so a name that is none is not limited and
// keeps the answer the call that follows would give: a 400 from resolve and preview, a profile
// made as before from the create routes.
func (m *Module) requireNamedTool(ctx context.Context, mc api.ModuleContext, driverIn string) error {
	if m.rt.ProfileAccess == nil {
		return nil
	}
	driver, _, err := resolveDriver(driverIn)
	if err != nil {
		return nil
	}
	return m.requireToolAccess(ctx, mc, driver)
}

// visibleTo keeps the entries of a listing the caller may use. key reports the profile
// reference and driver of one entry. An unreadable assignment fails the listing rather
// than hiding or showing an entry by guess.
func visibleTo[T any](ctx context.Context, m *Module, mc api.ModuleContext, items []T, key func(T) (profileRef, driver string)) ([]T, error) {
	if m.rt.ProfileAccess == nil {
		return items, nil
	}
	return visibleBy(items, mayUseEntry(ctx, m, mc, key))
}

// visibleBy keeps the entries allow does not refuse as unassigned; any other error fails the listing.
func visibleBy[T any](items []T, allow func(T) error) ([]T, error) {
	out := make([]T, 0, len(items))
	for _, item := range items {
		err := allow(item)
		if err == nil {
			out = append(out, item)
		} else if !errors.Is(err, ErrProfileNotAssigned) {
			return nil, err
		}
	}
	return out, nil
}

// mayUseEntry is allow for an entry that names a profile and a driver.
func mayUseEntry[T any](ctx context.Context, m *Module, mc api.ModuleContext, key func(T) (profileRef, driver string)) func(T) error {
	return func(item T) error {
		ref, driver := key(item)
		return m.callerMayUse(ctx, mc.Tenant, mc.Principal, ref, driver)
	}
}

// eachProfileOnRecord calls fn for the profiles that run on a provider record, until fn
// reports it is done. A retired profile runs nothing, so only a caller that wants the record's
// whole history asks for them.
func (m *Module) eachProfileOnRecord(ctx context.Context, tenant model.TenantID, recordRef string, withRetired bool, fn func(ProviderProfile) (done bool, err error)) error {
	q := model.Query{Limit: 100, Filters: []model.Filter{eq(colPPProviderRecordRef, recordRef)}}
	for {
		profiles, page, err := m.ListProfiles(ctx, tenant, "", q)
		if err != nil {
			return err
		}
		for _, p := range profiles {
			if p.State == ProfileRetired && !withRetired {
				continue
			}
			if done, err := fn(p); err != nil || done {
				return err
			}
		}
		if !page.HasMore {
			return nil
		}
		if page.Cursor == "" {
			// An unread rest is not an empty one: stopping here would read "no profile limits
			// this key" for profiles never looked at.
			return &runErr{http.StatusServiceUnavailable, "provider profile listing could not be read to its end"}
		}
		q.Cursor = page.Cursor
	}
}

// requireRecordAccess keeps the model path with the assignment. A provider record (the
// key a profile's managed launches run on) belongs to the people its profiles were assigned
// to: the caller must be able to use at least one live profile that runs on it. A record no
// profile runs on yet, or one a profile open to the caller runs on, is open to it as before.
// A retired profile runs nothing, so it never makes a key usable; one the caller was not
// assigned still keeps the key from it, so retiring the profile of a department's key does
// not hand the key to everyone else.
//
// shortcut: each key asked costs one profile listing and two resolver reads per profile on it,
// also when nothing was assigned; fine at tens of keys. Short-circuit on "no profile or tool
// binding stored for this tenant" if a measurement ever shows the record lists slow.
func (m *Module) requireRecordAccess(ctx context.Context, mc api.ModuleContext, recordRef string) error {
	if m.rt.ProfileAccess == nil || recordRef == "" {
		return nil
	}
	var usable, denied bool
	err := m.eachProfileOnRecord(ctx, mc.Tenant, recordRef, true, func(p ProviderProfile) (bool, error) {
		err := m.callerMayUse(ctx, mc.Tenant, mc.Principal, p.Ref, p.Driver)
		switch {
		case err == nil:
			usable = usable || p.State != ProfileRetired
			return usable, nil
		case errors.Is(err, ErrProfileNotAssigned):
			denied = true
			return false, nil
		default:
			return false, err
		}
	})
	if err != nil {
		return err
	}
	if denied && !usable {
		return ErrProfileNotAssigned
	}
	return nil
}

// addressedRecord is the record a route acts on for the caller. A record the caller may use no
// profile on is addressed as a reference nothing has, so every route answers it exactly as it
// answers an unknown record, whatever the body or the wiring: the same checks in the same order.
func (m *Module) addressedRecord(ctx context.Context, mc api.ModuleContext, ref string) (string, error) {
	err := m.requireRecordAccess(ctx, mc, ref)
	if errors.Is(err, ErrProfileNotAssigned) {
		return providerRecordRefPrefix + model.NewID().String(), nil
	}
	return ref, err
}

// MayUseProviderRecord reports whether principal may see the provider record ref, by the rule
// of the record's own routes. The models list asks it for the entries that are a record and
// nothing else. An unreadable assignment is an error, never a yes.
func (m *Module) MayUseProviderRecord(ctx context.Context, tenant model.TenantID, principal auth.Principal, ref string) (bool, error) {
	err := m.requireRecordAccess(ctx, api.ModuleContext{Tenant: tenant, Principal: principal}, ref)
	if errors.Is(err, ErrProfileNotAssigned) {
		return false, nil
	}
	return err == nil, err
}

// withUsableKey keeps the tools whose readiness the caller may be told. A tool that would run
// on a key (not on its own login) is shown to who may use a profile on that key, as the preview
// shows it: the answer names the key, its default model and its model list.
func (m *Module) withUsableKey(ctx context.Context, mc api.ModuleContext, tools []ToolReadiness) ([]ToolReadiness, error) {
	out := make([]ToolReadiness, 0, len(tools))
	for _, t := range tools {
		if t.Provider != nil {
			if err := m.requireRecordAccess(ctx, mc, t.Provider.Ref); errors.Is(err, ErrProfileNotAssigned) {
				continue
			} else if err != nil {
				return nil, err
			}
		}
		out = append(out, t)
	}
	return out, nil
}

// requireRecordShare is requireRecordAccess for a profile made on a record or moved onto it.
// Such a profile is open to everyone who may run a session unless an assignment is written for
// it, so on a record some assignment limits (a profile of it, retired or not, was assigned) it
// widens who reaches the key, also when the caller may use the profile that runs on it now:
// only a profile administrator does that. current is the record the profile is on already.
// Both are compared as the store keeps them (CreateProfile and PatchProfile trim the reference
// they store), so padding a reference with spaces does not step around the check.
func (m *Module) requireRecordShare(ctx context.Context, mc api.ModuleContext, recordRef, current string) error {
	recordRef, current = strings.TrimSpace(recordRef), strings.TrimSpace(current)
	if m.rt.ProfileAccess == nil || recordRef == "" || recordRef == current {
		return nil
	}
	var limited bool
	err := m.eachProfileOnRecord(ctx, mc.Tenant, recordRef, true, func(p ProviderProfile) (bool, error) {
		var err error
		limited, err = m.callerLimited(ctx, mc.Tenant, mc.Principal, p.Ref, p.Driver)
		return limited, err
	})
	if err != nil || !limited {
		return err
	}
	if !m.mayAdministerProfiles(ctx, mc) {
		return errKeyLimited
	}
	return nil
}

// callerLimited is callerMayUse's sibling: whether any assignment limits who may use the
// profile or its tool, whoever asks. An unreadable assignment is a refusal.
func (m *Module) callerLimited(ctx context.Context, tenant model.TenantID, principal auth.Principal, profileRef, driver string) (bool, error) {
	limited, err := m.rt.ProfileAccess.Limited(ctx, tenant, principal, profileRef, driver)
	if err != nil {
		m.warnf("sessions: provider profile assignment could not be read", "err", redactErr(err))
		return false, &runErr{http.StatusServiceUnavailable, "provider profile assignment could not be read"}
	}
	return limited, nil
}

// mayAdministerProfiles reports whether the authenticated caller holds the tenant-wide
// profile administration, which the assignments never limit, and is not confined to one
// workspace. No authorizer wired is a no.
func (m *Module) mayAdministerProfiles(ctx context.Context, mc api.ModuleContext) bool {
	if m.WorkAuthorizer == nil {
		return false
	}
	if _, confined := mc.Principal.ConfinedWorkspaceIn(mc.Tenant); confined {
		return false
	}
	return m.WorkAuthorizer.Authorize(ctx, auth.Request{
		Principal: mc.Principal, Tenant: mc.Tenant,
		Permission: permProfileAdmin, Resource: auth.ResourceFor(permProfileAdmin),
	}).Allow
}

// resolveProfileForCaller is ResolveProfile for a caller the assignments apply to: the tool
// must be assigned to it, the key the rule would run on must be one it may use, and a profile
// the rule reuses must be assigned to it. All three are asked before the profile is made. A
// rule that would make a profile on the key shares the key (a new profile is open to everyone
// who may run a session), so it is asked as sharing: no active profile reuses it when the
// person disabled the one assigned to them, and another tool of the key has none yet.
func (m *Module) resolveProfileForCaller(ctx context.Context, mc api.ModuleContext, driverIn string) (ResolvedProfile, error) {
	if m.rt.ProfileAccess != nil {
		if err := m.requireNamedTool(ctx, mc, driverIn); err != nil {
			return ResolvedProfile{}, err
		}
		preview, err := m.PreviewProfile(ctx, mc.Tenant, driverIn)
		if err != nil {
			return ResolvedProfile{}, err
		}
		if preview.Provider != nil {
			if preview.Profile.Ref == "" {
				err = m.requireRecordShare(ctx, mc, preview.Provider.Ref, "")
			} else {
				err = m.requireRecordAccess(ctx, mc, preview.Provider.Ref)
			}
			if err != nil {
				return ResolvedProfile{}, err
			}
		}
	}
	got, err := m.ResolveProfile(ctx, mc.Tenant, driverIn)
	if err != nil {
		return ResolvedProfile{}, err
	}
	if err := m.callerMayUse(ctx, mc.Tenant, mc.Principal, got.Profile.Ref, got.Profile.Driver); err != nil {
		return ResolvedProfile{}, err
	}
	return got, nil
}

// listVisible pages like the listing it wraps but shows only what the caller may use. A
// page the caller can use nothing on is not returned empty with more behind it: the next
// page is read until one has an entry or the listing ends, so a client that stops on an
// empty page never misses its own profiles. A page is therefore at most the limit and
// possibly shorter.
func listVisible[T any](ctx context.Context, m *Module, mc api.ModuleContext, q model.Query,
	fetch func(model.Query) ([]T, model.Page, error), key func(T) (profileRef, driver string)) ([]T, model.Page, error) {
	return listVisibleBy(m, q, fetch, mayUseEntry(ctx, m, mc, key))
}

// listVisibleBy is listVisible for entries allow judges by itself (a provider record is
// judged by the profiles that run on it).
func listVisibleBy[T any](m *Module, q model.Query, fetch func(model.Query) ([]T, model.Page, error), allow func(T) error) ([]T, model.Page, error) {
	items, page, err := fetch(q)
	if err != nil || m.rt.ProfileAccess == nil {
		return items, page, err
	}
	out, err := visibleBy(items, allow)
	for err == nil && len(out) == 0 && page.HasMore && page.Cursor != "" {
		q.Cursor = page.Cursor
		if items, page, err = fetch(q); err != nil {
			break
		}
		out, err = visibleBy(items, allow)
	}
	return out, page, err
}

// refuseUnassignedTarget is the move check on the profile a session would continue in:
// the caller must be its owner and be assigned it, as for a launch.
func (m *Module) refuseUnassignedTarget(ctx context.Context, mc api.ModuleContext, ref string) error {
	prof, err := m.GetProfile(ctx, mc.Tenant, ref)
	if err != nil && m.rt.ProfileAccess == nil {
		// No assignment to ask: the move answers a target it cannot read itself, as before.
		return nil
	}
	if err != nil {
		return err
	}
	return m.askProfileAccess(ctx, mc.Tenant, mc.Principal)(prof)
}
