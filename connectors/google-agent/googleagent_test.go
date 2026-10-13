// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package googleagent

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// fixedNow is the injected clock for deterministic iat/exp assertions.
var fixedNow = time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)

const (
	testTokenURL    = "https://oauth2.test.local/token"
	testClientEmail = "agent-reader@test-project.iam.gserviceaccount.com"
	testAccessToken = "ya29.test-access-token"
)

// testRSAKey generates ONE throwaway RSA key per test binary (never a committed
// real-looking key fixture; the testdata credentials JSON holds placeholders the
// tests replace).
var (
	testKeyOnce sync.Once
	testKeyVal  *rsa.PrivateKey
)

func testRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	testKeyOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		testKeyVal = k
	})
	return testKeyVal
}

// pemPKCS8 encodes the test key as a PKCS#8 PEM (Google's current SA key format).
func pemPKCS8(t *testing.T) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(testRSAKey(t))
	if err != nil {
		t.Fatalf("marshal pkcs8: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// pemPKCS1 encodes the test key as a legacy PKCS#1 PEM.
func pemPKCS1(t *testing.T) string {
	t.Helper()
	der := x509.MarshalPKCS1PrivateKey(testRSAKey(t))
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}))
}

// credsJSON renders the placeholder fixture into a usable SA key JSON: the
// throwaway PEM (JSON-escaped) and the test token URI replace the placeholders.
func credsJSON(t *testing.T, pemStr, tokenURI string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "credentials_placeholder.json"))
	if err != nil {
		t.Fatalf("read credentials fixture: %v", err)
	}
	out := strings.ReplaceAll(string(b), "__TEST_PRIVATE_KEY_PEM__", strings.ReplaceAll(pemStr, "\n", `\n`))
	out = strings.ReplaceAll(out, "__TEST_TOKEN_URI__", tokenURI)
	if !json.Valid([]byte(out)) {
		t.Fatal("rendered credentials fixture is not valid JSON")
	}
	return out
}

// ---------------------------------------------------------------------------
// Stub transport (the idp convention: routes keyed "METHOD path-substring")
// ---------------------------------------------------------------------------

type recordedRequest struct {
	Method string
	URL    string
	Auth   string
	Body   string
}

type route struct {
	status int
	body   string
}

type stubDoer struct {
	t       *testing.T
	routes  map[string][]route // key: METHOD + " " + pathOrURL substring
	calls   []recordedRequest
	fixture func(name string) string
}

func newStub(t *testing.T) *stubDoer {
	t.Helper()
	return &stubDoer{
		t:      t,
		routes: map[string][]route{},
		fixture: func(name string) string {
			b, err := os.ReadFile(filepath.Join("testdata", name))
			if err != nil {
				t.Fatalf("read fixture %s: %v", name, err)
			}
			return string(b)
		},
	}
}

func (d *stubDoer) on(method, match, body string) *stubDoer {
	return d.onStatus(method, match, 200, body)
}

func (d *stubDoer) onStatus(method, match string, status int, body string) *stubDoer {
	key := method + " " + match
	d.routes[key] = append(d.routes[key], route{status: status, body: body})
	return d
}

func (d *stubDoer) Do(req *http.Request) (*http.Response, error) {
	var bodyStr string
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		bodyStr = string(b)
		_ = req.Body.Close()
	}
	d.calls = append(d.calls, recordedRequest{
		Method: req.Method,
		URL:    req.URL.String(),
		Auth:   req.Header.Get("Authorization"),
		Body:   bodyStr,
	})
	for key, queued := range d.routes {
		parts := strings.SplitN(key, " ", 2)
		if parts[0] != req.Method || len(queued) == 0 {
			continue
		}
		if strings.Contains(req.URL.Path, parts[1]) || strings.Contains(req.URL.String(), parts[1]) {
			r := queued[0]
			d.routes[key] = queued[1:]
			h := http.Header{}
			h.Set("Content-Type", "application/json")
			return &http.Response{
				StatusCode: r.status,
				Header:     h,
				Body:       io.NopCloser(bytes.NewBufferString(r.body)),
				Request:    req,
			}, nil
		}
	}
	d.t.Fatalf("unexpected request: %s %s", req.Method, req.URL.String())
	return nil, fmt.Errorf("unreachable")
}

func sawPath(d *stubDoer, sub string) bool {
	for _, c := range d.calls {
		if strings.Contains(c.URL, sub) {
			return true
		}
	}
	return false
}

