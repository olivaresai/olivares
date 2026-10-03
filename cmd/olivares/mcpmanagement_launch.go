// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"net/url"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

// Enabling a tested server is sufficient for session use. The separate legacy
// native-tools flag does not add a second enable step to a managed server.
func sessionMCPAvailable(snapshot auth.MCPGatewaySnapshot) bool {
	if snapshot.SessionTools {
		return true
	}
	for _, row := range snapshot.Servers {
		if row.Enabled {
			return true
		}
	}
	return false
}

func (m *mcpManagement) ConfigureSessionMCP(ctx context.Context, tenant model.TenantID, runRef, driver string, spec *sessions.LaunchSpec) (func(), error) {
	noop := func() {}
	if m.source != "store" || (driver != "claude" && driver != "codex") {
		return noop, nil
	}
	snapshot, err := m.store.Get(ctx, tenant)
	if err != nil {
		return noop, auth.ErrMCPGatewayUnavailable
	}
	if !sessionMCPAvailable(snapshot) {
		return noop, nil
	}
	if m.eng == nil || m.sessionAuthenticator == nil || spec == nil {
		return noop, auth.ErrMCPGatewayUnavailable
	}
	// PEP already supplies the session edge address and its one bearer. Reusing
	// the loopback listener avoids making tool clients trust the public API's
	// self-signed certificate; no extra socket or credential is provisioned.
	var endpoint string
	for _, item := range spec.Env {
		if item.Name == envHookPEPURL {
			endpoint = item.Value
		}
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return noop, auth.ErrMCPGatewayUnavailable
	}
	u.Path, u.RawPath = "/session/mcp", ""
	return sessions.ConfigureSessionMCP(spec, driver, m.eng.dataDir, runRef, u.String(), envHookPEPToken)
}
