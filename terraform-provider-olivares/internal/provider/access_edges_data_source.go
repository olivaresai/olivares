// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Compile-time interface assertions.
var (
	_ datasource.DataSource              = (*accessEdgesDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*accessEdgesDataSource)(nil)
)

// accessEdgesDataSource reads the R/RW access-edge map and, when requested, the
// permitted-vs-observed least-privilege diff (the PERMITTED contrast). It is
// read-only: PERMITTED is a derived view, never a writable resource.
type accessEdgesDataSource struct {
	data *providerData
}

// accessEdgeDataModel is one access edge in the data-source result.
type accessEdgeDataModel struct {
	ID              types.String `tfsdk:"id"`
	OriginKind      types.String `tfsdk:"origin_kind"`
	OriginID        types.String `tfsdk:"origin_id"`
	ResourceID      types.String `tfsdk:"resource_id"`
	Mode            types.String `tfsdk:"mode"`
	SignalSource    types.String `tfsdk:"signal_source"`
	Confidence      types.String `tfsdk:"confidence"`
	Permitted       types.Bool   `tfsdk:"permitted"`
	Observed        types.Bool   `tfsdk:"observed"`
	OccurrenceCount types.Int64  `tfsdk:"occurrence_count"`
	FirstSeen       types.String `tfsdk:"first_seen"`
	LastSeen        types.String `tfsdk:"last_seen"`
}

// driftEdgeDataModel is one entry of the reconciled least-privilege diff.
type driftEdgeDataModel struct {
	Kind                  types.String `tfsdk:"kind"`
	ReconciliationPending types.Bool   `tfsdk:"reconciliation_pending"`
	ID                    types.String `tfsdk:"id"`
	OriginKind            types.String `tfsdk:"origin_kind"`
	OriginID              types.String `tfsdk:"origin_id"`
	ResourceID            types.String `tfsdk:"resource_id"`
	Mode                  types.String `tfsdk:"mode"`
	SignalSource          types.String `tfsdk:"signal_source"`
	Confidence            types.String `tfsdk:"confidence"`
	Permitted             types.Bool   `tfsdk:"permitted"`
	Observed              types.Bool   `tfsdk:"observed"`
	OccurrenceCount       types.Int64  `tfsdk:"occurrence_count"`
	FirstSeen             types.String `tfsdk:"first_seen"`
	LastSeen              types.String `tfsdk:"last_seen"`
}

// accessEdgesDataSourceModel maps the olivares_access_edges schema.
type accessEdgesDataSourceModel struct {
	Tenant       types.String          `tfsdk:"tenant"`
	IncludeDrift types.Bool            `tfsdk:"include_drift"`
	Edges        []accessEdgeDataModel `tfsdk:"edges"`
	Drift        []driftEdgeDataModel  `tfsdk:"drift"`
}

// NewAccessEdgesDataSource is the data source constructor.
func NewAccessEdgesDataSource() datasource.DataSource { return &accessEdgesDataSource{} }

// Metadata sets the full data source type name: olivares_access_edges.
func (d *accessEdgesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_access_edges"
}

// edgeAttributes are the shared access-edge attributes, reused by both lists.
func edgeAttributes(extra map[string]schema.Attribute) map[string]schema.Attribute {
	attrs := map[string]schema.Attribute{
		"id":               schema.StringAttribute{Computed: true, Description: "Access edge id."},
		"origin_kind":      schema.StringAttribute{Computed: true, Description: "agent | identity | session."},
		"origin_id":        schema.StringAttribute{Computed: true, Description: "Resolved origin id."},
		"resource_id":      schema.StringAttribute{Computed: true, Description: "Resolved resource id."},
		"mode":             schema.StringAttribute{Computed: true, Description: "read | write | readwrite | unknown."},
		"signal_source":    schema.StringAttribute{Computed: true, Description: "The collector that produced the edge."},
		"confidence":       schema.StringAttribute{Computed: true, Description: "attributed | approximate."},
		"permitted":        schema.BoolAttribute{Computed: true, Description: "Whether the edge is declared/permitted."},
		"observed":         schema.BoolAttribute{Computed: true, Description: "Whether the edge was observed in telemetry."},
		"occurrence_count": schema.Int64Attribute{Computed: true, Description: "How many times the access was observed."},
		"first_seen":       schema.StringAttribute{Computed: true, Description: "First-seen timestamp."},
		"last_seen":        schema.StringAttribute{Computed: true, Description: "Last-seen timestamp."},
	}
	for k, v := range extra {
		attrs[k] = v
	}
	return attrs
}

