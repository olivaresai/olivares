// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package managedsettings

import "strings"

// agentsdk.go is the focused AUTHORING surface for governing a customer's own
// Agent SDK fleet: the managed-settings posture that WINS PRECEDENCE over the SDK
// program's PROGRAMMATIC options. "Managed policy settings take precedence over
// programmatic options" and "load regardless" of the SDK's settingSources — settingSources:[]
// cannot disable them (VERIFIED 2026-06-19, code.claude.com/docs/en/agent-sdk/{typescript,
// claude-code-features,permissions}). So THIS managed layer — not the SDK program's own
// permissionMode/allowedTools/hooks — is what actually binds a fleet built on the SDK.
//
// It REUSES the verified Policy / Render / ValidateJSON / drift the connector already
// ships; it does NOT add a second precedence engine. The net-new logic is (a) the mapping
// from a focused Agent-SDK governance intent to the general Policy, and (b) the EXPOSED
// precedence contract — which managed key overrides which programmatic SDK option, with
// the honest limit that this is "verified-deployed", NOT "impossible to bypass".

// AgentSDKGuardrail is the focused governance intent for an Agent SDK fleet: the
// precedence-winning managed-settings posture that NEUTRALIZES a customer SDK program's
// dangerous programmatic options. Every field maps onto the general Policy (below); none
// of it is a new wire shape.
type AgentSDKGuardrail struct {
	// MaxMode pins permissions.defaultMode. NOTE: defaultMode is only the DEFAULT, not a
	// hard CAP — to actually FORBID bypass/auto, set ForbidBypass/ForbidAuto (the disable*
	// locks). ValidateAgentSDKGuardrail flags a MaxMode that looks like a cap but is not.
	MaxMode string
	// ForbidBypass / ForbidAuto set the non-overridable disable* locks: even
	// allowDangerouslySkipPermissions cannot re-enable bypass when
	// disableBypassPermissionsMode is managed (a managed deny/disable holds under bypass).
	ForbidBypass bool
	ForbidAuto   bool
	// Deny are managed disallowedTools-equivalent rules — they bind in EVERY mode,
	// including bypassPermissions. Ask force a prompt even under bypass.
	Deny []string
	Ask  []string
	// ForceManagedHooksOnly (allowManagedHooksOnly) loads ONLY managed/SDK hooks, so the
	// SDK program cannot replace the governed PEP hook with its own (anti-tamper).
	ForceManagedHooksOnly bool
	// PEPHookCommand, when set, distributes the governed PreToolUse PEP hook as a managed
	// (non-overridable) hook — the enforcement seam that routes each tool-call to the
	// control plane. It is the local PEP-client executable path on the managed host.
	PEPHookCommand string
	// ParentSettingsBehavior governs the SDK host's OWN programmatically-supplied managed
	// settings (the "parent" tier): ParentFirstWins drops them; ParentMerge applies them
	// UNDER this admin tier, tighten-only. Empty = the client default (first-wins).
	ParentSettingsBehavior string
}

// ToPolicy maps the guardrail onto the general managed-settings Policy, reusing the
// verified Render/drift. The mapping is the ONLY net-new logic; precedence/delivery are
// the existing engine. It returns an error only if the managed PEP hook cannot be built.
func (g AgentSDKGuardrail) ToPolicy() (Policy, error) {
	p := Policy{
		Permissions: Permissions{
			DefaultMode:                  strings.TrimSpace(g.MaxMode),
			Deny:                         trimmedNonEmpty(g.Deny),
			Ask:                          trimmedNonEmpty(g.Ask),
			DisableBypassPermissionsMode: g.ForbidBypass,
			DisableAutoMode:              g.ForbidAuto,
		},
		AllowManagedHooksOnly:  g.ForceManagedHooksOnly,
		ParentSettingsBehavior: strings.TrimSpace(g.ParentSettingsBehavior),
	}
	if cmd := strings.TrimSpace(g.PEPHookCommand); cmd != "" {
		hooks, err := PEPHook(PEPHookConfig{Command: cmd, Matcher: "", Redact: false})
		if err != nil {
			return Policy{}, err
		}
		p.Hooks = hooks
	}
	return p, nil
}

// RenderAgentSDKGuardrail emits the managed-settings.json that wins precedence over the SDK
// program's programmatic options. Pure; no I/O. This is the precedence-winning object the
// brief calls for — built from the verified Render, not a second renderer.
func RenderAgentSDKGuardrail(g AgentSDKGuardrail) ([]byte, error) {
	p, err := g.ToPolicy()
	if err != nil {
		return nil, err
	}
	return Render(p)
}

