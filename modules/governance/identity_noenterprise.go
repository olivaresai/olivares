//go:build !enterprise || !addon_ids

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"errors"
	"net/http"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
)

var ErrIdentityRequiresBusiness = errors.New("directory rosters and lifecycle management require Business Identity & Scale")

func (*Module) SyncRoster(context.Context, model.TenantID, identitysource.Graph) (RosterReport, error) {
	return RosterReport{}, ErrIdentityRequiresBusiness
}
func (*Module) Sync(context.Context) error { return ErrIdentityRequiresBusiness }
func identityUnavailable(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusNotImplemented, errorBody(ErrIdentityRequiresBusiness.Error()))
}

// handleRosterSync requires Business Identity & Scale to reconcile directory rosters;
// returns HTTP 501 when unavailable.
func (*Module) handleRosterSync(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	identityUnavailable(w, r, mc)
}

// handleListNHI requires Business Identity & Scale to query non-human identity inventory;
// returns HTTP 501 when unavailable.
func (*Module) handleListNHI(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	identityUnavailable(w, r, mc)
}

// handleGetNHI requires Business Identity & Scale to query non-human identity details;
// returns HTTP 501 when unavailable.
func (*Module) handleGetNHI(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	identityUnavailable(w, r, mc)
}

// handleListNHIEvents requires Business Identity & Scale to query non-human identity lifecycle events;
// returns HTTP 501 when unavailable.
func (*Module) handleListNHIEvents(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	identityUnavailable(w, r, mc)
}

// handleSetNHIOwnership requires Business Identity & Scale to assign non-human identity owners;
// returns HTTP 501 when unavailable.
func (*Module) handleSetNHIOwnership(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	identityUnavailable(w, r, mc)
}

// handleSetNHIPolicy requires Business Identity & Scale to configure non-human identity lifecycle policy;
// returns HTTP 501 when unavailable.
func (*Module) handleSetNHIPolicy(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	identityUnavailable(w, r, mc)
}

// handleRotateNHI requires Business Identity & Scale to rotate non-human identity credentials;
// returns HTTP 501 when unavailable.
func (*Module) handleRotateNHI(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	identityUnavailable(w, r, mc)
}

// handleOffboardNHI requires Business Identity & Scale to offboard non-human identities;
// returns HTTP 501 when unavailable.
func (*Module) handleOffboardNHI(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	identityUnavailable(w, r, mc)
}

func (*Module) handleFinalizeNHI(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	identityUnavailable(w, r, mc)
}

// handleRestoreNHI requires Business Identity & Scale to restore offboarded non-human identities;
// returns HTTP 501 when unavailable.
func (*Module) handleRestoreNHI(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	identityUnavailable(w, r, mc)
}

// handleNHISweep requires Business Identity & Scale to run non-human identity lifecycle sweeps;
// returns HTTP 501 when unavailable.
func (*Module) handleNHISweep(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	identityUnavailable(w, r, mc)
}

// handleNHIPosture requires Business Identity & Scale to analyze non-human identity posture;
// returns HTTP 501 when unavailable.
func (*Module) handleNHIPosture(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	identityUnavailable(w, r, mc)
}

// handleRegisterAgent requires Business Identity & Scale to register managed agent identities;
// returns HTTP 501 when unavailable.
func (*Module) handleRegisterAgent(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	identityUnavailable(w, r, mc)
}

// handleWifGraph requires Business Identity & Scale to query workload identity federation graphs;
// returns HTTP 501 when unavailable.
func (*IdentityConsole) handleWifGraph(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	identityUnavailable(w, r, mc)
}

// handleExternalKeys requires Business Identity & Scale to query customer-managed encryption key inventory;
// returns HTTP 501 when unavailable.
func (*IdentityConsole) handleExternalKeys(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	identityUnavailable(w, r, mc)
}

// handleResidency requires Business Identity & Scale to query workspace residency and customer-managed encryption posture;
// returns HTTP 501 when unavailable.
func (*IdentityConsole) handleResidency(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	identityUnavailable(w, r, mc)
}
