// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package agentcore

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

type exportGateFunc func(context.Context, ExportGateRequest) (ExportGateDecision, error)

func (f exportGateFunc) Authorize(ctx context.Context, req ExportGateRequest) (ExportGateDecision, error) {
	return f(ctx, req)
}

type recordingExportAuditor struct {
	records []ExportAuditRecord
}

func (a *recordingExportAuditor) Record(_ context.Context, rec ExportAuditRecord) {
	a.records = append(a.records, rec)
}

func newTestExporter(t *testing.T, d *stubDoer, allowlist *ExportAllowlist, gate ExportGate, auditor ExportAuditor) *Exporter {
	t.Helper()
	ex, err := NewExporter(ExporterConfig{
		Region:          "us-east-1",
		AccessKeyID:     testAKID,
		SecretAccessKey: testSecret,
		Endpoint:        "https://agentcore.test.local",
		Doer:            d,
		Allowlist:       allowlist,
		Gate:            gate,
		Auditor:         auditor,
		Clock:           func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	return ex
}

func approvedExportGate(approvers ...string) ExportGate {
	return exportGateFunc(func(_ context.Context, req ExportGateRequest) (ExportGateDecision, error) {
		// N approvers models N distinct PEOPLE, each acting through one credential, so the
		// stub states both identities: the quorum counts people, and a fixture that set
		// only Approvers would assert that credentials count as humans.
		return ExportGateDecision{
			ApprovalRef:     "approval-1",
			Status:          ExportApproved,
			PlanHash:        req.PlanHash,
			Approvers:       approvers,
			ApproverPersons: approvers,
		}, nil
	})
}

func TestExporterDenyPathsAuditAndDoNotWrite(t *testing.T) {
	base := testApplyPlan()
	cases := []struct {
		name        string
		plan        ExportPlan
		allowlist   *ExportAllowlist
		gate        ExportGate
		wantReason  string
		wantDenyErr bool
	}{
		{
			name:        "empty engine id",
			plan:        withPlanEngine(base, ""),
			allowlist:   NewExportAllowlist([]string{"*"}),
			gate:        approvedExportGate("alice"),
			wantReason:  "empty engine id",
			wantDenyErr: true,
		},
		{
			name:        "empty plan hash",
			plan:        withPlanHash(base, ""),
			allowlist:   NewExportAllowlist([]string{"*"}),
			gate:        approvedExportGate("alice"),
			wantReason:  "empty plan hash",
			wantDenyErr: true,
		},
		{
			name:        "nil allowlist denies",
			plan:        base,
			allowlist:   nil,
			gate:        approvedExportGate("alice"),
			wantReason:  "allowlist deny",
			wantDenyErr: true,
		},
		{
			name:        "no gate wired",
			plan:        base,
			allowlist:   NewExportAllowlist([]string{"*"}),
			gate:        nil,
			wantReason:  "gate not approved (no_gate)",
			wantDenyErr: true,
		},
		{
			name:      "gate pending",
			plan:      base,
			allowlist: NewExportAllowlist([]string{"*"}),
			gate: exportGateFunc(func(_ context.Context, req ExportGateRequest) (ExportGateDecision, error) {
				return ExportGateDecision{ApprovalRef: "approval-pending", Status: ExportPending, PlanHash: req.PlanHash}, nil
			}),
			wantReason:  "gate not approved (pending)",
			wantDenyErr: true,
		},
		{
			name:      "gate error fail closed",
			plan:      base,
			allowlist: NewExportAllowlist([]string{"*"}),
			gate: exportGateFunc(func(context.Context, ExportGateRequest) (ExportGateDecision, error) {
				return ExportGateDecision{}, errors.New("gate down")
			}),
			wantReason: "gate error (fail-closed)",
		},
		{
			name:      "plan hash mismatch",
			plan:      base,
			allowlist: NewExportAllowlist([]string{"*"}),
			gate: exportGateFunc(func(context.Context, ExportGateRequest) (ExportGateDecision, error) {
				return ExportGateDecision{ApprovalRef: "approval-stale", Status: ExportApproved, PlanHash: "other-plan", Approvers: []string{"alice"}, ApproverPersons: []string{"alice"}}, nil
			}),
			wantReason:  "plan not bound",
			wantDenyErr: true,
		},
		{
			name:        "delete needs dual control",
			plan:        testDeletePlan(),
			allowlist:   NewExportAllowlist([]string{"*"}),
			gate:        approvedExportGate("alice"),
			wantReason:  "dual-control not satisfied",
			wantDenyErr: true,
		},
		{
			name:        "active to log only update needs dual control",
			plan:        testDowngradePlan(),
			allowlist:   NewExportAllowlist([]string{"*"}),
			gate:        approvedExportGate("alice"),
			wantReason:  "dual-control not satisfied",
			wantDenyErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearAWSEnv(t)
			d := newStub(t)
			auditor := &recordingExportAuditor{}
			ex := newTestExporter(t, d, tc.allowlist, tc.gate, auditor)
			_, err := ex.Apply(context.Background(), tc.plan, ExportSpec{Tenant: "tenant-a", RequestedBy: "fran"})
			if err == nil {
				t.Fatal("Apply returned nil error for deny path")
			}
			var deny *ExportDenyError
			gotDenyErr := errors.As(err, &deny)
			if gotDenyErr != tc.wantDenyErr {
				t.Fatalf("ExportDenyError presence = %v, want %v (err=%T %[3]v)", gotDenyErr, tc.wantDenyErr, err)
			}
			if len(d.reqs) != 0 {
				t.Fatalf("deny path made %d HTTP request(s): %+v", len(d.reqs), d.reqs)
			}
			if len(auditor.records) != 1 {
				t.Fatalf("audit records = %d, want 1", len(auditor.records))
			}
			rec := auditor.records[0]
			if rec.Allowed {
				t.Errorf("deny audit Allowed = true")
			}
			if !strings.Contains(rec.Reason, tc.wantReason) {
				t.Errorf("audit reason = %q, want to contain %q", rec.Reason, tc.wantReason)
			}
			if !rec.At.Equal(testNow) {
				t.Errorf("audit At = %v, want %v", rec.At, testNow)
			}
		})
	}
}

func TestExporterApplyHappyPathOrderAndTokens(t *testing.T) {
	clearAWSEnv(t)
	d := newStub(t)
	d.onStatus(http.MethodPost, "/policy-engines/pe-export/policies", http.StatusAccepted, `{"policyId":"pol-create","policyArn":"arn:policy:create","status":"CREATING","statusReasons":["queued"]}`)
	d.onStatus(http.MethodPatch, "/policy-engines/pe-export/policies/pol-update", http.StatusAccepted, `{"policyId":"pol-update","policyArn":"arn:policy:update","status":"UPDATING","statusReasons":["accepted"]}`)
	d.onStatus(http.MethodDelete, "/policy-engines/pe-export/policies/pol-delete", http.StatusAccepted, `{"policyId":"pol-delete","policyArn":"arn:policy:delete","status":"DELETING","statusReasons":["accepted"]}`)
	auditor := &recordingExportAuditor{}
	ex := newTestExporter(t, d, NewExportAllowlist([]string{"*"}), approvedExportGate("alice", "bob"), auditor)
	plan := testFullApplyPlan()

	results, err := ex.Apply(context.Background(), plan, ExportSpec{Tenant: "tenant-a", RequestedBy: "fran"})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got, want := requestSequence(t, d.reqs), []string{
		"POST /policy-engines/pe-export/policies",
		"PATCH /policy-engines/pe-export/policies/pol-update",
		"DELETE /policy-engines/pe-export/policies/pol-delete",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("request sequence = %v, want %v", got, want)
	}
	if len(results) != 3 {
		t.Fatalf("results = %d, want 3", len(results))
	}
	if results[0].PolicyID != "pol-create" || results[0].Status != "CREATING" {
		t.Errorf("create result = %+v", results[0])
	}
	if results[1].PolicyID != "pol-update" || results[1].Status != "UPDATING" {
		t.Errorf("update result = %+v", results[1])
	}
	if results[2].PolicyID != "pol-delete" || results[2].Status != "DELETING" {
		t.Errorf("delete result = %+v", results[2])
	}

	createBody := decodeJSONBody(t, d.reqs[0].body)
	updateBody := decodeJSONBody(t, d.reqs[1].body)
	assertExportWriteBody(t, createBody, plan.PlanHash, plan.Creates[0].Name)
	assertExportWriteBody(t, updateBody, plan.PlanHash, plan.Updates[0].Name)
	if exportClientToken(plan.PlanHash, plan.Creates[0].Name) != exportClientToken(plan.PlanHash, plan.Creates[0].Name) {
		t.Fatal("client token must be stable for the same plan/name")
	}
	if d.reqs[2].body != "" {
		t.Fatalf("delete request body = %q, want empty", d.reqs[2].body)
	}
	if len(auditor.records) != 1 {
		t.Fatalf("audit records = %d, want 1", len(auditor.records))
	}
	rec := auditor.records[0]
	if !rec.Allowed || !rec.DualControl || rec.ApproverCount != 2 || rec.Failed != 0 {
		t.Errorf("audit record = %+v", rec)
	}
	if rec.Creates != 1 || rec.Updates != 1 || rec.Deletes != 1 {
		t.Errorf("audit counts = %d/%d/%d, want 1/1/1", rec.Creates, rec.Updates, rec.Deletes)
	}
}

func TestExporterApplyPartialFailureContinues(t *testing.T) {
	clearAWSEnv(t)
	d := newStub(t)
	d.onStatus(http.MethodPost, "/policy-engines/pe-export/policies", http.StatusAccepted, `{"policyId":"pol-create","policyArn":"arn:policy:create","status":"CREATING"}`)
	d.onStatus(http.MethodPatch, "/policy-engines/pe-export/policies/pol-update", http.StatusBadRequest, `{"message":"bad policy"}`)
	d.onStatus(http.MethodDelete, "/policy-engines/pe-export/policies/pol-delete", http.StatusAccepted, `{"policyId":"pol-delete","policyArn":"arn:policy:delete","status":"DELETING"}`)
	auditor := &recordingExportAuditor{}
	ex := newTestExporter(t, d, NewExportAllowlist([]string{"*"}), approvedExportGate("alice", "bob"), auditor)

	results, err := ex.Apply(context.Background(), testFullApplyPlan(), ExportSpec{Tenant: "tenant-a", RequestedBy: "fran"})
	var applyErr *ExportApplyError
	if !errors.As(err, &applyErr) {
		t.Fatalf("Apply error = %T %[1]v, want *ExportApplyError", err)
	}
	if applyErr.Failed != 1 || applyErr.Total != 3 {
		t.Fatalf("apply error = %+v, want failed=1 total=3", applyErr)
	}
	if got, want := requestSequence(t, d.reqs), []string{
		"POST /policy-engines/pe-export/policies",
		"PATCH /policy-engines/pe-export/policies/pol-update",
		"DELETE /policy-engines/pe-export/policies/pol-delete",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("request sequence = %v, want %v", got, want)
	}
	if len(results) != 3 || results[1].Err == nil || results[2].Status != "DELETING" {
		t.Fatalf("partial results = %+v", results)
	}
	if len(auditor.records) != 1 || auditor.records[0].Failed != 1 || !auditor.records[0].Allowed {
		t.Fatalf("audit records = %+v", auditor.records)
	}
}

func TestExporterPlanListsRemotePolicies(t *testing.T) {
	clearAWSEnv(t)
	d := newStub(t)
	d.on(http.MethodGet, "/policy-engines/pe-export/policies", d.fixture("export_remote_policies.json"))
	ex := newTestExporter(t, d, NewExportAllowlist([]string{"*"}), approvedExportGate("alice"), &recordingExportAuditor{})
	desired := []RenderedPolicy{
		testRenderedPolicy("olv_tenant_a_g_create000", "permit create;"),
		testRenderedPolicy("olv_tenant_a_g_unchanged", "permit unchanged;"),
	}
	plan, err := ex.Plan(context.Background(), "pe-export", "tenant-a", desired)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.EngineID != "pe-export" || plan.Tenant != "tenant-a" {
		t.Fatalf("plan identity = %q/%q", plan.EngineID, plan.Tenant)
	}
	if got, want := changeNames(plan.Creates), []string{"olv_tenant_a_g_create000"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("creates = %v, want %v", got, want)
	}
	if len(d.reqs) != 1 || !strings.Contains(d.reqs[0].url, "/policy-engines/pe-export/policies") {
		t.Fatalf("plan requests = %+v", d.reqs)
	}
}

func assertExportWriteBody(t *testing.T, body map[string]any, planHash, name string) {
	t.Helper()
	if body["validationMode"] != validationModeFailOnAnyFindings {
		t.Fatalf("validationMode = %v, want %s in body %+v", body["validationMode"], validationModeFailOnAnyFindings, body)
	}
	token, ok := body["clientToken"].(string)
	if !ok || len(token) != 64 {
		t.Fatalf("clientToken = %v, want 64 hex chars", body["clientToken"])
	}
	if token != exportClientToken(planHash, name) {
		t.Fatalf("clientToken = %q, want %q", token, exportClientToken(planHash, name))
	}
}

func decodeJSONBody(t *testing.T, raw string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode body %q: %v", raw, err)
	}
	return out
}

