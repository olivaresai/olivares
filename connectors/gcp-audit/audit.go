// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gcpaudit

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// logEntry is the subset of a Cloud Logging LogEntry the connector reads. Only
// the fields needed to build the access edge are declared; the request/response,
// requestMetadata, authorizationInfo, status, labels and resource detail are
// deliberately NOT read — the connector emits the edge, never the body, and never
// the resourceName (which can embed identifiers; the management edge is
// identity→API, mirroring AWS CloudTrail, while own the per-resource data
// plane). (docs/SECURITY-HARDENING.md.)
type logEntry struct {
	LogName      string            `json:"logName"`
	Timestamp    string            `json:"timestamp"`
	ProtoPayload auditProtoPayload `json:"protoPayload"`
}

type auditProtoPayload struct {
	ServiceName        string             `json:"serviceName"`
	MethodName         string             `json:"methodName"`
	AuthenticationInfo authenticationInfo `json:"authenticationInfo"`
}

// authenticationInfo carries the principal the access is attributed to. Only the
// principal's email (the identity reference) is read — never a token, key or any
// credential value (docs/SECURITY-HARDENING.md).
type authenticationInfo struct {
	PrincipalEmail string `json:"principalEmail"`
}

// entriesListRequest is the Cloud Logging v2 entries:list request body
// (cloud.google.com/logging/docs/reference/v2/rest/v2/entries/list). The reads
// are scoped by resourceNames (org and/or projects), narrowed by filter (audit
// type + timestamp window) and ordered newest-first; pageToken paginates.
type entriesListRequest struct {
	ResourceNames []string `json:"resourceNames"`
	Filter        string   `json:"filter"`
	OrderBy       string   `json:"orderBy"`
	PageSize      int      `json:"pageSize"`
	PageToken     string   `json:"pageToken,omitempty"`
}

// gatherAudit reads Cloud Audit Logs over the lookback window via entries:list,
// paginating up to max_events / max_pages, mapping each Admin Activity / Data
// Access entry into one control-plane activity edge. System Event and Policy
// Denied entries are skipped (a denied attempt is not an observed access; system
// events are Google-initiated, not a principal's action). It emits in a
// deterministic order and returns the first error so the caller records a single
// health finding. ctx is honored between pages.
//
// Two bounds limit a pass: max_events is the per-pass EVENT BUDGET (the connector
// keeps the most recent max_events; reaching it is the configured cap, not an
// error). max_pages bounds API pagination — if it is exhausted while the API
// still offers a next page (and the event budget was NOT the reason we stopped),
// the result is partial and a coverage finding is emitted (never a silent cap).
func (s *Source) gatherAudit(ctx context.Context, sink sdk.Sink, at time.Time) error {
	resourceNames := s.cfg.auditResourceNames()
	if len(resourceNames) == 0 {
		return nil // no scope ⇒ nothing to read (Open guards this when a credential is set).
	}
	start := at.Add(-s.cfg.lookback)
	filter := s.cfg.logFilter + ` AND timestamp >= "` + start.UTC().Format(time.RFC3339) + `"`

	var edges []model.EdgeObservation
	token := ""
	truncated := false
	for page := 0; page < s.cfg.maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		pageSize := s.cfg.maxEvents - len(edges)
		if pageSize <= 0 {
			break // event budget reached (intentional cap, not truncation).
		}
		if pageSize > 1000 {
			pageSize = 1000
		}

		var resp struct {
			Entries       []logEntry `json:"entries"`
			NextPageToken string     `json:"nextPageToken"`
		}
		req := entriesListRequest{
			ResourceNames: resourceNames,
			Filter:        filter,
			OrderBy:       "timestamp desc",
			PageSize:      pageSize,
			PageToken:     token,
		}
		if err := s.postJSON(ctx, s.cfg.loggingEndpoint+"/v2/entries:list", req, &resp); err != nil {
			return err
		}
		for _, e := range resp.Entries {
			edge, ok := s.mapEntry(e)
			if !ok {
				continue
			}
			edges = append(edges, edge)
			if len(edges) >= s.cfg.maxEvents {
				break
			}
		}
		if resp.NextPageToken == "" || len(edges) >= s.cfg.maxEvents {
			break // fully drained, or event budget reached: not a page-budget truncation.
		}
		token = resp.NextPageToken
		if page == s.cfg.maxPages-1 {
			truncated = true // page budget exhausted with more audit entries pending.
		}
	}

	// Deterministic emit order for stable golden tests: by resource, then
	// principal, then observed time.
	sort.SliceStable(edges, func(i, j int) bool {
		if edges[i].ResourceRef != edges[j].ResourceRef {
			return edges[i].ResourceRef < edges[j].ResourceRef
		}
		if edges[i].OriginRef != edges[j].OriginRef {
			return edges[i].OriginRef < edges[j].OriginRef
		}
		return edges[i].ObservedAt.Before(edges[j].ObservedAt)
	})
	for _, e := range edges {
		if err := emit(ctx, sink, e); err != nil {
			return err
		}
	}
	if truncated {
		if err := emit(ctx, sink, coverageFinding(subjectAudit, s.cfg.scopeRef(),
			"GCP Cloud Audit Logs partial: stopped at max_pages — raise max_pages or narrow lookback for full coverage", at)); err != nil {
			return err
		}
	}
	return nil
}

// mapEntry maps one LogEntry to an activity edge, or ok=false to skip it: an
// entry with no principal (can't attribute), a System Event / Policy Denied entry
// (out of scope: not a principal's observed access), an empty service/method, or
// an unparseable timestamp. The mode is derived from the audit category and the
// method verb (classifyMethod); the confidence drops to approximate for a
// declared shared principal.
func (s *Source) mapEntry(e logEntry) (model.EdgeObservation, bool) {
	cat := categoryFromLogName(e.LogName)
	if cat == catSystemEvent || cat == catPolicyDenied {
		return model.EdgeObservation{}, false
	}
	principal := strings.TrimSpace(e.ProtoPayload.AuthenticationInfo.PrincipalEmail)
	if principal == "" {
		return model.EdgeObservation{}, false
	}
	service := strings.TrimSpace(e.ProtoPayload.ServiceName)
	method := strings.TrimSpace(e.ProtoPayload.MethodName)
	if service == "" || method == "" {
		return model.EdgeObservation{}, false
	}
	ts, ok := parseTime(e.Timestamp)
	if !ok {
		return model.EdgeObservation{}, false
	}
	mode := classifyMethod(cat, method)
	resRef := service + ":" + method
	return activityEdge(principal, resRef, service, mode, s.cfg.shared.ConfidenceFor(principal), ts), true
}

// parseTime parses a Cloud Logging RFC3339 timestamp (with or without fractional
// seconds, always UTC 'Z') and normalizes it to UTC, returning ok=false if no
// layout matches.
func parseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, l := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
