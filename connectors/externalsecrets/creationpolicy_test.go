// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package externalsecrets_test

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/connectors/externalsecrets"
	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

type cpSink struct{ edges []model.EdgeObservation }

func (c *cpSink) Emit(_ context.Context, obs model.Observation) error {
	if e, ok := obs.(model.EdgeObservation); ok {
		c.edges = append(c.edges, e)
	}
	return nil
}

// TestCreationPolicyNoneEmitsNoEdge proves an ExternalSecret with
// creationPolicy=None (ESO does not provision the Secret) emits NO provisioning
// edge — while its SecretStore still appears in the inventory.
func TestCreationPolicyNoneEmitsNoEdge(t *testing.T) {
	s := externalsecrets.New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"path": "testdata/none.yaml"}}); err != nil {
		t.Fatal(err)
	}
	sink := &cpSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.edges) != 0 {
		t.Fatalf("creationPolicy=None must emit no edge, got %d: %+v", len(sink.edges), sink.edges)
	}
	// The store is still inventoried.
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, id := range g.Identities {
		if id.Kind == identitysource.KindSecretStore {
			found = true
		}
	}
	if !found {
		t.Fatal("the SecretStore should still appear in the inventory")
	}
}
