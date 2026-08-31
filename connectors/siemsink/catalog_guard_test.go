// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0
package siemsink

import (
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
	"github.com/olivaresai/olivares/sdk/siemwire"
)

func guardNotification() sdk.Notification {
	return sdk.Notification{
		Type: "finding.reported", Title: "least-privilege drift", Severity: model.SeverityHigh,
		Tenant: "acme", Time: time.Unix(1700000000, 0).UTC(),
	}
}

// TestCatalogSubsetRendersEveryDialect is the catalog guard for the renderer the
// eventing SIEM sinks share. Its vocabulary is the catalog's eventing surface
// minus the json passthrough (intercepted upstream as the raw wireEvent), and
// this guard holds the failure mode a format allow-list cannot see on its own:
// a subscription is accepted with a format, then only SOME of its event types
// can be rendered — a mixed subscription delivers the ledger record and
// dead-letters the finding, which reads as a transport fault rather than a
// configuration one. Every declarable token must render ITS OWN dialect, not
// merely render something.
func TestCatalogSubsetRendersEveryDialect(t *testing.T) {
	markers := map[siemwire.FormatToken]func(string) bool{
		siemwire.TokenCEF:          func(b string) bool { return strings.HasPrefix(b, "CEF:0|") },
		siemwire.TokenLEEF:         func(b string) bool { return strings.HasPrefix(b, "LEEF:2.0|") },
		siemwire.TokenSyslog:       func(b string) bool { return strings.HasPrefix(b, "<") && strings.Contains(b, ">1 ") },
		siemwire.TokenOTLP:         func(b string) bool { return strings.Contains(b, `"resourceLogs"`) },
		siemwire.TokenOTLPEnvelope: func(b string) bool { return strings.Contains(b, `"resourceLogs"`) },
		siemwire.TokenOCSF:         func(b string) bool { return strings.Contains(b, `"class_uid"`) },
	}
	set := formatSet()
	if want := len(set.Tokens()) - 1; len(markers) != want { // -1: json never reaches the renderer
		t.Fatalf("this test pins %d dialects but the renderable subset has %d — pin the new one", len(markers), want)
	}
	n := guardNotification()
	bodies := map[siemwire.FormatToken]string{}
	for _, tok := range set.Tokens() {
		if tok == siemwire.TokenJSON {
			continue
		}
		body, isJSON, err := EncodeNotification(string(tok), n)
		if err != nil {
			t.Errorf("format %s: %v — an accepted subscription format must render every event type", tok, err)
			continue
		}
		if !markers[tok](string(body)) {
			t.Errorf("format %s rendered the wrong dialect: %s", tok, body)
		}
		if wantJSON := strings.HasPrefix(string(body), "{"); isJSON != wantJSON {
			t.Errorf("format %s: isJSON=%v disagrees with the body shape", tok, isJSON)
		}
		bodies[tok] = string(body)
	}
	// Both OTLP spellings render the complete ExportLogsServiceRequest envelope,
	// as they now do on every surface — byte-identical, not merely same-shaped.
	if bodies[siemwire.TokenOTLP] != bodies[siemwire.TokenOTLPEnvelope] {
		t.Errorf("otlp and otlp_envelope must be byte-identical:\n otlp: %s\nalias: %s",
			bodies[siemwire.TokenOTLP], bodies[siemwire.TokenOTLPEnvelope])
	}
}

// TestEmptyFormatIsTheSurfaceDefault: "" resolves to the catalog's eventing
// default (OCSF), byte-identical to asking for it by name.
func TestEmptyFormatIsTheSurfaceDefault(t *testing.T) {
	n := guardNotification()
	def, _, err := EncodeNotification("", n)
	if err != nil {
		t.Fatalf("empty format: %v", err)
	}
	explicit, _, err := EncodeNotification(string(formatSet().Default()), n)
	if err != nil {
		t.Fatalf("explicit default: %v", err)
	}
	if string(def) != string(explicit) {
		t.Errorf("empty format must render the surface default:\n empty: %s\ndefault: %s", def, explicit)
	}
}

// TestNonRenderableSpellingsRefused: an unknown token stays refused, and so does
// json — a MEMBER of the eventing surface that must never reach a dialect
// renderer (the engine intercepts the structured passthrough upstream; a json
// body emitted here would be an unenveloped duplicate of the wireEvent).
func TestNonRenderableSpellingsRefused(t *testing.T) {
	n := guardNotification()
	for _, format := range []string{"not-a-format", "json", "OTLP", " otlp"} {
		if _, _, err := EncodeNotification(format, n); err == nil {
			t.Errorf("format %q must be refused (resolution is exact-match on normalized spellings)", format)
		}
	}
}
