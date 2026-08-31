// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package salesforce

import (
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

// defaultSObjectTypes is the default set of SObject types to sync when none
// are configured.
const defaultSObjectTypes = "Account,Case,Knowledge__kav,ContentDocument"

// liveClient holds the runtime state for Salesforce REST API access.
type liveClient struct {
	http         *http.Client
	baseURL      string
	token        string
	clientID     string
	username     string
	loginURL     string
	sobjectTypes []string
}

const salesforceAPIVersion = "v59.0"

// newLiveClient constructs a liveClient from resolved configuration.
func newLiveClient(cfg sdk.Config) (*liveClient, error) {
	baseURL := strings.TrimSpace(cfg.Get("base_url"))
	if baseURL == "" {
		return nil, errors.New("salesforce: base_url is required for live mode")
	}
	sobjectTypesRaw := strings.TrimSpace(cfg.Get("sobject_types"))
	if sobjectTypesRaw == "" {
		sobjectTypesRaw = defaultSObjectTypes
	}
	var sobjectTypes []string
	for _, t := range strings.Split(sobjectTypesRaw, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			sobjectTypes = append(sobjectTypes, t)
		}
	}
	loginURL := strings.TrimSpace(cfg.Get("login_url"))
	if loginURL == "" {
		loginURL = "https://login.salesforce.com"
	}
	return &liveClient{
		http:         &http.Client{},
		baseURL:      strings.TrimRight(baseURL, "/"),
		token:        strings.TrimSpace(cfg.Get("credential_ref")),
		clientID:     strings.TrimSpace(cfg.Get("client_id")),
		username:     strings.TrimSpace(cfg.Get("username")),
		loginURL:     strings.TrimRight(loginURL, "/"),
		sobjectTypes: sobjectTypes,
	}, nil
}

// DeltaList queries each configured SObject type for records modified since
// sinceToken (an RFC3339 high-water SystemModstamp). If sinceToken is empty,
// all records are returned (full sync). ResumeToken is the latest changed
// SystemModstamp from the results. Expired is always false (timestamps never
// expire in Salesforce).
func (s *Source) DeltaList(ctx context.Context, sinceToken string) (contentsource.DeltaPage, error) {
	if s.live == nil {
		return contentsource.DeltaPage{}, errors.New("salesforce: DeltaList requires live mode")
	}

	page := contentsource.DeltaPage{}
	var latestModifiedAt time.Time

	for _, sobjectType := range s.live.sobjectTypes {
		soql := buildSOQL(sobjectType, sinceToken)
		reqURL := fmt.Sprintf("%s/services/data/%s/query?q=%s", s.live.baseURL, salesforceAPIVersion, url.QueryEscape(soql))

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return contentsource.DeltaPage{}, fmt.Errorf("salesforce: build delta request: %w", err)
		}
		if s.live.token != "" {
			req.Header.Set("Authorization", "Bearer "+s.live.token)
		}
		req.Header.Set("Accept", "application/json")

		resp, err := s.live.http.Do(req)
		if err != nil {
			return contentsource.DeltaPage{}, fmt.Errorf("salesforce: delta request for %s: %w", sobjectType, err)
		}

		body, err := io.ReadAll(io.LimitReader(resp.Body, content.MaxBodyBytes*16))
		_ = resp.Body.Close()
		if err != nil {
			return contentsource.DeltaPage{}, fmt.Errorf("salesforce: read delta body: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			return contentsource.DeltaPage{}, fmt.Errorf("salesforce: delta returned HTTP %d for %s", resp.StatusCode, sobjectType)
		}

		var qr sfQueryResult
		if err := json.Unmarshal(body, &qr); err != nil {
			return contentsource.DeltaPage{}, fmt.Errorf("salesforce: parse delta response: %w", err)
		}

		for _, r := range qr.Records {
			if strings.TrimSpace(r.ID) == "" {
				continue
			}
			modifiedAt := parseTime(r.SystemModstamp)
			if modifiedAt.After(latestModifiedAt) {
				latestModifiedAt = modifiedAt
			}
			page.Changes = append(page.Changes, contentsource.DeltaEntry{
				DocRef: contentsource.DocRef{
					DocID:       content.Truncate(r.ID, content.MaxRefLen),
					Title:       content.Truncate(r.Name, content.MaxTitleLen),
					ContentType: "text/plain",
					ModifiedAt:  modifiedAt,
				},
				ChangeKind: contentsource.ChangeContent,
			})
		}
	}

	if !latestModifiedAt.IsZero() {
		page.ResumeToken = latestModifiedAt.UTC().Format(time.RFC3339)
	}

	return page, nil
}

