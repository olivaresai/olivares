// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package sharepoint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/connectors/internal/content"
	"github.com/olivaresai/olivares/sdk"
)

// Compile-time assertion: *Source always satisfies LiveSource (methods are
// defined unconditionally; they return errors when not in live mode).
var _ contentsource.LiveSource = (*Source)(nil)

// liveClient holds the runtime state for Microsoft Graph API access.
type liveClient struct {
	http      *http.Client
	siteID    string
	driveID   string
	graphBase string
	token     string
}

// newLiveClient constructs a liveClient from resolved configuration.
// The credential_ref setting is expected to hold the already-resolved bearer
// token (the composition root resolves the secret reference before Open).
func newLiveClient(cfg sdk.Config) (*liveClient, error) {
	siteID := strings.TrimSpace(cfg.Get("site_id"))
	if siteID == "" {
		return nil, errors.New("sharepoint: site_id is required for live mode")
	}
	driveID := strings.TrimSpace(cfg.Get("drive_id"))
	token := strings.TrimSpace(cfg.Get("credential_ref"))
	graphBase := strings.TrimSpace(cfg.Get("graph_base"))
	if graphBase == "" {
		graphBase = "https://graph.microsoft.com/v1.0"
	}
	return &liveClient{
		http:      &http.Client{},
		siteID:    siteID,
		driveID:   driveID,
		graphBase: graphBase,
		token:     token,
	}, nil
}

// graphDeltaResponse is the shape of a Microsoft Graph delta endpoint response.
type graphDeltaResponse struct {
	DeltaLink string      `json:"@odata.deltaLink"`
	NextLink  string      `json:"@odata.nextLink"`
	Value     []driveItem `json:"value"`
}

// DeltaList calls the Microsoft Graph delta endpoint and returns a page of
// changes since sinceToken. sinceToken is the persisted Graph deltaLink on the
// first pass call and a Graph nextLink on subsequent pagination calls. When the
// token has expired (HTTP 410 or 404) the returned page carries Expired=true so
// the caller can trigger a full re-sync.
func (s *Source) DeltaList(ctx context.Context, sinceToken string) (contentsource.DeltaPage, error) {
	if s.live == nil {
		return contentsource.DeltaPage{}, errors.New("sharepoint: DeltaList requires live mode")
	}

	var reqURL string
	if sinceToken != "" {
		// Graph returns a full delta link URL; use it verbatim.
		reqURL = sinceToken
	} else {
		reqURL = fmt.Sprintf(
			"%s/sites/%s/drive/root/delta?$select=id,name,parentReference,lastModifiedDateTime,permissions,sensitivityLabel,deleted",
			s.live.graphBase, s.live.siteID,
		)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return contentsource.DeltaPage{}, fmt.Errorf("sharepoint: build delta request: %w", err)
	}
	if s.live.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.live.token)
	}

	resp, err := s.live.http.Do(req)
	if err != nil {
		return contentsource.DeltaPage{}, fmt.Errorf("sharepoint: delta request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// 410 Gone or 404 Not Found means the delta token has expired.
	if resp.StatusCode == http.StatusGone || resp.StatusCode == http.StatusNotFound {
		return contentsource.DeltaPage{Expired: true}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return contentsource.DeltaPage{}, fmt.Errorf("sharepoint: delta returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, content.MaxBodyBytes*16))
	if err != nil {
		return contentsource.DeltaPage{}, fmt.Errorf("sharepoint: read delta body: %w", err)
	}

	var gr graphDeltaResponse
	if err := json.Unmarshal(body, &gr); err != nil {
		return contentsource.DeltaPage{}, fmt.Errorf("sharepoint: parse delta response: %w", err)
	}

	page := contentsource.DeltaPage{
		NextToken:   gr.NextLink,
		ResumeToken: gr.DeltaLink,
	}
	for _, item := range gr.Value {
		if strings.TrimSpace(item.ID) == "" {
			continue
		}
		entry := contentsource.DeltaEntry{
			DocRef: contentsource.DocRef{
				DocID:       content.Truncate(item.ID, content.MaxRefLen),
				Title:       content.Truncate(item.Name, content.MaxTitleLen),
				ContentType: "text/plain",
				ModifiedAt:  parseTime(item.LastModifiedDateTime),
			},
		}
		switch {
		case item.Deleted != nil:
			entry.ChangeKind = contentsource.ChangeDeleted
		case len(item.Permissions) > 0 || strings.TrimSpace(item.SensitivityLabel.DisplayName) != "":
			// Presence of permission or label data in a delta item indicates an
			// ACL or classification change; treat as ACL refresh.
			entry.ChangeKind = contentsource.ChangeACL
		default:
			entry.ChangeKind = contentsource.ChangeContent
		}
		page.Changes = append(page.Changes, entry)
	}
	return page, nil
}

// listLive enumerates driveItems through Microsoft Graph delta without a
// persisted delta token. The List cursor is the Graph @odata.nextLink only;
// @odata.deltaLink is ignored because List is a snapshot enumeration path.
func (s *Source) listLive(ctx context.Context, cursor string) ([]contentsource.DocRef, string, error) {
	if s.live == nil {
		return nil, "", errors.New("sharepoint: List requires live mode")
	}

	reqURL := strings.TrimSpace(cursor)
	if reqURL == "" {
		reqURL = fmt.Sprintf(
			"%s/sites/%s/drive/root/delta?$select=id,name,parentReference,lastModifiedDateTime,deleted",
			s.live.graphBase, s.live.siteID,
		)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("sharepoint: build list request: %w", err)
	}
	if s.live.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.live.token)
	}

	resp, err := s.live.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("sharepoint: list request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("sharepoint: list returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, content.MaxBodyBytes*16))
	if err != nil {
		return nil, "", fmt.Errorf("sharepoint: read list body: %w", err)
	}

	var gr graphDeltaResponse
	if err := json.Unmarshal(body, &gr); err != nil {
		return nil, "", fmt.Errorf("sharepoint: parse list response: %w", err)
	}

	refs := make([]contentsource.DocRef, 0, len(gr.Value))
	for _, item := range gr.Value {
		if strings.TrimSpace(item.ID) == "" || item.Deleted != nil {
			continue
		}
		refs = append(refs, contentsource.DocRef{
			DocID:       content.Truncate(item.ID, content.MaxRefLen),
			Title:       content.Truncate(item.Name, content.MaxTitleLen),
			ContentType: "text/plain",
			ModifiedAt:  parseTime(item.LastModifiedDateTime),
		})
	}
	return refs, gr.NextLink, nil
}

