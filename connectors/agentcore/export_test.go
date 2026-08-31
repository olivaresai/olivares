// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package agentcore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const (
	testGatewayA = "arn:aws:bedrock-agentcore:us-east-1:123456789012:gateway/gw-a"
	testGatewayB = "arn:aws:bedrock-agentcore:us-east-1:123456789012:gateway/gw-b"
)

func TestRenderExportGolden(t *testing.T) {
	escapedSubject := `sec"ops\lead`
	grant := ExportItem{
		Kind:        exportKindGrant,
		Tenant:      `tenant"one\prod`,
		SubjectKind: "role",
		SubjectRef:  escapedSubject,
		Workspace:   "ws-prod",
		Effect:      exportEffectPermit,
		Perms:       []string{"files:write", "files:read"},
	}
	items := []ExportItem{
		grant,
		{
			Kind:        exportKindModelAccess,
			Tenant:      "tenant-prod",
			SubjectKind: "user",
			SubjectRef:  "alice",
			Workspace:   "ws-prod",
			Effect:      exportEffectForbid,
			Models:      []string{"premium"},
		},
		{
			Kind:        exportKindSourceScope,
			Tenant:      "tenant-prod",
			SubjectKind: "workspace",
			SubjectRef:  "ws-prod",
			Workspace:   "ws-data",
			Effect:      exportEffectPermit,
			Sources:     []string{"jira"},
			Access:      "r",
		},
	}
	mapping := ExportMapping{
		WorkspaceGateways: map[string][]string{
			"ws-prod": {testGatewayB, testGatewayA},
			"ws-data": {testGatewayA},
		},
		SubjectClaims: map[string]ClaimBinding{
			"role:" + escapedSubject: {Tag: `role"tag\`, Value: escapedSubject},
			"user:alice":             {Tag: "username", Value: "alice"},
			"workspace:ws-prod":      {Tag: "workspace", Value: "ws-prod"},
		},
		PermActions: map[string][]string{
			"files:read":  {"TargetB___read", "TargetA___read"},
			"files:write": {"TargetA___write"},
		},
		ModelActions: map[string][]string{
			"premium": {"Model___invoke"},
		},
		SourceReadActions: map[string][]string{
			"jira": {"Jira___get"},
		},
	}

	rendered, unsupported := RenderExport(items, mapping, RenderOptions{EnforcementMode: enforcementModeLogOnly})
	if len(unsupported) != 0 {
		t.Fatalf("unsupported = %+v, want none", unsupported)
	}
	if len(rendered) != 5 {
		t.Fatalf("rendered = %d, want 5", len(rendered))
	}

	grantA := findRendered(t, rendered, "TargetA___write", testGatewayA)
	wantGrantStatement := `permit(
  principal is AgentCore::OAuthUser,
  action in [AgentCore::Action::"TargetA___read", AgentCore::Action::"TargetA___write", AgentCore::Action::"TargetB___read"],
  resource == AgentCore::Gateway::"arn:aws:bedrock-agentcore:us-east-1:123456789012:gateway/gw-a"
) when { principal.getTag("role\"tag\\") == "sec\"ops\\lead" };
`
	if grantA.Statement != wantGrantStatement {
		t.Fatalf("grant statement:\n%s\nwant:\n%s", grantA.Statement, wantGrantStatement)
	}
	if want := testExportPolicyName(grant, testGatewayA, exportEffectPermit); grantA.Name != want {
		t.Errorf("grant name = %q, want %q", grantA.Name, want)
	}
	wantMarker := "olivares-export v1 tenant=" + grant.Tenant + " sha256=" + testSHA256Hex(grantA.Statement)
	if grantA.Description != wantMarker {
		t.Errorf("description = %q, want %q", grantA.Description, wantMarker)
	}
	tenant, fp, ok := parseExportMarker(grantA.Description)
	if !ok || tenant != grant.Tenant || fp != testSHA256Hex(grantA.Statement) {
		t.Errorf("parseExportMarker = %q/%q/%v", tenant, fp, ok)
	}

	modelForbid := findRendered(t, rendered, "Model___invoke", testGatewayB)
	if !strings.HasPrefix(modelForbid.Statement, "forbid(\n") {
		t.Errorf("model_access deny must render forbid, got:\n%s", modelForbid.Statement)
	}
	sourceRead := findRendered(t, rendered, "Jira___get", testGatewayA)
	if strings.Contains(sourceRead.Statement, "Jira___write") {
		t.Errorf("read-only source policy must not fall back to rw actions:\n%s", sourceRead.Statement)
	}
	for _, p := range rendered {
		if p.EnforcementMode != enforcementModeLogOnly {
			t.Errorf("EnforcementMode for %s = %q, want LOG_ONLY", p.Name, p.EnforcementMode)
		}
	}
}

func TestRenderExportMergesIdentityRows(t *testing.T) {
	mapping := ExportMapping{
		WorkspaceGateways: map[string][]string{"ws-prod": {testGatewayA}},
		SubjectClaims: map[string]ClaimBinding{
			"role:ops":          {Tag: "role", Value: "ops"},
			"workspace:ws-prod": {Tag: "workspace", Value: "ws-prod"},
		},
		PermActions: map[string][]string{
			"files:read":  {"Tool___read"},
			"files:write": {"Tool___write"},
		},
		SourceActions:     map[string][]string{"jira": {"Jira___read", "Jira___write"}},
		SourceReadActions: map[string][]string{"jira": {"Jira___read"}},
	}
	base := ExportItem{
		Kind:        exportKindGrant,
		Tenant:      "tenant-a",
		SubjectKind: "role",
		SubjectRef:  "ops",
		Workspace:   "ws-prod",
		Effect:      exportEffectPermit,
	}

	// Two grant rows share one export identity (same subject/scope/effect) and
	// differ only in Perms: they MUST merge into ONE policy carrying the action
	// union — separate rendering would collide on the name and silently lose one.
	a := base
	a.Perms = []string{"files:read"}
	b := base
	b.Perms = []string{"files:write"}
	rendered, unsupported := RenderExport([]ExportItem{a, b}, mapping, RenderOptions{})
	if len(unsupported) != 0 {
		t.Fatalf("unsupported = %+v, want none", unsupported)
	}
	if len(rendered) != 1 {
		t.Fatalf("rendered = %d, want 1 merged policy", len(rendered))
	}
	for _, action := range []string{"Tool___read", "Tool___write"} {
		if !strings.Contains(rendered[0].Statement, action) {
			t.Errorf("merged statement must union actions, missing %s:\n%s", action, rendered[0].Statement)
		}
	}

	// An "r" and an "rw" projection of the same subject/workspace/source are
	// DIFFERENT policies: they must not merge and must not collide on the name.
	src := ExportItem{
		Kind:        exportKindSourceScope,
		Tenant:      "tenant-a",
		SubjectKind: "workspace",
		SubjectRef:  "ws-prod",
		Workspace:   "ws-prod",
		Effect:      exportEffectPermit,
		Sources:     []string{"jira"},
	}
	ro := src
	ro.Access = "r"
	rw := src
	rw.Access = "rw"
	rendered, unsupported = RenderExport([]ExportItem{ro, rw}, mapping, RenderOptions{})
	if len(unsupported) != 0 {
		t.Fatalf("unsupported = %+v, want none", unsupported)
	}
	if len(rendered) != 2 {
		t.Fatalf("rendered = %d, want 2 distinct policies for r vs rw", len(rendered))
	}
	if rendered[0].Name == rendered[1].Name {
		t.Fatalf("r and rw projections collided on one name %q", rendered[0].Name)
	}

	// Surface lists are unioned before the surface rule runs. A permit tainted by
	// any merged surface is unsupported because AgentCore cannot express it.
	surfA := base
	surfA.Surfaces = []string{"chat"}
	surfB := base
	surfB.Perms = []string{"files:write"}
	rendered, unsupported = RenderExport([]ExportItem{surfA, surfB}, mapping, RenderOptions{})
	if len(rendered) != 0 || len(unsupported) != 1 || unsupported[0].Reason != reasonSurfaceScoped {
		t.Fatalf("surface-tainted permit rendered=%+v unsupported=%+v, want one surface_scoped unsupported", rendered, unsupported)
	}

	// A surface-scoped forbid is conservative: losing the surface dimension can
	// only over-forbid, not over-permit, so it still exports.
	forbid := base
	forbid.Effect = exportEffectForbid
	forbid.Perms = []string{"files:read"}
	forbid.Surfaces = []string{"chat"}
	rendered, unsupported = RenderExport([]ExportItem{forbid}, mapping, RenderOptions{})
	if len(rendered) != 1 || len(unsupported) != 0 {
		t.Fatalf("surface-scoped forbid rendered=%+v unsupported=%+v, want one rendered policy", rendered, unsupported)
	}
}

func TestRenderExportUnsupportedReasons(t *testing.T) {
	baseMapping := func() ExportMapping {
		return ExportMapping{
			WorkspaceGateways: map[string][]string{"ws-prod": {testGatewayA, testGatewayB}},
			SubjectClaims: map[string]ClaimBinding{
				"role:ops": {Tag: "role", Value: "ops"},
			},
			PermActions: map[string][]string{"ok:read": {"Tool___read"}},
			SourceActions: map[string][]string{
				"jira": {"Jira___read", "Jira___write"},
			},
		}
	}
	baseItem := ExportItem{
		Kind:        exportKindGrant,
		Tenant:      "tenant-a",
		SubjectKind: "role",
		SubjectRef:  "ops",
		Workspace:   "ws-prod",
		Effect:      exportEffectPermit,
		Perms:       []string{"ok:read"},
	}

	cases := []struct {
		name        string
		item        ExportItem
		mapping     ExportMapping
		reason      string
		unsupported int
	}{
		{
			name:        "tenant-wide scope",
			item:        withWorkspace(baseItem, ""),
			mapping:     baseMapping(),
			reason:      reasonTenantWideScope,
			unsupported: 1,
		},
		{
			name:        "no gateway mapping",
			item:        withWorkspace(baseItem, "ws-missing"),
			mapping:     baseMapping(),
			reason:      reasonNoGatewayMapping,
			unsupported: 1,
		},
		{
			name:        "no subject claim",
			item:        withSubject(baseItem, "role", "missing"),
			mapping:     baseMapping(),
			reason:      reasonNoSubjectClaim,
			unsupported: 2,
		},
		{
			name:        "no action mapping",
			item:        withPerms(baseItem, []string{"missing:read"}),
			mapping:     baseMapping(),
			reason:      reasonNoActionMapping,
			unsupported: 2,
		},
		{
			name: "no read-only actions",
			item: ExportItem{
				Kind:        exportKindSourceScope,
				Tenant:      "tenant-a",
				SubjectKind: "role",
				SubjectRef:  "ops",
				Workspace:   "ws-prod",
				Effect:      exportEffectPermit,
				Sources:     []string{"jira"},
				Access:      "r",
			},
			mapping:     baseMapping(),
			reason:      reasonNoReadOnlyActions,
			unsupported: 2,
		},
		{
			name:        "agent group scope",
			item:        withScopeKind(baseItem, "agent_group"),
			mapping:     baseMapping(),
			reason:      reasonAgentGroupScope,
			unsupported: 1,
		},
		{
			name:        "explicit tenant scope",
			item:        withScopeKind(baseItem, "tenant"),
			mapping:     baseMapping(),
			reason:      reasonTenantWideScope,
			unsupported: 1,
		},
		{
			name:        "surface-scoped permit",
			item:        withSurfaces(baseItem, []string{"slack"}),
			mapping:     baseMapping(),
			reason:      reasonSurfaceScoped,
			unsupported: 2,
		},
		{
			name:        "bad effect",
			item:        withEffect(baseItem, "allow"),
			mapping:     baseMapping(),
			reason:      reasonBadEffect,
			unsupported: 2,
		},
		{
			name:        "bad kind",
			item:        withKind(baseItem, "alien"),
			mapping:     baseMapping(),
			reason:      reasonBadKind,
			unsupported: 2,
		},
		{
			name: "bad source access",
			item: ExportItem{
				Kind:        exportKindSourceScope,
				Tenant:      "tenant-a",
				SubjectKind: "role",
				SubjectRef:  "ops",
				Workspace:   "ws-prod",
				Effect:      exportEffectPermit,
				Sources:     []string{"jira"},
				Access:      "write",
			},
			mapping:     baseMapping(),
			reason:      reasonBadSourceAccess,
			unsupported: 2,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rendered, unsupported := RenderExport([]ExportItem{tc.item}, tc.mapping, RenderOptions{})
			if len(rendered) != 0 {
				t.Fatalf("rendered = %+v, want none", rendered)
			}
			if len(unsupported) != tc.unsupported {
				t.Fatalf("unsupported = %d, want %d: %+v", len(unsupported), tc.unsupported, unsupported)
			}
			for _, u := range unsupported {
				if u.Reason != tc.reason {
					t.Errorf("unsupported reason = %q, want %q", u.Reason, tc.reason)
				}
			}
			if len(rendered)+len(unsupported) != tc.unsupported {
				t.Errorf("rendered+unsupported = %d, want %d", len(rendered)+len(unsupported), tc.unsupported)
			}
		})
	}
}

func TestRenderExportNameStability(t *testing.T) {
	item := ExportItem{
		Kind:        exportKindGrant,
		Tenant:      "tenant-a",
		SubjectKind: "role",
		SubjectRef:  "ops",
		Workspace:   "ws-prod",
		Effect:      exportEffectPermit,
		Perms:       []string{"files:read"},
	}
	mapping := ExportMapping{
		WorkspaceGateways: map[string][]string{"ws-prod": {testGatewayA}, "ws-next": {testGatewayA}},
		SubjectClaims:     map[string]ClaimBinding{"role:ops": {Tag: "role", Value: "ops"}},
		PermActions: map[string][]string{
			"files:read":  {"Tool___read"},
			"files:write": {"Tool___write"},
		},
	}
	base, unsupported := RenderExport([]ExportItem{item}, mapping, RenderOptions{})
	if len(base) != 1 || len(unsupported) != 0 {
		t.Fatalf("base render = %+v unsupported=%+v", base, unsupported)
	}
	changedPerms, _ := RenderExport([]ExportItem{withPerms(item, []string{"files:write"})}, mapping, RenderOptions{})
	if changedPerms[0].Name != base[0].Name {
		t.Fatalf("content-only action change changed name: %q vs %q", changedPerms[0].Name, base[0].Name)
	}
	if changedPerms[0].Statement == base[0].Statement {
		t.Fatal("permission change should alter the statement")
	}

	changedWorkspace, _ := RenderExport([]ExportItem{withWorkspace(item, "ws-next")}, mapping, RenderOptions{})
	if changedWorkspace[0].Name == base[0].Name {
		t.Fatal("workspace identity change must change the name")
	}

	changedGatewayMapping := mapping
	changedGatewayMapping.WorkspaceGateways = map[string][]string{"ws-prod": {testGatewayB}}
	changedGateway, _ := RenderExport([]ExportItem{item}, changedGatewayMapping, RenderOptions{})
	if changedGateway[0].Name == base[0].Name {
		t.Fatal("gateway identity change must change the name")
	}
}

func TestBuildExportPlanDiffFixture(t *testing.T) {
	var fixture struct {
		Policies []policyItem `json:"policies"`
	}
	if err := json.Unmarshal([]byte(newStub(t).fixture("export_remote_policies.json")), &fixture); err != nil {
		t.Fatalf("fixture decode: %v", err)
	}
	desired := []RenderedPolicy{
		testRenderedPolicy("olv_tenant_a_g_create000", "permit create;"),
		testRenderedPolicy("olv_tenant_a_g_unchanged", "permit unchanged;"),
		testRenderedPolicy("olv_tenant_a_g_update000", "permit desired update;"),
	}
	unsupported := []UnsupportedItem{{Reason: reasonNoActionMapping, Item: ExportItem{Kind: exportKindGrant, Tenant: "tenant-a"}}}

	plan := BuildExportPlanWithUnsupported("pe-export", "tenant-a", desired, unsupported, fixture.Policies)
	if got, want := changeNames(plan.Creates), []string{"olv_tenant_a_g_create000"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("creates = %v, want %v", got, want)
	}
	if got, want := changeNames(plan.Updates), []string{"olv_tenant_a_g_update000"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("updates = %v, want %v", got, want)
	}
	if plan.Updates[0].PolicyID != "pol-update" {
		t.Errorf("update PolicyID = %q, want pol-update", plan.Updates[0].PolicyID)
	}
	if plan.Updates[0].RemoteFingerprint != testSHA256Hex("permit old update;") {
		t.Errorf("update RemoteFingerprint = %q", plan.Updates[0].RemoteFingerprint)
	}
	if plan.Updates[0].RemoteEnforcementMode != enforcementModeActive {
		t.Errorf("update RemoteEnforcementMode = %q, want ACTIVE", plan.Updates[0].RemoteEnforcementMode)
	}
	if got, want := changeNames(plan.Deletes), []string{"olv_tenant_a_g_delete000"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("deletes = %v, want %v", got, want)
	}
	if plan.Deletes[0].RemoteEnforcementMode != enforcementModeActive {
		t.Errorf("delete RemoteEnforcementMode = %q, want ACTIVE", plan.Deletes[0].RemoteEnforcementMode)
	}
	if got, want := plan.Unchanged, []string{"olv_tenant_a_g_unchanged"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unchanged = %v, want %v", got, want)
	}
	wantUnmanaged := []string{"manual_policy", "olv_tenant_b_g_keep0000"}
	if got := plan.Unmanaged; !reflect.DeepEqual(got, wantUnmanaged) {
		t.Fatalf("unmanaged = %v, want %v", got, wantUnmanaged)
	}
	if len(plan.Unsupported) != 1 || plan.Unsupported[0].Reason != reasonNoActionMapping {
		t.Fatalf("unsupported not passed through: %+v", plan.Unsupported)
	}
	if plan.PlanHash == "" {
		t.Fatal("PlanHash must be non-empty")
	}
	again := BuildExportPlanWithUnsupported("pe-export", "tenant-a", desired, unsupported, fixture.Policies)
	if again.PlanHash != plan.PlanHash {
		t.Fatalf("PlanHash unstable on identical inputs: %q vs %q", again.PlanHash, plan.PlanHash)
	}

	permutedDesired := []RenderedPolicy{desired[2], desired[0], desired[1]}
	permutedRemote := append([]policyItem(nil), fixture.Policies...)
	slices.Reverse(permutedRemote)
	permuted := BuildExportPlanWithUnsupported("pe-export", "tenant-a", permutedDesired, unsupported, permutedRemote)
	if permuted.PlanHash != plan.PlanHash {
		t.Fatalf("PlanHash changed after input permutation: %q vs %q", permuted.PlanHash, plan.PlanHash)
	}
}

func TestExportWireCreateUpdateDeletePolicy(t *testing.T) {
	clearAWSEnv(t)
	d := newStub(t)
	d.onStatus(http.MethodPost, "/policy-engines/pe%2Falpha/policies", http.StatusAccepted, `{"policyId":"pol-1","policyArn":"arn:policy:1","status":"CREATING","statusReasons":["queued"]}`)
	d.onStatus(http.MethodPatch, "/policy-engines/pe%2Falpha/policies/pol%2F1", http.StatusAccepted, `{"policyId":"pol-1","policyArn":"arn:policy:1","status":"UPDATING","statusReasons":["accepted"]}`)
	d.onStatus(http.MethodDelete, "/policy-engines/pe%2Falpha/policies/pol%2F1", http.StatusAccepted, `{"policyId":"pol-1","policyArn":"arn:policy:1","status":"DELETING","statusReasons":["accepted"]}`)
	s := openSource(t, d, onlineSettings(nil))
	c := s.newClient()

	createResp, err := createPolicy(context.Background(), c, "pe/alpha", createPolicyRequest{
		Name: "olv_tenant_a_g_create000",
		Definition: writePolicyDefinition{
			Cedar: &cedarPolicyBody{Statement: "permit create;"},
		},
		Description:     "desc",
		EnforcementMode: enforcementModeLogOnly,
		ValidationMode:  "FAIL_ON_ANY_FINDINGS",
		ClientToken:     "token-create",
	})
	if err != nil {
		t.Fatalf("createPolicy: %v", err)
	}
	if createResp.Status != "CREATING" || !reflect.DeepEqual(createResp.StatusReasons, []string{"queued"}) {
		t.Fatalf("create response = %+v", createResp)
	}
	var createBody map[string]any
	if err := json.Unmarshal([]byte(d.reqs[0].body), &createBody); err != nil {
		t.Fatalf("create body decode: %v", err)
	}
	createDefinition := createBody["definition"].(map[string]any)
	createCedar := createDefinition["cedar"].(map[string]any)
	if createCedar["statement"] != "permit create;" || createBody["validationMode"] != "FAIL_ON_ANY_FINDINGS" {
		t.Fatalf("create body shape = %s", d.reqs[0].body)
	}

	def := writePolicyDefinition{Cedar: &cedarPolicyBody{Statement: "permit update;"}}
	updateResp, err := updatePolicy(context.Background(), c, "pe/alpha", "pol/1", updatePolicyRequest{
		Definition:      &def,
		Description:     &updatedDescription{OptionalValue: "new desc"},
		EnforcementMode: enforcementModeActive,
		ValidationMode:  "FAIL_ON_ANY_FINDINGS",
		ClientToken:     "token-update",
	})
	if err != nil {
		t.Fatalf("updatePolicy: %v", err)
	}
	if updateResp.Status != "UPDATING" || updateResp.StatusReasons[0] != "accepted" {
		t.Fatalf("update response = %+v", updateResp)
	}
	var updateBody map[string]any
	if err := json.Unmarshal([]byte(d.reqs[1].body), &updateBody); err != nil {
		t.Fatalf("update body decode: %v", err)
	}
	updateDescription := updateBody["description"].(map[string]any)
	if updateDescription["optionalValue"] != "new desc" {
		t.Fatalf("update description wrapper = %s", d.reqs[1].body)
	}

	deleteResp, err := deletePolicy(context.Background(), c, "pe/alpha", "pol/1")
	if err != nil {
		t.Fatalf("deletePolicy: %v", err)
	}
	if deleteResp.Status != "DELETING" || d.reqs[2].body != "" {
		t.Fatalf("delete response/body = %+v / %q", deleteResp, d.reqs[2].body)
	}
}

func TestExportWirePolicyErrorsAreAPIErrors(t *testing.T) {
	cases := []struct {
		name   string
		method string
		sub    string
		call   func(context.Context, *client) error
	}{
		{
			name:   "create",
			method: http.MethodPost,
			sub:    "/policy-engines/pe/policies",
			call: func(ctx context.Context, c *client) error {
				_, err := createPolicy(ctx, c, "pe", createPolicyRequest{Name: "p", Definition: writePolicyDefinition{Cedar: &cedarPolicyBody{Statement: "permit;"}}})
				return err
			},
		},
		{
			name:   "update",
			method: http.MethodPatch,
			sub:    "/policy-engines/pe/policies/pol",
			call: func(ctx context.Context, c *client) error {
				_, err := updatePolicy(ctx, c, "pe", "pol", updatePolicyRequest{Description: &updatedDescription{OptionalValue: "desc"}})
				return err
			},
		},
		{
			name:   "delete",
			method: http.MethodDelete,
			sub:    "/policy-engines/pe/policies/pol",
			call: func(ctx context.Context, c *client) error {
				_, err := deletePolicy(ctx, c, "pe", "pol")
				return err
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearAWSEnv(t)
			d := newStub(t)
			d.onStatus(tc.method, tc.sub, http.StatusBadRequest, `{"message":"bad request"}`)
			c := openSource(t, d, onlineSettings(nil)).newClient()
			err := tc.call(context.Background(), c)
			var ae *apiError
			if !errors.As(err, &ae) {
				t.Fatalf("error = %T %[1]v, want *apiError", err)
			}
			if ae.status != http.StatusBadRequest {
				t.Errorf("apiError status = %d", ae.status)
			}
		})
	}
}

func TestExportWireListOps(t *testing.T) {
	clearAWSEnv(t)
	d := newStub(t)
	d.on(http.MethodGet, "/gateways/gw%2Fprod/targets/?", `{"items":[{"targetId":"target-1","name":"jira","targetType":"MCP","status":"ACTIVE"}],"nextToken":"GT+PAGE/2="}`)
	d.on(http.MethodGet, "/gateways/gw%2Fprod/targets/?", `{"items":[{"targetId":"target-2","name":"git","targetType":"MCP","status":"ACTIVE"}],"nextToken":"GT-UNUSED"}`)
	d.on(http.MethodGet, "/gateways/gw%2Fprod/targets/?", `{"items":[{"targetId":"target-3"}]}`)
	d.on(http.MethodPost, "/evaluators?", `{"evaluators":[{"evaluatorId":"eval-1","evaluatorArn":"arn:eval:1","evaluatorName":"quality","evaluatorType":"LLM","level":"ACCOUNT","status":"ACTIVE","lockedForModification":true}]}`)
	d.on(http.MethodPost, "/online-evaluation-configs?", `{"onlineEvaluationConfigs":[{"onlineEvaluationConfigId":"cfg-1","onlineEvaluationConfigArn":"arn:cfg:1","onlineEvaluationConfigName":"prod","status":"ACTIVE","executionStatus":"FAILED","failureReason":"timeout"}]}`)
	s := openSource(t, d, onlineSettings(map[string]string{"max_pages": "2"}))
	c := s.newClient()

	targets, err := s.listGatewayTargets(context.Background(), c, "gw/prod")
	if err != nil {
		t.Fatalf("listGatewayTargets: %v", err)
	}
	if got := len(targets); got != 2 {
		t.Fatalf("targets = %d, want max_pages-bounded 2", got)
	}
	evaluators, err := s.listEvaluators(context.Background(), c)
	if err != nil {
		t.Fatalf("listEvaluators: %v", err)
	}
	if len(evaluators) != 1 || !evaluators[0].LockedForModification {
		t.Fatalf("evaluators = %+v", evaluators)
	}
	configs, err := s.listOnlineEvaluationConfigs(context.Background(), c)
	if err != nil {
		t.Fatalf("listOnlineEvaluationConfigs: %v", err)
	}
	if len(configs) != 1 || configs[0].ExecutionStatus != "FAILED" {
		t.Fatalf("online configs = %+v", configs)
	}

	var targetCalls, evaluatorCalls, onlineCalls int
	for _, r := range d.reqs {
		u, err := url.Parse(r.url)
		if err != nil {
			t.Fatalf("parse request URL %q: %v", r.url, err)
		}
		switch {
		case strings.Contains(r.url, "/gateways/gw%2Fprod/targets/"):
			targetCalls++
			if r.method != http.MethodGet || u.Query().Get("maxResults") != "1000" {
				t.Errorf("gateway target request = %+v", r)
			}
		case strings.Contains(r.url, "/evaluators?"):
			evaluatorCalls++
			if r.method != http.MethodPost || r.body != "" || u.Query().Get("maxResults") != "100" {
				t.Errorf("evaluator request must be POST query-only with nil body: %+v", r)
			}
		case strings.Contains(r.url, "/online-evaluation-configs?"):
			onlineCalls++
			if r.method != http.MethodPost || r.body != "" || u.Query().Get("maxResults") != "100" {
				t.Errorf("online config request must be POST query-only with nil body: %+v", r)
			}
		}
	}
	if targetCalls != 2 {
		t.Errorf("gateway target calls = %d, want max_pages=2", targetCalls)
	}
	if evaluatorCalls != 1 || onlineCalls != 1 {
		t.Errorf("evaluator/online calls = %d/%d, want 1/1", evaluatorCalls, onlineCalls)
	}
}

func TestPolicyDefinitionPolicyUnionArm(t *testing.T) {
	var def policyDefinition
	if err := json.Unmarshal([]byte(newStub(t).fixture("policy_definition_policy.json")), &def); err != nil {
		t.Fatalf("fixture decode: %v", err)
	}
	if got := def.kind(); got != "policy" {
		t.Fatalf("policyDefinition.kind() = %q, want policy", got)
	}
	if got := (policyDefinition{Cedar: []byte(`{"statement":"permit;"}`)}).kind(); got != "cedar" {
		t.Fatalf("cedar definition kind = %q", got)
	}
	if got := (policyDefinition{Generated: []byte(`{"description":"baseline"}`)}).kind(); got != "generated" {
		t.Fatalf("generated definition kind = %q", got)
	}
}

func findRendered(t *testing.T, rendered []RenderedPolicy, action, gateway string) RenderedPolicy {
	t.Helper()
	for _, p := range rendered {
		if strings.Contains(p.Statement, action) && strings.Contains(p.Statement, gateway) {
			return p
		}
	}
	t.Fatalf("rendered policy with action %q gateway %q not found: %+v", action, gateway, rendered)
	return RenderedPolicy{}
}

func testRenderedPolicy(name, statement string) RenderedPolicy {
	return RenderedPolicy{
		Name:            name,
		Statement:       statement,
		Description:     exportMarker("tenant-a", statement),
		EnforcementMode: enforcementModeActive,
	}
}

func changeNames(changes []PlannedChange) []string {
	out := make([]string, 0, len(changes))
	for _, ch := range changes {
		out = append(out, ch.Name)
	}
	return out
}

func withWorkspace(item ExportItem, workspace string) ExportItem {
	item.Workspace = workspace
	return item
}

func withSubject(item ExportItem, kind, ref string) ExportItem {
	item.SubjectKind = kind
	item.SubjectRef = ref
	return item
}

func withPerms(item ExportItem, perms []string) ExportItem {
	item.Perms = perms
	return item
}

func withEffect(item ExportItem, effect string) ExportItem {
	item.Effect = effect
	return item
}

func withKind(item ExportItem, kind string) ExportItem {
	item.Kind = kind
	return item
}

func withScopeKind(item ExportItem, scopeKind string) ExportItem {
	item.ScopeKind = scopeKind
	return item
}

func withSurfaces(item ExportItem, surfaces []string) ExportItem {
	item.Surfaces = surfaces
	return item
}

func testExportPolicyName(item ExportItem, gatewayARN, effect string) string {
	return "olv_" + testTenantSlug(item.Tenant) + "_" + testKindCode(item.Kind) + "_" + testHashLengthPrefixed(
		exportPolicyNameVersion,
		strings.TrimSpace(item.Kind),
		item.Tenant,
		item.SubjectKind,
		item.SubjectRef,
		strings.TrimSpace(item.ScopeKind),
		item.Workspace,
		gatewayARN,
		effect,
		strings.TrimSpace(item.Access),
	)[:10]
}

func testTenantSlug(tenant string) string {
	var b strings.Builder
	for _, r := range tenant {
		if b.Len() >= 16 {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

func testKindCode(kind string) string {
	switch strings.TrimSpace(kind) {
	case exportKindGrant:
		return "g"
	case exportKindModelAccess:
		return "ma"
	case exportKindSourceScope:
		return "ss"
	default:
		return "x"
	}
}

func testHashLengthPrefixed(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		var lenbuf [8]byte
		n := len(part)
		for i := 0; i < 8; i++ {
			lenbuf[i] = byte(n >> (8 * (7 - i)))
		}
		_, _ = h.Write(lenbuf[:])
		_, _ = h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func testSHA256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
