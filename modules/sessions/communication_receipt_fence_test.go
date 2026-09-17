// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// communicationReceiptFenceTables are the two append-only tables the command
// receipt and overdue-origin reads observe. Their ACL posture — SELECT and
// INSERT held by the application role, UPDATE/DELETE/TRUNCATE absent — is what
// the product read paths in this file have to live within.
var communicationReceiptFenceTables = []string{communicationCommandTable, workEventTable}

// requireSplitOwnerPostgresForTest gates a split-owner PostgreSQL test the way
// the accepted HTTP journey does: absent PostgreSQL is a skip that says so on a
// developer box, and a FAILURE when the run declares it required, so a leg that
// was built to run cannot vanish quietly.
func requireSplitOwnerPostgresForTest(t *testing.T) {
	t.Helper()
	if enginetest.PostgresAvailable(t) {
		return
	}
	required, err := strconv.ParseBool(strings.TrimSpace(os.Getenv("OLIVARES_TEST_POSTGRES_REQUIRED")))
	if err == nil && required {
		t.Fatal("split-owner PostgreSQL receipt journeys are required for this run and no isolated server is available")
	}
	t.Skipf("set %s to run the split-owner PostgreSQL receipt journeys", enginetest.EnvSuperuserDSN)
}

// splitOwnerPostgresBackendForTest provisions a private split-owner database
// and describes it as a communication backend, so the ordinary product
// fixtures open it exactly as they open SQLite.
func splitOwnerPostgresBackendForTest(t *testing.T, name string) communicationSchemaBackend {
	t.Helper()
	pg := enginetest.IsolatedPostgresSplitOwner(t)
	return communicationSchemaBackend{
		name: name, engineName: store.EnginePostgres, dsn: pg.App, ownerDSN: pg.Owner,
	}
}

// assertReceiptFenceACL reads the application role's effective privileges on
// both evidence tables and then issues, as that role with the tenant bound
// exactly as the pool binds it, the statements the product must never need:
// a row-update lock, a row-share lock, an UPDATE and a DELETE of a real receipt
// and a real work event, each of which must be refused with SQLSTATE 42501
// while a plain SELECT of the same row succeeds. It fails on any privilege
// beyond SELECT+INSERT, so a future grant cannot hide the class of defect this
// file closes.
func assertReceiptFenceACL(
	t *testing.T,
	fixture directNoticeFixture,
	appDSN string,
	rows map[string]model.ID,
) {
	t.Helper()
	ctx := context.Background()
	app, err := sql.Open("pgx", appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close() //nolint:errcheck
	var appRole string
	var superuser, bypassRLS bool
	if err := app.QueryRowContext(ctx, `SELECT current_user, r.rolsuper, r.rolbypassrls
FROM pg_catalog.pg_roles r WHERE r.rolname = current_user`).Scan(&appRole, &superuser, &bypassRLS); err != nil {
		t.Fatalf("read application role posture: %v", err)
	}
	if superuser || bypassRLS {
		t.Fatalf("application role %s is superuser=%t bypassrls=%t, want neither", appRole, superuser, bypassRLS)
	}
	for _, table := range communicationReceiptFenceTables {
		var canSelect, canInsert, canUpdate, canDelete, canTruncate bool
		if err := app.QueryRowContext(ctx, `SELECT
pg_catalog.has_table_privilege($1, 'SELECT'), pg_catalog.has_table_privilege($1, 'INSERT'),
pg_catalog.has_table_privilege($1, 'UPDATE'), pg_catalog.has_table_privilege($1, 'DELETE'),
pg_catalog.has_table_privilege($1, 'TRUNCATE')`, table).Scan(
			&canSelect, &canInsert, &canUpdate, &canDelete, &canTruncate); err != nil {
			t.Fatalf("read %s privileges: %v", table, err)
		}
		t.Logf("K3_RECEIPT_FENCE_ACL|role=%s|table=%s|select=%t|insert=%t|update=%t|delete=%t|truncate=%t",
			appRole, table, canSelect, canInsert, canUpdate, canDelete, canTruncate)
		if !canSelect || !canInsert || canUpdate || canDelete || canTruncate {
			t.Fatalf("%s privileges select=%t insert=%t update=%t delete=%t truncate=%t, want SELECT+INSERT only",
				table, canSelect, canInsert, canUpdate, canDelete, canTruncate)
		}
		id, ok := rows[table]
		if !ok || !validCanonicalCommunicationID(id) {
			t.Fatalf("no probe row for %s", table)
		}
		for _, probe := range []struct {
			name string
			sql  string
		}{
			{"row-update lock", "SELECT id FROM " + table + " WHERE id = $1 FOR UPDATE"},
			{"row-share lock", "SELECT id FROM " + table + " WHERE id = $1 FOR SHARE"},
			{"update", "UPDATE " + table + " SET version = version WHERE id = $1"},
			{"delete", "DELETE FROM " + table + " WHERE id = $1"},
		} {
			tx, err := app.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tx.ExecContext(ctx,
				"SELECT pg_catalog.set_config('app.tenant_id', $1, true)", fixture.tenant.String()); err != nil {
				t.Fatal(err)
			}
			_, err = tx.ExecContext(ctx, probe.sql, id.String())
			_ = tx.Rollback()
			communicationRequireSQLState(t, err, "42501", table+" "+probe.name+" by the application role")
		}
		tx, err := app.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx,
			"SELECT pg_catalog.set_config('app.tenant_id', $1, true)", fixture.tenant.String()); err != nil {
			t.Fatal(err)
		}
		var selected string
		err = tx.QueryRowContext(ctx, "SELECT id FROM "+table+" WHERE id = $1", id.String()).Scan(&selected)
		_ = tx.Rollback()
		if err != nil || selected != id.String() {
			t.Fatalf("plain SELECT of %s by the application role = %q, %v", table, selected, err)
		}
	}
}

