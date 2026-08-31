// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func newTestReceiver() (*receiver, *[]claudeEvent, *[]hookEvent) {
	var otel []claudeEvent
	var hooks []hookEvent
	r := &receiver{
		onOTEL: func(e claudeEvent) { otel = append(otel, e) },
		onHook: func(h hookEvent) { hooks = append(hooks, h) },
		now:    func() time.Time { return testTime },
	}
	return r, &otel, &hooks
}

func TestLogsServiceExport(t *testing.T) {
	r, otel, _ := newTestReceiver()
	svc := &logsService{r: r}
	req := exportLogs(
		[]*commonpb.KeyValue{kvStr(attrSessionID, "s")},
		logRecord(evtToolResult, testTime, kvStr(attrToolName, "Read"), kvStr(attrToolUseID, "tu_1")),
	)
	if _, err := svc.Export(t.Context(), req); err != nil {
		t.Fatalf("Export error: %v", err)
	}
	if len(*otel) != 1 || (*otel)[0].toolName != "Read" {
		t.Errorf("Export did not ingest: %+v", *otel)
	}
}

func TestIngestStampsMissingTime(t *testing.T) {
	r, otel, _ := newTestReceiver()
	req := exportLogs(nil, logRecord(evtUserPrompt, time.Time{}, kvStr(attrSessionID, "s")))
	r.ingestLogs(req)
	if len(*otel) != 1 || !(*otel)[0].at.Equal(testTime) {
		t.Errorf("missing time not stamped: %+v", *otel)
	}
}

func TestLogsHTTPProtobuf(t *testing.T) {
	r, otel, _ := newTestReceiver()
	srv := httptest.NewServer(r.httpHandler("/hooks"))
	defer srv.Close()

	body, err := proto.Marshal(exportLogs(
		[]*commonpb.KeyValue{kvStr(attrSessionID, "s")},
		logRecord(evtAPIRequest, testTime, kvStr(attrModel, "claude-opus-4-8"), kvDouble(attrCostUSD, 0.01)),
	))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(srv.URL+"/v1/logs", "application/x-protobuf", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
	if len(*otel) != 1 || (*otel)[0].name != evtAPIRequest {
		t.Errorf("protobuf logs not ingested: %+v", *otel)
	}
}

func TestLogsHTTPJSON(t *testing.T) {
	r, otel, _ := newTestReceiver()
	srv := httptest.NewServer(r.httpHandler("/hooks"))
	defer srv.Close()

	body, err := protojson.Marshal(exportLogs(
		[]*commonpb.KeyValue{kvStr(attrSessionID, "s")},
		logRecord(evtToolResult, testTime, kvStr(attrToolName, "Write")),
	))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(srv.URL+"/v1/logs", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
	if len(*otel) != 1 || (*otel)[0].toolName != "Write" {
		t.Errorf("json logs not ingested: %+v", *otel)
	}
}

func TestMetricsHTTPAcknowledged(t *testing.T) {
	r, _, _ := newTestReceiver()
	srv := httptest.NewServer(r.httpHandler("/hooks"))
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/v1/metrics", "application/x-protobuf", bytes.NewReader(nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("metrics ack status = %d", resp.StatusCode)
	}
}

func TestLogsHTTPRejectsGet(t *testing.T) {
	r, _, _ := newTestReceiver()
	srv := httptest.NewServer(r.httpHandler("/hooks"))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/v1/logs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET /v1/logs status = %d, want 405", resp.StatusCode)
	}
}

func TestHookHTTPRoute(t *testing.T) {
	r, _, hooks := newTestReceiver()
	srv := httptest.NewServer(r.httpHandler("/hooks"))
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/hooks", "application/json",
		bytes.NewReader([]byte(`{"session_id":"s","hook_event_name":"PostToolUse","tool_name":"Read","tool_input":{"file_path":"/x"}}`)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if len(*hooks) != 1 || (*hooks)[0].toolName != "Read" {
		t.Errorf("hook route not wired: %+v", *hooks)
	}
}

func mustResponse(t *testing.T, msg proto.Message) []byte {
	t.Helper()
	b, err := proto.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestUnmarshalRoundTrip(t *testing.T) {
	body := mustResponse(t, &collogspb.ExportLogsServiceResponse{})
	var resp collogspb.ExportLogsServiceResponse
	if err := unmarshalOTLP(body, false, &resp); err != nil {
		t.Fatalf("protobuf round trip: %v", err)
	}
}
