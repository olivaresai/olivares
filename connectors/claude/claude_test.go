// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/protobuf/proto"

	"github.com/olivaresai/olivares/sdk"
)

func TestDescriptor(t *testing.T) {
	d := New().Descriptor()
	if d.Name != Name || d.Type != sdk.TypeSource || d.APIVersion != sdk.APIVersion {
		t.Errorf("descriptor = %+v", d)
	}
	if len(d.ConfigFields) == 0 {
		t.Error("descriptor declares no config fields")
	}
}

func TestOpenBothDisabledFails(t *testing.T) {
	s := New()
	err := s.Open(t.Context(), sdk.Config{Settings: map[string]string{
		cfgEnableGRPC: "false",
		cfgEnableHTTP: "false",
	}})
	if err == nil {
		t.Fatal("opening with both receivers disabled must fail")
	}
	_ = s.Close(t.Context())
}

func TestOpenRefusesNonLoopbackBindByDefault(t *testing.T) {
	// Secure-by-default: the unauthenticated OTLP ingest must refuse a non-loopback
	// bind unless the operator opts in (docs/SECURITY-HARDENING.md, §6).
	s := New()
	err := s.Open(t.Context(), sdk.Config{Settings: map[string]string{
		cfgEnableGRPC: "false",
		cfgEnableHTTP: "true",
		cfgHTTPAddr:   "0.0.0.0:0",
	}})
	if err == nil {
		_ = s.Close(t.Context())
		t.Fatal("a non-loopback bind must be refused by default")
	}

	// With the explicit opt-out it is allowed (operator accepted the risk).
	s2 := New()
	if err := s2.Open(t.Context(), sdk.Config{Settings: map[string]string{
		cfgEnableGRPC:      "false",
		cfgEnableHTTP:      "true",
		cfgHTTPAddr:        "0.0.0.0:0",
		cfgAllowPublicBind: "true",
	}}); err != nil {
		t.Fatalf("opt-in public bind should be allowed: %v", err)
	}
	_ = s2.Close(t.Context())
}

func TestOpenCloseReleasesListener(t *testing.T) {
	s := New()
	cfg := sdk.Config{Settings: map[string]string{
		cfgEnableGRPC: "false",
		cfgEnableHTTP: "true",
		cfgHTTPAddr:   "127.0.0.1:0",
	}}
	if err := s.Open(t.Context(), cfg); err != nil {
		t.Fatalf("open: %v", err)
	}
	addr := s.httpLis.Addr().String()
	if err := s.Close(t.Context()); err != nil {
		t.Fatalf("close: %v", err)
	}
	if s.httpLis != nil {
		t.Error("Close did not nil the listener")
	}
	// The port must be free again (no leak): a dial should be refused.
	if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
		_ = c.Close()
		t.Error("listener still accepting after Close")
	}
	// A second Close is a safe no-op.
	if err := s.Close(t.Context()); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestGatherEndToEnd(t *testing.T) {
	s := New()
	cfg := sdk.Config{Settings: map[string]string{
		cfgEnableGRPC:      "false",
		cfgEnableHTTP:      "true",
		cfgHTTPAddr:        "127.0.0.1:0",
		cfgCorrelationWait: "1s",
	}}
	if err := s.Open(t.Context(), cfg); err != nil {
		t.Fatalf("open: %v", err)
	}
	addr := s.httpLis.Addr().String()

	sink := &fakeSink{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Gather(ctx, sink) }()

	base := "http://" + addr

	// OTEL tool_result (no detail) + matching PostToolUse hook with the resource,
	// plus an api_request carrying cost — exercises the edge and cost paths.
	body, err := proto.Marshal(exportLogs(
		[]*commonpb.KeyValue{kvStr(attrSessionID, "sess-e2e")},
		logRecord(evtToolResult, testTime, kvStr(attrToolName, "Read"), kvStr(attrToolUseID, "tu_e2e")),
		logRecord(evtAPIRequest, testTime, kvStr(attrModel, "claude-opus-4-8"), kvDouble(attrCostUSD, 0.05), kvInt(attrInputTokens, 900)),
	))
	if err != nil {
		t.Fatal(err)
	}
	postRetry(t, base+"/v1/logs", "application/x-protobuf", body)
	postRetry(t, base+"/hooks", "application/json",
		[]byte(`{"session_id":"sess-e2e","hook_event_name":"PostToolUse","tool_name":"Read","tool_use_id":"tu_e2e","tool_input":{"file_path":"/etc/hosts"}}`))

	// The pair completes on the second offer → exactly one edge, resource from hook.
	waitFor(t, 2*time.Second, func() bool { return len(sink.edges()) == 1 && len(sink.costs()) == 1 })
	edges := sink.edges()
	if len(edges) != 1 {
		t.Fatalf("want 1 edge, got %d (%+v)", len(edges), edges)
	}
	if edges[0].ResourceRef != "/etc/hosts" || edges[0].OriginRef != "sess-e2e" {
		t.Errorf("edge = %+v", edges[0])
	}
	if costs := sink.costs(); len(costs) != 1 || costs[0].CostMicroUSD != 50000 {
		t.Errorf("cost = %+v", costs)
	}
	// The only finding in a healthy run is the OBS-10 self-audit posture record
	// emitted at Gather start; no anti-evasion or error finding is expected.
	f := sink.findings()
	if len(f) != 1 || f[0].Kind != "self_audit" {
		t.Errorf("want exactly the self_audit posture finding in a healthy run, got %+v", f)
	}
	if f[0].Title != "Claude telemetry content-capture posture: structural-only" {
		t.Errorf("self-audit posture finding = %+v", f[0])
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Gather returned error on clean stop: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Gather did not return after ctx cancel")
	}
}

