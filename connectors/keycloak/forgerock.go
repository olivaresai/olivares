// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package keycloak

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/connectors/internal/httpx"
)

// ForgeRock / PingIDM (ex-ForgeRock IDM) directory reader. Verified against the
// Ping Identity Platform docs (docs.pingidentity.com pingidm / pingoneaic):
// managed objects are read over the Common REST (CREST) API at
// /openidm/managed/{prefix}<object> with _queryFilter=true and _pageSize +
// _pagedResultsCookie paging; the response envelope is
// {result:[…], pagedResultsCookie}. A managed user's accountStatus is the string
// "active"/"inactive" (the disabled marker). Roles live at managed/{prefix}role and
// their members at managed/{prefix}role/{id}/members (a relationship whose rows
// carry a _ref like "managed/{prefix}user/{id}").
//
// Auth: self-managed PingIDM accepts the internal admin via X-OpenIDM-Username /
// X-OpenIDM-Password headers, OR an AM-issued OAuth2 bearer token. Identity Cloud
// (PingOne Advanced Identity Cloud) accepts ONLY a bearer token (its JWT-bearer
// service-account mint is a documented seam — supply a pre-obtained bearer here) and
// realm-namespaces every object (object_prefix=alpha_/bravo_). Every call carries
// Accept-API-Version: resource=1.0.

// frObjectPath returns the CREST managed-object path for a realm-prefixed object.
func (s *Source) frObjectPath(object string) string {
	return "/openidm/managed/" + s.frObjectPrefix + object
}

// snapshotForgeRock reads the configured PingIDM/Identity Cloud deployment read-only
// and assembles the identity graph: managed users, managed roles (with their member
// users as effective role links), and managed groups as collections. With no
// credential (neither password nor bearer) it returns the offline (empty) graph.
//
// Group MEMBERSHIP is intentionally not emitted: only the role-member relationship
// is primary-verified, so group rows are inventory-only and group membership is a
// documented seam rather than a guessed endpoint.
func (s *Source) snapshotForgeRock(ctx context.Context) (identitysource.Graph, error) {
	g := identitysource.Graph{Source: identitysource.SourceForgeRock, CapturedAt: s.clock().UTC()}
	if s.frPassword == "" && s.frBearer == "" {
		return g, nil // offline: no credential configured
	}
	if s.baseURL == "" {
		return identitysource.Graph{}, fmt.Errorf("keycloak: forgerock requires base_url (the server base; CREST is read under {base}/openidm)")
	}
	client := httpx.New(s.baseURL, s.doer, s.frAuth(), map[string]string{"Accept-API-Version": "resource=1.0"})

	// Users -> human identities.
	users, err := crestQuery[frUser](ctx, s, client, s.frObjectPath("user"), "_id,userName,givenName,sn,mail,accountStatus")
	if err != nil {
		return identitysource.Graph{}, err
	}
	for _, u := range users {
		g.Identities = append(g.Identities, frUserIdentity(u))
	}

	// Roles -> KindRole collections + their members (user∈role effective links).
	roles, err := crestQuery[frRole](ctx, s, client, s.frObjectPath("role"), "_id,name,description")
	if err != nil {
		return identitysource.Graph{}, err
	}
	for _, r := range roles {
		g.Collections = append(g.Collections, identitysource.Collection{
			Ref: frRoleRef(r.ID), Kind: identitysource.KindRole,
			DisplayName: firstNonEmpty(r.Name, r.ID), Source: identitysource.SourceForgeRock,
			Attributes: pruneAttrs(map[string]string{"description": r.Description}),
		})
		members, err := crestQuery[frRef](ctx, s, client, s.frObjectPath("role")+"/"+url.PathEscape(r.ID)+"/members", "_ref")
		if err != nil {
			return identitysource.Graph{}, err
		}
		for _, m := range members {
			if uid := refTail(m.Ref); uid != "" {
				g.Memberships = append(g.Memberships, identitysource.Membership{
					MemberRef: uid, MemberKind: identitysource.MemberIdentity,
					CollectionRef: frRoleRef(r.ID), Source: identitysource.SourceForgeRock,
				})
			}
		}
	}

	// Groups -> KindGroup collections (Identity Cloud). Self-managed PingIDM has no
	// managed/group object: CREST answers a missing managed-object type with 404, which
	// is the honest "no groups here" signal. Only 404 is absorbed — a 400 (malformed
	// query / wrong API version) or any other status is a real error that must surface,
	// never be silently turned into an empty group set.
	groups, err := crestQuery[frGroup](ctx, s, client, s.frObjectPath("group"), "_id,name,description")
	if err != nil {
		if !isHTTPStatus(err, http.StatusNotFound) {
			return identitysource.Graph{}, err
		}
	}
	for _, gr := range groups {
		g.Collections = append(g.Collections, identitysource.Collection{
			Ref: frGroupRef(gr.ID), Kind: identitysource.KindGroup,
			DisplayName: firstNonEmpty(gr.Name, gr.ID), Source: identitysource.SourceForgeRock,
			Attributes: pruneAttrs(map[string]string{"description": gr.Description}),
		})
	}
	return g, nil
}

