// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	fwprovider "github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// newAgentResource builds the resource under test.
func newAgentResource(t *testing.T) resource.Resource {
	t.Helper()
	r := NewAgentResource()
	if r == nil {
		t.Fatal("NewAgentResource returned nil")
	}
	return r
}

func TestProviderMetadata(t *testing.T) {
	p := New("test")()
	var resp fwprovider.MetadataResponse
	p.Metadata(context.Background(), fwprovider.MetadataRequest{}, &resp)

	if resp.TypeName != "olivares" {
		t.Errorf("TypeName = %q, want olivares", resp.TypeName)
	}
	if resp.Version != "test" {
		t.Errorf("Version = %q, want test", resp.Version)
	}
}

func TestProviderSchema(t *testing.T) {
	p := New("test")()
	var resp fwprovider.SchemaResponse
	p.Schema(context.Background(), fwprovider.SchemaRequest{}, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("provider Schema diagnostics: %v", resp.Diagnostics)
	}

	attrs := resp.Schema.GetAttributes()

	endpoint, ok := attrs["endpoint"]
	if !ok || !endpoint.IsRequired() {
		t.Errorf("endpoint must be a required attribute")
	}
	token, ok := attrs["api_token"]
	if !ok || !token.IsRequired() {
		t.Errorf("api_token must be a required attribute")
	}
	if !token.IsSensitive() {
		t.Errorf("api_token must be sensitive")
	}
	if tenant, ok := attrs["tenant"]; !ok || !tenant.IsOptional() {
		t.Errorf("tenant must be an optional attribute")
	}
	if insecure, ok := attrs["insecure_skip_verify"]; !ok || !insecure.IsOptional() {
		t.Errorf("insecure_skip_verify must be an optional attribute")
	}
}

func TestProviderResourcesRegistered(t *testing.T) {
	p := New("test")()
	resources := p.Resources(context.Background())
	if len(resources) != 11 {
		t.Fatalf("Resources len = %d, want 11", len(resources))
	}
	got := map[string]bool{}
	for _, ctor := range resources {
		var resp resource.MetadataResponse
		ctor().Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "olivares"}, &resp)
		got[resp.TypeName] = true
	}
	for _, want := range []string{
		"olivares_agent", "olivares_deployment", "olivares_policy", "olivares_agent_identity_binding",
		"olivares_budget", "olivares_capability_config", "olivares_notification_route",
		"olivares_workspace", "olivares_rbac_grant", "olivares_model_access", "olivares_model_group",
	} {
		if !got[want] {
			t.Errorf("resource %q not registered (have %v)", want, got)
		}
	}
}

func TestProviderDataSourcesRegistered(t *testing.T) {
	p := New("test")()
	sources := p.DataSources(context.Background())
	if len(sources) != 7 {
		t.Fatalf("DataSources len = %d, want 7", len(sources))
	}
	got := map[string]bool{}
	for _, ctor := range sources {
		var resp datasource.MetadataResponse
		ctor().Metadata(context.Background(), datasource.MetadataRequest{ProviderTypeName: "olivares"}, &resp)
		got[resp.TypeName] = true
	}
	for _, want := range []string{
		"olivares_policies", "olivares_identities", "olivares_access_edges",
		"olivares_deployment", "olivares_server_info", "olivares_budgets", "olivares_inventory",
	} {
		if !got[want] {
			t.Errorf("data source %q not registered (have %v)", want, got)
		}
	}
}

// TestGovernanceResourceSchemas runs the framework's own schema validation over the
// new governance resources (catches required+computed conflicts, default wiring).
func TestGovernanceResourceSchemas(t *testing.T) {
	for name, ctor := range map[string]func() resource.Resource{
		"olivares_policy":                 NewPolicyResource,
		"olivares_agent_identity_binding": NewAgentIdentityBindingResource,
		"olivares_budget":                 NewBudgetResource,
		"olivares_capability_config":      NewCapabilityConfigResource,
		"olivares_notification_route":     NewNotificationRouteResource,
		"olivares_workspace":              NewWorkspaceResource,
		"olivares_rbac_grant":             NewRBACGrantResource,
		"olivares_model_access":           NewModelAccessResource,
		"olivares_model_group":            NewModelGroupResource,
	} {
		var resp resource.SchemaResponse
		ctor().Schema(context.Background(), resource.SchemaRequest{}, &resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("%s Schema diagnostics: %v", name, resp.Diagnostics)
		}
		if diags := resp.Schema.ValidateImplementation(context.Background()); diags.HasError() {
			t.Fatalf("%s ValidateImplementation: %v", name, diags)
		}
	}
}

// TestGovernanceDataSourceSchemas runs the framework's schema validation over the
// new data sources.
func TestGovernanceDataSourceSchemas(t *testing.T) {
	for name, ctor := range map[string]func() datasource.DataSource{
		"olivares_policies":     NewPoliciesDataSource,
		"olivares_identities":   NewIdentitiesDataSource,
		"olivares_access_edges": NewAccessEdgesDataSource,
		"olivares_deployment":   NewDeploymentDataSource,
		"olivares_server_info":  NewServerInfoDataSource,
		"olivares_budgets":      NewBudgetsDataSource,
		"olivares_inventory":    NewInventoryDataSource,
	} {
		var resp datasource.SchemaResponse
		ctor().Schema(context.Background(), datasource.SchemaRequest{}, &resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("%s Schema diagnostics: %v", name, resp.Diagnostics)
		}
		if diags := resp.Schema.ValidateImplementation(context.Background()); diags.HasError() {
			t.Fatalf("%s ValidateImplementation: %v", name, diags)
		}
	}
}

