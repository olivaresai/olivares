// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package claudebatch is the Olivares AI read-only governance connector for the
// Anthropic Message Batches API and Files API. It inventories active batches
// (status, model, request counts, creator), tracks uploaded files (size, purpose,
// PII-in-filename scan), enforces operator-declared batch policy (allowed models,
// line limits, allowed creators), and signals upload retention expiry (TTL-based).
//
// READ-ONLY AND MINIMAL-DATA (docs/SECURITY-HARDENING.md-3): every call is a GET via the shared
// GET-only modelprovider client. The connector never reads batch request payloads,
// file content, or secrets — only inventory metadata (ids, status, counts, sizes,
// timestamps). It never creates, cancels, or deletes batches or files.
//
// It imports only the SDK and the Apache modelprovider/redact contracts, never the
// engine.
package claudebatch

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.claude-batch"

const (
	defaultBaseURL          = "https://api.anthropic.com"
	defaultAnthropicVersion = "2023-06-01"
	defaultMaxPages         = 20
	defaultPageLimit        = "100"
	defaultUploadTTL        = 30 * 24 * time.Hour // 30 days

	batchesPath = "/v1/messages/batches"
	filesPath   = "/v1/files"
)

// Finding kinds — each stream uses a distinct Kind so consumers can filter.
const (
	findingKindBatchInventory   = "batch_inventory"
	findingKindFileInventory    = "file_inventory"
	findingKindPolicyViolation  = "policy_violation"
	findingKindRetentionExpired = "retention_expired"
	findingKindPosture          = "posture"
)

// Source is the Batch/Files governance connector. It satisfies sdk.SourceConnector.
type Source struct {
	adminKey  string
	baseURL   string
	version   string
	orgRef    string
	maxPages  int
	uploadTTL time.Duration
	policy    *batchPolicy

	client *modelprovider.Client
	doer   modelprovider.Doer
	now    func() time.Time
}

var _ sdk.SourceConnector = (*Source)(nil)

// New returns a Batch/Files governance connector with default configuration.
func New() *Source {
	return &Source{
		baseURL:   defaultBaseURL,
		version:   defaultAnthropicVersion,
		orgRef:    "anthropic-org",
		maxPages:  defaultMaxPages,
		uploadTTL: defaultUploadTTL,
	}
}

// SetTestTransport injects a custom HTTP transport and clock for tests.
func (s *Source) SetTestTransport(doer modelprovider.Doer) {
	s.doer = doer
	s.now = func() time.Time { return time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC) }
}

// Descriptor returns the connector's self-description and declared configuration.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Claude Batch & Files Governance",
		Description: "Read-only: inventories Anthropic Message Batches and Files API uploads, enforces operator-declared batch policy (allowed models, line limits, allowed creators), and signals upload retention expiry. Never reads batch payloads or file content (minimal-data).",
		ConfigFields: []sdk.ConfigField{
			{Key: "admin_key", Type: sdk.FieldString, Secret: true, Description: "Anthropic Admin API key reference (read-only; never persisted). Empty = offline (no inventory emitted)."},
			{Key: "base_url", Type: sdk.FieldString, Default: defaultBaseURL, Description: "Anthropic API base URL."},
			{Key: "anthropic_version", Type: sdk.FieldString, Default: defaultAnthropicVersion, Description: "anthropic-version header value."},
			{Key: "org_ref", Type: sdk.FieldString, Default: "anthropic-org", Description: "Stable reference for the governed org (the finding subject)."},
			{Key: "max_pages", Type: sdk.FieldInt, Default: strconv.Itoa(defaultMaxPages), Description: "Pagination safety bound per gather."},
			{Key: "upload_ttl", Type: sdk.FieldDuration, Default: "720h", Description: "TTL for uploaded files. Files older than this emit a retention-expired finding. Default 30 days."},
			{Key: "batch_policy", Type: sdk.FieldString, Default: "", Description: "JSON object with batch governance policy: {\"allowed_models\":[\"claude-opus-4-8\"],\"max_lines\":10000,\"allowed_creators\":[\"user_abc\"]}. Empty = no policy enforcement."},
		},
	}
}

