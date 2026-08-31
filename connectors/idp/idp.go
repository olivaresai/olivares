// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package idp is the Olivares AI identity connector for the two cloud identity
// providers, Okta and Microsoft Entra ID (Azure AD). A single connector serves
// both behind a "provider" setting (okta|entra): the wire shapes and the auth
// scheme differ, the contract it exposes does not. It discovers human users,
// non-human identities (Okta service apps / Entra service principals) and groups,
// and the membership edges between them, and exposes them as an
// identitysource.Graph to module VI (governance).
//
// It is read-only and minimal-data (docs/SECURITY-HARDENING.md-3): every directory call is a GET
// (the shared httpx client is GET-only by construction), and the connector pulls
// identity METADATA only — object ids, display names, login/UPN, mail, account
// status, group memberships — never a credential value. The operator credential
// (an Okta SSWS API token, or an Entra client-credentials secret) is declared
// Secret, held in memory, applied per request via the auth function, and never
// logged or persisted. With no credential the connector runs offline and returns
// an empty Graph (no error).
//
// What flows where. The roster (identities, groups, memberships) travels
// the typed Snapshot Graph: a group/role membership is identity→group reference
// data, not an access edge, and that rule is unchanged (the pattern). Gather
// emits the genuine identity→resource PERMITTED grants both directories hold —
// an Okta app-user assignment (the user may use that app) and an Entra app-role
// assignment or org-wide delegated scope — as model.SignalPolicy edges feeding
// the permitted-vs-observed diff (ARCHITECTURE.md). Honesty bounds on the Entra scan:
//
//   - A service principal with appRoleAssignmentRequired=false is usable by EVERY
//     tenant user, so the emitted assignment edges are grants, never a complete
//     reachability map (absence ≠ denial).
//   - Only admin-consented org-wide delegated scopes (consentType "AllPrincipals")
//     are read; per-user "Principal" consents are deliberately out — they are
//     individual user choices, not the org-level grant surface governance diffs.
//     Reading /oauth2PermissionGrants requires the Directory.Read.All application
//     permission, which this connector's directory reads effectively already need.
//   - Group members that are neither users, service principals nor nested groups
//     (devices, contacts) are skipped everywhere: the roster has no counterpart
//     row for them.
//
// It imports only the SDK, the Apache identitysource contract and the shared
// httpx read-only client — never the engine.
package idp

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
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.idp"

// Provider selects the backend.
const (
	providerOkta  = "okta"
	providerEntra = "entra"
)

// Default configuration values.
const (
	defaultEntraBase = "https://graph.microsoft.com/v1.0"
	defaultMaxPages  = 50
	// defaultMaxApps bounds how many apps (Okta apps / Entra service principals)
	// one Gather run scans for assignment grants; a truncated scan is surfaced as
	// a coverage finding, never silent (docs/SECURITY-HARDENING.md).
	defaultMaxApps = 500
)

// The ResourceKinds of a PERMITTED app grant: an identity is permitted to use an
// Okta app / an Entra application (service principal).
const (
	resOktaApp  = "okta.app"
	resEntraApp = "entra.app"
)

// spTypeServiceIdentity is the servicePrincipalType of an Entra Agent ID agent
// identity — the entra-agent connector's row, deferred here.
const spTypeServiceIdentity = "ServiceIdentity"

// The Microsoft Graph directoryObject discriminators ("@odata.type") this
// connector classifies in /groups/{id}/members rows. Anything outside this set
// (devices, contacts, …) has no roster counterpart and is skipped.
const (
	memberTypeUser  = "#microsoft.graph.user"
	memberTypeSP    = "#microsoft.graph.servicePrincipal"
	memberTypeGroup = "#microsoft.graph.group"
)

// oktaActiveStatuses is the closed set of Okta user statuses that count as an
// active account. Anything outside it (STAGED, SUSPENDED, DEPROVISIONED, …) is a
// disabled account — a governance signal, never guessed.
var oktaActiveStatuses = map[string]bool{
	"ACTIVE":      true,
	"PROVISIONED": true,
	"RECOVERY":    true,
	"LOCKED_OUT":  true,
}

// Source is the Okta/Entra identity connector. It satisfies sdk.SourceConnector
// (Gather emits the app-assignment permitted grants) and
// identitysource.GraphProvider (the directory roster). One instance serves a
// single configured provider.
type Source struct {
	provider      string
	baseURL       string
	apiToken      string // Okta SSWS token (Secret)
	tenantID      string // Entra tenant
	clientID      string // Entra app (client) id
	clientSecret  string // Entra client secret (Secret)
	oauthTokenURL string // Entra token endpoint (override for tests)
	maxPages      int
	maxApps       int // Gather's app-scan cap (Okta apps / Entra service principals)

	client *httpx.Client    // built in Open once the token is known
	doer   httpx.Doer       // injected transport (tests); nil => http.DefaultClient
	now    func() time.Time // injectable clock (tests); nil => time.Now
}

// Compile-time proof that Source satisfies both contracts.
var (
	_ sdk.SourceConnector          = (*Source)(nil)
	_ identitysource.GraphProvider = (*Source)(nil)
)

// New returns an idp connector with default configuration.
func New() *Source {
	return &Source{maxPages: defaultMaxPages, maxApps: defaultMaxApps}
}

