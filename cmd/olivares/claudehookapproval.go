// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/claude"
	"github.com/olivaresai/olivares/connectors/redact"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/governance"
)

const claudeApprovalWait = 90 * time.Second
const maxClaudeReviewCommandBytes = 16 * 1024

// Only the reviewable command or target path leaves the raw hook input. The
// existing queue and its views receive the same redacted facts, never file text.
func claudeToolApprovalReason(in claude.HookDecisionInput, disp hookDisposition) (string, error) {
	reason, spans, err := claudeToolApprovalFacts(in, disp, func(text string) (string, []redact.GeneratedMaskSpan, error) { return text, nil, nil })
	if err != nil {
		return "", err
	}
	return redact.CleanMasked(reason, spans)
}

// The request carries pre-pattern-clean text and offsets produced by the exact
// masker. Prefixing shifts those offsets; cleaning never supplies new provenance.
// The queue applies the same pattern floor once to retain the proven display.
func claudeToolApprovalFacts(in claude.HookDecisionInput, disp hookDisposition, mask func(string) (string, []redact.GeneratedMaskSpan, error)) (string, []redact.GeneratedMaskSpan, error) {
	facts, err := buildClaudeApprovalFacts(in, disp, mask)
	return facts.reason, facts.reasonMasks, err
}

type claudeApprovalFacts struct {
	reason      string
	reasonMasks []redact.GeneratedMaskSpan
	review      *governance.ApprovalReview
	reviewMasks []redact.GeneratedMaskSpan
}

// The review boundary is recorded while building the facts, never discovered
// by parsing prose. Both displays carry the same masker-produced provenance.
func buildClaudeApprovalFacts(in claude.HookDecisionInput, disp hookDisposition, mask func(string) (string, []redact.GeneratedMaskSpan, error)) (claudeApprovalFacts, error) {
	input := in.RewriteBase()
	var rewritten claude.HookDecisionResult
	applyRewrite(&rewritten, in, disp)
	if rewritten.UpdatedInput != nil {
		input = rewritten.UpdatedInput
	}
	tool, toolSpans, err := mask(in.Tool)
	if err != nil {
		return claudeApprovalFacts{}, err
	}
	toolDisplay, err := redact.CleanMasked(tool, toolSpans)
	if err != nil || toolDisplay != in.Tool {
		return claudeApprovalFacts{}, errors.New("tool name is unavailable for human review")
	}
	prefix := "Claude Code requests " + toolDisplay
	var reason strings.Builder
	reason.WriteString(prefix)
	var spans []redact.GeneratedMaskSpan
	var reviewStart int
	appendMasked := func(text string) error {
		masked, generated, err := mask(text)
		if err != nil {
			return err
		}
		offset := reason.Len()
		for _, span := range generated {
			spans = append(spans, redact.GeneratedMaskSpan{Start: offset + span.Start, End: offset + span.End})
		}
		reason.WriteString(masked)
		return nil
	}
	switch in.Tool {
	case "Bash":
		command, _ := input["command"].(string)
		if strings.TrimSpace(command) == "" {
			return claudeApprovalFacts{}, errors.New("command is unavailable for human review")
		}
		if len(command) > maxClaudeReviewCommandBytes {
			return claudeApprovalFacts{}, fmt.Errorf("command too long to review (%d bytes)", len(command))
		}
		reason.WriteString("\ncommand: ")
		reviewStart = reason.Len()
		if err := appendMasked(command); err != nil {
			return claudeApprovalFacts{}, err
		}
	case "Read", "Edit", "Write":
		path, _ := input["file_path"].(string)
		if path == "" {
			if rewritten.UpdatedInput != nil {
				return claudeApprovalFacts{}, errors.New("rewritten path is unavailable for human review")
			}
			path = in.ResourceRef
		}
		reason.WriteString("\npath: ")
		reviewStart = reason.Len()
		if err := appendMasked(path); err != nil {
			return claudeApprovalFacts{}, err
		}
	default:
		if rewritten.UpdatedInput != nil {
			return claudeApprovalFacts{}, errors.New("rewritten tool input is unavailable for human review")
		}
		reason.WriteString(" on ")
		reviewStart = reason.Len()
		if err := appendMasked(in.ResourceKind); err != nil {
			return claudeApprovalFacts{}, err
		}
		reason.WriteString(": ")
		if err := appendMasked(in.ResourceRef); err != nil {
			return claudeApprovalFacts{}, err
		}
	}
	reviewText := reason.String()[reviewStart:]
	reviewMasks := make([]redact.GeneratedMaskSpan, 0, len(spans))
	for _, span := range spans {
		reviewMasks = append(reviewMasks, redact.GeneratedMaskSpan{Start: span.Start - reviewStart, End: span.End - reviewStart})
	}
	display, err := redact.CleanMasked(reviewText, reviewMasks)
	if err != nil {
		return claudeApprovalFacts{}, err
	}
	if in.Tool == "Bash" {
		command, _ := input["command"].(string)
		if _, err := reviewableShellCommand(command, display); err != nil {
			return claudeApprovalFacts{}, err
		}
	}
	return claudeApprovalFacts{
		reason: reason.String(), reasonMasks: spans,
		review: &governance.ApprovalReview{Tool: toolDisplay, Text: reviewText}, reviewMasks: reviewMasks,
	}, nil
}

