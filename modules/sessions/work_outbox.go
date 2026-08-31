// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

var errWorkEventSinkUnwired = errors.New("sessions: work event sink unwired")

type workOutboxClaim struct {
	row      model.Record
	envelope WorkEventEnvelope
}

// DrainWorkOutbox publishes at most limit committed events. Network/sink work
// occurs outside every store transaction. A lost settlement is recovered by
// the expiring claim and Eventing deduplicates the stable EventID.
func (m *Module) DrainWorkOutbox(ctx context.Context, tenant model.TenantID, limit int) error {
	return m.drainWorkOutboxWithData(ctx, m.workData(tenant), tenant, limit, true)
}

// ValidateWorkOutboxReplay observes the same state as replay without writing.
func (m *Module) ValidateWorkOutboxReplay(
	ctx context.Context,
	tenant model.TenantID,
	principal WorkPrincipal,
	cmd WorkOutboxReplayCommand,
) (Assessment, error) {
	plan, err := m.planWorkOutboxReplayWithData(ctx, m.workData(tenant), tenant, principal, cmd)
	if err != nil {
		if assessment, ok := assessmentFromError(m.clock.Now().String(), err); ok {
			return assessment, nil
		}
		return Assessment{}, err
	}
	plan.Assessment.PlanHash = ""
	return plan.Assessment, nil
}

// PlanWorkOutboxReplay content-addresses the exact dead-letter state that apply
// may requeue. It is observational: audit, receipt and outbox remain untouched.
func (m *Module) PlanWorkOutboxReplay(
	ctx context.Context,
	tenant model.TenantID,
	principal WorkPrincipal,
	cmd WorkOutboxReplayCommand,
) (Plan, error) {
	plan, err := m.planWorkOutboxReplayWithData(ctx, m.workData(tenant), tenant, principal, cmd)
	if err != nil {
		if assessment, ok := assessmentFromError(m.clock.Now().String(), err); ok {
			return Plan{
				Assessment: assessment, Command: cmd.Command,
				RowEffects: []string{}, ExternalCalls: []string{},
			}, nil
		}
		return Plan{}, err
	}
	return plan, nil
}

// ReplayWorkOutbox applies one planned admin replay without duplicating its
// WorkEvent. The exact retry resolves from CommandReceipt before revalidating
// the now-pending outbox row, so it never appends a second audit or repeats CAS.
func (m *Module) ReplayWorkOutbox(
	ctx context.Context,
	tenant model.TenantID,
	principal WorkPrincipal,
	cmd WorkOutboxReplayCommand,
) (WorkOutboxReplay, error) {
	return m.replayWorkOutboxWithData(ctx, m.workData(tenant), tenant, principal, cmd)
}

func (m *Module) planWorkOutboxReplayWithData(
	ctx context.Context,
	data workData,
	tenant model.TenantID,
	principal WorkPrincipal,
	cmd WorkOutboxReplayCommand,
) (Plan, error) {
	if cmd.Command != "outbox.replay" {
		return Plan{}, broken(http.StatusBadRequest, "invalid_command")
	}
	if tenant.IsZero() || tenant.IsSystem() || cmd.EventID.IsZero() {
		return Plan{}, broken(http.StatusBadRequest, "invalid_command")
	}
	if !principal.Admin {
		return Plan{}, broken(http.StatusForbidden, "forbidden")
	}
	var plan Plan
	err := data.View(ctx, func(sc store.Scope) error {
		var err error
		plan, _, _, err = m.planWorkOutboxReplayInScope(ctx, sc, cmd)
		return err
	})
	return plan, classifyWorkStoreError(err)
}

