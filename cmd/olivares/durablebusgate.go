// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"github.com/olivaresai/olivares/core/eventbus"
)

// durablebusgate.go is the AGPL composition-root glue for the OPTIONAL commercial
// HA durable event-bus backend (enterprise/durablebus — Fase 1 of the
// scale/reliability lever). It defines the build-neutral seam boot depends on so
// the bus selection in boot.go is identical in both editions; the factory itself
// is build-specific:
//
//   - wire_noenterprise.go (default AGPL build): newDurableBus is INERT — it
//     returns (nil,nil) when unconfigured, so the bus is built exactly as before
//     (in-proc default, or the open Core-NATS bridge when OLIVARES_BUS_CONFIG is
//     set); it returns an ERROR when OLIVARES_DURABLE_BUS_CONFIG is set, because a
//     durable backend the community binary cannot honor must FAIL the boot, never
//     silently run non-durable (docs/SECURITY-HARDENING.md — never a silent gap).
//   - cmd-overlay/olivares/durablebus_enterprise.go (-tags enterprise, private
//     repo): newDurableBus builds the real durable JetStream bus when licensed, or
//     falls back to the open Core-NATS bridge (warned) when unlicensed.
//
// The default artifact never references enterprise/durablebus (no rug-pull): the
// open in-proc bus and the open Core-NATS bridge are unchanged and remain the only
// backends the community edition can run.

// envDurableBusConfig points at the durable-backend JSON config file (a superset
// of natsbus.Config + the JetStream stream/dedup settings). It is mutually
// exclusive with envBusConfig (OLIVARES_BUS_CONFIG): the durable config already
// carries the NATS connection details, so setting both is an ambiguous-intent
// boot error.
const envDurableBusConfig = "OLIVARES_DURABLE_BUS_CONFIG"

// injectGatedBus is a Bus that accepts the late-bound HA leader predicate
// (store.Leader().Active). Both the open *natsbus.Bus and the enterprise durable
// bus satisfy it; the in-proc default does not (single-node, nothing to gate).
// boot installs the gate once, after leadership is resolved — for the durable bus
// that same call also arms its leader-gated JetStream consumer lifecycle.
type injectGatedBus interface {
	eventbus.Bus
	SetInjectGate(func() bool)
}

// newDurableBus is declared per build tag (wire_noenterprise.go returns inert;
// the enterprise overlay returns the real backend). It is given the same payload
// decoders and not-leader demotion the open bridge uses, so a durable deployment
// re-materializes module-owned payloads (e.g. voice.Telemetry) identically and an
// HA standby's expected ErrNotLeader does not drown the logs.
//
// Return contract:
//   - (nil, nil)        — not configured for a durable backend; boot falls through
//     to the existing OLIVARES_BUS_CONFIG / in-proc selection unchanged.
//   - (bus, nil)        — the durable bus (enterprise+licensed) OR the open
//     Core-NATS bridge built from the durable config's connection fields
//     (enterprise+unlicensed fallback, warned). Either way it is HA-gateable.
//   - (nil, err)        — configured but cannot be honored (community build, or
//     an invalid/unreadable config): boot FAILS CLOSED, never degrades silently.
//
// The signature is fixed by its two build-tagged declarations:
//
//	func newDurableBus(getenv func(string) string, decoders map[event.Type]natsbus.PayloadDecoder,
//		demote func(error) bool, log *slog.Logger, licenseFile, dataDir string) (injectGatedBus, error)
//
// licenseFile/dataDir let the enterprise overlay resolve+verify the commercial license
// ONCE at boot (reusing resolveLicense + license.Verify): with no valid license the
// durable backend declines to activate and the overlay falls back to the open
// Core-NATS bridge (warned). Unlike the hot-applied add-on entitlements, the bus's gate
// is a boot-time backend selection — installing a license to activate durability
// requires a restart. The community stub ignores both (it never reads a license).
