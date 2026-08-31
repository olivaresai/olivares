// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// federation.go —: federation of EXTERNAL MCP catalogs beyond the official
// metaregistry. The official registry documents itself as a METAREGISTRY whose
// intended consumers are downstream SUBREGISTRIES (ecosystem-vision doc); the
// parallel catalogs an org actually depends on are:
//
//   - Federated /v0.1 registries: GitHub's org/enterprise MCP registries are
//     BRING-YOUR-OWN — GitHub hosts only the allowlist POLICY; the registry itself
//     is customer-hosted and MUST implement the v0.1 registry OpenAPI
//     (GET /v0.1/servers + /versions/{latest|version}; docs.github.com
//     "Configure an MCP registry", verified 2026-06-10). Azure API Center and the
//     plane's own embedded sub-registry (subregistry.go) speak the same surface,
//     so ONE pinned client federates them all (registryAPIPath).
//   - The Docker MCP Catalog: a YAML feed of catalog entries with sha256-PINNED
//     images. Docker-BUILT entries (mcp/ namespace) are "built and digitally
//     signed by Docker" with signed SBOMs + provenance attestations; community
//     entries get only best-effort, UNATTESTED verification (docs.docker.com MCP
//     catalog + FAQ, verified 2026-06-10). The feed itself is undocumented/
//     unstable — treated like every preview source: failure degrades to one Info
//     finding, never fabricated drift.
//
// What federation feeds (deny-closed, minimal-data):
//   - allowlist membership: a server present in a federated ALLOWLIST registry is
//     Info provenance; an introspected server ABSENT from the org's allowlist is a
//     governance signal (MCP09 family) — only ever raised from a successfully
//     fetched snapshot, never from an error.
//   - Docker pin checks (SCORED posture issues, MCP04): a fleet image that drifted
//     from the catalog's pinned digest (rug-pull shape), an unpinned catalog image
//     (the pin cannot protect), and a community-built entry without Docker's
//     signature/attestations ("catalog without signature degrades the score").
//     The cryptographic verification of the attestations themselves is the
//     admission flow (modules/catalog mcpadmission, core/secure/modelsign) — the
//     connector only checks pins and provenance CLASS; it cannot import /core.

// defaultDockerCatalogURL is the live Docker MCP Catalog v2 feed (the same feed
// Docker Desktop and the MCP gateway consume; undocumented but real — verified
// live 2026-06-10, ~245 sha256-pinned entries).
const defaultDockerCatalogURL = "https://desktop.docker.com/mcp/catalog/v2/catalog.yaml"

// maxCatalogBody caps the Docker catalog feed (the live feed is ~0.5 MiB).
const maxCatalogBody = 8 << 20

// subjectDockerCatalog is the SubjectRef for catalog-level findings.
const subjectDockerCatalog = "desktop.docker.com/mcp/catalog"

// federatedRegistrySpec is one operator-declared federated registry implementing
// the pinned /v0.1 registry OpenAPI. Allowlist marks it as the org's curated
// allowlist (e.g. the GitHub BYO registry): introspected servers are then checked
// for MEMBERSHIP, not just provenance.
type federatedRegistrySpec struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	Allowlist bool   `json:"allowlist"`
}

// parseFederatedRegistries decodes the federated_registries config JSON.
func parseFederatedRegistries(raw string) ([]federatedRegistrySpec, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var specs []federatedRegistrySpec
	if err := json.Unmarshal([]byte(raw), &specs); err != nil {
		return nil, fmt.Errorf("mcp: parse %q: %w", cfgFederatedRegistries, err)
	}
	seen := map[string]struct{}{}
	for i, fr := range specs {
		name := strings.TrimSpace(fr.Name)
		if name == "" || strings.TrimSpace(fr.URL) == "" {
			return nil, fmt.Errorf("mcp: %s: entry #%d needs both name and url", cfgFederatedRegistries, i)
		}
		if _, dup := seen[name]; dup {
			return nil, fmt.Errorf("mcp: %s: duplicate registry name %q", cfgFederatedRegistries, name)
		}
		seen[name] = struct{}{}
	}
	return specs, nil
}

