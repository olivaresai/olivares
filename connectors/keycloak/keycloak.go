// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package keycloak is the Olivares AI self-hosted/enterprise IdP DIRECTORY
// connector. Despite the package name (kept for compatibility — its Descriptor is
// "olivares.keycloak"), it is a multi-provider directory reader selected by the
// `provider` setting, the way connectors/idp serves both Okta and Entra behind one
// connector. It discovers a directory's human users, its non-human identities
// (service accounts / M2M worker apps), its roles and groups, and the membership
// and effective-role edges between them, and exposes them as an
// identitysource.Graph to module VI (governance) in ADDITION to
// sdk.SourceConnector.
//
//   - provider=keycloak (default): Keycloak, the CNCF-incubating
//     self-hosted IdP — realm users, clients/service accounts, realm roles, groups,
//     read via the Admin REST API.
//   - provider=pingone: a PingOne (cloud) environment — users, WORKER
//     applications (M2M NHIs), groups and admin role assignments — read via the
//     PingOne Platform Management API (HAL), authenticated with an OAuth2
//     client-credentials "worker" token.
//   - provider=forgerock: a Ping Identity Platform / PingIDM (ex-ForgeRock
//     IDM) deployment — managed users, managed roles and their members — read via
//     the Common REST (CREST) managed-object API.
//
// PingFederate is deliberately NOT a provider: primary-source verification
// confirmed it is a federation/SSO server with NO user store of its own (it queries
// external LDAP/JDBC data stores; its Admin API exposes only configuration, admin
// accounts and OAuth clients). A "directory roster read" against PingFederate is a
// category error, so the connector refuses it with a clear pointer to the backing
// directory rather than inventing an endpoint. ForgeRock Identity Cloud (PingOne
// Advanced Identity Cloud) is reachable via provider=forgerock with a realm
// object-prefix (alpha_/bravo_), but its OAuth2 JWT-bearer service-account token
// mint is a documented seam (the connector accepts a pre-obtained bearer instead).
//
// It is read-only and minimal-data (docs/SECURITY-HARDENING.md-3): every directory call is a GET
// against the provider's API (the shared httpx client is GET-only by construction),
// and the only non-GET is the single OAuth2 token POST (keycloak/pingone). It pulls
// identity METADATA only — ids, usernames, display names, email, enabled state,
// role/group names — never a credential value or a client secret. The admin/worker
// secret (and the ForgeRock password/bearer) is declared Secret, held in memory,
// applied per request, and never logged or persisted. With no credential the
// connector runs offline and returns an empty Graph (no error).
//
// Honesty over guessing: a directory user is a human login account unless
// the source reveals it as a machine (a Keycloak serviceAccountClientId, a PingOne
// WORKER application) → NHI. A principal whose nature the directory does not reveal
// is PrincipalUnknown, never defaulted.
//
// It imports only the SDK, the Apache identitysource contract and the shared httpx
// read-only client — never the engine.
package keycloak

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/connectors/internal/httpx"
	"github.com/olivaresai/olivares/sdk"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.keycloak"

// Supported providers (selected by the `provider` setting). keycloak, pingone and
// forgerock are implemented; pingfederate is refused as a category error (it has no
// user store of its own —).
const (
	providerKeycloak     = "keycloak"
	providerPingOne      = "pingone"
	providerPing         = "ping" // alias for pingone
	providerForgeRock    = "forgerock"
	providerPingFederate = "pingfederate"
)

// Default configuration values.
const (
	defaultAuthRealm  = "master"
	defaultPageSize   = 100
	defaultMaxPages   = 50
	defaultPingRegion = "com" // PingOne North America (api.pingone.com / auth.pingone.com)
	pingMaxPageSize   = 200   // PingOne caps a /users page at 200 (and may return fewer)
)

