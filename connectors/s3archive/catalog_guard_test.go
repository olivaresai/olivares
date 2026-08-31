// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0
package s3archive

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/siemwire"
)

// The catalog guard this connector never had — and the one that would have
// caught its drift: until the catalog, s3archive was the only notification
// connector without asim, an unrecorded divergence among four hand copies.
// Every token the catalog's notification subset declares is accepted at Open
// AND renders its own dialect with the right key extension and Content-Type,
// the alias is byte-identical to its canonical form, an unknown token is
// rejected naming the accepted set, and corrupted internal state errors
// instead of silently relabelling as JSON.
func TestCatalogSubsetRendersEveryDialect(t *testing.T) {
	type shape struct {
		marker func(string) bool
		ext    string
		ct     string
	}
	shapes := map[siemwire.FormatToken]shape{
		siemwire.TokenJSON:         {func(b string) bool { return strings.HasPrefix(b, "{") && strings.Contains(b, `"type"`) }, ".json", "application/json"},
		siemwire.TokenCEF:          {func(b string) bool { return strings.HasPrefix(b, "CEF:0|") }, ".cef", "text/plain"},
		siemwire.TokenLEEF:         {func(b string) bool { return strings.HasPrefix(b, "LEEF:2.0|") }, ".leef", "text/plain"},
		siemwire.TokenSyslog:       {func(b string) bool { return strings.HasPrefix(b, "<") && strings.Contains(b, ">1 ") }, ".log", "text/plain"},
		siemwire.TokenOTLP:         {func(b string) bool { return strings.Contains(b, `"resourceLogs"`) }, ".json", "application/json"},
		siemwire.TokenOTLPEnvelope: {func(b string) bool { return strings.Contains(b, `"resourceLogs"`) }, ".json", "application/json"},
		siemwire.TokenOCSF:         {func(b string) bool { return strings.Contains(b, `"class_uid"`) }, ".json", "application/json"},
		siemwire.TokenASIM:         {func(b string) bool { return strings.Contains(b, `"EventSchema"`) }, ".json", "application/json"},
	}
	set := formatSet()
	if len(shapes) != len(set.Tokens()) {
		t.Fatalf("this test pins %d dialects but the subset has %d — pin the new one", len(shapes), len(set.Tokens()))
	}
	bodies := map[siemwire.FormatToken]string{}
	for _, tok := range set.Tokens() {
		o := New()
		err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
			"region": "eu-west-1", "bucket": "worm-bucket-test",
			"access_key_id": "AKIDEXAMPLE", "secret_access_key": "sk", "format": string(tok),
		}})
		if err != nil {
			t.Fatalf("Open rejected catalog token %s: %v", tok, err)
		}
		body, ext, ct, err := o.encodeNotification(sampleNotification())
		if err != nil {
			t.Fatalf("format %s accepted at Open but does not render: %v", tok, err)
		}
		want := shapes[tok]
		if !want.marker(string(body)) {
			t.Errorf("format %s rendered the wrong dialect: %s", tok, body)
		}
		if ext != want.ext || ct != want.ct {
			t.Errorf("format %s shipped ext=%q ct=%q, want ext=%q ct=%q", tok, ext, ct, want.ext, want.ct)
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
		"region": "eu-west-1", "bucket": "worm-bucket-test",
		"access_key_id": "AKIDEXAMPLE", "secret_access_key": "sk", "format": "xml",
	}})
	if err == nil || !strings.Contains(err.Error(), formatSet().List()) {
		t.Fatalf("want a rejection naming the accepted set, got: %v", err)
	}
}

func TestCorruptedStoredFormatErrorsInsteadOfJSONRelabel(t *testing.T) {
	o := New()
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"region": "eu-west-1", "bucket": "worm-bucket-test",
		"access_key_id": "AKIDEXAMPLE", "secret_access_key": "sk", "format": "cef",
	}}); err != nil {
		t.Fatal(err)
	}
	o.format = "corrupted"
	if _, _, _, err := o.encodeNotification(sampleNotification()); err == nil {
		t.Fatal("an unrecognized stored format must be an error, never a silent JSON relabel")
	}
}
