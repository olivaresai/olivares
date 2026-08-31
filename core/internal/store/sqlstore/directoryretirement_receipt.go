// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/olivaresai/olivares/core/internal/store/canon"
	"github.com/olivaresai/olivares/core/model"
	obstrace "github.com/olivaresai/olivares/core/observability/trace"
	"github.com/olivaresai/olivares/core/store"
)

// The audit metadata is also the durable retry discriminator after the source
// row has been physically removed. Keep its construction in one place so the
// first execution and a post-commit/ACK-lost replay compare the same canonical
// bytes, including the exact source generation.
func userRetirementAuditMeta(req UserRetirementRequest, tenantCount int) map[string]any {
	return map[string]any{
		"cause":          string(model.DirectoryCauseUserErased),
		"source_kind":    string(userDescriptor.Kind),
		"source_id":      req.UserID.String(),
		"source_version": req.ExpectedVersion,
		"tenant_count":   tenantCount,
	}
}

func directoryRetirementAuditMeta(
	req DirectoryPrincipalRetirementRequest,
	result DirectoryPrincipalRetirementResult,
	definitive bool,
) map[string]any {
	meta := map[string]any{
		"principal_kind":  string(result.Principal.PrincipalKind),
		"principal_ref":   result.Principal.PrincipalRef.String(),
		"workspace_ref":   result.Principal.WorkspaceRef.String(),
		"source_kind":     string(result.SourceKind),
		"source_id":       result.SourceID.String(),
		"source_version":  req.ExpectedVersion,
		"resulting_epoch": result.ResultingEpoch,
		"retirement_code": string(result.Code),
	}
	if definitive {
		meta["cause"] = string(directoryRetirementCause(req.PrincipalKind))
	}
	return meta
}

func (sys *systemScope) replayUserRetirement(
	ctx context.Context,
	req UserRetirementRequest,
	currentTenants []model.TenantID,
) (model.UserTombstone, bool, error) {
	sc := &tenantScope{s: sys.s, tx: sys.tx, tenant: model.SystemTenantID, readOnly: true}
	rec, found, err := sc.readOneDirectoryRecord(
		ctx,
		userTombstoneDescriptor,
		"tenant_id = ? AND source_kind = ? AND source_id = ?",
		model.SystemTenantID.String(), string(userDescriptor.Kind), req.UserID.String(),
	)
	if err != nil {
		return model.UserTombstone{}, false,
			directoryUnavailable("read prior User retirement receipt", err)
	}
	if !found {
		return model.UserTombstone{}, false, nil
	}
	base, err := baseFromRecord(rec)
	if err != nil {
		return model.UserTombstone{}, false,
			directoryUnavailable("decode prior User retirement receipt", err)
	}
	tombstone, err := userTombstoneCodec.Decode(base, rec)
	if err != nil {
		return model.UserTombstone{}, false,
			directoryUnavailable("validate prior User retirement receipt", err)
	}
	if tombstone.SourceID != req.UserID || tombstone.PrincipalRef != req.UserID ||
		tombstone.Actor != req.Actor {
		return model.UserTombstone{}, false,
			directoryUnavailable("prior User retirement receipt does not match request", nil)
	}
	draft := model.AuditDraft{
		Actor: req.Actor, ActorKind: req.ActorKind,
		Action:     model.AuditActionUserRetire,
		TargetKind: model.UserTombstoneKind, TargetID: tombstone.ID,
		Meta: userRetirementAuditMeta(req, len(tombstone.ResultingEpochs)),
	}
	if err := sys.requireExistingRetirementAudit(
		ctx, model.SystemTenantID, tombstone.AuditAnchor, draft,
	); err != nil {
		return model.UserTombstone{}, false, err
	}
	// A tenant created after the original commit was never part of that global
	// estate snapshot and therefore need not appear in the old immutable map.
	// Every still-current tenant that was carried is nevertheless exercised
	// through the real same-transaction DirectorySnapshotReader.
	if err := sys.requireUserRetirementWitnesses(
		ctx, tombstone, currentTenants, false,
	); err != nil {
		return model.UserTombstone{}, false, err
	}
	if err := sys.bindFor(ctx, model.SystemTenantID); err != nil {
		return model.UserTombstone{}, false, err
	}
	if err := sys.requireNoUserRetirementAuthority(ctx, req.UserID); err != nil {
		return model.UserTombstone{}, false, err
	}
	return tombstone, true, nil
}

