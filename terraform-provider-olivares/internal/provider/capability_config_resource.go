// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
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
	_ resource.Resource                = (*capabilityConfigResource)(nil)
	_ resource.ResourceWithConfigure   = (*capabilityConfigResource)(nil)
	_ resource.ResourceWithImportState = (*capabilityConfigResource)(nil)
)

// capabilityConfigResource manages the configuration of an MCP server connection
// (module: capabilities) as code: transport, endpoint, scope and secret
// references. It is the "connector/source" surface a platform team declares in
// HCL so the control plane's MCP connections live under source control.
//
// MINIMUM DATA (docs/SECURITY-HARDENING.md): the endpoint is a REFERENCE and the engine refuses
// one carrying an inline credential; secrets are declared by locator via
// secret_refs (env/vault/secret_manager/file/other), never as cleartext. The
// engine enforces both the authorization and the no-cleartext invariant — this
// resource is a declarative client, not a bypass.
type capabilityConfigResource struct {
	data *providerData
}

// capabilityConfigResourceModel maps the olivares_capability_config schema.
type capabilityConfigResourceModel struct {
	ID         types.String `tfsdk:"id"`
	Tenant     types.String `tfsdk:"tenant"`
	ServerRef  types.String `tfsdk:"server_ref"`
	Transport  types.String `tfsdk:"transport"`
	Endpoint   types.String `tfsdk:"endpoint"`
	Scope      types.String `tfsdk:"scope"`
	SecretRefs types.List   `tfsdk:"secret_refs"`
	Enabled    types.Bool   `tfsdk:"enabled"`
	Note       types.String `tfsdk:"note"`
	Revision   types.Int64  `tfsdk:"revision"`
}

// capabilitySecretRefModel is one secret reference in the secret_refs list.
type capabilitySecretRefModel struct {
	Name    types.String `tfsdk:"name"`
	RefKind types.String `tfsdk:"ref_kind"`
	Ref     types.String `tfsdk:"ref"`
	Hint    types.String `tfsdk:"hint"`
}

// secretRefObjectType is the element type of the secret_refs list, used to build
// a known-null/empty list value.
func secretRefObjectType() types.ObjectType {
	return types.ObjectType{AttrTypes: map[string]attr.Type{
		"name":     types.StringType,
		"ref_kind": types.StringType,
		"ref":      types.StringType,
		"hint":     types.StringType,
	}}
}

// NewCapabilityConfigResource is the resource constructor registered with the provider.
func NewCapabilityConfigResource() resource.Resource { return &capabilityConfigResource{} }

// Metadata sets the full resource type name: olivares_capability_config.
func (r *capabilityConfigResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_capability_config"
}

// Schema declares the olivares_capability_config attributes.
func (r *capabilityConfigResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Configuration of an MCP server connection in the Olivares AI control plane (capabilities module): " +
			"the \"connector/source\" of a tool server, declared as code. Secrets are referenced by locator only " +
			"(never cleartext); the engine rejects an endpoint or ref carrying an inline credential.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Server-assigned config ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"tenant": schema.StringAttribute{
				Optional:    true,
				Description: "Tenant UUID for this resource, overriding the provider-level tenant. Sent as X-Olivares-Tenant.",
			},
			"server_ref": schema.StringAttribute{
				Required:    true,
				Description: "Logical reference of the MCP server this config connects to.",
			},
			"transport": schema.StringAttribute{
				Required:    true,
				Description: "Transport: one of stdio, http, sse, ws.",
			},
			"endpoint": schema.StringAttribute{
				Optional:    true,
				Description: "Connection endpoint reference (e.g. a URL or command). Never an inline credential — the engine rejects one.",
			},
			"scope": schema.StringAttribute{
				Optional:    true,
				Description: "Optional scope label constraining where the config applies.",
			},
			"secret_refs": schema.ListNestedAttribute{
				Optional:    true,
				Description: "Secret references for the connection. Each is a LOCATOR (env var, Vault path, secret-manager key, file), never the secret value.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name":     schema.StringAttribute{Required: true, Description: "Logical name of the secret (e.g. \"api_key\")."},
						"ref_kind": schema.StringAttribute{Required: true, Description: "Locator kind: one of env, vault, secret_manager, file, other."},
						"ref":      schema.StringAttribute{Required: true, Description: "The locator (e.g. the env var name or Vault path). Never the credential value."},
						"hint":     schema.StringAttribute{Optional: true, Description: "Optional short masked partial for operator recognition (e.g. \"sk-…abcd\"). Never a full credential."},
					},
				},
			},
			"enabled": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
				Description: "Whether the connection config is active. Defaults to true.",
			},
			"note": schema.StringAttribute{
				Optional:    true,
				Description: "Optional free-text note.",
			},
			"revision": schema.Int64Attribute{
				Computed:    true,
				Description: "Engine-assigned revision, bumped on each update (the config keeps a revision history server-side).",
			},
		},
	}
}

