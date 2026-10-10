// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"log/slog"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
)

// mcpcontentgate.go is the AGPL composition-root glue for the MCP content
// inspection seams: the render inspector (handleUIRead HTML content) and the
// elicitation/sampling mediator (runtime PEP). It parallels
// contentinspectorgate.go (the inference-proxy firewall glue).
//
// The default AGPL build injects nil for both seams (wire_noenterprise.go),
// so the RS keeps its prior behavior — the render-gate, consent, and
// deny-closed inventory work, but there is no deep content inspection (no
// rug-pull). Under -tags enterprise with a firewall config,
// wire_enterprise.go injects the real inspectors from
// enterprise/contentfirewall.
//
// HONESTY: verified-deployed inspection AT THE RS PEP only. A client that
// does not transit the RS evades it (the documented limitation, rs.go).

// The render inspector and the elicitation mediator are edition ports
// (mcpRenderInspector, mcpElicitationMediator): -tags enterprise wires the
// commercial ones; the default build has neither (no inspection, no mediation,
// no rug-pull).

// mcpContentGateLog emits a one-time startup message when the MCP content
// gates are wired, for operator visibility.
func mcpContentGateLog(log *slog.Logger, ri mcpc.RenderInspector, em mcpc.ElicitationMediator) {
	if ri != nil {
		log.Info("mcp-gateway: render content inspector wired (Business: AI Runtime Security)")
	}
	if em != nil {
		log.Info("mcp-gateway: elicitation/sampling mediator wired (Business: AI Runtime Security)")
	}
}
