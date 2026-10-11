// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/core/dr/opgate"
	"github.com/olivaresai/olivares/core/envconfig"
	"github.com/olivaresai/olivares/core/secure"
)

func cmekUnavailable() error {
	return fmt.Errorf("CMEK requires the Business build; use that build with the customer's key service to run `olivares dr backup` or `olivares keys unseal`. Data recovery needs no active license; Community never replaces a CMEK key")
}
func addCMEKCommands(_ *cobra.Command) {}
func loadWrappedSigningKey(_, _ string) (loadedSigningKey, error) {
	return loadedSigningKey{}, cmekUnavailable()
}
func openCMEKOperatorConfig(_ context.Context, _ []byte) ([]byte, error) {
	return nil, cmekUnavailable()
}
func verifyCMEKEnvelope(_ context.Context, _ *secure.SealedEnvelope, _ string) error {
	return cmekUnavailable()
}

// Refuse before boot or an offline command creates keys, opens the store, or
// stages a backup. The registry supplies the configured file names; plaintext
// files, BYOK and HYOK retain their existing loaders and validation.
func checkCMEKInstall(dataDir string) error {
	for _, name := range []string{envKeyWrap, envAuditWrapped, envCatalogWrapped, envPolicyWrapped} {
		if strings.TrimSpace(envconfig.Get(name)) != "" {
			return cmekUnavailable()
		}
	}
	if strings.TrimSpace(envconfig.Get(envKeyCustody)) == "cmek" {
		return cmekUnavailable()
	}

	if dataDir == "" {
		resolved, err := defaultDataDir()
		if err == nil {
			dataDir = resolved
		}
	}
	// Use this installation's saved activation, without changing global state or
	// creating the directory. Process settings keep their existing precedence.
	overlay := map[string]string{}
	if dataDir != "" {
		if manifest, err := LoadActivationManifest(dataDir); err == nil {
			overlay = manifest.activeOverlay()
		}
	}
	reader := envconfig.Reader{Fallback: func(name string) string { return overlay[name] }}
	// Include configured prefix-family members from the process and saved activation.
	names := append([]string(nil), exactConfigEnvKeys...)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if configEnvKeyMode(name) == configKeyPrefix {
			names = append(names, name)
		}
	}
	for name := range overlay {
		if configEnvKeyMode(name) == configKeyPrefix {
			names = append(names, name)
		}
	}
	for _, name := range names {
		if !strings.HasSuffix(name, "_CONFIG") && name != envCommunicationContentKeyringFile && name != envCommunicationCursorKeyringFile && name != envEventingEgressPolicy {
			continue
		}
		path := cmekConfigPath(name, reader.Get(name))
		if path == "" {
			continue
		}
		probe, ok := probeCMEKConfigFile(path)
		if !ok {
			continue
		}
		if probe.Sealed {
			return cmekUnavailable()
		}
		// These are the two readOperatorConfig callers whose path comes from a
		// plaintext parent config, rather than directly from an environment setting.
		if name == "OLIVARES_PIV_CONFIG" {
			if nested, ok := probeCMEKConfigFile(probe.ClientCAFile); ok && nested.Sealed {
				return cmekUnavailable()
			}
		}
		if name == "OLIVARES_REPORTING_CONFIG" {
			if nested, ok := probeCMEKConfigFile(strings.TrimSpace(probe.BundleSigning.PrivateKeyPath)); ok && nested.Sealed {
				return cmekUnavailable()
			}
		}
	}
	return nil
}

type cmekConfigProbe struct {
	Sealed        bool
	ClientCAFile  string `json:"client_ca_file"`
	BundleSigning struct {
		PrivateKeyPath string `json:"private_key_path"`
	} `json:"bundle_signing"`
}

func probeCMEKConfigFile(path string) (cmekConfigProbe, bool) {
	// Some *_CONFIG settings are booleans or inline JSON. Their existing loaders
	// diagnose ordinary absence/syntax; this probe only recognizes ciphertext.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return cmekConfigProbe{}, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return cmekConfigProbe{}, false
	}
	data, err := io.ReadAll(f)
	if err != nil || !json.Valid(data) {
		return cmekConfigProbe{}, false
	}
	if secure.IsSealedEnvelope(data) {
		return cmekConfigProbe{Sealed: true}, true
	}
	// Each parent field uses the same case-insensitive and duplicate-key rules
	// as its loader, without an unrelated typed field hiding an envelope path.
	var ca struct {
		ClientCAFile string `json:"client_ca_file"`
	}
	var signing struct {
		BundleSigning struct {
			PrivateKeyPath string `json:"private_key_path"`
		} `json:"bundle_signing"`
	}
	_ = json.Unmarshal(data, &ca)
	_ = json.Unmarshal(data, &signing)
	return cmekConfigProbe{ClientCAFile: ca.ClientCAFile, BundleSigning: signing.BundleSigning}, true
}

func describeConfiguredKEK(prefix string) (string, error) {
	switch kind := strings.TrimSpace(envconfig.Get(prefix)); kind {
	case "", secure.ProviderAWS, secure.ProviderGCP, secure.ProviderAzure:
		return configuredKEKDescription(prefix), nil
	default:
		return "", fmt.Errorf("%s=%q unknown (use empty|aws-kms|gcp-kms|azure-kv)", prefix, kind)
	}
}

// This input is already a literal or resolved driver DSN. A SQLite file:
// URI is not another secret-file reference.
func checkCMEKSQLiteStore(dsn string) error {
	target, err := opgate.ResolveSQLiteTarget(dsn)
	if err != nil || target.Memory() {
		return nil
	} // Existing store validation owns these cases.
	return checkCMEKInstall(filepath.Dir(target.CanonicalPath()))
}

func checkCMEKSQLiteStoreRef(ctx context.Context, dsn string) error {
	resolved, err := resolveDSNRef(ctx, "--dsn", dsn, osGetenv)
	if err != nil {
		return nil
	} // The existing caller reports reference errors.
	return checkCMEKSQLiteStore(resolved)
}

func cmekConfigPath(name, value string) string {
	switch name {
	case "OLIVARES_AGENT_GATEWAY_CONFIG", "OLIVARES_APPROVAL_BRIDGE_CONFIG", "OLIVARES_CLAUDE_ADMIN_ACTUATOR_CONFIG", "OLIVARES_CLAUDE_ERASER_CONFIG", "OLIVARES_CLAUDE_FILES_CONFIG", "OLIVARES_CODEX_HOOK_PEP_CONFIG", "OLIVARES_DEPLOY_EXECUTOR_CONFIG", "OLIVARES_GROK_HOOK_PEP_CONFIG", "OLIVARES_HITL_CONFIG", "OLIVARES_HOOK_PEP_CONFIG", "OLIVARES_INFERENCE_PROXY_CONFIG", "OLIVARES_NHI_ACTUATORS_CONFIG", "OLIVARES_NOTIFY_CONFIG", "OLIVARES_ORCH_DISPATCH_CONFIG", "OLIVARES_PIV_CONFIG", "OLIVARES_RATELIMIT_CONFIG", "OLIVARES_SANDBOX_RUNTIME_CONFIG", "OLIVARES_SOURCES_CONFIG", "OLIVARES_VOICE_CALL_CONFIG", "OLIVARES_VOICE_DISPATCH_CONFIG", "OLIVARES_COMMUNICATION_CONTENT_KEYRING_FILE", "OLIVARES_COMMUNICATION_CURSOR_KEYRING_FILE":
		return value
	default:
		return strings.TrimSpace(value)
	}
}