// federationState is the per-Gather snapshot of every federated catalog: fetched
// ONCE per pass, then consulted per server without further network. A snapshot
// that failed to fetch is absent (nil/empty) — per-server checks then stay silent
// for it, because a check against missing data would fabricate findings.
type federationState struct {
	allowlists []allowlistSnapshot
	docker     *dockerCatalogSnapshot
}

// allowlistSnapshot is the active-name set of one successfully fetched allowlist
// registry.
type allowlistSnapshot struct {
	name  string
	names map[string]struct{}
}

// federationSnapshot fetches every configured federated catalog once and returns
// the snapshot plus the pass-level findings (per-registry sync classification for
// owned namespaces, and one Info degradation finding per unreachable source).
func (s *Source) federationSnapshot(ctx context.Context, at time.Time) (federationState, []model.FindingReport) {
	var (
		state federationState
		out   []model.FindingReport
	)
	for _, fr := range s.cfg.federatedRegistries {
		client := newRegistryClient(fr.URL, s.cfg.timeout)
		// Owned-namespace reconciliation against the federated registry (the same
		// yank/unmanaged classification the official sync runs, labeled per registry).
		for _, ns := range s.internal.ownedNamespaceList() {
			records, err := client.listNamespace(ctx, ns)
			if err != nil {
				out = append(out, federatedUnavailableFinding(fr.Name, ns, err, at))
				continue
			}
			out = append(out, s.classifyPublished(records, ns, " in federated registry "+fr.Name, at)...)
		}
		if !fr.Allowlist {
			continue
		}
		names, err := client.listActiveNames(ctx)
		if err != nil {
			out = append(out, federatedUnavailableFinding(fr.Name, "", err, at))
			continue
		}
		state.allowlists = append(state.allowlists, allowlistSnapshot{name: fr.Name, names: names})
	}
	if s.cfg.dockerCatalog {
		snap, err := fetchDockerCatalog(ctx, s.cfg.dockerCatalogURL, s.cfg.timeout)
		if err != nil {
			out = append(out, dockerCatalogUnavailableFinding(err, at))
		} else {
			state.docker = snap
		}
	}
	return state, out
}

// classifyPublished applies the owned-namespace sync classification (yank /
// unmanaged / discovered) to records fetched from any registry; where names which
// registry the classification came from (e.g. " in federated registry github").
func (s *Source) classifyPublished(records []registryRecord, ns, where string, at time.Time) []model.FindingReport {
	var out []model.FindingReport
	for _, srv := range records {
		switch status := srv.status(); status {
		case "deleted", "deprecated":
			out = append(out, model.FindingReport{
				Kind:        findingProvenance,
				Severity:    model.SeverityMedium,
				SubjectKind: subjectMCPServer,
				SubjectRef:  srv.Server.Name,
				Title:       "[MCP04] MCP server in owned namespace " + ns + " is " + status + where + " (supply-chain: possible yank/upgrade-risk)",
				DetailHash:  redact.Hash("mcp-fed-yank name=" + srv.Server.Name + " namespace=" + ns + " status=" + status + " where=" + where),
				OccurredAt:  at,
			})
		default:
			if s.internal.knownRegistryName(srv.Server.Name) {
				continue // vetted publication; the official-sync pass already inventories it
			}
			out = append(out, model.FindingReport{
				Kind:        findingProvenance,
				Severity:    model.SeverityLow,
				SubjectKind: subjectMCPServer,
				SubjectRef:  srv.Server.Name,
				Title:       "[MCP04] MCP server published under owned namespace " + ns + where + " is not in the approved internal registry (unmanaged publication)",
				DetailHash:  redact.Hash("mcp-fed-unmanaged name=" + srv.Server.Name + " namespace=" + ns + " where=" + where),
				OccurredAt:  at,
			})
		}
	}
	return out
}

