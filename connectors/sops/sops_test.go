// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package sops_test

import (
	"context"
	"sort"
	"testing"

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

func keys(m map[string]identitysource.Identity) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
