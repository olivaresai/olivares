// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
)

// SubscriptionListenMethod is the MCP method routed by SubscriptionUpstream.
const SubscriptionListenMethod = methodSubscriptionsListen

// MarshalSubscriptionUpstreamRequest builds the stateless MCP request body and
// returns its params separately so a composition-root HTTP adapter can derive
// the mandatory routing mirrors with UpstreamRoutingHeaders. Tenant, subject,
// scopes and trace context never enter the body; they are gateway authority and
// transport context, not upstream client-controlled params.
func MarshalSubscriptionUpstreamRequest(
	requestID int64,
	req SubscriptionListenRequest,
) ([]byte, json.RawMessage, error) {
	if requestID < 1 || subscriptionFilterEmpty(req.Filter) {
		return nil, nil, fmt.Errorf("mcp: invalid upstream subscription request")
	}
	params, err := nextRequestMeta().inject(struct {
		Notifications SubscriptionFilter `json:"notifications"`
	}{Notifications: normalizeSubscriptionFilter(req.Filter)})
	if err != nil {
		return nil, nil, err
	}
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return nil, nil, fmt.Errorf("mcp: marshal upstream subscription params: %w", err)
	}
	body, err := (rpcRequest{
		ID: requestID, Method: methodSubscriptionsListen, Params: json.RawMessage(paramsJSON),
	}).marshal()
	if err != nil {
		return nil, nil, fmt.Errorf("mcp: marshal upstream subscription request: %w", err)
	}
	return body, json.RawMessage(paramsJSON), nil
}

// ConsumeSubscriptionUpstreamStream verifies an upstream SSE listen stream.
// The acknowledgement must be first and every delivered notification must be
// correlated to requestID. EOF without the final correlated response is
// ErrSubscriptionRelayTruncated; callback errors are returned unchanged so a
// durable-ledger failure is never mislabeled as a transport drop.
func ConsumeSubscriptionUpstreamStream(
	reader io.Reader,
	requestID int64,
	emit func(SubscriptionEvent) error,
) error {
	if reader == nil || requestID < 1 || emit == nil {
		return fmt.Errorf("mcp: invalid upstream subscription stream")
	}
	acked := false
	var callbackErr error
	var protocolErr error
	err := scanSSE(reader, func(msg rpcMessage) error {
		fail := func(err error) error {
			protocolErr = err
			return err
		}
		if msg.JSONRPC != jsonRPCVersion {
			return fail(fmt.Errorf("mcp: upstream subscription sent an invalid JSON-RPC version"))
		}
		if msg.Method != "" {
			if msg.ID != nil {
				return fail(fmt.Errorf("mcp: upstream subscription sent a server request"))
			}
			subscriptionID, correlated := subscriptionIDForRequest(msg.Params, requestID)
			if !acked {
				if msg.Method != notificationSubscriptionsAcknowledged || !correlated {
					return fail(fmt.Errorf(
						"mcp: upstream subscription did not start with a correlated %s",
						notificationSubscriptionsAcknowledged,
					))
				}
				acked = true
				return nil
			}
			if msg.Method == notificationSubscriptionsAcknowledged || !correlated {
				return fail(fmt.Errorf("mcp: upstream subscription notification is not correlated"))
			}
			callbackErr = emit(SubscriptionEvent{
				Method: msg.Method, SubscriptionID: subscriptionID,
				Params: append(json.RawMessage(nil), msg.Params...),
			})
			return callbackErr
		}
		if msg.isResponseTo(requestID) {
			if !acked {
				return fail(fmt.Errorf("mcp: upstream subscription ended before acknowledgement"))
			}
			hasResult := len(bytes.TrimSpace(msg.Result)) > 0 &&
				!bytes.Equal(bytes.TrimSpace(msg.Result), []byte("null"))
			if msg.Error != nil {
				if hasResult {
					return fail(fmt.Errorf("mcp: upstream subscription teardown carries result and error"))
				}
				return fail(msg.Error)
			}
			if !hasResult {
				return fail(fmt.Errorf("mcp: upstream subscription teardown is missing its result"))
			}
			return errSubscriptionTornDown
		}
		if msg.ID != nil {
			return fail(fmt.Errorf("mcp: upstream subscription sent an uncorrelated response"))
		}
		return nil
	})
	switch {
	case callbackErr != nil:
		return callbackErr
	case protocolErr != nil:
		return protocolErr
	case errors.Is(err, errSubscriptionTornDown):
		return nil
	case err != nil:
		return fmt.Errorf("%w: %v", ErrSubscriptionRelayTruncated, err)
	case !acked:
		return fmt.Errorf("%w: upstream closed before acknowledgement", ErrSubscriptionRelayTruncated)
	default:
		return fmt.Errorf("%w: upstream closed without graceful teardown", ErrSubscriptionRelayTruncated)
	}
}

func subscriptionIDForRequest(params json.RawMessage, requestID int64) (string, bool) {
	var payload struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if json.Unmarshal(params, &payload) != nil || payload.Meta == nil {
		return "", false
	}
	raw := bytes.TrimSpace(payload.Meta[metaSubscriptionID])
	expected := strconv.FormatInt(requestID, 10)
	if bytes.Equal(raw, []byte(expected)) {
		return expected, true
	}
	var text string
	if json.Unmarshal(raw, &text) == nil && text == expected {
		return text, true
	}
	return "", false
}
