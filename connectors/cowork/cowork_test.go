// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cowork

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// openGather opens a loopback Source with the given extra settings and runs Gather in
// a goroutine, returning the base URL, the sink, and a stop func. auth is applied via
// the returned base + token.
func openGather(t *testing.T, settings map[string]string) (base string, sink *fakeSink, stop func()) {
	t.Helper()
	cfg := map[string]string{cfgEnableHTTP: "true", cfgHTTPAddr: "127.0.0.1:0"}
	for k, v := range settings {
		cfg[k] = v
	}
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: cfg}); err != nil {
		t.Fatalf("open: %v", err)
	}
	addr := s.httpLis.Addr().String()
	sink = &fakeSink{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Gather(ctx, sink) }()
	stop = func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Gather returned error on clean stop: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("Gather did not return after ctx cancel")
		}
	}
	return "http://" + addr, sink, stop
}

// TestGatherEndToEnd drives the receiver through a real OTLP/HTTP POST and asserts
// the full mapping: identity edges, a file-write access edge, a cost sample, the
// auto-approved-high-risk finding, the denied-decision finding, and the startup
// self-audit posture finding.
func TestGatherEndToEnd(t *testing.T) {
	const tok = "Bearer secret-tok"
	base, sink, stop := openGather(t, map[string]string{cfgAuthToken: tok, cfgAuthHeader: "Authorization"})
	defer stop()

	body, err := proto.Marshal(exportLogs(
		coworkRes(),
		logRecord(evtToolResult, testTime, kvStr(attrToolName, "Write"), kvStr(attrDecisionSource, srcConfig),
			kvObj(attrToolInput, kvStr("file_path", "/etc/app.conf"))),
		logRecord(evtAPIRequest, testTime, kvStr(attrModel, "claude-opus-4-8"), kvDouble(attrCostUSD, 0.05), kvInt(attrInputTokens, 900)),
		logRecord(evtToolDecision, testTime, kvStr(attrToolName, "Bash"), kvStr(attrDecision, decisionReject), kvStr(attrSource, "user_reject")),
	))
	if err != nil {
		t.Fatal(err)
	}
	resp := postRetry(t, base+"/v1/logs", "application/x-protobuf", body, "Authorization", tok)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST status = %d, want 200", resp.StatusCode)
	}

	waitFor(t, 2*time.Second, func() bool {
		return len(sink.edges()) >= 3 && len(sink.costs()) == 1 && len(sink.findingsOfKind(findingKindAutoApproved)) == 1
	})

	// Access edge: the file write, resource resolved from tool_input.
	var sawWrite bool
	for _, e := range sink.edges() {
		if e.ResourceKind == resFile && e.ResourceRef == "/etc/app.conf" {
			sawWrite = true
		}
	}
	if !sawWrite {
		t.Errorf("expected a file-write edge to /etc/app.conf, edges=%+v", sink.edges())
	}

	// Identity edges materialize the shared account (the correlation join point).
	var sawAccount bool
	for _, e := range sink.edges() {
		if e.ResourceKind == resIdentityAccount && e.ResourceRef == "user_01ACC" {
			sawAccount = true
		}
	}
	if !sawAccount {
		t.Error("expected a session→identity.account edge for the shared account id")
	}

	// Cost sample attributed to the account.
	costs := sink.costs()
	if len(costs) != 1 || costs[0].CostMicroUSD != 50000 || costs[0].Actor != "user_01ACC" {
		t.Errorf("cost = %+v", costs)
	}

	// The net-new governance finding + the denial finding + the startup posture.
	if n := len(sink.findingsOfKind(findingKindAutoApproved)); n != 1 {
		t.Errorf("auto-approved findings = %d, want 1", n)
	}
	if n := len(sink.findingsOfKind(findingKindPolicyDecision)); n != 1 {
		t.Errorf("policy-decision findings = %d, want 1", n)
	}
	if n := len(sink.findingsOfKind(findingKindSelfAudit)); n != 1 {
		t.Errorf("self-audit findings = %d, want 1 (the startup posture record)", n)
	}
}

// TestAuthRejectsMissingToken proves a configured auth_token gates the receiver: a
// POST without the matching header is 401 and emits nothing beyond the startup posture.
func TestAuthRejectsMissingToken(t *testing.T) {
	const tok = "Bearer secret-tok"
	base, sink, stop := openGather(t, map[string]string{cfgAuthToken: tok})
	defer stop()

	body, _ := proto.Marshal(exportLogs(coworkRes(), logRecord(evtToolResult, testTime, kvStr(attrToolName, "Read"))))
	resp := postRetry(t, base+"/v1/logs", "application/x-protobuf", body, "Authorization", "") // no token
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	// Give any (erroneous) emission a chance, then assert only the startup posture exists.
	time.Sleep(50 * time.Millisecond)
	if len(sink.edges()) != 0 {
		t.Errorf("an unauthorized POST must not produce edges, got %+v", sink.edges())
	}
}

