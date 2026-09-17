// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The independent review's first standing limit was that the recipient scan's
// index was pinned STRUCTURALLY and never measured: "que el orden de columnas del
// índice exista y pase el control estructural no prueba el plan elegido".
//
// This measures it, on both engines, with a fixture where a table scan would be
// visibly wrong: the recipient's own offered rows are a few dozen among thousands
// that belong to other recipients, other states and another workspace.
//
// The statement under EXPLAIN is not a paraphrase. genericRepo.List renders
// exactly `SELECT <descriptor columns> FROM <table> WHERE tenant_id = ? AND
// <filters…> ORDER BY <sort>, id ASC LIMIT <limit+1>`, and workspace confinement
// appends its own equality predicate, so the test rebuilds that string from the
// SAME descriptor and then PROVES the rebuild by requiring it to return exactly
// the ids, in exactly the order, that the confined repository returns for the
// same query. A statement that answers differently is not the one to explain.
const (
	incomingHandoffPlanOwnOffers  = 24
	incomingHandoffPlanTieGroup   = 8
	incomingHandoffPlanForeign    = 1500
	incomingHandoffPlanOtherState = 300
	incomingHandoffPlanOtherSpace = 200
	incomingHandoffPlanScanLimit  = 128
)

func TestIncomingHandoffCandidateScanIsAnIndexRangeOnBothEngines(t *testing.T) {
	for _, backend := range communicationSchemaBackends(t) {
		backend := backend
		t.Run(backend.name, func(t *testing.T) {
			fixture := communicationOpenFixture(t, backend)
			recipient := RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()}
			tie := seedIncomingHandoffPlanFixture(t, fixture, recipient)

			descriptor := communicationDescriptor(t, communicationCaptureSchema(t), handoffKind)
			columns := strings.Join(descriptor.AllColumns(), ", ")
			base := []model.Filter{
				{Column: colCommToKind, Op: model.OpEq, Value: string(recipient.Kind)},
				{Column: colCommToRef, Op: model.OpEq, Value: recipient.Ref},
				{Column: colCommState, Op: model.OpEq, Value: string(HandoffOffered)},
			}
			anchor := incomingHandoffAnchor{deadline: tie.deadline, handoffID: tie.id}

			plans := []struct {
				name    string
				query   model.Query
				explain string
				args    []any
			}{
				{
					name: "range after the anchor deadline",
					query: model.Query{
						Filters: append(append([]model.Filter(nil), base...), model.Filter{
							Column: colCommAckDeadline, Op: model.OpGt, Value: anchor.deadline,
						}),
						Sort:  []model.Sort{{Column: colCommAckDeadline}},
						Limit: incomingHandoffPlanScanLimit,
					},
				},
				{
					name: "remainder of the anchor tie group",
					query: model.Query{
						Filters: append(append([]model.Filter(nil), base...),
							model.Filter{
								Column: colCommAckDeadline, Op: model.OpEq, Value: anchor.deadline,
							},
							model.Filter{
								Column: model.ColID, Op: model.OpGt, Value: anchor.handoffID.String(),
							},
						),
						Limit: incomingHandoffPlanScanLimit,
					},
				},
			}

			for index := range plans {
				plan := &plans[index]
				t.Run(plan.name, func(t *testing.T) {
					wantIDs := confinedIncomingHandoffPlanRows(t, fixture, plan.query)
					if len(wantIDs) == 0 {
						t.Fatal("the fixture returned no rows, so nothing meaningful is planned")
					}
					statement, args := renderIncomingHandoffPlanStatement(
						columns, descriptor.Table, fixture.workspace, plan.query,
					)
					db := openIncomingHandoffPlanDatabase(t, backend, fixture.tenant)
					gotIDs := incomingHandoffPlanRows(
						t, db, backend, fixture.tenant, statement, args)
					if strings.Join(gotIDs, ",") != strings.Join(wantIDs, ",") {
						t.Fatalf(
							"the explained statement is not the repository's:\n got %v\nwant %v",
							gotIDs, wantIDs,
						)
					}
					explained := incomingHandoffExplain(
						t, db, backend, fixture.tenant, statement, args)
					t.Logf("K3_HANDOFF_PLAN engine=%s case=%q rows=%d\n%s",
						backend.name, plan.name, len(wantIDs), explained)
					if !strings.Contains(explained, "sessions_work_handoff_target") {
						t.Fatalf("the plan does not use sessions_work_handoff_target:\n%s", explained)
					}
					switch backend.engineName {
					case store.EngineSQLite:
						// SQLite says SEARCH for an index range and SCAN for a table walk.
						if !strings.Contains(explained, "SEARCH") ||
							strings.Contains(explained, "SCAN sessions_work_handoff") {
							t.Fatalf("SQLite did not choose an index range:\n%s", explained)
						}
					case store.EnginePostgres:
						if strings.Contains(explained, "Seq Scan on sessions_work_handoff") {
							t.Fatalf("PostgreSQL chose a sequential scan:\n%s", explained)
						}
					}
				})
			}
		})
	}
}

