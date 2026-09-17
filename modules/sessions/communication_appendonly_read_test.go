// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// communicationAppendOnlyGraph is one published direct notice persisted at the
// record level: a Message that left draft, its single Delivery, its single
// MessageAudience and the direct MessageAudienceRecipient arc between them.
type communicationAppendOnlyGraph struct {
	channelID, messageID, deliveryID, audienceID, contributionID model.ID
	recipientID                                                  model.ID
	audienceRecord, contributionRecord                           model.Record
}

func communicationPublishAppendOnlyGraph(
	t *testing.T,
	fixture communicationSchemaFixture,
	seed string,
) communicationAppendOnlyGraph {
	t.Helper()
	ctx := context.Background()
	now := model.NewTimestamp(communicationSchemaNow()).String()
	channel := communicationMustCreate(t, fixture, channelKind,
		communicationChannelRecord(fixture.workspace, seed))
	graph := communicationAppendOnlyGraph{
		channelID: model.ID(channel.String(model.ColID)), messageID: model.NewID(),
		deliveryID: model.NewID(), audienceID: model.NewID(), recipientID: model.NewID(),
	}
	staging, err := communicationCreateWithID(ctx, fixture.m, fixture.tenant, messageKind,
		graph.messageID, communicationStagingMessageRecord(
			fixture.workspace, graph.channelID, graph.messageID, seed,
		))
	if err != nil {
		t.Fatalf("create draft Message: %v", err)
	}
	if _, err := communicationCreateWithID(ctx, fixture.m, fixture.tenant, messageDeliveryKind,
		graph.deliveryID, model.Record{
			colWorkWorkspaceID: fixture.workspace.String(), colCommMessageID: graph.messageID.String(),
			colCommRecipientKind: string(RecipientUser), colCommRecipientRef: graph.recipientID.String(),
			colCommRecipientEpoch: int64(1), colCommDeliverySeq: int64(1),
			colCommRequired: false, colCommRouteReasonsJSON: `["direct"]`,
			colCommWakePolicy: string(WakeNone), colCommState: string(DeliveryAvailable),
			colCommAvailableAt: now,
		}); err != nil {
		t.Fatalf("create MessageDelivery: %v", err)
	}
	selector := AudienceSelector{Kind: AudienceUser, Ref: graph.recipientID.String(), WakePolicy: WakeNone}
	selectorBytes, err := canonicalJSON(selector)
	if err != nil {
		t.Fatalf("canonical audience selector: %v", err)
	}
	selectorHash := sha256.Sum256(selectorBytes)
	graph.audienceRecord = model.Record{
		colWorkWorkspaceID: fixture.workspace.String(), colCommMessageID: graph.messageID.String(),
		colCommOrdinal: int64(1), colCommSelectorKind: string(AudienceUser),
		colCommSelectorRef:      graph.recipientID.String(),
		colCommSelectorRequired: false, colCommSelectorWakePolicy: string(WakeNone),
		colCommChannelACLRevision: int64(1), colCommRouteRevision: int64(1),
		colCommSubscriptionRevision: int64(1), colCommDirectoryEpoch: int64(1),
		colCommDirectorySnapshotAt: now, colCommResolvedCount: int64(1),
		colCommSelectorHash: selectorHash[:],
		colCommResolvedHash: workSchemaHash(seed + "-resolution"),
	}
	if _, err := communicationCreateWithID(ctx, fixture.m, fixture.tenant, messageAudienceKind,
		graph.audienceID, graph.audienceRecord); err != nil {
		t.Fatalf("create MessageAudience: %v", err)
	}
	arc := MessageAudienceRecipient{
		AppendOnlyCommunicationEntity: AppendOnlyCommunicationEntity{CommunicationEntity: CommunicationEntity{
			ID: model.NewID(), TenantID: fixture.tenant, WorkspaceID: fixture.workspace,
			Version: 1, CreatedAt: communicationSchemaNow(),
		}},
		MessageAudienceID: graph.audienceID, MessageDeliveryID: graph.deliveryID,
		Recipient:      RecipientRef{Kind: RecipientUser, Ref: graph.recipientID.String()},
		RecipientEpoch: 1, WakePolicy: WakeNone, RouteReasons: []RouteReason{"direct"},
		Selector:       selector,
		DirectoryEpoch: 1, ChannelACLRevision: 1, RouteRevision: 1, SubscriptionRevision: 1,
		CausalKind: CausalDirect, CausalRef: graph.recipientID.String(),
	}
	hash, err := CanonicalAudienceCausalArcHash(arc)
	if err != nil {
		t.Fatalf("canonical direct causal arc: %v", err)
	}
	graph.contributionRecord = model.Record{
		colWorkWorkspaceID:       fixture.workspace.String(),
		colCommMessageAudienceID: graph.audienceID.String(), colCommMessageDeliveryID: graph.deliveryID.String(),
		colCommRecipientKind: string(RecipientUser), colCommRecipientRef: graph.recipientID.String(),
		colCommRecipientEpoch: int64(1), colCommRequired: false,
		colCommWakePolicy: string(WakeNone), colCommRouteReasonsJSON: `["direct"]`,
		colCommSelectorKind: string(AudienceUser), colCommSelectorRef: graph.recipientID.String(),
		colCommSelectorRequired: false, colCommSelectorWakePolicy: string(WakeNone),
		colCommDirectoryEpoch: int64(1), colCommChannelACLRevision: int64(1),
		colCommRouteRevision: int64(1), colCommSubscriptionRevision: int64(1),
		colCommCausalKind: string(CausalDirect), colCommCausalRef: graph.recipientID.String(),
		colCommCausalArcHash: hash,
	}
	created, err := communicationCreate(ctx, fixture.m, fixture.tenant,
		messageAudienceRecipientKind, graph.contributionRecord)
	if err != nil {
		t.Fatalf("create direct MessageAudienceRecipient: %v", err)
	}
	graph.contributionID = model.ID(created.String(model.ColID))

	published := workSchemaClone(staging)
	published[colCommState] = string(MessagePublished)
	published[colCommPublishedAt] = now
	published[colCommAudienceHash] = workSchemaHash(seed + "-audience")
	published[colCommLastEventSeq] = int64(1)
	if _, err := communicationUpdate(ctx, fixture.m, fixture.tenant, messageKind, published); err != nil {
		t.Fatalf("publish Message: %v", err)
	}
	return graph
}

