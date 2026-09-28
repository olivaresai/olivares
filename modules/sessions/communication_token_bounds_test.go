// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// communicationTokenBoundCase is one value written into a 256-character token
// column, with the outcome the store must give it.
type communicationTokenBoundCase struct {
	name     string
	value    string
	accepted bool
}

func communicationTokenBoundCases(valid string) []communicationTokenBoundCase {
	return []communicationTokenBoundCase{
		{name: "valid", value: valid, accepted: true},
		{name: "256_characters", value: strings.Repeat("a", 256), accepted: true},
		{name: "257_characters", value: strings.Repeat("a", 257), accepted: false},
		{name: "invalid_character", value: "security review", accepted: false},
	}
}

// TestCommunicationTokenBoundsAcrossBackends writes the two 256-character
// tokens the communication guards bound, a ChannelRoute event_type and a
// DecisionRequest authority_requirement, after every sessions migration of the
// engine has run. PostgreSQL caps a regular-expression bound at 255, so a guard
// written as {1,256} does not accept or refuse: it raises SQLSTATE 2201B on
// every row that reaches it. Each value goes through the codec as a valid
// token and is then replaced in the record, so the database alone decides.
func TestCommunicationTokenBoundsAcrossBackends(t *testing.T) {
	t.Parallel()

	for _, backend := range communicationSchemaBackends(t) {
		backend := backend
		t.Run(backend.name, func(t *testing.T) {
			fixture := communicationOpenFixture(t, backend)
			communicationAssertMigrationTip(t, backend.engineName, backend.dsn,
				communicationEmbeddedMigrationTip(t, string(backend.engineName)))
			ctx := context.Background()
			now := communicationSchemaNow()
			entity := func(id model.ID) MutableCommunicationEntity {
				return MutableCommunicationEntity{CommunicationEntity: CommunicationEntity{
					ID: id, TenantID: fixture.tenant, WorkspaceID: fixture.workspace,
					Version: 1, CreatedAt: now,
				}, UpdatedAt: now}
			}
			channel := communicationMustCreate(t, fixture, channelKind,
				communicationChannelRecord(fixture.workspace, "token-bounds"))
			channelID := model.ID(channel.String(model.ColID))

			t.Run("channel_route_event_type", func(t *testing.T) {
				for i, bound := range communicationTokenBoundCases("work.item.updated") {
					t.Run(bound.name, func(t *testing.T) {
						id := model.NewID()
						route := ChannelRouteRule{
							MutableCommunicationEntity: entity(id),
							RouteKey:                   "token-bounds-" + strconv.Itoa(i),
							Generation:                 1, SourceKind: RouteSourceWorkEvent,
							EventType: "work.item.updated", TargetChannelID: channelID,
							AudienceKind: RouteAudienceSubscribers, AckPolicy: AckPolicyNone,
							WakePolicy: WakeNone, State: ChannelRouteActive,
						}
						record, err := channelRouteRuleToRecord(route)
						if err != nil {
							t.Fatalf("encode ChannelRoute: %v", err)
						}
						record[colCommEventType] = bound.value
						_, err = communicationCreateWithID(ctx, fixture.m, fixture.tenant,
							channelRouteKind, id, record)
						communicationAssertTokenBound(t, backend.name, "event_type", bound, err,
							"invalid ChannelRoute shape")
					})
				}
			})

			t.Run("decision_request_authority_requirement", func(t *testing.T) {
				workItemID := model.NewID()
				if _, err := communicationCreateWithID(ctx, fixture.m, fixture.tenant, workItemKind,
					workItemID, workSchemaItem(fixture.workspace, "token bounds")); err != nil {
					t.Fatalf("create DecisionRequest WorkItem: %v", err)
				}
				dueAt := now.Add(24 * time.Hour)
				expiresAt := dueAt.Add(time.Minute)
				for i, bound := range communicationTokenBoundCases("security_review") {
					t.Run(bound.name, func(t *testing.T) {
						messageID := model.NewID()
						message := Message{
							MutableCommunicationEntity: entity(messageID),
							ChannelID:                  channelID, WorkItemID: workItemID, ThreadID: messageID,
							Kind: MessageDecisionRequest, State: MessageDraft,
							Sender:  CommunicationActorRef{Kind: ActorUser, Ref: model.NewID().String()},
							Payload: communicationTestPayloadForSlot(t, PayloadSlotMessage),
							Urgency: UrgencyNormal, AckPolicy: AckPolicyNone,
							AvailableAt: now, ExpiresAt: &expiresAt,
						}
						messageRecord, err := messageToRecord(message, 0)
						if err != nil {
							t.Fatalf("encode DecisionRequest Message: %v", err)
						}
						if _, err := communicationCreateWithID(ctx, fixture.m, fixture.tenant,
							messageKind, messageID, messageRecord); err != nil {
							t.Fatalf("create DecisionRequest Message: %v", err)
						}
						requestID := model.NewID()
						request := DecisionRequest{
							MutableCommunicationEntity: entity(requestID), MessageID: messageID,
							WorkItemID: workItemID, DecisionKey: "token_bounds_" + strconv.Itoa(i),
							Requester:            message.Sender,
							Owner:                CommunicationSubjectRef{Kind: SubjectUser, Ref: model.NewID().String()},
							State:                DecisionPending,
							Request:              communicationTestPayloadForSlot(t, PayloadSlotDecisionRequest),
							AuthorityRequirement: "security_review", DueAt: dueAt,
						}
						record, err := decisionRequestToRecord(request)
						if err != nil {
							t.Fatalf("encode DecisionRequest: %v", err)
						}
						record[colCommAuthorityRequirement] = bound.value
						_, err = communicationCreateWithID(ctx, fixture.m, fixture.tenant,
							decisionRequestKind, requestID, record)
						communicationAssertTokenBound(t, backend.name, "authority_requirement", bound, err,
							"invalid DecisionRequest envelope")
					})
				}
			})
		})
	}
}