func (m *Module) planWorkOutboxReplayInScope(
	ctx context.Context,
	sc store.Scope,
	cmd WorkOutboxReplayCommand,
) (Plan, model.Record, model.Record, error) {
	repo, err := sc.Ext(workOutboxKind)
	if err != nil {
		return Plan{}, nil, nil, err
	}
	rows, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{{
		Column: colOutboxEventID, Op: model.OpEq, Value: cmd.EventID.String(),
	}}, Limit: 2})
	if err != nil {
		return Plan{}, nil, nil, err
	}
	if len(rows) == 0 {
		events, extErr := sc.Ext(workEventKind)
		if extErr != nil {
			return Plan{}, nil, nil, extErr
		}
		eventRows, _, listErr := events.List(ctx, model.Query{Filters: []model.Filter{{
			Column: colEventID, Op: model.OpEq, Value: cmd.EventID.String(),
		}}, Limit: 1})
		if listErr != nil {
			return Plan{}, nil, nil, listErr
		}
		if len(eventRows) == 1 {
			return Plan{}, nil, nil, unknown(
				"evidence_unavailable", errors.New("sessions: work event has no outbox row"),
			)
		}
		return Plan{}, nil, nil, broken(http.StatusNotFound, "not_found")
	}
	if len(rows) != 1 {
		return Plan{}, nil, nil, unknown(
			"evidence_unavailable", errors.New("sessions: duplicate work outbox event rows"),
		)
	}
	outbox := rows[0]
	if err := validateWorkOutboxEvidence(outbox); err != nil {
		return Plan{}, nil, nil, err
	}
	if cmd.ExpectedVersion > 0 && outbox.Int(model.ColVersion) != cmd.ExpectedVersion {
		return Plan{}, nil, nil, broken(http.StatusPreconditionFailed, "version_mismatch")
	}
	if outbox.String(colOutboxState) != "dead_letter" {
		return Plan{}, nil, nil, broken(http.StatusConflict, "state_conflict")
	}
	events, err := sc.Ext(workEventKind)
	if err != nil {
		return Plan{}, nil, nil, err
	}
	event, err := workOutboxEvent(ctx, events, outbox)
	if err != nil {
		return Plan{}, nil, nil, err
	}
	plan := Plan{
		Assessment: Assessment{
			Verdict: VerdictClean, Code: "ok", ObservedAt: m.clock.Now().String(),
			Checks: []WorkCheck{{
				Name: "dead_letter_replayable", Verdict: VerdictClean,
				EvidenceRef: cmd.EventID.String(),
			}},
		},
		Command: "outbox.replay", ExpectedETag: fmt.Sprintf("\"v%d\"", outbox.Int(model.ColVersion)),
		RowEffects: []string{
			"core.audit:append", "sessions.work_outbox:cas", "sessions.work_command:append",
		},
		EventType: "", AuditAction: "sessions.work.outbox.replay",
		Permission: string(permWorkAdmin), ExternalCalls: []string{},
	}
	preimage := plan
	preimage.PlanHash, preimage.ObservedAt = "", ""
	b, err := canonicalJSON(struct {
		Plan          Plan     `json:"plan"`
		Command       string   `json:"command"`
		EventID       model.ID `json:"event_id"`
		AggregateKind string   `json:"aggregate_kind"`
		AggregateID   model.ID `json:"aggregate_id"`
		OutboxID      model.ID `json:"outbox_id"`
		State         string   `json:"state"`
		Version       int64    `json:"version"`
		Attempts      int64    `json:"attempts"`
		LastOutcome   string   `json:"last_outcome"`
	}{
		Plan: preimage, Command: cmd.Command, EventID: cmd.EventID,
		AggregateKind: event.String(colEventAggregateKind),
		AggregateID:   model.ID(event.String(colEventAggregateID)), OutboxID: recordID(outbox),
		State: outbox.String(colOutboxState), Version: outbox.Int(model.ColVersion),
		Attempts: outbox.Int(colOutboxAttempts), LastOutcome: outbox.String(colOutboxLastOutcome),
	})
	if err != nil {
		return Plan{}, nil, nil, err
	}
	plan.PlanHash = hexHash(hashBytes(b))
	plan.Assessment.PlanHash = plan.PlanHash
	return plan, outbox, event, nil
}

func normalizeWorkOutboxReplayCommand(cmd WorkOutboxReplayCommand) WorkOutboxReplayCommand {
	if cmd.ExpectedPlanHash == "" {
		cmd.ExpectedPlanHash = cmd.PlanHash
	}
	if cmd.HTTPMethod == "" {
		cmd.HTTPMethod = http.MethodPost
	}
	if cmd.CommandScope == "" {
		cmd.CommandScope = "POST /v1/m/sessions/work-events/{id}/replay"
	}
	return cmd
}

func workOutboxReplayHashes(
	principal WorkPrincipal,
	cmd WorkOutboxReplayCommand,
) (actorFP, requestHash, idemHash []byte, scope string, err error) {
	if principal.Actor == "" || principal.ActorKind == "" || principal.ActorRef == "" {
		return nil, nil, nil, "", fmt.Errorf("principal is not attributable")
	}
	if _, parseErr := model.ParseID(cmd.IdempotencyKey); parseErr != nil || cmd.IdempotencyKey == "" {
		return nil, nil, nil, "", broken(http.StatusBadRequest, "idempotency_key_required")
	}
	actor := sha256.Sum256([]byte(principal.ActorKind + "\x00" + principal.ActorRef + "\x00" + principal.Actor))
	idem := sha256.Sum256([]byte(cmd.IdempotencyKey))
	b, err := canonicalJSON(struct {
		Command          string   `json:"command"`
		EventID          model.ID `json:"event_id"`
		Method           string   `json:"method"`
		Scope            string   `json:"scope"`
		ExpectedVersion  int64    `json:"expected_version"`
		ExpectedPlanHash string   `json:"expected_plan_hash"`
	}{cmd.Command, cmd.EventID, cmd.HTTPMethod, cmd.CommandScope, cmd.ExpectedVersion, cmd.ExpectedPlanHash})
	if err != nil {
		return nil, nil, nil, "", err
	}
	request := sha256.Sum256(b)
	return actor[:], request[:], idem[:], cmd.CommandScope, nil
}

