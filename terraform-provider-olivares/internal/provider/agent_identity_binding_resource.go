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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/olivaresai/olivares/terraform-provider-olivares/internal/client"
)

// Compile-time interface assertions.
var (
	_ resource.Resource                   = (*agentIdentityBindingResource)(nil)
	_ resource.ResourceWithConfigure      = (*agentIdentityBindingResource)(nil)
	_ resource.ResourceWithImportState    = (*agentIdentityBindingResource)(nil)
	_ resource.ResourceWithValidateConfig = (*agentIdentityBindingResource)(nil)
)

// agentIdentityBindingResource manages the binding of an agent to its NHI
// identity (the access-map attribution bridge, module VI). Declaring the binding
// in HCL keeps the per-agent identity attribution under source control; the engine
// enforces governance:identity:admin and emits the shared-identity finding when an
// identity is bound to more than one agent.
type agentIdentityBindingResource struct {
	data *providerData
}

// agentIdentityBindingResourceModel maps the olivares_agent_identity_binding
// schema. Exactly one of identity_id / identity_ref / mint selects the target.
type agentIdentityBindingResourceModel struct {
	ID           types.String `tfsdk:"id"`
	Tenant       types.String `tfsdk:"tenant"`
	AgentID      types.String `tfsdk:"agent_id"`
	IdentityID   types.String `tfsdk:"identity_id"`
	IdentityRef  types.String `tfsdk:"identity_ref"`
	Mint         types.Bool   `tfsdk:"mint"`
	AllowUnknown types.Bool   `tfsdk:"allow_unknown"`
	Minted       types.Bool   `tfsdk:"minted"`
	Shared       types.Bool   `tfsdk:"shared"`
	AgentCount   types.Int64  `tfsdk:"agent_count"`
}

// NewAgentIdentityBindingResource is the resource constructor.
func NewAgentIdentityBindingResource() resource.Resource { return &agentIdentityBindingResource{} }

// Metadata sets the full resource type name: olivares_agent_identity_binding.
func (r *agentIdentityBindingResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_agent_identity_binding"
}

// Schema declares the olivares_agent_identity_binding attributes.
func (r *agentIdentityBindingResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Binds an agent to its non-human (NHI) identity in the Olivares AI control plane (module VI). " +
			"This is the access-map attribution bridge: with a firm binding, accesses are attributed to the agent's NHI. " +
			"Exactly one of identity_id, identity_ref or mint selects the target. Requires governance:identity:admin.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Resource identifier (equals agent_id; the binding is per-agent).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"tenant": schema.StringAttribute{
				Optional:    true,
				Description: "Tenant UUID for this resource, overriding the provider-level tenant. Sent as X-Olivares-Tenant.",
			},
			"agent_id": schema.StringAttribute{
				Required:      true,
				Description:   "The agent to bind. Immutable (changing it replaces the resource).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"identity_id": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Bind to an existing identity by its internal id. Mutually exclusive with identity_ref/mint. Also reports the resolved identity id.",
			},
			"identity_ref": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Bind to (find-or-creating) the identity whose external_id is this directory ref. Mutually exclusive with identity_id/mint. Also reports the resolved ref.",
			},
			"mint": schema.BoolAttribute{
				Optional:    true,
				Description: "Provision a fresh per-agent NHI identity and bind it. Mutually exclusive with identity_id/identity_ref.",
			},
			"allow_unknown": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
				Description: "Permit binding to an identity whose principal type the source never revealed. Defaults to false (an unknown is never silently treated as an NHI).",
			},
			"minted": schema.BoolAttribute{
				Computed:      true,
				Description:   "Whether a fresh NHI identity was minted for this binding.",
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
			"shared": schema.BoolAttribute{
				Computed:    true,
				Description: "Whether the bound identity is shared across more than one agent (which collapses per-agent attribution).",
			},
			"agent_count": schema.Int64Attribute{
				Computed:    true,
				Description: "Number of agents bound to the same identity.",
			},
		},
	}
}