// Open reads configuration and builds the read-only client.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.adminKey = cfg.Get("admin_key")
	if v := cfg.Get("base_url"); v != "" {
		s.baseURL = v
	}
	if v := cfg.Get("anthropic_version"); v != "" {
		s.version = v
	}
	if v := cfg.Get("org_ref"); v != "" {
		s.orgRef = v
	}
	s.maxPages = cfg.GetInt("max_pages", s.maxPages)
	s.uploadTTL = cfg.GetDuration("upload_ttl", s.uploadTTL)

	pol, err := parsePolicy(cfg.Get("batch_policy"))
	if err != nil {
		return fmt.Errorf("claudebatch: malformed batch_policy: %w", err)
	}
	s.policy = pol

	if s.adminKey != "" {
		s.client = modelprovider.NewClient(s.baseURL, s.doer, modelprovider.AuthAnthropicKey, s.adminKey,
			map[string]string{"anthropic-version": s.version})
	}
	return nil
}

// Gather runs the five governance streams. With no admin credential it emits a
// single posture finding and returns.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.client == nil {
		return sink.Emit(ctx, s.offlineFinding(s.clock().UTC()))
	}

	batches, err := s.fetchBatches(ctx)
	if err != nil {
		return err
	}
	if err := s.gatherBatchInventory(ctx, sink, batches); err != nil {
		return err
	}
	if s.policy != nil {
		if err := s.gatherBatchPolicy(ctx, sink, batches); err != nil {
			return err
		}
	}

	files, err := s.fetchFiles(ctx)
	if err != nil {
		return err
	}
	if err := s.gatherFileInventory(ctx, sink, files); err != nil {
		return err
	}
	if err := s.gatherRetention(ctx, sink, files); err != nil {
		return err
	}

	return s.gatherCompliancePosture(ctx, sink, len(batches), len(files))
}

// Close releases resources; the connector holds none.
func (s *Source) Close(context.Context) error { return nil }

// fetchBatches paginates GET /v1/messages/batches and returns all entries.
func (s *Source) fetchBatches(ctx context.Context) ([]batchEntry, error) {
	var all []batchEntry
	afterID := ""
	for i := 0; i < s.maxPages; i++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var resp batchListResponse
		q := url.Values{"limit": {defaultPageLimit}}
		if afterID != "" {
			q.Set("after_id", afterID)
		}
		if err := s.client.GetJSON(ctx, batchesPath, q, &resp); err != nil {
			return nil, err
		}
		all = append(all, resp.Data...)
		if !resp.HasMore || resp.LastID == "" {
			break
		}
		afterID = resp.LastID
	}
	return all, nil
}

// fetchFiles paginates GET /v1/files and returns all entries.
func (s *Source) fetchFiles(ctx context.Context) ([]fileEntry, error) {
	var all []fileEntry
	afterID := ""
	for i := 0; i < s.maxPages; i++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var resp fileListResponse
		q := url.Values{"limit": {defaultPageLimit}}
		if afterID != "" {
			q.Set("after_id", afterID)
		}
		if err := s.client.GetJSON(ctx, filesPath, q, &resp); err != nil {
			return nil, err
		}
		all = append(all, resp.Data...)
		if !resp.HasMore || resp.LastID == "" {
			break
		}
		afterID = resp.LastID
	}
	return all, nil
}

