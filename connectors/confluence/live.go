// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package confluence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/connectors/internal/content"
	"github.com/olivaresai/olivares/sdk"
)

// Compile-time assertion: *Source always satisfies LiveSource (methods are
// defined unconditionally; they return errors when not in live mode).
var _ contentsource.LiveSource = (*Source)(nil)

// liveClient holds the runtime state for Confluence Cloud REST API v2 access.
type liveClient struct {
	http     *http.Client
	spaceKey string
	baseURL  string
	token    string
}

// newLiveClient constructs a liveClient from resolved configuration.
// The credential_ref setting is expected to hold the already-resolved bearer
// token (the composition root resolves the secret reference before Open).
func newLiveClient(cfg sdk.Config) (*liveClient, error) {
	spaceKey := strings.TrimSpace(cfg.Get("space_key"))
	if spaceKey == "" {
		return nil, errors.New("confluence: space_key is required for live mode")
	}
	baseURL := strings.TrimSpace(cfg.Get("base_url"))
	if baseURL == "" {
		return nil, errors.New("confluence: base_url is required for live mode")
	}
	token := strings.TrimSpace(cfg.Get("credential_ref"))
	return &liveClient{
		http:     &http.Client{},
		spaceKey: spaceKey,
		baseURL:  strings.TrimRight(baseURL, "/"),
		token:    token,
	}, nil
}

// --- Confluence Cloud v2 API response shapes ---------------------------------

type confluenceV2PagesResponse struct {
	Results []confluenceV2Page `json:"results"`
	Links   struct {
		Next string `json:"next"`
	} `json:"_links"`
}

type confluenceV2Page struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	SpaceID string `json:"spaceId"`
	Version struct {
		CreatedAt string `json:"createdAt"`
	} `json:"version"`
}

type confluenceV2PermissionsResponse struct {
	Results []confluenceV2Permission `json:"results"`
}

