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

// budgetSchema returns the budget resource schema for test plumbing.
func budgetSchema(t *testing.T, r resource.Resource) rschema.Schema {
	t.Helper()
	var resp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}
	return resp.Schema
}

// rawFromBudgetModel marshals a model into a conforming tftypes value.
func rawFromBudgetModel(t *testing.T, sch rschema.Schema, m budgetResourceModel) tftypes.Value {
	t.Helper()
	st := tfsdk.State{Schema: sch}
	if d := st.Set(context.Background(), m); d.HasError() {
		t.Fatalf("set model: %v", d)
	}
	return st.Raw
}

// TestBudgetResourceCreateReadDriftImport exercises the full budget lifecycle
// against a mock control-plane API: Create persists and records the engine-filled
// defaults; Read detects out-of-band drift (the limit changed server-side);
// ImportState seeds the id. This is the DoD's "create/read/import + detect drift".
func TestBudgetResourceCreateReadDriftImport(t *testing.T) {
	ctx := context.Background()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			// Echo back the create with engine defaults filled in.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "bud-1", "name": "monthly-cap", "enabled": true,
				"dimension": "global", "limit_micro_usd": 500_000_000,
				"period": "monthly", "currency": "USD", "action": "alert",
				"thresholds": []float64{0.8, 1.0},
			})
		case http.MethodGet:
			// Read returns a DRIFTED limit (raised out of band).
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "bud-1", "name": "monthly-cap", "enabled": true,
				"dimension": "global", "limit_micro_usd": 999_000_000,
				"period": "monthly", "currency": "USD", "action": "alert",
				"thresholds": []float64{0.8, 1.0},
			})
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer srv.Close()

	r := NewBudgetResource()
	br := r.(*budgetResource)
	br.Configure(ctx, resource.ConfigureRequest{
		ProviderData: &providerData{client: client.New(client.Options{Endpoint: srv.URL, APIToken: "tok", Tenant: "tenant"})},
	}, &resource.ConfigureResponse{})

	sch := budgetSchema(t, r)

	configured := budgetResourceModel{
		ID: types.StringNull(), Tenant: types.StringNull(), Name: types.StringValue("monthly-cap"),
		Enabled: types.BoolValue(true), Dimension: types.StringValue("global"), Key: types.StringNull(),
		LimitMicroUSD: types.Int64Value(500_000_000), Period: types.StringValue("monthly"),
		Thresholds: types.ListValueMust(types.Float64Type, []attr.Value{types.Float64Value(0.8), types.Float64Value(1.0)}),
		Currency:   types.StringValue("USD"), Action: types.StringValue("alert"), ReservedMicroUSD: types.Int64Value(0),
	}

	// --- Create ---
	createResp := resource.CreateResponse{State: tfsdk.State{Schema: sch}}
	br.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: sch, Raw: rawFromBudgetModel(t, sch, configured)}}, &createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("Create diagnostics: %v", createResp.Diagnostics)
	}
	var created budgetResourceModel
	if d := createResp.State.Get(ctx, &created); d.HasError() {
		t.Fatalf("get created state: %v", d)
	}
	if created.ID.ValueString() != "bud-1" {
		t.Errorf("created id = %q, want bud-1", created.ID.ValueString())
	}
	if created.LimitMicroUSD.ValueInt64() != 500_000_000 {
		t.Errorf("created limit = %d, want 500000000", created.LimitMicroUSD.ValueInt64())
	}

	// --- Read (drift) ---
	readResp := resource.ReadResponse{State: tfsdk.State{Schema: sch}}
	br.Read(ctx, resource.ReadRequest{State: tfsdk.State{Schema: sch, Raw: rawFromBudgetModel(t, sch, created)}}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read diagnostics: %v", readResp.Diagnostics)
	}
	var refreshed budgetResourceModel
	if d := readResp.State.Get(ctx, &refreshed); d.HasError() {
		t.Fatalf("get refreshed state: %v", d)
	}
	if refreshed.LimitMicroUSD.ValueInt64() != 999_000_000 {
		t.Errorf("Read should surface drift: limit = %d, want 999000000", refreshed.LimitMicroUSD.ValueInt64())
	}

	// --- ImportState ---
	nullModel := budgetResourceModel{
		ID: types.StringNull(), Tenant: types.StringNull(), Name: types.StringNull(), Enabled: types.BoolNull(),
		Dimension: types.StringNull(), Key: types.StringNull(), LimitMicroUSD: types.Int64Null(),
		Period: types.StringNull(), Thresholds: types.ListNull(types.Float64Type), Currency: types.StringNull(),
		Action: types.StringNull(), ReservedMicroUSD: types.Int64Null(),
	}
	importResp := resource.ImportStateResponse{State: tfsdk.State{Schema: sch, Raw: rawFromBudgetModel(t, sch, nullModel)}}
	br.ImportState(ctx, resource.ImportStateRequest{ID: "bud-1"}, &importResp)
	if importResp.Diagnostics.HasError() {
		t.Fatalf("ImportState diagnostics: %v", importResp.Diagnostics)
	}
	var imported types.String
	if d := importResp.State.GetAttribute(ctx, path.Root("id"), &imported); d.HasError() {
		t.Fatalf("get imported id: %v", d)
	}
	if imported.ValueString() != "bud-1" {
		t.Errorf("imported id = %q, want bud-1", imported.ValueString())
	}
}

// TestBudgetValidateConfigKeyRequired confirms a non-global dimension without a
// key is rejected at plan time (mirroring the engine's server-side rule).
func TestBudgetValidateConfigKeyRequired(t *testing.T) {
	ctx := context.Background()
	r := NewBudgetResource().(*budgetResource)
	sch := budgetSchema(t, r)

	cfg := budgetResourceModel{
		ID: types.StringNull(), Tenant: types.StringNull(), Name: types.StringValue("model-cap"),
		Enabled: types.BoolValue(true), Dimension: types.StringValue("model"), Key: types.StringNull(),
		LimitMicroUSD: types.Int64Value(1_000_000), Period: types.StringValue("monthly"),
		Thresholds: types.ListNull(types.Float64Type), Currency: types.StringValue("USD"),
		Action: types.StringValue("alert"), ReservedMicroUSD: types.Int64Value(0),
	}
	resp := resource.ValidateConfigResponse{}
	r.ValidateConfig(ctx, resource.ValidateConfigRequest{
		Config: tfsdk.Config{Schema: sch, Raw: rawFromBudgetModel(t, sch, cfg)},
	}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("a non-global dimension without a key must be a config error")
	}
}