// communicationAcknowledgeAppendOnlyGraph appends the one MessageAck the
// Delivery may ever carry, the way the product does: the mutable Delivery moves
// to acknowledged naming the Ack, then the append-only Ack row is inserted.
func communicationAcknowledgeAppendOnlyGraph(
	t *testing.T,
	fixture communicationSchemaFixture,
	graph communicationAppendOnlyGraph,
) model.ID {
	t.Helper()
	ctx := context.Background()
	now := model.NewTimestamp(communicationSchemaNow()).String()
	ackID := model.NewID()
	var delivery model.Record
	if err := fixture.m.data.View(ctx, fixture.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(messageDeliveryKind)
		if err != nil {
			return err
		}
		delivery, err = repo.Get(ctx, graph.deliveryID)
		return err
	}); err != nil {
		t.Fatalf("read Delivery before Ack: %v", err)
	}
	acknowledged := workSchemaClone(delivery)
	acknowledged[colCommState] = string(DeliveryAcknowledged)
	acknowledged[colCommAckID] = ackID.String()
	acknowledged[colCommAcknowledgedAt] = now
	if _, err := communicationUpdate(ctx, fixture.m, fixture.tenant, messageDeliveryKind, acknowledged); err != nil {
		t.Fatalf("acknowledge Delivery: %v", err)
	}
	if _, err := communicationCreateWithID(ctx, fixture.m, fixture.tenant, messageAckKind, ackID, model.Record{
		colWorkWorkspaceID: fixture.workspace.String(), colCommDeliveryID: graph.deliveryID.String(),
		colCommAckKind: string(MessageAckReceived), colCommActorKind: string(ActorUser),
		colCommActorRef: graph.recipientID.String(), colCommAcknowledgedAt: now, colCommLate: false,
	}); err != nil {
		t.Fatalf("append MessageAck: %v", err)
	}
	return ackID
}

