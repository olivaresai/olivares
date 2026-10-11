// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/internal/approvalbridge"
	"github.com/olivaresai/olivares/connectors/redact"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/modules/sessions"
)

// Provider requests retain their existing turn/claim revalidation and method-specific
// reply codecs. This adapter only asks the existing human queue for one decision.
type providerApprovalAdapter struct {
	bridge       *approvalBridge
	approvalWait func(context.Context, auth.Principal, string, time.Time) (func(), error)
	reviewFacts  func(context.Context, model.TenantID, sessions.ProviderApprovalRequest) (sessions.ProviderApprovalRequest, error)
	reviewReason func(context.Context, model.TenantID, sessions.ProviderApprovalRequest, string) (string, []redact.GeneratedMaskSpan, error)
}

func (a providerApprovalAdapter) Approve(ctx context.Context, tenant model.TenantID, req sessions.ProviderApprovalRequest) (sessions.ProviderApprovalDecision, error) {
	if a.bridge == nil || a.bridge.LocalProposer == nil {
		return sessions.ProviderApprovalDecision{Reason: "approval service unavailable"}, nil
	}
	cred, ok := a.bridge.ProviderCred(tenant)
	if !ok {
		return sessions.ProviderApprovalDecision{Reason: "approval service unavailable"}, nil
	}
	if (req.Kind == "command_execution" || req.Kind == "file_change" || req.Kind == "legacy_exec_command" || req.Kind == "legacy_apply_patch") && !req.FactsComplete {
		return sessions.ProviderApprovalDecision{Reason: "provider did not supply the complete command or file paths for review"}, nil
	}
	// Requested grants remain the original authority; only display facts leave the
	// child. The same projection used by the policy ledger protects this queue.
	granted := append([]string(nil), req.Requested...)
	effective := providerEffectiveFacts(req)
	if a.reviewFacts != nil {
		reviewed, err := a.reviewFacts(ctx, tenant, req)
		if err != nil {
			return sessions.ProviderApprovalDecision{Reason: err.Error()}, nil
		}
		req = reviewed
	}

	consumer := newSingleUseConsumerID()
	question, err := json.Marshal(struct {
		Request  sessions.ProviderApprovalRequest
		Consumer string
	}{req, consumer})
	if err != nil {
		return sessions.ProviderApprovalDecision{}, err
	}
	plan := hexSHA(string(question))
	subject := approvalbridge.EncodeSubjectRef(req.RunRef, plan)
	const action = "sessions.provider.approval"
	const kind = "session_run"
	if deadline, ok := ctx.Deadline(); ok {
		seconds := int64(time.Until(deadline).Seconds()) + 1
		if seconds < 1 {
			return sessions.ProviderApprovalDecision{Reason: "approval expired"}, nil
		}
		if seconds < cred.ExpiresIn {
			cred.ExpiresIn = seconds
		}
	}
	type permissionScope struct {
		Permissions []string
		CommandLine string
		FilePaths   []string
	}
	scope, err := json.Marshal(permissionScope{req.Requested, req.CommandLine, req.FilePaths})
	if err != nil {
		return sessions.ProviderApprovalDecision{}, err
	}
	rawScope, err := json.Marshal(permissionScope{effective.Requested, effective.CommandLine, effective.FilePaths})
	if err != nil {
		return sessions.ProviderApprovalDecision{}, err
	}
	reason := fmt.Sprintf("Provider permission: driver=%s method=%s kind=%s run=%s turn=%s permissions=%s", effective.Driver, effective.Method, effective.Kind, effective.RunRef, effective.TurnID, rawScope)
	var reasonMasks []redact.GeneratedMaskSpan
	if a.reviewReason != nil {
		reason, reasonMasks, err = a.reviewReason(ctx, tenant, req, reason)
		if err != nil {
			return sessions.ProviderApprovalDecision{Reason: err.Error()}, nil
		}
	}
	display, err := redact.CleanMasked(reason, reasonMasks)
	if err != nil {
		return sessions.ProviderApprovalDecision{Reason: "permission scope is not reviewable"}, nil
	}
	// A secret can match a serialized key or span fields. The final display must
	// show exactly the independently reviewed operation before anything queues.
	expectedDisplay := fmt.Sprintf("Provider permission: driver=%s method=%s kind=%s run=%s turn=%s permissions=%s", req.Driver, req.Method, req.Kind, req.RunRef, req.TurnID, scope)
	if display != expectedDisplay {
		return sessions.ProviderApprovalDecision{Reason: "permission scope is not reviewable"}, nil
	}
	if len(reason) > 2000 || len(display) > 2000 {
		return sessions.ProviderApprovalDecision{Reason: "permission scope is too large for human review"}, nil
	}
	review, reviewMasks, reviewDisplay, err := a.structuredReview(ctx, tenant, effective, req)
	if err != nil {
		return sessions.ProviderApprovalDecision{Reason: "permission scope is not reviewable"}, nil
	}
	effect, err := holdEffect(ctx, a.bridge.LocalProposer, heldEffect{
		Tenant: tenant, Principal: req.Principal,
		// These offsets belong to pre-pattern bytes. Request applies the same shared
		// cleaner once; carrying offsets against display would be invalid provenance.
		Request:  governance.ApprovalRequest{Action: action, SubjectKind: kind, SubjectRef: subject, Reason: reason, ReasonMasks: reasonMasks, Review: review, ReviewMasks: reviewMasks, SessionRef: req.SessionRef, ExpiresInSeconds: cred.ExpiresIn, EscalateInSeconds: cred.EscalateIn},
		Consumer: consumer, Wait: a.approvalWait, Log: a.bridge.Log,
		Shown: func(approval governance.Approval) bool {
			return approval.Reason == display && (review == nil || approval.Review != nil && approval.Review.Tool == review.Tool && approval.Review.Text == reviewDisplay)
		},
	})
	if effect.Failed == stepRegister {
		return sessions.ProviderApprovalDecision{Reason: "session could not register its approval wait"}, err
	}
	if err != nil {
		return sessions.ProviderApprovalDecision{}, err
	}
	switch effect.Outcome {
	case effectAllowed:
		return sessions.ProviderApprovalDecision{Allow: true, Granted: granted}, nil
	case effectUnshown:
		return sessions.ProviderApprovalDecision{Reason: "permission scope changed during review"}, nil
	case effectForeign:
		return sessions.ProviderApprovalDecision{Reason: "approval scope changed"}, nil
	case effectRefused:
		return sessions.ProviderApprovalDecision{Reason: "human review " + effect.Status}, nil
	default:
		return sessions.ProviderApprovalDecision{Reason: "approval is no longer spendable"}, nil
	}
}

