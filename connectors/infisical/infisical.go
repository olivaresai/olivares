// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package infisical is the Olivares AI identity connector for Infisical. It
// discovers the organization's machine identities (non-human identities backed by
// universal-auth), its human members, its projects (workspaces), and the
// membership edges that bind identities and members into each project, and it
// exposes them as an identitysource.Graph to module VI (governance).
//
// It is read-only and minimal-data (docs/SECURITY-HARDENING.md-3): it performs only GETs against
// the Infisical REST API (the one exception is the universal-auth login, a POST
// that exchanges the operator's machine-identity client_id/client_secret for a
// short-lived access token — the spec'd auth handshake, never a mutation of the
// system it reads). The operator credential (client_secret, or a pre-issued
// access_token) is held in memory only, applied per request as a bearer header,
// and is NEVER logged or persisted. The Graph carries identity METADATA only —
// identity ids, names, member emails, project names, memberships — never a secret
// value, never a project's secrets.
//
// The roster (identities, members, projects, project memberships) travels the
// typed Snapshot Graph (the pattern); group/role memberships travel ONLY
// there, never as observations. Gather emits the PERMITTED side of the
// permitted-vs-observed diff: an Infisical project IS a secrets scope — a
// resource — so each project membership is a genuine identity→resource permitted
// grant, emitted as a model.SignalPolicy EdgeObservation. With no credential
// configured Snapshot returns an empty Graph and Gather emits nothing (offline),
// no error. It imports only the SDK, the Apache identitysource contract and the
// shared read-only httpx client — never the engine.
package infisical

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/connectors/internal/httpx"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.infisical"

// Default configuration values.
const (
	defaultBaseURL  = "https://app.infisical.com"
	defaultMaxPages = 50
)

// maxLoginBody bounds how much of the universal-auth login response is read.
const maxLoginBody = 64 << 10 // 64 KiB

// resProject is the ResourceKind of a PERMITTED grant: an Infisical project is a
// secrets scope, the resource a project membership grants access to.
const resProject = "infisical.project"

// pageLimit is the page size for the paginated identity-memberships listings (the
// server's documented default; the required totalCount drives the loop).
const pageLimit = 100

// Built-in Infisical project role slugs. A custom role's row carries roleCustom
// plus the custom slug in customRoleSlug.
const (
	roleAdmin    = "admin"
	roleMember   = "member"
	roleViewer   = "viewer"
	roleNoAccess = "no-access"
	roleCustom   = "custom"
)

// Source is the Infisical identity connector. It satisfies sdk.SourceConnector
// (the project-membership grant scan) and identitysource.GraphProvider (the
// identity roster).
type Source struct {
	baseURL      string
	orgID        string
	clientID     string
	clientSecret string
	accessToken  string
	loginURL     string
	maxPages     int

	doer httpx.Doer       // injectable transport (tests); nil => http.DefaultClient
	now  func() time.Time // injectable clock (tests); nil => time.Now

	// token is the resolved bearer used for read calls: either the configured
	// access_token, or the token minted by the universal-auth login. Held in
	// memory only; never logged or persisted.
	token string
}

// Compile-time proof that Source satisfies both contracts.
var (
	_ sdk.SourceConnector          = (*Source)(nil)
	_ identitysource.GraphProvider = (*Source)(nil)
)

// New returns an Infisical connector with default configuration.
func New() *Source {
	return &Source{
		baseURL:  defaultBaseURL,
		maxPages: defaultMaxPages,
	}
}

