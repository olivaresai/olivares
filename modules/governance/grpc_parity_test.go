// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/olivaresai/olivares/core/api/genpb/apiv1"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
	sdkplugin "github.com/olivaresai/olivares/sdk/plugin"
	olv1 "github.com/olivaresai/olivares/sdk/plugin/genpb/olivaresv1"
)

type parityIngestPublisher struct {
	mu       sync.Mutex
	accepted int64
	tenant   string
}

func (p *parityIngestPublisher) Ingest(_ context.Context, tenant, _ string, _ sdkmodel.Observation) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.accepted++
	p.tenant = tenant
	return nil
}

func (p *parityIngestPublisher) snapshot() (int64, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.accepted, p.tenant
}

// Exercise the registered RPCs over the wire and their REST routes through the
// complete HTTP server, using the same credentials and tenant selector.
func TestGRPCRESTAuthorizationParity(t *testing.T) {
	pub := &parityIngestPublisher{}
	h := newHarnessWith(t, harnessOpts{ingest: pub})
	httpServer := httptest.NewServer(h.srv.Handler())
	t.Cleanup(httpServer.Close)
	var admin string
	var fixtureTenant model.TenantID
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gs := h.srv.NewGRPCServer()
	served := make(chan error, 1)
	go func() { served <- gs.Serve(lis) }()
	t.Cleanup(func() {
		gs.Stop()
		if err := <-served; err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			t.Errorf("gRPC server: %v", err)
		}
	})
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	observation, err := sdkplugin.ObservationToPB(sdkmodel.CostSample{
		ProviderRef: "test", ModelRef: "test", OccurredAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	const (
		infoResult = iota
		listResult
		getResult
		createResult
		auditResult
		ingestResult
		resultCount
	)
	rows := []struct {
		rpc, method, route string
		wantIndex          int
		body               any
		request            func(string, string) proto.Message
		response           func() proto.Message
	}{
		{apiv1.ControlPlane_GetServerInfo_FullMethodName, "GET", "/v1/server-info", infoResult, nil,
			func(_, _ string) proto.Message { return &apiv1.Empty{} },
			func() proto.Message { return &apiv1.ServerInfo{} }},
		{apiv1.ControlPlane_ListAgents_FullMethodName, "GET", "/v1/agents?limit=100", listResult, nil,
			func(tenant, _ string) proto.Message { return &apiv1.ListAgentsRequest{Tenant: tenant, Limit: 100} },
			func() proto.Message { return &apiv1.ListAgentsResponse{} }},
		{apiv1.ControlPlane_GetAgent_FullMethodName, "GET", "/v1/agents/", getResult, nil,
			func(tenant, id string) proto.Message { return &apiv1.GetAgentRequest{Tenant: tenant, Id: id} },
			func() proto.Message { return &apiv1.Agent{} }},
		{apiv1.ControlPlane_CreateAgent_FullMethodName, "POST", "/v1/agents", createResult, map[string]any{"name": "parity-created", "kind": "test"},
			func(tenant, _ string) proto.Message {
				return &apiv1.CreateAgentRequest{Tenant: tenant, Name: "parity-created", Kind: "test"}
			}, func() proto.Message { return &apiv1.Agent{} }},
		{apiv1.ControlPlane_VerifyAudit_FullMethodName, "GET", "/v1/audit/verify", auditResult, nil,
			func(tenant, _ string) proto.Message { return &apiv1.VerifyAuditRequest{Tenant: tenant} },
			func() proto.Message { return &apiv1.VerifyAuditResponse{} }},
		{healthpb.Health_Check_FullMethodName, "GET", "/healthz", infoResult, nil,
			func(_, _ string) proto.Message { return &healthpb.HealthCheckRequest{} },
			func() proto.Message { return &healthpb.HealthCheckResponse{} }},
		{healthpb.Health_List_FullMethodName, "GET", "/healthz", infoResult, nil,
			func(_, _ string) proto.Message { return &healthpb.HealthListRequest{} },
			func() proto.Message { return &healthpb.HealthListResponse{} }},
		{healthpb.Health_Watch_FullMethodName, "GET", "/healthz", infoResult, nil,
			func(_, _ string) proto.Message { return &healthpb.HealthCheckRequest{} },
			func() proto.Message { return &healthpb.HealthCheckResponse{} }},
		// Push is collector-only: no REST route exists. Its admission still uses
		// the same real authorizer and an explicit expected outcome in every case.
		{olv1.IngestService_Push_FullMethodName, "", "", ingestResult, nil,
			func(tenant, _ string) proto.Message {
				return &olv1.IngestEnvelope{Tenant: tenant, Source: "parity", Observation: observation}
			},
			func() proto.Message { return &olv1.IngestSummary{} }},
	}

	// Health Check/List/Watch share the public HTTP health admission policy.
	// No wildcard exemption: every
	// RPC registered by this server must be exercised by a row below.
	covered := map[string]string{}
	for _, row := range rows {
		if covered[row.rpc] != "" {
			t.Fatalf("duplicate parity row: %s", row.rpc)
		}
		covered[row.rpc] = row.rpc
	}
	for name, info := range gs.GetServiceInfo() {
		for _, method := range info.Methods {
			full := "/" + name + "/" + method.Name
			if covered[full] == "" {
				t.Errorf("registered RPC %s has no REST parity row", full)
			}
			delete(covered, full)
		}
	}
	for name := range covered {
		t.Errorf("parity row %s does not name a registered RPC", name)
	}

	// Expected outcomes are independent of either implementation. Comparing two
	// identical refusals alone would miss a regression that locks everyone out.
	type scenario struct {
		name, authorization, tenant, id string
		want                            [resultCount]int
		visible                         []string // non-nil checks the confined row set
		headerPresent                   bool
	}
	agentCount := func() int {
		r := h.do("GET", "/v1/agents?limit=100", admin, nil, tenantHdr(fixtureTenant))
		if r.code != http.StatusOK {
			t.Fatalf("count agents: %d", r.code)
		}
		return len(r.body["items"].([]any))
	}
	check := func(tc scenario) {
		t.Helper()
		t.Run(tc.name, func(t *testing.T) {
			for _, row := range rows {
				i := row.wantIndex
				t.Run(strings.TrimPrefix(row.rpc, "/"), func(t *testing.T) {
					ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					if tc.authorization != "" || tc.headerPresent {
						ctx = metadata.AppendToOutgoingContext(ctx, "authorization", tc.authorization)
					}
					path := row.route
					if row.rpc == apiv1.ControlPlane_GetAgent_FullMethodName {
						path += tc.id
					}
					before := 0
					if row.wantIndex == createResult && admin != "" {
						before = agentCount()
					}
					beforeIngest, _ := pub.snapshot()
					rest := resp{}
					if row.route != "" {
						payload, err := json.Marshal(row.body)
						if err != nil {
							t.Fatal(err)
						}
						req, err := http.NewRequestWithContext(ctx, row.method, httpServer.URL+path, bytes.NewReader(payload))
						if err != nil {
							t.Fatal(err)
						}
						req.Header.Set("Authorization", tc.authorization)
						req.Header.Set("X-Olivares-Tenant", tc.tenant)
						response, err := httpServer.Client().Do(req)
						if err != nil {
							t.Fatal(err)
						}
						defer response.Body.Close()
						rest.code = response.StatusCode
						if err := json.NewDecoder(response.Body).Decode(&rest.body); err != nil {
							t.Fatal(err)
						}
					}
					out := row.response()
					var err error
					if row.rpc == healthpb.Health_Watch_FullMethodName || row.rpc == olv1.IngestService_Push_FullMethodName {
						var stream grpc.ClientStream
						stream, err = conn.NewStream(ctx, &grpc.StreamDesc{ServerStreams: row.wantIndex == infoResult, ClientStreams: row.wantIndex == ingestResult}, row.rpc)
						if err == nil {
							err = stream.SendMsg(row.request(tc.tenant, tc.id))
							if errors.Is(err, io.EOF) {
								err = nil
							} // RecvMsg carries the server refusal.
						}
						if err == nil {
							err = stream.CloseSend()
						}
						if err == nil {
							err = stream.RecvMsg(out)
						}
					} else {
						err = conn.Invoke(ctx, row.rpc, row.request(tc.tenant, tc.id), out)
					}
					wantGRPC, known := map[int]codes.Code{
						200: codes.OK, 201: codes.OK, 400: codes.InvalidArgument,
						401: codes.Unauthenticated, 403: codes.PermissionDenied,
						404: codes.NotFound, 409: codes.FailedPrecondition,
					}[tc.want[i]]
					if !known {
						t.Fatalf("unmapped expected HTTP status: %d", tc.want[i])
					}
					if (row.route != "" && rest.code != tc.want[i]) || status.Code(err) != wantGRPC {
						t.Fatalf("%s: REST=%d gRPC=%v; want %d/%s", path, rest.code, err, tc.want[i], wantGRPC)
					}
					if row.wantIndex == createResult && admin != "" {
						delta := 0
						if tc.want[i] == http.StatusCreated {
							delta = 2
						}
						if after := agentCount(); after != before+delta {
							t.Fatalf("create changed row count from %d to %d, want delta %d", before, after, delta)
						}
					}
					if row.wantIndex == ingestResult {
						delta := int64(0)
						if wantGRPC == codes.OK {
							delta = 1
						}
						after, deliveredTenant := pub.snapshot()
						if after != beforeIngest+delta {
							t.Fatalf("ingest delivered %d observations, want %d", after-beforeIngest, delta)
						}
						if delta > 0 && deliveredTenant != fixtureTenant.String() {
							t.Fatalf("ingest tenant=%q, want %q", deliveredTenant, fixtureTenant)
						}
					}
					if wantGRPC != codes.OK {
						return
					}
					switch v := out.(type) {
					case *olv1.IngestSummary:
						if v.GetAccepted() != 1 {
							t.Fatalf("accepted=%d, want 1", v.GetAccepted())
						}
					case *apiv1.ListAgentsResponse:
						var restIDs, grpcIDs []string
						for _, item := range rest.body["items"].([]any) {
							restIDs = append(restIDs, item.(map[string]any)["id"].(string))
						}
						for _, agent := range v.GetAgents() {
							grpcIDs = append(grpcIDs, agent.GetId())
						}
						if !slices.Equal(restIDs, grpcIDs) || rest.body["has_more"] != v.GetHasMore() {
							t.Fatalf("REST IDs=%v gRPC IDs=%v (pagination %v/%v)", restIDs, grpcIDs, rest.body["has_more"], v.GetHasMore())
						}
						if tc.visible != nil && !slices.Equal(grpcIDs, tc.visible) {
							t.Fatalf("visible IDs=%v, want %v", grpcIDs, tc.visible)
						}
					case *apiv1.Agent:
						if v.GetTenantId() != rest.body["tenant_id"] || v.GetName() != rest.body["name"] {
							t.Fatal("agent response differs from REST")
						}
						if row.rpc == apiv1.ControlPlane_GetAgent_FullMethodName && v.GetId() != rest.body["id"] {
							t.Fatal("agent identity differs from REST")
						}
					case *apiv1.ServerInfo:
						if v.GetSetupRequired() != rest.body["setup_required"] {
							t.Fatal("setup state differs from REST")
						}
					case *apiv1.VerifyAuditResponse:
						if v.GetOk() != rest.body["ok"] || v.GetChainOk() != rest.body["chain"].(map[string]any)["ok"] {
							t.Fatal("audit verdict differs from REST")
						}
					}
				})
			}
		})
	}
	check(scenario{name: "before-setup", id: model.NewID().String(), want: [resultCount]int{200, 409, 409, 409, 409, 409}})
	admin = h.adminLogin()
	tenant := h.createOrg(admin, "parity")
	fixtureTenant = tenant
	other := h.createOrg(admin, "parity-other")
	workspaces := map[string]string{}
	agents := map[string]string{}
	for _, name := range []string{"alpha", "beta", "empty"} {
		r := h.do("POST", "/v1/workspaces", admin, map[string]any{"name": name, "slug": name}, tenantHdr(tenant))
		if r.code != http.StatusCreated {
			t.Fatalf("create workspace: %d %s", r.code, r.raw)
		}
		workspaces[name] = r.body["id"].(string)
	}
	for _, name := range []string{"alpha", "beta", "unassigned"} {
		r := h.do("POST", "/v1/agents", admin, map[string]any{
			"name": name, "kind": "test", "workspace_id": workspaces[name],
		}, tenantHdr(tenant))
		if r.code != http.StatusCreated {
			t.Fatalf("create agent: %d %s", r.code, r.raw)
		}
		agents[name] = r.body["id"].(string)
	}
	tokens := map[string]string{}
	for _, user := range []struct{ name, role, workspace string }{
		{"admin", auth.RoleAdmin, ""}, {"viewer", auth.RoleViewer, ""}, {"editor", auth.RoleEditor, ""},
		{"confined", auth.RoleAdmin, "alpha"}, {"empty", auth.RoleViewer, "empty"},
	} {
		email := user.name + "@parity.test"
		r := h.do("POST", "/v1/users", admin, map[string]any{
			"email": email, "password": "parity-test-password", "tenant": tenant.String(),
			"role": user.role, "workspace_id": workspaces[user.workspace],
		}, nil)
		if r.code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", user.name, r.code, r.raw)
		}
		r = h.do("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": "parity-test-password"}, nil)
		if r.code != http.StatusOK {
			t.Fatalf("login %s: %d", user.name, r.code)
		}
		tokens[user.name] = "Bearer " + r.body["token"].(string)
	}
	r := h.do("POST", "/v1/tokens", admin, map[string]any{"name": "bound", "tenant": tenant.String(), "role": auth.RoleEditor}, nil)
	if r.code != http.StatusCreated {
		t.Fatalf("issue bound token: %d", r.code)
	}
	bound := "Bearer " + r.body["token"].(string)
	boundID := r.body["id"].(string)
	allow := [resultCount]int{200, 200, 200, 201, 200, 403}
	adminAllow := allow
	adminAllow[ingestResult] = 200
	read := [resultCount]int{200, 200, 200, 403, 200, 403}
	unauth := [resultCount]int{200, 401, 401, 401, 401, 401}
	denied := [resultCount]int{200, 403, 403, 403, 403, 403}
	badTenant := [resultCount]int{200, 400, 400, 400, 400, 400}
	for _, tc := range []scenario{
		{"anonymous", "", tenant.String(), agents["alpha"], unauth, nil, false},
		{"invalid-token", "Bearer invalid", tenant.String(), agents["alpha"], [resultCount]int{401, 401, 401, 401, 401, 401}, nil, false},
		{"malformed-header", "Basic invalid", tenant.String(), agents["alpha"], [resultCount]int{401, 401, 401, 401, 401, 401}, nil, false},
		{"empty-bearer", "Bearer ", tenant.String(), agents["alpha"], [resultCount]int{401, 401, 401, 401, 401, 401}, nil, false},
		{"blank-bearer", "Bearer   ", tenant.String(), agents["alpha"], [resultCount]int{401, 401, 401, 401, 401, 401}, nil, false},
		{"superadmin", "Bearer " + admin, tenant.String(), agents["alpha"], adminAllow, nil, false},
		{"admin", tokens["admin"], tenant.String(), agents["alpha"], adminAllow, nil, false},
		{"padded-admin-tenant", tokens["admin"], "  " + tenant.String() + "  ", agents["alpha"], adminAllow, nil, false},
		{"viewer", tokens["viewer"], tenant.String(), agents["alpha"], read, nil, false},
		{"editor", tokens["editor"], tenant.String(), agents["alpha"], allow, nil, false},
		{"bound-token", bound, tenant.String(), agents["alpha"], allow, nil, false},
		{"bound-default-tenant", bound, "", agents["alpha"], allow, nil, false},
		{"bound-blank-tenant", bound, "  ", agents["alpha"], allow, nil, false},
		{"bound-other-tenant", bound, other.String(), agents["alpha"], denied, nil, false},
		{"session-other-tenant", tokens["viewer"], other.String(), agents["alpha"], denied, nil, false},
		{"session-default-tenant", tokens["viewer"], "", agents["alpha"], read, nil, false},
		{"padded-tenant", tokens["viewer"], "  " + tenant.String() + "  ", agents["alpha"], read, nil, false},
		{"padded-bound-tenant", bound, "  " + tenant.String() + "  ", agents["alpha"], allow, nil, false},
		{"invalid-tenant", bound, "invalid", agents["alpha"], badTenant, nil, false},
		{"reserved-tenant", bound, model.SystemTenantID.String(), agents["alpha"], badTenant, nil, false},
		{"superadmin-missing-tenant", "Bearer " + admin, "", agents["alpha"], badTenant, nil, false},
		{"own-workspace", tokens["confined"], tenant.String(), agents["alpha"], [resultCount]int{200, 200, 200, 403, 200, 403}, []string{agents["alpha"]}, false},
		{"padded-own-workspace", tokens["confined"], "  " + tenant.String() + "  ", agents["alpha"], [resultCount]int{200, 200, 200, 403, 200, 403}, []string{agents["alpha"]}, false},
		{"padded-other-tenant", bound, "  " + other.String() + "  ", agents["alpha"], denied, nil, false},
		{"other-workspace", tokens["confined"], tenant.String(), agents["beta"], [resultCount]int{200, 200, 403, 403, 200, 403}, []string{agents["alpha"]}, false},
		{"unassigned-agent", tokens["confined"], tenant.String(), agents["unassigned"], [resultCount]int{200, 200, 403, 403, 200, 403}, []string{agents["alpha"]}, false},
		{"empty-workspace", tokens["empty"], tenant.String(), agents["alpha"], [resultCount]int{200, 200, 403, 403, 200, 403}, []string{}, false},
		{"missing-agent", tokens["viewer"], tenant.String(), model.NewID().String(), [resultCount]int{200, 200, 404, 403, 200, 403}, nil, false},
	} {
		check(tc)
	}
	for _, tc := range []scenario{
		{name: "empty-authorization-header", headerPresent: true, tenant: tenant.String(), id: agents["alpha"], want: unauth},
		{name: "blank-authorization-header", authorization: "  ", tenant: tenant.String(), id: agents["alpha"], want: unauth},
		{name: "padded-authorization-header", authorization: "  " + tokens["admin"] + "  ", tenant: tenant.String(), id: agents["alpha"], want: adminAllow},
		{name: "padded-confined-authorization-header", authorization: "  " + tokens["confined"] + "  ", tenant: tenant.String(), id: agents["alpha"], want: [resultCount]int{200, 200, 200, 403, 200, 403}, visible: []string{agents["alpha"]}},
	} {
		check(tc)
	}
	if r := h.do("DELETE", "/v1/tokens/"+boundID, admin, nil, nil); r.code != http.StatusNoContent {
		t.Fatalf("revoke token: %d", r.code)
	}
	check(scenario{"revoked-token", bound, tenant.String(), agents["alpha"], [resultCount]int{401, 401, 401, 401, 401, 401}, nil, false})
	h.publishGrant(admin, tenant, `forbid(principal in Role::"editor", action, resource);`)
	check(scenario{"policy-forbid", tokens["editor"], tenant.String(), agents["alpha"], denied, nil, false})
}
