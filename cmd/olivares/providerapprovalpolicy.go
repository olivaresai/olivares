// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/olivaresai/olivares/connectors/claude"
	"github.com/olivaresai/olivares/connectors/redact"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/modules/sessions"
)

// Native provider approvals use the same principal, live PDP, authored review
// policy and ledger as hooks. The driver still owns its wire codec and turn fence.
type sessionProviderPolicy struct {
	credentials   *auth.SessionCredentials
	eval          auth.PolicyEvaluator
	scoped        auth.ScopedAuthorizer
	approvals     *governance.EngineApprovals
	store         store.Store
	redactSecrets func(model.TenantID, string, []byte) ([]byte, []sessions.SecretMaskSpan, bool)
	evidenceScope *auth.SessionScope
}

func (g sessionProviderPolicy) Decide(ctx context.Context, tenant model.TenantID, req sessions.ProviderApprovalRequest) (sessions.ProviderApprovalPolicyDecision, error) {
	// Only the retained projection is redacted. Policy and execution keep the
	// input the provider actually supplied; an unreviewable projection refuses.
	req = providerEffectiveFacts(req)
	if bound, err := g.bindEvidence(ctx, tenant, req); err == nil {
		g = bound
	}
	reviewed, reviewErr := g.reviewFacts(ctx, tenant, req)
	evidenceFacts := req
	if reviewErr != nil {
		evidenceFacts = reviewed // Refused facts never reach the retained projection.
	}
	var out sessions.ProviderApprovalPolicyDecision
	if reviewErr != nil {
		out = sessions.ProviderApprovalPolicyDecision{Disposition: sessions.ProviderApprovalDeny, Reason: reviewErr.Error()}
	} else {
		out = g.verdict(ctx, tenant, req)
	}
	req = reviewed
	evidenceReason := out.Reason
	out.Reason, reviewErr = g.cleanEvidence(tenant, req.RunRef, out.Reason)
	if reviewErr != nil {
		out = sessions.ProviderApprovalPolicyDecision{Disposition: sessions.ProviderApprovalDeny, Reason: "session secret redaction is unavailable; input withheld"}
		req.CommandLine, req.FilePaths, req.Requested = "command not reviewable", nil, nil
		req.TurnID, req.ConversationID = "", ""
		req.FactsComplete = false
		evidenceFacts, evidenceReason = req, out.Reason
	}

	if g.store == nil {
		return sessions.ProviderApprovalPolicyDecision{Disposition: sessions.ProviderApprovalDeny, Reason: "decision audit is unavailable"}, nil
	}
	metadata := func(facts sessions.ProviderApprovalRequest, reason string) map[string]any {
		return map[string]any{"session_ref": facts.SessionRef, "run_ref": facts.RunRef, "turn_id": facts.TurnID,
			"method": facts.Method, "kind": facts.Kind, "decision": out.Disposition, "reason": reason,
			"command_line": facts.CommandLine, "file_paths": facts.FilePaths, "facts_complete": facts.FactsComplete}
	}
	meta := metadata(req, out.Reason)
	// Serialize effective input before masking to retain replacement provenance.
	// The final JSON must equal the field projection; changed keys or cross-field
	// matches cannot silently alter the recorded operation.
	encoded, encodeErr := json.Marshal(metadata(evidenceFacts, evidenceReason))
	expected, expectedErr := json.Marshal(meta)
	clean, cleanErr := g.cleanEvidence(tenant, req.RunRef, string(encoded))
	if encodeErr != nil || expectedErr != nil || cleanErr != nil || clean != string(expected) {
		out = sessions.ProviderApprovalPolicyDecision{Disposition: sessions.ProviderApprovalDeny, Reason: "provider details withheld"}
		meta = map[string]any{"decision": out.Disposition, "reason": out.Reason, "facts_complete": false}
		encoded, _ = json.Marshal(meta)
		clean, cleanErr = g.cleanEvidence(tenant, req.RunRef, string(encoded))
		if cleanErr != nil || clean != string(encoded) {
			meta = nil
		}
	}
	var dropped bool
	err := g.store.Mutate(ctx, tenant, func(sc store.Scope) error {
		ev, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor: "session:" + req.SessionRef, ActorKind: model.ActorAgent,
			Action: req.Driver + ".approval." + string(out.Disposition), TargetKind: model.Kind(req.Driver + ".tool.use"), TargetID: model.ID(req.RunRef),
			Meta: meta,
		})
		if err == nil {
			dropped = ev.Seq == 0 // Commit loss accounting; refuse only after commit.
		}
		return err
	})
	if err != nil || dropped {
		return sessions.ProviderApprovalPolicyDecision{Disposition: sessions.ProviderApprovalDeny, Reason: "decision audit is unavailable"}, nil
	}
	return out, nil
}

