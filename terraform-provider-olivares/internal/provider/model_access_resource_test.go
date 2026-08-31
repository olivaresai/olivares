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

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/olivaresai/olivares/terraform-provider-olivares/internal/client"
)

// modelAccessSchema returns the model access resource schema for test plumbing.
func modelAccessSchema(t *testing.T, r resource.Resource) rschema.Schema {
	t.Helper()
	var resp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}
	return resp.Schema
}

// rawFromModelAccessModel marshals a model into a conforming tftypes value.
func rawFromModelAccessModel(t *testing.T, sch rschema.Schema, m modelAccessResourceModel) tftypes.Value {
	t.Helper()
	st := tfsdk.State{Schema: sch}
	if d := st.Set(context.Background(), m); d.HasError() {
		t.Fatalf("set model: %v", d)
	}
	return st.Raw
}

// TestModelAccessResourceCreateReadDriftImport exercises the full model access
// lifecycle against a mock control-plane API: Create persists and records the
// engine-filled defaults; Read detects out-of-band drift (the effect changed
// server-side); ImportState seeds the id. This is the DoD's "create/read/import
// + detect drift".
func TestModelAccessResourceCreateReadDriftImport(t *testing.T) {
	ctx := context.Background()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			// Echo back the create with server-assigned fields.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "ma-1", "subject_type": "role", "subject_ref": "engineering",
				"model_pattern": "gpt-4*", "effect": "allow", "priority": 10,
				"created_at": "2026-06-30T10:00:00Z", "updated_at": "2026-06-30T10:00:00Z",
			})
		case http.MethodGet:
			// Read returns a DRIFTED effect (changed out of band from allow to deny).
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "ma-1", "subject_type": "role", "subject_ref": "engineering",
				"model_pattern": "gpt-4*", "effect": "deny", "priority": 10,
				"created_at": "2026-06-30T10:00:00Z", "updated_at": "2026-06-30T11:00:00Z",
			})
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer srv.Close()

	r := NewModelAccessResource()
	mar := r.(*modelAccessResource)
	mar.Configure(ctx, resource.ConfigureRequest{
		ProviderData: &providerData{client: client.New(client.Options{Endpoint: srv.URL, APIToken: "tok", Tenant: "tenant"})},
	}, &resource.ConfigureResponse{})

	sch := modelAccessSchema(t, r)

	configured := modelAccessResourceModel{
		ID: types.StringNull(), Tenant: types.StringNull(),
		SubjectType: types.StringValue("role"), SubjectRef: types.StringValue("engineering"),
		ModelPattern: types.StringValue("gpt-4*"), Effect: types.StringValue("allow"),
		Priority: types.Int64Value(10), CreatedAt: types.StringUnknown(), UpdatedAt: types.StringUnknown(),
	}

	// --- Create ---
	createResp := resource.CreateResponse{State: tfsdk.State{Schema: sch}}
	mar.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: sch, Raw: rawFromModelAccessModel(t, sch, configured)}}, &createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("Create diagnostics: %v", createResp.Diagnostics)
	}
	var created modelAccessResourceModel
	if d := createResp.State.Get(ctx, &created); d.HasError() {
		t.Fatalf("get created state: %v", d)
	}
	if created.ID.ValueString() != "ma-1" {
		t.Errorf("created id = %q, want ma-1", created.ID.ValueString())
	}
	if created.Effect.ValueString() != "allow" {
		t.Errorf("created effect = %q, want allow", created.Effect.ValueString())
	}
	if created.Priority.ValueInt64() != 10 {
		t.Errorf("created priority = %d, want 10", created.Priority.ValueInt64())
	}

	// --- Read (drift) ---
	readResp := resource.ReadResponse{State: tfsdk.State{Schema: sch}}
	mar.Read(ctx, resource.ReadRequest{State: tfsdk.State{Schema: sch, Raw: rawFromModelAccessModel(t, sch, created)}}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read diagnostics: %v", readResp.Diagnostics)
	}
	var refreshed modelAccessResourceModel
	if d := readResp.State.Get(ctx, &refreshed); d.HasError() {
		t.Fatalf("get refreshed state: %v", d)
	}
	if refreshed.Effect.ValueString() != "deny" {
		t.Errorf("Read should surface drift: effect = %q, want deny", refreshed.Effect.ValueString())
	}

	// --- ImportState ---
	nullModel := modelAccessResourceModel{
		ID: types.StringNull(), Tenant: types.StringNull(),
		SubjectType: types.StringNull(), SubjectRef: types.StringNull(),
		ModelPattern: types.StringNull(), Effect: types.StringNull(),
		Priority: types.Int64Null(), CreatedAt: types.StringNull(), UpdatedAt: types.StringNull(),
	}
	importResp := resource.ImportStateResponse{State: tfsdk.State{Schema: sch, Raw: rawFromModelAccessModel(t, sch, nullModel)}}
	mar.ImportState(ctx, resource.ImportStateRequest{ID: "ma-1"}, &importResp)
	if importResp.Diagnostics.HasError() {
		t.Fatalf("ImportState diagnostics: %v", importResp.Diagnostics)
	}
	var imported types.String
	if d := importResp.State.GetAttribute(ctx, path.Root("id"), &imported); d.HasError() {
		t.Fatalf("get imported id: %v", d)
	}
	if imported.ValueString() != "ma-1" {
		t.Errorf("imported id = %q, want ma-1", imported.ValueString())
	}
}
