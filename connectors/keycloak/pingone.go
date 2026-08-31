// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

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

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/connectors/internal/httpx"
)

// PingOne (cloud) directory reader. Verified against the PingOne Platform
// Management API (developer.pingidentity.com): two hosts — auth.pingone.{tld}
// mints the OAuth2 client-credentials "worker" token, api.pingone.{tld} serves the
// HAL Management API under /v1/environments/{envID}. A worker token's authority is
// governed by ROLE ASSIGNMENTS on the worker app, not OAuth scopes; for a read-only
// roster assign the built-in "Identity Data Read Only" role scoped to the
// environment. Collections are cursor-paged HAL (_embedded.<key>[] + _links.next.href).

// pingAPIHost returns the Management-API host (base_url override, else
// api.pingone.{region}). No trailing slash.
func (s *Source) pingAPIHost() string {
	if s.baseURL != "" {
		return s.baseURL
	}
	return "https://api.pingone." + s.pingRegion
}

// pingAuthHost returns the authorization host (auth.pingone.{region}) used for the
// worker-token endpoint. No trailing slash.
func (s *Source) pingAuthHost() string {
	return "https://auth.pingone." + s.pingRegion
}

// snapshotPingOne reads the configured PingOne environment read-only and assembles
// the identity graph: human users (with their group memberships), WORKER
// applications as NHIs, groups, and the effective admin role assignments (user→role
// and group→role). With no worker credential it returns the offline (empty) graph.
func (s *Source) snapshotPingOne(ctx context.Context) (identitysource.Graph, error) {
	g := identitysource.Graph{Source: identitysource.SourcePingOne, CapturedAt: s.clock().UTC()}
	if s.clientID == "" || s.clientSecret == "" {
		return g, nil // offline: no worker client-credentials configured
	}
	if s.pingEnvID == "" {
		return identitysource.Graph{}, fmt.Errorf("keycloak: pingone requires environment_id (the target environment whose directory is read)")
	}

	token, err := s.pingToken(ctx)
	if err != nil {
		return identitysource.Graph{}, err
	}
	if token == "" {
		return g, nil // defensive: no token => offline
	}
	client := httpx.New(s.pingAPIHost(), s.doer, httpx.Bearer(token), nil)
	base := "/v1/environments/" + url.PathEscape(s.pingEnvID)

	// roles is the catalog of admin roles DISCOVERED from the assignments we observe.
	// PingOne role ids are per-tenant (not constants) and there is no primary-verified
	// "list all roles" endpoint, so we never invent one: a role becomes a collection
	// the moment an assignment references it, named by role.name when the assignment
	// carries it, else by its id (honest degradation, never fabricated).
	roles := newRoleCatalog()

	// Groups -> KindGroup collections.
	groups, err := pingList[pingGroup](ctx, s, client, base+"/groups", "groups", s.pingLimit(), nil)
	if err != nil {
		return identitysource.Graph{}, err
	}
	for _, gr := range groups {
		g.Collections = append(g.Collections, identitysource.Collection{
			Ref: pingGroupRef(gr.ID), Kind: identitysource.KindGroup,
			DisplayName: firstNonEmpty(gr.Name, gr.ID), Source: identitysource.SourcePingOne,
			Attributes: pruneAttrs(map[string]string{"external_id": gr.ExternalID, "population": gr.populationID()}),
		})
	}

	// Users -> human identities + their group memberships. include=memberOfGroupIDs
	// folds each user's group ids into the same paged scan (the user-side membership
	// read PingOne documents).
	uq := s.pingLimit()
	uq.Set("include", "memberOfGroupIDs")
	users, err := pingList[pingUser](ctx, s, client, base+"/users", "users", uq, url.Values{"include": {"memberOfGroupIDs"}})
	if err != nil {
		return identitysource.Graph{}, err
	}
	for _, u := range users {
		g.Identities = append(g.Identities, pingUserIdentity(u))
		for _, gid := range u.MemberOfGroupIDs {
			g.Memberships = append(g.Memberships, identitysource.Membership{
				MemberRef: u.ID, MemberKind: identitysource.MemberIdentity,
				CollectionRef: pingGroupRef(gid), Source: identitysource.SourcePingOne,
			})
		}
		// Effective admin role links for the user (one GET per user; opt-out via
		// read_role_assignments for very large directories).
		if s.pingReadRoleAssgn {
			if err := s.pingAddRoleAssignments(ctx, client, base+"/users/"+url.PathEscape(u.ID)+"/roleAssignments",
				u.ID, identitysource.MemberIdentity, roles, &g); err != nil {
				return identitysource.Graph{}, err
			}
		}
	}

	// Applications -> WORKER apps are M2M NHIs (the service identities PingOne keeps
	// out of /users). Non-worker apps are SSO integrations, not directory principals,
	// so they are not rostered.
	apps, err := pingList[pingApp](ctx, s, client, base+"/applications", "applications", s.pingLimit(), nil)
	if err != nil {
		return identitysource.Graph{}, err
	}
	for _, a := range apps {
		if !a.isWorker() {
			continue
		}
		g.Identities = append(g.Identities, identitysource.Identity{
			Ref: a.ID, Type: identitysource.PrincipalNHI, Kind: "worker_application",
			DisplayName: firstNonEmpty(a.Name, a.ID), Source: identitysource.SourcePingOne,
			Disabled:   a.disabled(),
			Attributes: pruneAttrs(map[string]string{"app_type": a.Type}),
		})
	}

	// Group role assignments -> group∈role effective links (bounded by #groups).
	for _, gr := range groups {
		if err := s.pingAddRoleAssignments(ctx, client, base+"/groups/"+url.PathEscape(gr.ID)+"/roleAssignments",
			gr.ID, identitysource.MemberCollection, roles, &g); err != nil {
			return identitysource.Graph{}, err
		}
	}

	// Emit the discovered role collections in first-seen order (diff-stable).
	for _, rid := range roles.order {
		g.Collections = append(g.Collections, identitysource.Collection{
			Ref: pingRoleRef(rid), Kind: identitysource.KindRole,
			DisplayName: firstNonEmpty(roles.name[rid], rid), Source: identitysource.SourcePingOne,
		})
	}
	return g, nil
}