func openSource(t *testing.T, d *stubDoer, settings map[string]string) *Source {
	t.Helper()
	s := New()
	s.doer = d
	s.now = func() time.Time { return fixedNow }
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

// configuredSettings is the standard online configuration (key token_uri used as
// the token endpoint; both locations; the {location} base override).
func configuredSettings(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		"credentials_json": credsJSON(t, pemPKCS8(t), testTokenURL),
		"project":          "test-project",
		"locations":        "us-central1, europe-west4",
		"base_url":         "https://{location}-aiplatform.test.local",
	}
}

func gatherSettings(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		"credentials_json":         credsJSON(t, pemPKCS8(t), testTokenURL),
		"project":                  "test-project-id",
		"locations":                "us-central1",
		"base_url":                 "https://{location}-aiplatform.test.local",
		"registry_endpoint":        "https://agentregistry.test.local",
		"registry_locations":       "global",
		"networkservices_endpoint": "https://networkservices.test.local",
		"gateway_locations":        "us-central1",
		"read_registry":            "true",
		"read_gateways":            "true",
	}
}

type collectSink struct {
	obs []model.Observation
}

func (s *collectSink) Emit(_ context.Context, obs model.Observation) error {
	s.obs = append(s.obs, obs)
	return nil
}

func (s *collectSink) findings(t *testing.T) []model.FindingReport {
	t.Helper()
	out := make([]model.FindingReport, 0, len(s.obs))
	for _, obs := range s.obs {
		f, ok := obs.(model.FindingReport)
		if !ok {
			t.Fatalf("observation %T, want FindingReport", obs)
		}
		out = append(out, f)
	}
	return out
}

func runGather(t *testing.T, d *stubDoer, settings map[string]string) []model.FindingReport {
	t.Helper()
	s := openSource(t, d, settings)
	sink := &collectSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return sink.findings(t)
}

func kinds(findings []model.FindingReport) []string {
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		out = append(out, f.Kind)
	}
	return out
}

func sequence(findings []model.FindingReport) []string {
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		out = append(out, f.Kind+"|"+string(f.Severity)+"|"+f.SubjectKind+"|"+f.SubjectRef+"|"+f.Title+"|"+f.DetailHash+"|"+f.OccurredAt.Format(time.RFC3339Nano))
	}
	return out
}

// assertNoSecret walks every string field of the Graph and fails if any secret
// appears, proving the connector carries only identity metadata.
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

// assertNoKeyMaterial fails when an error message embeds the PEM (whole or any
// base64 line of it) — bounded provider excerpts only, never the credential.
func assertNoKeyMaterial(t *testing.T, msg, pemStr string) {
	t.Helper()
	if strings.Contains(msg, "PRIVATE KEY") {
		t.Errorf("error carries PEM markers: %v", msg)
	}
	for _, line := range strings.Split(pemStr, "\n") {
		if len(line) > 20 && strings.Contains(msg, line) {
			t.Errorf("error carries key material line %q", line)
		}
	}
}

func TestOpenMalformedConfig(t *testing.T) {
	pem8 := pemPKCS8(t)
	ecDER, err := x509.MarshalPKCS8PrivateKey(mustECKey(t))
	if err != nil {
		t.Fatalf("marshal ec key: %v", err)
	}
	ecPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: ecDER}))

	cases := []struct {
		name     string
		settings map[string]string
	}{
		{"not json", map[string]string{"project": "p", "credentials_json": `{"client_email":`}},
		{"missing private_key", map[string]string{"project": "p", "credentials_json": `{"client_email":"a@b","token_uri":"https://t"}`}},
		{"missing client_email", map[string]string{"project": "p", "credentials_json": credsJSONRaw(t, pem8, "", testTokenURL)}},
		{"private_key not PEM", map[string]string{"project": "p", "credentials_json": credsJSONRaw(t, "not-a-pem", "a@b", testTokenURL)}},
		{"private_key not RSA", map[string]string{"project": "p", "credentials_json": credsJSONRaw(t, ecPEM, "a@b", testTokenURL)}},
		{"credentials without project", map[string]string{"credentials_json": credsJSON(t, pem8, testTokenURL)}},
		{"credentials_file unreadable", map[string]string{"project": "p", "credentials_file": filepath.Join(t.TempDir(), "missing.json")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := New()
			if err := s.Open(context.Background(), sdk.Config{Settings: tc.settings}); err == nil {
				t.Errorf("Open must reject malformed config %q", tc.name)
			}
		})
	}
}