func (sys *systemScope) replayDirectoryRetirement(
	ctx context.Context,
	req DirectoryPrincipalRetirementRequest,
) (DirectoryPrincipalRetirementResult, bool, error) {
	sc := &tenantScope{s: sys.s, tx: sys.tx, tenant: req.TenantID, readOnly: true}
	rec, found, err := sc.readOneDirectoryRecord(
		ctx,
		directoryTombstoneDescriptor,
		"tenant_id = ? AND source_kind = ? AND source_id = ?",
		req.TenantID.String(), string(descriptorForRetirementKind(req.PrincipalKind).Kind),
		req.SourceID.String(),
	)
	if err != nil {
		return DirectoryPrincipalRetirementResult{}, false,
			directoryUnavailable("read prior directory retirement receipt", err)
	}
	if found {
		base, err := baseFromRecord(rec)
		if err != nil {
			return DirectoryPrincipalRetirementResult{}, false,
				directoryUnavailable("decode prior directory retirement receipt", err)
		}
		tombstone, err := directoryTombstoneCodec.Decode(base, rec)
		if err != nil {
			return DirectoryPrincipalRetirementResult{}, false,
				directoryUnavailable("validate prior directory retirement receipt", err)
		}
		if tombstone.PrincipalKind != req.PrincipalKind ||
			tombstone.SourceID != req.SourceID || tombstone.Actor != req.Actor {
			return DirectoryPrincipalRetirementResult{}, false,
				directoryUnavailable("prior directory retirement receipt does not match request", nil)
		}
		result := DirectoryPrincipalRetirementResult{
			Code: DirectoryRetirementDefinitive, Definitive: true,
			Principal: store.DirectoryPrincipalRef{
				PrincipalKind: tombstone.PrincipalKind,
				PrincipalRef:  tombstone.PrincipalRef,
				WorkspaceRef:  tombstone.WorkspaceRef,
			},
			SourceKind:     tombstone.SourceKind,
			SourceID:       tombstone.SourceID,
			ResultingEpoch: tombstone.ResultingEpoch,
			Tombstone:      &tombstone,
			AuditEventID:   tombstone.AuditAnchor.EventID,
			AuditSeq:       tombstone.AuditAnchor.Seq,
			AuditHash:      append([]byte(nil), tombstone.AuditAnchor.Hash...),
		}
		draft := model.AuditDraft{
			Actor: req.Actor, ActorKind: req.ActorKind,
			Action:     model.AuditActionDirectoryPrincipalRetire,
			TargetKind: model.DirectoryTombstoneKind, TargetID: tombstone.ID,
			Meta: directoryRetirementAuditMeta(req, result, true),
		}
		if err := sys.requireExistingRetirementAudit(
			ctx, req.TenantID, tombstone.AuditAnchor, draft,
		); err != nil {
			return DirectoryPrincipalRetirementResult{}, false, err
		}
		if err := sys.requireDirectoryRetirementWitness(ctx, tombstone, false); err != nil {
			return DirectoryPrincipalRetirementResult{}, false, err
		}
		return result, true, nil
	}
	if req.PrincipalKind != model.DirectoryPrincipalAgent {
		return DirectoryPrincipalRetirementResult{}, false, nil
	}
	return sys.replayAgentBindingRetirement(ctx, req)
}

type agentBindingRetirementMeta struct {
	PrincipalKind  string `json:"principal_kind"`
	PrincipalRef   string `json:"principal_ref"`
	WorkspaceRef   string `json:"workspace_ref"`
	SourceKind     string `json:"source_kind"`
	SourceID       string `json:"source_id"`
	SourceVersion  int64  `json:"source_version"`
	ResultingEpoch int64  `json:"resulting_epoch"`
	RetirementCode string `json:"retirement_code"`
}

