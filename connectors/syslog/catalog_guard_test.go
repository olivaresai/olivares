// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0
package syslog

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/siemwire"
)

func openGuard(t *testing.T, format string) *Output {
	t.Helper()
	o := New()
	cfg := map[string]string{"transport": "tcp", "address": "collector.internal:514"}
	if format != "" {
		cfg["format"] = format
	}
	if err := o.Open(context.Background(), sdk.Config{Settings: cfg}); err != nil {
		t.Fatalf("Open(format=%q): %v", format, err)
	}
	return o
}

// The catalog guard for the deliberate line-oriented subset: every token the
// syslog surface declares is accepted at Open AND renders its own dialect (a
// validator-only check would pass a token routed to the wrong encoder). CEF and
// LEEF ride as the MSG of a spec-correct RFC 5424 frame, so every record is
// frame-parseable; the dialect marker tells them apart inside it.
func TestCatalogSubsetRendersEveryDialect(t *testing.T) {
	markers := map[siemwire.FormatToken]func(string) bool{
		siemwire.TokenSyslog: func(b string) bool {
			return strings.HasPrefix(b, "<") && strings.Contains(b, ">1 ") &&
				!strings.Contains(b, "CEF:0|") && !strings.Contains(b, "LEEF:2.0|")
		},
		siemwire.TokenCEF: func(b string) bool {
			return strings.HasPrefix(b, "<") && strings.Contains(b, "CEF:0|")
		},
		siemwire.TokenLEEF: func(b string) bool {
			return strings.HasPrefix(b, "<") && strings.Contains(b, "LEEF:2.0|")
		},
	}
	set := formatSet()
	if len(markers) != len(set.Tokens()) {
		t.Fatalf("this test pins %d dialects but the subset has %d — pin the new one", len(markers), len(set.Tokens()))
	}
	n := sampleNotification()
	for _, tok := range set.Tokens() {
		o := openGuard(t, string(tok))
		record, err := o.encode(n)
		if err != nil {
			t.Fatalf("format %s accepted at Open but does not render: %v", tok, err)
		}
		if !markers[tok](record) {
			t.Errorf("format %s rendered the wrong dialect: %s", tok, record)
		}
	}
}

// TestEmptyFormatIsTheSurfaceDefault: an unset format resolves to the catalog's
// syslog-surface default (the native RFC 5424 record), byte-identical to asking
// for it by name.
func TestEmptyFormatIsTheSurfaceDefault(t *testing.T) {
	n := sampleNotification()
	def, err := openGuard(t, "").encode(n)
	if err != nil {
		t.Fatalf("default format: %v", err)
	}
	explicit, err := openGuard(t, string(formatSet().Default())).encode(n)
	if err != nil {
		t.Fatalf("explicit default: %v", err)
	}
	if def != explicit {
		t.Errorf("empty format must render the surface default:\n empty: %q\ndefault: %q", def, explicit)
	}
}

// TestUnknownFormatRejectedNamingTheSet: the deny-closed Open error carries the
// operator-facing choice list from the catalog, not a hand-typed one.
func TestUnknownFormatRejectedNamingTheSet(t *testing.T) {
	o := New()
	err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"transport": "tcp", "address": "collector.internal:514", "format": "otlp",
	}})
	if err == nil || !strings.Contains(err.Error(), formatSet().List()) {
		t.Fatalf("want a rejection naming the accepted set %q, got: %v", formatSet().List(), err)
	}
}

// TestCorruptedStateErrors: a format outside the subset in the struct (state no
// Open can produce) errors instead of silently relabelling as native syslog —
// the old default-case fallback would have emitted the wrong dialect.
func TestCorruptedStateErrors(t *testing.T) {
	o := openGuard(t, "cef")
	o.format = "corrupted"
	if _, err := o.encode(sampleNotification()); err == nil {
		t.Fatal("corrupted internal format state must error, not fall back to a dialect")
	}
	if err := o.Notify(context.Background(), sampleNotification()); err == nil {
		t.Fatal("Notify must propagate the corrupted-state error before framing or dialing")
	}
}
