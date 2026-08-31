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
	_ datasource.DataSource              = (*serverInfoDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*serverInfoDataSource)(nil)
)

// serverInfoDataSource reads the control plane's version/engine/license metadata
// so a module can branch honestly on the engine it is targeting (capability
// detection) rather than hardcoding assumptions.
type serverInfoDataSource struct {
	data *providerData
}

// serverInfoDataSourceModel maps the olivares_server_info schema.
type serverInfoDataSourceModel struct {
	Version         types.String `tfsdk:"version"`
	Engine          types.String `tfsdk:"engine"`
	SetupRequired   types.Bool   `tfsdk:"setup_required"`
	LicenseStatus   types.String `tfsdk:"license_status"`
	LicenseLicensee types.String `tfsdk:"license_licensee"`
}

// NewServerInfoDataSource is the data source constructor.
func NewServerInfoDataSource() datasource.DataSource { return &serverInfoDataSource{} }

// Metadata sets the full data source type name: olivares_server_info.
func (d *serverInfoDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_server_info"
}

// Schema declares the olivares_server_info attributes.
func (d *serverInfoDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads the Olivares AI control plane server metadata (version, engine, license), read-only.",
		Attributes: map[string]schema.Attribute{
			"version":          schema.StringAttribute{Computed: true, Description: "Control plane version."},
			"engine":           schema.StringAttribute{Computed: true, Description: "Storage engine identifier."},
			"setup_required":   schema.BoolAttribute{Computed: true, Description: "Whether first-run setup is still required."},
			"license_status":   schema.StringAttribute{Computed: true, Description: "License status."},
			"license_licensee": schema.StringAttribute{Computed: true, Description: "Licensee."},
		},
	}
}

// Configure receives the shared provider data.
func (d *serverInfoDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

// Read fetches the server-info metadata.
func (d *serverInfoDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	info, err := d.data.client.GetServerInfo(ctx, "")
	if err != nil {
		resp.Diagnostics.AddError("Unable to read server info", err.Error())
		return
	}
	state := serverInfoDataSourceModel{
		Version:         types.StringValue(info.Version),
		Engine:          types.StringValue(info.Engine),
		SetupRequired:   types.BoolValue(info.SetupRequired),
		LicenseStatus:   types.StringValue(info.License.Status),
		LicenseLicensee: types.StringValue(info.License.Licensee),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
