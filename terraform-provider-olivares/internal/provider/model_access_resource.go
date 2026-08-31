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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/olivaresai/olivares/terraform-provider-olivares/internal/client"
)

// Compile-time interface assertions.
var (
	_ resource.Resource                = (*modelAccessResource)(nil)
	_ resource.ResourceWithConfigure   = (*modelAccessResource)(nil)
	_ resource.ResourceWithImportState = (*modelAccessResource)(nil)
)

// modelAccessResource manages a model access rule (models module) as code: a
// subject-scoped allow/deny gate on a model pattern with a priority. It is the
// model-governance surface a platform team declares in HCL.
//
// The engine enforces models:model-access:admin on the bearer token and
// validates the spec — this resource is a declarative client.
type modelAccessResource struct {
	data *providerData
}

// modelAccessResourceModel maps the olivares_model_access schema. Every field
// round-trips from the engine's typed modelAccessDTO, so drift is detected
// per-attribute.
type modelAccessResourceModel struct {
	ID           types.String `tfsdk:"id"`
	Tenant       types.String `tfsdk:"tenant"`
	SubjectType  types.String `tfsdk:"subject_type"`
	SubjectRef   types.String `tfsdk:"subject_ref"`
	ModelPattern types.String `tfsdk:"model_pattern"`
	Effect       types.String `tfsdk:"effect"`
	Priority     types.Int64  `tfsdk:"priority"`
	CreatedAt    types.String `tfsdk:"created_at"`
	UpdatedAt    types.String `tfsdk:"updated_at"`
}

// NewModelAccessResource is the resource constructor registered with the
// provider.
func NewModelAccessResource() resource.Resource { return &modelAccessResource{} }

// Metadata sets the full resource type name: olivares_model_access.
func (r *modelAccessResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_model_access"
}

// Schema declares the olivares_model_access attributes.
func (r *modelAccessResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A model access rule in the Olivares AI control plane (models module): a subject-scoped " +
			"allow/deny gate on a model pattern with a priority. Authoring requires models:model-access:admin.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Server-assigned model access rule ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"tenant": schema.StringAttribute{
				Optional:    true,
				Description: "Tenant UUID for this resource, overriding the provider-level tenant. Sent as X-Olivares-Tenant.",
			},
			"subject_type": schema.StringAttribute{
				Required:    true,
				Description: "Type of subject the rule applies to (e.g. \"role\", \"team\", \"user\", \"agent\").",
			},
			"subject_ref": schema.StringAttribute{
				Required:    true,
				Description: "Reference identifier for the subject (e.g. a role name or user ID).",
			},
			"model_pattern": schema.StringAttribute{
				Required:    true,
				Description: "Glob pattern matched against model identifiers (e.g. \"gpt-4*\", \"claude-*\", \"*\").",
			},
			"effect": schema.StringAttribute{
				Required:    true,
				Description: "Access effect: \"allow\" or \"deny\".",
			},
			"priority": schema.Int64Attribute{
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(0),
				Description: "Evaluation priority — higher values win when multiple rules match. Defaults to 0.",
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
func (r *modelAccessResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create authors a new model access rule.
func (r *modelAccessResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan modelAccessResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ma := toModelAccess(plan)
	created, err := r.data.client.CreateModelAccess(ctx, r.tenantOverride(plan), ma)
	if err != nil {
		resp.Diagnostics.AddError("Unable to create model access rule", err.Error())
		return
	}
	applyModelAccess(&plan, created)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes state from the engine, removing the resource on 404. Every
// field round-trips, so an out-of-band edit surfaces as ordinary attribute
// drift.
func (r *modelAccessResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state modelAccessResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	got, err := r.data.client.GetModelAccess(ctx, r.tenantOverride(state), state.ID.ValueString())
	if err != nil {
		if errors.Is(err, client.ErrNotFound) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read model access rule", err.Error())
		return
	}
	applyModelAccess(&state, got)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update applies changes to an existing model access rule.
func (r *modelAccessResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan modelAccessResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var state modelAccessResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = state.ID
	ma := toModelAccess(plan)
	updated, err := r.data.client.UpdateModelAccess(ctx, r.tenantOverride(plan), plan.ID.ValueString(), ma)
	if err != nil {
		resp.Diagnostics.AddError("Unable to update model access rule", err.Error())
		return
	}
	applyModelAccess(&plan, updated)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete removes a model access rule.
func (r *modelAccessResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state modelAccessResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.data.client.DeleteModelAccess(ctx, r.tenantOverride(state), state.ID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Unable to delete model access rule", err.Error())
	}
}

// ImportState imports an existing model access rule by its id.
func (r *modelAccessResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// tenantOverride returns the per-resource tenant when set, otherwise empty.
func (r *modelAccessResource) tenantOverride(m modelAccessResourceModel) string {
	if !m.Tenant.IsNull() && !m.Tenant.IsUnknown() {
		return m.Tenant.ValueString()
	}
	return ""
}

// toModelAccess maps the writable plan fields onto a client.ModelAccess.
func toModelAccess(m modelAccessResourceModel) client.ModelAccess {
	return client.ModelAccess{
		SubjectType:  m.SubjectType.ValueString(),
		SubjectRef:   m.SubjectRef.ValueString(),
		ModelPattern: m.ModelPattern.ValueString(),
		Effect:       m.Effect.ValueString(),
		Priority:     m.Priority.ValueInt64(),
	}
}

// applyModelAccess writes the engine's modelAccessDTO back onto the model.
func applyModelAccess(m *modelAccessResourceModel, ma *client.ModelAccess) {
	m.ID = types.StringValue(ma.ID)
	m.SubjectType = types.StringValue(ma.SubjectType)
	m.SubjectRef = types.StringValue(ma.SubjectRef)
	m.ModelPattern = types.StringValue(ma.ModelPattern)
	m.Effect = types.StringValue(ma.Effect)
	m.Priority = types.Int64Value(ma.Priority)
	if ma.CreatedAt != "" {
		m.CreatedAt = types.StringValue(ma.CreatedAt)
	}
	if ma.UpdatedAt != "" {
		m.UpdatedAt = types.StringValue(ma.UpdatedAt)
	}
}
