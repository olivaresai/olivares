// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise || !addon_ids

package main

import (
	"errors"
	"github.com/olivaresai/olivares/cmd/olivares/internal/mcpgateway"
	"log/slog"
	"net/http"
)

func buildA2AInboundServer(*engine, *mcpgateway.A2AInboundConfig) (http.Handler, error) {
	return nil, errors.New("A2A delegation is a Business capability")
}
func buildA2APushReceiver(*engine, *mcpgateway.A2APushConfig, *slog.Logger) (http.Handler, error) {
	return nil, errors.New("A2A delegation is a Business capability")
}
