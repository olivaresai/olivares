// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"

	"github.com/olivaresai/olivares/cmd/olivares/internal/inferencepep"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/inferenceproxy"
	"github.com/olivaresai/olivares/modules/models"
)

// askPorts connects Ask Olivares (modules/models/ask_turn.go) to the engine: each tool call
// runs through the engine's own routes as the narrowed principal the authenticator derives
// for it, and each assembled prompt meets the tenant's inference-egress data-loss rules
// before it leaves, the same rules and toggle the governed Chat input gate applies. With the
// inference-egress module off no rules can be read, so Ask gets no ports and stays off.
func askPorts(authr *auth.Authenticator, srv *api.Server, proxy *inferenceproxy.Module) models.AskPorts {
	if proxy == nil {
		return models.AskPorts{}
	}
	return models.AskPorts{
		Principal: authr.AskPrincipal,
		Serve:     srv.ServeSession,
		PromptDenied: func(ctx context.Context, tenant model.TenantID, prompt string) ([]string, error) {
			pol, err := proxy.Policy(ctx, tenant)
			if err != nil || !pol.GateDLPRequest || !pol.DLPEnabled() {
				return nil, err
			}
			return pol.DLPDecide(inferencepep.ClassifyText([]string{prompt})), nil
		},
	}
}
