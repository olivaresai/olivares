// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package notion

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

// liveClient holds the runtime state for Notion API v1 access.
type liveClient struct {
	http    *http.Client
	baseURL string
	token   string
}

// notionAPIVersion is the stable Notion API version header sent on every request.
// See https://developers.notion.com/reference/versioning
const notionAPIVersion = "2022-06-28"

// newLiveClient constructs a liveClient from resolved configuration.
// The credential_ref setting is expected to hold the already-resolved bearer
// token (the composition root resolves the secret reference before Open).
// base_url defaults to "https://api.notion.com/v1" when not set; this allows
// tests to point at an httptest.Server.
func newLiveClient(cfg sdk.Config) (*liveClient, error) {
	baseURL := strings.TrimSpace(cfg.Get("base_url"))
	if baseURL == "" {
		baseURL = "https://api.notion.com/v1"
	}
	token := strings.TrimSpace(cfg.Get("credential_ref"))
	return &liveClient{
		http:    &http.Client{},
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
	}, nil
}

// --- Notion API response shapes -----------------------------------------------

type notionSearchResponse struct {
	Results    []notionSearchPage `json:"results"`
	NextCursor *string            `json:"next_cursor"`
	HasMore    bool               `json:"has_more"`
}

type notionSearchPage struct {
	Object         string `json:"object"`
	ID             string `json:"id"`
	LastEditedTime string `json:"last_edited_time"`
	Properties     struct {
		Title struct {
			Title []struct {
				PlainText string `json:"plain_text"`
			} `json:"title"`
		} `json:"title"`
	} `json:"properties"`
}

type notionBlocksResponse struct {
	Results    []notionAPIBlock `json:"results"`
	NextCursor *string          `json:"next_cursor"`
	HasMore    bool             `json:"has_more"`
}

type notionAPIBlock struct {
	Type             string          `json:"type"`
	Paragraph        notionTextBlock `json:"paragraph"`
	Heading1         notionTextBlock `json:"heading_1"`
	Heading2         notionTextBlock `json:"heading_2"`
	Heading3         notionTextBlock `json:"heading_3"`
	BulletedListItem notionTextBlock `json:"bulleted_list_item"`
	NumberedListItem notionTextBlock `json:"numbered_list_item"`
	Quote            notionTextBlock `json:"quote"`
	Code             notionTextBlock `json:"code"`
	ToDo             notionTextBlock `json:"to_do"`
	Toggle           notionTextBlock `json:"toggle"`
	Callout          notionTextBlock `json:"callout"`
}

type notionTextBlock struct {
	RichText []notionRichText `json:"rich_text"`
}

type notionRichText struct {
	PlainText string `json:"plain_text"`
}

