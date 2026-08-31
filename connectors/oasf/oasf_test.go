// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package oasf

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
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
	"github.com/olivaresai/olivares/connectors/internal/redact"
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

// ---------------------------------------------------------------------------
// Offline
// ---------------------------------------------------------------------------

func TestOfflineEmptyGraphAndGather(t *testing.T) {
	s := openSource(t, map[string]string{}) // no records source => offline
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("offline Snapshot must not error: %v", err)
	}
	if g.Source != identitysource.SourceOASF {
		t.Errorf("offline source = %q, want oasf", g.Source)
	}
	if !g.CapturedAt.Equal(testClock().UTC()) {
		t.Errorf("offline CapturedAt = %v, want clock", g.CapturedAt)
	}
	if len(g.Identities) != 0 || len(g.Collections) != 0 || len(g.Memberships) != 0 {
		t.Errorf("offline graph must be empty: %+v", g)
	}
	if fs := gatherFindings(t, s); len(fs) != 0 {
		t.Errorf("offline Gather emitted %d findings, want 0", len(fs))
	}
}

// ---------------------------------------------------------------------------
// Record import (fixture-driven Snapshot mapping)
// ---------------------------------------------------------------------------

func TestSnapshotFromFixture(t *testing.T) {
	s := openSource(t, map[string]string{"records_file": filepath.Join("testdata", "records.json")})
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(g.Identities) != 2 {
		t.Fatalf("identities = %d, want 2", len(g.Identities))
	}

	ra, ok := g.FindIdentity("oasf:research-agent@1.2.0")
	if !ok {
		t.Fatal("research-agent missing")
	}
	if ra.Type != identitysource.PrincipalNHI || ra.Kind != "agent_descriptor" {
		t.Errorf("type/kind = %q/%q, want nhi/agent_descriptor", ra.Type, ra.Kind)
	}
	if ra.DisplayName != "research-agent" || ra.Source != identitysource.SourceOASF {
		t.Errorf("display/source = %q/%q", ra.DisplayName, ra.Source)
	}
	want := map[string]string{"schema_version": "1.0.0", "badge": "none", "skills": "2", "locators": "1"}
	if !reflect.DeepEqual(ra.Attributes, want) {
		t.Errorf("attributes = %v, want %v", ra.Attributes, want)
	}

	ta, ok := g.FindIdentity("oasf:triage-agent@0.9.1")
	if !ok {
		t.Fatal("triage-agent missing")
	}
	if ta.Attributes["skills"] != "1" {
		t.Errorf("triage skills = %q, want 1", ta.Attributes["skills"])
	}
	if _, ok := ta.Attributes["locators"]; ok {
		t.Error("zero locators must be pruned, not emitted as \"0\"")
	}
	if ta.Attributes["badge"] != "none" {
		t.Errorf("triage badge = %q, want none", ta.Attributes["badge"])
	}
}

func TestRecordsDirAndDuplicates(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.json", validRecordJSON("agent-a", "1.0.0"))
	// Duplicate name@version in a second file: first occurrence wins, no dup row.
	write(t, dir, "b.json", "["+validRecordJSON("agent-a", "1.0.0")+","+validRecordJSON("agent-b", "2.0.0")+"]")
	write(t, dir, "ignored.txt", "not a record file")

	s := openSource(t, map[string]string{"records_dir": dir})
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(g.Identities) != 2 {
		t.Fatalf("identities = %d, want 2 (dedup + .json filter)", len(g.Identities))
	}
	if _, ok := g.FindIdentity("oasf:agent-b@2.0.0"); !ok {
		t.Error("agent-b (array element) missing")
	}
}