// receiptFenceRowByScope returns the ID of the one receipt of scope in the
// fixture, failing when there is not exactly one.
func receiptFenceRowByScope(t *testing.T, fixture directNoticeFixture, scope string) model.ID {
	t.Helper()
	var found []model.ID
	for _, row := range communicationRowsForTest(t, fixture, communicationCommandKind) {
		if row.String(colCommCommandScope) != scope {
			continue
		}
		id, err := model.ParseID(row.String(model.ColID))
		if err != nil {
			t.Fatalf("receipt row ID: %v", err)
		}
		found = append(found, id)
	}
	if len(found) != 1 {
		t.Fatalf("receipts of scope %s = %d, want exactly one", scope, len(found))
	}
	return found[0]
}

// workEventRowByEventID returns the ID of the one work event carrying eventID;
// a zero eventID selects any single event row of the fixture.
func workEventRowByEventID(t *testing.T, fixture directNoticeFixture, eventID model.ID) model.ID {
	t.Helper()
	var found []model.ID
	for _, row := range communicationRowsForTest(t, fixture, workEventKind) {
		if eventID != "" && row.String(colEventID) != eventID.String() {
			continue
		}
		id, err := model.ParseID(row.String(model.ColID))
		if err != nil {
			t.Fatalf("work event row ID: %v", err)
		}
		found = append(found, id)
	}
	if eventID == "" && len(found) > 0 {
		return found[0]
	}
	if len(found) != 1 {
		t.Fatalf("work events with event_id %s = %d, want exactly one", eventID, len(found))
	}
	return found[0]
}

