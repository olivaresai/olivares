// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

// OlivaresSubscriptionCursorMeta is the optional Olivares resume cursor a
// downstream client may carry in params._meta. Streamable HTTP removed the
// protocol-level Last-Event-ID contract in MCP 2026-07-28; the inline gateway
// nevertheless has to survive its own restart without discarding notifications
// it already accepted. The gateway therefore exposes its durable, opaque cursor
// both as an SSE id and through this namespaced request extension. The ordinary
// HTTP Last-Event-ID header is accepted as an equivalent transport spelling.
const OlivaresSubscriptionCursorMeta = "ai.olivares/subscriptionCursor"

const (
	subscriptionCatchUpLimit  = 128
	subscriptionCatchUpPages  = 1000
	subscriptionAppendRetries = 8
	maxSubscriptionCursorLen  = 1024
)

var (
	// ErrSubscriptionCursorConflict tells the relay that another writer advanced
	// the same durable stream after the expected cursor was read. The relay catches
	// up the intervening rows and retries; it never overwrites the cursor.
	ErrSubscriptionCursorConflict = errors.New("mcp: subscription cursor conflict")
	// ErrSubscriptionCursorInvalid reports a cursor that does not belong to the
	// selected tenant/subject/filter stream (or is otherwise malformed).
	ErrSubscriptionCursorInvalid = errors.New("mcp: invalid subscription cursor")
	// ErrSubscriptionRelayTruncated is the server-side spelling of an upstream
	// listen stream that ended without a graceful MCP teardown response. It aliases
	// the client transport's established classification.
	ErrSubscriptionRelayTruncated = ErrSubscriptionTruncated
)

// SubscriptionRoute is the durable namespace of one governed listen stream.
// FilterDigest is derived by the connector from the normalized filter. It is an
// opaque identifier, never notification content and never a bearer credential.
type SubscriptionRoute struct {
	Tenant       string
	Subject      string
	FilterDigest string
}

// SubscriptionListenRequest is handed to a streaming upstream only after the
// inbound bearer has passed the Resource Server PEP. It deliberately contains no
// inbound credential; a composition adapter authenticates upstream with its own
// credential, just like Upstream.Forward.
type SubscriptionListenRequest struct {
	Route       SubscriptionRoute
	Filter      SubscriptionFilter
	Scopes      []string
	TraceParent string
}

// SubscriptionUpstream is the long-lived counterpart of Upstream.Forward. Listen
// calls emit for notifications observed after the upstream acknowledgement and
// returns nil only after a graceful MCP teardown. A transport drop must return
// ErrSubscriptionRelayTruncated (possibly wrapped).
type SubscriptionUpstream interface {
	Listen(ctx context.Context, req SubscriptionListenRequest, emit func(SubscriptionEvent) error) error
}

// SubscriptionStoredEvent is one notification already committed to the durable
// ledger. Cursor is opaque and scoped to Route by the ledger implementation.
type SubscriptionStoredEvent struct {
	Cursor string
	Method string
	Params json.RawMessage
}

// SubscriptionCatchUpRequest asks for committed events strictly after Cursor.
// Empty Cursor means the beginning of this route's retained history.
type SubscriptionCatchUpRequest struct {
	Route  SubscriptionRoute
	Cursor string
	Limit  int
}

// SubscriptionCatchUpPage is ordered oldest-first. NextCursor is the last row
// returned (or the input cursor for an empty page). HasMore means another read is
// required before the caller is at the current durable head.
type SubscriptionCatchUpPage struct {
	Events     []SubscriptionStoredEvent
	NextCursor string
	HasMore    bool
}

// SubscriptionAppendRequest appends exactly one normalized notification if and
// only if ExpectedCursor is still the durable head. This CAS is what prevents two
// gateway instances from silently forking the same cursor after a restart.
type SubscriptionAppendRequest struct {
	Route          SubscriptionRoute
	ExpectedCursor string
	Event          SubscriptionEvent
}