type incomingHandoffPlanAnchor struct {
	id       model.ID
	deadline string
}

// seedIncomingHandoffPlanFixture writes COMPLETE offer graphs — WorkItem, its
// context WorkEvent, a published handoff_offer Message with its audience and
// contribution, the required Delivery and the Handoff — so every row passes the
// database's own lineage guards. Nothing is faked: the guards would reject a
// bare Handoff row, which is exactly why a plan fixture has to build the graph.
//
// A few dozen rows belong to the recipient under test, including one tie group
// that shares a single deadline, among hundreds that belong to another recipient
// and to a terminal state. The workspace column is NOT varied: a Channel and its
// Messages must share one workspace, so a second workspace would need a second
// Channel and a real Workspace row, and the column's contribution to this plan is
// structural (it is part of the index prefix) rather than selective — every row a
// recipient can reach lives in the workspace the request names.
func seedIncomingHandoffPlanFixture(
	t *testing.T,
	fixture communicationSchemaFixture,
	recipient RecipientRef,
) incomingHandoffPlanAnchor {
	t.Helper()
	// The engine stamps its rows from the DATABASE clock, so the fixture's own
	// instants are anchored to real time: a terminal row dated three weeks ago
	// would sit before its own stamped creation and the guards would refuse it.
	at := time.Now().UTC().Add(-time.Minute)
	other := RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()}
	channel := incomingHandoffPlanChannel(t, fixture)

	// delivery_seq is unique per (tenant, workspace), exactly as the product's own
	// publication assigns it, so the seeder keeps one running sequence per space.
	sequence := map[model.ID]int64{}
	write := func(to RecipientRef, workspace model.ID, state HandoffState, deadline time.Time) model.ID {
		sequence[workspace]++
		return incomingHandoffPlanOffer(
			t, fixture, channel, to, workspace, state, at, deadline, sequence[workspace],
		)
	}

	for i := 0; i < incomingHandoffPlanOwnOffers; i++ {
		write(recipient, fixture.workspace, HandoffOffered, at.Add(time.Duration(i+1)*time.Minute))
	}
	// One shared deadline in the MIDDLE of the recipient's own range, so both
	// statements have something to walk: the tie remainder, and everything after.
	tieDeadline := at.Add(time.Duration(incomingHandoffPlanOwnOffers/2) * time.Minute)
	tieIDs := make([]model.ID, 0, incomingHandoffPlanTieGroup)
	for i := 0; i < incomingHandoffPlanTieGroup; i++ {
		tieIDs = append(tieIDs, write(recipient, fixture.workspace, HandoffOffered, tieDeadline))
	}
	lowest := tieIDs[0]
	for _, id := range tieIDs {
		if id.String() < lowest.String() {
			lowest = id
		}
	}
	for i := 0; i < incomingHandoffPlanForeign; i++ {
		write(other, fixture.workspace, HandoffOffered, at.Add(time.Duration(i+1)*time.Second))
	}
	for i := 0; i < incomingHandoffPlanOtherState; i++ {
		write(recipient, fixture.workspace, HandoffRejected, at.Add(time.Duration(i+1)*time.Second))
	}
	return incomingHandoffPlanAnchor{
		id: lowest, deadline: model.NewTimestamp(tieDeadline).String(),
	}
}

func incomingHandoffPlanChannel(t *testing.T, fixture communicationSchemaFixture) Channel {
	t.Helper()
	id := model.NewID()
	record, err := communicationCreateWithID(
		context.Background(), fixture.m, fixture.tenant, channelKind, id,
		communicationChannelRecord(fixture.workspace, "handoff-plan"),
	)
	if err != nil {
		t.Fatalf("create the plan fixture Channel: %v", err)
	}
	channel, err := channelFromRecord(record)
	if err != nil {
		t.Fatalf("decode the plan fixture Channel: %v", err)
	}
	return channel
}