// TestCommunicationReceiptJourneysOnSplitOwnerPostgres drives the three
// product paths that used to take a row-update lock on append-only evidence,
// through their real entry points, on a split-owner PostgreSQL estate whose
// application role holds SELECT and INSERT only:
//
//   - a Message lifecycle command executed once and then replayed under the
//     same idempotency key (the receipt read under the command's transaction
//     key), with the reused-key/different-request control that proves the
//     replay actually observed the receipt;
//   - a delivery dispatch successor created once and replayed;
//   - an overdue escalation FIRST execution, the non-replay path that reads the
//     origin receipt and origin work event behind the locked source Message,
//     then its replay.
//
// Each journey also asserts the durable effect happened exactly once. The
// SQLite counterparts are the existing lifecycle, derived and successor tests.
func TestCommunicationReceiptJourneysOnSplitOwnerPostgres(t *testing.T) {
	t.Parallel()
	requireSplitOwnerPostgresForTest(t)

	t.Run("lifecycle-replay", func(t *testing.T) {
		backend := splitOwnerPostgresBackendForTest(t, "pg-split-owner-lifecycle")
		fixture := newDirectNoticeFixtureForBackend(t, backend, AckPolicyNone, 0, true, true, true)
		ctx := messageLifecycleTestContext(t)
		published, err := fixture.m.publishDirectNoticeWithAuthority(
			ctx, fixture.scope, fixture.ref, fixture.command(model.NewID(), "retract me on postgres"),
		)
		if err != nil {
			t.Fatalf("publish DirectNotice: %v", err)
		}
		authorizer := &messageLifecycleTestAuthorizer{
			wantScope: fixture.scope, wantID: published.MessageID, want: messageLifecycleRetract,
			authority: messageLifecycleAuthorityForTest(
				fixture, CommunicationActorRef{Kind: ActorUser, Ref: fixture.sender.String()},
			),
		}
		service, err := newMessageLifecycleService(fixture.m, authorizer, nil)
		if err != nil {
			t.Fatalf("new lifecycle service: %v", err)
		}
		beforeEvents := len(communicationRowsForTest(t, fixture, workEventKind))
		beforeOutbox := len(communicationRowsForTest(t, fixture, workOutboxKind))
		beforeReceipts := len(communicationRowsForTest(t, fixture, communicationCommandKind))
		command := messageLifecycleCommand{
			MessageID: published.MessageID, ExpectedVersion: published.Version,
			TerminalCode:   "sender_retracted",
			Reason:         CommunicationReasonContent{Code: "sender_retracted", Text: "obsolete notice"},
			IdempotencyKey: model.NewID().String(),
		}
		result, err := service.Transition(ctx, fixture.scope, messageLifecycleRetract, command)
		if err != nil {
			t.Fatalf("retract Message on split-owner PostgreSQL: %v", err)
		}
		message, deliveries := lifecycleMessageAndDeliveriesForTest(t, fixture, published.MessageID)
		if result.Replayed || result.State != MessageRetracted || message.State != MessageRetracted ||
			result.Version != published.Version+1 || message.Version != result.Version ||
			result.DeliveryChanges != 1 || len(deliveries) != 1 ||
			deliveries[0].State != DeliveryRetracted || result.AuditSeq < 1 {
			t.Fatalf("retract result/carrier = %#v / %#v / %#v", result, message, deliveries)
		}
		if len(communicationRowsForTest(t, fixture, workEventKind)) != beforeEvents+1 ||
			len(communicationRowsForTest(t, fixture, workOutboxKind)) != beforeOutbox+1 ||
			len(communicationRowsForTest(t, fixture, communicationCommandKind)) != beforeReceipts+1 {
			t.Fatal("retract did not commit exactly one event/outbox/receipt")
		}

		// The estate this journey runs on, measured on the rows the journey
		// itself appended.
		assertReceiptFenceACL(t, fixture, backend.dsn, map[string]model.ID{
			communicationCommandTable: receiptFenceRowByScope(t, fixture, messageLifecycleRetractScope),
			workEventTable:            workEventRowByEventID(t, fixture, result.EventID),
		})

		// Idempotent replay: the receipt exists, the read runs under the
		// command's transaction key, and the response is the recorded one.
		replay, err := service.Transition(ctx, fixture.scope, messageLifecycleRetract, command)
		if err != nil {
			t.Fatalf("replay retract on split-owner PostgreSQL: %v", err)
		}
		if !replay.Replayed || replay.CommandID != result.CommandID || replay.EventID != result.EventID ||
			replay.AuditSeq != result.AuditSeq || replay.Version != result.Version ||
			replay.State != result.State {
			t.Fatalf("retract replay diverged: first=%#v replay=%#v", result, replay)
		}
		if len(communicationRowsForTest(t, fixture, workEventKind)) != beforeEvents+1 ||
			len(communicationRowsForTest(t, fixture, workOutboxKind)) != beforeOutbox+1 ||
			len(communicationRowsForTest(t, fixture, communicationCommandKind)) != beforeReceipts+1 {
			t.Fatal("retract replay duplicated a durable effect")
		}
		// Control that the replay really observed the receipt: the same key
		// with a different request is a reuse conflict, which only the receipt's
		// recorded request digest can tell.
		reused := command
		reused.Reason.Text = "a different request under the same key"
		if _, err := service.Transition(ctx, fixture.scope, messageLifecycleRetract, reused); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("idempotency key reuse with a different request = %v, want store conflict", err)
		}
		if len(communicationRowsForTest(t, fixture, communicationCommandKind)) != beforeReceipts+1 {
			t.Fatal("idempotency key reuse appended a receipt")
		}
	})

	t.Run("successor-replay", func(t *testing.T) {
		backend := splitOwnerPostgresBackendForTest(t, "pg-split-owner-successor")
		direct := newDirectNoticeFixtureForBackend(t, backend, AckPolicyNone, 0, true, true, false)
		fixture, service := newDeliveryDispatchSuccessorFixtureFor(
			t, newDeliveryDispatchServiceFixtureFor(t, direct),
		)
		ctx := context.Background()
		failed := deliveryDispatchSuccessorRowsForTest(t, fixture)[0]
		beforeReceipts := len(communicationRowsForTestFixture(fixture.directNoticeFixture, communicationCommandKind))
		command := deliveryDispatchSuccessorCommand{
			PredecessorID: failed.ID, ExpectedVersion: failed.Version,
			SuccessorRoute: dispatchRouteIdentity(failed), IdempotencyKey: model.NewID().String(),
		}
		result, err := service.CreateSuccessor(ctx, fixture.scope, command)
		if err != nil {
			t.Fatalf("create dispatch successor on split-owner PostgreSQL: %v", err)
		}
		rows := deliveryDispatchSuccessorRowsForTest(t, fixture)
		if result.Replayed || len(rows) != 2 || rows[0].State != DispatchSuperseded ||
			rows[1].State != DispatchPending || rows[1].PredecessorID != rows[0].ID ||
			rows[1].DispatchGeneration != 2 || result.SuccessorID != rows[1].ID || result.AuditSeq < 1 {
			t.Fatalf("dispatch successor result=%+v rows=%+v", result, rows)
		}
		if got := len(communicationRowsForTestFixture(
			fixture.directNoticeFixture, communicationCommandKind,
		)); got != beforeReceipts+1 {
			t.Fatalf("command receipts=%d, want %d", got, beforeReceipts+1)
		}
		assertReceiptFenceACL(t, fixture.directNoticeFixture, backend.dsn, map[string]model.ID{
			communicationCommandTable: receiptFenceRowByScope(
				t, fixture.directNoticeFixture, deliveryDispatchSuccessorScope,
			),
			workEventTable: workEventRowByEventID(t, fixture.directNoticeFixture, ""),
		})

		replay, err := service.CreateSuccessor(ctx, fixture.scope, command)
		if err != nil {
			t.Fatalf("replay dispatch successor on split-owner PostgreSQL: %v", err)
		}
		if !replay.Replayed || replay.CommandID != result.CommandID ||
			replay.SuccessorID != result.SuccessorID || replay.AuditSeq != result.AuditSeq ||
			len(deliveryDispatchSuccessorRowsForTest(t, fixture)) != 2 ||
			len(communicationRowsForTestFixture(fixture.directNoticeFixture, communicationCommandKind)) != beforeReceipts+1 {
			t.Fatalf("dispatch successor replay=%+v", replay)
		}
		changed := command
		changed.IdempotencyKey = model.NewID().String()
		if _, err = service.CreateSuccessor(ctx, fixture.scope, changed); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("second key against superseded dispatch = %v, want store conflict", err)
		}
	})

	t.Run("overdue-escalation-first-execution", func(t *testing.T) {
		backend := splitOwnerPostgresBackendForTest(t, "pg-split-owner-escalation")
		ackFixture := newDirectNoticeExactAckFixtureForBackend(t, backend, AckPolicyEachRequired, 1)
		fixture := ackFixture.directNoticeFixture
		ctx := messageLifecycleTestContext(t)
		messageBefore, _ := lifecycleMessageAndDeliveriesForTest(t, fixture, ackFixture.published.MessageID)
		if messageBefore.AckDueAt == nil {
			t.Fatal("overdue escalation fixture has no Ack deadline")
		}
		waitDirectNoticeExactAckDBTime(t, fixture, *messageBefore.AckDueAt)
		overdueAuthorizer := &messageLifecycleTestAuthorizer{
			wantScope: fixture.scope, wantID: ackFixture.published.MessageID, want: messageLifecycleOverdue,
			authority: messageLifecycleAuthorityForTest(
				fixture, CommunicationActorRef{Kind: ActorSystem, Ref: "message-deadline-worker"},
			),
		}
		lifecycle, err := newMessageLifecycleService(fixture.m, overdueAuthorizer, nil)
		if err != nil {
			t.Fatalf("new overdue lifecycle service: %v", err)
		}
		overdueCommand := messageOverdueCommand{
			MessageID: ackFixture.published.MessageID, ExpectedVersion: ackFixture.published.Version,
			IdempotencyKey: model.NewID().String(),
		}
		overdue, err := lifecycle.MaterializeOverdue(ctx, fixture.scope, overdueCommand)
		if err != nil {
			t.Fatalf("materialize escalation origin overdue on split-owner PostgreSQL: %v", err)
		}
		if overdue.Replayed || overdue.ExpiredCount != 1 {
			t.Fatalf("overdue first execution = %#v", overdue)
		}
		assertReceiptFenceACL(t, fixture, backend.dsn, map[string]model.ID{
			communicationCommandTable: receiptFenceRowByScope(t, fixture, messageLifecycleOverdueScope),
			workEventTable:            workEventRowByEventID(t, fixture, overdue.EventID),
		})

		target := RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()}
		createDirectNoticeGrantForTest(
			t, fixture, CommunicationSubjectRef{Kind: SubjectUser, Ref: target.Ref}, true, false,
		)
		backingAgent := model.NewID()
		createDirectNoticeGrantForTest(
			t, fixture, CommunicationSubjectRef{Kind: SubjectAgent, Ref: backingAgent.String()}, false, true,
		)
		actor := CommunicationActorRef{Kind: ActorSystem, Ref: messageDerivedEscalatorRef}
		principal := CommunicationPrincipal{
			System: true, SystemActorRef: messageDerivedEscalatorRef, SystemGrantAgentID: backingAgent,
		}
		authority := messageDerivedAuthorityForTest(
			fixture, actor, principal,
			CommunicationSubjectRef{Kind: SubjectAgent, Ref: backingAgent.String()},
			target, messageDerivedEscalate,
		)
		service, err := newMessageDerivedService(fixture.m, &messageDerivedTestAuthorizer{
			authority: authority, wantScope: fixture.scope, wantID: ackFixture.published.MessageID,
			wantTarget: target, wantAction: messageDerivedEscalate,
		}, nil)
		if err != nil {
			t.Fatalf("new overdue escalation service: %v", err)
		}
		sourceBefore, deliveriesBefore := lifecycleMessageAndDeliveriesForTest(
			t, fixture, ackFixture.published.MessageID,
		)
		beforeMessages := len(communicationRowsForTest(t, fixture, messageKind))
		beforeEvents := len(communicationRowsForTest(t, fixture, workEventKind))
		beforeOutbox := len(communicationRowsForTest(t, fixture, workOutboxKind))
		beforeReceipts := len(communicationRowsForTest(t, fixture, communicationCommandKind))
		command := messageEscalateOverdueCommand{
			MessageID: ackFixture.published.MessageID, ExpectedVersion: overdue.Version,
			OriginEventID: overdue.EventID, Step: 1, Recipient: target,
		}
		// FIRST execution: this is the non-replay path that reads the origin
		// receipt and the origin work event behind the locked source Message.
		result, err := service.EscalateOverdue(ctx, fixture.scope, command)
		if err != nil {
			t.Fatalf("escalate overdue first execution on split-owner PostgreSQL: %v", err)
		}
		successor, deliveries := lifecycleMessageAndDeliveriesForTest(t, fixture, result.MessageID)
		sourceAfter, deliveriesAfter := lifecycleMessageAndDeliveriesForTest(
			t, fixture, ackFixture.published.MessageID,
		)
		if result.Replayed || successor.Kind != MessageSystem || successor.State != MessagePublished ||
			successor.ReplyToID != ackFixture.published.MessageID ||
			successor.OriginEventID != overdue.EventID || successor.AutomationDepth != 1 ||
			len(deliveries) != 1 || deliveries[0].Recipient != target ||
			deliveries[0].State != DeliveryAvailable || result.AuditSeq < 1 {
			t.Fatalf("escalation result/successor = %#v / %#v / %#v", result, successor, deliveries)
		}
		if !canonicalCommunicationValueEqual(sourceBefore, sourceAfter) ||
			!canonicalCommunicationValueEqual(deliveriesBefore, deliveriesAfter) ||
			len(deliveriesAfter) != 1 || deliveriesAfter[0].State != DeliveryExpired {
			t.Fatal("overdue escalation mutated its source Message or original Delivery")
		}
		if len(communicationRowsForTest(t, fixture, messageKind)) != beforeMessages+1 ||
			len(communicationRowsForTest(t, fixture, workEventKind)) != beforeEvents+1 ||
			len(communicationRowsForTest(t, fixture, workOutboxKind)) != beforeOutbox+1 ||
			len(communicationRowsForTest(t, fixture, communicationCommandKind)) != beforeReceipts+1 {
			t.Fatal("overdue escalation did not commit exactly one Message/event/outbox/receipt")
		}
		replay, err := service.EscalateOverdue(ctx, fixture.scope, command)
		if err != nil {
			t.Fatalf("replay overdue escalation on split-owner PostgreSQL: %v", err)
		}
		if !replay.Replayed || replay.CommandID != result.CommandID || replay.MessageID != result.MessageID ||
			replay.DeliveryID != result.DeliveryID || replay.EventID != result.EventID ||
			replay.AuditSeq != result.AuditSeq {
			t.Fatalf("overdue escalation replay diverged: first=%#v replay=%#v", result, replay)
		}
		conflict := command
		conflict.ExpectedVersion++
		if _, err = service.EscalateOverdue(ctx, fixture.scope, conflict); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("overdue escalation origin-step conflict = %v, want store conflict", err)
		}
		if len(communicationRowsForTest(t, fixture, messageKind)) != beforeMessages+1 ||
			len(communicationRowsForTest(t, fixture, workEventKind)) != beforeEvents+1 ||
			len(communicationRowsForTest(t, fixture, workOutboxKind)) != beforeOutbox+1 ||
			len(communicationRowsForTest(t, fixture, communicationCommandKind)) != beforeReceipts+1 {
			t.Fatal("overdue escalation replay/conflict duplicated a durable effect")
		}
		// The origin command itself replays too: its receipt read runs under
		// the lifecycle key, on the same estate.
		overdueReplay, err := lifecycle.MaterializeOverdue(ctx, fixture.scope, overdueCommand)
		if err != nil || !overdueReplay.Replayed || overdueReplay.EventID != overdue.EventID ||
			overdueReplay.CommandID != overdue.CommandID || overdueReplay.AuditSeq != overdue.AuditSeq {
			t.Fatalf("overdue replay = %#v, %v", overdueReplay, err)
		}
		if len(communicationRowsForTest(t, fixture, workEventKind)) != beforeEvents+1 ||
			len(communicationRowsForTest(t, fixture, communicationCommandKind)) != beforeReceipts+1 {
			t.Fatal("overdue replay duplicated a durable effect")
		}
	})
}

