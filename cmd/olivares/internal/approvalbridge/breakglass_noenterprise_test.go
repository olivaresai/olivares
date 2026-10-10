//go:build !enterprise

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package approvalbridge

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

func TestCommunityBridgeNeverAuthorizesEmergencyGrant(t *testing.T) {
	tenant := model.TenantID("tenant-community-edition")
	cred := ServiceCred{Tenant: tenant, TenantStr: tenant.String(), Token: "synthetic-proposer", ExpiresIn: 3600}
	b := NewWithCreds(map[model.TenantID]ServiceCred{tenant: cred}, time.Now, slog.New(slog.NewTextHandler(io.Discard, nil)))
	emergencyCalls := 0
	// A peer exposes an active grant. Community's bridge must not request or
	// trust that authority, even when its configured handler offers it.
	b.UseHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/m/governance/approvals"):
			io.WriteString(w, `{"items":[],"has_more":false}`)
		case r.URL.Path == "/v1/m/governance/breakglass/consume":
			emergencyCalls++
			io.WriteString(w, `{"granted":true,"grant":"historic-emergency"}`)
		case r.URL.Path == "/v1/m/governance/breakglass/historic-emergency":
			emergencyCalls++
			io.WriteString(w, `{"status":"active"}`)
		default:
			t.Errorf("unexpected peer request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	ctx := context.Background()
	if grant, allowed := b.breakGlassConsumeUnlessRejected(ctx, cred, "deploy.apply", "service", EncodeSubjectRef("api", "plan")); allowed || grant != "" {
		t.Errorf("Community consumed an emergency grant: grant=%q allowed=%v", grant, allowed)
	}
	if b.breakGlassActive(ctx, cred, "historic-emergency") {
		t.Error("Community accepted an active historic emergency grant")
	}
	status, bound, err := b.Status(ctx, tenant, breakGlassRef("historic-emergency", "plan"), "plan")
	if err != nil || status != Expired || bound != "plan" {
		t.Errorf("Community historic reference: status=%q bound=%q err=%v", status, bound, err)
	}
	if emergencyCalls != 0 {
		t.Errorf("Community sent %d emergency authorization requests; want zero", emergencyCalls)
	}
}
