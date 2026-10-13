// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package oasf

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

func testClock() time.Time { return time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC) }

// openSource builds and opens a connector with the test clock injected.
func openSource(t *testing.T, settings map[string]string) *Source {
	t.Helper()
	s := New()
	s.now = testClock
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

// write drops content into dir/name and returns the full path.
func write(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

// validRecordJSON renders one minimal-but-valid OASF 1.0.0 record object.
func validRecordJSON(name, version string) string {
	return fmt.Sprintf(`{
  "authors": ["Olivares AI"],
  "created_at": "2026-06-01T10:00:00Z",
  "description": "test agent",
  "name": %q,
  "schema_version": "1.0.0",
  "skills": [{"id": 10201, "name": "text_completion"}],
  "version": %q
}`, name, version)
}

// vcJSON renders an Agent Badge VC payload embedding name+version. schemaID ""
// omits credentialSchema; extra is appended raw (e.g. a proof member).
func vcJSON(name, version, schemaID, extra string) string {
	schema := ""
	if schemaID != "" {
		schema = fmt.Sprintf(`,"credentialSchema":[{"id":%q,"type":"JsonSchemaCredential"}]`, schemaID)
	}
	return fmt.Sprintf(`{"@context":["https://www.w3.org/2018/credentials/v1"],"type":["VerifiableCredential","AgentBadge"],"issuer":"did:example:issuer","credentialSubject":{"id":"did:example:sub","badge":{"name":%q,"version":%q,"description":"orig"}}%s%s}`,
		name, version, schema, extra)
}

const schemaURLCurrent = "https://schema.oasf.outshift.com/0.7.0/objects/record"

const schemaURLLegacy = "https://schema.oasf.agntcy.org/schema/objects/agent"

// badgeSigner holds one ES256 issuer key so several badges in a test share the
// same operator trust anchor (a single issuer_jwks).
type badgeSigner struct {
	key  *ecdsa.PrivateKey
	jwks string
}

func newBadgeSigner(t *testing.T) *badgeSigner {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	ks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
		{Key: key.Public(), KeyID: "badge-key", Algorithm: "ES256", Use: "sig"},
	}}
	blob, err := json.Marshal(ks)
	if err != nil {
		t.Fatalf("marshal jwks: %v", err)
	}
	return &badgeSigner{key: key, jwks: string(blob)}
}

// sign returns the compact JWS over payload.
func (bs *badgeSigner) sign(t *testing.T, payload string) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: bs.key},
		(&jose.SignerOptions{}).WithHeader("kid", "badge-key"))
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	obj, err := signer.Sign([]byte(payload))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	token, err := obj.CompactSerialize()
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	return token
}

// joseEnvelope wraps a compact JWS in the AGNTCY JOSE envelope.
func joseEnvelope(t *testing.T, token string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"envelope_type": "JOSE", "value": token})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	return string(b)
}

// collectSink gathers emitted findings (and fails on any other observation).
type collectSink struct {
	findings []model.FindingReport
}

func (c *collectSink) Emit(_ context.Context, obs model.Observation) error {
	f, ok := obs.(model.FindingReport)
	if !ok {
		return fmt.Errorf("unexpected observation type %T", obs)
	}
	c.findings = append(c.findings, f)
	return nil
}

// gatherFindings runs Gather and returns the collected findings.
func gatherFindings(t *testing.T, s *Source) []model.FindingReport {
	t.Helper()
	sink := &collectSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return sink.findings
}

// findingsOfKind filters findings by kind.
func findingsOfKind(fs []model.FindingReport, kind string) []model.FindingReport {
	var out []model.FindingReport
	for _, f := range fs {
		if f.Kind == kind {
			out = append(out, f)
		}
	}
	return out
}

// stubDoer is the injected transport for the JWKS-URL tests: it records every
// request (so auth-header absence is provable) and returns a fixed response.
type stubDoer struct {
	status int
	body   string
	calls  []*http.Request
}

func (d *stubDoer) Do(req *http.Request) (*http.Response, error) {
	d.calls = append(d.calls, req)
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode: d.status,
		Header:     h,
		Body:       io.NopCloser(strings.NewReader(d.body)),
		Request:    req,
	}, nil
}

// ---------------------------------------------------------------------------
// Descriptor / config
// ---------------------------------------------------------------------------

func TestDescriptorShape(t *testing.T) {
	d := New().Descriptor()
	if d.Name != Name || d.Type != sdk.TypeSource || d.APIVersion != sdk.APIVersion || d.Version != "0.1.0" {
		t.Errorf("descriptor header wrong: %+v", d)
	}
	keys := map[string]sdk.ConfigField{}
	for _, f := range d.ConfigFields {
		keys[f.Key] = f
	}
	for _, want := range []string{"records_file", "records_dir", "badges_file", "badges_dir", "issuer_jwks", "issuer_jwks_url", "require_badge", "timeout"} {
		if _, ok := keys[want]; !ok {
			t.Errorf("config field %q missing", want)
		}
	}
	// This connector holds NO secret: records/badges are local files and the
	// issuer JWKS is public key material. Nothing may be declared Secret.
	for _, f := range d.ConfigFields {
		if f.Secret {
			t.Errorf("field %q wrongly declared Secret (the connector has no secret config)", f.Key)
		}
	}
	if keys["require_badge"].Type != sdk.FieldBool || keys["require_badge"].Default != "false" {
		t.Errorf("require_badge field wrong: %+v", keys["require_badge"])
	}
}