// TestRequiredFieldValidationMatrix proves the OASF 1.0.0 REQUIRED set is
// enforced field by field: each broken record is NOT rostered and yields one
// oasf_descriptor_invalid finding whose title carries the reason CATEGORY.
func TestRequiredFieldValidationMatrix(t *testing.T) {
	base := func() map[string]any {
		return map[string]any{
			"authors":        []any{"A"},
			"created_at":     "2026-06-01T10:00:00Z",
			"description":    "d",
			"name":           "agent-x",
			"schema_version": "1.0.0",
			"skills":         []any{map[string]any{"id": 1}},
			"version":        "1.0.0",
		}
	}
	cases := []struct {
		name string
		drop string         // key removed from the record
		set  map[string]any // keys overwritten
		want string         // reason category expected in the finding title
	}{
		{"missing name", "name", nil, "missing_name"},
		{"missing version", "version", nil, "missing_version"},
		{"missing schema_version", "schema_version", nil, "missing_schema_version"},
		{"missing description", "description", nil, "missing_description"},
		{"missing authors", "authors", nil, "missing_authors"},
		{"empty authors", "", map[string]any{"authors": []any{}}, "missing_authors"},
		{"missing created_at", "created_at", nil, "missing_created_at"},
		{"created_at not RFC3339", "", map[string]any{"created_at": "yesterday"}, "created_at_not_rfc3339"},
		{"missing skills", "skills", nil, "missing_skills"},
		{"empty skills", "", map[string]any{"skills": []any{}}, "missing_skills"},
		{"authors wrong type", "", map[string]any{"authors": "just-me"}, "wrong_field_type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := base()
			if tc.drop != "" {
				delete(rec, tc.drop)
			}
			for k, v := range tc.set {
				rec[k] = v
			}
			blob, err := json.Marshal(rec)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			dir := t.TempDir()
			path := write(t, dir, "rec.json", string(blob))

			s := openSource(t, map[string]string{"records_file": path})
			g, err := s.Snapshot(context.Background())
			if err != nil {
				t.Fatalf("Snapshot: %v", err)
			}
			if len(g.Identities) != 0 {
				t.Errorf("invalid record was rostered: %+v", g.Identities)
			}
			fs := findingsOfKind(gatherFindings(t, s), FindingDescriptorInvalid)
			if len(fs) != 1 {
				t.Fatalf("findings = %d, want 1", len(fs))
			}
			if !strings.Contains(fs[0].Title, tc.want) {
				t.Errorf("title = %q, want category %q", fs[0].Title, tc.want)
			}
			if fs[0].Severity != model.SeverityMedium || fs[0].SubjectKind != "identity" {
				t.Errorf("severity/subjectKind = %q/%q", fs[0].Severity, fs[0].SubjectKind)
			}
		})
	}
}

func TestNotJSONRecordFile(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "garbage.json", "this is not json at all")
	s := openSource(t, map[string]string{"records_file": path})
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(g.Identities) != 0 {
		t.Error("garbage file produced identities")
	}
	fs := findingsOfKind(gatherFindings(t, s), FindingDescriptorInvalid)
	if len(fs) != 1 || !strings.Contains(fs[0].Title, "not_json") {
		t.Fatalf("findings = %+v, want one not_json", fs)
	}
	if fs[0].SubjectRef != "oasf:file:garbage.json" {
		t.Errorf("subject ref = %q, want file-derived", fs[0].SubjectRef)
	}
	// The finding never carries file contents.
	if strings.Contains(fs[0].Title, "this is not json") {
		t.Error("finding title leaked file contents")
	}
}

// ---------------------------------------------------------------------------
// Badge verification (the honest 4-state)
// ---------------------------------------------------------------------------

func TestJOSEBadgeVerified(t *testing.T) {
	dir := t.TempDir()
	recPath := write(t, dir, "rec.json", validRecordJSON("research-agent", "1.2.0"))
	bs := newBadgeSigner(t)
	token := bs.sign(t, vcJSON("research-agent", "1.2.0", schemaURLCurrent, ""))

	t.Run("enveloped", func(t *testing.T) {
		badgePath := write(t, t.TempDir(), "badge.json", joseEnvelope(t, token))
		s := openSource(t, map[string]string{
			"records_file": recPath, "badges_file": badgePath, "issuer_jwks": bs.jwks,
		})
		g, err := s.Snapshot(context.Background())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		id, ok := g.FindIdentity("oasf:research-agent@1.2.0")
		if !ok {
			t.Fatal("record missing")
		}
		if id.Attributes["badge"] != "verified" {
			t.Errorf("badge = %q, want verified", id.Attributes["badge"])
		}
		if fs := gatherFindings(t, s); len(fs) != 0 {
			t.Errorf("verified setup emitted findings: %+v", fs)
		}
	})

	t.Run("raw compact JWS file", func(t *testing.T) {
		bdir := t.TempDir()
		write(t, bdir, "badge.jws", token)
		s := openSource(t, map[string]string{
			"records_file": recPath, "badges_dir": bdir, "issuer_jwks": bs.jwks,
		})
		g, err := s.Snapshot(context.Background())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		id, _ := g.FindIdentity("oasf:research-agent@1.2.0")
		if id.Attributes["badge"] != "verified" {
			t.Errorf("badge = %q, want verified (raw JWS form)", id.Attributes["badge"])
		}
	})
}

