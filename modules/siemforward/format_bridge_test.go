// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The bridge between the ledger's format registry and the SIEM push path. The
// eventing allow-list and the renderer are two separate closed sets: the allow-list
// decides which format a subscription may DECLARE, the renderer decides which one it
// can actually PRODUCE, and nothing in the type system ties them together. When a new
// format was added to the allow-list without being routed here, `audit.recorded`
// still rendered (it goes through core/audit.FormatEvent, which knew the format)
// while `finding.reported` and every other bus event failed at delivery time and
// dead-lettered — a subscription that worked for one event type and silently lost the
// others. These tests iterate the registry across all three of the renderer's paths
// so that combination cannot reappear.
package siemforward

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/eventing"
	"github.com/olivaresai/olivares/sdk/siemwire"
)

// A rendered body is checked against a property only the correct encoder yields.
// "Non-empty" would not bite: a mis-routed format falls through to whatever another
// branch produces, which is also non-empty.
func isBareLogRecord(b string) bool {
	return isJSONObject(b) && strings.Contains(b, `"timeUnixNano"`) && !strings.Contains(b, `"resourceLogs"`)
}

func isOTLPRequestEnvelope(b string) bool {
	return isJSONObject(b) && strings.Contains(b, `"resourceLogs"`) && strings.Contains(b, `"scopeLogs"`)
}

// dialectMarker resolves the assertion for one format: ONE wire shape per token,
// on every path. That is the catalog-remap contract — until it landed, the token
// `otlp` was the bare-LogRecord projection on the ledger path and a COMPLETE
// request envelope on the finding/generic paths (through siemfmt.OTLPLogJSON), a
// known defect this file pinned with a change detector
// (TestKnownDefectOTLPTokenHasTwoWireShapes) whose failure message demanded
// exactly the inversion performed here. The accepted design (Codex design audit,
// an internal design note (not shipped) decision 3,
// implemented by the catalog remap) made `otlp` the complete request
// everywhere with `otlp_envelope` its exact alias, and preserved the bare
// projection under the explicit `otlp_log_record` token — a pull-export
// capability the eventing surface deliberately does not declare. Nothing was
// deleted; TestOTLPTokenHasOneWireShapeEverywhere asserts the unified contract.
var lineMarkers = map[audit.Format]func(body string) bool{
	audit.FormatCEF:           func(b string) bool { return strings.HasPrefix(b, "CEF:0|") },
	audit.FormatLEEF:          func(b string) bool { return strings.HasPrefix(b, "LEEF:2.0|") },
	audit.FormatSyslog:        func(b string) bool { return strings.HasPrefix(b, "<") && strings.Contains(b, ">1 ") },
	audit.FormatOCSF:          func(b string) bool { return isJSONObject(b) && strings.Contains(b, `"class_uid"`) },
	audit.FormatOTLP:          isOTLPRequestEnvelope,
	audit.FormatOTLPEnvelope:  isOTLPRequestEnvelope,
	audit.FormatOTLPLogRecord: isBareLogRecord,
}

func dialectMarker(f audit.Format) func(string) bool {
	return lineMarkers[f]
}

func isJSONObject(s string) bool {
	var probe map[string]any
	return json.Unmarshal([]byte(s), &probe) == nil
}

// bridgeEvent is the ledger event the payload helper encodes, kept here so the test
// can re-derive the expected bytes from the SAME encoder the pull export uses.
func bridgeEvent() model.AuditEvent {
	return model.AuditEvent{
		ID: "led-1", TenantID: "ten-1", Seq: 7, OccurredAt: model.NewTimestamp(when),
		Actor: "user:u1", ActorKind: "user", Action: "agent.create",
		TargetKind: "agent", TargetID: "ag-1",
		MetaCommitment: fixtureCommitment,
		PayloadHash:    widen(32, 0x01, 0x02),
		PrevHash:       widen(32, 0x0a),
		Hash:           widen(32, 0x0b, 0x0c),
		Sig:            widen(64, 0x0d),
	}
}

// sealedWidths are the real widths a sealed ledger event carries: 32-byte digests
// and a 64-byte Ed25519 signature. The fixtures below use them because the encoder
// and the push DTO now refuse anything else — and because a 64-byte signature's
// base64 ends in "==", which is the padding that exposes whether a text dialect's
// escaping survives a round trip. Leading bytes are preserved so the assertions
// that look for them still read the same.
func widen(n int, prefix ...byte) []byte {
	out := make([]byte, n)
	copy(out, prefix)
	return out
}

// fixtureCommitment is a deterministic stand-in for the blinded metadata
// commitment the store computes. This package cannot import the canonical package
// (it is internal to core), which is exactly why the DTO carries the value
// verbatim rather than recomputing it.
var fixtureCommitment = widen(32, 0xc0, 0x77, 0x11, 0x7e)

