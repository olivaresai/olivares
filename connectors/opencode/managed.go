// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package opencode

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/filedrift"
	"github.com/olivaresai/olivares/sdk/model"
)

// managed.go is the fleet half of the authoring surface (authoring.go): checking a
// published managed opencode.json and comparing it with the file a host reports.

// ValidateConfigJSON checks a managed opencode.json document and returns one issue per
// problem (empty means valid). Unknown keys pass, since opencode adds keys often; a known
// key with the wrong shape, an unknown permission action or a malformed MCP entry does not.
func ValidateConfigJSON(content []byte) []string {
	if strings.TrimSpace(string(content)) == "" {
		return []string{"document is empty"}
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(content, &doc); err != nil || doc == nil {
		return []string{"opencode.json must be a JSON object"}
	}
	var issues []string
	if raw, ok := doc["permission"]; ok {
		var perm map[string]json.RawMessage
		if json.Unmarshal(raw, &perm) != nil {
			issues = append(issues, "permission must be an object mapping a tool to ask, allow or deny")
		}
		for _, tool := range sortedKeys(perm) {
			var action string
			if json.Unmarshal(perm[tool], &action) == nil && !knownAction(action) {
				issues = append(issues, "permission."+tool+" must be ask, allow or deny")
			}
		}
	}
	if raw, ok := doc["share"]; ok {
		var share string
		if json.Unmarshal(raw, &share) != nil || (share != "manual" && share != "auto" && share != "disabled") {
			issues = append(issues, "share must be manual, auto or disabled")
		}
	}
	if raw, ok := doc["mcp"]; ok {
		var servers map[string]PolicyMCPServer
		if json.Unmarshal(raw, &servers) != nil {
			issues = append(issues, "mcp must be an object mapping a server name to its definition")
		}
		for _, name := range sortedKeys(servers) {
			issues = append(issues, mcpIssues(name, servers[name])...)
		}
	}
	return issues
}

func mcpIssues(name string, s PolicyMCPServer) []string {
	switch s.Type {
	case "local":
		if len(s.Command) == 0 {
			return []string{"mcp." + name + " is local and needs a command"}
		}
	case "remote":
		if s.URL == "" {
			return []string{"mcp." + name + " is remote and needs a url"}
		}
	default:
		return []string{"mcp." + name + " type must be local or remote"}
	}
	return nil
}

func knownAction(a string) bool { return a == "ask" || a == "allow" || a == "deny" }

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// VerifyDriftJSON compares the published managed opencode.json with the one a host
// reports. OpenCode merges the managed layer over user and project config key by key, so a
// key the host adds is its own; a published key that is absent or different is drift.
func VerifyDriftJSON(scope string, authored, observed []byte, at time.Time) ([]model.FindingReport, error) {
	return filedrift.JSON(filedrift.Spec{Origin: "opencode_managed_config", Tool: "OpenCode"}, scope, authored, observed, at)
}