// SubscriptionLedger is the connector-side port for the sessions-backed durable
// subscription ledger. Implementations must commit event+new cursor atomically;
// an in-memory cache does not satisfy this interface's contract.
type SubscriptionLedger interface {
	CatchUp(context.Context, SubscriptionCatchUpRequest) (SubscriptionCatchUpPage, error)
	Append(context.Context, SubscriptionAppendRequest) (SubscriptionStoredEvent, error)
}

type subscriptionListenParams struct {
	Notifications SubscriptionFilter         `json:"notifications"`
	Meta          map[string]json.RawMessage `json:"_meta"`
}

// handleSubscriptionListen is the only Resource Server path for
// subscriptions/listen. Authentication, revision/header validation and request
// parsing have already run in ServeHTTP. This method completes the PEP decision,
// catches up durable rows, persists every live notification before emitting it,
// and closes abruptly on upstream truncation so no graceful-continuity claim is
// fabricated.
func (rs *ResourceServer) handleSubscriptionListen(
	ctx context.Context,
	w http.ResponseWriter,
	r *http.Request,
	req rsRequest,
	tok validatedToken,
) {
	trace := requestTraceParent(r, req.Params)
	if req.isNotification() {
		rs.auditTraced(ctx, tok, methodSubscriptionsListen, "", false,
			"subscriptions/listen requires a request id", "", "MCP07", trace)
		rs.writeRPCError(w, http.StatusBadRequest, req.ID, rpcInvalidRequest,
			"subscriptions/listen requires a request id")
		return
	}

	params, metaCursor, err := parseSubscriptionListenParams(req.Params)
	if err != nil {
		rs.auditTraced(ctx, tok, methodSubscriptionsListen, "", false,
			"invalid subscriptions/listen filter", "", "MCP07", trace)
		rs.writeRPCError(w, http.StatusBadRequest, req.ID, rpcInvalidParams,
			"invalid subscriptions/listen params")
		return
	}
	filter := normalizeSubscriptionFilter(params.Notifications)
	if subscriptionFilterEmpty(filter) {
		rs.auditTraced(ctx, tok, methodSubscriptionsListen, "", false,
			"subscriptions/listen selected no notification types", "", "MCP07", trace)
		rs.writeRPCError(w, http.StatusBadRequest, req.ID, rpcInvalidParams,
			"subscriptions/listen must select at least one notification type")
		return
	}

	requiredScopes := subscriptionRequiredScopes(filter)
	for _, scope := range requiredScopes {
		if !tok.hasScope(scope) {
			rs.auditTraced(ctx, tok, methodSubscriptionsListen, scope, false,
				"insufficient scope for subscription filter", "", "MCP02", trace)
			rs.challengeScope(w, req.ID, scope)
			return
		}
	}

	if rs.subscriptionUpstream == nil || rs.subscriptionLedger == nil {
		rs.auditTraced(ctx, tok, methodSubscriptionsListen, strings.Join(requiredScopes, " "), false,
			"durable subscriptions/listen relay is not fully wired", "", "MCP07", trace)
		w.Header().Set("Retry-After", "1")
		rs.writeRPCError(w, http.StatusServiceUnavailable, req.ID, rpcEvidenceUnavailable,
			"durable subscriptions/listen relay unavailable")
		return
	}

	cursor, err := subscriptionResumeCursor(r.Header.Get("Last-Event-ID"), metaCursor)
	if err != nil {
		rs.auditTraced(ctx, tok, methodSubscriptionsListen, strings.Join(requiredScopes, " "), false,
			"invalid subscriptions/listen resume cursor", "", "MCP07", trace)
		rs.writeRPCError(w, http.StatusBadRequest, req.ID, rpcInvalidParams,
			"invalid subscriptions/listen resume cursor")
		return
	}
	route := SubscriptionRoute{
		Tenant: rs.tenant, Subject: tok.Subject,
		FilterDigest: subscriptionFilterDigest(filter),
	}

	// Resolve the first page before committing the SSE status so an invalid or
	// unavailable durable cursor receives an ordinary JSON-RPC refusal.
	first, err := rs.subscriptionLedger.CatchUp(ctx, SubscriptionCatchUpRequest{
		Route: route, Cursor: cursor, Limit: subscriptionCatchUpLimit,
	})
	if err != nil {
		if errors.Is(err, ErrSubscriptionCursorInvalid) {
			rs.auditTraced(ctx, tok, methodSubscriptionsListen, strings.Join(requiredScopes, " "), false,
				"subscriptions/listen cursor does not belong to this stream", "", "MCP07", trace)
			rs.writeRPCError(w, http.StatusBadRequest, req.ID, rpcInvalidParams,
				"invalid subscriptions/listen resume cursor")
			return
		}
		rs.auditTraced(ctx, tok, methodSubscriptionsListen, strings.Join(requiredScopes, " "), false,
			"durable subscriptions/listen catch-up unavailable", "", "MCP07", trace)
		w.Header().Set("Retry-After", "1")
		rs.writeRPCError(w, http.StatusServiceUnavailable, req.ID, rpcEvidenceUnavailable,
			"durable subscriptions/listen catch-up unavailable")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)
	if err := writeSubscriptionAcknowledged(rc, w, req.ID, filter); err != nil {
		return
	}
	rs.auditTraced(ctx, tok, methodSubscriptionsListen, strings.Join(requiredScopes, " "), true,
		"subscriptions/listen authorized through durable relay", "", "MCP07", trace)

	current, err := relaySubscriptionCatchUp(ctx, rs.subscriptionLedger, route, cursor, first,
		func(event SubscriptionStoredEvent) error {
			return writeStoredSubscriptionEvent(rc, w, req.ID, filter, event)
		})
	if err != nil {
		rs.auditSubscriptionRelayFailure(ctx, tok, requiredScopes, trace, err)
		return
	}

	upstreamReq := SubscriptionListenRequest{
		Route: route, Filter: filter, Scopes: subscriptionTokenScopes(tok), TraceParent: trace,
	}
	err = rs.subscriptionUpstream.Listen(ctx, upstreamReq, func(event SubscriptionEvent) error {
		normalized, nerr := normalizeSubscriptionEvent(filter, event)
		if nerr != nil {
			return nerr
		}
		if normalized.Method == "" {
			return nil // upstream acknowledgement; the RS emitted its own correlated ack
		}
		for attempt := 0; attempt < subscriptionAppendRetries; attempt++ {
			stored, aerr := rs.subscriptionLedger.Append(ctx, SubscriptionAppendRequest{
				Route: route, ExpectedCursor: current, Event: normalized,
			})
			if aerr == nil {
				if err := writeStoredSubscriptionEvent(rc, w, req.ID, filter, stored); err != nil {
					return err
				}
				current = stored.Cursor
				return nil
			}
			if !errors.Is(aerr, ErrSubscriptionCursorConflict) {
				return aerr
			}
			// Another writer won the CAS. Deliver its committed rows before retrying
			// our event so this downstream stream never jumps over a durable cursor.
			var cerr error
			current, cerr = relaySubscriptionCatchUpFrom(ctx, rs.subscriptionLedger, route, current,
				func(event SubscriptionStoredEvent) error {
					return writeStoredSubscriptionEvent(rc, w, req.ID, filter, event)
				})
			if cerr != nil {
				return cerr
			}
		}
		return fmt.Errorf("%w: append retry budget exhausted", ErrSubscriptionCursorConflict)
	})

	switch {
	case ctx.Err() != nil:
		return // downstream close is the MCP cancellation signal
	case err == nil:
		_ = writeSubscriptionTeardown(rc, w, req.ID)
	default:
		// In particular ErrSubscriptionRelayTruncated ends the SSE body WITHOUT a
		// success response. The durable cursor remains at the last committed event,
		// so a reconnect can catch up; the gateway never claims a clean teardown.
		rs.auditSubscriptionRelayFailure(ctx, tok, requiredScopes, trace, err)
	}
}