func TestTamperedBadgeInvalid(t *testing.T) {
	dir := t.TempDir()
	recPath := write(t, dir, "rec.json", validRecordJSON("research-agent", "1.2.0"))
	bs := newBadgeSigner(t)
	token := bs.sign(t, vcJSON("research-agent", "1.2.0", schemaURLCurrent, ""))

	// Tamper the PAYLOAD keeping the claimed name+version (so the outcome ties to
	// the record): the signature no longer matches.
	parts := strings.SplitN(token, ".", 3)
	evil := base64.RawURLEncoding.EncodeToString(
		[]byte(strings.Replace(vcJSON("research-agent", "1.2.0", schemaURLCurrent, ""), `"orig"`, `"evil"`, 1)))
	tampered := parts[0] + "." + evil + "." + parts[2]

	badgePath := write(t, dir, "badge.json", joseEnvelope(t, tampered))
	s := openSource(t, map[string]string{
		"records_file": recPath, "badges_file": badgePath, "issuer_jwks": bs.jwks,
	})

	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	id, ok := g.FindIdentity("oasf:research-agent@1.2.0")
	if !ok {
		t.Fatal("record missing (require_badge=false keeps it rostered)")
	}
	if id.Attributes["badge"] != "invalid" {
		t.Errorf("badge = %q, want invalid", id.Attributes["badge"])
	}

	fs := findingsOfKind(gatherFindings(t, s), FindingBadgeInvalid)
	if len(fs) != 1 {
		t.Fatalf("badge findings = %d, want 1", len(fs))
	}
	f := fs[0]
	if f.SubjectRef != "oasf:research-agent@1.2.0" || f.SubjectKind != "identity" {
		t.Errorf("subject = %q/%q", f.SubjectKind, f.SubjectRef)
	}
	if f.Title != "agent badge failed verification" || f.Severity != model.SeverityMedium {
		t.Errorf("title/severity = %q/%q", f.Title, f.Severity)
	}
	if want := redact.Hash(FindingBadgeInvalid + "|oasf:research-agent@1.2.0"); f.DetailHash != want {
		t.Errorf("detail hash = %q, want %q", f.DetailHash, want)
	}
	if !f.OccurredAt.Equal(testClock().UTC()) {
		t.Errorf("occurredAt = %v, want clock", f.OccurredAt)
	}
}

func TestEmbeddedProofUnverified(t *testing.T) {
	dir := t.TempDir()
	recPath := write(t, dir, "rec.json", validRecordJSON("research-agent", "1.2.0"))
	// The AGNTCY v1alpha1 embedded proof is non-conformant (no verificationMethod/
	// created/cryptosuite) — exactly what this connector refuses to "verify".
	vc := vcJSON("research-agent", "1.2.0", schemaURLLegacy, `,"proof":{"type":"DataIntegrityProof","proofValue":"z3FXQ"}`)
	badgePath := write(t, dir, "badge.json", `{"envelope_type":"EMBEDDED_PROOF","value":`+vc+`}`)

	t.Run("rostered as unverified", func(t *testing.T) {
		s := openSource(t, map[string]string{"records_file": recPath, "badges_file": badgePath})
		g, err := s.Snapshot(context.Background())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		id, ok := g.FindIdentity("oasf:research-agent@1.2.0")
		if !ok {
			t.Fatal("record missing")
		}
		if id.Attributes["badge"] != "unverified" {
			t.Errorf("badge = %q, want unverified (never trusted, never verified)", id.Attributes["badge"])
		}
		if fs := findingsOfKind(gatherFindings(t, s), FindingBadgeInvalid); len(fs) != 0 {
			t.Errorf("well-formed embedded proof must not be an invalid-badge finding: %+v", fs)
		}
	})

	t.Run("denied under require_badge", func(t *testing.T) {
		s := openSource(t, map[string]string{
			"records_file": recPath, "badges_file": badgePath, "require_badge": "true",
		})
		g, err := s.Snapshot(context.Background())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if len(g.Identities) != 0 {
			t.Errorf("unverified record rostered under require_badge: %+v", g.Identities)
		}
		fs := findingsOfKind(gatherFindings(t, s), FindingBadgeRequired)
		if len(fs) != 1 || fs[0].SubjectRef != "oasf:research-agent@1.2.0" {
			t.Fatalf("badge-required findings = %+v", fs)
		}
		if want := redact.Hash(FindingBadgeRequired + "|oasf:research-agent@1.2.0"); fs[0].DetailHash != want {
			t.Errorf("detail hash = %q, want %q", fs[0].DetailHash, want)
		}
	})
}

