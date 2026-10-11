// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestLoadAuditArchiveConfigDefaultsAndParsing(t *testing.T) {
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }
	log := discardLog()

	cfg := loadAuditArchiveConfig(getenv, log)
	if cfg.sink != "" || cfg.interval != defaultAuditArchiveInterval ||
		cfg.segmentEvents != audit.DefaultSegmentEvents || cfg.retainDays != defaultAuditArchiveRetainDays {
		t.Fatalf("defaults = %+v", cfg)
	}

	env[auditArchiveSinkEnv] = "s3archive"
	env[auditArchiveConfigEnv] = "/etc/olivares/archive.json"
	env[auditArchiveIntervalEnv] = "6h"
	env[auditArchiveSegmentEventsEnv] = "500"
	env[auditArchiveRetainDaysEnv] = "3650"
	cfg = loadAuditArchiveConfig(getenv, log)
	if cfg.sink != "s3archive" || cfg.configPath != "/etc/olivares/archive.json" ||
		cfg.interval != 6*time.Hour || cfg.segmentEvents != 500 || cfg.retainDays != 3650 {
		t.Fatalf("explicit env not honored: %+v", cfg)
	}

	// retain_days=0 is a legitimate explicit choice: defer to the bucket's
	// default Object Lock retention (zero RetainUntil per Put).
	env[auditArchiveRetainDaysEnv] = "0"
	if cfg = loadAuditArchiveConfig(getenv, log); cfg.retainDays != 0 {
		t.Fatalf("explicit retain_days=0 must be honored, got %d", cfg.retainDays)
	}

	// Typos keep the defaults (a typo must not silently change retention).
	env[auditArchiveIntervalEnv] = "soon"
	env[auditArchiveSegmentEventsEnv] = "-3"
	env[auditArchiveRetainDaysEnv] = "99999"
	cfg = loadAuditArchiveConfig(getenv, log)
	if cfg.interval != defaultAuditArchiveInterval || cfg.segmentEvents != audit.DefaultSegmentEvents || cfg.retainDays != defaultAuditArchiveRetainDays {
		t.Fatalf("invalid env must keep defaults: %+v", cfg)
	}
}

// countAnchorEvents counts audit.archive.segment events in a tenant's chain.
func countAnchorEvents(t *testing.T, st store.Store, tenant model.TenantID) int {
	t.Helper()
	n := 0
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 1, func(ev model.AuditEvent) error {
			if ev.Action == audit.ActionArchiveSegment {
				n++
			}
			return nil
		})
	}); err != nil {
		t.Fatalf("walk: %v", err)
	}
	return n
}

// headSeq reads a tenant's current chain head sequence.
func headSeq(t *testing.T, st store.Store, tenant model.TenantID) int64 {
	t.Helper()
	var seq int64
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		head, ok, err := sc.Audit().Head(context.Background())
		if ok {
			seq = head.Seq
		}
		return err
	}); err != nil {
		t.Fatalf("head: %v", err)
	}
	return seq
}

// appendChainEvents grows a tenant's chain by n events (the head moves).
func appendChainEvents(t *testing.T, st store.Store, tenant model.TenantID, n int) {
	t.Helper()
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		for i := 0; i < n; i++ {
			if _, err := sc.Audit().Append(context.Background(), model.AuditDraft{
				Actor: "user:x", ActorKind: "user", Action: "agent.update", TargetKind: "core.agent",
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
}

// setOrgSettingKey writes one Org.Settings key read-modify-write (sibling keys
// ride along), simulating hand-edited/corrupt bookkeeping.
func setOrgSettingKey(t *testing.T, st store.Store, tenant model.TenantID, key string, value any) {
	t.Helper()
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		org, err := sc.Org(context.Background())
		if err != nil {
			return err
		}
		settings := org.Settings
		if settings == nil {
			settings = map[string]any{}
		}
		settings[key] = value
		_, err = sc.SetOrgSettings(context.Background(), settings)
		return err
	}); err != nil {
		t.Fatalf("set org setting: %v", err)
	}
}
