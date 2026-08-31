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

// sessionCommunicationCredentialSource is the private composition bridge from
// the sessions runtime to core/auth's communication-session issuer. Every field
// is server-derived after Claim admission; no HTTP payload can select a purpose
// or widen the issuer's four-permission ceiling.
type sessionCommunicationCredentialSource struct {
	authenticator *auth.Authenticator
}

func (s sessionCommunicationCredentialSource) Mint(
	ctx context.Context,
	req sessions.CommunicationSessionCredentialRequest,
) (sessions.CommunicationSessionCredential, error) {
	if s.authenticator == nil {
		return sessions.CommunicationSessionCredential{}, fmt.Errorf(
			"communication-session credential issuer is not wired",
		)
	}
	actor, err := auth.NewSystemOperator(
		"sessions-runtime",
		"mint a purpose-restricted communication credential for an admitted session process",
	)
	if err != nil {
		return sessions.CommunicationSessionCredential{}, err
	}
	issued, err := s.authenticator.IssueCommunicationSessionCredential(
		ctx, actor, communicationSessionCredentialSpec(req),
	)
	if err != nil {
		return sessions.CommunicationSessionCredential{}, err
	}
	return sessions.CommunicationSessionCredential{
		ID: issued.ID, Token: issued.Token, Tenant: issued.Tenant,
		WorkspaceID: issued.WorkspaceID, SessionRef: issued.SessionRef,
		RunRef: issued.RunRef, AgentRef: issued.AgentRef,
		ClaimFence: issued.ClaimFence, NotAfter: issued.ExpiresAt,
	}, nil
}

func (s sessionCommunicationCredentialSource) Renew(
	ctx context.Context,
	id model.ID,
	expected sessions.CommunicationSessionCredentialRequest,
) (time.Time, error) {
	if s.authenticator == nil {
		return time.Time{}, fmt.Errorf("communication-session credential issuer is not wired")
	}
	actor, err := auth.NewSystemOperator(
		"sessions-runtime",
		"renew an admitted communication credential after its live Claim heartbeat",
	)
	if err != nil {
		return time.Time{}, err
	}
	return s.authenticator.RenewCommunicationSessionCredential(
		ctx, actor, id, communicationSessionCredentialSpec(expected),
	)
}

func (s sessionCommunicationCredentialSource) Revoke(
	ctx context.Context,
	id model.ID,
	expected sessions.CommunicationSessionCredentialRequest,
) error {
	if s.authenticator == nil {
		return fmt.Errorf("communication-session credential issuer is not wired")
	}
	actor, err := auth.NewSystemOperator(
		"sessions-runtime",
		"revoke an admitted communication credential after its authority ended",
	)
	if err != nil {
		return err
	}
	return s.authenticator.RevokeCommunicationSessionCredential(
		ctx, actor, id, communicationSessionCredentialSpec(expected),
	)
}

func communicationSessionCredentialSpec(
	req sessions.CommunicationSessionCredentialRequest,
) auth.CommunicationSessionCredentialSpec {
	return auth.CommunicationSessionCredentialSpec{
		Tenant: req.Tenant, WorkspaceID: req.WorkspaceID,
		SessionRef: req.SessionRef, RunRef: req.RunRef,
		AgentRef: req.AgentRef, ClaimFence: req.ClaimFence,
	}
}
