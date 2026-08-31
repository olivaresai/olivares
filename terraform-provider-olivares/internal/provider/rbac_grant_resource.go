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
	_ resource.Resource                = (*rbacGrantResource)(nil)
	_ resource.ResourceWithConfigure   = (*rbacGrantResource)(nil)
	_ resource.ResourceWithImportState = (*rbacGrantResource)(nil)
)

// rbacGrantResource manages a governance RBAC grant (module I) as code: an
// immutable binding of a subject (user, group, service account) to a role,
// optionally scoped to a resource boundary. It is the scoped-grants surface a
// platform team declares in HCL.
//
// Grants are immutable: changing any writable attribute forces a destroy-then-
// create replacement. The engine enforces governance:rbac:admin on the bearer
// token — this resource is a declarative client, never a governance bypass.
type rbacGrantResource struct {
	data *providerData
}

// rbacGrantResourceModel maps the olivares_rbac_grant schema. Every field
// round-trips from the engine's grant DTO, so drift is detected per-attribute.
type rbacGrantResourceModel struct {
	ID          types.String `tfsdk:"id"`
	Tenant      types.String `tfsdk:"tenant"`
	SubjectType types.String `tfsdk:"subject_type"`
	SubjectRef  types.String `tfsdk:"subject_ref"`
	Role        types.String `tfsdk:"role"`
	Scope       types.String `tfsdk:"scope"`
	ScopeRef    types.String `tfsdk:"scope_ref"`
	CreatedAt   types.String `tfsdk:"created_at"`
}

// NewRBACGrantResource is the resource constructor registered with the
// provider.
func NewRBACGrantResource() resource.Resource { return &rbacGrantResource{} }

// Metadata sets the full resource type name: olivares_rbac_grant.
func (r *rbacGrantResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_rbac_grant"
}

// Schema declares the olivares_rbac_grant attributes. All writable attributes
// carry RequiresReplace because grants are immutable (create/delete only).
func (r *rbacGrantResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "An RBAC grant in the Olivares AI governance control plane (module I): an immutable binding of a " +
			"subject to a role, optionally scoped to a resource boundary. Changing any attribute forces replacement. " +
			"Authoring requires governance:rbac:admin.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Server-assigned grant ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"tenant": schema.StringAttribute{
				Optional:    true,
				Description: "Tenant UUID for this resource, overriding the provider-level tenant. Sent as X-Olivares-Tenant.",
			},
			"subject_type": schema.StringAttribute{
				Required:      true,
				Description:   "Subject type: user, group or service_account. Immutable (changing it replaces the resource).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"subject_ref": schema.StringAttribute{
				Required:      true,
				Description:   "Subject reference (e.g. user email, group id, service account id). Immutable (changing it replaces the resource).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"role": schema.StringAttribute{
				Required:      true,
				Description:   "Role to grant (e.g. admin, viewer, editor). Immutable (changing it replaces the resource).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"scope": schema.StringAttribute{
				Optional:      true,
				Description:   "Optional scope type for the grant (e.g. project, workspace). Immutable (changing it replaces the resource).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"scope_ref": schema.StringAttribute{
				Optional:      true,
				Description:   "Optional scope reference narrowing the grant to a specific resource (e.g. project id). Immutable (changing it replaces the resource).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"created_at": schema.StringAttribute{
				Computed:      true,
				Description:   "Server-assigned creation timestamp (RFC 3339).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

// Configure receives the shared provider data (REST client + tenant default).
func (r *rbacGrantResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create authors a new RBAC grant.
func (r *rbacGrantResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan rbacGrantResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	g := toRBACGrant(plan)
	created, err := r.data.client.CreateRBACGrant(ctx, r.tenantOverride(plan), g)
	if err != nil {
		resp.Diagnostics.AddError("Unable to create RBAC grant", err.Error())
		return
	}
	applyRBACGrant(&plan, created)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes state from the engine, removing the resource on 404. Every
// field round-trips, so an out-of-band deletion surfaces as resource removal.
func (r *rbacGrantResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state rbacGrantResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	got, err := r.data.client.GetRBACGrant(ctx, r.tenantOverride(state), state.ID.ValueString())
	if err != nil {
		if errors.Is(err, client.ErrNotFound) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read RBAC grant", err.Error())
		return
	}
	applyRBACGrant(&state, got)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is a no-op stub — grants are immutable; every writable attribute has
// RequiresReplace, so Terraform will never call Update. The method is required
// to satisfy the resource.Resource interface.
func (r *rbacGrantResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Update not supported",
		"RBAC grants are immutable. Changing any attribute forces a replacement (destroy + create).")
}

// Delete removes an RBAC grant.
func (r *rbacGrantResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state rbacGrantResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.data.client.DeleteRBACGrant(ctx, r.tenantOverride(state), state.ID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Unable to delete RBAC grant", err.Error())
	}
}

// ImportState imports an existing grant by its id.
func (r *rbacGrantResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// tenantOverride returns the per-resource tenant when set, otherwise empty.
func (r *rbacGrantResource) tenantOverride(m rbacGrantResourceModel) string {
	if !m.Tenant.IsNull() && !m.Tenant.IsUnknown() {
		return m.Tenant.ValueString()
	}
	return ""
}

// toRBACGrant maps the writable plan fields onto a client.RBACGrant.
func toRBACGrant(m rbacGrantResourceModel) client.RBACGrant {
	g := client.RBACGrant{
		SubjectType: m.SubjectType.ValueString(),
		SubjectRef:  m.SubjectRef.ValueString(),
		Role:        m.Role.ValueString(),
	}
	if !m.Scope.IsNull() && !m.Scope.IsUnknown() {
		g.Scope = m.Scope.ValueString()
	}
	if !m.ScopeRef.IsNull() && !m.ScopeRef.IsUnknown() {
		g.ScopeRef = m.ScopeRef.ValueString()
	}
	return g
}

// applyRBACGrant writes the engine's grant DTO back onto the model. Scope and
// ScopeRef are kept null when the engine returns empty (so a config that never
// set them stays consistent).
func applyRBACGrant(m *rbacGrantResourceModel, g *client.RBACGrant) {
	m.ID = types.StringValue(g.ID)
	m.SubjectType = types.StringValue(g.SubjectType)
	m.SubjectRef = types.StringValue(g.SubjectRef)
	m.Role = types.StringValue(g.Role)
	m.CreatedAt = types.StringValue(g.CreatedAt)
	if g.Scope == "" {
		m.Scope = types.StringNull()
	} else {
		m.Scope = types.StringValue(g.Scope)
	}
	if g.ScopeRef == "" {
		m.ScopeRef = types.StringNull()
	} else {
		m.ScopeRef = types.StringValue(g.ScopeRef)
	}
}