func (rs *ResourceServer) auditSubscriptionRelayFailure(
	ctx context.Context,
	tok validatedToken,
	requiredScopes []string,
	trace string,
	err error,
) {
	reason := "subscriptions/listen relay failed; durable cursor retained"
	if errors.Is(err, ErrSubscriptionRelayTruncated) {
		reason = "subscriptions/listen upstream truncated; durable cursor retained for catch-up"
	}
	rs.auditTraced(ctx, tok, methodSubscriptionsListen, strings.Join(requiredScopes, " "), false,
		reason, "", "MCP07", trace)
}

func parseSubscriptionListenParams(raw json.RawMessage) (subscriptionListenParams, string, error) {
	v, err := decodeStrictJSON(raw)
	if err != nil || v.kind != canonObject {
		return subscriptionListenParams{}, "", fmt.Errorf("mcp: subscription params must be an object")
	}
	for _, member := range v.obj {
		if member.key != "notifications" && strings.EqualFold(member.key, "notifications") {
			return subscriptionListenParams{}, "", fmt.Errorf("mcp: case-variant notifications member")
		}
		if member.key != "_meta" && strings.EqualFold(member.key, "_meta") {
			return subscriptionListenParams{}, "", fmt.Errorf("mcp: case-variant _meta member")
		}
	}
	notifications := v.member("notifications")
	if notifications == nil || notifications.val.kind != canonObject {
		return subscriptionListenParams{}, "", fmt.Errorf("mcp: notifications filter required")
	}
	allowed := map[string]struct{}{
		"toolsListChanged": {}, "promptsListChanged": {},
		"resourcesListChanged": {}, "resourceSubscriptions": {},
	}
	for _, member := range notifications.val.obj {
		if _, ok := allowed[member.key]; !ok {
			return subscriptionListenParams{}, "", fmt.Errorf("mcp: unknown subscription filter %q", member.key)
		}
	}

	var params subscriptionListenParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return subscriptionListenParams{}, "", err
	}
	for _, uri := range params.Notifications.ResourceSubscriptions {
		if strings.TrimSpace(uri) == "" || len(uri) > 4096 || strings.ContainsAny(uri, "\r\n") {
			return subscriptionListenParams{}, "", fmt.Errorf("mcp: invalid resource subscription URI")
		}
	}
	metaCursor := ""
	if rawCursor, ok := params.Meta[OlivaresSubscriptionCursorMeta]; ok {
		if err := json.Unmarshal(rawCursor, &metaCursor); err != nil || metaCursor == "" {
			return subscriptionListenParams{}, "", fmt.Errorf("mcp: subscription cursor must be a non-empty string")
		}
	}
	return params, metaCursor, nil
}

