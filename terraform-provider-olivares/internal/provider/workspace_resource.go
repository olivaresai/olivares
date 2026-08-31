// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/olivaresai/olivares/terraform-provider-olivares/internal/client"
)

// Compile-time interface assertions.
var (
	_ resource.Resource                = (*workspaceResource)(nil)
	_ resource.ResourceWithConfigure   = (*workspaceResource)(nil)
	_ resource.ResourceWithImportState = (*workspaceResource)(nil)
)

// workspaceResource manages a sessions-module workspace as code: a named
// isolation boundary for sessions. The API surface is create-only (no PUT/PATCH),
// so any attribute change forces replacement.
//
// Authoring requires sessions:workspace:admin on the bearer token, enforced by
// the engine — this resource is a declarative client.
type workspaceResource struct {
	data *providerData
}

// workspaceResourceModel maps the olivares_workspace schema. Ref, Status,
// CreatedAt and UpdatedAt are server-assigned (Computed); Name and Description
// are user-provided but ForceNew since there is no update endpoint.
type workspaceResourceModel struct {
	Ref         types.String `tfsdk:"ref"`
	Tenant      types.String `tfsdk:"tenant"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	Status      types.String `tfsdk:"status"`
	CreatedAt   types.String `tfsdk:"created_at"`
	UpdatedAt   types.String `tfsdk:"updated_at"`
}

// NewWorkspaceResource is the resource constructor registered with the provider.
func NewWorkspaceResource() resource.Resource { return &workspaceResource{} }

// Metadata sets the full resource type name: olivares_workspace.
func (r *workspaceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_workspace"
}

// Schema declares the olivares_workspace attributes.
func (r *workspaceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A sessions-module workspace in the Olivares AI control plane: a named isolation boundary for " +
			"sessions. There is no update API, so any writable attribute change forces replacement. " +
			"Authoring requires sessions:workspace:admin.",
		Attributes: map[string]schema.Attribute{
			"ref": schema.StringAttribute{
				Computed:      true,
				Description:   "Server-assigned workspace ref (the unique identifier).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"tenant": schema.StringAttribute{
				Optional:    true,
				Description: "Tenant UUID for this resource, overriding the provider-level tenant. Sent as X-Olivares-Tenant.",
			},
			"name": schema.StringAttribute{
				Required:      true,
				Description:   "Human-readable workspace name.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"description": schema.StringAttribute{
				Optional:      true,
				Description:   "Optional workspace description.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"status": schema.StringAttribute{
				Computed:      true,
				Description:   "Server-assigned workspace status.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"created_at": schema.StringAttribute{
				Computed:      true,
				Description:   "Timestamp when the workspace was created.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"updated_at": schema.StringAttribute{
				Computed:    true,
				Description: "Timestamp when the workspace was last updated.",
			},
		},
	}
}

// Configure receives the shared provider data (REST client + tenant default).
func (r *workspaceResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	data, ok := req.ProviderData.(*providerData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type", fmt.Sprintf("Expected *providerData, got %T. This is a provider bug.", req.ProviderData))
		return
	}
	r.data = data
}

// Create authors a new workspace.
func (r *workspaceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan workspaceResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	created, err := r.data.client.CreateWorkspace(ctx, r.tenantOverride(plan), toWorkspace(plan))
	if err != nil {
		resp.Diagnostics.AddError("Unable to create workspace", err.Error())
		return
	}
	applyWorkspace(&plan, created)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes state from the engine, removing the resource on 404. Every
// field round-trips, so an out-of-band edit surfaces as ordinary attribute drift.
func (r *workspaceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state workspaceResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	got, err := r.data.client.GetWorkspace(ctx, r.tenantOverride(state), state.Ref.ValueString())
	if err != nil {
		if errors.Is(err, client.ErrNotFound) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read workspace", err.Error())
		return
	}
	applyWorkspace(&state, got)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is a no-op — the API has no PUT/PATCH for workspaces. All writable
// attributes carry RequiresReplace, so Terraform will destroy+create instead.
func (r *workspaceResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Update not supported",
		"The workspace API has no update endpoint. All writable attributes use RequiresReplace, "+
			"so this code path should never be reached. This is a provider bug.")
}

// Delete removes a workspace.
func (r *workspaceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state workspaceResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.data.client.DeleteWorkspace(ctx, r.tenantOverride(state), state.Ref.ValueString()); err != nil {
		resp.Diagnostics.AddError("Unable to delete workspace", err.Error())
	}
}

// ImportState imports an existing workspace by its ref.
func (r *workspaceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("ref"), req, resp)
}

// tenantOverride returns the per-resource tenant when set, otherwise empty.
func (r *workspaceResource) tenantOverride(m workspaceResourceModel) string {
	if !m.Tenant.IsNull() && !m.Tenant.IsUnknown() {
		return m.Tenant.ValueString()
	}
	return ""
}

// toWorkspace maps the writable plan fields onto a client.Workspace.
func toWorkspace(m workspaceResourceModel) client.Workspace {
	w := client.Workspace{
		Name: m.Name.ValueString(),
	}
	if !m.Description.IsNull() && !m.Description.IsUnknown() {
		w.Description = m.Description.ValueString()
	}
	return w
}

// applyWorkspace writes the engine's workspaceDTO back onto the model.
func applyWorkspace(m *workspaceResourceModel, w *client.Workspace) {
	m.Ref = types.StringValue(w.Ref)
	m.Name = types.StringValue(w.Name)
	if w.Description == "" {
		m.Description = types.StringNull()
	} else {
		m.Description = types.StringValue(w.Description)
	}
	if w.Status == "" {
		m.Status = types.StringNull()
	} else {
		m.Status = types.StringValue(w.Status)
	}
	if w.CreatedAt == "" {
		m.CreatedAt = types.StringNull()
	} else {
		m.CreatedAt = types.StringValue(w.CreatedAt)
	}
	if w.UpdatedAt == "" {
		m.UpdatedAt = types.StringNull()
	} else {
		m.UpdatedAt = types.StringValue(w.UpdatedAt)
	}
}