// incomingHandoffPlanOffer writes one complete offer graph and returns its
// Handoff id. The row shapes are the ones the handoff service fixture already
// proves the guards accept.
func incomingHandoffPlanOffer(
	t *testing.T,
	fixture communicationSchemaFixture,
	channel Channel,
	to RecipientRef,
	workspace model.ID,
	state HandoffState,
	at time.Time,
	deadline time.Time,
	deliverySeq int64,
) model.ID {
	t.Helper()
	ctx := context.Background()
	// One transaction per offer graph. The guards are per-statement triggers, so
	// batching changes nothing they check and keeps a fixture of this size cheap.
	var scope store.Scope
	create := func(kind model.Kind, id model.ID, record model.Record) model.Record {
		repo, err := scope.Ext(kind)
		if err != nil {
			t.Fatalf("open the plan fixture %s repository: %v", kind, err)
		}
		stored, err := repo.CreateWithID(ctx, id, record)
		if err != nil {
			t.Fatalf("write the plan fixture %s: %v", kind, err)
		}
		return stored
	}
	update := func(kind model.Kind, record model.Record) {
		repo, err := scope.Ext(kind)
		if err != nil {
			t.Fatalf("open the plan fixture %s repository: %v", kind, err)
		}
		if _, err := repo.Update(ctx, record); err != nil {
			t.Fatalf("update the plan fixture %s: %v", kind, err)
		}
	}
	var written model.ID
	if err := fixture.m.data.Mutate(ctx, fixture.tenant, func(sc store.Scope) error {
		scope = sc
		written = writeIncomingHandoffPlanGraph(
			t, create, update, fixture.tenant, channel, to, workspace, state, at, deadline,
			deliverySeq,
		)
		return nil
	}); err != nil {
		t.Fatalf("write the plan fixture offer graph: %v", err)
	}
	return written
}