// frAuth applies the configured ForgeRock credential per request: a bearer token if
// set, else the X-OpenIDM-Username/Password header pair. With no credential it sends
// no auth header at all (the offline guard).
func (s *Source) frAuth() httpx.AuthFunc {
	if s.frBearer != "" {
		return httpx.Bearer(s.frBearer)
	}
	user, pass := s.frUsername, s.frPassword
	return func(req *http.Request) {
		if pass == "" {
			return
		}
		req.Header.Set("X-OpenIDM-Username", user)
		req.Header.Set("X-OpenIDM-Password", pass)
	}
}

// crestQuery reads every page of a CREST managed-object query: GET path with
// _queryFilter=true, _pageSize and (after the first page) _pagedResultsCookie,
// projecting fields. It loops until the cookie comes back empty, the maxPages safety
// bound, or ctx cancellation.
func crestQuery[T any](ctx context.Context, s *Source, client *httpx.Client, path, fields string) ([]T, error) {
	var all []T
	cookie := ""
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		q := url.Values{
			"_queryFilter": {"true"},
			"_pageSize":    {strconv.Itoa(s.pageSize)},
		}
		if fields != "" {
			q.Set("_fields", fields)
		}
		if cookie != "" {
			q.Set("_pagedResultsCookie", cookie)
		}
		var page struct {
			Result []T    `json:"result"`
			Cookie string `json:"pagedResultsCookie"`
		}
		if err := client.GetJSON(ctx, path, q, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Result...)
		if page.Cookie == "" {
			break // null/empty cookie => last page
		}
		cookie = page.Cookie
	}
	return all, nil
}

// isHTTPStatus reports whether err is an httpx status error with one of the given
// codes (used to treat a missing managed-object type as "absent", not a failure).
func isHTTPStatus(err error, codes ...int) bool {
	var se *httpx.StatusError
	if !errors.As(err, &se) {
		return false
	}
	for _, c := range codes {
		if se.Status == c {
			return true
		}
	}
	return false
}

// refTail returns the last path segment of a CREST relationship _ref
// ("managed/alpha_user/{id}" -> "{id}").
func refTail(ref string) string {
	ref = strings.Trim(strings.TrimSpace(ref), "/")
	if ref == "" {
		return ""
	}
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		return ref[i+1:]
	}
	return ref
}

// frUserIdentity maps a managed user to a human Identity. accountStatus "inactive"
// is the disabled marker (PingIDM's enabled/disabled field).
func frUserIdentity(u frUser) identitysource.Identity {
	return identitysource.Identity{
		Ref: u.ID, Type: identitysource.PrincipalHuman, Kind: "user",
		DisplayName: u.displayName(), Source: identitysource.SourceForgeRock,
		Disabled:   strings.EqualFold(u.AccountStatus, "inactive"),
		Attributes: pruneAttrs(map[string]string{"username": u.UserName, "email": u.Mail}),
	}
}

func frRoleRef(id string) string  { return "role:" + id }
func frGroupRef(id string) string { return "group:" + id }

// PingIDM CREST managed-object wire shapes (only the fields the connector reads —
// never a credential).
type frUser struct {
	ID            string `json:"_id"`
	UserName      string `json:"userName"`
	GivenName     string `json:"givenName"`
	Sn            string `json:"sn"`
	Mail          string `json:"mail"`
	AccountStatus string `json:"accountStatus"`
}

func (u frUser) displayName() string {
	if full := strings.TrimSpace(u.GivenName + " " + u.Sn); full != "" {
		return full
	}
	return firstNonEmpty(u.UserName, u.ID)
}

type frRole struct {
	ID          string `json:"_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type frGroup struct {
	ID          string `json:"_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type frRef struct {
	Ref string `json:"_ref"`
}