// DeltaList calls the Notion POST /search endpoint and returns pages modified
// since sinceToken. sinceToken is an ISO 8601 timestamp (the last sync
// high-water mark); filtering is performed client-side because the Notion
// search API does not support a native "modified after" query parameter.
//
// ┌─────────────────────────────────────────────────────────────────────────────┐
// │ KNOWN LIMITATION — NOTION API CANNOT REPORT PAGE DELETIONS                 │
// │                                                                             │
// │ The Notion POST /search endpoint only surfaces pages that currently exist.  │
// │ There is no endpoint in the Notion API (version 2022-06-28) that returns   │
// │ deleted or archived pages removed since the last sync. Consequently,        │
// │ DeltaList will NEVER return ChangeDeleted entries.                          │
// │                                                                             │
// │ Deleted pages can only be detected via full-list reconciliation: comparing  │
// │ List's current live page set against the previously indexed set and issuing │
// │ deletes for any IDs that have disappeared. The sync handler performs this   │
// │ orphan detection separately, outside of DeltaList, during full sync passes. │
// └─────────────────────────────────────────────────────────────────────────────┘
//
// Timestamps never expire, so Expired is always false. All returned entries
// carry ChangeContent: the Notion search API does not distinguish ACL-only
// changes from content changes.
//
// ResumeToken is the RFC3339 timestamp of the most recently edited changed page
// in the response. Notion search pagination is not drained here today, so
// NextToken remains empty and the engine treats the pass as complete.
func (s *Source) DeltaList(ctx context.Context, sinceToken string) (contentsource.DeltaPage, error) {
	if s.live == nil {
		return contentsource.DeltaPage{}, errors.New("notion: DeltaList requires live mode")
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

	const searchBody = `{"sort":{"direction":"descending","timestamp":"last_edited_time"},"filter":{"property":"object","value":"page"}}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.live.baseURL+"/search",
		bytes.NewBufferString(searchBody),
	)
	if err != nil {
		return contentsource.DeltaPage{}, fmt.Errorf("notion: build delta request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Notion-Version", notionAPIVersion)
	if s.live.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.live.token)
	}

	resp, err := s.live.http.Do(req)
	if err != nil {
		return contentsource.DeltaPage{}, fmt.Errorf("notion: delta request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return contentsource.DeltaPage{}, fmt.Errorf("notion: delta returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, content.MaxBodyBytes*16))
	if err != nil {
		return contentsource.DeltaPage{}, fmt.Errorf("notion: read delta body: %w", err)
	}

	var sr notionSearchResponse
	if err := json.Unmarshal(body, &sr); err != nil {
		return contentsource.DeltaPage{}, fmt.Errorf("notion: parse delta response: %w", err)
	}

	// Timestamps never expire — Expired is always false.
	page := contentsource.DeltaPage{}
	var latestEditedAt time.Time
	for _, p := range sr.Results {
		if strings.TrimSpace(p.ID) == "" {
			continue
		}
		editedAt := parseTime(p.LastEditedTime)
		// Client-side filtering: skip pages not modified after sinceToken.
		if !sinceTime.IsZero() && !editedAt.After(sinceTime) {
			continue
		}
		// Track the most recent changed page to use as the persisted resume token.
		if editedAt.After(latestEditedAt) {
			latestEditedAt = editedAt
		}
		page.Changes = append(page.Changes, contentsource.DeltaEntry{
			DocRef: contentsource.DocRef{
				DocID:       content.Truncate(p.ID, content.MaxRefLen),
				Title:       notionPageTitle(p),
				ContentType: "text/plain",
				ModifiedAt:  editedAt,
			},
			// All changes are ChangeContent — the Notion search API does not
			// distinguish ACL-only changes from content changes.
			//
			// ChangeDeleted is never emitted. See the package-level doc on DeltaList
			// for the full explanation and the required orphan-detection fallback.
			ChangeKind: contentsource.ChangeContent,
		})
	}

	// ResumeToken stays empty when this pass had zero changes; the engine then
	// keeps the previously persisted timestamp instead of regressing the window.
	if !latestEditedAt.IsZero() {
		page.ResumeToken = latestEditedAt.UTC().Format(time.RFC3339)
	}

	return page, nil
}

func (s *Source) listLive(ctx context.Context, cursor string) ([]contentsource.DocRef, string, error) {
	if s.live == nil {
		return nil, "", errors.New("notion: List requires live mode")
	}

	payload := map[string]any{
		"page_size": 100,
		"filter": map[string]string{
			"property": "object",
			"value":    "page",
		},
	}
	if trimmed := strings.TrimSpace(cursor); trimmed != "" {
		payload["start_cursor"] = trimmed
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, "", fmt.Errorf("notion: build list body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.live.baseURL+"/search", bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("notion: build list request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Notion-Version", notionAPIVersion)
	if s.live.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.live.token)
	}

	resp, err := s.live.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("notion: list request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("notion: list returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, content.MaxBodyBytes*16))
	if err != nil {
		return nil, "", fmt.Errorf("notion: read list body: %w", err)
	}

	var sr notionSearchResponse
	if err := json.Unmarshal(body, &sr); err != nil {
		return nil, "", fmt.Errorf("notion: parse list response: %w", err)
	}

	refs := make([]contentsource.DocRef, 0, len(sr.Results))
	for _, p := range sr.Results {
		if strings.TrimSpace(p.ID) == "" {
			continue
		}
		refs = append(refs, contentsource.DocRef{
			DocID:       content.Truncate(p.ID, content.MaxRefLen),
			Title:       notionPageTitle(p),
			ContentType: "text/plain",
			ModifiedAt:  parseTime(p.LastEditedTime),
		})
	}

	next := ""
	if sr.HasMore && sr.NextCursor != nil {
		next = strings.TrimSpace(*sr.NextCursor)
	}
	return refs, next, nil
}

func notionPageTitle(p notionSearchPage) string {
	if len(p.Properties.Title.Title) == 0 {
		return ""
	}
	return content.Truncate(p.Properties.Title.Title[0].PlainText, content.MaxTitleLen)
}

// fetchLive reads a Notion page's child blocks and extracts only plain text from
// block types whose rich_text payload is content-bearing. Unsupported block types
// are skipped because the connector cannot honestly coerce them into text.
func (s *Source) fetchLive(ctx context.Context, docID string) (contentsource.Document, error) {
	if s.live == nil {
		return contentsource.Document{}, errors.New("notion: Fetch requires live mode")
	}

	var body strings.Builder
	nextCursor := ""
	for {
		endpoint := fmt.Sprintf("%s/blocks/%s/children?page_size=100", s.live.baseURL, url.PathEscape(docID))
		if nextCursor != "" {
			endpoint += "&start_cursor=" + url.QueryEscape(nextCursor)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return contentsource.Document{}, fmt.Errorf("notion: build fetch request: %w", err)
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Notion-Version", notionAPIVersion)
		if s.live.token != "" {
			req.Header.Set("Authorization", "Bearer "+s.live.token)
		}

		resp, err := s.live.http.Do(req)
		if err != nil {
			return contentsource.Document{}, fmt.Errorf("notion: fetch request: %w", err)
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, content.MaxBodyBytes))
		closeErr := resp.Body.Close()
		if readErr != nil {
			return contentsource.Document{}, fmt.Errorf("notion: read fetch body: %w", readErr)
		}
		if closeErr != nil {
			return contentsource.Document{}, fmt.Errorf("notion: close fetch body: %w", closeErr)
		}
		if resp.StatusCode != http.StatusOK {
			return contentsource.Document{}, fmt.Errorf("notion: fetch returned HTTP %d", resp.StatusCode)
		}

		var br notionBlocksResponse
		if err := json.Unmarshal(data, &br); err != nil {
			return contentsource.Document{}, fmt.Errorf("notion: parse fetch response: %w", err)
		}
		for _, block := range br.Results {
			text := strings.TrimSpace(blockPlainText(block))
			if text == "" || body.Len() >= content.MaxBodyBytes {
				continue
			}
			appendBlockText(&body, text)
		}

		if !br.HasMore || br.NextCursor == nil || strings.TrimSpace(*br.NextCursor) == "" {
			break
		}
		nextCursor = strings.TrimSpace(*br.NextCursor)
	}

	return contentsource.Document{
		Source:      contentsource.SourceNotion,
		DocID:       content.Truncate(docID, content.MaxRefLen),
		Body:        body.String(),
		ContentType: "text/plain",
		ACL:         []string{"workspace:shared"},
	}, nil
}

func appendBlockText(body *strings.Builder, text string) {
	if body.Len() >= content.MaxBodyBytes {
		return
	}
	if body.Len() > 0 {
		if body.Len()+1 > content.MaxBodyBytes {
			return
		}
		body.WriteString("\n")
	}
	for _, r := range text {
		if body.Len()+len(string(r)) > content.MaxBodyBytes {
			return
		}
		body.WriteRune(r)
	}
}

func blockPlainText(block notionAPIBlock) string {
	var rich []notionRichText
	switch block.Type {
	case "paragraph":
		rich = block.Paragraph.RichText
	case "heading_1":
		rich = block.Heading1.RichText
	case "heading_2":
		rich = block.Heading2.RichText
	case "heading_3":
		rich = block.Heading3.RichText
	case "bulleted_list_item":
		rich = block.BulletedListItem.RichText
	case "numbered_list_item":
		rich = block.NumberedListItem.RichText
	case "quote":
		rich = block.Quote.RichText
	case "code":
		rich = block.Code.RichText
	case "to_do":
		rich = block.ToDo.RichText
	case "toggle":
		rich = block.Toggle.RichText
	case "callout":
		rich = block.Callout.RichText
	default:
		return ""
	}
	var sb strings.Builder
	for _, rt := range rich {
		sb.WriteString(rt.PlainText)
	}
	return sb.String()
}

// FetchACL returns a workspace-level ACL placeholder for the given page.
//
// ┌─────────────────────────────────────────────────────────────────────────────┐
// │ KNOWN LIMITATION — NOTION API HAS NO PAGE-LEVEL ACL ENDPOINT               │
// │                                                                             │
// │ The Notion API (version 2022-06-28) does not expose a per-page             │
// │ permissions or ACL endpoint. Sharing state is only available at the         │
// │ workspace level through /users, which does not map to a per-document        │
// │ group ACL.                                                                  │
// │                                                                             │
// │ We return a single placeholder entry "workspace:shared". Retrieval policy   │
// │ will fall back to the knowledge base's default ACL for any Notion document. │
// │                                                                             │
// │ ExternalLabels is empty because Notion has no native sensitivity-label API. │
// │ Classification is empty for the same reason.                                │
// │                                                                             │
// │ If Notion exposes a page permissions API in a future version, this method   │
// │ should be updated to call it and populate ACL, ExternalLabels, and          │
// │ Classification accordingly.                                                 │
// └─────────────────────────────────────────────────────────────────────────────┘
func (s *Source) FetchACL(ctx context.Context, _ string) (contentsource.ACLResult, error) {
	if s.live == nil {
		return contentsource.ACLResult{}, errors.New("notion: FetchACL requires live mode")
	}
	if err := ctx.Err(); err != nil {
		return contentsource.ACLResult{}, err
	}
	// Notion does not expose page-level ACL; return a workspace placeholder.
	// ExternalLabels and Classification are always empty (no Notion label APIs).
	return contentsource.ACLResult{
		ACL: []string{"workspace:shared"},
	}, nil
}
