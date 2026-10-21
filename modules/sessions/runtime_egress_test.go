// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
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

// The proxy forwards only the key the record mint put in the child's environment:
// the policy carries its SHA-256, and only for the kinds whose tool sends the key
// as a bearer or key header the proxy compares (Claude Code, Codex).
func TestLaunchSpecNetworkPolicyPinsTheBoundKey(t *testing.T) {
	m := New(WithConfinement([]string{"/engine-data"}, true))
	const key = "sk-own-0123456789"
	for _, tc := range []struct {
		driver, kind, env string
		pinned            bool
	}{
		{"claude", "anthropic", "ANTHROPIC_API_KEY", true},
		{"codex", "openai", "OPENAI_API_KEY", true},
		{"codex", "openai_compatible", "OPENAI_API_KEY", true},
		{"codex", "ollama", "", false},
		{"gemini-cli", "gemini", "GEMINI_API_KEY", false}, // the key header is not measured: not checked
		{"claude", "anthropic", "", false},                // no key in the environment: nothing to pin
	} {
		var providerEnv []EnvVar
		if tc.env != "" {
			providerEnv = []EnvVar{{Name: tc.env, Value: " " + key + "\n"}, {Name: "ANTHROPIC_BASE_URL", Value: "https://selected.example/v1"}}
		}
		spec := m.childSpec(CreateRunParams{WorkspaceDir: "/work", ProviderHome: &ProviderHomeSnapshot{Driver: tc.driver, AuthSource: AuthSourceManagedInjection}},
			childDecision{cred: Credential{bound: BoundProvider{Kind: tc.kind, Endpoint: "https://selected.example/v1"}}, providerEnv: providerEnv})
		if spec.NetworkPolicy == nil {
			t.Fatalf("%s/%s: no network policy", tc.driver, tc.kind)
		}
		want := [sha256.Size]byte{}
		if tc.pinned {
			want = sha256.Sum256([]byte(key))
		}
		if spec.NetworkPolicy.CredentialSHA256 != want {
			t.Errorf("%s/%s env=%q: digest %x, want %x", tc.driver, tc.kind, tc.env, spec.NetworkPolicy.CredentialSHA256, want)
		}
		// The helper's policy travels in argv: neither the digest nor the callback may be in it.
		raw, err := json.Marshal(spec.NetworkPolicy)
		if err != nil || strings.Contains(string(raw), "CredentialSHA256") || strings.Contains(string(raw), "OnRefusal") {
			t.Errorf("%s/%s: policy JSON %s (%v)", tc.driver, tc.kind, raw, err)
		}
	}
}

// A refused key is one core Finding on the session's core Session. It names the
// session, host and header and carries no header value.
func TestEgressRefusalRecordsOneFindingOnTheSession(t *testing.T) {
	for _, be := range profileBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			ctx := context.Background()
			fr := &fakeRunner{initSID: "core-session"}
			m, st, _ := openProfiledRuntime(t, be, WithRunner(fr), WithCredentialSource(staticCred()))
			tenant := ensureTenant(t, st, "egress-refusal-"+be.name)
			config, user, _, _ := twoHomes(t)
			prof := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "claude", ConfigHome: config, UserHome: user, DisplayName: "A"})
			run, err := m.createRun(ctx, tenant, CreateRunParams{
				Transport: TransportStreamJSON, PermissionMode: "default", Isolation: IsolationNative,
				Actor: "user:u1", ActorKind: model.ActorUser, ProviderProfileRef: prof.Ref,
			})
			if err != nil || run.CoreSessionID == "" {
				t.Fatalf("run: %+v (%v)", run, err)
			}
			policy := &egress.Policy{Providers: []string{"https://api.example"}, CredentialSHA256: sha256.Sum256([]byte("sk-own"))}
			m.watchEgressRefusals(tenant, run.RunRef, policy)
			if policy.OnRefusal == nil {
				t.Fatal("a policy with a key digest has no refusal callback")
			}
			if err := policy.OnRefusal(egress.Refusal{Host: "api.example", Header: "X-Api-Key"}); err != nil {
				t.Fatal(err)
			}
			var found []model.Finding
			if err := st.View(ctx, tenant, func(sc store.Scope) error {
				var err error
				found, _, err = sc.Findings().List(ctx, model.Query{Filters: []model.Filter{eq("subject_kind", "session"), eq("subject_id", run.CoreSessionID)}, Limit: 10})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if len(found) != 1 {
				t.Fatalf("%d findings on the session, want 1", len(found))
			}
			f := found[0]
			text := fmt.Sprint(f.Title, f.Kind, f.Metadata)
			for _, want := range []string{run.RunRef, "api.example", "X-Api-Key"} {
				if !strings.Contains(text, want) {
					t.Errorf("finding does not name %q: %s", want, text)
				}
			}
			if f.Severity != model.SeverityHigh || f.Status != model.FindingOpen || f.Source != Name {
				t.Errorf("finding severity %s status %s source %s", f.Severity, f.Status, f.Source)
			}
			// A policy without a key digest gets no callback.
			bare := &egress.Policy{Providers: []string{"https://api.example"}}
			m.watchEgressRefusals(tenant, run.RunRef, bare)
			if bare.OnRefusal != nil {
				t.Error("an unkeyed policy was given a refusal callback")
			}
		})
	}
}
