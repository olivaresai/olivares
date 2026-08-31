// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gcpaudit

import (
	"context"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// crmFolder is the subset of a Resource Manager v3 Folder we read: its resource
// name ("folders/123"), parent ("organizations/456" or "folders/789") and state.
// displayName is intentionally NOT read — it can carry free-form text and is not
// needed for topology (the resource name is the stable ref).
type crmFolder struct {
	Name   string `json:"name"`
	Parent string `json:"parent"`
	State  string `json:"state"`
}

// crmProject is the subset of a Resource Manager v3 Project we read: its project
// id (the natural ref, which converges with audit principal/resource refs),
// parent and state. The project NUMBER and labels are not read.
type crmProject struct {
	ProjectID string `json:"projectId"`
	Parent    string `json:"parent"`
	State     string `json:"state"`
}

// iamServiceAccount is the subset of an IAM ServiceAccount we read: its email
// (the ref, which converges with the audit principalEmail and google-agent's SA
// ref) and disabled flag. The private-key material, OAuth2 client id and the
// uniqueId are not needed for topology and are not read.
type iamServiceAccount struct {
	Email    string `json:"email"`
	Disabled bool   `json:"disabled"`
}

// gatherInventory walks the Resource Manager hierarchy (org → folders → projects)
// and lists each project's service accounts, emitting topology edges in a
// deterministic (sorted) order. It returns the first error so the caller records
// a single health finding. ctx is honored between operations and pages.
//
// With no org configured (project-scoped operator) the hierarchy walk is skipped
// and only the explicit projects' service accounts are inventoried.
func (s *Source) gatherInventory(ctx context.Context, sink sdk.Sink, at time.Time) error {
	edges, projects, truncated, err := s.collectHierarchy(ctx, at)
	if err != nil {
		return err
	}

	// Combine discovered projects with the explicitly configured ones (dedup).
	projectSet := map[string]struct{}{}
	for _, p := range projects {
		projectSet[p] = struct{}{}
	}
	for _, p := range s.cfg.projects {
		projectSet[p] = struct{}{}
	}

	if s.cfg.enableServiceAccounts {
		ordered := make([]string, 0, len(projectSet))
		for p := range projectSet {
			ordered = append(ordered, p)
		}
		sort.Strings(ordered)
		for _, p := range ordered {
			if err := ctx.Err(); err != nil {
				return err
			}
			sas, saTrunc, err := s.listServiceAccounts(ctx, p)
			if err != nil {
				return err
			}
			truncated = truncated || saTrunc
			for _, sa := range sas {
				edges = append(edges, inventoryEdge(originProject, p, resServiceAccount, sa.Email, at))
			}
		}
	}

	sortEdges(edges)
	for _, e := range edges {
		if err := emit(ctx, sink, e); err != nil {
			return err
		}
	}
	// A list that stopped at max_pages left resources undiscovered: signal the
	// partial coverage honestly (never a silent cap), after emitting what we have.
	if truncated {
		if err := emit(ctx, sink, coverageFinding(subjectInventory, s.cfg.scopeRef(),
			"GCP inventory partial: a list stopped at max_pages — raise max_pages for full coverage", at)); err != nil {
			return err
		}
	}
	return nil
}

// collectHierarchy walks org → folders → projects (bounded by maxFolderDepth) and
// returns the topology edges plus the set of discovered project ids. When no org
// is configured it returns no hierarchy edges (the project-scoped path).
func (s *Source) collectHierarchy(ctx context.Context, at time.Time) ([]model.EdgeObservation, []string, bool, error) {
	var edges []model.EdgeObservation
	var projects []string
	truncated := false
	if s.cfg.orgID == "" {
		return edges, projects, truncated, nil
	}

	// BFS over the folder tree, starting at the organization. Each level lists its
	// child folders and projects. depth caps recursion defensively.
	type node struct {
		parent string // "organizations/123" or "folders/456"
		kind   string // originOrganization or originFolder
		depth  int
	}
	queue := []node{{parent: "organizations/" + s.cfg.orgID, kind: originOrganization, depth: 0}}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		if err := ctx.Err(); err != nil {
			return nil, nil, false, err
		}

		folders, fTrunc, err := s.listFolders(ctx, n.parent)
		if err != nil {
			return nil, nil, false, err
		}
		truncated = truncated || fTrunc
		for _, f := range folders {
			edges = append(edges, inventoryEdge(n.kind, n.parent, resFolder, f.Name, at))
			if n.depth+1 < maxFolderDepth {
				queue = append(queue, node{parent: f.Name, kind: originFolder, depth: n.depth + 1})
			}
		}

		projs, pTrunc, err := s.listProjects(ctx, n.parent)
		if err != nil {
			return nil, nil, false, err
		}
		truncated = truncated || pTrunc
		for _, p := range projs {
			edges = append(edges, inventoryEdge(n.kind, n.parent, resProject, p.ProjectID, at))
			projects = append(projects, p.ProjectID)
		}
	}
	return edges, projects, truncated, nil
}

