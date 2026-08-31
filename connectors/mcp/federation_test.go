// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

func TestParseFederatedRegistries(t *testing.T) {
	specs, err := parseFederatedRegistries(`[{"name":"github","url":"https://reg.example","allowlist":true}]`)
	if err != nil || len(specs) != 1 || !specs[0].Allowlist {
		t.Fatalf("parse: %v %+v", err, specs)
	}
	for _, bad := range []string{
		`[{"name":"","url":"https://x"}]`,                                 // missing name
		`[{"name":"a","url":""}]`,                                         // missing url
		`[{"name":"a","url":"https://x"},{"name":"a","url":"https://y"}]`, // duplicate
		`{"name":"a"}`, // not an array
	} {
		if _, err := parseFederatedRegistries(bad); err == nil {
			t.Errorf("config %s must be rejected", bad)
		}
	}
	if specs, err := parseFederatedRegistries(""); err != nil || specs != nil {
		t.Errorf("empty config must be (nil, nil), got %+v %v", specs, err)
	}
}

// allowlistStub serves a /v0.1 registry whose full enumeration yields the given
// active names (one page).
func allowlistStub(t *testing.T, names ...string) *httptest.Server {
	t.Helper()
	var records []string
	for _, n := range names {
		records = append(records, rec(n, "active"))
	}
	body := `{"servers":[` + strings.Join(records, ",") + `],"metadata":{}}`
	mux := http.NewServeMux()
	mux.HandleFunc("/v0.1/servers", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
	return httptest.NewServer(mux)
}

func TestFederatedAllowlistMembership(t *testing.T) {
	srv := allowlistStub(t, "io.github.acme/approved", "io.github.acme/other")
	defer srv.Close()

	s := &Source{cfg: config{
		timeout:             defaultTimeout,
		federatedRegistries: []federatedRegistrySpec{{Name: "github", URL: srv.URL, Allowlist: true}},
	}}
	fed, passFindings := s.federationSnapshot(t.Context(), fixedTime())
	if len(passFindings) != 0 {
		t.Fatalf("a healthy allowlist registry must yield no pass-level findings, got %s", titles(passFindings))
	}

	// A member server: Info provenance, never flagged.
	_, fs := fed.serverSignals(serverSpec{Name: "approved", RegistryName: "io.github.acme/approved"}, fixedTime())
	if len(fs) != 1 || fs[0].Kind != findingProvenance || fs[0].Severity != model.SeverityInfo {
		t.Errorf("an allowlisted server must yield one Info membership finding, got %s", titles(fs))
	}

	// An out-of-allowlist server: the MCP09 governance signal.
	_, fs = fed.serverSignals(serverSpec{Name: "rogue", RegistryName: "io.github.acme/rogue"}, fixedTime())
	if len(fs) != 1 || fs[0].Kind != findingShadow || fs[0].Severity != model.SeverityLow || !strings.Contains(fs[0].Title, "[MCP09]") {
		t.Errorf("an out-of-allowlist server must yield one Low [MCP09] finding, got %s", titles(fs))
	}

	// An unasserted server whose LOCAL name is not an allowlist entry is flagged…
	_, fs = fed.serverSignals(serverSpec{Name: "local"}, fixedTime())
	if len(fs) != 1 || fs[0].Kind != findingShadow {
		t.Errorf("an unasserted server must be reported out-of-allowlist, got %s", titles(fs))
	}
	// …but an operator that NAMES the server by its reverse-DNS registry name
	// resolves membership without registry_name (the registryClient.lookup rule).
	_, fs = fed.serverSignals(serverSpec{Name: "io.github.acme/approved"}, fixedTime())
	if len(fs) != 1 || fs[0].Kind != findingProvenance || fs[0].Severity != model.SeverityInfo {
		t.Errorf("a server named by its registry name must resolve allowlist membership, got %s", titles(fs))
	}
}

func TestFederatedAllowlistTruncationDegrades(t *testing.T) {
	// A registry whose pagination never completes (constant cursor) must degrade
	// to the unavailable finding — a PARTIAL allowlist would fabricate [MCP09]
	// out-of-allowlist findings for every server past the cut.
	mux := http.NewServeMux()
	mux.HandleFunc("/v0.1/servers", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"servers":[` + rec("io.github.acme/a", "active") + `],"metadata":{"nextCursor":"same"}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	s := &Source{cfg: config{
		timeout:             defaultTimeout,
		federatedRegistries: []federatedRegistrySpec{{Name: "github", URL: srv.URL, Allowlist: true}},
	}}
	fed, passFindings := s.federationSnapshot(t.Context(), fixedTime())
	if len(passFindings) != 1 || passFindings[0].Severity != model.SeverityInfo || !strings.Contains(passFindings[0].Title, "unavailable") {
		t.Fatalf("a truncated allowlist walk must degrade to one Info finding, got %s", titles(passFindings))
	}
	if issues, fs := fed.serverSignals(serverSpec{Name: "x", RegistryName: "io.github.acme/x"}, fixedTime()); len(issues) != 0 || len(fs) != 0 {
		t.Errorf("a truncated allowlist must never produce per-server flags, got %+v %s", issues, titles(fs))
	}
}

func TestFederationBothSourcesUnavailableDegrades(t *testing.T) {
	s := &Source{cfg: config{
		timeout:             defaultTimeout,
		federatedRegistries: []federatedRegistrySpec{{Name: "github", URL: "http://127.0.0.1:0", Allowlist: true}},
		dockerCatalog:       true,
		dockerCatalogURL:    "http://127.0.0.1:0",
	}}
	fed, passFindings := s.federationSnapshot(t.Context(), fixedTime())
	if len(passFindings) != 2 {
		t.Fatalf("both sources failing must degrade to exactly two Info findings, got %s", titles(passFindings))
	}
	for _, f := range passFindings {
		if f.Severity != model.SeverityInfo || !strings.Contains(f.Title, "unavailable") {
			t.Errorf("degradation finding must be Info/unavailable, got %q (%s)", f.Title, f.Severity)
		}
	}
	if len(fed.allowlists) != 0 || fed.docker != nil {
		t.Errorf("failed fetches must leave an empty snapshot, got %+v", fed)
	}
	issues, fs := fed.serverSignals(serverSpec{Name: "x", RegistryName: "io.github.acme/x", Command: "docker", Args: []string{"run", "mcp/brave-search:latest"}}, fixedTime())
	if len(issues) != 0 || len(fs) != 0 {
		t.Errorf("an empty snapshot must never produce per-server signals, got %+v %s", issues, titles(fs))
	}
}

func TestFederatedRegistryUnavailableDegrades(t *testing.T) {
	s := &Source{cfg: config{
		timeout:             defaultTimeout,
		federatedRegistries: []federatedRegistrySpec{{Name: "github", URL: "http://127.0.0.1:0", Allowlist: true}},
	}}
	fed, passFindings := s.federationSnapshot(t.Context(), fixedTime())
	if len(passFindings) != 1 || passFindings[0].Severity != model.SeverityInfo || !strings.Contains(passFindings[0].Title, "unavailable") {
		t.Fatalf("an unreachable federated registry must degrade to one Info finding, got %s", titles(passFindings))
	}
	// And — crucially — NO per-server membership flags from missing data.
	issues, fs := fed.serverSignals(serverSpec{Name: "x", RegistryName: "io.github.acme/x"}, fixedTime())
	if len(issues) != 0 || len(fs) != 0 {
		t.Errorf("an unavailable allowlist must never produce per-server signals, got %+v %s", issues, titles(fs))
	}
}

func TestFederatedOwnedNamespaceSync(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v0.1/servers", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("include_deleted") != "true" {
			t.Errorf("the namespace sync must request include_deleted=true")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"servers":[` +
			rec("io.github.acme/approved", "active") + `,` +
			rec("io.github.acme/rogue", "active") + `,` +
			rec("io.github.acme/old", "deleted") +
			`],"metadata":{}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	s := &Source{
		cfg: config{
			timeout:             defaultTimeout,
			federatedRegistries: []federatedRegistrySpec{{Name: "corp", URL: srv.URL}},
		},
		internal: mustInternal(t, []string{"io.github.acme"},
			[]internalEntry{{Name: "approved", RegistryName: "io.github.acme/approved"}}),
	}
	_, fs := s.federationSnapshot(t.Context(), fixedTime())
	var sawYank, sawUnmanaged, sawApproved bool
	for _, f := range fs {
		switch {
		case strings.Contains(f.Title, "deleted in federated registry corp"):
			sawYank = f.Severity == model.SeverityMedium && strings.Contains(f.Title, "[MCP04]")
		case strings.Contains(f.Title, "in federated registry corp is not in the approved internal registry"):
			sawUnmanaged = f.Severity == model.SeverityLow
		case strings.Contains(f.Title, "io.github.acme/approved"):
			sawApproved = true
		}
	}
	if !sawYank {
		t.Errorf("a deleted publication in a federated registry must raise a Medium [MCP04] yank: %s", titles(fs))
	}
	if !sawUnmanaged {
		t.Errorf("an unapproved publication in a federated registry must be flagged unmanaged: %s", titles(fs))
	}
	if sawApproved {
		t.Errorf("a vetted publication must not be re-reported by the federated sync: %s", titles(fs))
	}
}

// --- Docker MCP Catalog --------------------------------------------------------

// dockerCatalogFixture mirrors the live catalog.yaml v2 structure (verified
// 2026-06-10): mcp/ images are Docker-built (signed); other registries are
// community-built (unattested).
const dockerCatalogFixture = `version: 2
name: docker-mcp
registry:
  brave:
    description: Brave Search.
    title: Brave Search
    image: mcp/brave-search@sha256:b8938122495f7857c4cb81b77662f4737367665350700856d61724ce61109fac
  github:
    description: GitHub MCP server.
    title: GitHub
    image: ghcr.io/github/github-mcp-server@sha256:e3816a476a977cfb836e7d221510011436c654d11861db66ecfd826601aba6a4
  floaty:
    description: A catalog entry the feed itself does not pin.
    title: Floaty
    image: mcp/floaty:latest
`

const (
	braveDigest  = "sha256:b8938122495f7857c4cb81b77662f4737367665350700856d61724ce61109fac"
	wrongDigest  = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	githubDigest = "sha256:e3816a476a977cfb836e7d221510011436c654d11861db66ecfd826601aba6a4"
)

func dockerSnapshot(t *testing.T) *dockerCatalogSnapshot {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(dockerCatalogFixture))
	}))
	t.Cleanup(srv.Close)
	snap, err := fetchDockerCatalog(t.Context(), srv.URL, defaultTimeout)
	if err != nil {
		t.Fatalf("fetchDockerCatalog: %v", err)
	}
	return snap
}