// Configure receives the shared provider data (REST client + tenant default).
func (r *capabilityConfigResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create declares a new MCP server config.
func (r *capabilityConfigResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan capabilityConfigResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	cfg, diags := toCapabilityConfig(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	created, err := r.data.client.CreateCapabilityConfig(ctx, r.tenantOverride(plan), cfg)
	if err != nil {
		resp.Diagnostics.AddError("Unable to create capability config", err.Error())
		return
	}
	resp.Diagnostics.Append(applyCapabilityConfig(ctx, &plan, created)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes state from the engine, removing the resource on 404.
func (r *capabilityConfigResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state capabilityConfigResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	got, err := r.data.client.GetCapabilityConfig(ctx, r.tenantOverride(state), state.ID.ValueString())
	if err != nil {
		if errors.Is(err, client.ErrNotFound) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read capability config", err.Error())
		return
	}
	resp.Diagnostics.Append(applyCapabilityConfig(ctx, &state, got)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update applies changes to an existing MCP server config.
func (r *capabilityConfigResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan capabilityConfigResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var state capabilityConfigResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = state.ID
	cfg, diags := toCapabilityConfig(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	updated, err := r.data.client.UpdateCapabilityConfig(ctx, r.tenantOverride(plan), plan.ID.ValueString(), cfg)
	if err != nil {
		resp.Diagnostics.AddError("Unable to update capability config", err.Error())
		return
	}
	resp.Diagnostics.Append(applyCapabilityConfig(ctx, &plan, updated)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete removes an MCP server config.
func (r *capabilityConfigResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state capabilityConfigResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.data.client.DeleteCapabilityConfig(ctx, r.tenantOverride(state), state.ID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Unable to delete capability config", err.Error())
	}
}

// ImportState imports an existing MCP server config by its id.
func (r *capabilityConfigResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// tenantOverride returns the per-resource tenant when set, otherwise empty.
func (r *capabilityConfigResource) tenantOverride(m capabilityConfigResourceModel) string {
	if !m.Tenant.IsNull() && !m.Tenant.IsUnknown() {
		return m.Tenant.ValueString()
	}
	return ""
}

// toCapabilityConfig maps the writable plan fields onto a client.CapabilityConfig.
func toCapabilityConfig(ctx context.Context, m capabilityConfigResourceModel) (client.CapabilityConfig, diag.Diagnostics) {
	cfg := client.CapabilityConfig{
		ServerRef: m.ServerRef.ValueString(),
		Transport: m.Transport.ValueString(),
		Endpoint:  m.Endpoint.ValueString(),
		Scope:     m.Scope.ValueString(),
		Enabled:   m.Enabled.ValueBool(),
		Note:      m.Note.ValueString(),
	}
	var diags diag.Diagnostics
	if !m.SecretRefs.IsNull() && !m.SecretRefs.IsUnknown() {
		var refs []capabilitySecretRefModel
		diags.Append(m.SecretRefs.ElementsAs(ctx, &refs, false)...)
		cfg.SecretRefs = make([]client.SecretRef, 0, len(refs))
		for _, ref := range refs {
			cfg.SecretRefs = append(cfg.SecretRefs, client.SecretRef{
				Name:    ref.Name.ValueString(),
				RefKind: ref.RefKind.ValueString(),
				Ref:     ref.Ref.ValueString(),
				Hint:    ref.Hint.ValueString(),
			})
		}
	}
	return cfg, diags
}

// applyCapabilityConfig writes the engine's configDTO back onto the model. The
// engine normalizes (lowercases transport/ref_kind, trims), so the round-tripped
// state is the canonical form; keep optional fields null when the engine returns
// them empty so a config that never set them stays consistent.
func applyCapabilityConfig(ctx context.Context, m *capabilityConfigResourceModel, c *client.CapabilityConfig) diag.Diagnostics {
	m.ID = types.StringValue(c.ID)
	m.ServerRef = types.StringValue(c.ServerRef)
	m.Transport = types.StringValue(c.Transport)
	m.Enabled = types.BoolValue(c.Enabled)
	m.Revision = types.Int64Value(c.Revision)
	m.Endpoint = nullIfEmpty(c.Endpoint)
	m.Scope = nullIfEmpty(c.Scope)
	m.Note = nullIfEmpty(c.Note)

	if len(c.SecretRefs) == 0 {
		m.SecretRefs = types.ListNull(secretRefObjectType())
		return nil
	}
	refs := make([]capabilitySecretRefModel, 0, len(c.SecretRefs))
	for _, ref := range c.SecretRefs {
		refs = append(refs, capabilitySecretRefModel{
			Name:    types.StringValue(ref.Name),
			RefKind: types.StringValue(ref.RefKind),
			Ref:     types.StringValue(ref.Ref),
			Hint:    nullIfEmpty(ref.Hint),
		})
	}
	list, d := types.ListValueFrom(ctx, secretRefObjectType(), refs)
	m.SecretRefs = list
	return d
}

// nullIfEmpty maps "" to a null string, any other value to a known string. Used
// for optional attributes the engine omits (omitempty) when unset.
func nullIfEmpty(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}
