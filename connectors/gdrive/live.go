// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gdrive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// liveClient holds the runtime state for Google Drive API v3 access.
type liveClient struct {
	http    *http.Client
	driveID string
	apiBase string
	token   string
}

// newLiveClient constructs a liveClient from resolved configuration.
// The credential_ref setting is expected to hold the already-resolved bearer
// token (the composition root resolves the secret reference before Open).
func newLiveClient(cfg sdk.Config) (*liveClient, error) {
	driveID := strings.TrimSpace(cfg.Get("drive_id"))
	token := strings.TrimSpace(cfg.Get("credential_ref"))
	apiBase := strings.TrimSpace(cfg.Get("api_base"))
	if apiBase == "" {
		apiBase = "https://www.googleapis.com/drive/v3"
	}
	return &liveClient{
		http:    &http.Client{},
		driveID: driveID,
		apiBase: apiBase,
		token:   token,
	}, nil
}

// --- Google Drive Changes API response shapes ------------------------------------

type driveChangesResponse struct {
	NextPageToken     string             `json:"nextPageToken"`
	NewStartPageToken string             `json:"newStartPageToken"`
	Changes           []driveChangeEntry `json:"changes"`
}

type driveChangeEntry struct {
	FileID  string           `json:"fileId"`
	Removed bool             `json:"removed"`
	File    *driveChangeFile `json:"file"`
}

type driveChangeFile struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	MimeType     string            `json:"mimeType"`
	ModifiedTime string            `json:"modifiedTime"`
	Permissions  []drivePermission `json:"permissions"`
	Labels       []driveLabel      `json:"labels"`
}

// driveLabel represents a Google Workspace Label attached to a file.
type driveLabel struct {
	ID string `json:"id"`
}

type startPageTokenResponse struct {
	StartPageToken string `json:"startPageToken"`
}

type driveFileDetails struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	MimeType     string            `json:"mimeType"`
	ModifiedTime string            `json:"modifiedTime"`
	Parents      []string          `json:"parents"`
	Permissions  []drivePermission `json:"permissions"`
	Labels       []driveLabel      `json:"labels"`
}

type driveFilesListResponse struct {
	NextPageToken string            `json:"nextPageToken"`
	Files         []driveChangeFile `json:"files"`
}

