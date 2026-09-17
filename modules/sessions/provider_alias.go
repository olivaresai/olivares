// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// B1 — profile-scoped provider aliases.
//
// SG-00's sessions_alias binds (provider, external_id) to ONE canonical session
// per tenant. Two homes of the same provider can legitimately announce the SAME
// external id for two different processes, so a profiled run binds its provider
// id in a SCOPED table instead: (profile, provider, external_id) → sid, where the
// sid is an existing sessions_identity row (the one the launch already minted
// through claim_sid). The legacy table is left exactly as it was; unprofiled runs
// keep using it alone.
//
// The scoped alias is evidence of PROCESS OWNERSHIP, so it is only ever written
// by the bridge of the live process the runner actually created, inside one
// transaction that re-reads the run row and checks its launch generation, its
// claim and its profile. A frame from a fenced-out process, a foreign object or a
// cooperative observation that merely copies the id never reaches this write.

const (
	providerAliasKind  model.Kind = "sessions.provider_alias"
	providerAliasTable            = "sessions_provider_alias"
)

// sessions.provider_alias columns.
const (
	colPAProfileID  = "provider_profile_id"
	colPAProvider   = "provider"
	colPAExternalID = "external_id"
	colPASID        = "sid"
	colPARunRef     = "run_ref"
	colPALaunchID   = "launch_id"
	colPAClaimFence = "claim_fence"
	colPABoundAt    = "bound_at"
)

// ErrScopedAliasBound reports a (profile, provider, external_id) already bound to
// a DIFFERENT canonical session. It is recorded and reported, never forced: the
// process stays controllable by its own run_ref while its external link is
// reported as unconfirmed.
var ErrScopedAliasBound = errors.New("sessions: provider id already bound to another session within this profile")

func (m *Module) registerProviderAliasSchema(reg store.ExtensionRegistry) error {
	return reg.Register(model.EntityDescriptor{
		Kind:  providerAliasKind,
		Table: providerAliasTable,
		Fields: []model.FieldSpec{
			{Name: colPAProfileID, Kind: model.KindText},
			{Name: colPAProvider, Kind: model.KindText},
			{Name: colPAExternalID, Kind: model.KindText},
			{Name: colPASID, Kind: model.KindText, Indexed: true},
			{Name: colPARunRef, Kind: model.KindText, Indexed: true},
			{Name: colPALaunchID, Kind: model.KindUUID},
			{Name: colPAClaimFence, Kind: model.KindInt},
			{Name: colPABoundAt, Kind: model.KindTimestamp},
		},
		Indexes: []model.IndexSpec{{
			// The scoped guarantee. Every component is mandatory, so the same
			// external id under two profiles is two rows, and under one profile it is
			// one — decided by the engine, not by the writer.
			Name:    "sessions_provider_alias_scoped_uniq",
			Columns: []string{model.ColTenantID, colPAProfileID, colPAProvider, colPAExternalID},
			Unique:  true,
		}},
	})
}

// ManagedAliasInput is what the bridge presents: everything comes from the
// liveRun the runner registered and from the snapshot persisted before spawn —
// only ExternalID comes from the frame.
type managedAliasInput struct {
	profileID  string
	provider   string
	externalID string
	sid        string
	runRef     string
	launchID   model.ID
	claimFence int64
}