// Descriptor returns the connector's self-description and declared configuration.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Okta / Microsoft Entra ID",
		Description: "Reads users, service identities and groups from Okta or Microsoft Entra ID (read-only metadata; never credentials).",
		ConfigFields: []sdk.ConfigField{
			{Key: "provider", Type: sdk.FieldString, Required: true, Description: "Identity provider: okta or entra."},
			{Key: "base_url", Type: sdk.FieldString, Description: "API base URL. Okta: https://<org>.okta.com (full URL). Entra: defaults to https://graph.microsoft.com/v1.0."},
			{Key: "api_token", Type: sdk.FieldString, Secret: true, Description: "Okta SSWS API token reference (read-only; never persisted). Empty = offline (empty graph)."},
			{Key: "tenant_id", Type: sdk.FieldString, Description: "Entra tenant id (directory id)."},
			{Key: "client_id", Type: sdk.FieldString, Description: "Entra application (client) id."},
			{Key: "client_secret", Type: sdk.FieldString, Secret: true, Description: "Entra client secret reference (read-only; never persisted). Empty = offline (empty graph)."},
			{Key: "oauth_token_url", Type: sdk.FieldString, Description: "Entra OAuth2 token endpoint override (defaults to login.microsoftonline.com/{tenant_id}/oauth2/v2.0/token)."},
			{Key: "max_pages", Type: sdk.FieldInt, Default: strconv.Itoa(defaultMaxPages), Description: "Pagination safety bound per list call."},
			{Key: "max_apps", Type: sdk.FieldInt, Default: strconv.Itoa(defaultMaxApps), Description: "Apps scanned for assignment grants per Gather run (Okta apps / Entra service principals); truncation is surfaced as a coverage finding."},
		},
	}
}

// Open reads configuration and validates the provider. It does not contact the
// network (the directory lifetime belongs to Snapshot), so the only error here is
// an unknown/missing provider. For Okta it builds the read-only SSWS client now;
// for Entra the Graph client is built in Snapshot after the OAuth token is
// fetched (the token is not known until then).
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.provider = strings.ToLower(strings.TrimSpace(cfg.Get("provider")))
	s.baseURL = strings.TrimRight(cfg.Get("base_url"), "/")
	s.apiToken = cfg.Get("api_token")
	s.tenantID = cfg.Get("tenant_id")
	s.clientID = cfg.Get("client_id")
	s.clientSecret = cfg.Get("client_secret")
	s.oauthTokenURL = cfg.Get("oauth_token_url")
	s.maxPages = cfg.GetInt("max_pages", s.maxPages)
	if s.maxPages <= 0 {
		s.maxPages = defaultMaxPages
	}
	s.maxApps = cfg.GetInt("max_apps", s.maxApps)
	if s.maxApps <= 0 {
		s.maxApps = defaultMaxApps
	}

	switch s.provider {
	case providerOkta:
		// Okta: SSWS token applied as "Authorization: SSWS <token>", guarded by the
		// token so an unconfigured connector sends no auth header at all.
		s.client = httpx.New(s.baseURL, s.doer, httpx.Header("Authorization", "SSWS "+s.apiToken, s.apiToken), nil)
	case providerEntra:
		if s.baseURL == "" {
			s.baseURL = defaultEntraBase
		}
		// Entra: the Graph client is built in Snapshot after the OAuth token POST.
	default:
		return fmt.Errorf("idp: provider must be %q or %q, got %q", providerOkta, providerEntra, s.provider)
	}
	return nil
}

// Gather emits the configured directory's identity→app PERMITTED grants as
// model.SignalPolicy edges: Okta app-user assignments, Entra app-role
// assignments (direct, and expanded over a group principal's direct user-typed
// members) and Entra org-wide delegated scopes. Each is a genuine
// identity→resource grant — the directory says this principal MAY use that app —
// unlike a group membership, which stays on the typed Snapshot Graph. Delivery
// is at-least-once: any transport or Emit error aborts the run and the engine
// retries; a re-emitted edge converges on its natural key (origin, resource,
// mode — the engine's upsert OR-merges the flags; only the occurrence count
// accumulates per pass), and the single per-run clock keeps first/last_seen
// stable.
// Origins the connector's own Snapshot would not roster are never emitted; they
// are counted into a single coverage finding per run (docs/SECURITY-HARDENING.md never-silent).
// With no credential it returns nil immediately (offline, like Snapshot).
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	switch s.provider {
	case providerOkta:
		return s.gatherOkta(ctx, sink)
	case providerEntra:
		return s.gatherEntra(ctx, sink)
	default:
		return fmt.Errorf("idp: unknown provider %q", s.provider)
	}
}