// Descriptor returns the connector's self-description and declared configuration.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Infisical",
		Description: "Reads Infisical machine identities, human members, projects and memberships (read-only metadata; never secret values).",
		ConfigFields: []sdk.ConfigField{
			{Key: "base_url", Type: sdk.FieldString, Default: defaultBaseURL, Description: "Infisical API base URL (https://app.infisical.com or a self-hosted host)."},
			{Key: "org_id", Type: sdk.FieldString, Description: "Organization id to enumerate. Required for online discovery; empty = offline (empty graph)."},
			{Key: "client_id", Type: sdk.FieldString, Description: "Universal-auth machine-identity client id (paired with client_secret)."},
			{Key: "client_secret", Type: sdk.FieldString, Secret: true, Description: "Universal-auth machine-identity client secret (read-only; never persisted). Exchanged for a short-lived token."},
			{Key: "access_token", Type: sdk.FieldString, Secret: true, Description: "Pre-issued access token, used directly as a bearer (alternative to client_id/client_secret; never persisted)."},
			{Key: "login_url", Type: sdk.FieldString, Description: "Universal-auth login endpoint override (default {base_url}/api/v1/auth/universal-auth/login)."},
			{Key: "max_pages", Type: sdk.FieldInt, Default: strconv.Itoa(defaultMaxPages), Description: "Pagination safety bound per listing."},
		},
	}
}

// Open reads configuration. It never dials here and never fails for a missing
// credential: with no credential (or no org_id) the connector runs offline and
// Snapshot returns an empty Graph. The login (if needed) is deferred to Snapshot.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	if v := cfg.Get("base_url"); v != "" {
		s.baseURL = strings.TrimRight(v, "/")
	}
	s.orgID = cfg.Get("org_id")
	s.clientID = cfg.Get("client_id")
	s.clientSecret = cfg.Get("client_secret")
	s.accessToken = cfg.Get("access_token")
	if v := cfg.Get("login_url"); v != "" {
		s.loginURL = v
	} else {
		s.loginURL = s.baseURL + "/api/v1/auth/universal-auth/login"
	}
	if n := cfg.GetInt("max_pages", s.maxPages); n > 0 {
		s.maxPages = n
	}
	return nil
}

// Gather scans the org's project memberships and emits one EdgeObservation per
// (identity, project) PERMITTED grant with Source=model.SignalPolicy — the
// PERMITTED side of the permitted-vs-observed diff. An Infisical project IS a
// secrets scope (a resource), so a project membership is a genuine
// identity→resource grant; group/role memberships keep traveling ONLY the typed
// Snapshot Graph. The membership's roles classify the AccessMode (see grantMode)
// and ride the ToolRef as sorted slugs. A disabled member is NOT skipped: a
// disabled account still HOLDS its grant (the roster carries Disabled as the
// governance signal). It NEVER fetches a secret path — membership listings only.
//
// One clock capture stamps every observation of a run (it keeps first/last_seen
// stable); any transport or Emit error simply returns — the engine retries, and
// a re-emitted edge converges on its natural key (origin, resource, mode: the
// engine's upsert OR-merges the flags; only the occurrence count accumulates
// per pass). An origin the org-scope roster would not
// contain is never emitted as an edge: distinct stray origins are counted and
// surfaced as a single Info coverage finding (docs/SECURITY-HARDENING.md — never silent).
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.orgID == "" || !s.configured() {
		return nil // offline
	}
	now := s.clock()

	token, err := s.resolveToken(ctx)
	if err != nil {
		return err
	}
	client := httpx.New(s.baseURL, s.doer, httpx.Bearer(token), nil)

	// Convergence sets: the refs the org-scope roster (this connector's own
	// Snapshot) would carry. Same fetch helpers as Snapshot, independent calls.
	rostered := map[string]struct{}{}
	machine, err := s.fetchOrgIdentities(ctx, client)
	if err != nil {
		return err
	}
	for _, id := range machine {
		rostered[id.Ref] = struct{}{}
	}
	human, err := s.fetchOrgMembers(ctx, client)
	if err != nil {
		return err
	}
	for _, id := range human {
		rostered[id.Ref] = struct{}{}
	}

	projects, err := s.fetchProjects(ctx, client)
	if err != nil {
		return err
	}

	strays := map[string]struct{}{}
	for _, p := range projects {
		if err := ctx.Err(); err != nil {
			return err
		}
		pIdents, err := s.fetchProjectIdentities(ctx, client, p.id)
		if err != nil {
			return err
		}
		pMembers, err := s.fetchProjectMembers(ctx, client, p.id)
		if err != nil {
			return err
		}
		for _, gr := range append(pIdents, pMembers...) {
			mode, ok := grantMode(gr.roles)
			if !ok {
				continue // the roles grant nothing (no-access only) — no edge
			}
			if _, ok := rostered[gr.ref]; !ok {
				strays[gr.ref] = struct{}{} // would not converge; finding, not edge
				continue
			}
			if err := sink.Emit(ctx, model.EdgeObservation{
				OriginKind:   "identity",
				OriginRef:    gr.ref,
				ResourceKind: resProject,
				ResourceRef:  p.id,
				Mode:         mode,
				Source:       model.SignalPolicy,
				Confidence:   model.ConfidenceAttributed,
				ToolRef:      roleSlugs(gr.roles),
				ObservedAt:   now,
			}); err != nil {
				return err
			}
		}
	}

	// Never-silent convergence guard: exactly one Info finding per run names how
	// many distinct origins were suppressed (precedent: claude-compliance coverage).
	if n := len(strays); n > 0 {
		if err := sink.Emit(ctx, model.FindingReport{
			Kind:        "coverage",
			Severity:    model.SeverityInfo,
			SubjectKind: "identity_source",
			SubjectRef:  Name,
			Title:       strconv.Itoa(n) + " permitted-grant origins outside the rostered identity set were not emitted",
			OccurredAt:  now,
		}); err != nil {
			return err
		}
	}
	return nil
}

