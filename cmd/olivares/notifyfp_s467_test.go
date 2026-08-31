// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Item 5: ConnectorFingerprint must describe the LIVE connector actually
// behind a name, deny-close (ok=false) an unknown/never-opened one, and track the
// RESOLVED effective config — not a stale boot spec or a bare secret reference.
func TestConnectorFingerprintLiveAndDenyClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	defer srv.Close()

	specs := []notifyDestinationSpec{
		{Name: "good", Kind: "webhook", Config: map[string]string{"url": srv.URL}},
		{Name: "bad", Kind: "webhook", Config: map[string]string{}}, // no url ⇒ Open fails ⇒ never opened
	}
	d := newConnectorDispatcher(specs, nil, quietLog())
	d.openAll(context.Background(), nil, quietLog())

	if fp, ok := d.ConnectorFingerprint("good"); !ok || fp == "" {
		t.Fatalf("a live opened connector must have a fingerprint, got fp=%q ok=%v", fp, ok)
	}
	// In d.specs but NEVER opened: it is not a live connector, so DENY-CLOSED —
	// the old fingerprint walked d.specs and would have accepted it.
	if _, ok := d.ConnectorFingerprint("bad"); ok {
		t.Fatal("a spec that never opened must NOT yield a fingerprint (deny-closed)")
	}
	if _, ok := d.ConnectorFingerprint("nonexistent"); ok {
		t.Fatal("an unknown destination must be ok=false (deny-closed)")
	}

	// A SIGHUP-style add that is NOT in the boot specs must still be recognized as
	// LIVE (the old code, walking d.specs, would have returned ok=false).
	d.mu.Lock()
	d.resolvedFP["sighup-added"] = "live-digest"
	d.mu.Unlock()
	if fp, ok := d.ConnectorFingerprint("sighup-added"); !ok || fp != "live-digest" {
		t.Fatalf("a live (SIGHUP-added) connector absent from boot specs must be recognized, got fp=%q ok=%v", fp, ok)
	}

	// The fingerprint tracks the RESOLVED effective config: a different url ⇒ a
	// different fingerprint.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	defer srv2.Close()
	d2 := newConnectorDispatcher([]notifyDestinationSpec{{Name: "good", Kind: "webhook", Config: map[string]string{"url": srv2.URL}}}, nil, quietLog())
	d2.openAll(context.Background(), nil, quietLog())
	fp1, _ := d.ConnectorFingerprint("good")
	fp2, _ := d2.ConnectorFingerprint("good")
	if fp1 == fp2 {
		t.Fatal("a different effective config must change the connector fingerprint")
	}
}
