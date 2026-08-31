// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// ReadDirectoryEpoch reads the tenant's fencing fact through the exact
// transaction backing this Scope. It deliberately does not use Store.View: a
// resolver brackets its roster/tombstone reads with this method, and a nested
// transaction would make those three observations unrelated.
func (sc *tenantScope) ReadDirectoryEpoch(ctx context.Context) (model.DirectoryEpoch, error) {
	rec, found, err := sc.readOneDirectoryRecord(
		ctx,
		directoryEpochDescriptor,
		"tenant_id = ?",
		sc.tenant.String(),
	)
	if err != nil {
		return model.DirectoryEpoch{}, directoryUnavailable("read tenant epoch", err)
	}
	if !found {
		return model.DirectoryEpoch{}, directoryUnavailable("tenant epoch is absent", nil)
	}
	base, err := baseFromRecord(rec)
	if err != nil {
		return model.DirectoryEpoch{}, directoryUnavailable("decode tenant epoch base", err)
	}
	epoch, err := directoryEpochCodec.Decode(base, rec)
	if err != nil {
		return model.DirectoryEpoch{}, directoryUnavailable("validate tenant epoch", err)
	}
	if epoch.TenantID != sc.tenant {
		return model.DirectoryEpoch{}, directoryUnavailable("tenant epoch crossed scope", nil)
	}
	return epoch, nil
}

// ReadDirectoryTombstone resolves immutable retirement evidence through the
// surrounding transaction. A valid epoch is established first even for a
// negative lookup, so found=false never means "the evidence tables could not be
// read". User evidence lives in the system partition and is reached by a
// transaction-local rebind which is restored before this method returns.
func (sc *tenantScope) ReadDirectoryTombstone(
	ctx context.Context,
	ref store.DirectoryPrincipalRef,
) (store.DirectoryTombstoneWitness, bool, error) {
	if err := ref.Validate(); err != nil {
		return store.DirectoryTombstoneWitness{}, false, err
	}
	epoch, err := sc.ReadDirectoryEpoch(ctx)
	if err != nil {
		return store.DirectoryTombstoneWitness{}, false, err
	}

	if ref.PrincipalKind == model.DirectoryPrincipalUser {
		return sc.readUserDirectoryTombstone(ctx, ref, epoch.Version)
	}
	return sc.readTenantDirectoryTombstone(ctx, ref, epoch.Version)
}

func (sc *tenantScope) readTenantDirectoryTombstone(
	ctx context.Context,
	ref store.DirectoryPrincipalRef,
	currentEpoch int64,
) (store.DirectoryTombstoneWitness, bool, error) {
	rec, found, err := sc.readOneDirectoryRecord(
		ctx,
		directoryTombstoneDescriptor,
		"tenant_id = ? AND principal_kind = ? AND principal_ref = ? AND workspace_ref = ?",
		sc.tenant.String(),
		string(ref.PrincipalKind),
		ref.PrincipalRef.String(),
		encodeDirectoryWorkspaceRef(ref.WorkspaceRef),
	)
	if err != nil {
		return store.DirectoryTombstoneWitness{}, false,
			directoryUnavailable("read tenant tombstone", err)
	}
	if !found {
		return store.DirectoryTombstoneWitness{}, false, nil
	}

	base, err := baseFromRecord(rec)
	if err != nil {
		return store.DirectoryTombstoneWitness{}, false,
			directoryUnavailable("decode tenant tombstone base", err)
	}
	tombstone, err := directoryTombstoneCodec.Decode(base, rec)
	if err != nil {
		return store.DirectoryTombstoneWitness{}, false,
			directoryUnavailable("validate tenant tombstone", err)
	}
	if tombstone.TenantID != sc.tenant ||
		tombstone.PrincipalKind != ref.PrincipalKind ||
		tombstone.PrincipalRef != ref.PrincipalRef ||
		tombstone.WorkspaceRef != ref.WorkspaceRef ||
		tombstone.ResultingEpoch > currentEpoch {
		return store.DirectoryTombstoneWitness{}, false,
			directoryUnavailable("tenant tombstone is not fenced by the current epoch", nil)
	}
	switch tombstone.PrincipalKind {
	case model.DirectoryPrincipalIdentity:
		_, sourceFound, err := readDirectoryRetirementRecord(
			ctx, sc.tx, sc.s.dia, identityDescriptor, sc.tenant,
			tombstone.SourceID, false,
		)
		if err != nil {
			return store.DirectoryTombstoneWitness{}, false,
				directoryUnavailable("read retired Identity source", err)
		}
		if sourceFound {
			return store.DirectoryTombstoneWitness{}, false,
				directoryUnavailable("Identity tombstone coexists with its source", nil)
		}
		bindings, err := countPhysicalAgentsForIdentity(
			ctx, sc.tx, sc.s.dia, sc.tenant, tombstone.PrincipalRef,
		)
		if err != nil {
			return store.DirectoryTombstoneWitness{}, false, err
		}
		if bindings != 0 {
			return store.DirectoryTombstoneWitness{}, false,
				directoryUnavailable("Identity tombstone coexists with an Agent binding", nil)
		}
	case model.DirectoryPrincipalAgent:
		_, sourceFound, err := readDirectoryRetirementRecord(
			ctx, sc.tx, sc.s.dia, agentDescriptor, sc.tenant,
			tombstone.SourceID, false,
		)
		if err != nil {
			return store.DirectoryTombstoneWitness{}, false,
				directoryUnavailable("read retired Agent source", err)
		}
		if sourceFound {
			return store.DirectoryTombstoneWitness{}, false,
				directoryUnavailable("Agent tombstone coexists with its source", nil)
		}
		bindings, err := countPhysicalAgentPrincipalBindings(
			ctx, sc.tx, sc.s.dia, sc.tenant, tombstone.PrincipalRef,
			tombstone.WorkspaceRef, "",
		)
		if err != nil {
			return store.DirectoryTombstoneWitness{}, false, err
		}
		if bindings != 0 {
			return store.DirectoryTombstoneWitness{}, false,
				directoryUnavailable("Agent tombstone coexists with a physical binding", nil)
		}
	}
	if err := sc.validateDirectoryAuditAnchor(ctx, sc.tenant, tombstone.AuditAnchor); err != nil {
		return store.DirectoryTombstoneWitness{}, false, err
	}

	return store.DirectoryTombstoneWitness{
		TombstoneKind:    model.DirectoryTombstoneKind,
		TombstoneID:      tombstone.ID,
		TombstoneVersion: tombstone.Version,
		Principal: store.DirectoryPrincipalRef{
			PrincipalKind: tombstone.PrincipalKind,
			PrincipalRef:  tombstone.PrincipalRef,
			WorkspaceRef:  tombstone.WorkspaceRef,
		},
		RetirementEpoch: tombstone.ResultingEpoch,
	}, true, nil
}