// emitCoverage surfaces one Gather run's coverage gaps as findings (docs/SECURITY-HARDENING.md
// never-silent): the max_apps truncation, and the count of permitted-grant
// origins the convergence invariant withheld because Snapshot would not roster
// them. At most one finding of each class per run.
func emitCoverage(ctx context.Context, sink sdk.Sink, maxApps int, truncated bool, unrostered int, now time.Time) error {
	if truncated {
		if err := sink.Emit(ctx, model.FindingReport{
			Kind:        "coverage",
			Severity:    model.SeverityInfo,
			SubjectKind: "identity_source",
			SubjectRef:  Name,
			Title:       "permitted-grant app scan truncated at " + strconv.Itoa(maxApps) + " apps; raise max_apps to scan the remainder",
			OccurredAt:  now,
		}); err != nil {
			return err
		}
	}
	if unrostered > 0 {
		if err := sink.Emit(ctx, model.FindingReport{
			Kind:        "coverage",
			Severity:    model.SeverityInfo,
			SubjectKind: "identity_source",
			SubjectRef:  Name,
			Title:       strconv.Itoa(unrostered) + " permitted-grant origins outside the rostered identity set were not emitted",
			OccurredAt:  now,
		}); err != nil {
			return err
		}
	}
	return nil
}

// Close releases resources; the connector holds no long-lived connection.
func (s *Source) Close(context.Context) error { return nil }

// Snapshot reads the configured directory read-only and assembles the identity
// graph. With no credential it returns the connector's offline (empty) graph, no
// error. It never returns credential material.
func (s *Source) Snapshot(ctx context.Context) (identitysource.Graph, error) {
	switch s.provider {
	case providerOkta:
		return s.snapshotOkta(ctx)
	case providerEntra:
		return s.snapshotEntra(ctx)
	default:
		return identitysource.Graph{}, fmt.Errorf("idp: unknown provider %q", s.provider)
	}
}

// clock returns the connector's time source (injectable for tests).
func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// transport returns the injected Doer or the default HTTP client. Used for the
// Entra token POST (the only non-GET call), so the same stub drives every call.
func (s *Source) transport() httpx.Doer {
	if s.doer != nil {
		return s.doer
	}
	return http.DefaultClient
}

// ---------------------------------------------------------------------------
// Okta backend
// ---------------------------------------------------------------------------

func (s *Source) snapshotOkta(ctx context.Context) (identitysource.Graph, error) {
	g := identitysource.Graph{Source: identitysource.SourceOkta, CapturedAt: s.clock().UTC()}
	if s.apiToken == "" || s.client == nil {
		return g, nil // offline
	}

	users, err := s.oktaUsers(ctx)
	if err != nil {
		return identitysource.Graph{}, err
	}
	g.Identities = append(g.Identities, users...)

	apps, err := s.oktaApps(ctx)
	if err != nil {
		return identitysource.Graph{}, err
	}
	g.Identities = append(g.Identities, apps...)

	groups, err := s.oktaGroups(ctx)
	if err != nil {
		return identitysource.Graph{}, err
	}
	for _, grp := range groups {
		g.Collections = append(g.Collections, identitysource.Collection{
			Ref:         grp.ID,
			Kind:        identitysource.KindGroup,
			DisplayName: grp.Profile.Name,
			Source:      identitysource.SourceOkta,
		})
		members, err := s.oktaGroupMembers(ctx, grp.ID)
		if err != nil {
			return identitysource.Graph{}, err
		}
		for _, m := range members {
			g.Memberships = append(g.Memberships, identitysource.Membership{
				MemberRef:     m.ID,
				MemberKind:    identitysource.MemberIdentity,
				CollectionRef: grp.ID,
				Source:        identitysource.SourceOkta,
			})
		}
	}
	return g, nil
}

// oktaUsers lists /api/v1/users, following the Link rel="next" pagination header.
func (s *Source) oktaUsers(ctx context.Context) ([]identitysource.Identity, error) {
	var out []identitysource.Identity
	path := "/api/v1/users"
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var page []oktaUser
		next, err := s.oktaGetPaged(ctx, path, nil, &page)
		if err != nil {
			return nil, err
		}
		for _, u := range page {
			out = append(out, oktaUserIdentity(u))
		}
		if next == "" {
			break
		}
		path = next
	}
	return out, nil
}

// oktaApps maps the raw app listing to non-human identities (service apps).
func (s *Source) oktaApps(ctx context.Context) ([]identitysource.Identity, error) {
	apps, err := s.oktaAppList(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]identitysource.Identity, 0, len(apps))
	for _, a := range apps {
		out = append(out, identitysource.Identity{
			Ref:         a.ID,
			Type:        identitysource.PrincipalNHI,
			Kind:        "service_app",
			DisplayName: a.Label,
			Source:      identitysource.SourceOkta,
			Disabled:    a.Status != "" && a.Status != "ACTIVE",
		})
	}
	return out, nil
}

// oktaAppList fetches the raw /api/v1/apps rows (Link pagination, limit=200 —
// the apps endpoint's page max). It is the fetch Snapshot's roster mapping and
// Gather's assignment scan share; each half calls it independently (no
// cross-half cache).
func (s *Source) oktaAppList(ctx context.Context) ([]oktaApp, error) {
	var out []oktaApp
	path := "/api/v1/apps"
	query := url.Values{"limit": {"200"}}
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var page []oktaApp
		next, err := s.oktaGetPaged(ctx, path, query, &page)
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if next == "" {
			break
		}
		path, query = next, nil // next link is absolute and self-contained
	}
	return out, nil
}

// oktaDeadAssignmentStatuses are the app-user statuses whose ASSIGNMENT itself
// is revoked — a dead grant that permits nothing, skipped like an inactive
// credential. Every other status (the wire knows 12) still holds the grant: a
// disabled ACCOUNT keeps its assignment, and the roster's Disabled flag is the
// governance signal for that, never a reason to hide the grant.
var oktaDeadAssignmentStatuses = map[string]bool{
	"DEPROVISIONED": true,
	"REVOKED":       true,
}

