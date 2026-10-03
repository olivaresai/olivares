// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexRegisteredKeyLaunchOverridesTheNativeProvider(t *testing.T) {
	for _, kind := range []string{ProviderKindOpenAI, ProviderKindOpenAICompatible} {
		t.Run(kind, func(t *testing.T) {
			args := strings.Join(NewCodexDriver().LaunchArgs(DriverLaunch{
				BoundProvider: BoundProvider{Kind: kind, Endpoint: "https://bound.example/v1"},
			}), " ")
			for _, term := range []string{`model_provider="olivares_record"`, `base_url="https://bound.example/v1"`, `model_catalog_url="https://bound.example/v1/models"`, `env_key="OPENAI_API_KEY"`, `requires_openai_auth=false`, "features.api_key_model_discovery=false", `cli_auth_credentials_store="ephemeral"`} {
				if !strings.Contains(args, term) {
					t.Fatalf("registered-key launch does not pin %s: %s", term, args)
				}
			}
		})
	}
}

func TestCodexLaunchRefusesInheritedProviderAuthButKeepsOwnLogin(t *testing.T) {
	for _, kind := range []string{ProviderKindOpenAI, ProviderKindOllama} {
		for _, layer := range []string{"home", "cwd", "ancestor"} {
			t.Run(kind+"/"+layer, func(t *testing.T) {
				home, project := t.TempDir(), t.TempDir()
				cwd := filepath.Join(project, "child")
				if err := os.MkdirAll(cwd, 0o700); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(home, "config.toml")
				if layer == "cwd" {
					path = filepath.Join(cwd, "config.toml")
				} else if layer == "ancestor" {
					path = filepath.Join(project, ".codex", "config.toml")
					if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
						t.Fatal(err)
					}
				}
				bound := BoundProvider{Kind: kind, Endpoint: "http://127.0.0.1:11434/v1"}
				config := "model_providers = { '" + codexBoundProviderID(bound) + "' = { auth = { command = 'should-not-run' } } }\n"
				if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
					t.Fatal(err)
				}
				m, p := &Module{}, CreateRunParams{PermissionMode: permModeBypass, ProviderHome: &ProviderHomeSnapshot{Driver: providerDriverCodex}}
				spec := LaunchSpec{BoundProvider: bound, Dir: cwd, Env: []EnvVar{{Name: envCodexHome, Value: home}}}
				var refused *runErr
				err := m.prepareCodexSandbox(t.Context(), &p, &spec)
				if layer == "home" {
					if !errors.As(err, &refused) || refused.status != http.StatusConflict || !strings.Contains(refused.msg, "auth.command") {
						t.Fatalf("inherited auth was not refused before the Full launch: %v", err)
					}
				} else if err != nil {
					// Native project sanitization excludes model_providers even in
					// trusted folders; ignored auth must not block a valid launch.
					t.Fatalf("ignored project provider blocked the launch: %v", err)
				}
				spec.BoundProvider = BoundProvider{}
				if err := m.prepareCodexSandbox(t.Context(), &p, &spec); err != nil {
					t.Fatalf("own-login native config was changed: %v", err)
				}
			})
		}
	}
}

func TestCodexLocalLaunchPinsTheCatalogWithoutIntroducingAuthentication(t *testing.T) {
	args := strings.Join(NewCodexDriver().LaunchArgs(DriverLaunch{
		BoundProvider: BoundProvider{Kind: ProviderKindOllama, Endpoint: "http://127.0.0.1:11434/v1/"},
	}), " ")
	for _, term := range []string{`model_provider="olivares_ollama"`, `model_catalog_url="http://127.0.0.1:11434/v1/models"`, `requires_openai_auth=false`, "features.api_key_model_discovery=false", `cli_auth_credentials_store="ephemeral"`} {
		if !strings.Contains(args, term) {
			t.Fatalf("local launch does not pin %s: %s", term, args)
		}
	}
	if strings.Contains(args, "env_key=") || strings.Contains(args, ",auth=") || strings.Contains(args, "experimental_bearer_token=") {
		t.Fatal("local launch introduced authentication")
	}
}

func TestCodexLaunchDisablesNonessentialTraffic(t *testing.T) {
	args := strings.Join(NewCodexDriver().LaunchArgs(DriverLaunch{}), " ")
	if strings.Contains(args, "features.api_key_model_discovery") || strings.Contains(args, "cli_auth_credentials_store") {
		t.Error("own-login model discovery must retain the native configuration")
	}
	for _, term := range []string{"analytics.enabled=false", "features.plugins=false", "feedback.enabled=false", `otel.exporter="none"`, `otel.trace_exporter="none"`, "check_for_update_on_startup=false"} {
		if !strings.Contains(args, term) {
			t.Errorf("Codex launch does not disable %s: %s", term, args)
		}
	}
}

func TestCodexBoundThreadPinsAndChecksProviderBeforeInput(t *testing.T) {
	for _, resume := range []string{"", "thread-bound"} {
		for _, responseProvider := range []string{"olivares_record", "native_canary", ""} {
			t.Run("resume="+resume+"/response="+responseProvider, func(t *testing.T) {
				peer := newCodexPeer(t, func(cfg *DriverSessionConfig) {
					cfg.BoundProvider = BoundProvider{Kind: ProviderKindOpenAI, Endpoint: "https://bound.example/v1"}
					cfg.ResumeConversationID = resume
				})
				done := make(chan error, 1)
				go func() { _, err := peer.session.Handshake(context.Background()); done <- err }()
				id, _, _ := peer.nextRequest()
				peer.reply(id, map[string]any{})
				id, _, _ = peer.nextRequest()
				peer.reply(id, map[string]any{"account": nil, "requiresOpenaiAuth": false})
				id, method, params := peer.nextRequest()
				wantMethod := codexMethodThreadStart
				if resume != "" {
					wantMethod = codexMethodThreadResume
				}
				if method != wantMethod || params["modelProvider"] != "olivares_record" {
					t.Errorf("bound thread request = %s %v", method, params)
				}
				peer.reply(id, map[string]any{"thread": map[string]any{"id": "thread-bound"}, "modelProvider": responseProvider})
				err := <-done
				if responseProvider == "olivares_record" {
					if err != nil {
						t.Fatal(err)
					}
					return
				}
				if err == nil {
					t.Error("foreign or missing provider was accepted")
				}
				before := len(peer.sentLines())
				if crossed, err := peer.session.Input(context.Background(), "private operator turn"); err == nil || crossed {
					t.Errorf("input crossed after rejected provider: crossed=%v err=%v", crossed, err)
				}
				if len(peer.sentLines()) != before {
					t.Error("a turn was sent after provider mismatch")
				}
			})
		}
	}
}
