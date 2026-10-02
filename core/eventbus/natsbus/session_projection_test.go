// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package natsbus

import (
	"testing"

	"github.com/olivaresai/olivares/sdk/event"
	"github.com/olivaresai/olivares/sdk/model"
)

func TestCodecPreservesManagedSessionProjection(t *testing.T) {
	for _, obs := range []model.Observation{
		model.EdgeObservation{OriginKind: "session", OriginRef: "canonical-sid", ResourceKind: "file", ResourceRef: "source.txt"},
		model.CostSample{SessionRef: "canonical-sid", CostMicroUSD: 17},
		model.FindingReport{SubjectKind: "session", SubjectRef: "canonical-sid", Kind: "session.governed_deny"},
	} {
		for _, projected := range []bool{true, false} {
			in := event.FromObservation("tenant", "olivares.sessions", obs)
			in.SessionProjection = projected
			encoded, err := EncodeEvent(in)
			if err != nil {
				t.Fatal(err)
			}
			out, err := DecodeEvent(encoded, DefaultDecoders())
			if err != nil {
				t.Fatal(err)
			}
			if out.SessionProjection != projected || out.Source != in.Source {
				t.Fatalf("projection envelope lost: %+v", out)
			}
			if c, ok := event.CostOf(out); ok && c.SessionRef != "canonical-sid" {
				t.Fatal("FinOps canonical cost join changed")
			}
		}
	}
}
