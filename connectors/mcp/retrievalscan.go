// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"github.com/olivaresai/olivares/connectors/internal/textscan"
	"github.com/olivaresai/olivares/sdk/model"
)

// ScanRetrievedInjection returns the sorted ids of the instruction-injection
// markers present in retrieved content (e.g. a knowledge-base chunk). It is the
// same textscan.ScanInjection used for MCP metadata posture, exposed
// here so the composition root (cmd/olivares) can run the markers on retrieved
// chunks without importing the internal textscan package directly.
//
// An empty result means no marker matched. The ids are non-sensitive rule names
// (never matched content — docs/SECURITY-HARDENING.md).
func ScanRetrievedInjection(text string) []string {
	return textscan.ScanInjection(text)
}

// RetrievedMarkerSeverity returns the shared severity grade for an injection-marker
// id returned by ScanRetrievedInjection. An unknown id grades Medium (fail-safe:
// a marker that exists but is ungraded must never vanish below the default threshold).
func RetrievedMarkerSeverity(id string) model.Severity {
	return textscan.MarkerSeverity(id)
}
