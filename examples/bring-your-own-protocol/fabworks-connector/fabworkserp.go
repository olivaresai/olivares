// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package fabworkserp is an Olivares AI content-source connector. It serves
// documents and ACL references to governed knowledge through sdk.ContentSource.
// The contract is pull-based: Open configures once, List enumerates cheap
// document refs, Fetch returns one document body, and Close releases resources.
//
// ACL values are permission references only, never credentials. An empty ACL
// means the document inherits the knowledge base default. The engine owns
// retrieval governance, ACL intersection, redaction and indexing.
//
// A connector imports ONLY the Apache-2.0 SDK, never the upstream AGPL
// engine; scripts/check-boundary.sh enforces that boundary on the real build
// graph — run it in your CI.
package fabworkserp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk"
)

// Name is the connector's globally unique dotted identifier
// ("<vendor>.<connector>") — the value the engine registers it under.
const Name = "acme.fabworks-erp"

type backend interface {
	List(ctx context.Context, cursor string) ([]sdk.DocRef, string, error)
	Fetch(ctx context.Context, docID string) (sdk.Document, error)
}

type liveBackend interface {
	DeltaList(ctx context.Context, sinceToken string) (sdk.DeltaPage, error)
	FetchACL(ctx context.Context, docID string) (sdk.ACLResult, error)
}

// ContentSource is the connector. It holds the configuration Open resolved and
// a backend client. The generated scaffold used an in-memory backend; this
// example fills that seam with the fictional FabWorks REST-ish protocol client.
type ContentSource struct {
	baseURL string
	token   string
	backend backend
}

// Compile-time proof that ContentSource satisfies the SDK contracts.
var (
	_ sdk.ContentSource      = (*ContentSource)(nil)
	_ sdk.DeltaContentSource = (*ContentSource)(nil)
)

// New returns the connector with default configuration.
func New() *ContentSource { return &ContentSource{} }

// newWithBackend is used by tests to exercise the lifecycle against an
// in-memory fake backend.
func newWithBackend(b backend) *ContentSource { return &ContentSource{backend: b} }

// Descriptor returns the connector's stable self-description and declares the
// advisory governance surface this archetype materializes. Surfaces are for
// humans, catalogs and admission UIs; the engine enforces by configured source
// identity, not by trusting this metadata.
func (s *ContentSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeContentSource,
		Title:       "FabWorks ERP",
		Description: "Pulls governed documents from the fictional FabWorks ERP protocol.",
		Surfaces: []string{
			"knowledge.document",
		},
		ConfigFields: []sdk.ConfigField{
			{Key: "base_url", Type: sdk.FieldString, Required: true, Description: "FabWorks ERP protocol base URL."},
			{Key: "token", Type: sdk.FieldString, Secret: true, Description: "FabWorks API credential reference."},
		},
	}
}

// Open resolves configuration once, before List/Fetch. A configuration error
// belongs here so the engine can fail wiring before a sync starts.
func (s *ContentSource) Open(_ context.Context, cfg sdk.Config) error {
	s.baseURL = strings.TrimRight(cfg.Get("base_url"), "/")
	if s.baseURL == "" {
		return fmt.Errorf("base_url is required")
	}
	if _, err := url.ParseRequestURI(s.baseURL); err != nil {
		return fmt.Errorf("base_url is invalid: %w", err)
	}
	s.token = cfg.Get("token")
	if s.backend == nil {
		// FABWORKS-FILL START: generated connector seam filled with protocol client.
		s.backend = &erpClient{
			baseURL: s.baseURL,
			token:   s.token,
			client:  http.DefaultClient,
		}
		// FABWORKS-FILL END
	}
	return nil
}

// List returns lightweight document refs cheap enough to enumerate. It carries
// no body content; Fetch returns a body for one docID.
func (s *ContentSource) List(ctx context.Context, cursor string) ([]sdk.DocRef, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if s.backend == nil {
		return nil, "", fmt.Errorf("content source is not open")
	}
	return s.backend.List(ctx, cursor)
}

// Fetch returns one document's body plus provenance and source permission
// references. ACL values are references, never credential material.
func (s *ContentSource) Fetch(ctx context.Context, docID string) (sdk.Document, error) {
	if err := ctx.Err(); err != nil {
		return sdk.Document{}, err
	}
	if s.backend == nil {
		return sdk.Document{}, fmt.Errorf("content source is not open")
	}
	return s.backend.Fetch(ctx, docID)
}

// DeltaList returns live changes for the optional content.delta capability.
func (s *ContentSource) DeltaList(ctx context.Context, sinceToken string) (sdk.DeltaPage, error) {
	if err := ctx.Err(); err != nil {
		return sdk.DeltaPage{}, err
	}
	live, ok := s.backend.(liveBackend)
	if !ok {
		return sdk.DeltaPage{}, fmt.Errorf("content source backend does not support deltas")
	}
	return live.DeltaList(ctx, sinceToken)
}

// FetchACL refreshes one document's source ACL without fetching the body.
func (s *ContentSource) FetchACL(ctx context.Context, docID string) (sdk.ACLResult, error) {
	if err := ctx.Err(); err != nil {
		return sdk.ACLResult{}, err
	}
	live, ok := s.backend.(liveBackend)
	if !ok {
		return sdk.ACLResult{}, fmt.Errorf("content source backend does not support ACL refresh")
	}
	return live.FetchACL(ctx, docID)
}

