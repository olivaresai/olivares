// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package bedrockkb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/awssig"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

const (
	// Name is the connector's globally unique dotted identifier.
	Name    = "olivares.bedrock-kb"
	version = "0.1.0"

	signingService = "bedrock-agent-runtime"

	defaultRegion     = "us-east-1"
	defaultTimeout    = 30 * time.Second
	defaultMaxResults = 5
	healthCheckQuery  = "connectivity check"
	maxResponseBytes  = 4 << 20 // 4 MiB
)

// Configuration keys.
const (
	cfgAccessKeyID     = "access_key_id"
	cfgSecretAccessKey = "secret_access_key"
	cfgSessionToken    = "session_token"
	cfgRegion          = "region"
	cfgKnowledgeBaseID = "knowledge_base_id"
	cfgMaxResults      = "max_results"
	cfgTimeout         = "timeout"
	cfgEndpoint        = "endpoint"
)

// Environment-variable fallbacks for credentials.
const (
	envAccessKeyID     = "AWS_ACCESS_KEY_ID"
	envSecretAccessKey = "AWS_SECRET_ACCESS_KEY"
	envSessionToken    = "AWS_SESSION_TOKEN"
)

type config struct {
	akid            string
	secret          string
	token           string
	region          string
	knowledgeBaseID string
	maxResults      int
	timeout         time.Duration
	endpoint        string
}

// Doer is the HTTP transport (tests inject a fake).
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Source is the Bedrock Knowledge Bases governance connector.
type Source struct {
	cfg  config
	doer Doer
}

var _ sdk.SourceConnector = (*Source)(nil)

func New() *Source { return &Source{} }

func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     version,
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "AWS Bedrock Knowledge Bases governance",
		Description: "Read-only observation of Bedrock Knowledge Bases retrieval: verifies KB connectivity, observes retrieval configuration and data sources, emits governance findings. Does NOT store or index documents — Bedrock KB manages its own vector store.",
		ConfigFields: []sdk.ConfigField{
			{Key: cfgAccessKeyID, Type: sdk.FieldString, Secret: true, Description: "AWS access key id (falls back to AWS_ACCESS_KEY_ID)"},
			{Key: cfgSecretAccessKey, Type: sdk.FieldString, Secret: true, Description: "AWS secret access key (falls back to AWS_SECRET_ACCESS_KEY)"},
			{Key: cfgSessionToken, Type: sdk.FieldString, Secret: true, Description: "optional STS session token (falls back to AWS_SESSION_TOKEN)"},
			{Key: cfgRegion, Type: sdk.FieldString, Default: defaultRegion, Description: "AWS region for Bedrock Agent Runtime"},
			{Key: cfgKnowledgeBaseID, Type: sdk.FieldString, Required: true, Description: "Bedrock Knowledge Base ID to observe"},
			{Key: cfgMaxResults, Type: sdk.FieldInt, Default: fmt.Sprintf("%d", defaultMaxResults), Description: "max retrieval results per health-check query"},
			{Key: cfgTimeout, Type: sdk.FieldDuration, Default: defaultTimeout.String(), Description: "per-request HTTP timeout"},
			{Key: cfgEndpoint, Type: sdk.FieldString, Description: "Bedrock Agent Runtime endpoint base URL (default https://bedrock-agent-runtime.<region>.amazonaws.com)"},
		},
	}
}

func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	c := config{
		akid:            firstNonEmpty(strings.TrimSpace(cfg.Get(cfgAccessKeyID)), strings.TrimSpace(os.Getenv(envAccessKeyID))),
		secret:          firstNonEmpty(cfg.Get(cfgSecretAccessKey), os.Getenv(envSecretAccessKey)),
		token:           firstNonEmpty(cfg.Get(cfgSessionToken), os.Getenv(envSessionToken)),
		region:          firstNonEmpty(strings.TrimSpace(cfg.Get(cfgRegion)), defaultRegion),
		knowledgeBaseID: strings.TrimSpace(cfg.Get(cfgKnowledgeBaseID)),
		maxResults:      cfg.GetInt(cfgMaxResults, defaultMaxResults),
		timeout:         cfg.GetDuration(cfgTimeout, defaultTimeout),
		endpoint:        strings.TrimSpace(cfg.Get(cfgEndpoint)),
	}
	if c.knowledgeBaseID == "" {
		return fmt.Errorf("bedrock-kb: %s is required", cfgKnowledgeBaseID)
	}
	if c.akid == "" || c.secret == "" {
		return fmt.Errorf("bedrock-kb: missing credentials (set %q/%q or %s/%s)",
			cfgAccessKeyID, cfgSecretAccessKey, envAccessKeyID, envSecretAccessKey)
	}
	if c.maxResults <= 0 {
		c.maxResults = defaultMaxResults
	}
	if c.timeout <= 0 {
		c.timeout = defaultTimeout
	}
	if c.endpoint == "" {
		c.endpoint = "https://bedrock-agent-runtime." + c.region + ".amazonaws.com"
	}
	s.cfg = c
	s.doer = &http.Client{Timeout: c.timeout}
	return nil
}