func (s *Source) listLive(ctx context.Context, cursor string) ([]contentsource.DocRef, string, error) {
	if s.live == nil {
		return nil, "", errors.New("salesforce: List requires live mode")
	}

	reqURL := strings.TrimSpace(cursor)
	if reqURL == "" {
		sobjectType := "Account"
		if len(s.live.sobjectTypes) > 0 {
			sobjectType = s.live.sobjectTypes[0]
		}
		soql := fmt.Sprintf("SELECT Id,Name,SystemModstamp FROM %s ORDER BY SystemModstamp ASC LIMIT 200", sobjectType)
		reqURL = fmt.Sprintf("%s/services/data/%s/query?q=%s", s.live.baseURL, salesforceAPIVersion, url.QueryEscape(soql))
	} else if strings.HasPrefix(reqURL, "/") {
		reqURL = s.live.baseURL + reqURL
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("salesforce: build list request: %w", err)
	}
	if s.live.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.live.token)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := s.live.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("salesforce: list request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("salesforce: list returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, content.MaxBodyBytes*16))
	if err != nil {
		return nil, "", fmt.Errorf("salesforce: read list body: %w", err)
	}
	var qr sfQueryResult
	if err := json.Unmarshal(body, &qr); err != nil {
		return nil, "", fmt.Errorf("salesforce: parse list response: %w", err)
	}
	refs := make([]contentsource.DocRef, 0, len(qr.Records))
	for _, r := range qr.Records {
		if strings.TrimSpace(r.ID) == "" {
			continue
		}
		refs = append(refs, contentsource.DocRef{
			DocID:       content.Truncate(r.ID, content.MaxRefLen),
			Title:       content.Truncate(r.Name, content.MaxTitleLen),
			ContentType: "application/json",
			ModifiedAt:  parseTime(r.SystemModstamp),
		})
	}
	next := strings.TrimSpace(qr.NextRecordsURL)
	return refs, next, nil
}

func (s *Source) fetchLive(ctx context.Context, docID string) (contentsource.Document, error) {
	if s.live == nil {
		return contentsource.Document{}, errors.New("salesforce: Fetch requires live mode")
	}
	if !validSalesforceID(docID) {
		return contentsource.Document{}, fmt.Errorf("salesforce: invalid record ID %q", docID)
	}

	for _, sobjectType := range s.live.sobjectTypes {
		reqURL := fmt.Sprintf(
			"%s/services/data/%s/sobjects/%s/%s",
			s.live.baseURL, salesforceAPIVersion, url.PathEscape(sobjectType), url.PathEscape(docID),
		)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return contentsource.Document{}, fmt.Errorf("salesforce: build fetch request: %w", err)
		}
		if s.live.token != "" {
			req.Header.Set("Authorization", "Bearer "+s.live.token)
		}
		req.Header.Set("Accept", "application/json")

		resp, err := s.live.http.Do(req)
		if err != nil {
			return contentsource.Document{}, fmt.Errorf("salesforce: fetch request: %w", err)
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, content.MaxBodyBytes))
		closeErr := resp.Body.Close()
		if readErr != nil {
			return contentsource.Document{}, fmt.Errorf("salesforce: read fetch body: %w", readErr)
		}
		if closeErr != nil {
			return contentsource.Document{}, fmt.Errorf("salesforce: close fetch body: %w", closeErr)
		}
		if resp.StatusCode == http.StatusNotFound {
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return contentsource.Document{}, fmt.Errorf("salesforce: fetch returned HTTP %d", resp.StatusCode)
		}
		var r sfRecord
		if err := json.Unmarshal(body, &r); err != nil {
			return contentsource.Document{}, fmt.Errorf("salesforce: parse fetch response: %w", err)
		}
		if strings.TrimSpace(r.ID) == "" {
			r.ID = docID
		}
		if strings.TrimSpace(r.Attributes.Type) == "" {
			r.Attributes.Type = sobjectType
		}
		var acl []string
		if ownerID := strings.TrimSpace(r.OwnerID); ownerID != "" {
			acl = append(acl, "owner:"+ownerID)
		}
		if sharing := strings.TrimSpace(r.SharingModel); sharing != "" {
			acl = append(acl, "sharing:"+sharing)
		}
		return contentsource.Document{
			Source:      contentsource.SourceSalesforce,
			DocID:       content.Truncate(docID, content.MaxRefLen),
			Title:       content.Truncate(r.Name, content.MaxTitleLen),
			Body:        string(body),
			ContentType: "application/json",
			ACL:         content.CleanACL(acl),
			SpaceRef:    "sobject:" + r.Attributes.Type,
			ModifiedAt:  parseTime(r.SystemModstamp),
			Attributes:  map[string]string{"sobject_type": r.Attributes.Type},
		}, nil
	}
	return contentsource.Document{}, fmt.Errorf("salesforce: record %s not found in any configured SObject type", docID)
}

