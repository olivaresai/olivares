//go:build !enterprise

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// connectionsBusinessRequired returns HTTP 501 with business_required because governed connections require Business; no connection, grant or credential is read or changed.
func connectionsBusinessRequired(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusNotImplemented, api.ErrorBody("business_required", "This capability requires Olivares Business."))
}

func (m *Module) registerConnectionRoutes(reg api.RouteRegistrar) {
	reg.Handle("GET", "/connections", permRunAdmin, connectionsBusinessRequired)
	reg.Handle("PUT", "/connections/{name}", permRunAdmin, connectionsBusinessRequired)
	reg.Handle("DELETE", "/connections/{name}", permRunAdmin, connectionsBusinessRequired)
	reg.Handle("POST", "/connections/{name}/test", permRunAdmin, connectionsBusinessRequired)
	reg.Handle("PUT", "/connections/{name}/grants/{workspace}", permRunAdmin, connectionsBusinessRequired)
	reg.Handle("DELETE", "/connections/{name}/grants/{workspace}", permRunAdmin, connectionsBusinessRequired)
}

// ConnectionTools is the Business surface a session is offered for the connections its
// department holds a grant on. Community has none.
func (m *Module) ConnectionTools(context.Context, auth.Principal, model.TenantID) ([]ConnectionTool, error) {
	return nil, nil
}

// CallConnection serves a call to one of those tools and reports whether it took the
// call. Community takes none, so the session tool endpoint answers as before.
func (m *Module) CallConnection(context.Context, auth.Principal, model.TenantID, string, json.RawMessage) (ConnectionOutcome, bool, error) {
	return ConnectionOutcome{}, false, nil
}