// Close releases connector resources and must be safe to call even if Open
// failed.
func (s *ContentSource) Close(context.Context) error { return nil }

// FABWORKS-FILL START: invented FabWorks ERP JSON protocol adapter.

type erpClient struct {
	baseURL string
	token   string
	client  *http.Client
}

type docListResponse struct {
	Documents  []erpDocument `json:"documents"`
	NextCursor string        `json:"next_cursor"`
}

type changeResponse struct {
	Changes     []erpChange `json:"changes"`
	NextCursor  string      `json:"next_cursor"`
	ResumeToken string      `json:"resume_token"`
	Expired     bool        `json:"expired"`
}

type erpDocument struct {
	ID             string            `json:"id"`
	Title          string            `json:"title"`
	Body           string            `json:"body"`
	ContentType    string            `json:"content_type"`
	URI            string            `json:"uri"`
	ACLRefs        []string          `json:"acl_refs"`
	ModifiedAt     string            `json:"modified_at"`
	Metadata       map[string]string `json:"metadata"`
	ExternalLabels []string          `json:"external_labels"`
	Classification string            `json:"classification"`
	SpaceRef       string            `json:"space_ref"`
}

type erpACLResponse struct {
	ACLRefs        []string `json:"acl_refs"`
	ExternalLabels []string `json:"external_labels"`
	Classification string   `json:"classification"`
}

type erpChange struct {
	Kind        string `json:"kind"`
	DocID       string `json:"doc_id"`
	Title       string `json:"title"`
	ContentType string `json:"content_type"`
	ModifiedAt  string `json:"modified_at"`
}

func (c *erpClient) List(ctx context.Context, cursor string) ([]sdk.DocRef, string, error) {
	var resp docListResponse
	if err := c.get(ctx, "/fw/v1/documents", map[string]string{"cursor": cursor}, &resp); err != nil {
		return nil, "", err
	}
	refs := make([]sdk.DocRef, 0, len(resp.Documents))
	for _, d := range resp.Documents {
		refs = append(refs, sdk.DocRef{
			DocID:       d.ID,
			Title:       d.Title,
			ContentType: d.ContentType,
			ModifiedAt:  parseTime(d.ModifiedAt),
		})
	}
	return refs, resp.NextCursor, nil
}

func (c *erpClient) Fetch(ctx context.Context, docID string) (sdk.Document, error) {
	var d erpDocument
	if err := c.get(ctx, "/fw/v1/documents/"+url.PathEscape(docID), nil, &d); err != nil {
		return sdk.Document{}, err
	}
	attrs := cloneMap(d.Metadata)
	if d.URI != "" {
		attrs["uri"] = d.URI
	}
	return sdk.Document{
		Source:         sdk.SourceKind(Name),
		DocID:          d.ID,
		Title:          d.Title,
		Body:           []byte(d.Body),
		ContentType:    d.ContentType,
		ACL:            append([]string(nil), d.ACLRefs...),
		Classification: d.Classification,
		SpaceRef:       d.SpaceRef,
		ModifiedAt:     parseTime(d.ModifiedAt),
		Attributes:     attrs,
		ExternalLabels: append([]string(nil), d.ExternalLabels...),
	}, nil
}

func (c *erpClient) DeltaList(ctx context.Context, sinceToken string) (sdk.DeltaPage, error) {
	var resp changeResponse
	if err := c.get(ctx, "/fw/v1/changes", map[string]string{"cursor": sinceToken}, &resp); err != nil {
		return sdk.DeltaPage{}, err
	}
	changes := make([]sdk.Change, 0, len(resp.Changes))
	for _, ch := range resp.Changes {
		changes = append(changes, sdk.Change{
			DocRef: sdk.DocRef{
				DocID:       ch.DocID,
				Title:       ch.Title,
				ContentType: ch.ContentType,
				ModifiedAt:  parseTime(ch.ModifiedAt),
			},
			ChangeKind: sdk.ChangeKind(ch.Kind),
		})
	}
	return sdk.DeltaPage{
		Changes:     changes,
		NextToken:   resp.NextCursor,
		ResumeToken: resp.ResumeToken,
		Expired:     resp.Expired,
	}, nil
}

func (c *erpClient) FetchACL(ctx context.Context, docID string) (sdk.ACLResult, error) {
	var resp erpACLResponse
	if err := c.get(ctx, "/fw/v1/documents/"+url.PathEscape(docID)+"/acl", nil, &resp); err != nil {
		return sdk.ACLResult{}, err
	}
	return sdk.ACLResult{
		ACL:            append([]string(nil), resp.ACLRefs...),
		ExternalLabels: append([]string(nil), resp.ExternalLabels...),
		Classification: resp.Classification,
	}, nil
}

func (c *erpClient) get(ctx context.Context, endpoint string, query map[string]string, out any) error {
	u, err := url.Parse(c.baseURL + endpoint)
	if err != nil {
		return err
	}
	q := u.Query()
	for k, v := range query {
		if v != "" {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fabworks protocol %s returned HTTP %d", endpoint, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode FabWorks response: %w", err)
	}
	return nil
}

func parseTime(v string) time.Time {
	if v == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func cloneMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in)+1)
	for k, v := range in {
		out[k] = v
	}
	return out
}

// FABWORKS-FILL END