// listActiveNames enumerates a registry's ACTIVE server names (the allowlist set),
// paging like listNamespace but without a search term. Deleted/deprecated records
// are not allowlist entries.
//
// UNLIKE listNamespace, an INCOMPLETE walk here is an ERROR, never a result: the
// sync path's truncation only ever misses positive findings (fail-quiet), but a
// truncated ALLOWLIST would flag every server whose entry lies past the cut as
// out-of-allowlist — fabricated [MCP09] findings from missing data, exactly what
// the deny-closed rule forbids. Callers degrade to the honest unavailable finding.
func (c *registryClient) listActiveNames(ctx context.Context) (map[string]struct{}, error) {
	names := map[string]struct{}{}
	cursor := ""
	seen := map[string]struct{}{}
	for page := 0; page < maxSyncPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		res, err := c.fetchPage(ctx, "", cursor, false)
		if err != nil {
			return nil, err
		}
		for _, r := range res.Servers {
			if r.status() == "active" && r.Server.Name != "" {
				names[r.Server.Name] = struct{}{}
			}
		}
		if res.Metadata.NextCursor == "" {
			return names, nil // the walk COMPLETED — the only success exit
		}
		if _, dup := seen[res.Metadata.NextCursor]; dup {
			return nil, fmt.Errorf("mcp registry: allowlist enumeration repeated cursor (broken pagination; a partial allowlist would fabricate out-of-allowlist findings)")
		}
		seen[res.Metadata.NextCursor] = struct{}{}
		cursor = res.Metadata.NextCursor
	}
	return nil, fmt.Errorf("mcp registry: allowlist enumeration exceeded %d pages with more remaining (a truncated allowlist would fabricate out-of-allowlist findings)", maxSyncPages)
}

// serverSignals returns the per-server federation outputs: SCORED posture issues
// (Docker pin/provenance-class checks) and unscored provenance/membership findings.
// Pure function of the snapshot — no network.
func (f federationState) serverSignals(spec serverSpec, at time.Time) ([]postureIssue, []model.FindingReport) {
	var (
		issues   []postureIssue
		findings []model.FindingReport
	)
	// Membership matches by the operator-asserted reverse-DNS RegistryName, falling
	// back to the local Name (the same rule as registryClient.lookup — an operator
	// that names the fleet server by its registry name must resolve identically).
	want := spec.RegistryName
	if want == "" {
		want = spec.Name
	}
	for _, al := range f.allowlists {
		if _, ok := al.names[want]; ok {
			findings = append(findings, model.FindingReport{
				Kind:        findingProvenance,
				Severity:    model.SeverityInfo,
				SubjectKind: subjectMCPServer,
				SubjectRef:  spec.Name,
				Title:       "MCP server is present in federated allowlist registry " + al.name,
				DetailHash:  redact.Hash("mcp-allowlist-member server=" + spec.Name + " registry=" + al.name + " name=" + want),
				OccurredAt:  at,
			})
			continue
		}
		// Asserted-but-absent OR unasserted: either way this running server is not
		// an entry of the org's allowlist registry — the out-of-allowlist shape of
		// MCP09. Raised ONLY from a successfully fetched snapshot.
		findings = append(findings, model.FindingReport{
			Kind:        findingShadow,
			Severity:    model.SeverityLow,
			SubjectKind: subjectMCPServer,
			SubjectRef:  spec.Name,
			Title:       "[MCP09] MCP server is not present in federated allowlist registry " + al.name + " (out-of-allowlist server)",
			DetailHash:  redact.Hash("mcp-allowlist-absent server=" + spec.Name + " registry=" + al.name + " name=" + spec.RegistryName),
			OccurredAt:  at,
		})
	}
	if f.docker != nil {
		di, df := f.docker.serverSignals(spec, at)
		issues = append(issues, di...)
		findings = append(findings, df...)
	}
	return issues, findings
}

