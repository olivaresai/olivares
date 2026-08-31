// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// This file ingests Model Armor sanitization-result PLATFORM logs from Cloud Logging.
// The wire shape is intentionally narrow and pinned 2026-07-05 from Google's
// documented @type filter plus the Model Armor v1 discovery document's
// SanitizationResult fields. The SanitizeOperationLogEntry field-level schema is not
// published, so enums are string-open and absent fields are tolerated. The canonical
// selector is jsonPayload.@type, not logName: the documented MCP detection logName is
// primary-verified only for that stream, while @type covers template sanitize
// operations and floor-enforced Gemini generateContent calls uniformly.
//
// MINIMAL DATA: platform logs can carry the full prompt/response text. The structs below
// deliberately do NOT declare sanitizationInput or any SDP inspect/deidentify payload
// fields, so those values are ignored by encoding/json and cannot be emitted by accident.
package vertex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

const (
	sanitizationLogPayloadType = "type.googleapis.com/google.cloud.modelarmor.logging.v1.SanitizeOperationLogEntry"
	sanitizationMetricName     = "vertex.model_armor.sanitize_operations"
	guardrailFindingKind       = "guardrail"
)

// sanitizationEntriesListRequest is the Cloud Logging v2 entries:list request body.
// entries:list is a read-only POST by Google API design: the body carries the scoped
// resourceNames, filter and pagination cursor.
type sanitizationEntriesListRequest struct {
	ResourceNames []string `json:"resourceNames"`
	Filter        string   `json:"filter"`
	OrderBy       string   `json:"orderBy"`
	PageSize      int      `json:"pageSize"`
	PageToken     string   `json:"pageToken,omitempty"`
}

type sanitizationEntriesListResponse struct {
	Entries       []sanitizationLogEntry `json:"entries"`
	NextPageToken string                 `json:"nextPageToken"`
}

// sanitizationLogEntry is the minimal subset of a Cloud Logging LogEntry needed to map
// Model Armor result metadata. Do not add payload text fields here.
type sanitizationLogEntry struct {
	Timestamp string `json:"timestamp"`
	InsertID  string `json:"insertId"`
	LogName   string `json:"logName"`
	Resource  struct {
		Labels map[string]string `json:"labels"`
	} `json:"resource"`
	Labels      map[string]string       `json:"labels"`
	JSONPayload sanitizationJSONPayload `json:"jsonPayload"`
}

type sanitizationJSONPayload struct {
	OperationType             string             `json:"operationType"`
	SanitizationVerdict       string             `json:"sanitizationVerdict"`
	SanitizationVerdictReason string             `json:"sanitizationVerdictReason"`
	SanitizationResult        sanitizationResult `json:"sanitizationResult"`
}

type sanitizationResult struct {
	FilterMatchState string                              `json:"filterMatchState"`
	InvocationResult string                              `json:"invocationResult"`
	FilterResults    map[string]sanitizationFilterResult `json:"filterResults"`
}

type sanitizationFilterResult struct {
	RAIFilterResult            sanitizationRAIFilterResult `json:"raiFilterResult"`
	PIAndJailbreakFilterResult sanitizationSimpleResult    `json:"piAndJailbreakFilterResult"`
	MaliciousURIFilterResult   sanitizationSimpleResult    `json:"maliciousUriFilterResult"`
	SDPFilterResult            sanitizationSimpleResult    `json:"sdpFilterResult"`
	CSAMFilterFilterResult     sanitizationSimpleResult    `json:"csamFilterFilterResult"`
	VirusScanFilterResult      sanitizationSimpleResult    `json:"virusScanFilterResult"`
}

type sanitizationSimpleResult struct {
	MatchState string `json:"matchState"`
}

type sanitizationRAIFilterResult struct {
	MatchState       string
	NestedMatchState []string
}

