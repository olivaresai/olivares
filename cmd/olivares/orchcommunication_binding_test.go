// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/orchestration"
	"github.com/olivaresai/olivares/modules/sessions"
)

func TestWorkflowCommunicationActorCarriesTheRunBinding(t *testing.T) {
	binding, err := auth.ParseCredentialBindingStorage(model.NewID().String())
	if err != nil {
		t.Fatal(err)
	}
	run := model.NewID()
	actor, err := workflowCommunicationActor(orchestration.WorkActor{
		Kind: model.ActorUser, Ref: "user:x", UserIdentity: model.NewID(),
		CredentialBinding: binding, RunID: run,
	})
	if err != nil {
		t.Fatal(err)
	}
	if actor.CredentialBinding != binding || actor.CredentialSubject != run {
		t.Fatalf("adapter dropped the run binding: subject %s", actor.CredentialSubject)
	}
}

func TestWorkflowCommunicationAdapterNamesReauthenticationAndDenial(t *testing.T) {
	workItem := model.NewID()
	req := orchestration.WorkMessageRequest{
		Actor:      orchestration.WorkActor{Kind: model.ActorUser, Ref: "user:x", UserIdentity: model.NewID()},
		WorkItemID: workItem, ChannelID: model.NewID(),
		Recipient: orchestration.WorkParticipant{Kind: "user", Ref: model.NewID().String()},
		Body:      "continue",
	}
	for _, test := range []struct {
		kernel error
		want   error
	}{
		{sessions.ErrWorkflowReauthenticationRequired, orchestration.ErrWorkflowReauthenticationRequired},
		{sessions.ErrCommunicationForbidden, orchestration.ErrWorkflowEffectDenied},
	} {
		kernel := &recordingWorkflowCommunicationKernel{messageErr: test.kernel}
		adapter := &workflowCommunicationAdapter{kernel: kernel}
		_, err := adapter.SendWorkMessage(context.Background(), model.NewTenantID(), req)
		if !errors.Is(err, test.want) || !errors.Is(err, test.kernel) {
			t.Fatalf("adapter error = %v, want %v wrapping %v", err, test.want, test.kernel)
		}
	}
}