func (sys *systemScope) replayAgentBindingRetirement(
	ctx context.Context,
	req DirectoryPrincipalRetirementRequest,
) (DirectoryPrincipalRetirementResult, bool, error) {
	event, metaCanonical, found, err := sys.readRetirementAuditByTarget(
		ctx, req.TenantID, model.AuditActionAgentBindingRetire,
		agentDescriptor.Kind, req.SourceID,
	)
	if err != nil || !found {
		return DirectoryPrincipalRetirementResult{}, found, err
	}
	var carried agentBindingRetirementMeta
	if err := json.Unmarshal([]byte(metaCanonical), &carried); err != nil {
		return DirectoryPrincipalRetirementResult{}, false,
			directoryUnavailable("decode Agent binding retirement receipt metadata", err)
	}
	principalID, err := model.ParseID(carried.PrincipalRef)
	if err != nil {
		return DirectoryPrincipalRetirementResult{}, false,
			directoryUnavailable("decode Agent binding retirement principal", err)
	}
	workspaceID, err := model.ParseID(carried.WorkspaceRef)
	if err != nil {
		return DirectoryPrincipalRetirementResult{}, false,
			directoryUnavailable("decode Agent binding retirement workspace", err)
	}
	result := DirectoryPrincipalRetirementResult{
		Code: DirectoryRetirementCode(carried.RetirementCode), Definitive: false,
		Principal: store.DirectoryPrincipalRef{
			PrincipalKind: model.DirectoryPrincipalKind(carried.PrincipalKind),
			PrincipalRef:  principalID,
			WorkspaceRef:  workspaceID,
		},
		SourceKind:     model.Kind(carried.SourceKind),
		SourceID:       model.ID(carried.SourceID),
		ResultingEpoch: carried.ResultingEpoch,
		AuditEventID:   event.ID,
		AuditSeq:       event.Seq,
		AuditHash:      append([]byte(nil), event.Hash...),
	}
	if result.Code != DirectoryRetirementAgentBindingRemains ||
		result.Principal.PrincipalKind != model.DirectoryPrincipalAgent ||
		result.SourceKind != agentDescriptor.Kind || result.SourceID != req.SourceID ||
		carried.SourceVersion != req.ExpectedVersion || result.ResultingEpoch < 1 {
		return DirectoryPrincipalRetirementResult{}, false,
			directoryUnavailable("Agent binding retirement receipt does not match request", nil)
	}
	if err := result.Principal.Validate(); err != nil {
		return DirectoryPrincipalRetirementResult{}, false,
			directoryUnavailable("Agent binding retirement receipt principal is invalid", err)
	}
	draft := model.AuditDraft{
		Actor: req.Actor, ActorKind: req.ActorKind,
		Action:     model.AuditActionAgentBindingRetire,
		TargetKind: agentDescriptor.Kind, TargetID: req.SourceID,
		Meta: directoryRetirementAuditMeta(req, result, false),
	}
	if err := sys.validateExistingRetirementAudit(
		ctx, req.TenantID, event, metaCanonical, nil, draft,
	); err != nil {
		return DirectoryPrincipalRetirementResult{}, false, err
	}
	reader := &tenantScope{s: sys.s, tx: sys.tx, tenant: req.TenantID, readOnly: true}
	before, err := reader.ReadDirectoryEpoch(ctx)
	if err != nil {
		return DirectoryPrincipalRetirementResult{}, false, err
	}
	after, err := reader.ReadDirectoryEpoch(ctx)
	if err != nil {
		return DirectoryPrincipalRetirementResult{}, false, err
	}
	if before.Version != after.Version || before.Version < result.ResultingEpoch {
		return DirectoryPrincipalRetirementResult{}, false,
			directoryUnavailable("Agent binding retirement receipt is not fenced by the current epoch", nil)
	}
	return result, true, nil
}

func (sys *systemScope) requireUserRetirementWitnesses(
	ctx context.Context,
	tombstone model.UserTombstone,
	tenants []model.TenantID,
	requireEveryTenant bool,
) error {
	ref := store.DirectoryPrincipalRef{
		PrincipalKind: model.DirectoryPrincipalUser,
		PrincipalRef:  tombstone.PrincipalRef,
	}
	for _, tenant := range tenants {
		epoch, carried := tombstone.ResultingEpochs.EpochFor(tenant)
		if !carried {
			if requireEveryTenant {
				return directoryUnavailable(
					fmt.Sprintf("User retirement omitted tenant %s", tenant), nil,
				)
			}
			continue
		}
		if err := sys.requireRetirementWitness(
			ctx, tenant, ref, model.UserTombstoneKind,
			tombstone.ID, tombstone.Version, epoch, requireEveryTenant,
		); err != nil {
			return err
		}
	}
	return nil
}

func (sys *systemScope) requireDirectoryRetirementWitness(
	ctx context.Context,
	tombstone model.DirectoryTombstone,
	requireCurrentEpochExact bool,
) error {
	return sys.requireRetirementWitness(
		ctx,
		tombstone.TenantID,
		store.DirectoryPrincipalRef{
			PrincipalKind: tombstone.PrincipalKind,
			PrincipalRef:  tombstone.PrincipalRef,
			WorkspaceRef:  tombstone.WorkspaceRef,
		},
		model.DirectoryTombstoneKind,
		tombstone.ID,
		tombstone.Version,
		tombstone.ResultingEpoch,
		requireCurrentEpochExact,
	)
}

