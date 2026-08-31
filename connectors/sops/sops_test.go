// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package sops_test

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/connectors/sops"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// fakeDataKey is the marker baked into every per-recipient `enc` value in the
// fixture (and into `mac`). It is the ENCRYPTED DATA KEY material; the no-leak test
// asserts it NEVER appears in any emitted edge or snapshot identity.
const fakeDataKey = "FAKEDATAKEY"

type capturingSink struct{ edges []model.EdgeObservation }

func (c *capturingSink) Emit(_ context.Context, obs model.Observation) error {
	if e, ok := obs.(model.EdgeObservation); ok {
		c.edges = append(c.edges, e)
	}
	return nil
}

func open(t *testing.T, path string) *sops.Source {
	t.Helper()
	s := sops.New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"path": path}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func gather(t *testing.T, s *sops.Source) []model.EdgeObservation {
	t.Helper()
	sink := &capturingSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return sink.edges
}

// TestGatherEdges checks the provisioning edges from the one encrypted file in the
// repo: one edge per recipient (age + kms + pgp), all read, all from "sops", with
// the correct OriginRef and a relative ResourceRef.
func TestGatherEdges(t *testing.T) {
	edges := gather(t, open(t, "testdata/repo"))

	const wantRef = "secrets/secret.enc.yaml"
	wantOrigins := map[string]string{
		"sops.age:age1ql3z7hjy54pw3hyww5ayyfg7zqgvc7w3j2elw8zmrj2kg5sfn9aqmcac8p":              "age",
		"sops.kms:arn:aws:kms:eu-west-1:111122223333:key/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee": "kms",
		"sops.pgp:85D77543B3D624B63CEA9E6DBC17301B491B3F21":                                    "pgp",
	}
	if len(edges) != len(wantOrigins) {
		t.Fatalf("got %d edges, want %d: %+v", len(edges), len(wantOrigins), edges)
	}
	gotOrigins := map[string]bool{}
	for _, e := range edges {
		if e.OriginKind != "identity" {
			t.Errorf("origin kind = %q, want identity", e.OriginKind)
		}
		if e.ResourceKind != "sops.file" {
			t.Errorf("resource kind = %q, want sops.file", e.ResourceKind)
		}
		if e.ResourceRef != wantRef {
			t.Errorf("resource ref = %q, want %q", e.ResourceRef, wantRef)
		}
		if e.Mode != model.ModeRead {
			t.Errorf("mode = %q, want read (recipient can decrypt)", e.Mode)
		}
		if e.Source != model.SignalSource("sops") {
			t.Errorf("source = %q, want sops", e.Source)
		}
		if e.Confidence != model.ConfidenceAttributed {
			t.Errorf("confidence = %q, want attributed", e.Confidence)
		}
		wantTool, ok := wantOrigins[e.OriginRef]
		if !ok {
			t.Errorf("unexpected origin ref %q", e.OriginRef)
			continue
		}
		if e.ToolRef != wantTool {
			t.Errorf("origin %q tool ref = %q, want %q", e.OriginRef, e.ToolRef, wantTool)
		}
		gotOrigins[e.OriginRef] = true
	}
	for ref := range wantOrigins {
		if !gotOrigins[ref] {
			t.Errorf("missing edge for recipient %q", ref)
		}
	}
}

// TestGatherSingleFile checks that pointing at a single encrypted file works and
// the ResourceRef is the file's base name (relative to its own directory).
func TestGatherSingleFile(t *testing.T) {
	edges := gather(t, open(t, "testdata/repo/secrets/secret.enc.yaml"))
	if len(edges) != 3 {
		t.Fatalf("got %d edges, want 3: %+v", len(edges), edges)
	}
	for _, e := range edges {
		if e.ResourceRef != "secret.enc.yaml" {
			t.Errorf("resource ref = %q, want secret.enc.yaml", e.ResourceRef)
		}
	}
}

// TestPlainFileIgnored confirms a normal YAML file with no top-level `sops:` block
// yields no edge: pointing the connector straight at it produces nothing.
func TestPlainFileIgnored(t *testing.T) {
	edges := gather(t, open(t, "testdata/repo/plain/config.yaml"))
	if len(edges) != 0 {
		t.Fatalf("plain file produced %d edges, want 0: %+v", len(edges), edges)
	}
}

