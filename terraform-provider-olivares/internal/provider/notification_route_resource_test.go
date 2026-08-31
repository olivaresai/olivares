// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/olivaresai/olivares/terraform-provider-olivares/internal/client"
)

func notificationRouteSchema(t *testing.T, r resource.Resource) rschema.Schema {
	t.Helper()
	var resp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}
	return resp.Schema
}

func rawFromRouteModel(t *testing.T, sch rschema.Schema, m notificationRouteResourceModel) tftypes.Value {
	t.Helper()
	st := tfsdk.State{Schema: sch}
	if d := st.Set(context.Background(), m); d.HasError() {
		t.Fatalf("set model: %v", d)
	}
	return st.Raw
}

func strList(ss ...string) types.List {
	vals := make([]attr.Value, 0, len(ss))
	for _, s := range ss {
		vals = append(vals, types.StringValue(s))
	}
	return types.ListValueMust(types.StringType, vals)
}

// TestNotificationRouteCreateReadDriftImport exercises the route lifecycle against
// a mock API: Create persists matchers + destination; Read surfaces an out-of-band
// destination change; ImportState seeds the id.
func TestNotificationRouteCreateReadDriftImport(t *testing.T) {
	ctx := context.Background()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "rt-1", "name": "crit-to-pager", "enabled": true,
				"match_types": []string{"finding"}, "match_kinds": []string{},
				"min_severity": "critical", "match_sources": []string{}, "match_subject_kinds": []string{},
				"destination": "pagerduty-oncall", "dedup_window_seconds": 300,
				"throttle_window_seconds": 0, "priority": 10,
				"owner_actor": "svc:terraform", "created_at": "2026-06-09T10:00:00Z",
			})
		case http.MethodGet:
			// Read returns a DRIFTED destination (re-pointed out of band).
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "rt-1", "name": "crit-to-pager", "enabled": true,
				"match_types": []string{"finding"}, "match_kinds": []string{},
				"min_severity": "critical", "match_sources": []string{}, "match_subject_kinds": []string{},
				"destination": "slack-incidents", "dedup_window_seconds": 300,
				"throttle_window_seconds": 0, "priority": 10,
				"owner_actor": "svc:terraform", "created_at": "2026-06-09T10:00:00Z",
			})
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer srv.Close()

	r := NewNotificationRouteResource()
	nr := r.(*notificationRouteResource)
	nr.Configure(ctx, resource.ConfigureRequest{
		ProviderData: &providerData{client: client.New(client.Options{Endpoint: srv.URL, APIToken: "tok", Tenant: "tenant"})},
	}, &resource.ConfigureResponse{})
	sch := notificationRouteSchema(t, r)

	configured := notificationRouteResourceModel{
		ID: types.StringNull(), Tenant: types.StringNull(), Name: types.StringValue("crit-to-pager"),
		Enabled: types.BoolValue(true), MatchTypes: strList("finding"), MatchKinds: strList(),
		MinSeverity: types.StringValue("critical"), MatchSources: strList(), MatchSubjectKinds: strList(),
		Destination: types.StringValue("pagerduty-oncall"), DedupWindowSeconds: types.Int64Value(300),
		ThrottleWindowSeconds: types.Int64Value(0), Priority: types.Int64Value(10),
		OwnerActor: types.StringNull(), CreatedAt: types.StringNull(),
	}

	createResp := resource.CreateResponse{State: tfsdk.State{Schema: sch}}
	nr.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: sch, Raw: rawFromRouteModel(t, sch, configured)}}, &createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("Create diagnostics: %v", createResp.Diagnostics)
	}
	var created notificationRouteResourceModel
	if d := createResp.State.Get(ctx, &created); d.HasError() {
		t.Fatalf("get created state: %v", d)
	}
	if created.ID.ValueString() != "rt-1" || created.Destination.ValueString() != "pagerduty-oncall" {
		t.Errorf("created id/destination = %q/%q, want rt-1/pagerduty-oncall", created.ID.ValueString(), created.Destination.ValueString())
	}
	if created.OwnerActor.ValueString() != "svc:terraform" {
		t.Errorf("owner_actor = %q, want svc:terraform", created.OwnerActor.ValueString())
	}

	readResp := resource.ReadResponse{State: tfsdk.State{Schema: sch}}
	nr.Read(ctx, resource.ReadRequest{State: tfsdk.State{Schema: sch, Raw: rawFromRouteModel(t, sch, created)}}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read diagnostics: %v", readResp.Diagnostics)
	}
	var refreshed notificationRouteResourceModel
	if d := readResp.State.Get(ctx, &refreshed); d.HasError() {
		t.Fatalf("get refreshed state: %v", d)
	}
	if refreshed.Destination.ValueString() != "slack-incidents" {
		t.Errorf("Read should surface drift: destination = %q, want slack-incidents", refreshed.Destination.ValueString())
	}

	nullModel := notificationRouteResourceModel{
		ID: types.StringNull(), Tenant: types.StringNull(), Name: types.StringNull(), Enabled: types.BoolNull(),
		MatchTypes: types.ListNull(types.StringType), MatchKinds: types.ListNull(types.StringType),
		MinSeverity: types.StringNull(), MatchSources: types.ListNull(types.StringType), MatchSubjectKinds: types.ListNull(types.StringType),
		Destination: types.StringNull(), DedupWindowSeconds: types.Int64Null(), ThrottleWindowSeconds: types.Int64Null(),
		Priority: types.Int64Null(), OwnerActor: types.StringNull(), CreatedAt: types.StringNull(),
	}
	importResp := resource.ImportStateResponse{State: tfsdk.State{Schema: sch, Raw: rawFromRouteModel(t, sch, nullModel)}}
	nr.ImportState(ctx, resource.ImportStateRequest{ID: "rt-1"}, &importResp)
	if importResp.Diagnostics.HasError() {
		t.Fatalf("ImportState diagnostics: %v", importResp.Diagnostics)
	}
	var imported types.String
	if d := importResp.State.GetAttribute(ctx, path.Root("id"), &imported); d.HasError() {
		t.Fatalf("get imported id: %v", d)
	}
	if imported.ValueString() != "rt-1" {
		t.Errorf("imported id = %q, want rt-1", imported.ValueString())
	}
}
