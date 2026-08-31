// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0
package siem

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/siemwire"
)

// The catalog guard this connector never had: every token the catalog's
// notification subset declares is accepted at Open AND renders its own dialect
// with the right Content-Type, the alias is byte-identical to its canonical
// form, an unknown token is rejected naming the accepted set, and this
// connector's existing deny-closed corrupted-state error (the behavior the
// other three were equalized TO) stays exercised directly.
func TestCatalogSubsetRendersEveryDialect(t *testing.T) {
	type shape struct {
		marker func(string) bool
		ct     string
	}
	shapes := map[siemwire.FormatToken]shape{
		siemwire.TokenJSON:         {func(b string) bool { return strings.HasPrefix(b, "{") && strings.Contains(b, `"type"`) }, "application/json"},
		siemwire.TokenCEF:          {func(b string) bool { return strings.HasPrefix(b, "CEF:0|") }, "text/plain"},
		siemwire.TokenLEEF:         {func(b string) bool { return strings.HasPrefix(b, "LEEF:2.0|") }, "text/plain"},
		siemwire.TokenSyslog:       {func(b string) bool { return strings.HasPrefix(b, "<") && strings.Contains(b, ">1 ") }, "text/plain"},
		siemwire.TokenOTLP:         {func(b string) bool { return strings.Contains(b, `"resourceLogs"`) }, "application/json"},
		siemwire.TokenOTLPEnvelope: {func(b string) bool { return strings.Contains(b, `"resourceLogs"`) }, "application/json"},
		siemwire.TokenOCSF:         {func(b string) bool { return strings.Contains(b, `"class_uid"`) }, "application/json"},
		siemwire.TokenASIM:         {func(b string) bool { return strings.Contains(b, `"EventSchema"`) }, "application/json"},
	}
	set := formatSet()
	if len(shapes) != len(set.Tokens()) {
		t.Fatalf("this test pins %d dialects but the subset has %d — pin the new one", len(shapes), len(set.Tokens()))
	}
	bodies := map[siemwire.FormatToken]string{}
	for _, tok := range set.Tokens() {
		o := New()
		err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
			"destination": "http", "endpoint": "https://collector.example/in", "format": string(tok),
		}})
		if err != nil {
			t.Fatalf("Open rejected catalog token %s: %v", tok, err)
		}
		body, ct, err := o.formatBody(sampleNotification())
		if err != nil {
			t.Fatalf("format %s accepted at Open but does not render: %v", tok, err)
		}
		want := shapes[tok]
		if !want.marker(string(body)) {
			t.Errorf("format %s rendered the wrong dialect: %s", tok, body)
		}
		if ct != want.ct {
			t.Errorf("format %s shipped Content-Type %q, want %q", tok, ct, want.ct)
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
		"destination": "http", "endpoint": "https://collector.example/in", "format": "xml",
	}})
	if err == nil || !strings.Contains(err.Error(), formatSet().List()) {
		t.Fatalf("want a rejection naming the accepted set, got: %v", err)
	}
}

func TestCorruptedStoredFormatErrorsDirectly(t *testing.T) {
	o := New()
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"destination": "http", "endpoint": "https://collector.example/in", "format": "cef",
	}}); err != nil {
		t.Fatal(err)
	}
	o.format = "corrupted"
	if _, _, err := o.formatBody(sampleNotification()); err == nil {
		t.Fatal("an unrecognized stored format must be an error")
	}
}