// DeltaList calls the Drive Changes API and returns a page of changes since
// sinceToken. When sinceToken is empty, an initial page token is first obtained
// from the changes/startPageToken endpoint. HTTP 410 Gone returns Expired=true
// so the caller can trigger a full re-sync.
func (s *Source) DeltaList(ctx context.Context, sinceToken string) (contentsource.DeltaPage, error) {
	if s.live == nil {
		return contentsource.DeltaPage{}, errors.New("gdrive: DeltaList requires live mode")
	}

	pageToken := sinceToken
	if pageToken == "" {
		// Fetch the initial page token before listing changes.
		var err error
		pageToken, err = s.live.fetchStartPageToken(ctx)
		if err != nil {
			return contentsource.DeltaPage{}, err
		}
	}

	const fields = "changes(fileId,removed,file(id,name,mimeType,modifiedTime,permissions,labels)),newStartPageToken,nextPageToken"
	reqURL := fmt.Sprintf("%s/changes?pageToken=%s&fields=%s", s.live.apiBase, pageToken, fields)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return contentsource.DeltaPage{}, fmt.Errorf("gdrive: build changes request: %w", err)
	}
	if s.live.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.live.token)
	}

	resp, err := s.live.http.Do(req)
	if err != nil {
		return contentsource.DeltaPage{}, fmt.Errorf("gdrive: changes request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// HTTP 410 Gone means the page token has expired — caller must trigger full re-sync.
	if resp.StatusCode == http.StatusGone {
		return contentsource.DeltaPage{Expired: true}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return contentsource.DeltaPage{}, fmt.Errorf("gdrive: changes returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, content.MaxBodyBytes*16))
	if err != nil {
		return contentsource.DeltaPage{}, fmt.Errorf("gdrive: read changes body: %w", err)
	}

	var cr driveChangesResponse
	if err := json.Unmarshal(body, &cr); err != nil {
		return contentsource.DeltaPage{}, fmt.Errorf("gdrive: parse changes response: %w", err)
	}

	page := contentsource.DeltaPage{
		NextToken:   cr.NextPageToken,
		ResumeToken: cr.NewStartPageToken,
	}
	for _, ch := range cr.Changes {
		if strings.TrimSpace(ch.FileID) == "" {
			continue
		}
		entry := contentsource.DeltaEntry{
			DocRef: contentsource.DocRef{ContentType: "text/plain"},
		}
		switch {
		case ch.Removed:
			entry.DocRef.DocID = content.Truncate(ch.FileID, content.MaxRefLen)
			entry.ChangeKind = contentsource.ChangeDeleted
		case ch.File != nil:
			entry.DocRef.DocID = content.Truncate(ch.File.ID, content.MaxRefLen)
			entry.DocRef.Title = content.Truncate(ch.File.Name, content.MaxTitleLen)
			entry.DocRef.ContentType = contentTypeFor(ch.File.MimeType)
			entry.DocRef.ModifiedAt = parseTime(ch.File.ModifiedTime)
			entry.ChangeKind = contentsource.ChangeContent
		default:
			// No file metadata (e.g. shared drive metadata-only change); use fileId.
			entry.DocRef.DocID = content.Truncate(ch.FileID, content.MaxRefLen)
			entry.ChangeKind = contentsource.ChangeContent
		}
		page.Changes = append(page.Changes, entry)
	}
	return page, nil
}

func (s *Source) listLive(ctx context.Context, cursor string) ([]contentsource.DocRef, string, error) {
	if s.live == nil {
		return nil, "", errors.New("gdrive: List requires live mode")
	}

	values := url.Values{}
	values.Set("pageSize", "100")
	values.Set("fields", "nextPageToken,files(id,name,mimeType,modifiedTime)")
	values.Set("supportsAllDrives", "true")
	if s.live.driveID != "" {
		values.Set("corpora", "drive")
		values.Set("driveId", s.live.driveID)
		values.Set("includeItemsFromAllDrives", "true")
	}
	if token := strings.TrimSpace(cursor); token != "" {
		values.Set("pageToken", token)
	}
	reqURL := s.live.apiBase + "/files?" + values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("gdrive: build list request: %w", err)
	}
	if s.live.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.live.token)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := s.live.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("gdrive: list request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("gdrive: list returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, content.MaxBodyBytes*16))
	if err != nil {
		return nil, "", fmt.Errorf("gdrive: read list body: %w", err)
	}

	var lr driveFilesListResponse
	if err := json.Unmarshal(body, &lr); err != nil {
		return nil, "", fmt.Errorf("gdrive: parse list response: %w", err)
	}
	refs := make([]contentsource.DocRef, 0, len(lr.Files))
	for _, f := range lr.Files {
		if strings.TrimSpace(f.ID) == "" {
			continue
		}
		refs = append(refs, contentsource.DocRef{
			DocID:       content.Truncate(f.ID, content.MaxRefLen),
			Title:       content.Truncate(f.Name, content.MaxTitleLen),
			ContentType: contentTypeFor(f.MimeType),
			ModifiedAt:  parseTime(f.ModifiedTime),
		})
	}
	return refs, lr.NextPageToken, nil
}

func (s *Source) fetchLive(ctx context.Context, docID string) (contentsource.Document, error) {
	if s.live == nil {
		return contentsource.Document{}, errors.New("gdrive: Fetch requires live mode")
	}

	meta, err := s.live.fetchFileMetadata(ctx, docID)
	if err != nil {
		return contentsource.Document{}, err
	}

	escapedID := url.PathEscape(docID)
	contentType := ""
	var reqURL string
	if strings.HasPrefix(meta.MimeType, "application/vnd.google-apps.") {
		values := url.Values{}
		values.Set("mimeType", "text/plain")
		reqURL = fmt.Sprintf("%s/files/%s/export?%s", s.live.apiBase, escapedID, values.Encode())
		contentType = "text/plain"
	} else {
		reqURL = fmt.Sprintf("%s/files/%s?alt=media", s.live.apiBase, escapedID)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return contentsource.Document{}, fmt.Errorf("gdrive: build fetch request: %w", err)
	}
	if s.live.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.live.token)
	}
	resp, err := s.live.http.Do(req)
	if err != nil {
		return contentsource.Document{}, fmt.Errorf("gdrive: fetch request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return contentsource.Document{}, fmt.Errorf("gdrive: fetch returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, content.MaxBodyBytes))
	if err != nil {
		return contentsource.Document{}, fmt.Errorf("gdrive: read fetch body: %w", err)
	}
	if contentType == "" {
		contentType = strings.TrimSpace(resp.Header.Get("Content-Type"))
		if contentType == "" {
			contentType = "application/octet-stream"
		}
	}
	space := ""
	if len(meta.Parents) > 0 {
		space = "folder:" + meta.Parents[0]
	}
	return contentsource.Document{
		Source:      contentsource.SourceGDrive,
		DocID:       content.Truncate(docID, content.MaxRefLen),
		Title:       content.Truncate(meta.Name, content.MaxTitleLen),
		Body:        string(body),
		ContentType: contentType,
		SpaceRef:    space,
		ModifiedAt:  parseTime(meta.ModifiedTime),
		Attributes:  map[string]string{"mime_type": meta.MimeType},
	}, nil
}

