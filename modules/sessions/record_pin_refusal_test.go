// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

// A SESSION BOUND TO A PROVIDER RECORD REACHES ONLY THAT RECORD'S ENDPOINT, OR IT DOES NOT
// START. A driver x record pair with no native way to hold the tool
// to the record's address is refused when it is bound and again when it launches, with one
// sentence naming what the tool runs on.
func TestARecordTheToolCannotBeHeldToIsRefusedOnBindAndLaunch(t *testing.T) {
	m, _, tenant, _ := newRuntimeHarness(t, WithProviderSecretVault(newFakeVault()))
	m.UseExecutionEnvironmentRef(testEnvRef)
	gateway := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{Kind: ProviderKindOpenAICompatible, DisplayName: "Gateway",
		APIKey: testProviderKey, BaseURL: "https://llm.example.com/v1"})
	for driver, want := range map[string]string{
		providerDriverClaude:   `Claude Code runs only on an Anthropic key; "Gateway" (kind openai_compatible) is not one of these, so the session does not start`,
		providerDriverGrok:     `Grok Build runs only on an xAI key; "Gateway" (kind openai_compatible) is not one of these, so the session does not start`,
		providerDriverOpenCode: `OpenCode runs only on an Anthropic, OpenAI or xAI key at the provider's own address, or on a local model (Ollama); "Gateway" (kind openai_compatible) is not one of these, so the session does not start`,
		"some-future-driver":   "driver some-future-driver cannot be held to a provider from Providers; use its own sign-in",
	} {
		_, err := m.CreateProfile(context.Background(), tenant, CreateProfileInput{Driver: driver, ConfigHome: t.TempDir(), UserHome: t.TempDir(),
			AuthSource: AuthSourceManagedInjection, ProviderRecordRef: gateway.Ref})
		if statusOf(err) != http.StatusUnprocessableEntity || err.Error() != want {
			t.Errorf("bind %s to an openai_compatible record = %v, want 422 %q", driver, err, want)
		}
		_, _, err = m.mintFromProviderRecord(context.Background(), tenant, driver, gateway.Ref)
		var re *runErr
		if !errors.As(err, &re) || re.status != http.StatusUnprocessableEntity || re.msg != want {
			t.Errorf("launch %s on an openai_compatible record = %v, want 422 %q", driver, err, want)
		}
	}
	if _, _, err := m.mintFromProviderRecord(context.Background(), tenant, providerDriverCodex, gateway.Ref); err != nil {
		t.Fatalf("Codex on an openai_compatible record = %v; its own provider configuration takes the endpoint", err)
	}
}

// The resolve rule never picks a record the tool cannot be held to: the person gets the
// tool's "nothing to run on" sentence, not a launch that is then refused.
func TestResolveDoesNotPickARecordTheToolCannotBeHeldTo(t *testing.T) {
	m, tenant, _ := resolveHarness(t)
	addRecord(t, m, tenant, ProviderKindOpenAICompatible, "Gateway", "https://gw.example.test/v1", "sk-compat-0123456789")
	_, err := m.ResolveProfile(context.Background(), tenant, providerDriverClaude)
	var coded *codedRunErr
	if !errors.As(err, &coded) || coded.code != resolveCodeNothingToRunOn {
		t.Fatalf("resolve Claude Code with only an openai_compatible record = %v, want nothing_to_run_on", err)
	}
}

// A Claude key with no base_url is held to Anthropic's own API. On a node that sends Claude
// Code sessions through its deployment gateway, the launch would inherit the gateway; it is
// refused before spawn instead.
func TestClaudeKeyNeverInheritsTheDeploymentGateway(t *testing.T) {
	runner := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(runner), WithCredentialSource(&countingCredentialSource{}),
		WithProviderSecretVault(newFakeVault()), WithInferenceBaseURL("https://gateway.example.com"))
	m.UseExecutionEnvironmentRef(testEnvRef)
	rec := mustCreateRecord(t, m, tenant, anthropicInput("Anthropic"))
	configHome, userHome, _, _ := twoHomes(t)
	prof := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: providerDriverClaude, ConfigHome: configHome, UserHome: userHome,
		DisplayName: "bound", AuthSource: AuthSourceManagedInjection, ProviderRecordRef: rec.Ref})
	_, err := launchBound(m, tenant, prof)
	if statusOf(err) != http.StatusConflict {
		t.Fatalf("Claude key on a gateway node = %v, want 409", err)
	}
	if launchCount(runner) != 0 {
		t.Fatal("a refused launch started a child")
	}
}
