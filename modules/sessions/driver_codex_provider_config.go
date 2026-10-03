// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Native CLI tables merge recursively and TOML has no null to clear saved auth.
// Reject that conflicting authority before even the sandbox probe can spawn;
// catalog routing is independently pinned in the native provider overlay.
func checkCodexBoundProviderConfig(ctx context.Context, spec *LaunchSpec) error {
	if spec.BoundProvider.Kind == "" {
		return nil
	}
	var home string
	for _, item := range spec.Env {
		if item.Name == envCodexHome {
			home = item.Value
		}
	}
	unavailable := &runErr{http.StatusConflict, "Codex's saved provider configuration could not be checked; the bound session was not started"}
	if !filepath.IsAbs(home) {
		return unavailable
	}
	switch runtime.GOOS {
	case "linux":
	case "darwin":
		// The native forced preferences outrank CLI flags. Query only presence,
		// never their contents; an opaque managed authority cannot be pinned.
		probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		probe := exec.CommandContext(probeCtx, "/usr/bin/osascript", "-l", "JavaScript", "-e",
			`ObjC.import('CoreFoundation'); ($.CFPreferencesAppValueIsForced($('config_toml_base64'), $('com.openai.codex')) || $.CFPreferencesAppValueIsForced($('requirements_toml_base64'), $('com.openai.codex'))) ? 'present' : 'absent';`)
		probe.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
		probe.WaitDelay = time.Second
		out, err := probe.Output()
		if err != nil || strings.TrimSpace(string(out)) != "absent" {
			return &runErr{http.StatusConflict, "Codex's managed macOS configuration could not be pinned to this provider; the bound session was not started"}
		}
	default:
		return &runErr{http.StatusConflict, "Codex's native configuration cannot be checked on this platform; the bound session was not started"}
	}
	// Native0.160 removes model_providers from project layers. Only system/user
	// config can retain auth; managed config and requirements can override the pin.
	// No auth file, keyring or ignored project config is read.
	paths := []string{"/etc/codex/config.toml", "/etc/codex/managed_config.toml", "/etc/codex/requirements.toml", filepath.Join(home, "config.toml")}
	for _, path := range paths {
		if err := checkCodexBoundProviderFile(spec, path); err != nil {
			return err
		}
	}
	return nil
}

func checkCodexBoundProviderFile(spec *LaunchSpec, path string) error {
	providerID := codexBoundProviderID(spec.BoundProvider)
	unavailable := &runErr{http.StatusConflict, "Codex's saved provider configuration could not be checked; the bound session was not started"}
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return unavailable
	}
	file, err := os.Open(path)
	if err != nil {
		return unavailable
	}
	// Native values are compared in memory, never copied into an error or log.
	// The existing TOML dependency recognizes quoted, dotted and inline keys.
	limited := &io.LimitedReader{R: file, N: 1<<20 + 1}
	var policy map[string]any
	metadata, err := toml.NewDecoder(limited).Decode(&policy)
	_ = file.Close()
	if err != nil || limited.N == 0 {
		return unavailable
	}
	if metadata.IsDefined("model_providers", providerID, "auth") {
		return &runErr{http.StatusConflict, "The saved Codex provider uses auth.command; remove that provider auth before starting a bound session"}
	}
	name := filepath.Base(path)
	if name != "requirements.toml" && name != "managed_config.toml" {
		return nil // Ordinary configuration precedes the native argv pins.
	}
	required := name == "requirements.toml"
	if required && metadata.IsDefined("features") && metadata.IsDefined("feature_requirements") {
		return unavailable // Native treats these spellings as a duplicate field.
	}
	// The launch argv is the single source of expected values. Decode each
	// assignment separately: native CLI overrides allow repeated keys, last wins.
	type pin struct {
		key   toml.Key
		value any
	}
	pins := map[string]pin{}
	for i := 0; i < len(spec.Args); i++ {
		if spec.Args[i] != "-c" && spec.Args[i] != "--config" {
			continue
		}
		i++
		if i == len(spec.Args) {
			return unavailable
		}
		var override map[string]any
		keys, err := toml.Decode(spec.Args[i], &override)
		if err != nil {
			return unavailable
		}
		for _, key := range keys.Keys() {
			value, _ := codexConfigValue(override, key)
			if _, table := value.(map[string]any); !table {
				pins[key.String()] = pin{key, value}
			}
		}
	}
	keys := make([]string, 0, len(pins))
	for key := range pins {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		pin := pins[key]
		policyKey := slices.Clone(pin.key)
		if required && policyKey[0] == "features" && metadata.IsDefined("feature_requirements") {
			policyKey[0] = "feature_requirements"
		}
		value, defined := codexConfigValue(policy, policyKey)
		// A required provider replaces its whole native definition. Missing
		// pinned leaves (including env_key/catalog) cannot inherit the argv pin.
		replaced := required && len(pin.key) > 2 && pin.key[0] == "model_providers" && metadata.IsDefined(pin.key[:2]...)
		if (defined && !reflect.DeepEqual(value, pin.value)) || (replaced && !defined) {
			return codexHostPolicyConflict(path, policyKey.String())
		}
		// Native sandbox requirements use an allowlist, rather than an exact
		// scalar. Check it only when this launch actually pins sandbox_mode.
		if required && key == "sandbox_mode" {
			if allowed, defined := policy["allowed_sandbox_modes"]; defined {
				values, ok := allowed.([]any)
				if !ok || !slices.ContainsFunc(values, func(value any) bool { return reflect.DeepEqual(value, pin.value) }) {
					return codexHostPolicyConflict(path, "allowed_sandbox_modes")
				}
			}
		}
	}
	return nil
}

func codexConfigValue(config map[string]any, key []string) (any, bool) {
	var value any = config
	for _, part := range key {
		table, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		value, ok = table[part]
		if !ok {
			return nil, false
		}
	}
	return value, true
}

func codexHostPolicyConflict(path, key string) error {
	return &runErr{http.StatusConflict, "Codex cannot be kept to its provider's endpoint under this host policy (" + path + ": " + key + "): remove that requirement, or use Codex's own sign-in"}
}
