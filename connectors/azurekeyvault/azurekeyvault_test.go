// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package azurekeyvault_test

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/connectors/azurekeyvault"
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

func open(t *testing.T) *azurekeyvault.Source {
	t.Helper()
	s := azurekeyvault.New()
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
	// SecretGet(read) + KeySign(read, crypto-use) + KeyCreate(write) = 3.
	// Authentication names no object and is skipped.
	if len(sk.edges) != 3 {
		t.Fatalf("got %d edges, want 3: %+v", len(sk.edges), sk.edges)
	}
	var reads, writes int
	sawHSMCaller := false
	for _, e := range sk.edges {
		if e.Source != model.SignalSource("azure_diagnostic") {
			t.Errorf("source = %q", e.Source)
		}
		if e.OriginRef == "" {
			t.Errorf("no caller: %+v", e)
		}
		// The Managed HSM record encodes identity as a stringified JSON blob; its
		// appid must still be extracted.
		if e.OriginRef == "99999999-8888-7777-6666-555555555555" {
			sawHSMCaller = true
		}
		switch e.Mode {
		case model.ModeRead:
			reads++
		case model.ModeWrite:
			writes++
		}
	}
	if reads != 2 || writes != 1 {
		t.Errorf("reads=%d writes=%d, want 2/1", reads, writes)
	}
	if !sawHSMCaller {
		t.Error("Managed HSM stringified identity blob was not parsed to its appid")
	}
}
