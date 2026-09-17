// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package plugin_test

import (
	"testing"

	pb "github.com/olivaresai/olivares/sdk/plugin/genpb/olivaresv1"
)

// B1 boundary: the collector push envelope carries an observation, a tenant and
// a source LABEL — and no registration snapshot. The snapshot is stamped by the
// engine node that registered the source (core/runtime), so a remote collector
// cannot supply one however it labels itself. This pins the contract so the field
// is not added to the push envelope without the same host-stamp restriction.
func TestIngestEnvelopeCarriesNoSourceRegistration(t *testing.T) {
	fields := (&pb.IngestEnvelope{}).ProtoReflect().Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		name := string(fields.Get(i).Name())
		if name == "source_registration" || name == "provider_profile_id" || name == "canonical_sid" || name == "run_ref" || name == "launch_id" {
			t.Fatalf("IngestEnvelope must not carry reserved attribution field %q", name)
		}
	}
	if fields.Len() != 3 {
		t.Fatalf("IngestEnvelope has %d fields; tenant, source and observation are the whole contract", fields.Len())
	}
	// And the bus Event DOES carry it, as its own message.
	if (&pb.Event{}).ProtoReflect().Descriptor().Fields().ByName("source_registration") == nil {
		t.Fatal("bus Event lost its source_registration field")
	}
}