// TestNoDataKeyLeaks is the no-decrypt/no-leak guarantee (docs/SECURITY-HARDENING.md): the fixture
// embeds the encrypted data key (and mac) in every recipient's `enc`; that material
// must NEVER appear in any emitted edge OR any snapshot identity.
func TestNoDataKeyLeaks(t *testing.T) {
	s := open(t, "testdata/repo")
	edges := gather(t, s)
	if len(edges) == 0 {
		t.Fatal("expected edges")
	}
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Identities) == 0 {
		t.Fatal("expected snapshot identities")
	}

	edgeBlob, _ := json.Marshal(edges)
	idBlob, _ := json.Marshal(g.Identities)
	for name, blob := range map[string]string{"edges": string(edgeBlob), "identities": string(idBlob)} {
		if strings.Contains(blob, fakeDataKey) {
			t.Fatalf("the encrypted data key leaked into %s: %s", name, blob)
		}
		if strings.Contains(blob, "FAKEMAC") {
			t.Fatalf("the mac leaked into %s: %s", name, blob)
		}
		if strings.Contains(blob, "BEGIN PGP MESSAGE") || strings.Contains(blob, "BEGIN AGE ENCRYPTED FILE") {
			t.Fatalf("ciphertext leaked into %s: %s", name, blob)
		}
	}
}

// TestSnapshotInventory checks the inventory: every distinct recipient —
// across the encrypted file AND the .sops.yaml rules — is a secret_store NHI, and
// the rules contribute a recipient (the second age key) that no edge names.
func TestSnapshotInventory(t *testing.T) {
	s := open(t, "testdata/repo")
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if g.Source != identitysource.SourceSOPS {
		t.Errorf("graph source = %q, want sops", g.Source)
	}
	got := map[string]identitysource.Identity{}
	for _, id := range g.Identities {
		if id.Type != identitysource.PrincipalNHI {
			t.Errorf("identity %q type = %q, want nhi", id.Ref, id.Type)
		}
		if id.Kind != identitysource.KindSecretStore {
			t.Errorf("identity %q kind = %q, want secret_store", id.Ref, id.Kind)
		}
		if id.Source != identitysource.SourceSOPS {
			t.Errorf("identity %q source = %q, want sops", id.Ref, id.Source)
		}
		if id.Attributes["provider"] != "sops" {
			t.Errorf("identity %q provider attr = %q, want sops", id.Ref, id.Attributes["provider"])
		}
		if rt := id.Attributes["recipient_type"]; rt == "" {
			t.Errorf("identity %q missing recipient_type attr", id.Ref)
		}
		got[id.Ref] = id
	}
	// The encrypted file's three recipients + the second age key and the KMS arn
	// from .sops.yaml's first rule + the legacy pgp from the second rule.
	for _, want := range []string{
		"sops.age:age1ql3z7hjy54pw3hyww5ayyfg7zqgvc7w3j2elw8zmrj2kg5sfn9aqmcac8p",
		"sops.age:age1lggyhqrw2nlhcxprm67z43rta597azn8gk38fnem3d2es66x4z3qf0p5c0",
		"sops.kms:arn:aws:kms:eu-west-1:111122223333:key/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		"sops.pgp:85D77543B3D624B63CEA9E6DBC17301B491B3F21",
	} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing recipient NHI %q in %v", want, keys(got))
		}
	}
}

// TestRecipientsConverge asserts that every edge OriginRef is also present as a
// Snapshot identity Ref (the edge and the inventory converge on the same key).
func TestRecipientsConverge(t *testing.T) {
	s := open(t, "testdata/repo")
	edges := gather(t, s)
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range edges {
		if _, ok := g.FindIdentity(e.OriginRef); !ok {
			t.Errorf("edge origin %q has no converging snapshot identity", e.OriginRef)
		}
	}
}

// TestOfflineEmptyGraph confirms an unopened (no path) source returns an empty
// graph with no error.
func TestOfflineEmptyGraph(t *testing.T) {
	s := sops.New() // not opened => no path
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Identities) != 0 {
		t.Fatalf("offline graph not empty: %+v", g)
	}
	if g.Source != identitysource.SourceSOPS {
		t.Errorf("offline graph source = %q, want sops", g.Source)
	}
}

// TestClockDeterminism confirms the injectable clock drives ObservedAt/CapturedAt.
func TestClockDeterminism(t *testing.T) {
	fixed := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	s := open(t, "testdata/repo")
	sops.SetClock(s, func() time.Time { return fixed })
	edges := gather(t, s)
	for _, e := range edges {
		if !e.ObservedAt.Equal(fixed) {
			t.Errorf("observed at = %v, want %v", e.ObservedAt, fixed)
		}
	}
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !g.CapturedAt.Equal(fixed) {
		t.Errorf("captured at = %v, want %v", g.CapturedAt, fixed)
	}
}

func keys(m map[string]identitysource.Identity) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
