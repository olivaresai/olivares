//go:build !enterprise || !addon_ids

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Business identity commands that stay in Community as stubs answer --help with their help text and refuse to run.
func TestCommunityIdentityStubsRefuseToRunButAnswerHelp(t *testing.T) {
	for _, argv := range [][]string{
		{"identity", "wif"},
		{"identity", "external-keys"},
		{"identity", "residency"},
		{"governance", "nhi"},
	} {
		name := strings.Join(argv, " ")
		for _, help := range []string{"--help", "-h"} {
			out, err := runCLI(t, append(append([]string{}, argv...), help)...)
			if err != nil || out == "" {
				t.Errorf("`%s %s` must answer with its help text: err=%v, output=%q", name, help, err, out)
			}
		}
		if _, err := runCLI(t, argv...); !errors.Is(err, errIdentityRequiresBusiness) {
			t.Errorf("`%s` must refuse with the Business requirement, got %v", name, err)
		}
	}
}

func TestCommunityDirectoryRosterUnavailable(t *testing.T) {
	for _, kind := range []string{"ldap", "idp", "okta", "entra", "vault", "infisical"} {
		if source, ok := buildInProcSource(kind); ok || source != nil {
			t.Errorf("Community constructs paid directory source %q", kind)
		}
	}
	for _, kind := range []string{"ldap", "idp", "okta", "entra", "keycloak", "pingone", "ping", "forgerock", "vault", "infisical", "spiffe", "claude-console", "claude-wif", "entra-agent", "agentcore", "google-agent", "oasf", "paperclip", "agent365", "foundry-agents", "ai-control-tower", "onepassword", "aws-kms", "gcp-kms", "azure-key-vault", "external-secrets", "sops", "kmip"} {
		t.Run(kind, func(t *testing.T) {
			provider, connector, ok := buildRosterProvider(kind)
			if ok || provider != nil || connector != nil {
				t.Fatalf("Community exposes the %s directory roster; want an unavailable edition seam", kind)
			}
		})
	}
}

func TestCommunityLifecycleActuatorConfigurationRequiresBusiness(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lifecycle.json")
	if err := os.WriteFile(path, []byte(`{"tenants":[{"tenant":"existing-business-tenant","vault_token":"labelled-test-stand-in"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OLIVARES_NHI_ACTUATORS_CONFIG", path)
	prepareCompositionTestBoot(t)
	eng, err := boot(context.Background(), bootConfig{DataDir: t.TempDir(), Engine: "sqlite", Version: "test"})
	if eng != nil {
		eng.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "Business") {
		t.Fatalf("Community active lifecycle config refusal = %v", err)
	}
}

func TestCommunityExternalPluginIgnoresPaidKind(t *testing.T) {
	cfg := sourcesConfig{Sources: []sourceSpec{{Kind: "vault"}}}
	if err := validateIdentitySourcesConfig(cfg); err == nil {
		t.Fatal("first-party vault must require Business")
	}
	cfg.Sources[0].Plugin = &externalPluginSpec{}
	if err := validateIdentitySourcesConfig(cfg); err != nil {
		t.Fatalf("external observation plugin must ignore first-party kind: %v", err)
	}
}

// A directory roster on the Identity plane is Business Identity & Scale, and the
// Community wireRoster is a stub that wires nothing: the config must be refused
// at validation, never accepted and silently unwired. The wiring journey itself
// runs in the Business build's own suite, where the roster wires.
func TestCommunityIdentityRosterRequiresBusiness(t *testing.T) {
	cfg := sourcesConfig{Identity: []identitySpec{{Name: "okta-corp", Kind: "idp", Tenant: "acme"}}}
	err := validateIdentitySourcesConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "Business") {
		t.Fatalf("Community identity roster refusal = %v", err)
	}
}

func TestCommunityStoredDirectoryConfigAllowsOfflineBoot(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "sources.json")
	if err := os.WriteFile(sourcePath, []byte(`{"identity":[{"name":"retained-directory","kind":"vault","tenant":"existing-business-tenant"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	nhiPath := filepath.Join(dir, "lifecycle.json")
	if err := os.WriteFile(nhiPath, []byte(`{"tenants":[{"tenant":"existing-business-tenant","vault_token":"labelled-test-stand-in"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OLIVARES_SOURCES_CONFIG", sourcePath)
	t.Setenv("OLIVARES_NHI_ACTUATORS_CONFIG", nhiPath)
	if _, err := loadSourcesConfig(quietLog()); err != nil {
		t.Fatalf("read-only source config parser blocked retained directory: %v", err)
	}
	prepareCompositionTestBoot(t)
	eng, err := boot(context.Background(), bootConfig{DataDir: filepath.Join(dir, "estate"), Engine: "sqlite", Version: "test", NoIngest: true})
	if err != nil {
		t.Fatalf("offline store must retain unchanged Business config: %v", err)
	}
	defer eng.Close()
}
