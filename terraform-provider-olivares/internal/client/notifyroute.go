// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package client

import (
	"context"
	"net/http"
)

// notifyRoutesPath is the notify module's route collection (module routes mount
// under /v1/m/notify/). A route declares which events (by type/kind/source/
// subject + minimum severity) fan out to which destination, with dedup/throttle
// windows and a priority — the alerting-as-code surface for a platform team.
// Authoring requires notify:route:write, enforced by the engine.
const notifyRoutesPath = "/v1/m/notify/routes"

// NotificationRoute is the wire representation of a notification route, matching
// the notify module's routeDTO on read and createRouteInput on write. The engine
// does NOT change `name` on update (it is part of the route's identity), so the
// resource marks it RequiresReplace. OwnerActor/CreatedAt are engine-assigned.
type NotificationRoute struct {
	ID                    string   `json:"id,omitempty"`
	Name                  string   `json:"name"`
	Enabled               bool     `json:"enabled"`
	MatchTypes            []string `json:"match_types"`
	MatchKinds            []string `json:"match_kinds"`
	MinSeverity           string   `json:"min_severity,omitempty"`
	MatchSources          []string `json:"match_sources"`
	MatchSubjectKinds     []string `json:"match_subject_kinds"`
	Destination           string   `json:"destination"`
	DedupWindowSeconds    int64    `json:"dedup_window_seconds"`
	ThrottleWindowSeconds int64    `json:"throttle_window_seconds"`
	Priority              int64    `json:"priority"`
	OwnerActor            string   `json:"owner_actor,omitempty"`
	CreatedAt             string   `json:"created_at,omitempty"`
}

// notifyRouteRequest is the create/update body. enabled is a pointer so an
// omitted value defaults to true server-side (matching createRouteInput).
type notifyRouteRequest struct {
	Name                  string   `json:"name,omitempty"`
	Enabled               *bool    `json:"enabled"`
	MatchTypes            []string `json:"match_types"`
	MatchKinds            []string `json:"match_kinds"`
	MinSeverity           string   `json:"min_severity"`
	MatchSources          []string `json:"match_sources"`
	MatchSubjectKinds     []string `json:"match_subject_kinds"`
	Destination           string   `json:"destination"`
	DedupWindowSeconds    int64    `json:"dedup_window_seconds"`
	ThrottleWindowSeconds int64    `json:"throttle_window_seconds"`
	Priority              int64    `json:"priority"`
}

// notifyRouteList is the list envelope returned by GET /routes.
type notifyRouteList struct {
	Items   []NotificationRoute `json:"items"`
	Cursor  string              `json:"cursor"`
	HasMore bool                `json:"has_more"`
}

// CreateNotificationRoute declares a route (POST). tenantOverride, when non-empty,
// replaces the client-level tenant for this call.
func (c *Client) CreateNotificationRoute(ctx context.Context, tenantOverride string, rt NotificationRoute) (*NotificationRoute, error) {
	var out NotificationRoute
	if err := c.sendInto(ctx, http.MethodPost, notifyRoutesPath, tenantOverride, toRouteRequest(rt), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetNotificationRoute reads one route (GET). A 404 returns ErrNotFound so the
// resource can be dropped from state.
func (c *Client) GetNotificationRoute(ctx context.Context, tenantOverride, id string) (*NotificationRoute, error) {
	var out NotificationRoute
	if err := c.getInto(ctx, notifyRoutesPath+"/"+id, tenantOverride, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateNotificationRoute updates a route in place (PUT). name is immutable
// server-side (the engine ignores it on update); the resource marks it
// RequiresReplace so it is never changed here.
func (c *Client) UpdateNotificationRoute(ctx context.Context, tenantOverride, id string, rt NotificationRoute) (*NotificationRoute, error) {
	var out NotificationRoute
	if err := c.sendInto(ctx, http.MethodPut, notifyRoutesPath+"/"+id, tenantOverride, toRouteRequest(rt), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteNotificationRoute removes a route (DELETE). A 404 is treated as
// already-deleted.
func (c *Client) DeleteNotificationRoute(ctx context.Context, tenantOverride, id string) error {
	return c.deleteResource(ctx, notifyRoutesPath+"/"+id, tenantOverride)
}

// ListNotificationRoutes returns every route, following the cursor.
func (c *Client) ListNotificationRoutes(ctx context.Context, tenantOverride string) ([]NotificationRoute, error) {
	var all []NotificationRoute
	cursor := ""
	for {
		path := notifyRoutesPath
		if cursor != "" {
			path += "?cursor=" + cursor
		}
		var page notifyRouteList
		if err := c.getInto(ctx, path, tenantOverride, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Items...)
		if !page.HasMore || page.Cursor == "" {
			return all, nil
		}
		cursor = page.Cursor
	}
}

// toRouteRequest maps the writable fields onto the request body, always sending
// enabled explicitly (the resource resolves its default), so an update cannot
// silently flip it back to the server default.
func toRouteRequest(rt NotificationRoute) notifyRouteRequest {
	enabled := rt.Enabled
	return notifyRouteRequest{
		Name:                  rt.Name,
		Enabled:               &enabled,
		MatchTypes:            rt.MatchTypes,
		MatchKinds:            rt.MatchKinds,
		MinSeverity:           rt.MinSeverity,
		MatchSources:          rt.MatchSources,
		MatchSubjectKinds:     rt.MatchSubjectKinds,
		Destination:           rt.Destination,
		DedupWindowSeconds:    rt.DedupWindowSeconds,
		ThrottleWindowSeconds: rt.ThrottleWindowSeconds,
		Priority:              rt.Priority,
	}
}
