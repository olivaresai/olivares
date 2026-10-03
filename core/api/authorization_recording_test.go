// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/genpb/apiv1"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type recordedAuthorizations struct {
	mu      sync.Mutex
	records []auth.AuthorizationRecord
}

// This adapter exercises the public external-policy seam with immutable inputs
// that fit the policy-input ceiling but overflow the complete evidence envelope.
type nearLimitRetainedPolicy struct{ deniedResource string }

func (p nearLimitRetainedPolicy) Evaluate(ctx context.Context, req auth.Request) (auth.Decision, error) {
	if req.Permission != "agent:write" {
		return auth.DenyNothing{}.Evaluate(ctx, req)
	}
	auth.CaptureAuthorizationInputs(ctx, false, "external-policy-v1", strings.Repeat("x", model.MaxPolicyArtifactBytes-128))
	return auth.Decision{Allow: req.Resource.ID != p.deniedResource, Class: auth.ClassPolicy}, nil
}

func TestOversizedAuthorizationInputsRetainTheObservedAnswer(t *testing.T) {
	recorder := new(recordedAuthorizations)
	allowed, denied := model.NewID().String(), model.NewID().String()
	h := newHarnessOpts(t, func(o *api.Options) {
		o.AuthorizationRecorder = recorder
		o.Authorizer = auth.NewAuthorizer(nearLimitRetainedPolicy{deniedResource: denied})
	})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "bounded-history")
	editor, _ := h.authzMember(admin, "editor@bounded-history.test", "editor-password-1", auth.RoleEditor, tenant)
	for _, tc := range []struct {
		resource string
		allow    bool
	}{
		{allowed, true}, {denied, false},
	} {
		response := h.do("POST", "/access/v1/evaluation", admin, map[string]any{
			"subject":  map[string]any{"type": "user", "id": editor},
			"action":   map[string]any{"name": "agent:write"},
			"resource": map[string]any{"type": "agent", "id": tc.resource},
		}, tenantHdr(tenant))
		if response.code != 200 || response.body["decision"] != tc.allow {
			t.Fatalf("live bounded-input answer = %d %s, want %t", response.code, response.raw, tc.allow)
		}
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	var records []auth.AuthorizationRecord
	for _, record := range recorder.records {
		if record.Snapshot.Tenant == tenant && record.Snapshot.Permission == "agent:write" {
			records = append(records, record)
		}
	}
	if len(records) != 2 {
		t.Fatalf("retained %d oversized answers, want both bounded incomplete answers", len(records))
	}
	for i, record := range records {
		want, resource := auth.EvidenceAllow, allowed
		if i == 1 {
			want, resource = auth.EvidenceDeny, denied
		}
		if record.Outcome != want || record.PrincipalRef != editor || record.Snapshot.Resource.ID != resource || record.Snapshot.Permission != "agent:write" {
			t.Fatalf("incomplete history lost the observed answer: %+v", record)
		}
		if record.Snapshot.Complete || len(record.Snapshot.Policy)+len(record.Snapshot.Scoped) != 0 {
			t.Fatal("oversized history claimed reconstructible policy inputs")
		}
		b, err := json.Marshal(record.Snapshot)
		if err != nil || len(b) > model.MaxPolicyArtifactBytes {
			t.Fatalf("fallback envelope exceeds the retained artifact ceiling: %d, %v", len(b), err)
		}
	}
}

func TestAccessReviewRetainsEveryEvaluatedProjection(t *testing.T) {
	recorder := new(recordedAuthorizations)
	h := newHarnessOpts(t, func(o *api.Options) { o.AuthorizationRecorder = recorder })
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "review-history")
	viewers := make(map[string]bool)
	for i := 0; i < 130; i++ {
		id, _ := h.authzMember(admin, fmt.Sprintf("viewer%d@review-history.test", i), "viewer-password-1", auth.RoleViewer, tenant)
		viewers[id] = true
	}
	agent := h.mkAgent(admin, tenant, "review-agent")
	h.elevate(admin)
	response := h.do("POST", "/access/v1/access-review/export", admin, map[string]any{
		"resource":    map[string]any{"type": "agent", "id": agent},
		"permissions": []string{"agent:write"},
	}, tenantHdr(tenant))
	if response.code != 200 {
		t.Fatalf("large real access review = %d %s", response.code, response.raw)
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	seen := make(map[string]int)
	for _, record := range recorder.records {
		if record.Snapshot.Tenant != tenant || record.Snapshot.Permission != "agent:write" || record.Snapshot.Resource.ID != agent || !viewers[record.PrincipalRef] {
			continue
		}
		if record.Outcome != auth.EvidenceDeny || record.Purpose != sdk.PurposeCurrentWhatIf {
			t.Fatalf("viewer projection lost its deny or purpose: %+v", record)
		}
		seen[record.PrincipalRef]++
	}
	if len(seen) != len(viewers) {
		t.Fatalf("retained %d viewer projections, want all %d evaluated questions", len(seen), len(viewers))
	}
	for id, count := range seen {
		if count != 1 {
			t.Errorf("viewer %s retained %d answers, want one", id, count)
		}
	}
}