func providerEffectiveFacts(req sessions.ProviderApprovalRequest) sessions.ProviderApprovalRequest {
	if req.EffectiveCommandLine != "" {
		req.CommandLine = req.EffectiveCommandLine
	}
	if req.EffectiveFilePaths != nil {
		req.FilePaths = req.EffectiveFilePaths
	}
	return req
}

// reviewFacts is shared by the decision ledger and the actual human queue. The
// run's own exact-value redactor runs before pattern redaction, without vault I/O.
func (g sessionProviderPolicy) reviewFacts(ctx context.Context, tenant model.TenantID, req sessions.ProviderApprovalRequest) (sessions.ProviderApprovalRequest, error) {
	refuse := func(reason string) (sessions.ProviderApprovalRequest, error) {
		req.CommandLine, req.FilePaths, req.Requested = "command not reviewable", nil, nil
		req.TurnID, req.ConversationID = "", ""
		req.FactsComplete = false
		req.EffectiveCommandLine, req.EffectiveFilePaths = "", nil
		return req, errors.New(reason)
	}
	bound, err := g.bindEvidence(ctx, tenant, req)
	if err != nil {
		return refuse(err.Error())
	}
	g = bound
	// Redact field VALUES separately: a secret can equal a JSON field name.
	// Decoding a renamed key into a populated object would keep its raw value.
	req = providerEffectiveFacts(req)
	commandInput, paths := req.CommandLine, req.FilePaths
	maskedCommand, err := g.cleanEvidence(tenant, req.RunRef, commandInput)
	if err != nil {
		return refuse("session secret redaction is unavailable; input withheld")
	}
	req.FilePaths = slices.Clone(paths)
	for i, path := range req.FilePaths {
		clean, err := g.cleanEvidence(tenant, req.RunRef, path)
		if err != nil {
			return refuse("session secret redaction is unavailable; input withheld")
		}
		req.FilePaths[i] = clean
	}
	req.Requested = slices.Clone(req.Requested)
	for i, permission := range req.Requested {
		clean, err := g.cleanEvidence(tenant, req.RunRef, permission)
		if err != nil {
			return refuse("session secret redaction is unavailable; input withheld")
		}
		req.Requested[i] = clean
	}
	maskedTurn, err := g.cleanEvidence(tenant, req.RunRef, req.TurnID)
	if err != nil {
		return refuse("session secret redaction is unavailable; input withheld")
	}
	req.TurnID = maskedTurn
	req.EffectiveCommandLine, req.EffectiveFilePaths = "", nil
	command, err := redact.ReviewableShellCommand(commandInput, maskedCommand)
	if err != nil {
		return refuse("command not reviewable")
	}
	req.CommandLine = command

	return req, nil
}