// declarableSinkFormats are the tokens a subscription may DECLARE (the catalog's
// eventing subset) minus "json", whose structured passthrough the renderer
// intercepts before any dialect encoder runs (renderer.go:41-44) and which has
// its own rendering assertions elsewhere. otlp_log_record is deliberately NOT
// here: the eventing surface excludes it, so the renderer never legitimately
// sees it — its rendering lives on the pull-export side (core/audit's tests).
func declarableSinkFormats() []audit.Format {
	out := []audit.Format{}
	for _, f := range siemwire.EventingSinkFormats().Tokens() {
		if f == siemwire.TokenJSON {
			continue
		}
		out = append(out, f)
	}
	return out
}

// TestEveryDeclarableFormatRendersOnEveryEventPath: the renderer has three paths
// (audit.recorded, finding.reported, and the generic default) and each one reaches a
// different encoder. A declarable format must render on all three, because a
// subscription declares ONE format and receives whatever event types its filter
// matches.
func TestEveryDeclarableFormatRendersOnEveryEventPath(t *testing.T) {
	paths := []struct {
		name    string
		typ     string
		payload []byte
	}{
		{"ledger", "audit.recorded", auditPayload(t)},
		{"finding", "finding.reported", findingPayload(t)},
		// The default branch: any other bus event, projected through
		// genericNotification. This is the path the round-1 blocker broke.
		{"generic bus event", "agent.created", []byte(`{"id":"ag-1","name":"a"}`)},
	}
	r := NewRenderer()
	for _, f := range declarableSinkFormats() {
		for _, p := range paths {
			marker := dialectMarker(f)
			if marker == nil {
				t.Fatalf("format %q has no dialect assertion; add one rather than letting it render unchecked", f)
			}
			t.Run(string(f)+"/"+p.name, func(t *testing.T) {
				req, err := r.Render(
					eventing.SinkEvent{ID: "e1", Type: p.typ, Tenant: "ten-1", Source: "olivares.test", Time: when, Seq: 7, Payload: p.payload},
					eventing.SinkProfile{Kind: "https", Format: string(f), Endpoint: "https://collector/in"},
				)
				if err != nil {
					t.Fatalf("render %s as %s: %v (the engine retries then dead-letters this)", p.typ, f, err)
				}
				body := string(req.Body)
				if body == "" {
					t.Fatal("empty body")
				}
				if !marker(body) {
					t.Errorf("body is not in the %s dialect on the %s path: %s", f, p.name, body)
				}
			})
		}
	}
}

// TestOTLPTokenHasOneWireShapeEverywhere is the unified contract that replaced
// TestKnownDefectOTLPTokenHasTwoWireShapes when the catalog remap converged the
// shapes (that change detector failed on purpose at convergence; its failure
// message demanded this test). Both accepted spellings render the COMPLETE
// request envelope on every event path, and they render IDENTICAL bytes per
// path — the catalog's "exact alias" means byte equality, not same-shape.
func TestOTLPTokenHasOneWireShapeEverywhere(t *testing.T) {
	r := NewRenderer()
	paths := map[string][]byte{
		"audit.recorded":   auditPayload(t),
		"finding.reported": findingPayload(t),
		"agent.created":    []byte(`{"id":"ag-1"}`),
	}
	for typ, payload := range paths {
		bodies := map[audit.Format]string{}
		for _, f := range []audit.Format{audit.FormatOTLP, audit.FormatOTLPEnvelope} {
			req, err := r.Render(
				eventing.SinkEvent{ID: "e1", Type: typ, Tenant: "ten-1", Source: "olivares.test", Time: when, Seq: 7, Payload: payload},
				eventing.SinkProfile{Kind: "https", Format: string(f), Endpoint: "https://collector/in"},
			)
			if err != nil {
				t.Fatalf("render %s as %s: %v", typ, f, err)
			}
			if !isOTLPRequestEnvelope(string(req.Body)) {
				t.Errorf("%s as %s is not a request envelope: %s", typ, f, req.Body)
			}
			bodies[f] = string(req.Body)
		}
		if bodies[audit.FormatOTLP] != bodies[audit.FormatOTLPEnvelope] {
			t.Errorf("otlp and otlp_envelope diverged on %s:\n otlp: %s\nalias: %s",
				typ, bodies[audit.FormatOTLP], bodies[audit.FormatOTLPEnvelope])
		}
	}
}

// TestPushedLedgerBytesEqualPulledLedgerBytes: the push path claims it reuses the
// pull encoder so "the pushed bytes never drift from the pulled bytes and the
// integrity fields ride verbatim" (modules/siemforward/encode.go:136-139). A marker
// assertion cannot prove that — only byte equality can, for every DECLARABLE
// format (otlp_log_record is pull-only by design and never reaches this path;
// its bytes are pinned against the pull encoder in core/audit).
func TestPushedLedgerBytesEqualPulledLedgerBytes(t *testing.T) {
	r := NewRenderer()
	ev := bridgeEvent()
	for _, f := range declarableSinkFormats() {
		t.Run(string(f), func(t *testing.T) {
			want, err := audit.FormatEvent(ev, f)
			if err != nil {
				t.Fatalf("pull export cannot render %s: %v", f, err)
			}
			req, err := r.Render(
				eventing.SinkEvent{ID: "led-1", Type: "audit.recorded", Tenant: "ten-1", Source: "olivares.audit", Time: when, Seq: 7, Payload: auditPayload(t)},
				eventing.SinkProfile{Kind: "https", Format: string(f), Endpoint: "https://collector/in"},
			)
			if err != nil {
				t.Fatalf("push cannot render %s: %v", f, err)
			}
			if got := string(req.Body); got != want {
				t.Errorf("pushed bytes differ from pulled bytes for %s:\n push: %s\n pull: %s", f, got, want)
			}
		})
	}
}

