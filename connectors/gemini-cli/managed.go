// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package geminicli

import (
	"fmt"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/filedrift"
	"github.com/olivaresai/olivares/sdk/model"
)

// managed.go is the fleet half of the Gemini CLI connector: checking a published
// /etc/gemini-cli/settings.json (the system layer, which has the final say) and comparing
// it with the file a host reports.

// ValidateSettingsJSON checks a managed settings.json document and returns one issue per
// problem (empty means valid). It reuses the connector's own decode, so a governed key
// with the wrong type is refused, and unknown keys pass because Gemini CLI adds keys often.
func ValidateSettingsJSON(content []byte) []string {
	body := strings.TrimSpace(string(content))
	if body == "" {
		return []string{"document is empty"}
	}
	if !strings.HasPrefix(body, "{") {
		return []string{"settings.json must be a JSON object"}
	}
	s, err := parseSettings(content)
	if err != nil {
		return []string{"settings.json has a governed key of the wrong type: " + err.Error()}
	}
	var issues []string
	switch getApprovalMode(s) {
	case "", "default", "auto_edit", "plan":
	default:
		issues = append(issues, "general.defaultApprovalMode must be default, auto_edit or plan")
	}
	if s.Telemetry != nil && s.Telemetry.Target != "" && s.Telemetry.Target != "gcp" && s.Telemetry.Target != "local" {
		issues = append(issues, "telemetry.target must be gcp or local")
	}
	return issues
}

// driftSpec: the tool lists are allowlists, so their order is not drift; every other array
// (an MCP server's args) is a sequence and is compared in order. The MCP server set is an
// allowlist too, so a server the host adds is drift.
var driftSpec = filedrift.Spec{
	Origin:      "gemini_cli_managed_config",
	Tool:        "Gemini CLI",
	ExactTables: []string{"mcpServers"},
	SetArrays:   []string{"tools.core", "tools.allowed", "tools.exclude", "mcp.allowed", "mcp.excluded"},
}

// VerifyDriftJSON compares the published managed settings.json with the one a host
// reports. A key the host adds is its own, except a server under mcpServers; a published
// key that is absent or different is drift. The admin{} block is fetched remotely by
// Gemini CLI and is not in either file.
func VerifyDriftJSON(scope string, authored, observed []byte, at time.Time) ([]model.FindingReport, error) {
	if issues := ValidateSettingsJSON(authored); len(issues) > 0 {
		return nil, fmt.Errorf("Gemini CLI: authored document is invalid: %s", issues[0])
	}
	return filedrift.JSON(driftSpec, scope, authored, observed, at)
}