// gatherOkta scans each ACTIVE app's user assignments and emits one PERMITTED
// edge per live assignment: the directory says this user may use that app
// (R/W is honestly unknown — an assignment grants use, not a mode). Inactive
// apps serve nothing, so they are not scanned (a resource-level filter, not an
// identity guess). Origins absent from the user roster are counted, never
// emitted (convergence with Snapshot's roster).
func (s *Source) gatherOkta(ctx context.Context, sink sdk.Sink) error {
	if s.apiToken == "" || s.client == nil {
		return nil // offline
	}
	now := s.clock().UTC()

	// The convergence set: the same roster helper Snapshot uses, fetched once
	// per run (an independent call — no cross-half cache).
	users, err := s.oktaUsers(ctx)
	if err != nil {
		return err
	}
	rostered := make(map[string]bool, len(users))
	for _, u := range users {
		rostered[u.Ref] = true
	}

	apps, err := s.oktaAppList(ctx)
	if err != nil {
		return err
	}

	scanned, truncated, unrostered := 0, false, 0
	for _, app := range apps {
		if err := ctx.Err(); err != nil {
			return err
		}
		if app.Status != "ACTIVE" {
			continue // an inactive app serves nothing
		}
		if scanned >= s.maxApps {
			truncated = true
			break
		}
		scanned++
		assignments, err := s.oktaAppUsers(ctx, app.ID)
		if err != nil {
			return err
		}
		for _, au := range assignments {
			if au.ID == "" || oktaDeadAssignmentStatuses[au.Status] {
				continue // the assignment itself is revoked — a dead grant
			}
			if !rostered[au.ID] {
				unrostered++ // outside Snapshot's roster: counted, never emitted
				continue
			}
			if err := sink.Emit(ctx, model.EdgeObservation{
				OriginKind:   "identity",
				OriginRef:    au.ID,
				ResourceKind: resOktaApp,
				ResourceRef:  app.ID,
				Mode:         model.ModeUnknown,
				Source:       model.SignalPolicy,
				Confidence:   model.ConfidenceAttributed,
				ObservedAt:   now,
			}); err != nil {
				return err
			}
		}
	}
	return emitCoverage(ctx, sink, s.maxApps, truncated, unrostered, now)
}

// oktaAppUsers lists /api/v1/apps/{id}/users (Link pagination, limit=500 — this
// endpoint's page max; its default would be 50). Each row decodes to exactly
// {id, scope, status}: the wire row also carries credentials{userName,
// password{}} and a profile of arbitrary PII, whose fields are deliberately not
// declared so the decoder drops them (minimal-data, docs/SECURITY-HARDENING.md).
func (s *Source) oktaAppUsers(ctx context.Context, appID string) ([]oktaAppUser, error) {
	var out []oktaAppUser
	path := "/api/v1/apps/" + url.PathEscape(appID) + "/users"
	query := url.Values{"limit": {"500"}}
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var page []oktaAppUser
		next, err := s.oktaGetPaged(ctx, path, query, &page)
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if next == "" {
			break
		}
		path, query = next, nil
	}
	return out, nil
}

// oktaGroups lists /api/v1/groups.
func (s *Source) oktaGroups(ctx context.Context) ([]oktaGroup, error) {
	var out []oktaGroup
	path := "/api/v1/groups"
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var page []oktaGroup
		next, err := s.oktaGetPaged(ctx, path, nil, &page)
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if next == "" {
			break
		}
		path = next
	}
	return out, nil
}

// oktaGroupMembers lists /api/v1/groups/{id}/users.
func (s *Source) oktaGroupMembers(ctx context.Context, groupID string) ([]oktaUser, error) {
	var out []oktaUser
	path := "/api/v1/groups/" + url.PathEscape(groupID) + "/users"
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var page []oktaUser
		next, err := s.oktaGetPaged(ctx, path, nil, &page)
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if next == "" {
			break
		}
		path = next
	}
	return out, nil
}

// oktaGetPaged issues one GET via GetRaw so the Link header is readable, decodes
// the JSON array into out, and returns the absolute next-page URL (rel="next") or
// "" when there is no next page.
func (s *Source) oktaGetPaged(ctx context.Context, path string, query url.Values, out any) (string, error) {
	resp, err := s.client.GetRaw(ctx, path, query)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out); err != nil {
		return "", fmt.Errorf("idp: decode %s: %w", path, err)
	}
	return nextLink(resp.Header.Values("Link")), nil
}

// nextLink parses RFC 5988 Link header values and returns the URL whose
// rel="next", or "" when absent. Okta returns multiple Link headers (self, next).
func nextLink(links []string) string {
	for _, header := range links {
		for _, part := range strings.Split(header, ",") {
			segs := strings.Split(part, ";")
			if len(segs) < 2 {
				continue
			}
			urlPart := strings.TrimSpace(segs[0])
			if !strings.HasPrefix(urlPart, "<") || !strings.HasSuffix(urlPart, ">") {
				continue
			}
			isNext := false
			for _, attr := range segs[1:] {
				attr = strings.TrimSpace(attr)
				if attr == `rel="next"` || attr == "rel=next" {
					isNext = true
				}
			}
			if isNext {
				return urlPart[1 : len(urlPart)-1]
			}
		}
	}
	return ""
}