// TestEmbeddedProofEnvelopeWithoutProofIsInvalid: an EMBEDDED_PROOF envelope
// whose VC carries NO proof member is "invalid", exactly like a bare VC without
// proof — a proof-less credential is a claim, not a badge; the envelope
// declaration alone earns nothing.
func TestEmbeddedProofEnvelopeWithoutProofIsInvalid(t *testing.T) {
	dir := t.TempDir()
	recPath := write(t, dir, "rec.json", validRecordJSON("research-agent", "1.2.0"))
	vc := vcJSON("research-agent", "1.2.0", schemaURLLegacy, "") // no proof member
	badgePath := write(t, dir, "badge.json", `{"envelope_type":"EMBEDDED_PROOF","value":`+vc+`}`)

	s := openSource(t, map[string]string{"records_file": recPath, "badges_file": badgePath})
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	id, ok := g.FindIdentity("oasf:research-agent@1.2.0")
	if !ok {
		t.Fatal("record missing (require_badge off: an invalid badge does not unroster)")
	}
	if id.Attributes["badge"] != "invalid" {
		t.Errorf("badge = %q, want invalid (proof-less embedded-proof envelope)", id.Attributes["badge"])
	}
	fs := findingsOfKind(gatherFindings(t, s), FindingBadgeInvalid)
	if len(fs) != 1 || fs[0].SubjectRef != "oasf:research-agent@1.2.0" {
		t.Fatalf("invalid-badge findings = %+v", fs)
	}
}

// TestRequireBadgeGating proves the gate both ways: with require_badge=true only
// the badge-VERIFIED record survives; with false everything valid is rostered
// carrying its honest status.
func TestRequireBadgeGating(t *testing.T) {
	dir := t.TempDir()
	recs := "[" + validRecordJSON("agent-a", "1.0.0") + "," + validRecordJSON("agent-b", "1.0.0") + "]"
	recPath := write(t, dir, "recs.json", recs)
	bs := newBadgeSigner(t)
	badgePath := write(t, dir, "badge.json",
		joseEnvelope(t, bs.sign(t, vcJSON("agent-a", "1.0.0", schemaURLCurrent, ""))))

	t.Run("require_badge=true", func(t *testing.T) {
		s := openSource(t, map[string]string{
			"records_file": recPath, "badges_file": badgePath,
			"issuer_jwks": bs.jwks, "require_badge": "true",
		})
		g, err := s.Snapshot(context.Background())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if len(g.Identities) != 1 {
			t.Fatalf("identities = %d, want 1 (only verified)", len(g.Identities))
		}
		a, _ := g.FindIdentity("oasf:agent-a@1.0.0")
		if a.Attributes["badge"] != "verified" {
			t.Errorf("agent-a badge = %q", a.Attributes["badge"])
		}
		fs := findingsOfKind(gatherFindings(t, s), FindingBadgeRequired)
		if len(fs) != 1 || fs[0].SubjectRef != "oasf:agent-b@1.0.0" {
			t.Fatalf("denied finding = %+v, want agent-b", fs)
		}
	})

	t.Run("require_badge=false", func(t *testing.T) {
		s := openSource(t, map[string]string{
			"records_file": recPath, "badges_file": badgePath, "issuer_jwks": bs.jwks,
		})
		g, err := s.Snapshot(context.Background())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if len(g.Identities) != 2 {
			t.Fatalf("identities = %d, want 2", len(g.Identities))
		}
		b, _ := g.FindIdentity("oasf:agent-b@1.0.0")
		if b.Attributes["badge"] != "none" {
			t.Errorf("agent-b badge = %q, want none", b.Attributes["badge"])
		}
		if fs := findingsOfKind(gatherFindings(t, s), FindingBadgeRequired); len(fs) != 0 {
			t.Errorf("no gate => no badge-required findings, got %+v", fs)
		}
	})
}