func normalizeSubscriptionFilter(filter SubscriptionFilter) SubscriptionFilter {
	resources := append([]string(nil), filter.ResourceSubscriptions...)
	sort.Strings(resources)
	unique := resources[:0]
	for _, uri := range resources {
		if len(unique) == 0 || unique[len(unique)-1] != uri {
			unique = append(unique, uri)
		}
	}
	filter.ResourceSubscriptions = unique
	return filter
}

func subscriptionFilterEmpty(filter SubscriptionFilter) bool {
	return !filter.ToolsListChanged && !filter.PromptsListChanged &&
		!filter.ResourcesListChanged && len(filter.ResourceSubscriptions) == 0
}

func subscriptionRequiredScopes(filter SubscriptionFilter) []string {
	var scopes []string
	if filter.PromptsListChanged {
		scopes = append(scopes, scopePromptsRead)
	}
	if filter.ResourcesListChanged || len(filter.ResourceSubscriptions) > 0 {
		scopes = append(scopes, scopeResourcesRead)
	}
	sort.Strings(scopes)
	return scopes
}

func subscriptionTokenScopes(tok validatedToken) []string {
	scopes := make([]string, 0, len(tok.Scopes))
	for scope := range tok.Scopes {
		scopes = append(scopes, scope)
	}
	sort.Strings(scopes)
	return scopes
}

