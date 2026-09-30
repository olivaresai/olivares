// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// MCPGatewayService owns the effective source and runtime composition. The API
// supplies the authenticated tenant; a request body cannot choose another one.
type MCPGatewayService interface {
	Get(context.Context, model.TenantID) (auth.MCPGatewaySnapshot, error)
	PutServer(context.Context, auth.Principal, model.TenantID, int64, string, auth.MCPGatewayServerInput) (auth.MCPGatewaySnapshot, error)
	DeleteServer(context.Context, auth.Principal, model.TenantID, int64, string) (auth.MCPGatewaySnapshot, error)
	SetSessionTools(context.Context, auth.Principal, model.TenantID, int64, bool) (auth.MCPGatewaySnapshot, error)
	TestServer(context.Context, auth.Principal, model.TenantID, int64, string) (auth.MCPGatewaySnapshot, error)
}

// Runtime handlers own protocol authentication, including audience binding or
// exact-session purpose/Claim checks. The management API never supplies a bearer
// to an upstream. Nil handlers keep these surfaces unavailable.
type MCPGatewayRuntime interface {
	ServeGatewayHTTP(http.ResponseWriter, *http.Request)
	ServeSessionHTTP(http.ResponseWriter, *http.Request)
}

// Protocol credentials are resolved by the mounted protocol PEP, as the metrics
// endpoint resolves its own scrape bearer. This exception names only exact owned
// leaves with canonical tenant/server IDs; it cannot exempt a management route.
func (s *Server) isMCPProtocolRequest(r *http.Request) bool {
	if s.mcpGatewayRuntime == nil {
		return false
	}
	if r.URL.Path == "/session/mcp" {
		return true
	}
	path := r.URL.Path
	path = strings.TrimPrefix(path, "/.well-known/oauth-protected-resource")
	parts := strings.Split(path, "/")
	if len(parts) != 5 || parts[0] != "" || parts[1] != "mcp" || parts[2] != "gateway" {
		return false
	}
	tenant, err := model.ParseTenantID(parts[3])
	id, idErr := model.ParseID(parts[4])
	return err == nil && idErr == nil && !tenant.IsZero() && tenant != model.SystemTenantID && !id.IsZero() && tenant.String() == parts[3] && id.String() == parts[4]
}