func TestDeploymentResourceMetadata(t *testing.T) {
	var resp resource.MetadataResponse
	NewDeploymentResource().Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "olivares"}, &resp)
	if resp.TypeName != "olivares_deployment" {
		t.Errorf("TypeName = %q, want olivares_deployment", resp.TypeName)
	}
}

func TestDeploymentResourceSchema(t *testing.T) {
	var resp resource.SchemaResponse
	NewDeploymentResource().Schema(context.Background(), resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}
	for _, attr := range []string{"id", "subject_kind", "name", "environment", "target", "runtime", "spec", "spec_hash", "applied_version"} {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("missing attribute %q", attr)
		}
	}
}

func TestAgentResourceMetadata(t *testing.T) {
	r := newAgentResource(t)
	var resp resource.MetadataResponse
	r.Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "olivares"}, &resp)

	if resp.TypeName != "olivares_agent" {
		t.Errorf("TypeName = %q, want olivares_agent", resp.TypeName)
	}
}

func TestAgentResourceSchema(t *testing.T) {
	r := newAgentResource(t)
	var resp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("agent Schema diagnostics: %v", resp.Diagnostics)
	}
	// The framework's own implementation validation must pass (catches e.g.
	// required+computed conflicts, missing defaults wiring).
	if diags := resp.Schema.ValidateImplementation(context.Background()); diags.HasError() {
		t.Fatalf("ValidateImplementation: %v", diags)
	}

	attrs := resp.Schema.GetAttributes()

	required := []string{"name", "kind"}
	for _, name := range required {
		a, ok := attrs[name]
		if !ok {
			t.Errorf("missing attribute %q", name)
			continue
		}
		if !a.IsRequired() {
			t.Errorf("attribute %q must be required", name)
		}
	}

	computed := []string{"id", "tenant_id", "version", "created_at", "updated_at"}
	for _, name := range computed {
		a, ok := attrs[name]
		if !ok {
			t.Errorf("missing attribute %q", name)
			continue
		}
		if !a.IsComputed() {
			t.Errorf("attribute %q must be computed", name)
		}
	}

	// status is optional+computed (default "active").
	status, ok := attrs["status"]
	if !ok {
		t.Fatal("missing attribute status")
	}
	if !status.IsOptional() || !status.IsComputed() {
		t.Errorf("status must be optional+computed")
	}

	// tenant and external_id are optional.
	for _, name := range []string{"tenant", "external_id"} {
		a, ok := attrs[name]
		if !ok {
			t.Errorf("missing attribute %q", name)
			continue
		}
		if !a.IsOptional() {
			t.Errorf("attribute %q must be optional", name)
		}
	}
}

// TestAgentSchemaTerraformType exercises the terraform-plugin-go tftypes layer
// the framework uses for protocol encoding: the schema's object type must
// describe every declared attribute, and a conforming value must round-trip.
func TestAgentSchemaTerraformType(t *testing.T) {
	r := newAgentResource(t)
	var resp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("agent Schema diagnostics: %v", resp.Diagnostics)
	}

	tfType := resp.Schema.Type().TerraformType(context.Background())
	objType, ok := tfType.(tftypes.Object)
	if !ok {
		t.Fatalf("schema Terraform type = %T, want tftypes.Object", tfType)
	}

	for _, name := range []string{
		"id", "tenant", "name", "kind", "external_id",
		"status", "tenant_id", "version", "created_at", "updated_at",
	} {
		if _, ok := objType.AttributeTypes[name]; !ok {
			t.Errorf("object type missing attribute %q", name)
		}
	}

	// Construct a conforming value to confirm the type is valid/usable.
	val := tftypes.NewValue(objType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "agt_001"),
		"tenant":      tftypes.NewValue(tftypes.String, nil),
		"name":        tftypes.NewValue(tftypes.String, "billing-agent"),
		"kind":        tftypes.NewValue(tftypes.String, "worker"),
		"external_id": tftypes.NewValue(tftypes.String, nil),
		"status":      tftypes.NewValue(tftypes.String, "active"),
		"tenant_id":   tftypes.NewValue(tftypes.String, "tnt_1"),
		"version":     tftypes.NewValue(tftypes.Number, 1),
		"created_at":  tftypes.NewValue(tftypes.String, "2026-06-03T10:00:00Z"),
		"updated_at":  tftypes.NewValue(tftypes.String, "2026-06-03T10:00:00Z"),
	})
	if val.IsNull() {
		t.Error("constructed agent value should not be null")
	}
	// The value must conform to its declared object type.
	if !val.Type().(tftypes.Object).UsableAs(objType) {
		t.Error("constructed value type is not usable as the schema object type")
	}
}