// Bind the run-keyed cache to the resolved request generation. Every cache read
// checks the same scope afterward, so a newer cache never projects an old input.
func (g sessionProviderPolicy) bindEvidence(ctx context.Context, tenant model.TenantID, req sessions.ProviderApprovalRequest) (sessionProviderPolicy, error) {
	if g.evidenceScope != nil {
		return g, nil // This copy belongs only to the current decision.
	}
	if g.credentials == nil {
		return g, errors.New("session authority is unavailable")
	}
	p, scope, err := g.credentials.ResolveRun(ctx, tenant, req.RunRef)
	if err != nil || req.SessionRef == "" || scope.SessionRef != req.SessionRef || req.Principal.SessionIdentity != req.SessionRef || req.Principal.SessionFence != p.SessionFence {
		return g, errors.New("session authority is unavailable or changed")
	}
	if scope.SecretEnv != "" && g.redactSecrets == nil {
		return g, errors.New("session secret redaction is unavailable; input withheld")
	}
	g.evidenceScope = &scope
	mask := g.redactSecrets
	if mask != nil {
		g.redactSecrets = func(target model.TenantID, runRef string, input []byte) ([]byte, []sessions.SecretMaskSpan, bool) {
			if target != scope.TenantID || runRef != scope.RunRef {
				return nil, nil, false
			}
			masked, spans, ok := mask(target, runRef, input)
			if !ok {
				return nil, nil, false
			}
			_, current, err := g.credentials.ResolveRun(ctx, target, runRef)
			if err != nil || current.SessionRef != scope.SessionRef || current.RunRef != scope.RunRef || current.Fence != scope.Fence || current.SecretEnv != scope.SecretEnv {
				return nil, nil, false
			}
			return masked, spans, true
		}
	}
	return g, nil
}

func (g sessionProviderPolicy) reviewReason(ctx context.Context, tenant model.TenantID, req sessions.ProviderApprovalRequest, text string) (string, []redact.GeneratedMaskSpan, error) {
	bound, err := g.bindEvidence(ctx, tenant, req)
	if err != nil {
		return "", nil, err
	}
	return bound.maskEvidence(tenant, req.RunRef, text)
}

// maskEvidence returns the exact masker output before the pattern floor. Only
// the replacement port supplies provenance; existing marker text grants nothing.
func (g sessionProviderPolicy) maskEvidence(tenant model.TenantID, runRef, text string) (string, []redact.GeneratedMaskSpan, error) {
	if g.redactSecrets == nil {
		return text, nil, nil
	}
	masked, spans, ok := g.redactSecrets(tenant, runRef, []byte(text))
	if !ok {
		return "", nil, errors.New("session secret redaction is unavailable; input withheld")
	}
	generated := make([]redact.GeneratedMaskSpan, len(spans))
	for i, span := range spans {
		generated[i] = redact.GeneratedMaskSpan{Start: span.Start, End: span.End}
	}
	return string(masked), generated, nil
}

func (g sessionProviderPolicy) cleanEvidence(tenant model.TenantID, runRef, text string) (string, error) {
	masked, generated, err := g.maskEvidence(tenant, runRef, text)
	if err != nil {
		return "", err
	}
	return redact.CleanMasked(masked, generated)
}

