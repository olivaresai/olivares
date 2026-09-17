// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

var (
	errPolicySnapshotID      = errors.New("policy snapshot: invalid id")
	errPolicySnapshotTenant  = errors.New("policy snapshot: invalid tenant_id")
	errPolicySnapshotVersion = errors.New("policy snapshot: invalid version")
	errPolicySnapshotCreated = errors.New("policy snapshot: invalid created_at")
	errPolicySnapshotUpdated = errors.New("policy snapshot: invalid updated_at")
	errPolicySnapshotDeleted = errors.New("policy snapshot: invalid deleted_at")
	errPolicySnapshotBase    = errors.New("policy snapshot: invalid base metadata")
)

// policyRepo adds exact stored-Spec reads to the ordinary typed policy
// repository. Both views use the same tenant-pinned generic repository.
type policyRepo struct {
	*typedRepo[model.Policy]
}

var _ store.Repository[model.Policy] = (*policyRepo)(nil)
var _ store.RowLocker[model.Policy] = (*policyRepo)(nil)
var _ store.PolicySnapshotRepository = (*policyRepo)(nil)

func newPolicyRepo(g *genericRepo) store.Repository[model.Policy] {
	return &policyRepo{typedRepo: &typedRepo[model.Policy]{g: g, codec: policyCodec}}
}

func (r *policyRepo) GetPolicySnapshot(
	ctx context.Context,
	id model.ID,
) (store.PolicySnapshot, error) {
	rec, err := r.snapshotReader().Get(ctx, id)
	if err != nil {
		return store.PolicySnapshot{}, policySnapshotScanError(err)
	}
	return r.snapshot(rec)
}

func (r *policyRepo) ListPolicySnapshots(
	ctx context.Context,
	q model.Query,
) ([]store.PolicySnapshot, model.Page, error) {
	recs, page, err := r.snapshotReader().List(ctx, q)
	if err != nil {
		return nil, page, policySnapshotScanError(err)
	}
	out := make([]store.PolicySnapshot, 0, len(recs))
	for _, rec := range recs {
		snapshot, err := r.snapshot(rec)
		if err != nil {
			return nil, page, err
		}
		out = append(out, snapshot)
	}
	return out, page, nil
}

func (r *policyRepo) LockPolicySnapshot(
	ctx context.Context,
	id model.ID,
) (store.PolicySnapshot, error) {
	rec, err := r.snapshotReader().Lock(ctx, id)
	if err != nil {
		return store.PolicySnapshot{}, policySnapshotScanError(err)
	}
	return r.snapshot(rec)
}

func (r *policyRepo) snapshot(rec model.Record) (store.PolicySnapshot, error) {
	snapshot, err := policySnapshotFromRecord(rec)
	if err != nil {
		return store.PolicySnapshot{}, err
	}
	if snapshot.TenantID != r.g.tenant {
		return store.PolicySnapshot{}, errPolicySnapshotTenant
	}
	return snapshot, nil
}

func policySnapshotFromRecord(rec model.Record) (store.PolicySnapshot, error) {
	rawID := rec.String(model.ColID)
	id, err := model.ParseID(rawID)
	if err != nil || id.IsZero() || id.String() != rawID {
		return store.PolicySnapshot{}, errPolicySnapshotID
	}
	rawTenant := rec.String(model.ColTenantID)
	tenant, err := model.ParseTenantID(rawTenant)
	if err != nil || tenant.IsZero() || tenant.String() != rawTenant {
		return store.PolicySnapshot{}, errPolicySnapshotTenant
	}
	if rec.Int(model.ColVersion) < 1 {
		return store.PolicySnapshot{}, errPolicySnapshotVersion
	}
	base, err := baseFromRecord(rec)
	if err != nil {
		return store.PolicySnapshot{}, policySnapshotBaseError(err)
	}
	var spec *string
	if !rec.IsNull("spec") {
		stored := rec.String("spec")
		spec = &stored
	}
	return store.PolicySnapshot{
		BaseFields: base,
		Name:       rec.String("name"),
		Kind:       rec.String("kind"),
		Enabled:    rec.Bool("enabled"),
		Spec:       spec,
	}, nil
}

func policySnapshotBaseError(err error) error {
	switch {
	case strings.HasPrefix(err.Error(), model.ColCreatedAt+":"):
		return errPolicySnapshotCreated
	case strings.HasPrefix(err.Error(), model.ColUpdatedAt+":"):
		return errPolicySnapshotUpdated
	case strings.HasPrefix(err.Error(), model.ColDeletedAt+":"):
		return errPolicySnapshotDeleted
	default:
		return errPolicySnapshotBase
	}
}
