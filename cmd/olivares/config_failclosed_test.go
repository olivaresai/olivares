// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api/ratelimit"
)

type failClosedConfigLoaderCase struct {
	name    string
	envName string
	load    func() (any, error)
	empty   func(any) bool
}

func TestOperatorConfigLoadersFailClosed(t *testing.T) {
	log := discardLog()
	zero := func(want any) func(any) bool {
		return func(got any) bool { return reflect.DeepEqual(got, want) }
	}
	cases := []failClosedConfigLoaderCase{
		{name: "admin actuator", envName: claudeAdminActuatorConfigEnv, load: func() (any, error) {
			return loadClaudeAdminActuatorConfig(log)
		}, empty: zero(claudeAdminActuatorConfig{})},
		{name: "AgentCore export", envName: agentCoreExportConfigEnv, load: func() (any, error) {
			return loadAgentCoreExportConfig(os.Getenv, log)
		}, empty: zero(agentCoreExportConfig{})},
		{name: "approval bridge", envName: "OLIVARES_APPROVAL_BRIDGE_CONFIG", load: func() (any, error) {
			return loadApprovalBridgeConfig(log)
		}, empty: zero(approvalBridgeConfig{})},
		{name: "Claude Files", envName: "OLIVARES_CLAUDE_FILES_CONFIG", load: func() (any, error) {
			return loadClaudeFilesConfig(log)
		}, empty: zero(claudeFilesConfig{})},
		{name: "hook PEP", envName: "OLIVARES_HOOK_PEP_CONFIG", load: func() (any, error) {
			return loadHookPEPConfig(log)
		}, empty: zero(hookPEPConfig{})},
		{name: "deploy executor", envName: "OLIVARES_DEPLOY_EXECUTOR_CONFIG", load: func() (any, error) {
			return loadDeployExecutorConfig(log)
		}, empty: zero(deployExecutorConfig{})},
		{name: "Claude eraser", envName: "OLIVARES_CLAUDE_ERASER_CONFIG", load: func() (any, error) {
			return loadClaudeEraserConfig(log)
		}, empty: zero(claudeEraserConfig{})},
		{name: "HITL", envName: "OLIVARES_HITL_CONFIG", load: func() (any, error) {
			return loadHITLConfig(log)
		}, empty: zero(hitlConfig{})},
		{name: "inference proxy", envName: "OLIVARES_INFERENCE_PROXY_CONFIG", load: func() (any, error) {
			return loadInferenceProxyConfig(log)
		}, empty: zero(inferenceProxyConfig{})},
		{name: "agent gateway", envName: "OLIVARES_AGENT_GATEWAY_CONFIG", load: func() (any, error) {
			return loadAgentGatewayConfig(log)
		}, empty: zero(agentGatewayConfig{})},
		{name: "NHI actuators", envName: "OLIVARES_NHI_ACTUATORS_CONFIG", load: func() (any, error) {
			return loadNHIActuatorsConfig(log)
		}, empty: zero(nhiActuatorsConfig{})},
		{name: "notifications", envName: "OLIVARES_NOTIFY_CONFIG", load: func() (any, error) {
			return loadNotifyDestinations(log)
		}, empty: zero([]notifyDestinationSpec(nil))},
		{name: "orchestration dispatch", envName: "OLIVARES_ORCH_DISPATCH_CONFIG", load: func() (any, error) {
			return loadOrchDispatchConfig(log)
		}, empty: zero(orchDispatchConfig{})},
		{name: "PIV", envName: "OLIVARES_PIV_CONFIG", load: func() (any, error) {
			return loadPIVConfig(os.Getenv, log)
		}, empty: func(got any) bool { return reflect.ValueOf(got).IsNil() }},
		{name: "rate limit", envName: "OLIVARES_RATELIMIT_CONFIG", load: func() (any, error) {
			return loadRateLimitConfig(os.Getenv, log)
		}, empty: func(got any) bool {
			cfg, ok := got.(*ratelimit.Config)
			return ok && cfg != nil && cfg.Mode == ratelimit.ModeEnforce && len(cfg.Tiers) > 0
		}},
		{name: "sandbox runtime", envName: "OLIVARES_SANDBOX_RUNTIME_CONFIG", load: func() (any, error) {
			return loadSandboxRuntimeConfig(log)
		}, empty: zero(sandboxRuntimeConfig{})},
		{name: "sources", envName: "OLIVARES_SOURCES_CONFIG", load: func() (any, error) {
			return loadSourcesConfig(log)
		}, empty: zero(sourcesConfig{})},
		{name: "voice dispatch", envName: "OLIVARES_VOICE_DISPATCH_CONFIG", load: func() (any, error) {
			return loadVoiceDispatchConfig(log)
		}, empty: zero(voiceDispatchConfig{})},
		{name: "voice call", envName: envVoiceCallConfig, load: func() (any, error) {
			return loadVoiceCallConfig(log)
		}, empty: zero(voiceCallConfig{})},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Run("unset is optional", func(t *testing.T) {
				t.Setenv(tc.envName, "")
				got, err := tc.load()
				if err != nil {
					t.Fatalf("unset config returned an error: %v", err)
				}
				if !tc.empty(got) {
					t.Fatalf("unset config returned non-empty value: %#v", got)
				}
			})

			t.Run("unreadable file fails", func(t *testing.T) {
				t.Setenv(tc.envName, filepath.Join(t.TempDir(), "missing.json"))
				_, err := tc.load()
				if err == nil {
					t.Fatal("configured unreadable file must return an error")
				}
				if !strings.Contains(err.Error(), tc.envName) || !strings.Contains(err.Error(), "cannot be read") {
					t.Fatalf("error is not actionable: %v", err)
				}
			})

			t.Run("invalid JSON fails", func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "invalid.json")
				if err := os.WriteFile(path, []byte("not-json"), 0o600); err != nil {
					t.Fatal(err)
				}
				t.Setenv(tc.envName, path)
				_, err := tc.load()
				if err == nil {
					t.Fatal("configured invalid JSON must return an error")
				}
				if !strings.Contains(err.Error(), tc.envName) || !strings.Contains(err.Error(), "invalid JSON") {
					t.Fatalf("error is not actionable: %v", err)
				}
			})
		})
	}
}

