// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/runtime/hostmetrics"
)

type fakeHost struct{ err error }

func (f fakeHost) HostStatus(context.Context) (hostmetrics.Status, error) {
	return hostmetrics.Status{Memory: hostmetrics.Memory{TotalBytes: 1 << 30}, Filesystems: []hostmetrics.Filesystem{}, History: []hostmetrics.Sample{}}, f.err
}

func TestHostStatusIsSystemAdminOnlyAndNotAvailableWhereUnreadable(t *testing.T) {
	h := newHarnessOpts(t, func(o *api.Options) { o.HostMetrics = fakeHost{} })
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "hostmetrics")
	r := h.do("POST", "/v1/users", admin, map[string]any{"email": "owner@hostmetrics.test", "password": "tenantowner1", "tenant": tenant.String(), "role": auth.RoleOwner}, nil)
	if r.code != http.StatusCreated {
		t.Fatalf("owner = %d %s", r.code, r.raw)
	}
	r = h.do("POST", "/v1/auth/login", "", map[string]any{"email": "owner@hostmetrics.test", "password": "tenantowner1"}, nil)
	if r.code != http.StatusOK {
		t.Fatalf("login = %d %s", r.code, r.raw)
	}
	owner := r.body["token"].(string)
	for _, tc := range []struct {
		token  string
		status int
	}{{"", 401}, {owner, 403}, {admin, 200}} {
		if r = h.do(http.MethodGet, "/v1/system/host", tc.token, nil, nil); r.code != tc.status {
			t.Fatalf("token %q: expected %d, got %d %s", tc.token, tc.status, r.code, r.raw)
		}
	}
	if _, ok := r.body["memory"].(map[string]any); !ok {
		t.Fatalf("no memory in %s", r.raw)
	}

	off := newHarnessOpts(t, func(o *api.Options) { o.HostMetrics = fakeHost{err: hostmetrics.ErrUnavailable} })
	if r = off.do(http.MethodGet, "/v1/system/host", off.adminLogin(), nil, nil); r.code != http.StatusNotImplemented || errorCode(r) != "host_metrics_unavailable" {
		t.Fatalf("unreadable host = %d %s", r.code, r.raw)
	}
	unwired := newHarness(t)
	if r = unwired.do(http.MethodGet, "/v1/system/host", unwired.adminLogin(), nil, nil); r.code != http.StatusNotImplemented {
		t.Fatalf("unwired service = %d %s", r.code, r.raw)
	}
}
