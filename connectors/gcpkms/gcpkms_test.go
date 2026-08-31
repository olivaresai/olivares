// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gcpkms_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/gcpkms"
	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

type sink struct{ edges []model.EdgeObservation }

func (c *sink) Emit(_ context.Context, obs model.Observation) error {
	if e, ok := obs.(model.EdgeObservation); ok {
		c.edges = append(c.edges, e)
	}
	return nil
}

func open(t *testing.T) *gcpkms.Source {
	t.Helper()
	s := gcpkms.New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"path": "testdata/audit.json"}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestGatherEdges(t *testing.T) {
	s := open(t)
	sk := &sink{}
	if err := s.Gather(context.Background(), sk); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	// Decrypt(read) + CreateCryptoKey(write) + AccessSecretVersion(read) +
	// AddSecretVersion(write) = 4. The compute event is skipped.
	if len(sk.edges) != 4 {
		t.Fatalf("got %d edges, want 4: %+v", len(sk.edges), sk.edges)
	}
	var reads, writes, kms, secret int
	for _, e := range sk.edges {
		if e.Source != model.SignalSource("gcp_audit") {
			t.Errorf("source = %q", e.Source)
		}
		if e.Confidence != model.ConfidenceAttributed {
			t.Errorf("confidence = %q", e.Confidence)
		}
		switch e.Mode {
		case model.ModeRead:
			reads++
		case model.ModeWrite:
			writes++
		}
		switch e.ResourceKind {
		case "gcp.kms.key":
			kms++
		case "gcp.secret":
			secret++
		default:
			t.Errorf("kind %q", e.ResourceKind)
		}
	}
	if reads != 2 || writes != 2 || kms != 2 || secret != 2 {
		t.Errorf("reads=%d writes=%d kms=%d secret=%d, want 2/2/2/2", reads, writes, kms, secret)
	}
}

func TestNoSecretValueLeaks(t *testing.T) {
	s := open(t)
	sk := &sink{}
	if err := s.Gather(context.Background(), sk); err != nil {
		t.Fatal(err)
	}
	blob, _ := json.Marshal(sk.edges)
	// The fixture embeds a base64 secret payload; it must never reach an edge.
	if strings.Contains(string(blob), "U1VQRVJTRUNSRVRWQUxVRQ") {
		t.Fatalf("secret payload leaked: %s", blob)
	}
}

func TestSnapshotInventory(t *testing.T) {
	s := open(t)
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	refs := map[string]bool{}
	for _, id := range g.Identities {
		if id.Kind != identitysource.KindSecretStore || id.Type != identitysource.PrincipalNHI {
			t.Errorf("bad store NHI: %+v", id)
		}
		refs[id.Ref] = true
	}
	for _, want := range []string{"gcp.cloudkms:acme-prod", "gcp.secretmanager:acme-prod"} {
		if !refs[want] {
			t.Errorf("missing store %q in %v", want, refs)
		}
	}
}
