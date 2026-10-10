// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Gemini refreshes a saved auth method before ACP initialize. A bound key must
// not first authenticate as a Google account, Vertex or a gateway. Refuse those
// conflicting settings before spawning; the handshake then selects only the
// native gemini-api-key method. No credential file is read or altered.
func checkGeminiBoundProviderConfig(spec *LaunchSpec) error {
	if spec.BoundProvider.Kind != ProviderKindGemini {
		return nil
	}
	// Existing explicit credentials remain valid for legacy unbound launches.
	// A bound Google identity may receive only its own key and runtime controls.
	for _, name := range spec.EnvAllow {
		if strings.HasPrefix(name, "GEMINI_") || strings.HasPrefix(name, "GOOGLE_") {
			return conflictErr("a bound Gemini provider cannot inherit Google credentials or routing from env_allow")
		}
	}
	for _, item := range spec.Env {
		if strings.HasPrefix(item.Name, "GOOGLE_") {
			return conflictErr("a bound Gemini provider cannot accept another Google credential or route")
		}
		if strings.HasPrefix(item.Name, "GEMINI_") {
			switch item.Name {
			case "GEMINI_API_KEY", "GEMINI_CLI_HOME", envGeminiNoRelaunch:
			case "GEMINI_TELEMETRY_ENABLED", "GEMINI_TELEMETRY_LOG_PROMPTS":
				if item.Value != "false" {
					return conflictErr("a bound Gemini provider must disable vendor telemetry")
				}
			default:
				return conflictErr("a bound Gemini provider cannot accept another Gemini credential or route")
			}
		}
	}
	var parent string
	for _, item := range spec.Env {
		if item.Name == "GEMINI_CLI_HOME" {
			parent = item.Value
		}
	}
	if !filepath.IsAbs(parent) {
		return conflictErr("Gemini CLI's API-key profile has no selected configuration home")
	}
	paths := []string{"/etc/gemini-cli/system-defaults.json", "/etc/gemini-cli/settings.json", filepath.Join(parent, ".gemini", "settings.json")}
	if spec.Dir != "" {
		paths = append(paths, filepath.Join(spec.Dir, ".gemini", "settings.json"))
	}
	for _, path := range paths {
		if err := geminiKeySettingsRefusal(path); err != nil {
			return err
		}
	}
	return nil
}

func geminiKeySettingsRefusal(path string) error {
	unavailable := conflictErr("Gemini CLI's authentication settings could not be checked; the API-key session did not start")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > geminiSettingsMaxBytes {
		return unavailable
	}
	f, err := os.Open(path)
	if err != nil {
		return unavailable
	}
	raw, err := io.ReadAll(io.LimitReader(f, geminiSettingsMaxBytes+1))
	closeErr := f.Close()
	if err != nil || closeErr != nil || len(raw) > geminiSettingsMaxBytes {
		return unavailable
	}
	object, err := geminiJSONObject(raw)
	if err != nil {
		return unavailable
	}
	selected, err := geminiJSONLookup(object, "security", "auth", "selectedType")
	if err != nil {
		return unavailable
	}
	var method string
	if len(selected) != 0 && json.Unmarshal(selected, &method) != nil {
		return unavailable
	}
	external, err := geminiJSONLookup(object, "security", "auth", "useExternal")
	if err != nil {
		return unavailable
	}
	if method != "" && method != "gemini-api-key" || geminiJSTruthy(external) {
		return conflictErr("Gemini CLI's saved settings select another authentication method; use a separate API-key profile or select gemini-api-key in its native settings before starting this bound session")
	}
	return nil
}