func subscriptionFilterDigest(filter SubscriptionFilter) string {
	raw, _ := json.Marshal(filter) // fixed struct of primitive values; cannot fail
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func subscriptionResumeCursor(header, meta string) (string, error) {
	header = strings.TrimSpace(header)
	meta = strings.TrimSpace(meta)
	if header != "" && meta != "" && header != meta {
		return "", ErrSubscriptionCursorInvalid
	}
	cursor := header
	if cursor == "" {
		cursor = meta
	}
	if len(cursor) > maxSubscriptionCursorLen || strings.ContainsAny(cursor, "\r\n") {
		return "", ErrSubscriptionCursorInvalid
	}
	return cursor, nil
}

func relaySubscriptionCatchUp(
	ctx context.Context,
	ledger SubscriptionLedger,
	route SubscriptionRoute,
	after string,
	first SubscriptionCatchUpPage,
	emit func(SubscriptionStoredEvent) error,
) (string, error) {
	current := after
	page := first
	for n := 0; n < subscriptionCatchUpPages; n++ {
		next, err := emitSubscriptionPage(current, page, emit)
		if err != nil {
			return current, err
		}
		current = next
		if !page.HasMore {
			return current, nil
		}
		page, err = ledger.CatchUp(ctx, SubscriptionCatchUpRequest{
			Route: route, Cursor: current, Limit: subscriptionCatchUpLimit,
		})
		if err != nil {
			return current, err
		}
	}
	return current, fmt.Errorf("mcp: subscription catch-up exceeded %d pages", subscriptionCatchUpPages)
}

func relaySubscriptionCatchUpFrom(
	ctx context.Context,
	ledger SubscriptionLedger,
	route SubscriptionRoute,
	after string,
	emit func(SubscriptionStoredEvent) error,
) (string, error) {
	first, err := ledger.CatchUp(ctx, SubscriptionCatchUpRequest{
		Route: route, Cursor: after, Limit: subscriptionCatchUpLimit,
	})
	if err != nil {
		return after, err
	}
	return relaySubscriptionCatchUp(ctx, ledger, route, after, first, emit)
}

func emitSubscriptionPage(
	current string,
	page SubscriptionCatchUpPage,
	emit func(SubscriptionStoredEvent) error,
) (string, error) {
	for _, event := range page.Events {
		if !validSubscriptionCursor(event.Cursor) || event.Cursor == current {
			return current, fmt.Errorf("mcp: durable subscription ledger returned a non-advancing cursor")
		}
		if err := emit(event); err != nil {
			return current, err
		}
		current = event.Cursor
	}
	if page.HasMore && (page.NextCursor == "" || page.NextCursor != current) {
		return current, fmt.Errorf("mcp: durable subscription ledger returned an invalid continuation")
	}
	if page.NextCursor != "" && page.NextCursor != current {
		return current, fmt.Errorf("mcp: durable subscription ledger skipped a cursor")
	}
	return current, nil
}

func normalizeSubscriptionEvent(filter SubscriptionFilter, event SubscriptionEvent) (SubscriptionEvent, error) {
	if event.Method == notificationSubscriptionsAcknowledged {
		return SubscriptionEvent{}, nil
	}
	switch event.Method {
	case "notifications/tools/list_changed":
		if !filter.ToolsListChanged {
			return SubscriptionEvent{}, fmt.Errorf("mcp: upstream sent an unrequested tools-list notification")
		}
	case "notifications/prompts/list_changed":
		if !filter.PromptsListChanged {
			return SubscriptionEvent{}, fmt.Errorf("mcp: upstream sent an unrequested prompts-list notification")
		}
	case "notifications/resources/list_changed":
		if !filter.ResourcesListChanged {
			return SubscriptionEvent{}, fmt.Errorf("mcp: upstream sent an unrequested resources-list notification")
		}
	case "notifications/resources/updated":
		if len(filter.ResourceSubscriptions) == 0 {
			return SubscriptionEvent{}, fmt.Errorf("mcp: upstream sent an unrequested resource notification")
		}
	default:
		return SubscriptionEvent{}, fmt.Errorf("mcp: upstream sent unsupported subscription method %q", event.Method)
	}

	params, err := stripUpstreamSubscriptionID(event.Params)
	if err != nil {
		return SubscriptionEvent{}, err
	}
	return SubscriptionEvent{Method: event.Method, Params: params}, nil
}

func stripUpstreamSubscriptionID(raw json.RawMessage) (json.RawMessage, error) {
	v, err := decodeStrictJSON(raw)
	if err != nil || v.kind != canonObject {
		return nil, fmt.Errorf("mcp: subscription notification params must be an object")
	}
	var params map[string]json.RawMessage
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, err
	}
	if rawMeta, ok := params["_meta"]; ok {
		var meta map[string]json.RawMessage
		if err := json.Unmarshal(rawMeta, &meta); err != nil {
			return nil, fmt.Errorf("mcp: subscription notification _meta must be an object")
		}
		delete(meta, metaSubscriptionID)
		if len(meta) == 0 {
			delete(params, "_meta")
		} else {
			encoded, err := json.Marshal(meta)
			if err != nil {
				return nil, err
			}
			params["_meta"] = encoded
		}
	}
	return json.Marshal(params)
}