// Gather runs one observation pass: calls Retrieve on the configured KB with a
// health-check query, observes the results, and emits findings.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	at := time.Now().UTC()
	results, err := s.retrieve(ctx, healthCheckQuery)
	if err != nil {
		return sink.Emit(ctx, model.FindingReport{
			Kind:        "health",
			Severity:    model.SeverityMedium,
			SubjectKind: "bedrock.knowledge_base",
			SubjectRef:  s.cfg.knowledgeBaseID,
			Title:       "Bedrock KB retrieval health check failed",
			DetailHash:  hashStr(err.Error()),
			OccurredAt:  at,
		})
	}

	// Emit a health finding with the KB posture summary.
	summary := fmt.Sprintf("KB %s: %d results returned, %d unique sources",
		s.cfg.knowledgeBaseID, len(results), countUniqueSources(results))
	if err := sink.Emit(ctx, model.FindingReport{
		Kind:        "knowledge_retrieval",
		Severity:    model.SeverityInfo,
		SubjectKind: "bedrock.knowledge_base",
		SubjectRef:  s.cfg.knowledgeBaseID,
		Title:       summary,
		DetailHash:  hashStr(summary),
		OccurredAt:  at,
	}); err != nil {
		return err
	}

	// Emit an edge observation per unique data source discovered.
	sources := uniqueSources(results)
	for _, src := range sources {
		if err := sink.Emit(ctx, model.EdgeObservation{
			OriginKind:   "bedrock.knowledge_base",
			OriginRef:    s.cfg.knowledgeBaseID,
			ResourceKind: "knowledge_document",
			ResourceRef:  src,
			Mode:         "retrieval",
			ObservedAt:   at,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Source) Close(_ context.Context) error { return nil }

// SetDoer allows tests to inject a fake HTTP transport.
func (s *Source) SetDoer(d Doer) { s.doer = d }

// retrieveResult is one chunk from a Bedrock KB Retrieve response.
type retrieveResult struct {
	Content struct {
		Text string `json:"text"`
	} `json:"content"`
	Location struct {
		Type       string `json:"type"`
		S3Location *struct {
			URI string `json:"uri"`
		} `json:"s3Location"`
		WebLocation *struct {
			URL string `json:"url"`
		} `json:"webLocation"`
	} `json:"location"`
	Score float64 `json:"score"`
}

// retrieve calls the Bedrock Agent Runtime Retrieve API.
func (s *Source) retrieve(ctx context.Context, query string) ([]retrieveResult, error) {
	body := map[string]any{
		"retrievalQuery": map[string]any{
			"text": query,
		},
		"retrievalConfiguration": map[string]any{
			"vectorSearchConfiguration": map[string]any{
				"numberOfResults": s.cfg.maxResults,
			},
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	path := "/knowledgebases/" + s.cfg.knowledgeBaseID + "/retrieve"
	url := strings.TrimRight(s.cfg.endpoint, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	awssig.Sign(req, raw, signingService, s.cfg.region,
		awssig.Creds{AKID: s.cfg.akid, Secret: s.cfg.secret, Token: s.cfg.token},
		time.Now())

	resp, err := s.doer.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bedrock-kb: retrieve: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("bedrock-kb: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bedrock-kb: retrieve returned status %d", resp.StatusCode)
	}
	var out struct {
		RetrievalResults []retrieveResult `json:"retrievalResults"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("bedrock-kb: decode response: %w", err)
	}
	return out.RetrievalResults, nil
}

func countUniqueSources(results []retrieveResult) int {
	return len(uniqueSources(results))
}

func uniqueSources(results []retrieveResult) []string {
	seen := make(map[string]bool)
	var out []string
	for _, r := range results {
		src := sourceRef(r)
		if src != "" && !seen[src] {
			seen[src] = true
			out = append(out, src)
		}
	}
	return out
}

func sourceRef(r retrieveResult) string {
	switch r.Location.Type {
	case "S3":
		if r.Location.S3Location != nil {
			return r.Location.S3Location.URI
		}
	case "WEB":
		if r.Location.WebLocation != nil {
			return r.Location.WebLocation.URL
		}
	}
	return ""
}

func hashStr(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
