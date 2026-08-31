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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/olivaresai/olivares/terraform-provider-olivares/internal/client"
)

// Compile-time interface assertions.
var (
	_ resource.Resource                = (*notificationRouteResource)(nil)
	_ resource.ResourceWithConfigure   = (*notificationRouteResource)(nil)
	_ resource.ResourceWithImportState = (*notificationRouteResource)(nil)
)

// notificationRouteResource manages a notification route (notify module) as code:
// which events (by type/kind/source/subject and minimum severity) fan out to
// which destination, with dedup/throttle windows and a priority. It is the
// alerting-as-code surface a platform team keeps under source control.
//
// The engine enforces notify:route:write and self-audits each mutation; the
// provider only declares the desired route.
type notificationRouteResource struct {
	data *providerData
}

// notificationRouteResourceModel maps the olivares_notification_route schema.
type notificationRouteResourceModel struct {
	ID                    types.String `tfsdk:"id"`
	Tenant                types.String `tfsdk:"tenant"`
	Name                  types.String `tfsdk:"name"`
	Enabled               types.Bool   `tfsdk:"enabled"`
	MatchTypes            types.List   `tfsdk:"match_types"`
	MatchKinds            types.List   `tfsdk:"match_kinds"`
	MinSeverity           types.String `tfsdk:"min_severity"`
	MatchSources          types.List   `tfsdk:"match_sources"`
	MatchSubjectKinds     types.List   `tfsdk:"match_subject_kinds"`
	Destination           types.String `tfsdk:"destination"`
	DedupWindowSeconds    types.Int64  `tfsdk:"dedup_window_seconds"`
	ThrottleWindowSeconds types.Int64  `tfsdk:"throttle_window_seconds"`
	Priority              types.Int64  `tfsdk:"priority"`
	OwnerActor            types.String `tfsdk:"owner_actor"`
	CreatedAt             types.String `tfsdk:"created_at"`
}

// NewNotificationRouteResource is the resource constructor registered with the provider.
func NewNotificationRouteResource() resource.Resource { return &notificationRouteResource{} }

// Metadata sets the full resource type name: olivares_notification_route.
func (r *notificationRouteResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_notification_route"
}

// matchListAttribute is the shared shape of the four match_* filter lists.
func matchListAttribute(desc string) schema.ListAttribute {
	return schema.ListAttribute{
		Optional:    true,
		Computed:    true,
		ElementType: types.StringType,
		Description: desc,
	}
}

