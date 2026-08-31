// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package pagerduty

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/internal/delivery"
	"github.com/olivaresai/olivares/sdk"
)

// openLifecycle opens a connector with both the enqueue and change endpoints set to
// recognizable test URLs and a no-op sleep.
func openLifecycle(t *testing.T, doer delivery.Doer) *Output {
	t.Helper()
	o := New()
	o.doer = doer
	cfg := sdk.Config{Settings: map[string]string{
		"routing_key":       testRoutingKey,
		"events_url":        "https://events.pd.example/v2/enqueue",
		"change_events_url": "https://events.pd.example/v2/change/enqueue",
		"source":            "olivares-test",
	}}
	if err := o.Open(context.Background(), cfg); err != nil {
		t.Fatalf("Open: %v", err)
	}
	o.client = delivery.New(doer, delivery.Options{MaxAttempts: o.maxAttempts, Sleep: noWait})
	return o
}

func TestAcknowledgeAndResolve(t *testing.T) {
	for _, action := range []string{actionAcknowledge, actionResolve} {
		doer := &recordingDoer{status: 202, rawBody: `{"status":"success","dedup_key":"d-1"}`}
		o := openLifecycle(t, doer)
		n := sdk.Notification{Fields: map[string]string{fieldEventAction: action, fieldDedupKey: "d-1"}}
		if err := o.Notify(context.Background(), n); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		ev := doer.lastEvent(t)
		if ev.EventAction != action || ev.DedupKey != "d-1" {
			t.Fatalf("%s event = %+v", action, ev)
		}
		// ack/resolve must carry NO payload (PagerDuty rejects payload-less is fine).
		if ev.Payload != nil {
			t.Fatalf("%s must omit payload, got %+v", action, ev.Payload)
		}
		// Sent to the enqueue endpoint (not the change endpoint).
		if got := doer.reqs[len(doer.reqs)-1].URL.String(); !strings.HasSuffix(got, "/v2/enqueue") {
			t.Fatalf("%s sent to %s", action, got)
		}
	}
}

func TestAcknowledgeRequiresDedupKey(t *testing.T) {
	doer := &recordingDoer{status: 202, rawBody: `{}`}
	o := openLifecycle(t, doer)
	n := sdk.Notification{Fields: map[string]string{fieldEventAction: actionAcknowledge}}
	if err := o.Notify(context.Background(), n); err == nil {
		t.Fatal("acknowledge without dedup_key must error")
	}
	if len(doer.reqs) != 0 {
		t.Fatal("must not call the API without a dedup_key")
	}
}

func TestChangeEvent(t *testing.T) {
	doer := &recordingDoer{status: 202, rawBody: `{"status":"success"}`}
	o := openLifecycle(t, doer)
	n := sdk.Notification{
		Title: "deploy", Body: "v2 shipped",
		Fields: map[string]string{fieldEventAction: actionChange, "git_sha": "abc"},
	}
	if err := o.Notify(context.Background(), n); err != nil {
		t.Fatalf("change: %v", err)
	}
	req := doer.reqs[len(doer.reqs)-1]
	if !strings.HasSuffix(req.URL.String(), "/v2/change/enqueue") {
		t.Fatalf("change sent to %s", req.URL.String())
	}
	var ce changeEvent
	if err := json.Unmarshal(doer.bodies[len(doer.bodies)-1], &ce); err != nil {
		t.Fatalf("decode change: %v", err)
	}
	if ce.RoutingKey != testRoutingKey || ce.Payload.Summary != "deploy" {
		t.Fatalf("change event = %+v", ce)
	}
	if ce.Payload.CustomDetails["git_sha"] != "abc" {
		t.Fatalf("change custom_details = %v", ce.Payload.CustomDetails)
	}
	// event_action must NOT leak into custom_details.
	if _, ok := ce.Payload.CustomDetails[fieldEventAction]; ok {
		t.Fatalf("event_action leaked into custom_details")
	}
}

func TestTriggerStillDefault(t *testing.T) {
	doer := &recordingDoer{status: 202, rawBody: `{"status":"success","dedup_key":"d"}`}
	o := openLifecycle(t, doer)
	if err := o.Notify(context.Background(), sdk.Notification{Title: "x"}); err != nil {
		t.Fatalf("trigger: %v", err)
	}
	ev := doer.lastEvent(t)
	if ev.EventAction != actionTrigger || ev.Payload == nil {
		t.Fatalf("default action should be a trigger with payload: %+v", ev)
	}
}
