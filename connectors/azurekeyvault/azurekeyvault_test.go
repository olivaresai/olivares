// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package azurekeyvault_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/azurekeyvault"
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

// TestNoSecretValueLeaks is the minimal-data negative test (docs/SECURITY-HARDENING.md): a SAS
// token in the object URI's query string and a bearer-token claim in the identity
// blob must NEVER reach any emitted edge field or snapshot identity. The query
// string is stripped (it can carry a SAS credential) and the identity is read
// through a fixed non-secret claim allow-list (appid/oid/upn only).
func TestNoSecretValueLeaks(t *testing.T) {
	s := azurekeyvault.New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"path": "testdata/leak.json"}}); err != nil {
		t.Fatal(err)
	}
	sk := &sink{}
	if err := s.Gather(context.Background(), sk); err != nil {
		t.Fatal(err)
	}
	if len(sk.edges) == 0 {
		t.Fatal("expected an edge")
	}
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	blob, _ := json.Marshal(struct {
		Edges []model.EdgeObservation
		Ids   []identitysource.Identity
	}{sk.edges, g.Identities})
	for _, marker := range []string{"SECRETSASSIGNATURE_MUST_NEVER_LEAK", "SECRETBEARERTOKEN_MUST_NEVER_LEAK"} {
		if strings.Contains(string(blob), marker) {
			t.Fatalf("a credential (%s) leaked into the emitted data: %s", marker, blob)
		}
	}
	// The object is still named (without its SAS query string).
	if !strings.Contains(string(blob), "secrets/db-password/abcdef") {
		t.Fatalf("the object ref was lost entirely: %s", blob)
	}
}

func TestSnapshotInventory(t *testing.T) {
	s := open(t)
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	services := map[string]bool{}
	for _, id := range g.Identities {
		if id.Kind != identitysource.KindSecretStore || id.Type != identitysource.PrincipalNHI {
			t.Errorf("bad store NHI: %+v", id)
		}
		services[id.Attributes["service"]] = true
	}
	if !services["keyvault"] || !services["managedhsm"] {
		t.Errorf("expected both keyvault and managedhsm custodians, got %v", services)
	}
}
