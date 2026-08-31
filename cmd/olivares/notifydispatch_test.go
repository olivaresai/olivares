// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/modules/notify"
	"github.com/olivaresai/olivares/sdk"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// TestConnectorDispatcherDeliversViaWebhook proves the XV seam end-to-end: the
// adapter opens a REAL webhook output connector and a notify.Dispatcher
// Deliver call reaches the destination as a non-sensitive JSON notification.
func TestConnectorDispatcherDeliversViaWebhook(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	specs := []notifyDestinationSpec{{Name: "hook", Kind: "webhook", Config: map[string]string{"url": srv.URL}}}
	d := newConnectorDispatcher(specs, nil, slog.Default())
	d.openAll(context.Background(), nil, slog.Default())

	if got := d.Destinations(); len(got) != 1 || got[0] != "hook" {
		t.Fatalf("Destinations() = %v, want [hook]", got)
	}

	n := sdk.Notification{
		Type: "finding.reported", Title: "Agent a1 is DOWN", Body: "health_subject_down",
		Severity: sdkmodel.SeverityHigh, Tenant: "t1",
		Fields: map[string]string{"kind": "health_subject_down", "subject_ref": "a1"},
		Time:   time.Unix(1700000000, 0).UTC(),
	}
	if err := d.Deliver(context.Background(), model.NewTenantID(), "hook", n); err != nil {
		t.Fatalf("Deliver via webhook connector: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatalf("webhook body is not JSON: %s", gotBody)
	}
	if payload["title"] != "Agent a1 is DOWN" {
		t.Errorf("webhook payload title = %v, want %q (body: %s)", payload["title"], n.Title, gotBody)
	}
	if payload["severity"] != "high" {
		t.Errorf("webhook payload severity = %v, want high", payload["severity"])
	}
}

// TestConnectorDispatcherUnknownDestination: an unprovisioned name returns the
// sentinel the module classifies as unknown_destination.
func TestConnectorDispatcherUnknownDestination(t *testing.T) {
	d := newConnectorDispatcher(nil, nil, slog.Default())
	d.openAll(context.Background(), nil, slog.Default())
	if got := d.Destinations(); len(got) != 0 {
		t.Fatalf("empty dispatcher Destinations() = %v, want []", got)
	}
	err := d.Deliver(context.Background(), model.NewTenantID(), "nope", sdk.Notification{})
	if !errors.Is(err, notify.ErrUnknownDestination) {
		t.Fatalf("Deliver to unknown destination = %v, want ErrUnknownDestination", err)
	}
}

// TestConnectorDispatcherSkipsBadSpec: an unknown connector kind and a connector
// that fails Open (webhook with no url) are both skipped, not fatal.
func TestConnectorDispatcherSkipsBadSpec(t *testing.T) {
	specs := []notifyDestinationSpec{
		{Name: "x", Kind: "nope"},
		{Name: "y", Kind: "webhook", Config: map[string]string{}}, // missing required url → Open fails
	}
	d := newConnectorDispatcher(specs, nil, slog.Default())
	d.openAll(context.Background(), nil, slog.Default())
	if got := d.Destinations(); len(got) != 0 {
		t.Fatalf("Destinations() = %v, want [] (both specs invalid)", got)
	}
}

// TestBuildOutputConnectorKinds: every output connector kind builds (the six
// plus the SIEM/log/telemetry egress kinds); an unknown kind errors.
func TestBuildOutputConnectorKinds(t *testing.T) {
	kinds := []string{
		"slack", "teams", "pagerduty", "opsgenie", "webhook", "siem", //
		"syslog", "splunkhec", "otlplog", "chronicle", "datadog", "elastic", "snmp", "filelog", //
		"s3archive", // Object-lock WORM sink
	}
	for _, k := range kinds {
		c, err := buildOutputConnector(k)
		if err != nil {
			t.Errorf("buildOutputConnector(%q) = %v, want a connector", k, err)
			continue
		}
		if c.Descriptor().Type != sdk.TypeOutput {
			t.Errorf("buildOutputConnector(%q) is not an output connector", k)
		}
	}
	if _, err := buildOutputConnector("does-not-exist"); err == nil {
		t.Error("buildOutputConnector(unknown) should error")
	}
}

// TestLoadNotifyDestinations: the optional config file is parsed; an unset env or a
// missing/invalid file yields no specs (never a panic).
func TestLoadNotifyDestinations(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "n.json")
	if err := os.WriteFile(p, []byte(`[{"name":"a","kind":"webhook","config":{"url":"http://x"}}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OLIVARES_NOTIFY_CONFIG", p)
	specs, err := loadNotifyDestinations(slog.Default())
	if err != nil {
		t.Fatalf("load destinations: %v", err)
	}
	if len(specs) != 1 || specs[0].Name != "a" || specs[0].Kind != "webhook" {
		t.Fatalf("loadNotifyDestinations = %v", specs)
	}
	t.Setenv("OLIVARES_NOTIFY_CONFIG", "")
	if got, err := loadNotifyDestinations(slog.Default()); err != nil || got != nil {
		t.Errorf("unset env should yield nil, got %v", got)
	}
}

// TestOutputPluginKindsAreCoherent (E5): the plugin-destination map must stay
// coherent with the rest of the composition — a plugin kind must NOT also build
// in-process (the dispatcher would silently prefer one path), and every *-output
// binary `task build:connectors` embeds must be reachable through the map (an
// embedded-but-unmapped binary is exactly the dead artifact this session removed).
func TestOutputPluginKindsAreCoherent(t *testing.T) {
	for kind := range outputPluginForKind {
		if _, err := buildOutputConnector(kind); err == nil {
			t.Errorf("kind %q is both an out-of-process plugin destination and an in-process output; pick one path", kind)
		}
	}
	// scripts/build-connectors.sh is the canonical list of embedded plugin
	// binaries (E1 — task build:connectors, the Dockerfiles and GoReleaser
	// all run the same script).
	script, err := os.ReadFile("../../scripts/build-connectors.sh")
	if err != nil {
		t.Fatalf("read scripts/build-connectors.sh: %v", err)
	}
	mapped := map[string]bool{}
	for _, bin := range outputPluginForKind {
		mapped[bin] = true
	}
	built := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^build ([a-z0-9-]+-output)\b`).FindAllStringSubmatch(string(script), -1) {
		built[m[1]] = true
		if !mapped[m[1]] {
			t.Errorf("embedded output binary %q has no outputPluginForKind mapping — it would be compiled and shipped but unreachable", m[1])
		}
	}
	if len(built) == 0 {
		t.Fatal("found no `build *-output` lines in scripts/build-connectors.sh (did the canonical list move?)")
	}
	for bin := range mapped {
		if !built[bin] {
			t.Errorf("outputPluginForKind maps to %q but scripts/build-connectors.sh does not build it", bin)
		}
	}
}

// TestConnectorDispatcherPluginKindHonestSkip (E5): a plugin destination
// whose loader is not wired (no runtime / no scratch dir — e.g. a harness that
// never reaches boot) is skipped honestly — it never lands in Destinations() and
// Deliver returns unknown_destination, mirroring the sources path's "not wired"
// posture. (The embedded-binary happy path needs a real broker and is exercised
// by the release smoke, not a unit test; whether the binary is embedded in THIS
// test binary depends on whether `task build:connectors` ran, so asserting on it
// here would flake.)
func TestConnectorDispatcherPluginKindHonestSkip(t *testing.T) {
	specs := []notifyDestinationSpec{{Name: "bus", Kind: "kafka", Config: map[string]string{"brokers": "localhost:9092"}}}

	// No runtime loader at all (rt nil).
	d := newConnectorDispatcher(specs, nil, quietLog())
	d.embedDir = t.TempDir()
	d.openAll(context.Background(), nil, quietLog())
	if got := d.Destinations(); len(got) != 0 {
		t.Fatalf("Destinations() = %v, want [] (no runtime loader)", got)
	}
	if err := d.Deliver(context.Background(), model.NewTenantID(), "bus", sdk.Notification{}); !errors.Is(err, notify.ErrUnknownDestination) {
		t.Fatalf("Deliver to skipped plugin destination = %v, want ErrUnknownDestination", err)
	}

	// Runtime present but no scratch dir for extraction.
	d2 := newConnectorDispatcher(specs, nil, quietLog())
	d2.rt = runtime.New(runtime.Options{Logger: quietLog()})
	d2.openAll(context.Background(), nil, quietLog())
	if got := d2.Destinations(); len(got) != 0 {
		t.Fatalf("Destinations() = %v, want [] (no embed scratch dir)", got)
	}
}
