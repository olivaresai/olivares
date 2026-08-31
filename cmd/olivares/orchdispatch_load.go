// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	a2a "github.com/olivaresai/olivares/connectors/a2a"
	executor "github.com/olivaresai/olivares/core/runtime/executor"
)

// orchdispatch_load.go loads the orchestration Dispatcher from the operator-
// provisioned config (OLIVARES_ORCH_DISPATCH_CONFIG), mirroring
// loadDeployExecutorConfig / loadNotifyDestinations / loadApprovalBridgeConfig: an
// absent path keeps the deny-closed unwiredDispatcher, while a supplied unreadable/
// invalid file fails startup. Operator secrets (A2A out-of-band auth headers, trust
// anchors) live here, never in the module store.

// orchDispatchConfig is the operator's fire-actuation provisioning. Both blocks are
// OPTIONAL; with neither present the dispatcher is not wired (deny-closed).
type orchDispatchConfig struct {
	// OrchestratorRef is the comm-graph identity of the governed scheduler for an A2A
	// delegation edge. Optional; empty resolves to defaultOrchestratorRef.
	OrchestratorRef string `json:"orchestrator_ref,omitempty"`
	// Runtime maps schedule subjects to desired deployments reconciled via the
	// executor (the runtime fire route).
	Runtime struct {
		Targets []orchRuntimeTargetJSON `json:"targets"`
	} `json:"runtime,omitempty"`
	// A2A maps schedule subjects to verified remote agents a fire delegates to (the
	// signed-card A2A Task route).
	A2A struct {
		Agents []orchA2AAgentJSON `json:"agents"`
	} `json:"a2a,omitempty"`
}

// orchRuntimeTargetJSON is one operator-declared runtime target. Image/command/
// resource fields are references (non-sensitive); env values and wirings carry
// secret-store REFERENCES only, never a cleartext secret.
type orchRuntimeTargetJSON struct {
	SubjectKind string            `json:"subject_kind"`
	SubjectRef  string            `json:"subject_ref"`
	Runtime     string            `json:"runtime"`
	Target      string            `json:"target"`
	Environment string            `json:"environment"`
	Name        string            `json:"name"`
	Image       string            `json:"image"`
	Command     string            `json:"command"`
	Replicas    int               `json:"replicas"`
	Resources   map[string]string `json:"resources"`
	EnvRefs     []secretBindJSON  `json:"env_refs"`
	Wirings     []wiringJSON      `json:"wirings"`
}

type secretBindJSON struct {
	Name      string `json:"name"`
	SecretRef string `json:"secret_ref"`
}

type wiringJSON struct {
	ResourceKind string `json:"resource_kind"`
	ResourceRef  string `json:"resource_ref"`
	Mode         string `json:"mode"`
	SecretRef    string `json:"secret_ref"`
}

// orchA2AAgentJSON is one verified-before-use remote A2A agent. TrustJWKS (inline) or
// TrustJWKSFile (a path to a JWK Set) is the OPERATOR trust anchor; without one the
// agent's card can never reach trustVerified and every emission to it is denied.
// Headers are out-of-band auth (held here by value, never in the store, never in the
// A2A payload).
type orchA2AAgentJSON struct {
	SubjectKind string `json:"subject_kind"`
	SubjectRef  string `json:"subject_ref"`
	// Authority is the operator-pinned peer authority used by K5
	// ProtocolBinding. It is intentionally distinct from URL: remote-work plans
	// name this stable authority and never carry an endpoint or credential.
	Authority     string            `json:"authority"`
	Name          string            `json:"name"`
	URL           string            `json:"url"`
	TrustJWKS     string            `json:"trust_jwks"`
	TrustJWKSFile string            `json:"trust_jwks_file"`
	Headers       map[string]string `json:"headers"`
	WellKnownPath string            `json:"well_known_path"`
	Skill         string            `json:"skill"`
	// Scopes is the deny-by-default K5 least-privilege allowlist for this
	// agent/skill. The legacy scheduled-fire path does not consult it; the full
	// RemoteWorkExecutor does and refuses an empty list.
	Scopes []string `json:"scopes"`
	// ProtocolRuleRefs and ProtocolPermissionProfileRef are the exact
	// operator-owned policy tuple a live ProtocolBindingSpec must carry. Empty
	// values resolve to the closed remote-work defaults.
	ProtocolRuleRefs             []string `json:"protocol_rule_refs"`
	ProtocolPermissionProfileRef string   `json:"protocol_permission_profile_ref"`
	Text                         string   `json:"text"`
	TimeoutSeconds               int      `json:"timeout_seconds"`
	// InterruptRoute is local operator authority for actionable remote Task
	// pauses. A K5 target is not wired unless all three IDs are present.
	InterruptChannelID       string `json:"interrupt_channel_id"`
	InterruptSenderUserID    string `json:"interrupt_sender_user_id"`
	InterruptRecipientUserID string `json:"interrupt_recipient_user_id"`
}

