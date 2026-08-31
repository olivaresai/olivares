// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

const (
	accessGuest      = 10
	accessReporter   = 20
	accessDeveloper  = 30
	accessMaintainer = 40
	accessOwner      = 50
)

type apiProject struct {
	ID                int    `json:"id"`
	PathWithNamespace string `json:"path_with_namespace"`
	DefaultBranch     string `json:"default_branch"`
	Visibility        string `json:"visibility"`
}

type apiBranch struct {
	Name string `json:"name"`
}

type member struct {
	Username    string `json:"username"`
	AccessLevel int    `json:"access_level"`
}

// listProjects returns all projects within the configured group, including
// subgroups. It paginates using the X-Next-Page header.
func (s *Source) listProjects(ctx context.Context) ([]apiProject, error) {
	endpoint := fmt.Sprintf("%s/api/v4/groups/%s/projects", s.apiBase, url.PathEscape(s.group))
	var all []apiProject
	page := "1"
	for page != "" {
		u := fmt.Sprintf("%s?include_subgroups=true&per_page=100&page=%s", endpoint, page)
		var batch []apiProject
		nextPage, err := s.apiGet(ctx, u, &batch)
		if err != nil {
			return nil, fmt.Errorf("gitlab: list projects: %w", err)
		}
		all = append(all, batch...)
		page = nextPage
	}
	return all, nil
}

// listBranches returns branch names for a project.
func (s *Source) listBranches(ctx context.Context, projectID int) ([]string, error) {
	endpoint := fmt.Sprintf("%s/api/v4/projects/%d/repository/branches", s.apiBase, projectID)
	var all []string
	page := "1"
	for page != "" {
		u := fmt.Sprintf("%s?per_page=100&page=%s", endpoint, page)
		var batch []apiBranch
		nextPage, err := s.apiGet(ctx, u, &batch)
		if err != nil {
			return nil, fmt.Errorf("gitlab: list branches project %d: %w", projectID, err)
		}
		for _, b := range batch {
			all = append(all, b.Name)
		}
		page = nextPage
	}
	return all, nil
}

// listProjectMembers returns all members of a project (including inherited).
func (s *Source) listProjectMembers(ctx context.Context, projectID int) ([]member, error) {
	endpoint := fmt.Sprintf("%s/api/v4/projects/%d/members/all", s.apiBase, projectID)
	return s.listMembers(ctx, endpoint)
}

// listGroupMembers returns all members of the configured group (including inherited).
func (s *Source) listGroupMembers(ctx context.Context) ([]member, error) {
	endpoint := fmt.Sprintf("%s/api/v4/groups/%s/members/all", s.apiBase, url.PathEscape(s.group))
	return s.listMembers(ctx, endpoint)
}

func (s *Source) listMembers(ctx context.Context, endpoint string) ([]member, error) {
	var all []member
	page := "1"
	for page != "" {
		u := fmt.Sprintf("%s?per_page=100&page=%s", endpoint, page)
		var batch []member
		nextPage, err := s.apiGet(ctx, u, &batch)
		if err != nil {
			return nil, fmt.Errorf("gitlab: list members: %w", err)
		}
		all = append(all, batch...)
		page = nextPage
	}
	return all, nil
}

// apiGet performs an authenticated GET, decodes JSON into dst, and returns
// the X-Next-Page value (empty string when no more pages).
func (s *Source) apiGet(ctx context.Context, rawURL string, dst interface{}) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("PRIVATE-TOKEN", s.token)

	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d from %s", resp.StatusCode, rawURL)
	}

	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}

	nextPage := resp.Header.Get("X-Next-Page")
	if nextPage != "" {
		if _, err := strconv.Atoi(nextPage); err != nil {
			nextPage = ""
		}
	}
	return nextPage, nil
}
