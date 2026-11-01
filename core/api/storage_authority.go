// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"net/http"
	"regexp"
	"time"

	"github.com/olivaresai/olivares/core/auth"
)

var storageIntentHex = regexp.MustCompile(`^[0-9a-f]{64}$`)
var storageDriveTarget = regexp.MustCompile(`^drive:[0-9a-f]{64}$`)

// handleAuthorizeStorage asks the existing authorizer about one confirmed
// storage intent. The caller supplies no identity, permission or helper audience.
func (s *Server) handleAuthorizeStorage(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	var input struct {
		Task         string `json:"task"`
		Target       string `json:"target"`
		IntentDigest string `json:"intent_digest"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		s.badRequest(w, r, "invalid storage intent")
		return
	}
	if (input.Task != "storage.backup" && input.Task != "storage.grow") || !storageDriveTarget.MatchString(input.Target) || !storageIntentHex.MatchString(input.IntentDigest) {
		s.badRequest(w, r, "invalid storage intent")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	// This is an installation-wide action, using the same current system
	// authorizer as its outer route. Tenant evidence cannot represent the
	// reserved system scope. Ask policy again for the exact disk and intent.
	current := mc.Principal
	decision := s.authz.Authorize(ctx, auth.Request{Principal: current, Tenant: mc.Tenant, Permission: auth.PermSystemAdmin, Resource: auth.ResourceAttrs{Kind: "appliance.task", ID: input.Task, Extra: map[string]string{"target": input.Target, "intent_digest": input.IntentDigest, "act": "storage", "audience": "appliance-helper:olivares-portal-storage"}}})
	until := time.Now().Add(time.Second)
	allowed := current.Kind == auth.KindUser && decision.Allow && ctx.Err() == nil && auth.StepUpSatisfied(r.Context(), current)
	writeJSON(w, http.StatusOK, struct {
		Allowed    bool      `json:"allowed"`
		Actor      string    `json:"actor"`
		ValidUntil time.Time `json:"valid_until"`
	}{allowed, current.Actor(), until})
}
