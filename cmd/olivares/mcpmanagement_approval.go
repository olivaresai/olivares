// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"time"

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
	ref := ""
	decision := func(status mcpc.GateStatus) (mcpc.GateDecision, error) {
		return mcpc.GateDecision{ApprovalRef: ref, Status: status, PlanHash: req.PlanHash}, nil
	}
	if req.Tenant != g.tenant.String() || req.Subject != g.principal.SessionIdentity || req.RequestedBy != req.Subject || req.PlanHash == "" || g.check(ctx) != nil {
		return decision(mcpc.StatusRejected)
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
	service := g.m.eng.engineApprovals
	waitCtx, cancel := context.WithTimeout(ctx, managedMCPApprovalWait)
	defer cancel()
	deadline, _ := waitCtx.Deadline()
	consumer := newSingleUseConsumerID()
	subject := encodeSubjectRef(g.principal.SessionRunRef, g.serverID+":"+req.PlanHash+":"+consumer)
	request, err := service.Request(waitCtx, g.tenant, g.principal, governance.ApprovalRequest{
		SessionRef: g.principal.SessionIdentity, Action: "mcp.tool.call", SubjectKind: "tool", SubjectRef: subject,
		Reason:           reason,
		ExpiresInSeconds: int64(managedMCPApprovalWait / time.Second),
	})
	if err != nil {
		return decision(mcpc.StatusRejected)
	}
	ref = request.ID
	pending := true
	// Withdraw on interrupted/failed waits too. A racing terminal decision is
	// retained; Cancel cannot overwrite an approved, rejected or expired answer.
	defer func() {
		if !pending {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cleanupCancel()
		closed, err := service.Cancel(cleanupCtx, g.tenant, g.principal, ref)
		if err != nil && (closed.ID == "" || closed.Status == nbPending) {
			g.m.eng.log.Warn("session MCP: approval cancellation incomplete", "approval_ref", ref)
		}
	}()
	if request.ExpiresAt != "" {
		expires, err := model.ParseTimestamp(request.ExpiresAt)
		if err != nil {
			return decision(mcpc.StatusRejected)
		}
		if expires.Time().Before(deadline) {
			deadline = expires.Time()
		}
	}
	endWait, err := g.m.eng.sessionsMod.BeginApprovalWait(waitCtx, g.principal, ref, deadline)
	if err != nil {
		return decision(mcpc.StatusRejected)
	}
	defer endWait()
	answer, err := service.Wait(waitCtx, g.tenant, ref)
	endWait()
	if errors.Is(err, context.DeadlineExceeded) {
		return decision(mcpc.StatusExpired)
	}
	if err != nil {
		return decision(mcpc.StatusRejected)
	}
	pending = false
	if answer.Status != nbApproved {
		return decision(mapMCPGateStatus(answer.Status))
	}
	if answer.SessionRef != g.principal.SessionIdentity || answer.Action != "mcp.tool.call" || answer.SubjectKind != "tool" || answer.SubjectRef != subject {
		return decision(mcpc.StatusRejected)
	}
	// Human review cannot outlive credential, claim, launcher, server policy or
	// process authority. The guarded upstream repeats this before dispatch.
	if g.check(waitCtx) != nil {
		return decision(mcpc.StatusRejected)
	}
	spent, err := service.Consume(waitCtx, g.tenant, ref, consumer, "mcp-toolcall-v1")
	if err != nil || !spent.Granted || spent.Replay {
		return decision(mcpc.StatusRejected)
	}
	return decision(mcpc.StatusApproved)
}
