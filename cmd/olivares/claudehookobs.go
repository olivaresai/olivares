// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/olivaresai/olivares/connectors/claude"
	"github.com/olivaresai/olivares/core/model"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// B-05 — the Claude PEP vetoes calls and says NOTHING.
//
// Every governed decision this decider makes was invisible to the rest of the
// product: 1,641 lines that could refuse a tool call, and zero edges published.
// The session plane's live view therefore painted a Claude session that a PEP was
// actively policing exactly like one nobody was watching, because the only thing
// it had to go on — the engine and posture labels an edge carries — never
// arrived. The session id was parsed out of the hook payload, propagated through
// two functions, and there it died.
//
// Codex does this correctly in the same repository: one edge plus up to two
// findings per call (codexhookpepserver.go). This is that shape, and it is
// deliberately a COPY of it rather than a second design — the consumer folds both
// through one code path, so a second dialect would be a second thing to keep in
// agreement.
//
// The emission is best-effort and must never change a verdict. It runs AFTER the
// decision is anchored: a bus that is slow, full or absent cannot turn an allow
// into a deny or the other way round.

// claudeSignalSource names this producer on the bus.
const claudeSignalSource = "claude-hook-pep"

// claudePublishTimeout bounds one publish so a stalled bus cannot hold a hook
// request open. The hook is in the critical path of somebody's tool call.
const claudePublishTimeout = 2 * time.Second

// postureForClaude states whether the PEP could have impeded THIS ACCESS.
//
// It deliberately does NOT use claude.HookEnforcementFor(event).Enforceable, and
// the difference is the whole point. That flag is true for PostToolUse too,
// because a PostToolUse verdict can block further PROCESSING — but by then the
// tool has already run and the resource has already been touched. An access edge
// records the access, so calling that "enforced" would tell an operator the PEP
// was in a position to stop something it could only comment on afterwards.
//
// The question is the narrow one Codex asks (session.CanImpede): only the events
// that gate the call BEFORE it happens are enforced. Copying the sibling's SHAPE
// while inventing its semantics would have produced exactly the overstatement the
// posture column exists to prevent.
func postureForClaude(event string) string {
	if claude.HookEnforcementFor(event).Enforceable && impedesBeforeTheCall(event) {
		return sdkmodel.PostureEnforced
	}
	return sdkmodel.PostureObserved
}

// impedesBeforeTheCall reports whether a verdict on this event lands before the
// tool runs.
func impedesBeforeTheCall(event string) bool {
	switch event {
	case "PreToolUse", "PermissionRequest":
		return true
	default:
		return false
	}
}

// claudeEdgeFor builds the access edge for one governed hook decision, or false
// when the event is not a resource access.
//
// A DENIED call still produces an edge, exactly as on the Codex side: what a
// session TRIED to do is the fact an operator needs, and dropping denials would
// leave the graph showing only what governance failed to stop.
func claudeEdgeFor(in claude.HookDecisionInput, allowed bool, at time.Time) (sdkmodel.EdgeObservation, bool) {
	if in.SessionID == "" || in.Tool == "" || in.ResourceKind == "" {
		return sdkmodel.EdgeObservation{}, false
	}
	return sdkmodel.EdgeObservation{
		OriginKind:   "session",
		OriginRef:    in.SessionID,
		ResourceKind: in.ResourceKind,
		ResourceRef:  in.ResourceRef,
		Mode:         sdkmodel.AccessMode(in.Mode),
		Source:       claudeSignalSource,
		// Attributed, not inferred: the session id came from Claude's own hook
		// payload for this exact call, not from a heuristic join. It is also the
		// first time this field is READ — it was parsed, propagated through two
		// functions, and dropped.
		Confidence: sdkmodel.ConfidenceAttributed,
		ToolRef:    in.Tool,
		ObservedAt: at,
		// Set LAST and never merged with operator-supplied attributes. The Claude
		// connector honors operator-allowlisted OTEL resource attributes as labels
		// (connectors/claude/identity.go), and "engine"/"posture" are not builtin
		// keys there — so an operator who allowlisted attributes with those names
		// could otherwise assert their own engine and posture. The engine's
		// statement about its own governance is not an operator-supplied dimension.
		Labels: map[string]string{
			sdkmodel.LabelEngine:  sdkmodel.EngineClaude,
			sdkmodel.LabelPosture: postureForClaude(in.Event),
		},
	}, true
}

// claudeDenyFinding records a governed refusal as a session-scoped finding — the
// second door the sessions module folds. Without it the live view can show that a
// session exists and not that governance stopped it doing something.
func claudeDenyFinding(in claude.HookDecisionInput, allowed bool, reason string) (sdkmodel.FindingReport, bool) {
	if in.SessionID == "" || allowed {
		return sdkmodel.FindingReport{}, false
	}
	sum := sha256.Sum256([]byte(in.Event + "|" + in.Tool + "|" + reason))
	return sdkmodel.FindingReport{
		SubjectKind: "session",
		SubjectRef:  in.SessionID,
		Kind:        "session.governed_deny",
		Severity:    sdkmodel.SeverityMedium,
		// The title names the tool and the posture, never the reason text: a
		// reason can quote the operator's own policy or the call's arguments.
		Title:      "claude call denied by policy (" + in.Tool + ", " + postureForClaude(in.Event) + ")",
		DetailHash: hex.EncodeToString(sum[:]),
	}, true
}

// publishDecisionSignal emits the edge and the deny finding for one decision.
// Best-effort by construction: a publish failure is logged and the verdict
// stands. It is called after the decision is anchored, so nothing here can
// change what the caller was told.
func (d *claudeHookDecider) publishDecisionSignal(ctx context.Context, tenant model.TenantID, in claude.HookDecisionInput, allowed bool, reason string) {
	if d.bus == nil || tenant.IsZero() {
		return
	}
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), claudePublishTimeout)
	defer cancel()
	if edge, ok := claudeEdgeFor(in, allowed, d.clock()); ok {
		d.publishHookObs(pctx, tenant, edge)
	}
	if f, ok := claudeDenyFinding(in, allowed, reason); ok {
		d.publishHookObs(pctx, tenant, f)
	}
}
