// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"strings"

	"github.com/olivaresai/olivares/core/api"
)

// loadAuthZenConfig reads the AuthZEN/access-review surface EXPOSURE controls from
// the environment (composition-root wiring, so the surface is configurable in
// production, not only by embedders):
//
//	OLIVARES_AUTHZEN_DISABLED          true ⇒ the whole /access/v1 surface + discovery 404
//	OLIVARES_AUTHZEN_SEARCH_DISABLED   true ⇒ only the reverse-query searches are off
//	OLIVARES_AUTHZEN_EXPORT_DISABLED   true ⇒ only the sealed access-review export is off
//	OLIVARES_AUTHZEN_ALLOWED_CIDRS     comma-separated CIDRs ⇒ confine to that network
//
// It returns nil when nothing is set (the default: fully enabled, gated only by the
// per-call bearer + authz:read/authz:admin + AAL3 checks).
func loadAuthZenConfig(getenv func(string) string) *api.AuthZenConfig {
	disabled := azEnvTrue(getenv("OLIVARES_AUTHZEN_DISABLED"))
	searchOff := azEnvTrue(getenv("OLIVARES_AUTHZEN_SEARCH_DISABLED"))
	exportOff := azEnvTrue(getenv("OLIVARES_AUTHZEN_EXPORT_DISABLED"))
	var cidrs []string
	if raw := strings.TrimSpace(getenv("OLIVARES_AUTHZEN_ALLOWED_CIDRS")); raw != "" {
		for _, c := range strings.Split(raw, ",") {
			if c = strings.TrimSpace(c); c != "" {
				cidrs = append(cidrs, c)
			}
		}
	}
	if !disabled && !searchOff && !exportOff && len(cidrs) == 0 {
		return nil
	}
	return &api.AuthZenConfig{
		Disabled:       disabled,
		SearchDisabled: searchOff,
		ExportDisabled: exportOff,
		AllowedCIDRs:   cidrs,
	}
}

// azEnvTrue reports whether an env value means "true" (1 / true, case-insensitive).
func azEnvTrue(v string) bool {
	v = strings.TrimSpace(v)
	return v == "1" || strings.EqualFold(v, "true")
}