func writeIncomingHandoffPlanGraph(
	t *testing.T,
	create func(model.Kind, model.ID, model.Record) model.Record,
	update func(model.Kind, model.Record),
	tenant model.TenantID,
	channel Channel,
	to RecipientRef,
	workspace model.ID,
	state HandoffState,
	at time.Time,
	deadline time.Time,
	deliverySeq int64,
) model.ID {
	t.Helper()
	workID := model.NewID()
	item := workSchemaItem(workspace, "plan fixture")
	item[colWorkLastEventSeq] = int64(1)
	create(workItemKind, workID, item)
	event := workSchemaEvent(workspace, workID.String(), model.NewID().String(), 1, "plan")
	create(workEventKind, model.NewID(), event)

	messageID, audienceID := model.NewID(), model.NewID()
	deliveryID, contributionID := model.NewID(), model.NewID()
	mutable := func(id model.ID) MutableCommunicationEntity {
		return MutableCommunicationEntity{CommunicationEntity: CommunicationEntity{
			ID: id, TenantID: tenant, WorkspaceID: workspace, Version: 1, CreatedAt: at,
		}, UpdatedAt: at}
	}
	appendOnly := func(id model.ID) AppendOnlyCommunicationEntity {
		return AppendOnlyCommunicationEntity{CommunicationEntity: CommunicationEntity{
			ID: id, TenantID: tenant, WorkspaceID: workspace, Version: 1, CreatedAt: at,
		}}
	}
	expires := deadline.Add(time.Minute)
	message := Message{
		MutableCommunicationEntity: mutable(messageID),
		ChannelID:                  channel.ID, WorkItemID: workID, ThreadID: messageID,
		Kind: MessageHandoffOffer, State: MessageDraft,
		Sender:  CommunicationActorRef{Kind: ActorUser, Ref: model.NewID().String()},
		Payload: communicationTestPayloadForSlot(t, PayloadSlotMessage),
		Urgency: UrgencyNormal, AckPolicy: AckPolicyEachRequired,
		AvailableAt: at, AckDueAt: &deadline, ExpiresAt: &expires,
	}
	audienceKind, err := recipientAudienceKind(to)
	if err != nil {
		t.Fatalf("plan fixture audience kind: %v", err)
	}
	selector := AudienceSelector{
		Kind: audienceKind, Ref: to.Ref, Required: true, WakePolicy: WakeNone,
	}
	selectorRaw, err := canonicalJSON(selector)
	if err != nil {
		t.Fatalf("plan fixture selector: %v", err)
	}
	selectorHash := sha256.Sum256(selectorRaw)
	audience := MessageAudience{
		AppendOnlyCommunicationEntity: appendOnly(audienceID), MessageID: messageID,
		Ordinal: 1, Selector: selector, ChannelACLRevision: channel.ACLRevision,
		RouteRevision: channel.RouteRevision, SubscriptionRevision: channel.SubscriptionRevision,
		DirectoryEpoch: 1, DirectorySnapshotAt: at, ResolvedCount: 1,
		SelectorHash: selectorHash[:], ResolvedHash: make([]byte, sha256.Size),
	}
	delivery := MessageDelivery{
		MutableCommunicationEntity: mutable(deliveryID), MessageID: messageID,
		Recipient: to, RecipientEpoch: 1, DeliverySeq: deliverySeq, Required: true,
		RouteReasons: []RouteReason{"direct"}, WakePolicy: WakeNone,
		State: DeliveryAvailable, AvailableAt: at, AckDueAt: &deadline, ExpiresAt: &expires,
	}
	contribution := communicationStateTestSealCausalArc(MessageAudienceRecipient{
		AppendOnlyCommunicationEntity: appendOnly(contributionID),
		MessageAudienceID:             audienceID, MessageDeliveryID: deliveryID,
		Recipient: to, RecipientEpoch: 1, Required: true, WakePolicy: WakeNone,
		RouteReasons: []RouteReason{"direct"}, Selector: selector, DirectoryEpoch: 1,
		ChannelACLRevision: channel.ACLRevision, RouteRevision: channel.RouteRevision,
		SubscriptionRevision: channel.SubscriptionRevision,
		CausalKind:           CausalDirect, CausalRef: to.Ref,
	})
	audience.ResolvedHash, err = canonicalResolvedAudienceHash(
		audience, []MessageAudienceRecipient{contribution},
	)
	if err != nil {
		t.Fatalf("plan fixture resolved audience hash: %v", err)
	}
	draft, err := messageToRecord(message, 1)
	if err != nil {
		t.Fatalf("encode the plan fixture Message: %v", err)
	}
	storedMessage := create(messageKind, messageID, draft)
	audienceRecord, err := messageAudienceToRecord(audience)
	if err != nil {
		t.Fatalf("encode the plan fixture audience: %v", err)
	}
	storedAudience := create(messageAudienceKind, audienceID, audienceRecord)
	deliveryRecord, err := messageDeliveryToRecord(delivery)
	if err != nil {
		t.Fatalf("encode the plan fixture Delivery: %v", err)
	}
	create(messageDeliveryKind, deliveryID, deliveryRecord)
	contributionRecord, err := messageAudienceRecipientToRecord(contribution)
	if err != nil {
		t.Fatalf("encode the plan fixture contribution: %v", err)
	}
	storedContribution := create(messageAudienceRecipientKind, contributionID, contributionRecord)

	// Publish from the row as the ENGINE stored it: the update guard requires
	// every immutable column to be byte-identical, and the audience seal must
	// cover the stored rows, not the in-memory drafts.
	decodedMessage, err := messageFromRecord(storedMessage, 1)
	if err != nil {
		t.Fatalf("decode the stored plan fixture Message: %v", err)
	}
	decodedAudience, err := messageAudienceFromRecord(storedAudience)
	if err != nil {
		t.Fatalf("decode the stored plan fixture audience: %v", err)
	}
	decodedContribution, err := messageAudienceRecipientFromRecord(storedContribution)
	if err != nil {
		t.Fatalf("decode the stored plan fixture contribution: %v", err)
	}
	audienceHash, err := CanonicalMessageAudienceHash(
		decodedMessage, []MessageAudience{decodedAudience},
		[]MessageAudienceRecipient{decodedContribution},
	)
	if err != nil {
		t.Fatalf("plan fixture audience hash: %v", err)
	}
	publishedAt := decodedMessage.UpdatedAt
	decodedMessage.State = MessagePublished
	decodedMessage.PublishedAt = &publishedAt
	decodedMessage.AudienceHash = audienceHash
	publishedRecord, err := messageToRecord(decodedMessage, 1)
	if err != nil {
		t.Fatalf("encode the published plan fixture Message: %v", err)
	}
	update(messageKind, publishedRecord)

	handoffID := model.NewID()
	handoff := Handoff{
		MutableCommunicationEntity: mutable(handoffID),
		WorkItemID:                 workID, MessageID: messageID, DeliveryID: deliveryID,
		From:           RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()},
		FromOwnerEpoch: 1, To: to, ContextEventSeq: 1,
		Payload: communicationTestPayloadForSlot(t, PayloadSlotHandoff),
		State:   HandoffOffered, AckDeadline: deadline,
	}
	handoff.ContextHash, err = CanonicalHandoffContextHash(handoff)
	if err != nil {
		t.Fatalf("plan fixture context hash: %v", err)
	}
	record, err := handoffToRecord(handoff)
	if err != nil {
		t.Fatalf("encode the plan fixture Handoff: %v", err)
	}
	storedHandoff := create(handoffKind, handoffID, record)
	if state == HandoffOffered {
		return handoffID
	}
	// A terminal row is REACHED, not written: its terminal instant has to sit
	// inside the created/updated window the ENGINE stamped, which only the stored
	// row can report.
	decodedHandoff, err := handoffFromRecord(storedHandoff)
	if err != nil {
		t.Fatalf("decode the stored plan fixture Handoff: %v", err)
	}
	terminal := decodedHandoff.CreatedAt
	decodedHandoff.State = HandoffRejected
	decodedHandoff.RejectedAt = &terminal
	decodedHandoff.TerminalCode = "plan_fixture"
	reason := communicationTestPayloadForSlot(t, PayloadSlotHandoffTerminalReason)
	decodedHandoff.TerminalReason = &reason
	terminalRecord, err := handoffToRecord(decodedHandoff)
	if err != nil {
		t.Fatalf("encode the terminal plan fixture Handoff: %v", err)
	}
	update(handoffKind, terminalRecord)
	return handoffID
}