func (sc *tenantScope) readUserDirectoryTombstone(
	ctx context.Context,
	ref store.DirectoryPrincipalRef,
	currentEpoch int64,
) (witness store.DirectoryTombstoneWitness, found bool, err error) {
	err = sc.withDirectoryTenantBinding(ctx, model.SystemTenantID, func() error {
		rec, rowFound, readErr := sc.readOneDirectoryRecord(
			ctx,
			userTombstoneDescriptor,
			"tenant_id = ? AND principal_kind = ? AND principal_ref = ?",
			model.SystemTenantID.String(),
			string(model.DirectoryPrincipalUser),
			ref.PrincipalRef.String(),
		)
		if readErr != nil {
			return directoryUnavailable("read user tombstone", readErr)
		}
		if !rowFound {
			return nil
		}

		base, decodeErr := baseFromRecord(rec)
		if decodeErr != nil {
			return directoryUnavailable("decode user tombstone base", decodeErr)
		}
		tombstone, decodeErr := userTombstoneCodec.Decode(base, rec)
		if decodeErr != nil {
			return directoryUnavailable("validate user tombstone", decodeErr)
		}
		if tombstone.PrincipalKind != ref.PrincipalKind ||
			tombstone.PrincipalRef != ref.PrincipalRef {
			return directoryUnavailable("user tombstone does not match the lookup key", nil)
		}
		_, sourceFound, sourceErr := readDirectoryRetirementRecord(
			ctx, sc.tx, sc.s.dia, userDescriptor, model.SystemTenantID,
			tombstone.SourceID, false,
		)
		if sourceErr != nil {
			return directoryUnavailable("read retired User source", sourceErr)
		}
		if sourceFound {
			return directoryUnavailable("User tombstone coexists with its source", nil)
		}
		retirementEpoch, carried := tombstone.ResultingEpochs.EpochFor(sc.tenant)
		if !carried || retirementEpoch > currentEpoch {
			return directoryUnavailable(
				"user tombstone does not carry a valid epoch for the bound tenant",
				nil,
			)
		}
		if anchorErr := sc.validateDirectoryAuditAnchor(
			ctx, model.SystemTenantID, tombstone.AuditAnchor,
		); anchorErr != nil {
			return anchorErr
		}

		witness = store.DirectoryTombstoneWitness{
			TombstoneKind:    model.UserTombstoneKind,
			TombstoneID:      tombstone.ID,
			TombstoneVersion: tombstone.Version,
			Principal: store.DirectoryPrincipalRef{
				PrincipalKind: tombstone.PrincipalKind,
				PrincipalRef:  tombstone.PrincipalRef,
			},
			RetirementEpoch: retirementEpoch,
		}
		found = true
		return nil
	})
	if err != nil {
		return store.DirectoryTombstoneWitness{}, false, err
	}
	return witness, found, nil
}

