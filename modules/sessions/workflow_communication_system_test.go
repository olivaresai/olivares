// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestWorkflowSystemResultPersistsWithAgentBackedAuthority(t *testing.T) {
	fixture := newWorkflowCommunicationFixture(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	agentID := model.NewID()
	if err := fixture.st.Mutate(ctx, fixture.tenant, func(sc store.Scope) error {
		items, err := sc.Ext(workItemKind)
		if err != nil {
			return err
		}
		work, err := items.Get(ctx, fixture.workID)
		if err != nil {
			return err
		}
		work[colWorkOwnerKind] = string(RecipientAgent)
		work[colWorkOwnerRef] = agentID.String()
		work[colWorkOwnerEpoch] = work.Int(colWorkOwnerEpoch) + 1
		_, err = items.Update(ctx, work)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	grantID := model.NewID()
	grant := ChannelGrant{
		MutableCommunicationEntity: MutableCommunicationEntity{CommunicationEntity: CommunicationEntity{
			ID: grantID, TenantID: fixture.tenant, WorkspaceID: fixture.workspace,
			Version: 1, CreatedAt: fixture.now,
		}, UpdatedAt: fixture.now},
		ChannelID:  fixture.channel.ID,
		Subject:    CommunicationSubjectRef{Kind: SubjectAgent, Ref: agentID.String()},
		Generation: 1, CanRead: true, CanWrite: true, State: ChannelGrantActive,
		GrantedBy: CommunicationActorRef{Kind: ActorUser, Ref: fixture.sender.String()},
	}
	grantRecord, err := channelGrantToRecord(grant)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := communicationCreateFencedWithID(
		ctx, fixture.m, fixture.st, fixture.tenant, channelGrantKind, grantID, grantRecord, fixture.sender,
	); err != nil {
		t.Fatal(err)
	}
	operator, err := auth.NewSystemOperator("test:workflow-result", "scheduled session completed")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := fixture.authr.ResolveSystemScope(ctx, operator, fixture.tenant, auth.CommunicationSessionMessageSendWrite)
	if err != nil {
		t.Fatal(err)
	}
	fixture.source.evidence.FreshUntil = time.Now().Add(25 * time.Second)
	result, err := fixture.m.SendWorkflowWorkTask(ctx, fixture.tenant, WorkflowWorkTaskCommand{
		Actor: WorkflowCommunicationActor{
			AuditKind: model.ActorSystem, AuditRef: "system:orchestration-cron", SystemPrincipal: principal,
		},
		WorkItemID: fixture.workID, ChannelID: fixture.channel.ID, Recipient: fixture.target,
		Content: MessageContent{Subject: "Scheduled result", Blocks: []MessageContentBlock{{
			Type: ContentBlockReference, Reference: &ContentReference{Kind: "workflow_body", Ref: "run:scheduled/worktree/diff?base=base&head=head&exit=0"},
		}}},
		IdempotencyKey: "workflow-system-result:" + model.NewID().String(),
	})
	if err != nil {
		t.Fatalf("send system workflow result: %v", err)
	}
	record := workflowCommunicationRecord(t, fixture, messageKind, result.MessageID)
	message, err := messageFromRecord(record, 0)
	if err != nil {
		t.Fatal(err)
	}
	if message.Kind != MessageSystem || message.WorkItemID != fixture.workID ||
		message.Sender != (CommunicationActorRef{Kind: ActorSystem, Ref: "orchestration-cron"}) {
		t.Fatalf("stored system result = %+v", message)
	}
}