// TestCredentialSchemaBothHosts proves BOTH OASF schema hosts are tolerated —
// the live outshift.com one and the dead agntcy.org one the AGNTCY identity
// spec still references — while an alien schema URL is rejected.
func TestCredentialSchemaBothHosts(t *testing.T) {
	bs := newBadgeSigner(t)
	cases := []struct {
		name     string
		schema   string
		want     string // expected badge attribute
		findings int    // expected oasf_badge_invalid count
	}{
		{"current outshift host", schemaURLCurrent, "verified", 0},
		{"legacy agntcy host", schemaURLLegacy, "verified", 0},
		{"alien schema host", "https://example.com/schema/agent", "invalid", 1},
		{"no credentialSchema", "", "verified", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			recPath := write(t, dir, "rec.json", validRecordJSON("agent-a", "1.0.0"))
			badgePath := write(t, dir, "badge.json",
				joseEnvelope(t, bs.sign(t, vcJSON("agent-a", "1.0.0", tc.schema, ""))))
			s := openSource(t, map[string]string{
				"records_file": recPath, "badges_file": badgePath, "issuer_jwks": bs.jwks,
			})
			g, err := s.Snapshot(context.Background())
			if err != nil {
				t.Fatalf("Snapshot: %v", err)
			}
			id, _ := g.FindIdentity("oasf:agent-a@1.0.0")
			if id.Attributes["badge"] != tc.want {
				t.Errorf("badge = %q, want %q", id.Attributes["badge"], tc.want)
			}
			if fs := findingsOfKind(gatherFindings(t, s), FindingBadgeInvalid); len(fs) != tc.findings {
				t.Errorf("invalid-badge findings = %d, want %d", len(fs), tc.findings)
			}
		})
	}
}

func TestHS256BadgeRejected(t *testing.T) {
	dir := t.TempDir()
	recPath := write(t, dir, "rec.json", validRecordJSON("agent-a", "1.0.0"))
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.HS256, Key: []byte("0123456789abcdef0123456789abcdef")},
		(&jose.SignerOptions{}).WithHeader("kid", "badge-key"))
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	obj, err := signer.Sign([]byte(vcJSON("agent-a", "1.0.0", schemaURLCurrent, "")))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	token, err := obj.CompactSerialize()
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	badgePath := write(t, dir, "hs.json", joseEnvelope(t, token))

	bs := newBadgeSigner(t) // a real trust anchor exists; the alg is still refused
	s := openSource(t, map[string]string{
		"records_file": recPath, "badges_file": badgePath, "issuer_jwks": bs.jwks,
	})
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	id, _ := g.FindIdentity("oasf:agent-a@1.0.0")
	if id.Attributes["badge"] == "verified" {
		t.Fatal("HS256 badge must never verify (alg allowlist)")
	}
	fs := findingsOfKind(gatherFindings(t, s), FindingBadgeInvalid)
	if len(fs) != 1 {
		t.Fatalf("invalid-badge findings = %d, want 1", len(fs))
	}
	if fs[0].SubjectRef != "oasf:badge:hs.json" {
		t.Errorf("subject ref = %q, want badge-derived (payload is untrusted at parse rejection)", fs[0].SubjectRef)
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
// Issuer JWKS over URL (the only network call)
// ---------------------------------------------------------------------------

func TestJWKSURLFetchAndNoAuthHeader(t *testing.T) {
	dir := t.TempDir()
	recPath := write(t, dir, "rec.json", validRecordJSON("agent-a", "1.0.0"))
	bs := newBadgeSigner(t)
	badgePath := write(t, dir, "badge.json",
		joseEnvelope(t, bs.sign(t, vcJSON("agent-a", "1.0.0", schemaURLCurrent, ""))))

	d := &stubDoer{status: 200, body: bs.jwks}
	s := New()
	s.now = testClock
	s.doer = d
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"records_file": recPath, "badges_file": badgePath,
		"issuer_jwks_url": "https://issuer.test/keys",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	id, _ := g.FindIdentity("oasf:agent-a@1.0.0")
	if id.Attributes["badge"] != "verified" {
		t.Errorf("badge = %q, want verified via fetched JWKS", id.Attributes["badge"])
	}
	if len(d.calls) == 0 {
		t.Fatal("JWKS URL was never fetched")
	}
	for _, req := range d.calls {
		if req.Method != http.MethodGet {
			t.Errorf("non-GET call: %s %s", req.Method, req.URL)
		}
		if !strings.Contains(req.URL.String(), "issuer.test/keys") {
			t.Errorf("unexpected URL: %s", req.URL)
		}
		// No credential is configured, so no auth header may ever be sent.
		if got := req.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization header sent without a credential: %q", got)
		}
	}
}