func TestGatherRedactsSecretEndToEnd(t *testing.T) {
	s := New()
	if err := s.Open(t.Context(), sdk.Config{Settings: map[string]string{
		cfgEnableGRPC: "false", cfgEnableHTTP: "true", cfgHTTPAddr: "127.0.0.1:0", cfgCorrelationWait: "1s",
	}}); err != nil {
		t.Fatalf("open: %v", err)
	}
	addr := s.httpLis.Addr().String()
	sink := &fakeSink{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Gather(ctx, sink) }()
	base := "http://" + addr

	// Pair an OTEL tool_result with a hook whose tool_input carries a secret, for
	// both a file path and a Bash command.
	body, _ := proto.Marshal(exportLogs(
		[]*commonpb.KeyValue{kvStr(attrSessionID, "sx")},
		logRecord(evtToolResult, testTime, kvStr(attrToolName, "Read"), kvStr(attrToolUseID, "t1")),
		logRecord(evtToolResult, testTime, kvStr(attrToolName, "Bash"), kvStr(attrToolUseID, "t2")),
	))
	postRetry(t, base+"/v1/logs", "application/x-protobuf", body)
	postRetry(t, base+"/hooks", "application/json",
		[]byte(`{"session_id":"sx","hook_event_name":"PostToolUse","tool_name":"Read","tool_use_id":"t1","tool_input":{"file_path":"/tmp/AKIAIOSFODNN7EXAMPLE/x"}}`))
	postRetry(t, base+"/hooks", "application/json",
		[]byte(`{"session_id":"sx","hook_event_name":"PostToolUse","tool_name":"Bash","tool_use_id":"t2","tool_input":{"command":"deploy --token=ghp_1234567890abcdefghijklmnopqrstuvwxyzAB"}}`))

	waitFor(t, 2*time.Second, func() bool { return len(sink.edges()) == 2 })
	for _, e := range sink.edges() {
		blob := e.OriginRef + "|" + e.ResourceKind + "|" + e.ResourceRef + "|" + e.ToolRef
		for _, leak := range []string{"AKIAIOSFODNN7EXAMPLE", "ghp_1234567890"} {
			if strings.Contains(blob, leak) {
				t.Errorf("secret %q leaked into edge: %q", leak, blob)
			}
		}
	}
}

func TestGatherOTELDetailPath(t *testing.T) {
	// OTEL_LOG_TOOL_DETAILS=1: the OTEL tool_result itself carries tool_input, so
	// the resource resolves with no hook. A secret in that input must be redacted.
	s := New()
	if err := s.Open(t.Context(), sdk.Config{Settings: map[string]string{
		cfgEnableGRPC: "false", cfgEnableHTTP: "true", cfgHTTPAddr: "127.0.0.1:0", cfgCorrelationWait: "60s",
	}}); err != nil {
		t.Fatalf("open: %v", err)
	}
	addr := s.httpLis.Addr().String()
	sink := &fakeSink{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Gather(ctx, sink) }()

	body, _ := proto.Marshal(exportLogs(
		[]*commonpb.KeyValue{kvStr(attrSessionID, "sd")},
		logRecord(evtToolResult, testTime, kvStr(attrToolName, "Read"),
			kvObj(attrToolInput, kvStr("file_path", "/tmp/AKIAIOSFODNN7EXAMPLE/x"))),
	))
	postRetry(t, "http://"+addr+"/v1/logs", "application/x-protobuf", body)

	cancel() // large window: only the shutdown drain delivers the OTEL-only edge.
	<-done
	edges := sink.edges()
	if len(edges) != 1 || edges[0].ResourceKind != resFile {
		t.Fatalf("OTEL detail path edge = %+v", edges)
	}
	if strings.Contains(edges[0].ResourceRef, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("OTEL detail path leaked a secret: %q", edges[0].ResourceRef)
	}
}

func TestGatherDrainsPendingOnShutdown(t *testing.T) {
	s := New()
	cfg := sdk.Config{Settings: map[string]string{
		cfgEnableGRPC: "false",
		cfgEnableHTTP: "true",
		cfgHTTPAddr:   "127.0.0.1:0",
		// A long window means the janitor never sweeps the pending call before we
		// stop, so only the shutdown drain can deliver it — exactly the path the
		// emitCtx fix protects.
		cfgCorrelationWait: "60s",
	}}
	if err := s.Open(t.Context(), cfg); err != nil {
		t.Fatalf("open: %v", err)
	}
	addr := s.httpLis.Addr().String()

	sink := &fakeSink{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Gather(ctx, sink) }()

	// A hook with no matching OTEL span: it stays buffered in the correlator.
	postRetry(t, "http://"+addr+"/hooks", "application/json",
		[]byte(`{"session_id":"s","hook_event_name":"PostToolUse","tool_name":"Write","tool_input":{"file_path":"/pending"}}`))

	cancel() // stop before any sweep — the drain must still flush the pending edge.
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Gather did not return")
	}
	edges := sink.edges()
	if len(edges) != 1 || edges[0].ResourceRef != "/pending" {
		t.Fatalf("pending edge lost on shutdown: %+v", edges)
	}
}

// postRetry POSTs body, retrying briefly while the server's accept loop comes up.
func postRetry(t *testing.T, url, contentType string, body []byte) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		resp, err := http.Post(url, contentType, bytes.NewReader(body))
		if err == nil {
			_ = resp.Body.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("POST %s failed: %v", url, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitFor polls cond until true or the timeout elapses.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}