func (m *Module) replayWorkOutboxWithData(
	ctx context.Context,
	data workData,
	tenant model.TenantID,
	principal WorkPrincipal,
	cmd WorkOutboxReplayCommand,
) (WorkOutboxReplay, error) {
	cmd = normalizeWorkOutboxReplayCommand(cmd)
	if cmd.Command != "outbox.replay" {
		return WorkOutboxReplay{}, broken(http.StatusBadRequest, "invalid_command")
	}
	if tenant.IsZero() || tenant.IsSystem() || cmd.EventID.IsZero() {
		return WorkOutboxReplay{}, broken(http.StatusBadRequest, "invalid_command")
	}
	if !principal.Admin {
		return WorkOutboxReplay{}, broken(http.StatusForbidden, "forbidden")
	}
	if cmd.ExpectedVersion < 1 {
		return WorkOutboxReplay{}, broken(http.StatusPreconditionRequired, "version_required")
	}
	if cmd.ExpectedPlanHash == "" {
		return WorkOutboxReplay{}, broken(http.StatusPreconditionFailed, "plan_changed")
	}
	expectedPlanHash, err := decodeHash(cmd.ExpectedPlanHash, true)
	if err != nil {
		return WorkOutboxReplay{}, broken(http.StatusBadRequest, "invalid_command")
	}
	cmd.ExpectedPlanHash = hexHash(expectedPlanHash)
	actorFP, requestHash, idemHash, scope, err := workOutboxReplayHashes(principal, cmd)
	if err != nil {
		return WorkOutboxReplay{}, err
	}
	if replay, found, err := m.lookupWorkOutboxReplay(
		ctx, data, actorFP, idemHash, scope, requestHash,
	); err != nil {
		return WorkOutboxReplay{}, classifyWorkStoreError(err)
	} else if found {
		replay.Replayed = true
		return replay, nil
	}

	var result WorkOutboxReplay
	var auditGap bool
	err = data.Mutate(ctx, func(sc store.Scope) error {
		if replay, found, err := findWorkOutboxReplayReceipt(
			ctx, sc, actorFP, idemHash, scope, requestHash,
		); err != nil {
			return err
		} else if found {
			result, result.Replayed = replay, true
			return nil
		}
		clock, ok := sc.(store.TransactionClock)
		if !ok {
			return unknown("clock_unavailable", nil)
		}
		now, err := clock.TransactionNow(ctx)
		if err != nil {
			return unknown("clock_unavailable", err)
		}
		plan, row, event, err := m.planWorkOutboxReplayInScope(ctx, sc, cmd)
		if err != nil {
			return err
		}
		if plan.PlanHash != cmd.ExpectedPlanHash {
			return broken(http.StatusPreconditionFailed, "plan_changed")
		}
		planHash, err := decodeHash(plan.PlanHash, true)
		if err != nil {
			return err
		}
		commandID := model.NewID()
		auditEvent, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor: principal.Actor, ActorKind: principal.ActorKind,
			Action: "sessions.work.outbox.replay", TargetKind: workCommandKind,
			TargetID: commandID, PayloadHash: planHash,
			Meta: map[string]any{
				"command": "outbox.replay", "event_id": cmd.EventID.String(),
				"aggregate_kind": event.String(colEventAggregateKind),
				"aggregate_id":   event.String(colEventAggregateID),
				"workspace_id":   row.String(colWorkWorkspaceID),
				"attempts":       row.Int(colOutboxAttempts),
			},
		})
		if err != nil {
			return err
		}
		if auditEvent.Seq == 0 {
			auditGap = true
			return nil
		}
		repo, err := sc.Ext(workOutboxKind)
		if err != nil {
			return err
		}
		priorState, priorVersion := row.String(colOutboxState), row.Int(model.ColVersion)
		row[colOutboxState], row[colOutboxNextAttemptAt] = "pending", now.String()
		row[colOutboxClaimOwner], row[colOutboxClaimUntil], row[colOutboxPublishedAt] = nil, nil, nil
		row[colOutboxLastOutcome] = "admin_requeued"
		row, err = repo.Update(ctx, row)
		if err != nil {
			return err
		}
		result = WorkOutboxReplay{
			Verdict: VerdictClean, Code: "requeued", CommandID: commandID,
			OutboxID: recordID(row), EventID: cmd.EventID,
			AggregateKind: event.String(colEventAggregateKind),
			AggregateID:   model.ID(event.String(colEventAggregateID)),
			State:         row.String(colOutboxState), Version: row.Int(model.ColVersion),
			Attempts: row.Int(colOutboxAttempts), PriorState: priorState, PriorVersion: priorVersion,
			PlanHash: plan.PlanHash, AuditSeq: auditEvent.Seq,
		}
		if result.AggregateKind == string(workItemKind) {
			result.WorkItemID = result.AggregateID
		}
		response, err := canonicalJSON(result)
		if err != nil || len(response) > 16*1024 {
			return broken(http.StatusInternalServerError, "response_too_large")
		}
		result.responseJSON = string(response)
		receipts, err := sc.Ext(workCommandKind)
		if err != nil {
			return err
		}
		_, err = receipts.Create(ctx, model.Record{
			colWorkWorkspaceID: row.String(colWorkWorkspaceID),
			colCommandID:       commandID.String(), colCommandActorFP: actorFP,
			colCommandScope: scope, colCommandIdempotency: idemHash,
			colCommandRequestHash: requestHash, colCommandPlanHash: planHash,
			colCommandResultKind: string(workOutboxKind), colCommandResultID: recordID(row).String(),
			colCommandHTTPStatus: int64(http.StatusAccepted), colCommandResponse: string(response),
			colCommandResponseHash: hashBytes(response), colCommandAuditSeq: auditEvent.Seq,
			colCommandAuditHash: auditEvent.Hash, colCommandCompletedAt: now.String(),
		})
		return err
	})
	if err != nil {
		err = classifyWorkStoreError(err)
		if errors.Is(err, store.ErrConflict) ||
			(asWorkError(err) != nil && asWorkError(err).code == "state_conflict") {
			if replay, found, replayErr := m.lookupWorkOutboxReplay(
				ctx, data, actorFP, idemHash, scope, requestHash,
			); replayErr != nil {
				return WorkOutboxReplay{}, classifyWorkStoreError(replayErr)
			} else if found {
				replay.Replayed = true
				return replay, nil
			}
		}
		return WorkOutboxReplay{}, err
	}
	if auditGap {
		return WorkOutboxReplay{}, unknown("evidence_unavailable", nil)
	}
	return result, nil
}

