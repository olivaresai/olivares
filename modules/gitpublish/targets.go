// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package gitpublish

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TargetInput creates a target. It selects approved bindings by id; it
// carries no secret reference, endpoint, installation id or path.
type TargetInput struct {
	Workspace         model.ID
	CredentialBinding string
	RepositoryBinding string
	PushPrefix        string
	MergeBases        []string
}

// TargetUpdate edits a target under optimistic concurrency. Empty binding ids
// keep the current ones.
type TargetUpdate struct {
	ExpectedVersion   int64
	CredentialBinding string
	RepositoryBinding string
	PushPrefix        string
	MergeBases        []string
}

var branchRe = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

func validBranch(b string) bool {
	return b != "" && branchRe.MatchString(b) && !strings.Contains(b, "..") && !strings.HasPrefix(b, "/") &&
		!strings.HasSuffix(b, "/") && !strings.HasSuffix(b, ".lock") && !strings.Contains(b, "//")
}

func validPrefix(p string) bool {
	return strings.HasSuffix(p, "/") && validBranch(strings.TrimSuffix(p, "/"))
}

// bindings selects the approved credential and repository bindings of tg in
// its workspace and checks the repository is inside the credential's owners.
func (m *Module) bindings(ctx context.Context, tenant model.TenantID, workspace model.ID, cbID, rbID string) (CredentialBinding, RepositoryBinding, *Error) {
	cb, err := m.opts.Custody.CredentialBinding(ctx, tenant, workspace, cbID)
	if err != nil {
		if errors.Is(err, ErrBindingNotApproved) {
			return cb, RepositoryBinding{}, refuse("binding_not_approved", http.StatusUnprocessableEntity)
		}
		return cb, RepositoryBinding{}, errUnavailable
	}
	rb, err := m.opts.Custody.RepositoryBinding(ctx, tenant, workspace, rbID)
	if err != nil {
		if errors.Is(err, ErrBindingNotApproved) {
			return cb, rb, refuse("binding_not_approved", http.StatusUnprocessableEntity)
		}
		return cb, rb, errUnavailable
	}
	for _, o := range cb.AllowedOwners {
		if strings.EqualFold(o, rb.Owner) {
			return cb, rb, nil
		}
	}
	return cb, rb, refuse("repository_outside_binding", http.StatusUnprocessableEntity)
}

func (m *Module) admitTarget(ctx context.Context, c Caller, target, workspace model.ID) (Admission, *Error) {
	adm, err := m.opts.Authority.Admit(ctx, c.Principal, c.Tenant, Question{
		Permission: permTargetAdmin, Action: actionTarget, MinimumAAL: auth.AAL3, Target: target, Workspace: workspace,
	})
	if err != nil {
		return nil, authError(err)
	}
	return adm, nil
}

// prefixOverlapsBase reports whether a push prefix would let a push land on
// a merge base.
func prefixOverlapsBase(prefix string, bases []string) bool {
	for _, b := range bases {
		if strings.HasPrefix(b, prefix) {
			return true
		}
	}
	return false
}

func validTargetFields(prefix string, bases []string) bool {
	if !validPrefix(prefix) || len(bases) > 16 {
		return false
	}
	for _, b := range bases {
		if !validBranch(b) || strings.Contains(b, ",") {
			return false
		}
	}
	return true
}

// CreateTarget records an approved target (target:admin, AAL3).
func (m *Module) CreateTarget(ctx context.Context, c Caller, in TargetInput) (Target, error) {
	if !m.wired() {
		return Target{}, errUnavailable
	}
	if in.Workspace.IsZero() || in.CredentialBinding == "" || in.RepositoryBinding == "" || !validTargetFields(in.PushPrefix, in.MergeBases) {
		return Target{}, errInvalid
	}
	if prefixOverlapsBase(in.PushPrefix, in.MergeBases) {
		return Target{}, refuse("push_prefix_overlaps_base", http.StatusUnprocessableEntity)
	}
	actx, cancel := m.admissionContext(ctx)
	defer cancel()
	adm, e := m.admitTarget(actx, c, "", in.Workspace)
	if e != nil {
		return Target{}, e
	}
	if _, _, e := m.bindings(actx, c.Tenant, in.Workspace, in.CredentialBinding, in.RepositoryBinding); e != nil {
		return Target{}, e
	}
	var out Target
	err := m.data.Mutate(actx, c.Tenant, func(sc store.Scope) error {
		if err := adm.Lock(actx, sc, txNow(actx, sc, m.now())); err != nil {
			return err
		}
		repo, err := sc.Ext(kindTarget)
		if err != nil {
			return err
		}
		rec, err := repo.Create(actx, model.Record{
			"workspace_id": in.Workspace.String(), "credential_binding": in.CredentialBinding, "repository_binding": in.RepositoryBinding,
			"push_prefix": in.PushPrefix, "merge_bases": strings.Join(in.MergeBases, ","), "created_by": adm.Subject().Actor,
		})
		if err != nil {
			return err
		}
		out = targetFrom(rec)
		return appendAudit(actx, sc, adm.Subject().Actor, adm.Subject().ActorKind, "gitpublish.target.created", kindTarget, out.ID, targetMeta(out))
	})
	if err != nil {
		return Target{}, storeError(err)
	}
	return out, nil
}

