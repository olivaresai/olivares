// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	obstrace "github.com/olivaresai/olivares/core/observability/trace"
	"github.com/olivaresai/olivares/core/store"
)

func TestTracingSettingsPersistApplyAndPreserveDeployment(t *testing.T) {
	for _, key := range []string{"OLIVARES_OTEL_ENABLED", "OLIVARES_OTEL_ENDPOINT", "OTEL_EXPORTER_OTLP_ENDPOINT", "OLIVARES_OTEL_SAMPLE_RATIO"} {
		t.Setenv(key, "")
	}
	_, settings := bootForSettings(t)
	ctx := context.Background()
	p, err := obstrace.New(ctx, obstrace.FromEnv("test"))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Shutdown(ctx)
	svc := newTracingSettingsService(settings, p, "test")
	initial, err := svc.TracingSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if initial.Effective.Enabled || initial.Settings.SampleRatio != 1 {
		t.Fatal("fresh tracing defaults changed")
	}
	if err := settings.writeModules(ctx, operator(t), "test.modules", []string{"eventing"}); err != nil {
		t.Fatal(err)
	}
	choice := obstrace.DefaultSettings()
	choice.Endpoint = "https://collector.example:4318"
	choice.Protocol = obstrace.ProtocolHTTP
	choice.SampleRatio = 0.25
	saved, err := svc.SaveTracingSettings(ctx, operator(t), choice)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Settings != choice || saved.Effective.Enabled {
		t.Fatal("saved OFF choice was not retained")
	}
	// A second service represents the next engine start, using the same durable product record.
	restarted, err := obstrace.New(ctx, obstrace.FromEnv("test"))
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Shutdown(ctx)
	again := newTracingSettingsService(settings, restarted, "test")
	if err := again.applyStored(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := again.TracingSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Settings != choice {
		t.Fatal("choice did not survive a new service")
	}
	doc, _, err := settings.load(ctx)
	if err != nil || doc.Modules == nil || len(doc.Modules.Selected) != 1 || doc.Modules.Selected[0] != "eventing" {
		t.Fatal("tracing replaced module state")
	}
	bad := choice
	bad.Endpoint = "https://user:secret@collector.example"
	bad.Enabled = true
	if _, err := again.SaveTracingSettings(ctx, operator(t), bad); err == nil {
		t.Fatal("credential URL accepted")
	}
	after, _ := again.TracingSettings(ctx)
	if after.Settings != choice {
		t.Fatal("invalid choice changed saved settings")
	}

	// A shared record may change on another node before this provider applies it.
	other := choice
	other.Endpoint = "https://other-collector.example:4318"
	if err := settings.update(ctx, operator(t), "deployment.settings.tracing", nil, func(doc *productSettingsDoc) { doc.Tracing = &other }); err != nil {
		t.Fatal(err)
	}
	observed, err := again.TracingSettings(ctx)
	if err != nil || observed.Settings != other || observed.Effective.Endpoint != choice.Endpoint {
		t.Fatal("effective settings must describe this provider, not an unapplied shared record")
	}
	t.Setenv("OLIVARES_OTEL_SAMPLE_RATIO", "0.75")
	if check := doctorTracingOverridesCheck(); check.Status != "warn" || !strings.Contains(check.Detail, "OLIVARES_OTEL_SAMPLE_RATIO") || strings.Contains(check.Detail, "0.75") {
		t.Fatal("doctor must name the override without exposing its value")
	}
	// Environment inputs are read on the next start, as on a real process.
	if err := again.applyStored(ctx); err != nil {
		t.Fatal(err)
	}
	overridden, err := again.TracingSettings(ctx)
	if err != nil || overridden.Effective.SampleRatio != 0.75 || overridden.Settings.SampleRatio != 0.25 || len(overridden.Overrides) != 1 {
		t.Fatal("published override lost or concealed")
	}
	t.Setenv("OLIVARES_OTEL_ENDPOINT", "https://legacy:secret@collector.example")
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "authorization=synthetic-secret")
	if visible := obstrace.VisibleSettings(obstrace.FromEnv("test")); visible.Endpoint != "<redacted>" {
		t.Fatal("legacy endpoint credentials must be hidden")
	}
	if check := doctorTracingOverridesCheck(); strings.Contains(check.Detail, "secret") || !strings.Contains(check.Detail, "OTEL_EXPORTER_OTLP_HEADERS") {
		t.Fatal("doctor exposed an override credential or hid its presence")
	}

}