func communicationAppendOnlyRecordDigest(records []model.Record) string {
	hasher := sha256.New()
	for _, record := range records {
		hasher.Write([]byte(record.String(model.ColID)))
		hasher.Write([]byte{0})
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

func communicationRequireTransactionUnavailable(t *testing.T, err error, what string) {
	t.Helper()
	if !errors.Is(err, errCommunicationTransactionUnavailable) {
		t.Fatalf("%s = %v, want the transaction-unavailable refusal", what, err)
	}
}

func TestCommunicationAppendOnlySetsAreObservedBehindTheirLockedParentAcrossBackends(t *testing.T) {
	t.Parallel()

	for _, backend := range communicationSchemaBackends(t) {
		backend := backend
		t.Run(backend.name, func(t *testing.T) {
			fixture := communicationOpenFixture(t, backend)
			ctx := context.Background()
			graph := communicationPublishAppendOnlyGraph(t, fixture, "append-only-observe")
			ackID := communicationAcknowledgeAppendOnlyGraph(t, fixture, graph)
			draftID := model.NewID()
			if _, err := communicationCreateWithID(ctx, fixture.m, fixture.tenant, messageKind,
				draftID, communicationStagingMessageRecord(
					fixture.workspace, graph.channelID, draftID, "append-only-draft",
				)); err != nil {
				t.Fatalf("create second draft Message: %v", err)
			}
			scope := DirectoryScopeRef{TenantID: fixture.tenant, WorkspaceID: fixture.workspace}
			audienceFilter := []model.Filter{{
				Column: colCommMessageID, Op: model.OpEq, Value: graph.messageID.String(),
			}}
			ackFilter := []model.Filter{{
				Column: colCommDeliveryID, Op: model.OpEq, Value: graph.deliveryID.String(),
			}}

			err := fixture.m.mutateCommunication(ctx, scope, func(tx *communicationTx) error {
				// A read is not a fence: Get and List never register a lock.
				messageRepo, err := tx.repo(messageKind)
				if err != nil {
					return err
				}
				if _, err := messageRepo.Get(ctx, graph.messageID); err != nil {
					return err
				}
				_, err = observeAppendOnlyRecordSet(
					ctx, tx, messageAudienceKind, publishedMessageSetFence(graph.messageID),
					audienceFilter, 64,
				)
				communicationRequireTransactionUnavailable(t, err, "audience observation after Get only")

				// The set lockers refuse append-only descriptors outright.
				_, err = lockDirectNoticeRecordSet(ctx, tx, messageAudienceKind, audienceFilter, 64)
				communicationRequireTransactionUnavailable(t, err, "row-update lock of append-only audience set")
				_, err = lockDirectNoticeBatchRecordSets(ctx, tx, messageAudienceRecipientKind,
					[]directNoticeReadSetSpec{{OwnerID: graph.messageID, Bound: 1}})
				communicationRequireTransactionUnavailable(t, err, "row-update batch lock of append-only contribution set")

				// The observer refuses mutable descriptors and unknown fences.
				_, err = observeAppendOnlyRecordSet(
					ctx, tx, messageDeliveryKind, publishedMessageSetFence(graph.messageID),
					audienceFilter, 64,
				)
				communicationRequireTransactionUnavailable(t, err, "append-only observation of mutable Delivery set")
				_, err = observeAppendOnlyRecordSet(
					ctx, tx, decisionResponseKind, publishedMessageSetFence(graph.messageID),
					audienceFilter, 64,
				)
				communicationRequireTransactionUnavailable(t, err, "append-only observation without a proven fence")

				// The real fence: the Message row lock, observed outside draft.
				if _, err := tx.lockRecord(ctx, messageKind, graph.messageID); err != nil {
					return err
				}
				audiences, err := observeAppendOnlyRecordSet(
					ctx, tx, messageAudienceKind, publishedMessageSetFence(graph.messageID),
					audienceFilter, 64,
				)
				if err != nil {
					return fmt.Errorf("audience observation behind locked published Message: %w", err)
				}
				if len(audiences) != 1 || audiences[0].String(model.ColID) != graph.audienceID.String() {
					return fmt.Errorf("audience set = %d rows, want the one persisted audience", len(audiences))
				}
				audience, err := messageAudienceFromRecord(audiences[0])
				if err != nil || audience.MessageID != graph.messageID {
					return fmt.Errorf("observed audience does not decode to its Message: %v", err)
				}
				contributions, err := observeAppendOnlyContributionSet(
					ctx, tx, publishedMessageSetFence(graph.messageID), []MessageAudience{audience},
				)
				if err != nil {
					return fmt.Errorf("contribution observation behind locked published Message: %w", err)
				}
				if len(contributions) != 1 || contributions[0].String(model.ColID) != graph.contributionID.String() {
					return fmt.Errorf("contribution set = %d rows, want the one persisted arc", len(contributions))
				}
				contribution, err := messageAudienceRecipientFromRecord(contributions[0])
				if err != nil || contribution.MessageAudienceID != graph.audienceID ||
					contribution.MessageDeliveryID != graph.deliveryID {
					return fmt.Errorf("observed contribution does not decode to its audience: %v", err)
				}
				batch, err := observeAppendOnlyBatchRecordSets(ctx, tx, messageAudienceRecipientKind,
					[]directNoticeReadSetSpec{{
						OwnerID: graph.messageID, Bound: directNoticeReadSetBound,
						Queries: [][]model.Filter{{{
							Column: colCommMessageAudienceID, Op: model.OpEq, Value: graph.audienceID.String(),
						}}},
					}})
				if err != nil || len(batch[graph.messageID]) != 1 {
					return fmt.Errorf("batch contribution observation = %d rows, %v; want one", len(batch[graph.messageID]), err)
				}

				// An audience of a Message this transaction did not lock is not
				// covered by the fence of the one it did.
				foreign := MessageAudience{
					AppendOnlyCommunicationEntity: audience.AppendOnlyCommunicationEntity,
					MessageID:                     draftID,
				}
				_, err = observeAppendOnlyContributionSet(
					ctx, tx, publishedMessageSetFence(graph.messageID), []MessageAudience{foreign},
				)
				if !errors.Is(err, ErrCommunicationEvidenceUnknown) {
					return fmt.Errorf("contribution observation outside the fenced Message = %v, want unknown evidence", err)
				}

				// A locked Message still in draft leaves its audience set open, so
				// the observer refuses to treat it as closed.
				if _, err := tx.lockRecord(ctx, messageKind, draftID); err != nil {
					return err
				}
				_, err = observeAppendOnlyRecordSet(
					ctx, tx, messageAudienceKind, publishedMessageSetFence(draftID),
					[]model.Filter{{Column: colCommMessageID, Op: model.OpEq, Value: draftID.String()}}, 64,
				)
				if !errors.Is(err, ErrCommunicationEvidenceUnknown) ||
					!strings.Contains(err.Error(), "still open") {
					return fmt.Errorf("audience observation behind a locked DRAFT Message = %v, want still-open refusal", err)
				}

				// The Ack set has its own fence. The locked, published Message is
				// not it: an Ack arrives long after publication.
				_, err = observeAppendOnlyRecordSet(
					ctx, tx, messageAckKind, publishedMessageSetFence(graph.messageID), ackFilter, 1,
				)
				communicationRequireTransactionUnavailable(t, err, "Ack observation behind the Message fence")
				_, err = observeAppendOnlyRecordSet(
					ctx, tx, messageAckKind, lockedDeliverySetFence(graph.deliveryID), ackFilter, 1,
				)
				communicationRequireTransactionUnavailable(t, err, "Ack observation before the Delivery row lock")
				deliveries, err := lockDirectNoticeRecordSet(
					ctx, tx, messageDeliveryKind, audienceFilter, directNoticeReadSetBound,
				)
				if err != nil || len(deliveries) != 1 {
					return fmt.Errorf("mutable Delivery set lock = %d rows, %v; want one", len(deliveries), err)
				}
				acks, err := observeAppendOnlyRecordSet(
					ctx, tx, messageAckKind, lockedDeliverySetFence(graph.deliveryID), ackFilter, 1,
				)
				if err != nil {
					return fmt.Errorf("Ack observation behind the locked Delivery: %w", err)
				}
				if len(acks) != 1 || acks[0].String(model.ColID) != ackID.String() {
					return fmt.Errorf("Ack set = %d rows, want the one appended Ack", len(acks))
				}
				ack, err := messageAckFromRecord(acks[0])
				if err != nil || ack.DeliveryID != graph.deliveryID {
					return fmt.Errorf("observed Ack does not decode to its Delivery: %v", err)
				}
				return nil
			})
			if err != nil {
				t.Fatalf("append-only observation on %s: %v", backend.name, err)
			}
		})
	}
}

// communicationSplitOwnerAppendOnlyTables are the K3 append-only tables the
// authorization graphs observe; their exact ACL posture is what this file
// proves the read path can live within.
var communicationSplitOwnerAppendOnlyTables = []string{
	messageAudienceTable, messageAudienceRecipientTable, messageAckTable,
}

// communicationRequireSQLState matches the SQLSTATE pgx renders into every
// server error ("... (SQLSTATE 42501)"), so the test needs no driver import.
func communicationRequireSQLState(t *testing.T, err error, state, what string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "(SQLSTATE "+state+")") {
		t.Fatalf("%s = %v, want SQLSTATE %s", what, err, state)
	}
}

func TestCommunicationAppendOnlyReadOnSplitOwnerPostgres(t *testing.T) {
	t.Parallel()

	if !enginetest.PostgresAvailable(t) {
		t.Skipf("%s unset: split-owner PostgreSQL append-only read NOT exercised",
			enginetest.EnvSuperuserDSN)
	}
	ctx := context.Background()
	pg := enginetest.IsolatedPostgresSplitOwner(t)
	m := New()
	st, err := engine.Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner, Debug: true,
		Clock: &testClock{now: communicationSchemaNow()},
	}, m.RegisterSchema)
	if err != nil {
		t.Fatalf("open split-owner PostgreSQL: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, err := sys.CreateOrg(ctx, model.Org{
			Name: "Append-only read", Slug: "append-only-read", Status: model.StatusActive,
		})
		if err == nil {
			tenant = org.TenantID
		}
		return err
	}); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	m.UseData(api.NewModuleData(st))
	m.UseCommunicationGuardReconciliationData(
		NewCommunicationGuardReconciliationData(api.NewModuleData(st)),
	)
	var workspace model.ID
	if err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		value, err := sc.DefaultWorkspace(ctx)
		if err == nil {
			workspace = value.ID
		}
		return err
	}); err != nil {
		t.Fatalf("default workspace: %v", err)
	}
	fixture := communicationSchemaFixture{m: m, st: st, tenant: tenant, workspace: workspace}

	// The application role's effective privileges, read on the application
	// pool: SELECT and INSERT held, UPDATE/DELETE/TRUNCATE absent, on all three.
	app, err := sql.Open("pgx", pg.App)
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
	for _, table := range communicationSplitOwnerAppendOnlyTables {
		var canSelect, canInsert, canUpdate, canDelete, canTruncate bool
		if err := app.QueryRowContext(ctx, `SELECT
pg_catalog.has_table_privilege($1, 'SELECT'), pg_catalog.has_table_privilege($1, 'INSERT'),
pg_catalog.has_table_privilege($1, 'UPDATE'), pg_catalog.has_table_privilege($1, 'DELETE'),
pg_catalog.has_table_privilege($1, 'TRUNCATE')`, table).Scan(
			&canSelect, &canInsert, &canUpdate, &canDelete, &canTruncate); err != nil {
			t.Fatalf("read %s privileges: %v", table, err)
		}
		t.Logf("K3_APPEND_ONLY_ACL|role=%s|table=%s|select=%t|insert=%t|update=%t|delete=%t|truncate=%t",
			appRole, table, canSelect, canInsert, canUpdate, canDelete, canTruncate)
		if !canSelect || !canInsert || canUpdate || canDelete || canTruncate {
			t.Fatalf("%s privileges select=%t insert=%t update=%t delete=%t truncate=%t, want SELECT+INSERT only",
				table, canSelect, canInsert, canUpdate, canDelete, canTruncate)
		}
	}

	graph := communicationPublishAppendOnlyGraph(t, fixture, "split-owner-read")
	ackID := communicationAcknowledgeAppendOnlyGraph(t, fixture, graph)

	// The retained cause, reproduced on purpose against the disposable
	// database: a row-update lock on append-only evidence is exactly what the
	// application role may not take, and so are UPDATE and DELETE themselves.
	for table, id := range map[string]model.ID{
		messageAudienceTable:          graph.audienceID,
		messageAudienceRecipientTable: graph.contributionID,
		messageAckTable:               ackID,
	} {
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
			// Bind the tenant exactly as the application pool does, so the
			// row-level security policy answers and the privilege check is the
			// only thing left to refuse.
			if _, err := tx.ExecContext(ctx,
				"SELECT pg_catalog.set_config('app.tenant_id', $1, true)", tenant.String()); err != nil {
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
			"SELECT pg_catalog.set_config('app.tenant_id', $1, true)", tenant.String()); err != nil {
			t.Fatal(err)
		}
		var selected string
		err = tx.QueryRowContext(ctx, "SELECT id FROM "+table+" WHERE id = $1", id.String()).Scan(&selected)
		_ = tx.Rollback()
		if err != nil || selected != id.String() {
			t.Fatalf("plain SELECT of %s by the application role = %q, %v", table, selected, err)
		}
	}

	// Publication closed the audience graph: late appends are refused by the
	// schema even though the application role holds INSERT.
	lateAudience := workSchemaClone(graph.audienceRecord)
	lateAudience[colCommOrdinal] = int64(2)
	lateAudience[colCommSelectorHash] = workSchemaHash("late-selector")
	lateAudience[colCommResolvedHash] = workSchemaHash("late-resolution")
	if _, err := communicationCreateWithID(ctx, fixture.m, fixture.tenant,
		messageAudienceKind, model.NewID(), lateAudience); err == nil {
		t.Fatal("MessageAudience was appended after its Message was published")
	}
	lateContribution := workSchemaClone(graph.contributionRecord)
	if _, err := communicationCreate(ctx, fixture.m, fixture.tenant,
		messageAudienceRecipientKind, lateContribution); err == nil {
		t.Fatal("MessageAudienceRecipient was appended after its Message was published")
	}

	// The product read: the application role, holding only SELECT and INSERT
	// on the evidence tables, authorizes the published graph behind the locked
	// Message and Delivery fences. This is the statement the HTTP journey
	// failed on before the correction.
	scope := DirectoryScopeRef{TenantID: tenant, WorkspaceID: workspace}
	audienceFilter := []model.Filter{{
		Column: colCommMessageID, Op: model.OpEq, Value: graph.messageID.String(),
	}}
	observe := func(tx *communicationTx) (string, error) {
		audiences, err := observeAppendOnlyRecordSet(
			ctx, tx, messageAudienceKind, publishedMessageSetFence(graph.messageID), audienceFilter, 64,
		)
		if err != nil {
			return "", err
		}
		decoded := make([]MessageAudience, 0, len(audiences))
		for _, record := range audiences {
			audience, err := messageAudienceFromRecord(record)
			if err != nil {
				return "", err
			}
			decoded = append(decoded, audience)
		}
		contributions, err := observeAppendOnlyContributionSet(
			ctx, tx, publishedMessageSetFence(graph.messageID), decoded,
		)
		if err != nil {
			return "", err
		}
		if len(audiences) != 1 || len(contributions) != 1 {
			return "", fmt.Errorf("graph = %d audiences, %d contributions; want one each",
				len(audiences), len(contributions))
		}
		return communicationAppendOnlyRecordDigest(audiences) + "/" +
			communicationAppendOnlyRecordDigest(contributions), nil
	}
	if err := fixture.m.mutateCommunication(ctx, scope, func(tx *communicationTx) error {
		if _, err := tx.lockRecord(ctx, messageKind, graph.messageID); err != nil {
			return err
		}
		if _, err := observe(tx); err != nil {
			return err
		}
		deliveries, err := lockDirectNoticeRecordSet(
			ctx, tx, messageDeliveryKind, audienceFilter, directNoticeReadSetBound,
		)
		if err != nil || len(deliveries) != 1 {
			return fmt.Errorf("Delivery set lock = %d rows, %v", len(deliveries), err)
		}
		acks, err := observeAppendOnlyRecordSet(
			ctx, tx, messageAckKind, lockedDeliverySetFence(graph.deliveryID),
			[]model.Filter{{Column: colCommDeliveryID, Op: model.OpEq, Value: graph.deliveryID.String()}}, 1,
		)
		if err != nil || len(acks) != 1 || acks[0].String(model.ColID) != ackID.String() {
			return fmt.Errorf("Ack set behind the locked Delivery = %d rows, %v", len(acks), err)
		}
		return nil
	}); err != nil {
		t.Fatalf("application-role authorization read of the published graph: %v", err)
	}

	// Two independent actors under the fence. On this Community line the
	// lineage writer takes the tenant's exclusive L1 advisory gate before every
	// non-system Mutate callback on PostgreSQL (core/internal/store/sqlstore/
	// lineage_writer.go, key core.lineage.writer.tenant.v8:<tenant>), so a
	// second same-tenant Mutate cannot even begin its callback while the fence
	// transaction is open. That gate is preserved as it is, and it is NOT taken
	// as proof of the Message row fence: the second actor never enters Mutate —
	// it is the application role, tenant GUC bound, asking for the Message row
	// FOR UPDATE — so its wait can only be the row/transaction lock this
	// transaction holds. Both waits are observed in pg_locks against the same
	// owner, the graph is re-read unchanged, the fence transaction commits, and
	// only after that may the schema refuse the append (SQLSTATE 23514) and the
	// reader complete. A deadline anywhere is a failure, never a refusal.
	lateID := model.NewID()
	late := workSchemaClone(graph.audienceRecord)
	late[colCommOrdinal] = int64(3)
	late[colCommSelectorHash] = workSchemaHash("concurrent-late-selector")
	late[colCommResolvedHash] = workSchemaHash("concurrent-late-resolution")
	communicationRequireLateAudienceTargetsPublishedMessage(ctx, t, app, tenant, graph.messageID, late)

	probeCtx, cancelProbe := context.WithTimeout(ctx, 30*time.Second)
	defer cancelProbe()
	var (
		fenceHeld    = make(chan struct{})
		openFence    sync.Once
		signalFence  = func() { openFence.Do(func() { close(fenceHeld) }) }
		actors       sync.WaitGroup
		appendResult = make(chan error, 1)
		readerResult = make(chan error, 1)
		readerPID    = make(chan int, 1)
	)
	// Every exit, including a failed setup, releases both actors: the deadline
	// is cancelled, the gate is opened so a goroutine waiting on it observes the
	// cancellation, and both are awaited before the pools close (LIFO defers).
	defer func() {
		cancelProbe()
		signalFence()
		actors.Wait()
	}()
	actors.Add(2)
	go func() {
		// Actor A: the product's own append path, a normal Mutate.
		defer actors.Done()
		select {
		case <-fenceHeld:
		case <-probeCtx.Done():
			appendResult <- fmt.Errorf("append actor was never released: %w", probeCtx.Err())
			return
		}
		_, err := communicationCreateWithID(probeCtx, fixture.m, fixture.tenant,
			messageAudienceKind, lateID, late)
		appendResult <- err
	}()
	go func() {
		// Actor B: the application role reading the Message row FOR UPDATE on a
		// raw connection with the tenant GUC bound. No write, no Mutate, no gate.
		defer actors.Done()
		select {
		case <-fenceHeld:
		case <-probeCtx.Done():
			readerResult <- fmt.Errorf("reader actor was never released: %w", probeCtx.Err())
			return
		}
		readerResult <- communicationLockMessageRowAsApplication(probeCtx, app, tenant, graph.messageID, readerPID)
	}()

	var beforeDigest string
	var appendWaiterPID, readerWaiterPID, holderPID int
	if err := fixture.m.mutateCommunication(probeCtx, scope, func(tx *communicationTx) error {
		if _, err := tx.lockRecord(probeCtx, messageKind, graph.messageID); err != nil {
			return err
		}
		var err error
		if beforeDigest, err = observe(tx); err != nil {
			return err
		}
		signalFence()
		// 1. The append is waiting, ungranted, on this tenant's exact L1 key,
		//    and pg_blocking_pids names one granted holder.
		appendWaiterPID, holderPID, err = communicationAwaitAdvisoryGateWait(
			probeCtx, app, communicationLineageTenantGateKey(tenant), appendResult)
		if err != nil {
			return fmt.Errorf("tenant lineage gate wait: %w", err)
		}
		t.Logf("K3_APPEND_ONLY_TENANT_GATE_WAIT|waiter=%d|holder=%d|key=core.lineage.writer.tenant.v8:<fixture-tenant>|granted=false",
			appendWaiterPID, holderPID)
		// 2. Separately, the raw reader is waiting on a transaction/tuple lock
		//    held by that same owner: the Message row fence, not the tenant gate.
		select {
		case readerWaiterPID = <-readerPID:
		case err := <-readerResult:
			return fmt.Errorf("Message row reader concluded before taking its lock: %v", err)
		case <-probeCtx.Done():
			return fmt.Errorf("Message row reader never started: %w", probeCtx.Err())
		}
		if err := communicationAwaitRowLockWait(probeCtx, app, readerWaiterPID, holderPID, readerResult); err != nil {
			return fmt.Errorf("Message row wait: %w", err)
		}
		t.Logf("K3_APPEND_ONLY_MESSAGE_ROW_WAIT|waiter=%d|holder=%d|lock=transactionid_or_tuple|role=application|tenant_guc=bound",
			readerWaiterPID, holderPID)
		if appendWaiterPID == readerWaiterPID || appendWaiterPID == holderPID || readerWaiterPID == holderPID {
			return fmt.Errorf("actors are not distinct backends: append=%d reader=%d holder=%d",
				appendWaiterPID, readerWaiterPID, holderPID)
		}
		// 3. Neither actor has concluded, and the graph re-reads identical under
		//    the fence that is still held.
		select {
		case err := <-appendResult:
			return fmt.Errorf("append concluded while the fence was held: %v", err)
		default:
		}
		select {
		case err := <-readerResult:
			return fmt.Errorf("Message row reader concluded while the fence was held: %v", err)
		default:
		}
		afterDigest, err := observe(tx)
		if err != nil {
			return err
		}
		if afterDigest != beforeDigest {
			return fmt.Errorf("audience graph digest changed under the fence: %s -> %s", beforeDigest, afterDigest)
		}
		t.Log("K3_APPEND_ONLY_FENCE_RELEASE|graph_unchanged=true|both_actors_waiting=true|committing=true")
		// 4. Real release: returning nil commits this transaction.
		return nil
	}); err != nil {
		t.Fatalf("fenced enumeration with independent waiting actors: %v", err)
	}

	// 5. After the release the append is refused by the schema — exactly 23514,
	//    not a deadline, a cancellation or a missing privilege — and the reader
	//    completes with the exact Message.
	select {
	case err := <-appendResult:
		communicationRequireSQLState(t, err, "23514", "late MessageAudience append after the fence released")
		t.Logf("K3_APPEND_ONLY_APPEND_AFTER_RELEASE|sqlstate=23514|error=%v", err)
	case <-probeCtx.Done():
		t.Fatalf("late append did not conclude after the fence released: %v", probeCtx.Err())
	}
	select {
	case err := <-readerResult:
		if err != nil {
			t.Fatalf("application-role Message row read after the fence released: %v", err)
		}
		t.Log("K3_APPEND_ONLY_MESSAGE_ROW_AFTER_RELEASE|exact_message=true")
	case <-probeCtx.Done():
		t.Fatalf("Message row read did not conclude after the fence released: %v", probeCtx.Err())
	}
	// No new row exists, and the graph behind a fresh fence is the same one.
	communicationRequireNoAudienceRow(probeCtx, t, app, tenant, lateID)
	if err := fixture.m.mutateCommunication(probeCtx, scope, func(tx *communicationTx) error {
		if _, err := tx.lockRecord(probeCtx, messageKind, graph.messageID); err != nil {
			return err
		}
		afterDigest, err := observe(tx)
		if err != nil {
			return err
		}
		if afterDigest != beforeDigest {
			return fmt.Errorf("audience graph digest changed after the refused append: %s -> %s",
				beforeDigest, afterDigest)
		}
		return nil
	}); err != nil {
		t.Fatalf("graph after the refused append: %v", err)
	}
	t.Log("K3_APPEND_ONLY_FINAL|new_audience_rows=0|audiences=1|contributions=1|digest_unchanged=true")
}

