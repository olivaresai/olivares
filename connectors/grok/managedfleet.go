// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package grok

import (
	"fmt"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/olivaresai/olivares/connectors/internal/filedrift"
	"github.com/olivaresai/olivares/sdk/model"
)

// managedfleet.go is the fleet half of the requirements.toml authoring in
// managedconfig.go: checking a published /etc/grok/requirements.toml and comparing it with
// the file a host reports.

type requirementsDoc struct {
	Sandbox    *struct{ Profile string } `toml:"sandbox"`
	MCPServers map[string]any            `toml:"mcp_servers"`
}

// ValidateRequirementsTOML checks a requirements.toml document and returns one issue per
// problem (empty means valid). A sandbox profile outside the documented set is refused:
// guessing what a new name grants is how a guarantee the agent does not give gets published.
func ValidateRequirementsTOML(content []byte) []string {
	if strings.TrimSpace(string(content)) == "" {
		return []string{"document is empty"}
	}
	var doc requirementsDoc
	md, err := toml.Decode(string(content), &doc)
	if err != nil {
		return []string{"requirements.toml is not valid TOML: " + err.Error()}
	}
	if doc.Sandbox == nil && !md.IsDefined("mcp_servers") {
		return []string{"the document authors no requirements (set [sandbox] profile or [mcp_servers])"}
	}
	var issues []string
	if doc.Sandbox != nil && doc.Sandbox.Profile != "" {
		if _, ok := perfiles[doc.Sandbox.Profile]; !ok {
			issues = append(issues, fmt.Sprintf("sandbox.profile %q is not one of %s", doc.Sandbox.Profile, strings.Join(perfilesConocidos(), ", ")))
		}
	}
	return issues
}

// VerifyDriftTOML compares the published requirements.toml with the one a host reports.
// Grok clamps the system file as its highest layer, so a published key that is absent or
// different on the host is drift; an extra MCP server the host lists is drift too, because
// the allowlist is the set of names.
func VerifyDriftTOML(scope string, authored, observed []byte, at time.Time) ([]model.FindingReport, error) {
	if issues := ValidateRequirementsTOML(authored); len(issues) > 0 {
		return nil, fmt.Errorf("Grok Build: authored document is invalid: %s", issues[0])
	}
	return filedrift.TOML(filedrift.Spec{Origin: "grok_managed_config", Tool: "Grok Build", ExactTables: []string{"mcp_servers"}}, scope, authored, observed, at)
}
