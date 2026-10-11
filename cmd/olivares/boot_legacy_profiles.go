// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/residency"
)

const (
	// legacyProfileNamingTimeout bounds one pass of the naming, wait for leadership
	// included. What it did not finish is tried again at the next promotion.
	legacyProfileNamingTimeout = 2 * time.Minute
	// legacyProfileLeaderPoll is how often the naming checks that this node is the writer.
	legacyProfileLeaderPoll = 100 * time.Millisecond
)

// legacyProfileAdopter names the provider profiles that predate account names.
type legacyProfileAdopter interface {
	AdoptUnnamedProfiles(context.Context, model.TenantID) (int, error)
}

// startLegacyProfileAdoption gives the unnamed provider profiles of every tenant
// this region serves a generated account name. It runs in its own goroutine and
// returns at once, so a promotion never waits for it; the channel it returns
// closes when the pass ends. ctx is the engine's lifetime context, and the pass is
// also bounded by legacyProfileNamingTimeout.
//
// It starts only once isLeader is true: the promotion hook runs before leadership
// is visible, and a write made before then fails closed and would not be retried
// until the next promotion. It is best effort by design: the profiles work without
// a name, and the adoption is idempotent, so what could not be named now is named
// at the next promotion.
func startLegacyProfileAdoption(
	ctx context.Context,
	listOrgs sessionOrgLister,
	residencyReg *residency.Registry,
	adopter legacyProfileAdopter,
	isLeader func() bool,
	log *slog.Logger,
) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		ctx, cancel := context.WithTimeout(ctx, legacyProfileNamingTimeout)
		defer cancel()
		if !waitForLeadership(ctx, isLeader, legacyProfileLeaderPoll) {
			return
		}
		adoptLegacyProfiles(ctx, listOrgs, residencyReg, adopter, log)
	}()
	return done
}

// waitForLeadership polls ready until it is true (true) or ctx ends (false).
func waitForLeadership(ctx context.Context, ready func() bool, every time.Duration) bool {
	tick := time.NewTicker(every)
	defer tick.Stop()
	for !ready() {
		select {
		case <-ctx.Done():
			return false
		case <-tick.C:
		}
	}
	return true
}

func adoptLegacyProfiles(
	ctx context.Context,
	listOrgs sessionOrgLister,
	residencyReg *residency.Registry,
	adopter legacyProfileAdopter,
	log *slog.Logger,
) {
	orgs, err := listOrgs(ctx)
	if err != nil {
		log.Warn("sessions: could not list tenants to name their provider profiles; they keep working unnamed", "error", err)
		return
	}
	for _, org := range orgs {
		if ctx.Err() != nil {
			return
		}
		tenant, parseErr := model.ParseTenantID(org.ID.String())
		if parseErr != nil || tenant.IsZero() || tenant.IsSystem() {
			continue
		}
		if residencyReg != nil && !residencyReg.Serves(org.DataRegion) {
			continue
		}
		named, err := adopter.AdoptUnnamedProfiles(ctx, tenant)
		if err != nil {
			log.Warn("sessions: some provider profiles could not be named; they keep working unnamed",
				"tenant", tenant.String(), "named", named, "error", err)
		} else if named > 0 {
			log.Info("sessions: named existing provider profiles", "tenant", tenant.String(), "named", named)
		}
	}
}