func (sys *systemScope) requireRetirementWitness(
	ctx context.Context,
	tenant model.TenantID,
	ref store.DirectoryPrincipalRef,
	tombstoneKind model.Kind,
	tombstoneID model.ID,
	tombstoneVersion int64,
	retirementEpoch int64,
	requireCurrentEpochExact bool,
) error {
	if err := sys.bindFor(ctx, tenant); err != nil {
		return err
	}
	reader := &tenantScope{s: sys.s, tx: sys.tx, tenant: tenant, readOnly: true}
	before, err := reader.ReadDirectoryEpoch(ctx)
	if err != nil {
		return err
	}
	witness, found, err := reader.ReadDirectoryTombstone(ctx, ref)
	if err != nil {
		return err
	}
	if !found {
		return directoryUnavailable("retirement tombstone has no same-transaction witness", nil)
	}
	after, err := reader.ReadDirectoryEpoch(ctx)
	if err != nil {
		return err
	}
	if before.Version != after.Version || before.Version < retirementEpoch ||
		(requireCurrentEpochExact && before.Version != retirementEpoch) ||
		witness.TombstoneKind != tombstoneKind || witness.TombstoneID != tombstoneID ||
		witness.TombstoneVersion != tombstoneVersion || witness.Principal != ref ||
		witness.RetirementEpoch != retirementEpoch {
		return directoryUnavailable("same-transaction retirement witness diverged", nil)
	}
	return nil
}

func (sys *systemScope) requireExistingRetirementAudit(
	ctx context.Context,
	tenant model.TenantID,
	anchor model.RetirementAuditAnchor,
	draft model.AuditDraft,
) error {
	event, metaCanonical, found, err := sys.readRetirementAuditByID(
		ctx, tenant, anchor.EventID,
	)
	if err != nil {
		return err
	}
	if !found {
		return directoryUnavailable("retirement audit receipt is absent", nil)
	}
	return sys.validateExistingRetirementAudit(
		ctx, tenant, event, metaCanonical, &anchor, draft,
	)
}

func (sys *systemScope) validateExistingRetirementAudit(
	ctx context.Context,
	tenant model.TenantID,
	event model.AuditEvent,
	metaCanonical string,
	anchor *model.RetirementAuditAnchor,
	draft model.AuditDraft,
) error {
	if event.Seq < 1 || event.TenantID != tenant || event.Actor != draft.Actor ||
		event.ActorKind != draft.ActorKind || event.Action != draft.Action ||
		event.TargetKind != draft.TargetKind || event.TargetID != draft.TargetID ||
		len(event.Hash) != 32 {
		return directoryUnavailable("retirement audit receipt does not match request", nil)
	}
	if err := validateStableRetirementAuditMeta(metaCanonical, draft.Meta); err != nil {
		return err
	}
	if parsed, err := model.ParseID(event.ID.String()); err != nil || parsed != event.ID {
		return directoryUnavailable("retirement audit receipt id is not canonical", err)
	}
	if anchor != nil && (anchor.EventID != event.ID || anchor.Seq != event.Seq ||
		!bytes.Equal(anchor.Hash, event.Hash) || anchor.Action != event.Action ||
		anchor.TargetKind != event.TargetKind || anchor.TargetID != event.TargetID) {
		return directoryUnavailable("retirement audit anchor diverged from its receipt", nil)
	}
	report, err := sys.auditLogFor(tenant).Verify(ctx, event.Seq)
	if err != nil {
		return directoryUnavailable("verify retirement audit receipt chain", err)
	}
	if !report.OK || report.Checked < 1 {
		return directoryUnavailable(
			fmt.Sprintf("retirement audit receipt chain is invalid at %d: %s",
				report.BreakAt, report.Reason),
			nil,
		)
	}
	return nil
}

