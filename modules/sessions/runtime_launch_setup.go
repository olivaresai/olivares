// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

const sessionLaunchSetupTimeout = 5 * time.Second

// Setup diagnostics name a fixed stage, never a lower-trust error that has seen
// injected credentials. A registry outage cannot hold the create request open.
func (m *Module) prepareSessionLaunch(ctx, runCtx context.Context, tenant model.TenantID, runRef string, p *CreateRunParams, spec *LaunchSpec) (string, error) {
	if err := m.prepareCodexSandbox(ctx, p, spec); err != nil {
		return "Codex sandbox check", err
	}
	if err := m.prepareClaudeHooks(spec, *p, runRef); err != nil {
		return "session hook setup", err
	}
	if m.rt.sessionMCP == nil {
		return "", nil
	}
	setupCtx, cancel := context.WithTimeout(ctx, sessionLaunchSetupTimeout)
	defer cancel()
	cleanup, err := m.rt.sessionMCP.ConfigureSessionMCP(setupCtx, tenant, runRef, launchDriverKey(*p), spec)
	if cleanup != nil {
		context.AfterFunc(runCtx, cleanup)
	}
	return "session MCP setup", err
}
