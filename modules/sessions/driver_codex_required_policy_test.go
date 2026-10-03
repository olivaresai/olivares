// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexBoundLaunchRefusesConflictingHostRequirements(t *testing.T) {
	for _, kind := range []string{ProviderKindOpenAI, ProviderKindOllama} {
		bound := BoundProvider{Kind: kind, Endpoint: "https://bound.example/v1"}
		provider := codexBoundProviderID(bound)
		for _, tc := range []struct{ name, policy, key string }{
			{"credential-store", `cli_auth_credentials_store="file"`, "cli_auth_credentials_store"},
			{"provider", `model_provider="other"`, "model_provider"},
			{"endpoint", "[model_providers." + provider + "]\nname='policy'\nbase_url='https://foreign.example/v1'", "model_providers." + provider},
			{"update", "check_for_update_on_startup=true", "check_for_update_on_startup"},
			{"feedback", "[feedback]\nenabled=true", "feedback.enabled"},
			{"analytics", "[analytics]\nenabled=true", "analytics.enabled"},
			{"plugins", "[features]\nplugins=true", "features.plugins"},
			{"discovery", "[features]\napi_key_model_discovery=true", "features.api_key_model_discovery"},
			{"feature-alias", "[feature_requirements]\nplugins=true", "feature_requirements.plugins"},
			{"telemetry", "[otel]\nexporter='otlp-http'", "otel.exporter"},
			{"traces", "[otel]\ntrace_exporter='otlp-http'", "otel.trace_exporter"},
			{"sandbox", "allowed_sandbox_modes=['read-only']", "allowed_sandbox_modes"},
		} {
			for _, layer := range []string{"requirements.toml", "managed_config.toml"} {
				if layer == "managed_config.toml" && (tc.name == "feature-alias" || tc.name == "sandbox") {
					continue // These are native requirements shapes.
				}
				t.Run(kind+"/"+layer+"/"+tc.name, func(t *testing.T) {
					path := filepath.Join(t.TempDir(), layer)
					if err := os.WriteFile(path, []byte(tc.policy), 0o600); err != nil {
						t.Fatal(err)
					}
					spec := LaunchSpec{BoundProvider: bound, Args: NewCodexDriver().LaunchArgs(DriverLaunch{BoundProvider: bound, Preset: PresetFull})}
					var refused *runErr
					err := checkCodexBoundProviderFile(&spec, path)
					if !errors.As(err, &refused) || refused.status != http.StatusConflict {
						t.Fatalf("conflicting required %s reached the launch: %v", tc.name, err)
					}
					if !strings.Contains(refused.msg, "under this host policy ("+path+": "+tc.key) || !strings.HasSuffix(refused.msg, "): remove that requirement, or use Codex's own sign-in") {
						t.Fatalf("refusal did not identify the native policy and recovery: %q", refused.msg)
					}
					if strings.Contains(refused.msg, "foreign.example") || strings.Contains(refused.msg, "otlp-http") {
						t.Fatal("refusal exposed a saved configuration value")
					}
				})
			}
		}
	}
}

func TestCodexBoundLaunchAllowsUnrelatedAndMatchingHostRequirements(t *testing.T) {
	bound := BoundProvider{Kind: ProviderKindOpenAI, Endpoint: "https://bound.example/v1"}
	for _, layer := range []string{"requirements.toml", "managed_config.toml"} {
		for _, tc := range []struct{ name, policy string }{
			{"unrelated", "allow_login_shell=false"},
			{"unrelated-provider", "[model_providers.other]\nname='Other'\nbase_url='https://foreign.example/v1'"},
			{"matching-provider", "model_provider='olivares_record'\n[model_providers.olivares_record]\nbase_url='https://bound.example/v1'\nname='Olivares record'\nwire_api='responses'\nrequires_openai_auth=false\nmodel_catalog_url='https://bound.example/v1/models'\nenv_key='OPENAI_API_KEY'"},
			{"matching", "cli_auth_credentials_store='ephemeral'\ncheck_for_update_on_startup=false\n[feedback]\nenabled=false\n[features]\nplugins=false\napi_key_model_discovery=false"},
			{"empty-feedback", "[feedback]"},
		} {
			t.Run(layer+"/"+tc.name, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), layer)
				if err := os.WriteFile(path, []byte(tc.policy), 0o600); err != nil {
					t.Fatal(err)
				}
				spec := LaunchSpec{BoundProvider: bound, Args: NewCodexDriver().LaunchArgs(DriverLaunch{BoundProvider: bound})}
				if err := checkCodexBoundProviderFile(&spec, path); err != nil {
					t.Fatalf("compatible host policy blocked the launch: %v", err)
				}
			})
		}
	}
}

func TestCodexRequiredProviderCannotDiscardPinnedLeaves(t *testing.T) {
	for _, kind := range []string{ProviderKindOpenAI, ProviderKindOllama} {
		bound := BoundProvider{Kind: kind, Endpoint: "https://bound.example/v1"}
		provider := codexBoundProviderID(bound)
		fields := []string{"name='Olivares record'", "base_url='https://bound.example/v1'", "wire_api='responses'", "requires_openai_auth=false", "model_catalog_url='https://bound.example/v1/models'"}
		if kind == ProviderKindOpenAI {
			fields = append(fields, "env_key='OPENAI_API_KEY'")
		}
		for omitted, field := range fields {
			t.Run(kind+"/omit-"+strings.SplitN(field, "=", 2)[0], func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "requirements.toml")
				remaining := append(append([]string{}, fields[:omitted]...), fields[omitted+1:]...)
				policy := "[model_providers." + provider + "]\n" + strings.Join(remaining, "\n")
				if err := os.WriteFile(path, []byte(policy), 0o600); err != nil {
					t.Fatal(err)
				}
				spec := LaunchSpec{BoundProvider: bound, Args: NewCodexDriver().LaunchArgs(DriverLaunch{BoundProvider: bound})}
				var refused *runErr
				if err := checkCodexBoundProviderFile(&spec, path); !errors.As(err, &refused) || refused.status != http.StatusConflict {
					t.Fatalf("required provider replacement discarded %s: %v", field, err)
				}
			})
		}
	}
}

func TestCodexBoundPolicyReadsTheActualLaunchPin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requirements.toml")
	if err := os.WriteFile(path, []byte("feedback.enabled=true"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A different effective argv pin must drive the same guard; there is no
	// independent copy of the driver's expected privacy values.
	spec := LaunchSpec{BoundProvider: BoundProvider{Kind: ProviderKindOpenAI}, Args: []string{"-c", "feedback.enabled=true"}}
	if err := checkCodexBoundProviderFile(&spec, path); err != nil {
		t.Fatal(err)
	}
	spec.Args = append(spec.Args, "-c", "feedback.enabled=false")
	if err := checkCodexBoundProviderFile(&spec, path); err == nil {
		t.Fatal("the final native argv pin was ignored")
	}
}
