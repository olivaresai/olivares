// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestSubscriptionUpstreamCodecOmitsGatewayAuthorityAndConsumesGracefulStream(t *testing.T) {
	body, params, err := MarshalSubscriptionUpstreamRequest(9, SubscriptionListenRequest{
		Route: SubscriptionRoute{
			Tenant: "tenant-secret", Subject: "subject-secret", FilterDigest: "digest-secret",
		},
		Filter: SubscriptionFilter{ToolsListChanged: true},
		Scopes: []string{"scope-secret"}, TraceParent: "trace-secret",
	})
	if err != nil {
		t.Fatalf("marshal subscription request: %v", err)
	}
	for _, secret := range []string{"tenant-secret", "subject-secret", "digest-secret", "scope-secret", "trace-secret"} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("gateway authority %q escaped in upstream body: %s", secret, body)
		}
	}
	if !strings.Contains(string(body), `"method":"subscriptions/listen"`) ||
		UpstreamRoutingHeaders(SubscriptionListenMethod, params)[headerMCPProtocolVersion] != revision20260728 {
		t.Fatalf("upstream request body/params = %s / %s", body, params)
	}

	stream := strings.Join([]string{
		`data: {"jsonrpc":"2.0","method":"notifications/subscriptions/acknowledged","params":{"_meta":{"io.modelcontextprotocol/subscriptionId":9}}}`,
		"",
		`data: {"jsonrpc":"2.0","method":"notifications/tools/list_changed","params":{"_meta":{"io.modelcontextprotocol/subscriptionId":9},"change":1}}`,
		"",
		`data: {"jsonrpc":"2.0","id":9,"result":{}}`,
		"",
	}, "\n")
	var events []SubscriptionEvent
	err = ConsumeSubscriptionUpstreamStream(strings.NewReader(stream), 9, func(event SubscriptionEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil || len(events) != 1 || events[0].Method != "notifications/tools/list_changed" ||
		events[0].SubscriptionID != "9" || !json.Valid(events[0].Params) {
		t.Fatalf("consume graceful subscription = %#v, err=%v", events, err)
	}
}

func TestSubscriptionUpstreamCodecClassifiesTruncationAndPreservesCallbackError(t *testing.T) {
	truncated := strings.Join([]string{
		`data: {"jsonrpc":"2.0","method":"notifications/subscriptions/acknowledged","params":{"_meta":{"io.modelcontextprotocol/subscriptionId":"3"}}}`,
		"",
		`data: {"jsonrpc":"2.0","method":"notifications/tools/list_changed","params":{"_meta":{"io.modelcontextprotocol/subscriptionId":"3"}}}`,
		"",
	}, "\n")
	if err := ConsumeSubscriptionUpstreamStream(strings.NewReader(truncated), 3, func(SubscriptionEvent) error {
		return nil
	}); !errors.Is(err, ErrSubscriptionRelayTruncated) {
		t.Fatalf("truncated stream error = %v", err)
	}

	callbackFailure := errors.New("durable append failed")
	if err := ConsumeSubscriptionUpstreamStream(strings.NewReader(truncated), 3, func(SubscriptionEvent) error {
		return callbackFailure
	}); !errors.Is(err, callbackFailure) || errors.Is(err, ErrSubscriptionRelayTruncated) {
		t.Fatalf("callback stream error = %v", err)
	}
}