// loadOrchDispatchConfig reads OLIVARES_ORCH_DISPATCH_CONFIG. A missing path is an
// empty config (dispatcher not wired; fires stay declared-not-fired). A supplied path
// must be readable and contain valid JSON or startup fails closed.
func loadOrchDispatchConfig(_ *slog.Logger) (orchDispatchConfig, error) {
	path := os.Getenv("OLIVARES_ORCH_DISPATCH_CONFIG")
	if path == "" {
		return orchDispatchConfig{}, nil
	}
	var cfg orchDispatchConfig
	if err := loadOperatorJSONConfig("OLIVARES_ORCH_DISPATCH_CONFIG", path, &cfg); err != nil {
		return orchDispatchConfig{}, err
	}
	return cfg, nil
}

// newOrchestrationDispatcher builds the real orchestration.Dispatcher from config,
// or nil when neither a runtime target nor an A2A agent is provisioned (the module
// then keeps its deny-closed unwiredDispatcher). exec is the shared engine (may
// be nil; the runtime route then fails closed per fire with an explicit error).
func newOrchestrationDispatcher(cfg orchDispatchConfig, exec *executor.Executor, log *slog.Logger) *orchestrationDispatcher {
	runtimes := make(map[string]runtimeTarget)
	for _, t := range cfg.Runtime.Targets {
		kind := orDefaultStr(t.SubjectKind, "agent")
		if strings.TrimSpace(t.SubjectRef) == "" || strings.TrimSpace(t.Runtime) == "" {
			log.Warn("orch-dispatch: skipping runtime target with empty subject_ref/runtime")
			continue
		}
		runtimes[subjectKey(kind, t.SubjectRef)] = runtimeTarget{
			runtime: t.Runtime, target: t.Target, environment: t.Environment, name: t.Name,
			image: t.Image, command: t.Command, replicas: t.Replicas, resources: t.Resources,
			envRefs: toSecretBindings(t.EnvRefs), wirings: toWirings(t.Wirings),
		}
	}

	agents := make(map[string]a2aTarget)
	for _, a := range cfg.A2A.Agents {
		kind := orDefaultStr(a.SubjectKind, "agent")
		if strings.TrimSpace(a.SubjectRef) == "" || strings.TrimSpace(a.URL) == "" {
			log.Warn("orch-dispatch: skipping a2a agent with empty subject_ref/url")
			continue
		}
		anchor := resolveTrustAnchor(a, log)
		if len(anchor) == 0 {
			log.Warn("orch-dispatch: a2a agent has no trust anchor; every emission to it will be DENIED (deny-closed)", "subject", a.SubjectRef)
		}
		agents[subjectKey(kind, a.SubjectRef)] = a2aTarget{
			name: orDefaultStr(a.Name, a.SubjectRef), url: a.URL, skill: a.Skill, text: a.Text,
			client: a2a.NewClient(a2a.EmitConfig{
				TrustJWKS:     anchor,
				Headers:       a.Headers,
				WellKnownPath: a.WellKnownPath,
				Timeout:       time.Duration(a.TimeoutSeconds) * time.Second,
			}),
		}
	}

	if len(runtimes) == 0 && len(agents) == 0 {
		return nil
	}
	log.Info("orch-dispatch: orchestration dispatcher wired (module IV now ACTS)", "runtime_targets", len(runtimes), "a2a_agents", len(agents))
	d := &orchestrationDispatcher{runtimes: runtimes, agents: agents, orchestratorRef: strings.TrimSpace(cfg.OrchestratorRef), log: log}
	// Assign the engine ONLY when non-nil so a nil *executor.Executor never becomes a
	// non-nil runtimeEngine interface (the runtime route's nil check must stay honest).
	if exec != nil {
		d.exec = exec
	}
	return d
}

// resolveTrustAnchor returns the operator trust anchor for an A2A agent: inline JWKS
// wins; otherwise the file at TrustJWKSFile (a public key set, not a secret). A
// missing/unreadable file yields nil (the agent then denies every emission).
func resolveTrustAnchor(a orchA2AAgentJSON, log *slog.Logger) []byte {
	if s := strings.TrimSpace(a.TrustJWKS); s != "" {
		return []byte(s)
	}
	if p := strings.TrimSpace(a.TrustJWKSFile); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			log.Warn("orch-dispatch: cannot read a2a trust_jwks_file; agent emissions will be denied", "path", p, "subject", a.SubjectRef)
			return nil
		}
		return b
	}
	return nil
}

func toSecretBindings(in []secretBindJSON) []executor.SecretBinding {
	if len(in) == 0 {
		return nil
	}
	out := make([]executor.SecretBinding, 0, len(in))
	for _, s := range in {
		out = append(out, executor.SecretBinding{Name: s.Name, SecretRef: s.SecretRef})
	}
	return out
}

func toWirings(in []wiringJSON) []executor.Wiring {
	if len(in) == 0 {
		return nil
	}
	out := make([]executor.Wiring, 0, len(in))
	for _, w := range in {
		out = append(out, executor.Wiring{ResourceKind: w.ResourceKind, ResourceRef: w.ResourceRef, Mode: w.Mode, SecretRef: w.SecretRef})
	}
	return out
}

func orDefaultStr(v, d string) string {
	if strings.TrimSpace(v) == "" {
		return d
	}
	return v
}

func canonicalA2AScopes(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			seen[value] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for value := range seen {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