// federatedUnavailableFinding reports that a federated registry could not be
// consulted this pass (namespace-scoped when the failure was a namespace sync).
func federatedUnavailableFinding(registry, ns string, err error, at time.Time) model.FindingReport {
	subject := registry
	title := "federated MCP registry " + registry + " unavailable this pass (membership/sync checks degraded, never fabricated)"
	if ns != "" {
		subject = ns
		title = "federated MCP registry " + registry + " sync unavailable for owned namespace " + ns + " this pass"
	}
	return model.FindingReport{
		Kind:        findingProvenance,
		Severity:    model.SeverityInfo,
		SubjectKind: subjectMCPServer,
		SubjectRef:  subject,
		Title:       title,
		DetailHash:  redact.Hash("mcp-fed-unavailable registry=" + registry + " ns=" + ns + " err=" + err.Error()),
		OccurredAt:  at,
	}
}

// --- Docker MCP Catalog -------------------------------------------------------

// dockerCatalogSnapshot indexes the catalog feed by image base (e.g.
// "mcp/brave-search", "ghcr.io/github/github-mcp-server").
type dockerCatalogSnapshot struct {
	byImage map[string]dockerCatalogPin
}

// dockerCatalogPin is what the catalog pins for one entry: the sha256 digest and
// whether the image is Docker-BUILT (the mcp/ namespace — signed by Docker with
// SBOM/provenance attestations) vs community-built (best-effort, unattested).
type dockerCatalogPin struct {
	digest      string // "sha256:<hex>", "" when the feed entry itself is unpinned
	dockerBuilt bool
}

// dockerCatalogFile is the catalog.yaml v2 subset this connector reads.
type dockerCatalogFile struct {
	Version  int    `yaml:"version"`
	Name     string `yaml:"name"`
	Registry map[string]struct {
		Image string `yaml:"image"`
	} `yaml:"registry"`
}

// fetchDockerCatalog fetches and indexes the Docker MCP Catalog feed.
func fetchDockerCatalog(ctx context.Context, feedURL string, timeout time.Duration) (*dockerCatalogSnapshot, error) {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mcp docker catalog: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxCatalogBody))
		return nil, fmt.Errorf("mcp docker catalog: http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCatalogBody))
	if err != nil {
		return nil, fmt.Errorf("mcp docker catalog: read: %w", err)
	}
	var file dockerCatalogFile
	if err := yaml.Unmarshal(body, &file); err != nil {
		return nil, fmt.Errorf("mcp docker catalog: decode: %w", err)
	}
	if len(file.Registry) == 0 {
		return nil, fmt.Errorf("mcp docker catalog: no registry entries parsed (feed format drift?)")
	}
	snap := &dockerCatalogSnapshot{byImage: map[string]dockerCatalogPin{}}
	for _, entry := range file.Registry {
		base, _, digest := splitImageRef(entry.Image)
		if base == "" {
			continue
		}
		snap.byImage[base] = dockerCatalogPin{digest: digest, dockerBuilt: strings.HasPrefix(base, "mcp/")}
	}
	return snap, nil
}