// listFolders lists the active child folders of parent, following pageToken
// pagination up to maxPages. DELETE_REQUESTED / deleted folders are skipped. The
// truncated return reports that the page budget was exhausted with a next page
// still pending (the caller surfaces it as a coverage finding).
func (s *Source) listFolders(ctx context.Context, parent string) (out []crmFolder, truncated bool, err error) {
	token := ""
	for page := 0; page < s.cfg.maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		q := url.Values{"parent": {parent}, "pageSize": {"500"}}
		if token != "" {
			q.Set("pageToken", token)
		}
		var resp struct {
			Folders       []crmFolder `json:"folders"`
			NextPageToken string      `json:"nextPageToken"`
		}
		if err := s.getJSON(ctx, s.cfg.crmEndpoint+"/v3/folders?"+q.Encode(), &resp); err != nil {
			return nil, false, err
		}
		for _, f := range resp.Folders {
			if isActive(f.State) {
				out = append(out, f)
			}
		}
		if resp.NextPageToken == "" {
			break
		}
		token = resp.NextPageToken
		if page == s.cfg.maxPages-1 {
			truncated = true // more folders remain beyond the page budget.
		}
	}
	return out, truncated, nil
}

// listProjects lists the active child projects of parent, following pageToken
// pagination up to maxPages. Non-active projects are skipped.
func (s *Source) listProjects(ctx context.Context, parent string) (out []crmProject, truncated bool, err error) {
	token := ""
	for page := 0; page < s.cfg.maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		q := url.Values{"parent": {parent}, "pageSize": {"500"}}
		if token != "" {
			q.Set("pageToken", token)
		}
		var resp struct {
			Projects      []crmProject `json:"projects"`
			NextPageToken string       `json:"nextPageToken"`
		}
		if err := s.getJSON(ctx, s.cfg.crmEndpoint+"/v3/projects?"+q.Encode(), &resp); err != nil {
			return nil, false, err
		}
		for _, p := range resp.Projects {
			if p.ProjectID != "" && isActive(p.State) {
				out = append(out, p)
			}
		}
		if resp.NextPageToken == "" {
			break
		}
		token = resp.NextPageToken
		if page == s.cfg.maxPages-1 {
			truncated = true
		}
	}
	return out, truncated, nil
}

// listServiceAccounts lists a project's service accounts, following pageToken
// pagination up to maxPages. It reads only the email and disabled flag — never
// any key material.
func (s *Source) listServiceAccounts(ctx context.Context, project string) (out []iamServiceAccount, truncated bool, err error) {
	token := ""
	for page := 0; page < s.cfg.maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		q := url.Values{"pageSize": {"100"}}
		if token != "" {
			q.Set("pageToken", token)
		}
		path := s.cfg.iamEndpoint + "/v1/projects/" + url.PathEscape(project) + "/serviceAccounts?" + q.Encode()
		var resp struct {
			Accounts      []iamServiceAccount `json:"accounts"`
			NextPageToken string              `json:"nextPageToken"`
		}
		if err := s.getJSON(ctx, path, &resp); err != nil {
			return nil, false, err
		}
		for _, a := range resp.Accounts {
			if a.Email != "" {
				out = append(out, a)
			}
		}
		if resp.NextPageToken == "" {
			break
		}
		token = resp.NextPageToken
		if page == s.cfg.maxPages-1 {
			truncated = true
		}
	}
	return out, truncated, nil
}

// isActive reports whether a Resource Manager state denotes a live resource. An
// empty state is treated as active (some list responses omit it).
func isActive(state string) bool {
	switch strings.ToUpper(strings.TrimSpace(state)) {
	case "", "ACTIVE":
		return true
	default:
		return false
	}
}

// sortEdges orders edges by resource kind, resource ref, then origin ref for a
// deterministic emit order, so golden tests are stable regardless of API page
// ordering or map iteration.
func sortEdges(edges []model.EdgeObservation) {
	sort.SliceStable(edges, func(i, j int) bool {
		if edges[i].ResourceKind != edges[j].ResourceKind {
			return edges[i].ResourceKind < edges[j].ResourceKind
		}
		if edges[i].ResourceRef != edges[j].ResourceRef {
			return edges[i].ResourceRef < edges[j].ResourceRef
		}
		return edges[i].OriginRef < edges[j].OriginRef
	})
}
