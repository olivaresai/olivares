// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package siemforward

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/eventing"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

var when = time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC)

// auditPayload builds the format-neutral ledger payload the forwarder would store.
func auditPayload(t *testing.T) []byte {
	t.Helper()
	ev := model.AuditEvent{
		ID: "led-1", TenantID: "ten-1", Seq: 7, OccurredAt: model.NewTimestamp(when),
		Actor: "user:u1", ActorKind: "user", Action: "agent.create",
		TargetKind: "agent", TargetID: "ag-1",
		MetaCommitment: fixtureCommitment,
		PayloadHash:    widen(32, 0x01, 0x02),
		PrevHash:       widen(32, 0x0a),
		Hash:           widen(32, 0x0b, 0x0c),
		Sig:            widen(64, 0x0d),
	}
	b, err := json.Marshal(auditWireFrom(ev))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func findingPayload(t *testing.T) []byte {
	t.Helper()
	fr := sdkmodel.FindingReport{
		Kind: "guardrail", Severity: sdkmodel.SeverityHigh, SubjectKind: "agent", SubjectRef: "ag-1",
		Title: "prompt injection blocked", DetailHash: "abcdef", OccurredAt: when,
		OWASPLLM: []string{"LLM01:2025"}, OWASPASI: []string{"ASI01"},
	}
	b, err := json.Marshal(fr)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The ledger goes to Splunk HEC as an OCSF /event envelope with Splunk auth, and the
// integrity fields ride verbatim inside the OCSF unmapped container.
func TestRenderLedgerToSplunkOCSF(t *testing.T) {
	r := NewRenderer()
	req, err := r.Render(
		eventing.SinkEvent{ID: "led-1", Type: "audit.recorded", Tenant: "ten-1", Source: "olivares.audit", Time: when, Seq: 7, Payload: auditPayload(t)},
		eventing.SinkProfile{Kind: "splunk_hec", Format: "ocsf", Cred: "hec-tok", Opts: map[string]string{"index": "audit"}, Endpoint: "https://splunk:8088"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if req.URL != "https://splunk:8088/services/collector/event" {
		t.Fatalf("url = %q", req.URL)
	}
	if req.Headers["Authorization"] != "Splunk hec-tok" {
		t.Fatalf("auth = %q", req.Headers["Authorization"])
	}
	// The HEC envelope wraps an OCSF object carrying the integrity fields verbatim.
	var env struct {
		Event json.RawMessage `json:"event"`
		Index string          `json:"index"`
	}
	if err := json.Unmarshal(req.Body, &env); err != nil {
		t.Fatalf("not a HEC envelope: %v / %s", err, req.Body)
	}
	if env.Index != "audit" {
		t.Fatalf("index = %q", env.Index)
	}
	ocsf := string(env.Event)
	for _, want := range []string{`"class_uid":6003`, `"ai.olivares.audit.hash":"0b0c0000`, `"ai.olivares.audit.seq":7`} {
		if !strings.Contains(ocsf, want) {
			t.Fatalf("OCSF missing %q in %s", want, ocsf)
		}
	}
}

// The ledger as CEF over a generic HTTPS sink is a text line, posted verbatim.
func TestRenderLedgerToHTTPSCEF(t *testing.T) {
	r := NewRenderer()
	req, err := r.Render(
		eventing.SinkEvent{Type: "audit.recorded", Tenant: "ten-1", Time: when, Seq: 7, Payload: auditPayload(t)},
		eventing.SinkProfile{Kind: "https", Format: "cef", Endpoint: "https://collector/in"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if req.URL != "https://collector/in" {
		t.Fatalf("url = %q", req.URL)
	}
	body := string(req.Body)
	if !strings.HasPrefix(body, "CEF:0|") {
		t.Fatalf("expected a CEF line, got %q", body)
	}
	if !strings.Contains(body, "olvHash=0b0c0000") {
		t.Fatalf("CEF missing the chain hash: %q", body)
	}
}

// A finding to Datadog rides the Logs Intake v2 entry with DD-API-KEY; the encoded
// OCSF event is the log message and the detail is never present beyond its hash.
func TestRenderFindingToDatadog(t *testing.T) {
	r := NewRenderer()
	req, err := r.Render(
		eventing.SinkEvent{Type: "finding.reported", Tenant: "ten-1", Time: when, Payload: findingPayload(t)},
		eventing.SinkProfile{Kind: "datadog", Format: "ocsf", Cred: "ddkey", Endpoint: "https://http-intake.logs.datadoghq.com"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if req.URL != "https://http-intake.logs.datadoghq.com/api/v2/logs" {
		t.Fatalf("url = %q", req.URL)
	}
	if req.Headers["DD-API-KEY"] != "ddkey" {
		t.Fatalf("dd key = %q", req.Headers["DD-API-KEY"])
	}
	body := string(req.Body)
	if !strings.Contains(body, "abcdef") {
		t.Fatalf("finding detail_hash must survive: %s", body)
	}
}

// json format is a structured passthrough envelope (no dialect transform).
func TestRenderJSONPassthrough(t *testing.T) {
	r := NewRenderer()
	req, err := r.Render(
		eventing.SinkEvent{ID: "e1", Type: "finding.reported", Tenant: "ten-1", Time: when, Payload: findingPayload(t)},
		eventing.SinkProfile{Kind: "https", Format: "json", Endpoint: "https://x/in"},
	)
	if err != nil {
		t.Fatal(err)
	}
	var g genericEvent
	if err := json.Unmarshal(req.Body, &g); err != nil {
		t.Fatalf("json passthrough not an envelope: %v / %s", err, req.Body)
	}
	if g.Type != "finding.reported" || g.ID != "e1" {
		t.Fatalf("envelope = %+v", g)
	}
}

// Deny-closed: an unknown sink kind errors (the engine then dead-letters honestly).
func TestRenderUnknownKindErrors(t *testing.T) {
	r := NewRenderer()
	_, err := r.Render(
		eventing.SinkEvent{Type: "finding.reported", Payload: findingPayload(t)},
		eventing.SinkProfile{Kind: "nonsense", Format: "ocsf", Endpoint: "https://x"},
	)
	if err == nil {
		t.Fatal("unknown sink kind must error")
	}
}