// communicationAssertTokenBound judges one write. A refusal counts only when
// the guard's own shape message names it: a regular-expression error is a
// refusal for another reason, not a bound.
func communicationAssertTokenBound(
	t *testing.T,
	engineName, column string,
	bound communicationTokenBoundCase,
	err error,
	refusal string,
) {
	t.Helper()
	outcome := "accepted"
	if err != nil {
		outcome = "refused"
		if !strings.Contains(err.Error(), refusal) {
			outcome = "error"
		}
	}
	t.Logf("token-bound engine=%s column=%s case=%s length=%d outcome=%s",
		engineName, column, bound.name, len(bound.value), outcome)
	switch {
	case bound.accepted && err != nil:
		t.Errorf("%s %s of %d characters was not accepted: %v", column, bound.name, len(bound.value), err)
	case !bound.accepted && err == nil:
		t.Errorf("%s %s of %d characters was accepted", column, bound.name, len(bound.value))
	case !bound.accepted && outcome != "refused":
		t.Errorf("%s %s of %d characters failed for another reason: %v, want %q",
			column, bound.name, len(bound.value), err, refusal)
	}
}

// communicationEmbeddedMigrationTip is the highest sessions migration version
// the binary embeds for engineName, so the test asserts that every one ran.
func communicationEmbeddedMigrationTip(t *testing.T, engineName string) int {
	t.Helper()
	names := communicationMigrationNames(t, communicationCaptureSchema(t).migrations[0].fs, engineName)
	if len(names) == 0 {
		t.Fatalf("no embedded %s sessions migrations", engineName)
	}
	tip, err := strconv.Atoi(strings.SplitN(names[len(names)-1], "_", 2)[0])
	if err != nil {
		t.Fatalf("parse %s sessions migration %s: %v", engineName, names[len(names)-1], err)
	}
	return tip
}