// Kept local for the Claude call sites; native provider adapters use the same
// shared guard without importing the composition root.
func reviewableShellCommand(command, redacted string) (string, error) {
	return redact.ReviewableShellCommand(command, redacted)
}

// The review authorizes the exact effective input, including unchanged fields.
// A live policy recheck may tighten access, but it cannot substitute another
// input after the person has approved. This digest stays request-local.
func claudeApprovalInputHash(in claude.HookDecisionInput, result claude.HookDecisionResult) ([sha256.Size]byte, error) {
	input := in.RewriteBase()
	if result.UpdatedInput != nil {
		input = result.UpdatedInput
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(encoded), nil
}

type claudeReviewedCallKey struct{}
type claudeReviewedCall struct{ run, plan string }

// The vendor must receive a terminal decision before its hook timeout. The
// session itself proposes; its launching administrator can decide that request.
func (d *claudeHookDecider) gateViaSessionApproval(ctx context.Context, tenant model.TenantID, p auth.Principal, in claude.HookDecisionInput, disp hookDisposition, actor, tier, version string) claude.HookDecisionResult {
	credentials, ok := d.authr.(*sessionHookCredentials)
	if !ok || d.approvals == nil {
		return deny("human approval service is unavailable", actor, tier, version)
	}
	// Unknown tools have only a derived reference for their reviewer. A full
	// secret match may have been lost during that projection; do not queue a
	// partially hidden operation or a fragment of its input.
	switch in.Tool {
	case "Bash", "Read", "Edit", "Write":
	default:
		matched, maskErr := d.hookInputContainsSecret(ctx, in)
		if matched || maskErr != nil {
			return deny(withheldHookInput, actor, tier, version)
		}
	}
	facts, err := buildClaudeApprovalFacts(in, disp, func(text string) (string, []redact.GeneratedMaskSpan, error) { return d.exactHookMask(ctx, text) })
	if err != nil {
		return deny(err.Error(), actor, tier, version)
	}
	var planned claude.HookDecisionResult
	applyRewrite(&planned, in, disp)
	reviewedInput, err := claudeApprovalInputHash(in, planned)
	if err != nil {
		return deny("effective tool input is unavailable for human review", actor, tier, version)
	}
	waitCtx, cancel := context.WithTimeout(ctx, claudeApprovalWait)
	defer cancel()
	consumer := newSingleUseConsumerID()
	subject := encodeSubjectRef(in.SessionID, in.PlanHash+":"+consumer)
	request, err := d.approvals.Request(waitCtx, tenant, p, governance.ApprovalRequest{
		SessionRef: p.SessionIdentity, Action: hookActionCapability, SubjectKind: "claude.tool", SubjectRef: subject,
		Reason:           facts.reason,
		ReasonMasks:      facts.reasonMasks,
		Review:           facts.review,
		ReviewMasks:      facts.reviewMasks,
		ExpiresInSeconds: int64(claudeApprovalWait / time.Second),
	})
	if err != nil {
		return deny("could not request human approval", actor, tier, version)
	}
	var endApprovalWait func()
	if d.approvalWait != nil {
		// An authored policy may omit its durable expiry. The child still has
		// a bounded wait; project that deadline without changing the policy.
		expires, _ := waitCtx.Deadline() // WithTimeout above always supplies one.
		if request.ExpiresAt != "" {
			policyExpiry, parseErr := model.ParseTimestamp(request.ExpiresAt)
			if parseErr != nil {
				err = parseErr
			} else if policyExpiry.Time().Before(expires) {
				expires = policyExpiry.Time()
			}
		}
		if err == nil {
			endApprovalWait, err = d.approvalWait(waitCtx, p, request.ID, expires)
			if err == nil {
				defer endApprovalWait()
			}
		}
		if err != nil {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			_, _ = d.approvals.Cancel(cleanupCtx, tenant, p, request.ID)
			cleanupCancel()
			return deny("session could not register its approval wait", actor, tier, version)
		}
	}
	answer, err := d.approvals.Wait(waitCtx, tenant, request.ID)
	if endApprovalWait != nil {
		endApprovalWait()
	}
	if err != nil {
		if errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			// The helper or turn is gone. Withdraw only this session's pending
			// request; a racing terminal decision is retained by the service.
			cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			closed, cancelErr := d.approvals.Cancel(cleanupCtx, tenant, p, request.ID)
			cleanupCancel()
			if cancelErr != nil && (closed.ID == "" || closed.Status == nbPending) && d.log != nil {
				d.log.Error("hook-pep: approval cancellation incomplete", "approval_ref", request.ID)
			}
			if errors.Is(err, context.Canceled) {
				return deny("tool-call interrupted while waiting for human approval", actor, tier, version)
			}
		}
		if errors.Is(err, context.DeadlineExceeded) {
			// Wait owns the audited expiry transition, including rollback recovery.
			return deny("human approval did not arrive before the tool-call deadline; retry after review", actor, tier, version)
		}
		return deny("human approval could not be read", actor, tier, version)
	}
	if answer.Status != nbApproved || answer.SessionRef != p.SessionIdentity || answer.Action != hookActionCapability || answer.SubjectKind != "claude.tool" || answer.SubjectRef != subject {
		return deny("human review did not approve this tool-call ("+answer.Status+")", actor, tier, version)
	}
	current, scope, err := credentials.ResolveRun(waitCtx, tenant, in.SessionID)
	if err != nil || scope.SessionRef != p.SessionIdentity || current.SessionFence != p.SessionFence {
		return deny("session authority changed during human review", actor, tier, version)
	}
	// Re-run every live invariant and deny policy. The private context grant
	// bypasses only another human wait for this exact plan; it cannot bypass deny.
	reviewedCtx := context.WithValue(waitCtx, sessionHookScopeKey{}, scope)
	reviewedCtx = context.WithValue(reviewedCtx, claudeReviewedCallKey{}, claudeReviewedCall{in.SessionID, in.PlanHash})
	res, _, _, err := d.decide(reviewedCtx, in, current, nil)
	if err != nil || res.Permission != claude.DecisionAllow {
		if err != nil {
			return deny("policy could not be rechecked after human review", actor, tier, version)
		}
		return res
	}
	currentInput, err := claudeApprovalInputHash(in, res)
	if err != nil || currentInput != reviewedInput {
		return deny("tool input changed during human review; retry for review", actor, tier, version)
	}
	spent, err := d.approvals.Consume(waitCtx, tenant, request.ID, consumer, version)
	if err != nil || !spent.Granted || spent.Replay {
		return deny("human approval is no longer valid for this tool-call", actor, tier, version)
	}
	res.Reason = "approved by human review (" + request.ID + ")"
	return res
}
