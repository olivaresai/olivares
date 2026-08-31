// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cloudevents

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

func TestStructuredEnvelopeHasRequiredAttributes(t *testing.T) {
	ev := Event{
		ID:              "evt-1",
		Source:          "/olivares/olivares",
		Type:            "ai.olivares.finding.reported",
		Time:            time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC),
		DataContentType: "application/json",
		Data:            json.RawMessage(`{"k":"v"}`),
		Extensions:      map[string]string{"severity": "high"},
	}
	b, err := ev.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// specversion MUST be the wire value "1.0", never "1.0.2".
	if got := m["specversion"]; got != "1.0" {
		t.Fatalf("specversion = %v, want 1.0", got)
	}
	for _, attr := range []string{"id", "source", "type", "specversion"} {
		if _, ok := m[attr]; !ok {
			t.Fatalf("required attribute %q missing", attr)
		}
	}
	if m["severity"] != "high" {
		t.Fatalf("extension severity not promoted to a top-level attribute: %v", m["severity"])
	}
	// data is embedded as a nested object, not a string.
	data, ok := m["data"].(map[string]any)
	if !ok || data["k"] != "v" {
		t.Fatalf("data member not embedded verbatim: %v", m["data"])
	}
	if m["time"] != "2026-06-06T12:00:00Z" {
		t.Fatalf("time not RFC3339 UTC: %v", m["time"])
	}
}

func TestDeterministicSerialization(t *testing.T) {
	ev := Event{
		ID: "id", Source: "/s", Type: "t",
		Extensions: map[string]string{"severity": "low", "tenantref": "acme"},
	}
	a, err := ev.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for i := 0; i < 20; i++ {
		b, err := ev.MarshalJSON()
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if string(a) != string(b) {
			t.Fatalf("serialization not deterministic:\n%s\n%s", a, b)
		}
	}
}

func TestValidateRejectsMissingRequiredAndBadExtensions(t *testing.T) {
	cases := []struct {
		name string
		ev   Event
	}{
		{"no id", Event{Source: "/s", Type: "t"}},
		{"no source", Event{ID: "i", Type: "t"}},
		{"no type", Event{ID: "i", Source: "/s"}},
		{"reserved extension", Event{ID: "i", Source: "/s", Type: "t", Extensions: map[string]string{"id": "x"}}},
		{"upper extension", Event{ID: "i", Source: "/s", Type: "t", Extensions: map[string]string{"Sev": "x"}}},
		{"underscore extension", Event{ID: "i", Source: "/s", Type: "t", Extensions: map[string]string{"a_b": "x"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.ev.Validate(); err == nil {
				t.Fatalf("expected validation error for %s", c.name)
			}
			if _, err := c.ev.MarshalJSON(); err == nil {
				t.Fatalf("expected marshal to reject invalid event %s", c.name)
			}
		})
	}
}

func TestBinaryHTTPHeaders(t *testing.T) {
	ev := Event{
		ID: "evt-2", Source: "/olivares", Type: "ai.olivares.x",
		Time:            time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		DataContentType: "application/json",
		Data:            json.RawMessage(`{"a":1}`),
		Extensions:      map[string]string{"severity": "critical"},
	}
	h, body, err := ev.BinaryHTTP()
	if err != nil {
		t.Fatalf("binary: %v", err)
	}
	want := map[string]string{
		"ce-id": "evt-2", "ce-source": "/olivares", "ce-type": "ai.olivares.x",
		"ce-specversion": "1.0", "ce-time": "2026-01-02T03:04:05Z", "ce-severity": "critical",
		"Content-Type": "application/json",
	}
	for k, v := range want {
		if h[k] != v {
			t.Fatalf("header %q = %q, want %q", k, h[k], v)
		}
	}
	if string(body) != `{"a":1}` {
		t.Fatalf("binary body = %q", body)
	}
}

func TestFromNotification(t *testing.T) {
	n := sdk.Notification{
		Type: "finding.reported", Title: "Drift detected", Body: "1 unexpected access",
		Severity: model.SeverityHigh, Tenant: "t-acme",
		Fields: map[string]string{"approval_id": "abc"},
		Time:   time.Date(2026, 6, 6, 9, 0, 0, 0, time.UTC),
	}
	ev, err := FromNotification("evt-3", "/olivares/olivares", n)
	if err != nil {
		t.Fatalf("from notification: %v", err)
	}
	if ev.Type != "ai.olivares.finding.reported" {
		t.Fatalf("type = %q", ev.Type)
	}
	if ev.Extensions["severity"] != "high" || ev.Extensions["tenantref"] != "t-acme" {
		t.Fatalf("extensions = %v", ev.Extensions)
	}
	b, err := ev.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m struct {
		Data notificationData `json:"data"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m.Data.Title != "Drift detected" || m.Data.Fields["approval_id"] != "abc" {
		t.Fatalf("data not carried: %+v", m.Data)
	}
}

func TestEmptyTypeNotificationStillValid(t *testing.T) {
	ev, err := FromNotification("e", "/s", sdk.Notification{})
	if err != nil {
		t.Fatalf("from notification: %v", err)
	}
	if ev.Type != "ai.olivares.notification" {
		t.Fatalf("empty type fallback = %q", ev.Type)
	}
}
