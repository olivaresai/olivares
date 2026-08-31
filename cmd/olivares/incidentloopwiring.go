// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"log/slog"

	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/sdk/event"
)

// incidentloopwiring.go is the AGPL composition-root glue for the OPTIONAL
// commercial ITSM/ChatOps governance close-loop (enterprise/incidentloop). It
// defines the seams the default build keeps INERT and the enterprise build fills:
//
//   - enterpriseOutputConnector(kind) — lets buildOutputConnector resolve the
//     "teamsbot" destination kind (the registered-bot Action.Execute Teams
//     connector) under -tags enterprise; the default build returns (nil,false) so
//     "teamsbot" is simply an unknown kind (no rug-pull — the Apache connectors/teams
//     Workflows destination is unchanged).
//   - newIncidentCloseLoop(...) — builds the governance→incident close-loop
//     subscriber from OLIVARES_INCIDENTLOOP_CONFIG under -tags enterprise; the default
//     build returns nil.
//
// Both constructors are declared in wire_enterprise.go (real) and
// wire_noenterprise.go (nil), so the default artifact never references the closed
// module. This file holds only the build-independent seam: the interface the
// composition root depends on, and the bus subscription that binds it.

// incidentCloseLoop is the narrow seam the composition root subscribes to the bus.
// *enterprise/incidentloop.CloseLoop satisfies it under -tags enterprise; the
// default build supplies nil. OnFinding matches event.Handler, so it binds directly
// to bus.Subscribe.
type incidentCloseLoop interface {
	OnFinding(ctx context.Context, e event.Event) error
}

// subscribeIncidentCloseLoop wires the enterprise close-loop to the bus, listening
// for finding.reported so governance lifecycle findings drive the correlated
// incident. It is opt-in and nil-safe: with no enterprise build or no config the
// close-loop is nil and this is a no-op. A subscribe error leaves the close-loop
// inactive — never a boot failure — and the governance finding still stands on the
// bus for the passive notify sinks.
func subscribeIncidentCloseLoop(ctx context.Context, getenv func(string) string, bus eventbus.Bus, log *slog.Logger) {
	cl := newIncidentCloseLoop(ctx, getenv, log)
	if cl == nil {
		return
	}
	if _, err := subscribeClassed(bus, eventbus.ClassNotify, "incident-close-loop",
		[]event.Type{event.TypeFindingReported}, cl.OnFinding); err != nil {
		log.Warn("incident close-loop: bus subscription failed; close-loop inactive", "err", err)
	}
}
