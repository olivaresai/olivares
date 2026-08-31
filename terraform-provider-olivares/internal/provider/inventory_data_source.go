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
	_ datasource.DataSource              = (*inventoryDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*inventoryDataSource)(nil)
)

// inventoryDataSource reads the reconciled inventory of the governed estate — the
// cross-source view of what the control plane has discovered (agents, MCP
// servers, identities, resources…) — so a Terraform/OpenTofu module can reference
// the live estate without reimplementing the REST calls. Read-only; requires
// inventory:read.
type inventoryDataSource struct {
	data *providerData
}

// inventoryEntityModel is one reconciled estate entity in the result.
type inventoryEntityModel struct {
	Kind            types.String `tfsdk:"kind"`
	EntityID        types.String `tfsdk:"entity_id"`
	Name            types.String `tfsdk:"name"`
	Ref             types.String `tfsdk:"ref"`
	Status          types.String `tfsdk:"status"`
	SignalSources   types.List   `tfsdk:"signal_sources"`
	Hosts           types.List   `tfsdk:"hosts"`
	FirstSeen       types.String `tfsdk:"first_seen"`
	LastSeen        types.String `tfsdk:"last_seen"`
	OccurrenceCount types.Int64  `tfsdk:"occurrence_count"`
}

// inventorySummaryModel is the roll-up: counts by kind and source plus the total.
type inventorySummaryModel struct {
	Total     types.Int64 `tfsdk:"total"`
	Truncated types.Bool  `tfsdk:"truncated"`
	ByKind    types.Map   `tfsdk:"by_kind"`
	BySource  types.Map   `tfsdk:"by_source"`
}

// inventoryDataSourceModel maps the olivares_inventory schema.
type inventoryDataSourceModel struct {
	Tenant   types.String           `tfsdk:"tenant"`
	Kind     types.String           `tfsdk:"kind"`
	Status   types.String           `tfsdk:"status"`
	Entities []inventoryEntityModel `tfsdk:"entities"`
	Summary  *inventorySummaryModel `tfsdk:"summary"`
}

// NewInventoryDataSource is the data source constructor.
func NewInventoryDataSource() datasource.DataSource { return &inventoryDataSource{} }

// Metadata sets the full data source type name: olivares_inventory.
func (d *inventoryDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_inventory"
}

// Schema declares the olivares_inventory attributes.
func (d *inventoryDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads the reconciled inventory of the Olivares AI control plane estate (read-only): the discovered " +
			"entities and a roll-up summary. Requires inventory:read.",
		Attributes: map[string]schema.Attribute{
			"tenant": schema.StringAttribute{
				Optional:    true,
				Description: "Tenant UUID, overriding the provider-level tenant. Sent as X-Olivares-Tenant.",
			},
			"kind": schema.StringAttribute{
				Optional:    true,
				Description: "Filter entities by kind (e.g. \"agent\", \"mcp_server\", \"identity\"). Omit for all kinds.",
			},
			"status": schema.StringAttribute{
				Optional:    true,
				Description: "Filter entities by status. Omit for all statuses.",
			},
			"entities": schema.ListNestedAttribute{
				Computed:    true,
				Description: "The matching reconciled estate entities.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"kind":             schema.StringAttribute{Computed: true, Description: "Entity kind."},
						"entity_id":        schema.StringAttribute{Computed: true, Description: "Resolved entity id."},
						"name":             schema.StringAttribute{Computed: true, Description: "Entity name."},
						"ref":              schema.StringAttribute{Computed: true, Description: "Logical reference."},
						"status":           schema.StringAttribute{Computed: true, Description: "Entity status."},
						"signal_sources":   schema.ListAttribute{Computed: true, ElementType: types.StringType, Description: "Collectors that observed the entity."},
						"hosts":            schema.ListAttribute{Computed: true, ElementType: types.StringType, Description: "Hosts the entity was seen on."},
						"first_seen":       schema.StringAttribute{Computed: true, Description: "First-seen timestamp."},
						"last_seen":        schema.StringAttribute{Computed: true, Description: "Last-seen timestamp."},
						"occurrence_count": schema.Int64Attribute{Computed: true, Description: "How many times the entity was observed."},
					},
				},
			},
			"summary": schema.SingleNestedAttribute{
				Computed:    true,
				Description: "Estate roll-up: counts by kind and by source plus the total. Reflects the whole estate, not the filtered entity list.",
				Attributes: map[string]schema.Attribute{
					"total":     schema.Int64Attribute{Computed: true, Description: "Total reconciled entities."},
					"truncated": schema.BoolAttribute{Computed: true, Description: "True when the roll-up was bounded by the scan cap (honest gradation)."},
					"by_kind":   schema.MapAttribute{Computed: true, ElementType: types.Int64Type, Description: "Entity count per kind."},
					"by_source": schema.MapAttribute{Computed: true, ElementType: types.Int64Type, Description: "Entity count per signal source."},
				},
			},
		},
	}
}

// Configure receives the shared provider data.
func (d *inventoryDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

// Read queries the inventory entities (filtered) and the roll-up summary.
func (d *inventoryDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg inventoryDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenant := dsTenant(cfg.Tenant)

	entities, err := d.data.client.ListInventoryEntities(ctx, tenant, cfg.Kind.ValueString(), cfg.Status.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Unable to read inventory entities", err.Error())
		return
	}
	cfg.Entities = make([]inventoryEntityModel, 0, len(entities))
	for _, e := range entities {
		sources, diags := stringList(ctx, e.SignalSources)
		resp.Diagnostics.Append(diags...)
		hosts, diags := stringList(ctx, e.Hosts)
		resp.Diagnostics.Append(diags...)
		cfg.Entities = append(cfg.Entities, inventoryEntityModel{
			Kind:            types.StringValue(e.Kind),
			EntityID:        types.StringValue(e.EntityID),
			Name:            types.StringValue(e.Name),
			Ref:             nullIfEmpty(e.Ref),
			Status:          types.StringValue(e.Status),
			SignalSources:   sources,
			Hosts:           hosts,
			FirstSeen:       types.StringValue(e.FirstSeen),
			LastSeen:        types.StringValue(e.LastSeen),
			OccurrenceCount: types.Int64Value(e.OccurrenceCount),
		})
	}

	summary, err := d.data.client.GetInventorySummary(ctx, tenant)
	if err != nil {
		resp.Diagnostics.AddError("Unable to read inventory summary", err.Error())
		return
	}
	byKind := make(map[string]int64, len(summary.ByKind))
	for k, v := range summary.ByKind {
		byKind[k] = int64(v.Total)
	}
	bySource := make(map[string]int64, len(summary.BySource))
	for k, v := range summary.BySource {
		bySource[k] = int64(v)
	}
	byKindMap, diags := types.MapValueFrom(ctx, types.Int64Type, byKind)
	resp.Diagnostics.Append(diags...)
	bySourceMap, diags := types.MapValueFrom(ctx, types.Int64Type, bySource)
	resp.Diagnostics.Append(diags...)
	cfg.Summary = &inventorySummaryModel{
		Total:     types.Int64Value(int64(summary.Total)),
		Truncated: types.BoolValue(summary.Truncated),
		ByKind:    byKindMap,
		BySource:  bySourceMap,
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}

// stringList builds a string list attribute, a known-empty list when the slice
// is empty (a Computed list attribute must be a known value, never null here).
func stringList(ctx context.Context, ss []string) (types.List, diag.Diagnostics) {
	if len(ss) == 0 {
		return types.ListValueFrom(ctx, types.StringType, []string{})
	}
	return types.ListValueFrom(ctx, types.StringType, ss)
}
