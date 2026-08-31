// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/store"
)

func quietSlog() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.NewFile(0, os.DevNull), &slog.HandlerOptions{Level: slog.LevelError + 4}))
}

func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// TestLoadBusConfig pins the fail-boot-closed loader family contract: unset =
// in-proc default (nil, nil); ANY error — missing file, bad JSON, unknown key,
// invalid backend/prefix — is an error the boot must abort on, never a silent
// in-proc fallback (a partitioned node).
func TestLoadBusConfig(t *testing.T) {
	log := quietSlog()

	if cfg, err := loadBusConfig(envMap(nil), log); err != nil || cfg != nil {
		t.Fatalf("unset env must mean in-proc default: cfg=%v err=%v", cfg, err)
	}

	if _, err := loadBusConfig(envMap(map[string]string{envBusConfig: "/nope/missing.json"}), log); err == nil {
		t.Fatal("missing file must abort, not fall back")
	}

	write := func(content string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "bus.json")
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	for name, content := range map[string]string{
		"bad json":        `{"backend": "nats",`,
		"unknown key":     `{"backend": "nats", "url": "nats://h:4222", "ulr": "typo"}`,
		"unknown backend": `{"backend": "kafka", "url": "k://h"}`,
		"missing url":     `{"backend": "nats"}`,
		"bad prefix":      `{"backend": "nats", "url": "nats://h:4222", "subject_prefix": "a..b"}`,
	} {
		if _, err := loadBusConfig(envMap(map[string]string{envBusConfig: write(content)}), log); err == nil {
			t.Errorf("%s must abort the boot", name)
		}
	}

	p := write(`{"backend": "nats", "url": "nats://h:4222"}`)
	cfg, err := loadBusConfig(envMap(map[string]string{envBusConfig: p}), log)
	if err != nil || cfg == nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if cfg.SubjectPrefix == "" || cfg.Name == "" {
		t.Fatalf("defaults must be applied: %+v", cfg)
	}
}

// TestResolveRateLimitStore pins the backend-selection contract: unset/in-process
// = per-node; postgres requires the postgres engine; anything else aborts.
func TestResolveRateLimitStore(t *testing.T) {
	for _, tc := range []struct {
		val     string
		engine  store.Engine
		want    bool
		wantErr string
	}{
		{"", store.EngineSQLite, false, ""},
		{"", store.EnginePostgres, false, ""},
		{"in-process", store.EnginePostgres, false, ""},
		{"postgres", store.EnginePostgres, true, ""},
		{"Postgres", store.EnginePostgres, true, ""}, // case-insensitive
		{"postgres", store.EngineSQLite, false, "requires --engine postgres"},
		{"redis", store.EnginePostgres, false, "unknown value"},
	} {
		got, err := resolveRateLimitStore(envMap(map[string]string{envRateLimitStore: tc.val}), tc.engine)
		if tc.wantErr == "" {
			if err != nil || got != tc.want {
				t.Errorf("%q/%s: got %v err %v", tc.val, tc.engine, got, err)
			}
		} else if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("%q/%s: want error containing %q, got %v", tc.val, tc.engine, tc.wantErr, err)
		}
	}
}
