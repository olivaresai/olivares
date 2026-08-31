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

// workspaceSchema returns the workspace resource schema for test plumbing.
func workspaceSchema(t *testing.T, r resource.Resource) rschema.Schema {
	t.Helper()
	var resp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}
	return resp.Schema
}

// rawFromWorkspaceModel marshals a model into a conforming tftypes value.
func rawFromWorkspaceModel(t *testing.T, sch rschema.Schema, m workspaceResourceModel) tftypes.Value {
	t.Helper()
	st := tfsdk.State{Schema: sch}
	if d := st.Set(context.Background(), m); d.HasError() {
		t.Fatalf("set model: %v", d)
	}
	return st.Raw
}

// TestWorkspaceResourceCreateReadDriftImport exercises the full workspace
// lifecycle against a mock control-plane API: Create persists and records
// the server-assigned ref and status; Read detects out-of-band drift (the
// description changed server-side); ImportState seeds the ref.
func TestWorkspaceResourceCreateReadDriftImport(t *testing.T) {
	ctx := context.Background()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			// Echo back the create with server-assigned fields filled in.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ref": "ws-abc123", "name": "dev-sandbox",
				"description": "Development sandbox",
				"status":      "active",
				"created_at":  "2026-06-30T10:00:00Z",
				"updated_at":  "2026-06-30T10:00:00Z",
			})
		case http.MethodGet:
			// Read returns a DRIFTED description (changed out of band).
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ref": "ws-abc123", "name": "dev-sandbox",
				"description": "Updated by someone else",
				"status":      "active",
				"created_at":  "2026-06-30T10:00:00Z",
				"updated_at":  "2026-06-30T11:00:00Z",
			})
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer srv.Close()

	r := NewWorkspaceResource()
	wr := r.(*workspaceResource)
	wr.Configure(ctx, resource.ConfigureRequest{
		ProviderData: &providerData{client: client.New(client.Options{Endpoint: srv.URL, APIToken: "tok", Tenant: "tenant"})},
	}, &resource.ConfigureResponse{})

	sch := workspaceSchema(t, r)

	configured := workspaceResourceModel{
		Ref:         types.StringUnknown(),
		Tenant:      types.StringNull(),
		Name:        types.StringValue("dev-sandbox"),
		Description: types.StringValue("Development sandbox"),
		Status:      types.StringUnknown(),
		CreatedAt:   types.StringUnknown(),
		UpdatedAt:   types.StringUnknown(),
	}

	// --- Create ---
	createResp := resource.CreateResponse{State: tfsdk.State{Schema: sch}}
	wr.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: sch, Raw: rawFromWorkspaceModel(t, sch, configured)}}, &createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("Create diagnostics: %v", createResp.Diagnostics)
	}
	var created workspaceResourceModel
	if d := createResp.State.Get(ctx, &created); d.HasError() {
		t.Fatalf("get created state: %v", d)
	}
	if created.Ref.ValueString() != "ws-abc123" {
		t.Errorf("created ref = %q, want ws-abc123", created.Ref.ValueString())
	}
	if created.Status.ValueString() != "active" {
		t.Errorf("created status = %q, want active", created.Status.ValueString())
	}
	if created.Description.ValueString() != "Development sandbox" {
		t.Errorf("created description = %q, want \"Development sandbox\"", created.Description.ValueString())
	}

	// --- Read (drift) ---
	readResp := resource.ReadResponse{State: tfsdk.State{Schema: sch}}
	wr.Read(ctx, resource.ReadRequest{State: tfsdk.State{Schema: sch, Raw: rawFromWorkspaceModel(t, sch, created)}}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read diagnostics: %v", readResp.Diagnostics)
	}
	var refreshed workspaceResourceModel
	if d := readResp.State.Get(ctx, &refreshed); d.HasError() {
		t.Fatalf("get refreshed state: %v", d)
	}
	if refreshed.Description.ValueString() != "Updated by someone else" {
		t.Errorf("Read should surface drift: description = %q, want \"Updated by someone else\"", refreshed.Description.ValueString())
	}
	if refreshed.UpdatedAt.ValueString() != "2026-06-30T11:00:00Z" {
		t.Errorf("Read should surface drifted updated_at = %q, want 2026-06-30T11:00:00Z", refreshed.UpdatedAt.ValueString())
	}

	// --- ImportState ---
	nullModel := workspaceResourceModel{
		Ref: types.StringNull(), Tenant: types.StringNull(), Name: types.StringNull(),
		Description: types.StringNull(), Status: types.StringNull(),
		CreatedAt: types.StringNull(), UpdatedAt: types.StringNull(),
	}
	importResp := resource.ImportStateResponse{State: tfsdk.State{Schema: sch, Raw: rawFromWorkspaceModel(t, sch, nullModel)}}
	wr.ImportState(ctx, resource.ImportStateRequest{ID: "ws-abc123"}, &importResp)
	if importResp.Diagnostics.HasError() {
		t.Fatalf("ImportState diagnostics: %v", importResp.Diagnostics)
	}
	var imported types.String
	if d := importResp.State.GetAttribute(ctx, path.Root("ref"), &imported); d.HasError() {
		t.Fatalf("get imported ref: %v", d)
	}
	if imported.ValueString() != "ws-abc123" {
		t.Errorf("imported ref = %q, want ws-abc123", imported.ValueString())
	}
}

// TestWorkspaceResourceDelete confirms the delete path calls the API without
// error on a successful 204.
func TestWorkspaceResourceDelete(t *testing.T) {
	ctx := context.Background()

	var deleteCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleteCalled = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		t.Errorf("unexpected method %s", r.Method)
	}))
	defer srv.Close()

	r := NewWorkspaceResource()
	wr := r.(*workspaceResource)
	wr.Configure(ctx, resource.ConfigureRequest{
		ProviderData: &providerData{client: client.New(client.Options{Endpoint: srv.URL, APIToken: "tok", Tenant: "tenant"})},
	}, &resource.ConfigureResponse{})

	sch := workspaceSchema(t, r)

	state := workspaceResourceModel{
		Ref: types.StringValue("ws-abc123"), Tenant: types.StringNull(),
		Name: types.StringValue("dev-sandbox"), Description: types.StringNull(),
		Status: types.StringValue("active"), CreatedAt: types.StringValue("2026-06-30T10:00:00Z"),
		UpdatedAt: types.StringValue("2026-06-30T10:00:00Z"),
	}

	deleteResp := resource.DeleteResponse{State: tfsdk.State{Schema: sch}}
	wr.Delete(ctx, resource.DeleteRequest{State: tfsdk.State{Schema: sch, Raw: rawFromWorkspaceModel(t, sch, state)}}, &deleteResp)
	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("Delete diagnostics: %v", deleteResp.Diagnostics)
	}
	if !deleteCalled {
		t.Error("expected DELETE to be called on the mock server")
	}
}
