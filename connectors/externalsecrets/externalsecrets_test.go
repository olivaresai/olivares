// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package externalsecrets_test

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/externalsecrets"
	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

type capturingSink struct{ edges []model.EdgeObservation }

func (c *capturingSink) Emit(_ context.Context, obs model.Observation) error {
	if e, ok := obs.(model.EdgeObservation); ok {
		c.edges = append(c.edges, e)
	}
	return nil
}

func open(t *testing.T) *externalsecrets.Source {
	t.Helper()
	s := externalsecrets.New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"path": "testdata/manifests.yaml"}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func gather(t *testing.T, s *externalsecrets.Source) []model.EdgeObservation {
	t.Helper()
	sink := &capturingSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return sink.edges
}

// TestGatherEdges checks the ExternalSecret → provisioning-edge mapping: every
// data/dataFrom entry becomes one write edge whose OriginRef is the store, whose
// ResourceRef is <namespace>/<targetSecretName>, and whose ToolRef is the backend
// key it reads. The no-data ExternalSecret and the non-ESO ConfigMap emit nothing.
func TestGatherEdges(t *testing.T) {
	edges := gather(t, open(t))

	// app-credentials: 2 data entries + 1 dataFrom.extract = 3 edges.
	// cluster-wide:    1 data entry                         = 1 edge.
	// empty-es:        no data/dataFrom                     = 0 edges.
	if len(edges) != 4 {
		t.Fatalf("got %d edges, want 4: %+v", len(edges), edges)
	}

	for _, e := range edges {
		if e.OriginKind != "identity" {
			t.Errorf("edge OriginKind = %q, want identity: %+v", e.OriginKind, e)
		}
		if e.ResourceKind != "k8s.secret" {
			t.Errorf("edge ResourceKind = %q, want k8s.secret", e.ResourceKind)
		}
		if e.Mode != model.ModeWrite {
			t.Errorf("edge Mode = %q, want write", e.Mode)
		}
		if e.Source != model.SignalSource("eso") {
			t.Errorf("edge Source = %q, want eso", e.Source)
		}
		if e.Confidence != model.ConfidenceAttributed {
			t.Errorf("edge Confidence = %q, want attributed", e.Confidence)
		}
		if e.ToolRef == "" {
			t.Errorf("edge ToolRef empty (must name the backend key): %+v", e)
		}
		if e.ObservedAt.IsZero() {
			t.Errorf("edge ObservedAt is zero: %+v", e)
		}
	}

	// The app-credentials ExternalSecret resolves to the namespaced vault store and
	// the explicit target.name, in its own namespace.
	want := map[string]string{
		"apps/app/db":     "default/app-credentials-secret", // appears twice (username+password)
		"apps/app/shared": "default/app-credentials-secret",
	}
	gotOrigin := map[string]string{}
	for _, e := range edges {
		if rr, ok := want[e.ToolRef]; ok {
			if e.ResourceRef != rr {
				t.Errorf("ToolRef %q ResourceRef = %q, want %q", e.ToolRef, e.ResourceRef, rr)
			}
			if e.OriginRef != "eso.store:SecretStore:default:vault-backend" {
				t.Errorf("ToolRef %q OriginRef = %q, want vault store", e.ToolRef, e.OriginRef)
			}
		}
		gotOrigin[e.ToolRef] = e.OriginRef
	}

	// cluster-wide: ClusterSecretStore origin, ResourceRef in the ES's own namespace
	// (team-a), target name defaulted to metadata.name (cluster-wide).
	var found bool
	for _, e := range edges {
		if e.ToolRef == "prod/MUST_NOT_LEAK/api" {
			found = true
			if e.OriginRef != "eso.store:ClusterSecretStore:cluster:aws-sm" {
				t.Errorf("cluster-wide OriginRef = %q", e.OriginRef)
			}
			if e.ResourceRef != "team-a/cluster-wide" {
				t.Errorf("cluster-wide ResourceRef = %q, want team-a/cluster-wide", e.ResourceRef)
			}
		}
	}
	if !found {
		t.Error("cluster-wide ExternalSecret produced no edge")
	}
}