func TestDockerCatalogPinMatch(t *testing.T) {
	snap := dockerSnapshot(t)
	spec := serverSpec{Name: "brave", Command: "docker", Args: []string{"run", "-i", "mcp/brave-search@" + braveDigest}}
	issues, fs := snap.serverSignals(spec, fixedTime())
	if len(issues) != 0 {
		t.Errorf("a pin-matching Docker-built image must raise no issues, got %+v", issues)
	}
	if len(fs) != 1 || fs[0].Severity != model.SeverityInfo || !strings.Contains(fs[0].Title, "matches the Docker MCP Catalog pinned digest") {
		t.Errorf("a pin match must yield one Info provenance finding, got %s", titles(fs))
	}
}

func TestDockerCatalogPinDrift(t *testing.T) {
	snap := dockerSnapshot(t)
	spec := serverSpec{Name: "brave", Command: "docker", Args: []string{"run", "mcp/brave-search@" + wrongDigest}}
	issues, fs := snap.serverSignals(spec, fixedTime())
	is, ok := issueByDetail(issues, "docker-pin-drift")
	if !ok || is.severity != model.SeverityMedium || is.mcp != "MCP04" {
		t.Errorf("a drifted digest must be a Medium MCP04 issue, got %+v ok=%v", is, ok)
	}
	if len(fs) != 0 {
		t.Errorf("a drifted pin must not also claim a provenance match, got %s", titles(fs))
	}
	// The raw digests stay out of titles (they ride the hashed detail).
	if strings.Contains(is.title, wrongDigest) {
		t.Errorf("the drifted digest must not ride in the title: %q", is.title)
	}
}