// receiptFenceSeed is one hand-appended receipt of a scope in the closed
// fence table, appended the way the product does (INSERT only), plus the
// identity the observer must reproduce exactly.
type receiptFenceSeed struct {
	receipt CommunicationCommandReceipt
	scope   DirectoryScopeRef
}

func seedReceiptFenceReceipt(
	t *testing.T,
	fixture communicationSchemaFixture,
	commandScope string,
	resultKind model.Kind,
	resultID model.ID,
	eventID model.ID,
	seed string,
) receiptFenceSeed {
	t.Helper()
	ctx := context.Background()
	scope := DirectoryScopeRef{TenantID: fixture.tenant, WorkspaceID: fixture.workspace}
	at := communicationSchemaNow()
	actor := workSchemaHash(seed + "-actor")
	idem := workSchemaHash(seed + "-idempotency")
	receipt := CommunicationCommandReceipt{
		AppendOnlyCommunicationEntity: AppendOnlyCommunicationEntity{
			CommunicationEntity: CommunicationEntity{
				ID: model.NewID(), TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID,
				Version: 1, CreatedAt: at,
			},
		},
		CommandID: model.NewID(), ActorFingerprint: actor, CommandScope: commandScope,
		IdempotencyKeyHash: idem, RequestDigest: workSchemaHash(seed + "-request"),
		PlanHash: workSchemaHash(seed + "-plan"), ResultKind: string(resultKind), ResultID: resultID,
		HTTPStatus: 200,
		ResponseProjectionJSON: CommunicationCommandResponseProjection{
			IDs:     map[string]model.ID{"message_id": resultID},
			Version: 2, State: string(MessagePublished),
			Counts: map[string]int64{"delivery_count": 1},
		},
		AuditSeq: 1, AuditHash: workSchemaHash(seed + "-audit"), CompletedAt: at,
	}
	// The receipt insert guard binds event_id to an existing work Event of
	// the same tenant/workspace; a receipt without an Event names none.
	if eventID != "" {
		receipt.EventID = eventID
		receipt.ResponseProjectionJSON.IDs["event_id"] = eventID
	}
	binding, err := CanonicalCommunicationReceiptResponseBinding(receipt)
	if err != nil {
		t.Fatalf("receipt response binding: %v", err)
	}
	digest := sha256.Sum256(binding)
	receipt.ResponseDigest = digest[:]
	record, err := communicationCommandReceiptToRecord(receipt)
	if err != nil {
		t.Fatalf("encode receipt: %v", err)
	}
	for _, column := range []string{
		model.ColID, model.ColTenantID, model.ColVersion, model.ColCreatedAt, model.ColUpdatedAt,
	} {
		delete(record, column)
	}
	if _, err := communicationCreateWithID(
		ctx, fixture.m, fixture.tenant, communicationCommandKind, receipt.ID, record,
	); err != nil {
		t.Fatalf("append receipt row: %v", err)
	}
	return receiptFenceSeed{receipt: receipt, scope: scope}
}

