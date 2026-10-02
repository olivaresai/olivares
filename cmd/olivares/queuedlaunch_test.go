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
)

type queuedRunPolicy struct{ question *auth.Request }

func (p queuedRunPolicy) Evaluate(_ context.Context, request auth.Request) (auth.Decision, error) {
	*p.question = request
	return auth.Decision{Allow: request.Resource.Kind != "run", Reason: "run launches forbidden"}, nil
}

func TestQueuedSessionLaunchUsesRESTPolicyQuestion(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	principal, err := h.authr.Authenticate(ctx, h.adminToken)
	if err != nil {
		t.Fatal(err)
	}
	credential, bound := auth.QueuedCredentialFrom(principal)
	if !bound {
		t.Fatal("authenticated launcher has no credential reference")
	}
	credential, err = h.authr.BindQueuedCredential(ctx, credential)
	if err != nil {
		t.Fatal(err)
	}
	var question auth.Request
	workspace, run := model.NewID(), model.NewID().String()
	_, err = authorizeQueuedSessionLaunch(ctx, h.authr, auth.NewAuthorizer(queuedRunPolicy{&question}), model.TenantID(h.tenantA), credential, run, workspace)
	if !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("queued launch bypassed the REST run forbid: %v", err)
	}
	if question.Resource.ID != run || question.Resource.WorkspaceID != workspace || question.Principal.Actor() != principal.Actor() {
		t.Fatal("policy lost the stored run, workspace or authenticated launcher")
	}
}