func TestDockerCatalogUnpinnedImage(t *testing.T) {
	snap := dockerSnapshot(t)
	spec := serverSpec{Name: "brave", Command: "docker", Args: []string{"run", "mcp/brave-search:latest"}}
	issues, _ := snap.serverSignals(spec, fixedTime())
	if is, ok := issueByDetail(issues, "docker-unpinned"); !ok || is.severity != model.SeverityLow {
		t.Errorf("an unpinned catalog image must be a Low issue, got %+v ok=%v", is, ok)
	}
}

func TestDockerCatalogCommunityUnattested(t *testing.T) {
	// "Catalog without signature degrades the score": a community-built entry
	// (non-mcp/ image) carries no Docker signature/SBOM/provenance attestation.
	snap := dockerSnapshot(t)
	spec := serverSpec{Name: "gh", Command: "docker", Args: []string{"run", "ghcr.io/github/github-mcp-server@" + githubDigest}}
	issues, fs := snap.serverSignals(spec, fixedTime())
	is, ok := issueByDetail(issues, "docker-community-unattested")
	if !ok || is.severity != model.SeverityLow || is.mcp != "MCP04" {
		t.Fatalf("a community-built entry must degrade the score (Low MCP04), got %+v ok=%v", is, ok)
	}
	// The pin still matches → the Info provenance finding coexists with the issue.
	if len(fs) != 1 || !strings.Contains(fs[0].Title, "matches") {
		t.Errorf("a matching community pin still yields the match finding, got %s", titles(fs))
	}

	// And it must flow into the posture GRADE.
	postFs := postureFindings(spec, catalog{server: InitializeResult{ServerInfo: serverInfo{Name: "gh"}}}, issues, fixedTime())
	if sc := scoreFinding(t, postFs); !strings.Contains(sc.Title, "grade A") && sc.Severity == model.SeverityInfo {
		t.Errorf("federation issues must reflect in the posture summary, got %q", sc.Title)
	}
	if _, ok := findByTag(postFs, "MCP04"); !ok {
		t.Errorf("the unattested-entry issue must surface as an [MCP04] posture finding: %s", titles(postFs))
	}
}

