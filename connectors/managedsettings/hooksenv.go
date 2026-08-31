// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package managedsettings

import (
	"fmt"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/connectors/internal/redact"
)

// This file is the NET-NEW authoring of the TWO managed-settings surfaces adds on
// top of the permission/MCP/sandbox surface the connector already renders (CLA-05):
//
//   - `hooks`: the managed Claude Code hooks block. Its headline use is DISTRIBUTING
//     the PreToolUse Policy-Enforcement-Point (PEP) hook as a MANAGED hook —
//     non-overridable, anti-tamper when paired with `allowManagedHooksOnly` — so the
//     control plane can turn "observe" into "govern" on the fleet without touching the
//     subscription. This connector only AUTHORS/RENDERS the hook entry; the PEP decision
//     endpoint/binary itself is (the legal arrow: governance module → this Apache
//     connector → rendered file → deploy/VII distributes it).
//   - `env`: the managed telemetry environment. This is the SANCTIONED observation path
//     (G1): Claude Code's OpenTelemetry export is NOT plan-gated, so pushing
//     `CLAUDE_CODE_ENABLE_TELEMETRY` + `OTEL_*` from the managed tier lets the control
//     plane OBSERVE subscription use legally — it never proxies inference or brokers the
//     subscription credential.
//
// VERIFIED 2026-06-08 against the live docs (recorded in
//):
//   - https://code.claude.com/docs/en/hooks (the `{matcher, hooks:[{type,command,timeout}]}`
//     shape; `type` is "command"; PreToolUse decision allow/deny/ask/defer)
//   - https://code.claude.com/docs/en/settings (top-level `env` object; `hooks` object)
//   - https://code.claude.com/docs/en/monitoring-usage (the CLAUDE_CODE_ENABLE_TELEMETRY
//     + OTEL_* contract; OTEL_LOG_USER_PROMPTS / OTEL_LOG_TOOL_* default OFF)
// Re-verify on the next build; do not pin from memory.

// hookEventPreToolUse is the Claude Code hook event the PEP binds to: it runs
// BEFORE a tool executes and its decision (allow/deny/ask/defer) gates the action.
const hookEventPreToolUse = "PreToolUse"

// hookEventPostToolUse runs AFTER a tool and is where output redaction (secrets/PII)
// is applied (PostToolUse redaction half).
const hookEventPostToolUse = "PostToolUse"

// hookEventMessageDisplay is the 2.1.17x display-only hook event (VERIFIED
// 2026-06-10, docs.claude.com/en/docs/claude-code/hooks; changelog 2.1.152): it
// fires "while assistant message text is displayed", supports NO matchers, has NO
// decision control (its only output is hookSpecificOutput.displayContent, which
// replaces the delta ON SCREEN only — the transcript and what Claude sees keep
// the original), and defaults to a 10s timeout. Named so authoring can validate
// the no-matcher rule; the runtime/PEP semantics live in connectors/claude.
const hookEventMessageDisplay = "MessageDisplay"

// Hook entry types (VERIFIED 2026-06-10, hooks doc). "command" is a local
// executable receiving the event JSON on stdin; "prompt" (the 2.1.x addition) is
// an LLM-evaluated prompt hook whose config may set continueOnBlock.
const (
	hookTypeCommand = "command"
	hookTypePrompt  = "prompt"
)

// HookCommand is one hook entry — a `command` hook (a local executable that
// receives the event JSON on stdin and returns the decision JSON on stdout) or a
// 2.1.x `prompt` hook (an LLM-evaluated check; VERIFIED 2026-06-10, hooks doc
// "Prompt-based hooks"). Timeout is in seconds; 0 means the Claude Code default.
// The command/prompt is operator-controlled content on the managed host — this
// connector never executes it, only renders it.
type HookCommand struct {
	Type    string `json:"type"`
	Command string `json:"command,omitempty"`
	// Prompt is the prompt-hook text (required for type "prompt").
	Prompt  string `json:"prompt,omitempty"`
	Timeout int    `json:"timeout,omitempty"`
	// ContinueOnBlock is a PROMPT-hook config field (changelog 2.1.139): when the
	// prompt returns ok:false, feed the reason back to Claude and CONTINUE the
	// turn instead of stopping (the client implements it as continue:true on the
	// resulting decision block; PostToolUse/TeammateIdle honor it, other events
	// end the turn regardless). Default false — the STRICTEST behavior.
	ContinueOnBlock bool `json:"continueOnBlock,omitempty"`
}

// HookMatcher binds a tool-name matcher to the commands that run for it. An empty
// Matcher matches ALL tools (the deny-closed default coverage a PEP wants: no tool
// escapes the enforcement point). Matcher is a tool name or a regex (Claude Code
// matches it against the tool being invoked).
type HookMatcher struct {
	Matcher string        `json:"matcher,omitempty"`
	Hooks   []HookCommand `json:"hooks"`
}