// TestSnapshotInventory checks the secret_store inventory: the
// SecretStore and ClusterSecretStore become secret_store NHIs carrying the backend.
func TestSnapshotInventory(t *testing.T) {
	g, err := open(t).Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if g.Source != identitysource.SourceExternalSecrets {
		t.Errorf("graph source = %q, want externalsecrets", g.Source)
	}
	got := map[string]identitysource.Identity{}
	for _, id := range g.Identities {
		if id.Type != identitysource.PrincipalNHI {
			t.Errorf("store NHI %q has type %q, want nhi", id.Ref, id.Type)
		}
		if id.Kind != identitysource.KindSecretStore {
			t.Errorf("store NHI %q has kind %q, want secret_store", id.Ref, id.Kind)
		}
		if id.Attributes["provider"] != "eso" {
			t.Errorf("store NHI %q provider attr = %q", id.Ref, id.Attributes["provider"])
		}
		got[id.Ref] = id
	}

	cases := []struct {
		ref       string
		backend   string
		storeKind string
	}{
		{"eso.store:SecretStore:default:vault-backend", "vault", "SecretStore"},
		{"eso.store:ClusterSecretStore:cluster:aws-sm", "aws", "ClusterSecretStore"},
	}
	for _, c := range cases {
		id, ok := got[c.ref]
		if !ok {
			t.Errorf("missing store NHI %q in %v", c.ref, keysOf(got))
			continue
		}
		if id.Attributes["backend"] != c.backend {
			t.Errorf("store %q backend = %q, want %q", c.ref, id.Attributes["backend"], c.backend)
		}
		if id.Attributes["store_kind"] != c.storeKind {
			t.Errorf("store %q store_kind = %q, want %q", c.ref, id.Attributes["store_kind"], c.storeKind)
		}
	}
}

// TestEdgeStoreConvergence asserts that every edge's OriginRef equals the Ref of an
// inventoried store NHI — inventory and use converge on one node (the contract).
func TestEdgeStoreConvergence(t *testing.T) {
	s := open(t)
	edges := gather(t, s)
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	storeRefs := map[string]bool{}
	for _, id := range g.Identities {
		storeRefs[id.Ref] = true
	}
	for _, e := range edges {
		if !storeRefs[e.OriginRef] {
			t.Errorf("edge OriginRef %q has no matching store NHI (refs: %v)", e.OriginRef, keysOf2(storeRefs))
		}
	}
}

// TestNoSecretValueLeaks is the minimal-data negative test: a CRD manifest holds no
// secret value, so no emitted field may carry one. We assert the literal key-name
// token used in the fixture is never mistaken for / emitted as a value (it appears
// only as a backend key NAME in ToolRef, which is intended; there must be no other
// payload), and that no edge carries an unexpected field.
func TestNoSecretValueLeaks(t *testing.T) {
	edges := gather(t, open(t))
	if len(edges) == 0 {
		t.Fatal("expected edges")
	}
	blob, _ := json.Marshal(edges)
	// "MUST_NOT_LEAK" is part of a key NAME (prod/MUST_NOT_LEAK/api); it is allowed
	// to appear ONLY inside a ToolRef. It must never appear as any standalone value.
	for _, e := range edges {
		other := e.OriginRef + "|" + e.ResourceRef + "|" + string(e.Mode) + "|" + string(e.Source)
		if strings.Contains(other, "MUST_NOT_LEAK") {
			t.Fatalf("a key name leaked outside ToolRef: %+v", e)
		}
	}
	// Sanity: the only occurrences in the serialized edges are inside ToolRef.
	var probe []struct {
		ToolRef string `json:"ToolRef"`
	}
	if err := json.Unmarshal(blob, &probe); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, p := range probe {
		if strings.Contains(p.ToolRef, "MUST_NOT_LEAK") {
			count++
		}
	}
	if strings.Count(string(blob), "MUST_NOT_LEAK") != count {
		t.Fatalf("MUST_NOT_LEAK appears outside ToolRef in %s", blob)
	}
}

// TestMultiDocumentParsing confirms a single multi-document file yields every ESO
// object (the fixture is one file with 6 documents, 4 of them ESO CRDs).
func TestMultiDocumentParsing(t *testing.T) {
	s := open(t)
	edges := gather(t, s)
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Identities) != 2 {
		t.Fatalf("multi-doc parse: got %d stores, want 2", len(g.Identities))
	}
	if len(edges) != 4 {
		t.Fatalf("multi-doc parse: got %d edges, want 4", len(edges))
	}
}

// TestOfflineEmptyGraph: with no configured path, Snapshot returns an empty graph
// and no error (offline), exactly like awskms.
func TestOfflineEmptyGraph(t *testing.T) {
	s := externalsecrets.New() // not opened => no path
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Identities) != 0 {
		t.Fatalf("offline graph not empty: %+v", g)
	}
	if g.Source != identitysource.SourceExternalSecrets {
		t.Errorf("offline graph source = %q", g.Source)
	}
}

// TestOpenRequiresPath: the path field is required.
func TestOpenRequiresPath(t *testing.T) {
	s := externalsecrets.New()
	if err := s.Open(context.Background(), sdk.Config{}); err == nil {
		t.Fatal("Open with no path: want error, got nil")
	}
}

func keysOf(m map[string]identitysource.Identity) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func keysOf2(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