// Source is the Keycloak identity connector. It satisfies sdk.SourceConnector (a
// no-op Gather — a directory roster is reference data, not an access edge) and
// identitysource.GraphProvider (the directory roster). One instance serves a
// single configured Keycloak base, optionally scoped to one realm.
type Source struct {
	provider string
	baseURL  string // Keycloak base / PingOne API host override / ForgeRock server base
	pageSize int
	maxPages int

	// Keycloak (provider=keycloak).
	realm        string // target realm to read; empty = enumerate all visible realms
	authRealm    string // realm to authenticate the admin client against (default master)
	clientID     string // admin/worker client id (client-credentials) — shared with PingOne
	clientSecret string // admin/worker client secret (Secret) — shared with PingOne
	tokenURL     string // token endpoint override (tests / non-default)

	// PingOne (provider=pingone). clientID/clientSecret above are the worker app's.
	pingRegion        string // TLD region: com/eu/ca/com.au/sg/asia (default com)
	pingEnvID         string // target environment id whose directory is read
	pingAuthEnvID     string // worker app's environment id for the token path (defaults to pingEnvID)
	pingReadRoleAssgn bool   // read per-user admin role assignments (default true)

	// ForgeRock / PingIDM (provider=forgerock).
	frUsername     string // X-OpenIDM-Username (self-managed admin)
	frPassword     string // X-OpenIDM-Password (Secret)
	frBearer       string // pre-obtained AM/IC OAuth2 bearer token (Secret; alternative to user/pass)
	frObjectPrefix string // managed-object realm prefix: "" self-managed, "alpha_"/"bravo_" Identity Cloud

	doer httpx.Doer       // injected transport (tests); nil => http.DefaultClient
	now  func() time.Time // injectable clock (tests); nil => time.Now
}

// Compile-time proof that Source satisfies both contracts.
var (
	_ sdk.SourceConnector          = (*Source)(nil)
	_ identitysource.GraphProvider = (*Source)(nil)
)

// New returns a directory connector with default configuration (provider=keycloak).
func New() *Source {
	return &Source{
		provider: providerKeycloak, authRealm: defaultAuthRealm,
		pageSize: defaultPageSize, maxPages: defaultMaxPages,
		pingRegion: defaultPingRegion, pingReadRoleAssgn: true,
	}
}

// Descriptor returns the connector's self-description and declared configuration.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Directory (Keycloak / PingOne / ForgeRock)",
		Description: "Reads users, service/worker identities, roles and groups from a self-hosted or cloud IdP directory (Keycloak Admin REST, PingOne Platform API, or ForgeRock/PingIDM CREST) — read-only metadata; never credentials; emits no observation stream, roster travels via identity Snapshot.",
		ConfigFields: []sdk.ConfigField{
			{Key: "provider", Type: sdk.FieldString, Default: providerKeycloak, Description: "Directory provider: keycloak | pingone | forgerock. pingfederate is refused (it has no user store of its own — point at the backing LDAP/JDBC directory)."},
			{Key: "base_url", Type: sdk.FieldString, Description: "keycloak: base URL e.g. https://kc.corp (Admin REST under {base}/admin/realms). pingone: optional API host override (default https://api.pingone.{region}). forgerock: server base e.g. https://idm.corp (CREST under {base}/openidm)."},
			{Key: "page_size", Type: sdk.FieldInt, Default: strconv.Itoa(defaultPageSize), Description: "List page size (keycloak first/max, pingone limit≤200, forgerock _pageSize)."},
			{Key: "max_pages", Type: sdk.FieldInt, Default: strconv.Itoa(defaultMaxPages), Description: "Pagination safety bound per list call."},
			// Keycloak
			{Key: "realm", Type: sdk.FieldString, Description: "keycloak: target realm. Empty = enumerate every realm the admin client can see (bounded by max_pages)."},
			{Key: "auth_realm", Type: sdk.FieldString, Default: defaultAuthRealm, Description: "keycloak: realm the admin client authenticates against (default master)."},
			{Key: "client_id", Type: sdk.FieldString, Description: "keycloak admin client id / pingone worker app client id (client-credentials grant)."},
			{Key: "client_secret", Type: sdk.FieldString, Secret: true, Description: "keycloak admin / pingone worker client secret reference (read-only; never persisted). Empty = offline (empty graph)."},
			{Key: "token_url", Type: sdk.FieldString, Description: "keycloak/pingone: OAuth2 token endpoint override. keycloak default {base}/realms/{auth_realm}/protocol/openid-connect/token; pingone default https://auth.pingone.{region}/{environment_id}/as/token."},
			// PingOne
			{Key: "region", Type: sdk.FieldString, Default: defaultPingRegion, Description: "pingone: region TLD — com (NA), eu, ca, com.au, sg, asia. Used for both the auth and API hosts."},
			{Key: "environment_id", Type: sdk.FieldString, Description: "pingone: the TARGET environment id whose directory is read (Management-API path)."},
			{Key: "auth_environment_id", Type: sdk.FieldString, Description: "pingone: the WORKER app's environment id, embedded in the token path {auth}/{id}/as/token. Defaults to environment_id; SET THIS when the worker app lives in a separate admin environment from the directory it reads."},
			{Key: "read_role_assignments", Type: sdk.FieldBool, Default: "true", Description: "pingone: also read per-user admin role assignments (one GET per user; disable for very large directories — group memberships are always read)."},
			// ForgeRock / PingIDM
			{Key: "username", Type: sdk.FieldString, Description: "forgerock: X-OpenIDM-Username for a self-managed PingIDM admin (alternative to bearer_token)."},
			{Key: "password", Type: sdk.FieldString, Secret: true, Description: "forgerock: X-OpenIDM-Password reference (Secret). Empty (and no bearer_token) = offline."},
			{Key: "bearer_token", Type: sdk.FieldString, Secret: true, Description: "forgerock: pre-obtained AM / Identity Cloud OAuth2 bearer token (Secret; alternative to username/password). Identity Cloud JWT-bearer minting is a documented seam."},
			{Key: "object_prefix", Type: sdk.FieldString, Description: "forgerock: managed-object realm prefix — empty for self-managed (managed/user), alpha_/bravo_ for Identity Cloud (managed/alpha_user)."},
		},
	}
}

