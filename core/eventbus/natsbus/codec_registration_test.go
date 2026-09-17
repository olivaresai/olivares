// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package natsbus

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/olivaresai/olivares/sdk/event"
	"github.com/olivaresai/olivares/sdk/model"
	pb "github.com/olivaresai/olivares/sdk/plugin/genpb/olivaresv1"
)

// B1: the internal bridge preserves a snapshot an authorized node stamped whole,
// decodes an absent one to nil, and never promotes a partial one.
func TestCodecSourceRegistrationRoundtrip(t *testing.T) {
	at := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)
	reg := &event.SourceRegistration{SourceID: "row-a", SourceRevision: 3, EnvironmentRef: "env-1", BindingRef: "binding-approved-before-enqueue"}
	in := event.Event{
		ID: "id-1", Type: event.TypeEdgeObserved, Tenant: "tn-1", Source: "grok-home-a", Time: at,
		SourceRegistration: reg,
		Payload: model.EdgeObservation{
			OriginKind: "session", OriginRef: "ext-1", ResourceRef: "public.t", Mode: model.ModeRead, ObservedAt: at,
		},
	}
	data, err := EncodeEvent(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := DecodeEvent(data, DefaultDecoders())
	if err != nil {
		t.Fatal(err)
	}
	if out.SourceRegistration == nil || *out.SourceRegistration != *reg {
		t.Fatalf("registration did not roundtrip: %+v", out.SourceRegistration)
	}
	if out.SourceRegistration == reg {
		t.Fatal("decoder must produce its own value, not alias the encoder's")
	}

	// Absent stays absent.
	in.SourceRegistration = nil
	data, err = EncodeEvent(in)
	if err != nil {
		t.Fatal(err)
	}
	if out, err = DecodeEvent(data, DefaultDecoders()); err != nil || out.SourceRegistration != nil {
		t.Fatalf("absent registration decoded to %+v (%v)", out.SourceRegistration, err)
	}

	// A PARTIAL snapshot on the wire (an old or foreign frame) decodes to nil: the
	// bridge preserves, it never completes.
	var pe pb.Event
	if err := proto.Unmarshal(data, &pe); err != nil {
		t.Fatal(err)
	}
	pe.SourceRegistration = &pb.SourceRegistration{SourceId: "row-a"}
	partial, err := proto.Marshal(&pe)
	if err != nil {
		t.Fatal(err)
	}
	if out, err = DecodeEvent(partial, DefaultDecoders()); err != nil || out.SourceRegistration != nil {
		t.Fatalf("partial registration decoded to %+v (%v), want nil", out.SourceRegistration, err)
	}
}
