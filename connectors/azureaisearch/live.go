// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package azureaisearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/connectors/internal/content"
	"github.com/olivaresai/olivares/sdk"
)

// Compile-time assertion: *Source always satisfies LiveSource (methods are
// defined unconditionally; they return errors when not in live mode).
var _ contentsource.LiveSource = (*Source)(nil)

// liveClient holds the runtime state for Azure AI Search REST API access.
type liveClient struct {
	http           *http.Client
	endpoint       string
	indexName      string
	apiKey         string
	authScheme     string
	securityField  string
	timestampField string
	contentField   string
	titleField     string
}

// newLiveClient constructs a liveClient from resolved configuration.
// The credential_ref setting is expected to hold the already-resolved API key
// or token (the composition root resolves the secret reference before Open).
func newLiveClient(cfg sdk.Config) (*liveClient, error) {
	endpoint := strings.TrimSpace(cfg.Get("endpoint"))
	if endpoint == "" {
		return nil, errors.New("azureaisearch: endpoint is required for live mode")
	}
	indexName := strings.TrimSpace(cfg.Get("index_name"))
	if indexName == "" {
		return nil, errors.New("azureaisearch: index_name is required for live mode")
	}
	authScheme := strings.TrimSpace(cfg.Get("auth_scheme"))
	if authScheme == "" {
		authScheme = "api_key"
	}
	contentField := strings.TrimSpace(cfg.Get("content_field"))
	if contentField == "" {
		contentField = "content"
	}
	titleField := strings.TrimSpace(cfg.Get("title_field"))
	if titleField == "" {
		titleField = "title"
	}
	return &liveClient{
		http:           &http.Client{},
		endpoint:       strings.TrimRight(endpoint, "/"),
		indexName:      indexName,
		apiKey:         strings.TrimSpace(cfg.Get("credential_ref")),
		authScheme:     authScheme,
		securityField:  strings.TrimSpace(cfg.Get("security_field")),
		timestampField: strings.TrimSpace(cfg.Get("timestamp_field")),
		contentField:   contentField,
		titleField:     titleField,
	}, nil
}

// apiVersion is the Azure AI Search REST API version used by this connector.
const apiVersion = "2024-07-01"

// setAuth sets the authentication header on the request according to the
// configured auth scheme.
func (lc *liveClient) setAuth(req *http.Request) {
	switch lc.authScheme {
	case "managed_identity":
		if lc.apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+lc.apiKey)
		}
	default: // api_key
		if lc.apiKey != "" {
			req.Header.Set("api-key", lc.apiKey)
		}
	}
}

// selectFields returns the comma-separated $select value for search queries.
func (lc *liveClient) selectFields() string {
	fields := []string{"id", lc.titleField, lc.contentField}
	if lc.timestampField != "" {
		fields = append(fields, lc.timestampField)
	}
	if lc.securityField != "" {
		fields = append(fields, lc.securityField)
	}
	return strings.Join(fields, ",")
}