// Open reads configuration and validates the provider. It performs no network I/O
// (the directory lifetime belongs to Snapshot, after the token POST). PingFederate
// and any unknown provider are refused here rather than inventing an API.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.provider = strings.ToLower(strings.TrimSpace(firstNonEmpty(cfg.Get("provider"), providerKeycloak)))
	if s.provider == providerPing {
		s.provider = providerPingOne // accept "ping" as an alias for pingone
	}
	s.baseURL = strings.TrimRight(strings.TrimSpace(cfg.Get("base_url")), "/")
	s.pageSize = cfg.GetInt("page_size", defaultPageSize)
	if s.pageSize <= 0 {
		s.pageSize = defaultPageSize
	}
	s.maxPages = cfg.GetInt("max_pages", defaultMaxPages)
	if s.maxPages <= 0 {
		s.maxPages = defaultMaxPages
	}
	// Keycloak / shared client-credentials.
	s.realm = strings.TrimSpace(cfg.Get("realm"))
	s.authRealm = firstNonEmpty(strings.TrimSpace(cfg.Get("auth_realm")), defaultAuthRealm)
	s.clientID = cfg.Get("client_id")
	s.clientSecret = cfg.Get("client_secret")
	s.tokenURL = strings.TrimSpace(cfg.Get("token_url"))
	// PingOne.
	s.pingRegion = strings.ToLower(firstNonEmpty(strings.TrimSpace(cfg.Get("region")), defaultPingRegion))
	s.pingEnvID = strings.TrimSpace(cfg.Get("environment_id"))
	s.pingAuthEnvID = strings.TrimSpace(cfg.Get("auth_environment_id"))
	s.pingReadRoleAssgn = cfg.GetBool("read_role_assignments", true)
	// ForgeRock / PingIDM.
	s.frUsername = strings.TrimSpace(cfg.Get("username"))
	s.frPassword = cfg.Get("password")
	s.frBearer = cfg.Get("bearer_token")
	s.frObjectPrefix = strings.TrimSpace(cfg.Get("object_prefix"))

	switch s.provider {
	case providerKeycloak, providerPingOne, providerForgeRock:
		return nil
	case providerPingFederate:
		// Verified category error: PingFederate is a federation/SSO server with
		// no user store of its own — it queries external LDAP/JDBC data stores and its
		// Admin API exposes only configuration, admin accounts and OAuth clients. Point
		// the connector at the BACKING directory (e.g. PingDirectory/AD via ldap, or
		// PingOne) instead of inventing a roster endpoint PingFederate does not have.
		return fmt.Errorf("keycloak: provider %q has no end-user directory to read (PingFederate federates to external LDAP/JDBC stores); configure the backing directory connector (ldap/pingone) instead", s.provider)
	default:
		return fmt.Errorf("keycloak: unknown provider %q (want keycloak|pingone|forgerock)", s.provider)
	}
}