// TestGatherRedactsSecretEndToEnd proves a secret embedded in a tool input never
// survives into an emitted edge field.
func TestGatherRedactsSecretEndToEnd(t *testing.T) {
	base, sink, stop := openGather(t, nil)
	defer stop()

	const secret = "AKIAIOSFODNN7EXAMPLE"
	body, _ := proto.Marshal(exportLogs(
		coworkRes(),
		logRecord(evtToolResult, testTime, kvStr(attrToolName, "Write"),
			kvObj(attrToolInput, kvStr("file_path", "/data/"+secret+"/out.txt"))),
	))
	resp := postRetry(t, base+"/v1/logs", "application/x-protobuf", body, "", "")
	_ = resp.Body.Close()

	waitFor(t, 2*time.Second, func() bool { return len(sink.edges()) >= 3 })
	for _, e := range sink.edges() {
		blob := e.OriginRef + "|" + e.ResourceKind + "|" + e.ResourceRef + "|" + e.ToolRef
		if strings.Contains(blob, secret) {
			t.Errorf("secret leaked into an edge: %q", blob)
		}
	}
}

// TestOpenDenyClosedPublicBind proves the inbound-push security stance: a non-loopback
// bind without an auth_token is refused, and accepted once a token (or the explicit
// escape hatch) is set.
func TestOpenDenyClosedPublicBind(t *testing.T) {
	openWith := func(settings map[string]string) error {
		cfg := map[string]string{cfgEnableHTTP: "true", cfgHTTPAddr: "0.0.0.0:0"}
		for k, v := range settings {
			cfg[k] = v
		}
		s := New()
		err := s.Open(context.Background(), sdk.Config{Settings: cfg})
		_ = s.Close(context.Background())
		return err
	}
	if err := openWith(nil); err == nil {
		t.Error("a non-loopback bind without auth_token must be refused (deny-closed)")
	}
	if err := openWith(map[string]string{cfgAuthToken: "Bearer t"}); err != nil {
		t.Errorf("a non-loopback bind WITH auth_token must be allowed: %v", err)
	}
	if err := openWith(map[string]string{cfgAllowPublicBind: "true"}); err != nil {
		t.Errorf("allow_public_bind must permit a non-loopback bind: %v", err)
	}
}

// TestOpenRejectsNoReceiver proves Open fails when no receiver is enabled.
func TestOpenRejectsNoReceiver(t *testing.T) {
	s := New()
	err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{cfgEnableHTTP: "false"}})
	if err == nil {
		t.Error("Open with enable_http=false must fail (nothing to receive)")
	}
}

// TestOpenRejectsInvalidConnectorControls proves the deny-closed authoring stance is
// wired through Open: a malformed (or invalid-level) connector_controls policy fails
// BEFORE Gather, never silently leaving the org ungoverned.
func TestOpenRejectsInvalidConnectorControls(t *testing.T) {
	for name, raw := range map[string]string{
		"malformed JSON": `{"connectors": {`,
		"invalid level":  `{"default": "allow"}`,
	} {
		s := New()
		err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
			cfgEnableHTTP: "true", cfgHTTPAddr: "127.0.0.1:0", cfgConnectorControls: raw,
		}})
		_ = s.Close(context.Background())
		if err == nil {
			t.Errorf("%s connector_controls must fail Open", name)
			continue
		}
		if !strings.Contains(err.Error(), "invalid connector_controls") {
			t.Errorf("%s: error should name the failing key, got %v", name, err)
		}
	}
	// A valid policy opens fine (the positive control for this guard).
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		cfgEnableHTTP: "true", cfgHTTPAddr: "127.0.0.1:0",
		cfgConnectorControls: `{"connectors": {"github": {"level": "always_allow"}}}`,
	}}); err != nil {
		t.Errorf("a valid connector_controls policy must open: %v", err)
	}
	_ = s.Close(context.Background())
}