// oktaUserIdentity maps one Okta user to an Identity.
func oktaUserIdentity(u oktaUser) identitysource.Identity {
	display := u.Profile.DisplayName
	if display == "" {
		display = u.Profile.Login
	}
	id := identitysource.Identity{
		Ref:         u.ID,
		Type:        identitysource.PrincipalHuman,
		Kind:        "user",
		DisplayName: display,
		Source:      identitysource.SourceOkta,
		Disabled:    !oktaActiveStatuses[u.Status],
		Attributes:  map[string]string{},
	}
	if u.Profile.Login != "" {
		id.Attributes["login"] = u.Profile.Login
	}
	if u.Profile.Email != "" {
		id.Attributes["email"] = u.Profile.Email
	}
	if len(id.Attributes) == 0 {
		id.Attributes = nil
	}
	return id
}

// Okta wire shapes (only the fields the connector reads — never a credential).
type oktaUser struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Profile struct {
		Login       string `json:"login"`
		Email       string `json:"email"`
		DisplayName string `json:"displayName"`
	} `json:"profile"`
}

type oktaApp struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Status string `json:"status"`
}

// oktaAppUser is one app-assignment row from /api/v1/apps/{id}/users, reduced to
// the three fields the grant scan reads. ID is the Okta USER id (the assignment
// has no id of its own worth carrying); Scope distinguishes a direct ("USER")
// from a group-derived ("GROUP") assignment — both are live grants and emit
// identically. The wire row's credentials and profile objects are NOT declared
// here on purpose: the decoder must drop them on the floor.
type oktaAppUser struct {
	ID     string `json:"id"`
	Scope  string `json:"scope"`
	Status string `json:"status"`
}

type oktaGroup struct {
	ID      string `json:"id"`
	Profile struct {
		Name string `json:"name"`
	} `json:"profile"`
}

// ---------------------------------------------------------------------------
// Entra (Microsoft Graph) backend
// ---------------------------------------------------------------------------

func (s *Source) snapshotEntra(ctx context.Context) (identitysource.Graph, error) {
	g := identitysource.Graph{Source: identitysource.SourceEntra, CapturedAt: s.clock().UTC()}
	if s.clientID == "" || s.clientSecret == "" {
		return g, nil // offline: no client-credentials configured
	}

	token, err := s.entraToken(ctx)
	if err != nil {
		return identitysource.Graph{}, err
	}
	if token == "" {
		return g, nil // defensive: no token => offline
	}
	// Build the Graph client with a bearer token. The token lives only on this
	// call's stack + the client's auth closure; it is never logged or persisted.
	client := httpx.New(s.baseURL, s.doer, httpx.Bearer(token), nil)

	users, err := s.entraUsers(ctx, client)
	if err != nil {
		return identitysource.Graph{}, err
	}
	g.Identities = append(g.Identities, users...)

	sps, deferred, err := s.entraServicePrincipals(ctx, client)
	if err != nil {
		return identitysource.Graph{}, err
	}
	g.Identities = append(g.Identities, sps...)
	g.DeferredAgentIdentities = len(deferred)

	groups, err := s.entraGroups(ctx, client)
	if err != nil {
		return identitysource.Graph{}, err
	}
	for _, grp := range groups {
		g.Collections = append(g.Collections, identitysource.Collection{
			Ref:         grp.ID,
			Kind:        identitysource.KindGroup,
			DisplayName: grp.DisplayName,
			Source:      identitysource.SourceEntra,
		})
		members, err := s.entraGroupMembers(ctx, client, grp.ID)
		if err != nil {
			return identitysource.Graph{}, err
		}
		for _, m := range members {
			kind, ok := entraMemberKind(m.Type)
			if !ok || m.ID == "" {
				continue // a device/contact member: the roster has no counterpart row
			}
			g.Memberships = append(g.Memberships, identitysource.Membership{
				MemberRef:     m.ID,
				MemberKind:    kind,
				CollectionRef: grp.ID,
				Source:        identitysource.SourceEntra,
			})
		}
	}
	return g, nil
}

// entraMemberKind classifies a group member by its directoryObject
// discriminator: users and service principals are roster identities, a nested
// group is a nested collection (NOT an identity — a group of groups must walk
// as transitive membership, not masquerade as a principal), and anything else
// (devices, contacts) has no roster counterpart, so ok is false and the member
// is skipped.
func entraMemberKind(odataType string) (identitysource.MemberKind, bool) {
	switch odataType {
	case memberTypeUser, memberTypeSP:
		return identitysource.MemberIdentity, true
	case memberTypeGroup:
		return identitysource.MemberCollection, true
	default:
		return "", false
	}
}