func writeSubscriptionAcknowledged(
	rc *http.ResponseController,
	w io.Writer,
	id json.RawMessage,
	filter SubscriptionFilter,
) error {
	params := map[string]any{
		"notifications": filter,
		"_meta":         map[string]json.RawMessage{metaSubscriptionID: cloneRaw(id)},
	}
	return writeSubscriptionNotification(rc, w, "", notificationSubscriptionsAcknowledged, params)
}

func writeStoredSubscriptionEvent(
	rc *http.ResponseController,
	w io.Writer,
	id json.RawMessage,
	filter SubscriptionFilter,
	event SubscriptionStoredEvent,
) error {
	if !validSubscriptionCursor(event.Cursor) {
		return fmt.Errorf("mcp: durable subscription ledger returned an invalid cursor")
	}
	normalized, err := normalizeSubscriptionEvent(filter, SubscriptionEvent{
		Method: event.Method, Params: event.Params,
	})
	if err != nil || normalized.Method == "" {
		if err == nil {
			err = fmt.Errorf("mcp: durable subscription ledger contains an acknowledgement")
		}
		return err
	}
	var params map[string]json.RawMessage
	if err := json.Unmarshal(normalized.Params, &params); err != nil {
		return err
	}
	meta := map[string]json.RawMessage{}
	if rawMeta, ok := params["_meta"]; ok {
		if err := json.Unmarshal(rawMeta, &meta); err != nil {
			return err
		}
	}
	meta[metaSubscriptionID] = cloneRaw(id)
	encodedMeta, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	params["_meta"] = encodedMeta
	return writeSubscriptionNotification(rc, w, event.Cursor, normalized.Method, params)
}

func writeSubscriptionNotification(
	rc *http.ResponseController,
	w io.Writer,
	cursor string,
	method string,
	params any,
) error {
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": jsonRPCVersion, "method": method, "params": params,
	})
	if err != nil {
		return err
	}
	var frame strings.Builder
	if cursor != "" {
		frame.WriteString("id: ")
		frame.WriteString(cursor)
		frame.WriteByte('\n')
	}
	frame.WriteString("data: ")
	frame.Write(payload)
	frame.WriteString("\n\n")
	if _, err := io.WriteString(w, frame.String()); err != nil {
		return err
	}
	return rc.Flush()
}

func writeSubscriptionTeardown(
	rc *http.ResponseController,
	w io.Writer,
	id json.RawMessage,
) error {
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": jsonRPCVersion,
		"id":      cloneRaw(id),
		"result": map[string]any{
			"_meta": map[string]json.RawMessage{metaSubscriptionID: cloneRaw(id)},
		},
	})
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
		return err
	}
	return rc.Flush()
}

func validSubscriptionCursor(cursor string) bool {
	return cursor != "" && len(cursor) <= maxSubscriptionCursorLen && !strings.ContainsAny(cursor, "\r\n")
}

func cloneRaw(raw json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), raw...)
}
