// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/olivaresai/olivares/terraform-provider-olivares/internal/client"
)

// Compile-time interface assertions.
var (
	_ datasource.DataSource              = (*deploymentDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*deploymentDataSource)(nil)
)

// deploymentDataSource reads a deployment definition's governed state (desired +
// applied) so a module can reference the live reconciliation state of a deployment
// it does not own, without reimplementing the REST read.
type deploymentDataSource struct {
	data *providerData
}

// deploymentDataSourceModel maps the olivares_deployment (data source) schema.
type deploymentDataSourceModel struct {
	Tenant         types.String `tfsdk:"tenant"`
	ID             types.String `tfsdk:"id"`
	SubjectKind    types.String `tfsdk:"subject_kind"`
	SubjectRef     types.String `tfsdk:"subject_ref"`
	Name           types.String `tfsdk:"name"`
	Environment    types.String `tfsdk:"environment"`
	Target         types.String `tfsdk:"target"`
	Runtime        types.String `tfsdk:"runtime"`
	SourceRef      types.String `tfsdk:"source_ref"`
	DesiredStatus  types.String `tfsdk:"desired_status"`
	CurrentVersion types.Int64  `tfsdk:"current_version"`
	AppliedVersion types.Int64  `tfsdk:"applied_version"`
	SpecHash       types.String `tfsdk:"spec_hash"`
}

// NewDeploymentDataSource is the data source constructor.
func NewDeploymentDataSource() datasource.DataSource { return &deploymentDataSource{} }

// Metadata sets the full data source type name: olivares_deployment.
func (d *deploymentDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_deployment"
}

// Schema declares the olivares_deployment data source attributes.
func (d *deploymentDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads a deployment definition's governed state (module VII), read-only. Requires deploy:deployment:read.",
		Attributes: map[string]schema.Attribute{
			"tenant": schema.StringAttribute{
				Optional:    true,
				Description: "Tenant UUID, overriding the provider-level tenant. Sent as X-Olivares-Tenant.",
			},
			"id": schema.StringAttribute{
				Required:    true,
				Description: "The deployment definition id to read.",
			},
			"subject_kind":    schema.StringAttribute{Computed: true, Description: "agent | mcp_server."},
			"subject_ref":     schema.StringAttribute{Computed: true, Description: "Logical reference of the deployed subject."},
			"name":            schema.StringAttribute{Computed: true, Description: "Logical deployment name."},
			"environment":     schema.StringAttribute{Computed: true, Description: "Deployment environment."},
			"target":          schema.StringAttribute{Computed: true, Description: "Runtime target reference."},
			"runtime":         schema.StringAttribute{Computed: true, Description: "Executor/runtime kind."},
			"source_ref":      schema.StringAttribute{Computed: true, Description: "GitOps source reference."},
			"desired_status":  schema.StringAttribute{Computed: true, Description: "active | retired."},
			"current_version": schema.Int64Attribute{Computed: true, Description: "Latest declared revision (desired)."},
			"applied_version": schema.Int64Attribute{Computed: true, Description: "Revision actually reconciled (real); 0 = never applied."},
			"spec_hash":       schema.StringAttribute{Computed: true, Description: "Hex SHA-256 of the current desired spec."},
		},
	}
}

// Configure receives the shared provider data.
func (d *deploymentDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

// Read fetches the deployment definition by id.
func (d *deploymentDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg deploymentDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	got, err := d.data.client.GetDeployment(ctx, dsTenant(cfg.Tenant), cfg.ID.ValueString())
	if err != nil {
		if errors.Is(err, client.ErrNotFound) {
			resp.Diagnostics.AddError("Deployment not found", fmt.Sprintf("No deployment definition with id %q.", cfg.ID.ValueString()))
			return
		}
		resp.Diagnostics.AddError("Unable to read deployment", err.Error())
		return
	}
	cfg.SubjectKind = types.StringValue(got.SubjectKind)
	cfg.SubjectRef = types.StringValue(got.SubjectRef)
	cfg.Name = types.StringValue(got.Name)
	cfg.Environment = types.StringValue(got.Environment)
	cfg.Target = types.StringValue(got.Target)
	cfg.Runtime = types.StringValue(got.Runtime)
	cfg.DesiredStatus = types.StringValue(got.DesiredStatus)
	cfg.CurrentVersion = types.Int64Value(got.CurrentVersion)
	cfg.AppliedVersion = types.Int64Value(got.AppliedVersion)
	cfg.SpecHash = types.StringValue(got.SpecHash)
	if got.SourceRef == "" {
		cfg.SourceRef = types.StringNull()
	} else {
		cfg.SourceRef = types.StringValue(got.SourceRef)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
