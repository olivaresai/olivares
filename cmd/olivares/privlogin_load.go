//go:build !enterprise || !addon_ids

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/olivaresai/olivares/core/auth"
)

// A configured protection must never be silently dropped on edition downgrade.
// Offline readers and exports do not load listener configuration.
func loadPIVConfig(getenv func(string) string, _ *slog.Logger) (*auth.PIVConfig, error) {
	path := getenv("OLIVARES_PIV_CONFIG")
	if path == "" {
		return nil, nil
	}
	var config json.RawMessage
	if err := loadOperatorJSONConfig("OLIVARES_PIV_CONFIG", path, &config); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("OLIVARES_PIV_CONFIG=%q requires Business Identity & Scale; this build cannot serve smart-card sign-in", path)
}

func configurePIVTLS(*http.Server, *auth.PIVConfig, bool, *slog.Logger) {}