// Telemetry env keys (VERIFIED 2026-06-08, code.claude.com/docs/en/monitoring-usage).
// These are the managed-settings `env` keys that turn Claude Code's OpenTelemetry
// export ON and point it at the control plane's collector — the sanctioned, plan-
// UNGATED way to OBSERVE subscription use (never to proxy it).
const (
	EnvEnableTelemetry = "CLAUDE_CODE_ENABLE_TELEMETRY" // "1" turns OTEL export on
	EnvOTLPProtocol    = "OTEL_EXPORTER_OTLP_PROTOCOL"  // grpc | http/protobuf | http/json
	EnvOTLPEndpoint    = "OTEL_EXPORTER_OTLP_ENDPOINT"  // the control-plane collector URL
	EnvMetricsExporter = "OTEL_METRICS_EXPORTER"        // "otlp" to export metrics
	EnvLogsExporter    = "OTEL_LOGS_EXPORTER"           // "otlp" to export events/logs
	// Privacy knobs — DEFAULT OFF (minimal-data, docs/SECURITY-HARDENING.md). Turning these on ships
	// prompt/tool CONTENT off the developer's machine into telemetry; the control plane
	// must opt in explicitly and govern the residency/redaction of what it then receives.
	EnvLogUserPrompts = "OTEL_LOG_USER_PROMPTS"
	EnvLogToolContent = "OTEL_LOG_TOOL_CONTENT"
	EnvLogToolDetails = "OTEL_LOG_TOOL_DETAILS"
	EnvOTLPHeaders    = "OTEL_EXPORTER_OTLP_HEADERS" // a footgun: commonly an inline bearer token
)

// EnvBaseURL is Claude Code's inference endpoint override (NOT a telemetry key — grouped
// here with the env-key vocabulary). A non-default ANTHROPIC_BASE_URL routes inference
// through a proxy/gateway and BYPASSES server-managed-settings entirely (VERIFIED
// 2026-06-20, code.claude.com/docs/en/server-managed-settings: server-managed settings
// "are not available when using ... a non-default ANTHROPIC_BASE_URL"). A live value
// diverging from the org's authorized gateway is therefore a posture finding (verify.go);
// it is compared by PRESENCE+divergence and the detail is hashed — the URL (which may embed
// a token) is never emitted.
const EnvBaseURL = "ANTHROPIC_BASE_URL"

// TelemetryConfig is the governance-authored intent for the managed telemetry `env`.
// It is deliberately small: it turns observation ON and points it at the collector,
// leaving CONTENT capture OFF unless the operator explicitly opts in (and accepts the
// residency/redaction duty that creates).
type TelemetryConfig struct {
	// Endpoint is the control-plane OTLP collector URL (required to deliver telemetry).
	Endpoint string
	// Protocol is the OTLP wire protocol; defaults to "grpc" when empty.
	Protocol string
	// Metrics / Logs select which signals to export (at least one should be true to be
	// useful). Both default to true via TelemetryEnv when the struct leaves them unset
	// only if Endpoint is set — see TelemetryEnv.
	Metrics bool
	Logs    bool
	// IncludePrompts / IncludeToolContent are the EXPLICIT, off-by-default content knobs.
	// Leaving them false keeps the managed env minimal-data.
	IncludePrompts     bool
	IncludeToolContent bool
}

// TelemetryEnv renders the managed telemetry `env` map from the authored intent. It
// is the helper the console uses to compose the "observe subscription use legally"
// posture: CLAUDE_CODE_ENABLE_TELEMETRY=1 + the OTLP exporter env, with content
// capture OFF unless explicitly requested. It NEVER sets OTEL_EXPORTER_OTLP_HEADERS
// (an inline bearer token in a plaintext managed file is a credential leak — auth to
// the collector must be mTLS or a secret-manager reference, not inlined here).
//
// An empty Endpoint yields an env that enables telemetry but leaves the endpoint to
// the host's own OTEL config (a valid posture when the collector is auto-discovered);
// the caller decides. Metrics/Logs both default ON here (the common case) so a
// zero-value-but-endpoint config still exports something rather than silently nothing.
func TelemetryEnv(cfg TelemetryConfig) map[string]string {
	env := map[string]string{EnvEnableTelemetry: "1"}
	proto := strings.TrimSpace(cfg.Protocol)
	if proto == "" {
		proto = "grpc"
	}
	env[EnvOTLPProtocol] = proto
	if ep := strings.TrimSpace(cfg.Endpoint); ep != "" {
		env[EnvOTLPEndpoint] = ep
	}
	// Default both signals ON unless the caller explicitly narrowed to one.
	metrics, logs := cfg.Metrics, cfg.Logs
	if !metrics && !logs {
		metrics, logs = true, true
	}
	if metrics {
		env[EnvMetricsExporter] = "otlp"
	}
	if logs {
		env[EnvLogsExporter] = "otlp"
	}
	// Content capture is opt-in; when off we OMIT the keys entirely (absence = default
	// off) so the rendered env stays minimal rather than spelling out "0" everywhere.
	if cfg.IncludePrompts {
		env[EnvLogUserPrompts] = "1"
	}
	if cfg.IncludeToolContent {
		env[EnvLogToolContent] = "1"
	}
	return env
}