// Schema declares the olivares_access_edges attributes.
func (d *accessEdgesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads the R/RW access-edge map of the Olivares AI control plane, and optionally the permitted-vs-observed least-privilege diff (read-only). Requires accessgraph:read.",
		Attributes: map[string]schema.Attribute{
			"tenant": schema.StringAttribute{
				Optional:    true,
				Description: "Tenant UUID, overriding the provider-level tenant. Sent as X-Olivares-Tenant.",
			},
			"include_drift": schema.BoolAttribute{
				Optional:    true,
				Description: "Also fetch the permitted-vs-observed least-privilege diff into `drift`. Defaults to false.",
			},
			"edges": schema.ListNestedAttribute{
				Computed:     true,
				Description:  "The access edges (R/RW map).",
				NestedObject: schema.NestedAttributeObject{Attributes: edgeAttributes(nil)},
			},
			"drift": schema.ListNestedAttribute{
				Computed:    true,
				Description: "The RECONCILED permitted-vs-observed least-privilege diff (module III; empty unless include_drift is true). Cross-origin false positives are reconciled away — `kind` is unexpected_access or unused_grant.",
				NestedObject: schema.NestedAttributeObject{Attributes: edgeAttributes(map[string]schema.Attribute{
					"kind":                   schema.StringAttribute{Computed: true, Description: "unexpected_access (observed, not permitted) | unused_grant (permitted, never observed)."},
					"reconciliation_pending": schema.BoolAttribute{Computed: true, Description: "True when an unexpected access cannot yet be proven permitted because the agent↔identity link is unresolved — honest uncertainty, not a firm violation."},
				})},
			},
		},
	}
}

// Configure receives the shared provider data.
func (d *accessEdgesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

// Read queries the access-edge map (and optionally the drift diff).
func (d *accessEdgesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg accessEdgesDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenant := dsTenant(cfg.Tenant)
	edges, err := d.data.client.ListAccessEdges(ctx, tenant)
	if err != nil {
		resp.Diagnostics.AddError("Unable to read access edges", err.Error())
		return
	}
	cfg.Edges = make([]accessEdgeDataModel, 0, len(edges))
	for _, e := range edges {
		cfg.Edges = append(cfg.Edges, accessEdgeDataModel{
			ID: types.StringValue(e.ID), OriginKind: types.StringValue(e.OriginKind), OriginID: types.StringValue(e.OriginID),
			ResourceID: types.StringValue(e.ResourceID), Mode: types.StringValue(e.Mode), SignalSource: types.StringValue(e.SignalSource),
			Confidence: types.StringValue(e.Confidence), Permitted: types.BoolValue(e.Permitted), Observed: types.BoolValue(e.Observed),
			OccurrenceCount: types.Int64Value(e.OccurrenceCount), FirstSeen: types.StringValue(e.FirstSeen), LastSeen: types.StringValue(e.LastSeen),
		})
	}

	cfg.Drift = []driftEdgeDataModel{}
	if cfg.IncludeDrift.ValueBool() {
		drift, err := d.data.client.ListDrift(ctx, tenant)
		if err != nil {
			resp.Diagnostics.AddError("Unable to read access-edge drift", err.Error())
			return
		}
		cfg.Drift = make([]driftEdgeDataModel, 0, len(drift))
		for _, de := range drift {
			e := de.Edge
			cfg.Drift = append(cfg.Drift, driftEdgeDataModel{
				Kind:                  types.StringValue(de.Kind),
				ReconciliationPending: types.BoolValue(de.ReconciliationPending),
				ID:                    types.StringValue(e.ID), OriginKind: types.StringValue(e.OriginKind), OriginID: types.StringValue(e.OriginID),
				ResourceID: types.StringValue(e.ResourceID), Mode: types.StringValue(e.Mode), SignalSource: types.StringValue(e.SignalSource),
				Confidence: types.StringValue(e.Confidence), Permitted: types.BoolValue(e.Permitted), Observed: types.BoolValue(e.Observed),
				OccurrenceCount: types.Int64Value(e.OccurrenceCount), FirstSeen: types.StringValue(e.FirstSeen), LastSeen: types.StringValue(e.LastSeen),
			})
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
