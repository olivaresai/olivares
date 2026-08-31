// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cfmcpportals

import (
	"sort"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// detectShadowMCPs diffs the discovered server names against the approved set
// and returns one high-severity finding per unmanaged server. The findings are
// sorted by name for deterministic, golden-testable output.
func detectShadowMCPs(discovered []mcpServer, approved map[string]struct{}, accountID string, at time.Time) []model.FindingReport {
	if len(approved) == 0 {
		return nil
	}
	var shadows []mcpServer
	for _, s := range discovered {
		name := s.nameRef()
		if name == "" {
			continue
		}
		if _, ok := approved[name]; !ok {
			shadows = append(shadows, s)
		}
	}
	sort.Slice(shadows, func(i, j int) bool { return shadows[i].nameRef() < shadows[j].nameRef() })

	findings := make([]model.FindingReport, 0, len(shadows))
	for _, s := range shadows {
		findings = append(findings, model.FindingReport{
			Kind:        "shadow_mcp",
			Severity:    model.SeverityHigh,
			SubjectKind: resMCPServer,
			SubjectRef:  redact.Clean(s.nameRef()),
			Title:       "Unmanaged MCP server detected in Cloudflare One: " + redact.Clean(s.nameRef()),
			DetailHash:  redact.Hash(s.nameRef() + "|" + s.URL),
			OccurredAt:  at,
		})
	}
	return findings
}
