// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

// sessionWorkCredentialSource is the sole composition-root bridge from the
// sessions runtime to core/auth's exact-SID issuer. The module supplies facts it
// has already established server-side (canonical SID, live Claim, run and agent);
// this adapter never accepts HTTP input and never exposes a general token issuer.
type sessionWorkCredentialSource struct {
	authenticator *auth.Authenticator
}

func (s sessionWorkCredentialSource) Mint(
	ctx context.Context,
	req sessions.WorkSessionCredentialRequest,
) (sessions.WorkSessionCredential, error) {
	if s.authenticator == nil {
		return sessions.WorkSessionCredential{}, fmt.Errorf("work-session credential issuer is not wired")
	}
	actor, err := auth.NewSystemOperator(
		"sessions-runtime",
		"mint a purpose-restricted credential for an admitted session process",
	)
	if err != nil {
		return sessions.WorkSessionCredential{}, err
	}
	issued, err := s.authenticator.IssueWorkSessionCredential(ctx, actor, auth.WorkSessionCredentialSpec{
		Tenant: req.Tenant, SessionRef: req.SessionRef, RunRef: req.RunRef,
		AgentRef: req.AgentRef, ClaimFence: req.ClaimFence,
	})
	if err != nil {
		return sessions.WorkSessionCredential{}, err
	}
	return sessions.WorkSessionCredential{
		ID: issued.ID, Token: issued.Token, Tenant: issued.Tenant,
		SessionRef: issued.SessionRef, RunRef: issued.RunRef, AgentRef: issued.AgentRef,
		ClaimFence: issued.ClaimFence, NotAfter: issued.ExpiresAt,
	}, nil
}

func (s sessionWorkCredentialSource) Revoke(
	ctx context.Context,
	id model.ID,
	expected sessions.WorkSessionCredentialRequest,
) error {
	if s.authenticator == nil {
		return fmt.Errorf("work-session credential issuer is not wired")
	}
	actor, err := auth.NewSystemOperator(
		"sessions-runtime",
		"revoke the admitted session process credential after its authority ended",
	)
	if err != nil {
		return err
	}
	return s.authenticator.RevokeWorkSessionCredential(ctx, actor, id, auth.WorkSessionCredentialSpec{
		Tenant: expected.Tenant, SessionRef: expected.SessionRef,
		RunRef: expected.RunRef, AgentRef: expected.AgentRef, ClaimFence: expected.ClaimFence,
	})
}

func (s sessionWorkCredentialSource) Renew(
	ctx context.Context,
	id model.ID,
	expected sessions.WorkSessionCredentialRequest,
) (time.Time, error) {
	if s.authenticator == nil {
		return time.Time{}, fmt.Errorf("work-session credential issuer is not wired")
	}
	actor, err := auth.NewSystemOperator(
		"sessions-runtime",
		"renew an admitted session credential after its live Claim heartbeat",
	)
	if err != nil {
		return time.Time{}, err
	}
	return s.authenticator.RenewWorkSessionCredential(ctx, actor, id, auth.WorkSessionCredentialSpec{
		Tenant: expected.Tenant, SessionRef: expected.SessionRef,
		RunRef: expected.RunRef, AgentRef: expected.AgentRef, ClaimFence: expected.ClaimFence,
	})
}