// credsJSONRaw builds a key JSON with explicit parts (for malformed-config cases).
func credsJSONRaw(t *testing.T, pemStr, email, tokenURI string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{
		"type":         "service_account",
		"client_email": email,
		"private_key":  pemStr,
		"token_uri":    tokenURI,
	})
	if err != nil {
		t.Fatalf("marshal creds: %v", err)
	}
	return string(b)
}

func mustECKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa: %v", err)
	}
	return k
}

func TestCredentialsFileAndPKCS1(t *testing.T) {
	// Legacy PKCS#1 PEM, loaded from a file path: both must be accepted.
	path := filepath.Join(t.TempDir(), "sa.json")
	if err := os.WriteFile(path, []byte(credsJSONRaw(t, pemPKCS1(t), testClientEmail, testTokenURL)), 0o600); err != nil {
		t.Fatalf("write creds file: %v", err)
	}
	s := New()
	err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"project":          "test-project",
		"credentials_file": path,
	}})
	if err != nil {
		t.Fatalf("Open with PKCS#1 credentials_file: %v", err)
	}
	if s.key == nil || s.key.rsa == nil {
		t.Fatal("key not loaded from file")
	}
	if s.tokenURL != testTokenURL {
		t.Errorf("tokenURL = %q, want the key's token_uri", s.tokenURL)
	}
}

func TestTokenURLResolution(t *testing.T) {
	pem8 := pemPKCS8(t)
	cases := []struct {
		name     string
		settings map[string]string
		want     string
	}{
		{
			"config override wins",
			map[string]string{"project": "p", "token_url": "https://override.test/token",
				"credentials_json": credsJSONRaw(t, pem8, "a@b", "https://key.test/token")},
			"https://override.test/token",
		},
		{
			"key token_uri",
			map[string]string{"project": "p",
				"credentials_json": credsJSONRaw(t, pem8, "a@b", "https://key.test/token")},
			"https://key.test/token",
		},
		{
			"google default",
			map[string]string{"project": "p",
				"credentials_json": credsJSONRaw(t, pem8, "a@b", "")},
			"https://oauth2.googleapis.com/token",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := New()
			if err := s.Open(context.Background(), sdk.Config{Settings: tc.settings}); err != nil {
				t.Fatalf("Open: %v", err)
			}
			if s.tokenURL != tc.want {
				t.Errorf("tokenURL = %q, want %q", s.tokenURL, tc.want)
			}
		})
	}
}

func TestLocationsParsing(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"project": "p"}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if len(s.locations) != 1 || s.locations[0] != "us-central1" {
		t.Errorf("default locations = %v, want [us-central1]", s.locations)
	}
	s2 := New()
	if err := s2.Open(context.Background(), sdk.Config{Settings: map[string]string{"project": "p", "locations": " europe-west4 ,, us-east5 "}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if len(s2.locations) != 2 || s2.locations[0] != "europe-west4" || s2.locations[1] != "us-east5" {
		t.Errorf("locations = %v", s2.locations)
	}
}

func TestDescriptorShape(t *testing.T) {
	desc := New().Descriptor()
	if desc.Name != Name || desc.Type != sdk.TypeSource || desc.APIVersion != sdk.APIVersion || desc.Version != "0.2.0" {
		t.Errorf("descriptor header wrong: %+v", desc)
	}
	secret := map[string]bool{}
	for _, f := range desc.ConfigFields {
		secret[f.Key] = f.Secret
	}
	if !secret["credentials_json"] {
		t.Error("credentials_json must be Secret")
	}
	for _, k := range []string{
		"credentials_file", "project", "locations", "token_url", "base_url",
		"read_registry", "registry_endpoint", "registry_locations",
		"read_gateways", "networkservices_endpoint", "gateway_locations",
		"page_size", "max_pages", "timeout",
	} {
		if _, declared := secret[k]; !declared {
			t.Errorf("config field %q not declared", k)
		}
	}
}

func TestGatherShadowDetectionTrailingSegment(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodPost, testTokenURL, d.fixture("token.json"))
	d.on(http.MethodGet, "/agents", d.fixture("registry_agents_shadow.json"))
	d.on(http.MethodGet, "/mcpServers", d.fixture("registry_mcp_empty.json"))
	d.on(http.MethodGet, "/reasoningEngines", d.fixture("engines_shadow.json"))

	settings := gatherSettings(t)
	settings["read_gateways"] = "false"
	findings := runGather(t, d, settings)

	var shadows []model.FindingReport
	for _, f := range findings {
		if f.Kind == findingAgentOutsideRegistry {
			shadows = append(shadows, f)
		}
	}
	if len(shadows) != 2 {
		t.Fatalf("shadow findings = %d, want 2: %#v", len(shadows), findings)
	}
	if shadows[0].SubjectRef != "shadow-sa@test-project-id.iam.gserviceaccount.com" {
		t.Errorf("first shadow subject = %q", shadows[0].SubjectRef)
	}
	if shadows[1].SubjectRef != "projects/9876543210/locations/us-central1/reasoningEngines/shadow-empty" {
		t.Errorf("empty-identity shadow subject = %q", shadows[1].SubjectRef)
	}
	for _, f := range shadows {
		if f.Severity != model.SeverityMedium || f.SubjectKind != "identity" {
			t.Errorf("bad shadow finding: %+v", f)
		}
	}
}

