// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"net"
	"net/http"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/olivaresai/olivares/core/api/genpb/apiv1"
	"github.com/olivaresai/olivares/core/auth"
)

// #491: a refused request said only "forbidden", so an invited teammate could not
// tell what to ask for, nor whom: an organization admin or the server's superadmin.
// The refusal keeps its status and code and its message names the missing
// permission and where it applies.
func TestForbiddenNamesTheMissingPermissionAndItsScope(t *testing.T) {
	h := newHarness(t)
	super := h.adminLogin()
	tenant := h.createOrg(super, "acme")
	viewer := h.tenantTokenRole(super, tenant, "viewer@x.io", auth.RoleViewer)
	admin := h.tenantTokenRole(super, tenant, "orgadmin@x.io", auth.RoleAdmin)
	inOrg := func(perm, role string) string {
		return `This needs the "` + perm + `" permission in this organization (built-in roles: ` + role + ` and above).`
	}
	serverWide := func(perm string) string {
		return `This needs the "` + perm + `" permission across the whole server, which only a superadmin holds.`
	}

	cases := []struct {
		name, method, path, token string
		body                      any
		want                      string
	}{
		// admit, the decision behind every tenant route.
		{"tenant route", "GET", "/v1/members", viewer, nil, inOrg("user:read", "admin")},
		// Handler checks on authenticated routes.
		{"list tokens", "GET", "/v1/tokens", viewer, nil, inOrg("token:read", "admin")},
		{"issue token", "POST", "/v1/tokens", viewer,
			map[string]any{"name": "t", "tenant": tenant.String(), "role": auth.RoleViewer}, inOrg("token:write", "admin")},
		{"grant membership", "POST", "/v1/memberships", viewer,
			map[string]any{"user_id": "x", "tenant": tenant.String(), "role": auth.RoleViewer}, inOrg("membership:write", "admin")},
		// authzSystem: an organization admin is not enough.
		{"system route", "GET", "/v1/users", admin, nil, serverWide("user:read")},
		{"system admin route", "GET", "/v1/console/modules", admin, nil, serverWide("system:admin")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := h.do(tc.method, tc.path, tc.token, tc.body, tenantHdr(tenant))
			if r.code != http.StatusForbidden {
				t.Fatalf("%s %s = %d %s, want 403", tc.method, tc.path, r.code, r.raw)
			}
			e, _ := r.body["error"].(map[string]any)
			if e["code"] != "forbidden" {
				t.Fatalf("code = %v, want the published forbidden", e["code"])
			}
			if e["message"] != tc.want {
				t.Fatalf("message = %q, want %q", e["message"], tc.want)
			}
		})
	}

	// gRPC renders the same refusal through the same mapping (grpcError prefixes
	// the code): the viewer's CreateAgent and its REST twin say the same sentence.
	t.Run("grpc parity", func(t *testing.T) {
		lis, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		gs := h.srv.NewGRPCServer()
		go func() { _ = gs.Serve(lis) }()
		defer gs.Stop()
		conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		want := inOrg("agent:write", "editor")
		ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+viewer)
		_, gerr := apiv1.NewControlPlaneClient(conn).CreateAgent(ctx, &apiv1.CreateAgentRequest{Tenant: tenant.String(), Name: "bot", Kind: "claude-code"})
		if status.Code(gerr) != codes.PermissionDenied || status.Convert(gerr).Message() != "forbidden: "+want {
			t.Fatalf("gRPC = %v, want PermissionDenied %q", gerr, "forbidden: "+want)
		}
		rest := h.do("POST", "/v1/agents", viewer, map[string]any{"name": "bot", "kind": "claude-code"}, tenantHdr(tenant))
		e, _ := rest.body["error"].(map[string]any)
		if rest.code != http.StatusForbidden || e["message"] != want {
			t.Fatalf("REST = %d %s, want 403 %q", rest.code, rest.raw, want)
		}
	})
}
