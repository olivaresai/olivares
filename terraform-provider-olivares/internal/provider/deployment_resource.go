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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/olivaresai/olivares/terraform-provider-olivares/internal/client"
)

// Compile-time interface assertions.
var (
	_ resource.Resource                = (*deploymentResource)(nil)
	_ resource.ResourceWithConfigure   = (*deploymentResource)(nil)
	_ resource.ResourceWithImportState = (*deploymentResource)(nil)
)

// deploymentResource manages the DESIRED STATE of a deployment (module VII). It is
// the GitOps/manage-as-code surface: a deployment definition declared in HCL and
// reconciled against the engine's deploy module.
//
// IMPORTANT: applying this resource declares desired state in the control plane;
// it does NOT mutate the customer's infrastructure. Reconciling the definition to
// the real estate (the actual provision/update/retire) is a separate, HITL-
// governed action in the engine (deny-by-default). `terraform destroy` likewise
// removes only the desired-state record — and is refused while the deployment is
// still applied (retire it first).
type deploymentResource struct {
	data *providerData
}

// deploymentResourceModel maps the olivares_deployment schema to Go values.
type deploymentResourceModel struct {
	ID             types.String `tfsdk:"id"`
	Tenant         types.String `tfsdk:"tenant"`
	SubjectKind    types.String `tfsdk:"subject_kind"`
	SubjectRef     types.String `tfsdk:"subject_ref"`
	Name           types.String `tfsdk:"name"`
	Environment    types.String `tfsdk:"environment"`
	Target         types.String `tfsdk:"target"`
	Runtime        types.String `tfsdk:"runtime"`
	SourceRef      types.String `tfsdk:"source_ref"`
	Spec           types.String `tfsdk:"spec"`
	DesiredStatus  types.String `tfsdk:"desired_status"`
	CurrentVersion types.Int64  `tfsdk:"current_version"`
	AppliedVersion types.Int64  `tfsdk:"applied_version"`
	SpecHash       types.String `tfsdk:"spec_hash"`
}

// NewDeploymentResource is the resource constructor registered with the provider.
func NewDeploymentResource() resource.Resource { return &deploymentResource{} }

// Metadata sets the full resource type name: olivares_deployment.
func (r *deploymentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_deployment"
}

// requiresReplace is the plan modifier for the definition's immutable identity
// fields (the engine's update path does not change them).
func requiresReplace() []planmodifier.String {
	return []planmodifier.String{stringplanmodifier.RequiresReplace()}
}

// Schema declares the olivares_deployment attributes.
func (r *deploymentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Desired state of an agent/MCP deployment in the Olivares AI control plane (module VII). " +
			"Declaring this resource records desired state only; reconciling it to real infrastructure is a separate, human-in-the-loop-governed action in the engine and is never triggered by terraform apply.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Server-assigned definition ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"tenant": schema.StringAttribute{
				Optional:    true,
				Description: "Tenant UUID for this resource, overriding the provider-level tenant. Sent as X-Olivares-Tenant.",
			},
			"subject_kind": schema.StringAttribute{
				Required:      true,
				Description:   "What is deployed: \"agent\" or \"mcp_server\". Immutable (changing it replaces the resource).",
				PlanModifiers: requiresReplace(),
			},
			"subject_ref": schema.StringAttribute{
				Required:      true,
				Description:   "Logical reference of the deployed subject. Immutable.",
				PlanModifiers: requiresReplace(),
			},
			"name": schema.StringAttribute{
				Required:      true,
				Description:   "Logical deployment name, unique per environment. Immutable.",
				PlanModifiers: requiresReplace(),
			},
			"environment": schema.StringAttribute{
				Required:      true,
				Description:   "Deployment environment (e.g. \"prod\", \"staging\"). Immutable.",
				PlanModifiers: requiresReplace(),
			},
			"runtime": schema.StringAttribute{
				Required:      true,
				Description:   "Executor/runtime kind (e.g. \"docker\", \"k8s\"). Immutable.",
				PlanModifiers: requiresReplace(),
			},
			"target": schema.StringAttribute{
				Required:    true,
				Description: "Runtime target reference from the inventory (e.g. \"docker.host/node1\", \"k8s.namespace/prod\").",
			},
			"source_ref": schema.StringAttribute{
				Optional:    true,
				Description: "GitOps source reference (e.g. \"git:<repo>#<commit>\"). Never a credential.",
			},
			"spec": schema.StringAttribute{
				Required:    true,
				Description: "The desired-state spec as a JSON document (image/command/resources/env_refs/wirings/identity). Secrets are referenced by secret_ref only — never cleartext.",
			},
			"desired_status": schema.StringAttribute{
				Computed:    true,
				Description: "Desired lifecycle status as resolved by the engine (\"active\"/\"retired\").",
			},
			"current_version": schema.Int64Attribute{
				Computed:    true,
				Description: "Latest declared revision number (desired).",
			},
			"applied_version": schema.Int64Attribute{
				Computed:    true,
				Description: "Revision actually reconciled to infrastructure (real); 0 = never applied.",
			},
			"spec_hash": schema.StringAttribute{
				Computed:    true,
				Description: "Hex SHA-256 of the current desired spec, as computed by the engine.",
			},
		},
	}
}

