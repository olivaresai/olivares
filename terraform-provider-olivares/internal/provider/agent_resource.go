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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/olivaresai/olivares/terraform-provider-olivares/internal/client"
)

// Compile-time interface assertions.
var (
	_ resource.Resource                = (*agentResource)(nil)
	_ resource.ResourceWithConfigure   = (*agentResource)(nil)
	_ resource.ResourceWithImportState = (*agentResource)(nil)
)

// defaultAgentStatus is applied when status is omitted in configuration.
const defaultAgentStatus = "active"

// agentResource manages the lifecycle of an olivares_agent.
type agentResource struct {
	data *providerData
}

// agentResourceModel maps the olivares_agent schema to Go values.
type agentResourceModel struct {
	ID         types.String `tfsdk:"id"`
	Tenant     types.String `tfsdk:"tenant"`
	Name       types.String `tfsdk:"name"`
	Kind       types.String `tfsdk:"kind"`
	ExternalID types.String `tfsdk:"external_id"`
	Status     types.String `tfsdk:"status"`
	TenantID   types.String `tfsdk:"tenant_id"`
	Version    types.Int64  `tfsdk:"version"`
	CreatedAt  types.String `tfsdk:"created_at"`
	UpdatedAt  types.String `tfsdk:"updated_at"`
}

// NewAgentResource is the resource constructor registered with the provider.
func NewAgentResource() resource.Resource {
	return &agentResource{}
}

// Metadata sets the full resource type name: olivares_agent.
func (r *agentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_agent"
}

// Schema declares the olivares_agent attributes.
func (r *agentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "An agent registered in the Olivares AI control plane.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "Server-assigned agent ID.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"tenant": schema.StringAttribute{
				Optional:    true,
				Description: "Tenant UUID for this resource, overriding the provider-level tenant. Sent as X-Olivares-Tenant.",
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Human-readable agent name.",
			},
			"kind": schema.StringAttribute{
				Required:    true,
				Description: "Agent kind/type discriminator.",
			},
			"external_id": schema.StringAttribute{
				Optional:    true,
				Description: "Caller-defined external identifier for correlation.",
			},
			"status": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(defaultAgentStatus),
				Description: "Agent status. Defaults to \"active\".",
			},
			"tenant_id": schema.StringAttribute{
				Computed:    true,
				Description: "Tenant the agent belongs to, as resolved by the engine.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"version": schema.Int64Attribute{
				Computed:    true,
				Description: "Optimistic-concurrency version assigned by the engine.",
			},
			"created_at": schema.StringAttribute{
				Computed:    true,
				Description: "RFC 3339 creation timestamp.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"updated_at": schema.StringAttribute{
				Computed:    true,
				Description: "RFC 3339 last-update timestamp.",
			},
		},
	}
}

// Configure receives the shared provider data (REST client + tenant default).
func (r *agentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return // provider not configured yet (e.g. during validate)
	}
	data, ok := req.ProviderData.(*providerData)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected provider data type",
			fmt.Sprintf("Expected *providerData, got %T. This is a provider bug.", req.ProviderData),
		)
		return
	}
	r.data = data
}

// Create provisions a new agent.
func (r *agentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan agentResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	created, err := r.data.client.CreateAgent(ctx, r.tenantOverride(plan), toAgent(plan))
	if err != nil {
		resp.Diagnostics.AddError("Unable to create agent", err.Error())
		return
	}

	applyAgent(&plan, created)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes state from the engine, removing the resource on 404.
func (r *agentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state agentResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	got, err := r.data.client.GetAgent(ctx, r.tenantOverride(state), state.ID.ValueString())
	if err != nil {
		if errors.Is(err, client.ErrNotFound) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read agent", err.Error())
		return
	}

	applyAgent(&state, got)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update applies changes to an existing agent.
func (r *agentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan agentResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// id is computed and only ever lives in state; carry it onto the plan model.
	var state agentResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = state.ID

	updated, err := r.data.client.UpdateAgent(ctx, r.tenantOverride(plan), plan.ID.ValueString(), toAgent(plan))
	if err != nil {
		resp.Diagnostics.AddError("Unable to update agent", err.Error())
		return
	}

	applyAgent(&plan, updated)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete removes an agent.
func (r *agentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state agentResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.data.client.DeleteAgent(ctx, r.tenantOverride(state), state.ID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Unable to delete agent", err.Error())
	}
}

// ImportState imports an existing agent by its id.
func (r *agentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// tenantOverride returns the per-resource tenant when set, otherwise empty so
// the client falls back to its provider-level tenant.
func (r *agentResource) tenantOverride(m agentResourceModel) string {
	if !m.Tenant.IsNull() && !m.Tenant.IsUnknown() {
		return m.Tenant.ValueString()
	}
	return ""
}

// toAgent maps the writable plan fields onto a client.Agent.
func toAgent(m agentResourceModel) client.Agent {
	return client.Agent{
		Name:       m.Name.ValueString(),
		Kind:       m.Kind.ValueString(),
		ExternalID: m.ExternalID.ValueString(),
		Status:     m.Status.ValueString(),
	}
}

// applyAgent writes the engine's AgentDTO back onto the model. The user-supplied
// `tenant` attribute is intentionally left untouched (it is request-side only,
// distinct from the computed `tenant_id` the engine reports).
func applyAgent(m *agentResourceModel, a *client.Agent) {
	m.ID = types.StringValue(a.ID)
	m.Name = types.StringValue(a.Name)
	m.Kind = types.StringValue(a.Kind)
	m.Status = types.StringValue(a.Status)
	m.TenantID = types.StringValue(a.TenantID)
	m.Version = types.Int64Value(a.Version)
	m.CreatedAt = types.StringValue(a.CreatedAt)
	m.UpdatedAt = types.StringValue(a.UpdatedAt)

	// external_id is optional: keep it null when the engine returns empty so the
	// plan stays consistent for configurations that never set it.
	if a.ExternalID == "" {
		m.ExternalID = types.StringNull()
	} else {
		m.ExternalID = types.StringValue(a.ExternalID)
	}
}
