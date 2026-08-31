// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package awskms_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/awskms"
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

func open(t *testing.T) *awskms.Source {
	t.Helper()
	s := awskms.New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"path": "testdata/trail.json"}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestGatherEdges(t *testing.T) {
	s := open(t)
	sink := &capturingSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	// Decrypt(1) + ReEncrypt(2 keys) + CreateKey(1) + GetSecretValue(1) +
	// PutSecretValue(1) = 6 edges. GetRandomPassword (no resource) and the S3 event
	// are skipped.
	if len(sink.edges) != 6 {
		t.Fatalf("got %d edges, want 6: %+v", len(sink.edges), sink.edges)
	}

	var kmsKeyRead, secretRead, secretWrite, kmsWrite int
	for _, e := range sink.edges {
		if e.Source != model.SignalCloudTrail {
			t.Errorf("edge source = %q, want cloudtrail", e.Source)
		}
		if e.OriginKind != "identity" || e.OriginRef == "" {
			t.Errorf("bad origin: %+v", e)
		}
		switch e.ResourceKind {
		case "aws.kms.key":
			switch e.Mode {
			case model.ModeRead:
				kmsKeyRead++
			case model.ModeWrite:
				kmsWrite++
			}
		case "aws.secret":
			switch e.Mode {
			case model.ModeRead:
				secretRead++
			case model.ModeWrite:
				secretWrite++
			}
		default:
			t.Errorf("unexpected resource kind %q", e.ResourceKind)
		}
	}
	// 1 Decrypt + 2 ReEncrypt = 3 KMS reads; 1 CreateKey = 1 KMS write.
	if kmsKeyRead != 3 || kmsWrite != 1 {
		t.Errorf("kms reads=%d writes=%d, want 3/1", kmsKeyRead, kmsWrite)
	}
	// 1 GetSecretValue read; 1 PutSecretValue write.
	if secretRead != 1 || secretWrite != 1 {
		t.Errorf("secret reads=%d writes=%d, want 1/1", secretRead, secretWrite)
	}
}

// TestNoSecretValueLeaks is the minimal-data negative test (docs/SECURITY-HARDENING.md): the
// fixture embeds a SecretString; it must NEVER appear in any emitted edge field.
func TestNoSecretValueLeaks(t *testing.T) {
	s := open(t)
	sink := &capturingSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	blob, _ := json.Marshal(sink.edges)
	if strings.Contains(string(blob), "SUPERSECRETVALUE") {
		t.Fatalf("a secret value leaked into the emitted edges: %s", blob)
	}
	if len(sink.edges) == 0 {
		t.Fatal("expected edges")
	}
}

// TestSnapshotInventory checks the secret_store inventory: the KMS and
// Secrets Manager custodians seen in the export converge as secret_store NHIs.
func TestSnapshotInventory(t *testing.T) {
	s := open(t)
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if g.Source != identitysource.SourceAWSKMS {
		t.Errorf("graph source = %q", g.Source)
	}
	got := map[string]identitysource.Identity{}
	for _, id := range g.Identities {
		if id.Type != identitysource.PrincipalNHI || id.Kind != identitysource.KindSecretStore {
			t.Errorf("store NHI has wrong type/kind: %+v", id)
		}
		got[id.Ref] = id
	}
	for _, want := range []string{
		"aws.kms:111122223333:us-west-2",
		"aws.secretsmanager:111122223333:us-west-2",
	} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing store NHI %q in %v", want, got)
		}
	}
}

func TestOfflineEmptyGraph(t *testing.T) {
	s := awskms.New() // not opened => no path
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Identities) != 0 {
		t.Fatalf("offline graph not empty: %+v", g)
	}
}
