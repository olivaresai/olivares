// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package main

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/eventbus/natsbus"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessioncockpit"
	"github.com/olivaresai/olivares/sdk/event"
)

// editionPortsForBuild is the Community edition: the zero value of editionPorts plus
// the behaviors Community really has. This file imports nothing from enterprise/, so
// the default artifact never links commercial code; the Business overlay fills the
// same value from its own constructors (edition_ports.go).
func editionPortsForBuild() editionPorts {
	return editionPorts{
		name: "community",
		// Since B10 self-hosted has no user cap in any tier: the policy reports
		// unlimited active accounts and never reads a license or the CRL.
		seatPolicy: func(*licenseHolder, crlViewFunc) auth.SeatPolicy {
			return auth.NewCommunitySeatPolicy()
		},
		// Every MCP upstream uses the operator-configured credential.
		upstreamCredentialProvider: func(staticAuth string) UpstreamCredentialProvider {
			return &staticCredentialProvider{authHeader: staticAuth}
		},
		durableBus:                 communityDurableBus,
		moduleRegistrars:           communityModuleRegistrars,
		circuitBreakerDeclarations: communityCircuitBreakerDeclarations,
	}
}

// communityDurableBus refuses a configured durable event bus. Running the
// cluster non-durably while the operator believes enforcement events are durable
// would be a silent gap (docs/SECURITY-HARDENING.md), so a set OLIVARES_DURABLE_BUS_CONFIG fails the
// boot with an honest error; unset returns (nil, nil) and boot keeps the open
// in-proc / Core-NATS bus.
func communityDurableBus(getenv func(string) string, _ map[event.Type]natsbus.PayloadDecoder, _ func(error) bool, _ *slog.Logger, _, _ string) (injectGatedBus, error) {
	if strings.TrimSpace(getenv(envDurableBusConfig)) == "" {
		return nil, nil
	}
	return nil, fmt.Errorf("%s is set but this is the community edition: the HA durable event-bus "+
		"(at-least-once + dedup over NATS JetStream) is a Business capability (Identity & Scale) — run the Business "+
		"edition, or unset %s to use the open in-proc / Core-NATS bridge backend", envDurableBusConfig, envDurableBusConfig)
}

// communityModuleRegistrars registers the AGPL availability descriptor. It declares
// one read permission and no routes: /v1/m/session-cockpit/availability answers 404
// by absence. Exactly one module owns the namespace.
func communityModuleRegistrars(EditionConfig) []api.Module {
	return []api.Module{sessioncockpit.NewPlaceholder()}
}

// communityCircuitBreakerDeclarations declares the breaker rule table's text columns.
// The table is registered in every edition, but this artifact links neither the engine
// that writes rules nor any reader of their configuration: the gate reads only the
// breaker state. The Business build declares these columns from its engine.
func communityCircuitBreakerDeclarations() circuitBreakerRuleDecls {
	return circuitBreakerRuleDecls{
		Config: model.None("breaker configuration no reader in this edition reads; the gate reads only the breaker state: cmd/olivares/internal/inferencepep/circuitbreakergate.go:37, cmd/olivares/internal/inferencepep/circuitbreakergate.go:18"),
		Tier:   model.None("an agent risk tier from the tier vocabulary: modules/governance/agentrisk.go:526"),
		// No writer of this column is in this edition, so its spelling of the
		// author is not fixed here: it is matched against every alias.
		Author: model.Scan(model.ClassEvidence),
	}
}