// seedReceiptFenceOverdueEvent appends one overdue work Event of a standalone
// Message, shaped as persistMessageLifecycleEvent shapes it.
func seedReceiptFenceOverdueEvent(
	t *testing.T,
	fixture communicationSchemaFixture,
	messageID model.ID,
	eventID model.ID,
	seed string,
) {
	t.Helper()
	ctx := context.Background()
	payload, err := canonicalJSON(messageLifecycleEventProjection{
		SchemaVersion: 1, Command: messageLifecycleOverdue, MessageID: messageID,
		State: MessagePublished, Version: 2, DeliveryCount: 1, PlanHash: "00",
	})
	if err != nil {
		t.Fatalf("overdue event payload: %v", err)
	}
	now := model.NewTimestamp(communicationSchemaNow()).String()
	if _, err := communicationCreate(ctx, fixture.m, fixture.tenant, workEventKind, model.Record{
		colWorkWorkspaceID: fixture.workspace.String(),
		colEventID:         eventID.String(), colEventAggregateKind: string(messageKind),
		colEventAggregateID: messageID.String(), colEventSeq: int64(1),
		colEventType: messageLifecycleOverdueEvent, colEventActorKind: string(ActorSystem),
		colEventActorRef: "message-deadline-worker", colEventOccurredAt: now,
		colEventPayload: string(payload), colEventPayloadHash: hashBytes(payload),
		colEventCommandID: model.NewID().String(), colEventAuditSeq: int64(1),
		colEventAuditHash: workSchemaHash(seed + "-event-audit"),
	}); err != nil {
		t.Fatalf("append overdue work event: %v", err)
	}
}