// pingAddRoleAssignments reads one principal's role-assignment collection and
// appends a member∈role membership per assignment, recording each role in the
// catalog.
func (s *Source) pingAddRoleAssignments(ctx context.Context, client *httpx.Client, path, memberRef string, kind identitysource.MemberKind, roles *roleCatalog, g *identitysource.Graph) error {
	ras, err := pingList[pingRoleAssignment](ctx, s, client, path, "roleAssignments", nil, nil)
	if err != nil {
		return err
	}
	for _, ra := range ras {
		if ra.Role == nil || ra.Role.ID == "" {
			continue
		}
		roles.add(ra.Role.ID, ra.Role.Name)
		g.Memberships = append(g.Memberships, identitysource.Membership{
			MemberRef: memberRef, MemberKind: kind,
			CollectionRef: pingRoleRef(ra.Role.ID), Source: identitysource.SourcePingOne,
		})
	}
	return nil
}

// pingLimit builds the ?limit= query for a collection read, capped at the PingOne
// per-page maximum.
func (s *Source) pingLimit() url.Values {
	n := s.pageSize
	if n > pingMaxPageSize {
		n = pingMaxPageSize
	}
	return url.Values{"limit": {strconv.Itoa(n)}}
}

// pingToken performs the OAuth2 client-credentials grant for a PingOne worker app
// using the SAME injected transport (so a test stubs it). Credentials go in the
// HTTP Basic header (PingOne's documented worker auth); the secret is never logged
// and never placed in the error.
func (s *Source) pingToken(ctx context.Context) (string, error) {
	tokenURL := s.tokenURL
	if tokenURL == "" {
		// The token path embeds the WORKER app's environment id, which may differ from
		// the target environment whose directory is read (a supported PingOne topology).
		// Default it to the target env, overridable via auth_environment_id / token_url.
		authEnv := firstNonEmpty(s.pingAuthEnvID, s.pingEnvID)
		tokenURL = s.pingAuthHost() + "/" + url.PathEscape(authEnv) + "/as/token"
	}
	form := url.Values{"grant_type": {"client_credentials"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("keycloak: build pingone token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(s.clientID, s.clientSecret)

	resp, err := s.transport().Do(req)
	if err != nil {
		return "", fmt.Errorf("keycloak: pingone token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		return "", fmt.Errorf("keycloak: pingone token endpoint status %d: %s", resp.StatusCode, strings.TrimSpace(string(excerpt)))
	}
	var tr struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tr); err != nil {
		return "", fmt.Errorf("keycloak: decode pingone token response: %w", err)
	}
	return tr.AccessToken, nil
}

// pingList reads every page of a PingOne HAL collection: the first GET uses path +
// query; each subsequent page follows _links.next.href (an absolute, same-origin URL
// that carries the opaque cursor + limit). carry holds query params that the cursor
// href is NOT guaranteed to preserve (e.g. include=memberOfGroupIDs) — they are
// re-appended to every follow-up href so a paginated /users scan keeps returning
// memberships; re-appending a value PingOne already preserved is harmless. It stops
// at the absent next link, the maxPages safety bound, or ctx cancellation.
// embeddedKey is the _embedded sub-key holding the array.
func pingList[T any](ctx context.Context, s *Source, client *httpx.Client, path, embeddedKey string, query, carry url.Values) ([]T, error) {
	var all []T
	next := path
	q := query
	for i := 0; i < s.maxPages && next != ""; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var page struct {
			Embedded map[string]json.RawMessage `json:"_embedded"`
			Links    struct {
				Next *struct {
					Href string `json:"href"`
				} `json:"next"`
			} `json:"_links"`
		}
		if err := client.GetJSON(ctx, next, q, &page); err != nil {
			return nil, err
		}
		if raw, ok := page.Embedded[embeddedKey]; ok && len(raw) > 0 {
			var items []T
			if err := json.Unmarshal(raw, &items); err != nil {
				return nil, fmt.Errorf("keycloak: decode pingone %s page: %w", embeddedKey, err)
			}
			all = append(all, items...)
		}
		if page.Links.Next == nil || page.Links.Next.Href == "" {
			break // last page
		}
		// The cursor href is an absolute URL already carrying its own query, so the
		// carry params are appended to the href STRING (with &) and q is nil — passing
		// a query to GetJSON for such a URL would corrupt it with a second '?'.
		next = appendQuery(page.Links.Next.Href, carry)
		q = nil
	}
	return all, nil
}

// appendQuery appends extra query params to a URL that already (PingOne cursor hrefs
// always) carries a query string, joining with '&'. Empty extra returns u unchanged.
func appendQuery(u string, extra url.Values) string {
	if len(extra) == 0 {
		return u
	}
	sep := "&"
	if !strings.Contains(u, "?") {
		sep = "?"
	}
	return u + sep + extra.Encode()
}

// pingUserIdentity maps a PingOne user to a human Identity. enabled (default true)
// is the disabled marker; PingOne keeps machine identities out of /users (they are
// WORKER applications), so a /users row is always a human/provisioned account.
func pingUserIdentity(u pingUser) identitysource.Identity {
	return identitysource.Identity{
		Ref: u.ID, Type: identitysource.PrincipalHuman, Kind: "user",
		DisplayName: u.displayName(), Source: identitysource.SourcePingOne,
		Disabled:   u.Enabled != nil && !*u.Enabled,
		Attributes: pruneAttrs(map[string]string{"username": u.Username, "email": u.Email, "population": u.populationID()}),
	}
}

// roleCatalog accumulates roles discovered from assignments, preserving first-seen
// order for a diff-stable Snapshot and keeping the best (non-empty) display name.
type roleCatalog struct {
	order []string
	name  map[string]string
}

func newRoleCatalog() *roleCatalog { return &roleCatalog{name: map[string]string{}} }

func (rc *roleCatalog) add(id, name string) {
	if _, seen := rc.name[id]; !seen {
		rc.order = append(rc.order, id)
		rc.name[id] = ""
	}
	if name != "" {
		rc.name[id] = name
	}
}

// pingGroupRef / pingRoleRef namespace a PingOne collection id so a group id and a
// role id can never collide within one Graph.
func pingGroupRef(id string) string { return "group:" + id }
func pingRoleRef(id string) string  { return "role:" + id }

// PingOne Management API wire shapes (only the fields the connector reads — never a
// credential).
type pingUser struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
	Enabled  *bool  `json:"enabled"` // pointer: absent => treat as enabled (default true)
	Name     *struct {
		Given     string `json:"given"`
		Family    string `json:"family"`
		Formatted string `json:"formatted"`
	} `json:"name"`
	Population *struct {
		ID string `json:"id"`
	} `json:"population"`
	MemberOfGroupIDs []string `json:"memberOfGroupIDs"`
}

func (u pingUser) displayName() string {
	if u.Name != nil {
		if u.Name.Formatted != "" {
			return u.Name.Formatted
		}
		if full := strings.TrimSpace(u.Name.Given + " " + u.Name.Family); full != "" {
			return full
		}
	}
	return firstNonEmpty(u.Username, u.ID)
}

func (u pingUser) populationID() string {
	if u.Population != nil {
		return u.Population.ID
	}
	return ""
}

type pingGroup struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	ExternalID  string `json:"externalId"`
	Population  *struct {
		ID string `json:"id"`
	} `json:"population"`
}

func (g pingGroup) populationID() string {
	if g.Population != nil {
		return g.Population.ID
	}
	return ""
}

type pingApp struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Type       string   `json:"type"`       // WEB_APP / NATIVE_APP / SINGLE_PAGE_APP / WORKER / SERVICE / CUSTOM
	GrantTypes []string `json:"grantTypes"` // CLIENT_CREDENTIALS marks an M2M principal
	Enabled    *bool    `json:"enabled"`
}

// isWorker reports whether the application is a machine-to-machine principal that
// authenticates as itself: a WORKER-type app, or any app whose grant types include
// the client-credentials grant.
func (a pingApp) isWorker() bool {
	if strings.EqualFold(a.Type, "WORKER") {
		return true
	}
	for _, gt := range a.GrantTypes {
		if strings.EqualFold(gt, "CLIENT_CREDENTIALS") {
			return true
		}
	}
	return false
}

func (a pingApp) disabled() bool { return a.Enabled != nil && !*a.Enabled }

type pingRoleAssignment struct {
	ID   string `json:"id"`
	Role *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"role"`
	Scope *struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	} `json:"scope"`
}
