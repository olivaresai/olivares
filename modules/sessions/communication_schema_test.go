// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type communicationSchemaEntity struct {
	kind       model.Kind
	table      string
	appendOnly bool
}

var communicationSchemaEntities = []communicationSchemaEntity{
	{channelKind, channelTable, false},
	{channelGrantKind, channelGrantTable, false},
	{channelSubscriptionKind, channelSubscriptionTable, false},
	{channelLabelDefinitionKind, channelLabelDefinitionTable, false},
	{channelRouteKind, channelRouteTable, false},
	{communicationEndpointKind, communicationEndpointTable, false},
	{messageKind, messageTable, false},
	{messageAudienceKind, messageAudienceTable, true},
	{messageAudienceRecipientKind, messageAudienceRecipientTable, true},
	{messageDeliveryKind, messageDeliveryTable, false},
	{inboxCursorKind, inboxCursorTable, false},
	{inboxCursorBarrierKind, inboxCursorBarrierTable, false},
	{messageAckKind, messageAckTable, true},
	{communicationGuardKind, communicationGuardTable, false},
	{decisionRequestKind, decisionRequestTable, false},
	{decisionResponseKind, decisionResponseTable, true},
	{handoffKind, handoffTable, false},
	{deliveryDispatchKind, deliveryDispatchTable, false},
	{deliveryAttemptKind, deliveryAttemptTable, false},
	{communicationCommandKind, communicationCommandTable, true},
}

var communicationLegacyDescriptorKinds = map[model.Kind]struct{}{
	liveKind: {}, timelineKind: {}, templateKind: {}, runKind: {}, runEventKind: {},
	identityKind: {}, aliasKind: {}, claimKind: {}, workspaceKind: {},
	workItemKind: {}, workDependencyKind: {}, workAcceptanceKind: {}, workDecisionKind: {},
	workDecisionHeadKind: {}, workLeaseKind: {}, workCommandKind: {}, workEventKind: {},
	workOutboxKind: {}, workGuardKind: {},
}

var protocolBindingDescriptorKinds = map[model.Kind]string{
	protocolBindingSpecKind:        protocolBindingSpecTable,
	protocolBindingKind:            protocolBindingTable,
	protocolInterruptKind:          protocolInterruptTable,
	protocolReplayGuardKind:        protocolReplayGuardTable,
	protocolSubscriptionCursorKind: protocolSubscriptionCursorTable,
	protocolSubscriptionEventKind:  protocolSubscriptionEventTable,
}

type communicationCapturedMigration struct {
	namespace string
	fs        fs.FS
}

type communicationCapturedInvariants struct {
	namespace string
	byEngine  map[store.Engine][]store.SchemaTrigger
}

type communicationSchemaCaptureRegistry struct {
	descriptors  []model.EntityDescriptor
	migrations   []communicationCapturedMigration
	invariants   []communicationCapturedInvariants
	rollouts     []store.RolloutControl
	initializers []store.WorkspaceInitializer
}

func (r *communicationSchemaCaptureRegistry) Register(d model.EntityDescriptor) error {
	r.descriptors = append(r.descriptors, d)
	return nil
}

func (r *communicationSchemaCaptureRegistry) Migrations(namespace string, migrations fs.FS) error {
	r.migrations = append(r.migrations, communicationCapturedMigration{namespace, migrations})
	return nil
}

func (r *communicationSchemaCaptureRegistry) SchemaInvariants(
	namespace string,
	byEngine map[store.Engine][]store.SchemaTrigger,
) error {
	copyByEngine := make(map[store.Engine][]store.SchemaTrigger, len(byEngine))
	for engineName, triggers := range byEngine {
		copyByEngine[engineName] = append([]store.SchemaTrigger(nil), triggers...)
	}
	r.invariants = append(r.invariants, communicationCapturedInvariants{namespace, copyByEngine})
	return nil
}

func (r *communicationSchemaCaptureRegistry) RolloutControl(control store.RolloutControl) error {
	r.rollouts = append(r.rollouts, control)
	return nil
}

func (r *communicationSchemaCaptureRegistry) WorkspaceInitializer(
	initializer store.WorkspaceInitializer,
) error {
	r.initializers = append(r.initializers, initializer)
	return nil
}

func communicationCaptureSchema(t *testing.T) *communicationSchemaCaptureRegistry {
	t.Helper()
	reg := &communicationSchemaCaptureRegistry{}
	if err := New().RegisterSchema(reg); err != nil {
		t.Fatalf("capture sessions schema: %v", err)
	}
	return reg
}

func communicationEntityByKind() map[model.Kind]communicationSchemaEntity {
	entities := make(map[model.Kind]communicationSchemaEntity, len(communicationSchemaEntities))
	for _, entity := range communicationSchemaEntities {
		entities[entity.kind] = entity
	}
	return entities
}

func communicationTables() map[string]bool {
	tables := make(map[string]bool, len(communicationSchemaEntities))
	for _, entity := range communicationSchemaEntities {
		tables[entity.table] = true
	}
	return tables
}

func communicationDescriptorFields(descriptor model.EntityDescriptor) map[string]model.FieldSpec {
	fields := make(map[string]model.FieldSpec, len(descriptor.Fields))
	for _, field := range descriptor.Fields {
		fields[field.Name] = field
	}
	return fields
}

func communicationRequireFields(
	t *testing.T,
	descriptor model.EntityDescriptor,
	names ...string,
) {
	t.Helper()
	fields := communicationDescriptorFields(descriptor)
	for _, name := range names {
		if _, ok := fields[name]; !ok {
			t.Errorf("%s is missing required field %q", descriptor.Kind, name)
		}
	}
}

func TestCommunicationSchemaInventoryIsExactlyTwenty(t *testing.T) {
	t.Parallel()

	reg := communicationCaptureSchema(t)
	want := communicationEntityByKind()
	got := make(map[model.Kind]model.EntityDescriptor, len(want))
	legacySeen := make(map[model.Kind]struct{}, len(communicationLegacyDescriptorKinds))
	protocolSeen := make(map[model.Kind]struct{}, len(protocolBindingDescriptorKinds))
	tables := make(map[string]model.Kind, len(want))
	for _, descriptor := range reg.descriptors {
		entity, isCommunication := want[descriptor.Kind]
		if !isCommunication {
			if table, protocol := protocolBindingDescriptorKinds[descriptor.Kind]; protocol {
				if _, duplicate := protocolSeen[descriptor.Kind]; duplicate {
					t.Fatalf("K5 protocol binding kind %s registered more than once", descriptor.Kind)
				}
				if descriptor.Table != table {
					t.Fatalf("K5 protocol binding kind %s table = %q, want %q", descriptor.Kind, descriptor.Table, table)
				}
				protocolSeen[descriptor.Kind] = struct{}{}
				continue
			}
			if _, legacy := communicationLegacyDescriptorKinds[descriptor.Kind]; !legacy {
				t.Fatalf("unexpected sessions descriptor outside the K1/K2 + exact-20 K3 + K5 manifests: %s", descriptor.Kind)
			}
			if _, duplicate := legacySeen[descriptor.Kind]; duplicate {
				t.Fatalf("legacy sessions kind %s registered more than once", descriptor.Kind)
			}
			legacySeen[descriptor.Kind] = struct{}{}
			continue
		}
		if _, duplicate := got[descriptor.Kind]; duplicate {
			t.Fatalf("communication kind %s registered more than once", descriptor.Kind)
		}
		if previous, duplicate := tables[descriptor.Table]; duplicate {
			t.Fatalf("communication table %s is shared by %s and %s", descriptor.Table, previous, descriptor.Kind)
		}
		got[descriptor.Kind] = descriptor
		tables[descriptor.Table] = descriptor.Kind
		if descriptor.Table != entity.table {
			t.Errorf("%s table = %q, want %q", descriptor.Kind, descriptor.Table, entity.table)
		}
		if descriptor.AppendOnly != entity.appendOnly {
			t.Errorf("%s AppendOnly = %v, want %v", descriptor.Kind, descriptor.AppendOnly, entity.appendOnly)
		}
		if descriptor.SoftDelete {
			t.Errorf("%s unexpectedly uses soft delete", descriptor.Kind)
		}
		if !entity.appendOnly && !descriptor.RetainOnTenantDrop {
			t.Errorf("mutable communication descriptor %s is not retained on tenant drop", descriptor.Kind)
		}
		if descriptor.WorkspaceLineage != hiddenWorkspaceLineage {
			t.Errorf("%s workspace lineage = %+v, want %+v", descriptor.Kind, descriptor.WorkspaceLineage, hiddenWorkspaceLineage)
		}
		workspace, ok := communicationDescriptorFields(descriptor)[colWorkWorkspaceID]
		if !ok || workspace.Kind != model.KindUUID || workspace.Nullable {
			t.Errorf("%s workspace_id = %+v, want required UUID", descriptor.Kind, workspace)
		}
	}
	if len(got) != 20 {
		missing := make([]string, 0, len(want)-len(got))
		for kind := range want {
			if _, ok := got[kind]; !ok {
				missing = append(missing, string(kind))
			}
		}
		sort.Strings(missing)
		t.Fatalf("registered communication descriptors = %d, want exactly 20; missing %v", len(got), missing)
	}
	if len(legacySeen) != len(communicationLegacyDescriptorKinds) ||
		len(protocolSeen) != len(protocolBindingDescriptorKinds) ||
		len(reg.descriptors) != len(communicationLegacyDescriptorKinds)+len(communicationSchemaEntities)+len(protocolBindingDescriptorKinds) {
		t.Fatalf("registered sessions descriptors = %d (%d legacy + %d K3 + %d K5), want exactly %d + 20 + 6",
			len(reg.descriptors), len(legacySeen), len(got), len(protocolSeen), len(communicationLegacyDescriptorKinds))
	}

	if got[handoffKind].Table != "sessions_work_handoff" {
		t.Errorf("Handoff table = %q, want existing sessions_work_handoff identity", got[handoffKind].Table)
	}
	communicationRequireFields(t, got[messageKind], colCommLastEventSeq)
	communicationRequireFields(t, got[messageAudienceRecipientKind], colCommCausalArcHash)
	for _, kind := range []model.Kind{messageAudienceKind, messageAudienceRecipientKind} {
		fields := communicationDescriptorFields(got[kind])
		channelACL, ok := fields[colCommChannelACLRevision]
		if !ok || channelACL.Kind != model.KindInt || channelACL.Nullable {
			t.Errorf("%s channel_acl_revision = %+v, want required int", kind, channelACL)
		}
		if _, stale := fields[colCommACLRevision]; stale {
			t.Errorf("%s retained ambiguous acl_revision instead of channel_acl_revision", kind)
		}
	}
	communicationRequireFields(t, got[channelGrantKind], colCommCanRead, colCommCanWrite, colCommCanAdmin)
	communicationRequireFields(t, got[deliveryDispatchKind],
		colCommRootDispatchID, colCommPredecessorID, colCommDispatchGeneration,
		colCommRerouteRung, colCommPolicyGeneration, colCommEndpointID,
		colCommEndpointGeneration, colCommRouteRuleID, colCommRouteRuleGeneration,
		colCommResolutionDeadlineAt, colCommResolutionCode, colCommReconciledAttemptID,
		colCommReconciledEndpointID, colCommReconciledEndpointGeneration,
		colCommReconciliationVerdict, colCommReconciliationCode,
		colCommReconciliationEvidenceRef, colCommReconciliationObservedAt,
		colCommProviderAcceptanceHash,
	)

}

func TestCommunicationSchemaRegistersNamespaceOnce(t *testing.T) {
	t.Parallel()

	reg := communicationCaptureSchema(t)
	if len(reg.migrations) != 1 || reg.migrations[0].namespace != Namespace {
		t.Fatalf("sessions migration registrations = %+v, want exactly one for %q", reg.migrations, Namespace)
	}
	if len(reg.invariants) != 1 || reg.invariants[0].namespace != Namespace {
		t.Fatalf("sessions invariant registrations = %+v, want exactly one for %q", reg.invariants, Namespace)
	}

	wantMigrations := map[string][]string{
		"postgres": {
			"0012_communication_validate_function.sql",
			"0013_communication_validate_triggers.sql",
			"0014_communication_no_delete_function.sql",
			"0015_communication_no_delete_triggers.sql",
			"0016_communication_event_validate_function.sql",
			"0017_communication_event_validate_trigger.sql",
			"0018_communication_command_cursor_projection.sql",
			"0019_protocol_binding_validate_functions.sql",
			"0020_protocol_binding_triggers.sql",
		},
		"sqlite": communicationSQLiteMigrationNames(),
	}
	for engineName, wantNames := range wantMigrations {
		gotNames := communicationMigrationNames(t, reg.migrations[0].fs, engineName)
		gotNames = migrationNamesAfter(gotNames, map[string]int{"postgres": 11, "sqlite": 32}[engineName])
		if !reflect.DeepEqual(gotNames, wantNames) {
			t.Errorf("%s Slice F migrations = %v, want %v", engineName, gotNames, wantNames)
		}
	}

	for _, engineName := range store.SupportedEngines() {
		triggers := reg.invariants[0].byEngine[engineName]
		if len(triggers) == 0 {
			t.Fatalf("%s invariant declaration is empty", engineName)
		}
		seen := make(map[string]bool, len(triggers))
		for _, trigger := range triggers {
			key := trigger.Table + "\x00" + trigger.Name
			if seen[key] {
				t.Errorf("%s invariant (%s,%s) is registered twice", engineName, trigger.Table, trigger.Name)
			}
			seen[key] = true
			workSchemaRequireDigest(t, string(engineName)+" "+trigger.Name, trigger.DefinitionSHA256)
			if trigger.Table != communicationCommandTable {
				continue
			}
			if engineName == store.EnginePostgres {
				if trigger.Name != "sessions_communication_command_guard" ||
					trigger.DefinitionSHA256 !=
						"fdef4326ded0968658859b969e82fd7fbfed5f5ae3501d8c9c7d7eea22f8d567" ||
					len(trigger.Transitions) != 1 ||
					trigger.Transitions[0].MigrationVersion != 18 ||
					trigger.Transitions[0].PreviousDefinitionSHA256 !=
						"93b8463fa70601b2c68318f3572c75e8341aae8753fa681d63cb4722f3bd396a" ||
					trigger.Transitions[0].PostgresFunctionIdentity == nil ||
					trigger.Transitions[0].PostgresFunctionIdentity.PreviousName !=
						"olivares_sessions_communication_validate" ||
					trigger.Transitions[0].PostgresFunctionIdentity.NextName !=
						"olivares_sessions_communication_command_validate_v18" {
					t.Errorf("PostgreSQL CommunicationCommand v18 transition = %+v", trigger)
				}
				continue
			}
			if trigger.Name != "sessions_communication_command_guard_ins" ||
				trigger.DefinitionSHA256 != "7946feb2f27f444cdc85601690df5ce9e19248dd114db9490f1bb7cee4f583f2" ||
				len(trigger.Transitions) != 2 ||
				trigger.Transitions[0].MigrationVersion != 85 ||
				trigger.Transitions[0].PreviousDefinitionSHA256 !=
					"f67652ec1ac04d9a0cc42178a450a5416059578ee13a46854235cca57f67a085" ||
				trigger.Transitions[0].PostgresFunctionIdentity != nil ||
				trigger.Transitions[1].MigrationVersion != 86 ||
				trigger.Transitions[1].PreviousDefinitionSHA256 !=
					"ba6bdd1a2e669b4b54287edf4b1c2423a4b740af317e70c0f9f7c85e26088f40" ||
				trigger.Transitions[1].PostgresFunctionIdentity != nil {
				t.Errorf("SQLite CommunicationCommand v85/v86 transition chain = %+v", trigger)
			}
		}
		workEventName := "sessions_work_event_guard"
		if engineName == store.EngineSQLite {
			workEventName += "_ins"
		}
		if !seen[workEventTable+"\x00"+workEventName] {
			t.Errorf("%s does not declare the dual WorkEvent insert guard", engineName)
		}
		for _, entity := range communicationSchemaEntities {
			guardName := entity.table + "_guard"
			if engineName == store.EngineSQLite {
				guardName += "_ins"
			}
			if !seen[entity.table+"\x00"+guardName] {
				t.Errorf("%s does not declare the guard for %s", engineName, entity.table)
			}
			noDeleteKey := entity.table + "\x00" + entity.table + "_no_delete"
			if entity.appendOnly {
				if seen[noDeleteKey] {
					t.Errorf("%s append-only table %s unexpectedly declares mutable no-delete", engineName, entity.table)
				}
			} else if !seen[noDeleteKey] {
				t.Errorf("%s does not declare no-delete for mutable %s", engineName, entity.table)
			}
			if engineName == store.EngineSQLite {
				updateKey := entity.table + "\x00" + entity.table + "_guard_upd"
				if entity.appendOnly && seen[updateKey] {
					t.Errorf("SQLite append-only table %s unexpectedly declares an update guard", entity.table)
				}
				if !entity.appendOnly && !seen[updateKey] {
					t.Errorf("SQLite does not declare the update guard for mutable %s", entity.table)
				}
			}
		}
	}
}

func communicationSQLiteMigrationNames() []string {
	entity := []string{
		"0033_channel_guard_ins.sql", "0034_channel_guard_upd.sql",
		"0035_channel_grant_guard_ins.sql", "0036_channel_grant_guard_upd.sql",
		"0037_channel_subscription_guard_ins.sql", "0038_channel_subscription_guard_upd.sql",
		"0039_channel_label_definition_guard_ins.sql", "0040_channel_label_definition_guard_upd.sql",
		"0041_channel_route_guard_ins.sql", "0042_channel_route_guard_upd.sql",
		"0043_communication_endpoint_guard_ins.sql", "0044_communication_endpoint_guard_upd.sql",
		"0045_message_guard_ins.sql", "0046_message_guard_upd.sql",
		"0047_message_audience_guard_ins.sql",
		"0048_message_audience_recipient_guard_ins.sql",
		"0049_message_delivery_guard_ins.sql", "0050_message_delivery_guard_upd.sql",
		"0051_inbox_cursor_guard_ins.sql", "0052_inbox_cursor_guard_upd.sql",
		"0053_inbox_cursor_barrier_guard_ins.sql", "0054_inbox_cursor_barrier_guard_upd.sql",
		"0055_message_ack_guard_ins.sql",
		"0056_communication_guard_guard_ins.sql", "0057_communication_guard_guard_upd.sql",
		"0058_decision_request_guard_ins.sql", "0059_decision_request_guard_upd.sql",
		"0060_decision_response_guard_ins.sql",
		"0061_work_handoff_guard_ins.sql", "0062_work_handoff_guard_upd.sql",
		"0063_delivery_dispatch_guard_ins.sql", "0064_delivery_dispatch_guard_upd.sql",
		"0065_delivery_attempt_guard_ins.sql", "0066_delivery_attempt_guard_upd.sql",
		"0067_communication_command_guard_ins.sql",
	}
	mutable := []string{
		"channel", "channel_grant", "channel_subscription", "channel_label_definition",
		"channel_route", "communication_endpoint", "message", "message_delivery",
		"inbox_cursor", "inbox_cursor_barrier", "communication_guard", "decision_request",
		"work_handoff", "delivery_dispatch", "delivery_attempt",
	}
	for i, name := range mutable {
		entity = append(entity, fmt.Sprintf("%04d_%s_no_delete.sql", 68+i, name))
	}
	return append(entity,
		"0083_drop_work_event_guard_ins.sql",
		"0084_work_event_message_guard_ins.sql",
		"0085_communication_command_cursor_projection.sql",
		"0086_communication_command_cursor_projection_canonical_keys.sql",
		"0087_protocol_binding_spec_guard_ins.sql",
		"0088_protocol_binding_spec_guard_upd.sql",
		"0089_protocol_binding_guard_ins.sql",
		"0090_protocol_binding_guard_upd.sql",
		"0091_protocol_binding_spec_no_delete.sql",
		"0092_protocol_binding_no_delete.sql",
	)
}