func TestDockerCatalogEntryUnpinned(t *testing.T) {
	// The CATALOG entry itself carries no digest: there is no pin to match or
	// drift from, so neither claim may be fabricated — only the honest
	// "entry unpinned" issue (plus nothing else even when the fleet pins).
	snap := dockerSnapshot(t)
	spec := serverSpec{Name: "f", Command: "docker", Args: []string{"run", "mcp/floaty@" + wrongDigest}}
	issues, fs := snap.serverSignals(spec, fixedTime())
	if is, ok := issueByDetail(issues, "docker-catalog-entry-unpinned"); !ok || is.severity != model.SeverityLow {
		t.Errorf("an unpinned catalog entry must raise the honest Low issue, got %+v ok=%v", is, ok)
	}
	if _, ok := issueByDetail(issues, "docker-pin-drift"); ok {
		t.Errorf("no drift can exist against a pinless entry")
	}
	if len(fs) != 0 {
		t.Errorf("no match claim may be fabricated against a pinless entry, got %s", titles(fs))
	}
	// Same for an unpinned fleet image: the entry-unpinned issue, never the
	// "while the Docker MCP Catalog pins one" title.
	issues, _ = snap.serverSignals(serverSpec{Name: "f", Command: "docker", Args: []string{"run", "mcp/floaty:latest"}}, fixedTime())
	if _, ok := issueByDetail(issues, "docker-unpinned"); ok {
		t.Errorf("the fleet-unpinned title would falsely assert the catalog pins this image")
	}
	if _, ok := issueByDetail(issues, "docker-catalog-entry-unpinned"); !ok {
		t.Errorf("the honest entry-unpinned issue must fire, got %+v", issues)
	}
}

