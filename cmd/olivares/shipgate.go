// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/gitpublish"
)

// shipGate is the approval policy's door before a ship's push. The tenant's own
// approval policies decide whether a person must review the push: with none
// that matches the governed action "gitpublish.push" on a publication target,
// the ship is granted; with one, the push waits for a decision of another
// person (separation of duty is the approval service's, as for every approval)
// and an approved grant is spent once, under the ship's operation id, so a
// retry of the same ship re-obtains it and no other ship can use it.
type shipGate struct {
	policy shipPolicy
	bridge shipBridge
}

type shipPolicy interface {
	ReviewPolicy(ctx context.Context, tenant model.TenantID, action, subjectKind string) (string, error)
}

type shipBridge interface {
	GateUnspentForSession(ctx context.Context, tenant model.TenantID, action, subjectKind, subjectRef, planHash, reason, requestedBy, sessionRef string) (ref, status, boundHash string, err error)
	ConsumeApproval(ctx context.Context, tenant model.TenantID, ref, consumerID, policyVersion string) (granted, replay bool, err error)
}

func (g shipGate) Authorize(ctx context.Context, tenant model.TenantID, r gitpublish.GateRequest) (gitpublish.GateDecision, error) {
	policy, err := g.policy.ReviewPolicy(ctx, tenant, r.Action, r.SubjectKind)
	if err != nil {
		return gitpublish.GateDecision{}, err
	}
	if policy == "" {
		return gitpublish.GateDecision{State: gitpublish.GateGranted}, nil
	}
	ref, status, _, err := g.bridge.GateUnspentForSession(ctx, tenant, r.Action, r.SubjectKind, r.SubjectRef, r.PlanHash, r.Reason, r.RequestedBy, "")
	if err != nil {
		return gitpublish.GateDecision{}, err
	}
	switch status {
	case nbPending:
		return gitpublish.GateDecision{Ref: ref, State: gitpublish.GatePending}, nil
	case nbApproved:
		granted, replay, err := g.bridge.ConsumeApproval(ctx, tenant, ref, r.ConsumerID, "")
		if err != nil {
			return gitpublish.GateDecision{}, err
		}
		if granted && !replay {
			return gitpublish.GateDecision{Ref: ref, State: gitpublish.GateGranted}, nil
		}
	}
	// Rejected, canceled, expired, spent by another, no gate or an emergency
	// grant: none of them is a person's approval of this push.
	return gitpublish.GateDecision{Ref: ref, State: gitpublish.GateDenied}, nil
}