// TestCorruptedSinkFormatIsRefusedOnEveryPath is the review-caught bypass
// guard: the audit.recorded path delegates to core/audit.FormatEvent, whose
// encoder serves the WIDER ledger surface, so without the renderer's own
// surface check a corrupted stored otlp_log_record — a ledger token eventing
// deliberately does not declare — rendered a bare LogRecord while every other
// event type failed. Corruption must fail identically on all three paths.
func TestCorruptedSinkFormatIsRefusedOnEveryPath(t *testing.T) {
	r := NewRenderer()
	paths := []struct {
		name    string
		typ     string
		payload []byte
	}{
		{"ledger", "audit.recorded", auditPayload(t)},
		{"finding", "finding.reported", findingPayload(t)},
		{"generic bus event", "agent.created", []byte(`{"id":"ag-1","name":"a"}`)},
	}
	for _, format := range []string{"otlp_log_record", "corrupted"} {
		for _, p := range paths {
			if _, err := r.Render(
				eventing.SinkEvent{ID: "e1", Type: p.typ, Tenant: "ten-1", Source: "olivares.test", Time: when, Seq: 7, Payload: p.payload},
				eventing.SinkProfile{Kind: "https", Format: format, Endpoint: "https://collector/in"},
			); err == nil {
				t.Errorf("format %q on the %s path must be refused, not rendered", format, p.name)
			}
		}
	}
}

// TestAPreUpgradePayloadStillRenders is the compatibility half of the commitment
// contract, and it guards against permanent evidence loss rather than a cosmetic
// error message.
//
// auditWire is not a transient wire shape: the eventing intake PERSISTS the
// marshaled payload and the dispatcher re-reads it at claim time, so payloads
// written before the commitment field existed are still queued and still
// replayable after an upgrade. If decoding one failed, every such delivery would
// burn its retry ladder into the dead-letter queue — and nothing would ever
// re-enqueue them, because the forward cursor has already advanced past those
// records and the intake dedups by audit event id. An absent key therefore MUST
// decode, and it means exactly what a NULL meta_blind column means on the row:
// a record sealed before metadata blinding existed.
func TestAPreUpgradePayloadStillRenders(t *testing.T) {
	legacy := `{"id":"led-1","tenant_id":"ten-1","seq":7,"occurred_at":"2026-01-02T03:04:05.000000000Z",` +
		`"actor":"user:u1","actor_kind":"user","action":"agent.create","target_kind":"agent",` +
		`"target_id":"ag-1","payload_hash":"` + hexOf(widen(32, 0x01, 0x02)) + `",` +
		`"prev_hash":"` + hexOf(widen(32, 0x0a)) + `","hash":"` + hexOf(widen(32, 0x0b, 0x0c)) + `","sig":""}`
	for _, f := range declarableSinkFormats() {
		body, _, _, _, err := auditBody([]byte(legacy), string(f))
		if err != nil {
			t.Fatalf("format %s: a pre-upgrade payload must still render, got: %v", f, err)
		}
		if len(body) == 0 {
			t.Fatalf("format %s: empty body", f)
		}
		if strings.Contains(string(body), "meta_commitment") || strings.Contains(string(body), "olvMetaCommitment") {
			t.Fatalf("format %s: a pre-upgrade record must carry NO commitment key: %s", f, body)
		}
	}
}

// TestAWrongWidthCommitmentInThePayloadIsRefused keeps the other half: absent is
// legal, short is not — it would render a line whose reconstruction fails at the
// consumer, which reads as tampering rather than as a corrupt payload.
func TestAWrongWidthCommitmentInThePayloadIsRefused(t *testing.T) {
	bad := `{"id":"led-1","tenant_id":"ten-1","seq":7,"occurred_at":"2026-01-02T03:04:05.000000000Z",` +
		`"actor":"user:u1","actor_kind":"user","action":"agent.create","target_kind":"agent",` +
		`"target_id":"ag-1","meta_commitment":"010203","payload_hash":"` + hexOf(widen(32, 0x01)) + `",` +
		`"prev_hash":"` + hexOf(widen(32, 0x0a)) + `","hash":"` + hexOf(widen(32, 0x0b)) + `","sig":""}`
	if _, _, _, _, err := auditBody([]byte(bad), "cef"); err == nil {
		t.Fatal("a 3-byte meta_commitment in a stored payload must be refused")
	}
}

func hexOf(b []byte) string { return hex.EncodeToString(b) }