// withDirectoryTenantBinding temporarily changes only the tenant pin of sc.tx.
// Restoring the original tenant is part of the read contract: a successful
// global lookup followed by a tenant-local read must still observe the caller's
// partition. A failed restore makes the evidence unavailable even when the
// lookup itself succeeded.
func (sc *tenantScope) withDirectoryTenantBinding(
	ctx context.Context,
	tenant model.TenantID,
	fn func() error,
) error {
	if err := sc.s.dia.BindTenant(ctx, sc.tx, tenant); err != nil {
		return directoryUnavailable("bind directory evidence partition", err)
	}
	readErr := fn()
	restoreErr := sc.s.dia.BindTenant(ctx, sc.tx, sc.tenant)
	if restoreErr != nil {
		return errors.Join(
			readErr,
			directoryUnavailable("restore tenant after directory evidence read", restoreErr),
		)
	}
	return readErr
}

func (sc *tenantScope) validateDirectoryAuditAnchor(
	ctx context.Context,
	tenant model.TenantID,
	anchor model.RetirementAuditAnchor,
) error {
	q := sc.s.dia.Rebind(
		"SELECT id, seq, hash, action, target_kind, target_id FROM " +
			directoryWriterRelation(sc.s.dia, auditTable) +
			" WHERE tenant_id = ? AND id = ? LIMIT 2",
	)
	rows, err := sc.tx.QueryContext(ctx, q, tenant.String(), anchor.EventID.String())
	if err != nil {
		return directoryUnavailable("read retirement audit anchor", err)
	}
	defer rows.Close() //nolint:errcheck // rows.Err below is authoritative

	type auditProjection struct {
		id         string
		seq        int64
		hash       []byte
		action     string
		targetKind string
		targetID   string
	}
	var events []auditProjection
	for rows.Next() {
		var event auditProjection
		if err := rows.Scan(
			&event.id,
			&event.seq,
			&event.hash,
			&event.action,
			&event.targetKind,
			&event.targetID,
		); err != nil {
			return directoryUnavailable("decode retirement audit anchor", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return directoryUnavailable("read retirement audit anchor", err)
	}
	if len(events) != 1 {
		return directoryUnavailable("retirement audit anchor is absent or ambiguous", nil)
	}
	event := events[0]
	if event.id != anchor.EventID.String() || event.seq != anchor.Seq ||
		!bytes.Equal(event.hash, anchor.Hash) || event.action != anchor.Action ||
		event.targetKind != string(anchor.TargetKind) ||
		event.targetID != anchor.TargetID.String() {
		return directoryUnavailable("retirement audit anchor does not match the ledger", nil)
	}
	return nil
}

// readOneDirectoryRecord performs a cardinality-sensitive descriptor read. A
// missing row is represented separately from SQL/shape errors; two rows are an
// integrity failure even if a damaged database has also lost its unique index.
func (sc *tenantScope) readOneDirectoryRecord(
	ctx context.Context,
	desc model.EntityDescriptor,
	predicate string,
	args ...any,
) (model.Record, bool, error) {
	cols := desc.AllColumns()
	q := fmt.Sprintf(
		"SELECT %s FROM %s WHERE %s LIMIT 2",
		strings.Join(cols, ", "),
		directoryWriterRelation(sc.s.dia, desc.Table),
		predicate,
	)
	rows, err := sc.tx.QueryContext(ctx, sc.s.dia.Rebind(q), args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close() //nolint:errcheck // rows.Err below is authoritative

	var records []model.Record
	for rows.Next() {
		state, err := newScanState(desc, cols)
		if err != nil {
			return nil, false, err
		}
		if err := rows.Scan(state.dests...); err != nil {
			return nil, false, err
		}
		records = append(records, state.record())
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	switch len(records) {
	case 0:
		return nil, false, nil
	case 1:
		return records[0], true, nil
	default:
		return nil, false, errors.New("directory evidence lookup returned duplicate rows")
	}
}

func directoryUnavailable(reason string, cause error) error {
	if cause == nil {
		return fmt.Errorf("%w: %s", store.ErrDirectoryUnavailable, reason)
	}
	return fmt.Errorf("%w: %s: %w", store.ErrDirectoryUnavailable, reason, cause)
}
