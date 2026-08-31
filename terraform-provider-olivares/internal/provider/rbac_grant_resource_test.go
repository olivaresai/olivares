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

// rbacGrantSchema returns the RBAC grant resource schema for test plumbing.
func rbacGrantSchema(t *testing.T, r resource.Resource) rschema.Schema {
	t.Helper()
	var resp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}
	return resp.Schema
}

// rawFromRBACGrantModel marshals a model into a conforming tftypes value.
func rawFromRBACGrantModel(t *testing.T, sch rschema.Schema, m rbacGrantResourceModel) tftypes.Value {
	t.Helper()
	st := tfsdk.State{Schema: sch}
	if d := st.Set(context.Background(), m); d.HasError() {
		t.Fatalf("set model: %v", d)
	}
	return st.Raw
}

// TestRBACGrantResourceCreateReadDriftImport exercises the full RBAC grant
// lifecycle against a mock control-plane API: Create persists and records the
// server-assigned id and timestamp; Read detects out-of-band drift (the role
// changed server-side); ImportState seeds the id. This is the DoD's
// "create/read/import + detect drift".
func TestRBACGrantResourceCreateReadDriftImport(t *testing.T) {
	ctx := context.Background()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			// Echo back the create with server-assigned id and timestamp.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "grant-1", "subject_type": "user",
				"subject_ref": "alice@example.com", "role": "editor",
				"scope": "project", "scope_ref": "proj-42",
				"created_at": "2026-06-30T10:00:00Z",
			})
		case http.MethodGet:
			// Read returns a DRIFTED role (changed out of band to
			// illustrate drift detection on the immutable grant).
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "grant-1", "subject_type": "user",
				"subject_ref": "alice@example.com", "role": "admin",
				"scope": "project", "scope_ref": "proj-42",
				"created_at": "2026-06-30T10:00:00Z",
			})
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer srv.Close()

	r := NewRBACGrantResource()
	gr := r.(*rbacGrantResource)
	gr.Configure(ctx, resource.ConfigureRequest{
		ProviderData: &providerData{client: client.New(client.Options{Endpoint: srv.URL, APIToken: "tok", Tenant: "tenant"})},
	}, &resource.ConfigureResponse{})

	sch := rbacGrantSchema(t, r)

	configured := rbacGrantResourceModel{
		ID: types.StringNull(), Tenant: types.StringNull(),
		SubjectType: types.StringValue("user"),
		SubjectRef:  types.StringValue("alice@example.com"),
		Role:        types.StringValue("editor"),
		Scope:       types.StringValue("project"),
		ScopeRef:    types.StringValue("proj-42"),
		CreatedAt:   types.StringNull(),
	}

	// --- Create ---
	createResp := resource.CreateResponse{State: tfsdk.State{Schema: sch}}
	gr.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: sch, Raw: rawFromRBACGrantModel(t, sch, configured)}}, &createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("Create diagnostics: %v", createResp.Diagnostics)
	}
	var created rbacGrantResourceModel
	if d := createResp.State.Get(ctx, &created); d.HasError() {
		t.Fatalf("get created state: %v", d)
	}
	if created.ID.ValueString() != "grant-1" {
		t.Errorf("created id = %q, want grant-1", created.ID.ValueString())
	}
	if created.Role.ValueString() != "editor" {
		t.Errorf("created role = %q, want editor", created.Role.ValueString())
	}
	if created.CreatedAt.ValueString() != "2026-06-30T10:00:00Z" {
		t.Errorf("created_at = %q, want 2026-06-30T10:00:00Z", created.CreatedAt.ValueString())
	}

	// --- Read (drift) ---
	readResp := resource.ReadResponse{State: tfsdk.State{Schema: sch}}
	gr.Read(ctx, resource.ReadRequest{State: tfsdk.State{Schema: sch, Raw: rawFromRBACGrantModel(t, sch, created)}}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read diagnostics: %v", readResp.Diagnostics)
	}
	var refreshed rbacGrantResourceModel
	if d := readResp.State.Get(ctx, &refreshed); d.HasError() {
		t.Fatalf("get refreshed state: %v", d)
	}
	if refreshed.Role.ValueString() != "admin" {
		t.Errorf("Read should surface drift: role = %q, want admin", refreshed.Role.ValueString())
	}

	// --- ImportState ---
	nullModel := rbacGrantResourceModel{
		ID: types.StringNull(), Tenant: types.StringNull(),
		SubjectType: types.StringNull(), SubjectRef: types.StringNull(),
		Role: types.StringNull(), Scope: types.StringNull(),
		ScopeRef: types.StringNull(), CreatedAt: types.StringNull(),
	}
	importResp := resource.ImportStateResponse{State: tfsdk.State{Schema: sch, Raw: rawFromRBACGrantModel(t, sch, nullModel)}}
	gr.ImportState(ctx, resource.ImportStateRequest{ID: "grant-1"}, &importResp)
	if importResp.Diagnostics.HasError() {
		t.Fatalf("ImportState diagnostics: %v", importResp.Diagnostics)
	}
	var imported types.String
	if d := importResp.State.GetAttribute(ctx, path.Root("id"), &imported); d.HasError() {
		t.Fatalf("get imported id: %v", d)
	}
	if imported.ValueString() != "grant-1" {
		t.Errorf("imported id = %q, want grant-1", imported.ValueString())
	}
}

// TestRBACGrantResourceDeleteNotFound confirms that deleting a grant whose
// server returns 404 (already-deleted) succeeds without error (idempotent).
func TestRBACGrantResourceDeleteNotFound(t *testing.T) {
	ctx := context.Background()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		t.Errorf("unexpected method %s", r.Method)
	}))
	defer srv.Close()

	r := NewRBACGrantResource()
	gr := r.(*rbacGrantResource)
	gr.Configure(ctx, resource.ConfigureRequest{
		ProviderData: &providerData{client: client.New(client.Options{Endpoint: srv.URL, APIToken: "tok", Tenant: "tenant"})},
	}, &resource.ConfigureResponse{})

	sch := rbacGrantSchema(t, r)

	state := rbacGrantResourceModel{
		ID: types.StringValue("grant-gone"), Tenant: types.StringNull(),
		SubjectType: types.StringValue("user"), SubjectRef: types.StringValue("bob@example.com"),
		Role: types.StringValue("viewer"), Scope: types.StringNull(),
		ScopeRef: types.StringNull(), CreatedAt: types.StringValue("2026-06-30T10:00:00Z"),
	}

	deleteResp := resource.DeleteResponse{State: tfsdk.State{Schema: sch}}
	gr.Delete(ctx, resource.DeleteRequest{State: tfsdk.State{Schema: sch, Raw: rawFromRBACGrantModel(t, sch, state)}}, &deleteResp)
	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("Delete diagnostics (should be idempotent on 404): %v", deleteResp.Diagnostics)
	}
}