// DeltaList queries the Azure AI Search index for documents modified since
// sinceToken. sinceToken is an RFC3339 timestamp; when empty the full index is
// queried. Filtering uses the configured timestamp_field in an OData filter.
//
// Timestamps never expire — Expired is always false. All returned entries carry
// ChangeContent: the search API does not distinguish ACL-only changes from
// content changes.
//
// ┌─────────────────────────────────────────────────────────────────────────────┐
// │ KNOWN LIMITATION — AZURE AI SEARCH CANNOT REPORT DOCUMENT DELETIONS        │
// │                                                                             │
// │ Search/list responses only surface documents that currently exist. Deleted │
// │ documents are detected by full-list reconciliation: comparing List's live   │
// │ document set with the indexed set and deleting disappeared IDs.             │
// └─────────────────────────────────────────────────────────────────────────────┘
//
// ResumeToken is the RFC3339 timestamp of the most recently modified changed
// document in the response. NextToken is reserved for provider pagination.
func (s *Source) DeltaList(ctx context.Context, sinceToken string) (contentsource.DeltaPage, error) {
	if s.live == nil {
		return contentsource.DeltaPage{}, errors.New("azureaisearch: DeltaList requires live mode")
	}

	reqURL := fmt.Sprintf(
		"%s/indexes/%s/docs/search?api-version=%s",
		s.live.endpoint, s.live.indexName, apiVersion,
	)

	// Build the search request body.
	body := map[string]any{
		"search":  "*",
		"$top":    1000,
		"$select": s.live.selectFields(),
	}
	if s.live.timestampField != "" {
		body["$orderby"] = s.live.timestampField + " asc"
		if sinceToken != "" {
			body["filter"] = fmt.Sprintf("%s gt %s", s.live.timestampField, sinceToken)
		}
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return contentsource.DeltaPage{}, fmt.Errorf("azureaisearch: marshal search body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return contentsource.DeltaPage{}, fmt.Errorf("azureaisearch: build delta request: %w", err)
	}
	s.live.setAuth(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := s.live.http.Do(req)
	if err != nil {
		return contentsource.DeltaPage{}, fmt.Errorf("azureaisearch: delta request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return contentsource.DeltaPage{}, fmt.Errorf("azureaisearch: delta returned HTTP %d", resp.StatusCode)
	}

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, content.MaxBodyBytes*16))
	if err != nil {
		return contentsource.DeltaPage{}, fmt.Errorf("azureaisearch: read delta body: %w", err)
	}

	var sr searchResponse
	if err := json.Unmarshal(respBody, &sr); err != nil {
		return contentsource.DeltaPage{}, fmt.Errorf("azureaisearch: parse delta response: %w", err)
	}

	// Timestamps never expire — Expired is always false.
	page := contentsource.DeltaPage{}
	var latestModifiedAt time.Time
	for _, raw := range sr.Value {
		docID := extractDocID(raw)
		if docID == "" {
			continue
		}
		title := stringField(raw, s.live.titleField)
		modifiedAt := parseTimestampField(raw, s.live.timestampField)

		if modifiedAt.After(latestModifiedAt) {
			latestModifiedAt = modifiedAt
		}

		page.Changes = append(page.Changes, contentsource.DeltaEntry{
			DocRef: contentsource.DocRef{
				DocID:       content.Truncate(docID, content.MaxRefLen),
				Title:       content.Truncate(title, content.MaxTitleLen),
				ContentType: "text/plain",
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
		return nil, "", errors.New("azureaisearch: List requires live mode")
	}

	reqURL := strings.TrimSpace(cursor)
	if reqURL == "" {
		values := url.Values{}
		values.Set("api-version", apiVersion)
		values.Set("$select", s.live.selectFields())
		values.Set("$top", "1000")
		reqURL = fmt.Sprintf("%s/indexes/%s/docs?%s", s.live.endpoint, s.live.indexName, values.Encode())
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("azureaisearch: build list request: %w", err)
	}
	s.live.setAuth(req)
	req.Header.Set("Accept", "application/json")

	resp, err := s.live.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("azureaisearch: list request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("azureaisearch: list returned HTTP %d", resp.StatusCode)
	}
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, content.MaxBodyBytes*16))
	if err != nil {
		return nil, "", fmt.Errorf("azureaisearch: read list body: %w", err)
	}
	var sr searchResponse
	if err := json.Unmarshal(respBody, &sr); err != nil {
		return nil, "", fmt.Errorf("azureaisearch: parse list response: %w", err)
	}
	refs := make([]contentsource.DocRef, 0, len(sr.Value))
	for _, raw := range sr.Value {
		docID := extractDocID(raw)
		if docID == "" {
			continue
		}
		refs = append(refs, contentsource.DocRef{
			DocID:       content.Truncate(docID, content.MaxRefLen),
			Title:       content.Truncate(stringField(raw, s.live.titleField), content.MaxTitleLen),
			ContentType: "application/json",
			ModifiedAt:  parseTimestampField(raw, s.live.timestampField),
		})
	}
	return refs, strings.TrimSpace(sr.NextLink), nil
}

func (s *Source) fetchLive(ctx context.Context, docID string) (contentsource.Document, error) {
	if s.live == nil {
		return contentsource.Document{}, errors.New("azureaisearch: Fetch requires live mode")
	}
	values := url.Values{}
	values.Set("api-version", apiVersion)
	values.Set("$select", s.live.selectFields())
	reqURL := fmt.Sprintf(
		"%s/indexes/%s/docs/%s?%s",
		s.live.endpoint, s.live.indexName, url.PathEscape(docID), values.Encode(),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return contentsource.Document{}, fmt.Errorf("azureaisearch: build fetch request: %w", err)
	}
	s.live.setAuth(req)
	req.Header.Set("Accept", "application/json")

	resp, err := s.live.http.Do(req)
	if err != nil {
		return contentsource.Document{}, fmt.Errorf("azureaisearch: fetch request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return contentsource.Document{}, fmt.Errorf("azureaisearch: fetch returned HTTP %d", resp.StatusCode)
	}
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, content.MaxBodyBytes))
	if err != nil {
		return contentsource.Document{}, fmt.Errorf("azureaisearch: read fetch body: %w", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(respBody, &raw); err != nil {
		return contentsource.Document{}, fmt.Errorf("azureaisearch: parse fetch response: %w", err)
	}
	return contentsource.Document{
		Source:      contentsource.SourceAzureAISearch,
		DocID:       content.Truncate(docID, content.MaxRefLen),
		Title:       content.Truncate(stringField(raw, s.live.titleField), content.MaxTitleLen),
		Body:        string(respBody),
		ContentType: "application/json",
		ACL:         content.CleanACL(extractACL(raw, s.live.securityField)),
		SpaceRef:    "index:" + s.live.indexName,
		ModifiedAt:  parseTimestampField(raw, s.live.timestampField),
	}, nil
}

// FetchACL fetches the security field for a single document from the index and
// returns its ACL entries as "principal:<value>".
//
// If no security_field is configured, an empty ACLResult is returned.
func (s *Source) FetchACL(ctx context.Context, docID string) (contentsource.ACLResult, error) {
	if s.live == nil {
		return contentsource.ACLResult{}, errors.New("azureaisearch: FetchACL requires live mode")
	}

	// If no security field is configured, there are no ACL entries to fetch.
	if s.live.securityField == "" {
		return contentsource.ACLResult{}, nil
	}

	reqURL := fmt.Sprintf(
		"%s/indexes/%s/docs/%s?api-version=%s&$select=%s",
		s.live.endpoint, s.live.indexName, docID, apiVersion, s.live.securityField,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return contentsource.ACLResult{}, fmt.Errorf("azureaisearch: build acl request: %w", err)
	}
	s.live.setAuth(req)
	req.Header.Set("Accept", "application/json")

	resp, err := s.live.http.Do(req)
	if err != nil {
		return contentsource.ACLResult{}, fmt.Errorf("azureaisearch: acl request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return contentsource.ACLResult{}, fmt.Errorf("azureaisearch: acl returned HTTP %d", resp.StatusCode)
	}

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, content.MaxBodyBytes))
	if err != nil {
		return contentsource.ACLResult{}, fmt.Errorf("azureaisearch: read acl body: %w", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(respBody, &raw); err != nil {
		return contentsource.ACLResult{}, fmt.Errorf("azureaisearch: parse acl response: %w", err)
	}

	acl := extractACL(raw, s.live.securityField)
	return contentsource.ACLResult{
		ACL: content.CleanACL(acl),
	}, nil
}
