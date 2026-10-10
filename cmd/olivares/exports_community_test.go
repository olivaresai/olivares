// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	obstrace "github.com/olivaresai/olivares/core/observability/trace"
)

func TestCommunityPushConnectorsUnavailable(t *testing.T) {
	for _, kind := range []string{"siem", "syslog", "splunkhec", "otlplog", "chronicle", "datadog", "elastic", "snmp", "filelog", "servicenow", "jira", "pagerduty", "opsgenie"} {
		t.Run(kind, func(t *testing.T) {
			if c, err := buildOutputConnector(kind); c != nil || err == nil {
				t.Fatalf("Community offers %q: connector=%T err=%v", kind, c, err)
			}
		})
	}
	for _, kind := range []string{"slack", "teams", "webhook", "email", "twilio"} {
		if c, err := buildOutputConnector(kind); c == nil || err != nil {
			t.Fatalf("generic notifications %q changed: %v", kind, err)
		}
	}
}

func TestCommunityTracingRetainsSettingsWithoutDelivery(t *testing.T) {
	for _, key := range []string{"OLIVARES_OTEL_ENABLED", "OLIVARES_OTEL_ENDPOINT", "OTEL_EXPORTER_OTLP_ENDPOINT"} {
		t.Setenv(key, "")
	}
	eng, settings := bootForSettings(t)
	ctx := context.Background()
	p, err := obstrace.New(ctx, obstrace.FromEnv("test"))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Shutdown(ctx)
	svc := newTracingSettingsService(settings, p, "test")
	choice := obstrace.DefaultSettings()
	choice.Enabled, choice.Endpoint, choice.Protocol, choice.Insecure = true, "http://127.0.0.1:4318", obstrace.ProtocolHTTP, true
	got, err := svc.SaveTracingSettings(ctx, operator(t), choice)
	if err != nil {
		t.Fatal(err)
	}
	if got.Settings != choice || got.Effective.Enabled || p.Enabled() {
		t.Fatal("Community live settings either lose the stored choice or enable delivery")
	}
	if err := svc.applyStored(ctx); err != nil {
		t.Fatal(err)
	}
	got, err = svc.TracingSettings(ctx)
	if err != nil || got.Settings != choice || got.Effective.Enabled {
		t.Fatal("Community restart either loses settings or enables delivery")
	}

	// The normal Community DR commands must carry the retained Business choices.
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}
	priorVersion := version
	version = "1.0"
	t.Cleanup(func() { version = priorVersion })
	passFile := filepath.Join(t.TempDir(), "passphrase")
	if err := os.WriteFile(passFile, []byte("operations settings backup fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "operations.drbundle")
	restoredDir := t.TempDir()
	for _, args := range [][]string{
		{"backup", "--data-dir", eng.dataDir, "--engine", "sqlite", "--out", bundle, "--passphrase-file", passFile},
		{"verify", "--in", bundle, "--passphrase-file", passFile},
		{"restore", "--in", bundle, "--data-dir", restoredDir, "--engine", "sqlite", "--passphrase-file", passFile, "--force"},
	} {
		if out, err := runDR(args...); err != nil {
			t.Fatalf("dr %s: %v\n%s", args[0], err, out)
		}
	}
	restored, err := boot(ctx, bootConfig{DataDir: restoredDir, Engine: "sqlite", Version: version})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restored.Close() })
	doc, exists, err := newProductSettings(restored.store, restoredDir).load(ctx)
	if err != nil || !exists || doc.Tracing == nil || *doc.Tracing != choice {
		t.Fatal("Community backup loses retained telemetry settings")
	}
	if restored.tracer.Enabled() {
		t.Fatal("restored Community settings enabled delivery")
	}
}

func TestCommunityPostureExportUnavailable(t *testing.T) {
	_, h, admin, tenant := bootWithModuleProfile(t, []string{"posture"})
	if code, _, raw := doDemoViewJSON(t, h, "GET", "/v1/m/posture/export", admin, tenant, nil); code != 501 {
		t.Fatalf("posture export = %d, want 501: %s", code, raw)
	}
}

func TestCommunityOperationsCommandsAbsent(t *testing.T) {
	root := newRootCmd()
	for _, args := range [][]string{{"posture", "export"}, {"observability", "traces", "export"}} {
		path := "olivares " + strings.Join(args, " ")
		t.Run(path, func(t *testing.T) {
			found, _, err := root.Find(args)
			if err == nil && found != nil && found.CommandPath() == path {
				t.Fatalf("Community registers paid export command %s", path)
			}
		})
	}
}