// Schema declares the olivares_notification_route attributes.
func (r *notificationRouteResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A notification route in the Olivares AI control plane (notify module): the rule that fans matching " +
			"events out to a destination. Matchers are AND-combined; an empty matcher matches all. Authoring requires notify:route:write.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Server-assigned route ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"tenant": schema.StringAttribute{
				Optional:    true,
				Description: "Tenant UUID for this resource, overriding the provider-level tenant. Sent as X-Olivares-Tenant.",
			},
			"name": schema.StringAttribute{
				Required:      true,
				Description:   "Human-readable route name. Immutable (the engine never renames a route; changing it replaces the resource).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"enabled": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
				Description: "Whether the route is active. Defaults to true.",
			},
			"match_types": matchListAttribute("Event types to match (e.g. \"finding\", \"alert\"). Empty matches all."),
			"match_kinds": matchListAttribute("Event kinds to match. Empty matches all."),
			"min_severity": schema.StringAttribute{
				Optional:    true,
				Description: "Minimum severity to forward: empty, or one of info, low, medium, high, critical. Empty forwards every severity.",
			},
			"match_sources":       matchListAttribute("Event sources to match. Empty matches all."),
			"match_subject_kinds": matchListAttribute("Subject kinds to match. Empty matches all."),
			"destination": schema.StringAttribute{
				Required:    true,
				Description: "Destination reference the matching events are delivered to (a configured channel/webhook id).",
			},
			"dedup_window_seconds": schema.Int64Attribute{
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(0),
				Description: "Suppress duplicate notifications within this many seconds (0 = no dedup).",
			},
			"throttle_window_seconds": schema.Int64Attribute{
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(0),
				Description: "Rate-limit notifications to at most one per this many seconds (0 = no throttle).",
			},
			"priority": schema.Int64Attribute{
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(0),
				Description: "Route priority for ordering/selection (higher wins). Defaults to 0.",
			},
			"owner_actor": schema.StringAttribute{
				Computed:    true,
				Description: "Actor that authored the route, recorded by the engine.",
			},
			"created_at": schema.StringAttribute{
				Computed:      true,
				Description:   "RFC 3339 creation timestamp.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

// Configure receives the shared provider data (REST client + tenant default).
func (r *notificationRouteResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create declares a new notification route.
func (r *notificationRouteResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan notificationRouteResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	rt, diags := toNotificationRoute(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	created, err := r.data.client.CreateNotificationRoute(ctx, r.tenantOverride(plan), rt)
	if err != nil {
		resp.Diagnostics.AddError("Unable to create notification route", err.Error())
		return
	}
	resp.Diagnostics.Append(applyNotificationRoute(ctx, &plan, created)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes state from the engine, removing the resource on 404.
func (r *notificationRouteResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state notificationRouteResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	got, err := r.data.client.GetNotificationRoute(ctx, r.tenantOverride(state), state.ID.ValueString())
	if err != nil {
		if errors.Is(err, client.ErrNotFound) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read notification route", err.Error())
		return
	}
	resp.Diagnostics.Append(applyNotificationRoute(ctx, &state, got)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update applies changes to an existing notification route (name is immutable and
// marked RequiresReplace, so it never changes here).
func (r *notificationRouteResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan notificationRouteResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var state notificationRouteResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = state.ID
	rt, diags := toNotificationRoute(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	updated, err := r.data.client.UpdateNotificationRoute(ctx, r.tenantOverride(plan), plan.ID.ValueString(), rt)
	if err != nil {
		resp.Diagnostics.AddError("Unable to update notification route", err.Error())
		return
	}
	resp.Diagnostics.Append(applyNotificationRoute(ctx, &plan, updated)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete removes a notification route.
func (r *notificationRouteResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state notificationRouteResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.data.client.DeleteNotificationRoute(ctx, r.tenantOverride(state), state.ID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Unable to delete notification route", err.Error())
	}
}

// ImportState imports an existing notification route by its id.
func (r *notificationRouteResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// tenantOverride returns the per-resource tenant when set, otherwise empty.
func (r *notificationRouteResource) tenantOverride(m notificationRouteResourceModel) string {
	if !m.Tenant.IsNull() && !m.Tenant.IsUnknown() {
		return m.Tenant.ValueString()
	}
	return ""
}

// toNotificationRoute maps the writable plan fields onto a client.NotificationRoute.
func toNotificationRoute(ctx context.Context, m notificationRouteResourceModel) (client.NotificationRoute, diag.Diagnostics) {
	var diags diag.Diagnostics
	rt := client.NotificationRoute{
		Name:                  m.Name.ValueString(),
		Enabled:               m.Enabled.ValueBool(),
		MinSeverity:           m.MinSeverity.ValueString(),
		Destination:           m.Destination.ValueString(),
		DedupWindowSeconds:    m.DedupWindowSeconds.ValueInt64(),
		ThrottleWindowSeconds: m.ThrottleWindowSeconds.ValueInt64(),
		Priority:              m.Priority.ValueInt64(),
	}
	rt.MatchTypes = stringsFromList(ctx, m.MatchTypes, &diags)
	rt.MatchKinds = stringsFromList(ctx, m.MatchKinds, &diags)
	rt.MatchSources = stringsFromList(ctx, m.MatchSources, &diags)
	rt.MatchSubjectKinds = stringsFromList(ctx, m.MatchSubjectKinds, &diags)
	return rt, diags
}

// applyNotificationRoute writes the engine's routeDTO back onto the model.
func applyNotificationRoute(ctx context.Context, m *notificationRouteResourceModel, rt *client.NotificationRoute) diag.Diagnostics {
	var diags diag.Diagnostics
	m.ID = types.StringValue(rt.ID)
	m.Name = types.StringValue(rt.Name)
	m.Enabled = types.BoolValue(rt.Enabled)
	m.Destination = types.StringValue(rt.Destination)
	m.DedupWindowSeconds = types.Int64Value(rt.DedupWindowSeconds)
	m.ThrottleWindowSeconds = types.Int64Value(rt.ThrottleWindowSeconds)
	m.Priority = types.Int64Value(rt.Priority)
	m.MinSeverity = nullIfEmpty(rt.MinSeverity)
	m.OwnerActor = nullIfEmpty(rt.OwnerActor)
	m.CreatedAt = nullIfEmpty(rt.CreatedAt)
	m.MatchTypes = listFromStrings(ctx, rt.MatchTypes, &diags)
	m.MatchKinds = listFromStrings(ctx, rt.MatchKinds, &diags)
	m.MatchSources = listFromStrings(ctx, rt.MatchSources, &diags)
	m.MatchSubjectKinds = listFromStrings(ctx, rt.MatchSubjectKinds, &diags)
	return diags
}

// stringsFromList decodes a list attribute into a []string, returning nil for a
// null/unknown list (the engine treats an absent matcher as "match all").
func stringsFromList(ctx context.Context, l types.List, diags *diag.Diagnostics) []string {
	if l.IsNull() || l.IsUnknown() {
		return nil
	}
	var out []string
	diags.Append(l.ElementsAs(ctx, &out, false)...)
	return out
}

// listFromStrings builds a string list attribute, mapping an empty slice to a
// known-empty list so an Optional+Computed matcher round-trips without drift.
func listFromStrings(ctx context.Context, ss []string, diags *diag.Diagnostics) types.List {
	if len(ss) == 0 {
		return types.ListValueMust(types.StringType, []attr.Value{})
	}
	l, d := types.ListValueFrom(ctx, types.StringType, ss)
	diags.Append(d...)
	return l
}
