// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeprojects

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// PolicyConfig is the operator-declared governance policy for projects. Deny-closed:
// a rule that cannot be evaluated (malformed regex, unparseable limit) is a hard error
// at Open time — a typo must not silently leave a project ungoverned.
type PolicyConfig struct {
	ForbiddenNamePatterns   []string `json:"forbidden_name_patterns,omitempty"`
	MaxMembersPerProject    int      `json:"max_members_per_project,omitempty"`
	MaxAPIKeysPerProject    int      `json:"max_api_keys_per_project,omitempty"`
	RequireArchiveAfterDays int      `json:"require_archive_after_days,omitempty"`

	compiledPatterns []*regexp.Regexp
}

// parsePolicy parses and validates a JSON policy config. Malformed rules are a hard
// error (deny-closed: a typo must not silently skip governance).
func parsePolicy(s string) (*PolicyConfig, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	var pc PolicyConfig
	if err := json.Unmarshal([]byte(s), &pc); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	for _, pat := range pc.ForbiddenNamePatterns {
		re, err := regexp.Compile(pat)
		if err != nil {
			return nil, fmt.Errorf("invalid forbidden_name_pattern %q: %w", pat, err)
		}
		pc.compiledPatterns = append(pc.compiledPatterns, re)
	}
	return &pc, nil
}

// evaluateProjectPolicy runs the operator's policy rules against one project and
// emits HIGH findings for violations. Deny-closed: a rule violation is a finding, not
// a silent pass.
func (s *Source) evaluateProjectPolicy(ctx context.Context, sink sdk.Sink, p project, now time.Time) error {
	if s.policy == nil {
		return nil
	}

	name := strings.ToLower(strings.TrimSpace(p.Name))
	for _, re := range s.policy.compiledPatterns {
		if re.MatchString(name) {
			if err := sink.Emit(ctx, model.FindingReport{
				Kind:        "policy_violation",
				Severity:    model.SeverityHigh,
				SubjectKind: resProject,
				SubjectRef:  p.ID,
				Title:       "Project name matches forbidden pattern: " + sanitizeName(p.Name),
				DetailHash:  redact.Hash(p.ID + "|forbidden_name|" + re.String()),
				OccurredAt:  now,
			}); err != nil {
				return err
			}
		}
	}

	if s.policy.RequireArchiveAfterDays > 0 && p.ArchivedAt == "" && p.CreatedAt != "" {
		created, err := time.Parse(time.RFC3339, p.CreatedAt)
		if err == nil {
			age := now.Sub(created)
			limit := time.Duration(s.policy.RequireArchiveAfterDays) * 24 * time.Hour
			if age > limit {
				if err := sink.Emit(ctx, model.FindingReport{
					Kind:        "policy_violation",
					Severity:    model.SeverityMedium,
					SubjectKind: resProject,
					SubjectRef:  p.ID,
					Title:       fmt.Sprintf("Project exceeds archive-after-days limit (%d days): %s", s.policy.RequireArchiveAfterDays, sanitizeName(p.Name)),
					DetailHash:  redact.Hash(p.ID + "|stale|" + p.CreatedAt),
					OccurredAt:  now,
				}); err != nil {
					return err
				}
			}
		}
	}

	return nil
}