// ValidateConfig enforces that exactly one of identity_id / identity_ref / mint is
// set (the engine also enforces this, but a config-time error is a better UX).
func (r *agentIdentityBindingResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg agentIdentityBindingResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Skip while any selector is unknown (unresolved interpolation).
	if cfg.IdentityID.IsUnknown() || cfg.IdentityRef.IsUnknown() || cfg.Mint.IsUnknown() {
		return
	}
	selectors := 0
	if !cfg.IdentityID.IsNull() && cfg.IdentityID.ValueString() != "" {
		selectors++
	}
	if !cfg.IdentityRef.IsNull() && cfg.IdentityRef.ValueString() != "" {
		selectors++
	}
	if !cfg.Mint.IsNull() && cfg.Mint.ValueBool() {
		selectors++
	}
	if selectors != 1 {
		resp.Diagnostics.AddError(
			"Exactly one identity selector required",
			"Set exactly one of identity_id, identity_ref or mint to choose the identity to bind.",
		)
	}
}

// Configure receives the shared provider data.
func (r *agentIdentityBindingResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create binds the agent to its identity.
func (r *agentIdentityBindingResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan agentIdentityBindingResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	binding, err := r.data.client.BindAgentIdentity(ctx, r.tenantOverride(plan), plan.AgentID.ValueString(),
		valueOrEmpty(plan.IdentityID), valueOrEmpty(plan.IdentityRef), plan.Mint.ValueBool(), plan.AllowUnknown.ValueBool())
	if err != nil {
		resp.Diagnostics.AddError("Unable to bind agent identity", err.Error())
		return
	}
	applyBinding(&plan, binding)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes the binding from the bindings list, removing the resource if the
// agent is no longer bound (out-of-band unbind).
func (r *agentIdentityBindingResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state agentIdentityBindingResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	binding, err := r.data.client.GetBinding(ctx, r.tenantOverride(state), state.AgentID.ValueString())
	if err != nil {
		if errors.Is(err, client.ErrNotFound) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read agent identity binding", err.Error())
		return
	}
	// The bindings LIST does not carry `minted` (only the bind response does), so
	// preserve the bind-time value rather than clobbering it to false on refresh.
	prevMinted := state.Minted
	applyBinding(&state, binding)
	if !prevMinted.IsNull() && !prevMinted.IsUnknown() {
		state.Minted = prevMinted
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update rebinds the agent (idempotent re-POST). agent_id is RequiresReplace.
func (r *agentIdentityBindingResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan agentIdentityBindingResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	binding, err := r.data.client.BindAgentIdentity(ctx, r.tenantOverride(plan), plan.AgentID.ValueString(),
		valueOrEmpty(plan.IdentityID), valueOrEmpty(plan.IdentityRef), plan.Mint.ValueBool(), plan.AllowUnknown.ValueBool())
	if err != nil {
		resp.Diagnostics.AddError("Unable to update agent identity binding", err.Error())
		return
	}
	applyBinding(&plan, binding)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete unbinds the agent's identity.
func (r *agentIdentityBindingResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state agentIdentityBindingResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.data.client.UnbindAgentIdentity(ctx, r.tenantOverride(state), state.AgentID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Unable to unbind agent identity", err.Error())
	}
}

// ImportState imports an existing binding by its agent id.
func (r *agentIdentityBindingResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("agent_id"), req, resp)
}

// tenantOverride returns the per-resource tenant when set, otherwise empty.
func (r *agentIdentityBindingResource) tenantOverride(m agentIdentityBindingResourceModel) string {
	if !m.Tenant.IsNull() && !m.Tenant.IsUnknown() {
		return m.Tenant.ValueString()
	}
	return ""
}

// applyBinding writes the engine's binding back onto the model. The id mirrors the
// agent id; mint stays as configured (the engine reports `minted`, not `mint`).
func applyBinding(m *agentIdentityBindingResourceModel, b *client.Binding) {
	m.ID = types.StringValue(b.AgentID)
	m.AgentID = types.StringValue(b.AgentID)
	m.IdentityID = types.StringValue(b.IdentityID)
	if b.IdentityRef == "" {
		m.IdentityRef = types.StringNull()
	} else {
		m.IdentityRef = types.StringValue(b.IdentityRef)
	}
	m.Minted = types.BoolValue(b.Minted)
	m.Shared = types.BoolValue(b.Shared)
	m.AgentCount = types.Int64Value(b.AgentCount)
}

// valueOrEmpty returns the string value, or "" when null/unknown.
func valueOrEmpty(v types.String) string {
	if v.IsNull() || v.IsUnknown() {
		return ""
	}
	return v.ValueString()
}
