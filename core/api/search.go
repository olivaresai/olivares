// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The federated console search: GET /v1/search fans out server-side to
// every registered kind provider and returns only names the caller is entitled
// to list. Authorization is deny-closed PER KIND — each kind declares the same
// read permission its feature's list route requires, and a caller who lacks it
// never learns whether the kind matched (the kind is skipped, not errored).
// Results carry entity names and a small non-sensitive detail string only:
// never config values, secret references, endpoints or spec bodies.

const (
	// searchMaxQueryLen bounds the query; anything longer is a client error.
	searchMaxQueryLen = 100
	// searchPerKindLimit caps results per kind so one noisy kind cannot starve
	// the rest of the ⌘K dropdown.
	searchPerKindLimit = 5
	// searchScanLimit bounds how many records a provider scans per request.
	// Console search is a bounded name scan, not a paginated browse: a tenant
	// with more entities than this in one kind gets Truncated=true, and the
	// feature's own list view (with real keyset pagination) is the exhaustive
	// surface.
	searchScanLimit = 500
)

// SearchResult is one federated search hit. Name is the entity's display name;
// Detail is a short NON-SENSITIVE annotation (status, kind) — providers must
// never put config values, credentials, endpoints or spec content here.
type SearchResult struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Name   string `json:"name"`
	Detail string `json:"detail,omitempty"`
}

// SearchKind is one searchable entity kind: the result kind tag, the permission
// its results are gated on, and the provider that scans for matches. Search
// receives the request's tenant-pinned ModuleContext, the normalized
// (lower-cased, trimmed) query and a result limit; it returns matches in
// name order.
type SearchKind struct {
	// Kind tags results (e.g. "eventing.subscription"); the console maps it to
	// a feature route. Unique across the deployment (enforced at mount).
	Kind string
	// Permission gates the kind: it must be the SAME read permission the
	// feature's list route requires, so search can never widen read access.
	Permission auth.Permission
	// System marks a deployment-wide (superadmin) kind: authorization runs
	// against the system tenant, exactly like the feature's authzSystem gate.
	System bool
	// Search scans for q (already lower-cased) and returns up to limit matches.
	Search func(ctx context.Context, mc ModuleContext, q string, limit int) ([]SearchResult, error)
}

// Searcher is optionally implemented by an api.Module to contribute kinds to
// the federated console search. The engine collects kinds at mount time.
type Searcher interface {
	SearchKinds() []SearchKind
}

// searchResponseDTO is the GET /v1/search response.
type searchResponseDTO struct {
	Results []SearchResult `json:"results"`
	// Truncated reports that at least one kind had more matches than the
	// per-kind cap (or scanned to its bound) — the console shows a "refine your
	// query" hint instead of pretending the list is exhaustive.
	Truncated bool `json:"truncated"`
	// Degraded reports that at least one kind could NOT be searched: its provider
	// returned an error and the kind was skipped.
	//
	// IT IS A DIFFERENT ANSWER FROM Truncated AND THAT IS THE WHOLE POINT (2026-08-06).
	// Truncated means "there are more of these than fit"; degraded means "a source
	// blew up and this list is missing whatever it held". Until today a failed
	// provider was logged at WARN, skipped, and the response still said
	// `truncated:false` — i.e. it published an incomplete list as an exhaustive one.
	// In a compliance product that is the same class of lie the console already
	// refuses elsewhere: "you have no legal holds" and "the server did not answer"
	// cannot be the same screen.
	Degraded bool `json:"degraded"`
	// DegradedKinds names them, because "something failed" is not actionable and
	// "the audit kind failed" is. Never nil in the wire form: an absent list and an
	// empty one must not be told apart by whether the field is there.
	DegradedKinds []string `json:"degraded_kinds"`
}

// handleSearch is GET /v1/search?q= — the federated, RBAC-aware console search.
// Any authenticated tenant principal may call it; every kind is gated on its
// own permission (deny-closed), so the response only ever narrows what the
// caller could already list feature by feature. No self-audit: each result is
// a name from a list surface the caller is entitled to read, and the audited
// read remains the feature's own list/get route.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFrom(r.Context())
	if !ok {
		s.writeError(w, r, auth.ErrUnauthenticated)
		return
	}
	tenant, err := s.resolveTenant(r, p)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if q == "" || len(q) > searchMaxQueryLen {
		s.writeError(w, r, errBadRequest)
		return
	}

	ctx := r.Context()
	out := searchResponseDTO{Results: []SearchResult{}, DegradedKinds: []string{}}
	for _, k := range s.searchKinds {
		authTenant := tenant
		if k.System {
			authTenant = model.SystemTenantID
		}
		dec := s.authz.Authorize(ctx, auth.Request{
			Principal:  p,
			Permission: k.Permission,
			Tenant:     authTenant,
			Resource:   auth.ResourceFor(k.Permission),
		})
		if !dec.Allow {
			continue
		}
		// B-03: federated search reaches the same module data as a module
		// route, so it carries the same confinement mark — otherwise search would
		// be the way around the seat. It is marked per kind, after that kind's own
		// authorization, matching how the module routes do it.
		kindCtx := withModuleRequestBoundary(ctx, tenant, p)
		mc := ModuleContext{Principal: p, Tenant: tenant, Data: NewScopedData(s.st, tenant)}
		res, err := k.Search(kindCtx, mc, q, searchPerKindLimit+1)
		if err != nil {
			// One broken provider degrades that kind, never the whole search — and the
			// CALLER is told, which is what changed. Logging it at WARN and returning a
			// response that still claimed completeness meant the only party who could act
			// on the gap was the one reading the server's logs, not the one holding the
			// half-empty list.
			s.log.Warn("api: search provider failed; kind skipped",
				"kind", k.Kind, "err", err, "request_id", requestID(ctx))
			out.Degraded = true
			out.DegradedKinds = append(out.DegradedKinds, k.Kind)
			continue
		}
		if len(res) > searchPerKindLimit {
			res = res[:searchPerKindLimit]
			out.Truncated = true
		}
		out.Results = append(out.Results, res...)
	}
	writeJSON(w, http.StatusOK, out)
}