// confinedIncomingHandoffPlanRows runs the query through the SAME confined
// repository the service uses, so the explained statement can be held to its
// answer instead of being trusted.
func confinedIncomingHandoffPlanRows(
	t *testing.T,
	fixture communicationSchemaFixture,
	query model.Query,
) []string {
	t.Helper()
	ctx := context.Background()
	var ids []string
	if err := fixture.m.data.View(ctx, fixture.tenant, func(raw store.Scope) error {
		confined, err := store.ConfineWorkspace(ctx, raw, fixture.workspace)
		if err != nil {
			return err
		}
		repo, err := confined.Ext(handoffKind)
		if err != nil {
			return err
		}
		rows, _, err := repo.List(ctx, query)
		if err != nil {
			return err
		}
		for _, row := range rows {
			ids = append(ids, row.String(model.ColID))
		}
		return nil
	}); err != nil {
		t.Fatalf("read the confined candidate rows: %v", err)
	}
	return ids
}

// renderIncomingHandoffPlanStatement rebuilds genericRepo.List's statement for a
// confined scope: the tenant predicate first, the caller's filters in order, the
// forced workspace equality last, the declared sort with id appended, and
// LIMIT limit+1.
func renderIncomingHandoffPlanStatement(
	columns string,
	table string,
	workspace model.ID,
	query model.Query,
) (string, []any) {
	where := []string{"tenant_id = ?"}
	args := []any{}
	for _, filter := range query.Filters {
		operator := map[model.Op]string{
			model.OpEq: "=", model.OpGt: ">", model.OpLt: "<",
			model.OpGte: ">=", model.OpLte: "<=",
		}[filter.Op]
		where = append(where, filter.Column+" "+operator+" ?")
		args = append(args, filter.Value)
	}
	where = append(where, colWorkWorkspaceID+" = ?")
	args = append(args, workspace.String())
	order := make([]string, 0, len(query.Sort)+1)
	for _, sort := range query.Sort {
		order = append(order, sort.Column+" ASC")
	}
	order = append(order, "id ASC")
	return fmt.Sprintf("SELECT %s FROM %s WHERE %s ORDER BY %s LIMIT %d",
		columns, table, strings.Join(where, " AND "), strings.Join(order, ", "),
		query.Limit+1), args
}

