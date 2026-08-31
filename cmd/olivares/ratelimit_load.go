// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"log/slog"

	"github.com/olivaresai/olivares/core/api/ratelimit"
)

// loadRateLimitConfig builds the inbound rate-limiter config (OPS-5) for the
// composition root. It is ALWAYS non-nil — production is rate-limited by default
// (secure-by-default; an un-configured deployment is the vulnerable one). With no
// OLIVARES_RATELIMIT_CONFIG it returns the built-in defaults (enforce mode, built-in
// tiers, every tenant on the default tier). Once the operator supplies a path, an
// unreadable file or invalid JSON fails startup instead of silently replacing the
// requested policy with defaults.
//
// The file overlays — it does not wholesale-replace — the built-in tier table, so an
// operator can re-quota one tier (or assign a few tenants) without redefining all.
// Tenant→tier assignment is operator config here (an operator/billing fact a tenant
// must not self-assert); the ratelimit.TierResolver seam leaves room for a future
// store-backed (Org.Settings) resolver without touching the limiter.
func loadRateLimitConfig(getenv func(string) string, log *slog.Logger) (*ratelimit.Config, error) {
	cfg := ratelimit.DefaultConfig()

	path := getenv("OLIVARES_RATELIMIT_CONFIG")
	if path == "" {
		return &cfg, nil
	}
	var f rateLimitFile
	if err := loadOperatorJSONConfig("OLIVARES_RATELIMIT_CONFIG", path, &f); err != nil {
		return nil, err
	}

	if m := ratelimit.Mode(f.Mode); m == ratelimit.ModeEnforce || m == ratelimit.ModeReportOnly || m == ratelimit.ModeOff {
		cfg.Mode = m
	} else if f.Mode != "" {
		log.Warn("ratelimit: unknown mode in OLIVARES_RATELIMIT_CONFIG; keeping enforce", "mode", f.Mode)
	}
	if cfg.Mode == ratelimit.ModeOff {
		log.Warn("ratelimit: limiter is OFF by operator config — inbound requests are NOT rate-limited")
	}
	if f.DefaultTier != "" {
		cfg.DefaultTier = f.DefaultTier
	}
	// Overlay operator tiers onto the built-in table; warn (do not silently floor) on
	// a degenerate limit so a typo is visible — the limiter still clamps it to the
	// hard floor at use, never to "unlimited".
	for name, tl := range f.Tiers {
		warnDegenerate(log, name, tl)
		cfg.Tiers[name] = tl
	}
	applied := 0
	if len(f.TenantTiers) > 0 {
		r := buildTenantResolver(f.TenantTiers, cfg.Tiers, log)
		cfg.Resolver = r
		applied = len(r) // entries that actually resolve (malformed/unknown were skipped)
	}
	log.Info("ratelimit: loaded inbound quotas", "mode", string(cfg.Mode), "default_tier", cfg.DefaultTier,
		"tiers", len(cfg.Tiers), "tenant_overrides", applied)
	return &cfg, nil
}

// rateLimitFile is the operator's JSON shape for OLIVARES_RATELIMIT_CONFIG. Limits
// decode straight into the ratelimit types (which carry the json tags).
type rateLimitFile struct {
	Mode        string                          `json:"mode"`         // enforce|report_only|off
	DefaultTier string                          `json:"default_tier"` // tier for unplaced tenants
	Tiers       map[string]ratelimit.TierLimits `json:"tiers"`        // overlays the built-in table
	TenantTiers map[string]string               `json:"tenant_tiers"` // tenant uuid -> tier name
}

// buildTenantResolver turns the tenant→tier map into a StaticTierResolver, skipping
// (with a warning) any malformed tenant id or unknown tier name so one bad entry
// never breaks the rest.
func buildTenantResolver(raw map[string]string, tiers map[string]ratelimit.TierLimits, log *slog.Logger) ratelimit.StaticTierResolver {
	m := make(ratelimit.StaticTierResolver, len(raw))
	for tenantStr, tier := range raw {
		tenant, present, err := parseBusinessTenant("ratelimit config: tenant_tiers key", tenantStr)
		if err != nil || !present {
			log.Warn("ratelimit: skipping tenant_tiers entry with a malformed tenant id", "tenant", tenantStr)
			continue
		}
		if _, ok := tiers[tier]; !ok {
			log.Warn("ratelimit: tenant assigned to an undefined tier; it will use the default tier", "tenant", tenantStr, "tier", tier)
			continue
		}
		m[tenant] = tier
	}
	return m
}

// warnDegenerate logs a warning for any non-positive rate or sub-unit burst in a
// tier limit. The limiter floors these at use; the warning makes the typo visible.
func warnDegenerate(log *slog.Logger, name string, tl ratelimit.TierLimits) {
	bad := func(what string, l ratelimit.Limit) {
		if l.Rate <= 0 || l.Burst < 1 {
			log.Warn("ratelimit: degenerate limit in tier; it will be clamped to the hard floor",
				"tier", name, "which", what, "rate", l.Rate, "burst", l.Burst)
		}
	}
	for class, l := range tl.PerClass {
		bad(string(class), l)
	}
	bad("total", tl.Total)
}