// entraToken performs the OAuth2 client-credentials grant against the token
// endpoint using the SAME injected transport, so a test stubs it. It returns the
// access token (held only in memory); a non-2xx is an error that never carries
// the client secret.
func (s *Source) entraToken(ctx context.Context) (string, error) {
	tokenURL := s.oauthTokenURL
	if tokenURL == "" {
		if s.tenantID == "" {
			return "", fmt.Errorf("idp: entra requires tenant_id (or oauth_token_url) for the token endpoint")
		}
		tokenURL = "https://login.microsoftonline.com/" + url.PathEscape(s.tenantID) + "/oauth2/v2.0/token"
	}
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {s.clientID},
		"client_secret": {s.clientSecret},
		"scope":         {"https://graph.microsoft.com/.default"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("idp: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := s.transport().Do(req)
	if err != nil {
		return "", fmt.Errorf("idp: token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		// The excerpt is the provider's error body; the request form (which holds the
		// secret) is never included.
		return "", fmt.Errorf("idp: token endpoint status %d: %s", resp.StatusCode, strings.TrimSpace(string(excerpt)))
	}
	var tr struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tr); err != nil {
		return "", fmt.Errorf("idp: decode token response: %w", err)
	}
	return tr.AccessToken, nil
}

// entraUsers lists /users, following @odata.nextLink pagination.
func (s *Source) entraUsers(ctx context.Context, client *httpx.Client) ([]identitysource.Identity, error) {
	var out []identitysource.Identity
	path := "/users"
	query := url.Values{"$select": {"id,displayName,userPrincipalName,accountEnabled,mail"}}
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var resp entraUsersResponse
		if err := client.GetJSON(ctx, path, query, &resp); err != nil {
			return nil, err
		}
		for _, u := range resp.Value {
			id := identitysource.Identity{
				Ref:         u.ID,
				Type:        identitysource.PrincipalHuman,
				Kind:        "user",
				DisplayName: u.DisplayName,
				Source:      identitysource.SourceEntra,
				Disabled:    !u.AccountEnabled,
				Attributes:  map[string]string{},
			}
			if u.UserPrincipalName != "" {
				id.Attributes["upn"] = u.UserPrincipalName
			}
			if u.Mail != "" {
				id.Attributes["mail"] = u.Mail
			}
			if len(id.Attributes) == 0 {
				id.Attributes = nil
			}
			out = append(out, id)
		}
		if resp.NextLink == "" {
			break
		}
		path, query = resp.NextLink, nil // next link is absolute and self-contained
	}
	return out, nil
}

// entraServicePrincipals lists /servicePrincipals as non-human identities.
//
// Entra Agent ID agent identities ARE service principals (subtype
// agentIdentity, servicePrincipalType "ServiceIdentity"), so they surface in this
// plain listing too. They are SKIPPED here on purpose: the dedicated entra-agent
// connector owns them — it stamps the per-agent SourceEntraAgent/KindAgentIdentity
// provenance the attribution axis treats as firm. Letting both connectors
// emit the same object id would make the converged row's Provider flap between
// "entra" and "entra-agent" on alternating syncs (governance reconciles by
// external_id alone, last sync wins), silently toggling firmness. The skip is
// COUNTED (deferred return) and surfaced on Graph.DeferredAgentIdentities, so an
// estate running idp without entra-agent sees the unwatched class in every
// roster-sync audit record rather than a silent hole (docs/SECURITY-HARDENING.md). The deferred
// return is the SET of skipped object ids: Snapshot folds its size into
// Graph.DeferredAgentIdentities, and Gather uses it to refuse a deferred
// principal on an app-role assignment (ownership cuts both ways).
func (s *Source) entraServicePrincipals(ctx context.Context, client *httpx.Client) (out []identitysource.Identity, deferred map[string]bool, err error) {
	path := "/servicePrincipals"
	query := url.Values{"$select": {"id,displayName,accountEnabled,servicePrincipalType,appRoleAssignmentRequired"}}
	deferred = map[string]bool{}
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		var resp entraSPsResponse
		if err := client.GetJSON(ctx, path, query, &resp); err != nil {
			return nil, nil, err
		}
		for _, sp := range resp.Value {
			if sp.ServicePrincipalType == spTypeServiceIdentity {
				deferred[sp.ID] = true // an Entra Agent ID agent identity — the entra-agent connector's row
				continue
			}
			out = append(out, identitysource.Identity{
				Ref:         sp.ID,
				Type:        identitysource.PrincipalNHI,
				Kind:        "service_principal",
				DisplayName: sp.DisplayName,
				Source:      identitysource.SourceEntra,
				Disabled:    !sp.AccountEnabled,
			})
		}
		if resp.NextLink == "" {
			break
		}
		path, query = resp.NextLink, nil
	}
	return out, deferred, nil
}

// entraGroups lists /groups.
func (s *Source) entraGroups(ctx context.Context, client *httpx.Client) ([]entraGroup, error) {
	var out []entraGroup
	path := "/groups"
	query := url.Values{"$select": {"id,displayName"}}
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var resp entraGroupsResponse
		if err := client.GetJSON(ctx, path, query, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Value...)
		if resp.NextLink == "" {
			break
		}
		path, query = resp.NextLink, nil
	}
	return out, nil
}

// entraGroupMembers lists /groups/{id}/members and returns the TYPED member
// rows: Graph includes the "@odata.type" directoryObject discriminator alongside
// $select=id, and classification needs it — a bare id cannot tell a user from a
// nested group from a device. Shared by Snapshot (membership kinds) and Gather
// (group-assignment expansion over direct user-typed members).
func (s *Source) entraGroupMembers(ctx context.Context, client *httpx.Client, groupID string) ([]entraMember, error) {
	var out []entraMember
	path := "/groups/" + url.PathEscape(groupID) + "/members"
	query := url.Values{"$select": {"id"}}
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var resp entraMembersResponse
		if err := client.GetJSON(ctx, path, query, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Value...)
		if resp.NextLink == "" {
			break
		}
		path, query = resp.NextLink, nil
	}
	return out, nil
}