func TestGatherRegistryEmptyWithActiveAgents(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodPost, testTokenURL, d.fixture("token.json"))
	d.on(http.MethodGet, "/agents", d.fixture("registry_agents_empty.json"))
	d.on(http.MethodGet, "/mcpServers", d.fixture("registry_mcp_empty.json"))
	d.on(http.MethodGet, "/reasoningEngines", d.fixture("engines_shadow.json"))

	settings := gatherSettings(t)
	settings["read_gateways"] = "false"
	findings := runGather(t, d, settings)
	if !hasFinding(findings, findingRegistryEmpty, model.SeverityMedium) {
		t.Fatalf("missing empty-registry finding: %#v", findings)
	}

	d2 := newStub(t)
	d2.on(http.MethodPost, testTokenURL, d2.fixture("token.json"))
	d2.on(http.MethodGet, "/agents", d2.fixture("registry_agents_empty.json"))
	d2.on(http.MethodGet, "/mcpServers", d2.fixture("registry_mcp_empty.json"))
	d2.on(http.MethodGet, "/reasoningEngines", `{}`)
	findings = runGather(t, d2, settings)
	if hasKind(findings, findingRegistryEmpty) {
		t.Fatalf("empty registry with zero engines emitted finding: %#v", findings)
	}
}

func TestGatherRegistryUnreadableAllSkipsShadow(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodPost, testTokenURL, d.fixture("token.json"))
	d.onStatus(http.MethodGet, "/locations/global/agents", http.StatusForbidden, `{"error":{"code":403}}`)
	d.onStatus(http.MethodGet, "/locations/us-central1/agents", http.StatusNotFound, `{"error":{"code":404}}`)

	settings := gatherSettings(t)
	settings["registry_locations"] = "global,us-central1"
	settings["read_gateways"] = "false"
	findings := runGather(t, d, settings)

	if len(findings) != 1 || findings[0].Kind != findingRegistryUnreadable || findings[0].Severity != model.SeverityMedium {
		t.Fatalf("findings = %#v, want one registry unreadable medium", findings)
	}
	if hasKind(findings, findingAgentOutsideRegistry) {
		t.Fatal("shadow analysis must be skipped when no registry location is readable")
	}
	for _, c := range d.calls {
		if strings.Contains(c.URL, "reasoningEngines") {
			t.Fatalf("reasoningEngines called despite fully unreadable registry: %s", c.URL)
		}
	}
}

func TestGatherRegistryPartialUnreadableStillShadows(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodPost, testTokenURL, d.fixture("token.json"))
	d.onStatus(http.MethodGet, "/locations/global/agents", http.StatusForbidden, `{"error":{"code":403}}`)
	d.on(http.MethodGet, "/locations/europe-west4/agents", d.fixture("registry_agents_empty.json"))
	d.on(http.MethodGet, "/locations/europe-west4/mcpServers", d.fixture("registry_mcp_empty.json"))
	d.on(http.MethodGet, "/reasoningEngines", d.fixture("engines_shadow.json"))

	settings := gatherSettings(t)
	settings["registry_locations"] = "global,europe-west4"
	settings["read_gateways"] = "false"
	findings := runGather(t, d, settings)
	if hasKind(findings, findingRegistryUnreadable) {
		t.Fatalf("partial unreadable must not emit all-unreadable finding: %#v", findings)
	}
	if !hasKind(findings, findingAgentOutsideRegistry) {
		t.Fatalf("shadow analysis did not run for readable location: %#v", findings)
	}
	if !hasFinding(findings, findingRegistryPartialCoverage, model.SeverityLow) {
		t.Fatalf("missing low partial coverage finding: %#v", findings)
	}
}

