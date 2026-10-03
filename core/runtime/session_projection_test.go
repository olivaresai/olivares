// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/eventbus/natsbus"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	"github.com/olivaresai/olivares/sdk/model"
)

func TestSessionProjectionRequiresRegisteredSessionsHost(t *testing.T) {
	r, b := budgetRuntime(t)
	owner, other := &budgetModule{name: "olivares.sessions"}, &budgetModule{name: "test.foreign"}
	for _, m := range []*budgetModule{owner, other} {
		if err := r.AddModule(m, sdk.Config{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	e := event.FromObservation("tenant-a", "olivares.sessions", model.EdgeObservation{OriginKind: "session", OriginRef: "canonical", ResourceKind: "file", ResourceRef: "source.txt"})
	e.SessionProjection = true
	if err := other.host.Publish(t.Context(), e); !errors.Is(err, ErrReservedSessionProjection) || len(b.snapshot()) != 0 {
		t.Fatalf("foreign marker admitted: %v", err)
	}
	e.Source = "attacker-label"
	if err := owner.host.Publish(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	got := b.snapshot()
	if len(got) != 1 || !got[0].SessionProjection || got[0].Source != "olivares.sessions" {
		t.Fatalf("owning host did not stamp its publication: %+v", got)
	}
	for _, payload := range []any{(*model.EdgeObservation)(nil), "not an observation"} {
		bad := e
		bad.Payload = payload
		if err := owner.host.Publish(t.Context(), bad); !errors.Is(err, ErrReservedSessionProjection) || len(b.snapshot()) != 1 {
			t.Fatal("invalid projected payload admitted")
		}
	}
}

func TestCollectorIngestCannotCarryManagedSessionProjection(t *testing.T) {
	r, b := budgetRuntime(t)
	if err := r.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Even a transported engine envelope cannot propose its marker through
	// the collector's Observation boundary. Only the internal bus preserves it.
	e := event.FromObservation("tenant-a", "olivares.sessions", model.EdgeObservation{OriginKind: "session", OriginRef: "canonical", ResourceKind: "file", ResourceRef: "source.txt", Labels: map[string]string{"SessionProjection": "true", "session_projection": "true"}})
	e.SessionProjection = true
	encoded, err := natsbus.EncodeEvent(e)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := natsbus.DecodeEvent(encoded, natsbus.DefaultDecoders())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Ingest(context.Background(), decoded.Tenant, decoded.Source, decoded.Payload.(model.Observation)); err != nil {
		t.Fatal(err)
	}
	got := b.snapshot()
	if len(got) != 1 || got[0].SessionProjection {
		t.Fatal("collector copied the engine's projection marker")
	}
	// The ordinary registered-source Sink also builds a fresh envelope; labels
	// and an owner-looking source name never stamp managed authority.
	sink := busSink{bus: b, tenant: "tenant-a", source: "olivares.sessions", registration: &event.SourceRegistration{SourceID: "collector", SourceRevision: 1, EnvironmentRef: "host"}}
	if err := sink.Emit(t.Context(), decoded.Payload.(model.Observation)); err != nil {
		t.Fatal(err)
	}
	got = b.snapshot()
	if len(got) != 2 || got[1].SessionProjection || got[1].SourceRegistration == nil {
		t.Fatal("registered collector stamped managed projection")
	}
}
