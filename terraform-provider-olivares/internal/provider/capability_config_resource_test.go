// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

func capabilityConfigSchema(t *testing.T, r resource.Resource) rschema.Schema {
	t.Helper()
	var resp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}
	return resp.Schema
}

func rawFromCapabilityConfigModel(t *testing.T, sch rschema.Schema, m capabilityConfigResourceModel) tftypes.Value {
	t.Helper()
	st := tfsdk.State{Schema: sch}
	if d := st.Set(context.Background(), m); d.HasError() {
		t.Fatalf("set model: %v", d)
	}
	return st.Raw
}

func secretRefList(refs ...capabilitySecretRefModel) types.List {
	if len(refs) == 0 {
		return types.ListNull(secretRefObjectType())
	}
	vals := make([]attr.Value, 0, len(refs))
	for _, ref := range refs {
		obj, _ := types.ObjectValueFrom(context.Background(), secretRefObjectType().AttrTypes, ref)
		vals = append(vals, obj)
	}
	return types.ListValueMust(secretRefObjectType(), vals)
}

// TestCapabilityConfigCreateReadDriftImport exercises the full MCP-config
// lifecycle against a mock API, including secret-ref round-trip and that the
// endpoint never carries a credential. Read surfaces an out-of-band disable.
func TestCapabilityConfigCreateReadDriftImport(t *testing.T) {
	ctx := context.Background()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			// Guard the minimum-data invariant: the request must not carry a secret value.
			body, _ := io.ReadAll(r.Body)
			if strings.Contains(string(body), "sk-secret-value") {
				t.Error("the provider must never send a cleartext secret to the engine")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "cfg-1", "server_ref": "github-mcp", "transport": "http",
				"endpoint": "https://mcp.internal/github", "enabled": true, "revision": 1,
				"secret_refs": []map[string]any{{"name": "token", "ref_kind": "vault", "ref": "secret/data/gh#token"}},
			})
		case http.MethodGet:
			// Read returns the config DISABLED out of band.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "cfg-1", "server_ref": "github-mcp", "transport": "http",
				"endpoint": "https://mcp.internal/github", "enabled": false, "revision": 1,
				"secret_refs": []map[string]any{{"name": "token", "ref_kind": "vault", "ref": "secret/data/gh#token"}},
			})
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer srv.Close()

	r := NewCapabilityConfigResource()
	cr := r.(*capabilityConfigResource)
	cr.Configure(ctx, resource.ConfigureRequest{
		ProviderData: &providerData{client: client.New(client.Options{Endpoint: srv.URL, APIToken: "tok", Tenant: "tenant"})},
	}, &resource.ConfigureResponse{})
	sch := capabilityConfigSchema(t, r)

	configured := capabilityConfigResourceModel{
		ID: types.StringNull(), Tenant: types.StringNull(), ServerRef: types.StringValue("github-mcp"),
		Transport: types.StringValue("http"), Endpoint: types.StringValue("https://mcp.internal/github"),
		Scope: types.StringNull(), Enabled: types.BoolValue(true), Note: types.StringNull(),
		Revision: types.Int64Null(),
		SecretRefs: secretRefList(capabilitySecretRefModel{
			Name: types.StringValue("token"), RefKind: types.StringValue("vault"),
			Ref: types.StringValue("secret/data/gh#token"), Hint: types.StringNull(),
		}),
	}

	createResp := resource.CreateResponse{State: tfsdk.State{Schema: sch}}
	cr.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: sch, Raw: rawFromCapabilityConfigModel(t, sch, configured)}}, &createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("Create diagnostics: %v", createResp.Diagnostics)
	}
	var created capabilityConfigResourceModel
	if d := createResp.State.Get(ctx, &created); d.HasError() {
		t.Fatalf("get created state: %v", d)
	}
	if created.ID.ValueString() != "cfg-1" || created.Revision.ValueInt64() != 1 {
		t.Errorf("created id/revision = %q/%d, want cfg-1/1", created.ID.ValueString(), created.Revision.ValueInt64())
	}
	if len(created.SecretRefs.Elements()) != 1 {
		t.Errorf("secret_refs len = %d, want 1", len(created.SecretRefs.Elements()))
	}

	readResp := resource.ReadResponse{State: tfsdk.State{Schema: sch}}
	cr.Read(ctx, resource.ReadRequest{State: tfsdk.State{Schema: sch, Raw: rawFromCapabilityConfigModel(t, sch, created)}}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read diagnostics: %v", readResp.Diagnostics)
	}
	var refreshed capabilityConfigResourceModel
	if d := readResp.State.Get(ctx, &refreshed); d.HasError() {
		t.Fatalf("get refreshed state: %v", d)
	}
	if refreshed.Enabled.ValueBool() {
		t.Error("Read should surface drift: enabled must be false after the out-of-band disable")
	}

	nullModel := capabilityConfigResourceModel{
		ID: types.StringNull(), Tenant: types.StringNull(), ServerRef: types.StringNull(),
		Transport: types.StringNull(), Endpoint: types.StringNull(), Scope: types.StringNull(),
		Enabled: types.BoolNull(), Note: types.StringNull(), Revision: types.Int64Null(),
		SecretRefs: types.ListNull(secretRefObjectType()),
	}
	importResp := resource.ImportStateResponse{State: tfsdk.State{Schema: sch, Raw: rawFromCapabilityConfigModel(t, sch, nullModel)}}
	cr.ImportState(ctx, resource.ImportStateRequest{ID: "cfg-1"}, &importResp)
	if importResp.Diagnostics.HasError() {
		t.Fatalf("ImportState diagnostics: %v", importResp.Diagnostics)
	}
	var imported types.String
	if d := importResp.State.GetAttribute(ctx, path.Root("id"), &imported); d.HasError() {
		t.Fatalf("get imported id: %v", d)
	}
	if imported.ValueString() != "cfg-1" {
		t.Errorf("imported id = %q, want cfg-1", imported.ValueString())
	}
}
