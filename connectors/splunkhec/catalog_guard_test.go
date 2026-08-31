// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0
package splunkhec

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/siemwire"
)

// The catalog guard this connector never had: every token the catalog's
// notification subset declares is accepted at Open AND renders its own dialect,
// the alias is byte-identical to its canonical form, text formats route to
// /raw (isTextFormat), an unknown token is rejected naming the accepted set,
// and corrupted internal state errors instead of silently relabelling as JSON.
func TestCatalogSubsetRendersEveryDialect(t *testing.T) {
	markers := map[siemwire.FormatToken]func(string) bool{
		siemwire.TokenJSON:         func(b string) bool { return strings.HasPrefix(b, "{") && strings.Contains(b, `"type"`) },
		siemwire.TokenCEF:          func(b string) bool { return strings.HasPrefix(b, "CEF:0|") },
		siemwire.TokenLEEF:         func(b string) bool { return strings.HasPrefix(b, "LEEF:2.0|") },
		siemwire.TokenSyslog:       func(b string) bool { return strings.HasPrefix(b, "<") && strings.Contains(b, ">1 ") },
		siemwire.TokenOTLP:         func(b string) bool { return strings.Contains(b, `"resourceLogs"`) },
		siemwire.TokenOTLPEnvelope: func(b string) bool { return strings.Contains(b, `"resourceLogs"`) },
		siemwire.TokenOCSF:         func(b string) bool { return strings.Contains(b, `"class_uid"`) },
		siemwire.TokenASIM:         func(b string) bool { return strings.Contains(b, `"EventSchema"`) },
	}
	wantText := map[siemwire.FormatToken]bool{
		siemwire.TokenCEF: true, siemwire.TokenLEEF: true, siemwire.TokenSyslog: true,
	}
	set := formatSet()
	if len(markers) != len(set.Tokens()) {
		t.Fatalf("this test pins %d dialects but the subset has %d — pin the new one", len(markers), len(set.Tokens()))
	}
	bodies := map[siemwire.FormatToken]string{}
	for _, tok := range set.Tokens() {
		o := New()
		err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
			"endpoint": "https://splunk.example:8088", "token": "tkn", "format": string(tok),
		}})
		if err != nil {
			t.Fatalf("Open rejected catalog token %s: %v", tok, err)
		}
		body, err := o.encode(sampleNotification())
		if err != nil {
			t.Fatalf("format %s accepted at Open but does not render: %v", tok, err)
		}
		if !markers[tok](string(body)) {
			t.Errorf("format %s rendered the wrong dialect: %s", tok, body)
		}
		if got := o.isTextFormat(); got != wantText[tok] {
			t.Errorf("format %s isTextFormat = %v, want %v (drives /raw vs /event)", tok, got, wantText[tok])
		}
		bodies[tok] = string(body)
	}
	if bodies[siemwire.TokenOTLP] != bodies[siemwire.TokenOTLPEnvelope] {
		t.Errorf("otlp and otlp_envelope must be byte-identical:\n otlp: %s\nalias: %s",
			bodies[siemwire.TokenOTLP], bodies[siemwire.TokenOTLPEnvelope])
	}
}

func TestUnknownFormatRejectedNamingTheSet(t *testing.T) {
	o := New()
	err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"endpoint": "https://splunk.example:8088", "token": "tkn", "format": "protobuf",
	}})
	if err == nil || !strings.Contains(err.Error(), formatSet().List()) {
		t.Fatalf("want a rejection naming the accepted set, got: %v", err)
	}
}

func TestCorruptedStoredFormatErrorsInsteadOfJSONRelabel(t *testing.T) {
	o := New()
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"endpoint": "https://splunk.example:8088", "token": "tkn", "format": "cef",
	}}); err != nil {
		t.Fatal(err)
	}
	o.format = "corrupted"
	if _, err := o.encode(sampleNotification()); err == nil {
		t.Fatal("an unrecognized stored format must be an error, never a silent JSON relabel")
	}
}