// PEPHookConfig configures the managed PreToolUse PEP hook that managed settings
// distributes on the policy layer's behalf. Command is the local PEP-client executable the hook runs (owns
// what it does: consult the control-plane PDP, return allow/deny/ask). Matcher scopes
// which tools the PEP gates ("" = all tools, the deny-closed default). Redact installs
// a paired PostToolUse hook (same command, post phase) for output redaction.
type PEPHookConfig struct {
	// Command is the PEP-client executable path on the managed host (required).
	Command string
	// Matcher scopes the PreToolUse hook; "" matches all tools (full coverage).
	Matcher string
	// TimeoutSecs bounds the hook; 0 uses the Claude Code default. A PEP should set a
	// small, explicit timeout so a hung control plane fails the hook fast (deny-closed
	// is concern: a non-zero hook exit/blank decision must not silently allow).
	TimeoutSecs int
	// Redact also installs a PostToolUse redaction hook bound to the same command.
	Redact bool
}

// PEPHook builds the managed `hooks` block that distributes the PreToolUse PEP
// (and, optionally, the PostToolUse redaction hook). It returns the event→matchers map
// to assign to Policy.Hooks. An empty Command is an error (a PEP hook with no command
// is a silent no-op that would look governed while enforcing nothing — deny-closed on
// the authoring side, never render a hollow PEP).
func PEPHook(cfg PEPHookConfig) (map[string][]HookMatcher, error) {
	cmd := strings.TrimSpace(cfg.Command)
	if cmd == "" {
		return nil, fmt.Errorf("managed-settings: PEP hook requires a command (a hookless PEP enforces nothing)")
	}
	if cfg.TimeoutSecs < 0 {
		return nil, fmt.Errorf("managed-settings: PEP hook timeout must be >= 0, got %d", cfg.TimeoutSecs)
	}
	entry := HookMatcher{
		Matcher: strings.TrimSpace(cfg.Matcher),
		Hooks:   []HookCommand{{Type: hookTypeCommand, Command: cmd, Timeout: cfg.TimeoutSecs}},
	}
	hooks := map[string][]HookMatcher{hookEventPreToolUse: {entry}}
	if cfg.Redact {
		hooks[hookEventPostToolUse] = []HookMatcher{entry}
	}
	return hooks, nil
}

// validateEnv checks a managed `env` block server-side (defense in depth). It is
// deny-closed on INLINE CREDENTIALS: a managed-settings.json is a plaintext file on the
// host's disk, so a value that looks like a secret (a bearer token, an sk-ant- key, a
// password) must NEVER be authored here — it would distribute the secret to every
// managed host in the clear. OTEL_EXPORTER_OTLP_HEADERS is called out by name because
// it is the usual place an operator is tempted to inline a collector token.
func validateEnv(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	var issues []string
	for _, k := range sortedKeys(env) {
		v := env[k]
		if k == EnvOTLPHeaders && strings.TrimSpace(v) != "" {
			issues = append(issues, fmt.Sprintf("env.%s must not inline collector credentials in a plaintext managed file — use mTLS or a secret-manager reference, not %s", EnvOTLPHeaders, EnvOTLPHeaders))
			continue
		}
		if redact.ContainsSecret(k + "=" + v) {
			issues = append(issues, fmt.Sprintf("env.%s appears to inline a credential — managed-settings.json is plaintext on the host; reference a secret manager instead", k))
		}
	}
	return issues
}