// Close releases resources; the connector holds none (no long-lived connection).
func (s *Source) Close(context.Context) error { return nil }

// configured reports whether a credential is present that can authenticate reads.
func (s *Source) configured() bool {
	if s.accessToken != "" {
		return true
	}
	return s.clientID != "" && s.clientSecret != ""
}

// Snapshot authenticates read-only and assembles the identity graph: machine
// identities (NHIs) and human members of the org, the projects, and the
// memberships wiring identities/members into each project. With no credential or
// no org it returns an empty Graph (offline). It never returns credential material.
func (s *Source) Snapshot(ctx context.Context) (identitysource.Graph, error) {
	g := identitysource.Graph{Source: identitysource.SourceInfisical, CapturedAt: s.clock()}
	if s.orgID == "" || !s.configured() {
		return g, nil // offline
	}

	token, err := s.resolveToken(ctx)
	if err != nil {
		return identitysource.Graph{}, err
	}
	client := httpx.New(s.baseURL, s.doer, httpx.Bearer(token), nil)

	machine, err := s.fetchOrgIdentities(ctx, client)
	if err != nil {
		return identitysource.Graph{}, err
	}
	g.Identities = append(g.Identities, machine...)

	human, err := s.fetchOrgMembers(ctx, client)
	if err != nil {
		return identitysource.Graph{}, err
	}
	g.Identities = append(g.Identities, human...)

	projects, err := s.fetchProjects(ctx, client)
	if err != nil {
		return identitysource.Graph{}, err
	}
	for _, p := range projects {
		g.Collections = append(g.Collections, identitysource.Collection{
			Ref:         p.id,
			Kind:        identitysource.KindGroup,
			DisplayName: p.name,
			Source:      identitysource.SourceInfisical,
			Attributes:  map[string]string{"kind": "project"},
		})

		pIdents, err := s.fetchProjectIdentities(ctx, client, p.id)
		if err != nil {
			return identitysource.Graph{}, err
		}
		pMembers, err := s.fetchProjectMembers(ctx, client, p.id)
		if err != nil {
			return identitysource.Graph{}, err
		}
		for _, m := range append(pIdents, pMembers...) {
			g.Memberships = append(g.Memberships, identitysource.Membership{
				MemberRef:     m.ref,
				MemberKind:    identitysource.MemberIdentity,
				CollectionRef: p.id,
				Source:        identitysource.SourceInfisical,
			})
		}
	}
	return g, nil
}

