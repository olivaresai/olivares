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
	_ datasource.DataSource              = (*identitiesDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*identitiesDataSource)(nil)
)

// identitiesDataSource reads the reconciled NHI identity roster so a module can
// reference governed identities (e.g. to bind an agent) without reimplementing the
// roster read. The roster PROVIDERS are operator-config, not REST-managed; only
// the resulting identities are exposed here.
type identitiesDataSource struct {
	data *providerData
}

// identityDataModel is one identity in the data-source result.
type identityDataModel struct {
	ID            types.String `tfsdk:"id"`
	Ref           types.String `tfsdk:"ref"`
	Name          types.String `tfsdk:"name"`
	Kind          types.String `tfsdk:"kind"`
	Source        types.String `tfsdk:"source"`
	PrincipalType types.String `tfsdk:"principal_type"`
	Disabled      types.Bool   `tfsdk:"disabled"`
}

// identitiesDataSourceModel maps the olivares_identities schema.
type identitiesDataSourceModel struct {
	Tenant     types.String        `tfsdk:"tenant"`
	Identities []identityDataModel `tfsdk:"identities"`
}

// NewIdentitiesDataSource is the data source constructor.
func NewIdentitiesDataSource() datasource.DataSource { return &identitiesDataSource{} }

// Metadata sets the full data source type name: olivares_identities.
func (d *identitiesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_identities"
}

// Schema declares the olivares_identities attributes.
func (d *identitiesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads the reconciled identity roster in the Olivares AI control plane (read-only). Requires governance:identity:read.",
		Attributes: map[string]schema.Attribute{
			"tenant": schema.StringAttribute{
				Optional:    true,
				Description: "Tenant UUID, overriding the provider-level tenant. Sent as X-Olivares-Tenant.",
			},
			"identities": schema.ListNestedAttribute{
				Computed:    true,
				Description: "The reconciled identities.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id":             schema.StringAttribute{Computed: true, Description: "Internal identity id."},
						"ref":            schema.StringAttribute{Computed: true, Description: "External (directory) ref."},
						"name":           schema.StringAttribute{Computed: true, Description: "Identity display name."},
						"kind":           schema.StringAttribute{Computed: true, Description: "Identity kind."},
						"source":         schema.StringAttribute{Computed: true, Description: "The provider that supplied the identity."},
						"principal_type": schema.StringAttribute{Computed: true, Description: "nhi | human | unknown (never coerced)."},
						"disabled":       schema.BoolAttribute{Computed: true, Description: "Whether the identity is disabled."},
					},
				},
			},
		},
	}
}

// Configure receives the shared provider data.
func (d *identitiesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

// Read queries the identity roster and writes it to state.
func (d *identitiesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg identitiesDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	identities, err := d.data.client.ListIdentities(ctx, dsTenant(cfg.Tenant))
	if err != nil {
		resp.Diagnostics.AddError("Unable to read identities", err.Error())
		return
	}
	cfg.Identities = make([]identityDataModel, 0, len(identities))
	for _, i := range identities {
		cfg.Identities = append(cfg.Identities, identityDataModel{
			ID:            types.StringValue(i.ID),
			Ref:           types.StringValue(i.Ref),
			Name:          types.StringValue(i.Name),
			Kind:          types.StringValue(i.Kind),
			Source:        types.StringValue(i.Source),
			PrincipalType: types.StringValue(i.PrincipalType),
			Disabled:      types.BoolValue(i.Disabled),
		})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
