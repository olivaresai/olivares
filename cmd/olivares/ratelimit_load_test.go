// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/api/ratelimit"
	"github.com/olivaresai/olivares/core/model"
)

func envFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadRateLimitConfigDefaults(t *testing.T) {
	cfg, err := loadRateLimitConfig(envFrom(nil), discardLog())
	if err != nil {
		t.Fatalf("load defaults: %v", err)
	}
	if cfg == nil {
		t.Fatal("config must always be non-nil (secure-by-default)")
	}
	if cfg.Mode != ratelimit.ModeEnforce {
		t.Fatalf("unconfigured mode = %q, want enforce", cfg.Mode)
	}
	if cfg.DefaultTier != ratelimit.TierDefault {
		t.Fatalf("default tier = %q, want %q", cfg.DefaultTier, ratelimit.TierDefault)
	}
	if _, ok := cfg.Tiers[ratelimit.TierDefault]; !ok {
		t.Fatal("built-in default tier must be present")
	}
}

func TestLoadRateLimitConfigFromFile(t *testing.T) {
	tenant := model.TenantID("11111111-1111-1111-1111-111111111111")
	const body = `{
	  "mode": "report_only",
	  "default_tier": "default",
	  "tiers": {
	    "vip": {
	      "per_class": { "read": {"rate":500,"burst":1000}, "write": {"rate":200,"burst":400} },
	      "total": {"rate":600,"burst":1200}
	    }
	  },
	  "tenant_tiers": { "11111111-1111-1111-1111-111111111111": "vip", "not-a-uuid": "vip", "22222222-2222-2222-2222-222222222222": "ghost-tier" }
	}`
	path := filepath.Join(t.TempDir(), "rl.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadRateLimitConfig(envFrom(map[string]string{"OLIVARES_RATELIMIT_CONFIG": path}), discardLog())
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.Mode != ratelimit.ModeReportOnly {
		t.Fatalf("mode = %q, want report_only", cfg.Mode)
	}
	// The operator tier is overlaid; the built-in tiers survive.
	if _, ok := cfg.Tiers["vip"]; !ok {
		t.Fatal("operator tier 'vip' must be present")
	}
	if _, ok := cfg.Tiers[ratelimit.TierDefault]; !ok {
		t.Fatal("overlay must not drop the built-in default tier")
	}
	// The valid tenant maps to vip; the malformed id and the ghost-tier entry are skipped.
	if cfg.Resolver == nil {
		t.Fatal("resolver must be built from tenant_tiers")
	}
	if got := cfg.Resolver.Tier(tenant); got != "vip" {
		t.Fatalf("tenant tier = %q, want vip", got)
	}
	if got := cfg.Resolver.Tier(model.TenantID("22222222-2222-2222-2222-222222222222")); got != "" {
		t.Fatalf("tenant assigned to an undefined tier must be skipped (got %q)", got)
	}
}

func TestLoadRateLimitConfigModesAndDegenerate(t *testing.T) {
	write := func(body string) string {
		p := filepath.Join(t.TempDir(), "rl.json")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	load := func(path string) *ratelimit.Config {
		cfg, err := loadRateLimitConfig(envFrom(map[string]string{"OLIVARES_RATELIMIT_CONFIG": path}), discardLog())
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		return cfg
	}

	if cfg := load(write(`{"mode":"off"}`)); cfg.Mode != ratelimit.ModeOff {
		t.Fatalf("mode=off must load as ModeOff, got %q", cfg.Mode)
	}
	if cfg := load(write(`{"mode":"bogus"}`)); cfg.Mode != ratelimit.ModeEnforce {
		t.Fatalf("unknown mode must keep enforce, got %q", cfg.Mode)
	}
	// A degenerate limit is stored VERBATIM: the loader must NOT pre-floor it (the
	// limiter clamps to the hard floor at USE — that's the documented contract). A
	// future refactor that floored at load would break this and silently change posture.
	cfg := load(write(`{"tiers":{"bad":{"per_class":{"read":{"rate":0,"burst":0}},"total":{"rate":0,"burst":0}}}}`))
	tl, ok := cfg.Tiers["bad"]
	if !ok {
		t.Fatal("degenerate tier should still be loaded (clamped at use, not dropped at load)")
	}
	if rl := tl.PerClass[ratelimit.ClassRead]; rl.Rate != 0 || rl.Burst != 0 {
		t.Fatalf("degenerate limit must be stored verbatim, got %+v", rl)
	}
}

func TestLoadRateLimitConfigBadInputsFailClosed(t *testing.T) {
	// Unreadable path -> startup error.
	if _, err := loadRateLimitConfig(envFrom(map[string]string{"OLIVARES_RATELIMIT_CONFIG": "/nonexistent/rl.json"}), discardLog()); err == nil {
		t.Fatal("missing file must return an error")
	}
	// Invalid JSON -> startup error.
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadRateLimitConfig(envFrom(map[string]string{"OLIVARES_RATELIMIT_CONFIG": path}), discardLog()); err == nil {
		t.Fatal("invalid JSON must return an error")
	}
}
