// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// SessionIdentitySnapshot describes an already persisted identity. Reading it
// neither creates/touches aliases nor follows a merge into a different identity.
type SessionIdentitySnapshot struct {
	TenantID                model.TenantID
	SID, Origin, MergedInto string
	WorkspaceID             model.ID
	Version                 int64
}

type SessionIdentityReader interface {
	ReadSessionIdentity(context.Context, model.TenantID, string) (SessionIdentitySnapshot, error)
}

func (m *Module) ReadSessionIdentity(ctx context.Context, tenant model.TenantID, sid string) (SessionIdentitySnapshot, error) {
	if m == nil || m.data == nil {
		return SessionIdentitySnapshot{}, errors.New("sessions: identity reader has no data handle")
	}
	if tenant == "" || !validCanonicalSID(sid) {
		return SessionIdentitySnapshot{}, store.ErrNotFound
	}
	var out SessionIdentitySnapshot
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		var err error
		out, err = ReadSessionIdentityInScope(ctx, sc, sid)
		return err
	})
	return out, err
}

// ReadSessionIdentityInScope reads the exact SID from an existing tenant View.
// A consumer composing core and private evidence can keep identity version and
// lineage facts in that View instead of nesting a second store transaction.
func ReadSessionIdentityInScope(ctx context.Context, sc store.Scope, sid string) (SessionIdentitySnapshot, error) {
	if sc == nil || sc.Tenant().IsZero() || !validCanonicalSID(sid) {
		return SessionIdentitySnapshot{}, store.ErrNotFound
	}
	rec, found, err := findIdentity(ctx, sc, sid)
	if err != nil {
		return SessionIdentitySnapshot{}, err
	}
	if !found {
		return SessionIdentitySnapshot{}, store.ErrNotFound
	}
	out := SessionIdentitySnapshot{TenantID: model.TenantID(rec.String(model.ColTenantID)), SID: rec.String(colSID), Origin: rec.String(colOrigin), MergedInto: rec.String(colMergedInto), WorkspaceID: model.ID(rec.String(colIDWorkspaceID)), Version: rec.Int(model.ColVersion)}
	if out.TenantID != sc.Tenant() || out.SID != sid || out.Version < 1 {
		return SessionIdentitySnapshot{}, errors.New("sessions: noncanonical identity snapshot")
	}
	return out, nil
}

// RunLaunchSnapshot is the presented, authority-free view of one run's launch
// identity. A work item id is a REFERENCE and authorizes nothing; the lineage is
// not presented at all, because a caller that may read this row has already been
// confined to the workspace that owns it.
type RunLaunchSnapshot struct {
	RunRef          string
	RuntimeLaunchID model.ID
	Lifecycle       string
	WorkItemID      model.ID
}

// RunLaunchReader is the narrow port a presentation consumer depends on. The
// caller names the run by the Run arm's run_ref and names the stored workspace
// its own route authorized; the reader confines itself to that workspace.
type RunLaunchReader interface {
	ReadRunLaunch(context.Context, model.TenantID, model.ID, string) (RunLaunchSnapshot, error)
}

var _ RunLaunchReader = (*Module)(nil)

// ReadRunLaunch presents one run's launch identity through the CONFINED reader.
//
// ⛔ IT NEVER REPAIRS AND IT NEVER INVENTS A LINEAGE. A run whose authorization
// workspace is still NULL is hidden by the descriptor's unset-is-hidden rule, so
// it is indistinguishable here from an absent or foreign run — all three are
// ErrNotFound, and telling them apart would be an existence probe. A run that IS
// lawfully visible but whose identity facts cannot establish the authority this
// presentation rests on refuses with ErrRunAuthorityUnavailable instead, because
// "I may not say" and "there is nothing" are different answers and a caller acts
// on them differently.
//
// Presenting an empty work item for a run whose lineage is missing would be the
// third, worst answer: a valid-looking row built out of absent evidence.
func (m *Module) ReadRunLaunch(
	ctx context.Context,
	tenant model.TenantID,
	workspace model.ID,
	runRef string,
) (RunLaunchSnapshot, error) {
	if m == nil || m.data == nil {
		return RunLaunchSnapshot{}, errors.New("sessions: run launch reader has no data handle")
	}
	if tenant.IsZero() || workspace.IsZero() || runRef == "" {
		return RunLaunchSnapshot{}, store.ErrNotFound
	}
	var out RunLaunchSnapshot
	err := m.data.View(ctx, tenant, func(raw store.Scope) error {
		sc, err := store.ConfineWorkspace(ctx, raw, workspace)
		if err != nil {
			return err
		}
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rows, _, err := repo.List(ctx, model.Query{
			Filters: []model.Filter{eq(colRunRef, runRef)},
			Limit:   1,
		})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return store.ErrNotFound
		}
		out, err = runLaunchFromRecord(rows[0], runRef)
		return err
	})
	if errors.Is(err, store.ErrWorkspaceConfinement) {
		// A request already confined to ANOTHER workspace asked about this one.
		// That is the foreign case, and it answers exactly like it.
		return RunLaunchSnapshot{}, store.ErrNotFound
	}
	if err != nil {
		return RunLaunchSnapshot{}, err
	}
	return out, nil
}

// ErrRunAuthorityUnavailable reports a run that IS lawfully visible and whose
// presented authority could not be established. It is deliberately not
// ErrNotFound: concealment answers for rows the caller may not see, and using it
// here would hide a real availability problem behind an existence answer.
var ErrRunAuthorityUnavailable = errors.New("sessions: run authority is unavailable")

// runLaunchFromRecord decodes the presented facts and refuses an incoherent row.
func runLaunchFromRecord(rec model.Record, runRef string) (RunLaunchSnapshot, error) {
	if rec.String(colRunRef) != runRef {
		return RunLaunchSnapshot{}, ErrRunAuthorityUnavailable
	}
	// The row reached us through the lineage filter, so it carries one. Reading it
	// back is the belt-and-braces half: a value the declared encoding cannot read
	// is a fault, and a faulty row is refused rather than presented.
	if _, err := model.ParseID(rec.String(colRunAuthzWorkspaceID)); err != nil {
		return RunLaunchSnapshot{}, ErrRunAuthorityUnavailable
	}
	out := RunLaunchSnapshot{
		RunRef:          runRef,
		RuntimeLaunchID: model.ID(rec.String(colRuntimeLaunchID)),
		Lifecycle:       rec.String(colState),
		WorkItemID:      model.ID(rec.String(colRunWorkItemID)),
	}
	if out.Lifecycle == "" {
		return RunLaunchSnapshot{}, ErrRunAuthorityUnavailable
	}
	return out, nil
}