func (m *Module) lookupWorkOutboxReplay(
	ctx context.Context,
	data workData,
	actorFP, idemHash []byte,
	scope string,
	requestHash []byte,
) (WorkOutboxReplay, bool, error) {
	var result WorkOutboxReplay
	var found bool
	err := data.View(ctx, func(sc store.Scope) error {
		var err error
		result, found, err = findWorkOutboxReplayReceipt(
			ctx, sc, actorFP, idemHash, scope, requestHash,
		)
		return err
	})
	return result, found, err
}

// decodeWorkOutboxReplay keeps K1 receipts readable after WorkEvent became a
// dual aggregate. New receipts bind both aggregate_kind and aggregate_id; an
// old work_item_id is accepted only as the exact sessions.work_item alias.
func decodeWorkOutboxReplay(response []byte) (WorkOutboxReplay, error) {
	var result WorkOutboxReplay
	if err := json.Unmarshal(response, &result); err != nil {
		return WorkOutboxReplay{}, err
	}
	result.responseJSON = string(response)
	if result.WorkItemID.IsZero() {
		return result, nil
	}
	if result.AggregateKind == "" && result.AggregateID.IsZero() {
		result.AggregateKind = string(workItemKind)
		result.AggregateID = result.WorkItemID
		return result, nil
	}
	if result.AggregateKind != string(workItemKind) || result.AggregateID != result.WorkItemID {
		return WorkOutboxReplay{}, errors.New("sessions: conflicting outbox replay aggregate aliases")
	}
	return result, nil
}

