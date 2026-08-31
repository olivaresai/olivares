// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0
package filelog

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/siemwire"
)

// The catalog guard this connector never had: every token the catalog's
// notification subset declares is accepted at Open AND renders its own dialect
// (a validator-only check would pass a token routed to the wrong encoder), the
// alias is byte-identical to its canonical form, an unknown token is rejected
// naming the accepted set, and corrupted internal state errors instead of
// silently relabelling as JSON.
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
	set := formatSet()
	if len(markers) != len(set.Tokens()) {
		t.Fatalf("this test pins %d dialects but the subset has %d — pin the new one", len(markers), len(set.Tokens()))
	}
	bodies := map[siemwire.FormatToken]string{}
	for _, tok := range set.Tokens() {
		o, _ := openFile(t, string(tok))
		line, err := o.encode(sampleNotification())
		_ = o.Close(context.Background())
		if err != nil {
			t.Fatalf("format %s accepted at Open but does not render: %v", tok, err)
		}
		if !markers[tok](string(line)) {
			t.Errorf("format %s rendered the wrong dialect: %s", tok, line)
		}
		bodies[tok] = string(line)
	}
	if bodies[siemwire.TokenOTLP] != bodies[siemwire.TokenOTLPEnvelope] {
		t.Errorf("otlp and otlp_envelope must be byte-identical:\n otlp: %s\nalias: %s",
			bodies[siemwire.TokenOTLP], bodies[siemwire.TokenOTLPEnvelope])
	}
}

func TestUnknownFormatRejectedNamingTheSet(t *testing.T) {
	o := New()
	err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"path": t.TempDir() + "/x.log", "format": "protobuf",
	}})
	if err == nil || !strings.Contains(err.Error(), formatSet().List()) {
		t.Fatalf("want a rejection naming the accepted set, got: %v", err)
	}
}

func TestCorruptedStoredFormatErrorsInsteadOfJSONRelabel(t *testing.T) {
	o, _ := openFile(t, "cef")
	defer o.Close(context.Background())
	o.format = "corrupted"
	if _, err := o.encode(sampleNotification()); err == nil {
		t.Fatal("an unrecognized stored format must be an error, never a silent JSON relabel")
	}
}