func TestGatherMCPDestructiveTools(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodPost, testTokenURL, d.fixture("token.json"))
	d.on(http.MethodGet, "/agents", d.fixture("registry_agents_empty.json"))
	d.on(http.MethodGet, "/mcpServers", d.fixture("registry_mcp_destructive.json"))
	d.on(http.MethodGet, "/reasoningEngines", `{}`)

	settings := gatherSettings(t)
	settings["read_gateways"] = "false"
	findings := runGather(t, d, settings)
	var toolFinding model.FindingReport
	for _, f := range findings {
		if f.Kind == findingRegistryToolDestructive {
			toolFinding = f
			break
		}
	}
	if toolFinding.Kind == "" {
		t.Fatalf("missing destructive tool finding: %#v", findings)
	}
	if toolFinding.Severity != model.SeverityLow || !strings.Contains(toolFinding.Title, "delete-world, open-net") {
		t.Errorf("tool finding = %+v", toolFinding)
	}
	wantDetail := findingRegistryToolDestructive + "|google-agent|projects/test-project-id/locations/global/mcpServers/risky-tools|destructive=1|open_world=1|tools=delete-world,open-net"
	if toolFinding.DetailHash != redact.Hash(wantDetail) {
		t.Errorf("tool DetailHash = %q", toolFinding.DetailHash)
	}
	assertNoSentinelInFindings(t, findings)

	d2 := newStub(t)
	d2.on(http.MethodPost, testTokenURL, d2.fixture("token.json"))
	d2.on(http.MethodGet, "/agents", d2.fixture("registry_agents_empty.json"))
	d2.on(http.MethodGet, "/mcpServers", d2.fixture("registry_mcp_benign.json"))
	d2.on(http.MethodGet, "/reasoningEngines", `{}`)
	findings = runGather(t, d2, settings)
	if hasKind(findings, findingRegistryToolDestructive) {
		t.Fatalf("benign MCP tools emitted finding: %#v", findings)
	}
}

func TestGatherGatewayPosture(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodPost, testTokenURL, d.fixture("token.json"))
	d.on(http.MethodGet, "/agentGateways", d.fixture("gateways_mixed.json"))

	settings := gatherSettings(t)
	settings["read_registry"] = "false"
	findings := runGather(t, d, settings)

	if got := kinds(findings); len(got) != 3 ||
		got[0] != findingGatewayPosture ||
		got[1] != findingGatewayNoRegistry ||
		got[2] != findingGatewayPartialCoverage {
		t.Fatalf("gateway finding order = %v; findings=%#v", got, findings)
	}
	info := findings[0]
	if info.Severity != model.SeverityInfo {
		t.Errorf("gateway posture severity = %s", info.Severity)
	}
	wantInfoDetail := findingGatewayPosture + "|google-agent|projects/test-project-id/locations/us-central1/agentGateways/linked-gw|access_path=CLIENT_TO_AGENT self_managed=false registries=1 model_armor_binding=unreadable_at_ga"
	if info.DetailHash != redact.Hash(wantInfoDetail) {
		t.Errorf("gateway posture DetailHash = %q", info.DetailHash)
	}
	if findings[1].Severity != model.SeverityMedium || findings[1].SubjectRef != "projects/test-project-id/locations/us-central1/agentGateways/no-registry-gw" {
		t.Errorf("no-registry finding = %+v", findings[1])
	}
}

func TestGatherReadGatewaysFalseSkipsNetworkServices(t *testing.T) {
	d := newStub(t)
	d.on(http.MethodPost, testTokenURL, d.fixture("token.json"))
	d.on(http.MethodGet, "/agents", d.fixture("registry_agents_empty.json"))
	d.on(http.MethodGet, "/mcpServers", d.fixture("registry_mcp_empty.json"))
	d.on(http.MethodGet, "/reasoningEngines", `{}`)

	settings := gatherSettings(t)
	settings["read_gateways"] = "false"
	_ = runGather(t, d, settings)
	for _, c := range d.calls {
		if strings.Contains(c.URL, "networkservices") {
			t.Fatalf("networkservices called with read_gateways=false: %s", c.URL)
		}
	}
}