func TestOpenConfigErrors(t *testing.T) {
	s := New()
	// Both JWKS sources set: malformed config.
	err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"issuer_jwks": `{"keys":[]}`, "issuer_jwks_url": "https://issuer.test/keys",
	}})
	if err == nil {
		t.Error("expected error when both issuer_jwks and issuer_jwks_url are set")
	}
	// Malformed inline JWKS: malformed config.
	err = New().Open(context.Background(), sdk.Config{Settings: map[string]string{"issuer_jwks": `{not json`}})
	if err == nil {
		t.Error("expected error for malformed issuer_jwks")
	}
	// Nothing configured: NOT an error (offline is a valid state).
	if err := New().Open(context.Background(), sdk.Config{Settings: map[string]string{}}); err != nil {
		t.Errorf("Open with empty config must not fail: %v", err)
	}
}

func TestMalformedBadgeFile(t *testing.T) {
	dir := t.TempDir()
	recPath := write(t, dir, "rec.json", validRecordJSON("agent-a", "1.0.0"))
	badgePath := write(t, dir, "bad.json", "neither json nor a jws")
	s := openSource(t, map[string]string{"records_file": recPath, "badges_file": badgePath})

	fs := findingsOfKind(gatherFindings(t, s), FindingBadgeInvalid)
	if len(fs) != 1 || fs[0].SubjectRef != "oasf:badge:bad.json" {
		t.Fatalf("findings = %+v, want one with badge-derived ref", fs)
	}
}

// ---------------------------------------------------------------------------
// Export / round-trip (the primitive)
// ---------------------------------------------------------------------------

func TestExportRoundTrip(t *testing.T) {
	x := Record{
		Annotations:   map[string]string{"env": "prod"},
		Authors:       []string{"Olivares AI"},
		CreatedAt:     "2026-06-01T10:00:00Z",
		Description:   "test agent",
		Domains:       []Domain{{ID: 905, Name: "technology"}},
		Locators:      []Locator{{Type: "docker_image", URL: "ghcr.io/x:1"}},
		Modules:       []Module{{Name: "runtime/framework", Data: map[string]any{"framework": "langgraph"}}},
		Name:          "research-agent",
		SchemaVersion: "1.0.0",
		Skills:        []Skill{{ID: 10201, Name: "text_completion"}},
		Version:       "1.2.0",
	}
	out, err := Export(x)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	got, err := Import(out)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if !reflect.DeepEqual(got, x) {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got, x)
	}
	// Deterministic output: exporting twice yields identical bytes.
	out2, err := Export(x)
	if err != nil {
		t.Fatalf("Export again: %v", err)
	}
	if !bytes.Equal(out, out2) {
		t.Error("Export is not deterministic")
	}
}

func TestExportFillsSchemaVersion(t *testing.T) {
	x := Record{
		Authors:     []string{"A"},
		CreatedAt:   "2026-06-01T10:00:00Z",
		Description: "d",
		Name:        "agent-x",
		Skills:      []Skill{{ID: 1}},
		Version:     "1.0.0",
	}
	out, err := Export(x)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	got, err := Import(out)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if got.SchemaVersion != SchemaVersion {
		t.Errorf("schema_version = %q, want %q filled", got.SchemaVersion, SchemaVersion)
	}
}

func TestExportRejectsInvalid(t *testing.T) {
	_, err := Export(Record{Name: "x", Version: "1"})
	if err == nil {
		t.Fatal("Export must validate the REQUIRED set")
	}
	if !strings.Contains(err.Error(), "missing_") {
		t.Errorf("error should carry the reason category: %v", err)
	}
	if _, err := Import([]byte(`{"name":"x"}`)); err == nil {
		t.Fatal("Import must validate the REQUIRED set")
	}
}

// assertNoSecret walks every string field of the Graph and fails if any secret
// appears, proving the connector carries only descriptor metadata.
func assertNoSecret(t *testing.T, g identitysource.Graph, secrets ...string) {
	t.Helper()
	var fields []string
	for _, id := range g.Identities {
		fields = append(fields, id.Ref, id.Kind, id.DisplayName, string(id.Type))
		for k, v := range id.Attributes {
			fields = append(fields, k, v)
		}
	}
	for _, c := range g.Collections {
		fields = append(fields, c.Ref, c.DisplayName)
		for k, v := range c.Attributes {
			fields = append(fields, k, v)
		}
	}
	for _, m := range g.Memberships {
		fields = append(fields, m.MemberRef, m.CollectionRef)
	}
	for _, f := range fields {
		for _, secret := range secrets {
			if secret != "" && strings.Contains(f, secret) {
				t.Errorf("secret leaked into Graph field %q", f)
			}
		}
	}
}