// communicationLineageTenantGateKey is the advisory key the current Community
// line's lineage writer takes exclusively for a tenant before every non-system
// Mutate callback on PostgreSQL (core/internal/store/sqlstore/lineage_writer.go,
// lineageTenantKeyPrefix). The prefix is repeated here because that package is
// internal to core; if it ever moves, the wait below is never observed and the
// test fails at its deadline instead of passing by accident.
func communicationLineageTenantGateKey(tenant model.TenantID) string {
	return "core.lineage.writer.tenant.v8:" + tenant.String()
}

// communicationRequireLateAudienceTargetsPublishedMessage proves that the late
// MessageAudience row names the fenced Message, that Message's workspace and
// the fixture tenant, and that the Message has left draft. The PostgreSQL
// validate trigger reports a crossed tenant/workspace reference and a
// non-draft parent with one message, so a 23514 is only evidence of the
// publication closure once every other cause has been excluded here.
func communicationRequireLateAudienceTargetsPublishedMessage(
	ctx context.Context, t *testing.T, app *sql.DB, tenant model.TenantID,
	messageID model.ID, late model.Record,
) {
	t.Helper()
	if got := late.String(colCommMessageID); got != messageID.String() {
		t.Fatalf("late audience names Message %q, want the fenced %s", got, messageID)
	}
	tx, err := app.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx,
		"SELECT pg_catalog.set_config('app.tenant_id', $1, true)", tenant.String()); err != nil {
		t.Fatal(err)
	}
	var state, tenantID, workspaceID string
	if err := tx.QueryRowContext(ctx,
		"SELECT "+colCommState+", "+model.ColTenantID+", "+colWorkWorkspaceID+
			" FROM "+messageTable+" WHERE "+model.ColID+" = $1", messageID.String(),
	).Scan(&state, &tenantID, &workspaceID); err != nil {
		t.Fatalf("read the fenced Message as the application role: %v", err)
	}
	if state != string(MessagePublished) {
		t.Fatalf("fenced Message state = %q, want %q", state, MessagePublished)
	}
	if tenantID != tenant.String() || workspaceID != late.String(colWorkWorkspaceID) {
		t.Fatalf("late audience tenant/workspace = %s/%s, Message = %s/%s: the refusal would not be the publication closure",
			tenant, late.String(colWorkWorkspaceID), tenantID, workspaceID)
	}
}