// Gather emits no observations: a Keycloak roster is reference data exposed
// through Snapshot, and a group/role membership is not an access edge. It returns
// nil immediately (a batch source with nothing to stream).
func (s *Source) Gather(context.Context, sdk.Sink) error { return nil }

// Close releases resources; the connector holds no long-lived connection.
func (s *Source) Close(context.Context) error { return nil }

// Snapshot reads the configured directory read-only and assembles the identity
// graph, dispatching by provider. With no credential it returns the connector's
// offline (empty) graph, no error. It never returns credential material.
func (s *Source) Snapshot(ctx context.Context) (identitysource.Graph, error) {
	switch s.provider {
	case providerPingOne:
		return s.snapshotPingOne(ctx)
	case providerForgeRock:
		return s.snapshotForgeRock(ctx)
	default:
		return s.snapshotKeycloak(ctx)
	}
}

// snapshotKeycloak reads the configured Keycloak realm(s) read-only and assembles
// the identity graph. With no admin client-credentials it returns the offline
// (empty) graph, no error. It never returns credential material.
func (s *Source) snapshotKeycloak(ctx context.Context) (identitysource.Graph, error) {
	g := identitysource.Graph{Source: identitysource.SourceKeycloak, CapturedAt: s.clock().UTC()}
	if s.clientID == "" || s.clientSecret == "" {
		return g, nil // offline: no client-credentials configured
	}

	token, err := s.token(ctx)
	if err != nil {
		return identitysource.Graph{}, err
	}
	if token == "" {
		return g, nil // defensive: no token => offline
	}
	// Build the read-only Admin client with the bearer token. The token lives only
	// on this call's stack + the client's auth closure; never logged or persisted.
	client := httpx.New(s.baseURL, s.doer, httpx.Bearer(token), nil)

	realms, err := s.targetRealms(ctx, client)
	if err != nil {
		return identitysource.Graph{}, err
	}
	for _, realm := range realms {
		if err := s.snapshotRealm(ctx, client, realm, &g); err != nil {
			return identitysource.Graph{}, err
		}
	}
	return g, nil
}

// targetRealms resolves which realms to read: the single configured realm, or
// every realm the admin client can enumerate (GET /admin/realms) when none is set.
func (s *Source) targetRealms(ctx context.Context, client *httpx.Client) ([]string, error) {
	if s.realm != "" {
		return []string{s.realm}, nil
	}
	var page []kcRealm
	if err := client.GetJSON(ctx, "/admin/realms", url.Values{"briefRepresentation": {"true"}}, &page); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(page))
	for _, r := range page {
		if r.Realm != "" {
			out = append(out, r.Realm)
		}
	}
	return out, nil
}

// snapshotRealm reads one realm's users, clients, roles and groups and appends
// their identities/collections/memberships to g.
func (s *Source) snapshotRealm(ctx context.Context, client *httpx.Client, realm string, g *identitysource.Graph) error {
	base := "/admin/realms/" + url.PathEscape(realm)

	// Users — human realm accounts, unless the realm reveals a service account.
	users, err := s.pagedUsers(ctx, client, base+"/users")
	if err != nil {
		return err
	}
	for _, u := range users {
		g.Identities = append(g.Identities, userIdentity(realm, u))
	}

	// Clients — confidential service-account clients are NHI; a plain client's
	// principal nature is not revealed (Unknown).
	clients, err := s.pagedClients(ctx, client, base+"/clients")
	if err != nil {
		return err
	}
	for _, c := range clients {
		g.Identities = append(g.Identities, clientIdentity(realm, c))
	}

	// Realm roles — assignable roles (KindRole). Names are per-realm, so the Ref
	// carries the realm to stay globally unique.
	roles, err := s.pagedRoles(ctx, client, base+"/roles")
	if err != nil {
		return err
	}
	for _, r := range roles {
		g.Collections = append(g.Collections, identitysource.Collection{
			Ref:         roleRef(realm, r.Name),
			Kind:        identitysource.KindRole,
			DisplayName: r.Name,
			Source:      identitysource.SourceKeycloak,
			Attributes:  pruneAttrs(map[string]string{"realm": realm, "description": r.Description}),
		})
		// Users holding this realm role (user∈role). Bounded by paging.
		holders, err := s.pagedUsers(ctx, client, base+"/roles/"+url.PathEscape(r.Name)+"/users")
		if err != nil {
			return err
		}
		for _, u := range holders {
			g.Memberships = append(g.Memberships, identitysource.Membership{
				MemberRef: u.ID, MemberKind: identitysource.MemberIdentity,
				CollectionRef: roleRef(realm, r.Name), Source: identitysource.SourceKeycloak,
			})
		}
	}

	// Groups — directory groups (KindGroup), with nested subgroups as nested
	// collection memberships and their members as identity memberships.
	groups, err := s.pagedGroups(ctx, client, base+"/groups")
	if err != nil {
		return err
	}
	for i := range groups {
		if err := s.addGroup(ctx, client, base, realm, groups[i], "", g); err != nil {
			return err
		}
	}
	return nil
}

