// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"errors"
	"strings"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The governance retirement step. When a tenant removes an account, this step
// removes what governance stores in that tenant that lets the account act there,
// and reports, by id, what the tenant itself must resolve:
//   - the account's user-subject scoped grants are deleted, with the same
//     discipline as a revoke (the managed projection is re-planned, the tenant's
//     authorization epoch advances when the projection changes);
//   - the content every surface whose content can authorize has put in force is
//     matched against every alias of the account, including its credential ids:
//     for a Cedar surface its selected revision, for a managed surface its
//     newest published revision and the newest one distributed to hosts; a hit
//     blocks the retirement until the tenant revises the policy (authored
//     content is never rewritten);
//   - an agent or other non-human identity the account sponsors or owns blocks the
//     retirement until the tenant reassigns it;
//   - a stored revision whose surface no registry knows blocks it too.
// The step's transaction first pins the tenant's authorization epoch and the
// account's authority version the pass read, so a pass that read before a lift or
// a new offboard conflicts and changes nothing.

// retirementModule is the declared module name of this step.
const retirementModule = "governance"

// retirementCovers are the counted columns this module's step reads.
var retirementCovers = []string{
	string(scopedGrantKind) + "." + colSGSubjectRef,
	string(revisionKind) + "." + colRevContent,
	string(nhiLifecycleKind) + "." + colNHISponsorRef,
	string(nhiLifecycleKind) + "." + colNHIOwnerRef,
}

// RetirementStep returns this module's retirement step.
func (m *Module) RetirementStep() auth.RetirementStep { return retirementStep{m: m} }

// RetirementCovers returns the counted columns this module's step reads, as
// kind.column.
func (m *Module) RetirementCovers() []string { return append([]string(nil), retirementCovers...) }

type retirementStep struct{ m *Module }

// Module implements auth.RetirementStep.
func (s retirementStep) Module() string { return retirementModule }

// RetireUser implements auth.RetirementStep.
func (s retirementStep) RetireUser(ctx context.Context, req auth.RetirementRequest) (auth.RetirementOutcome, error) {
	m := s.m
	if m == nil || m.data == nil {
		return auth.RetirementOutcome{}, errors.New("governance: the retirement step has no data handle")
	}
	var (
		out       auth.RetirementOutcome
		reload    bool
		freshness FreshnessRecord
	)
	err := m.data.Mutate(ctx, req.Tenant, func(sc store.Scope) error {
		out = auth.RetirementOutcome{}
		if _, err := auth.PinFence(ctx, sc, auth.FenceAuthorization, []store.UserAuthorityFactRef{
			{UserID: req.User, Version: req.UserAuthority},
		}); err != nil {
			return err
		}
		if err := lockOrCheckPolicyAuthorizationEpoch(ctx, sc, true); err != nil {
			return err
		}
		changed, fr, err := retireUserGrants(ctx, sc, req.User)
		if err != nil {
			return err
		}
		reload, freshness = changed, fr
		blocking, unknown, err := revisionsNaming(ctx, sc, retirementAliases(req))
		if err != nil {
			return err
		}
		nhi, err := identitiesNaming(ctx, sc, req.ExternalID)
		if err != nil {
			return err
		}
		out.Blocking = append(blocking, nhi...)
		out.UnknownKinds = unknown
		reader, ok := sc.(store.AuthorizationEpochReader)
		if !ok {
			return policyAuthorizationEpochUnavailable("scope lacks authorization epoch capability", nil)
		}
		fact, err := reader.ReadAuthorizationEpoch(ctx)
		if err != nil {
			return err
		}
		out.FactVersion = fact.Version
		return nil
	})
	if err != nil {
		return auth.RetirementOutcome{}, err
	}
	if reload {
		m.reloadTenantGrantsLogged(ctx, req.Tenant, freshness)
	}
	return out, nil
}

