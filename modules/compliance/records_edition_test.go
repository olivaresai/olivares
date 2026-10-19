// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Legal hold and erasure with key shred are Business capabilities (docs/editions.md): a module
// built without the Business option answers each of their routes 501 business_required after the
// role check, and the Business option keeps the behavior every other test in this package pins.
// Retention, risk and residency stay Community.

type routeLister struct{ routes [][2]string }

func (l *routeLister) Handle(method, pattern string, _ auth.Permission, _ api.ModuleHandler) {
	l.routes = append(l.routes, [2]string{method, pattern})
}

func (l *routeLister) HandleEntity(method, pattern string, p auth.Permission, _ api.EntityRef, h api.ModuleHandler) {
	l.Handle(method, pattern, p, h)
}

// recordsRoutes are the routes of the legal-hold and erasure planes, in route order.
func recordsRoutes(t *testing.T) [][2]string {
	t.Helper()
	var published routeLister
	New().APIRoutes(&published)
	var out [][2]string
	for _, r := range published.routes {
		for _, prefix := range []string{"/holds", "/erasure", "/data-subjects/", "/claude-files"} {
			if strings.HasPrefix(r[1], prefix) {
				out = append(out, r)
				break
			}
		}
	}
	// 6 hold routes, 6 erasure routes, 2 data-subject routes, 2 Claude Files routes.
	if len(out) != 16 {
		t.Fatalf("the module publishes %d legal-hold and erasure routes; want 16 (a refusal never removes a published route): %v", len(out), out)
	}
	return out
}

