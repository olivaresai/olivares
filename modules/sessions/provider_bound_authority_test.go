// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"testing"
)

// The record mint hands every record-bound launch its non-secret launch authority: the
// record's kind and the one endpoint it may reach (its base_url, else its vendor's API).
func TestRecordMintCarriesTheBoundProvider(t *testing.T) {
	for _, tc := range []struct {
		name, driver string
		in           CreateProviderRecordInput
		models       []string
		want         BoundProvider
	}{
		{"anthropic key", providerDriverClaude, CreateProviderRecordInput{Kind: ProviderKindAnthropic, DisplayName: "A", APIKey: testProviderKey}, nil,
			BoundProvider{ProviderKindAnthropic, "https://api.anthropic.com"}},
		{"anthropic key at its own address", providerDriverClaude, CreateProviderRecordInput{Kind: ProviderKindAnthropic, DisplayName: "A", APIKey: testProviderKey, BaseURL: "https://anthropic-gw.example.com/"}, nil,
			BoundProvider{ProviderKindAnthropic, "https://anthropic-gw.example.com"}},
		{"openai key", providerDriverCodex, CreateProviderRecordInput{Kind: ProviderKindOpenAI, DisplayName: "O", APIKey: testProviderKey}, nil,
			BoundProvider{ProviderKindOpenAI, "https://api.openai.com/v1"}},
		{"xai key", providerDriverGrok, CreateProviderRecordInput{Kind: ProviderKindXAI, DisplayName: "X", APIKey: testProviderKey}, nil,
			BoundProvider{ProviderKindXAI, "https://api.x.ai/v1"}},
		{"openai-compatible endpoint", providerDriverCodex, CreateProviderRecordInput{Kind: ProviderKindOpenAICompatible, DisplayName: "G", APIKey: testProviderKey, BaseURL: "https://llm.example.com/v1"}, nil,
			BoundProvider{ProviderKindOpenAICompatible, "https://llm.example.com/v1"}},
		{"local model", providerDriverOpenCode, CreateProviderRecordInput{Kind: ProviderKindOllama, DisplayName: "L", BaseURL: "http://127.0.0.1:11435"}, []string{"qwen3:8b"},
			BoundProvider{ProviderKindOllama, "http://127.0.0.1:11435/v1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _, tenant, _ := newRuntimeHarness(t, WithProviderSecretVault(newFakeVault()),
				WithProviderProbe(&fakeProbe{result: ProviderProbeResult{Models: tc.models}}))
			rec := mustCreateRecord(t, m, tenant, tc.in)
			if tc.models != nil {
				if _, err := m.TestProviderRecord(context.Background(), tenant, rec.Ref); err != nil {
					t.Fatal(err)
				}
			}
			cred, _, err := m.mintFromProviderRecord(context.Background(), tenant, tc.driver, rec.Ref)
			if err != nil {
				t.Fatalf("mint = %v", err)
			}
			if cred.bound != tc.want {
				t.Fatalf("bound provider = %+v, want %+v", cred.bound, tc.want)
			}
		})
	}
}

// launchCapturingDriver is the OpenCode driver, recording the DriverLaunch it is given.
type launchCapturingDriver struct {
	openCodeDriver
	got *DriverLaunch
}

func (d launchCapturingDriver) LaunchArgs(l DriverLaunch) []string {
	*d.got = l
	return d.openCodeDriver.LaunchArgs(l)
}

// The driver receives the carrier from the launch itself; a launch with no record
// carries none.
func TestTheBoundProviderReachesTheDriver(t *testing.T) {
	var got DriverLaunch
	m, _, tenant, _ := newRuntimeHarness(t, WithProviderDriver(launchCapturingDriver{got: &got}), WithProviderSecretVault(newFakeVault()))
	m.UseExecutionEnvironmentRef(testEnvRef)
	rec := mustCreateRecord(t, m, tenant, anthropicInput("Anthropic"))
	bound := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: providerDriverOpenCode, ConfigHome: t.TempDir(), UserHome: t.TempDir(),
		AuthSource: AuthSourceManagedInjection, ProviderRecordRef: rec.Ref})
	own := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: providerDriverOpenCode, ConfigHome: t.TempDir(), UserHome: t.TempDir(),
		AuthSource: AuthSourceAccountHome})
	for _, tc := range []struct {
		profile ProviderProfile
		want    BoundProvider
	}{
		{bound, BoundProvider{ProviderKindAnthropic, "https://api.anthropic.com"}},
		{own, BoundProvider{}},
	} {
		got = DriverLaunch{}
		p := CreateRunParams{ProviderProfileRef: tc.profile.Ref}
		if err := m.resolveLaunchProfileInto(context.Background(), tenant, &p); err != nil {
			t.Fatal(err)
		}
		cred, env, err := m.mintLaunchAuthority(context.Background(), tenant, "", p)
		if err != nil {
			t.Fatal(err)
		}
		m.buildLaunchSpec(p, cred, WorkSessionCredential{}, CommunicationSessionCredential{}, "", nil, nil, env)
		if got.BoundProvider != tc.want {
			t.Fatalf("the driver received %+v, want %+v", got.BoundProvider, tc.want)
		}
	}
}
