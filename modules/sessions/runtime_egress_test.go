// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/modules/sessions/egress"
)

func TestLaunchSpecNetworkPolicy(t *testing.T) {
	m := New(WithConfinement([]string{"/engine-data"}, true))
	bound := BoundProvider{Kind: "anthropic", Endpoint: "https://selected.example/v1"}
	for _, resume := range []string{"", "existing-conversation"} {
		spec := m.childSpec(CreateRunParams{WorkspaceDir: "/work", ProviderHome: &ProviderHomeSnapshot{Driver: providerDriverClaude}}, childDecision{cred: Credential{bound: bound}, resumeID: resume, gateEnv: []EnvVar{{Name: "OLIVARES_HOOK_PEP_URL", Value: "http://127.0.0.1:8447/"}}})
		if spec.NetworkPolicy == nil || !reflect.DeepEqual(spec.NetworkPolicy.Providers, []string{bound.Endpoint}) {
			t.Fatalf("resume=%q: custom binding must exclusively select its own endpoint: %+v", resume, spec.NetworkPolicy)
		}
		if !reflect.DeepEqual(spec.NetworkPolicy.Controls, []string{"http://127.0.0.1:8447/"}) {
			t.Fatalf("hook control endpoint missing: %+v", spec.NetworkPolicy)
		}
	}
}

// The boundary relays every hook endpoint the launch gate grants, the Codex and
// Grok hooks beside the PEP one, and no other value of the gate's.
func TestLaunchSpecNetworkPolicyRelaysEveryHookEndpoint(t *testing.T) {
	m := New(WithConfinement([]string{"/engine-data"}, true))
	gate := []EnvVar{
		{Name: "OLIVARES_HOOK_PEP_URL", Value: "http://127.0.0.1:8447/"},
		{Name: "OLIVARES_HOOK_PEP_TOKEN", Value: "http://127.0.0.1:9000/"},
		{Name: "OLIVARES_CODEX_HOOK_URL", Value: "http://127.0.0.1:8448/"},
		{Name: "OLIVARES_CODEX_HOOK_TOKEN", Value: "http://127.0.0.1:9002/"},
		{Name: "OLIVARES_GROK_HOOK_URL", Value: "http://127.0.0.1:8449/"},
		{Name: "OLIVARES_CONTEXT_STRATEGY", Value: "http://127.0.0.1:9001/"},
	}
	p := CreateRunParams{WorkspaceDir: "/work", ProviderHome: &ProviderHomeSnapshot{Driver: providerDriverCodex, AuthSource: AuthSourceManagedInjection}}
	spec := m.childSpec(p, childDecision{cred: Credential{bound: BoundProvider{Kind: "openai", Endpoint: "https://selected.example/v1"}}, gateEnv: gate})
	want := []string{"http://127.0.0.1:8447/", "http://127.0.0.1:8448/", "http://127.0.0.1:8449/"}
	if spec.NetworkPolicy == nil || !reflect.DeepEqual(spec.NetworkPolicy.Controls, want) {
		t.Fatalf("a confined Codex launch does not relay exactly its hook endpoints: %+v", spec.NetworkPolicy)
	}
}

// A record-bound launch of a measured tool (Claude Code, Codex) is confined to its
// record's endpoint. A tool whose host inventory has not been measured behind the
// proxy keeps today's network until it is (Grok Build and OpenCode), and so do
// account logins, whose vendor endpoints are not measured yet.
func TestLaunchSpecNetworkPolicyEnforcesOnlyMeasuredTools(t *testing.T) {
	m := New(WithConfinement([]string{"/engine-data"}, true))
	for _, tc := range []struct{ driver, kind string }{{"claude", "anthropic"}, {"codex", "openai"}, {"codex", "openai_compatible"}, {"codex", "ollama"}} {
		bound := BoundProvider{Kind: tc.kind, Endpoint: "https://selected.example/v1"}
		spec := m.childSpec(CreateRunParams{WorkspaceDir: "/work", ProviderHome: &ProviderHomeSnapshot{Driver: tc.driver, AuthSource: AuthSourceManagedInjection}}, childDecision{cred: Credential{bound: bound}})
		if spec.NetworkPolicy == nil || !reflect.DeepEqual(spec.NetworkPolicy.Providers, []string{bound.Endpoint}) {
			t.Fatalf("a profiled record-bound %s launch on %s is not confined to its endpoint: %+v", tc.driver, tc.kind, spec.NetworkPolicy)
		}
	}
	for _, tc := range []struct{ driver, kind string }{{"grok", "xai"}, {"opencode", "anthropic"}, {"claude", ""}, {"codex", ""}} {
		spec := m.childSpec(CreateRunParams{WorkspaceDir: "/work", ProviderHome: &ProviderHomeSnapshot{Driver: tc.driver, AuthSource: AuthSourceAccountHome}}, childDecision{cred: Credential{bound: BoundProvider{Kind: tc.kind, Endpoint: "https://selected.example/v1"}}})
		if spec.NetworkPolicy != nil {
			t.Fatalf("%s bound=%q: unmeasured tool network is enforced: %+v", tc.driver, tc.kind, spec.NetworkPolicy)
		}
	}
}

// The run detail of a Codex sandbox fallback says whether the session's network
// is confined, from the launch's own policy.
func TestCodexSandboxDetailNamesTheNetworkBoundary(t *testing.T) {
	confined := LaunchSpec{NetworkPolicy: &egress.Policy{Providers: []string{"https://selected.example/v1"}}}
	for _, tc := range []struct {
		fallback bool
		spec     LaunchSpec
		want     string
	}{
		{false, LaunchSpec{}, ""},
		{false, confined, ""},
		{true, LaunchSpec{}, "Codex native sandbox unavailable; OS confinement enforced; network not confined"},
		{true, confined, "Codex native sandbox unavailable; OS confinement enforced; network confined to the bound provider"},
	} {
		if got := codexSandboxDetail(CreateRunParams{codexSandboxFallback: tc.fallback}, tc.spec); got != tc.want {
			t.Errorf("fallback=%v confined=%v: %q, want %q", tc.fallback, tc.spec.NetworkPolicy != nil, got, tc.want)
		}
	}
}