// resolveToken returns the bearer used for read calls. A pre-issued access_token
// is used directly; otherwise the universal-auth client_id/client_secret are
// exchanged for a short-lived token (cached for the life of the Source). The
// credential never appears in any returned error.
func (s *Source) resolveToken(ctx context.Context) (string, error) {
	if s.accessToken != "" {
		return s.accessToken, nil
	}
	if s.token != "" {
		return s.token, nil
	}
	tok, err := s.login(ctx)
	if err != nil {
		return "", err
	}
	s.token = tok
	return tok, nil
}

// login performs the universal-auth handshake: POST {login_url} with
// {clientId, clientSecret} and parse {accessToken}. It uses the injected Doer
// directly (httpx is GET-only) and never logs or echoes the secret — on a non-2xx
// it surfaces only the status code and a bounded, credential-free excerpt.
func (s *Source) login(ctx context.Context) (string, error) {
	body, err := json.Marshal(loginRequest{ClientID: s.clientID, ClientSecret: s.clientSecret})
	if err != nil {
		return "", fmt.Errorf("infisical: marshal login: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.loginURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("infisical: build login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := s.transport().Do(req)
	if err != nil {
		return "", fmt.Errorf("infisical: universal-auth login: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, maxLoginBody))
		return "", fmt.Errorf("infisical: universal-auth login: status %d: %s", resp.StatusCode, strings.TrimSpace(string(excerpt)))
	}
	var lr loginResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxLoginBody)).Decode(&lr); err != nil {
		return "", fmt.Errorf("infisical: decode login response: %w", err)
	}
	if lr.AccessToken == "" {
		return "", fmt.Errorf("infisical: universal-auth login returned no access token")
	}
	return lr.AccessToken, nil
}

// fetchOrgIdentities lists the org's machine identities (universal-auth backed
// non-human identities) via GET /api/v2/organizations/{org_id}/identity-memberships,
// following the offset/limit pagination so a roster beyond one page is never
// silently truncated. Org rows carry a SINGULAR role string.
func (s *Source) fetchOrgIdentities(ctx context.Context, client *httpx.Client) ([]identitysource.Identity, error) {
	rows, err := s.fetchIdentityMemberships(ctx, client, "/api/v2/organizations/"+s.orgID+"/identity-memberships")
	if err != nil {
		return nil, err
	}
	out := make([]identitysource.Identity, 0, len(rows))
	for _, m := range rows {
		id := m.Identity
		if id.ID == "" {
			continue
		}
		out = append(out, identitysource.Identity{
			Ref:         id.ID,
			Type:        identitysource.PrincipalNHI,
			Kind:        "machine_identity",
			DisplayName: id.Name,
			Source:      identitysource.SourceInfisical,
		})
	}
	return out, nil
}

// fetchOrgMembers lists the org's human members via
// GET /api/v2/organizations/{org_id}/memberships (the v1 organization router does
// not exist on the live API), keyed on the user id. Rows without a user id (e.g.
// pending invites) are skipped; an inactive member is NOT skipped — it still holds
// its grants — and rides the roster with Disabled=true as the governance signal.
func (s *Source) fetchOrgMembers(ctx context.Context, client *httpx.Client) ([]identitysource.Identity, error) {
	var resp orgUsersResponse
	path := "/api/v2/organizations/" + s.orgID + "/memberships"
	if err := client.GetJSON(ctx, path, nil, &resp); err != nil {
		return nil, err
	}
	out := make([]identitysource.Identity, 0, len(resp.Users))
	for _, m := range resp.Users {
		u := m.User
		if u.ID == "" {
			continue // a non-user membership row (no human behind it); skip
		}
		attrs := map[string]string{}
		if u.Email != "" {
			attrs["email"] = u.Email
		}
		if len(attrs) == 0 {
			attrs = nil
		}
		out = append(out, identitysource.Identity{
			Ref:         u.ID,
			Type:        identitysource.PrincipalHuman,
			Kind:        "user",
			DisplayName: humanName(u),
			Source:      identitysource.SourceInfisical,
			Disabled:    m.IsActive != nil && !*m.IsActive,
			Attributes:  attrs,
		})
	}
	return out, nil
}

// fetchProjects lists the org's projects via GET /api/v1/projects (the former
// /api/v1/workspace listing lives on a deprecated router). Extra response fields
// (slug, environments, …) are ignored.
func (s *Source) fetchProjects(ctx context.Context, client *httpx.Client) ([]project, error) {
	var resp projectsResponse
	if err := client.GetJSON(ctx, "/api/v1/projects", nil, &resp); err != nil {
		return nil, err
	}
	out := make([]project, 0, len(resp.Projects))
	for _, p := range resp.Projects {
		if p.ID == "" {
			continue
		}
		out = append(out, project{id: p.ID, name: p.Name})
	}
	return out, nil
}

// fetchProjectIdentities lists the machine identities attached to one project via
// GET /api/v2/workspace/{projectID}/identity-memberships (paginated like the org
// listing). Project rows carry a roles ARRAY (no singular role), which feeds the
// Gather grant classification; Snapshot uses only the refs.
func (s *Source) fetchProjectIdentities(ctx context.Context, client *httpx.Client, projectID string) ([]memberGrant, error) {
	rows, err := s.fetchIdentityMemberships(ctx, client, "/api/v2/workspace/"+projectID+"/identity-memberships")
	if err != nil {
		return nil, err
	}
	out := make([]memberGrant, 0, len(rows))
	for _, m := range rows {
		if m.Identity.ID != "" {
			out = append(out, memberGrant{ref: m.Identity.ID, roles: m.Roles})
		}
	}
	return out, nil
}

// fetchProjectMembers lists the human members attached to one project via
// GET /api/v1/projects/{projectID}/memberships (the v2 workspace memberships
// route has no GET). Rows carry ONLY a roles array; the endpoint is not paginated.
func (s *Source) fetchProjectMembers(ctx context.Context, client *httpx.Client, projectID string) ([]memberGrant, error) {
	var resp projectMembershipsResponse
	path := "/api/v1/projects/" + projectID + "/memberships"
	if err := client.GetJSON(ctx, path, nil, &resp); err != nil {
		return nil, err
	}
	out := make([]memberGrant, 0, len(resp.Memberships))
	for _, m := range resp.Memberships {
		if m.User.ID != "" {
			out = append(out, memberGrant{ref: m.User.ID, roles: m.Roles})
		}
	}
	return out, nil
}

// fetchIdentityMemberships pages through one identity-memberships listing
// (offset/limit, required totalCount) until the reported total is collected or a
// page comes back short, bounded by maxPages so a lying server cannot loop it.
func (s *Source) fetchIdentityMemberships(ctx context.Context, client *httpx.Client, path string) ([]identityMembership, error) {
	var out []identityMembership
	for page := 0; page < s.maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		q := url.Values{
			"offset": []string{strconv.Itoa(len(out))},
			"limit":  []string{strconv.Itoa(pageLimit)},
		}
		var resp identityMembershipsPage
		if err := client.GetJSON(ctx, path, q, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.IdentityMemberships...)
		if len(out) >= resp.TotalCount || len(resp.IdentityMemberships) < pageLimit {
			break
		}
	}
	return out, nil
}