func findWorkOutboxReplayReceipt(
	ctx context.Context,
	sc store.Scope,
	actorFP, idemHash []byte,
	scope string,
	requestHash []byte,
) (WorkOutboxReplay, bool, error) {
	repo, err := sc.Ext(workCommandKind)
	if err != nil {
		return WorkOutboxReplay{}, false, err
	}
	rows, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{
		{Column: colCommandActorFP, Op: model.OpEq, Value: actorFP},
		{Column: colCommandScope, Op: model.OpEq, Value: scope},
		{Column: colCommandIdempotency, Op: model.OpEq, Value: idemHash},
	}, Limit: 1})
	if err != nil || len(rows) == 0 {
		return WorkOutboxReplay{}, false, err
	}
	receipt := rows[0]
	if !bytesEqual(receipt.Bytes(colCommandRequestHash), requestHash) {
		return WorkOutboxReplay{}, false, broken(http.StatusConflict, "idempotency_key_reused")
	}
	response := []byte(receipt.String(colCommandResponse))
	if !bytesEqual(receipt.Bytes(colCommandResponseHash), hashBytes(response)) {
		return WorkOutboxReplay{}, false, unknown(
			"evidence_unavailable", errors.New("outbox replay receipt response digest mismatch"),
		)
	}
	result, err := decodeWorkOutboxReplay(response)
	if err != nil {
		return WorkOutboxReplay{}, false, unknown("evidence_unavailable", err)
	}
	if result.Verdict != VerdictClean || result.Code != "requeued" ||
		result.CommandID.String() != receipt.String(colCommandID) ||
		receipt.String(colCommandResultKind) != string(workOutboxKind) ||
		result.OutboxID.String() != receipt.String(colCommandResultID) ||
		result.PlanHash != hexHash(receipt.Bytes(colCommandPlanHash)) ||
		result.AuditSeq != receipt.Int(colCommandAuditSeq) || result.EventID.IsZero() ||
		!validWorkEventAggregateKind(result.AggregateKind) || result.AggregateID.IsZero() ||
		(result.AggregateKind == string(workItemKind) && result.WorkItemID != result.AggregateID) ||
		(result.AggregateKind == string(messageKind) && !result.WorkItemID.IsZero()) ||
		result.State != "pending" ||
		result.PriorState != "dead_letter" || result.PriorVersion < 1 ||
		result.Version != result.PriorVersion+1 || result.Attempts < 1 {
		return WorkOutboxReplay{}, false, unknown(
			"evidence_unavailable", errors.New("outbox replay receipt response anchors mismatch"),
		)
	}
	outbox, err := sc.Ext(workOutboxKind)
	if err != nil {
		return WorkOutboxReplay{}, false, err
	}
	row, err := outbox.Get(ctx, result.OutboxID)
	if err != nil || row.String(colOutboxEventID) != result.EventID.String() ||
		row.Int(model.ColVersion) < result.Version || row.Int(colOutboxAttempts) < result.Attempts {
		return WorkOutboxReplay{}, false, unknown(
			"evidence_unavailable", errors.New("outbox replay receipt target mismatch"),
		)
	}
	if err := validateWorkOutboxEvidence(row); err != nil {
		return WorkOutboxReplay{}, false, err
	}
	events, err := sc.Ext(workEventKind)
	if err != nil {
		return WorkOutboxReplay{}, false, err
	}
	event, err := workOutboxEvent(ctx, events, row)
	if err != nil || event.String(colEventAggregateKind) != result.AggregateKind ||
		event.String(colEventAggregateID) != result.AggregateID.String() {
		return WorkOutboxReplay{}, false, unknown(
			"evidence_unavailable", errors.New("outbox replay receipt aggregate mismatch"),
		)
	}
	stop := errors.New("sessions: stop replay audit walk")
	var auditEvent model.AuditEvent
	err = sc.Audit().Walk(ctx, result.AuditSeq, func(ev model.AuditEvent) error {
		auditEvent = ev
		return stop
	})
	if err != nil && !errors.Is(err, stop) {
		return WorkOutboxReplay{}, false, err
	}
	if auditEvent.Seq != result.AuditSeq || auditEvent.Action != "sessions.work.outbox.replay" ||
		auditEvent.TargetKind != workCommandKind || auditEvent.TargetID != result.CommandID ||
		!bytesEqual(auditEvent.PayloadHash, receipt.Bytes(colCommandPlanHash)) ||
		!bytesEqual(auditEvent.Hash, receipt.Bytes(colCommandAuditHash)) {
		return WorkOutboxReplay{}, false, unknown(
			"evidence_unavailable", errors.New("outbox replay receipt audit anchors mismatch"),
		)
	}
	return result, true, nil
}

func (m *Module) drainWorkOutboxWithData(
	ctx context.Context,
	data workData,
	tenant model.TenantID,
	limit int,
	allowDeadLetter bool,
) error {
	if m.workEventSink == nil {
		return errWorkEventSinkUnwired
	}
	if limit < 1 {
		limit = 100
	}
	if limit > 200 {
		limit = 200
	}
	for i := 0; i < limit; i++ {
		claim, ok, err := m.claimWorkOutbox(ctx, data, tenant, allowDeadLetter)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		deliveryErr := m.workEventSink.IngestDurable(ctx, claim.envelope)
		if err := m.settleWorkOutbox(ctx, data, claim, deliveryErr); err != nil {
			return err
		}
		if deliveryErr != nil {
			// The durable row already carries the bounded outcome and next retry.
			// Returning the cause makes the pump's run NO HE PODIDO MIRAR instead
			// of falsely reporting a clean delivery cycle.
			return deliveryErr
		}
	}
	return nil
}

