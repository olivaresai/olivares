// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package event_test

import (
	"fmt"

	"github.com/olivaresai/olivares/sdk/event"
	"github.com/olivaresai/olivares/sdk/model"
)

// ExampleFromObservation lifts a connector observation onto the bus envelope and
// reads it back type-safely. FromObservation is what the engine uses so the
// Type ⇒ payload invariant holds by construction; a module consumer recovers the
// typed fact with EdgeOf / CostOf / FindingOf.
func ExampleFromObservation() {
	e := event.FromObservation("tenant-1", "example.hello", model.EdgeObservation{
		OriginKind:   "agent",
		OriginRef:    "billing-agent",
		ResourceKind: "file",
		ResourceRef:  "/repo/README.md",
		Mode:         model.ModeRead,
	})

	edge, ok := event.EdgeOf(e)
	fmt.Println(e.Type, ok, edge.ResourceRef)
	// Output: edge.observed true /repo/README.md
}
