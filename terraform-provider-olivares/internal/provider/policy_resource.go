// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/olivaresai/olivares/terraform-provider-olivares/internal/client"
)

// Compile-time interface assertions.
var (
	_ resource.Resource                = (*policyResource)(nil)
	_ resource.ResourceWithConfigure   = (*policyResource)(nil)
	_ resource.ResourceWithImportState = (*policyResource)(nil)
)

// policyResource manages a governance policy (module VI) as code: an ABAC
// deny-rule policy or a human-in-the-loop approval policy, authored in HCL and
// reconciled against the governance module's REST API.
//
// IMPORTANT: the engine enforces governance:policy:admin on the bearer token and
// strict-parses the spec server-side; this resource is a declarative client, not
// a bypass of the authorization or validation the engine imposes.
type policyResource struct {
	data *providerData
}

// policyResourceModel maps the olivares_policy schema to Go values. The submitted
// spec is preserved verbatim; spec_canonical exposes the engine's canonical
// re-serialization (unknown/empty fields dropped) as the drift signal — mirroring
// olivares_deployment's spec_hash, since the engine rewrites the spec on read.
type policyResourceModel struct {
	ID            types.String `tfsdk:"id"`
	Tenant        types.String `tfsdk:"tenant"`
	Name          types.String `tfsdk:"name"`
	Kind          types.String `tfsdk:"kind"`
	Enabled       types.Bool   `tfsdk:"enabled"`
	Spec          types.String `tfsdk:"spec"`
	SpecCanonical types.String `tfsdk:"spec_canonical"`
}

// NewPolicyResource is the resource constructor registered with the provider.
func NewPolicyResource() resource.Resource { return &policyResource{} }

// Metadata sets the full resource type name: olivares_policy.
func (r *policyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_policy"
}

// Schema declares the olivares_policy attributes.
func (r *policyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A governance policy in the Olivares AI control plane (module VI). " +
			"An \"abac\" policy carries deny rules that further-restrict the RBAC grant (it can only narrow, never widen); " +
			"an \"approval\" policy declares the human-in-the-loop chain for matching actions. Authoring requires the " +
			"governance:policy:admin permission, enforced by the engine.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Server-assigned policy ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"tenant": schema.StringAttribute{
				Optional:    true,
				Description: "Tenant UUID for this resource, overriding the provider-level tenant. Sent as X-Olivares-Tenant.",
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Human-readable policy name.",
			},
			"kind": schema.StringAttribute{
				Required:      true,
				Description:   "Policy kind: \"abac\" or \"approval\". Immutable (changing it replaces the resource).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"enabled": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
				Description: "Whether the policy is active. Defaults to true.",
			},
			"spec": schema.StringAttribute{
				Required: true,
				Description: "The policy spec as a JSON document (use jsonencode). For \"abac\": {\"rules\":[{\"deny\":true," +
					"\"permission\":...,\"verb\":...,\"resource\":...,\"principal_kind\":...}]}. For \"approval\": " +
					"{\"required_approvals\":N,\"expires_in_seconds\":N,\"escalate_in_seconds\":N,\"match\":{\"action\":...," +
					"\"subject_kind\":...}}. Secrets must never appear here; the engine rejects unknown fields.",
			},
			"spec_canonical": schema.StringAttribute{
				Computed: true,
				Description: "The engine's canonical re-serialization of the stored spec (unknown/empty fields dropped). " +
					"It is the drift signal: a change here against an unchanged configured spec means the policy was edited out of band.",
			},
		},
	}
}

// Configure receives the shared provider data (REST client + tenant default).
func (r *policyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create authors a new governance policy.
func (r *policyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan policyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !json.Valid([]byte(plan.Spec.ValueString())) {
		resp.Diagnostics.AddAttributeError(path.Root("spec"), "Invalid spec", "spec must be a valid JSON document (use jsonencode).")
		return
	}
	created, err := r.data.client.CreatePolicy(ctx, r.tenantOverride(plan), toPolicy(plan))
	if err != nil {
		resp.Diagnostics.AddError("Unable to create policy", err.Error())
		return
	}
	applyPolicy(&plan, created)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes computed state from the engine, removing the resource on 404. It
// leaves the configured spec untouched (the engine canonicalizes it); spec_canonical
// carries the drift signal.
func (r *policyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state policyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	got, err := r.data.client.GetPolicy(ctx, r.tenantOverride(state), state.ID.ValueString())
	if err != nil {
		if errors.Is(err, client.ErrNotFound) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read policy", err.Error())
		return
	}
	applyPolicy(&state, got)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update applies changes to an existing governance policy (kind is immutable and
// marked RequiresReplace, so it never changes here).
func (r *policyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan policyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var state policyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = state.ID
	if !json.Valid([]byte(plan.Spec.ValueString())) {
		resp.Diagnostics.AddAttributeError(path.Root("spec"), "Invalid spec", "spec must be a valid JSON document (use jsonencode).")
		return
	}
	updated, err := r.data.client.UpdatePolicy(ctx, r.tenantOverride(plan), plan.ID.ValueString(), toPolicy(plan))
	if err != nil {
		resp.Diagnostics.AddError("Unable to update policy", err.Error())
		return
	}
	applyPolicy(&plan, updated)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete removes a governance policy.
func (r *policyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state policyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.data.client.DeletePolicy(ctx, r.tenantOverride(state), state.ID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Unable to delete policy", err.Error())
	}
}

// ImportState imports an existing policy by its id.
func (r *policyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// tenantOverride returns the per-resource tenant when set, otherwise empty.
func (r *policyResource) tenantOverride(m policyResourceModel) string {
	if !m.Tenant.IsNull() && !m.Tenant.IsUnknown() {
		return m.Tenant.ValueString()
	}
	return ""
}

// toPolicy maps the writable plan fields onto a client.Policy.
func toPolicy(m policyResourceModel) client.Policy {
	return client.Policy{
		Name:    m.Name.ValueString(),
		Kind:    m.Kind.ValueString(),
		Enabled: m.Enabled.ValueBool(),
		Spec:    json.RawMessage(m.Spec.ValueString()),
	}
}

// applyPolicy writes the engine's policyDTO back onto the model, leaving the
// configured spec untouched and recording the canonical spec as the drift signal.
func applyPolicy(m *policyResourceModel, p *client.Policy) {
	m.ID = types.StringValue(p.ID)
	m.Name = types.StringValue(p.Name)
	m.Kind = types.StringValue(p.Kind)
	m.Enabled = types.BoolValue(p.Enabled)
	if len(p.Spec) == 0 {
		m.SpecCanonical = types.StringValue("")
	} else {
		m.SpecCanonical = types.StringValue(string(p.Spec))
	}
}
