// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// apiRepo is a repository returned by the GitHub REST API.
type apiRepo struct {
	FullName      string `json:"full_name"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"default_branch"`
}

// collaborator is a user with permissions on a repository.
type collaborator struct {
	Login       string        `json:"login"`
	Permissions permissionSet `json:"permissions"`
}

// permissionSet is the permission bits on a collaborator or team repo.
type permissionSet struct {
	Admin bool `json:"admin"`
	Push  bool `json:"push"`
	Pull  bool `json:"pull"`
}

// team is an organization team.
type team struct {
	Slug       string `json:"slug"`
	Name       string `json:"name"`
	Permission string `json:"permission"`
}

// teamRepo is a repository as seen through a team's access.
type teamRepo struct {
	FullName    string        `json:"full_name"`
	Permissions permissionSet `json:"permissions"`
}

// listRepos returns all repositories in the configured organization,
// following pagination via the Link header.
func (s *Source) listRepos(ctx context.Context) ([]apiRepo, error) {
	var all []apiRepo
	url := fmt.Sprintf("%s/orgs/%s/repos?per_page=100", s.apiBase, s.org)
	for url != "" {
		var page []apiRepo
		next, err := s.apiGet(ctx, url, &page)
		if err != nil {
			return nil, fmt.Errorf("list repos: %w", err)
		}
		all = append(all, page...)
		url = next
	}
	return all, nil
}

// listBranches returns the branch names for a repository.
func (s *Source) listBranches(ctx context.Context, repoFullName string) ([]string, error) {
	type branchItem struct {
		Name string `json:"name"`
	}
	var all []string
	url := fmt.Sprintf("%s/repos/%s/branches?per_page=100", s.apiBase, repoFullName)
	for url != "" {
		var page []branchItem
		next, err := s.apiGet(ctx, url, &page)
		if err != nil {
			return nil, fmt.Errorf("list branches %s: %w", repoFullName, err)
		}
		for _, b := range page {
			all = append(all, b.Name)
		}
		url = next
	}
	return all, nil
}

// listCollaborators returns the collaborators and their permissions for a
// repository.
func (s *Source) listCollaborators(ctx context.Context, repoFullName string) ([]collaborator, error) {
	var all []collaborator
	url := fmt.Sprintf("%s/repos/%s/collaborators?per_page=100", s.apiBase, repoFullName)
	for url != "" {
		var page []collaborator
		next, err := s.apiGet(ctx, url, &page)
		if err != nil {
			return nil, fmt.Errorf("list collaborators %s: %w", repoFullName, err)
		}
		all = append(all, page...)
		url = next
	}
	return all, nil
}

// listTeams returns the teams in the configured organization.
func (s *Source) listTeams(ctx context.Context) ([]team, error) {
	var all []team
	url := fmt.Sprintf("%s/orgs/%s/teams?per_page=100", s.apiBase, s.org)
	for url != "" {
		var page []team
		next, err := s.apiGet(ctx, url, &page)
		if err != nil {
			return nil, fmt.Errorf("list teams: %w", err)
		}
		all = append(all, page...)
		url = next
	}
	return all, nil
}

// listTeamRepos returns the repositories a team has access to.
func (s *Source) listTeamRepos(ctx context.Context, teamSlug string) ([]teamRepo, error) {
	var all []teamRepo
	url := fmt.Sprintf("%s/orgs/%s/teams/%s/repos?per_page=100", s.apiBase, s.org, teamSlug)
	for url != "" {
		var page []teamRepo
		next, err := s.apiGet(ctx, url, &page)
		if err != nil {
			return nil, fmt.Errorf("list team repos %s: %w", teamSlug, err)
		}
		all = append(all, page...)
		url = next
	}
	return all, nil
}

// apiGet performs an authenticated GET, decoding the JSON response into dst.
// It returns the URL of the next page (from the Link header) or "" if none.
func (s *Source) apiGet(ctx context.Context, url string, dst interface{}) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if remaining := resp.Header.Get("X-RateLimit-Remaining"); remaining == "0" {
		resetStr := resp.Header.Get("X-RateLimit-Reset")
		if resetEpoch, err := strconv.ParseInt(resetStr, 10, 64); err == nil {
			wait := time.Until(time.Unix(resetEpoch, 0))
			if wait > 0 && wait < 15*time.Minute {
				time.Sleep(wait + time.Second)
			}
		}
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("GitHub API %s: %d %s", url, resp.StatusCode, string(body))
	}

	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}

	return parseLinkNext(resp.Header.Get("Link")), nil
}

// linkNextRe extracts the URL for rel="next" from a GitHub Link header.
var linkNextRe = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// parseLinkNext extracts the next-page URL from a Link header value, or ""
// if there is no next page.
func parseLinkNext(link string) string {
	if link == "" {
		return ""
	}
	for _, part := range strings.Split(link, ",") {
		m := linkNextRe.FindStringSubmatch(strings.TrimSpace(part))
		if len(m) == 2 {
			return m[1]
		}
	}
	return ""
}
