// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
)

// denyPermissionTo withholds one permission from one person, as a custom role without it does.
type denyPermissionTo struct {
	perm  auth.Permission
	email string
}

func (d denyPermissionTo) Evaluate(_ context.Context, req auth.Request) (auth.Decision, error) {
	if req.Permission == d.perm && req.Principal.Email == d.email {
		return auth.Decision{Allow: false, Reason: "withheld", Class: auth.ClassPolicy}, nil
	}
	return auth.Decision{Allow: true}, nil
}

// TestLeaseTakeoverAndRevokeNeedOnlyTheRoutePermission: the takeover and revoke routes
// publish sessions:lease:admin, and that permission is the whole contract. A person who
// holds it without sessions:work:admin passes the command's admin check; a person who
// holds neither is refused.
func TestLeaseTakeoverAndRevokeNeedOnlyTheRoutePermission(t *testing.T) {
	const leaseAdminEmail = "lease-admin@k2.test"
	f := newK2FixtureWithPolicy(t, "k2-lease-admin-route", denyPermissionTo{perm: permWorkAdmin, email: leaseAdminEmail})
	leaseAdmin := workAPIRoleToken(t, f.h, f.admin, f.tenant, auth.RoleAdmin, leaseAdminEmail)
	acquired := f.acquire(f.driver, nil)
	if acquired.code != http.StatusOK {
		t.Fatalf("seed acquire = %d %s", acquired.code, acquired.raw)
	}
	f.version = int64(acquired.body["version"].(float64))
	fence := f.lease(t).Fence

	// Neither permission: refused on both routes.
	for _, route := range []string{"/lease/takeover", "/lease/revoke"} {
		got := f.apply(http.MethodPost, route, f.human, map[string]any{"holder_sid": f.sid, "fence": fence, "ttl_seconds": 60, "reason": "not an admin"})
		if got.code != http.StatusForbidden {
			t.Fatalf("%s without sessions:lease:admin = %d %s, want 403", route, got.code, got.raw)
		}
	}

	// Takeover of a live lease reaches its own guards: lease_held (it would also need
	// force and a decision), not forbidden.
	takeover := f.apply(http.MethodPost, "/lease/takeover", leaseAdmin, map[string]any{"holder_sid": f.sid, "fence": fence, "ttl_seconds": 60})
	if takeover.code != http.StatusConflict || workAPIErrorCode(takeover) != "lease_held" {
		t.Fatalf("lease admin takeover of a live lease = %d %s, want 409 lease_held", takeover.code, takeover.raw)
	}

	revoked := f.apply(http.MethodPost, "/lease/revoke", leaseAdmin, map[string]any{"fence": fence, "reason": "operator recovers the item"})
	if revoked.code != http.StatusOK {
		t.Fatalf("lease admin revoke = %d %s, want 200", revoked.code, revoked.raw)
	}
	if state := f.lease(t).State; state != workLeaseRevoked {
		t.Fatalf("lease after revoke = %q, want %q", state, workLeaseRevoked)
	}
}