func TestAuthZENSupportedBatchRetainsEveryFinalWriteAnswer(t *testing.T) {
	recorder := new(recordedAuthorizations)
	h := newHarnessOpts(t, func(o *api.Options) { o.AuthorizationRecorder = recorder })
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "batch-history")
	editor, _ := h.authzMember(admin, "editor@batch-history.test", "editor-password-1", auth.RoleEditor, tenant)
	viewer, _ := h.authzMember(admin, "viewer@batch-history.test", "viewer-password-1", auth.RoleViewer, tenant)
	resources := make([]string, 1000)
	items := make([]any, len(resources))
	for i := range resources {
		resources[i] = model.NewID().String()
		subject := editor
		if i%2 == 1 {
			subject = viewer
		}
		items[i] = map[string]any{
			"subject":  map[string]any{"type": "user", "id": subject},
			"resource": map[string]any{"type": "agent", "id": resources[i]},
		}
	}
	response := h.do("POST", "/access/v1/evaluations", admin, map[string]any{
		"action": map[string]any{"name": "agent:write"}, "evaluations": items,
	}, tenantHdr(tenant))
	answers, _ := response.body["evaluations"].([]any)
	if response.code != 200 || len(answers) != 1000 {
		t.Fatalf("supported batch returned %d answers, HTTP%d", len(answers), response.code)
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	var records []auth.AuthorizationRecord
	for i, answer := range answers {
		if answer.(map[string]any)["decision"] != (i%2 == 0) {
			t.Fatalf("batch answer %d disagreed with the editor/viewer control", i)
		}
	}
	for _, record := range recorder.records {
		if record.Snapshot.Tenant == tenant && record.Snapshot.Permission == "agent:write" {
			records = append(records, record)
		}
	}
	if len(records) != 1000 {
		t.Fatalf("retained %d final write answers, want all 1000 supported questions", len(records))
	}
	for i, record := range records {
		want := auth.EvidenceAllow
		if i%2 == 1 {
			want = auth.EvidenceDeny
		}
		if record.Snapshot.Resource.ID != resources[i] || record.Outcome != want {
			t.Fatalf("retained answer %d lost its exact question or outcome", i)
		}
	}
}

func (r *recordedAuthorizations) RecordAuthorization(_ context.Context, record auth.AuthorizationRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, record)
	return nil
}

func TestGRPCFinalAuthorizationWritesAndDenialsAreRetained(t *testing.T) {
	recorder := new(recordedAuthorizations)
	h := newHarnessOpts(t, func(o *api.Options) { o.AuthorizationRecorder = recorder })
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "grpc-history")
	h.onboardPassword(admin, tenant, "viewer@grpc-history.test", auth.RoleViewer, "viewer-password-1")
	viewer := h.login("viewer@grpc-history.test", "viewer-password-1")

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gs := h.srv.NewGRPCServer()
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	cl := apiv1.NewControlPlaneClient(conn)
	for _, tc := range []struct {
		token string
		want  codes.Code
	}{
		{admin, codes.OK},
		{viewer, codes.PermissionDenied},
	} {
		ctx := metadata.AppendToOutgoingContext(t.Context(), "authorization", "Bearer "+tc.token)
		_, err := cl.CreateAgent(ctx, &apiv1.CreateAgentRequest{Tenant: tenant.String(), Name: "history-agent", Kind: "claude-code"})
		if status.Code(err) != tc.want {
			t.Fatalf("CreateAgent status = %s, want %s", status.Code(err), tc.want)
		}
	}
	ctx := metadata.AppendToOutgoingContext(t.Context(), "authorization", "Bearer "+viewer)
	if _, err := cl.ListAgents(ctx, &apiv1.ListAgentsRequest{Tenant: tenant.String()}); err != nil {
		t.Fatal(err)
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	var outcomes []auth.EvidenceOutcome
	for _, record := range recorder.records {
		if record.Snapshot.Tenant != tenant {
			continue
		}
		if record.Snapshot.Permission == "agent:read" {
			t.Fatal("ordinary successful read consumed history retention")
		}
		if record.Snapshot.Permission == "agent:write" {
			outcomes = append(outcomes, record.Outcome)
		}
	}
	if len(outcomes) != 2 || outcomes[0] != auth.EvidenceAllow || outcomes[1] != auth.EvidenceDeny {
		t.Fatalf("final gRPC write answers = %v, want [allow deny] before each RPC returns", outcomes)
	}
}
