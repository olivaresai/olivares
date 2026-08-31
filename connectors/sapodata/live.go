// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package sapodata

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

// liveClient holds the runtime state for SAP OData v4 API access.
type liveClient struct {
	http        *http.Client
	baseURL     string
	servicePath string
	token       string
	authScheme  string
	tokenURL    string
	entitySets  []string
}

// newLiveClient constructs a liveClient from resolved configuration.
// The credential_ref setting is expected to hold the already-resolved
// credential (the composition root resolves the secret reference before Open).
func newLiveClient(cfg sdk.Config) (*liveClient, error) {
	baseURL := strings.TrimSpace(cfg.Get("base_url"))
	if baseURL == "" {
		return nil, errors.New("sapodata: base_url is required for live mode")
	}
	servicePath := strings.TrimSpace(cfg.Get("service_path"))
	if servicePath == "" {
		return nil, errors.New("sapodata: service_path is required for live mode")
	}
	authScheme := strings.TrimSpace(cfg.Get("auth_scheme"))
	if authScheme == "" {
		authScheme = "basic"
	}
	tokenURL := strings.TrimSpace(cfg.Get("token_url"))
	if authScheme == "oauth2_btp" && tokenURL == "" {
		return nil, errors.New("sapodata: token_url is required for oauth2_btp auth scheme")
	}
	token := strings.TrimSpace(cfg.Get("credential_ref"))

	var entitySets []string
	if raw := strings.TrimSpace(cfg.Get("entity_sets")); raw != "" {
		for _, es := range strings.Split(raw, ",") {
			if s := strings.TrimSpace(es); s != "" {
				entitySets = append(entitySets, s)
			}
		}
	}

	return &liveClient{
		http:        &http.Client{},
		baseURL:     strings.TrimRight(baseURL, "/"),
		servicePath: strings.TrimRight(servicePath, "/"),
		token:       token,
		authScheme:  authScheme,
		tokenURL:    tokenURL,
		entitySets:  entitySets,
	}, nil
}

// setAuth adds authentication headers to the request based on the configured
// auth scheme. For "basic" it adds the credential as-is (pre-resolved Base64);
// for "oauth2_btp" it adds a Bearer token (in production the composition root
// would handle the OAuth flow; here the resolved credential is the token).
func (lc *liveClient) setAuth(req *http.Request) {
	if lc.token == "" {
		return
	}
	switch lc.authScheme {
	case "oauth2_btp":
		req.Header.Set("Authorization", "Bearer "+lc.token)
	default: // "basic"
		req.Header.Set("Authorization", "Basic "+lc.token)
	}
}

// validateDeltaLink checks that a delta link URL targets the same host as the
// configured baseURL, preventing SSRF via a crafted sinceToken.
func (lc *liveClient) validateDeltaLink(link string) error {
	linkURL, err := url.Parse(link)
	if err != nil {
		return fmt.Errorf("sapodata: invalid delta link URL: %w", err)
	}
	baseURL, err := url.Parse(lc.baseURL)
	if err != nil {
		return fmt.Errorf("sapodata: invalid base_url: %w", err)
	}
	if !strings.EqualFold(linkURL.Host, baseURL.Host) || linkURL.Scheme != baseURL.Scheme {
		return fmt.Errorf("sapodata: delta link host %q does not match configured base %q", linkURL.Host, baseURL.Host)
	}
	return nil
}

// --- OData v4 delta response shape ---------------------------------------------

type odataDeltaResponse struct {
	Value     []odataEntity `json:"value"`
	NextLink  string        `json:"@odata.nextLink"`
	DeltaLink string        `json:"@odata.deltaLink"`
}

