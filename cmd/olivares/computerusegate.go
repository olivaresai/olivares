// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"strconv"
	"strings"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/core/model"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// computerusegate.go is the AGPL composition-root glue for the OPTIONAL commercial
// computer-use governance gate (enterprise/computeruse — deep OCR, timeline recording,
// real-time DLP over screenshots). It defines the seam the inference-proxy decider
// consumes and translates the gate's verdict into the decider's existing primitives: a
// proxy deny, published posture findings, and an OPENED governed approval via the
// existing bridge.
//
// The open-core computer-use PEP (this file + the authorizeChain insert) provides:
//  1. Request-side gate: detects computer_use tool declarations in req.Tools and
//     checks tenant policy (is computer-use allowed?). Deny-closed when gate is
//     present and denies.
//  2. Response-side audit: extracts computer-use actions (click, type, screenshot,
//     scroll, key) from the model's tool_use response blocks, audits each action in
//     the ledger, and runs the existing DLP classifier over typed text. Sensitive
//     typed text emits a HIGH finding; in buffer mode the response is blocked.
//  3. Screenshots: relies on the existing DLP unscanned-deny-closed for images. The
//     enterprise add-on adds OCR + coordinate-based redaction + timeline recording.
//
// The default AGPL build injects a nil gate (wire_noenterprise.go), so this glue is
// inert: the inline proxy keeps its prior behavior — NO rug-pull. Under
// `-tags enterprise` with a computer-use config, wire_enterprise.go injects the real
// gate (computeruse.Gate), the ONLY edge by which the AGPL root references the
// commercial module (build-tag-gated, like newFederation).
//
// HONESTY: verified-deployed governance AT THE PROXY only. A caller who points
// ANTHROPIC_BASE_URL elsewhere evades it (modules/inferenceproxy/doc.go). The gate
// can only DENY; it cannot bypass a gate ahead of it.

// computerUseGate is the narrow seam the decider depends on (Go structurally satisfies
// it; *computeruse.Gate implements it under -tags enterprise).
type computerUseGate interface {
	GovernComputerUse(ctx context.Context, in claudeapi.ComputerUseInput) claudeapi.ComputerUseDecision
}

// publishComputerUseFindings turns the gate's posture/forensic findings into bus
// FindingReports. Minimal data: the decider hashes Detail into DetailHash; no
// screenshot or typed text is stored. A nil bus makes this a no-op.
func (d *inferenceProxyDecider) publishComputerUseFindings(ctx context.Context, tenant model.TenantID, modelRef string, fs []claudeapi.ComputerUseFinding) {
	for _, f := range fs {
		sev := sdkmodel.SeverityInfo
		switch f.Severity {
		case "high":
			sev = sdkmodel.SeverityHigh
		case "medium":
			sev = sdkmodel.SeverityMedium
		}
		d.publish(ctx, tenant, sdkmodel.FindingReport{
			Kind:        firstNonEmpty(f.Kind, "computer_use"),
			Severity:    sev,
			SubjectKind: "anthropic.computer_use",
			SubjectRef:  firstNonEmpty(f.ActionType, modelRef),
			Title:       f.Title,
			DetailHash:  hexSHA(modelRef + "|computer_use|" + f.Detail),
			OccurredAt:  d.clock().UTC(),
			OWASPLLM:    f.OWASPLLM,
		})
	}
}

// openComputerUseApproval opens a governed approval for denied computer-use access.
func (d *inferenceProxyDecider) openComputerUseApproval(ctx context.Context, tenant model.TenantID, actor string, intent *claudeapi.ComputerUseApprovalIntent) {
	if d.approvals == nil || intent == nil {
		return
	}
	requestedBy := firstNonEmpty(actor, model.ActorSystem)
	if _, _, _, err := d.approvals.gateOnce(ctx, tenant, intent.Action, "anthropic.computer_use", intent.Subject, intent.PlanHash, intent.Reason, requestedBy); err != nil && d.log != nil {
		d.log.Warn("inference-proxy: computer-use approval intent could not be opened (deny stands)", "err", err)
	}
}

// auditComputerUseActions extracts computer-use actions from a model response and
// emits audit findings for each. The model proposes actions (click, type, screenshot,
// scroll, key); the client executes them. This is the POST-forward detective audit.
//
// For typed text: the existing DLP classifier runs over the text the model asked to
// type. Sensitive typed text emits a HIGH finding. The response is already committed
// at this point (streamed or buffered), so this is detective unless the response was
// buffered (the caller checks block).
func (d *inferenceProxyDecider) auditComputerUseActions(ctx context.Context, tenant model.TenantID, actor, modelRef string, resp claudeapi.MessageResponse) bool {
	actions := claudeapi.ExtractComputerUseActions(resp)
	if len(actions) == 0 {
		return false
	}

	shouldBlock := false

	for _, a := range actions {
		d.publish(ctx, tenant, sdkmodel.FindingReport{
			Kind:        "computer_use_action",
			Severity:    sdkmodel.SeverityInfo,
			SubjectKind: "anthropic.computer_use",
			SubjectRef:  a.Type,
			Title:       "Computer-use action proposed: " + a.Type,
			DetailHash:  hexSHA(modelRef + "|action|" + a.Type + "|" + coordHash(a.Coordinate)),
			OccurredAt:  d.clock().UTC(),
		})

		if a.Type == "type" && strings.TrimSpace(a.Text) != "" {
			classes := classifyText([]string{a.Text})
			if len(classes) > 0 {
				d.publish(ctx, tenant, sdkmodel.FindingReport{
					Kind:        "computer_use_sensitive_input",
					Severity:    sdkmodel.SeverityHigh,
					SubjectKind: "anthropic.computer_use",
					SubjectRef:  "type",
					Title:       "Computer-use typed text contains sensitive content (DLP classes detected)",
					DetailHash:  hexSHA(modelRef + "|typed_text_dlp|" + strings.Join(classes, ",")),
					OccurredAt:  d.clock().UTC(),
					OWASPLLM:    []string{"LLM02:2025"},
				})
				shouldBlock = true
			}
		}
	}

	return shouldBlock
}

func coordHash(coord [2]int) string {
	if coord[0] == 0 && coord[1] == 0 {
		return "none"
	}
	return strconv.Itoa(coord[0]) + "," + strconv.Itoa(coord[1])
}
