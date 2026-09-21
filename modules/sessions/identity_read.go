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

// RunLaunchTargetSnapshot is the presented, authority-free view of WHERE one run
// was launched: the provider profile it was launched under, the driver that
// profile names, and the execution environment it was resolved against. Each is
// a REFERENCE and authorizes nothing, and the three provider facts are empty
// TOGETHER on a run launched under no profile at all.
//
// The two provider HOME paths and the authorized authentication source are
// deliberately absent. The schema states the homes are exposed only on the
// authorized configuration read, and the auth source is a decision somebody made
// before the launch rather than a selector a launch is named by. An omitted field
// is cheaper to add later than a presented one is to withdraw from a public type.
type RunLaunchTargetSnapshot struct {
	RunRef                 string
	ProviderProfileID      string
	ProviderDriver         string
	ProviderEnvironmentRef string
}

// RunLaunchTargetReader is the sibling narrow port of RunLaunchReader, for a
// presentation consumer that must say what a run was launched AT without holding
// the run. The caller names the run by the Run arm's run_ref and names the stored
// workspace its own route authorized; the reader confines itself to that
// workspace. It is one method, and it embeds nothing.
type RunLaunchTargetReader interface {
	ReadRunLaunchTarget(context.Context, model.TenantID, model.ID, string) (RunLaunchTargetSnapshot, error)
}

var _ RunLaunchTargetReader = (*Module)(nil)

// ReadRunLaunchTarget presents one run's launch target through the CONFINED
// reader, under the concealment rules ReadRunLaunch states above and one refusal
// of its own, stated below.
//
// ⛔ IT NEVER REPAIRS AND IT NEVER INVENTS A LINEAGE. Absent, foreign and
// lineage-unset runs are one answer — store.ErrNotFound — and a request already
// confined to another workspace is answered exactly as a foreign one, because
// telling those apart is an existence probe. A run that IS lawfully visible but
// whose row cannot establish the authority this presentation rests on refuses
// with ErrRunAuthorityUnavailable instead: "I may not say" and "there is nothing"
// are different answers and a caller acts on them differently.
//
// THE ONE REFUSAL A STORED ROW CAN ACTUALLY REACH is a run that NAMES a provider
// profile and carries no driver. It is this port's rule and not one of
// ReadRunLaunch's, because these are this port's fields: the profile columns are
// written and cleared as ONE stamp, and a profile cannot be registered without a
// driver, so such a row cannot have come from a launch and cannot name a target.
// It answers ErrRunAuthorityUnavailable rather than a target assembled out of
// half a stamp. A run launched under NO profile is the opposite case and not a
// fault: it is presented, with the three provider fields empty. The two coherence
// checks shared with the sibling — a reference mismatch, an unreadable lineage —
// are belt-and-braces, because the equality filter and the confinement filter
// mean no stored row arrives at them.
//
// The confined lookup is written out rather than shared with ReadRunLaunch.
// Sharing would mean rewriting that method's body, and a read whose refusals a
// consumer already depends on is not worth disturbing to save nine lines.
func (m *Module) ReadRunLaunchTarget(
	ctx context.Context,
	tenant model.TenantID,
	workspace model.ID,
	runRef string,
) (RunLaunchTargetSnapshot, error) {
	if m == nil || m.data == nil {
		return RunLaunchTargetSnapshot{}, errors.New("sessions: run launch target reader has no data handle")
	}
	if tenant.IsZero() || workspace.IsZero() || runRef == "" {
		return RunLaunchTargetSnapshot{}, store.ErrNotFound
	}
	var out RunLaunchTargetSnapshot
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
		out, err = runLaunchTargetFromRecord(rows[0], runRef)
		return err
	})
	if errors.Is(err, store.ErrWorkspaceConfinement) {
		// A request already confined to ANOTHER workspace asked about this one.
		// That is the foreign case, and it answers exactly like it.
		return RunLaunchTargetSnapshot{}, store.ErrNotFound
	}
	if err != nil {
		return RunLaunchTargetSnapshot{}, err
	}
	return out, nil
}

// runLaunchTargetFromRecord decodes the presented facts and refuses an incoherent
// row.
func runLaunchTargetFromRecord(rec model.Record, runRef string) (RunLaunchTargetSnapshot, error) {
	if rec.String(colRunRef) != runRef {
		return RunLaunchTargetSnapshot{}, ErrRunAuthorityUnavailable
	}
	// The same belt-and-braces half its sibling reads: the row arrived through the
	// lineage filter, so it carries one, and a value the declared encoding cannot
	// read is a fault rather than a row to present.
	if _, err := model.ParseID(rec.String(colRunAuthzWorkspaceID)); err != nil {
		return RunLaunchTargetSnapshot{}, ErrRunAuthorityUnavailable
	}
	out := RunLaunchTargetSnapshot{
		RunRef:                 runRef,
		ProviderProfileID:      rec.String(colRunProfileID),
		ProviderDriver:         rec.String(colRunProfileDriver),
		ProviderEnvironmentRef: rec.String(colRunProfileEnvRef),
	}
	// A run launched under NO profile is a REAL state, not a fault: the snapshot
	// columns are written and cleared as one stamp, so all-empty is that run,
	// reported honestly. A profile NAMED with no driver is the third state — the
	// writer cannot produce it and a launch cannot be named by it — so it refuses
	// rather than presenting a target assembled out of half a stamp.
	if out.ProviderProfileID != "" && out.ProviderDriver == "" {
		return RunLaunchTargetSnapshot{}, ErrRunAuthorityUnavailable
	}
	return out, nil
}