func TestJWKSURLFetchFailureIsHardError(t *testing.T) {
	dir := t.TempDir()
	recPath := write(t, dir, "rec.json", validRecordJSON("agent-a", "1.0.0"))
	bs := newBadgeSigner(t)
	badgePath := write(t, dir, "badge.json",
		joseEnvelope(t, bs.sign(t, vcJSON("agent-a", "1.0.0", schemaURLCurrent, ""))))

	d := &stubDoer{status: 500, body: `{"error":"boom"}`}
	s := New()
	s.now = testClock
	s.doer = d
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"records_file": recPath, "badges_file": badgePath,
		"issuer_jwks_url": "https://issuer.test/keys",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	// A trust-anchor fetch failure must be an ERROR, never a quiet "invalid"
	// badge (a transient fault must not masquerade as tampering).
	if _, err := s.Snapshot(context.Background()); err == nil {
		t.Fatal("expected Snapshot error on JWKS fetch failure")
	} else if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should carry the status: %v", err)
	}
	if err := s.Gather(context.Background(), &collectSink{}); err == nil {
		t.Fatal("expected Gather error on JWKS fetch failure")
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

// ---------------------------------------------------------------------------
// Security invariant: no badge/JWS material leaks into any emitted string.
// ---------------------------------------------------------------------------

func TestNoSecretLeaksIntoGraphOrFindings(t *testing.T) {
	dir := t.TempDir()
	recPath := write(t, dir, "recs.json",
		"["+validRecordJSON("agent-a", "1.0.0")+","+validRecordJSON("agent-b", "1.0.0")+"]")
	bs := newBadgeSigner(t)
	token := bs.sign(t, vcJSON("agent-a", "1.0.0", schemaURLCurrent, ""))
	// One good badge plus one tampered one, so both code paths emit.
	parts := strings.SplitN(token, ".", 3)
	evil := base64.RawURLEncoding.EncodeToString(
		[]byte(strings.Replace(vcJSON("agent-b", "1.0.0", schemaURLCurrent, ""), `"orig"`, `"evil"`, 1)))
	bdir := t.TempDir()
	write(t, bdir, "good.json", joseEnvelope(t, token))
	write(t, bdir, "tampered.json", joseEnvelope(t, parts[0]+"."+evil+"."+parts[2]))

	s := openSource(t, map[string]string{
		"records_file": recPath, "badges_dir": bdir, "issuer_jwks": bs.jwks,
	})
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	fs := gatherFindings(t, s)

	// The JWS compact token (a signed credential artifact) and the JWKS blob must
	// never be re-emitted in any roster or finding string.
	secrets := []string{token, parts[2], bs.jwks}
	assertNoSecret(t, g, secrets...)
	for _, f := range fs {
		for _, sec := range secrets {
			for _, field := range []string{f.Kind, f.Title, f.SubjectKind, f.SubjectRef, f.DetailHash} {
				if sec != "" && strings.Contains(field, sec) {
					t.Errorf("badge material leaked into finding field %q", field)
				}
			}
		}
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