// gatherEntra scans each non-deferred service principal's app-role assignments
// (/appRoleAssignedTo) and the tenant's org-wide delegated scopes
// (/oauth2PermissionGrants, consentType AllPrincipals) and emits one PERMITTED
// edge per resolvable grant. A "User"/"ServicePrincipal" principal emits
// directly; a "Group" principal is expanded over the group's DIRECT user-typed
// members — Entra does not cascade nested groups for app roles, so the faithful
// expansion does not either — each expanded edge carrying ToolRef
// "group:<group object id>". A deferred ServiceIdentity is never scanned as a
// resource nor emitted as a principal (the entra-agent connector owns that
// surface); it and any other unrosterable principal are counted, never emitted.
func (s *Source) gatherEntra(ctx context.Context, sink sdk.Sink) error {
	if s.clientID == "" || s.clientSecret == "" {
		return nil // offline
	}
	now := s.clock().UTC()

	// Gather mints its own token (an independent call, like every fetch shared
	// with Snapshot — no cross-half cache).
	token, err := s.entraToken(ctx)
	if err != nil {
		return err
	}
	if token == "" {
		return nil // defensive: no token => offline
	}
	client := httpx.New(s.baseURL, s.doer, httpx.Bearer(token), nil)

	// The convergence sets: the same roster helpers Snapshot uses. An emitted
	// origin must be a rostered user or a rostered (non-deferred) SP.
	users, err := s.entraUsers(ctx, client)
	if err != nil {
		return err
	}
	rosteredUsers := make(map[string]bool, len(users))
	for _, u := range users {
		rosteredUsers[u.Ref] = true
	}
	sps, deferred, err := s.entraServicePrincipals(ctx, client)
	if err != nil {
		return err
	}
	rosteredSPs := make(map[string]bool, len(sps))
	for _, sp := range sps {
		rosteredSPs[sp.Ref] = true
	}

	emit := func(originRef, resourceRef, toolRef string) error {
		return sink.Emit(ctx, model.EdgeObservation{
			OriginKind:   "identity",
			OriginRef:    originRef,
			ResourceKind: resEntraApp,
			ResourceRef:  resourceRef,
			Mode:         model.ModeUnknown, // an assignment grants use; R/W is honestly unknown
			Source:       model.SignalPolicy,
			Confidence:   model.ConfidenceAttributed,
			ToolRef:      toolRef,
			ObservedAt:   now,
		})
	}

	// Per-run memo: one group often backs assignments on several apps.
	memberCache := map[string][]entraMember{}
	groupMembers := func(gid string) ([]entraMember, error) {
		if m, ok := memberCache[gid]; ok {
			return m, nil
		}
		m, err := s.entraGroupMembers(ctx, client, gid)
		if err != nil {
			return nil, err
		}
		memberCache[gid] = m
		return m, nil
	}

	scanned, truncated, unrostered := 0, false, 0
	for _, sp := range sps {
		if err := ctx.Err(); err != nil {
			return err
		}
		if scanned >= s.maxApps {
			truncated = true
			break
		}
		scanned++
		assignments, err := s.entraAppRoleAssignedTo(ctx, client, sp.Ref)
		if err != nil {
			return err
		}
		for _, a := range assignments {
			switch a.PrincipalType {
			case "User":
				if !rosteredUsers[a.PrincipalID] {
					unrostered++
					continue
				}
				if err := emit(a.PrincipalID, sp.Ref, ""); err != nil {
					return err
				}
			case "ServicePrincipal":
				if deferred[a.PrincipalID] || !rosteredSPs[a.PrincipalID] {
					unrostered++ // deferred (ownership) or unknown: counted, never emitted
					continue
				}
				if err := emit(a.PrincipalID, sp.Ref, ""); err != nil {
					return err
				}
			case "Group":
				members, err := groupMembers(a.PrincipalID)
				if err != nil {
					return err
				}
				for _, m := range members {
					if m.Type != memberTypeUser {
						continue // app roles reach only the group's direct USERS; nested groups/devices/SPs gain nothing
					}
					if !rosteredUsers[m.ID] {
						unrostered++
						continue
					}
					if err := emit(m.ID, sp.Ref, "group:"+a.PrincipalID); err != nil {
						return err
					}
				}
			default:
				unrostered++ // a principal class the roster has no counterpart for
			}
		}
	}

	// Org-wide delegated scopes: admin-consented AllPrincipals grants only (the
	// org-level grant surface); per-user Principal consents are out by design.
	grants, err := s.entraOAuth2Grants(ctx, client)
	if err != nil {
		return err
	}
	for _, gr := range grants {
		if deferred[gr.ClientID] || !rosteredSPs[gr.ClientID] {
			unrostered++
			continue
		}
		if err := emit(gr.ClientID, gr.ResourceID, truncateScope(gr.Scope)); err != nil {
			return err
		}
	}

	return emitCoverage(ctx, sink, s.maxApps, truncated, unrostered, now)
}

