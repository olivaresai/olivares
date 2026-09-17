// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"fmt"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// custodial_relation.go (P2 / W1) — the FIXED relation the managed-stop surface
// operates over, resolved ONCE at Open from the closed registry.
//
// The relation is fixed by the engine and not by a registration, which is the
// whole point: a module cannot add a surface, rename a column, substitute
// another entity for the run or point the claim qualification at a table it
// controls. What a module CAN do is fail to declare the shape the engine
// requires — and then the capability is simply unavailable, with the reason
// named, while ordinary startup and every existing operation continue.
//
// That is the state of an ordinary build today: modules/sessions registers
// sessions.run WITHOUT the authorization-workspace lineage this relation
// requires, so ManagedStopReadiness reports relation_invalid and every Bind
// refuses before it locks, reads or writes anything.

// The fixed kinds and tables.
const (
	custodyRunKind    model.Kind = "sessions.run"
	custodyRunTable              = "sessions_run"
	custodyClaimKind  model.Kind = "sessions.claim"
	custodyClaimTable            = "sessions_claim"
)

// The fixed run columns: natural key, launch generation, authorization lineage
// and the admission stamp that names the claim row.
const (
	custodyRunRefColumn        = "run_ref"
	custodyRunLaunchColumn     = "runtime_launch_id"
	custodyRunLineageColumn    = "authz_workspace_id"
	custodyRunClaimHolderCol   = "claim_holder"
	custodyRunClaimFenceCol    = "claim_fence"
	custodyRunClaimSubjectCol  = "claim_sid"
	custodyRunUniqueIndexCols  = model.ColTenantID + "," + custodyRunRefColumn
	custodyClaimSubjectColumn  = "sid"
	custodyClaimHolderColumn   = "holder"
	custodyClaimFenceColumn    = "fence"
	custodyClaimStateColumn    = "claim_state"
	custodyClaimDeadlineColumn = "lease_expires_at"
	custodyClaimUniqueIndexCol = model.ColTenantID + "," + custodyClaimSubjectColumn
)

// custodialRelation is the resolved, validated pair. It holds descriptor VALUES
// (the registry owns their slices and never mutates them after boot), so the
// handle reads a stable shape without re-resolving the registry per request.
type custodialRelation struct {
	run   model.EntityDescriptor
	claim model.EntityDescriptor
	// lease is the claim descriptor's declared lease-fence spec, carried
	// separately because the qualification reads every one of its coordinates.
	lease model.AuthorizationLeaseFenceSpec
}

// resolveCustodialRelation validates the registered run and claim descriptors
// against the engine's fixed relation. It is called once, on the CLOSED
// registry, so its verdict cannot change while the store is serving.
//
// It never fails a boot: an invalid relation makes the capability unavailable,
// and the error it returns is the operator-facing reason for that.
func resolveCustodialRelation(reg *registry) (custodialRelation, error) {
	var out custodialRelation
	run, ok := reg.lookup(custodyRunKind)
	if !ok {
		return out, fmt.Errorf("%w: entity %q is not registered", store.ErrCustodyRelationInvalid, custodyRunKind)
	}
	claim, ok := reg.lookup(custodyClaimKind)
	if !ok {
		return out, fmt.Errorf("%w: entity %q is not registered", store.ErrCustodyRelationInvalid, custodyClaimKind)
	}
	return resolveCustodialRelationFromDescriptors(run, claim)
}

