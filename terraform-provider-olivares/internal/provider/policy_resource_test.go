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

// policySchema returns the policy resource schema for test plumbing.
func policySchema(t *testing.T, r resource.Resource) rschema.Schema {
	t.Helper()
	var resp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}
	return resp.Schema
}

// rawFromPolicyModel marshals a model into a conforming tftypes value via a
// throwaway State (which has Set), so a Plan/State can be built for the handlers.
func rawFromPolicyModel(t *testing.T, sch rschema.Schema, m policyResourceModel) tftypes.Value {
	t.Helper()
	st := tfsdk.State{Schema: sch}
	if d := st.Set(context.Background(), m); d.HasError() {
		t.Fatalf("set model: %v", d)
	}
	return st.Raw
}

// TestPolicyResourceCreateReadDriftImport exercises the full resource lifecycle
// against a mock control-plane API: Create persists and records the canonical
// spec; Read detects out-of-band drift (the canonical spec changes); ImportState
// seeds the id. This is the DoD's "create/read/import against a mock + detect drift".
func TestPolicyResourceCreateReadDriftImport(t *testing.T) {
	ctx := context.Background()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			// Create echoes back the canonical spec exactly as configured.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "pol-1", "name": "deny-agent-write", "kind": "abac", "enabled": true,
				"spec": map[string]any{"rules": []any{map[string]any{"deny": true, "verb": "write", "resource": "agent"}}},
			})
		case http.MethodGet:
			// Read returns a DRIFTED canonical spec (an extra rule added out of band).
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "pol-1", "name": "deny-agent-write", "kind": "abac", "enabled": true,
				"spec": map[string]any{"rules": []any{
					map[string]any{"deny": true, "verb": "write", "resource": "agent"},
					map[string]any{"deny": true, "verb": "admin", "resource": "agent"},
				}},
			})
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer srv.Close()

	r := NewPolicyResource()
	pr := r.(*policyResource)
	pr.Configure(ctx, resource.ConfigureRequest{
		ProviderData: &providerData{client: client.New(client.Options{Endpoint: srv.URL, APIToken: "tok", Tenant: "tenant"})},
	}, &resource.ConfigureResponse{})

	sch := policySchema(t, r)

	configured := policyResourceModel{
		ID:            types.StringNull(),
		Tenant:        types.StringNull(),
		Name:          types.StringValue("deny-agent-write"),
		Kind:          types.StringValue("abac"),
		Enabled:       types.BoolValue(true),
		Spec:          types.StringValue(`{"rules":[{"deny":true,"verb":"write","resource":"agent"}]}`),
		SpecCanonical: types.StringNull(),
	}

	// --- Create ---
	createResp := resource.CreateResponse{State: tfsdk.State{Schema: sch}}
	pr.Create(ctx, resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: sch, Raw: rawFromPolicyModel(t, sch, configured)},
	}, &createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("Create diagnostics: %v", createResp.Diagnostics)
	}
	var created policyResourceModel
	if d := createResp.State.Get(ctx, &created); d.HasError() {
		t.Fatalf("get created state: %v", d)
	}
	if created.ID.ValueString() != "pol-1" {
		t.Errorf("created id = %q, want pol-1", created.ID.ValueString())
	}
	if created.Spec.ValueString() != configured.Spec.ValueString() {
		t.Errorf("configured spec must be preserved, got %q", created.Spec.ValueString())
	}
	createdCanonical := created.SpecCanonical.ValueString()
	if createdCanonical == "" {
		t.Fatal("spec_canonical should be set after create")
	}

	// --- Read (drift) ---
	readResp := resource.ReadResponse{State: tfsdk.State{Schema: sch}}
	pr.Read(ctx, resource.ReadRequest{
		State: tfsdk.State{Schema: sch, Raw: rawFromPolicyModel(t, sch, created)},
	}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read diagnostics: %v", readResp.Diagnostics)
	}
	var refreshed policyResourceModel
	if d := readResp.State.Get(ctx, &refreshed); d.HasError() {
		t.Fatalf("get refreshed state: %v", d)
	}
	if refreshed.SpecCanonical.ValueString() == createdCanonical {
		t.Error("Read should surface drift: spec_canonical must change after the out-of-band edit")
	}
	if refreshed.Spec.ValueString() != configured.Spec.ValueString() {
		t.Error("Read must NOT clobber the configured spec")
	}

	// --- ImportState ---
	// Seed the import state with an all-null (but known) object, as the framework
	// does before calling ImportState, so the id passthrough can write into it.
	nullModel := policyResourceModel{
		ID: types.StringNull(), Tenant: types.StringNull(), Name: types.StringNull(),
		Kind: types.StringNull(), Enabled: types.BoolNull(), Spec: types.StringNull(), SpecCanonical: types.StringNull(),
	}
	importResp := resource.ImportStateResponse{State: tfsdk.State{Schema: sch, Raw: rawFromPolicyModel(t, sch, nullModel)}}
	pr.ImportState(ctx, resource.ImportStateRequest{ID: "pol-1"}, &importResp)
	if importResp.Diagnostics.HasError() {
		t.Fatalf("ImportState diagnostics: %v", importResp.Diagnostics)
	}
	var imported types.String
	if d := importResp.State.GetAttribute(ctx, path.Root("id"), &imported); d.HasError() {
		t.Fatalf("get imported id: %v", d)
	}
	if imported.ValueString() != "pol-1" {
		t.Errorf("imported id = %q, want pol-1", imported.ValueString())
	}
}