func TestGatherPaginationBoundsEmitPartials(t *testing.T) {
	t.Run("registry", func(t *testing.T) {
		d := newStub(t)
		d.on(http.MethodPost, testTokenURL, d.fixture("token.json"))
		d.on(http.MethodGet, "/agents", `{"agents":[],"nextPageToken":"MORE"}`)
		d.on(http.MethodGet, "/mcpServers", d.fixture("registry_mcp_empty.json"))
		d.on(http.MethodGet, "/reasoningEngines", `{}`)

		settings := gatherSettings(t)
		settings["read_gateways"] = "false"
		settings["max_pages"] = "1"
		findings := runGather(t, d, settings)
		if !hasFinding(findings, findingRegistryPartialCoverage, model.SeverityLow) {
			t.Fatalf("missing registry pagination partial: %#v", findings)
		}
	})

	t.Run("gateway", func(t *testing.T) {
		d := newStub(t)
		d.on(http.MethodPost, testTokenURL, d.fixture("token.json"))
		d.on(http.MethodGet, "/agentGateways", d.fixture("gateways_page_loop.json"))

		settings := gatherSettings(t)
		settings["read_registry"] = "false"
		settings["max_pages"] = "1"
		findings := runGather(t, d, settings)
		if !hasFinding(findings, findingGatewayPartialCoverage, model.SeverityLow) {
			t.Fatalf("missing gateway pagination partial: %#v", findings)
		}
	})
}

func TestGatherOfflineNoCalls(t *testing.T) {
	d := newStub(t)
	s := openSource(t, d, map[string]string{"project": "test-project-id"})
	sink := &collectSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("offline Gather: %v", err)
	}
	if len(sink.obs) != 0 {
		t.Fatalf("offline Gather emitted observations: %#v", sink.obs)
	}
	if len(d.calls) != 0 {
		t.Fatalf("offline Gather made HTTP calls: %#v", d.calls)
	}
}

func TestGatherDeterministic(t *testing.T) {
	run := func(t *testing.T) []string {
		d := newStub(t)
		d.on(http.MethodPost, testTokenURL, d.fixture("token.json"))
		d.on(http.MethodGet, "/agents", d.fixture("registry_agents_shadow.json"))
		d.on(http.MethodGet, "/mcpServers", d.fixture("registry_mcp_destructive.json"))
		d.on(http.MethodGet, "/reasoningEngines", d.fixture("engines_shadow.json"))
		d.on(http.MethodGet, "/agentGateways", d.fixture("gateways_mixed.json"))
		return sequence(runGather(t, d, gatherSettings(t)))
	}
	a := run(t)
	b := run(t)
	if strings.Join(a, "\n") != strings.Join(b, "\n") {
		t.Fatalf("Gather sequence changed:\nA=%v\nB=%v", a, b)
	}
}

func hasKind(findings []model.FindingReport, kind string) bool {
	for _, f := range findings {
		if f.Kind == kind {
			return true
		}
	}
	return false
}

func hasFinding(findings []model.FindingReport, kind string, severity model.Severity) bool {
	for _, f := range findings {
		if f.Kind == kind && f.Severity == severity {
			return true
		}
	}
	return false
}

func assertNoSentinelInFindings(t *testing.T, findings []model.FindingReport) {
	t.Helper()
	sentinels := []string{"SENTINEL-AGENT-DESC", "SENTINEL-CARD", "SENTINEL-SKILL", "SENTINEL-TOOL-DESC", "SENTINEL-UNKNOWN"}
	for _, f := range findings {
		for _, field := range []string{f.Kind, f.SubjectKind, f.SubjectRef, f.Title, f.DetailHash} {
			for _, sentinel := range sentinels {
				if strings.Contains(field, sentinel) {
					t.Fatalf("sentinel %q leaked into finding field %q", sentinel, field)
				}
			}
		}
	}
}

func assertNoSentinelInGraph(t *testing.T, g identitysource.Graph) {
	t.Helper()
	sentinels := []string{"SENTINEL-AGENT-DESC", "SENTINEL-CARD", "SENTINEL-SKILL", "SENTINEL-TOOL-DESC", "SENTINEL-UNKNOWN"}
	var fields []string
	for _, id := range g.Identities {
		fields = append(fields, id.Ref, id.Kind, id.DisplayName)
		for k, v := range id.Attributes {
			fields = append(fields, k, v)
		}
	}
	for _, field := range fields {
		for _, sentinel := range sentinels {
			if strings.Contains(field, sentinel) {
				t.Fatalf("sentinel %q leaked into graph field %q", sentinel, field)
			}
		}
	}
}
