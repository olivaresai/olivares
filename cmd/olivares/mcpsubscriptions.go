// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

// mcpSubscriptionLedger is the narrow AGPL composition adapter between the
// Apache MCP relay and the sessions durable cursor/event authority. Tenant,
// workspace and upstream peer are fixed by operator configuration; the request
// can select only its authenticated subject and normalized filter digest.
type mcpSubscriptionLedger struct {
	tenant        model.TenantID
	workspace     model.ID
	peerAuthority string
	store         sessions.ProtocolSubscriptionStore
}

var _ mcpc.SubscriptionLedger = (*mcpSubscriptionLedger)(nil)

func newMCPSubscriptionLedger(
	tenant model.TenantID,
	workspace model.ID,
	peerAuthority string,
	store sessions.ProtocolSubscriptionStore,
) (*mcpSubscriptionLedger, error) {
	parsedTenant, tenantErr := model.ParseTenantID(tenant.String())
	parsedWorkspace, workspaceErr := model.ParseID(workspace.String())
	peerAuthority, peerErr := canonicalMCPSubscriptionPeer(peerAuthority)
	if tenantErr != nil || workspaceErr != nil || tenant.IsZero() || tenant.IsSystem() ||
		parsedTenant != tenant || workspace.IsZero() || parsedWorkspace != workspace ||
		peerErr != nil || store == nil {
		return nil, fmt.Errorf("mcp durable subscriptions: invalid local routing configuration")
	}
	return &mcpSubscriptionLedger{
		tenant: tenant, workspace: workspace, peerAuthority: peerAuthority, store: store,
	}, nil
}

func canonicalMCPSubscriptionPeer(value string) (string, error) {
	value = strings.TrimSpace(value)
	parsed, err := url.Parse(value)
	if err != nil || parsed == nil {
		return "", fmt.Errorf("invalid MCP subscription peer authority")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if len(value) > 512 || parsed.Host == "" ||
		(scheme != "http" && scheme != "https") || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || strings.ContainsAny(value, "\x00\r\n") {
		return "", fmt.Errorf("invalid MCP subscription peer authority")
	}
	parsed.Scheme = scheme
	parsed.Host = strings.ToLower(parsed.Host)
	if parsed.Path == "/" {
		parsed.Path = ""
	} else {
		parsed.Path = strings.TrimSuffix(parsed.Path, "/")
	}
	return parsed.String(), nil
}

func (l *mcpSubscriptionLedger) CatchUp(
	ctx context.Context,
	request mcpc.SubscriptionCatchUpRequest,
) (mcpc.SubscriptionCatchUpPage, error) {
	route, err := l.route(request.Route)
	if err != nil {
		return mcpc.SubscriptionCatchUpPage{}, err
	}
	page, err := l.store.CatchUpProtocolSubscription(ctx, l.tenant, sessions.ProtocolSubscriptionCatchUp{
		Route: route, Cursor: request.Cursor, Limit: request.Limit,
	})
	if err != nil {
		return mcpc.SubscriptionCatchUpPage{}, mapMCPSubscriptionLedgerError(err)
	}
	events := make([]mcpc.SubscriptionStoredEvent, 0, len(page.Events))
	for _, event := range page.Events {
		events = append(events, mcpc.SubscriptionStoredEvent{
			Cursor: event.Cursor, Method: event.Method,
			Params: append([]byte(nil), event.Params...),
		})
	}
	return mcpc.SubscriptionCatchUpPage{
		Events: events, NextCursor: page.NextCursor, HasMore: page.HasMore,
	}, nil
}

func (l *mcpSubscriptionLedger) Append(
	ctx context.Context,
	request mcpc.SubscriptionAppendRequest,
) (mcpc.SubscriptionStoredEvent, error) {
	route, err := l.route(request.Route)
	if err != nil {
		return mcpc.SubscriptionStoredEvent{}, err
	}
	event, err := l.store.AppendProtocolSubscriptionEvent(ctx, l.tenant, sessions.ProtocolSubscriptionAppend{
		Route: route, ExpectedCursor: request.ExpectedCursor,
		Method: request.Event.Method, Params: append([]byte(nil), request.Event.Params...),
	})
	if err != nil {
		return mcpc.SubscriptionStoredEvent{}, mapMCPSubscriptionLedgerError(err)
	}
	return mcpc.SubscriptionStoredEvent{
		Cursor: event.Cursor, Method: event.Method, Params: append([]byte(nil), event.Params...),
	}, nil
}

func (l *mcpSubscriptionLedger) route(
	route mcpc.SubscriptionRoute,
) (sessions.ProtocolSubscriptionRoute, error) {
	if l == nil || route.Tenant != l.tenant.String() || route.Subject != strings.TrimSpace(route.Subject) ||
		route.Subject == "" || route.FilterDigest != strings.TrimSpace(route.FilterDigest) ||
		route.FilterDigest == "" {
		return sessions.ProtocolSubscriptionRoute{}, fmt.Errorf(
			"%w: subscription route is outside the configured tenant", mcpc.ErrSubscriptionCursorInvalid,
		)
	}
	return sessions.ProtocolSubscriptionRoute{
		WorkspaceID: l.workspace, Protocol: sessions.BindingProtocolMCP,
		PeerAuthority: l.peerAuthority, Subject: route.Subject, FilterDigest: route.FilterDigest,
	}, nil
}

func mapMCPSubscriptionLedgerError(err error) error {
	switch {
	case errors.Is(err, sessions.ErrProtocolSubscriptionConflict):
		return fmt.Errorf("%w: sessions cursor changed", mcpc.ErrSubscriptionCursorConflict)
	case errors.Is(err, sessions.ErrProtocolSubscriptionCursor),
		errors.Is(err, sessions.ErrInvalidProtocolSubscription):
		return fmt.Errorf("%w: sessions rejected the route cursor", mcpc.ErrSubscriptionCursorInvalid)
	default:
		return fmt.Errorf("mcp durable subscriptions: %w", err)
	}
}