func TestDockerCatalogOffCatalogImageIsSilent(t *testing.T) {
	snap := dockerSnapshot(t)
	spec := serverSpec{Name: "x", Command: "docker", Args: []string{"run", "registry.corp.example/internal/tool@" + wrongDigest}}
	issues, fs := snap.serverSignals(spec, fixedTime())
	if len(issues) != 0 || len(fs) != 0 {
		t.Errorf("an off-catalog image proves nothing and must stay silent, got %+v %s", issues, titles(fs))
	}
}

func TestDockerCatalogUnavailableDegrades(t *testing.T) {
	s := &Source{cfg: config{timeout: defaultTimeout, dockerCatalog: true, dockerCatalogURL: "http://127.0.0.1:0"}}
	fed, fs := s.federationSnapshot(t.Context(), fixedTime())
	if len(fs) != 1 || fs[0].Severity != model.SeverityInfo || !strings.Contains(fs[0].Title, "Docker MCP Catalog feed unavailable") {
		t.Fatalf("an unreachable catalog feed must degrade to one Info finding, got %s", titles(fs))
	}
	if fed.docker != nil {
		t.Errorf("a failed fetch must leave no snapshot (no guessed checks)")
	}
}

// TestFederationIssuesEmitWithPostureScanOff: posture_scan gates the text scan
// and the grade — NOT the federation supply-chain verdicts. With the scanner off
// and docker_catalog on, a pin/attestation issue must still surface (otherwise
// the pin-match Info channel keeps emitting while the failure channel is silently
// gated off by an unrelated flag).
func TestFederationIssuesEmitWithPostureScanOff(t *testing.T) {
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(dockerCatalogFixture))
	}))
	defer feed.Close()

	spec := helperSpec("dockerized")
	spec.Args = append(spec.Args, "mcp/brave-search:latest") // ignored by the helper; seen by imageRefsOf
	s := &Source{cfg: config{
		servers:          []serverSpec{spec},
		timeout:          defaultTimeout,
		postureScan:      false,
		dockerCatalog:    true,
		dockerCatalogURL: feed.URL,
	}}
	sink := &fakeSink{}
	if err := s.Gather(t.Context(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var sawUnpinned, sawGrade bool
	for _, f := range sink.findings() {
		if strings.Contains(f.Title, "runs without a digest pin") {
			sawUnpinned = true
		}
		if strings.Contains(f.Title, "posture: grade") {
			sawGrade = true
		}
	}
	if !sawUnpinned {
		t.Errorf("the federation verdict must emit with posture_scan=false: %s", titles(sink.findings()))
	}
	if sawGrade {
		t.Errorf("posture_scan=false must not emit a grade summary")
	}
}

func TestSplitImageRef(t *testing.T) {
	cases := []struct{ ref, base, tag, digest string }{
		{"mcp/brave-search@" + braveDigest, "mcp/brave-search", "", braveDigest},
		{"mcp/brave-search:1.2@" + braveDigest, "mcp/brave-search", "1.2", braveDigest},
		{"mcp/brave-search:latest", "mcp/brave-search", "latest", ""},
		{"localhost:5000/img", "localhost:5000/img", "", ""},
		{"localhost:5000/img:v1", "localhost:5000/img", "v1", ""},
		{"mcp/x@md5:abc", "mcp/x", "", ""}, // only sha256 digests count
	}
	for _, c := range cases {
		base, tag, digest := splitImageRef(c.ref)
		if base != c.base || tag != c.tag || digest != c.digest {
			t.Errorf("splitImageRef(%q) = (%q,%q,%q), want (%q,%q,%q)", c.ref, base, tag, digest, c.base, c.tag, c.digest)
		}
	}
}
