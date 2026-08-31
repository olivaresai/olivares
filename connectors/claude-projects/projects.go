// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package claudeprojects is the Olivares AI read-only governance connector for
// Anthropic Organization Projects. It inventories projects, their members and
// project-scoped API keys via the Admin API, emitting PERMITTED access edges and
// governance posture findings that the engine appends to the tamper-evident audit
// ledger.
//
// READ-ONLY BY CONSTRUCTION (docs/SECURITY-HARDENING.md-3). Every call is a GET via the shared
// GET-only modelprovider client, so this connector CANNOT create, modify or delete
// projects — it observes and governs.
//
// Knowledge files, custom instructions and artifacts are NOT available through the
// Admin API (Anthropic does not expose these endpoints). This is documented honestly
// as a coverage gap. Artifact lifecycle tracking is derived from Compliance API
// activity events when the companion claude-compliance connector is configured.
//
// Credential model: the connector requires an Admin API key (sk-ant-admin01-) with
// at least the organizations.read scope. It is configured independently from the
// claude-api inference connector (single responsibility, separate failure domain).
//
// Minimal data (docs/SECURITY-HARDENING.md): findings carry project references (id/name), member
// user IDs (never email/PII), and API key IDs (never the secret). Hashed detail
// for tamper-evident audit.
package claudeprojects

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
const Name = "olivares.claude-projects"

const (
	defaultBaseURL          = "https://api.anthropic.com"
	defaultAnthropicVersion = "2023-06-01"
	defaultOrgRef           = "anthropic-org"
	defaultMaxPages         = 50
	defaultPageLimit        = "100"

	resProject  = "anthropic.project"
	resMember   = "anthropic.project_member"
	resAPIKey   = "anthropic.project_api_key"
	resArtifact = "anthropic.artifact"
)

// Source is the Projects governance connector. It satisfies sdk.SourceConnector:
// Gather inventories projects, members and API keys from the Admin API and emits
// access edges + posture findings to the sink.
type Source struct {
	apiKey  string
	orgID   string
	orgRef  string
	baseURL string
	version string

	maxPages int

	policy *PolicyConfig

	client *modelprovider.Client
	doer   modelprovider.Doer
	now    func() time.Time
}

var _ sdk.SourceConnector = (*Source)(nil)

// New returns a Projects connector with default configuration.
func New() *Source {
	return &Source{
		baseURL:  defaultBaseURL,
		version:  defaultAnthropicVersion,
		orgRef:   defaultOrgRef,
		maxPages: defaultMaxPages,
	}
}

// SetTestTransport injects a custom HTTP transport and clock for tests.
func (s *Source) SetTestTransport(doer modelprovider.Doer) {
	s.doer = doer
	s.now = func() time.Time { return time.Date(2026, 6, 27, 0, 0, 0, 0, time.UTC) }
}

// Descriptor returns the connector's self-description and declared configuration.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:       Name,
		Version:    "0.1.0",
		APIVersion: sdk.APIVersion,
		Type:       sdk.TypeSource,
		Title:      "Claude Projects governance (Admin API inventory)",
		Description: "Read-only: inventories Anthropic Organization Projects (name, membership, " +
			"project-scoped API keys) via the Admin API and emits PERMITTED access edges " +
			"(project→member, project→api_key) plus governance posture findings (stale/archived " +
			"projects, naming violations, membership anomalies) to the tamper-evident ledger. " +
			"Evaluates operator-configurable policy rules (forbidden instruction patterns, " +
			"knowledge limits, naming conventions). " +
			"COVERAGE GAP: knowledge files, custom instructions and artifacts are NOT available " +
			"through the Admin API (Anthropic does not expose these endpoints) — artifact lifecycle " +
			"is tracked via Compliance API activity events when the companion claude-compliance " +
			"connector is configured. " +
			"Requires an Admin API key (sk-ant-admin01-) with organizations.read scope. " +
			"Rate limits shared with the Admin API (600 req/min per org).",
		ConfigFields: []sdk.ConfigField{
			{Key: "api_key", Type: sdk.FieldString, Secret: true, Description: "Anthropic Admin API key (sk-ant-admin01- with organizations.read scope). Empty = offline (no project evidence emitted)."},
			{Key: "organization_id", Type: sdk.FieldString, Description: "Anthropic organization ID to inventory. Required when api_key is set."},
			{Key: "base_url", Type: sdk.FieldString, Default: defaultBaseURL, Description: "Anthropic API base URL."},
			{Key: "anthropic_version", Type: sdk.FieldString, Default: defaultAnthropicVersion, Description: "anthropic-version header value."},
			{Key: "org_ref", Type: sdk.FieldString, Default: defaultOrgRef, Description: "Stable reference for the governed org (the evidence subject)."},
			{Key: "max_pages", Type: sdk.FieldInt, Default: strconv.Itoa(defaultMaxPages), Description: "Pagination safety bound per endpoint."},
			{Key: "policy", Type: sdk.FieldString, Description: "JSON policy configuration (forbidden patterns, limits). Empty = no policy evaluation."},
		},
	}
}