// coreSearchKinds are the kinds owned by the core surface (module kinds are
// collected from Searcher modules at mount).
func (s *Server) coreSearchKinds() []SearchKind {
	return []SearchKind{
		{
			Kind:       "workspace",
			Permission: "tenant:read",
			Search:     s.searchWorkspaces,
		},
		{
			Kind:       "user",
			Permission: "user:read",
			Search:     s.searchUsers,
		},
		{
			// Configured ingestion sources are a deployment-wide superadmin
			// surface: same system:admin gate as /v1/console/sources.
			Kind:       "connector",
			Permission: "system:admin",
			System:     true,
			Search:     s.searchConnectors,
		},
	}
}

func (s *Server) searchWorkspaces(ctx context.Context, mc ModuleContext, q string, limit int) ([]SearchResult, error) {
	// intentionally NOT workspace-confined. The dedicated list endpoint
	// (handleListWorkspaces) authorizes tenant:read and returns ALL of the tenant's
	// workspaces with no confinement filter (unlike handleListAgentGroups, which
	// applies parseFilteredListQuery) — workspaces are deliberately tenant-global to a
	// tenant:read holder. Search stays consistent with that: it reveals exactly what
	// the list endpoint reveals, never more. (Whether the workspace *list* itself
	// should confine is a separate policy question, out of scope for this user leak.)
	var out []SearchResult
	err := mc.Data.View(ctx, func(sc store.Scope) error {
		wss, _, err := sc.Workspaces().List(ctx, model.Query{Limit: searchScanLimit})
		if err != nil {
			return err
		}
		for _, ws := range wss {
			if !strings.Contains(strings.ToLower(ws.Name), q) &&
				!strings.Contains(strings.ToLower(ws.Slug), q) {
				continue
			}
			out = append(out, SearchResult{
				Kind: "workspace", ID: ws.ID.String(), Name: ws.Name, Detail: ws.Slug,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sortSearchResults(out)
	return capSearchResults(out, limit), nil
}

func (s *Server) searchUsers(ctx context.Context, mc ModuleContext, q string, limit int) ([]SearchResult, error) {
	// The roster is exactly what user:read already exposes on /v1/members
	// (display name, email, effective role) — search surfaces no extra field.
	roster, err := s.authr.TenantRoster(ctx, mc.Tenant)
	if err != nil {
		return nil, err
	}
	// search must never reveal MORE than the entity's own list endpoint. A
	// workspace-CONFINED caller sees only its workspace's members on /v1/members
	// (handleListMembers applies the same filterRosterToWorkspace); ⌘K search must
	// confine identically, or a WS-A-confined admin could enumerate WS-B users
	// (incl. emails) — reconnaissance-sensitive cross-workspace PII.
	if confinedWS, confined := mc.Principal.ConfinedWorkspaceIn(mc.Tenant); confined {
		roster = filterRosterToWorkspace(roster, confinedWS)
	}
	var out []SearchResult
	for _, m := range roster {
		if !strings.Contains(strings.ToLower(m.User.DisplayName), q) &&
			!strings.Contains(strings.ToLower(m.User.Email), q) {
			continue
		}
		name := m.User.DisplayName
		if name == "" {
			name = m.User.Email
		}
		out = append(out, SearchResult{
			Kind: "user", ID: m.User.ID.String(), Name: name, Detail: m.Role,
		})
	}
	sortSearchResults(out)
	return capSearchResults(out, limit), nil
}

func (s *Server) searchConnectors(ctx context.Context, _ ModuleContext, q string, limit int) ([]SearchResult, error) {
	if s.sourceRoster == nil {
		return nil, nil
	}
	sources, err := s.sourceRoster.ListSources(ctx)
	if err != nil {
		return nil, err
	}
	var out []SearchResult
	for _, src := range sources {
		if !strings.Contains(strings.ToLower(src.Name), q) {
			continue
		}
		detail := src.Kind
		if src.Status != "" {
			detail += " · " + src.Status
		}
		// Name/kind/status only — NEVER src.Config (it carries secret refs).
		out = append(out, SearchResult{Kind: "connector", ID: src.Name, Name: src.Name, Detail: detail})
	}
	sortSearchResults(out)
	return capSearchResults(out, limit), nil
}

func sortSearchResults(rs []SearchResult) {
	sort.Slice(rs, func(i, j int) bool { return rs[i].Name < rs[j].Name })
}

func capSearchResults(rs []SearchResult, limit int) []SearchResult {
	if limit > 0 && len(rs) > limit {
		return rs[:limit]
	}
	return rs
}
