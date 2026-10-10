// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package hookpep

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
	"github.com/olivaresai/olivares/modules/governance/effectgate"
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
func (d *Decider) gateViaSessionApproval(ctx context.Context, tenant model.TenantID, p auth.Principal, in claude.HookDecisionInput, disp hookDisposition, actor, tier, version string) claude.HookDecisionResult {
	credentials, ok := d.Authr.(SessionCredentials)
	if !ok || d.Approvals == nil || d.SubjectRef == nil {
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
	consumer := NewSingleUseConsumerID()
	var reviewed claude.HookDecisionResult // the reply the recheck settles on
	a, err := effectgate.Hold(waitCtx, d.Approvals, effectgate.HeldEffect{
		Tenant: tenant, Principal: p,
		Request: governance.ApprovalRequest{
			SessionRef: p.SessionIdentity, Action: ActionCapability, SubjectKind: "claude.tool",
			SubjectRef:       d.SubjectRef(in.SessionID, in.PlanHash+":"+consumer),
			Reason:           facts.reason,
			ReasonMasks:      facts.reasonMasks,
			Review:           facts.review,
			ReviewMasks:      facts.reviewMasks,
			ExpiresInSeconds: int64(claudeApprovalWait / time.Second),
		},
		Consumer: consumer, PolicyVersion: version, Wait: d.ApprovalWait, Log: d.Log,
		Recheck: func(ctx context.Context) error {
			current, scope, err := credentials.ResolveRun(ctx, tenant, in.SessionID)
			if err != nil || scope.SessionRef != p.SessionIdentity || current.SessionFence != p.SessionFence {
				reviewed = deny("session authority changed during human review", actor, tier, version)
				return errors.New(reviewed.Reason)
			}
			// Re-run every live invariant and deny policy. The private context grant
			// bypasses only another human wait for this exact plan; it cannot bypass deny.
			reviewedCtx := context.WithValue(ctx, sessionHookScopeKey{}, scope)
			reviewedCtx = context.WithValue(reviewedCtx, claudeReviewedCallKey{}, claudeReviewedCall{in.SessionID, in.PlanHash})
			if reviewed, _, _, err = d.decide(reviewedCtx, in, current, nil); err != nil {
				reviewed = deny("policy could not be rechecked after human review", actor, tier, version)
			}
			if reviewed.Permission != claude.DecisionAllow {
				return errors.New(reviewed.Reason)
			}
			if currentInput, err := claudeApprovalInputHash(in, reviewed); err != nil || currentInput != reviewedInput {
				reviewed = deny("tool input changed during human review; retry for review", actor, tier, version)
				return errors.New(reviewed.Reason)
			}
			return nil
		},
	})
	switch a.Failed {
	case effectgate.Open:
		return deny("could not request human approval", actor, tier, version)
	case effectgate.Register:
		return deny("session could not register its approval wait", actor, tier, version)
	case effectgate.Wait:
		switch {
		case errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded):
			return deny("tool-call interrupted while waiting for human approval", actor, tier, version)
		case errors.Is(err, context.DeadlineExceeded):
			// Wait owns the audited expiry transition, including rollback recovery.
			return deny("human approval did not arrive before the tool-call deadline; retry after review", actor, tier, version)
		}
		return deny("human approval could not be read", actor, tier, version)
	case effectgate.Recheck:
		return reviewed
	case effectgate.Spend:
		return deny("human approval is no longer valid for this tool-call", actor, tier, version)
	}
	switch a.Outcome {
	case effectgate.Allowed:
		reviewed.Reason = "approved by human review (" + a.Ref + ")"
		return reviewed
	case effectgate.Replay, effectgate.Unspendable:
		return deny("human approval is no longer valid for this tool-call", actor, tier, version)
	default: // not approved, or an answer about another effect
		return deny("human review did not approve this tool-call ("+a.Status+")", actor, tier, version)
	}
}
