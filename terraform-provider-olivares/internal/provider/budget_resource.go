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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/olivaresai/olivares/terraform-provider-olivares/internal/client"
)

// Compile-time interface assertions.
var (
	_ resource.Resource                   = (*budgetResource)(nil)
	_ resource.ResourceWithConfigure      = (*budgetResource)(nil)
	_ resource.ResourceWithImportState    = (*budgetResource)(nil)
	_ resource.ResourceWithValidateConfig = (*budgetResource)(nil)
)

// Engine defaults for a budget (FinOps module). Mirrored here as schema defaults
// so a plan is stable: the value the engine fills when omitted equals what
// Terraform records, so there is no perpetual diff.
const (
	defaultBudgetDimension = "global"
	defaultBudgetPeriod    = "monthly"
	defaultBudgetCurrency  = "USD"
	defaultBudgetAction    = "alert"
)

// budgetResource manages a FinOps budget (module XI) as code: a named spend cap
// on a dimension over a period, with alert thresholds and an enforcement action.
// It is the cost-guardrail surface a platform team declares in HCL.
//
// IMPORTANT: a "throttle"/"block" action makes FinOps EMIT a hard-cap signal an
// actuation seam (the orchestration HITL gate / model router) may consume; FinOps
// itself never denies a request. The engine enforces finops:budget:write on the
// bearer token and validates the spec — this resource is a declarative client.
type budgetResource struct {
	data *providerData
}

// budgetResourceModel maps the olivares_budget schema. Every field round-trips
// from the engine's typed budgetDTO, so drift is detected per-attribute (no
// spec_hash needed — unlike the free-form policy/deployment specs).
type budgetResourceModel struct {
	ID               types.String `tfsdk:"id"`
	Tenant           types.String `tfsdk:"tenant"`
	Name             types.String `tfsdk:"name"`
	Enabled          types.Bool   `tfsdk:"enabled"`
	Dimension        types.String `tfsdk:"dimension"`
	Key              types.String `tfsdk:"key"`
	LimitMicroUSD    types.Int64  `tfsdk:"limit_micro_usd"`
	Period           types.String `tfsdk:"period"`
	Thresholds       types.List   `tfsdk:"thresholds"`
	Currency         types.String `tfsdk:"currency"`
	Action           types.String `tfsdk:"action"`
	ReservedMicroUSD types.Int64  `tfsdk:"reserved_micro_usd"`
}

// NewBudgetResource is the resource constructor registered with the provider.
func NewBudgetResource() resource.Resource { return &budgetResource{} }

// Metadata sets the full resource type name: olivares_budget.
func (r *budgetResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_budget"
}

// Schema declares the olivares_budget attributes.
func (r *budgetResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A FinOps spend budget in the Olivares AI control plane (module XI): a named, enabled spend cap on a " +
			"dimension over a period, with alert thresholds and an enforcement action. Authoring requires finops:budget:write.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Server-assigned budget ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"tenant": schema.StringAttribute{
				Optional:    true,
				Description: "Tenant UUID for this resource, overriding the provider-level tenant. Sent as X-Olivares-Tenant.",
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Human-readable budget name.",
			},
			"enabled": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
				Description: "Whether the budget is active. Defaults to true.",
			},
			"dimension": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString(defaultBudgetDimension),
				Description: "Spend dimension the cap scopes on: global, model, provider, agent, session, team, project, " +
					"workspace, api_key, actor, service_tier, context_window, inference_geo or gateway. Defaults to \"global\". " +
					"(cost_type is rejected — it never accrues against the estimated stream budgets aggregate.)",
			},
			"key": schema.StringAttribute{
				Optional:    true,
				Description: "The dimension key to scope on (e.g. the model id for dimension=model). Required for any non-global dimension; must be empty for global.",
			},
			"limit_micro_usd": schema.Int64Attribute{
				Required:    true,
				Description: "Spend cap for the period, in micro-USD (1 USD = 1_000_000). Must be positive.",
			},
			"period": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(defaultBudgetPeriod),
				Description: "Reset period: daily, weekly, monthly or total. Defaults to \"monthly\".",
			},
			"thresholds": schema.ListAttribute{
				Optional:    true,
				ElementType: types.Float64Type,
				Description: "Fractional alert thresholds in (0,1], e.g. [0.8, 0.9, 1.0] to alert at 80/90/100% of the limit. Omit for none.",
			},
			"currency": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(defaultBudgetCurrency),
				Description: "Reporting currency label. Defaults to \"USD\". (Limits are always micro-USD; this is a display label.)",
			},
			"action": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString(defaultBudgetAction),
				Description: "Enforcement action on breach: \"alert\" (showback-only, the safe default), \"throttle\" or \"block\" " +
					"(additionally emit a hard-cap signal an actuation seam may consume). Defaults to \"alert\".",
			},
			"reserved_micro_usd": schema.Int64Attribute{
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(0),
				Description: "Committed/reserved capacity (e.g. a Priority Tier commitment) counted toward the limit so a reservation cannot be silently over-consumed. An accounting line, not a charge. Defaults to 0.",
			},
		},
	}
}

