// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cloudevents

import (
	"encoding/json"
	"testing"
	"time"
)

func sampleEvent() Event {
	return Event{
		ID:              "evt-1",
		Source:          "/olivares/olivares",
		Type:            "ai.olivares.edge.observed",
		Subject:         "kafka/orders.events",
		Time:            time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC),
		DataContentType: "application/json",
		Data:            json.RawMessage(`{"resource":"orders.events"}`),
		Extensions:      map[string]string{"severity": "info", "partitionkey": "orders"},
	}
}

func TestStructuredRoundtrip(t *testing.T) {
	e := sampleEvent()
	body, err := e.StructuredBytes()
	if err != nil {
		t.Fatalf("structured bytes: %v", err)
	}
	got, err := Parse(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.ID != e.ID || got.Source != e.Source || got.Type != e.Type || got.Subject != e.Subject {
		t.Fatalf("context attributes lost: %+v", got)
	}
	if !got.Time.Equal(e.Time) {
		t.Fatalf("time lost: %v", got.Time)
	}
	if got.Extensions["severity"] != "info" || got.Extensions["partitionkey"] != "orders" {
		t.Fatalf("extensions lost: %v", got.Extensions)
	}
	if string(got.Data) == "" {
		t.Fatalf("data lost")
	}
}

func TestKafkaBinaryRoundtripAndKey(t *testing.T) {
	e := sampleEvent()
	headers, key, body, err := e.KafkaBinary()
	if err != nil {
		t.Fatalf("kafka binary: %v", err)
	}
	// ce_-prefixed headers, content-type carries datacontenttype (unprefixed).
	if string(headers["ce_id"]) != "evt-1" || string(headers["ce_type"]) != e.Type {
		t.Fatalf("ce_ headers wrong: %v", headers)
	}
	if string(headers["content-type"]) != "application/json" {
		t.Fatalf("datacontenttype must map to content-type header, got %q", headers["content-type"])
	}
	if _, leaked := headers["ce_datacontenttype"]; leaked {
		t.Fatalf("datacontenttype must NOT be a ce_ header")
	}
	// partitionkey extension → record key.
	if string(key) != "orders" {
		t.Fatalf("partitionkey not mapped to key: %q", key)
	}
	// Roundtrip back via FromBinary.
	attrs := map[string]string{}
	for k, v := range headers {
		attrs[k] = string(v)
	}
	got, err := FromBinary(KafkaPrefix, attrs, string(headers["content-type"]), body)
	if err != nil {
		t.Fatalf("from kafka binary: %v", err)
	}
	if got.ID != e.ID || got.Type != e.Type || got.DataContentType != "application/json" {
		t.Fatalf("kafka roundtrip lost attributes: %+v", got)
	}
	if got.Extensions["partitionkey"] != "orders" {
		t.Fatalf("partitionkey extension lost on roundtrip: %v", got.Extensions)
	}
}

func TestAMQPAndMQTTBinaryPrefixes(t *testing.T) {
	e := sampleEvent()

	amqp, ct, _, err := e.AMQPBinary()
	if err != nil {
		t.Fatalf("amqp binary: %v", err)
	}
	if amqp["cloudEvents_id"] != "evt-1" || amqp["cloudEvents_type"] != e.Type {
		t.Fatalf("amqp cloudEvents_ prefix wrong: %v", amqp)
	}
	if ct != "application/json" {
		t.Fatalf("amqp content-type wrong: %q", ct)
	}
	if _, bad := amqp["cloudEvents_datacontenttype"]; bad {
		t.Fatalf("datacontenttype must not be a cloudEvents_ property")
	}

	mqtt, _, _, err := e.MQTTBinary()
	if err != nil {
		t.Fatalf("mqtt binary: %v", err)
	}
	// MQTT uses the bare attribute name as the user-property name (no prefix).
	if mqtt["id"] != "evt-1" || mqtt["type"] != e.Type {
		t.Fatalf("mqtt bare-name user properties wrong: %v", mqtt)
	}
	if _, prefixed := mqtt["ce_id"]; prefixed {
		t.Fatalf("mqtt must not prefix attribute names")
	}
}

func TestFromBinaryIgnoresForeignAttributes(t *testing.T) {
	// A Kafka header set that mixes a non-CloudEvents header must be ignored.
	attrs := map[string]string{
		"ce_specversion": "1.0",
		"ce_id":          "x",
		"ce_source":      "/s",
		"ce_type":        "t",
		"x-trace":        "abc", // foreign, no ce_ prefix
	}
	got, err := FromBinary(KafkaPrefix, attrs, "", nil)
	if err != nil {
		t.Fatalf("from binary: %v", err)
	}
	if got.Extensions["trace"] != "" || got.Extensions["x-trace"] != "" {
		t.Fatalf("foreign header leaked into extensions: %v", got.Extensions)
	}
}