// retireUserGrants deletes user's user-subject scoped grants in the scope's
// tenant with the revoke discipline, and reports whether the managed projection
// changed. The caller has pinned the tenant's authorization epoch.
func retireUserGrants(ctx context.Context, sc store.Scope, user model.ID) (bool, FreshnessRecord, error) {
	repo, err := sc.Ext(scopedGrantKind)
	if err != nil {
		return false, FreshnessRecord{}, err
	}
	recs, err := listAll(ctx, repo, eq(colSGSubjectKind, subjectUser), eq(colSGSubjectRef, user.String()))
	if err != nil || len(recs) == 0 {
		return false, FreshnessRecord{}, err
	}
	gone := make(map[model.ID]bool, len(recs))
	for _, r := range recs {
		gone[model.ID(r.String(model.ColID))] = true
	}
	state, err := loadManagedProjectionState(ctx, sc)
	if err != nil {
		return false, FreshnessRecord{}, err
	}
	prospective := state
	prospective.grants = make([]scopedGrant, 0, len(state.grants))
	for _, g := range state.grants {
		if !gone[g.ID] {
			prospective.grants = append(prospective.grants, g)
		}
	}
	plan, err := planManagedProjection(ctx, sc, prospective)
	if err != nil {
		return false, FreshnessRecord{}, err
	}
	if plan.changed {
		if err := advancePolicyAuthorizationEpoch(ctx, sc); err != nil {
			return false, FreshnessRecord{}, err
		}
	}
	for id := range gone {
		if err := repo.Delete(ctx, id); err != nil {
			return false, FreshnessRecord{}, err
		}
		if _, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor: model.ActorSystem, ActorKind: model.ActorSystem,
			Action: "governance.rbac.retire", TargetKind: scopedGrantKind, TargetID: id,
			Meta: map[string]any{"subject_kind": subjectUser},
		}); err != nil {
			return false, FreshnessRecord{}, err
		}
	}
	if err := appendManagedProjection(ctx, sc, plan, model.ActorSystem); err != nil {
		return false, FreshnessRecord{}, err
	}
	if err := persistManagedProjectionFreshness(ctx, sc, plan); err != nil {
		return false, FreshnessRecord{}, err
	}
	return plan.changed, plan.freshness, nil
}

// retirementAliases are the values untyped policy content may name the account
// by: its id, its email, its external id and its credential ids.
func retirementAliases(req auth.RetirementRequest) []string {
	out := []string{req.User.String()}
	if req.Email != "" {
		out = append(out, req.Email)
	}
	if req.ExternalID != "" {
		out = append(out, req.ExternalID)
	}
	for _, c := range req.Credentials {
		out = append(out, c.String())
	}
	return out
}