// ValidateConfig enforces the engine's dimension/key invariant at plan time for a
// better UX (the engine also enforces it server-side).
func (r *budgetResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg budgetResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// dimension defaults to "global" when omitted; only validate when it is set
	// to a concrete non-global value and key is known.
	if cfg.Dimension.IsUnknown() || cfg.Key.IsUnknown() {
		return
	}
	dim := cfg.Dimension.ValueString()
	if cfg.Dimension.IsNull() || dim == "" || dim == defaultBudgetDimension {
		return
	}
	if cfg.Key.IsNull() || cfg.Key.ValueString() == "" {
		resp.Diagnostics.AddAttributeError(path.Root("key"), "key required for non-global dimension",
			fmt.Sprintf("dimension %q scopes the budget on a specific key; set `key` to the value to cap (e.g. the model id for dimension=model).", dim))
	}
}

// Configure receives the shared provider data (REST client + tenant default).
func (r *budgetResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create authors a new budget.
func (r *budgetResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan budgetResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	b, diags := toBudget(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	created, err := r.data.client.CreateBudget(ctx, r.tenantOverride(plan), b)
	if err != nil {
		resp.Diagnostics.AddError("Unable to create budget", err.Error())
		return
	}
	resp.Diagnostics.Append(applyBudget(ctx, &plan, created)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes state from the engine, removing the resource on 404. Every field
// round-trips, so an out-of-band edit surfaces as ordinary attribute drift.
func (r *budgetResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state budgetResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	got, err := r.data.client.GetBudget(ctx, r.tenantOverride(state), state.ID.ValueString())
	if err != nil {
		if errors.Is(err, client.ErrNotFound) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read budget", err.Error())
		return
	}
	resp.Diagnostics.Append(applyBudget(ctx, &state, got)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update applies changes to an existing budget.
func (r *budgetResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan budgetResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var state budgetResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = state.ID
	b, diags := toBudget(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	updated, err := r.data.client.UpdateBudget(ctx, r.tenantOverride(plan), plan.ID.ValueString(), b)
	if err != nil {
		resp.Diagnostics.AddError("Unable to update budget", err.Error())
		return
	}
	resp.Diagnostics.Append(applyBudget(ctx, &plan, updated)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete removes a budget.
func (r *budgetResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state budgetResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.data.client.DeleteBudget(ctx, r.tenantOverride(state), state.ID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Unable to delete budget", err.Error())
	}
}

// ImportState imports an existing budget by its id.
func (r *budgetResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// tenantOverride returns the per-resource tenant when set, otherwise empty.
func (r *budgetResource) tenantOverride(m budgetResourceModel) string {
	if !m.Tenant.IsNull() && !m.Tenant.IsUnknown() {
		return m.Tenant.ValueString()
	}
	return ""
}

// toBudget maps the writable plan fields onto a client.Budget.
func toBudget(ctx context.Context, m budgetResourceModel) (client.Budget, diag.Diagnostics) {
	b := client.Budget{
		Name:             m.Name.ValueString(),
		Enabled:          m.Enabled.ValueBool(),
		Dimension:        m.Dimension.ValueString(),
		Key:              m.Key.ValueString(),
		LimitMicroUSD:    m.LimitMicroUSD.ValueInt64(),
		Period:           m.Period.ValueString(),
		Currency:         m.Currency.ValueString(),
		Action:           m.Action.ValueString(),
		ReservedMicroUSD: m.ReservedMicroUSD.ValueInt64(),
	}
	var diags diag.Diagnostics
	if !m.Thresholds.IsNull() && !m.Thresholds.IsUnknown() {
		var ts []float64
		diags.Append(m.Thresholds.ElementsAs(ctx, &ts, false)...)
		b.Thresholds = ts
	}
	return b, diags
}

// applyBudget writes the engine's budgetDTO back onto the model. Thresholds are
// kept null when the engine returns none (so a config that never set them stays
// consistent).
func applyBudget(ctx context.Context, m *budgetResourceModel, b *client.Budget) diag.Diagnostics {
	m.ID = types.StringValue(b.ID)
	m.Name = types.StringValue(b.Name)
	m.Enabled = types.BoolValue(b.Enabled)
	m.Dimension = types.StringValue(b.Dimension)
	m.LimitMicroUSD = types.Int64Value(b.LimitMicroUSD)
	m.Period = types.StringValue(b.Period)
	m.Currency = types.StringValue(b.Currency)
	m.Action = types.StringValue(b.Action)
	m.ReservedMicroUSD = types.Int64Value(b.ReservedMicroUSD)
	if b.Key == "" {
		m.Key = types.StringNull()
	} else {
		m.Key = types.StringValue(b.Key)
	}
	if len(b.Thresholds) == 0 {
		m.Thresholds = types.ListNull(types.Float64Type)
		return nil
	}
	list, d := types.ListValueFrom(ctx, types.Float64Type, b.Thresholds)
	m.Thresholds = list
	return d
}
