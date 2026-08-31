// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"net/http"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/core/secret"
)

const effectiveConfigRedactedValue = "<redacted>"

// handleEffectiveConfig returns the live, redacted composition-root projection
// of the production config registry. It is a secretless operational read, so it
// needs system:admin but not AAL3.
func (s *Server) handleEffectiveConfig(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authzSystem(w, r, "system:admin"); !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.effectiveConfigProjection())
}

func (s *Server) effectiveConfigProjection() effectiveConfigResponse {
	entries := []EffectiveConfigEntry{}
	if s.effectiveConfig != nil {
		entries = append(entries, s.effectiveConfig()...)
	}
	for i := range entries {
		entries[i] = sanitizeEffectiveConfigEntry(entries[i])
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Key == entries[j].Key {
			return entries[i].Source < entries[j].Source
		}
		return entries[i].Key < entries[j].Key
	})

	violations := []string{}
	if s.effectiveConfigViolations != nil {
		violations = sortedUniqueStrings(s.effectiveConfigViolations())
	}
	return effectiveConfigResponse{
		Entries: entries, StrictViolations: violations,
	}
}

// sanitizeEffectiveConfigEntry is a transport-boundary backstop. The
// composition root already uses the CLI registry's exact redaction path, but a
// malformed embedder callback must not turn Redacted=true into a disclosure.
// The key/value checks also cover the registry's canonical secret cases if a
// callback accidentally omits the marker.
func sanitizeEffectiveConfigEntry(entry EffectiveConfigEntry) EffectiveConfigEntry {
	entry.Key = strings.TrimSpace(entry.Key)
	if entry.Source != "activation" {
		entry.Source = "env"
	}
	if entry.Redacted || effectiveConfigKeyRequiresRedaction(entry.Key, entry.Value) {
		entry.Value = effectiveConfigRedactedValue
		entry.Redacted = true
	}
	return entry
}

func effectiveConfigKeyRequiresRedaction(key, value string) bool {
	if secret.IsCredentialBearingConfigKey(key) || secret.ContainsInlineCredential(value) {
		return true
	}
	for _, part := range strings.Split(strings.ToUpper(key), "_") {
		switch part {
		case "KEY", "TOKEN", "SECRET", "PASSPHRASE", "PEM":
			return true
		}
	}
	return false
}

func sortedUniqueStrings(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			set[value] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

// handleUpdateCheck runs an explicit check-now against the configured signed
// update channel. Air-gapped/unconfigured deployments return an honest 501.
func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authzSystem(w, r, "system:admin"); !ok {
		return
	}
	if s.updateRefresh == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{
			"error": "update checking not configured",
		})
		return
	}
	writeJSON(w, http.StatusOK, s.updateRefresh(r.Context()))
}