// revisionsNaming returns the revisions in force, of every surface whose content
// can authorize, that name one of aliases, as blocking references; and the
// revision table when it holds a row whose surface no registry knows. It reads
// the whole revision table for the unknown surfaces, not only the surfaces it
// knows.
func revisionsNaming(ctx context.Context, sc store.Scope, aliases []string) ([]string, []string, error) {
	repo, err := sc.Ext(revisionKind)
	if err != nil {
		return nil, nil, err
	}
	all, err := listAll(ctx, repo)
	if err != nil {
		return nil, nil, err
	}
	var unknown []string
	for _, r := range all {
		if !surfaces.accepts(r.String(colRevSurface)) {
			unknown = append(unknown, string(revisionKind))
			break
		}
	}
	var blocking []string
	for _, e := range surfaces.entries {
		if e.content == nil || !e.content.Counted() {
			continue
		}
		numbers, err := revisionsInForce(ctx, sc, e, all)
		if errors.Is(err, model.ErrUnknownKind) {
			// The surface's selection names a stream nothing derives: that row is
			// of a kind no registry knows, and it blocks like any other.
			if len(unknown) == 0 {
				unknown = append(unknown, string(revisionKind))
			}
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		for _, r := range all {
			if r.String(colRevSurface) != e.name || !numbers[r.Int(colRevNumber)] {
				continue
			}
			if namesAny(r.String(colRevContent), aliases) {
				blocking = append(blocking, string(revisionKind)+":"+r.String(model.ColID))
			}
		}
	}
	return blocking, unknown, nil
}

// revisionsInForce returns the revision numbers of surface e whose content is in
// force, by the surface's own rule: a Cedar or OPA surface's selected revision,
// which is what its loader compiles; a managed surface's newest published
// revision, which is what a publish puts in force, and the newest revision
// distributed to hosts, which is what they apply while a newer one has no signed
// artifact yet. all is every revision row.
func revisionsInForce(ctx context.Context, sc store.Scope, e surfaceEntry, all []model.Record) (map[int64]bool, error) {
	out := map[int64]bool{}
	if e.family != surfaceFamilyManaged {
		number, found, err := activeRevisionNumber(ctx, sc, e.name)
		if err != nil || !found {
			return out, err
		}
		out[number] = true
		return out, nil
	}
	var newest int64
	for _, r := range all {
		if r.String(colRevSurface) == e.name && r.Int(colRevNumber) > newest {
			newest = r.Int(colRevNumber)
		}
	}
	if newest > 0 {
		out[newest] = true
	}
	dist, err := sc.Ext(distributionKind)
	if err != nil {
		return nil, err
	}
	artifacts, err := listAll(ctx, dist, eq(colDistSurface, e.name))
	if err != nil {
		return nil, err
	}
	var distributed int64
	for _, a := range artifacts {
		if n := a.Int(colDistRevision); n > distributed {
			distributed = n
		}
	}
	if distributed > 0 {
		out[distributed] = true
	}
	return out, nil
}

// identitiesNaming returns the non-human identities the account sponsors or
// owns, by external id, as blocking references naming the row and the identity.
func identitiesNaming(ctx context.Context, sc store.Scope, externalID string) ([]string, error) {
	if externalID == "" {
		return nil, nil
	}
	repo, err := sc.Ext(nhiLifecycleKind)
	if err != nil {
		return nil, err
	}
	var out []string
	seen := map[string]bool{}
	for _, col := range []string{colNHISponsorRef, colNHIOwnerRef} {
		recs, err := listAll(ctx, repo, eq(col, externalID))
		if err != nil {
			return nil, err
		}
		for _, r := range recs {
			id := r.String(model.ColID)
			if seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, string(nhiLifecycleKind)+":"+id+":"+r.String(colNHIIdentityRef))
		}
	}
	return out, nil
}

// namesAny reports whether content names one of aliases. A canonical id, the
// account's or one of its credentials', matches wherever it appears, in any
// case, exactly as the writers' fence finds it: its fixed form cannot run into
// another value by accident. Any other alias matches as a whole token, an
// occurrence not run together with a neighbouring identifier character; email
// addresses match case-insensitively.
func namesAny(content string, aliases []string) bool {
	lower := strings.ToLower(content)
	for _, a := range aliases {
		if a == "" {
			continue
		}
		if canonicalID(a) {
			if strings.Contains(lower, strings.ToLower(a)) {
				return true
			}
			continue
		}
		hay, needle := content, a
		if strings.Contains(a, "@") {
			hay, needle = lower, strings.ToLower(a)
		}
		for i := 0; ; {
			j := strings.Index(hay[i:], needle)
			if j < 0 {
				break
			}
			start, end := i+j, i+j+len(needle)
			if !identChar(hay, start-1) && !identChar(hay, end) {
				return true
			}
			i = start + 1
		}
	}
	return false
}

// canonicalID reports whether a is an id in its canonical text form.
func canonicalID(a string) bool {
	if len(a) != 36 {
		return false
	}
	_, err := model.ParseID(a)
	return err == nil
}

// identChar reports whether s[i] exists and would run together with an alias.
func identChar(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	c := s[i]
	return c == '_' || c == '-' || c == '.' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
