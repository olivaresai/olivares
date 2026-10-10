// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/internal/approvalbridge"
	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/connectors/redact"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/governance"
)

// Leave room for execution and the terminal reply within the MCP request's 60s
// bound. A caller disconnect, session stop or earlier policy expiry ends sooner.
const managedMCPApprovalWait = 50 * time.Second
const maxManagedMCPReviewArgumentsBytes = 16 * 1024

// Managed sessions propose as their one issued principal. The existing human
// queue owns decisions; this adapter holds the same call until a terminal answer.
type managedSessionApprovalGate struct {
	m                    *mcpManagement
	tenant               model.TenantID
	principal            auth.Principal
	serverID, serverName string
	check                func(context.Context) error
}

func (g managedSessionApprovalGate) Authorize(ctx context.Context, req mcpc.ToolApprovalRequest) (mcpc.GateDecision, error) {
	decision := func(ref string, status mcpc.GateStatus) (mcpc.GateDecision, error) {
		return mcpc.GateDecision{ApprovalRef: ref, Status: status, PlanHash: req.PlanHash}, nil
	}
	if req.Tenant != g.tenant.String() || req.Subject != g.principal.SessionIdentity || req.RequestedBy != req.Subject || req.PlanHash == "" || g.check(ctx) != nil {
		return decision("", mcpc.StatusRejected)
	}
	// The preview is complete or refused, never a truncated plan the reviewer
	// could mistake for the entire effect. Execution keeps the original bytes.
	if len(req.Arguments) == 0 || len(req.Arguments) > maxManagedMCPReviewArgumentsBytes || !json.Valid(req.Arguments) {
		return mcpc.GateDecision{}, mcpc.ErrArgumentsNotReviewable
	}
	preview, err := redact.ReviewableJSON(req.Arguments)
	if err != nil {
		return mcpc.GateDecision{}, mcpc.ErrArgumentsNotReviewable
	}
	reason := "MCP tool: " + redact.Clean(req.Tool) + "\nserver: " + redact.Clean(g.serverName) + "\narguments: " + string(preview)
	waitCtx, cancel := context.WithTimeout(ctx, managedMCPApprovalWait)
	defer cancel()
	consumer := newSingleUseConsumerID()
	a, err := holdEffect(waitCtx, g.m.eng.engineApprovals, heldEffect{
		Tenant: g.tenant, Principal: g.principal,
		Request: governance.ApprovalRequest{
			SessionRef: g.principal.SessionIdentity, Action: "mcp.tool.call", SubjectKind: "tool",
			SubjectRef:       approvalbridge.EncodeSubjectRef(g.principal.SessionRunRef, g.serverID+":"+req.PlanHash+":"+consumer),
			Reason:           reason,
			ExpiresInSeconds: int64(managedMCPApprovalWait / time.Second),
		},
		Consumer: consumer, PolicyVersion: "mcp-toolcall-v1", Wait: g.m.eng.sessionsMod.BeginApprovalWait, Log: g.m.eng.log,
		// Human review cannot outlive credential, claim, launcher, server policy or
		// process authority. The guarded upstream repeats this before dispatch.
		Recheck: g.check,
	})
	switch {
	case a.Failed == stepWait && errors.Is(err, context.DeadlineExceeded):
		return decision(a.Ref, mcpc.StatusExpired)
	case err == nil && a.Outcome == effectAllowed:
		// This call requested a fresh approval and just spent it once (gate.go).
		return mcpc.GateDecision{ApprovalRef: a.Ref, Status: mcpc.StatusApproved, PlanHash: req.PlanHash, Spent: a.Spent}, nil
	case err == nil && a.Outcome == effectRefused && a.Status != nbApproved:
		// A human answer that did not approve keeps its own status.
		return decision(a.Ref, mapMCPGateStatus(a.Status))
	}
	return decision(a.Ref, mcpc.StatusRejected)
}
