// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package model_test

import (
	"fmt"

	"github.com/olivaresai/olivares/sdk/model"
)

// ExampleEdgeObservation builds the fact a source emits when it sees an origin
// (an agent, identity or session) touch a resource — the spine of the R/RW access
// map. It carries identifiers and the access classification only, never payloads,
// SQL bodies, secrets or PII.
func ExampleEdgeObservation() {
	obs := model.EdgeObservation{
		OriginKind:   "agent",
		OriginRef:    "billing-agent",
		ResourceKind: "postgres.table",
		ResourceRef:  "public.customers",
		Mode:         model.ModeReadWrite,
		Source:       model.SignalPGAudit,
		Confidence:   model.ConfidenceAttributed,
	}
	fmt.Println(obs.ObservationType(), obs.Mode, obs.Mode.Valid())
	// Output: edge readwrite true
}