// resolveCustodialRelationFromDescriptors is the verdict itself, over the two
// resolved descriptors. It is separate from the registry lookup so the exact
// deviations it refuses can be exercised one at a time.
func resolveCustodialRelationFromDescriptors(
	run, claim model.EntityDescriptor,
) (custodialRelation, error) {
	var out custodialRelation
	if run.Kind != custodyRunKind || claim.Kind != custodyClaimKind {
		return out, fmt.Errorf("%w: the relation is fixed to %q and %q",
			store.ErrCustodyRelationInvalid, custodyRunKind, custodyClaimKind)
	}
	if run.Table != custodyRunTable {
		return out, fmt.Errorf("%w: entity %q is table %q, not %q",
			store.ErrCustodyRelationInvalid, custodyRunKind, run.Table, custodyRunTable)
	}
	if claim.Table != custodyClaimTable {
		return out, fmt.Errorf("%w: entity %q is table %q, not %q",
			store.ErrCustodyRelationInvalid, custodyClaimKind, claim.Table, custodyClaimTable)
	}
	if run.AppendOnly || run.SoftDelete {
		return out, fmt.Errorf("%w: entity %q must be an ordinary mutable relation",
			store.ErrCustodyRelationInvalid, custodyRunKind)
	}
	if claim.AppendOnly || claim.SoftDelete {
		return out, fmt.Errorf("%w: entity %q must be an ordinary mutable relation",
			store.ErrCustodyRelationInvalid, custodyClaimKind)
	}
	for _, want := range []struct {
		column string
		kind   model.SQLKind
	}{
		{custodyRunRefColumn, model.KindText},
		{custodyRunLaunchColumn, model.KindUUID},
		{custodyRunLineageColumn, model.KindUUID},
		{custodyRunClaimHolderCol, model.KindText},
		{custodyRunClaimFenceCol, model.KindInt},
		{custodyRunClaimSubjectCol, model.KindText},
	} {
		if err := requireCustodyColumn(run, want.column, want.kind); err != nil {
			return out, err
		}
	}
	for _, want := range []struct {
		column string
		kind   model.SQLKind
	}{
		{custodyClaimSubjectColumn, model.KindText},
		{custodyClaimHolderColumn, model.KindText},
		{custodyClaimFenceColumn, model.KindInt},
		{custodyClaimStateColumn, model.KindText},
		{custodyClaimDeadlineColumn, model.KindTimestamp},
	} {
		if err := requireCustodyColumn(claim, want.column, want.kind); err != nil {
			return out, err
		}
	}
	// The lineage declaration is what makes the confined target read possible at
	// all, and the UNSET semantics are load bearing: a run whose authorization
	// workspace is still NULL must be HIDDEN from a confined reader, never
	// resolved to the tenant's default workspace. The opposite spelling belongs
	// to sessions.identity and must not be copied here.
	lineage := run.WorkspaceLineage
	if !lineage.Declared() {
		return out, fmt.Errorf("%w: entity %q declares no workspace lineage",
			store.ErrCustodyRelationInvalid, custodyRunKind)
	}
	if lineage.Column != custodyRunLineageColumn {
		return out, fmt.Errorf("%w: entity %q declares lineage on %q, not %q",
			store.ErrCustodyRelationInvalid, custodyRunKind, lineage.Column, custodyRunLineageColumn)
	}
	if lineage.Encoding != model.WorkspaceLineageID {
		return out, fmt.Errorf("%w: entity %q declares lineage encoding %q, not %q",
			store.ErrCustodyRelationInvalid, custodyRunKind, lineage.Encoding, model.WorkspaceLineageID)
	}
	if lineage.Unset != model.WorkspaceUnsetHidden {
		return out, fmt.Errorf("%w: entity %q declares unset semantics %q, not %q",
			store.ErrCustodyRelationInvalid, custodyRunKind, lineage.Unset, model.WorkspaceUnsetHidden)
	}
	lease := claim.AuthorizationLeaseFence
	if !claim.AuthorizationFact || !lease.Declared() {
		return out, fmt.Errorf("%w: entity %q declares no authorization lease fence",
			store.ErrCustodyRelationInvalid, custodyClaimKind)
	}
	if lease.SubjectColumn != custodyClaimSubjectColumn ||
		lease.FenceColumn != custodyClaimFenceColumn ||
		lease.StateColumn != custodyClaimStateColumn ||
		lease.DeadlineColumn != custodyClaimDeadlineColumn {
		return out, fmt.Errorf(
			"%w: entity %q declares lease coordinates {%s,%s,%s,%s}, not {%s,%s,%s,%s}",
			store.ErrCustodyRelationInvalid, custodyClaimKind,
			lease.SubjectColumn, lease.FenceColumn, lease.StateColumn, lease.DeadlineColumn,
			custodyClaimSubjectColumn, custodyClaimFenceColumn,
			custodyClaimStateColumn, custodyClaimDeadlineColumn)
	}
	if lease.ActiveValue == "" {
		return out, fmt.Errorf("%w: entity %q declares no active lease value",
			store.ErrCustodyRelationInvalid, custodyClaimKind)
	}
	// The two unique indexes are what make "read by natural key" single-valued.
	// Without them the qualification could see two rows for one subject, and
	// choosing between them would be the substitution this relation exists to
	// prevent.
	if err := requireCustodyUniqueIndex(run, custodyRunUniqueIndexCols); err != nil {
		return out, err
	}
	if err := requireCustodyUniqueIndex(claim, custodyClaimUniqueIndexCol); err != nil {
		return out, err
	}
	out.run, out.claim, out.lease = run, claim, lease
	return out, nil
}

func requireCustodyColumn(d model.EntityDescriptor, column string, kind model.SQLKind) error {
	got, ok := d.KindOfColumn(column)
	if !ok {
		return fmt.Errorf("%w: entity %q declares no column %q",
			store.ErrCustodyRelationInvalid, d.Kind, column)
	}
	if got != kind {
		return fmt.Errorf("%w: entity %q column %q is %s, not %s",
			store.ErrCustodyRelationInvalid, d.Kind, column,
			custodyKindName(got), custodyKindName(kind))
	}
	return nil
}

// custodyKindName renders a column kind for an operator-facing refusal. The
// engine's SQLKind is an unexported-order integer, and a number in a message
// that names a schema deviation is not a diagnosis.
func custodyKindName(k model.SQLKind) string {
	switch k {
	case model.KindText:
		return "text"
	case model.KindInt:
		return "int"
	case model.KindTimestamp:
		return "timestamp"
	case model.KindUUID:
		return "uuid"
	default:
		return fmt.Sprintf("kind(%d)", int(k))
	}
}

func requireCustodyUniqueIndex(d model.EntityDescriptor, columns string) error {
	for _, ix := range d.Indexes {
		if !ix.Unique {
			continue
		}
		if joinIndexColumns(ix.Columns) == columns {
			return nil
		}
	}
	return fmt.Errorf("%w: entity %q declares no unique index on (%s)",
		store.ErrCustodyRelationInvalid, d.Kind, columns)
}

func joinIndexColumns(columns []string) string {
	out := ""
	for i, c := range columns {
		if i > 0 {
			out += ","
		}
		out += c
	}
	return out
}