// communicationLockMessageRowAsApplication takes the Message row FOR UPDATE on
// a raw application-role transaction bound to the tenant GUC, reports its
// backend pid first, and commits once the row is granted. It performs no write
// and never enters the module's Mutate path, so the only thing it can wait on
// is the row/transaction lock of whoever holds that Message.
func communicationLockMessageRowAsApplication(
	ctx context.Context, app *sql.DB, tenant model.TenantID, messageID model.ID, pidOut chan<- int,
) error {
	raw, err := app.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer raw.Rollback() //nolint:errcheck
	if _, err := raw.ExecContext(ctx,
		"SELECT pg_catalog.set_config('app.tenant_id', $1, true)", tenant.String()); err != nil {
		return err
	}
	var pid int
	if err := raw.QueryRowContext(ctx, "SELECT pg_catalog.pg_backend_pid()").Scan(&pid); err != nil {
		return err
	}
	select {
	case pidOut <- pid:
	default:
		return errors.New("Message row reader pid was not consumed")
	}
	var found string
	if err := raw.QueryRowContext(ctx,
		"SELECT "+model.ColID+" FROM "+messageTable+" WHERE "+model.ColID+" = $1 FOR UPDATE",
		messageID.String()).Scan(&found); err != nil {
		return err
	}
	if found != messageID.String() {
		return fmt.Errorf("Message row lock returned %q, want %s", found, messageID)
	}
	return raw.Commit()
}

