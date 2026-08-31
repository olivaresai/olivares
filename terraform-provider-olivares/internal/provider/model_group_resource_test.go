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

// modelGroupSchema returns the model group resource schema for test plumbing.
func modelGroupSchema(t *testing.T, r resource.Resource) rschema.Schema {
	t.Helper()
	var resp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}
	return resp.Schema
}

// rawFromModelGroupModel marshals a model into a conforming tftypes value.
func rawFromModelGroupModel(t *testing.T, sch rschema.Schema, m modelGroupResourceModel) tftypes.Value {
	t.Helper()
	st := tfsdk.State{Schema: sch}
	if d := st.Set(context.Background(), m); d.HasError() {
		t.Fatalf("set model: %v", d)
	}
	return st.Raw
}

// TestModelGroupResourceCreateReadDriftImport exercises the full model group
// lifecycle against a mock control-plane API: Create persists and records the
// engine response; Read detects out-of-band drift (the name changed
// server-side); ImportState seeds the id. This is the DoD's "create/read/import
// + detect drift".
func TestModelGroupResourceCreateReadDriftImport(t *testing.T) {
	ctx := context.Background()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			// Echo back the create with server-assigned fields.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "mg-1", "name": "tier-1-models",
				"description": "Production-grade models",
				"models":      []string{"gpt-4o", "claude-sonnet-4"},
				"created_at":  "2026-06-30T10:00:00Z",
				"updated_at":  "2026-06-30T10:00:00Z",
			})
		case http.MethodGet:
			// Read returns a DRIFTED name (renamed out of band).
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "mg-1", "name": "tier-1-models-renamed",
				"description": "Production-grade models",
				"models":      []string{"gpt-4o", "claude-sonnet-4"},
				"created_at":  "2026-06-30T10:00:00Z",
				"updated_at":  "2026-06-30T10:05:00Z",
			})
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer srv.Close()

	r := NewModelGroupResource()
	mgr := r.(*modelGroupResource)
	mgr.Configure(ctx, resource.ConfigureRequest{
		ProviderData: &providerData{client: client.New(client.Options{Endpoint: srv.URL, APIToken: "tok", Tenant: "tenant"})},
	}, &resource.ConfigureResponse{})

	sch := modelGroupSchema(t, r)

	configured := modelGroupResourceModel{
		ID: types.StringNull(), Tenant: types.StringNull(),
		Name:        types.StringValue("tier-1-models"),
		Description: types.StringValue("Production-grade models"),
		Models: types.ListValueMust(types.StringType, []attr.Value{
			types.StringValue("gpt-4o"), types.StringValue("claude-sonnet-4"),
		}),
		CreatedAt: types.StringUnknown(),
		UpdatedAt: types.StringUnknown(),
	}

	// --- Create ---
	createResp := resource.CreateResponse{State: tfsdk.State{Schema: sch}}
	mgr.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: sch, Raw: rawFromModelGroupModel(t, sch, configured)}}, &createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("Create diagnostics: %v", createResp.Diagnostics)
	}
	var created modelGroupResourceModel
	if d := createResp.State.Get(ctx, &created); d.HasError() {
		t.Fatalf("get created state: %v", d)
	}
	if created.ID.ValueString() != "mg-1" {
		t.Errorf("created id = %q, want mg-1", created.ID.ValueString())
	}
	if created.Name.ValueString() != "tier-1-models" {
		t.Errorf("created name = %q, want tier-1-models", created.Name.ValueString())
	}

	// --- Read (drift) ---
	readResp := resource.ReadResponse{State: tfsdk.State{Schema: sch}}
	mgr.Read(ctx, resource.ReadRequest{State: tfsdk.State{Schema: sch, Raw: rawFromModelGroupModel(t, sch, created)}}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read diagnostics: %v", readResp.Diagnostics)
	}
	var refreshed modelGroupResourceModel
	if d := readResp.State.Get(ctx, &refreshed); d.HasError() {
		t.Fatalf("get refreshed state: %v", d)
	}
	if refreshed.Name.ValueString() != "tier-1-models-renamed" {
		t.Errorf("Read should surface drift: name = %q, want tier-1-models-renamed", refreshed.Name.ValueString())
	}

	// --- ImportState ---
	nullModel := modelGroupResourceModel{
		ID: types.StringNull(), Tenant: types.StringNull(),
		Name: types.StringNull(), Description: types.StringNull(),
		Models:    types.ListNull(types.StringType),
		CreatedAt: types.StringNull(),
		UpdatedAt: types.StringNull(),
	}
	importResp := resource.ImportStateResponse{State: tfsdk.State{Schema: sch, Raw: rawFromModelGroupModel(t, sch, nullModel)}}
	mgr.ImportState(ctx, resource.ImportStateRequest{ID: "mg-1"}, &importResp)
	if importResp.Diagnostics.HasError() {
		t.Fatalf("ImportState diagnostics: %v", importResp.Diagnostics)
	}
	var imported types.String
	if d := importResp.State.GetAttribute(ctx, path.Root("id"), &imported); d.HasError() {
		t.Fatalf("get imported id: %v", d)
	}
	if imported.ValueString() != "mg-1" {
		t.Errorf("imported id = %q, want mg-1", imported.ValueString())
	}
}