// addGroup appends a group (and its subgroups, depth-first) as collections, the
// nested-collection edge to its parent, and its direct member users.
func (s *Source) addGroup(ctx context.Context, client *httpx.Client, base, realm string, grp kcGroup, parentRef string, g *identitysource.Graph) error {
	g.Collections = append(g.Collections, identitysource.Collection{
		Ref:         grp.ID,
		Kind:        identitysource.KindGroup,
		DisplayName: grp.Name,
		Source:      identitysource.SourceKeycloak,
		Attributes:  pruneAttrs(map[string]string{"realm": realm, "path": grp.Path}),
	})
	if parentRef != "" {
		g.Memberships = append(g.Memberships, identitysource.Membership{
			MemberRef: grp.ID, MemberKind: identitysource.MemberCollection,
			CollectionRef: parentRef, Source: identitysource.SourceKeycloak,
		})
	}
	// Direct members (user∈group).
	members, err := s.pagedUsers(ctx, client, base+"/groups/"+url.PathEscape(grp.ID)+"/members")
	if err != nil {
		return err
	}
	for _, u := range members {
		g.Memberships = append(g.Memberships, identitysource.Membership{
			MemberRef: u.ID, MemberKind: identitysource.MemberIdentity,
			CollectionRef: grp.ID, Source: identitysource.SourceKeycloak,
		})
	}
	for i := range grp.SubGroups {
		if err := s.addGroup(ctx, client, base, realm, grp.SubGroups[i], grp.ID, g); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Classification (honest, never guessed —)
// ---------------------------------------------------------------------------

// userIdentity maps a Keycloak realm user to an Identity. A service-account user
// (serviceAccountClientId set) is NHI; otherwise it is a human login account.
func userIdentity(realm string, u kcUser) identitysource.Identity {
	id := identitysource.Identity{
		Ref:         u.ID,
		DisplayName: displayName(u),
		Source:      identitysource.SourceKeycloak,
		Disabled:    !u.Enabled,
		Attributes:  pruneAttrs(map[string]string{"realm": realm, "username": u.Username, "email": u.Email}),
	}
	if u.ServiceAccountClientID != "" {
		id.Type = identitysource.PrincipalNHI
		id.Kind = "service_account"
		if id.Attributes == nil {
			id.Attributes = map[string]string{}
		}
		id.Attributes["service_account_client"] = u.ServiceAccountClientID
	} else {
		id.Type = identitysource.PrincipalHuman
		id.Kind = "user"
	}
	return id
}

// clientIdentity maps a Keycloak client to an Identity. A client with
// serviceAccountsEnabled authenticates as itself → NHI. A plain client's
// principal nature is not revealed by the directory → Unknown (never defaulted).
func clientIdentity(realm string, c kcClient) identitysource.Identity {
	id := identitysource.Identity{
		Ref:         c.ID,
		DisplayName: firstNonEmpty(c.Name, c.ClientID),
		Source:      identitysource.SourceKeycloak,
		Disabled:    !c.Enabled,
		Attributes:  pruneAttrs(map[string]string{"realm": realm, "client_id": c.ClientID}),
	}
	if c.ServiceAccountsEnabled {
		id.Type = identitysource.PrincipalNHI
		id.Kind = "service_account_client"
	} else {
		id.Type = identitysource.PrincipalUnknown
		id.Kind = "client"
	}
	return id
}

// displayName builds a human label from the user's name fields, falling back to
// the username.
func displayName(u kcUser) string {
	full := strings.TrimSpace(u.FirstName + " " + u.LastName)
	return firstNonEmpty(full, u.Username, u.ID)
}

// roleRef disambiguates a per-realm role name into a globally unique Ref.
func roleRef(realm, name string) string { return realm + "/role:" + name }

// ---------------------------------------------------------------------------
// Paging helpers (Keycloak Admin API: first/max query params)
// ---------------------------------------------------------------------------

func (s *Source) pagedUsers(ctx context.Context, client *httpx.Client, path string) ([]kcUser, error) {
	return listAll[kcUser](ctx, s, client, path)
}

func (s *Source) pagedClients(ctx context.Context, client *httpx.Client, path string) ([]kcClient, error) {
	return listAll[kcClient](ctx, s, client, path)
}

func (s *Source) pagedRoles(ctx context.Context, client *httpx.Client, path string) ([]kcRole, error) {
	return listAll[kcRole](ctx, s, client, path)
}

// pagedGroups reads the top-level groups (their subGroups are nested inline by
// the Admin API up to the server's default depth).
func (s *Source) pagedGroups(ctx context.Context, client *httpx.Client, path string) ([]kcGroup, error) {
	return listAll[kcGroup](ctx, s, client, path)
}

// listAll reads every page of a Keycloak Admin list endpoint with first/max
// paging, stopping at a short page (fewer than pageSize rows), the maxPages safety
// bound, or ctx cancellation. The Admin API returns a plain JSON array per page.
func listAll[T any](ctx context.Context, s *Source, client *httpx.Client, path string) ([]T, error) {
	var all []T
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var page []T
		query := url.Values{
			"first": {strconv.Itoa(i * s.pageSize)},
			"max":   {strconv.Itoa(s.pageSize)},
		}
		if err := client.GetJSON(ctx, path, query, &page); err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) < s.pageSize {
			break // short page => last page
		}
	}
	return all, nil
}