func (g sessionProviderPolicy) verdict(ctx context.Context, tenant model.TenantID, req sessions.ProviderApprovalRequest) sessions.ProviderApprovalPolicyDecision {
	refuse := func(reason string) sessions.ProviderApprovalPolicyDecision {
		return sessions.ProviderApprovalPolicyDecision{Disposition: sessions.ProviderApprovalDeny, Reason: reason}
	}
	if g.credentials == nil || g.eval == nil || g.approvals == nil {
		return refuse("session policy authority is unavailable")
	}
	p, scope, err := g.credentials.ResolveRun(ctx, tenant, req.RunRef)
	if err != nil || req.SessionRef == "" || scope.SessionRef != req.SessionRef || req.Principal.SessionIdentity != req.SessionRef || req.Principal.SessionFence != p.SessionFence {
		return refuse("session authority is unavailable or changed")
	}
	role, member := p.RoleIn(tenant)
	if !member || !auth.RoleGrants(role, "sessions:run:write") {
		return refuse("launcher's current authority does not permit this provider action")
	}
	mode, kind, resource := "unknown", "provider.tool", req.Kind
	switch req.Kind {
	case "command_execution", "legacy_exec_command":
		if !req.FactsComplete || strings.TrimSpace(req.CommandLine) == "" {
			return refuse("provider did not supply the complete command for policy review")
		}
		kind, resource = "shell", req.CommandLine
	case "file_change", "legacy_apply_patch":
		if !req.FactsComplete || len(req.FilePaths) == 0 {
			return refuse("provider did not supply the complete file paths for policy review")
		}
		mode, kind, resource = "write", "file", strings.Join(req.FilePaths, "\n")
	case "permissions", "mcp_elicitation", "tool_call_permission":
	default:
		return refuse("provider approval surface is unknown")
	}
	paths, _ := json.Marshal(req.FilePaths)
	question := auth.Request{Principal: p, Tenant: tenant, Permission: auth.Permission(req.Driver + ".tool.use:" + codexModeVerb(mode)),
		Resource: auth.ResourceAttrs{Kind: kind, ID: resource, WorkspaceID: scope.WorkspaceID,
			Extra: map[string]string{"engine": req.Driver, "tool": req.Kind, "mode": mode, "method": req.Method,
				"command_line": req.CommandLine, "file_paths": string(paths), "session_ref": scope.SessionRef, "run_ref": scope.RunRef, "folder_path": scope.FolderPath}}}
	// The live PDP keeps the effective input. Its existing evidence writers
	// receive only this run's projection, with no global redactor or vault I/O.
	question.EvidenceRedactor = func(text string) string {
		clean, err := g.cleanEvidence(tenant, req.RunRef, text)
		if err != nil {
			return "provider details withheld"
		}
		return clean
	}
	decision, err := g.eval.Evaluate(ctx, question)
	if err != nil || !decision.Allow {
		return refuse(firstNonEmptyStr(decision.Reason, "live policy denies this provider action"))
	}
	if g.scoped != nil {
		scoped, err := g.scoped.Scoped(ctx, question)
		if err != nil || scoped.Effect == auth.EffectForbid {
			return refuse(firstNonEmptyStr(scoped.Reason, "scoped policy denies this provider action"))
		}
	}
	if scope.Preset == sessions.PresetFull {
		if !auth.RoleGrants(role, "sessions:run:admin") {
			return refuse("launcher's current authority does not permit full session permissions")
		}
		full := auth.Request{Principal: p, Tenant: tenant, Permission: "sessions:run:admin",
			Resource: auth.ResourceAttrs{Kind: "session_run", ID: scope.RunRef, WorkspaceID: scope.WorkspaceID}}
		decision, err := g.eval.Evaluate(ctx, full)
		if err != nil || !decision.Allow {
			return refuse(firstNonEmptyStr(decision.Reason, "live policy denies full session permissions"))
		}
		if g.scoped != nil {
			decision, err := g.scoped.Scoped(ctx, full)
			if err != nil || decision.Effect == auth.EffectForbid {
				return refuse(firstNonEmptyStr(decision.Reason, "scoped policy denies full session permissions"))
			}
		}
	}
	if scope.Preset == sessions.PresetCustom {
		return refuse("session template tool restrictions cannot be proved for this provider action")
	}
	preset := claude.DecisionAllow
	if scope.Preset != sessions.PresetNone {
		preset, err = sessionPresetDecision(scope.Preset, mode)
		if err != nil {
			return refuse("session permission preset is unavailable")
		}
	}
	if preset == claude.DecisionDeny {
		return refuse("read-only session does not permit this action")
	}
	policy, err := g.approvals.ReviewPolicy(ctx, tenant, "sessions.provider.approval", "session_run")
	if err != nil {
		return refuse("approval policy is unavailable")
	}
	if preset == claude.DecisionAsk {
		return sessions.ProviderApprovalPolicyDecision{Disposition: sessions.ProviderApprovalAsk, Reason: "session permission preset requires human approval"}
	}
	if policy != "" {
		return sessions.ProviderApprovalPolicyDecision{Disposition: sessions.ProviderApprovalAsk, Reason: "human approval required by policy"}
	}
	return sessions.ProviderApprovalPolicyDecision{Disposition: sessions.ProviderApprovalAllow, Granted: append([]string(nil), req.Requested...), Reason: "permitted by live session policy"}
}