// Open reads configuration and, when an API key is present, builds the read-only
// Admin API client.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.apiKey = cfg.Get("api_key")
	s.orgID = strings.TrimSpace(cfg.Get("organization_id"))
	if b := strings.TrimRight(cfg.Get("base_url"), "/"); b != "" {
		s.baseURL = b
	}
	if v := cfg.Get("anthropic_version"); v != "" {
		s.version = v
	}
	if o := strings.TrimSpace(cfg.Get("org_ref")); o != "" {
		s.orgRef = o
	}
	s.maxPages = cfg.GetInt("max_pages", s.maxPages)
	if s.maxPages <= 0 {
		s.maxPages = defaultMaxPages
	}

	if polStr := cfg.Get("policy"); strings.TrimSpace(polStr) != "" {
		pol, err := parsePolicy(polStr)
		if err != nil {
			return fmt.Errorf("claudeprojects: invalid policy config: %w", err)
		}
		s.policy = pol
	}

	if s.apiKey != "" {
		s.client = modelprovider.NewClient(s.baseURL, s.doer, modelprovider.AuthAnthropicKey, s.apiKey,
			map[string]string{"anthropic-version": s.version})
	}
	return nil
}

func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// Gather inventories projects, members and API keys, emitting edges and findings.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.client == nil {
		return nil
	}
	if s.orgID == "" {
		return sink.Emit(ctx, model.FindingReport{
			Kind:        "posture",
			Severity:    model.SeverityMedium,
			SubjectKind: resProject,
			SubjectRef:  s.orgRef,
			Title:       "Projects connector has an API key but no organization_id configured — project inventory is disabled",
			DetailHash:  redact.Hash(s.orgRef + "|missing-org-id"),
			OccurredAt:  s.clock().UTC(),
		})
	}

	if err := s.gatherCoverageGaps(ctx, sink); err != nil {
		return err
	}

	return s.gatherProjects(ctx, sink)
}

func (s *Source) gatherCoverageGaps(ctx context.Context, sink sdk.Sink) error {
	return sink.Emit(ctx, model.FindingReport{
		Kind:        "posture",
		Severity:    model.SeverityInfo,
		SubjectKind: resProject,
		SubjectRef:  s.orgRef,
		Title: "Claude Projects governance covers project inventory (name/membership/API keys) via the Admin API. " +
			"3 known gaps: (1) knowledge files/custom instructions NOT available via Admin API; " +
			"(2) artifact content/lifecycle NOT available via Admin API — tracked via Compliance API activity events; " +
			"(3) project-level settings/permissions NOT available via Admin API.",
		DetailHash: redact.Hash(s.orgRef + "|coverage|knowledge=off;artifacts=activity-only;settings=off"),
		OccurredAt: s.clock().UTC(),
	})
}

func (s *Source) gatherProjects(ctx context.Context, sink sdk.Sink) error {
	now := s.clock().UTC()
	afterID := ""

	for page := 0; page < s.maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		var resp projectsResponse
		if err := s.getProjects(ctx, afterID, &resp); err != nil {
			return err
		}

		for _, p := range resp.Data {
			if p.ID == "" {
				continue
			}

			if err := s.processProject(ctx, sink, p, now); err != nil {
				return err
			}
		}

		if !resp.HasMore || resp.LastID == "" {
			break
		}
		afterID = resp.LastID
	}
	return nil
}

func (s *Source) processProject(ctx context.Context, sink sdk.Sink, p project, now time.Time) error {
	if err := sink.Emit(ctx, model.FindingReport{
		Kind:        "inventory",
		Severity:    model.SeverityInfo,
		SubjectKind: resProject,
		SubjectRef:  p.ID,
		Title:       "Project inventoried: " + sanitizeName(p.Name),
		DetailHash:  redact.Hash(p.ID + "|" + p.Name + "|" + p.CreatedAt + "|archived=" + p.ArchivedAt),
		OccurredAt:  now,
	}); err != nil {
		return err
	}

	if p.ArchivedAt != "" {
		if err := sink.Emit(ctx, model.FindingReport{
			Kind:        "posture",
			Severity:    model.SeverityLow,
			SubjectKind: resProject,
			SubjectRef:  p.ID,
			Title:       "Project is archived: " + sanitizeName(p.Name),
			DetailHash:  redact.Hash(p.ID + "|archived|" + p.ArchivedAt),
			OccurredAt:  now,
		}); err != nil {
			return err
		}
	}

	if s.policy != nil {
		if err := s.evaluateProjectPolicy(ctx, sink, p, now); err != nil {
			return err
		}
	}

	if err := s.gatherMembers(ctx, sink, p.ID, now); err != nil {
		return err
	}

	return s.gatherAPIKeys(ctx, sink, p.ID, now)
}