// receiptFenceStructuralControls is the backend-independent proof that the
// receipt and origin observers are closed by the transaction's OWN witness
// and by nothing a caller can assert:
//
//   - a receipt observation before its command key is acquired is refused,
//     although an ordinary Get of the same row succeeds (a read is not a
//     fence, and a fence built from the identity is not a fence until its key
//     is witnessed);
//   - a different acquired key, and the WRONG key family computed for the
//     same identity, are refused;
//   - a scope outside the closed table has no fence and is refused;
//   - under the acquired key the observation returns exactly the persisted
//     receipt, and an identity without a receipt reports "not found";
//   - the overdue origin pair is refused without the source Message row lock,
//     still refused after a Get of that Message, and returned behind the real
//     lock; a receipt naming a different Message is refused;
//   - tx.lockRecord refuses both append-only kinds while a mutable kind still
//     locks.
func receiptFenceStructuralControls(t *testing.T, fixture communicationSchemaFixture) {
	t.Helper()
	ctx := context.Background()
	scope := DirectoryScopeRef{TenantID: fixture.tenant, WorkspaceID: fixture.workspace}
	graph := communicationPublishAppendOnlyGraph(t, fixture, "receipt-fence-"+string(fixture.st.Engine()))
	originEventID := model.NewID()
	seedReceiptFenceOverdueEvent(t, fixture, graph.messageID, originEventID, "origin")
	origin := seedReceiptFenceReceipt(
		t, fixture, messageLifecycleOverdueScope, messageKind, graph.messageID, originEventID, "origin",
	)
	// A second standalone Message (a draft is enough for an Event aggregate)
	// whose overdue pair must never be observable behind the first one's lock.
	foreignMessageID := model.NewID()
	if _, err := communicationCreateWithID(ctx, fixture.m, fixture.tenant, messageKind,
		foreignMessageID, communicationStagingMessageRecord(
			fixture.workspace, graph.channelID, foreignMessageID, "receipt-fence-foreign",
		)); err != nil {
		t.Fatalf("create foreign Message: %v", err)
	}
	foreignEventID := model.NewID()
	seedReceiptFenceOverdueEvent(t, fixture, foreignMessageID, foreignEventID, "foreign")
	seedReceiptFenceReceipt(
		t, fixture, messageLifecycleOverdueScope, messageKind, foreignMessageID, foreignEventID, "foreign",
	)
	retract := seedReceiptFenceReceipt(
		t, fixture, messageLifecycleRetractScope, messageKind, graph.messageID, "", "retract",
	)

	requireStructural := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, errCommunicationTransactionUnavailable) {
			t.Fatalf("%s = %v, want the structural refusal", what, err)
		}
	}
	err := fixture.m.mutateCommunication(ctx, scope, func(tx *communicationTx) error {
		repo, err := tx.repo(communicationCommandKind)
		if err != nil {
			return err
		}
		// A read is possible and is not a fence.
		if _, err := repo.Get(ctx, retract.receipt.ID); err != nil {
			return fmt.Errorf("Get of the receipt row: %w", err)
		}
		fence, err := newCommandReceiptFence(
			scope, messageLifecycleRetractScope,
			retract.receipt.ActorFingerprint, retract.receipt.IdempotencyKeyHash,
		)
		if err != nil {
			return err
		}
		if tx.lockedTransactionKey(fence.key) {
			return errors.New("fence key reported acquired before any lock")
		}
		_, _, err = observeCommandReceipt(ctx, tx, fence)
		requireStructural("receipt observation before the command key", err)
		// A key string a caller merely supplies is not a witness either.
		supplied := fence
		supplied.key = "sessions.communication.message.lifecycle:supplied-by-caller"
		_, _, err = observeCommandReceipt(ctx, tx, supplied)
		requireStructural("receipt observation with a supplied key", err)
		// A different acquired key does not close this identity.
		other, err := newCommandReceiptFence(
			scope, messageLifecycleRetractScope,
			retract.receipt.ActorFingerprint, workSchemaHash("some-other-idempotency"),
		)
		if err != nil {
			return err
		}
		if err := tx.lockTransaction(ctx, other.key); err != nil {
			return err
		}
		_, _, err = observeCommandReceipt(ctx, tx, fence)
		requireStructural("receipt observation under a different command key", err)
		// The wrong key FAMILY for the same identity: derived-shaped, acquired,
		// and still not the writer's key for a lifecycle scope.
		wrongFamily := messageDerivedReceiptLockKey(
			scope, messageLifecycleRetractScope, retract.receipt.IdempotencyKeyHash,
		)
		if wrongFamily == fence.key {
			return errors.New("wrong-family key collides with the lifecycle key")
		}
		if err := tx.lockTransaction(ctx, wrongFamily); err != nil {
			return err
		}
		_, _, err = observeCommandReceipt(ctx, tx, fence)
		requireStructural("receipt observation under the wrong key family", err)
		// A scope outside the closed table has no proven fence.
		_, err = newCommandReceiptFence(
			scope, "workflow.communication.ack.observe",
			retract.receipt.ActorFingerprint, retract.receipt.IdempotencyKeyHash,
		)
		requireStructural("receipt fence for an unmapped scope", err)

		// The real key: the observation returns exactly the persisted receipt.
		if err := tx.lockTransaction(ctx, fence.key); err != nil {
			return err
		}
		if !tx.lockedTransactionKey(fence.key) {
			return errors.New("acquired fence key is not witnessed")
		}
		observed, found, err := observeCommandReceipt(ctx, tx, fence)
		if err != nil || !found {
			return fmt.Errorf("receipt observation under the command key = found %t, %v", found, err)
		}
		if observed.ID != retract.receipt.ID || observed.CommandID != retract.receipt.CommandID ||
			observed.CommandScope != messageLifecycleRetractScope ||
			!bytes.Equal(observed.ActorFingerprint, retract.receipt.ActorFingerprint) ||
			!bytes.Equal(observed.IdempotencyKeyHash, retract.receipt.IdempotencyKeyHash) ||
			!bytes.Equal(observed.RequestDigest, retract.receipt.RequestDigest) ||
			!bytes.Equal(observed.ResponseDigest, retract.receipt.ResponseDigest) ||
			observed.TenantID != scope.TenantID || observed.WorkspaceID != scope.WorkspaceID {
			return fmt.Errorf("observed receipt %#v differs from the persisted one", observed)
		}
		// An identity with no receipt, under its own acquired key, is simply absent.
		if _, found, err := observeCommandReceipt(ctx, tx, other); err != nil || found {
			return fmt.Errorf("absent receipt observation = found %t, %v", found, err)
		}
		// The product lookup itself, under the same key, agrees.
		looked, found, err := lookupMessageLifecycleReceipt(
			ctx, tx, scope, messageLifecycleRetractScope, retract.receipt.ActorFingerprint,
			retract.receipt.IdempotencyKeyHash, retract.receipt.RequestDigest,
		)
		if err != nil || !found || looked.ID != retract.receipt.ID {
			return fmt.Errorf("product lifecycle lookup = found %t, %v", found, err)
		}
		if _, _, err := lookupMessageLifecycleReceipt(
			ctx, tx, scope, messageLifecycleRetractScope, retract.receipt.ActorFingerprint,
			retract.receipt.IdempotencyKeyHash, workSchemaHash("another-request"),
		); !errors.Is(err, store.ErrConflict) {
			return fmt.Errorf("product lifecycle lookup with another request = %v, want conflict", err)
		}

		// The overdue origin pair: refused without the source Message lock, and
		// a Get of the Message is not that lock.
		_, err = observeMessageOverdueOrigin(ctx, tx, graph.messageID, originEventID)
		requireStructural("origin observation without the source Message lock", err)
		messages, err := tx.repo(messageKind)
		if err != nil {
			return err
		}
		if _, err := messages.Get(ctx, graph.messageID); err != nil {
			return err
		}
		_, err = observeMessageOverdueOrigin(ctx, tx, graph.messageID, originEventID)
		requireStructural("origin observation after Get of the source Message", err)
		if _, err := tx.lockRecord(ctx, messageKind, graph.messageID); err != nil {
			return err
		}
		pair, err := observeMessageOverdueOrigin(ctx, tx, graph.messageID, originEventID)
		if err != nil {
			return fmt.Errorf("origin observation behind the locked source Message: %w", err)
		}
		if pair.receipt.ID != origin.receipt.ID || pair.receipt.EventID != originEventID ||
			pair.receipt.ResultID != graph.messageID ||
			pair.event.String(colEventID) != originEventID.String() ||
			pair.event.String(colEventType) != messageLifecycleOverdueEvent {
			return fmt.Errorf("origin pair = %#v / %#v", pair.receipt, pair.event)
		}
		// A receipt of another Message behind THIS Message's lock crosses lineage.
		if _, err := observeMessageOverdueOrigin(ctx, tx, graph.messageID, foreignEventID); err == nil ||
			!errors.Is(err, ErrCommunicationEvidenceUnknown) ||
			errors.Is(err, errCommunicationTransactionUnavailable) {
			return fmt.Errorf("origin observation of a foreign Message's event = %v, want lineage refusal", err)
		}
		// An event nobody wrote is unavailable.
		if _, err := observeMessageOverdueOrigin(ctx, tx, graph.messageID, model.NewID()); err == nil ||
			!errors.Is(err, ErrCommunicationEvidenceUnknown) {
			return fmt.Errorf("origin observation of an unknown event = %v, want unavailable", err)
		}

		// The class closure: a row-update lock on either append-only kind is
		// refused at lockRecord itself, while a mutable row still locks.
		_, err = tx.lockRecord(ctx, communicationCommandKind, retract.receipt.ID)
		requireStructural("lockRecord on a command receipt", err)
		events, err := tx.repo(workEventKind)
		if err != nil {
			return err
		}
		eventRows, _, err := events.List(ctx, model.Query{
			Filters: []model.Filter{{Column: colEventID, Op: model.OpEq, Value: originEventID.String()}},
			Limit:   1,
		})
		if err != nil || len(eventRows) != 1 {
			return fmt.Errorf("origin event rows = %d, %v", len(eventRows), err)
		}
		_, err = tx.lockRecord(ctx, workEventKind, model.ID(eventRows[0].String(model.ColID)))
		requireStructural("lockRecord on a work event", err)
		if _, err := tx.lockRecord(ctx, channelKind, graph.channelID); err != nil {
			return fmt.Errorf("lockRecord on a mutable Channel: %w", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("receipt fence structural controls: %v", err)
	}
}

// TestCommunicationReceiptFenceIsProvedAcrossBackends runs the structural
// controls on SQLite and, when available, on single-role PostgreSQL.
func TestCommunicationReceiptFenceIsProvedAcrossBackends(t *testing.T) {
	t.Parallel()

	for _, backend := range communicationSchemaBackends(t) {
		backend := backend
		t.Run(backend.name, func(t *testing.T) {
			t.Parallel()
			receiptFenceStructuralControls(t, communicationOpenFixture(t, backend))
		})
	}
}

// TestCommunicationReceiptFenceIsProvedOnSplitOwnerPostgres runs the same
// structural controls on the split-owner estate, where the application role
// cannot take the row lock the observers replace.
func TestCommunicationReceiptFenceIsProvedOnSplitOwnerPostgres(t *testing.T) {
	t.Parallel()
	requireSplitOwnerPostgresForTest(t)
	backend := splitOwnerPostgresBackendForTest(t, "pg-split-owner-structural")
	receiptFenceStructuralControls(t, communicationOpenFixture(t, backend))
}