// validateStableRetirementAuditMeta compares the idempotency-bearing metadata
// while allowing the exact optional trace/span pair that audit.Append adds at
// seal time. Request trace context is correlation, not part of the retirement
// identity: an ACK-lost retry under a different/no trace must still recognize
// the immutable receipt. No other stored key is ignored.
func validateStableRetirementAuditMeta(
	stored string,
	want map[string]any,
) error {
	decoder := json.NewDecoder(strings.NewReader(stored))
	decoder.UseNumber()
	var got map[string]any
	if err := decoder.Decode(&got); err != nil {
		return directoryUnavailable("decode retirement audit receipt metadata", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return directoryUnavailable("retirement audit receipt metadata has trailing data", err)
	}
	canonicalStored, err := canon.CanonicalMeta(got)
	if err != nil || canonicalStored != stored {
		return directoryUnavailable("retirement audit receipt metadata is not canonical", err)
	}
	traceValue, hasTrace := got[obstrace.MetaTraceID]
	spanValue, hasSpan := got[obstrace.MetaSpanID]
	if hasTrace != hasSpan {
		return directoryUnavailable("retirement audit receipt has partial trace correlation", nil)
	}
	if hasTrace {
		traceID, traceOK := traceValue.(string)
		spanID, spanOK := spanValue.(string)
		if !traceOK || !spanOK || !canonicalLowerHex(traceID, 32, true) ||
			!canonicalLowerHex(spanID, 16, true) {
			return directoryUnavailable("retirement audit receipt trace correlation is invalid", nil)
		}
		delete(got, obstrace.MetaTraceID)
		delete(got, obstrace.MetaSpanID)
	}
	gotStable, err := canon.CanonicalMeta(got)
	if err != nil {
		return directoryUnavailable("canonicalize stable retirement audit metadata", err)
	}
	wantStable, err := canon.CanonicalMeta(want)
	if err != nil {
		return directoryUnavailable("canonicalize retirement audit receipt", err)
	}
	if gotStable != wantStable {
		return directoryUnavailable("retirement audit receipt metadata does not match request", nil)
	}
	return nil
}

func canonicalLowerHex(raw string, width int, rejectZero bool) bool {
	if len(raw) != width || strings.ToLower(raw) != raw {
		return false
	}
	decoded, err := hex.DecodeString(raw)
	if err != nil || len(decoded) != width/2 {
		return false
	}
	if rejectZero {
		for _, b := range decoded {
			if b != 0 {
				return true
			}
		}
		return false
	}
	return true
}

func (sys *systemScope) readRetirementAuditByID(
	ctx context.Context,
	tenant model.TenantID,
	id model.ID,
) (model.AuditEvent, string, bool, error) {
	return sys.readOneRetirementAudit(
		ctx,
		"tenant_id = ? AND id = ?",
		tenant.String(), id.String(),
	)
}

func (sys *systemScope) readRetirementAuditByTarget(
	ctx context.Context,
	tenant model.TenantID,
	action string,
	targetKind model.Kind,
	targetID model.ID,
) (model.AuditEvent, string, bool, error) {
	return sys.readOneRetirementAudit(
		ctx,
		"tenant_id = ? AND action = ? AND target_kind = ? AND target_id = ?",
		tenant.String(), action, string(targetKind), targetID.String(),
	)
}

func (sys *systemScope) readOneRetirementAudit(
	ctx context.Context,
	predicate string,
	args ...any,
) (model.AuditEvent, string, bool, error) {
	query := "SELECT " + columnList(auditColumns) + " FROM " +
		directoryWriterRelation(sys.s.dia, auditTable) + " WHERE " + predicate + " LIMIT 2"
	rows, err := sys.tx.QueryContext(ctx, sys.s.dia.Rebind(query), args...)
	if err != nil {
		return model.AuditEvent{}, "", false,
			directoryUnavailable("read retirement audit receipt", err)
	}
	defer rows.Close() //nolint:errcheck // rows.Err below is authoritative
	var events []model.AuditEvent
	var metas []string
	for rows.Next() {
		event, metaCanonical, _, err := scanAudit(rows)
		if err != nil {
			return model.AuditEvent{}, "", false,
				directoryUnavailable("decode retirement audit receipt", err)
		}
		events = append(events, event)
		metas = append(metas, metaCanonical)
	}
	if err := rows.Err(); err != nil {
		return model.AuditEvent{}, "", false,
			directoryUnavailable("read retirement audit receipt", err)
	}
	switch len(events) {
	case 0:
		return model.AuditEvent{}, "", false, nil
	case 1:
		return events[0], metas[0], true, nil
	default:
		return model.AuditEvent{}, "", false,
			directoryUnavailable("retirement audit receipt is ambiguous", nil)
	}
}

// Compile-time guards: the concrete reader used above is the same capability
// exposed to modules, not a retirement-specific SQL approximation.
var _ store.DirectorySnapshotReader = (*tenantScope)(nil)
