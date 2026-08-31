// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"strings"

	"github.com/olivaresai/olivares/core/api"
)

// loadMetricsConfig reads the metrics access-control settings from the
// environment:
//
//	OLIVARES_METRICS_TOKEN         static bearer token for scrape auth
//	OLIVARES_METRICS_ALLOWED_CIDRS comma-separated CIDRs (direct peer check)
//
// Returns nil when nothing is set (the default: unauthenticated, controlled by
// network-level measures — bind address, NetworkPolicy, firewall).
func loadMetricsConfig(getenv func(string) string) *api.MetricsConfig {
	token := strings.TrimSpace(getenv("OLIVARES_METRICS_TOKEN"))
	var cidrs []string
	if raw := strings.TrimSpace(getenv("OLIVARES_METRICS_ALLOWED_CIDRS")); raw != "" {
		for _, c := range strings.Split(raw, ",") {
			if c = strings.TrimSpace(c); c != "" {
				cidrs = append(cidrs, c)
			}
		}
	}
	if token == "" && len(cidrs) == 0 {
		return nil
	}
	return &api.MetricsConfig{
		Token:        token,
		AllowedCIDRs: cidrs,
	}
}