func (s *Source) gatherMembers(ctx context.Context, sink sdk.Sink, projectID string, now time.Time) error {
	afterID := ""
	for page := 0; page < s.maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		var resp membersResponse
		if err := s.getMembersPage(ctx, projectID, afterID, &resp); err != nil {
			return err
		}

		for _, m := range resp.Data {
			if m.UserID == "" {
				continue
			}
			mode := model.ModeRead
			if m.Role == "admin" || m.Role == "developer" {
				mode = model.ModeReadWrite
			}
			if err := sink.Emit(ctx, model.EdgeObservation{
				OriginKind:   resProject,
				OriginRef:    projectID,
				ResourceKind: resMember,
				ResourceRef:  m.UserID,
				Mode:         mode,
				Source:       model.SignalPolicy,
				Confidence:   model.ConfidenceAttributed,
				ToolRef:      m.Role,
				ObservedAt:   now,
			}); err != nil {
				return err
			}
		}

		if !resp.HasMore || resp.LastID == "" {
			break
		}
		afterID = resp.LastID
	}
	return nil
}

func (s *Source) gatherAPIKeys(ctx context.Context, sink sdk.Sink, projectID string, now time.Time) error {
	afterID := ""
	for page := 0; page < s.maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		var resp apiKeysResponse
		if err := s.getAPIKeysPage(ctx, projectID, afterID, &resp); err != nil {
			return err
		}

		for _, k := range resp.Data {
			if k.ID == "" {
				continue
			}
			if err := sink.Emit(ctx, model.EdgeObservation{
				OriginKind:   resProject,
				OriginRef:    projectID,
				ResourceKind: resAPIKey,
				ResourceRef:  k.ID,
				Mode:         model.ModeUnknown,
				Source:       model.SignalPolicy,
				Confidence:   model.ConfidenceAttributed,
				ToolRef:      k.Status,
				ObservedAt:   now,
			}); err != nil {
				return err
			}

			if k.Status == "active" {
				if err := sink.Emit(ctx, model.FindingReport{
					Kind:        "inventory",
					Severity:    model.SeverityInfo,
					SubjectKind: resAPIKey,
					SubjectRef:  k.ID,
					Title:       "Active project API key: " + sanitizeName(k.Name),
					DetailHash:  redact.Hash(projectID + "|" + k.ID + "|" + k.Name + "|active"),
					OccurredAt:  now,
				}); err != nil {
					return err
				}
			}
		}

		if !resp.HasMore || resp.LastID == "" {
			break
		}
		afterID = resp.LastID
	}
	return nil
}

// Close releases resources; the connector holds no long-lived connection.
func (s *Source) Close(_ context.Context) error { return nil }

// --- HTTP helpers ---

func (s *Source) getProjects(ctx context.Context, afterID string, out *projectsResponse) error {
	p := fmt.Sprintf("/v1/organizations/%s/projects", url.PathEscape(s.orgID))
	q := url.Values{"limit": {defaultPageLimit}}
	if afterID != "" {
		q.Set("after_id", afterID)
	}
	return s.client.GetJSON(ctx, p, q, out)
}

func (s *Source) getMembersPage(ctx context.Context, projectID, afterID string, out *membersResponse) error {
	p := fmt.Sprintf("/v1/organizations/%s/projects/%s/members",
		url.PathEscape(s.orgID), url.PathEscape(projectID))
	q := url.Values{"limit": {defaultPageLimit}}
	if afterID != "" {
		q.Set("after_id", afterID)
	}
	return s.client.GetJSON(ctx, p, q, out)
}

func (s *Source) getAPIKeysPage(ctx context.Context, projectID, afterID string, out *apiKeysResponse) error {
	p := fmt.Sprintf("/v1/organizations/%s/projects/%s/api_keys",
		url.PathEscape(s.orgID), url.PathEscape(projectID))
	q := url.Values{"limit": {defaultPageLimit}}
	if afterID != "" {
		q.Set("after_id", afterID)
	}
	return s.client.GetJSON(ctx, p, q, out)
}

// sanitizeName bounds a project/key display name for findings (never raw user content
// in a title — the title lands in the ledger and SIEM export).
func sanitizeName(name string) string {
	name = strings.TrimSpace(name)
	if len(name) > 80 {
		return name[:80] + "…"
	}
	if name == "" {
		return "(unnamed)"
	}
	return name
}