func errorCodeOf(r resp) string {
	e, _ := r.body["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

func TestCommunityRefusesEveryLegalHoldAndErasureRoute(t *testing.T) {
	h := newCommunityHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adm := h.roleToken(admin, tenant, "a@x.io", "admin")

	for _, route := range recordsRoutes(t) {
		method, pattern := route[0], route[1]
		t.Run(method+" "+pattern, func(t *testing.T) {
			path := s138Base + strings.ReplaceAll(pattern, "{id}", "x-1")
			var body any
			if method == http.MethodPost || method == http.MethodPut {
				body = map[string]any{}
			}
			r := h.do(method, path, adm, body, tenantHdr(tenant))
			if r.code != http.StatusNotImplemented || errorCodeOf(r) != "business_required" {
				t.Fatalf("%s %s = %d %s; want 501 business_required", method, path, r.code, r.raw)
			}
			if !strings.Contains(r.raw, "Business") {
				t.Fatalf("%s %s refusal does not name Business: %s", method, path, r.raw)
			}
		})
	}
}

// The edition answer comes after authentication and the role check, as the recording,
// steering and break-glass refusals do.
func TestCommunityLegalHoldRefusalFollowsTheRoleCheck(t *testing.T) {
	h := newCommunityHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@x.io", "viewer")

	if r := h.do("POST", s138Base+"/holds", viewer, map[string]any{}, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("viewer POST /holds = %d %s; want 403 before the edition answer", r.code, r.raw)
	}
	if r := h.do("GET", s138Base+"/holds", "", nil, tenantHdr(tenant)); r.code != http.StatusUnauthorized {
		t.Fatalf("anonymous GET /holds = %d %s; want 401", r.code, r.raw)
	}
}

// What stays Community: retention schedules, risk and residency answer as before.
func TestCommunityKeepsRetentionRiskAndResidency(t *testing.T) {
	h := newCommunityHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adm := h.roleToken(admin, tenant, "a@x.io", "admin")

	for _, path := range []string{"/retention/classes", "/retention/policies", "/retention/runs", "/risk", "/residency"} {
		if r := h.do("GET", s138Base+path, adm, nil, tenantHdr(tenant)); r.code != http.StatusOK {
			t.Fatalf("Community GET %s = %d %s; want 200", path, r.code, r.raw)
		}
	}
}

// A hold a Business engine stored keeps vetoing destruction when a Community module reads the
// same store: preservation is never silently dropped (docs/editions.md).
func TestCommunityStillHonorsAStoredHold(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adm := h.roleToken(admin, tenant, "a@x.io", "admin")
	h.createHold(adm, tenant, map[string]any{
		"matter_ref": "m-1", "reason": "r", "scope_kind": "subject", "subject_kind": "kb", "subject_ref": "kb-9",
	})

	community := New(WithClock(fixedClock{t: time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)}))
	community.UseData(api.NewModuleData(h.st))
	dec, err := community.CheckHold(context.Background(), tenant, HoldSubject{Kind: "kb", Ref: "kb-9"})
	if err != nil || !dec.Held || len(dec.Holds) != 1 || dec.Holds[0].MatterRef != "m-1" {
		t.Fatalf("Community CheckHold over a stored hold = %+v, %v; want held by m-1", dec, err)
	}
}

// The retention sweep is the destruction path Community keeps. A hold a Business engine stored
// must stop its purge, and the same schedule must purge where no hold is stored, so the veto is
// the cause and not an inert schedule. The hold is written into the store: Community cannot place one.
func TestCommunitySweepStillHonorsAStoredHold(t *testing.T) {
	clock := &movableClock{t: time.Now().UTC()}
	gate := &stubApprovalGate{status: GateStatusApproved, ref: "ap-ret", approvers: []string{"user:approver"}}
	h := newCommunityHarness(t, WithClock(clock), WithApprovalGate(gate))
	admin := h.adminLogin()
	held := h.createOrg(admin, "held")
	free := h.createOrg(admin, "free")

	for _, tenant := range []model.TenantID{held, free} {
		adm := h.roleToken(admin, tenant, "a@"+tenant.String()+".io", "admin")
		h.seedExtRows(tenant, knowledgeMemoryStandInKind, 2, model.Record{"agent_ref": "a-1", "mkey": "k1", "content": "x"})
		if r := h.putPolicy(adm, tenant, "agent.memory", map[string]any{"retention_days": 30, "disposition": "purge", "basis": "test schedule"}); r.code != http.StatusOK {
			t.Fatalf("Community enable purge schedule = %d %s; retention stays Community", r.code, r.raw)
		}
		if tenant == held {
			if r := h.do("POST", s138Base+"/holds", adm, map[string]any{"matter_ref": "c-1", "reason": "r", "scope_kind": "tenant"}, tenantHdr(tenant)); r.code != http.StatusNotImplemented {
				t.Fatalf("Community placed a hold: %d %s", r.code, r.raw)
			}
			h.mutate(held, func(sc store.Scope) error {
				repo, err := sc.Ext(legalHoldKind)
				if err != nil {
					return err
				}
				_, err = repo.Create(context.Background(), model.Record{
					"matter_ref": "case-1", "scope_kind": "tenant", "subject_kind": nil, "subject_ref": nil,
					"reason": "stored by a Business engine", "status": "active", "created_by": "test",
				})
				return err
			})
		}
		clock.advance(60 * 24 * time.Hour)
		sweep := h.sweepNow(adm, tenant)
		wantPurged, wantRows := 2, 0
		if tenant == held {
			wantPurged, wantRows = 0, 2
		}
		if intOf(sweep.body["purged"]) != wantPurged || h.countExtRows(tenant, knowledgeMemoryStandInKind) != wantRows {
			t.Fatalf("Community sweep of %s purged %v and left %d rows; want %d purged and %d rows left: %s",
				tenant, sweep.body["purged"], h.countExtRows(tenant, knowledgeMemoryStandInKind), wantPurged, wantRows, sweep.raw)
		}
	}
}

func TestBusinessKeepsLegalHoldAndErasure(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adm := h.roleToken(admin, tenant, "a@x.io", "admin")

	id := h.createHold(adm, tenant, map[string]any{
		"matter_ref": "m-2", "reason": "r", "scope_kind": "tenant",
	})
	for _, path := range []string{"/holds", "/holds/" + id, "/holds/" + id + "/events", "/erasure", "/claude-files"} {
		if r := h.do("GET", s138Base+path, adm, nil, tenantHdr(tenant)); r.code != http.StatusOK {
			t.Fatalf("Business GET %s = %d %s; want 200", path, r.code, r.raw)
		}
	}
	// A subject nobody asked to erase: the handler answers its own 404, not the edition refusal.
	if r := h.do("GET", s138Base+"/data-subjects/u-7/erasure-status", adm, nil, tenantHdr(tenant)); r.code != http.StatusNotFound || errorCodeOf(r) != "module_error" {
		t.Fatalf("Business erasure-status of an unknown subject = %d %s; want the handler's 404 module_error", r.code, r.raw)
	}
}