func openIncomingHandoffPlanDatabase(
	t *testing.T,
	backend communicationSchemaBackend,
	tenant model.TenantID,
) *sql.DB {
	t.Helper()
	driver := "sqlite"
	if backend.engineName == store.EnginePostgres {
		driver = "pgx"
	}
	db, err := sql.Open(driver, backend.dsn)
	if err != nil {
		t.Fatalf("open the %s plan connection: %v", backend.name, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	switch backend.engineName {
	case store.EngineSQLite:
		if _, err := db.ExecContext(ctx, "ANALYZE"); err != nil {
			t.Fatalf("analyze the SQLite fixture: %v", err)
		}
	case store.EnginePostgres:
		// The application role reads under row-level security, exactly as the
		// product does, so the plan carries the same policy predicate.
		if _, err := db.ExecContext(
			ctx, "SELECT set_config('app.tenant_id',$1,false)", tenant.String(),
		); err != nil {
			t.Fatalf("pin the PostgreSQL plan tenant: %v", err)
		}
		if _, err := db.ExecContext(ctx, "ANALYZE sessions_work_handoff"); err != nil {
			t.Fatalf("analyze the PostgreSQL fixture: %v", err)
		}
	}
	return db
}

func incomingHandoffPlanStatementFor(
	backend communicationSchemaBackend,
	statement string,
	tenant model.TenantID,
) (string, []any) {
	args := []any{tenant.String()}
	if backend.engineName != store.EnginePostgres {
		return statement, args
	}
	var out strings.Builder
	position := 1
	for _, r := range statement {
		if r == '?' {
			fmt.Fprintf(&out, "$%d", position)
			position++
			continue
		}
		out.WriteRune(r)
	}
	return out.String(), args
}

func incomingHandoffPlanRows(
	t *testing.T,
	db *sql.DB,
	backend communicationSchemaBackend,
	tenant model.TenantID,
	statement string,
	args []any,
) []string {
	t.Helper()
	sqlText, bound := incomingHandoffPlanStatementFor(backend, statement, tenant)
	bound = append(bound, args...)
	rows, err := db.QueryContext(context.Background(), sqlText, bound...)
	if err != nil {
		t.Fatalf("run the explained statement: %v\n%s", err, sqlText)
	}
	defer rows.Close() //nolint:errcheck
	columns, err := rows.Columns()
	if err != nil {
		t.Fatalf("read the explained statement columns: %v", err)
	}
	var ids []string
	for rows.Next() {
		cells := make([]any, len(columns))
		holders := make([]sql.NullString, len(columns))
		for i := range cells {
			cells[i] = &holders[i]
		}
		if err := rows.Scan(cells...); err != nil {
			t.Fatalf("scan the explained statement: %v", err)
		}
		for i, name := range columns {
			if name == model.ColID {
				ids = append(ids, holders[i].String)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate the explained statement: %v", err)
	}
	return ids
}

func incomingHandoffExplain(
	t *testing.T,
	db *sql.DB,
	backend communicationSchemaBackend,
	tenant model.TenantID,
	statement string,
	args []any,
) string {
	t.Helper()
	prefix := "EXPLAIN QUERY PLAN "
	if backend.engineName == store.EnginePostgres {
		prefix = "EXPLAIN (ANALYZE, BUFFERS, VERBOSE) "
	}
	sqlText, bound := incomingHandoffPlanStatementFor(backend, prefix+statement, tenant)
	bound = append(bound, args...)
	rows, err := db.QueryContext(context.Background(), sqlText, bound...)
	if err != nil {
		t.Fatalf("explain the statement: %v\n%s", err, sqlText)
	}
	defer rows.Close() //nolint:errcheck
	columns, err := rows.Columns()
	if err != nil {
		t.Fatalf("read the explain columns: %v", err)
	}
	var out strings.Builder
	for rows.Next() {
		cells := make([]any, len(columns))
		holders := make([]sql.NullString, len(columns))
		for i := range cells {
			cells[i] = &holders[i]
		}
		if err := rows.Scan(cells...); err != nil {
			t.Fatalf("scan the explain output: %v", err)
		}
		parts := make([]string, 0, len(columns))
		for i := range columns {
			if holders[i].Valid {
				parts = append(parts, holders[i].String)
			}
		}
		out.WriteString("  " + strings.Join(parts, " | ") + "\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate the explain output: %v", err)
	}
	return out.String()
}
