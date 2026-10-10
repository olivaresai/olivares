// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

type nativeServiceVault struct {
	*fakeVault
	opens atomic.Int64
}

func (v *nativeServiceVault) Open(ctx context.Context, tenant model.TenantID, ref string) ([]byte, error) {
	v.opens.Add(1)
	return v.fakeVault.Open(ctx, tenant, ref)
}

func requireAPIOnlyNativeRefusal(t *testing.T, err error) {
	t.Helper()
	var refusal *runErr
	if !errors.As(err, &refusal) || refusal.status != http.StatusUnprocessableEntity ||
		err.Error() != "This provider is for API use; native sessions cannot use it yet." {
		t.Fatalf("native API-only provider refusal = %v, want the 422 API-only sentence", err)
	}
}

func TestServiceProviderRefusesNativeMintBeforeOpeningTheVault(t *testing.T) {
	vault := &nativeServiceVault{fakeVault: newFakeVault()}
	m, _, tenant, _ := newRuntimeHarness(t, WithProviderSecretVault(vault))
	record := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{
		Kind: ProviderKindOpenAICompatible, Service: "deepseek", DisplayName: "API-only",
		APIKey: testProviderKey,
	})
	for _, driver := range []string{providerDriverClaude, providerDriverCodex, providerDriverGrok, providerDriverOpenCode} {
		t.Run(driver, func(t *testing.T) {
			_, env, err := m.mintFromProviderRecord(context.Background(), tenant, driver, record.Ref)
			requireAPIOnlyNativeRefusal(t, err)
			if len(env) != 0 || vault.opens.Load() != 0 {
				t.Fatalf("API-only provider opened the vault or produced a native environment: opens=%d variables=%d", vault.opens.Load(), len(env))
			}
		})
	}
	t.Run("untagged_record_keeps_native_use", func(t *testing.T) {
		openedBefore := vault.opens.Load()
		// FH dcd9162f803's reviewed endpoint-bound mapping no longer admits an
		// arbitrary compatible key in every tool. Keep this untagged control
		// on each driver's supported pair; the API-only refusal above is unchanged.
		for _, tc := range []struct{ driver, kind, baseURL string }{
			{providerDriverClaude, ProviderKindAnthropic, ""},
			{providerDriverCodex, ProviderKindOpenAICompatible, "https://api.deepseek.com"},
			{providerDriverGrok, ProviderKindXAI, ""},
			{providerDriverOpenCode, ProviderKindAnthropic, ""},
		} {
			legacy := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{
				Kind: tc.kind, DisplayName: "Untagged " + tc.driver,
				BaseURL: tc.baseURL, APIKey: testProviderKey,
			})
			_, env, err := m.mintFromProviderRecord(context.Background(), tenant, tc.driver, legacy.Ref)
			if err != nil || len(env) == 0 {
				t.Fatalf("untagged %s provider behavior changed: variables=%d error=%v", tc.driver, len(env), err)
			}
		}
		if opens := vault.opens.Load() - openedBefore; opens != 4 {
			t.Fatalf("untagged native credential resolutions = %d, want 4", opens)
		}
	})
}

func TestServiceProviderRefusesProfileCreateAndBindingChanges(t *testing.T) {
	for _, driver := range []string{providerDriverClaude, providerDriverCodex, providerDriverGrok, providerDriverOpenCode} {
		t.Run(driver, func(t *testing.T) {
			m, _, tenant, _, _ := providerHarness(t)
			m.UseExecutionEnvironmentRef(testEnvRef)
			record := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{
				Kind: ProviderKindOpenAICompatible, Service: "deepseek", DisplayName: "API-only",
				APIKey: testProviderKey,
			})
			configHome, userHome, _, _ := twoHomes(t)
			input := CreateProfileInput{
				Driver: driver, ConfigHome: configHome, UserHome: userHome,
				AuthSource: AuthSourceManagedInjection, ProviderRecordRef: record.Ref,
			}
			ctx := context.Background()
			_, err := m.CreateProfile(ctx, tenant, input)
			requireAPIOnlyNativeRefusal(t, err)
			profiles, _, err := m.ListProfiles(ctx, tenant, "", model.Query{})
			if err != nil || len(profiles) != 0 {
				t.Fatalf("refused registration left a profile: count=%d error=%v", len(profiles), err)
			}
			// The same home remains available after refusal. An ordinary profile
			// can be created, and the refused patch leaves its version and binding.
			input.ProviderRecordRef = ""
			profile := mustCreateProfile(t, m, tenant, input)
			_, err = m.PatchProfile(ctx, tenant, profile.Ref, ProfilePatch{ProviderRecordRef: &record.Ref})
			requireAPIOnlyNativeRefusal(t, err)
			unchanged, err := m.GetProfile(ctx, tenant, profile.Ref)
			if err != nil || unchanged.Version != profile.Version || unchanged.ProviderRecordRef != "" {
				t.Fatalf("refused binding changed the profile: version=%d binding=%q error=%v", unchanged.Version, unchanged.ProviderRecordRef, err)
			}
		})
	}
}
