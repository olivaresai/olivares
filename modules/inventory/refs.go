// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inventory

import (
	"path"
	"strings"

	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// Resource-kind labels carried on an edge's ResourceKind (the contract,
// §2.1 and §3). They classify what an edge touched so the right core entity is
// materialized.
const (
	rkMCPTool             = "mcp.tool"
	rkMCPServer           = "mcp.server"
	rkMCPResource         = "mcp.resource"
	rkMCPResourceTemplate = "mcp.resource_template"
	rkMCPPrompt           = "mcp.prompt"
	rkClaudeTool          = "claude.tool"
	rkA2AAgent            = "a2a.agent" // remote/peer agent in an A2A edge (AIP-05)
	rkFile                = "file"

	// (C1-C5): Claude Managed Agents control-plane resource kinds. These literal
	// values MUST match what the connectors/claude-managed-agents Source emits (there is no
	// shared import across the Apache/AGPL license boundary; agreement is by value).
	rkCMAVault        = "anthropic.vault"
	rkCMAVaultCred    = "anthropic.vault_credential"
	rkCMAMemoryStore  = "anthropic.memory_store"
	rkCMAEnvironment  = "anthropic.environment"
	rkCMAPermPolicy   = "anthropic.permission_policy"
	rkCMASkill        = "anthropic.skill"
	rkCMAManagedAgent = "anthropic.managed_agent" // a CMA session / managed-agent run (incl. threads)

	// (CUR-3): the Dreams memory-curation job (a governed Resource), an agent's
	// declared built-in/custom tool (the PERMITTED tools[] expansion — a Tool entity)
	// and an agent DEFINITION named by a multi-agent roster grant (an Agent entity).
	// Same by-value contract as above.
	rkCMADream     = "anthropic.dream"
	rkCMAAgentTool = "anthropic.agent_tool"
	rkCMAAgentDef  = "anthropic.agent"
)

// mcpReadOnlyHint maps an mcp.tool edge's mode back to the readOnlyHint to stamp
// on the materialized Tool. read is the only "does not modify" assertion; any
// other mode leaves the hint false (the asymmetric default of an UNTRUSTED
// annotation).
func mcpReadOnlyHint(mode sdkmodel.AccessMode) bool { return mode == sdkmodel.ModeRead }

// splitServerTool splits an MCP "server/leaf" reference (e.g. "github/create_issue"
// or "github/prompt-name") into its server and leaf parts on the first slash. A
// reference with no slash has no server.
func splitServerTool(ref string) (server, leaf string) {
	if i := strings.IndexByte(ref, '/'); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	return "", ref
}

// resourceName derives a short, human display name for a resource from its kind
// and (already redacted) reference: the basename of a file path, else the
// reference itself, else the kind when the reference is empty (e.g. web.search,
// whose query is never stored).
func resourceName(kind, ref string) string {
	if ref == "" {
		return kind
	}
	if kind == rkFile {
		if b := path.Base(ref); b != "" && b != "." && b != "/" {
			return b
		}
	}
	return ref
}

// hostOf returns the host an edge was observed on. The cooperative edges do
// not carry a host (the OTEL origin is a session id; min-data); a runtime/cloud
// connector that does carry one will surface it here when that path lands. For
// now it is unknown.
func hostOf(_ sdkmodel.EdgeObservation) string { return "" }