func (m *Module) claimWorkOutbox(
	ctx context.Context,
	data workData,
	tenant model.TenantID,
	allowDeadLetter bool,
) (workOutboxClaim, bool, error) {
	var claim workOutboxClaim
	var found bool
	err := data.Mutate(ctx, func(sc store.Scope) error {
		clock, ok := sc.(store.TransactionClock)
		if !ok {
			return unknown("clock_unavailable", nil)
		}
		now, err := clock.TransactionNow(ctx)
		if err != nil {
			return err
		}
		repo, err := sc.Ext(workOutboxKind)
		if err != nil {
			return err
		}
		pendingFilters := []model.Filter{
			{Column: colOutboxState, Op: model.OpEq, Value: "pending"},
			{Column: colOutboxNextAttemptAt, Op: model.OpLte, Value: now.String()},
		}
		if !allowDeadLetter {
			pendingFilters = append(pendingFilters, model.Filter{Column: colOutboxAttempts, Op: model.OpLt, Value: int64(9)})
		}
		deliveringFilters := []model.Filter{
			{Column: colOutboxState, Op: model.OpEq, Value: "delivering"},
			{Column: colOutboxClaimUntil, Op: model.OpLte, Value: now.String()},
		}
		if !allowDeadLetter {
			deliveringFilters = append(deliveringFilters, model.Filter{Column: colOutboxAttempts, Op: model.OpLt, Value: int64(9)})
		}
		events, err := sc.Ext(workEventKind)
		if err != nil {
			return err
		}
		row, ev, candidateFound, err := firstClaimableWorkOutbox(
			ctx, events, repo, pendingFilters, deliveringFilters,
		)
		if err != nil {
			return err
		}
		if !candidateFound {
			// Every due candidate is waiting for an earlier fact in its own
			// aggregate. A different aggregate remains independently claimable.
			return nil
		}
		row[colOutboxState] = "delivering"
		row[colOutboxAttempts] = row.Int(colOutboxAttempts) + 1
		row[colOutboxClaimOwner] = "sessions.work-pump"
		row[colOutboxClaimUntil] = model.NewTimestamp(now.Time().Add(30 * time.Second)).String()
		row[colOutboxLastOutcome] = nil
		row, err = repo.Update(ctx, row)
		if err != nil {
			return err
		}
		claim = workOutboxClaim{row: row, envelope: WorkEventEnvelope{
			TenantID: tenant, WorkspaceID: model.ID(ev.String(colWorkWorkspaceID)),
			EventID: model.ID(ev.String(colEventID)), AggregateKind: ev.String(colEventAggregateKind),
			AggregateID: model.ID(ev.String(colEventAggregateID)), Sequence: ev.Int(colEventSeq),
			Type: ev.String(colEventType), OccurredAt: ev.String(colEventOccurredAt),
			Payload: []byte(ev.String(colEventPayload)),
		}}
		found = true
		return nil
	})
	return claim, found, err
}

func firstClaimableWorkOutbox(
	ctx context.Context,
	events store.GenericRepo,
	outbox store.GenericRepo,
	pendingFilters []model.Filter,
	deliveringFilters []model.Filter,
) (model.Record, model.Record, bool, error) {
	row, event, found, err := firstReadyWorkOutbox(ctx, events, outbox, pendingFilters)
	var firstEvidenceErr error
	if err != nil {
		if !candidateWorkOutboxEvidenceError(err) {
			return nil, nil, false, err
		}
		firstEvidenceErr = err
	} else if found {
		return row, event, true, nil
	}

	row, event, found, err = firstReadyWorkOutbox(ctx, events, outbox, deliveringFilters)
	if err != nil {
		if !candidateWorkOutboxEvidenceError(err) {
			return nil, nil, false, err
		}
		if firstEvidenceErr == nil {
			firstEvidenceErr = err
		}
	} else if found {
		return row, event, true, nil
	}
	if firstEvidenceErr != nil {
		return nil, nil, false, firstEvidenceErr
	}
	return nil, nil, false, nil
}

func firstReadyWorkOutbox(
	ctx context.Context,
	events store.GenericRepo,
	outbox store.GenericRepo,
	filters []model.Filter,
) (model.Record, model.Record, bool, error) {
	query := model.Query{Filters: filters, Limit: 100}
	var firstEvidenceErr error
	for {
		rows, page, err := outbox.List(ctx, query)
		if err != nil {
			return nil, nil, false, err
		}
		for _, candidate := range rows {
			if err := validateWorkOutboxEvidence(candidate); err != nil {
				if !candidateWorkOutboxEvidenceError(err) {
					return nil, nil, false, err
				}
				if firstEvidenceErr == nil {
					firstEvidenceErr = err
				}
				continue
			}
			event, err := workOutboxEvent(ctx, events, candidate)
			if err != nil {
				if !candidateWorkOutboxEvidenceError(err) {
					return nil, nil, false, err
				}
				if firstEvidenceErr == nil {
					firstEvidenceErr = err
				}
				continue
			}
			ready, err := workOutboxPredecessorPublished(ctx, events, outbox, event)
			if err != nil {
				if !candidateWorkOutboxEvidenceError(err) {
					return nil, nil, false, err
				}
				if firstEvidenceErr == nil {
					firstEvidenceErr = err
				}
				continue
			}
			if ready {
				return candidate, event, true, nil
			}
		}
		if !page.HasMore || page.Cursor == "" {
			if firstEvidenceErr != nil {
				return nil, nil, false, firstEvidenceErr
			}
			return nil, nil, false, nil
		}
		query.Cursor = page.Cursor
	}
}