// DeltaList calls the SAP OData v4 endpoint with delta-token change tracking
// and returns a page of entity changes. When sinceToken is non-empty it is the
// full OData delta link URL and is used verbatim. When empty the initial
// entity-set URL with $deltatoken=! is built from configuration.
//
// HTTP 410 Gone means the delta token has expired; the returned page carries
// Expired=true so the caller can trigger a full re-sync.
func (s *Source) DeltaList(ctx context.Context, sinceToken string) (contentsource.DeltaPage, error) {
	if s.live == nil {
		return contentsource.DeltaPage{}, errors.New("sapodata: DeltaList requires live mode")
	}

	var reqURL string
	if sinceToken != "" {
		// The previous delta link is a full URL returned by the server. Validate
		// that its host matches the configured baseURL to prevent SSRF: a crafted
		// sinceToken pointing at an internal service would leak credentials.
		if err := s.live.validateDeltaLink(sinceToken); err != nil {
			return contentsource.DeltaPage{}, err
		}
		reqURL = sinceToken
	} else {
		// Build the initial delta request. Use the first configured entity set.
		entitySet := "EntitySet"
		if len(s.live.entitySets) > 0 {
			entitySet = s.live.entitySets[0]
		}
		reqURL = fmt.Sprintf(
			"%s%s/%s?$deltatoken=!",
			s.live.baseURL, s.live.servicePath, entitySet,
		)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return contentsource.DeltaPage{}, fmt.Errorf("sapodata: build delta request: %w", err)
	}
	s.live.setAuth(req)
	req.Header.Set("Accept", "application/json")

	resp, err := s.live.http.Do(req)
	if err != nil {
		return contentsource.DeltaPage{}, fmt.Errorf("sapodata: delta request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// HTTP 410 Gone means the delta token has expired.
	if resp.StatusCode == http.StatusGone {
		return contentsource.DeltaPage{Expired: true}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return contentsource.DeltaPage{}, fmt.Errorf("sapodata: delta returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, content.MaxBodyBytes*16))
	if err != nil {
		return contentsource.DeltaPage{}, fmt.Errorf("sapodata: read delta body: %w", err)
	}

	var dr odataDeltaResponse
	if err := json.Unmarshal(body, &dr); err != nil {
		return contentsource.DeltaPage{}, fmt.Errorf("sapodata: parse delta response: %w", err)
	}

	page := contentsource.DeltaPage{
		NextToken:   dr.NextLink,
		ResumeToken: dr.DeltaLink,
	}
	for _, e := range dr.Value {
		if strings.TrimSpace(e.ID) == "" {
			continue
		}
		entry := contentsource.DeltaEntry{
			DocRef: contentsource.DocRef{
				DocID:       content.Truncate(e.ID, content.MaxRefLen),
				Title:       content.Truncate(e.Name, content.MaxTitleLen),
				ContentType: "text/plain",
				ModifiedAt:  parseTime(e.ModifiedAt),
			},
		}
		// An entity with an empty Name and empty Description in a delta response
		// is treated as a deletion marker.
		if strings.TrimSpace(e.Name) == "" && strings.TrimSpace(e.Description) == "" {
			entry.ChangeKind = contentsource.ChangeDeleted
		} else {
			entry.ChangeKind = contentsource.ChangeContent
		}
		page.Changes = append(page.Changes, entry)
	}
	return page, nil
}

func (s *Source) listLive(ctx context.Context, cursor string) ([]contentsource.DocRef, string, error) {
	if s.live == nil {
		return nil, "", errors.New("sapodata: List requires live mode")
	}

	reqURL := strings.TrimSpace(cursor)
	if reqURL != "" {
		if err := s.live.validateDeltaLink(reqURL); err != nil {
			return nil, "", err
		}
	} else {
		reqURL = fmt.Sprintf("%s%s/%s?$top=100", s.live.baseURL, s.live.servicePath, s.live.entitySet())
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("sapodata: build list request: %w", err)
	}
	s.live.setAuth(req)
	req.Header.Set("Accept", "application/json")

	resp, err := s.live.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("sapodata: list request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("sapodata: list returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, content.MaxBodyBytes*16))
	if err != nil {
		return nil, "", fmt.Errorf("sapodata: read list body: %w", err)
	}
	var lr odataDeltaResponse
	if err := json.Unmarshal(body, &lr); err != nil {
		return nil, "", fmt.Errorf("sapodata: parse list response: %w", err)
	}
	refs := make([]contentsource.DocRef, 0, len(lr.Value))
	for _, e := range lr.Value {
		if strings.TrimSpace(e.ID) == "" {
			continue
		}
		if strings.TrimSpace(e.Name) == "" && strings.TrimSpace(e.Description) == "" {
			continue
		}
		refs = append(refs, contentsource.DocRef{
			DocID:       content.Truncate(e.ID, content.MaxRefLen),
			Title:       content.Truncate(e.Name, content.MaxTitleLen),
			ContentType: "application/json",
			ModifiedAt:  parseTime(e.ModifiedAt),
		})
	}
	return refs, lr.NextLink, nil
}

func (s *Source) fetchLive(ctx context.Context, docID string) (contentsource.Document, error) {
	if s.live == nil {
		return contentsource.Document{}, errors.New("sapodata: Fetch requires live mode")
	}

	reqURL := fmt.Sprintf("%s%s/%s", s.live.baseURL, s.live.servicePath, s.live.entityPath(docID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return contentsource.Document{}, fmt.Errorf("sapodata: build fetch request: %w", err)
	}
	s.live.setAuth(req)
	req.Header.Set("Accept", "application/json")

	resp, err := s.live.http.Do(req)
	if err != nil {
		return contentsource.Document{}, fmt.Errorf("sapodata: fetch request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return contentsource.Document{}, fmt.Errorf("sapodata: fetch returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, content.MaxBodyBytes))
	if err != nil {
		return contentsource.Document{}, fmt.Errorf("sapodata: read fetch body: %w", err)
	}
	var e odataEntity
	if err := json.Unmarshal(body, &e); err != nil {
		return contentsource.Document{}, fmt.Errorf("sapodata: parse fetch response: %w", err)
	}
	if strings.TrimSpace(e.ID) == "" {
		e.ID = docID
	}
	var acl []string
	if g := strings.TrimSpace(e.AuthGroup); g != "" {
		acl = append(acl, "role:"+g)
	}
	return contentsource.Document{
		Source:      contentsource.SourceSAPOData,
		DocID:       content.Truncate(docID, content.MaxRefLen),
		Title:       content.Truncate(e.Name, content.MaxTitleLen),
		Body:        string(body),
		ContentType: "application/json",
		ACL:         content.CleanACL(acl),
		SpaceRef:    "entity_set:" + extractEntitySet(e.ID),
		ModifiedAt:  parseTime(e.ModifiedAt),
		Attributes:  e.Attributes,
	}, nil
}

func (lc *liveClient) entitySet() string {
	if len(lc.entitySets) > 0 {
		return lc.entitySets[0]
	}
	return "EntitySet"
}

func (lc *liveClient) entityPath(docID string) string {
	docID = strings.TrimSpace(docID)
	if strings.Contains(docID, "(") {
		return docID
	}
	escapedKey := url.PathEscape(strings.ReplaceAll(docID, "'", "''"))
	return fmt.Sprintf("%s('%s')", lc.entitySet(), escapedKey)
}

// FetchACL queries the SAP OData v4 endpoint for a single entity's
// AuthorizationGroup and returns the corresponding ACL.
func (s *Source) FetchACL(ctx context.Context, docID string) (contentsource.ACLResult, error) {
	if s.live == nil {
		return contentsource.ACLResult{}, errors.New("sapodata: FetchACL requires live mode")
	}

	// Build the entity URL. Escape single quotes per OData spec (double them)
	// and URL-encode the key to prevent path injection.
	escapedKey := url.PathEscape(strings.ReplaceAll(docID, "'", "''"))
	reqURL := fmt.Sprintf(
		"%s%s/%s('%s')?$select=AuthorizationGroup",
		s.live.baseURL, s.live.servicePath, s.live.entitySet(), escapedKey,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return contentsource.ACLResult{}, fmt.Errorf("sapodata: build acl request: %w", err)
	}
	s.live.setAuth(req)
	req.Header.Set("Accept", "application/json")

	resp, err := s.live.http.Do(req)
	if err != nil {
		return contentsource.ACLResult{}, fmt.Errorf("sapodata: acl request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return contentsource.ACLResult{}, fmt.Errorf("sapodata: acl returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, content.MaxBodyBytes))
	if err != nil {
		return contentsource.ACLResult{}, fmt.Errorf("sapodata: read acl body: %w", err)
	}

	var e odataEntity
	if err := json.Unmarshal(body, &e); err != nil {
		return contentsource.ACLResult{}, fmt.Errorf("sapodata: parse acl response: %w", err)
	}

	var acl []string
	if g := strings.TrimSpace(e.AuthGroup); g != "" {
		acl = append(acl, "role:"+g)
	}

	return contentsource.ACLResult{
		ACL: content.CleanACL(acl),
	}, nil
}
