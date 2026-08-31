// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Compile-time interface assertions.
var (
	_ datasource.DataSource              = (*budgetsDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*budgetsDataSource)(nil)
)

// budgetsDataSource reads the FinOps budgets in the governed estate, so a
// Terraform/OpenTofu module can reference declared spend caps without
// reimplementing the REST call. Read-only; requires finops:budget:read.
type budgetsDataSource struct {
	data *providerData
}

// budgetDataModel is one budget in the data-source result.
type budgetDataModel struct {
	ID               types.String `tfsdk:"id"`
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

// budgetsDataSourceModel maps the olivares_budgets schema.
type budgetsDataSourceModel struct {
	Tenant  types.String      `tfsdk:"tenant"`
	Budgets []budgetDataModel `tfsdk:"budgets"`
}

// NewBudgetsDataSource is the data source constructor.
func NewBudgetsDataSource() datasource.DataSource { return &budgetsDataSource{} }

// Metadata sets the full data source type name: olivares_budgets.
func (d *budgetsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_budgets"
}

// Schema declares the olivares_budgets attributes.
func (d *budgetsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads the FinOps spend budgets in the Olivares AI control plane (read-only). Requires finops:budget:read.",
		Attributes: map[string]schema.Attribute{
			"tenant": schema.StringAttribute{
				Optional:    true,
				Description: "Tenant UUID, overriding the provider-level tenant. Sent as X-Olivares-Tenant.",
			},
			"budgets": schema.ListNestedAttribute{
				Computed:    true,
				Description: "The declared budgets.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id":                 schema.StringAttribute{Computed: true, Description: "Budget ID."},
						"name":               schema.StringAttribute{Computed: true, Description: "Budget name."},
						"enabled":            schema.BoolAttribute{Computed: true, Description: "Whether the budget is active."},
						"dimension":          schema.StringAttribute{Computed: true, Description: "Spend dimension scoped on."},
						"key":                schema.StringAttribute{Computed: true, Description: "Dimension key (empty for global)."},
						"limit_micro_usd":    schema.Int64Attribute{Computed: true, Description: "Spend cap for the period, in micro-USD."},
						"period":             schema.StringAttribute{Computed: true, Description: "Reset period."},
						"thresholds":         schema.ListAttribute{Computed: true, ElementType: types.Float64Type, Description: "Fractional alert thresholds."},
						"currency":           schema.StringAttribute{Computed: true, Description: "Reporting currency label."},
						"action":             schema.StringAttribute{Computed: true, Description: "Enforcement action: alert | throttle | block."},
						"reserved_micro_usd": schema.Int64Attribute{Computed: true, Description: "Reserved/committed capacity counted toward the limit."},
					},
				},
			},
		},
	}
}

// Configure receives the shared provider data.
func (d *budgetsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	data, ok := req.ProviderData.(*providerData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type", fmt.Sprintf("Expected *providerData, got %T. This is a provider bug.", req.ProviderData))
		return
	}
	d.data = data
}

// Read queries the budgets and writes them to state.
func (d *budgetsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg budgetsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	budgets, err := d.data.client.ListBudgets(ctx, dsTenant(cfg.Tenant))
	if err != nil {
		resp.Diagnostics.AddError("Unable to read budgets", err.Error())
		return
	}
	cfg.Budgets = make([]budgetDataModel, 0, len(budgets))
	for _, b := range budgets {
		thresholds, diags := thresholdList(ctx, b.Thresholds)
		resp.Diagnostics.Append(diags...)
		cfg.Budgets = append(cfg.Budgets, budgetDataModel{
			ID:               types.StringValue(b.ID),
			Name:             types.StringValue(b.Name),
			Enabled:          types.BoolValue(b.Enabled),
			Dimension:        types.StringValue(b.Dimension),
			Key:              nullIfEmpty(b.Key),
			LimitMicroUSD:    types.Int64Value(b.LimitMicroUSD),
			Period:           types.StringValue(b.Period),
			Thresholds:       thresholds,
			Currency:         types.StringValue(b.Currency),
			Action:           types.StringValue(b.Action),
			ReservedMicroUSD: types.Int64Value(b.ReservedMicroUSD),
		})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}

// thresholdList builds the thresholds list attribute, a known-empty list when
// the budget has none (a Computed list attribute must be a known value, never
// null here — same contract as stringList in inventory_data_source.go).
func thresholdList(ctx context.Context, ts []float64) (types.List, diag.Diagnostics) {
	if len(ts) == 0 {
		return types.ListValueFrom(ctx, types.Float64Type, []float64{})
	}
	return types.ListValueFrom(ctx, types.Float64Type, ts)
}