func communicationMigrationNames(t *testing.T, migrations fs.FS, engineName string) []string {
	t.Helper()
	entries, err := fs.ReadDir(migrations, engineName)
	if err != nil {
		t.Fatalf("read %s migrations: %v", engineName, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names
}

func migrationNamesAfter(names []string, tip int) []string {
	var after []string
	for _, name := range names {
		version, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if err == nil && version > tip {
			after = append(after, name)
		}
	}
	return after
}

func TestCommunicationWorkEventDualAggregateAcrossBackends(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	workspaceID := model.NewID()
	eventID := model.NewID()
	messageID := model.NewID()
	outbox := model.Record{
		colOutboxEventID: eventID.String(), colWorkWorkspaceID: workspaceID.String(),
	}
	event := func(kind model.Kind, aggregateID model.ID) model.Record {
		return model.Record{
			colEventID: eventID.String(), colWorkWorkspaceID: workspaceID.String(),
			colEventAggregateKind: string(kind), colEventAggregateID: aggregateID.String(),
			colEventSeq: int64(1), colEventPayload: "{}",
			colEventPayloadHash: hashBytes([]byte("{}")),
		}
	}
	repo := func(row model.Record) workListRepoForTest {
		return workListRepoForTest{list: func(model.Query) ([]model.Record, error) {
			return []model.Record{row}, nil
		}}
	}

	t.Run("message aggregate is consumable", func(t *testing.T) {
		got, err := workOutboxEvent(ctx, repo(event(messageKind, messageID)), outbox)
		if err != nil {
			t.Fatalf("message WorkEvent rejected by WorkOutbox consumer: %v", err)
		}
		if got.String(colEventAggregateKind) != string(messageKind) ||
			got.String(colEventAggregateID) != messageID.String() {
			t.Fatalf("message aggregate changed during lookup: %v", got)
		}
	})

	t.Run("legacy work item remains consumable", func(t *testing.T) {
		workItemID := model.NewID()
		if _, err := workOutboxEvent(ctx, repo(event(workItemKind, workItemID)), outbox); err != nil {
			t.Fatalf("legacy WorkItem WorkEvent rejected: %v", err)
		}
	})

	t.Run("aggregate vocabulary stays closed", func(t *testing.T) {
		if _, err := workOutboxEvent(ctx, repo(event("sessions.channel", model.NewID())), outbox); err == nil {
			t.Fatal("third WorkEvent aggregate kind was accepted")
		}
	})

	t.Run("payload hash mismatch never reaches the sink", func(t *testing.T) {
		poisoned := event(messageKind, messageID)
		poisoned[colEventPayloadHash] = workSchemaHash("not-the-payload")
		if _, err := workOutboxEvent(ctx, repo(poisoned), outbox); err == nil {
			t.Fatal("WorkOutbox accepted a WorkEvent whose payload hash does not match its payload")
		}
	})

	t.Run("poisoned aggregate does not block an independent ready fact", func(t *testing.T) {
		poisonedEventID := model.NewID()
		readyEventID := model.NewID()
		poisoned := event(messageKind, model.NewID())
		poisoned[colEventID] = poisonedEventID.String()
		poisoned[colEventPayloadHash] = workSchemaHash("not-the-payload")
		ready := event(workItemKind, model.NewID())
		ready[colEventID] = readyEventID.String()
		candidate := func(id, eventID model.ID) model.Record {
			return model.Record{
				model.ColID: id.String(), model.ColVersion: int64(1),
				colOutboxEventID: eventID.String(), colWorkWorkspaceID: workspaceID.String(),
				colOutboxState: "pending", colOutboxAttempts: int64(0),
				colOutboxNextAttemptAt: workSchemaTime(),
			}
		}
		poisonedOutbox := candidate(model.NewID(), poisonedEventID)
		events := workListRepoForTest{list: func(query model.Query) ([]model.Record, error) {
			for _, filter := range query.Filters {
				if filter.Column != colEventID || filter.Op != model.OpEq {
					continue
				}
				switch filter.Value {
				case poisonedEventID.String():
					return []model.Record{poisoned}, nil
				case readyEventID.String():
					return []model.Record{ready}, nil
				}
			}
			return nil, nil
		}}
		expiredDelivery := candidate(model.NewID(), readyEventID)
		expiredDelivery[colOutboxState] = "delivering"
		expiredDelivery[colOutboxAttempts] = int64(1)
		expiredDelivery[colOutboxClaimOwner] = "expired-worker"
		expiredDelivery[colOutboxClaimUntil] = workSchemaTime()
		cohorts := workListRepoForTest{list: func(query model.Query) ([]model.Record, error) {
			for _, filter := range query.Filters {
				if filter.Column != colOutboxState || filter.Op != model.OpEq {
					continue
				}
				switch filter.Value {
				case "pending":
					return []model.Record{poisonedOutbox}, nil
				case "delivering":
					return []model.Record{expiredDelivery}, nil
				}
			}
			return nil, nil
		}}
		pendingFilters := []model.Filter{{Column: colOutboxState, Op: model.OpEq, Value: "pending"}}
		deliveringFilters := []model.Filter{{Column: colOutboxState, Op: model.OpEq, Value: "delivering"}}
		gotOutbox, gotEvent, found, err := firstClaimableWorkOutbox(
			ctx, events, cohorts, pendingFilters, deliveringFilters,
		)
		if err != nil || !found || gotOutbox.String(model.ColID) != expiredDelivery.String(model.ColID) ||
			gotEvent.String(colEventID) != readyEventID.String() {
			t.Fatalf("expired delivery behind poisoned pending aggregate = found %v, outbox %v, event %v, err %v",
				found, gotOutbox, gotEvent, err)
		}

		onlyPoison := workListRepoForTest{list: func(query model.Query) ([]model.Record, error) {
			for _, filter := range query.Filters {
				if filter.Column == colOutboxState && filter.Op == model.OpEq && filter.Value == "pending" {
					return []model.Record{poisonedOutbox}, nil
				}
			}
			return nil, nil
		}}
		if _, _, found, err := firstClaimableWorkOutbox(
			ctx, events, onlyPoison, pendingFilters, deliveringFilters,
		); err == nil || found ||
			!candidateWorkOutboxEvidenceError(err) {
			t.Fatalf("lone poisoned fact = found %v, err %v; want UNKNOWN/evidence_unavailable", found, err)
		}

		blockedEventID := model.NewID()
		predecessorEventID := model.NewID()
		blocked := event(workItemKind, model.NewID())
		blocked[colEventID] = blockedEventID.String()
		blocked[colEventSeq] = int64(2)
		predecessor := event(workItemKind, model.ID(blocked.String(colEventAggregateID)))
		predecessor[colEventID] = predecessorEventID.String()
		blockedOutbox := candidate(model.NewID(), blockedEventID)
		predecessorOutbox := candidate(model.NewID(), predecessorEventID)
		blockedEvents := workListRepoForTest{list: func(query model.Query) ([]model.Record, error) {
			for _, filter := range query.Filters {
				if filter.Column == colEventID && filter.Op == model.OpEq && filter.Value == poisonedEventID.String() {
					return []model.Record{poisoned}, nil
				}
				if filter.Column == colEventID && filter.Op == model.OpEq && filter.Value == blockedEventID.String() {
					return []model.Record{blocked}, nil
				}
				if filter.Column == colEventSeq && filter.Op == model.OpLt {
					return []model.Record{predecessor}, nil
				}
			}
			return nil, nil
		}}
		poisonAndBlocked := workListRepoForTest{list: func(query model.Query) ([]model.Record, error) {
			for _, filter := range query.Filters {
				if filter.Column == colOutboxEventID && filter.Op == model.OpEq &&
					filter.Value == predecessorEventID.String() {
					return []model.Record{predecessorOutbox}, nil
				}
			}
			return []model.Record{poisonedOutbox, blockedOutbox}, nil
		}}
		if _, _, found, err := firstReadyWorkOutbox(ctx, blockedEvents, poisonAndBlocked, nil); err == nil ||
			found || !candidateWorkOutboxEvidenceError(err) {
			t.Fatalf("poison plus valid blocked fact = found %v, err %v; want UNKNOWN", found, err)
		}
	})

	t.Run("replay DTO does not mislabel message identity", func(t *testing.T) {
		replay := WorkOutboxReplay{
			AggregateKind: string(messageKind), AggregateID: messageID,
		}
		raw, err := json.Marshal(replay)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "work_item_id") ||
			!strings.Contains(string(raw), `"aggregate_kind":"sessions.message"`) ||
			!strings.Contains(string(raw), `"aggregate_id":"`+messageID.String()+`"`) {
			t.Fatalf("message replay JSON carries legacy/missing aggregate identity: %s", raw)
		}
	})

	for _, backend := range communicationSchemaBackends(t) {
		backend := backend
		t.Run("durable/"+backend.name, func(t *testing.T) {
			fixture := communicationOpenFixture(t, backend)
			channel := communicationMustCreate(t, fixture, channelKind,
				communicationChannelRecord(fixture.workspace, "dual-event"))
			workItem := communicationMustCreate(t, fixture, workItemKind,
				workSchemaItem(fixture.workspace, "dual aggregate"))
			messageID := model.NewID()
			stagingMessage, err := communicationCreateWithID(
				context.Background(), fixture.m, fixture.tenant, messageKind, messageID,
				communicationStagingMessageRecord(
					fixture.workspace, model.ID(channel.String(model.ColID)), messageID, "dual-event",
				),
			)
			if err != nil {
				t.Fatalf("create standalone draft Message: %v", err)
			}
			publishedMessage := workSchemaClone(stagingMessage)
			publishedMessage[colCommState] = string(MessagePublished)
			publishedMessage[colCommPublishedAt] = model.NewTimestamp(communicationSchemaNow()).String()
			publishedMessage[colCommAudienceHash] = workSchemaHash("dual-event-audience")
			publishedMessage[colCommLastEventSeq] = int64(1)
			publishedMessage, err = communicationUpdate(
				context.Background(), fixture.m, fixture.tenant, messageKind, publishedMessage,
			)
			if err != nil {
				t.Fatalf("publish standalone Message: %v", err)
			}

			for name, direct := range map[string]model.Record{
				"published": func() model.Record {
					id := model.NewID()
					record := communicationStagingMessageRecord(
						fixture.workspace, model.ID(channel.String(model.ColID)), id, "direct-published",
					)
					record[colCommState] = string(MessagePublished)
					record[colCommPublishedAt] = model.NewTimestamp(communicationSchemaNow()).String()
					record[colCommAudienceHash] = workSchemaHash("direct-published-audience")
					record[colCommLastEventSeq] = int64(1)
					record[model.ColID] = id.String()
					return record
				}(),
				"discarded": func() model.Record {
					id := model.NewID()
					record := communicationStagingMessageRecord(
						fixture.workspace, model.ID(channel.String(model.ColID)), id, "direct-discarded",
					)
					record[colCommState] = string(MessageDiscarded)
					record[colCommTerminalAt] = model.NewTimestamp(communicationSchemaNow()).String()
					record[colCommTerminalCode] = "discarded"
					record[colCommLastEventSeq] = int64(1)
					record[model.ColID] = id.String()
					return record
				}(),
			} {
				id := model.ID(direct.String(model.ColID))
				delete(direct, model.ColID)
				if _, err := communicationCreateWithID(context.Background(), fixture.m, fixture.tenant,
					messageKind, id, direct); err == nil {
					t.Fatalf("Message was inserted directly in %s state", name)
				}
			}

			makeEvent := func(kind model.Kind, aggregateID model.ID, sequence int64, seed string) model.Record {
				record := workSchemaEvent(
					fixture.workspace, aggregateID.String(), model.NewID().String(), sequence, seed,
				)
				record[colEventAggregateKind] = string(kind)
				record[colEventType] = seed + ".created"
				return record
			}
			workEvent := communicationMustCreate(t, fixture, workEventKind,
				makeEvent(workItemKind, model.ID(workItem.String(model.ColID)), 1, "work"))
			messageEvent := communicationMustCreate(t, fixture, workEventKind,
				makeEvent(messageKind, messageID, 1, "message"))
			messageAfterEvent := workSchemaClone(publishedMessage)
			messageAfterEvent[colCommLastEventSeq] = int64(2)
			messageAfterEvent, err = communicationUpdate(context.Background(), fixture.m, fixture.tenant,
				messageKind, messageAfterEvent)
			if err != nil {
				t.Fatalf("advance standalone Message event CAS without state change: %v", err)
			}
			publicationRewrite := workSchemaClone(messageAfterEvent)
			publicationRewrite[colCommLastEventSeq] = int64(3)
			publicationRewrite[colCommAudienceHash] = workSchemaHash("rewritten-audience")
			if _, err := communicationUpdate(context.Background(), fixture.m, fixture.tenant,
				messageKind, publicationRewrite); err == nil {
				t.Fatal("same-state Message event CAS rewrote the sealed audience")
			}
			messageEvent2 := communicationMustCreate(t, fixture, workEventKind,
				makeEvent(messageKind, messageID, 2, "message-ack"))
			retractedMessage := workSchemaClone(messageAfterEvent)
			retractedMessage[colCommState] = string(MessageRetracted)
			retractedMessage[colCommLastEventSeq] = int64(3)
			retractedMessage[colCommTerminalAt] = model.NewTimestamp(communicationSchemaNow()).String()
			retractedMessage[colCommTerminalCode] = "sender_retracted"
			retractedMessage, err = communicationUpdate(context.Background(), fixture.m, fixture.tenant,
				messageKind, retractedMessage)
			if err != nil {
				t.Fatalf("terminalize standalone Message: %v", err)
			}
			terminalEvent := communicationMustCreate(t, fixture, workEventKind,
				makeEvent(messageKind, messageID, 3, "message-retracted"))
			terminalAfterLateEvent := workSchemaClone(retractedMessage)
			terminalAfterLateEvent[colCommLastEventSeq] = int64(4)
			terminalAfterLateEvent, err = communicationUpdate(
				context.Background(), fixture.m, fixture.tenant, messageKind, terminalAfterLateEvent,
			)
			if err != nil {
				t.Fatalf("advance terminal standalone Message for late event: %v", err)
			}
			lateEvent := communicationMustCreate(t, fixture, workEventKind,
				makeEvent(messageKind, messageID, 4, "message-late-ack"))
			terminalRewrite := workSchemaClone(terminalAfterLateEvent)
			terminalRewrite[colCommLastEventSeq] = int64(5)
			terminalRewrite[colCommTerminalCode] = "rewritten_terminal_evidence"
			if _, err := communicationUpdate(context.Background(), fixture.m, fixture.tenant,
				messageKind, terminalRewrite); err == nil {
				t.Fatal("same-state terminal Message event CAS rewrote terminal evidence")
			}

			durableEvents := []model.Record{
				workEvent, messageEvent, messageEvent2, terminalEvent, lateEvent,
			}
			var durableOutboxes []model.Record
			for _, event := range durableEvents {
				outbox := workSchemaOutbox(fixture.workspace, event.String(colEventID))
				durableOutboxes = append(durableOutboxes,
					communicationMustCreate(t, fixture, workOutboxKind, outbox))
			}
			replayMessageID := model.NewID()
			replayDraft, err := communicationCreateWithID(context.Background(), fixture.m, fixture.tenant,
				messageKind, replayMessageID, communicationStagingMessageRecord(
					fixture.workspace, model.ID(channel.String(model.ColID)), replayMessageID, "replay-message",
				))
			if err != nil {
				t.Fatalf("create replay Message: %v", err)
			}
			replayPublished := workSchemaClone(replayDraft)
			replayPublished[colCommState] = string(MessagePublished)
			replayPublished[colCommPublishedAt] = model.NewTimestamp(communicationSchemaNow()).String()
			replayPublished[colCommAudienceHash] = workSchemaHash("replay-message-audience")
			replayPublished[colCommLastEventSeq] = int64(1)
			if _, err := communicationUpdate(context.Background(), fixture.m, fixture.tenant,
				messageKind, replayPublished); err != nil {
				t.Fatalf("publish replay Message: %v", err)
			}
			replayEvent := communicationMustCreate(t, fixture, workEventKind,
				makeEvent(messageKind, replayMessageID, 1, "message-replay"))
			communicationMustCreate(t, fixture, workOutboxKind,
				workSchemaOutbox(fixture.workspace, replayEvent.String(colEventID)))
			deadLetterWorkOutboxForTest(t, workFixture{
				m: fixture.m, st: fixture.st, tenant: fixture.tenant, workspace: fixture.workspace,
			}, model.ID(replayEvent.String(colEventID)))

			thirdKind := makeEvent(channelKind, model.ID(channel.String(model.ColID)), 1, "channel")
			if _, err := communicationCreate(context.Background(), fixture.m, fixture.tenant,
				workEventKind, thirdKind); err == nil {
				t.Fatal("third WorkEvent aggregate kind was persisted")
			}

			if err := fixture.st.Close(); err != nil {
				t.Fatalf("close before durability check: %v", err)
			}
			reopenedModule := New()
			reopened, err := engine.Open(context.Background(), store.Config{
				Engine: backend.engineName, DSN: backend.dsn, Debug: true,
				Clock: &testClock{now: communicationSchemaNow()},
			}, reopenedModule.RegisterSchema)
			if err != nil {
				t.Fatalf("reopen %s: %v", backend.name, err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			reopenedModule.UseData(api.NewModuleData(reopened))
			sink := &recordingWorkSink{}
			reopenedModule.UseWorkEventSink(sink)
			if err := reopenedModule.DrainWorkOutbox(context.Background(), fixture.tenant, 10); err != nil {
				t.Fatalf("drain dual-aggregate outbox after reopen: %v", err)
			}
			sink.mu.Lock()
			gotEnvelopes := append([]WorkEventEnvelope(nil), sink.events...)
			sink.mu.Unlock()
			if len(gotEnvelopes) != 5 {
				t.Fatalf("drained envelopes = %d, want 5", len(gotEnvelopes))
			}
			gotAggregates := make(map[string]model.ID, len(gotEnvelopes))
			for _, envelope := range gotEnvelopes {
				gotAggregates[envelope.AggregateKind] = envelope.AggregateID
			}
			if gotAggregates[string(workItemKind)] != model.ID(workItem.String(model.ColID)) ||
				gotAggregates[string(messageKind)] != messageID {
				t.Fatalf("drained aggregate identities = %v", gotAggregates)
			}
			var messageSequences []int64
			for _, envelope := range gotEnvelopes {
				if envelope.AggregateKind == string(messageKind) && envelope.AggregateID == messageID {
					messageSequences = append(messageSequences, envelope.Sequence)
				}
			}
			if !reflect.DeepEqual(messageSequences, []int64{1, 2, 3, 4}) {
				t.Fatalf("standalone Message event order = %v, want [1 2 3 4]", messageSequences)
			}

			admin := WorkPrincipal{
				ActorKind: string(model.ActorUser), ActorRef: model.NewID().String(),
				Actor: "communication-schema-admin", Admin: true,
			}
			replayFixture := workFixture{
				m: reopenedModule, st: reopened, tenant: fixture.tenant,
				workspace: fixture.workspace, principal: admin,
			}
			replayEventID := model.ID(replayEvent.String(colEventID))
			cmd := WorkOutboxReplayCommand{Command: "outbox.replay", EventID: replayEventID}
			plan, err := reopenedModule.PlanWorkOutboxReplay(
				context.Background(), fixture.tenant, admin, cmd,
			)
			if err != nil || plan.Verdict != VerdictClean || plan.PlanHash == "" {
				t.Fatalf("plan Message outbox replay = %#v, %v", plan, err)
			}
			beforeReplay := workOutboxSnapshotForTest(t, replayFixture, replayEventID)
			cmd.ExpectedVersion = beforeReplay.version
			cmd.ExpectedPlanHash = plan.PlanHash
			cmd.IdempotencyKey = model.NewID().String()
			firstReplay, err := reopenedModule.ReplayWorkOutbox(
				context.Background(), fixture.tenant, admin, cmd,
			)
			if err != nil || firstReplay.AggregateKind != string(messageKind) ||
				firstReplay.AggregateID != replayMessageID || !firstReplay.WorkItemID.IsZero() ||
				firstReplay.EventID != replayEventID || firstReplay.State != "pending" ||
				firstReplay.responseJSON == "" || firstReplay.Replayed {
				t.Fatalf("Message outbox replay = %#v, %v", firstReplay, err)
			}
			exactReplay, err := reopenedModule.ReplayWorkOutbox(
				context.Background(), fixture.tenant, admin, cmd,
			)
			if err != nil || !exactReplay.Replayed || exactReplay.responseJSON != firstReplay.responseJSON ||
				exactReplay.AggregateKind != string(messageKind) || !exactReplay.WorkItemID.IsZero() {
				t.Fatalf("exact Message outbox retry = %#v, %v", exactReplay, err)
			}
			if err := reopenedModule.DrainWorkOutbox(context.Background(), fixture.tenant, 1); err != nil {
				t.Fatalf("drain replayed Message outbox: %v", err)
			}
			sink.mu.Lock()
			drainedReplay := append([]WorkEventEnvelope(nil), sink.events...)
			sink.mu.Unlock()
			if len(drainedReplay) != 6 || drainedReplay[5].EventID != replayEventID ||
				drainedReplay[5].AggregateKind != string(messageKind) ||
				drainedReplay[5].AggregateID != replayMessageID {
				t.Fatalf("replayed Message envelope = %#v", drainedReplay)
			}
			if err := reopenedModule.data.View(context.Background(), fixture.tenant, func(sc store.Scope) error {
				events, err := sc.Ext(workEventKind)
				if err != nil {
					return err
				}
				for _, event := range durableEvents {
					if _, err := events.Get(context.Background(), model.ID(event.String(model.ColID))); err != nil {
						return fmt.Errorf("get durable %s event: %w", event.String(colEventAggregateKind), err)
					}
				}
				outboxRepo, err := sc.Ext(workOutboxKind)
				if err != nil {
					return err
				}
				for _, outbox := range durableOutboxes {
					got, err := outboxRepo.Get(context.Background(), model.ID(outbox.String(model.ColID)))
					if err != nil {
						return err
					}
					if got.String(colOutboxState) != "published" {
						return fmt.Errorf("durable outbox state = %q, want published", got.String(colOutboxState))
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCommunicationCausalArcHashPersistenceAcrossBackends(t *testing.T) {
	t.Parallel()

	reg := communicationCaptureSchema(t)
	descriptor := communicationDescriptor(t, reg, messageAudienceRecipientKind)
	fields := communicationDescriptorFields(descriptor)
	hashField, ok := fields[colCommCausalArcHash]
	if !ok || hashField.Kind != model.KindBytes || hashField.Nullable {
		t.Fatalf("causal_arc_hash = %+v, want required bytes", hashField)
	}
	if !communicationHasIndex(descriptor, true,
		model.ColTenantID, colCommMessageAudienceID, colCommCausalArcHash,
	) {
		t.Fatal("audience-recipient schema lacks unique (tenant,message_audience,causal_arc_hash)")
	}
	delivery := communicationDescriptor(t, reg, messageDeliveryKind)
	if !communicationHasIndex(delivery, true,
		model.ColTenantID, colCommMessageID, colCommRecipientKind, colCommRecipientRef,
	) {
		t.Fatal("delivery schema does not globally deduplicate one recipient per message")
	}

	scope := DirectoryScopeRef{TenantID: model.TenantID(model.NewID()), WorkspaceID: model.NewID()}
	recipient := RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()}
	audienceID, deliveryID := model.NewID(), model.NewID()
	makeArc := func(groupRef string, subscriptionID model.ID) MessageAudienceRecipient {
		arc := MessageAudienceRecipient{
			AppendOnlyCommunicationEntity: AppendOnlyCommunicationEntity{CommunicationEntity: CommunicationEntity{
				ID: model.NewID(), TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID,
				Version: 1, CreatedAt: communicationSchemaNow(),
			}},
			MessageAudienceID: audienceID, MessageDeliveryID: deliveryID,
			Recipient: recipient, RecipientEpoch: 3, WakePolicy: WakeNone,
			RouteReasons:   []RouteReason{"subscriber"},
			Selector:       AudienceSelector{Kind: AudienceSubscribers, WakePolicy: WakeNone},
			DirectoryEpoch: 42, ChannelACLRevision: 5, RouteRevision: 6,
			SubscriptionRevision: 7, CausalKind: CausalSubscriber, CausalRef: groupRef,
			CausalFactKind: model.Kind("core.user_group_member"),
			CausalFactID:   model.NewID(), CausalFactVersion: 8,
			OriginalSubscriber: &CommunicationSubjectRef{Kind: SubjectUserGroup, Ref: groupRef},
			SubscriptionID:     subscriptionID, SubscriptionGeneration: 1,
		}
		hash, err := CanonicalAudienceCausalArcHash(arc)
		if err != nil {
			t.Fatalf("canonical causal arc: %v", err)
		}
		arc.CausalArcHash = hash
		return arc
	}
	first := makeArc(model.NewID().String(), model.NewID())
	second := makeArc(model.NewID().String(), model.NewID())
	if reflect.DeepEqual(first.CausalArcHash, second.CausalArcHash) {
		t.Fatal("two independent causal arcs collapsed to one hash")
	}
	recreatedFact := first
	recreatedFact.ID = model.NewID()
	recreatedFact.CausalFactID = model.NewID()
	recreatedFact.CausalFactVersion++
	recreatedHash, err := CanonicalAudienceCausalArcHash(recreatedFact)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.CausalArcHash, recreatedHash) {
		t.Fatal("re-created directory fact changed the stable causal relation hash")
	}

	for _, backend := range communicationSchemaBackends(t) {
		backend := backend
		t.Run("durable/"+backend.name, func(t *testing.T) {
			fixture := communicationOpenFixture(t, backend)
			ctx := context.Background()
			now := model.NewTimestamp(communicationSchemaNow()).String()
			channel := communicationMustCreate(t, fixture, channelKind,
				communicationChannelRecord(fixture.workspace, "causal-arcs"))
			channelID := model.ID(channel.String(model.ColID))
			messageID := model.NewID()
			stagingMessage, err := communicationCreateWithID(ctx, fixture.m, fixture.tenant,
				messageKind, messageID, communicationStagingMessageRecord(
					fixture.workspace, channelID, messageID, "causal-arcs",
				))
			if err != nil {
				t.Fatalf("create staging Message: %v", err)
			}

			recipientID := model.NewID()
			deliveryID := model.NewID()
			deliveryRecord := model.Record{
				colWorkWorkspaceID: fixture.workspace.String(), colCommMessageID: messageID.String(),
				colCommRecipientKind: string(RecipientUser), colCommRecipientRef: recipientID.String(),
				colCommRecipientEpoch: int64(1), colCommDeliverySeq: int64(1),
				colCommRequired: false, colCommRouteReasonsJSON: `["subscriber"]`,
				colCommWakePolicy: string(WakeNone), colCommState: string(DeliveryAvailable),
				colCommAvailableAt: now,
			}
			if _, err := communicationCreateWithID(ctx, fixture.m, fixture.tenant,
				messageDeliveryKind, deliveryID, deliveryRecord); err != nil {
				t.Fatalf("create MessageDelivery: %v", err)
			}

			audienceID := model.NewID()
			audienceRecord := model.Record{
				colWorkWorkspaceID: fixture.workspace.String(), colCommMessageID: messageID.String(),
				colCommOrdinal: int64(1), colCommSelectorKind: string(AudienceSubscribers),
				colCommSelectorRequired: false, colCommSelectorWakePolicy: string(WakeNone),
				colCommChannelACLRevision: int64(1), colCommRouteRevision: int64(1),
				colCommSubscriptionRevision: int64(1), colCommDirectoryEpoch: int64(1),
				colCommDirectorySnapshotAt: now, colCommResolvedCount: int64(1),
				colCommSelectorHash: workSchemaHash("subscriber-selector"),
				colCommResolvedHash: workSchemaHash("subscriber-resolution"),
			}
			if _, err := communicationCreateWithID(ctx, fixture.m, fixture.tenant,
				messageAudienceKind, audienceID, audienceRecord); err != nil {
				t.Fatalf("create MessageAudience: %v", err)
			}

			groupRefs := []string{model.NewID().String(), model.NewID().String()}
			subscriptionIDs := make([]model.ID, 0, len(groupRefs))
			for i, groupRef := range groupRefs {
				subscription := model.Record{
					colWorkWorkspaceID: fixture.workspace.String(), colCommChannelID: channelID.String(),
					colCommSubscriberKind: string(SubjectUserGroup), colCommSubscriberRef: groupRef,
					colCommGeneration: int64(1), colCommMode: string(SubscriptionAll),
					colCommWake: string(WakeNone), colCommRequiredForCritical: false,
					colCommState: string(SubscriptionActive),
				}
				created := communicationMustCreate(t, fixture, channelSubscriptionKind, subscription)
				subscriptionIDs = append(subscriptionIDs, model.ID(created.String(model.ColID)))
				_ = i
			}

			makePersistedArc := func(groupRef string, subscriptionID model.ID) (MessageAudienceRecipient, model.Record) {
				arc := MessageAudienceRecipient{
					AppendOnlyCommunicationEntity: AppendOnlyCommunicationEntity{CommunicationEntity: CommunicationEntity{
						ID: model.NewID(), TenantID: fixture.tenant, WorkspaceID: fixture.workspace,
						Version: 1, CreatedAt: communicationSchemaNow(),
					}},
					MessageAudienceID: audienceID, MessageDeliveryID: deliveryID,
					Recipient:      RecipientRef{Kind: RecipientUser, Ref: recipientID.String()},
					RecipientEpoch: 1, WakePolicy: WakeNone, RouteReasons: []RouteReason{"subscriber"},
					Selector:       AudienceSelector{Kind: AudienceSubscribers, WakePolicy: WakeNone},
					DirectoryEpoch: 1, ChannelACLRevision: 1, RouteRevision: 1,
					SubscriptionRevision: 1, CausalKind: CausalSubscriber, CausalRef: groupRef,
					CausalFactKind: model.Kind("core.user_group_member"),
					CausalFactID:   model.NewID(), CausalFactVersion: 1,
					OriginalSubscriber: &CommunicationSubjectRef{Kind: SubjectUserGroup, Ref: groupRef},
					SubscriptionID:     subscriptionID, SubscriptionGeneration: 1,
				}
				hash, err := CanonicalAudienceCausalArcHash(arc)
				if err != nil {
					t.Fatalf("canonical persisted causal arc: %v", err)
				}
				arc.CausalArcHash = hash
				record := model.Record{
					colWorkWorkspaceID:       fixture.workspace.String(),
					colCommMessageAudienceID: audienceID.String(), colCommMessageDeliveryID: deliveryID.String(),
					colCommRecipientKind: string(RecipientUser), colCommRecipientRef: recipientID.String(),
					colCommRecipientEpoch: int64(1), colCommRequired: false,
					colCommWakePolicy: string(WakeNone), colCommRouteReasonsJSON: `["subscriber"]`,
					colCommSelectorKind: string(AudienceSubscribers), colCommSelectorRequired: false,
					colCommSelectorWakePolicy: string(WakeNone), colCommDirectoryEpoch: int64(1),
					colCommChannelACLRevision: int64(1), colCommRouteRevision: int64(1),
					colCommSubscriptionRevision: int64(1), colCommCausalKind: string(CausalSubscriber),
					colCommCausalRef: groupRef, colCommCausalFactKind: "core.user_group_member",
					colCommCausalFactID: arc.CausalFactID.String(), colCommCausalFactVersion: int64(1),
					colCommOriginalSubscriberKind: string(SubjectUserGroup),
					colCommOriginalSubscriberRef:  groupRef, colCommSubscriptionID: subscriptionID.String(),
					colCommSubscriptionGeneration: int64(1), colCommCausalArcHash: hash,
				}
				return arc, record
			}

			persisted := make([]model.Record, 0, 2)
			for i, groupRef := range groupRefs {
				arc, record := makePersistedArc(groupRef, subscriptionIDs[i])
				if i == 0 {
					invalidFactID := workSchemaClone(record)
					invalidFactID[colCommCausalFactID] = "00000000-0000-4000-8000-000000000001"
					if _, err := communicationCreate(ctx, fixture.m, fixture.tenant,
						messageAudienceRecipientKind, invalidFactID); err == nil {
						t.Fatal("causal membership fact accepted a UUIDv4 identity")
					}
				}
				created := communicationMustCreate(t, fixture, messageAudienceRecipientKind, record)
				if !reflect.DeepEqual(created.Bytes(colCommCausalArcHash), arc.CausalArcHash) {
					t.Fatalf("persisted causal arc %d changed hash", i)
				}
				persisted = append(persisted, created)
			}
			duplicate := workSchemaClone(persisted[0])
			delete(duplicate, model.ColID)
			delete(duplicate, model.ColTenantID)
			delete(duplicate, model.ColCreatedAt)
			delete(duplicate, model.ColUpdatedAt)
			delete(duplicate, model.ColVersion)
			duplicate[colCommCausalFactID] = model.NewID().String()
			if _, err := communicationCreate(ctx, fixture.m, fixture.tenant,
				messageAudienceRecipientKind, duplicate); err == nil {
				t.Fatal("duplicate semantic causal arc was persisted")
			}
			missingFact := workSchemaClone(duplicate)
			missingFact[colCommCausalArcHash] = workSchemaHash("missing-current-causal-fact")
			delete(missingFact, colCommCausalFactKind)
			delete(missingFact, colCommCausalFactID)
			delete(missingFact, colCommCausalFactVersion)
			if _, err := communicationCreate(ctx, fixture.m, fixture.tenant,
				messageAudienceRecipientKind, missingFact); err == nil {
				t.Fatal("group causal arc without its observed membership fact was persisted")
			}

			lateGroupRef := model.NewID().String()
			lateSubscription := communicationMustCreate(t, fixture, channelSubscriptionKind, model.Record{
				colWorkWorkspaceID: fixture.workspace.String(), colCommChannelID: channelID.String(),
				colCommSubscriberKind: string(SubjectUserGroup), colCommSubscriberRef: lateGroupRef,
				colCommGeneration: int64(1), colCommMode: string(SubscriptionAll),
				colCommWake: string(WakeNone), colCommRequiredForCritical: false,
				colCommState: string(SubscriptionActive),
			})
			published := workSchemaClone(stagingMessage)
			published[colCommState] = string(MessagePublished)
			published[colCommPublishedAt] = now
			published[colCommAudienceHash] = workSchemaHash("causal-arcs-audience")
			published[colCommLastEventSeq] = int64(1)
			if _, err := communicationUpdate(ctx, fixture.m, fixture.tenant, messageKind, published); err != nil {
				t.Fatalf("publish Message after complete audience graph: %v", err)
			}

			lateAudience := workSchemaClone(audienceRecord)
			lateAudience[colCommOrdinal] = int64(2)
			lateAudience[colCommSelectorHash] = workSchemaHash("late-selector")
			lateAudience[colCommResolvedHash] = workSchemaHash("late-resolution")
			if _, err := communicationCreateWithID(ctx, fixture.m, fixture.tenant,
				messageAudienceKind, model.NewID(), lateAudience); err == nil {
				t.Fatal("MessageAudience was appended after its Message was published")
			}
			lateDelivery := workSchemaClone(deliveryRecord)
			lateDelivery[colCommRecipientRef] = model.NewID().String()
			lateDelivery[colCommDeliverySeq] = int64(2)
			if _, err := communicationCreateWithID(ctx, fixture.m, fixture.tenant,
				messageDeliveryKind, model.NewID(), lateDelivery); err == nil {
				t.Fatal("MessageDelivery was appended after its Message was published")
			}
			_, lateRecipient := makePersistedArc(
				lateGroupRef, model.ID(lateSubscription.String(model.ColID)),
			)
			if _, err := communicationCreate(ctx, fixture.m, fixture.tenant,
				messageAudienceRecipientKind, lateRecipient); err == nil {
				t.Fatal("MessageAudienceRecipient was appended after its Message was published")
			}

			if err := fixture.st.Close(); err != nil {
				t.Fatalf("close before causal-arc durability check: %v", err)
			}
			reopenedModule := New()
			reopened, err := engine.Open(ctx, store.Config{
				Engine: backend.engineName, DSN: backend.dsn, Debug: true,
				Clock: &testClock{now: communicationSchemaNow()},
			}, reopenedModule.RegisterSchema)
			if err != nil {
				t.Fatalf("reopen %s: %v", backend.name, err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			reopenedModule.UseData(api.NewModuleData(reopened))
			if err := reopenedModule.data.View(ctx, fixture.tenant, func(sc store.Scope) error {
				repo, err := sc.Ext(messageAudienceRecipientKind)
				if err != nil {
					return err
				}
				for _, want := range persisted {
					got, err := repo.Get(ctx, model.ID(want.String(model.ColID)))
					if err != nil {
						return err
					}
					if !reflect.DeepEqual(got.Bytes(colCommCausalArcHash), want.Bytes(colCommCausalArcHash)) {
						return errors.New("causal_arc_hash changed after reopen")
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCommunicationDispatchRouteAndReconcileDurabilityAcrossBackends(t *testing.T) {
	t.Parallel()

	reg := communicationCaptureSchema(t)
	decision := communicationDescriptor(t, reg, decisionRequestKind)
	if !communicationHasIndex(decision, false,
		model.ColTenantID, colCommState, colCommDueAt, model.ColID,
	) {
		t.Error("decision request schema lacks expiry-worker deadline index")
	}
	handoff := communicationDescriptor(t, reg, handoffKind)
	if !communicationHasIndex(handoff, false,
		model.ColTenantID, colCommState, colCommAckDeadline, model.ColID,
	) {
		t.Error("handoff schema lacks expiry-worker deadline index")
	}
	descriptor := communicationDescriptor(t, reg, deliveryDispatchKind)
	communicationRequireFields(t, descriptor,
		colCommRootDispatchID, colCommPredecessorID, colCommEndpointID,
		colCommEndpointGeneration, colCommRouteRuleID, colCommRouteRuleGeneration,
		colCommDispatchGeneration, colCommRerouteRung, colCommPolicyGeneration,
		colCommResolutionDeadlineAt, colCommResolutionCode, colCommReconciledAttemptID,
		colCommReconciledEndpointID, colCommReconciledEndpointGeneration,
		colCommReconciliationVerdict, colCommReconciliationCode,
		colCommReconciliationEvidenceRef, colCommReconciliationObservedAt,
		colCommProviderAcceptanceHash,
	)
	if !communicationHasIndex(descriptor, true,
		model.ColTenantID, colCommRootDispatchID, colCommDispatchGeneration,
	) {
		t.Error("dispatch schema lacks unique root/generation")
	}
	if !communicationHasIndex(descriptor, true, model.ColTenantID, colCommPredecessorID) {
		t.Error("dispatch schema lacks unique predecessor/no-fork index")
	}
	if !communicationHasIndex(descriptor, false,
		model.ColTenantID, colCommState, colCommResolutionDeadlineAt, model.ColID,
	) {
		t.Error("dispatch schema lacks terminal-resolution deadline scheduling index")
	}
	attempt := communicationDescriptor(t, reg, deliveryAttemptKind)
	if !communicationHasIndex(attempt, true, model.ColTenantID, colCommDispatchID) {
		t.Error("delivery attempt schema permits more than one external attempt per generation")
	}

	for _, backend := range communicationSchemaBackends(t) {
		backend := backend
		t.Run("durable/"+backend.name, func(t *testing.T) {
			fixture := communicationOpenFixture(t, backend)
			ctx := context.Background()
			nowTime := communicationSchemaNow()
			now := model.NewTimestamp(nowTime).String()
			future := model.NewTimestamp(nowTime.Add(time.Hour)).String()
			channel := communicationMustCreate(t, fixture, channelKind,
				communicationChannelRecord(fixture.workspace, "dispatch"))
			channelID := model.ID(channel.String(model.ColID))
			messageID := model.NewID()
			if _, err := communicationCreateWithID(ctx, fixture.m, fixture.tenant,
				messageKind, messageID, communicationStagingMessageRecord(
					fixture.workspace, channelID, messageID, "dispatch-message",
				)); err != nil {
				t.Fatalf("create staging Message: %v", err)
			}
			deliveryID := model.NewID()
			if _, err := communicationCreateWithID(ctx, fixture.m, fixture.tenant,
				messageDeliveryKind, deliveryID, model.Record{
					colWorkWorkspaceID: fixture.workspace.String(), colCommMessageID: messageID.String(),
					colCommRecipientKind: string(RecipientAgent), colCommRecipientRef: model.NewID().String(),
					colCommRecipientEpoch: int64(1), colCommDeliverySeq: int64(1),
					colCommRequired: false, colCommRouteReasonsJSON: `["direct"]`,
					colCommWakePolicy: string(WakeNone), colCommState: string(DeliveryAvailable),
					colCommAvailableAt: now,
				}); err != nil {
				t.Fatalf("create MessageDelivery: %v", err)
			}
			endpoint := communicationMustCreate(t, fixture, communicationEndpointKind, model.Record{
				colWorkWorkspaceID: fixture.workspace.String(),
				colCommOwnerKind:   string(RecipientAgent), colCommOwnerRef: model.NewID().String(),
				colCommProviderKey: "driver:dispatch-test", colTransport: "test",
				colCommEndpointRef: "dispatch-endpoint", colCommCapabilitiesJSON: "{}",
				colCommSupportLevel: string(EndpointStable), colCommPriority: int64(0),
				colCommState: string(EndpointActive), colCommGeneration: int64(1),
			})
			endpointID := model.ID(endpoint.String(model.ColID))

			pendingRecord := func(id, root, predecessor model.ID, generation, rung int64, seed string) model.Record {
				record := model.Record{
					colWorkWorkspaceID: fixture.workspace.String(), colCommDeliveryID: deliveryID.String(),
					colCommRootDispatchID: root.String(), colCommEndpointID: endpointID.String(),
					colCommEndpointGeneration: int64(1), colCommDispatchGeneration: generation,
					colCommRerouteRung: rung, colCommPolicyGeneration: int64(1),
					colCommState: string(DispatchPending), colCommAttemptCount: int64(0),
					colCommIdempotencyKeyHash: workSchemaHash(seed),
				}
				if !predecessor.IsZero() {
					record[colCommPredecessorID] = predecessor.String()
				}
				_ = id
				return record
			}
			createDispatch := func(id, root, predecessor model.ID, generation, rung int64, seed string) model.Record {
				record, err := communicationCreateWithID(ctx, fixture.m, fixture.tenant,
					deliveryDispatchKind, id, pendingRecord(id, root, predecessor, generation, rung, seed))
				if err != nil {
					t.Fatalf("create dispatch generation %d/rung %d: %v", generation, rung, err)
				}
				return record
			}
			reserveAttempt := func(dispatch model.Record, seed string) (model.Record, model.Record) {
				claimed := workSchemaClone(dispatch)
				claimed[colCommState] = string(DispatchInFlight)
				claimed[colCommAttemptCount] = int64(1)
				claimed[colCommClaimOwner] = "dispatch-worker"
				claimed[colCommClaimUntil] = future
				claimed, err := communicationUpdate(ctx, fixture.m, fixture.tenant, deliveryDispatchKind, claimed)
				if err != nil {
					t.Fatalf("claim dispatch: %v", err)
				}
				attemptID := model.NewID()
				attemptRecord, err := communicationCreateWithID(ctx, fixture.m, fixture.tenant,
					deliveryAttemptKind, attemptID, model.Record{
						colWorkWorkspaceID: fixture.workspace.String(),
						colCommDispatchID:  claimed.String(model.ColID), colCommAttemptSeq: int64(1),
						colCommState: string(AttemptReserved), colCommStartedAt: now,
						colCommTransmitBoundary: string(TransmitUnknown), colCommRequestHash: workSchemaHash(seed),
					})
				if err != nil {
					t.Fatalf("reserve attempt: %v", err)
				}
				return claimed, attemptRecord
			}
			finishAttempt := func(dispatch model.Record, verdict AssessmentVerdict, boundary TransmitBoundary, seed string) (model.Record, model.Record) {
				claimed, attemptRecord := reserveAttempt(dispatch, seed)
				finished := workSchemaClone(attemptRecord)
				var err error
				finished[colCommState] = string(AttemptFinished)
				finished[colCommTransmitBoundary] = string(boundary)
				finished[colCommFinishedAt] = now
				finished[colCommVerdict] = string(verdict)
				finished[colCommCode] = seed
				finished, err = communicationUpdate(ctx, fixture.m, fixture.tenant, deliveryAttemptKind, finished)
				if err != nil {
					t.Fatalf("finish attempt: %v", err)
				}

				terminal := workSchemaClone(claimed)
				delete(terminal, colCommClaimOwner)
				delete(terminal, colCommClaimUntil)
				terminal[colCommLastVerdict] = string(verdict)
				terminal[colCommLastCode] = seed
				terminal[colCommResolutionDeadlineAt] = future
				terminal[colCommResolutionCode] = "provider_resolution_pending"
				switch verdict {
				case VerdictBroken:
					terminal[colCommState] = string(DispatchFailed)
				case VerdictUnknown:
					terminal[colCommState] = string(DispatchUnknown)
				default:
					t.Fatalf("unsupported dispatch finish verdict %s", verdict)
				}
				terminal, err = communicationUpdate(ctx, fixture.m, fixture.tenant, deliveryDispatchKind, terminal)
				if err != nil {
					t.Fatalf("finish dispatch: %v", err)
				}
				return terminal, finished
			}
			supersede := func(dispatch model.Record) model.Record {
				after := workSchemaClone(dispatch)
				after[colCommState] = string(DispatchSuperseded)
				delete(after, colCommResolutionDeadlineAt)
				after[colCommSettledAt] = now
				after, err := communicationUpdate(ctx, fixture.m, fixture.tenant, deliveryDispatchKind, after)
				if err != nil {
					t.Fatalf("supersede dispatch: %v", err)
				}
				return after
			}

			compatibilityRootID := model.NewID()
			compatibilityRoot := createDispatch(
				compatibilityRootID, compatibilityRootID, "", 1, 0, "attempt-compatibility",
			)
			compatibilityClaim, _ := reserveAttempt(compatibilityRoot, "attempt-compatibility")
			fabricatedSuccess := workSchemaClone(compatibilityClaim)
			fabricatedSuccess[colCommState] = string(DispatchSucceeded)
			delete(fabricatedSuccess, colCommClaimOwner)
			delete(fabricatedSuccess, colCommClaimUntil)
			fabricatedSuccess[colCommLastVerdict] = string(VerdictClean)
			fabricatedSuccess[colCommLastCode] = "fabricated_success"
			fabricatedSuccess[colCommResolutionCode] = "fabricated_success"
			fabricatedSuccess[colCommSettledAt] = now
			if _, err := communicationUpdate(ctx, fixture.m, fixture.tenant,
				deliveryDispatchKind, fabricatedSuccess); err == nil {
				t.Fatal("Dispatch succeeded while its DeliveryAttempt was still reserved")
			}

			receiptRootID := model.NewID()
			receiptRoot := createDispatch(
				receiptRootID, receiptRootID, "", 1, 0, "attempt-provider-receipt",
			)
			_, receiptAttempt := reserveAttempt(receiptRoot, "attempt-provider-receipt")
			crossedWithoutReceipt := workSchemaClone(receiptAttempt)
			crossedWithoutReceipt[colCommState] = string(AttemptFinished)
			crossedWithoutReceipt[colCommTransmitBoundary] = string(TransmitCrossed)
			crossedWithoutReceipt[colCommFinishedAt] = now
			crossedWithoutReceipt[colCommVerdict] = string(VerdictClean)
			crossedWithoutReceipt[colCommCode] = "provider_accepted"
			if _, err := communicationUpdate(ctx, fixture.m, fixture.tenant,
				deliveryAttemptKind, crossedWithoutReceipt); err == nil {
				t.Fatal("crossed CLEAN DeliveryAttempt accepted without provider_receipt_hash")
			}
			crossedWithReceipt := workSchemaClone(crossedWithoutReceipt)
			crossedWithReceipt[colCommProviderReceiptHash] = workSchemaHash("provider-receipt")
			if _, err := communicationUpdate(ctx, fixture.m, fixture.tenant,
				deliveryAttemptKind, crossedWithReceipt); err != nil {
				t.Fatalf("crossed CLEAN DeliveryAttempt with provider receipt rejected: %v", err)
			}

			rootID := model.NewID()
			terminalInsert := pendingRecord(rootID, rootID, "", 1, 0, "terminal-insert")
			terminalInsert[colCommState] = string(DispatchSucceeded)
			terminalInsert[colCommAttemptCount] = int64(1)
			terminalInsert[colCommLastVerdict] = string(VerdictClean)
			terminalInsert[colCommLastCode] = "fabricated_success"
			terminalInsert[colCommResolutionCode] = "fabricated_success"
			terminalInsert[colCommSettledAt] = now
			if _, err := communicationCreateWithID(ctx, fixture.m, fixture.tenant,
				deliveryDispatchKind, rootID, terminalInsert); err == nil {
				t.Fatal("initial Dispatch was inserted directly in a terminal state")
			}
			root := createDispatch(rootID, rootID, "", 1, 0, "dispatch-1")
			rootFailed, rootAttempt := finishAttempt(root, VerdictBroken, TransmitNotCrossed, "dispatch_failed_1")
			rewrittenAttempt := workSchemaClone(rootAttempt)
			rewrittenAttempt[colCommCode] = "rewritten_terminal_evidence"
			if _, err := communicationUpdate(ctx, fixture.m, fixture.tenant,
				deliveryAttemptKind, rewrittenAttempt); err == nil {
				t.Fatal("terminal DeliveryAttempt evidence was rewritten")
			}
			root = supersede(rootFailed)
			if _, err := communicationCreateWithID(ctx, fixture.m, fixture.tenant, deliveryDispatchKind,
				model.NewID(), pendingRecord(model.NewID(), rootID, rootID, 3, 0, "skip-generation")); err == nil {
				t.Fatal("dispatch lineage accepted generation 1 -> 3 skip")
			}

			generation2ID := model.NewID()
			generation2 := createDispatch(generation2ID, rootID, rootID, 2, 0, "dispatch-2")
			if _, err := communicationCreateWithID(ctx, fixture.m, fixture.tenant, deliveryDispatchKind,
				model.NewID(), pendingRecord(model.NewID(), rootID, rootID, 2, 0, "dispatch-fork")); err == nil {
				t.Fatal("dispatch lineage accepted a second child for one predecessor")
			}
			generation2Failed, _ := finishAttempt(generation2, VerdictBroken, TransmitNotCrossed, "dispatch_failed_2")
			generation2 = supersede(generation2Failed)

			generation3ID := model.NewID()
			generation3 := createDispatch(generation3ID, rootID, generation2ID, 3, 1, "dispatch-3")
			generation3Unknown, generation3Attempt := finishAttempt(
				generation3, VerdictUnknown, TransmitUnknown, "dispatch_unknown_3",
			)
			fabricatedReconciliation := workSchemaClone(generation3Unknown)
			fabricatedReconciliation[colCommState] = string(DispatchSucceeded)
			fabricatedReconciliation[colCommLastVerdict] = string(VerdictClean)
			fabricatedReconciliation[colCommLastCode] = "provider_accepted"
			delete(fabricatedReconciliation, colCommResolutionDeadlineAt)
			fabricatedReconciliation[colCommResolutionCode] = "provider_accepted"
			fabricatedReconciliation[colCommReconciledAttemptID] = model.NewID().String()
			fabricatedReconciliation[colCommReconciledEndpointID] = endpointID.String()
			fabricatedReconciliation[colCommReconciledEndpointGeneration] = int64(1)
			fabricatedReconciliation[colCommReconciliationVerdict] = string(VerdictClean)
			fabricatedReconciliation[colCommReconciliationCode] = "accepted"
			fabricatedReconciliation[colCommReconciliationEvidenceRef] = "provider:fabricated"
			fabricatedReconciliation[colCommReconciliationObservedAt] = now
			fabricatedReconciliation[colCommProviderAcceptanceHash] = workSchemaHash("fabricated")
			fabricatedReconciliation[colCommSettledAt] = now
			if _, err := communicationUpdate(ctx, fixture.m, fixture.tenant,
				deliveryDispatchKind, fabricatedReconciliation); err == nil {
				t.Fatal("Dispatch reconciliation accepted a nonexistent DeliveryAttempt")
			}
			reconciled := workSchemaClone(generation3Unknown)
			reconciled[colCommState] = string(DispatchSucceeded)
			reconciled[colCommLastVerdict] = string(VerdictClean)
			reconciled[colCommLastCode] = "provider_accepted"
			delete(reconciled, colCommResolutionDeadlineAt)
			reconciled[colCommResolutionCode] = "provider_accepted"
			reconciled[colCommReconciledAttemptID] = generation3Attempt.String(model.ColID)
			reconciled[colCommReconciledEndpointID] = endpointID.String()
			reconciled[colCommReconciledEndpointGeneration] = int64(1)
			reconciled[colCommReconciliationVerdict] = string(VerdictClean)
			reconciled[colCommReconciliationCode] = "accepted"
			reconciled[colCommReconciliationEvidenceRef] = "provider:dispatch-3"
			reconciled[colCommReconciliationObservedAt] = now
			reconciled[colCommProviderAcceptanceHash] = workSchemaHash("provider-accepted")
			reconciled[colCommSettledAt] = now
			missingProviderAcceptance := workSchemaClone(reconciled)
			delete(missingProviderAcceptance, colCommProviderAcceptanceHash)
			if _, err := communicationUpdate(ctx, fixture.m, fixture.tenant,
				deliveryDispatchKind, missingProviderAcceptance); err == nil {
				t.Fatal("UNKNOWN Dispatch reconciliation accepted without provider acceptance hash")
			}
			updatedReconciled, err := communicationUpdate(
				ctx, fixture.m, fixture.tenant, deliveryDispatchKind, reconciled,
			)
			if err != nil {
				t.Fatalf("reconcile UNKNOWN dispatch: %v", err)
			}
			reconciled = updatedReconciled

			deadlineRootID := model.NewID()
			deadlineRoot := createDispatch(deadlineRootID, deadlineRootID, "", 1, 0, "dispatch-deadline")
			deadlineRoot, _ = finishAttempt(deadlineRoot, VerdictBroken, TransmitNotCrossed, "dispatch_deadline")
			wantDeadline := deadlineRoot.String(colCommResolutionDeadlineAt)

			if err := fixture.st.Close(); err != nil {
				t.Fatalf("close before dispatch durability check: %v", err)
			}
			reopenedModule := New()
			reopened, err := engine.Open(ctx, store.Config{
				Engine: backend.engineName, DSN: backend.dsn, Debug: true,
				Clock: &testClock{now: communicationSchemaNow()},
			}, reopenedModule.RegisterSchema)
			if err != nil {
				t.Fatalf("reopen %s: %v", backend.name, err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			reopenedModule.UseData(api.NewModuleData(reopened))
			if err := reopenedModule.data.View(ctx, fixture.tenant, func(sc store.Scope) error {
				repo, err := sc.Ext(deliveryDispatchKind)
				if err != nil {
					return err
				}
				got, err := repo.Get(ctx, model.ID(reconciled.String(model.ColID)))
				if err != nil {
					return err
				}
				if got.Int(colCommDispatchGeneration) != 3 || got.Int(colCommRerouteRung) != 1 ||
					got.String(colCommReconciliationEvidenceRef) != "provider:dispatch-3" ||
					!reflect.DeepEqual(got.Bytes(colCommProviderAcceptanceHash), workSchemaHash("provider-accepted")) {
					return fmt.Errorf("reconciled dispatch lost durable route/evidence: %v", got)
				}
				failed, err := repo.Get(ctx, model.ID(deadlineRoot.String(model.ColID)))
				if err != nil {
					return err
				}
				if failed.String(colCommResolutionDeadlineAt) != wantDeadline {
					return errors.New("failed dispatch lost resolution deadline after reopen")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type communicationSchemaCredentialSource struct{}

func (communicationSchemaCredentialSource) Mint(
	context.Context,
	CommunicationSessionCredentialRequest,
) (CommunicationSessionCredential, error) {
	return CommunicationSessionCredential{}, errors.New("not called")
}

func (communicationSchemaCredentialSource) Renew(
	context.Context,
	model.ID,
	CommunicationSessionCredentialRequest,
) (time.Time, error) {
	return time.Time{}, errors.New("not called")
}

func (communicationSchemaCredentialSource) Revoke(
	context.Context,
	model.ID,
	CommunicationSessionCredentialRequest,
) error {
	return errors.New("not called")
}

func TestCommunicationK3ReadinessIsTwoPhase(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "readiness.db")
	m := New()
	st, err := engine.Open(ctx, store.Config{
		Engine: store.EngineSQLite, DSN: dsn, Debug: true,
	}, m.RegisterSchema)
	if err != nil {
		t.Fatalf("open schema-complete store: %v", err)
	}
	defer st.Close() //nolint:errcheck

	statuser, ok := st.(store.DirectoryStatuser)
	if !ok {
		t.Fatal("store does not expose the K3 directory readiness witness")
	}
	status, supported, err := statuser.DirectoryStatus(ctx)
	if err != nil || !supported {
		t.Fatalf("directory status = supported %v, err %v", supported, err)
	}
	if status.Enabled {
		t.Fatal("Slice F schema alone enabled K3 before composition readiness")
	}
	if m.CommunicationSessionCredentialsEnabled() {
		t.Fatal("new module starts with communication credentials enabled")
	}
	m.UseCommunicationSessionCredentialSource(communicationSchemaCredentialSource{})
	if m.CommunicationSessionCredentialsEnabled() {
		t.Fatal("late-binding only the issuer enabled K3")
	}
}

func communicationDescriptor(
	t *testing.T,
	reg *communicationSchemaCaptureRegistry,
	kind model.Kind,
) model.EntityDescriptor {
	t.Helper()
	for _, descriptor := range reg.descriptors {
		if descriptor.Kind == kind {
			return descriptor
		}
	}
	t.Fatalf("descriptor %s not registered", kind)
	return model.EntityDescriptor{}
}

func communicationHasIndex(
	descriptor model.EntityDescriptor,
	unique bool,
	columns ...string,
) bool {
	for _, index := range descriptor.Indexes {
		if index.Unique == unique && reflect.DeepEqual(index.Columns, columns) {
			return true
		}
	}
	return false
}

func communicationSchemaNow() time.Time {
	return time.Date(2026, time.August, 15, 12, 0, 0, 0, time.UTC)
}

func TestCommunicationSchemaParity(t *testing.T) {
	t.Parallel()

	reg := communicationCaptureSchema(t)

	t.Run("sqlite", func(t *testing.T) {
		freshDSN := filepath.Join(t.TempDir(), "fresh.db")
		upgradeDSN := filepath.Join(t.TempDir(), "upgrade.db")
		communicationOpenAndClose(t, store.EngineSQLite, freshDSN, New().RegisterSchema)
		communicationOpenAndClose(t, store.EngineSQLite, upgradeDSN, communicationK2Registration(t, reg))
		communicationAssertMigrationTip(t, store.EngineSQLite, upgradeDSN, 32)
		communicationOpenAndClose(t, store.EngineSQLite, upgradeDSN, New().RegisterSchema)

		fresh := communicationSQLiteCatalog(t, freshDSN)
		upgraded := communicationSQLiteCatalog(t, upgradeDSN)
		if !reflect.DeepEqual(fresh, upgraded) {
			t.Fatalf("SQLite fresh/upgrade communication catalogs differ:\nfresh:\n%s\nupgrade:\n%s",
				strings.Join(fresh, "\n"), strings.Join(upgraded, "\n"))
		}
		communicationAssertMigrationTip(t, store.EngineSQLite, upgradeDSN, 92)
	})

	if !enginetest.PostgresAvailable(t) {
		t.Run("postgres", func(t *testing.T) {
			t.Skipf("%s unset: PostgreSQL fresh/upgrade parity NOT exercised", enginetest.EnvSuperuserDSN)
		})
		return
	}
	t.Run("postgres", func(t *testing.T) {
		freshPG := enginetest.IsolatedPostgres(t)
		upgradePG := enginetest.IsolatedPostgres(t)
		communicationOpenAndClose(t, store.EnginePostgres, freshPG.App, New().RegisterSchema)
		communicationOpenAndClose(t, store.EnginePostgres, upgradePG.App, communicationK2Registration(t, reg))
		communicationAssertMigrationTip(t, store.EnginePostgres, upgradePG.App, 11)
		communicationOpenAndClose(t, store.EnginePostgres, upgradePG.App, New().RegisterSchema)

		fresh := communicationPostgresCatalog(t, freshPG.App)
		upgraded := communicationPostgresCatalog(t, upgradePG.App)
		if !reflect.DeepEqual(fresh, upgraded) {
			t.Fatalf("PostgreSQL fresh/upgrade communication catalogs differ:\nfresh:\n%s\nupgrade:\n%s",
				strings.Join(fresh, "\n"), strings.Join(upgraded, "\n"))
		}
		communicationAssertMigrationTip(t, store.EnginePostgres, upgradePG.App, 20)
	})
}

func communicationOpenAndClose(
	t *testing.T,
	engineName store.Engine,
	dsn string,
	register func(store.ExtensionRegistry) error,
) {
	t.Helper()
	st, err := engine.Open(context.Background(), store.Config{
		Engine: engineName, DSN: dsn, Debug: true,
	}, register)
	if err != nil {
		t.Fatalf("open %s: %v", engineName, err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close %s: %v", engineName, err)
	}
}

func communicationK2Registration(
	t *testing.T,
	live *communicationSchemaCaptureRegistry,
) func(store.ExtensionRegistry) error {
	t.Helper()
	if len(live.migrations) != 1 || len(live.invariants) != 1 {
		t.Fatalf("live sessions registration = %d migrations/%d invariants, want 1/1",
			len(live.migrations), len(live.invariants))
	}
	commKinds := communicationEntityByKind()
	legacyDescriptors := make([]model.EntityDescriptor, 0, len(live.descriptors)-20)
	for _, descriptor := range live.descriptors {
		if _, communication := commKinds[descriptor.Kind]; communication {
			continue
		}
		if _, protocol := protocolBindingDescriptorKinds[descriptor.Kind]; protocol {
			continue
		}
		if _, legacy := communicationLegacyDescriptorKinds[descriptor.Kind]; !legacy {
			t.Fatalf("unexpected descriptor %s cannot be classified as K2 or K3", descriptor.Kind)
		}
		legacyDescriptors = append(legacyDescriptors, descriptor)
	}
	if len(legacyDescriptors) != len(communicationLegacyDescriptorKinds) {
		t.Fatalf("legacy descriptor count = %d, want %d", len(legacyDescriptors), len(communicationLegacyDescriptorKinds))
	}
	legacyMigrations := communicationMigrationPrefix(t, live.migrations[0].fs)
	legacyInvariants := communicationK2Invariants(t, live.invariants[0].byEngine)
	rollouts := append([]store.RolloutControl(nil), live.rollouts...)

	return func(reg store.ExtensionRegistry) error {
		for _, descriptor := range legacyDescriptors {
			if err := reg.Register(descriptor); err != nil {
				return err
			}
		}
		for _, rollout := range rollouts {
			if err := reg.RolloutControl(rollout); err != nil {
				return err
			}
		}
		if err := reg.Migrations(Namespace, legacyMigrations); err != nil {
			return err
		}
		return reg.SchemaInvariants(Namespace, legacyInvariants)
	}
}

func communicationMigrationPrefix(t *testing.T, source fs.FS) fstest.MapFS {
	return communicationMigrationThrough(t, source, map[string]int{"postgres": 11, "sqlite": 32})
}

func communicationMigrationThrough(
	t *testing.T,
	source fs.FS,
	tips map[string]int,
) fstest.MapFS {
	t.Helper()
	result := fstest.MapFS{}
	for engineName, tip := range tips {
		entries, err := fs.ReadDir(source, engineName)
		if err != nil {
			t.Fatalf("read %s K2 migration source: %v", engineName, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
				continue
			}
			version, err := strconv.Atoi(strings.SplitN(entry.Name(), "_", 2)[0])
			if err != nil {
				t.Fatalf("parse migration %s: %v", entry.Name(), err)
			}
			if version > tip {
				continue
			}
			name := path.Join(engineName, entry.Name())
			body, err := fs.ReadFile(source, name)
			if err != nil {
				t.Fatalf("read migration %s: %v", name, err)
			}
			result[name] = &fstest.MapFile{Data: body, Mode: 0o444}
		}
	}
	return result
}

func communicationPreCursorReceiptRegistration(
	t *testing.T,
	live *communicationSchemaCaptureRegistry,
) func(store.ExtensionRegistry) error {
	t.Helper()
	if len(live.migrations) != 1 || len(live.invariants) != 1 {
		t.Fatalf("live sessions registration = %d migrations/%d invariants, want 1/1",
			len(live.migrations), len(live.invariants))
	}
	migrations := communicationMigrationThrough(t, live.migrations[0].fs, map[string]int{
		"postgres": 17,
		"sqlite":   84,
	})
	invariants := communicationCloneSchemaInvariants(live.invariants[0].byEngine)
	for engineName, triggers := range invariants {
		for i, trigger := range triggers {
			if engineName == store.EnginePostgres &&
				trigger.Name == "sessions_communication_command_guard" {
				trigger.DefinitionSHA256 =
					"93b8463fa70601b2c68318f3572c75e8341aae8753fa681d63cb4722f3bd396a"
				trigger.Transitions = nil
			} else if engineName == store.EngineSQLite &&
				trigger.Name == "sessions_communication_command_guard_ins" {
				trigger.DefinitionSHA256 =
					"f67652ec1ac04d9a0cc42178a450a5416059578ee13a46854235cca57f67a085"
				trigger.Transitions = nil
			}
			invariants[engineName][i] = trigger
		}
	}
	return communicationPreK5CapturedRegistration(live, migrations, invariants)
}

func communicationPreCanonicalCursorReceiptRegistration(
	t *testing.T,
	live *communicationSchemaCaptureRegistry,
) func(store.ExtensionRegistry) error {
	t.Helper()
	if len(live.migrations) != 1 || len(live.invariants) != 1 {
		t.Fatalf("live sessions registration = %d migrations/%d invariants, want 1/1",
			len(live.migrations), len(live.invariants))
	}
	migrations := communicationMigrationThrough(t, live.migrations[0].fs, map[string]int{
		"postgres": 18,
		"sqlite":   85,
	})
	invariants := communicationCloneSchemaInvariants(live.invariants[0].byEngine)
	for i, trigger := range invariants[store.EngineSQLite] {
		if trigger.Name != "sessions_communication_command_guard_ins" {
			continue
		}
		trigger.DefinitionSHA256 =
			"ba6bdd1a2e669b4b54287edf4b1c2423a4b740af317e70c0f9f7c85e26088f40"
		trigger.Transitions = trigger.Transitions[:1]
		invariants[store.EngineSQLite][i] = trigger
	}
	return communicationPreK5CapturedRegistration(live, migrations, invariants)
}

func communicationPreK5CapturedRegistration(
	live *communicationSchemaCaptureRegistry,
	migrations fs.FS,
	invariants map[store.Engine][]store.SchemaTrigger,
) func(store.ExtensionRegistry) error {
	historical := *live
	historical.descriptors = make([]model.EntityDescriptor, 0, len(live.descriptors))
	protocolTables := make(map[string]bool, len(protocolBindingDescriptorKinds))
	for _, table := range protocolBindingDescriptorKinds {
		protocolTables[table] = true
	}
	for _, descriptor := range live.descriptors {
		if _, protocol := protocolBindingDescriptorKinds[descriptor.Kind]; protocol {
			continue
		}
		historical.descriptors = append(historical.descriptors, descriptor)
	}

	historicalInvariants := make(map[store.Engine][]store.SchemaTrigger, len(invariants))
	for engineName, triggers := range invariants {
		for _, trigger := range triggers {
			if protocolTables[trigger.Table] {
				continue
			}
			historicalInvariants[engineName] = append(historicalInvariants[engineName], trigger)
		}
	}
	return communicationCapturedRegistration(&historical, migrations, historicalInvariants)
}

func communicationCloneSchemaInvariants(
	source map[store.Engine][]store.SchemaTrigger,
) map[store.Engine][]store.SchemaTrigger {
	cloned := make(map[store.Engine][]store.SchemaTrigger, len(source))
	for engineName, triggers := range source {
		cloned[engineName] = make([]store.SchemaTrigger, len(triggers))
		for i, trigger := range triggers {
			trigger.Transitions = make([]store.SchemaTriggerTransition, len(trigger.Transitions))
			for j, transition := range triggers[i].Transitions {
				trigger.Transitions[j] = transition
				if transition.PostgresFunctionIdentity != nil {
					identity := *transition.PostgresFunctionIdentity
					trigger.Transitions[j].PostgresFunctionIdentity = &identity
				}
			}
			cloned[engineName][i] = trigger
		}
	}
	return cloned
}

func communicationCapturedRegistration(
	live *communicationSchemaCaptureRegistry,
	migrations fs.FS,
	invariants map[store.Engine][]store.SchemaTrigger,
) func(store.ExtensionRegistry) error {
	descriptors := append([]model.EntityDescriptor(nil), live.descriptors...)
	rollouts := append([]store.RolloutControl(nil), live.rollouts...)
	initializers := append([]store.WorkspaceInitializer(nil), live.initializers...)
	return func(reg store.ExtensionRegistry) error {
		for _, descriptor := range descriptors {
			if err := reg.Register(descriptor); err != nil {
				return err
			}
		}
		for _, rollout := range rollouts {
			if err := reg.RolloutControl(rollout); err != nil {
				return err
			}
		}
		for _, initializer := range initializers {
			if err := reg.WorkspaceInitializer(initializer); err != nil {
				return err
			}
		}
		if err := reg.Migrations(Namespace, migrations); err != nil {
			return err
		}
		return reg.SchemaInvariants(Namespace, invariants)
	}
}

func communicationK2Invariants(
	t *testing.T,
	live map[store.Engine][]store.SchemaTrigger,
) map[store.Engine][]store.SchemaTrigger {
	t.Helper()
	legacyEventDigests := map[store.Engine]map[string]string{
		store.EnginePostgres: {
			"sessions_work_event_guard": "cc3befb97b7a35a92abc3d6c5ded62b865bf44e1d1e803ef2a1d0df065e91127",
		},
		store.EngineSQLite: {
			"sessions_work_event_guard_ins": "1cfb4914780db893087113a12b0669e50f169cd806be3c3b2539532ea6ae8701",
			"sessions_work_event_guard_upd": "f1d0623bab66af6b5f85dd12afa5340e6454e9537b3de48bdc36dbf262e97333",
		},
	}
	tables := communicationTables()
	for _, table := range protocolBindingDescriptorKinds {
		tables[table] = true
	}
	result := make(map[store.Engine][]store.SchemaTrigger, len(live))
	for _, engineName := range store.SupportedEngines() {
		for _, invariant := range live[engineName] {
			if tables[invariant.Table] {
				continue
			}
			if invariant.Table == workEventTable {
				digest, ok := legacyEventDigests[engineName][invariant.Name]
				if !ok {
					continue
				}
				invariant.DefinitionSHA256 = digest
			}
			result[engineName] = append(result[engineName], invariant)
		}
		for name, digest := range legacyEventDigests[engineName] {
			found := false
			for _, invariant := range result[engineName] {
				found = found || (invariant.Table == workEventTable && invariant.Name == name)
			}
			if !found {
				result[engineName] = append(result[engineName], store.SchemaTrigger{
					Name: name, Table: workEventTable, DefinitionSHA256: digest,
				})
			}
		}
	}
	return result
}

func communicationAssertMigrationTip(t *testing.T, engineName store.Engine, dsn string, want int) {
	t.Helper()
	driver := "sqlite"
	placeholder := "?"
	if engineName == store.EnginePostgres {
		driver, placeholder = "pgx", "$1"
	}
	raw, err := sql.Open(driver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close() //nolint:errcheck
	var got int
	if err := raw.QueryRowContext(context.Background(),
		"SELECT COALESCE(MAX(version), 0) FROM schema_migrations_mod_sessions WHERE version <= "+placeholder,
		want,
	).Scan(&got); err != nil {
		t.Fatalf("read %s sessions migration tip: %v", engineName, err)
	}
	if got != want {
		t.Fatalf("%s sessions migration tip = %d, want %d", engineName, got, want)
	}
}

func communicationSQLiteCatalog(t *testing.T, dsn string) []string {
	t.Helper()
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close() //nolint:errcheck
	tables := communicationTables()
	tables[workEventTable] = true
	rows, err := raw.QueryContext(context.Background(), `
SELECT type, name, tbl_name, COALESCE(sql, '')
FROM sqlite_schema
ORDER BY type, tbl_name, name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close() //nolint:errcheck
	var catalog []string
	for rows.Next() {
		var objectType, name, table, definition string
		if err := rows.Scan(&objectType, &name, &table, &definition); err != nil {
			t.Fatal(err)
		}
		if !tables[table] || (table == workEventTable && !strings.Contains(name, "work_event_guard")) {
			continue
		}
		catalog = append(catalog, strings.Join([]string{objectType, table, name, definition}, "\x00"))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return append(catalog, communicationMigrationLedger(t, raw)...)
}

func communicationPostgresCatalog(t *testing.T, dsn string) []string {
	t.Helper()
	raw, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close() //nolint:errcheck
	tables := communicationTables()
	tableNames := make([]string, 0, len(tables))
	for table := range tables {
		tableNames = append(tableNames, table)
	}
	sort.Strings(tableNames)
	quoted := make([]string, len(tableNames))
	for i, table := range tableNames {
		quoted[i] = "'" + table + "'"
	}
	in := strings.Join(quoted, ",")
	queries := []string{
		`SELECT 'column', table_name, column_name,
data_type || ':' || is_nullable || ':' || COALESCE(column_default, '')
FROM information_schema.columns
WHERE table_schema = current_schema() AND table_name IN (` + in + `)`,
		`SELECT 'index', tablename, indexname, indexdef
FROM pg_indexes
WHERE schemaname = current_schema() AND tablename IN (` + in + `)`,
		`SELECT 'policy', tablename, policyname,
COALESCE(qual, '') || ':' || COALESCE(with_check, '')
FROM pg_policies
WHERE schemaname = current_schema() AND tablename IN (` + in + `)`,
		`SELECT 'trigger', c.relname, t.tgname,
t.tgenabled::text || ':' || pg_catalog.pg_get_triggerdef(t.oid, false) || ':' ||
pg_catalog.pg_get_functiondef(t.tgfoid)
FROM pg_catalog.pg_trigger t
JOIN pg_catalog.pg_class c ON c.oid = t.tgrelid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = current_schema() AND NOT t.tgisinternal
AND (c.relname IN (` + in + `) OR
     (c.relname = 'sessions_work_event' AND t.tgname = 'sessions_work_event_guard'))`,
	}
	var catalog []string
	for _, query := range queries {
		rows, err := raw.QueryContext(context.Background(), query)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var objectType, table, name, definition string
			if err := rows.Scan(&objectType, &table, &name, &definition); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			catalog = append(catalog, strings.Join([]string{objectType, table, name, definition}, "\x00"))
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(catalog)
	return append(catalog, communicationMigrationLedger(t, raw)...)
}

func communicationMigrationLedger(t *testing.T, raw *sql.DB) []string {
	t.Helper()
	rows, err := raw.QueryContext(context.Background(), `
SELECT version, name, phase, COALESCE(reverted_at, '')
FROM schema_migrations_mod_sessions
ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close() //nolint:errcheck
	var ledger []string
	for rows.Next() {
		var version int
		var name, phase, reverted string
		if err := rows.Scan(&version, &name, &phase, &reverted); err != nil {
			t.Fatal(err)
		}
		ledger = append(ledger, fmt.Sprintf("migration\x00%04d\x00%s\x00%s\x00%s", version, name, phase, reverted))
	}
	return ledger
}

func communicationSQLiteTriggerDigest(t *testing.T, dsn, name string) string {
	t.Helper()
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close() //nolint:errcheck
	var definition string
	if err := raw.QueryRowContext(context.Background(),
		"SELECT sql FROM sqlite_schema WHERE type='trigger' AND name=?",
		name,
	).Scan(&definition); err != nil {
		t.Fatalf("read SQLite trigger %s: %v", name, err)
	}
	digest := sha256.Sum256([]byte(definition))
	return hex.EncodeToString(digest[:])
}

func communicationPostgresTriggerDigest(t *testing.T, dsn, table, name string) string {
	t.Helper()
	raw, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close() //nolint:errcheck
	var triggerDefinition, functionDefinition string
	if err := raw.QueryRowContext(context.Background(), `
SELECT pg_catalog.pg_get_triggerdef(t.oid, false), pg_catalog.pg_get_functiondef(t.tgfoid)
FROM pg_catalog.pg_trigger t
JOIN pg_catalog.pg_class c ON c.oid = t.tgrelid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = current_schema() AND c.relname = $1 AND t.tgname = $2
	AND NOT t.tgisinternal`, table, name).Scan(&triggerDefinition, &functionDefinition); err != nil {
		t.Fatalf("read PostgreSQL trigger %s on %s: %v", name, table, err)
	}
	definition := fmt.Sprintf("trigger:%d:%sfunction:%d:%s",
		len(triggerDefinition), triggerDefinition, len(functionDefinition), functionDefinition)
	digest := sha256.Sum256([]byte(definition))
	return hex.EncodeToString(digest[:])
}

func communicationPostgresFunctionCallerCount(t *testing.T, dsn, functionName string) int {
	t.Helper()
	raw, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close() //nolint:errcheck
	var count int
	if err := raw.QueryRowContext(context.Background(), `
SELECT count(*)
FROM pg_catalog.pg_trigger t
JOIN pg_catalog.pg_proc p ON p.oid = t.tgfoid
JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = current_schema() AND p.proname = $1 AND p.pronargs = 0
	AND NOT t.tgisinternal`, functionName).Scan(&count); err != nil {
		t.Fatalf("count PostgreSQL callers of %s: %v", functionName, err)
	}
	return count
}

func communicationPostgresFunctionExists(t *testing.T, dsn, functionName string) bool {
	t.Helper()
	raw, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close() //nolint:errcheck
	var exists bool
	if err := raw.QueryRowContext(context.Background(), `
SELECT EXISTS (
	SELECT 1 FROM pg_catalog.pg_proc p
	JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
	WHERE n.nspname = current_schema() AND p.proname = $1 AND p.pronargs = 0
)`, functionName).Scan(&exists); err != nil {
		t.Fatalf("probe PostgreSQL function %s: %v", functionName, err)
	}
	return exists
}

func communicationAssertPostgresCommandFunctionPosture(t *testing.T, dsn string) {
	t.Helper()
	if got := communicationPostgresFunctionCallerCount(t, dsn,
		"olivares_sessions_communication_validate"); got != 19 {
		t.Fatalf("old shared communication validator callers = %d, want 19 unchanged callers", got)
	}
	if got := communicationPostgresFunctionCallerCount(t, dsn,
		"olivares_sessions_communication_command_validate_v18"); got != 1 {
		t.Fatalf("v18 command validator callers = %d, want only CommunicationCommand", got)
	}
	raw, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close() //nolint:errcheck
	var canExecute, appDirectExecute, publicCanExecute bool
	if err := raw.QueryRowContext(context.Background(), `
SELECT pg_catalog.has_function_privilege(current_user, p.oid, 'EXECUTE'),
	EXISTS (
		SELECT 1 FROM pg_catalog.aclexplode(p.proacl) acl
		WHERE acl.grantee = (SELECT r.oid FROM pg_catalog.pg_roles r
			WHERE r.rolname = current_user)
			AND acl.privilege_type = 'EXECUTE' AND NOT acl.is_grantable),
	EXISTS (
		SELECT 1 FROM pg_catalog.aclexplode(p.proacl) acl
		WHERE acl.grantee = 0 AND acl.privilege_type = 'EXECUTE')
FROM pg_catalog.pg_proc p
JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = current_schema()
	AND p.proname = 'olivares_sessions_communication_command_validate_v18'
	AND p.pronargs = 0`).Scan(&canExecute, &appDirectExecute, &publicCanExecute); err != nil {
		t.Fatalf("read v18 command-validator ACL: %v", err)
	}
	if !canExecute || !appDirectExecute || publicCanExecute {
		t.Fatalf("v18 command-validator ACL = can_execute:%v app_direct:%v public:%v, want true/true/false",
			canExecute, appDirectExecute, publicCanExecute)
	}
}

func TestCommunicationSQLiteCursorReceiptMigrationRetainsV67Predicates(t *testing.T) {
	t.Parallel()

	legacy, err := fs.ReadFile(sessionsMigrationsFS,
		"migrations/sqlite/0067_communication_command_guard_ins.sql")
	if err != nil {
		t.Fatal(err)
	}
	current, err := fs.ReadFile(sessionsMigrationsFS,
		"migrations/sqlite/0085_communication_command_cursor_projection.sql")
	if err != nil {
		t.Fatal(err)
	}
	normalized := string(current)
	normalized = strings.Replace(normalized,
		"DROP TRIGGER IF EXISTS sessions_communication_command_guard_ins;\n"+
			"CREATE TRIGGER sessions_communication_command_guard_ins",
		"CREATE TRIGGER IF NOT EXISTS sessions_communication_command_guard_ins", 1)
	normalized = strings.Replace(normalized,
		"WHERE key NOT IN ('ids','version','state','counts','digests','inbox_cursor'))",
		"WHERE key NOT IN ('ids','version','state','counts','digests'))", 1)
	startMarker := "\n\t\tOR (json_type(NEW.response_projection_json,'$.inbox_cursor') IS NOT NULL"
	endMarker := "\n\t\tOR ((SELECT count(*) FROM json_each(NEW.response_projection_json,'$.ids'))"
	start := strings.Index(normalized, startMarker)
	end := strings.Index(normalized, endMarker)
	if start < 0 || end <= start {
		t.Fatalf("cannot isolate the additive v85 cursor projection block: start=%d end=%d", start, end)
	}
	normalized = normalized[:start] + normalized[end:]
	if normalized != string(legacy) {
		t.Fatal("SQLite v85 changed a pre-existing v67 CommunicationCommand predicate")
	}
}

func TestCommunicationSQLiteCursorReceiptCanonicalKeysMigrationRetainsV85Predicates(t *testing.T) {
	t.Parallel()

	legacy, err := fs.ReadFile(sessionsMigrationsFS,
		"migrations/sqlite/0085_communication_command_cursor_projection.sql")
	if err != nil {
		t.Fatal(err)
	}
	current, err := fs.ReadFile(sessionsMigrationsFS,
		"migrations/sqlite/0086_communication_command_cursor_projection_canonical_keys.sql")
	if err != nil {
		t.Fatal(err)
	}
	canonicalBlock := "\t\t\t(NEW.response_projection_json COLLATE BINARY) IS NOT (CASE\n" +
		"\t\t\t\tWHEN json_type(NEW.response_projection_json,\n" +
		"\t\t\t\t\t'$.inbox_cursor.barrier_delivery_id') IS NULL THEN\n" +
		"\t\t\t\t\t'{\"inbox_cursor\":{\"last_seen_seq\":' || json_extract(\n" +
		"\t\t\t\t\t\tNEW.response_projection_json,'$.inbox_cursor.last_seen_seq') ||\n" +
		"\t\t\t\t\t\t'},\"version\":' || json_extract(\n" +
		"\t\t\t\t\t\tNEW.response_projection_json,'$.version') || '}'\n" +
		"\t\t\t\tELSE\n" +
		"\t\t\t\t\t'{\"inbox_cursor\":{\"barrier_delivery_id\":\"' || json_extract(\n" +
		"\t\t\t\t\t\tNEW.response_projection_json,'$.inbox_cursor.barrier_delivery_id') ||\n" +
		"\t\t\t\t\t\t'\",\"barrier_reason\":\"' || json_extract(\n" +
		"\t\t\t\t\t\tNEW.response_projection_json,'$.inbox_cursor.barrier_reason') ||\n" +
		"\t\t\t\t\t\t'\",\"last_seen_seq\":' || json_extract(\n" +
		"\t\t\t\t\t\tNEW.response_projection_json,'$.inbox_cursor.last_seen_seq') ||\n" +
		"\t\t\t\t\t\t'},\"version\":' || json_extract(\n" +
		"\t\t\t\t\t\tNEW.response_projection_json,'$.version') || '}'\n" +
		"\t\t\t\tEND) COLLATE BINARY\n" +
		"\t\t\tOR "
	normalized := strings.Replace(string(current), canonicalBlock, "\t\t\t", 1)
	if normalized == string(current) {
		t.Fatal("cannot isolate the additive v86 canonical cursor projection predicate")
	}
	if normalized != string(legacy) {
		t.Fatal("SQLite v86 changed a pre-existing v85 CommunicationCommand predicate")
	}
}

func TestCommunicationSQLiteCursorReceiptCanonicalKeysMigrationFreshAndUpgrade(t *testing.T) {
	t.Parallel()

	reg := communicationCaptureSchema(t)
	freshDSN := filepath.Join(t.TempDir(), "cursor-receipt-fresh.db")
	upgradeDSN := filepath.Join(t.TempDir(), "cursor-receipt-v85.db")

	communicationOpenAndClose(t, store.EngineSQLite, freshDSN, New().RegisterSchema)
	communicationAssertMigrationTip(t, store.EngineSQLite, freshDSN, 92)

	communicationOpenAndClose(t, store.EngineSQLite, upgradeDSN,
		communicationPreCanonicalCursorReceiptRegistration(t, reg))
	communicationAssertMigrationTip(t, store.EngineSQLite, upgradeDSN, 85)
	if got := communicationSQLiteTriggerDigest(t, upgradeDSN,
		"sessions_communication_command_guard_ins"); got !=
		"ba6bdd1a2e669b4b54287edf4b1c2423a4b740af317e70c0f9f7c85e26088f40" {
		t.Fatalf("SQLite v85 CommunicationCommand digest = %s, want exact transition prestate", got)
	}
	wrongDestination := communicationCloneSchemaInvariants(reg.invariants[0].byEngine)
	for i, trigger := range wrongDestination[store.EngineSQLite] {
		if trigger.Name == "sessions_communication_command_guard_ins" {
			trigger.DefinitionSHA256 = strings.Repeat("0", sha256.Size*2)
			wrongDestination[store.EngineSQLite][i] = trigger
		}
	}
	failed, err := engine.Open(context.Background(), store.Config{
		Engine: store.EngineSQLite, DSN: upgradeDSN, Debug: true,
	}, communicationCapturedRegistration(reg, reg.migrations[0].fs, wrongDestination))
	if failed != nil {
		_ = failed.Close()
	}
	if !errors.Is(err, store.ErrSchemaTriggerTampered) {
		t.Fatalf("SQLite v86 wrong destination digest = %v, want ErrSchemaTriggerTampered", err)
	}
	communicationAssertMigrationTip(t, store.EngineSQLite, upgradeDSN, 85)
	if got := communicationSQLiteTriggerDigest(t, upgradeDSN,
		"sessions_communication_command_guard_ins"); got !=
		"ba6bdd1a2e669b4b54287edf4b1c2423a4b740af317e70c0f9f7c85e26088f40" {
		t.Fatalf("failed SQLite v86 left trigger digest %s, want rolled-back v85 prestate", got)
	}
	raw, err := sql.Open("sqlite", upgradeDSN)
	if err != nil {
		t.Fatal(err)
	}
	var v86Receipts int
	if err := raw.QueryRowContext(context.Background(),
		"SELECT count(*) FROM schema_migrations_mod_sessions WHERE version=86",
	).Scan(&v86Receipts); err != nil {
		_ = raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	if v86Receipts != 0 {
		t.Fatalf("failed SQLite v86 migration receipts = %d, want atomic rollback", v86Receipts)
	}

	communicationOpenAndClose(t, store.EngineSQLite, upgradeDSN, New().RegisterSchema)
	communicationAssertMigrationTip(t, store.EngineSQLite, upgradeDSN, 92)
	if got := communicationSQLiteTriggerDigest(t, upgradeDSN,
		"sessions_communication_command_guard_ins"); got !=
		"7946feb2f27f444cdc85601690df5ce9e19248dd114db9490f1bb7cee4f583f2" {
		t.Fatalf("SQLite v86 CommunicationCommand digest = %s, want measured destination", got)
	}

	fresh := communicationSQLiteCatalog(t, freshDSN)
	upgraded := communicationSQLiteCatalog(t, upgradeDSN)
	if !reflect.DeepEqual(fresh, upgraded) {
		t.Fatalf("SQLite canonical cursor receipt fresh/v85-upgrade catalogs differ:\nfresh:\n%s\nupgrade:\n%s",
			strings.Join(fresh, "\n"), strings.Join(upgraded, "\n"))
	}
}

func TestCommunicationPostgresCursorReceiptMigrationFreshAndUpgrade(t *testing.T) {
	t.Parallel()

	if !enginetest.PostgresAvailable(t) {
		t.Skipf("%s unset: PostgreSQL cursor-receipt migration NOT exercised",
			enginetest.EnvSuperuserDSN)
	}
	reg := communicationCaptureSchema(t)
	freshPG := enginetest.IsolatedPostgres(t)
	upgradePG := enginetest.IsolatedPostgres(t)

	communicationOpenAndClose(t, store.EnginePostgres, freshPG.App, New().RegisterSchema)
	communicationAssertMigrationTip(t, store.EnginePostgres, freshPG.App, 20)
	communicationAssertPostgresCommandFunctionPosture(t, freshPG.App)

	communicationOpenAndClose(t, store.EnginePostgres, upgradePG.App,
		communicationPreCursorReceiptRegistration(t, reg))
	communicationAssertMigrationTip(t, store.EnginePostgres, upgradePG.App, 17)
	if got := communicationPostgresTriggerDigest(t, upgradePG.App,
		communicationCommandTable, "sessions_communication_command_guard"); got !=
		"93b8463fa70601b2c68318f3572c75e8341aae8753fa681d63cb4722f3bd396a" {
		t.Fatalf("PostgreSQL v17 CommunicationCommand digest = %s, want exact transition prestate", got)
	}
	if got := communicationPostgresFunctionCallerCount(t, upgradePG.App,
		"olivares_sessions_communication_validate"); got != 20 {
		t.Fatalf("PostgreSQL v17 shared validator callers = %d, want 20", got)
	}
	if communicationPostgresFunctionExists(t, upgradePG.App,
		"olivares_sessions_communication_command_validate_v18") {
		t.Fatal("PostgreSQL v17 unexpectedly contains the reserved v18 function identity")
	}

	wrongDestination := communicationCloneSchemaInvariants(reg.invariants[0].byEngine)
	for i, trigger := range wrongDestination[store.EnginePostgres] {
		if trigger.Name == "sessions_communication_command_guard" {
			trigger.DefinitionSHA256 = strings.Repeat("0", sha256.Size*2)
			wrongDestination[store.EnginePostgres][i] = trigger
		}
	}
	failed, err := engine.Open(context.Background(), store.Config{
		Engine: store.EnginePostgres, DSN: upgradePG.App, Debug: true,
	}, communicationCapturedRegistration(reg, reg.migrations[0].fs, wrongDestination))
	if failed != nil {
		_ = failed.Close()
	}
	if !errors.Is(err, store.ErrSchemaTriggerTampered) {
		t.Fatalf("PostgreSQL v18 wrong destination digest = %v, want ErrSchemaTriggerTampered", err)
	}
	communicationAssertMigrationTip(t, store.EnginePostgres, upgradePG.App, 17)
	if got := communicationPostgresTriggerDigest(t, upgradePG.App,
		communicationCommandTable, "sessions_communication_command_guard"); got !=
		"93b8463fa70601b2c68318f3572c75e8341aae8753fa681d63cb4722f3bd396a" {
		t.Fatalf("failed PostgreSQL v18 left trigger digest %s, want rolled-back v17 prestate", got)
	}
	if got := communicationPostgresFunctionCallerCount(t, upgradePG.App,
		"olivares_sessions_communication_command_validate_v18"); got != 0 {
		t.Fatalf("failed PostgreSQL v18 left %d callers of rolled-back next function", got)
	}
	if communicationPostgresFunctionExists(t, upgradePG.App,
		"olivares_sessions_communication_command_validate_v18") {
		t.Fatal("failed PostgreSQL v18 left the reserved next function behind")
	}
	if got := communicationPostgresFunctionCallerCount(t, upgradePG.App,
		"olivares_sessions_communication_validate"); got != 20 {
		t.Fatalf("failed PostgreSQL v18 changed old shared-validator callers to %d, want 20", got)
	}

	communicationOpenAndClose(t, store.EnginePostgres, upgradePG.App, New().RegisterSchema)
	communicationAssertMigrationTip(t, store.EnginePostgres, upgradePG.App, 20)
	if got := communicationPostgresTriggerDigest(t, upgradePG.App,
		communicationCommandTable, "sessions_communication_command_guard"); got !=
		"fdef4326ded0968658859b969e82fd7fbfed5f5ae3501d8c9c7d7eea22f8d567" {
		t.Fatalf("PostgreSQL v18 CommunicationCommand digest = %s, want measured destination", got)
	}
	communicationAssertPostgresCommandFunctionPosture(t, upgradePG.App)

	fresh := communicationPostgresCatalog(t, freshPG.App)
	upgraded := communicationPostgresCatalog(t, upgradePG.App)
	if !reflect.DeepEqual(fresh, upgraded) {
		t.Fatalf("PostgreSQL cursor receipt fresh/v17-upgrade catalogs differ:\nfresh:\n%s\nupgrade:\n%s",
			strings.Join(fresh, "\n"), strings.Join(upgraded, "\n"))
	}
}

func TestCommunicationPostgresCursorReceiptMigrationSplitOwner(t *testing.T) {
	t.Parallel()

	if !enginetest.PostgresAvailable(t) {
		t.Skipf("%s unset: split-owner PostgreSQL cursor-receipt migration NOT exercised",
			enginetest.EnvSuperuserDSN)
	}
	pg := enginetest.IsolatedPostgresSplitOwner(t)
	st, err := engine.Open(context.Background(), store.Config{
		Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner, Debug: true,
	}, New().RegisterSchema)
	if err != nil {
		t.Fatalf("open split-owner PostgreSQL cursor-receipt schema: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close split-owner PostgreSQL cursor-receipt schema: %v", err)
	}
	communicationAssertMigrationTip(t, store.EnginePostgres, pg.App, 20)
	communicationAssertPostgresCommandFunctionPosture(t, pg.App)

	raw, err := sql.Open("pgx", pg.App)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close() //nolint:errcheck
	var appOwnsFunction bool
	if err := raw.QueryRowContext(context.Background(), `
SELECT pg_catalog.pg_get_userbyid(p.proowner) = current_user
FROM pg_catalog.pg_proc p
JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = current_schema()
	AND p.proname = 'olivares_sessions_communication_command_validate_v18'
	AND p.pronargs = 0`).Scan(&appOwnsFunction); err != nil {
		t.Fatalf("read split-owner v18 command-validator owner: %v", err)
	}
	if appOwnsFunction {
		t.Fatal("split-owner v18 command validator is owned by the application role")
	}
}

func TestCommunicationSQLiteCursorReceiptCanonicalComparisonIsByteExact(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	raw, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "cursor-collation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close() //nolint:errcheck
	if _, err := raw.ExecContext(ctx, `CREATE TABLE sessions_communication_command (
id TEXT, tenant_id TEXT, created_at TEXT, updated_at TEXT, version INTEGER,
workspace_id TEXT, command_id TEXT, actor_fingerprint BLOB, command_scope TEXT,
idempotency_key_hash BLOB, request_digest BLOB, seal_key_version TEXT,
digest_key_version TEXT, plan_hash BLOB, result_kind TEXT, result_id TEXT,
http_status INTEGER, response_projection_json TEXT COLLATE RTRIM,
response_digest BLOB, event_id TEXT, audit_seq INTEGER, audit_hash BLOB,
completed_at TEXT
);
CREATE TABLE sessions_work_event (event_id TEXT, tenant_id TEXT, workspace_id TEXT);`); err != nil {
		t.Fatalf("create SQLite canonical-collation fixture: %v", err)
	}
	migration, err := fs.ReadFile(sessionsMigrationsFS,
		"migrations/sqlite/0086_communication_command_cursor_projection_canonical_keys.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, string(migration)); err != nil {
		t.Fatalf("apply SQLite v86 canonical-collation fixture: %v", err)
	}
	canonical := `{"inbox_cursor":{"last_seen_seq":0},"version":1}`
	mutant := canonical + " "
	var collationEqual, bytesEqual bool
	if err := raw.QueryRowContext(ctx, `SELECT
(? COLLATE RTRIM) = (? COLLATE RTRIM), CAST(? AS BLOB) = CAST(? AS BLOB)`,
		canonical, mutant, canonical, mutant).Scan(&collationEqual, &bytesEqual); err != nil {
		t.Fatalf("probe SQLite RTRIM comparison: %v", err)
	}
	if !collationEqual || bytesEqual {
		t.Fatalf("SQLite RTRIM probe = collation_equal:%v bytes_equal:%v, want true/false",
			collationEqual, bytesEqual)
	}
	if err := communicationInsertCursorCanonicalityProbe(ctx, raw, store.EngineSQLite,
		"sqlite-canonical-control", canonical); err != nil {
		t.Fatalf("SQLite canonical control rejected: %v", err)
	}
	if err := communicationInsertCursorCanonicalityProbe(ctx, raw, store.EngineSQLite,
		"sqlite-rtrim-mutant", mutant); err == nil ||
		!strings.Contains(err.Error(), "invalid CommunicationCommand receipt") {
		t.Fatalf("SQLite RTRIM mutante de verificación = %v, want closed receipt rejection", err)
	}
	communicationAssertCanonicalityProbeRows(t, raw, 1)
}

func TestCommunicationPostgresCursorReceiptCanonicalComparisonIsByteExact(t *testing.T) {
	t.Parallel()

	if !enginetest.PostgresAvailable(t) {
		t.Skipf("%s unset: PostgreSQL canonical-collation comparison NOT exercised",
			enginetest.EnvSuperuserDSN)
	}
	ctx := context.Background()
	pg := enginetest.IsolatedPostgres(t)
	raw, err := sql.Open("pgx", pg.App)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close() //nolint:errcheck
	if _, err := raw.ExecContext(ctx, `CREATE COLLATION public.sessions_cursor_ignore_punct (
provider = icu, locale = 'und-u-ka-shifted-ks-level3', deterministic = false
);
CREATE TABLE public.sessions_communication_command (
id pg_catalog.text, tenant_id pg_catalog.text, created_at pg_catalog.text,
updated_at pg_catalog.text, version pg_catalog.int8, workspace_id pg_catalog.text,
command_id pg_catalog.text, actor_fingerprint pg_catalog.bytea,
command_scope pg_catalog.text, idempotency_key_hash pg_catalog.bytea,
request_digest pg_catalog.bytea, seal_key_version pg_catalog.text,
digest_key_version pg_catalog.text, plan_hash pg_catalog.bytea,
result_kind pg_catalog.text, result_id pg_catalog.text, http_status pg_catalog.int8,
response_projection_json pg_catalog.text COLLATE public.sessions_cursor_ignore_punct,
response_digest pg_catalog.bytea, event_id pg_catalog.text, audit_seq pg_catalog.int8,
audit_hash pg_catalog.bytea, completed_at pg_catalog.text
);
CREATE TABLE public.sessions_work_event (
event_id pg_catalog.text, tenant_id pg_catalog.text, workspace_id pg_catalog.text
);
CREATE FUNCTION public.olivares_sessions_communication_validate() RETURNS trigger
LANGUAGE plpgsql AS $legacy$ BEGIN RETURN NEW; END $legacy$;
CREATE TRIGGER sessions_communication_command_guard BEFORE INSERT OR UPDATE
ON public.sessions_communication_command FOR EACH ROW
EXECUTE FUNCTION public.olivares_sessions_communication_validate();`); err != nil {
		t.Fatalf("create PostgreSQL canonical-collation fixture: %v", err)
	}
	migration, err := fs.ReadFile(sessionsMigrationsFS,
		"migrations/postgres/0018_communication_command_cursor_projection.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, string(migration)); err != nil {
		t.Fatalf("apply PostgreSQL v18 canonical-collation fixture: %v", err)
	}
	canonical := `{"inbox_cursor":{"last_seen_seq":0},"version":1}`
	mutant := `{"inbox_cursor": {"last_seen_seq":0},"version":1}`
	var collationEqual, bytesEqual bool
	if err := raw.QueryRowContext(ctx, `SELECT
($1::pg_catalog.text COLLATE public.sessions_cursor_ignore_punct) =
	($2::pg_catalog.text COLLATE public.sessions_cursor_ignore_punct),
pg_catalog.convert_to($1::pg_catalog.text, 'UTF8') =
	pg_catalog.convert_to($2::pg_catalog.text, 'UTF8')`,
		canonical, mutant).Scan(&collationEqual, &bytesEqual); err != nil {
		t.Fatalf("probe PostgreSQL nondeterministic collation comparison: %v", err)
	}
	if !collationEqual || bytesEqual {
		t.Fatalf("PostgreSQL collation probe = collation_equal:%v bytes_equal:%v, want true/false",
			collationEqual, bytesEqual)
	}
	if err := communicationInsertCursorCanonicalityProbe(ctx, raw, store.EnginePostgres,
		"postgres-canonical-control", canonical); err != nil {
		t.Fatalf("PostgreSQL canonical control rejected: %v", err)
	}
	if err := communicationInsertCursorCanonicalityProbe(ctx, raw, store.EnginePostgres,
		"postgres-collation-mutant", mutant); err == nil ||
		!strings.Contains(err.Error(), "invalid CommunicationCommand receipt") {
		t.Fatalf("PostgreSQL collation mutante de verificación = %v, want closed receipt rejection", err)
	}
	communicationAssertCanonicalityProbeRows(t, raw, 1)
}

func communicationInsertCursorCanonicalityProbe(
	ctx context.Context,
	raw *sql.DB,
	engineName store.Engine,
	label string,
	projection string,
) error {
	placeholders := make([]string, 23)
	for i := range placeholders {
		placeholders[i] = "?"
		if engineName == store.EnginePostgres {
			placeholders[i] = fmt.Sprintf("$%d", i+1)
		}
	}
	insert := `INSERT INTO sessions_communication_command
(id, tenant_id, created_at, updated_at, version, workspace_id, command_id,
 actor_fingerprint, command_scope, idempotency_key_hash, request_digest,
 seal_key_version, digest_key_version, plan_hash, result_kind, result_id,
 http_status, response_projection_json, response_digest, event_id, audit_seq,
 audit_hash, completed_at)
VALUES (` + strings.Join(placeholders, ", ") + `)` // #nosec G202 -- closed engine-specific placeholders
	now := model.NewTimestamp(communicationSchemaNow()).String()
	_, err := raw.ExecContext(ctx, insert,
		model.NewID().String(), model.NewID().String(), now, now, int64(1),
		model.NewID().String(), model.NewID().String(), workSchemaHash(label+"-actor"),
		"PUT:/v1/communication/inbox-cursor", workSchemaHash(label+"-idempotency"),
		workSchemaHash(label+"-request"), nil, nil, workSchemaHash(label+"-plan"),
		string(inboxCursorKind), model.NewID().String(), int64(200), projection,
		workSchemaHash(label+"-response"), nil, int64(0), nil, now,
	)
	return err
}

func communicationAssertCanonicalityProbeRows(t *testing.T, raw *sql.DB, want int) {
	t.Helper()
	var got int
	if err := raw.QueryRowContext(context.Background(),
		"SELECT count(*) FROM sessions_communication_command").Scan(&got); err != nil {
		t.Fatalf("count canonical-collation fixture rows: %v", err)
	}
	if got != want {
		t.Fatalf("canonical-collation fixture rows = %d, want %d", got, want)
	}
}

func TestCommunicationCursorReceiptProjectionGuardAcrossBackends(t *testing.T) {
	t.Parallel()

	for _, backend := range communicationSchemaBackends(t) {
		backend := backend
		t.Run(backend.name, func(t *testing.T) {
			communicationTestCursorReceiptProjectionGuard(t, backend)
		})
	}
}

func communicationTestCursorReceiptProjectionGuard(t *testing.T, backend communicationSchemaBackend) {
	t.Helper()
	ctx := context.Background()
	fixture := communicationOpenFixture(t, backend)

	channel := communicationMustCreate(t, fixture, channelKind,
		communicationChannelRecord(fixture.workspace, "cursor-receipt-guard"))
	messageID := model.NewID()
	if _, err := communicationCreateWithID(ctx, fixture.m, fixture.tenant, messageKind, messageID,
		communicationStagingMessageRecord(
			fixture.workspace, model.ID(channel.String(model.ColID)), messageID, "cursor-receipt-event",
		)); err != nil {
		t.Fatalf("create cursor receipt event aggregate: %v", err)
	}
	event := communicationMustCreate(t, fixture, workEventKind, model.Record{
		colWorkWorkspaceID:    fixture.workspace.String(),
		colEventID:            model.NewID().String(),
		colEventAggregateKind: string(messageKind),
		colEventAggregateID:   messageID.String(),
		colEventSeq:           int64(1),
		colEventType:          "message.cursor_receipt_test",
		colEventActorKind:     "system",
		colEventActorRef:      "cursor-receipt-test",
		colEventOccurredAt:    model.NewTimestamp(communicationSchemaNow()).String(),
		colEventPayload:       "{}",
		colEventPayloadHash:   hashBytes([]byte("{}")),
		colEventCommandID:     model.NewID().String(),
		colEventAuditSeq:      int64(1),
		colEventAuditHash:     workSchemaHash("cursor-receipt-event-audit"),
	})
	if err := fixture.st.Close(); err != nil {
		t.Fatalf("close fixture before direct %s writes: %v", backend.name, err)
	}

	driver := "sqlite"
	if backend.engineName == store.EnginePostgres {
		driver = "pgx"
	}
	raw, err := sql.Open(driver, backend.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close() //nolint:errcheck
	placeholders := make([]string, 23)
	for i := range placeholders {
		placeholders[i] = "?"
		if backend.engineName == store.EnginePostgres {
			placeholders[i] = fmt.Sprintf("$%d", i+1)
		}
	}
	insert := `INSERT INTO sessions_communication_command
(id, tenant_id, created_at, updated_at, version, workspace_id, command_id,
 actor_fingerprint, command_scope, idempotency_key_hash, request_digest,
 seal_key_version, digest_key_version, plan_hash, result_kind, result_id,
 http_status, response_projection_json, response_digest, event_id, audit_seq,
 audit_hash, completed_at)
VALUES (` + strings.Join(placeholders, ", ") + `)` // #nosec G202 -- closed engine-specific placeholders
	execReceipt := func(args ...any) error {
		tx, err := raw.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback() //nolint:errcheck
		if backend.engineName == store.EnginePostgres {
			if _, err := tx.ExecContext(ctx,
				"SELECT pg_catalog.set_config('app.tenant_id', $1, true)",
				fixture.tenant.String(),
			); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, insert, args...); err != nil {
			return err
		}
		return tx.Commit()
	}
	type receiptCase struct {
		name                 string
		resultKind           string
		missingResult        bool
		httpStatus           int64
		projection           string
		eventID              any
		wantOK               bool
		allowJSONSyntaxError bool
	}
	barrierID := model.NewID().String()
	validCursor := `{"inbox_cursor":{"last_seen_seq":0},"version":1}`
	cases := []receiptCase{
		{name: "cursor without barrier", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: validCursor, wantOK: true},
		{name: "not yet available barrier", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: fmt.Sprintf(
				`{"inbox_cursor":{"barrier_delivery_id":%q,"barrier_reason":"not_yet_available","last_seen_seq":7},"version":2}`,
				barrierID), wantOK: true},
		{name: "temporarily invisible barrier", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: fmt.Sprintf(
				`{"inbox_cursor":{"barrier_delivery_id":%q,"barrier_reason":"temporarily_invisible","last_seen_seq":8},"version":3}`,
				model.NewID().String()), wantOK: true},
		{name: "cursor integer bounds are representable", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: `{"inbox_cursor":{"last_seen_seq":9223372036854775807},"version":9223372036854775807}`,
			wantOK:     true},
		{name: "ordinary receipt remains valid", resultKind: string(channelKind), httpStatus: 201,
			projection: `{}`, wantOK: true},
		{name: "ordinary legacy receipt may retain JSON whitespace", resultKind: string(channelKind), httpStatus: 201,
			projection: `{"version": 1}`, wantOK: true},
		{name: "ordinary legacy receipt may retain escaped string value", resultKind: string(channelKind), httpStatus: 201,
			projection: `{"state":"act\u0069ve"}`, wantOK: true},
		{name: "ordinary result carries cursor", resultKind: string(channelKind), httpStatus: 201,
			projection: validCursor},
		{name: "ordinary result carries null cursor", resultKind: string(channelKind), httpStatus: 201,
			projection: `{"inbox_cursor":null}`},
		{name: "cursor lacks result id", resultKind: string(inboxCursorKind), missingResult: true,
			httpStatus: 200, projection: validCursor},
		{name: "cursor status is not 200", resultKind: string(inboxCursorKind), httpStatus: 201,
			projection: validCursor},
		{name: "cursor carries event", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: validCursor, eventID: event.String(colEventID)},
		{name: "cursor lacks projection version", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: `{"inbox_cursor":{"last_seen_seq":0}}`},
		{name: "cursor projection version is zero", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: `{"inbox_cursor":{"last_seen_seq":0},"version":0}`},
		{name: "cursor projection version is null", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: `{"inbox_cursor":{"last_seen_seq":0},"version":null}`},
		{name: "cursor projection version is fractional", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: `{"inbox_cursor":{"last_seen_seq":0},"version":1.5}`},
		{name: "cursor projection version exceeds int64", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: `{"inbox_cursor":{"last_seen_seq":0},"version":9223372036854775808}`},
		{name: "cursor projection is malformed JSON", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: `{"version":1`, allowJSONSyntaxError: true},
		{name: "cursor projection is absent", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: `{"version":1}`},
		{name: "cursor projection is null", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: `{"inbox_cursor":null,"version":1}`},
		{name: "cursor projection is an array", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: `{"inbox_cursor":[],"version":1}`},
		{name: "last seen sequence is absent", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: `{"inbox_cursor":{},"version":1}`},
		{name: "last seen sequence is negative", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: `{"inbox_cursor":{"last_seen_seq":-1},"version":1}`},
		{name: "last seen sequence is text", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: `{"inbox_cursor":{"last_seen_seq":"0"},"version":1}`},
		{name: "last seen sequence is fractional", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: `{"inbox_cursor":{"last_seen_seq":0.5},"version":1}`},
		{name: "last seen sequence exceeds int64", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: `{"inbox_cursor":{"last_seen_seq":9223372036854775808},"version":1}`},
		{name: "cursor projection has unknown key", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: `{"inbox_cursor":{"last_seen_seq":0,"next_after":1},"version":1}`},
		{name: "cursor projection duplicates sequence", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: `{"inbox_cursor":{"last_seen_seq":0,"last_seen_seq":1},"version":1}`},
		{name: "cursor projection triples sequence", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: `{"inbox_cursor":{"last_seen_seq":0,"last_seen_seq":1,"last_seen_seq":2},"version":1}`},
		{name: "barrier id lacks reason", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: fmt.Sprintf(
				`{"inbox_cursor":{"barrier_delivery_id":%q,"last_seen_seq":0},"version":1}`, barrierID)},
		{name: "barrier reason lacks id", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: `{"inbox_cursor":{"barrier_reason":"not_yet_available","last_seen_seq":0},"version":1}`},
		{name: "barrier id is UUIDv4", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: `{"inbox_cursor":{"barrier_delivery_id":"00000000-0000-4000-8000-000000000001","barrier_reason":"not_yet_available","last_seen_seq":0},"version":1}`},
		{name: "barrier id is uppercase", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: fmt.Sprintf(
				`{"inbox_cursor":{"barrier_delivery_id":%q,"barrier_reason":"not_yet_available","last_seen_seq":0},"version":1}`,
				strings.ToUpper(barrierID))},
		{name: "barrier reason is unknown", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: fmt.Sprintf(
				`{"inbox_cursor":{"barrier_delivery_id":%q,"barrier_reason":"blocked","last_seen_seq":0},"version":1}`, barrierID)},
		{name: "barrier pair is null", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: `{"inbox_cursor":{"barrier_delivery_id":null,"barrier_reason":null,"last_seen_seq":0},"version":1}`},
		{name: "cursor carries ids", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: `{"ids":{},"inbox_cursor":{"last_seen_seq":0},"version":1}`},
		{name: "cursor carries state", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: `{"inbox_cursor":{"last_seen_seq":0},"state":"active","version":1}`},
		{name: "cursor carries counts", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: `{"counts":{},"inbox_cursor":{"last_seen_seq":0},"version":1}`},
		{name: "cursor carries digests", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: `{"digests":{},"inbox_cursor":{"last_seen_seq":0},"version":1}`},
		{name: "cursor carries a third top-level key", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: `{"inbox_cursor":{"last_seen_seq":0},"unknown":null,"version":1}`},
		{name: "cursor duplicates top-level version", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: `{"inbox_cursor":{"last_seen_seq":0},"version":1,"version":2}`},
		{name: "cursor aliases top-level version with JSON escape", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: `{"inbox_cursor":{"last_seen_seq":0},"\u0076ersion":1}`},
		{name: "cursor aliases top-level projection key with JSON escape", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: `{"\u0069nbox_cursor":{"last_seen_seq":0},"version":1}`},
		{name: "cursor aliases nested sequence key with JSON escape", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: `{"inbox_cursor":{"\u006cast_seen_seq":0},"version":1}`},
		{name: "cursor aliases nested barrier id key with JSON escape", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: fmt.Sprintf(
				`{"inbox_cursor":{"\u0062arrier_delivery_id":%q,"barrier_reason":"not_yet_available","last_seen_seq":0},"version":1}`,
				barrierID)},
		{name: "cursor aliases nested barrier reason key with JSON escape", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: fmt.Sprintf(
				`{"inbox_cursor":{"barrier_delivery_id":%q,"barrier_\u0072eason":"not_yet_available","last_seen_seq":0},"version":1}`,
				barrierID)},
		{name: "cursor barrier value uses a JSON escape", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: fmt.Sprintf(
				`{"inbox_cursor":{"barrier_delivery_id":%q,"barrier_reason":"not_yet_\u0061vailable","last_seen_seq":0},"version":1}`,
				barrierID)},
		{name: "cursor aliases every required key with JSON escapes", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: `{"\u0069nbox_cursor":{"\u006cast_seen_seq":0},"\u0076ersion":1}`},
		{name: "cursor top-level keys are reordered", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: `{"version":1,"inbox_cursor":{"last_seen_seq":0}}`},
		{name: "cursor barrier keys are reordered", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: fmt.Sprintf(
				`{"inbox_cursor":{"last_seen_seq":0,"barrier_delivery_id":%q,"barrier_reason":"not_yet_available"},"version":1}`,
				barrierID)},
		{name: "cursor projection contains insignificant whitespace", resultKind: string(inboxCursorKind), httpStatus: 200,
			projection: `{"inbox_cursor": {"last_seen_seq":0},"version":1}`},
	}
	successes := 0
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resultID := any(model.NewID().String())
			if tc.resultKind == string(channelKind) {
				resultID = channel.String(model.ColID)
			}
			if tc.missingResult {
				resultID = nil
			}
			now := model.NewTimestamp(communicationSchemaNow()).String()
			err := execReceipt(
				model.NewID().String(), fixture.tenant.String(), now, now, int64(1),
				fixture.workspace.String(), model.NewID().String(),
				workSchemaHash("cursor-receipt-actor"), "PUT:/v1/communication/inbox-cursor",
				workSchemaHash("cursor-receipt-idempotency-"+tc.name),
				workSchemaHash("cursor-receipt-request-"+tc.name), nil, nil,
				workSchemaHash("cursor-receipt-plan-"+tc.name), tc.resultKind, resultID,
				tc.httpStatus, tc.projection,
				workSchemaHash("cursor-receipt-response-"+tc.name), tc.eventID, int64(1),
				workSchemaHash("cursor-receipt-audit-"+tc.name), now,
			)
			if tc.wantOK {
				if err != nil {
					t.Fatalf("valid direct receipt rejected: %v", err)
				}
				successes++
				return
			}
			if err == nil {
				t.Fatal("invalid direct receipt persisted")
			}
			if !strings.Contains(err.Error(), "invalid CommunicationCommand receipt") &&
				!(tc.allowJSONSyntaxError && strings.Contains(strings.ToLower(err.Error()), "malformed json")) {
				t.Fatalf("invalid direct receipt failed outside the closed projection guard: %v", err)
			}
		})
	}
	var got int
	tx, err := raw.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck
	if backend.engineName == store.EnginePostgres {
		if _, err := tx.ExecContext(ctx,
			"SELECT pg_catalog.set_config('app.tenant_id', $1, true)",
			fixture.tenant.String(),
		); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sessions_communication_command
WHERE command_scope = 'PUT:/v1/communication/inbox-cursor'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != successes {
		t.Fatalf("direct cursor receipt rows = %d, want exactly %d valid cases", got, successes)
	}
}

func TestCommunicationSchemaInvariantCatalogDigests(t *testing.T) {
	t.Parallel()

	reg := communicationCaptureSchema(t)
	declared := communicationInvariantSubset(reg.invariants[0].byEngine)
	if got := len(declared[store.EnginePostgres]); got != 36 {
		t.Fatalf("PostgreSQL F invariants = %d, want 20 guards + 15 no-delete + 1 dual event", got)
	}
	if got := len(declared[store.EngineSQLite]); got != 51 {
		t.Fatalf("SQLite F invariants = %d, want 35 guards + 15 no-delete + 1 dual event", got)
	}

	t.Run("sqlite", func(t *testing.T) {
		dsn := filepath.Join(t.TempDir(), "invariant-digests.db")
		communicationOpenAndClose(t, store.EngineSQLite, dsn, New().RegisterSchema)
		communicationAssertLiveInvariantDigests(t, store.EngineSQLite, dsn, declared[store.EngineSQLite])

		raw, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatal(err)
		}
		const trigger = "sessions_channel_guard_ins"
		if _, err := raw.ExecContext(context.Background(), "DROP TRIGGER "+trigger); err != nil {
			_ = raw.Close()
			t.Fatalf("drop %s: %v", trigger, err)
		}
		if _, err := raw.ExecContext(context.Background(), `CREATE TRIGGER sessions_channel_guard_ins
BEFORE INSERT ON sessions_channel BEGIN SELECT 1; END`); err != nil {
			_ = raw.Close()
			t.Fatalf("install same-name no-op trigger: %v", err)
		}
		if err := raw.Close(); err != nil {
			t.Fatal(err)
		}
		st, err := engine.Open(context.Background(), store.Config{
			Engine: store.EngineSQLite, DSN: dsn, Debug: true,
		}, New().RegisterSchema)
		if st != nil {
			_ = st.Close()
		}
		if !errors.Is(err, store.ErrSchemaTriggerTampered) {
			t.Fatalf("open with same-name no-op communication trigger = %v, want ErrSchemaTriggerTampered", err)
		}
	})

	if !enginetest.PostgresAvailable(t) {
		t.Run("postgres", func(t *testing.T) {
			t.Skipf("%s unset: PostgreSQL communication digest catalog NOT exercised", enginetest.EnvSuperuserDSN)
		})
		return
	}
	t.Run("postgres", func(t *testing.T) {
		pg := enginetest.IsolatedPostgres(t)
		communicationOpenAndClose(t, store.EnginePostgres, pg.App, New().RegisterSchema)
		communicationAssertLiveInvariantDigests(t, store.EnginePostgres, pg.App, declared[store.EnginePostgres])
	})
}

func communicationInvariantSubset(
	all map[store.Engine][]store.SchemaTrigger,
) map[store.Engine][]store.SchemaTrigger {
	tables := communicationTables()
	result := make(map[store.Engine][]store.SchemaTrigger, len(all))
	for engineName, invariants := range all {
		for _, invariant := range invariants {
			dualEvent := invariant.Table == workEventTable &&
				(invariant.Name == "sessions_work_event_guard" ||
					invariant.Name == "sessions_work_event_guard_ins")
			if tables[invariant.Table] || dualEvent {
				result[engineName] = append(result[engineName], invariant)
			}
		}
	}
	return result
}

func communicationAssertLiveInvariantDigests(
	t *testing.T,
	engineName store.Engine,
	dsn string,
	invariants []store.SchemaTrigger,
) {
	t.Helper()
	driver := "sqlite"
	if engineName == store.EnginePostgres {
		driver = "pgx"
	}
	raw, err := sql.Open(driver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close() //nolint:errcheck
	for _, invariant := range invariants {
		var definition string
		if engineName == store.EngineSQLite {
			if err := raw.QueryRowContext(context.Background(),
				"SELECT sql FROM sqlite_schema WHERE type='trigger' AND tbl_name=? AND name=?",
				invariant.Table, invariant.Name,
			).Scan(&definition); err != nil {
				t.Fatalf("read SQLite invariant (%s,%s): %v", invariant.Table, invariant.Name, err)
			}
		} else {
			var triggerDefinition, functionDefinition string
			if err := raw.QueryRowContext(context.Background(), `
SELECT pg_catalog.pg_get_triggerdef(t.oid, false), pg_catalog.pg_get_functiondef(t.tgfoid)
FROM pg_catalog.pg_trigger t
JOIN pg_catalog.pg_class c ON c.oid = t.tgrelid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = current_schema() AND c.relname = $1 AND t.tgname = $2 AND NOT t.tgisinternal`,
				invariant.Table, invariant.Name,
			).Scan(&triggerDefinition, &functionDefinition); err != nil {
				t.Fatalf("read PostgreSQL invariant (%s,%s): %v", invariant.Table, invariant.Name, err)
			}
			definition = fmt.Sprintf("trigger:%d:%sfunction:%d:%s",
				len(triggerDefinition), triggerDefinition, len(functionDefinition), functionDefinition)
		}
		digest := sha256.Sum256([]byte(definition))
		got := hex.EncodeToString(digest[:])
		if got != invariant.DefinitionSHA256 {
			t.Errorf("%s invariant (%s,%s) live digest = %s, declared %s",
				engineName, invariant.Table, invariant.Name, got, invariant.DefinitionSHA256)
		}
	}
}

type communicationSchemaBackend struct {
	name       string
	engineName store.Engine
	dsn        string
}

func communicationSchemaBackends(t *testing.T) []communicationSchemaBackend {
	t.Helper()
	result := []communicationSchemaBackend{{
		name: "sqlite", engineName: store.EngineSQLite,
		dsn: filepath.Join(t.TempDir(), "communication.db"),
	}}
	if enginetest.PostgresAvailable(t) {
		pg := enginetest.IsolatedPostgres(t)
		result = append(result, communicationSchemaBackend{
			name: "postgres", engineName: store.EnginePostgres, dsn: pg.App,
		})
	} else {
		t.Logf("%s unset: PostgreSQL communication backend NOT exercised", enginetest.EnvSuperuserDSN)
	}
	return result
}

type communicationSchemaFixture struct {
	m         *Module
	st        store.Store
	tenant    model.TenantID
	workspace model.ID
}

func communicationOpenFixture(t *testing.T, backend communicationSchemaBackend) communicationSchemaFixture {
	return communicationOpenFixtureWithClock(t, backend, &testClock{now: communicationSchemaNow()})
}

func communicationOpenFixtureWithClock(
	t *testing.T,
	backend communicationSchemaBackend,
	clock model.Clock,
) communicationSchemaFixture {
	t.Helper()
	ctx := context.Background()
	m := New()
	st, err := engine.Open(ctx, store.Config{
		Engine: backend.engineName, DSN: backend.dsn, Debug: true, Clock: clock,
	}, m.RegisterSchema)
	if err != nil {
		t.Fatalf("open %s: %v", backend.name, err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, err := sys.CreateOrg(ctx, model.Org{
			Name: "Communication schema", Slug: "communication-schema", Status: model.StatusActive,
		})
		if err == nil {
			tenant = org.TenantID
		}
		return err
	}); err != nil {
		t.Fatalf("create %s tenant: %v", backend.name, err)
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
	return communicationSchemaFixture{m: m, st: st, tenant: tenant, workspace: workspace}
}

func TestCommunicationTransactionTimeStampsDomainEffectsAcrossBackends(t *testing.T) {
	t.Parallel()

	for _, backend := range communicationSchemaBackends(t) {
		backend := backend
		t.Run(backend.name, func(t *testing.T) {
			fixture := communicationOpenFixtureWithClock(t, backend, nil)
			ctx := context.Background()
			channel := communicationMustCreate(t, fixture, channelKind,
				communicationChannelRecord(fixture.workspace, "transaction-stamp"))
			resultID := model.ID(channel.String(model.ColID))

			unstampedErr := fixture.m.data.Mutate(ctx, fixture.tenant, func(sc store.Scope) error {
				clock, ok := sc.(store.TransactionClock)
				if !ok {
					return errors.New("communication scope has no TransactionClock")
				}
				dbNow, err := clock.TransactionNow(ctx)
				if err != nil {
					return err
				}
				confined, err := store.ConfineWorkspace(ctx, sc, fixture.workspace)
				if err != nil {
					return err
				}
				repo, err := confined.Ext(communicationCommandKind)
				if err != nil {
					return err
				}
				record := communicationCommandRecord(fixture.workspace, resultID)
				record[colCommCompletedAt] = dbNow.String()
				_, err = repo.Create(ctx, record)
				return err
			})
			if unstampedErr == nil {
				t.Fatal("ordinary GenericRepo write silently substituted process time for DB time")
			}

			if err := fixture.m.data.Mutate(ctx, fixture.tenant, func(sc store.Scope) error {
				clock, ok := sc.(store.TransactionClock)
				if !ok {
					return errors.New("communication scope has no TransactionClock")
				}
				dbNow, err := clock.TransactionNow(ctx)
				if err != nil {
					return err
				}
				confined, err := store.ConfineWorkspace(ctx, sc, fixture.workspace)
				if err != nil {
					return err
				}
				repo, err := confined.Ext(communicationCommandKind)
				if err != nil {
					return err
				}
				stamped, ok := repo.(store.TransactionStampedGenericRepo)
				if !ok {
					return errors.New("communication GenericRepo lacks transaction-stamped writes")
				}
				record := communicationCommandRecord(fixture.workspace, resultID)
				record[colCommCommandID] = model.NewID().String()
				record[colCommIdempotencyKeyHash] = workSchemaHash("transaction-stamped-command")
				record[colCommCompletedAt] = dbNow.String()
				created, err := stamped.CreateAtTransactionTime(ctx, record)
				if err != nil {
					return err
				}
				if created.String(model.ColCreatedAt) != dbNow.String() ||
					created.String(model.ColUpdatedAt) != dbNow.String() ||
					created.String(colCommCompletedAt) != dbNow.String() {
					return fmt.Errorf("transaction-stamped Command times = %s/%s/%s, want %s",
						created.String(model.ColCreatedAt), created.String(model.ColUpdatedAt),
						created.String(colCommCompletedAt), dbNow.String())
				}
				return nil
			}); err != nil {
				t.Fatalf("transaction-stamped CommunicationCommand: %v", err)
			}
		})
	}
}

func communicationChannelRecord(workspace model.ID, seed string) model.Record {
	return model.Record{
		colWorkWorkspaceID: workspace.String(), colCommSlug: "channel-" + seed,
		colCommName: "Channel " + seed, colCommKind: string(ChannelCoordination),
		colCommState: string(ChannelActive), colCommSensitivity: string(ChannelInternal),
		colCommContentProtection:    string(ContentProtectionStorage),
		colCommProtectionGeneration: int64(1), colCommDefaultAckPolicy: string(AckPolicyNone),
		colCommDefaultAckTimeoutMS: int64(0), colCommDefaultWake: string(WakeNone),
		colCommMaxFanout: int64(100), colCommMaxAutomationDepth: int64(4),
		colCommACLRevision: int64(1), colCommRouteRevision: int64(1),
		colCommSubscriptionRevision: int64(1),
	}
}

func communicationStagingMessageRecord(
	workspace, channelID, messageID model.ID,
	seed string,
) model.Record {
	return model.Record{
		colWorkWorkspaceID: workspace.String(),
		colCommChannelID:   channelID.String(), colCommThreadID: messageID.String(),
		colCommKind: string(MessageNotice), colCommState: string(MessageDraft),
		colCommSenderKind: string(ActorUser), colCommSenderRef: model.NewID().String(),
		"payload_encoding": string(PayloadPlainJSON), "payload_plain_json": "{}",
		"payload_schema": "communication.message.v1", "payload_digest": workSchemaHash(seed),
		"payload_protection_generation": int64(1),
		colCommUrgency:                  string(UrgencyNormal), colCommAckPolicy: string(AckPolicyNone),
		colCommAckQuorum:       int64(0),
		colCommAvailableAt:     model.NewTimestamp(communicationSchemaNow()).String(),
		colCommAutomationDepth: int64(0), colCommLastEventSeq: int64(0),
	}
}

func communicationBoundaryMessagePayloads(t *testing.T) ([]byte, []byte) {
	t.Helper()
	content := MessageContent{
		Subject: "boundary",
		Blocks: []MessageContentBlock{
			{Type: ContentBlockText, Format: TextPlain, Text: "a"},
			{Type: ContentBlockText, Format: TextPlain, Text: "b"},
		},
	}
	base, err := canonicalJSON(content)
	if err != nil {
		t.Fatalf("canonical boundary payload base: %v", err)
	}
	remaining := maxMessageBytes - len(base)
	firstRoom := maxMessageTextBytes - len(content.Blocks[0].Text)
	firstGrowth := remaining
	if firstGrowth > firstRoom {
		firstGrowth = firstRoom
	}
	content.Blocks[0].Text += strings.Repeat("a", firstGrowth)
	secondGrowth := remaining - firstGrowth
	if secondGrowth < 0 || len(content.Blocks[1].Text)+secondGrowth > maxMessageTextBytes {
		t.Fatalf("cannot construct exact protected payload boundary: remaining=%d", remaining)
	}
	content.Blocks[1].Text += strings.Repeat("b", secondGrowth)
	maximum, err := CanonicalProtectedPayloadSlot(PayloadSlotMessage, content)
	if err != nil || len(maximum) != maxMessageBytes {
		t.Fatalf("maximum protected payload = %d bytes, %v; want %d", len(maximum), err, maxMessageBytes)
	}

	oversizedContent := content
	oversizedContent.Blocks = append([]MessageContentBlock(nil), content.Blocks...)
	oversizedContent.Blocks[1].Text += "b"
	oversized, err := canonicalJSON(oversizedContent)
	if err != nil || len(oversized) != maxMessageBytes+1 {
		t.Fatalf("oversized protected payload = %d bytes, %v; want %d",
			len(oversized), err, maxMessageBytes+1)
	}
	return maximum, oversized
}

func communicationCreate(
	ctx context.Context,
	m *Module,
	tenant model.TenantID,
	kind model.Kind,
	record model.Record,
) (model.Record, error) {
	var created model.Record
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(kind)
		if err != nil {
			return err
		}
		created, err = repo.Create(ctx, record)
		return err
	})
	return created, err
}

func communicationCreateWithID(
	ctx context.Context,
	m *Module,
	tenant model.TenantID,
	kind model.Kind,
	id model.ID,
	record model.Record,
) (model.Record, error) {
	var created model.Record
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(kind)
		if err != nil {
			return err
		}
		created, err = repo.CreateWithID(ctx, id, record)
		return err
	})
	return created, err
}

func communicationUpdate(
	ctx context.Context,
	m *Module,
	tenant model.TenantID,
	kind model.Kind,
	record model.Record,
) (model.Record, error) {
	var updated model.Record
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(kind)
		if err != nil {
			return err
		}
		updated, err = repo.Update(ctx, record)
		return err
	})
	return updated, err
}

func communicationMustCreate(
	t *testing.T,
	fixture communicationSchemaFixture,
	kind model.Kind,
	record model.Record,
) model.Record {
	t.Helper()
	created, err := communicationCreate(context.Background(), fixture.m, fixture.tenant, kind, record)
	if err != nil {
		t.Fatalf("create valid %s: %v", kind, err)
	}
	return created
}

func TestCommunicationSchemaRejectsInvalidRowsAcrossBackends(t *testing.T) {
	t.Parallel()

	for _, backend := range communicationSchemaBackends(t) {
		backend := backend
		t.Run(backend.name, func(t *testing.T) {
			fixture := communicationOpenFixture(t, backend)
			channel := communicationMustCreate(t, fixture, channelKind,
				communicationChannelRecord(fixture.workspace, "valid"))
			channelID := model.ID(channel.String(model.ColID))

			maximumPayload, oversizedPayload := communicationBoundaryMessagePayloads(t)
			maximumMessageID := model.NewID()
			maximumMessage := communicationStagingMessageRecord(
				fixture.workspace, channelID, maximumMessageID, "maximum-plain-payload",
			)
			maximumMessage["payload_plain_json"] = string(maximumPayload)
			maximumMessage["payload_digest"] = hashBytes(maximumPayload)
			if _, err := communicationCreateWithID(context.Background(), fixture.m, fixture.tenant,
				messageKind, maximumMessageID, maximumMessage); err != nil {
				t.Fatalf("64 KiB canonical Message payload rejected: %v", err)
			}
			oversizedMessageID := model.NewID()
			oversizedMessage := communicationStagingMessageRecord(
				fixture.workspace, channelID, oversizedMessageID, "oversized-plain-payload",
			)
			oversizedMessage["payload_plain_json"] = string(oversizedPayload)
			oversizedMessage["payload_digest"] = hashBytes(oversizedPayload)
			if _, err := communicationCreateWithID(context.Background(), fixture.m, fixture.tenant,
				messageKind, oversizedMessageID, oversizedMessage); err == nil {
				t.Error("64 KiB + 1 canonical Message payload was persisted")
			}

			unsealedAudienceID := model.NewID()
			unsealedAudience, err := communicationCreateWithID(
				context.Background(), fixture.m, fixture.tenant, messageKind, unsealedAudienceID,
				communicationStagingMessageRecord(
					fixture.workspace, channelID, unsealedAudienceID, "missing-audience-hash",
				),
			)
			if err != nil {
				t.Fatalf("create draft for missing audience hash: %v", err)
			}
			unsealedAudience[colCommState] = string(MessagePublished)
			unsealedAudience[colCommPublishedAt] = model.NewTimestamp(communicationSchemaNow()).String()
			unsealedAudience[colCommLastEventSeq] = int64(1)
			if _, err := communicationUpdate(context.Background(), fixture.m, fixture.tenant,
				messageKind, unsealedAudience); err == nil {
				t.Error("Message published with a NULL audience hash")
			}

			sealedChannelRecord := communicationChannelRecord(fixture.workspace, "sealed-envelope")
			sealedChannelRecord[colCommContentProtection] = string(ContentProtectionApplicationSealed)
			sealedChannel := communicationMustCreate(t, fixture, channelKind, sealedChannelRecord)
			sealedChannelID := model.ID(sealedChannel.String(model.ColID))
			sealedDraft := func(id model.ID, keyVersionJSON string) model.Record {
				record := communicationStagingMessageRecord(
					fixture.workspace, sealedChannelID, id, "sealed-envelope",
				)
				record["payload_encoding"] = string(PayloadSealedV1)
				delete(record, "payload_plain_json")
				record["payload_sealed_json"] = `{"ciphertext":"YQ==","key_version":` + keyVersionJSON + `}`
				record["payload_seal_key_version"] = "7"
				record["payload_digest_key_version"] = "digest-v1"
				return record
			}
			validSealedID := model.NewID()
			if _, err := communicationCreateWithID(context.Background(), fixture.m, fixture.tenant,
				messageKind, validSealedID, sealedDraft(validSealedID, `"7"`)); err != nil {
				t.Fatalf("sealed payload with string key_version rejected: %v", err)
			}
			numericKeyID := model.NewID()
			if _, err := communicationCreateWithID(context.Background(), fixture.m, fixture.tenant,
				messageKind, numericKeyID, sealedDraft(numericKeyID, `7`)); err == nil {
				t.Error("sealed payload accepted a numeric envelope key_version")
			}

			invalidKind := communicationChannelRecord(fixture.workspace, "bad-kind")
			invalidKind[colCommKind] = "chat"
			invalidProtection := communicationChannelRecord(fixture.workspace, "bad-protection")
			invalidProtection[colCommSensitivity] = string(ChannelRestricted)
			invalidProtection[colCommContentProtection] = string(ContentProtectionStorage)
			invalidRevision := communicationChannelRecord(fixture.workspace, "bad-revision")
			invalidRevision[colCommACLRevision] = int64(0)
			for name, record := range map[string]model.Record{
				"unknown channel kind":       invalidKind,
				"restricted without sealing": invalidProtection,
				"zero ACL revision":          invalidRevision,
			} {
				if _, err := communicationCreate(context.Background(), fixture.m, fixture.tenant, channelKind, record); err == nil {
					t.Errorf("%s accepted", name)
				}
			}

			grant := model.Record{
				colWorkWorkspaceID: fixture.workspace.String(),
				colCommChannelID:   channel.String(model.ColID),
				colCommSubjectKind: string(SubjectUser), colCommSubjectRef: model.NewID().String(),
				colCommGeneration: int64(1), colCommCanRead: false, colCommCanWrite: true,
				colCommCanAdmin: false, colCommState: string(ChannelGrantActive),
				colCommGrantedByKind: string(ActorUser), colCommGrantedByRef: model.NewID().String(),
			}
			communicationMustCreate(t, fixture, channelGrantKind, grant)
			v4ID := "00000000-0000-4000-8000-000000000001"
			noBits := workSchemaClone(grant)
			noBits[colCommSubjectRef] = model.NewID().String()
			noBits[colCommCanWrite] = false
			if _, err := communicationCreate(context.Background(), fixture.m, fixture.tenant, channelGrantKind, noBits); err == nil {
				t.Error("ChannelGrant with no permission bits accepted")
			}
			for name, ref := range map[string]string{
				"UUIDv4 subject": v4ID,
				"opaque subject": strings.Repeat("x", 36),
			} {
				invalidRef := workSchemaClone(grant)
				invalidRef[colCommSubjectRef] = ref
				if _, err := communicationCreate(context.Background(), fixture.m, fixture.tenant,
					channelGrantKind, invalidRef); err == nil {
					t.Errorf("ChannelGrant accepted %s", name)
				}
			}
			systemGrant := workSchemaClone(grant)
			systemGrant[colCommSubjectRef] = model.NewID().String()
			systemGrant[colCommGrantedByKind] = string(ActorSystem)
			systemGrant[colCommGrantedByRef] = "schema-worker"
			communicationMustCreate(t, fixture, channelGrantKind, systemGrant)
			spacedSystemGrant := workSchemaClone(systemGrant)
			spacedSystemGrant[colCommSubjectRef] = model.NewID().String()
			spacedSystemGrant[colCommGrantedByRef] = " schema-worker "
			if _, err := communicationCreate(context.Background(), fixture.m, fixture.tenant,
				channelGrantKind, spacedSystemGrant); err == nil {
				t.Error("ChannelGrant accepted a whitespace-padded system actor ref")
			}

			invalidSubscription := model.Record{
				colWorkWorkspaceID: fixture.workspace.String(), colCommChannelID: channelID.String(),
				colCommSubscriberKind: string(SubjectUser), colCommSubscriberRef: v4ID,
				colCommGeneration: int64(1), colCommMode: string(SubscriptionAll),
				colCommWake: string(WakeNone), colCommRequiredForCritical: false,
				colCommState: string(SubscriptionActive),
			}
			if _, err := communicationCreate(context.Background(), fixture.m, fixture.tenant,
				channelSubscriptionKind, invalidSubscription); err == nil {
				t.Error("ChannelSubscription accepted a UUIDv4 subscriber")
			}
			filteredSubscription := workSchemaClone(invalidSubscription)
			filteredSubscription[colCommSubscriberRef] = model.NewID().String()
			filteredSubscription[colCommFilterJSON] = "{}"
			filteredSubscription[colCommFilterHash] = hashBytes([]byte("{}"))
			communicationMustCreate(t, fixture, channelSubscriptionKind, filteredSubscription)
			invalidFilter := workSchemaClone(filteredSubscription)
			invalidFilter[colCommSubscriberRef] = model.NewID().String()
			invalidFilter[colCommFilterJSON] = "not-json"
			invalidFilter[colCommFilterHash] = workSchemaHash("not-json")
			if _, err := communicationCreate(context.Background(), fixture.m, fixture.tenant,
				channelSubscriptionKind, invalidFilter); err == nil {
				t.Error("ChannelSubscription accepted invalid FilterJSON text")
			}
			invalidEndpoint := model.Record{
				colWorkWorkspaceID: fixture.workspace.String(),
				colCommOwnerKind:   string(RecipientAgent), colCommOwnerRef: v4ID,
				colCommProviderKey: "driver:invalid-owner", colTransport: "test",
				colCommEndpointRef: "invalid-owner", colCommCapabilitiesJSON: "{}",
				colCommSupportLevel: string(EndpointStable), colCommPriority: int64(0),
				colCommState: string(EndpointActive), colCommGeneration: int64(1),
			}
			if _, err := communicationCreate(context.Background(), fixture.m, fixture.tenant,
				communicationEndpointKind, invalidEndpoint); err == nil {
				t.Error("CommunicationEndpoint accepted a UUIDv4 owner")
			}
			invalidCapabilities := workSchemaClone(invalidEndpoint)
			invalidCapabilities[colCommOwnerRef] = model.NewID().String()
			invalidCapabilities[colCommEndpointRef] = "invalid-capabilities"
			invalidCapabilities[colCommCapabilitiesJSON] = "not-json"
			if _, err := communicationCreate(context.Background(), fixture.m, fixture.tenant,
				communicationEndpointKind, invalidCapabilities); err == nil {
				t.Error("CommunicationEndpoint accepted invalid CapabilitiesJSON text")
			}

			contentBearingReceipt := communicationCommandRecord(
				fixture.workspace, model.ID(channel.String(model.ColID)),
			)
			contentBearingReceipt[colCommResponseProjectionJSON] =
				`{"state":"SECRET/PII","ids":{"body":"not-an-id"},"counts":{"x":-1},"digests":{"x":"not32"}}`
			contentBearingReceipt[colCommResponseDigest] = workSchemaHash("content-bearing-receipt")
			if _, err := communicationCreate(context.Background(), fixture.m, fixture.tenant,
				communicationCommandKind, contentBearingReceipt); err == nil {
				t.Error("CommunicationCommand persisted content-bearing response projection")
			}
			v4Command := communicationCommandRecord(
				fixture.workspace, model.ID(channel.String(model.ColID)),
			)
			v4Command[colCommCommandID] = v4ID
			v4Command[colCommIdempotencyKeyHash] = workSchemaHash("v4-command-idempotency")
			if _, err := communicationCreate(context.Background(), fixture.m, fixture.tenant,
				communicationCommandKind, v4Command); err == nil {
				t.Error("CommunicationCommand accepted a UUIDv4 command_id")
			}
			v4Result := communicationCommandRecord(fixture.workspace, model.ID(v4ID))
			v4Result[colCommIdempotencyKeyHash] = workSchemaHash("v4-result-idempotency")
			if _, err := communicationCreate(context.Background(), fixture.m, fixture.tenant,
				communicationCommandKind, v4Result); err == nil {
				t.Error("CommunicationCommand accepted a UUIDv4 result_id")
			}
		})
	}
}

func TestCommunicationCursorBarrierUniquenessIsActiveOnlyAcrossBackends(t *testing.T) {
	t.Parallel()

	for _, backend := range communicationSchemaBackends(t) {
		backend := backend
		t.Run(backend.name, func(t *testing.T) {
			fixture := communicationOpenFixture(t, backend)
			ctx := context.Background()
			now := model.NewTimestamp(communicationSchemaNow()).String()
			channel := communicationMustCreate(t, fixture, channelKind,
				communicationChannelRecord(fixture.workspace, "barrier"))
			messageID := model.NewID()
			recipientID := model.NewID()
			message := communicationStagingMessageRecord(
				fixture.workspace, model.ID(channel.String(model.ColID)), messageID, "barrier-message",
			)
			if _, err := communicationCreateWithID(ctx, fixture.m, fixture.tenant,
				messageKind, messageID, message); err != nil {
				t.Fatalf("create staging Message: %v", err)
			}

			deliveryID := model.NewID()
			delivery := model.Record{
				colWorkWorkspaceID: fixture.workspace.String(), colCommMessageID: messageID.String(),
				colCommRecipientKind: string(RecipientUser), colCommRecipientRef: recipientID.String(),
				colCommRecipientEpoch: int64(1), colCommDeliverySeq: int64(1),
				colCommRequired: false, colCommRouteReasonsJSON: `["direct"]`,
				colCommWakePolicy: string(WakeNone), colCommState: string(DeliveryAvailable),
				colCommAvailableAt: now,
			}
			directTerminal := workSchemaClone(delivery)
			directTerminal[colCommRecipientRef] = model.NewID().String()
			directTerminal[colCommDeliverySeq] = int64(2)
			directTerminal[colCommState] = string(DeliveryRetracted)
			if _, err := communicationCreateWithID(ctx, fixture.m, fixture.tenant,
				messageDeliveryKind, model.NewID(), directTerminal); err == nil {
				t.Fatal("MessageDelivery was inserted directly in a terminal state")
			}
			if _, err := communicationCreateWithID(ctx, fixture.m, fixture.tenant,
				messageDeliveryKind, deliveryID, delivery); err != nil {
				t.Fatalf("create MessageDelivery: %v", err)
			}

			filterHash := workSchemaHash("barrier-filter")
			cursor := model.Record{
				colWorkWorkspaceID: fixture.workspace.String(),
				colCommReaderKind:  string(RecipientUser), colCommReaderRef: recipientID.String(),
				colCommMailboxKind: string(MailboxPersonal), colCommMailboxRef: recipientID.String(),
				colCommLastSeenSeq: int64(0), colCommLastSeenAt: now, colCommFilterHash: filterHash,
			}
			persistedCursor := communicationMustCreate(t, fixture, inboxCursorKind, cursor)

			barrier := model.Record{
				colWorkWorkspaceID: fixture.workspace.String(),
				colCommReaderKind:  string(RecipientUser), colCommReaderRef: recipientID.String(),
				colCommMailboxKind: string(MailboxPersonal), colCommMailboxRef: recipientID.String(),
				colCommFilterHash: filterHash, colCommDeliveryID: deliveryID.String(),
				colCommBarrierSeq: int64(1), colCommCause: string(BarrierTemporarilyInvisible),
				colCommState: string(CursorBarrierActive), colCommReasonCode: "acl_temporarily_invisible",
			}
			first := communicationMustCreate(t, fixture, inboxCursorBarrierKind, barrier)
			foreignRecipientBarrier := workSchemaClone(barrier)
			foreignRecipientBarrier[colCommReaderRef] = model.NewID().String()
			foreignRecipientBarrier[colCommMailboxRef] = foreignRecipientBarrier[colCommReaderRef]
			foreignRecipientBarrier[colCommFilterHash] = workSchemaHash("foreign-recipient-barrier")
			if _, err := communicationCreate(ctx, fixture.m, fixture.tenant,
				inboxCursorBarrierKind, foreignRecipientBarrier); err == nil {
				t.Fatal("personal CursorBarrier accepted a Delivery for another recipient")
			}
			if _, err := communicationCreate(ctx, fixture.m, fixture.tenant,
				inboxCursorBarrierKind, workSchemaClone(barrier)); err == nil {
				t.Fatal("second active barrier with the same identity was accepted")
			}
			cursorAcrossActiveBarrier := workSchemaClone(persistedCursor)
			cursorAcrossActiveBarrier[colCommLastSeenSeq] = int64(1)
			cursorAcrossActiveBarrier[colCommLastSeenAt] = now
			if _, err := communicationUpdate(ctx, fixture.m, fixture.tenant,
				inboxCursorKind, cursorAcrossActiveBarrier); err == nil {
				t.Fatal("InboxCursor advanced across an active barrier")
			}

			resolved := workSchemaClone(first)
			resolved[colCommState] = string(CursorBarrierResolved)
			resolved[colCommResolvedAt] = now
			if _, err := communicationUpdate(ctx, fixture.m, fixture.tenant,
				inboxCursorBarrierKind, resolved); err != nil {
				t.Fatalf("resolve first barrier: %v", err)
			}
			second, err := communicationCreate(ctx, fixture.m, fixture.tenant,
				inboxCursorBarrierKind, workSchemaClone(barrier))
			if err != nil {
				t.Fatalf("create active barrier after prior barrier resolved: %v", err)
			}
			secondResolved := workSchemaClone(second)
			secondResolved[colCommState] = string(CursorBarrierResolved)
			secondResolved[colCommResolvedAt] = now
			if _, err := communicationUpdate(ctx, fixture.m, fixture.tenant,
				inboxCursorBarrierKind, secondResolved); err != nil {
				t.Fatalf("resolve recreated barrier: %v", err)
			}
			if _, err := communicationUpdate(ctx, fixture.m, fixture.tenant,
				inboxCursorKind, cursorAcrossActiveBarrier); err != nil {
				t.Fatalf("advance InboxCursor after every matching barrier resolved: %v", err)
			}
		})
	}
}

func TestCommunicationDeliveryDeadlineTransitionsAcrossBackends(t *testing.T) {
	t.Parallel()

	for _, backend := range communicationSchemaBackends(t) {
		backend := backend
		t.Run(backend.name, func(t *testing.T) {
			clock := &testClock{now: communicationSchemaNow()}
			fixture := communicationOpenFixtureWithClock(t, backend, clock)
			ctx := context.Background()
			availableAt := communicationSchemaNow()
			ackDueAt := availableAt.Add(time.Hour)
			expiresAt := ackDueAt.Add(time.Hour)
			available := model.NewTimestamp(availableAt).String()
			due := model.NewTimestamp(ackDueAt).String()
			expires := model.NewTimestamp(expiresAt).String()

			channel := communicationMustCreate(t, fixture, channelKind,
				communicationChannelRecord(fixture.workspace, "delivery-deadline"))
			messageID := model.NewID()
			message := communicationStagingMessageRecord(
				fixture.workspace, model.ID(channel.String(model.ColID)), messageID,
				"delivery-deadline",
			)
			message[colCommAckPolicy] = string(AckPolicyEachRequired)
			message[colCommAckDueAt] = due
			message[colCommExpiresAt] = expires
			if _, err := communicationCreateWithID(ctx, fixture.m, fixture.tenant,
				messageKind, messageID, message); err != nil {
				t.Fatalf("create deadline Message: %v", err)
			}

			createDelivery := func(seed string, seq int64) model.Record {
				deliveryID := model.NewID()
				record, err := communicationCreateWithID(ctx, fixture.m, fixture.tenant,
					messageDeliveryKind, deliveryID, model.Record{
						colWorkWorkspaceID:    fixture.workspace.String(),
						colCommMessageID:      messageID.String(),
						colCommRecipientKind:  string(RecipientAgent),
						colCommRecipientRef:   model.NewID().String(),
						colCommRecipientEpoch: int64(1), colCommDeliverySeq: seq,
						colCommRequired: true, colCommRouteReasonsJSON: `["` + seed + `"]`,
						colCommWakePolicy: string(WakeNone), colCommState: string(DeliveryAvailable),
						colCommAvailableAt: available, colCommAckDueAt: due, colCommExpiresAt: expires,
					})
				if err != nil {
					t.Fatalf("create %s Delivery: %v", seed, err)
				}
				return record
			}

			early := createDelivery("deadline_early", 1)
			earlyExpired := workSchemaClone(early)
			earlyExpired[colCommState] = string(DeliveryExpired)
			if _, err := communicationUpdate(ctx, fixture.m, fixture.tenant,
				messageDeliveryKind, earlyExpired); err == nil {
				t.Fatal("MessageDelivery expired before either deadline elapsed")
			}
			earlyRetracted := workSchemaClone(early)
			earlyRetracted[colCommState] = string(DeliveryRetracted)
			if _, err := communicationUpdate(ctx, fixture.m, fixture.tenant,
				messageDeliveryKind, earlyRetracted); err != nil {
				t.Fatalf("MessageDelivery retraction before its deadline rejected: %v", err)
			}

			ackAtDue := createDelivery("deadline_ack", 2)
			retractAtDue := createDelivery("deadline_retract", 3)
			expireAtDue := createDelivery("deadline_expire", 4)
			clock.set(ackDueAt)

			acknowledged := workSchemaClone(ackAtDue)
			acknowledged[colCommState] = string(DeliveryAcknowledged)
			acknowledged[colCommAckID] = model.NewID().String()
			acknowledged[colCommAcknowledgedAt] = due
			if _, err := communicationUpdate(ctx, fixture.m, fixture.tenant,
				messageDeliveryKind, acknowledged); err == nil {
				t.Fatal("MessageDelivery acknowledged at its elapsed AckDueAt")
			}

			retracted := workSchemaClone(retractAtDue)
			retracted[colCommState] = string(DeliveryRetracted)
			if _, err := communicationUpdate(ctx, fixture.m, fixture.tenant,
				messageDeliveryKind, retracted); err == nil {
				t.Fatal("MessageDelivery retracted instead of expiring at its AckDueAt")
			}

			expired := workSchemaClone(expireAtDue)
			expired[colCommState] = string(DeliveryExpired)
			if _, err := communicationUpdate(ctx, fixture.m, fixture.tenant,
				messageDeliveryKind, expired); err != nil {
				t.Fatalf("MessageDelivery did not expire at its exact AckDueAt: %v", err)
			}
		})
	}
}

func TestCommunicationTenantAndWorkspaceIsolation(t *testing.T) {
	t.Parallel()

	for _, backend := range communicationSchemaBackends(t) {
		backend := backend
		t.Run(backend.name, func(t *testing.T) {
			fixture := communicationOpenFixture(t, backend)
			ctx := context.Background()
			var otherWorkspace model.ID
			if err := fixture.m.data.Mutate(ctx, fixture.tenant, func(sc store.Scope) error {
				workspace, err := sc.Workspaces().Create(ctx, model.Workspace{
					Name: "Other", Slug: "other", Status: model.StatusActive,
				})
				if err == nil {
					otherWorkspace = workspace.ID
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			own := communicationMustCreate(t, fixture, channelKind,
				communicationChannelRecord(fixture.workspace, "own"))
			communicationMustCreate(t, fixture, channelKind,
				communicationChannelRecord(otherWorkspace, "foreign-workspace"))

			if err := fixture.m.data.View(ctx, fixture.tenant, func(raw store.Scope) error {
				confined, err := store.ConfineWorkspace(ctx, raw, fixture.workspace)
				if err != nil {
					return err
				}
				repo, err := confined.Ext(channelKind)
				if err != nil {
					return err
				}
				rows, _, err := repo.List(ctx, model.Query{})
				if err == nil && (len(rows) != 1 || rows[0].String(model.ColID) != own.String(model.ColID)) {
					t.Errorf("confined channel rows = %v, want only %s", rows, own.String(model.ColID))
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}

			var tenantB model.TenantID
			if err := fixture.st.System(ctx, func(sys store.SystemScope) error {
				org, err := sys.CreateOrg(ctx, model.Org{
					Name: "Communication schema B", Slug: "communication-schema-b", Status: model.StatusActive,
				})
				if err == nil {
					tenantB = org.TenantID
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := fixture.m.data.View(ctx, tenantB, func(sc store.Scope) error {
				repo, err := sc.Ext(channelKind)
				if err != nil {
					return err
				}
				_, err = repo.Get(ctx, model.ID(own.String(model.ColID)))
				return err
			}); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("tenant B Get(tenant A channel) = %v, want ErrNotFound", err)
			}

			if backend.engineName == store.EnginePostgres {
				communicationAssertPostgresRLSCatalog(t, backend.dsn)
				communicationAssertPostgresRLS(t, backend.dsn, fixture.tenant, tenantB,
					channelTable, own.String(model.ColID))
			}
		})
	}
}

func communicationAssertPostgresRLSCatalog(t *testing.T, dsn string) {
	t.Helper()
	raw, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close() //nolint:errcheck

	tables := communicationTables()
	tableNames := make([]string, 0, len(tables))
	for table := range tables {
		tableNames = append(tableNames, table)
	}
	sort.Strings(tableNames)
	quoted := make([]string, len(tableNames))
	for i, table := range tableNames {
		quoted[i] = "'" + table + "'"
	}
	rows, err := raw.QueryContext(context.Background(), `
SELECT c.relname, c.relrowsecurity, c.relforcerowsecurity,
	(SELECT count(*) FROM pg_catalog.pg_policies p
	 WHERE p.schemaname = n.nspname AND p.tablename = c.relname)
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = current_schema()
	AND c.relkind = 'r'
	AND c.relname IN (`+strings.Join(quoted, ",")+`)
ORDER BY c.relname`) // #nosec G202 -- table names come from the closed exact-20 manifest
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close() //nolint:errcheck

	seen := make(map[string]bool, len(tableNames))
	for rows.Next() {
		var table string
		var enabled, forced bool
		var policies int
		if err := rows.Scan(&table, &enabled, &forced, &policies); err != nil {
			t.Fatal(err)
		}
		seen[table] = true
		if !enabled || !forced || policies < 1 {
			t.Errorf("%s RLS catalog = enabled %v, forced %v, policies %d; want true/true/>=1",
				table, enabled, forced, policies)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, table := range tableNames {
		if !seen[table] {
			t.Errorf("communication RLS catalog is missing %s", table)
		}
	}
}

func communicationAssertPostgresRLS(
	t *testing.T,
	dsn string,
	tenantA, tenantB model.TenantID,
	table, id string,
) {
	t.Helper()
	raw, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close() //nolint:errcheck
	count := func(tenant model.TenantID) int {
		t.Helper()
		tx, err := raw.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback() //nolint:errcheck
		if _, err := tx.Exec("SELECT pg_catalog.set_config('app.tenant_id', $1, true)", tenant.String()); err != nil {
			t.Fatal(err)
		}
		var got int
		if err := tx.QueryRow("SELECT count(*) FROM "+table+" WHERE id = $1", id).Scan(&got); err != nil { // #nosec G202 -- table is closed test manifest
			t.Fatal(err)
		}
		return got
	}
	if got := count(tenantB); got != 0 {
		t.Fatalf("FORCE RLS leaked tenant A row to tenant B: %d", got)
	}
	if got := count(tenantA); got != 1 {
		t.Fatalf("FORCE RLS hid tenant A row from tenant A: %d", got)
	}
}

func TestCommunicationRowsSurviveDropTenantAcrossBackends(t *testing.T) {
	t.Parallel()

	reg := communicationCaptureSchema(t)
	for _, entity := range communicationSchemaEntities {
		descriptor := communicationDescriptor(t, reg, entity.kind)
		if !descriptor.AppendOnly && !descriptor.RetainOnTenantDrop {
			t.Fatalf("mutable descriptor %s would be removed by DropTenant", entity.kind)
		}
	}

	for _, backend := range communicationSchemaBackends(t) {
		backend := backend
		t.Run(backend.name, func(t *testing.T) {
			fixture := communicationOpenFixture(t, backend)
			ctx := context.Background()
			channel := communicationMustCreate(t, fixture, channelKind,
				communicationChannelRecord(fixture.workspace, "retained"))
			command := communicationMustCreate(t, fixture, communicationCommandKind,
				communicationCommandRecord(fixture.workspace, model.ID(channel.String(model.ColID))))

			if err := fixture.st.System(ctx, func(sys store.SystemScope) error {
				return sys.DropTenant(ctx, fixture.tenant)
			}); err != nil {
				t.Fatalf("DropTenant: %v", err)
			}
			for _, row := range []struct {
				kind model.Kind
				id   model.ID
			}{
				{channelKind, model.ID(channel.String(model.ColID))},
				{communicationCommandKind, model.ID(command.String(model.ColID))},
			} {
				if err := fixture.m.data.View(ctx, fixture.tenant, func(sc store.Scope) error {
					repo, err := sc.Ext(row.kind)
					if err != nil {
						return err
					}
					_, err = repo.Get(ctx, row.id)
					return err
				}); err != nil {
					t.Errorf("retained %s after DropTenant: %v", row.kind, err)
				}
				if err := fixture.m.data.Mutate(ctx, fixture.tenant, func(sc store.Scope) error {
					repo, err := sc.Ext(row.kind)
					if err != nil {
						return err
					}
					return repo.Delete(ctx, row.id)
				}); err == nil {
					t.Errorf("retained %s became hard-deletable after DropTenant", row.kind)
				}
			}
		})
	}
}

func communicationCommandRecord(workspace, resultID model.ID) model.Record {
	return model.Record{
		colWorkWorkspaceID: workspace.String(), colCommCommandID: model.NewID().String(),
		colCommActorFingerprint:   workSchemaHash("communication-actor"),
		colCommCommandScope:       "POST:/v1/communication/messages",
		colCommIdempotencyKeyHash: workSchemaHash("communication-idempotency"),
		colCommRequestDigest:      workSchemaHash("communication-request"),
		colCommPlanHash:           workSchemaHash("communication-plan"),
		colCommResultKind:         string(channelKind), colCommResultID: resultID.String(),
		colCommHTTPStatus: int64(201), colCommResponseProjectionJSON: "{}",
		colCommResponseDigest: workSchemaHash("communication-response"),
		colCommAuditSeq:       int64(1), colCommAuditHash: workSchemaHash("communication-audit"),
		colCommCompletedAt: model.NewTimestamp(communicationSchemaNow()).String(),
	}
}

func protocolInterruptBindingForTest(
	t *testing.T,
	fixture workflowCommunicationFixture,
	protocol BindingProtocol,
) ProtocolBinding {
	t.Helper()
	digest := func(label string) []byte {
		sum := sha256.Sum256([]byte(label))
		return append([]byte(nil), sum[:]...)
	}
	bindingID := model.NewID()
	observedAt := fixture.now
	stored := storedProtocolBinding{
		ProtocolBinding: ProtocolBinding{
			MutableCommunicationEntity: MutableCommunicationEntity{
				CommunicationEntity: CommunicationEntity{
					ID: bindingID, TenantID: fixture.tenant, WorkspaceID: fixture.workspace,
					Version: 1, CreatedAt: fixture.now,
				},
				UpdatedAt: fixture.now,
			},
			BindingSpecID: model.NewID(), BindingSpecGeneration: 1,
			PinnedSpecHash:    digest("protocol-interrupt-spec"),
			PinnedMappingHash: digest("protocol-interrupt-mapping"),
			PinnedLossesHash:  digest("protocol-interrupt-losses"),
			WorkItemID:        fixture.workID, Protocol: protocol,
			ProtocolVersion: "2026-08-01", Direction: BindingOutbound,
			PeerAuthority:     "https://protocol-interrupt.example",
			RemoteResourceRef: "remote:protocol-interrupt",
			AttemptID:         model.NewID(), Generation: 1, SyntheticSID: newSID(),
			OwnerKind: "agent", OwnerRef: "agent:protocol-interrupt", OwnerEpoch: 1,
			ExternalKind: "task", ExternalID: "task:protocol-interrupt",
			LocalState: "blocked", RemoteState: "input_required",
			RemoteRevision: "2026-08-01", ObservationVerdict: ProtocolObservationClean,
			ObservationCode: "input_required", LastObservedAt: &observedAt,
			LastCommandID: model.NewID(), LastEventID: model.NewID(), LastEventSeq: 1,
		},
		dispatchKeyHash: digest("protocol-interrupt-dispatch"),
		reservationHash: digest("protocol-interrupt-reservation"),
	}
	if _, err := communicationCreateWithID(
		context.Background(), fixture.m, fixture.tenant,
		protocolBindingKind, bindingID, encodeProtocolBinding(stored),
	); err != nil {
		t.Fatalf("create protocol interrupt binding: %v", err)
	}
	return stored.ProtocolBinding
}

func makeProtocolInterruptRecipientWriter(t *testing.T, fixture workflowCommunicationFixture) {
	t.Helper()
	for _, row := range communicationRowsForTest(t, fixture.directNoticeFixture, channelGrantKind) {
		grant, err := channelGrantFromRecord(row)
		if err != nil {
			t.Fatalf("decode protocol interrupt recipient grant: %v", err)
		}
		if grant.ChannelID != fixture.channel.ID ||
			grant.Subject != (CommunicationSubjectRef{Kind: SubjectUser, Ref: fixture.target.Ref}) {
			continue
		}
		if err := fixture.m.mutateCommunication(
			context.Background(), fixture.scope, func(tx *communicationTx) error {
				locked, lockErr := tx.lockRecord(context.Background(), channelGrantKind, grant.ID)
				if lockErr != nil {
					return lockErr
				}
				prior, decodeErr := channelGrantFromRecord(locked)
				if decodeErr != nil {
					return decodeErr
				}
				beforeVersion := prior.Version
				revoker := CommunicationActorRef{Kind: ActorUser, Ref: fixture.sender.String()}
				prior.State, prior.RevokedBy = ChannelGrantRevoked, &revoker
				prior.Version++
				prior.UpdatedAt = tx.now.Time()
				priorRecord, encodeErr := channelGrantToRecord(prior)
				if encodeErr != nil {
					return encodeErr
				}
				priorRecord[model.ColVersion] = beforeVersion
				if _, updateErr := tx.update(
					context.Background(), channelGrantKind, priorRecord,
				); updateErr != nil {
					return updateErr
				}
				successorID := model.NewID()
				successor := ChannelGrant{
					MutableCommunicationEntity: MutableCommunicationEntity{
						CommunicationEntity: CommunicationEntity{
							ID: successorID, TenantID: fixture.tenant,
							WorkspaceID: fixture.workspace, Version: 1,
							CreatedAt: tx.now.Time(),
						},
						UpdatedAt: tx.now.Time(),
					},
					ChannelID: prior.ChannelID, Subject: prior.Subject,
					Generation: prior.Generation + 1, CanRead: true, CanWrite: true,
					State: ChannelGrantActive, GrantedBy: revoker, SupersedesID: prior.ID,
				}
				successorRecord, encodeErr := channelGrantToRecord(successor)
				if encodeErr != nil {
					return encodeErr
				}
				_, createErr := tx.createWithID(
					context.Background(), channelGrantKind, successorID, successorRecord,
				)
				return createErr
			},
		); err != nil {
			t.Fatalf("grant protocol interrupt recipient write: %v", err)
		}
		return
	}
	t.Fatal("protocol interrupt recipient grant is absent")
}

func TestProtocolInterruptMessageAckResponseRecoversAndReplays(t *testing.T) {
	t.Parallel()

	fixture := newWorkflowCommunicationFixture(t, false)
	makeProtocolInterruptRecipientWriter(t, fixture)
	binding := protocolInterruptBindingForTest(t, fixture, BindingProtocolMCP)
	recipientID, err := model.ParseID(fixture.target.Ref)
	if err != nil {
		t.Fatalf("parse protocol interrupt recipient: %v", err)
	}
	route := ProtocolInterruptRoute{
		ChannelID: fixture.channel.ID, SenderUserID: fixture.sender,
		RecipientUserID: recipientID,
	}
	requestKey := strings.Repeat("a", sha256.Size*2)
	requestContent := strings.Repeat("b", sha256.Size*2)
	command := ProtocolInterruptCommand{
		BindingID: binding.ID, Generation: binding.Generation, Route: route,
		RemoteState: "input_required", Requests: []ProtocolInterruptRequestRef{{
			KeyDigest: requestKey, ContentDigest: requestContent,
		}},
	}
	created, err := fixture.m.RecordProtocolInterrupt(context.Background(), fixture.tenant, command)
	if err != nil {
		t.Fatalf("record protocol interrupt: %v", err)
	}
	if len(created.Messages) != 1 || created.Messages[0].KeyDigest != requestKey ||
		created.Messages[0].MessageID.IsZero() || created.Messages[0].DeliveryID.IsZero() ||
		created.Messages[0].Replayed {
		t.Fatalf("protocol interrupt result = %+v", created)
	}
	replayed, err := fixture.m.RecordProtocolInterrupt(context.Background(), fixture.tenant, command)
	if err != nil || len(replayed.Messages) != 1 || !replayed.Messages[0].Replayed ||
		replayed.Messages[0].MessageID != created.Messages[0].MessageID ||
		replayed.Messages[0].DeliveryID != created.Messages[0].DeliveryID {
		t.Fatalf("protocol interrupt replay = %+v, %v", replayed, err)
	}

	responseDigest := strings.Repeat("c", sha256.Size*2)
	operationID, effectDigest := "operation:protocol-input", strings.Repeat("d", sha256.Size*2)
	_, routeHash, err := route.normalize()
	if err != nil {
		t.Fatalf("normalize protocol interrupt route: %v", err)
	}
	link, err := fixture.m.loadProtocolInterruptLink(
		context.Background(), fixture.tenant, binding, route, routeHash, requestKey,
	)
	if err != nil {
		t.Fatalf("load protocol interrupt link: %v", err)
	}
	responseHash, _ := parseProtocolInterruptDigest(responseDigest, "response content digest")
	operationHash := protocolInterruptOpaqueHash("protocol-input-response-operation-v1", operationID)
	effectHash := protocolInterruptOpaqueHash("protocol-input-response-effect-v1", effectDigest)
	if _, err := fixture.m.claimProtocolInputResponse(
		context.Background(), fixture.tenant, link, link.RouteHash,
		responseHash, operationHash, effectHash,
	); err != nil {
		t.Fatalf("seed interrupted response claim: %v", err)
	}

	response := ProtocolInputResponseCommand{
		BindingID: binding.ID, Generation: binding.Generation, Route: route,
		OperationID: operationID, EffectDigest: effectDigest,
		Responses: []ProtocolInputResponseRef{{
			KeyDigest: requestKey, ResponseDigest: responseDigest,
		}},
	}
	prepared, err := fixture.m.PrepareProtocolInputResponses(
		context.Background(), fixture.tenant, response,
	)
	if err != nil {
		t.Fatalf("prepare protocol input response after claimed-stop: %v", err)
	}
	if len(prepared.Responses) != 1 || prepared.Responses[0].AckID.IsZero() ||
		prepared.Responses[0].ResponseMessageID.IsZero() || prepared.Responses[0].Replayed {
		t.Fatalf("prepared protocol input response = %+v", prepared)
	}
	ackRecord := workflowCommunicationRecord(
		t, fixture, messageAckKind, prepared.Responses[0].AckID,
	)
	ack, err := messageAckFromRecord(ackRecord)
	if err != nil || ack.DeliveryID != created.Messages[0].DeliveryID ||
		ack.Actor != (CommunicationActorRef{Kind: ActorUser, Ref: recipientID.String()}) || ack.Late {
		t.Fatalf("protocol input MessageAck = %+v, %v", ack, err)
	}
	requestDeliveryRecord := workflowCommunicationRecord(
		t, fixture, messageDeliveryKind, created.Messages[0].DeliveryID,
	)
	requestDelivery, err := messageDeliveryFromRecord(requestDeliveryRecord)
	if err != nil || requestDelivery.State != DeliveryAcknowledged ||
		requestDelivery.AckID != prepared.Responses[0].AckID {
		t.Fatalf("acknowledged protocol input Delivery = %+v, %v", requestDelivery, err)
	}
	responseMessageRecord := workflowCommunicationRecord(
		t, fixture, messageKind, prepared.Responses[0].ResponseMessageID,
	)
	responseMessage, err := messageFromRecord(responseMessageRecord, 0)
	if err != nil || responseMessage.Kind != MessageWorkTask ||
		responseMessage.WorkItemID != fixture.workID || responseMessage.ChannelID != fixture.channel.ID ||
		responseMessage.Sender != (CommunicationActorRef{Kind: ActorUser, Ref: recipientID.String()}) {
		t.Fatalf("protocol input response Message = %+v, %v", responseMessage, err)
	}
	var ackReceipt, responseReceipt CommunicationCommandReceipt
	for _, row := range communicationRowsForTest(
		t, fixture.directNoticeFixture, communicationCommandKind,
	) {
		receipt, decodeErr := communicationCommandReceiptFromRecord(row)
		if decodeErr != nil {
			t.Fatalf("decode protocol input command receipt: %v", decodeErr)
		}
		switch {
		case receipt.ResultKind == string(messageAckKind) &&
			receipt.ResultID == prepared.Responses[0].AckID:
			ackReceipt = receipt
		case receipt.ResultKind == string(messageKind) &&
			receipt.ResultID == prepared.Responses[0].ResponseMessageID:
			responseReceipt = receipt
		}
	}
	if ackReceipt.ID.IsZero() || responseReceipt.ID.IsZero() {
		t.Fatalf("protocol input Ack/response receipts = %+v / %+v", ackReceipt, responseReceipt)
	}
	ackEvent := workflowCommunicationRecord(t, fixture, workEventKind, ackReceipt.EventID)
	responseEvent := workflowCommunicationRecord(t, fixture, workEventKind, responseReceipt.EventID)
	if ackEvent.String(colEventAggregateKind) != string(workItemKind) ||
		ackEvent.String(colEventAggregateID) != fixture.workID.String() ||
		ackEvent.String(colEventType) != communicationMessageAcknowledged ||
		responseEvent.String(colEventAggregateKind) != string(workItemKind) ||
		responseEvent.String(colEventAggregateID) != fixture.workID.String() ||
		responseEvent.String(colEventType) != workflowWorkTaskEventType ||
		ackEvent.Int(colEventSeq)+1 != responseEvent.Int(colEventSeq) {
		t.Fatalf("protocol input Ack/response causal Events = %+v / %+v", ackEvent, responseEvent)
	}
	completed, err := fixture.m.loadProtocolInterruptLink(
		context.Background(), fixture.tenant, binding, route, link.RouteHash, requestKey,
	)
	if err != nil || completed.State != protocolInterruptResponded ||
		completed.AckID != prepared.Responses[0].AckID ||
		completed.ResponseMessageID != prepared.Responses[0].ResponseMessageID ||
		completed.ResponseDeliveryID.IsZero() {
		t.Fatalf("completed protocol interrupt link = %+v, %v", completed, err)
	}
	responseDeliveryRecord := workflowCommunicationRecord(
		t, fixture, messageDeliveryKind, completed.ResponseDeliveryID,
	)
	responseDelivery, err := messageDeliveryFromRecord(responseDeliveryRecord)
	if err != nil || responseDelivery.MessageID != completed.ResponseMessageID ||
		responseDelivery.Recipient != (RecipientRef{
			Kind: RecipientUser, Ref: fixture.sender.String(),
		}) {
		t.Fatalf("protocol input reverse response Delivery = %+v, %v", responseDelivery, err)
	}
	exactReplay, err := fixture.m.PrepareProtocolInputResponses(
		context.Background(), fixture.tenant, response,
	)
	if err != nil || len(exactReplay.Responses) != 1 || !exactReplay.Responses[0].Replayed ||
		exactReplay.Responses[0].AckID != prepared.Responses[0].AckID ||
		exactReplay.Responses[0].ResponseMessageID != prepared.Responses[0].ResponseMessageID {
		t.Fatalf("protocol input response replay = %+v, %v", exactReplay, err)
	}
	conflict := response
	conflict.Responses = []ProtocolInputResponseRef{{
		KeyDigest: requestKey, ResponseDigest: strings.Repeat("e", sha256.Size*2),
	}}
	if _, err := fixture.m.PrepareProtocolInputResponses(
		context.Background(), fixture.tenant, conflict,
	); !errors.Is(err, ErrInvalidCommunicationTransition) {
		t.Fatalf("conflicting protocol input response = %v, want invalid transition", err)
	}
}