func (lc *liveClient) fetchFileMetadata(ctx context.Context, docID string) (driveFileDetails, error) {
	values := url.Values{}
	values.Set("fields", "id,name,mimeType,modifiedTime,parents")
	values.Set("supportsAllDrives", "true")
	reqURL := fmt.Sprintf("%s/files/%s?%s", lc.apiBase, url.PathEscape(docID), values.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return driveFileDetails{}, fmt.Errorf("gdrive: build metadata request: %w", err)
	}
	if lc.token != "" {
		req.Header.Set("Authorization", "Bearer "+lc.token)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := lc.http.Do(req)
	if err != nil {
		return driveFileDetails{}, fmt.Errorf("gdrive: metadata request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return driveFileDetails{}, fmt.Errorf("gdrive: metadata returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, content.MaxBodyBytes))
	if err != nil {
		return driveFileDetails{}, fmt.Errorf("gdrive: read metadata body: %w", err)
	}
	var meta driveFileDetails
	if err := json.Unmarshal(body, &meta); err != nil {
		return driveFileDetails{}, fmt.Errorf("gdrive: parse metadata response: %w", err)
	}
	return meta, nil
}

// fetchStartPageToken requests the current changes start page token from Drive.
func (lc *liveClient) fetchStartPageToken(ctx context.Context) (string, error) {
	reqURL := lc.apiBase + "/changes/startPageToken"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", fmt.Errorf("gdrive: build startPageToken request: %w", err)
	}
	if lc.token != "" {
		req.Header.Set("Authorization", "Bearer "+lc.token)
	}
	resp, err := lc.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("gdrive: startPageToken request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gdrive: startPageToken returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", fmt.Errorf("gdrive: read startPageToken body: %w", err)
	}
	var spt startPageTokenResponse
	if err := json.Unmarshal(body, &spt); err != nil {
		return "", fmt.Errorf("gdrive: parse startPageToken response: %w", err)
	}
	return spt.StartPageToken, nil
}

// FetchACL calls the Drive files endpoint for a single document and returns
// its current permissions and attached Workspace labels.
func (s *Source) FetchACL(ctx context.Context, docID string) (contentsource.ACLResult, error) {
	if s.live == nil {
		return contentsource.ACLResult{}, errors.New("gdrive: FetchACL requires live mode")
	}

	reqURL := fmt.Sprintf(
		"%s/files/%s?fields=permissions(id,type,emailAddress,domain,role),labels",
		s.live.apiBase, docID,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return contentsource.ACLResult{}, fmt.Errorf("gdrive: build acl request: %w", err)
	}
	if s.live.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.live.token)
	}

	resp, err := s.live.http.Do(req)
	if err != nil {
		return contentsource.ACLResult{}, fmt.Errorf("gdrive: acl request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return contentsource.ACLResult{}, fmt.Errorf("gdrive: acl returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, content.MaxBodyBytes))
	if err != nil {
		return contentsource.ACLResult{}, fmt.Errorf("gdrive: read acl body: %w", err)
	}

	var fd driveFileDetails
	if err := json.Unmarshal(body, &fd); err != nil {
		return contentsource.ACLResult{}, fmt.Errorf("gdrive: parse acl response: %w", err)
	}

	acl := make([]string, 0, len(fd.Permissions))
	for _, p := range fd.Permissions {
		switch strings.ToLower(p.Type) {
		case "group":
			acl = append(acl, "group:"+principalRef(p.ID, p.EmailAddress))
		case "user":
			acl = append(acl, "user:"+principalRef(p.ID, p.EmailAddress))
		case "domain":
			if p.Domain != "" {
				acl = append(acl, "domain:"+p.Domain)
			}
		case "anyone":
			acl = append(acl, "anyone")
		}
	}

	var extLabels []string
	for _, lbl := range fd.Labels {
		if lbl.ID != "" {
			extLabels = append(extLabels, "gdrive:"+lbl.ID)
		}
	}

	return contentsource.ACLResult{
		ACL:            content.CleanACL(acl),
		ExternalLabels: extLabels,
	}, nil
}