func candidateWorkOutboxEvidenceError(err error) bool {
	workErr := asWorkError(err)
	return workErr != nil && workErr.verdict == VerdictUnknown &&
		workErr.code == "evidence_unavailable"
}

func validateWorkOutboxEvidence(row model.Record) error {
	invalid := func(detail string) error {
		return unknown("evidence_unavailable", errors.New("sessions: inconsistent work outbox "+detail))
	}
	if id, err := model.ParseID(row.String(model.ColID)); err != nil || id.IsZero() {
		return invalid("id")
	}
	if eventID, err := model.ParseID(row.String(colOutboxEventID)); err != nil || eventID.IsZero() {
		return invalid("event id")
	}
	if workspaceID, err := model.ParseID(row.String(colWorkWorkspaceID)); err != nil || workspaceID.IsZero() {
		return invalid("workspace lineage")
	}
	if row.Int(model.ColVersion) < 1 || row.Int(colOutboxAttempts) < 0 {
		return invalid("generation")
	}
	if _, err := model.ParseTimestamp(row.String(colOutboxNextAttemptAt)); err != nil {
		return invalid("retry time")
	}
	claimOwner := row.String(colOutboxClaimOwner)
	claimUntil := row.String(colOutboxClaimUntil)
	publishedAt := row.String(colOutboxPublishedAt)
	switch row.String(colOutboxState) {
	case "pending":
		if claimOwner != "" || claimUntil != "" || publishedAt != "" {
			return invalid("pending lifecycle")
		}
	case "delivering":
		if row.Int(colOutboxAttempts) < 1 || claimOwner == "" || publishedAt != "" {
			return invalid("delivering lifecycle")
		}
		if _, err := model.ParseTimestamp(claimUntil); err != nil {
			return invalid("claim time")
		}
	case "published":
		if row.Int(colOutboxAttempts) < 1 || claimOwner != "" || claimUntil != "" ||
			row.String(colOutboxLastOutcome) != "published" {
			return invalid("published lifecycle")
		}
		if _, err := model.ParseTimestamp(publishedAt); err != nil {
			return invalid("published time")
		}
	case "dead_letter":
		if row.Int(colOutboxAttempts) < 10 || claimOwner != "" || claimUntil != "" ||
			publishedAt != "" || row.String(colOutboxLastOutcome) == "" {
			return invalid("dead-letter lifecycle")
		}
	default:
		return invalid("state")
	}
	return nil
}

func workOutboxEvent(
	ctx context.Context,
	events store.GenericRepo,
	row model.Record,
) (model.Record, error) {
	eventRows, _, err := events.List(ctx, model.Query{Filters: []model.Filter{{
		Column: colEventID, Op: model.OpEq, Value: row.String(colOutboxEventID),
	}}, Limit: 2})
	if err != nil {
		return nil, err
	}
	if len(eventRows) != 1 {
		return nil, unknown(
			"evidence_unavailable", fmt.Errorf("sessions: outbox event rows = %d", len(eventRows)),
		)
	}
	event := eventRows[0]
	eventID, eventIDErr := model.ParseID(event.String(colEventID))
	aggregateID, aggregateIDErr := model.ParseID(event.String(colEventAggregateID))
	if eventIDErr != nil || eventID.IsZero() || eventID.String() != row.String(colOutboxEventID) ||
		aggregateIDErr != nil || aggregateID.IsZero() ||
		event.String(colWorkWorkspaceID) != row.String(colWorkWorkspaceID) ||
		!validWorkEventAggregateKind(event.String(colEventAggregateKind)) ||
		event.Int(colEventSeq) < 1 ||
		!bytesEqual(event.Bytes(colEventPayloadHash), hashBytes([]byte(event.String(colEventPayload)))) {
		return nil, unknown(
			"evidence_unavailable", errors.New("sessions: outbox event lineage is inconsistent"),
		)
	}
	return event, nil
}

func validWorkEventAggregateKind(kind string) bool {
	return kind == string(workItemKind) || kind == string(messageKind)
}

