// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package recording_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/recording"
)

// Privileged-session recording is a Business capability (docs/editions.md): a module built
// without the Business edition answers every one of its routes 501 business_required and
// records nothing, and the Business edition keeps the behavior the tests above pin.

type routeLister struct{ routes [][2]string }

func (l *routeLister) Handle(method, pattern string, _ auth.Permission, _ api.ModuleHandler) {
	l.routes = append(l.routes, [2]string{method, pattern})
}

func (l *routeLister) HandleEntity(method, pattern string, p auth.Permission, _ api.EntityRef, h api.ModuleHandler) {
	l.Handle(method, pattern, p, h)
}

func TestCommunityRefusesEveryRecordingRoute(t *testing.T) {
	var published routeLister
	recording.New().APIRoutes(&published)
	if len(published.routes) != 13 {
		t.Fatalf("the module publishes %d routes; want all 13 kept (a refusal never removes a published route)", len(published.routes))
	}

	h := newCommunityHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, op := h.roleUser(admin, tenant, "op@x.io", "admin")

	for _, route := range published.routes {
		method, pattern := route[0], route[1]
		t.Run(method+" "+pattern, func(t *testing.T) {
			path := "/v1/m/recording" + strings.ReplaceAll(pattern, "{id}", "rs-1")
			var body any
			if method == http.MethodPut || method == http.MethodPost {
				body = map[string]any{}
			}
			r := h.do(method, path, op, body, tenantHdr(tenant))
			if r.code != http.StatusNotImplemented || errorCode(r) != "business_required" {
				t.Fatalf("%s %s = %d %s; want 501 business_required", method, path, r.code, r.raw)
			}
			if !strings.Contains(r.raw, "Business") {
				t.Fatalf("%s %s refusal does not name Business: %s", method, path, r.raw)
			}
		})
	}
}

func TestCommunityRecordsNothing(t *testing.T) {
	h := newCommunityHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, op := h.roleUser(admin, tenant, "op@x.io", "admin")

	// A surface Business records by default, and the break-glass route that is recorded for everyone.
	for _, path := range []string{"/v1/m/governance/things", "/v1/m/governance/breakglass/consume"} {
		method := http.MethodGet
		if strings.HasSuffix(path, "consume") {
			method = http.MethodPost
		}
		if r := h.do(method, path, op, nil, tenantHdr(tenant)); r.code != http.StatusOK {
			t.Fatalf("%s %s = %d %s; Community must not gate, deny or record it", method, path, r.code, r.raw)
		}
	}

	// Neither a configured surface nor the break-glass floor is gated or recorded.
	for _, call := range []api.RecordedCall{
		{Namespace: "governance", Method: http.MethodGet, Pattern: "/things", Tenant: model.TenantID(tenant.String())},
		{Namespace: "governance", Method: http.MethodPost, Pattern: "/breakglass/consume", Permission: "governance:breakglass:write", Tenant: model.TenantID(tenant.String())},
	} {
		dec, err := h.rec.Gate(context.Background(), call)
		if err != nil || dec.Record {
			t.Fatalf("Community Gate(%s %s) = %+v, %v; want no recording and no denial", call.Method, call.Pattern, dec, err)
		}
	}

	var stored int
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("recording.session")
		if err != nil {
			return err
		}
		recs, _, err := repo.List(context.Background(), model.Query{Limit: 10})
		stored = len(recs)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if stored != 0 {
		t.Fatalf("Community opened %d recording sessions; want none", stored)
	}
}

// The edition answer comes after authentication and the role check, as the steering and
// break-glass refusals do: a caller without the permission is still forbidden, not told
// about the edition, and an anonymous caller is still unauthenticated.
func TestCommunityRecordingRefusalFollowsTheRoleCheck(t *testing.T) {
	h := newCommunityHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, viewer := h.roleUser(admin, tenant, "v@x.io", "viewer")

	for _, route := range [][2]string{{"GET", "/sessions"}, {"PUT", "/config"}, {"POST", "/sweep"}} {
		if r := h.do(route[0], "/v1/m/recording"+route[1], viewer, map[string]any{}, tenantHdr(tenant)); r.code != http.StatusForbidden {
			t.Fatalf("viewer %s %s = %d %s; want 403 before the edition answer", route[0], route[1], r.code, r.raw)
		}
	}
	// notice is a read-verb permission every role holds, so it reaches the edition answer.
	if r := h.do("GET", "/v1/m/recording/notice", viewer, nil, tenantHdr(tenant)); r.code != http.StatusNotImplemented || errorCode(r) != "business_required" {
		t.Fatalf("viewer notice = %d %s; want 501 business_required", r.code, r.raw)
	}
	if r := h.do("GET", "/v1/m/recording/notice", "", nil, tenantHdr(tenant)); r.code != http.StatusUnauthorized {
		t.Fatalf("anonymous notice = %d %s; want 401", r.code, r.raw)
	}
}