// fetchLive downloads a drive item body through Microsoft Graph. Graph may
// redirect to a pre-signed download URL; the default http.Client follows that
// redirect and strips Authorization when it crosses hosts.
func (s *Source) fetchLive(ctx context.Context, docID string) (contentsource.Document, error) {
	if s.live == nil {
		return contentsource.Document{}, errors.New("sharepoint: Fetch requires live mode")
	}

	escapedID := url.PathEscape(docID)
	var reqURL string
	if s.live.driveID != "" {
		reqURL = fmt.Sprintf("%s/drives/%s/items/%s/content", s.live.graphBase, s.live.driveID, escapedID)
	} else {
		reqURL = fmt.Sprintf("%s/sites/%s/drive/items/%s/content", s.live.graphBase, s.live.siteID, escapedID)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return contentsource.Document{}, fmt.Errorf("sharepoint: build fetch request: %w", err)
	}
	if s.live.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.live.token)
	}

	resp, err := s.live.http.Do(req)
	if err != nil {
		return contentsource.Document{}, fmt.Errorf("sharepoint: fetch request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return contentsource.Document{}, fmt.Errorf("sharepoint: fetch returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, content.MaxBodyBytes))
	if err != nil {
		return contentsource.Document{}, fmt.Errorf("sharepoint: read fetch body: %w", err)
	}
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	return contentsource.Document{
		Source:      contentsource.SourceSharePoint,
		DocID:       content.Truncate(docID, content.MaxRefLen),
		Title:       contentDispositionTitle(resp.Header.Get("Content-Disposition")),
		Body:        string(body),
		ContentType: contentType,
		SpaceRef:    "site:" + s.live.siteID,
	}, nil
}

func contentDispositionTitle(header string) string {
	if strings.TrimSpace(header) == "" {
		return ""
	}
	_, params, err := mime.ParseMediaType(header)
	if err != nil {
		return ""
	}
	return content.Truncate(strings.TrimSpace(params["filename"]), content.MaxTitleLen)
}

// FetchACL calls the Graph items endpoint for a single document and returns
// its current permission groups and sensitivity labels.
func (s *Source) FetchACL(ctx context.Context, docID string) (contentsource.ACLResult, error) {
	if s.live == nil {
		return contentsource.ACLResult{}, errors.New("sharepoint: FetchACL requires live mode")
	}

	// Build the item URL; prefer drives/{driveId} when a drive ID is configured,
	// fall back to sites/{siteId}/drive for the site's default drive.
	var reqURL string
	if s.live.driveID != "" {
		reqURL = fmt.Sprintf(
			"%s/drives/%s/items/%s?$select=id,permissions,sensitivityLabel",
			s.live.graphBase, s.live.driveID, docID,
		)
	} else {
		reqURL = fmt.Sprintf(
			"%s/sites/%s/drive/items/%s?$select=id,permissions,sensitivityLabel",
			s.live.graphBase, s.live.siteID, docID,
		)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return contentsource.ACLResult{}, fmt.Errorf("sharepoint: build acl request: %w", err)
	}
	if s.live.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.live.token)
	}

	resp, err := s.live.http.Do(req)
	if err != nil {
		return contentsource.ACLResult{}, fmt.Errorf("sharepoint: acl request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return contentsource.ACLResult{}, fmt.Errorf("sharepoint: acl returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, content.MaxBodyBytes))
	if err != nil {
		return contentsource.ACLResult{}, fmt.Errorf("sharepoint: read acl body: %w", err)
	}

	var item driveItem
	if err := json.Unmarshal(body, &item); err != nil {
		return contentsource.ACLResult{}, fmt.Errorf("sharepoint: parse acl response: %w", err)
	}

	acl := make([]string, 0, len(item.Permissions))
	for _, p := range item.Permissions {
		if g := p.GrantedToV2.SiteGroup.DisplayName; g != "" {
			acl = append(acl, "group:"+g)
		}
		if g := p.GrantedToV2.Group.DisplayName; g != "" {
			acl = append(acl, "group:"+g)
		}
	}

	var extLabels []string
	if label := strings.TrimSpace(item.SensitivityLabel.DisplayName); label != "" {
		extLabels = append(extLabels, "purview:"+strings.ToLower(label))
	}

	return contentsource.ACLResult{
		ACL:            content.CleanACL(acl),
		ExternalLabels: extLabels,
		Classification: strings.ToLower(strings.TrimSpace(item.SensitivityLabel.DisplayName)),
	}, nil
}
