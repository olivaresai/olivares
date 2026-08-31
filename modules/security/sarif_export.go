// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/siemwire"
)

const (
	// sarifFindingLimit is GitHub's maximum result count for one SARIF run.
	sarifFindingLimit       = 25000
	sarifToolName           = "Olivares AI"
	sarifToolInformationURI = "https://olivares.ai"
	sarifAutomationID       = "security/findings"
)

// handleExportFindings exports a complete filtered snapshot. Request pagination
// is deliberately ignored: export walks store cursors until exhaustion or the
// per-run interoperability cap.
func (m *Module) handleExportFindings(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.writeSARIFExport(w, r, mc, sarifFindingLimit)
}

func (m *Module) writeSARIFExport(w http.ResponseWriter, r *http.Request, mc api.ModuleContext, maxResults int) {
	if format := strings.TrimSpace(r.URL.Query().Get("format")); format != "sarif" {
		writeJSON(w, http.StatusBadRequest, errorBody("unsupported format; valid values: sarif"))
		return
	}

	findings, truncated, err := collectSARIFFindings(
		r.Context(), mc.Data, mc.Tenant, findingFilters(r), maxResults,
	)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	body, err := siemwire.SARIF(siemwire.ToolInfo{
		Name:           sarifToolName,
		Version:        m.engineVersion,
		InformationURI: sarifToolInformationURI,
		AutomationID:   sarifAutomationID,
	}, findings)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/sarif+json")
	w.Header().Set("Content-Disposition", `attachment; filename="olivares-findings.sarif"`)
	if truncated {
		w.Header().Set("X-Olivares-Truncated", "true")
	}
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(body); err != nil {
		m.debugf("security: write SARIF export failed", "err", err)
	}
}

func collectSARIFFindings(
	ctx context.Context,
	data api.ScopedData,
	tenant model.TenantID,
	filters []model.Filter,
	maxResults int,
) ([]siemwire.SARIFFinding, bool, error) {
	if maxResults < 1 {
		return nil, false, errors.New("SARIF result limit must be positive")
	}

	out := make([]siemwire.SARIFFinding, 0, min(maxResults, listCap))
	truncated := false
	err := data.View(ctx, func(sc store.Scope) error {
		cursor := ""
		for len(out) < maxResults {
			pageLimit := min(listCap, maxResults-len(out))
			found, page, err := sc.Findings().List(ctx, model.Query{
				Filters: filters,
				Limit:   pageLimit,
				Cursor:  cursor,
			})
			if err != nil {
				return err
			}
			for _, finding := range found {
				if len(out) == maxResults {
					truncated = true
					break
				}
				out = append(out, findingToSARIF(tenant, finding))
			}
			if len(out) == maxResults {
				truncated = truncated || page.HasMore
				return nil
			}
			if !page.HasMore {
				return nil
			}
			if page.Cursor == "" {
				// A missing continuation cursor makes the remaining result set
				// unknowable; surface the partial response honestly.
				truncated = true
				return nil
			}
			cursor = page.Cursor
		}
		return nil
	})
	return out, truncated, err
}

func findingToSARIF(tenant model.TenantID, finding model.Finding) siemwire.SARIFFinding {
	ruleID := metadataString(finding.Metadata, "rule_ref")
	if ruleID == "" {
		ruleID = "olv-" + finding.Kind
	}
	artifactURI := metadataString(finding.Metadata, "artifact_uri")
	if artifactURI == "" {
		// Deliberate, DOCUMENTED fallback (CLI help + egress docs): a synthetic
		// URI keeps the run valid and ingestible (ADO accepts URI-only locations
		// behind allowmissingpartialfingerprints; GitLab requires only that a
		// physicalLocation exist), but GitHub renders alerts only for URIs that
		// match a committed file — detectors that want GitHub anchoring must set
		// artifact_uri (research annex 2026-07-24 §2). The identity stays in
		// ruleId + message either way.
		artifactURI = "governance/" + finding.SubjectKind + "/" + finding.SubjectID.String()
	}
	level, securitySeverity := sarifSeverity(finding.Severity)
	return siemwire.SARIFFinding{
		RuleID:           ruleID,
		RuleName:         finding.Kind,
		ShortDescription: finding.Title,
		// The finding carries no rich guidance today, so help is plain text: the
		// markdown form stays empty rather than putting unrendered markup in front
		// of an analyst whose consumer only reads text.
		HelpText:         finding.Title,
		Level:            level,
		SecuritySeverity: securitySeverity,
		Tags:             sarifTaxonomyTags(finding.Metadata),
		Message:          finding.Title,
		ArtifactURI:      artifactURI,
		StartLine:        1,
		Fingerprint:      hashHex(tenant.String() + "/" + finding.ID.String()),
	}
}

func sarifSeverity(severity model.Severity) (level, securitySeverity string) {
	switch severity {
	case model.SeverityCritical:
		return "error", "9.5"
	case model.SeverityHigh:
		return "error", "8.0"
	case model.SeverityMedium:
		return "warning", "5.0"
	case model.SeverityLow:
		return "note", "2.0"
	default:
		// Core persistence currently has no info value; this preserves the
		// specified projection if an older/imported row carries it.
		return "note", "0.5"
	}
}

func metadataString(metadata map[string]any, key string) string {
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

func sarifTaxonomyTags(metadata map[string]any) []string {
	tags := make([]string, 0)
	seen := make(map[string]struct{})
	for _, axis := range []string{"owasp_llm", "owasp_asi", "atlas"} {
		for _, value := range metadataStrings(metadata[axis]) {
			tag := "external/" + axis + "/" + value
			if _, exists := seen[tag]; exists {
				continue
			}
			seen[tag] = struct{}{}
			tags = append(tags, tag)
		}
	}
	return tags
}

func metadataStrings(value any) []string {
	var values []string
	switch typed := value.(type) {
	case string:
		values = []string{typed}
	case []string:
		values = typed
	case []any:
		values = make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				values = append(values, text)
			}
		}
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}