// buildSOQL constructs the SOQL query for incremental or full sync of a given
// SObject type.
func buildSOQL(sobjectType, sinceToken string) string {
	q := fmt.Sprintf(
		"SELECT Id,Name,Description,SystemModstamp,OwnerId FROM %s",
		sobjectType,
	)
	if sinceToken != "" {
		q += fmt.Sprintf(" WHERE SystemModstamp > %s", sinceToken)
	}
	q += " ORDER BY SystemModstamp ASC LIMIT 200"
	return q
}

// validSalesforceID reports whether s looks like a valid Salesforce record ID
// (15 or 18 alphanumeric characters). Rejecting anything else prevents SOQL
// injection when the ID is used in query parameters or URL paths.
func validSalesforceID(s string) bool {
	n := len(s)
	if n != 15 && n != 18 {
		return false
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

// FetchACL queries Salesforce for the owner of the record identified by docID
// and returns an ACLResult with owner-based entries. It uses the sObject REST
// endpoint (not SOQL) to avoid injection risks.
func (s *Source) FetchACL(ctx context.Context, docID string) (contentsource.ACLResult, error) {
	if s.live == nil {
		return contentsource.ACLResult{}, errors.New("salesforce: FetchACL requires live mode")
	}
	if !validSalesforceID(docID) {
		return contentsource.ACLResult{}, fmt.Errorf("salesforce: invalid record ID %q", docID)
	}

	// Use the sObject row endpoint instead of SOQL to avoid injection.
	for _, sobjectType := range s.live.sobjectTypes {
		reqURL := fmt.Sprintf(
			"%s/services/data/%s/sobjects/%s/%s?fields=Id,OwnerId",
			s.live.baseURL, salesforceAPIVersion, url.PathEscape(sobjectType), url.PathEscape(docID),
		)

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return contentsource.ACLResult{}, fmt.Errorf("salesforce: build ACL request: %w", err)
		}
		if s.live.token != "" {
			req.Header.Set("Authorization", "Bearer "+s.live.token)
		}
		req.Header.Set("Accept", "application/json")

		resp, err := s.live.http.Do(req)
		if err != nil {
			return contentsource.ACLResult{}, fmt.Errorf("salesforce: ACL request: %w", err)
		}

		body, err := io.ReadAll(io.LimitReader(resp.Body, content.MaxBodyBytes))
		_ = resp.Body.Close()
		if err != nil {
			return contentsource.ACLResult{}, fmt.Errorf("salesforce: read ACL body: %w", err)
		}

		if resp.StatusCode == http.StatusNotFound {
			continue // not found in this type, try next
		}
		if resp.StatusCode != http.StatusOK {
			continue
		}

		var r sfRecord
		if err := json.Unmarshal(body, &r); err != nil {
			return contentsource.ACLResult{}, fmt.Errorf("salesforce: parse ACL response: %w", err)
		}

		var acl []string
		if ownerID := strings.TrimSpace(r.OwnerID); ownerID != "" {
			acl = append(acl, "owner:"+ownerID)
		}

		return contentsource.ACLResult{
			ACL: content.CleanACL(acl),
		}, nil
	}

	return contentsource.ACLResult{}, fmt.Errorf("salesforce: record %s not found in any configured SObject type", docID)
}