// token performs the OAuth2 client-credentials grant against the Keycloak token
// endpoint using the SAME injected transport (so a test stubs it). It returns the
// access token (held only in memory); a non-2xx is an error that never carries
// the client secret.
func (s *Source) token(ctx context.Context) (string, error) {
	tokenURL := s.tokenURL
	if tokenURL == "" {
		if s.baseURL == "" {
			return "", fmt.Errorf("keycloak: base_url (or token_url) is required for the token endpoint")
		}
		tokenURL = s.baseURL + "/realms/" + url.PathEscape(s.authRealm) + "/protocol/openid-connect/token"
	}
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {s.clientID},
		"client_secret": {s.clientSecret},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("keycloak: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := s.transport().Do(req)
	if err != nil {
		return "", fmt.Errorf("keycloak: token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		// The excerpt is the provider's error body; the request form (which holds the
		// secret) is never included.
		return "", fmt.Errorf("keycloak: token endpoint status %d: %s", resp.StatusCode, strings.TrimSpace(string(excerpt)))
	}
	var tr struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tr); err != nil {
		return "", fmt.Errorf("keycloak: decode token response: %w", err)
	}
	return tr.AccessToken, nil
}

// clock returns the connector's time source (injectable for tests).
func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// transport returns the injected Doer or the default HTTP client (used for the
// single token POST, so one stub drives every call).
func (s *Source) transport() httpx.Doer {
	if s.doer != nil {
		return s.doer
	}
	return http.DefaultClient
}

// firstNonEmpty returns the first non-empty string of its arguments.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// pruneAttrs drops empty values and returns nil when nothing remains (keeping
// Snapshots diff-stable).
func pruneAttrs(m map[string]string) map[string]string {
	for k, v := range m {
		if v == "" {
			delete(m, k)
		}
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// Keycloak Admin API wire shapes (only the fields the connector reads — never a
// credential, never a client secret).
type kcRealm struct {
	Realm string `json:"realm"`
}

type kcUser struct {
	ID                     string `json:"id"`
	Username               string `json:"username"`
	Email                  string `json:"email"`
	FirstName              string `json:"firstName"`
	LastName               string `json:"lastName"`
	Enabled                bool   `json:"enabled"`
	ServiceAccountClientID string `json:"serviceAccountClientId"`
}

type kcClient struct {
	ID                     string `json:"id"`
	ClientID               string `json:"clientId"`
	Name                   string `json:"name"`
	Enabled                bool   `json:"enabled"`
	ServiceAccountsEnabled bool   `json:"serviceAccountsEnabled"`
}

type kcRole struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type kcGroup struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	SubGroups []kcGroup `json:"subGroups"`
}