// The human sees the same field projection already proven by reviewFacts. Mask
// the original facts again only to retain generated offsets on pre-clean bytes;
// never attach those offsets to the cleaned display or discover them in text.
func (a providerApprovalAdapter) structuredReview(ctx context.Context, tenant model.TenantID, effective, reviewed sessions.ProviderApprovalRequest) (*governance.ApprovalReview, []redact.GeneratedMaskSpan, string, error) {
	var tool, text, expected string
	command := false
	switch {
	case effective.CommandLine != "":
		tool, text, expected = "Command", effective.CommandLine, reviewed.CommandLine
		command = true
	case len(effective.FilePaths) != 0:
		tool, text, expected = "File change", strings.Join(effective.FilePaths, "\n"), strings.Join(reviewed.FilePaths, "\n")
	case len(effective.Requested) != 0:
		tool, text, expected = "Provider permission", strings.Join(effective.Requested, "\n"), strings.Join(reviewed.Requested, "\n")
	default:
		return nil, nil, "", nil
	}
	var masks []redact.GeneratedMaskSpan
	var err error
	if a.reviewReason != nil {
		text, masks, err = a.reviewReason(ctx, tenant, reviewed, text)
		if err != nil {
			return nil, nil, "", err
		}
	}
	display, err := redact.CleanMasked(text, masks)
	if err != nil || display != expected {
		return nil, nil, "", errors.New("structured provider review changed the proven facts")
	}
	if command {
		if _, err := redact.ReviewableShellCommand(effective.CommandLine, display); err != nil {
			return nil, nil, "", err
		}
	}
	return &governance.ApprovalReview{Tool: tool, Text: text}, masks, display, nil
}