// workOutboxPredecessorPublished prevents a later fact from overtaking an
// earlier fact of the same aggregate. It deliberately does not impose a global
// order: a delayed, claimed or failed aggregate cannot stop an independent one.
func workOutboxPredecessorPublished(
	ctx context.Context,
	events store.GenericRepo,
	outbox store.GenericRepo,
	event model.Record,
) (bool, error) {
	sequence := event.Int(colEventSeq)
	if sequence <= 1 {
		return true, nil
	}
	predecessors, _, err := events.List(ctx, model.Query{
		Filters: []model.Filter{
			{Column: colEventAggregateKind, Op: model.OpEq, Value: event.String(colEventAggregateKind)},
			{Column: colEventAggregateID, Op: model.OpEq, Value: event.String(colEventAggregateID)},
			{Column: colEventSeq, Op: model.OpLt, Value: sequence},
		},
		Sort: []model.Sort{{Column: colEventSeq, Desc: true}}, Limit: 1,
	})
	if err != nil {
		return false, err
	}
	if len(predecessors) != 1 {
		return false, unknown(
			"evidence_unavailable", errors.New("sessions: outbox predecessor event missing"),
		)
	}
	if predecessors[0].Int(colEventSeq) != sequence-1 {
		return false, unknown(
			"evidence_unavailable", errors.New("sessions: outbox predecessor sequence is inconsistent"),
		)
	}
	rows, _, err := outbox.List(ctx, model.Query{Filters: []model.Filter{{
		Column: colOutboxEventID, Op: model.OpEq, Value: predecessors[0].String(colEventID),
	}}, Limit: 2})
	if err != nil {
		return false, err
	}
	if len(rows) != 1 {
		return false, unknown(
			"evidence_unavailable", errors.New("sessions: outbox predecessor row missing"),
		)
	}
	if err := validateWorkOutboxEvidence(rows[0]); err != nil {
		return false, err
	}
	if rows[0].String(colOutboxEventID) != predecessors[0].String(colEventID) ||
		rows[0].String(colWorkWorkspaceID) != predecessors[0].String(colWorkWorkspaceID) {
		return false, unknown(
			"evidence_unavailable", errors.New("sessions: outbox predecessor lineage is inconsistent"),
		)
	}
	switch rows[0].String(colOutboxState) {
	case "published":
		return true, nil
	case "pending", "delivering", "dead_letter":
		return false, nil
	default:
		return false, unknown(
			"evidence_unavailable", errors.New("sessions: outbox predecessor state is inconsistent"),
		)
	}
}

func (m *Module) settleWorkOutbox(
	ctx context.Context,
	data workData,
	claim workOutboxClaim,
	deliveryErr error,
) error {
	return data.Mutate(ctx, func(sc store.Scope) error {
		clock, ok := sc.(store.TransactionClock)
		if !ok {
			return unknown("clock_unavailable", nil)
		}
		now, err := clock.TransactionNow(ctx)
		if err != nil {
			return err
		}
		repo, err := sc.Ext(workOutboxKind)
		if err != nil {
			return err
		}
		row, err := repo.Get(ctx, recordID(claim.row))
		if err != nil {
			return err
		}
		if row.Int(model.ColVersion) != claim.row.Int(model.ColVersion) || row.String(colOutboxState) != "delivering" {
			return store.ErrConflict
		}
		row[colOutboxClaimOwner], row[colOutboxClaimUntil] = nil, nil
		if deliveryErr == nil {
			row[colOutboxState], row[colOutboxPublishedAt], row[colOutboxLastOutcome] = "published", now.String(), "published"
		} else if row.Int(colOutboxAttempts) >= 10 {
			row[colOutboxState], row[colOutboxLastOutcome] = "dead_letter", "retry_exhausted"
			_, findingErr := sc.Findings().Create(ctx, model.Finding{
				Kind: "delivery", Severity: model.SeverityHigh, Status: model.FindingOpen,
				Source: Name, SubjectKind: string(workEventKind), SubjectID: claim.envelope.EventID,
				Title:      "Durable work event delivery exhausted",
				DetailHash: hashBytes([]byte("sessions.work.delivery.exhausted\x00" + claim.envelope.EventID.String())),
				OccurredAt: now,
				Metadata: map[string]any{
					"event_id": claim.envelope.EventID.String(), "workspace_id": claim.envelope.WorkspaceID.String(),
					"attempts": row.Int(colOutboxAttempts), "outcome": "retry_exhausted",
				},
			})
			if findingErr != nil {
				return findingErr
			}
		} else {
			backoff := time.Duration(1<<minInt64(row.Int(colOutboxAttempts), 8)) * time.Second
			row[colOutboxState], row[colOutboxNextAttemptAt], row[colOutboxLastOutcome] =
				"pending", model.NewTimestamp(now.Time().Add(backoff)).String(), classifySinkFailure(deliveryErr)
		}
		_, err = repo.Update(ctx, row)
		return err
	})
}

func minInt64(v, max int64) int64 {
	if v > max {
		return max
	}
	return v
}

func classifySinkFailure(err error) string {
	switch {
	case err == nil:
		return "published"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "sink_unavailable"
	}
}