// UpdateTarget edits a target (target:admin, AAL3). Every edit bumps the
// version, so an intent admitted on the old version refuses at A4.
func (m *Module) UpdateTarget(ctx context.Context, c Caller, id model.ID, up TargetUpdate) (Target, error) {
	if !m.wired() {
		return Target{}, errUnavailable
	}
	if !validTargetFields(up.PushPrefix, up.MergeBases) {
		return Target{}, errInvalid
	}
	if prefixOverlapsBase(up.PushPrefix, up.MergeBases) {
		return Target{}, refuse("push_prefix_overlaps_base", http.StatusUnprocessableEntity)
	}
	actx, cancel := m.admissionContext(ctx)
	defer cancel()
	var cur Target
	if err := m.data.View(actx, c.Tenant, func(sc store.Scope) error {
		var err error
		cur, err = loadTarget(actx, sc, id)
		return err
	}); err != nil {
		return Target{}, storeError(err)
	}
	adm, e := m.admitTarget(actx, c, cur.ID, cur.Workspace)
	if e != nil {
		return Target{}, e
	}
	cbID, rbID := cur.CredentialBinding, cur.RepositoryBinding
	if up.CredentialBinding != "" {
		cbID = up.CredentialBinding
	}
	if up.RepositoryBinding != "" {
		rbID = up.RepositoryBinding
	}
	if _, _, e := m.bindings(actx, c.Tenant, cur.Workspace, cbID, rbID); e != nil {
		return Target{}, e
	}
	var out Target
	err := m.data.Mutate(actx, c.Tenant, func(sc store.Scope) error {
		if err := adm.Lock(actx, sc, txNow(actx, sc, m.now())); err != nil {
			return err
		}
		repo, err := sc.Ext(kindTarget)
		if err != nil {
			return err
		}
		rec, err := repo.Get(actx, id)
		if err != nil {
			return err
		}
		if rec.Int(model.ColVersion) != up.ExpectedVersion {
			return store.ErrConflict
		}
		rec["credential_binding"], rec["repository_binding"] = cbID, rbID
		rec["push_prefix"], rec["merge_bases"] = up.PushPrefix, strings.Join(up.MergeBases, ",")
		rec, err = repo.Update(actx, rec)
		if err != nil {
			return err
		}
		out = targetFrom(rec)
		return appendAudit(actx, sc, adm.Subject().Actor, adm.Subject().ActorKind, "gitpublish.target.updated", kindTarget, out.ID, targetMeta(out))
	})
	if err != nil {
		return Target{}, storeError(err)
	}
	return out, nil
}

// targetMeta is the bounded metadata of a target event: no binding secret,
// endpoint or path.
func targetMeta(t Target) map[string]any {
	return map[string]any{
		"workspace_id": t.Workspace.String(), "credential_binding": t.CredentialBinding, "repository_binding": t.RepositoryBinding,
		"push_prefix": t.PushPrefix, "merge_bases": strings.Join(t.MergeBases, ","), "version": strconv.FormatInt(t.Version, 10),
	}
}

// storeError maps a store failure onto the closed vocabulary.
func storeError(err error) error {
	if err == nil {
		return nil
	}
	var e *Error
	switch {
	case errors.As(err, &e):
		return e
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrTenantViolation), errors.Is(err, store.ErrWorkspaceConfinement):
		return errNotFound
	case errors.Is(err, store.ErrConflict):
		return refuse("version_conflict", http.StatusConflict)
	case errors.Is(err, auth.ErrRouteUndecided), errors.Is(err, auth.ErrRouteDenied):
		return authError(err)
	}
	return errUnavailable
}