func TestTracingSettingsRequirePersistedAudit(t *testing.T) {
	for _, key := range []string{"OLIVARES_OTEL_ENABLED", "OLIVARES_OTEL_ENDPOINT", "OTEL_EXPORTER_OTLP_ENDPOINT", "OLIVARES_OTEL_SAMPLE_RATIO"} {
		t.Setenv(key, "")
	}
	for _, mode := range []store.AuditSpoolMode{"", store.AuditSpoolDegrade, store.AuditSpoolBlock} {
		name := string(mode)
		if name == "" {
			name = "persisted"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			cfg := store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(dir, "settings.db")}
			st, err := coreengine.Open(ctx, cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.System(ctx, func(sys store.SystemScope) error { _, err := sys.EnsureSystemTenant(ctx); return err }); err != nil {
				t.Fatal(err)
			}
			p, err := obstrace.New(ctx, obstrace.FromEnv("test"))
			if err != nil {
				t.Fatal(err)
			}
			defer p.Shutdown(ctx)
			settings := newProductSettings(st, dir)
			svc := newTracingSettingsService(settings, p, "test")
			old := obstrace.DefaultSettings()
			old.Endpoint = "https://old-collector.example:4318"
			old.Protocol = obstrace.ProtocolHTTP
			if _, err := svc.SaveTracingSettings(ctx, operator(t), old); err != nil {
				t.Fatal(err)
			}
			before, _, err := settings.load(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			if mode != "" {
				cfg.AuditSpoolMaxBytes = 1
				cfg.AuditSpoolOnFull = mode
			}
			st, err = coreengine.Open(ctx, cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			settings.st = st
			collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
			defer collector.Close()
			choice := old
			choice.Endpoint = collector.URL
			choice.Insecure = true
			choice.Enabled = true
			_, saveErr := svc.SaveTracingSettings(ctx, operator(t), choice)
			after, _, err := settings.load(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "" {
				if saveErr != nil || after.Tracing == nil || *after.Tracing != choice || p.Enabled() != (thisEdition.name == "enterprise") {
					t.Fatalf("audited mutation did not apply: %v", saveErr)
				}
			} else {
				if !errors.Is(saveErr, api.ErrTracingUnavailable) {
					t.Errorf("unaudited mutation = %v, want unavailable", saveErr)
				}
				if !reflect.DeepEqual(before, after) || p.Settings() != old {
					t.Error("failed audit changed the document or active provider")
				}
				if saveErr != nil && strings.Contains(saveErr.Error(), collector.URL) {
					t.Error("collector disclosed in failure")
				}
			}
			count := 0
			if err := st.AuthView(ctx, func(as store.AuthScope) error {
				return as.Audit().Walk(ctx, 0, func(ev model.AuditEvent) error {
					if ev.Action == "deployment.settings.tracing" {
						if ev.Seq <= 0 {
							t.Error("nonpositive audit sequence")
						}
						count++
					}
					return nil
				})
			}); err != nil {
				t.Fatal(err)
			}
			want := 1
			if mode == "" {
				want = 2
			}
			if count != want {
				t.Fatalf("persisted tracing events = %d, want %d", count, want)
			}
			if mode == store.AuditSpoolDegrade {
				if err := settings.writeModules(ctx, operator(t), "test.modules", []string{"eventing"}); err != nil {
					t.Fatalf("unrelated settings audit policy changed: %v", err)
				}
			}
		})
	}
}