// ValidateAgentSDKGuardrail validates the guardrail SERVER-SIDE (defense in depth): it
// renders the managed document and runs the connector's verified ValidateJSON, then adds
// the SDK-specific honesty check below. It returns the issue list (empty = valid) and an
// error only if the guardrail cannot be rendered at all.
func ValidateAgentSDKGuardrail(g AgentSDKGuardrail) ([]string, error) {
	rendered, err := RenderAgentSDKGuardrail(g)
	if err != nil {
		return nil, err
	}
	issues := ValidateJSON(rendered)
	// HONESTY: a managed defaultMode is the DEFAULT, not a hard CAP — only
	// disableBypassPermissionsMode actually FORBIDS bypassPermissions. So a guardrail that
	// pins a safe MaxMode but leaves ForbidBypass off does NOT stop the SDK program from
	// requesting bypassPermissions (or flipping to it via setPermissionMode at runtime).
	if mode := strings.TrimSpace(g.MaxMode); mode != "" && mode != ModeBypassPermissions && !g.ForbidBypass {
		issues = append(issues, "MaxMode pins a non-bypass default but ForbidBypass is off — defaultMode is only the DEFAULT, not a cap; set ForbidBypass (disableBypassPermissionsMode) to actually forbid bypassPermissions")
	}
	if mode := strings.TrimSpace(g.MaxMode); mode != "" && mode != ModeAuto && mode != ModeBypassPermissions && !g.ForbidAuto {
		issues = append(issues, "MaxMode pins a non-auto default but ForbidAuto is off — a program can still request permissionMode:'auto'; set ForbidAuto (disableAutoMode) to forbid the auto classifier")
	}
	return issues, nil
}

// AgentSDKPrecedenceFact maps a managed-settings key to the SDK PROGRAMMATIC option it
// overrides and the verified guarantee. It is the exposed contract dependents (the
// authoring console, the per-subagent editor #20) consume.
type AgentSDKPrecedenceFact struct {
	ManagedKey         string `json:"managed_key"`
	OverridesSDKOption string `json:"overrides_sdk_option"`
	Guarantee          string `json:"guarantee"`
}

// AgentSDKPrecedence returns the verified managed-key → SDK-option override mapping
// (reference data, not fabricated). Source: code.claude.com/docs/en/agent-sdk/
// {permissions,typescript,claude-code-features}, 2026-06-19.
func AgentSDKPrecedence() []AgentSDKPrecedenceFact {
	return []AgentSDKPrecedenceFact{
		{"permissions.defaultMode", "permissionMode", "the managed default applies; managed policy settings load regardless of the SDK's settingSources (settingSources:[] cannot disable them)"},
		{"permissions.disableBypassPermissionsMode", "permissionMode:'bypassPermissions' / allowDangerouslySkipPermissions", "forbids bypass outright; a scoped deny rule blocks the tool even in bypassPermissions"},
		{"permissions.disableAutoMode", "permissionMode:'auto'", "forbids the auto classifier mode"},
		{"permissions.deny", "disallowedTools / allowedTools", "a managed deny rule blocks the tool in EVERY mode, including bypassPermissions; deny always merges across scopes"},
		{"permissions.ask", "canUseTool / permissionPromptToolName", "a managed ask rule forces a prompt even in bypassPermissions"},
		{"allowManagedHooksOnly", "hooks", "loads ONLY managed/SDK hooks so the program cannot replace the governed PEP hook with its own"},
		{"hooks.PreToolUse (managed PEP)", "hooks / permissionPromptToolName", "distributes the governed PreToolUse PEP as a non-overridable managed hook — hooks run FIRST and can deny outright"},
		{"parentSettingsBehavior", "programmatic managed settings supplied by the SDK host", "first-wins drops them; merge applies them UNDER this admin tier, tighten-only"},
	}
}

// AgentSDKPrecedencePreview returns the console dry-run lines for governing an Agent SDK
// fleet: the per-option neutralization mapping, the MANDATORY honesty caveat, and the
// generic precedence/delivery facts (reused from DeliveryPreview). The caveat is verbatim
// the brief's requirement: enforcement is VERIFIED-DEPLOYED, NOT impossible to bypass.
func AgentSDKPrecedencePreview(g AgentSDKGuardrail) []DryRunLine {
	lines := []DryRunLine{{
		Scope: "agent-sdk-precedence",
		Note:  "managed policy settings take precedence over the SDK program's programmatic options and load regardless of its settingSources (settingSources:[] cannot disable them) — this is the layer that binds a customer-owned agent fleet",
	}}
	for _, f := range AgentSDKPrecedence() {
		lines = append(lines, DryRunLine{
			Scope: "overrides-sdk-option",
			Note:  f.ManagedKey + " overrides " + f.OverridesSDKOption + ": " + f.Guarantee,
		})
	}
	lines = append(lines, DryRunLine{
		Scope: "honesty-not-unbypassable",
		Note:  "this enforcement is VERIFIED-DEPLOYED, NOT impossible to bypass: a custom ANTHROPIC_BASE_URL or a third-party model provider (CLAUDE_CODE_USE_BEDROCK/VERTEX/FOUNDRY/MANTLE) routes the SDK program's traffic OUTSIDE the governed path and past the PEP — a per-client condition the control plane cannot observe from here",
	})
	// Reuse the verified generic precedence/delivery facts so the console view is complete.
	if rendered, err := RenderAgentSDKGuardrail(g); err == nil {
		lines = append(lines, DeliveryPreview(HasAnyKeys(rendered))...)
	}
	return lines
}

// trimmedNonEmpty returns a copy of in with each element trimmed and the empties dropped,
// or nil when nothing remains (so an all-blank slice renders no key).
func trimmedNonEmpty(in []string) []string {
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