// communicationAwaitAdvisoryGateWait polls pg_locks until one backend waits,
// ungranted, on the exclusive advisory lock whose two 32-bit halves equal
// hashtextextended(key, 0), with a granted holder that pg_blocking_pids names
// for that waiter. It returns both pids. A result on concluded before the wait
// is seen, or the deadline, is an error: elapsed time is never a witness.
func communicationAwaitAdvisoryGateWait(
	ctx context.Context, db *sql.DB, key string, concluded <-chan error,
) (waiter, holder int, err error) {
	const query = `SELECT w.pid, h.pid
FROM pg_catalog.pg_locks w
JOIN pg_catalog.pg_locks h
  ON h.locktype = w.locktype AND h.database = w.database
 AND h.classid = w.classid AND h.objid = w.objid AND h.objsubid = w.objsubid
WHERE w.locktype = 'advisory' AND NOT w.granted AND w.mode = 'ExclusiveLock'
  AND h.granted AND h.mode = 'ExclusiveLock'
  AND w.database = (SELECT oid FROM pg_catalog.pg_database WHERE datname = current_database())
  AND h.pid = ANY (pg_catalog.pg_blocking_pids(w.pid))
  AND w.classid::bigint = ((pg_catalog.hashtextextended($1, 0) >> 32) & 4294967295)
  AND w.objid::bigint = (pg_catalog.hashtextextended($1, 0) & 4294967295)`
	for {
		err = db.QueryRowContext(ctx, query, key).Scan(&waiter, &holder)
		if err == nil {
			return waiter, holder, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, 0, err
		}
		select {
		case result := <-concluded:
			return 0, 0, fmt.Errorf("actor concluded before its advisory gate wait was observed: %v", result)
		case <-ctx.Done():
			return 0, 0, fmt.Errorf("advisory gate wait never observed: %w", ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// communicationAwaitRowLockWait polls pg_locks until the given backend waits,
// ungranted, on a transactionid or tuple lock and pg_blocking_pids names holder
// as a blocker of it. The advisory gate is excluded on purpose: only a
// row-level wait proves the Message fence.
func communicationAwaitRowLockWait(
	ctx context.Context, db *sql.DB, waiter, holder int, concluded <-chan error,
) error {
	const query = `SELECT count(*) FROM pg_catalog.pg_locks
WHERE pid = $1 AND NOT granted AND locktype IN ('transactionid', 'tuple')
  AND $2 = ANY (pg_catalog.pg_blocking_pids(pid))`
	for {
		var waiting int
		if err := db.QueryRowContext(ctx, query, waiter, holder).Scan(&waiting); err != nil {
			return err
		}
		if waiting > 0 {
			return nil
		}
		select {
		case result := <-concluded:
			return fmt.Errorf("actor concluded before its row lock wait was observed: %v", result)
		case <-ctx.Done():
			return fmt.Errorf("row lock wait never observed: %w", ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// communicationRequireNoAudienceRow proves, as the application role bound to
// the tenant, that no MessageAudience row with the given id exists.
func communicationRequireNoAudienceRow(
	ctx context.Context, t *testing.T, app *sql.DB, tenant model.TenantID, id model.ID,
) {
	t.Helper()
	tx, err := app.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx,
		"SELECT pg_catalog.set_config('app.tenant_id', $1, true)", tenant.String()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := tx.QueryRowContext(ctx,
		"SELECT count(*) FROM "+messageAudienceTable+" WHERE "+model.ColID+" = $1", id.String(),
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("refused append persisted %d MessageAudience row(s)", count)
	}
}