type confluenceV2Permission struct {
	Principal struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"principal"`
	Operation struct {
		Key        string `json:"key"`
		TargetType string `json:"targetType"`
	} `json:"operation"`
}

// DeltaList calls the Confluence Cloud v2 pages endpoint and returns pages
// modified since sinceToken. sinceToken is an ISO 8601 timestamp (the last
// sync high-water mark); filtering is performed client-side because the
// Confluence v2 pages API has no native "modified since" query parameter.
//
// ┌─────────────────────────────────────────────────────────────────────────────┐
// │ KNOWN LIMITATION — CONFLUENCE API CANNOT REPORT PAGE DELETIONS             │
// │                                                                             │
// │ The Confluence pages list API only surfaces pages that currently exist.     │
// │ Deleted pages can only be detected via full-list reconciliation: comparing  │
// │ List's current live page set against the previously indexed set and issuing │
// │ deletes for IDs that disappeared. The sync handler performs this orphan     │
// │ detection separately, outside of DeltaList, during full sync passes.        │
// └─────────────────────────────────────────────────────────────────────────────┘
//
// Timestamps never expire, so Expired is always false. All returned entries
// carry ChangeContent: the pages API does not distinguish ACL-only changes
// from content changes.
//
// ResumeToken is the RFC3339 timestamp of the most recently modified changed
// page in the response. NextToken is reserved for provider pagination.
func (s *Source) DeltaList(ctx context.Context, sinceToken string) (contentsource.DeltaPage, error) {
	if s.live == nil {
		return contentsource.DeltaPage{}, errors.New("confluence: DeltaList requires live mode")
	}

	// Parse sinceToken as a reference timestamp (zero value = include all pages).
	var sinceTime time.Time
	if sinceToken != "" {
		if t, err := time.Parse(time.RFC3339, sinceToken); err == nil {
			sinceTime = t.UTC()
		}
		// On parse failure fall through with zero time (no filtering) so a bad
		// token triggers a full re-sync rather than a hard error.
	}

	reqURL := fmt.Sprintf(
		"%s/wiki/api/v2/spaces/%s/pages?sort=modified-date&limit=100",
		s.live.baseURL, s.live.spaceKey,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return contentsource.DeltaPage{}, fmt.Errorf("confluence: build delta request: %w", err)
	}
	if s.live.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.live.token)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := s.live.http.Do(req)
	if err != nil {
		return contentsource.DeltaPage{}, fmt.Errorf("confluence: delta request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return contentsource.DeltaPage{}, fmt.Errorf("confluence: delta returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, content.MaxBodyBytes*16))
	if err != nil {
		return contentsource.DeltaPage{}, fmt.Errorf("confluence: read delta body: %w", err)
	}

	var pr confluenceV2PagesResponse
	if err := json.Unmarshal(body, &pr); err != nil {
		return contentsource.DeltaPage{}, fmt.Errorf("confluence: parse delta response: %w", err)
	}

	// Timestamps never expire — Expired is always false.
	page := contentsource.DeltaPage{}
	var latestModifiedAt time.Time
	for _, p := range pr.Results {
		if strings.TrimSpace(p.ID) == "" {
			continue
		}
		modifiedAt := parseTime(p.Version.CreatedAt)
		// Client-side filtering: skip pages not modified after sinceToken.
		if !sinceTime.IsZero() && !modifiedAt.After(sinceTime) {
			continue
		}
		if modifiedAt.After(latestModifiedAt) {
			latestModifiedAt = modifiedAt
		}
		page.Changes = append(page.Changes, contentsource.DeltaEntry{
			DocRef: contentsource.DocRef{
				DocID:       content.Truncate(p.ID, content.MaxRefLen),
				Title:       content.Truncate(p.Title, content.MaxTitleLen),
				ContentType: "text/html",
				ModifiedAt:  modifiedAt,
			},
			ChangeKind: contentsource.ChangeContent,
		})
	}

	if !latestModifiedAt.IsZero() {
		page.ResumeToken = latestModifiedAt.UTC().Format(time.RFC3339)
	}

	return page, nil
}

func (s *Source) listLive(ctx context.Context, cursor string) ([]contentsource.DocRef, string, error) {
	if s.live == nil {
		return nil, "", errors.New("confluence: List requires live mode")
	}
	reqURL := strings.TrimSpace(cursor)
	if reqURL == "" {
		reqURL = fmt.Sprintf(
			"%s/wiki/api/v2/spaces/%s/pages?sort=modified-date&limit=100",
			s.live.baseURL, s.live.spaceKey,
		)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("confluence: build list request: %w", err)
	}
	if s.live.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.live.token)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := s.live.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("confluence: list request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("confluence: list returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, content.MaxBodyBytes*16))
	if err != nil {
		return nil, "", fmt.Errorf("confluence: read list body: %w", err)
	}
	var pr confluenceV2PagesResponse
	if err := json.Unmarshal(body, &pr); err != nil {
		return nil, "", fmt.Errorf("confluence: parse list response: %w", err)
	}
	refs := make([]contentsource.DocRef, 0, len(pr.Results))
	for _, p := range pr.Results {
		if strings.TrimSpace(p.ID) == "" {
			continue
		}
		refs = append(refs, contentsource.DocRef{
			DocID:       content.Truncate(p.ID, content.MaxRefLen),
			Title:       content.Truncate(p.Title, content.MaxTitleLen),
			ContentType: "text/html",
			ModifiedAt:  parseTime(p.Version.CreatedAt),
		})
	}
	return refs, s.absoluteURL(pr.Links.Next), nil
}

func (s *Source) fetchLive(ctx context.Context, docID string) (contentsource.Document, error) {
	if s.live == nil {
		return contentsource.Document{}, errors.New("confluence: Fetch requires live mode")
	}
	reqURL := fmt.Sprintf(
		"%s/wiki/rest/api/content/%s?expand=body.storage,version,space",
		s.live.baseURL, docID,
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return contentsource.Document{}, fmt.Errorf("confluence: build fetch request: %w", err)
	}
	if s.live.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.live.token)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := s.live.http.Do(req)
	if err != nil {
		return contentsource.Document{}, fmt.Errorf("confluence: fetch request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return contentsource.Document{}, fmt.Errorf("confluence: fetch returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, content.MaxBodyBytes))
	if err != nil {
		return contentsource.Document{}, fmt.Errorf("confluence: read fetch body: %w", err)
	}
	var p confluencePage
	if err := json.Unmarshal(body, &p); err != nil {
		return contentsource.Document{}, fmt.Errorf("confluence: parse fetch response: %w", err)
	}
	return contentsource.Document{
		Source:      contentsource.SourceConfluence,
		DocID:       content.Truncate(docID, content.MaxRefLen),
		Title:       content.Truncate(p.Title, content.MaxTitleLen),
		Body:        content.Truncate(p.Body.Storage.Value, content.MaxBodyBytes),
		ContentType: "text/html",
		SpaceRef:    "space:" + p.Space.Key,
		ModifiedAt:  parseTime(p.Version.When),
	}, nil
}

func (s *Source) absoluteURL(next string) string {
	next = strings.TrimSpace(next)
	if next == "" {
		return ""
	}
	if strings.HasPrefix(next, "http://") || strings.HasPrefix(next, "https://") {
		return next
	}
	if strings.HasPrefix(next, "/") {
		return s.live.baseURL + next
	}
	return s.live.baseURL + "/" + next
}

// FetchACL fetches the space-level permissions for the page identified by
// docID. It makes two API calls: first to GET the page (to resolve its spaceId),
// then to GET the space permissions.
//
// Confluence has no native sensitivity labels on pages; ExternalLabels and
// Classification are always empty in the returned ACLResult.
func (s *Source) FetchACL(ctx context.Context, docID string) (contentsource.ACLResult, error) {
	if s.live == nil {
		return contentsource.ACLResult{}, errors.New("confluence: FetchACL requires live mode")
	}

	// Step 1: fetch the page to resolve its spaceId.
	pageURL := fmt.Sprintf("%s/wiki/api/v2/pages/%s", s.live.baseURL, docID)
	pageReq, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return contentsource.ACLResult{}, fmt.Errorf("confluence: build page request: %w", err)
	}
	if s.live.token != "" {
		pageReq.Header.Set("Authorization", "Bearer "+s.live.token)
	}
	pageReq.Header.Set("Accept", "application/json")

	pageResp, err := s.live.http.Do(pageReq)
	if err != nil {
		return contentsource.ACLResult{}, fmt.Errorf("confluence: page request: %w", err)
	}
	defer func() { _ = pageResp.Body.Close() }()

	if pageResp.StatusCode != http.StatusOK {
		return contentsource.ACLResult{}, fmt.Errorf("confluence: page returned HTTP %d", pageResp.StatusCode)
	}

	pageBody, err := io.ReadAll(io.LimitReader(pageResp.Body, content.MaxBodyBytes))
	if err != nil {
		return contentsource.ACLResult{}, fmt.Errorf("confluence: read page body: %w", err)
	}

	var p confluenceV2Page
	if err := json.Unmarshal(pageBody, &p); err != nil {
		return contentsource.ACLResult{}, fmt.Errorf("confluence: parse page response: %w", err)
	}
	if strings.TrimSpace(p.SpaceID) == "" {
		return contentsource.ACLResult{}, fmt.Errorf("confluence: page %s has no spaceId", docID)
	}

	// Step 2: fetch space-level permissions using the resolved spaceId.
	permsURL := fmt.Sprintf("%s/wiki/api/v2/spaces/%s/permissions", s.live.baseURL, p.SpaceID)
	permsReq, err := http.NewRequestWithContext(ctx, http.MethodGet, permsURL, nil)
	if err != nil {
		return contentsource.ACLResult{}, fmt.Errorf("confluence: build permissions request: %w", err)
	}
	if s.live.token != "" {
		permsReq.Header.Set("Authorization", "Bearer "+s.live.token)
	}
	permsReq.Header.Set("Accept", "application/json")

	permsResp, err := s.live.http.Do(permsReq)
	if err != nil {
		return contentsource.ACLResult{}, fmt.Errorf("confluence: permissions request: %w", err)
	}
	defer func() { _ = permsResp.Body.Close() }()

	if permsResp.StatusCode != http.StatusOK {
		return contentsource.ACLResult{}, fmt.Errorf("confluence: permissions returned HTTP %d", permsResp.StatusCode)
	}

	permsBody, err := io.ReadAll(io.LimitReader(permsResp.Body, content.MaxBodyBytes))
	if err != nil {
		return contentsource.ACLResult{}, fmt.Errorf("confluence: read permissions body: %w", err)
	}

	var permsResult confluenceV2PermissionsResponse
	if err := json.Unmarshal(permsBody, &permsResult); err != nil {
		return contentsource.ACLResult{}, fmt.Errorf("confluence: parse permissions response: %w", err)
	}

	acl := make([]string, 0, len(permsResult.Results))
	for _, perm := range permsResult.Results {
		// Only include group principals with read permission for space-level ACL.
		if strings.ToLower(perm.Principal.Type) != "group" {
			continue
		}
		if strings.ToLower(perm.Operation.Key) != "read" {
			continue
		}
		if name := strings.TrimSpace(perm.Principal.Name); name != "" {
			acl = append(acl, "group:"+name)
		} else if id := strings.TrimSpace(perm.Principal.ID); id != "" {
			acl = append(acl, "group:"+id)
		}
	}

	// Confluence does not expose native sensitivity labels on pages.
	// ExternalLabels and Classification are always empty.
	return contentsource.ACLResult{
		ACL: content.CleanACL(acl),
	}, nil
}
