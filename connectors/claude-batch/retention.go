// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudebatch

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// gatherRetention checks each file's created_at against the configured upload_ttl
// and emits a FindingReport for files that have exceeded the retention window. The
// connector is read-only — it signals expiry, never deletes.
func (s *Source) gatherRetention(ctx context.Context, sink sdk.Sink, files []fileEntry) error {
	if s.uploadTTL <= 0 {
		return nil
	}
	now := s.clock().UTC()
	cutoff := now.Add(-s.uploadTTL)

	for _, fi := range files {
		if fi.ID == "" {
			continue
		}
		created := parseTime(fi.CreatedAt, now)
		if created.After(cutoff) {
			continue
		}
		age := now.Sub(created).Truncate(time.Hour)
		f := model.FindingReport{
			Kind:        findingKindRetentionExpired,
			Severity:    model.SeverityLow,
			SubjectKind: "claude_file",
			SubjectRef:  fi.ID,
			Title: fmt.Sprintf("File %s exceeded upload retention (%s old, TTL %s): %s",
				fi.ID, formatDuration(age), formatDuration(s.uploadTTL), redact.Clean(fi.Filename)),
			DetailHash: redact.Hash(strings.Join([]string{
				fi.ID, fi.Filename, fi.CreatedAt,
				fmt.Sprintf("ttl=%s", s.uploadTTL),
			}, "|")),
			OccurredAt: now,
		}
		if err := sink.Emit(ctx, f); err != nil {
			return err
		}
	}
	return nil
}

// formatDuration returns a human-readable duration (e.g. "30d", "2h", "45m").
func formatDuration(d time.Duration) string {
	if d >= 24*time.Hour {
		days := int(d.Hours() / 24)
		return fmt.Sprintf("%dd", days)
	}
	if d >= time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}
