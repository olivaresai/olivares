// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package siemsink

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// fixedTime is a stable event time so the envelopes are byte-deterministic.
var fixedTime = time.Date(2026, 6, 12, 10, 30, 0, 0, time.UTC)

func jsonEvent() Event {
	return Event{
		Body:       []byte(`{"class_uid":6003,"message":"agent.create"}`),
		BodyIsJSON: true,
		Message:    "agent.create",
		Time:       fixedTime,
		Source:     "olivares.security",
		Tags:       map[string]string{"severity": "high", "tenant": "t1", "type": "finding.reported"},
	}
}

func textEvent() Event {
	return Event{
		Body:       []byte(`CEF:0|Olivares|ControlPlane|1.0|agent.create|agent.create|3|olvSeq=7`),
		BodyIsJSON: false,
		Message:    "agent.create",
		Time:       fixedTime,
		Source:     "olivares.audit",
	}
}

func TestRenderUnknownKindFailsClosed(t *testing.T) {
	if _, err := Render(Sink{Kind: "nope", Endpoint: "https://x"}, jsonEvent()); err == nil {
		t.Fatal("unknown kind must error (deny-closed)")
	}
	if _, err := Render(Sink{Kind: ""}, jsonEvent()); err == nil {
		t.Fatal("empty kind must error")
	}
}

func TestHTTPSVerbatim(t *testing.T) {
	req, err := Render(Sink{Kind: KindHTTPS, Endpoint: "https://collector.example/in"}, jsonEvent())
	if err != nil {
		t.Fatal(err)
	}
	if req.URL != "https://collector.example/in" {
		t.Fatalf("url = %q", req.URL)
	}
	if string(req.Body) != string(jsonEvent().Body) {
		t.Fatalf("body must be verbatim, got %q", req.Body)
	}
	if req.Header["Content-Type"] != "application/json" {
		t.Fatalf("content-type = %q", req.Header["Content-Type"])
	}
	// No sink auth header — the generic HTTPS sink is authenticated by the engine HMAC.
	if _, ok := req.Header["Authorization"]; ok {
		t.Fatal("https sink must not stamp its own Authorization header")
	}
}

func TestSplunkHECJSONEnvelope(t *testing.T) {
	req, err := Render(Sink{
		Kind: KindSplunkHEC, Endpoint: "https://splunk:8088/", Cred: "tok123",
		Opts: map[string]string{"index": "main", "sourcetype": "olivares:audit", "host": "h1"},
	}, jsonEvent())
	if err != nil {
		t.Fatal(err)
	}
	if req.URL != "https://splunk:8088/services/collector/event" {
		t.Fatalf("url = %q", req.URL)
	}
	if req.Header["Authorization"] != "Splunk tok123" {
		t.Fatalf("auth = %q", req.Header["Authorization"])
	}
	var env hecEnvelope
	if err := json.Unmarshal(req.Body, &env); err != nil {
		t.Fatalf("envelope not json: %v", err)
	}
	if env.Index != "main" || env.Sourcetype != "olivares:audit" || env.Host != "h1" {
		t.Fatalf("routing not carried: %+v", env)
	}
	// The event rides verbatim under "event" (integrity fields untouched).
	if string(env.Event) != string(jsonEvent().Body) {
		t.Fatalf("event not verbatim: %s", env.Event)
	}
	if env.Time == 0 {
		t.Fatal("time must be set from event time")
	}
}

func TestSplunkHECTextUsesRaw(t *testing.T) {
	req, err := Render(Sink{
		Kind: KindSplunkHEC, Endpoint: "https://splunk:8088", Cred: "tok",
		Opts: map[string]string{"index": "audit", "sourcetype": "cef"},
	}, textEvent())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(req.URL, "https://splunk:8088/services/collector/raw?") {
		t.Fatalf("text format must use /raw: %q", req.URL)
	}
	if !strings.Contains(req.URL, "index=audit") || !strings.Contains(req.URL, "sourcetype=cef") {
		t.Fatalf("routing must be query params: %q", req.URL)
	}
	if string(req.Body) != string(textEvent().Body) {
		t.Fatalf("raw body must be verbatim: %q", req.Body)
	}
	if req.Header["Content-Type"] != "text/plain" {
		t.Fatalf("raw content-type = %q", req.Header["Content-Type"])
	}
}

func TestSplunkHECRequiresToken(t *testing.T) {
	if _, err := Render(Sink{Kind: KindSplunkHEC, Endpoint: "https://x"}, jsonEvent()); err == nil {
		t.Fatal("missing token must fail closed")
	}
}

