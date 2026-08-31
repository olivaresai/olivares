// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Compile-time interface assertions.
var (
	_ datasource.DataSource              = (*policiesDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*policiesDataSource)(nil)
)

// policiesDataSource reads the governed estate's governance policies, so a
// Terraform/OpenTofu module can reference authored policy without reimplementing
// the REST call.
type policiesDataSource struct {
	data *providerData
}

// policyDataModel is one policy in the data-source result.
type policyDataModel struct {
	ID      types.String `tfsdk:"id"`
	Name    types.String `tfsdk:"name"`
	Kind    types.String `tfsdk:"kind"`
	Enabled types.Bool   `tfsdk:"enabled"`
	Spec    types.String `tfsdk:"spec"`
}

// policiesDataSourceModel maps the olivares_policies schema.
type policiesDataSourceModel struct {
	Tenant   types.String      `tfsdk:"tenant"`
	Kind     types.String      `tfsdk:"kind"`
	Policies []policyDataModel `tfsdk:"policies"`
}

// NewPoliciesDataSource is the data source constructor.
func NewPoliciesDataSource() datasource.DataSource { return &policiesDataSource{} }

// Metadata sets the full data source type name: olivares_policies.
func (d *policiesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_policies"
}

// Schema declares the olivares_policies attributes.
func (d *policiesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads the governance policies in the Olivares AI control plane (read-only). Requires governance:policy:read.",
		Attributes: map[string]schema.Attribute{
			"tenant": schema.StringAttribute{
				Optional:    true,
				Description: "Tenant UUID, overriding the provider-level tenant. Sent as X-Olivares-Tenant.",
			},
			"kind": schema.StringAttribute{
				Optional:    true,
				Description: "Filter by policy kind (\"abac\" or \"approval\"). Omit for all kinds.",
			},
			"policies": schema.ListNestedAttribute{
				Computed:    true,
				Description: "The matching governance policies.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id":      schema.StringAttribute{Computed: true, Description: "Policy ID."},
						"name":    schema.StringAttribute{Computed: true, Description: "Policy name."},
						"kind":    schema.StringAttribute{Computed: true, Description: "Policy kind."},
						"enabled": schema.BoolAttribute{Computed: true, Description: "Whether the policy is active."},
						"spec":    schema.StringAttribute{Computed: true, Description: "The engine's canonical policy spec as a JSON document."},
					},
				},
			},
		},
	}
}

// Configure receives the shared provider data.
func (d *policiesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	data, ok := req.ProviderData.(*providerData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type", fmt.Sprintf("Expected *providerData, got %T. This is a provider bug.", req.ProviderData))
		return
	}
	d.data = data
}

// Read queries the governance policies and writes them to state.
func (d *policiesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg policiesDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	policies, err := d.data.client.ListPolicies(ctx, dsTenant(cfg.Tenant), cfg.Kind.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Unable to read policies", err.Error())
		return
	}
	cfg.Policies = make([]policyDataModel, 0, len(policies))
	for _, p := range policies {
		spec := ""
		if len(p.Spec) > 0 {
			spec = string(p.Spec)
		}
		cfg.Policies = append(cfg.Policies, policyDataModel{
			ID:      types.StringValue(p.ID),
			Name:    types.StringValue(p.Name),
			Kind:    types.StringValue(p.Kind),
			Enabled: types.BoolValue(p.Enabled),
			Spec:    types.StringValue(spec),
		})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