// serverSignals checks a server spec's container image references against the
// catalog. Only images the CATALOG knows are graded (an off-catalog image proves
// nothing); the issues are SCORED into the server's posture grade.
func (d *dockerCatalogSnapshot) serverSignals(spec serverSpec, at time.Time) ([]postureIssue, []model.FindingReport) {
	var (
		issues   []postureIssue
		findings []model.FindingReport
	)
	for _, ref := range imageRefsOf(spec) {
		base, _, digest := splitImageRef(ref)
		pin, ok := d.byImage[base]
		if !ok {
			continue
		}
		switch {
		case pin.digest == "":
			// The CATALOG entry itself carries no digest — there is no pin to match
			// or drift from, so neither claim may be fabricated (degraded honestly).
			issues = append(issues, postureIssue{
				mcp: "MCP04", severity: model.SeverityLow,
				title:     "container image " + base + " has an unpinned Docker MCP Catalog entry (no catalog digest exists to verify the running image against)",
				detailKey: "docker-catalog-entry-unpinned image=" + base,
			})
		case digest == "":
			issues = append(issues, postureIssue{
				mcp: "MCP04", severity: model.SeverityLow,
				title:     "container image " + base + " runs without a digest pin while the Docker MCP Catalog pins one (the catalog pin cannot protect an unpinned fleet image)",
				detailKey: "docker-unpinned image=" + base,
			})
		case digest != pin.digest:
			issues = append(issues, postureIssue{
				mcp: "MCP04", severity: model.SeverityMedium,
				title:     "container image " + base + " digest drifted from the Docker MCP Catalog pin (the image the fleet runs is not the image the catalog pins — rug-pull shape)",
				detailKey: "docker-pin-drift image=" + base + " running=" + digest + " pinned=" + pin.digest,
			})
		default:
			findings = append(findings, model.FindingReport{
				Kind:        findingProvenance,
				Severity:    model.SeverityInfo,
				SubjectKind: subjectMCPServer,
				SubjectRef:  spec.Name,
				Title:       "container image " + base + " matches the Docker MCP Catalog pinned digest",
				DetailHash:  redact.Hash("mcp-docker-pin-match server=" + spec.Name + " image=" + base + " digest=" + digest),
				OccurredAt:  at,
			})
		}
		if !pin.dockerBuilt {
			// "Catalog without signature degrades the score": a community-built
			// entry carries NO Docker signature/SBOM/provenance attestations
			// (best-effort verification only) — hold it to the admission flow
			// before trusting it.
			issues = append(issues, postureIssue{
				mcp: "MCP04", severity: model.SeverityLow,
				title:     "container image " + base + " is a community-built Docker MCP Catalog entry (no Docker signature/SBOM/provenance attestation; best-effort verification only) — verify via signed admission before approving",
				detailKey: "docker-community-unattested image=" + base,
			})
		}
	}
	return issues, findings
}

// imageRefsOf extracts candidate container-image references from a server spec's
// command line (the docker-run shape of a catalog MCP server). Tokens also split
// on "=" so --image=ref forms match.
func imageRefsOf(spec serverSpec) []string {
	var out []string
	tokens := append([]string{spec.Command}, spec.Args...)
	for _, tok := range tokens {
		for _, part := range strings.Split(tok, "=") {
			if part = strings.TrimSpace(part); part != "" && strings.ContainsAny(part, "/@") {
				out = append(out, part)
			}
		}
	}
	return out
}

// splitImageRef splits an OCI image reference into base, tag and digest
// ("mcp/foo:1.2@sha256:<hex>" → "mcp/foo", "1.2", "sha256:<hex>").
func splitImageRef(ref string) (base, tag, digest string) {
	ref = strings.TrimSpace(ref)
	if i := strings.Index(ref, "@"); i >= 0 {
		digest = ref[i+1:]
		ref = ref[:i]
		if !strings.HasPrefix(digest, "sha256:") {
			digest = ""
		}
	}
	// A colon AFTER the last slash is a tag separator (a colon before it is a
	// registry port, e.g. localhost:5000/img).
	slash := strings.LastIndexByte(ref, '/')
	if i := strings.LastIndexByte(ref, ':'); i > slash {
		tag = ref[i+1:]
		ref = ref[:i]
	}
	return ref, tag, digest
}

// dockerCatalogUnavailableFinding reports that the Docker MCP Catalog feed could
// not be consulted this pass — pin/provenance checks are degraded, never guessed.
func dockerCatalogUnavailableFinding(err error, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        findingProvenance,
		Severity:    model.SeverityInfo,
		SubjectKind: subjectMCPServer,
		SubjectRef:  subjectDockerCatalog,
		Title:       "Docker MCP Catalog feed unavailable this pass (image pin/provenance checks degraded, never fabricated)",
		DetailHash:  redact.Hash("mcp-docker-catalog-unavailable err=" + err.Error()),
		OccurredAt:  at,
	}
}