// gatherBatchInventory emits one FindingReport per batch (kind=batch_inventory).
func (s *Source) gatherBatchInventory(ctx context.Context, sink sdk.Sink, batches []batchEntry) error {
	for _, b := range batches {
		if b.ID == "" {
			continue
		}
		f := model.FindingReport{
			Kind:        findingKindBatchInventory,
			Severity:    model.SeverityInfo,
			SubjectKind: "claude_batch",
			SubjectRef:  b.ID,
			Title:       "Batch " + b.ID + " [" + b.ProcessingStatus + "] — " + strconv.FormatInt(b.RequestCounts.totalLines(), 10) + " lines",
			DetailHash:  redact.Hash(strings.Join([]string{b.ID, b.ProcessingStatus, b.CreatedAt, b.EndedAt}, "|")),
			OccurredAt:  parseTime(b.CreatedAt, s.clock()),
		}
		if err := sink.Emit(ctx, f); err != nil {
			return err
		}
	}
	return nil
}

// gatherFileInventory emits one FindingReport per file. Filenames are scanned for
// PII/secret shapes via redact.ContainsSecret; a hit elevates severity to Medium.
func (s *Source) gatherFileInventory(ctx context.Context, sink sdk.Sink, files []fileEntry) error {
	for _, fi := range files {
		if fi.ID == "" {
			continue
		}
		sev := model.SeverityInfo
		title := "File " + fi.ID + " [" + fi.Purpose + "] " + redact.Clean(fi.Filename) + " (" + formatBytes(fi.SizeBytes) + ")"
		if redact.ContainsSecret(fi.Filename) {
			sev = model.SeverityMedium
			title = "File " + fi.ID + " — filename contains possible secret/PII: " + redact.Clean(fi.Filename)
		}
		f := model.FindingReport{
			Kind:        findingKindFileInventory,
			Severity:    sev,
			SubjectKind: "claude_file",
			SubjectRef:  fi.ID,
			Title:       title,
			DetailHash:  redact.Hash(strings.Join([]string{fi.ID, fi.Filename, fi.Purpose, fi.CreatedAt, strconv.FormatInt(fi.SizeBytes, 10)}, "|")),
			OccurredAt:  parseTime(fi.CreatedAt, s.clock()),
		}
		if err := sink.Emit(ctx, f); err != nil {
			return err
		}
	}
	return nil
}

// gatherCompliancePosture emits a posture finding noting batch/file governance
// coverage and whether the Compliance Activity Feed can correlate.
func (s *Source) gatherCompliancePosture(ctx context.Context, sink sdk.Sink, batchCount, fileCount int) error {
	at := s.clock().UTC()
	title := fmt.Sprintf(
		"Batch/Files governance: %d batches, %d files inventoried. "+
			"Compliance Activity Feed (claude-compliance connector) covers "+
			"batch/file lifecycle events when configured — correlate by subject ref.",
		batchCount, fileCount)
	return sink.Emit(ctx, model.FindingReport{
		Kind:        findingKindPosture,
		Severity:    model.SeverityInfo,
		SubjectKind: "claude_batch_files",
		SubjectRef:  s.orgRef,
		Title:       title,
		DetailHash:  redact.Hash(s.orgRef + "|posture|" + at.Format(time.RFC3339)),
		OccurredAt:  at,
	})
}

// offlineFinding is emitted when no admin_key is configured.
func (s *Source) offlineFinding(at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        findingKindPosture,
		Severity:    model.SeverityLow,
		SubjectKind: "claude_batch_files",
		SubjectRef:  s.orgRef,
		Title: "Batch/Files governance connector offline: no admin_key configured. " +
			"Batch inventory, file tracking, policy enforcement and retention checks are unavailable.",
		DetailHash: redact.Hash(s.orgRef + "|offline|batch-files"),
		OccurredAt: at,
	}
}

// clock returns the connector's time source (injectable for tests).
func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// parseTime parses an RFC3339 timestamp, falling back to the given fallback so a
// missing/odd timestamp never aborts a gather.
func parseTime(raw string, fallback time.Time) time.Time {
	if raw == "" {
		return fallback
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return fallback
	}
	return t.UTC()
}

// formatBytes returns a human-readable byte size.
func formatBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(b)/float64(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(b)/float64(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(b)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
