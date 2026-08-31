// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// TestSetOrgStatusSurface covers the authority around PUT
// /v1/system/orgs/{tenant_id}/status: who may withdraw a tenant's service,
// and what the engine refuses to accept. The ENFORCEMENT that a suspended tenant
// stops being served lives in the store guard and is asserted end-to-end against
// the real composition root in cmd/olivares (suspension_wireproof_test.go) and at
// the store in core/suspension — a route test could never prove it, because the
// guard is wired below this layer.
func TestSetOrgStatusSurface(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	path := "/v1/system/orgs/" + tenant.String() + "/status"

	// RBAC before assurance: an elevated TENANT admin is still forbidden — cutting
	// off a customer is a system decision, never one a tenant makes about itself.
	member := h.mkMember(admin, "tenant-admin@acme.io", "tenantadmin1", auth.RoleAdmin, tenant)
	h.elevate(member)
	if denied := h.do(http.MethodPut, path, member,
		map[string]any{"status": "suspended"}, tenantHdr(tenant)); denied.code != http.StatusForbidden ||
		errCode(denied.body) == "step_up_required" {
		t.Fatalf("tenant admin suspend = %d %s, want RBAC 403", denied.code, denied.raw)
	}

	// AAL3 is deliberately NOT required, and that is a decision, not an omission.
	// Requiring it would make the NON-destructive door harder to reach than the
	// DESTRUCTIVE one — handleDropOrg, the unrecoverable purge, needs system:admin
	// alone — and the safe door must never be the locked one. It would also lock
	// out the caller this exists for: an AAL3 route is human-session-only by
	// construction, and the cloud control plane authenticates with an API key.
	if noStepUp := h.do(http.MethodPut, path, admin,
		map[string]any{"status": "active"}, nil); noStepUp.code != http.StatusOK {
		t.Fatalf("un-elevated superadmin suspend = %d %s, want 200 (no AAL3 gate)", noStepUp.code, noStepUp.raw)
	}
	// The two doors must stay symmetric: if a future change re-gates this one on
	// AAL3, it must gate the destructive delete at least as hard.
	if drop := h.do(http.MethodDelete, "/v1/system/orgs/"+tenant.String(), admin, nil, nil); drop.code == http.StatusForbidden &&
		errCode(drop.body) == "step_up_required" {
		t.Fatal("the destructive delete now needs AAL3 but the reversible withdrawal does not: re-check the asymmetry")
	}
	tenant = h.createOrg(admin, "acme2")
	path = "/v1/system/orgs/" + tenant.String() + "/status"

	// The reserved system tenant is never suspendable: it holds the auth and
	// provisioning partition that is the only way back.
	if sys := h.do(http.MethodPut, "/v1/system/orgs/"+model.SystemTenantID.String()+"/status", admin,
		map[string]any{"status": "suspended"}, nil); sys.code != http.StatusBadRequest {
		t.Fatalf("system-tenant suspend = %d %s, want 400", sys.code, sys.raw)
	}

	// Only the two service states are accepted. "inactive" is a lifecycle state
	// for OTHER entities; an org holding it would be neither served nor suspended.
	for _, bad := range []string{"inactive", "error", "deleted", ""} {
		if r := h.do(http.MethodPut, path, admin, map[string]any{"status": bad}, nil); r.code != http.StatusBadRequest {
			t.Fatalf("status %q = %d %s, want 400", bad, r.code, r.raw)
		}
	}

	// Suspend, and read the decision back off the org.
	ok := h.do(http.MethodPut, path, admin, map[string]any{"status": "suspended"}, nil)
	if ok.code != http.StatusOK || ok.body["tenant_id"] != tenant.String() || ok.body["status"] != "suspended" {
		t.Fatalf("suspend = %d %s", ok.code, ok.raw)
	}
	// The operator can still SEE a suspended tenant — otherwise it could never be
	// restored or, after the grace period, deleted.
	list := h.do(http.MethodGet, "/v1/system/orgs", admin, nil, nil)
	var found bool
	for _, it := range list.body["items"].([]any) {
		if o, _ := it.(map[string]any); o["tenant_id"] == tenant.String() {
			found = true
			if o["status"] != "suspended" {
				t.Fatalf("listed status = %v, want suspended", o["status"])
			}
		}
	}
	if !found {
		t.Fatalf("a suspended tenant vanished from the org list: %s", list.raw)
	}

	// Restoring is the same call with the other value — reversibility is not a
	// separate, privileged path.
	back := h.do(http.MethodPut, path, admin, map[string]any{"status": "active"}, nil)
	if back.code != http.StatusOK || back.body["status"] != "active" {
		t.Fatalf("restore = %d %s", back.code, back.raw)
	}
}

// TestSetOrgStatusContractDoesNotPromiseAAL3 is the copy-versus-code check the
// external contrast asked for. The AAL3 gate on this route was removed on purpose
// (see handleSetOrgStatus and the assertion above) — but the PUBLISHED OpenAPI
// description still announced "AAL3 step-up", so the contract every generated
// client and every integrator reads promised a control the server does not apply.
//
// Published copy that promises a control the code does not enforce is the forbidden
// family, and the direction matters: it is not a doc nicety, it tells an operator a
// step-up protects this route when nothing does.
//
// The check is anchored on a KNOWN POSITIVE in the same document — setOrgRegion,
// whose handler really does call requireAAL3 — so it cannot pass by simply failing
// to find the string anywhere.
func TestSetOrgStatusContractDoesNotPromiseAAL3(t *testing.T) {
	h := newHarness(t)
	res := h.do(http.MethodGet, "/openapi.json", "", nil, nil)
	if res.code != http.StatusOK {
		t.Fatalf("GET /openapi.json = %d", res.code)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
			Summary     string `json:"summary"`
			Description string `json:"description"`
		} `json:"paths"`
	}
	if err := json.Unmarshal([]byte(res.raw), &doc); err != nil {
		t.Fatalf("decode spec: %v", err)
	}
	text := func(opID string) string {
		for _, ops := range doc.Paths {
			for _, op := range ops {
				if op.OperationID == opID {
					return op.Summary + " " + op.Description
				}
			}
		}
		t.Fatalf("operation %q not found in the served spec", opID)
		return ""
	}
	// Calibration: a route that DOES enforce AAL3 still says so. Without this the
	// assertion below would also pass on a spec that had lost the phrase entirely,
	// or on a probe that never matched anything.
	if region := text("setOrgRegion"); !strings.Contains(region, "AAL3") {
		t.Fatalf("calibration failed: setOrgRegion enforces AAL3 but its contract text does not say so (%q) — "+
			"this probe cannot distinguish a fixed setOrgStatus from a spec that lost the phrase", region)
	}
	if status := text("setOrgStatus"); strings.Contains(status, "AAL3") {
		t.Fatalf("the published contract promises a control the handler does not enforce: setOrgStatus says %q, "+
			"but handleSetOrgStatus deliberately does not call requireAAL3", status)
	}
}