// Configure receives the shared provider data (REST client + tenant default).
func (r *deploymentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create declares a new deployment definition.
func (r *deploymentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan deploymentResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !json.Valid([]byte(plan.Spec.ValueString())) {
		resp.Diagnostics.AddAttributeError(path.Root("spec"), "Invalid spec", "spec must be a valid JSON document (use jsonencode).")
		return
	}
	created, err := r.data.client.CreateDeployment(ctx, r.tenantOverride(plan), toDeployment(plan))
	if err != nil {
		resp.Diagnostics.AddError("Unable to create deployment", err.Error())
		return
	}
	applyDeployment(&plan, created)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes computed state from the engine, removing the resource on 404. It
// deliberately does NOT overwrite the configured spec from the server's canonical
// re-serialization (which could differ only in formatting); the spec_hash is the
// drift signal. Out-of-band spec edits via the API are not reconciled here.
func (r *deploymentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state deploymentResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	got, err := r.data.client.GetDeployment(ctx, r.tenantOverride(state), state.ID.ValueString())
	if err != nil {
		if errors.Is(err, client.ErrNotFound) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read deployment", err.Error())
		return
	}
	applyDeployment(&state, got)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update declares a new desired revision (and optionally changes target/source_ref).
func (r *deploymentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan deploymentResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var state deploymentResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = state.ID
	if !json.Valid([]byte(plan.Spec.ValueString())) {
		resp.Diagnostics.AddAttributeError(path.Root("spec"), "Invalid spec", "spec must be a valid JSON document (use jsonencode).")
		return
	}
	updated, err := r.data.client.UpdateDeployment(ctx, r.tenantOverride(plan), plan.ID.ValueString(), toDeployment(plan))
	if err != nil {
		resp.Diagnostics.AddError("Unable to update deployment", err.Error())
		return
	}
	applyDeployment(&plan, updated)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete removes the desired-state record (refused by the engine while applied).
func (r *deploymentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state deploymentResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.data.client.DeleteDeployment(ctx, r.tenantOverride(state), state.ID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Unable to delete deployment", err.Error())
	}
}

// ImportState imports an existing deployment definition by its id.
func (r *deploymentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// tenantOverride returns the per-resource tenant when set, otherwise empty.
func (r *deploymentResource) tenantOverride(m deploymentResourceModel) string {
	if !m.Tenant.IsNull() && !m.Tenant.IsUnknown() {
		return m.Tenant.ValueString()
	}
	return ""
}

// toDeployment maps the writable plan fields onto a client.Deployment.
func toDeployment(m deploymentResourceModel) client.Deployment {
	return client.Deployment{
		SubjectKind: m.SubjectKind.ValueString(), SubjectRef: m.SubjectRef.ValueString(),
		Name: m.Name.ValueString(), Environment: m.Environment.ValueString(),
		Target: m.Target.ValueString(), Runtime: m.Runtime.ValueString(),
		SourceRef: m.SourceRef.ValueString(), Spec: json.RawMessage(m.Spec.ValueString()),
	}
}

// applyDeployment writes the engine's DeploymentDTO back onto the model, leaving
// the configured spec untouched (see Read).
func applyDeployment(m *deploymentResourceModel, d *client.Deployment) {
	m.ID = types.StringValue(d.ID)
	m.SubjectKind = types.StringValue(d.SubjectKind)
	m.SubjectRef = types.StringValue(d.SubjectRef)
	m.Name = types.StringValue(d.Name)
	m.Environment = types.StringValue(d.Environment)
	m.Target = types.StringValue(d.Target)
	m.Runtime = types.StringValue(d.Runtime)
	m.DesiredStatus = types.StringValue(d.DesiredStatus)
	m.CurrentVersion = types.Int64Value(d.CurrentVersion)
	m.AppliedVersion = types.Int64Value(d.AppliedVersion)
	m.SpecHash = types.StringValue(d.SpecHash)
	if d.SourceRef == "" {
		m.SourceRef = types.StringNull()
	} else {
		m.SourceRef = types.StringValue(d.SourceRef)
	}
}
