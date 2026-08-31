// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"strings"

	"github.com/olivaresai/olivares/core/store"
)

// envRateLimitStore selects the rate limiter's bucket backend.
//
//	unset | "in-process" → per-node in-proc shards (the default; single-node
//	                       deployments pay no per-request store round trip)
//	"postgres"           → shared global buckets in the engine's Postgres —
//	                       REQUIRED for HA, where per-node buckets multiply
//	                       every quota by the replica count. The Helm chart
//	                       sets this automatically when replicaCount > 1.
//
// This is a backend SELECTION, so it belongs to the fail-boot-closed family
// (OLIVARES_LEDGER_SIGNER, OLIVARES_BUS_CONFIG), not the warn-and-degrade
// loaders: an HA operator who asked for global buckets and got silent
// per-node enforcement would be running every quota ×N with no error anywhere.
const envRateLimitStore = "OLIVARES_RATELIMIT_STORE"

// resolveRateLimitStore returns whether the shared Postgres store is selected.
func resolveRateLimitStore(getenv func(string) string, engine store.Engine) (bool, error) {
	switch v := strings.ToLower(strings.TrimSpace(getenv(envRateLimitStore))); v {
	case "", "in-process":
		return false, nil
	case "postgres":
		if engine != store.EnginePostgres {
			return false, fmt.Errorf("%s=postgres requires --engine postgres (the shared buckets live in the engine's Postgres)", envRateLimitStore)
		}
		return true, nil
	default:
		return false, fmt.Errorf("%s: unknown value %q (use \"postgres\" or \"in-process\")", envRateLimitStore, v)
	}
}