// transport returns the injected Doer or the default HTTP client.
func (s *Source) transport() httpx.Doer {
	if s.doer != nil {
		return s.doer
	}
	return http.DefaultClient
}

// clock returns the connector's time source (injectable for tests), UTC.
func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

// humanName composes a display label from first/last name, falling back to email.
// It never carries a secret — these are directory metadata fields.
func humanName(u user) string {
	name := strings.TrimSpace(strings.TrimSpace(u.FirstName) + " " + strings.TrimSpace(u.LastName))
	if name != "" {
		return name
	}
	return u.Email
}

// project is the minimal in-memory view of an Infisical project.
type project struct {
	id   string
	name string
}

// memberGrant is one project membership row reduced to what both halves need:
// the member's identity/user ref (Snapshot's membership edge) and its project
// roles (Gather's grant classification).
type memberGrant struct {
	ref   string
	roles []membershipRole
}

// grantMode maps a membership's roles to the edge's AccessMode. Built-in slugs:
// admin and member are read-write (the built-in member role edits secrets — a
// documented assumption about Infisical's default permission set), viewer is
// read-only, no-access grants nothing. A custom (or unrecognized) role is never
// guessed. The KNOWN roles' modes union vault-style (read+write => readwrite);
// an extra custom role rides only the ToolRef. When NO known role yields a mode
// but an unknown one is present the edge is honest ModeUnknown; ok=false when
// the roles grant nothing nameable (only no-access, or none at all) — no edge.
func grantMode(roles []membershipRole) (model.AccessMode, bool) {
	var mode model.AccessMode
	unknown := false
	for _, r := range roles {
		switch r.Role {
		case roleAdmin, roleMember:
			mode = mergeMode(mode, model.ModeReadWrite)
		case roleViewer:
			mode = mergeMode(mode, model.ModeRead)
		case roleNoAccess:
			// grants nothing; contributes neither a mode nor uncertainty
		default:
			unknown = true
		}
	}
	if mode != "" {
		return mode, true
	}
	if unknown {
		return model.ModeUnknown, true
	}
	return "", false
}