func requestSequence(t *testing.T, reqs []recordedReq) []string {
	t.Helper()
	out := make([]string, 0, len(reqs))
	for _, r := range reqs {
		u, err := url.Parse(r.url)
		if err != nil {
			t.Fatalf("parse request URL %q: %v", r.url, err)
		}
		out = append(out, r.method+" "+u.EscapedPath())
	}
	return out
}

func testApplyPlan() ExportPlan {
	return ExportPlan{
		EngineID: "pe-export",
		Tenant:   "tenant-a",
		PlanHash: strings.Repeat("a", 64),
		Creates: []PlannedChange{
			{
				Op:              exportOpCreate,
				Name:            "olv_tenant_a_g_create",
				Statement:       "permit create;",
				Description:     exportMarker("tenant-a", "permit create;"),
				EnforcementMode: enforcementModeActive,
			},
		},
	}
}

func testDeletePlan() ExportPlan {
	plan := testApplyPlan()
	plan.Creates = nil
	plan.Deletes = []PlannedChange{{
		Op:                    exportOpDelete,
		Name:                  "olv_tenant_a_g_delete",
		PolicyID:              "pol-delete",
		RemoteEnforcementMode: enforcementModeActive,
	}}
	return plan
}

func testDowngradePlan() ExportPlan {
	plan := testApplyPlan()
	plan.Creates = nil
	plan.Updates = []PlannedChange{{
		Op:                    exportOpUpdate,
		Name:                  "olv_tenant_a_g_update",
		PolicyID:              "pol-update",
		Statement:             "permit update;",
		Description:           exportMarker("tenant-a", "permit update;"),
		EnforcementMode:       enforcementModeLogOnly,
		RemoteEnforcementMode: enforcementModeActive,
	}}
	return plan
}

func testFullApplyPlan() ExportPlan {
	plan := testApplyPlan()
	plan.Updates = []PlannedChange{{
		Op:                    exportOpUpdate,
		Name:                  "olv_tenant_a_g_update",
		PolicyID:              "pol-update",
		Statement:             "permit update;",
		Description:           exportMarker("tenant-a", "permit update;"),
		EnforcementMode:       enforcementModeActive,
		RemoteEnforcementMode: enforcementModeActive,
	}}
	plan.Deletes = []PlannedChange{{
		Op:                    exportOpDelete,
		Name:                  "olv_tenant_a_g_delete",
		PolicyID:              "pol-delete",
		RemoteEnforcementMode: enforcementModeActive,
	}}
	return plan
}

func withPlanEngine(plan ExportPlan, engineID string) ExportPlan {
	plan.EngineID = engineID
	return plan
}

func withPlanHash(plan ExportPlan, planHash string) ExportPlan {
	plan.PlanHash = planHash
	return plan
}