// bindManagedProviderAliasWithin writes the scoped alias INSIDE the caller's
// transaction after proving, on the committed run row, that the caller is still
// the current incarnation of a run launched under this profile and this claim.
// It also records the captured provider id on the run row in the same
// transaction, so "captured" is never true in memory while the database holds no
// proof. A different sid already bound under the key is a discrepancy: reported,
// not rewritten.
func (m *Module) bindManagedProviderAliasWithin(ctx context.Context, sc store.Scope, in managedAliasInput) error {
	if in.profileID == "" || in.provider == "" || in.externalID == "" || in.sid == "" || in.runRef == "" || in.launchID.IsZero() {
		return badRequest("managed alias requires profile, provider, external id, sid, run and launch")
	}
	runRepo, err := sc.Ext(runKind)
	if err != nil {
		return err
	}
	rec, err := findRunRec(ctx, runRepo, in.runRef)
	if err != nil {
		return err
	}
	// Generation, claim and profile are read from the ROW, not from memory, and the
	// row is part of this transaction: a successor that took over between the frame
	// and this write turns the check — or the CAS on the run update — into a refusal.
	if err := guardRuntimeLaunch(in.launchID)(rec); err != nil {
		return err
	}
	if rec.String(colRunClaimSID) != in.sid || rec.Int(colClaimFence) != in.claimFence {
		return conflictErr("the run's claim is not the one this process was launched under")
	}
	if rec.String(colRunProfileID) != in.profileID {
		return conflictErr("the run's persisted profile is not the one this process was launched under")
	}
	if rec.String(colRunProfileDriver) != in.provider {
		return conflictErr("the run's persisted driver is not the provider announcing this id")
	}
	if _, ok, ferr := findIdentity(ctx, sc, in.sid); ferr != nil {
		return ferr
	} else if !ok {
		return store.ErrNotFound
	}
	existing, found, err := findScopedAlias(ctx, sc, in.profileID, in.provider, in.externalID)
	if err != nil {
		return err
	}
	if found && existing.String(colPASID) != in.sid {
		return fmt.Errorf("%w: %s/%s:%s -> %s", ErrScopedAliasBound, in.profileID, in.provider, in.externalID, existing.String(colPASID))
	}
	if !found {
		repo, err := sc.Ext(providerAliasKind)
		if err != nil {
			return err
		}
		if _, err := repo.Create(ctx, model.Record{
			colPAProfileID:  in.profileID,
			colPAProvider:   in.provider,
			colPAExternalID: in.externalID,
			colPASID:        in.sid,
			colPARunRef:     in.runRef,
			colPALaunchID:   in.launchID.String(),
			colPAClaimFence: in.claimFence,
			colPABoundAt:    model.NewTimestamp(m.now()).String(),
		}); err != nil {
			return err
		}
	}
	if rec.String(colClaudeSessionID) == "" {
		rec[colClaudeSessionID] = in.externalID
	} else if rec.String(colClaudeSessionID) != in.externalID {
		return conflictErr("the run already captured a different provider session id")
	}
	_, err = runRepo.Update(ctx, rec)
	return err
}

// findScopedAlias reads one scoped alias row by its natural key.
func findScopedAlias(ctx context.Context, sc store.Scope, profileID, provider, externalID string) (model.Record, bool, error) {
	repo, err := sc.Ext(providerAliasKind)
	if err != nil {
		return nil, false, err
	}
	recs, _, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{eq(colPAProfileID, profileID), eq(colPAProvider, provider), eq(colPAExternalID, externalID)},
		Limit:   1,
	})
	if err != nil || len(recs) == 0 {
		return nil, false, err
	}
	return recs[0], true, nil
}

// ScopedAlias is the read view of one profile-scoped alias.
type ScopedAlias struct {
	ProfileRef string
	Provider   string
	ExternalID string
	SID        string
	RunRef     string
	LaunchID   model.ID
	ClaimFence int64
	BoundAt    string
}

// LookupScopedAlias resolves (profile, provider, external id) to the canonical
// session it is PROVEN to belong to, for an authorized consumer that wants to
// know — informational. It never mints, never binds and never adopts: an
// observation that copies a managed run's id resolves to nothing here unless the
// bridge of that run wrote the row.
func (m *Module) LookupScopedAlias(ctx context.Context, tenant model.TenantID, profileRef, provider, externalID string) (ScopedAlias, bool, error) {
	if m.data == nil {
		return ScopedAlias{}, false, errNoData
	}
	var out ScopedAlias
	found := false
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		rec, ok, err := findScopedAlias(ctx, sc, profileRef, provider, externalID)
		if err != nil || !ok {
			return err
		}
		found = true
		out = ScopedAlias{
			ProfileRef: rec.String(colPAProfileID), Provider: rec.String(colPAProvider),
			ExternalID: rec.String(colPAExternalID), SID: rec.String(colPASID),
			RunRef: rec.String(colPARunRef), LaunchID: model.ID(rec.String(colPALaunchID)),
			ClaimFence: rec.Int(colPAClaimFence), BoundAt: rec.String(colPABoundAt),
		}
		return nil
	})
	return out, found, err
}

// scopedAliasStatus maps a scoped-alias failure to what the bridge logs.
func scopedAliasStatus(err error) int {
	var re *runErr
	if errors.As(err, &re) {
		return re.status
	}
	return http.StatusInternalServerError
}