// mergeMode unions two access modes (read+write => readwrite, vault precedent).
func mergeMode(a, b model.AccessMode) model.AccessMode {
	if a == "" {
		return b
	}
	if a == b {
		return a
	}
	return model.ModeReadWrite
}

// roleSlugs renders a membership's role slugs sorted and de-duplicated for a
// deterministic ToolRef. A custom role rides as its customRoleSlug verbatim — it
// is a role NAME, never a secret; built-ins ride as their role string.
func roleSlugs(roles []membershipRole) string {
	set := map[string]struct{}{}
	for _, r := range roles {
		slug := r.Role
		if r.Role == roleCustom && r.CustomRoleSlug != "" {
			slug = r.CustomRoleSlug
		}
		if slug != "" {
			set[slug] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for slug := range set {
		out = append(out, slug)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// --- API wire shapes ---
//
// Verified against the live OpenAPI (app.infisical.com/api/docs/json, 2026-06-11)
// plus the backend source. Deviations from the originally assumed API:
//   - GET /api/v1/organization/{orgID}/memberships does not exist; org members
//     come from GET /api/v2/organizations/{orgID}/memberships, keyed "users".
//   - GET /api/v1/workspace lives on a deprecated router; the project listing is
//     GET /api/v1/projects, keyed "projects".
//   - /api/v2/workspace/{id}/memberships has no GET (POST/DELETE only); project
//     user memberships come from GET /api/v1/projects/{id}/memberships.
//   - Both identity-memberships listings are offset/limit-paginated (server
//     default limit=100) and carry a required totalCount.
// Org identity rows carry a SINGULAR "role"; project rows (users and identities
// alike) carry a "roles" ARRAY — one struct covers both, the absent field stays
// zero. Extra response fields are ignored everywhere.

type loginRequest struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
}

type loginResponse struct {
	AccessToken string `json:"accessToken"`
}

type identityMembershipsPage struct {
	IdentityMemberships []identityMembership `json:"identityMemberships"`
	TotalCount          int                  `json:"totalCount"`
}

type identityMembership struct {
	Role     string           `json:"role"`  // org scope only
	Roles    []membershipRole `json:"roles"` // project scope only
	Identity identity         `json:"identity"`
}

type membershipRole struct {
	Role           string `json:"role"`
	CustomRoleSlug string `json:"customRoleSlug"`
}

type identity struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type orgUsersResponse struct {
	Users []orgUserMembership `json:"users"`
}

type orgUserMembership struct {
	// IsActive is a pointer so an absent flag (older self-hosted builds) is not
	// mistaken for an explicit deactivation.
	IsActive *bool `json:"isActive"`
	User     user  `json:"user"`
}

type user struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
}

type projectsResponse struct {
	Projects []projectRow `json:"projects"`
}

type projectRow struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type projectMembershipsResponse struct {
	Memberships []projectMembership `json:"memberships"`
}

type projectMembership struct {
	User  user             `json:"user"`
	Roles []membershipRole `json:"roles"`
}