// TestGatherConnectorControlsEndToEnd drives the full control loop through a real
// OTLP/HTTP POST: at Gather start the configured policy is projected as PERMITTED
// policy edges hanging off the org identity, and a tool_result executing a BLOCKED
// mcp tool yields the high connector-control drift finding.
func TestGatherConnectorControlsEndToEnd(t *testing.T) {
	const policy = `{
		"connectors": {
			"github": {"level": "needs_approval", "tools": {"delete_repo": "blocked", "read_issues": "always_allow"}},
			"slack":  {"level": "always_allow"}
		}
	}`
	base, sink, stop := openGather(t, map[string]string{cfgConnectorControls: policy, cfgOrgRef: "org-9"})
	defer stop()

	// The PERMITTED side lands before any telemetry: github + github/read_issues +
	// slack (delete_repo is blocked → no edge), in deterministic sorted order.
	waitFor(t, 2*time.Second, func() bool {
		n := 0
		for _, e := range sink.edges() {
			if e.Source == model.SignalPolicy {
				n++
			}
		}
		return n >= 3
	})
	var permitted []model.EdgeObservation
	for _, e := range sink.edges() {
		if e.Source == model.SignalPolicy {
			permitted = append(permitted, e)
		}
	}
	if len(permitted) != 3 {
		t.Fatalf("permitted edges = %d, want 3: %+v", len(permitted), permitted)
	}
	wantRefs := []struct{ kind, ref, toolRef string }{
		{resMCPServer, "github", ""},
		{resMCP, "github/read_issues", "read_issues"},
		{resMCPServer, "slack", ""},
	}
	for i, w := range wantRefs {
		e := permitted[i]
		if e.ResourceKind != w.kind || e.ResourceRef != w.ref || e.ToolRef != w.toolRef {
			t.Errorf("permitted[%d] = %s/%s tool=%q, want %s/%s tool=%q", i, e.ResourceKind, e.ResourceRef, e.ToolRef, w.kind, w.ref, w.toolRef)
		}
		if e.OriginKind != originIdentity || e.OriginRef != "org-9" {
			t.Errorf("permitted[%d] origin = %s/%s, want identity/org-9", i, e.OriginKind, e.OriginRef)
		}
		if e.Mode != model.ModeUnknown || e.Confidence != model.ConfidenceAttributed {
			t.Errorf("permitted[%d] mode/confidence = %s/%s", i, e.Mode, e.Confidence)
		}
		if e.ObservedAt.IsZero() {
			t.Errorf("permitted[%d] missing ObservedAt", i)
		}
	}

	// A BLOCKED mcp tool EXECUTES (tool_result) → the high drift finding.
	body, err := proto.Marshal(exportLogs(
		coworkRes(),
		logRecord(evtToolResult, testTime, kvStr(attrToolName, "mcp__github__delete_repo"), kvStr(attrDecisionSource, srcConfig)),
	))
	if err != nil {
		t.Fatal(err)
	}
	resp := postRetry(t, base+"/v1/logs", "application/x-protobuf", body, "", "")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST status = %d, want 200", resp.StatusCode)
	}

	waitFor(t, 2*time.Second, func() bool { return len(sink.findingsOfKind(findingKindControlDrift)) == 1 })
	drifts := sink.findingsOfKind(findingKindControlDrift)
	if len(drifts) != 1 {
		t.Fatalf("control drift findings = %d, want 1 (findings: %+v)", len(drifts), sink.findings())
	}
	f := drifts[0]
	if f.Severity != model.SeverityHigh || f.SubjectKind != originSession || f.SubjectRef != "sess-1" {
		t.Errorf("drift = %+v", f)
	}
	if want := "Cowork connector control drift: blocked connector/tool executed: github/delete_repo"; f.Title != want {
		t.Errorf("title = %q, want %q", f.Title, want)
	}
	if len(f.OWASPASI) != 1 || f.OWASPASI[0] != "ASI02" || len(f.OWASPLLM) != 1 || f.OWASPLLM[0] != "LLM06:2025" {
		t.Errorf("OWASP refs = ASI %v / LLM %v", f.OWASPASI, f.OWASPLLM)
	}
	if f.DetailHash == "" {
		t.Error("drift finding must carry a detail hash")
	}
}

// TestDescriptorShape sanity-checks the descriptor wiring.
func TestDescriptorShape(t *testing.T) {
	d := New().Descriptor()
	if d.Name != Name || d.Type != sdk.TypeSource || d.APIVersion != sdk.APIVersion {
		t.Errorf("descriptor = %+v", d)
	}
	if len(d.ConfigFields) == 0 {
		t.Error("descriptor should declare config fields")
	}
	if d.Version != "0.2.0" {
		t.Errorf("version = %q, want 0.2.0 (the connector-controls config surface)", d.Version)
	}
	// Ensure the secret auth_token field is marked Secret, and the new controls
	// surface is declared (connector_controls is policy, NOT a secret).
	var foundToken, foundControls, foundOrgRef bool
	for _, f := range d.ConfigFields {
		switch f.Key {
		case cfgAuthToken:
			foundToken = true
			if !f.Secret {
				t.Error("auth_token must be marked Secret")
			}
		case cfgConnectorControls:
			foundControls = true
			if f.Secret {
				t.Error("connector_controls is an authored policy, not a secret")
			}
		case cfgOrgRef:
			foundOrgRef = true
			if f.Default != defaultOrgRef {
				t.Errorf("org_ref default = %q, want %q", f.Default, defaultOrgRef)
			}
		}
	}
	if !foundToken || !foundControls || !foundOrgRef {
		t.Errorf("descriptor missing fields: token=%v controls=%v orgRef=%v", foundToken, foundControls, foundOrgRef)
	}
}