// UnmarshalJSON decodes only RAI matchState values. The discovery shape uses
// raiFilterTypeResults as a per-filter map; accepting a list as well keeps the parser
// defensive without retaining any extra fields.
func (r *sanitizationRAIFilterResult) UnmarshalJSON(data []byte) error {
	var raw struct {
		MatchState           string          `json:"matchState"`
		RAIFilterTypeResults json.RawMessage `json:"raiFilterTypeResults"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	r.MatchState = raw.MatchState
	r.NestedMatchState = nil
	if len(raw.RAIFilterTypeResults) == 0 {
		return nil
	}
	var byName map[string]sanitizationSimpleResult
	if err := json.Unmarshal(raw.RAIFilterTypeResults, &byName); err == nil {
		keys := make([]string, 0, len(byName))
		for k := range byName {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			r.NestedMatchState = append(r.NestedMatchState, byName[k].MatchState)
		}
		return nil
	}
	var list []sanitizationSimpleResult
	if err := json.Unmarshal(raw.RAIFilterTypeResults, &list); err == nil {
		for _, item := range list {
			r.NestedMatchState = append(r.NestedMatchState, item.MatchState)
		}
	}
	return nil
}

type sanitizationMetricKey struct {
	verdict   string
	operation string
}

// gatherSanitization reads Model Armor sanitization result log entries over the lookback
// window, maps matched/blocking results into guardrail findings, and emits aggregate
// operation metrics. It preserves log-entry API order for findings; map folds are sorted.
func (s *Source) gatherSanitization(ctx context.Context, sink sdk.Sink, at time.Time) error {
	start := at.Add(-s.cfg.sanitizationLookback).UTC().Format(time.RFC3339)
	filter := `jsonPayload.@type="` + sanitizationLogPayloadType + `" AND timestamp>="` + start + `"`

	var findings []model.FindingReport
	metricCounts := map[sanitizationMetricKey]int64{}
	token := ""
	pagesRead := 0
	truncated := false
	for page := 0; page < s.cfg.maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		req := sanitizationEntriesListRequest{
			ResourceNames: []string{"projects/" + s.cfg.project},
			Filter:        filter,
			OrderBy:       "timestamp desc",
			PageSize:      1000,
			PageToken:     token,
		}
		var resp sanitizationEntriesListResponse
		if err := s.postJSON(ctx, strings.TrimRight(s.cfg.loggingEndpoint, "/")+"/v2/entries:list", req, &resp); err != nil {
			return err
		}
		pagesRead++
		for _, entry := range resp.Entries {
			finding, ok, metricKey := s.mapSanitizationEntry(entry, at)
			metricCounts[metricKey]++
			if ok {
				findings = append(findings, finding)
			}
		}
		if resp.NextPageToken == "" {
			break
		}
		token = resp.NextPageToken
		if page == s.cfg.maxPages-1 {
			truncated = true
		}
	}

	for _, finding := range findings {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := emit(ctx, sink, finding); err != nil {
			return err
		}
	}
	for _, sample := range s.sanitizationMetricSamples(metricCounts, at) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := emit(ctx, sink, sample); err != nil {
			return err
		}
	}
	if truncated {
		detail := fmt.Sprintf("vertex.model_armor_sanitization project=%s coverage=partial pages=%d cursor_pending=true", s.cfg.project, pagesRead)
		if err := emit(ctx, sink, postureFinding(model.SeverityLow, subjectArmorSanitization, s.projectRef(),
			"Vertex Model Armor sanitization-log coverage partial", detail, at)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Source) mapSanitizationEntry(entry sanitizationLogEntry, at time.Time) (model.FindingReport, bool, sanitizationMetricKey) {
	payload := entry.JSONPayload
	result := payload.SanitizationResult
	matchedFilters := result.matchedFilterNames()
	blocked := containsOpen(payload.SanitizationVerdict, "BLOCK")
	matched := isMatchFound(result.FilterMatchState) || len(matchedFilters) > 0
	metricKey := sanitizationMetricKey{
		verdict:   sanitizationOutcome(blocked, matched),
		operation: sanitizeOperation(payload.OperationType, "unknown"),
	}
	if !blocked && !matched {
		return model.FindingReport{}, false, metricKey
	}

	severity := model.SeverityMedium
	action := "flagged"
	if blocked {
		severity = model.SeverityHigh
		action = "blocked"
	}
	occurredAt := at
	if ts, ok := parseRFC3339(entry.Timestamp); ok {
		occurredAt = ts
	}
	templateID := strings.TrimSpace(entry.Resource.Labels["template_id"])
	location := strings.TrimSpace(entry.Resource.Labels["location"])
	container := strings.TrimSpace(entry.Resource.Labels["resource_container"])
	subjectRef := s.sanitizationSubjectRef(templateID, container, location)
	owaspLLM, owaspASI := result.taxonomy(matchedFilters)

	return model.FindingReport{
		Kind:        guardrailFindingKind,
		Severity:    severity,
		SubjectKind: subjectArmorSanitization,
		SubjectRef:  redact.Clean(subjectRef),
		Title:       fmt.Sprintf("Model Armor %s %s: filters [%s]", action, sanitizeOperation(payload.OperationType, "operation"), strings.Join(matchedFilters, "|")),
		DetailHash:  redact.Hash(sanitizationFingerprint(entry, matchedFilters, templateID, location, container)),
		OccurredAt:  occurredAt,
		OWASPLLM:    owaspLLM,
		OWASPASI:    owaspASI,
	}, true, metricKey
}

func (s *Source) sanitizationSubjectRef(templateID, container, location string) string {
	if templateID != "" {
		return templateID
	}
	if container != "" && location != "" {
		return container + "/" + location
	}
	return s.projectRef()
}

func sanitizationFingerprint(entry sanitizationLogEntry, matchedFilters []string, templateID, location, container string) string {
	payload := entry.JSONPayload
	return strings.Join([]string{
		"insert_id=" + entry.InsertID,
		"log_name=" + entry.LogName,
		"verdict=" + payload.SanitizationVerdict,
		"verdict_reason=" + payload.SanitizationVerdictReason,
		"invocation_result=" + payload.SanitizationResult.InvocationResult,
		"matched_filters=" + strings.Join(matchedFilters, ","),
		"client_name=" + entry.Labels["modelarmor.googleapis.com/client_name"],
		"client_correlation_id=" + entry.Labels["modelarmor.googleapis.com/client_correlation_id"],
		"template_id=" + templateID,
		"location=" + location,
		"resource_container=" + container,
	}, "|")
}

func (r sanitizationResult) matchedFilterNames() []string {
	if len(r.FilterResults) == 0 {
		return nil
	}
	keys := make([]string, 0, len(r.FilterResults))
	for k := range r.FilterResults {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if r.FilterResults[k].matched() {
			out = append(out, k)
		}
	}
	return out
}

func (r sanitizationResult) taxonomy(matchedFilters []string) ([]string, []string) {
	if len(matchedFilters) == 0 {
		return nil, nil
	}
	llm := map[string]struct{}{}
	asi := map[string]struct{}{}
	for _, name := range matchedFilters {
		filter := r.FilterResults[name]
		if filter.piAndJailbreakMatched() {
			llm["LLM01:2025"] = struct{}{}
			asi["ASI01"] = struct{}{}
		}
		if filter.sdpMatched() {
			llm["LLM02:2025"] = struct{}{}
		}
	}
	return sortedSet(llm), sortedSet(asi)
}

func sortedSet(in map[string]struct{}) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for k := range in {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (r sanitizationFilterResult) matched() bool {
	return r.RAIFilterResult.matched() ||
		isMatchFound(r.PIAndJailbreakFilterResult.MatchState) ||
		isMatchFound(r.MaliciousURIFilterResult.MatchState) ||
		isMatchFound(r.SDPFilterResult.MatchState) ||
		isMatchFound(r.CSAMFilterFilterResult.MatchState) ||
		isMatchFound(r.VirusScanFilterResult.MatchState)
}

func (r sanitizationFilterResult) piAndJailbreakMatched() bool {
	return isMatchFound(r.PIAndJailbreakFilterResult.MatchState)
}

func (r sanitizationFilterResult) sdpMatched() bool {
	return isMatchFound(r.SDPFilterResult.MatchState)
}

func (r sanitizationRAIFilterResult) matched() bool {
	if isMatchFound(r.MatchState) {
		return true
	}
	for _, state := range r.NestedMatchState {
		if isMatchFound(state) {
			return true
		}
	}
	return false
}

func sanitizationOutcome(blocked, matched bool) string {
	switch {
	case blocked:
		return "block"
	case matched:
		return "match"
	default:
		return "no_match"
	}
}

func sanitizeOperation(op, fallback string) string {
	op = strings.ToLower(strings.TrimSpace(op))
	if op == "" {
		return fallback
	}
	return op
}

func isMatchFound(state string) bool {
	return strings.EqualFold(strings.TrimSpace(state), "MATCH_FOUND")
}

func containsOpen(value, needle string) bool {
	return strings.Contains(strings.ToUpper(strings.TrimSpace(value)), needle)
}

func (s *Source) sanitizationMetricSamples(counts map[sanitizationMetricKey]int64, at time.Time) []model.MetricSample {
	keys := make([]sanitizationMetricKey, 0, len(counts))
	for k, v := range counts {
		if v != 0 {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].verdict != keys[j].verdict {
			return keys[i].verdict < keys[j].verdict
		}
		return keys[i].operation < keys[j].operation
	})
	out := make([]model.MetricSample, 0, len(keys))
	for _, k := range keys {
		out = append(out, model.MetricSample{
			Name:        sanitizationMetricName,
			Value:       counts[k],
			Additive:    true,
			Unit:        "1",
			SubjectKind: "project",
			SubjectRef:  s.projectRef(),
			OccurredAt:  at,
			Dimensions: map[string]string{
				"verdict":   k.verdict,
				"operation": k.operation,
			},
		})
	}
	return out
}

// postJSON issues one Bearer-authorized POST with a JSON body and decodes the JSON
// response into out. It is deliberately local to this connector. The only caller is
// Cloud Logging entries:list, a read-only POST whose body is the query.
func (s *Source) postJSON(ctx context.Context, fullURL string, body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fullURL, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	if err := s.authorize(ctx, req); err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	return s.do(req, out)
}