func TestDatadogEntry(t *testing.T) {
	req, err := Render(Sink{
		Kind: KindDatadog, Endpoint: "https://http-intake.logs.datadoghq.com", Cred: "ddkey",
		Opts: map[string]string{"service": "cp", "source": "olivares"},
	}, jsonEvent())
	if err != nil {
		t.Fatal(err)
	}
	if req.URL != "https://http-intake.logs.datadoghq.com/api/v2/logs" {
		t.Fatalf("url = %q", req.URL)
	}
	if req.Header["DD-API-KEY"] != "ddkey" {
		t.Fatalf("dd key header = %q", req.Header["DD-API-KEY"])
	}
	var arr []ddEntry
	if err := json.Unmarshal(req.Body, &arr); err != nil || len(arr) != 1 {
		t.Fatalf("body must be one-element array: %v / %s", err, req.Body)
	}
	if arr[0].Message != string(jsonEvent().Body) {
		t.Fatalf("message must carry the encoded event: %q", arr[0].Message)
	}
	// ddtags deterministic + sorted.
	if arr[0].DDTags != "severity:high,tenant:t1,type:finding.reported" {
		t.Fatalf("ddtags = %q", arr[0].DDTags)
	}
}

func TestNewRelicPayload(t *testing.T) {
	req, err := Render(Sink{
		Kind: KindNewRelic, Endpoint: "https://log-api.newrelic.com", Cred: "nrkey",
	}, jsonEvent())
	if err != nil {
		t.Fatal(err)
	}
	if req.URL != "https://log-api.newrelic.com/log/v1" {
		t.Fatalf("url = %q", req.URL)
	}
	if req.Header["Api-Key"] != "nrkey" {
		t.Fatalf("api-key header = %q", req.Header["Api-Key"])
	}
	var arr []nrPayload
	if err := json.Unmarshal(req.Body, &arr); err != nil || len(arr) != 1 || len(arr[0].Logs) != 1 {
		t.Fatalf("body must be [{logs:[1]}]: %v / %s", err, req.Body)
	}
	if arr[0].Logs[0].Message != string(jsonEvent().Body) {
		t.Fatalf("message = %q", arr[0].Logs[0].Message)
	}
	if arr[0].Logs[0].Timestamp != fixedTime.UnixMilli() {
		t.Fatalf("timestamp = %d", arr[0].Logs[0].Timestamp)
	}
}

func TestSentinelDCR(t *testing.T) {
	req, err := Render(Sink{
		Kind: KindSentinelDCR, Endpoint: "https://dce.eastus-1.ingest.monitor.azure.com", Cred: "bearer-xyz",
		Opts: map[string]string{"dcr_immutable_id": "dcr-abc", "stream": "Custom-Olivares_CL"},
	}, jsonEvent())
	if err != nil {
		t.Fatal(err)
	}
	want := "https://dce.eastus-1.ingest.monitor.azure.com/dataCollectionRules/dcr-abc/streams/Custom-Olivares_CL?api-version=2023-01-01"
	if req.URL != want {
		t.Fatalf("url = %q", req.URL)
	}
	if req.Header["Authorization"] != "Bearer bearer-xyz" {
		t.Fatalf("auth = %q", req.Header["Authorization"])
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(req.Body, &arr); err != nil || len(arr) != 1 {
		t.Fatalf("body must be a one-element array: %v / %s", err, req.Body)
	}
	if string(arr[0]) != string(jsonEvent().Body) {
		t.Fatalf("element must be the verbatim event: %s", arr[0])
	}
}

func TestSentinelDCRTextWrapped(t *testing.T) {
	req, err := Render(Sink{
		Kind: KindSentinelDCR, Endpoint: "https://dce", Cred: "b",
		Opts: map[string]string{"dcr_immutable_id": "d", "stream": "s"},
	}, textEvent())
	if err != nil {
		t.Fatal(err)
	}
	var arr []map[string]string
	if err := json.Unmarshal(req.Body, &arr); err != nil || len(arr) != 1 {
		t.Fatalf("text must wrap to one object: %v / %s", err, req.Body)
	}
	if arr[0]["Message"] != string(textEvent().Body) {
		t.Fatalf("Message = %q", arr[0]["Message"])
	}
}

func TestSentinelDCRRequiresRouting(t *testing.T) {
	if _, err := Render(Sink{Kind: KindSentinelDCR, Endpoint: "https://dce", Cred: "b"}, jsonEvent()); err == nil {
		t.Fatal("missing dcr/stream must fail closed")
	}
}

func TestDeterministic(t *testing.T) {
	s := Sink{Kind: KindDatadog, Endpoint: "https://x", Cred: "k"}
	a, _ := Render(s, jsonEvent())
	b, _ := Render(s, jsonEvent())
	if string(a.Body) != string(b.Body) {
		t.Fatal("render must be deterministic")
	}
}