func TestAuditArchiveOperatorConfigFailsClosed(t *testing.T) {
	log := discardLog()
	load := func() (*auditArchiveLoop, error) {
		return newAuditArchiveLoop(loadAuditArchiveConfig(os.Getenv, log), nil, nil, nil, log)
	}

	t.Run("unset is optional", func(t *testing.T) {
		t.Setenv(auditArchiveSinkEnv, "")
		t.Setenv(auditArchiveConfigEnv, "")
		loop, err := load()
		if err != nil || loop != nil {
			t.Fatalf("unset archive config = (%v, %v), want (nil, nil)", loop, err)
		}
	})

	t.Run("unreadable file fails", func(t *testing.T) {
		t.Setenv(auditArchiveSinkEnv, "s3archive")
		t.Setenv(auditArchiveConfigEnv, filepath.Join(t.TempDir(), "missing.json"))
		if _, err := load(); err == nil {
			t.Fatal("configured unreadable archive file must return an error")
		}
	})

	t.Run("invalid JSON fails", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "invalid.json")
		if err := os.WriteFile(path, []byte("not-json"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(auditArchiveSinkEnv, "s3archive")
		t.Setenv(auditArchiveConfigEnv, path)
		if _, err := load(); err == nil {
			t.Fatal("configured invalid archive JSON must return an error")
		}
	})
}

func TestPolicyMaxStalenessConfigFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "unset is optional", value: ""},
		{name: "valid positive duration", value: "72h"},
		{name: "invalid duration fails", value: "not-a-duration", wantErr: true},
		{name: "non-positive duration fails", value: "0s", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			getenv := func(key string) string {
				if key == "OLIVARES_POLICY_MAX_STALENESS" {
					return tc.value
				}
				return ""
			}
			_, err := loadGovernanceOptions(getenv, discardLog())
			if (err != nil) != tc.wantErr {
				t.Fatalf("loadGovernanceOptions(%q) error = %v, wantErr=%t", tc.value, err, tc.wantErr)
			}
			if err != nil && (!strings.Contains(err.Error(), "OLIVARES_POLICY_MAX_STALENESS") || !strings.Contains(err.Error(), "refusing to start")) {
				t.Fatalf("error is not actionable: %v", err)
			}
		})
	}
}

func TestOperatorInlineJSONConfigFailsClosed(t *testing.T) {
	var cfg map[string]any
	if err := loadOperatorInlineJSONConfig("OLIVARES_INLINE_TEST_CONFIG", `{"enabled":true}`, &cfg); err != nil {
		t.Fatalf("valid inline JSON returned an error: %v", err)
	}
	if cfg["enabled"] != true {
		t.Fatalf("valid inline JSON decoded as %#v", cfg)
	}
	if err := loadOperatorInlineJSONConfig("OLIVARES_INLINE_TEST_CONFIG", `{"enabled":`, &cfg); err == nil {
		t.Fatal("configured invalid inline JSON must return an error")
	} else if !strings.Contains(err.Error(), "OLIVARES_INLINE_TEST_CONFIG") || !strings.Contains(err.Error(), "invalid inline JSON") {
		t.Fatalf("error is not actionable: %v", err)
	}
}

func TestBootPropagatesInvalidSourcesConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid-sources.json")
	if err := os.WriteFile(path, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OLIVARES_SOURCES_CONFIG", path)

	eng, err := boot(context.Background(), bootConfig{
		DataDir: t.TempDir(), Engine: "sqlite", DSN: ":memory:", Version: "test", Logger: discardLog(),
	})
	if eng != nil {
		_ = eng.Close()
		t.Fatal("boot returned an engine despite invalid configured sources")
	}
	if err == nil {
		t.Fatal("boot must fail when configured sources contain invalid JSON")
	}
	if !strings.Contains(err.Error(), "load sources operator config") || !strings.Contains(err.Error(), "OLIVARES_SOURCES_CONFIG") {
		t.Fatalf("boot error is not actionable: %v", err)
	}
}

func TestBootPropagatesInvalidAuditSpoolConfig(t *testing.T) {
	t.Setenv(auditSpoolMaxBytesEnv, "12x")

	eng, err := boot(context.Background(), bootConfig{
		DataDir: t.TempDir(), Engine: "sqlite", DSN: ":memory:", Version: "test", Logger: discardLog(),
	})
	if eng != nil {
		_ = eng.Close()
		t.Fatal("boot returned an engine despite invalid configured audit spool budget")
	}
	if err == nil {
		t.Fatal("boot must fail when the configured audit spool budget is invalid")
	}
	if !strings.Contains(err.Error(), "load audit spool operator config") || !strings.Contains(err.Error(), auditSpoolMaxBytesEnv) || !strings.Contains(err.Error(), "refusing to start") {
		t.Fatalf("boot error is not actionable: %v", err)
	}
}

func TestBootPropagatesInvalidPolicyMaxStaleness(t *testing.T) {
	t.Setenv("OLIVARES_POLICY_MAX_STALENESS", "never")

	eng, err := boot(context.Background(), bootConfig{
		DataDir: t.TempDir(), Engine: "sqlite", DSN: ":memory:", Version: "test", Logger: discardLog(),
	})
	if eng != nil {
		_ = eng.Close()
		t.Fatal("boot returned an engine despite invalid policy max staleness")
	}
	if err == nil {
		t.Fatal("boot must fail when configured policy max staleness is invalid")
	}
	if !strings.Contains(err.Error(), "OLIVARES_POLICY_MAX_STALENESS") || !strings.Contains(err.Error(), "refusing to start") {
		t.Fatalf("boot error is not actionable: %v", err)
	}
}
