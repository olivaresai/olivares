// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

func TestWorkflowMessageReadRequiresCurrentRecipientAuthority(t *testing.T) {
	t.Parallel()
	f := newWorkflowBindingFixture(t, workflowSQLiteBackend(t, "workflow-message-read"), false)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := f.task(f.bound, "workflow-message-read")
	published, err := f.send(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	opens := 0
	opener := func(ctx context.Context, sealer CommunicationContentSealer, plan ProtectedPayloadOpenPlan) (json.RawMessage, error) {
		opens++
		return OpenProtectedPayload(ctx, sealer, plan)
	}
	read := func(scope DirectoryScopeRef, ref auth.PrincipalRef) (DirectNoticeReadResult, error) {
		return f.m.getDirectNoticeMessageWithCurrentAuthority(ctx, scope, ref, published.MessageID, opener, false)
	}
	before := f.effectCounts()
	got, err := read(f.scope, f.targetRef)
	if err != nil || got.Message.ID != published.MessageID || got.Delivery.ID != published.DeliveryID ||
		!canonicalCommunicationValueEqual(got.Message.Content, cmd.Content) {
		t.Fatalf("recipient reads workflow message: %+v, %v", got, err)
	}
	if opens != 1 {
		t.Fatalf("payload opens = %d, want 1", opens)
	}
	f.requireNoEffect(before, "message read")
	for _, test := range []struct {
		name  string
		scope DirectoryScopeRef
		ref   auth.PrincipalRef
	}{
		{"sender is not recipient", f.scope, f.ref},
		{"other workspace", DirectoryScopeRef{TenantID: f.tenant, WorkspaceID: model.NewID()}, f.targetRef},
		{"other tenant", DirectoryScopeRef{TenantID: model.NewTenantID(), WorkspaceID: f.workspace}, f.targetRef},
	} {
		if _, err := read(test.scope, test.ref); err == nil {
			t.Fatalf("%s: unauthorized read succeeded", test.name)
		}
	}
	for _, row := range communicationRowsForTest(t, f.directNoticeFixture, channelGrantKind) {
		grant, err := channelGrantFromRecord(row)
		if err != nil {
			t.Fatal(err)
		}
		if grant.Subject.Ref != f.target.Ref || grant.State != ChannelGrantActive {
			continue
		}
		grant.State = ChannelGrantRevoked
		grant.RevokedBy = &CommunicationActorRef{Kind: ActorUser, Ref: f.sender.String()}
		record, err := channelGrantToRecord(grant)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := communicationUpdate(ctx, f.m, f.tenant, channelGrantKind, record); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := read(f.scope, f.targetRef); !errors.Is(err, ErrCommunicationNotFound) {
		t.Fatalf("recipient after grant revocation = %v, want hidden message", err)
	}
	if opens != 1 {
		t.Fatalf("refused reads opened content: %d opens", opens)
	}
}
