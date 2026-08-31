// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
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
	_ resource.Resource                = (*modelGroupResource)(nil)
	_ resource.ResourceWithConfigure   = (*modelGroupResource)(nil)
	_ resource.ResourceWithImportState = (*modelGroupResource)(nil)
)

// modelGroupResource manages a model group (models module) as code: a named
// set of model IDs that policies and budgets can reference as a single unit.
// It is the "which models may this agent use" surface a platform team declares
// in HCL. The engine enforces models:model-group:write on the bearer token and
// validates the spec — this resource is a declarative client.
type modelGroupResource struct {
	data *providerData
}

// modelGroupResourceModel maps the olivares_model_group schema. Every field
// round-trips from the engine's typed modelGroupDTO, so drift is detected
// per-attribute.
type modelGroupResourceModel struct {
	ID          types.String `tfsdk:"id"`
	Tenant      types.String `tfsdk:"tenant"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	Models      types.List   `tfsdk:"models"`
	CreatedAt   types.String `tfsdk:"created_at"`
	UpdatedAt   types.String `tfsdk:"updated_at"`
}

// NewModelGroupResource is the resource constructor registered with the provider.
func NewModelGroupResource() resource.Resource { return &modelGroupResource{} }

// Metadata sets the full resource type name: olivares_model_group.
func (r *modelGroupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_model_group"
}

// Schema declares the olivares_model_group attributes.
func (r *modelGroupResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A model group in the Olivares AI control plane (models module): a named set of model IDs that " +
			"policies and budgets can reference as a single unit. Authoring requires models:model-group:write.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Server-assigned model group ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"tenant": schema.StringAttribute{
				Optional:    true,
				Description: "Tenant UUID for this resource, overriding the provider-level tenant. Sent as X-Olivares-Tenant.",
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Human-readable model group name.",
			},
			"description": schema.StringAttribute{
				Optional:    true,
				Description: "Optional description of the model group's purpose.",
			},
			"models": schema.ListAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "List of model identifiers that belong to this group. Omit for an empty group.",
			},
			"created_at": schema.StringAttribute{
				Computed:      true,
				Description:   "Server-assigned creation timestamp (RFC 3339).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"updated_at": schema.StringAttribute{
				Computed:    true,
				Description: "Server-assigned last-update timestamp (RFC 3339).",
			},
		},
	}
}

// Configure receives the shared provider data (REST client + tenant default).
func (r *modelGroupResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create authors a new model group.
func (r *modelGroupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan modelGroupResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	mg, diags := toModelGroup(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	created, err := r.data.client.CreateModelGroup(ctx, r.tenantOverride(plan), mg)
	if err != nil {
		resp.Diagnostics.AddError("Unable to create model group", err.Error())
		return
	}
	resp.Diagnostics.Append(applyModelGroup(ctx, &plan, created)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes state from the engine, removing the resource on 404. Every
// field round-trips, so an out-of-band edit surfaces as ordinary attribute
// drift.
func (r *modelGroupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state modelGroupResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	got, err := r.data.client.GetModelGroup(ctx, r.tenantOverride(state), state.ID.ValueString())
	if err != nil {
		if errors.Is(err, client.ErrNotFound) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read model group", err.Error())
		return
	}
	resp.Diagnostics.Append(applyModelGroup(ctx, &state, got)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update applies changes to an existing model group.
func (r *modelGroupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan modelGroupResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var state modelGroupResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = state.ID
	mg, diags := toModelGroup(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	updated, err := r.data.client.UpdateModelGroup(ctx, r.tenantOverride(plan), plan.ID.ValueString(), mg)
	if err != nil {
		resp.Diagnostics.AddError("Unable to update model group", err.Error())
		return
	}
	resp.Diagnostics.Append(applyModelGroup(ctx, &plan, updated)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete removes a model group.
func (r *modelGroupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state modelGroupResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.data.client.DeleteModelGroup(ctx, r.tenantOverride(state), state.ID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Unable to delete model group", err.Error())
	}
}

// ImportState imports an existing model group by its id.
func (r *modelGroupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// tenantOverride returns the per-resource tenant when set, otherwise empty.
func (r *modelGroupResource) tenantOverride(m modelGroupResourceModel) string {
	if !m.Tenant.IsNull() && !m.Tenant.IsUnknown() {
		return m.Tenant.ValueString()
	}
	return ""
}

// toModelGroup maps the writable plan fields onto a client.ModelGroup.
func toModelGroup(ctx context.Context, m modelGroupResourceModel) (client.ModelGroup, diag.Diagnostics) {
	mg := client.ModelGroup{
		Name:        m.Name.ValueString(),
		Description: m.Description.ValueString(),
	}
	var diags diag.Diagnostics
	if !m.Models.IsNull() && !m.Models.IsUnknown() {
		var models []string
		diags.Append(m.Models.ElementsAs(ctx, &models, false)...)
		mg.Models = models
	}
	return mg, diags
}

// applyModelGroup writes the engine's modelGroupDTO back onto the model. Models
// is kept null when the engine returns none (so a config that never set them
// stays consistent).
func applyModelGroup(ctx context.Context, m *modelGroupResourceModel, mg *client.ModelGroup) diag.Diagnostics {
	m.ID = types.StringValue(mg.ID)
	m.Name = types.StringValue(mg.Name)
	if mg.Description == "" {
		m.Description = types.StringNull()
	} else {
		m.Description = types.StringValue(mg.Description)
	}
	if mg.CreatedAt != "" {
		m.CreatedAt = types.StringValue(mg.CreatedAt)
	}
	if mg.UpdatedAt != "" {
		m.UpdatedAt = types.StringValue(mg.UpdatedAt)
	}
	if len(mg.Models) == 0 {
		m.Models = types.ListNull(types.StringType)
		return nil
	}
	list, d := types.ListValueFrom(ctx, types.StringType, mg.Models)
	m.Models = list
	return d
}