// entraAppRoleAssignedTo lists /servicePrincipals/{id}/appRoleAssignedTo —
// every principal granted an app role ON the given (resource) service
// principal — reduced to {principalId, principalType} per row.
func (s *Source) entraAppRoleAssignedTo(ctx context.Context, client *httpx.Client, spID string) ([]entraAppRoleAssignment, error) {
	var out []entraAppRoleAssignment
	path := "/servicePrincipals/" + url.PathEscape(spID) + "/appRoleAssignedTo"
	query := url.Values{"$select": {"principalId,principalType"}}
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var resp entraAppRoleAssignmentsResponse
		if err := client.GetJSON(ctx, path, query, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Value...)
		if resp.NextLink == "" {
			break
		}
		path, query = resp.NextLink, nil
	}
	return out, nil
}

// entraOAuth2Grants lists the tenant's ADMIN-CONSENTED, org-wide delegated
// permission grants (/oauth2PermissionGrants filtered to consentType
// 'AllPrincipals'). Per-user ("Principal") consents are deliberately not read:
// they are individual user choices, not the org-level grant surface, and
// pulling them would enumerate user→app consent pairs the governance diff does
// not need (minimal-data). Requires Directory.Read.All, which the directory
// reads above effectively already need.
func (s *Source) entraOAuth2Grants(ctx context.Context, client *httpx.Client) ([]entraOAuth2Grant, error) {
	var out []entraOAuth2Grant
	path := "/oauth2PermissionGrants"
	query := url.Values{"$filter": {"consentType eq 'AllPrincipals'"}}
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var resp entraOAuth2GrantsResponse
		if err := client.GetJSON(ctx, path, query, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Value...)
		if resp.NextLink == "" {
			break
		}
		path, query = resp.NextLink, nil
	}
	return out, nil
}

// maxScopeRunes bounds the delegated-scope string an edge's ToolRef carries.
const maxScopeRunes = 256

// truncateScope normalizes a space-separated scope string for ToolRef: trimmed,
// and cut to maxScopeRunes runes with a trailing "…" when longer (governance
// needs to recognize the grant, not archive an unbounded provider string).
func truncateScope(scope string) string {
	scope = strings.TrimSpace(scope)
	runes := []rune(scope)
	if len(runes) <= maxScopeRunes {
		return scope
	}
	return string(runes[:maxScopeRunes]) + "…"
}

// Entra (Microsoft Graph) wire shapes (only the fields the connector reads).
type entraUsersResponse struct {
	NextLink string `json:"@odata.nextLink"`
	Value    []struct {
		ID                string `json:"id"`
		DisplayName       string `json:"displayName"`
		UserPrincipalName string `json:"userPrincipalName"`
		AccountEnabled    bool   `json:"accountEnabled"`
		Mail              string `json:"mail"`
	} `json:"value"`
}

// entraSP is one raw /servicePrincipals row.
type entraSP struct {
	ID             string `json:"id"`
	DisplayName    string `json:"displayName"`
	AccountEnabled bool   `json:"accountEnabled"`
	// ServicePrincipalType distinguishes Entra Agent ID agent identities
	// ("ServiceIdentity"), which the entra-agent connector owns.
	ServicePrincipalType string `json:"servicePrincipalType"`
	// AppRoleAssignmentRequired=false means EVERY tenant user can use the app,
	// so the assignment edges Gather emits are grants, never a complete
	// reachability map (see the package doc: absence ≠ denial).
	AppRoleAssignmentRequired bool `json:"appRoleAssignmentRequired"`
}

type entraSPsResponse struct {
	NextLink string    `json:"@odata.nextLink"`
	Value    []entraSP `json:"value"`
}

type entraGroup struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

type entraGroupsResponse struct {
	NextLink string       `json:"@odata.nextLink"`
	Value    []entraGroup `json:"value"`
}

// entraMember is one /groups/{id}/members row: the object id plus the
// "@odata.type" directoryObject discriminator Graph returns alongside
// $select=id (the members collection is heterogeneous — users, service
// principals, nested groups, devices).
type entraMember struct {
	ID   string `json:"id"`
	Type string `json:"@odata.type"`
}

type entraMembersResponse struct {
	NextLink string        `json:"@odata.nextLink"`
	Value    []entraMember `json:"value"`
}

// entraAppRoleAssignment is one /servicePrincipals/{id}/appRoleAssignedTo row,
// reduced to the principal half (the resource half is the SP being scanned).
type entraAppRoleAssignment struct {
	PrincipalID string `json:"principalId"`
	// PrincipalType is "User", "Group" or "ServicePrincipal".
	PrincipalType string `json:"principalType"`
}

type entraAppRoleAssignmentsResponse struct {
	NextLink string                   `json:"@odata.nextLink"`
	Value    []entraAppRoleAssignment `json:"value"`
}

// entraOAuth2Grant is one /oauth2PermissionGrants row: the client SP granted
// the space-separated delegated scopes on the resource SP.
type entraOAuth2Grant struct {
	ClientID   string `json:"clientId"`
	ResourceID string `json:"resourceId"`
	Scope      string `json:"scope"`
}

type entraOAuth2GrantsResponse struct {
	NextLink string             `json:"@odata.nextLink"`
	Value    []entraOAuth2Grant `json:"value"`
}