// validateHooks checks a managed `hooks` block server-side. Every hook entry must
// be a well-formed `command` hook (non-empty command) or `prompt` hook (non-empty
// prompt; the 2.1.x type — VERIFIED 2026-06-10) with a non-negative timeout — a
// malformed or empty hook is rejected so a hollow (non-enforcing) PEP never
// publishes looking valid. continueOnBlock is a prompt-hook config field only; on
// a command hook it is dead config the client ignores, so it is flagged.
// MessageDisplay supports no matchers (changelog 2.1.152) — a matcher there would
// silently never scope anything.
func validateHooks(hooks map[string][]HookMatcher) []string {
	if len(hooks) == 0 {
		return nil
	}
	var issues []string
	for _, event := range sortedHookEvents(hooks) {
		for i, m := range hooks[event] {
			if event == hookEventMessageDisplay && strings.TrimSpace(m.Matcher) != "" {
				issues = append(issues, fmt.Sprintf("hooks.%s[%d].matcher %q — MessageDisplay does not support matchers (it fires for every assistant message); remove the matcher", event, i, m.Matcher))
			}
			if len(m.Hooks) == 0 {
				issues = append(issues, fmt.Sprintf("hooks.%s[%d] has no command entries (an empty matcher enforces nothing)", event, i))
			}
			for j, h := range m.Hooks {
				ctx := fmt.Sprintf("hooks.%s[%d].hooks[%d]", event, i, j)
				switch t := strings.TrimSpace(h.Type); t {
				case "", hookTypeCommand:
					if strings.TrimSpace(h.Command) == "" {
						issues = append(issues, ctx+" has an empty command")
					}
					if h.ContinueOnBlock {
						issues = append(issues, ctx+".continueOnBlock is a prompt-hook config field — on a command hook the client ignores it (dead config)")
					}
				case hookTypePrompt:
					if strings.TrimSpace(h.Prompt) == "" {
						issues = append(issues, ctx+` has an empty prompt (a "prompt" hook checks nothing without one)`)
					}
				default:
					issues = append(issues, fmt.Sprintf("%s.type %q is not supported — Claude Code hooks are %q or %q", ctx, h.Type, hookTypeCommand, hookTypePrompt))
				}
				if h.Timeout < 0 {
					issues = append(issues, fmt.Sprintf("%s.timeout must be >= 0, got %d", ctx, h.Timeout))
				}
			}
		}
	}
	return issues
}

// AntiTamperReview returns authoring-time advisories about a policy's hook/env posture
// — NON-fatal notes (distinct from ValidateJSON's hard issues) the console surfaces so
// an operator distributes the PEP and telemetry HONESTLY. The two it raises:
//
//   - Hooks WITHOUT AllowManagedHooksOnly: Claude Code's local hooks have no native
//     tamper-protection (documented caveat). A managed PreToolUse PEP that is
//     not paired with allowManagedHooksOnly can be undercut by a developer's own
//     user/project hooks — so the enforcement looks present but is bypassable.
//   - Content-capture telemetry (OTEL_LOG_USER_PROMPTS / OTEL_LOG_TOOL_CONTENT) ON:
//     this ships prompt/tool CONTENT off the host into telemetry, creating a residency/
//     redaction duty the control plane must own (docs/SECURITY-HARDENING.md) — flagged so it is a
//     deliberate choice, never an accident.
func AntiTamperReview(p Policy) []string {
	var notes []string
	if hasPreToolUseHook(p.Hooks) && !p.AllowManagedHooksOnly {
		notes = append(notes, "managed hooks are authored without allowManagedHooksOnly — Claude Code local hooks have no native tamper-protection; set allowManagedHooksOnly so a developer's user/project hooks cannot undercut the managed PEP (anti-tamper)")
	}
	if v, ok := p.Env[EnvLogUserPrompts]; ok && envValueOn(v) {
		notes = append(notes, EnvLogUserPrompts+" is ON — prompt CONTENT will leave the host in telemetry; the control plane must govern its residency/redaction (docs/08 §3)")
	}
	if v, ok := p.Env[EnvLogToolContent]; ok && envValueOn(v) {
		notes = append(notes, EnvLogToolContent+" is ON — tool CONTENT will leave the host in telemetry; the control plane must govern its residency/redaction (docs/08 §3)")
	}
	return notes
}

// envValueOn reports whether an env value is an enabling ("on") value.
func envValueOn(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// hasPreToolUseHook reports whether a hooks block carries at least one PreToolUse
// command (the PEP signal) — used by drift to detect an undistributed enforcement point.
func hasPreToolUseHook(hooks map[string][]HookMatcher) bool {
	for _, m := range hooks[hookEventPreToolUse] {
		for _, h := range m.Hooks {
			if strings.TrimSpace(h.Command) != "" {
				return true
			}
		}
	}
	return false
}

// envHasTelemetry reports whether a managed env turns Claude Code telemetry ON
// (CLAUDE_CODE_ENABLE_TELEMETRY set to a non-empty, non-"0" value).
func envHasTelemetry(env map[string]string) bool {
	v, ok := env[EnvEnableTelemetry]
	if !ok {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// sortedKeys returns a map's keys in stable order (deterministic validation output).
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sortedHookEvents returns a hooks map's event names in stable order.
func sortedHookEvents(m map[string][]HookMatcher) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
