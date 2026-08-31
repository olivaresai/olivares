// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// cloudTrailTarget is the X-Amz-Target value for LookupEvents on the CloudTrail
// JSON protocol. LookupEvents returns ONLY management events by AWS design.
const cloudTrailTarget = "com.amazonaws.cloudtrail.v20131101.CloudTrail_20131101.LookupEvents"

// cloudTrailContentType is the AWS JSON 1.1 content type CloudTrail expects.
const cloudTrailContentType = "application/x-amz-json-1.1"

// lookupRequest is the LookupEvents request body. Times are Unix seconds (the
// CloudTrail JSON contract). NextToken paginates; MaxResults bounds a page.
type lookupRequest struct {
	StartTime  int64  `json:"StartTime"`
	EndTime    int64  `json:"EndTime"`
	MaxResults int    `json:"MaxResults,omitempty"`
	NextToken  string `json:"NextToken,omitempty"`
}

// lookupResponse is the LookupEvents response page.
type lookupResponse struct {
	Events    []lookupEvent `json:"Events"`
	NextToken string        `json:"NextToken"`
}

// lookupEvent is one returned event. The authoritative detail lives in the
// CloudTrailEvent JSON STRING, which we parse separately; EventTime is a
// convenience timestamp on the envelope.
type lookupEvent struct {
	EventTime       float64 `json:"EventTime"`
	CloudTrailEvent string  `json:"CloudTrailEvent"`
}

// ctEvent is the parsed CloudTrailEvent. Only the fields needed to classify the
// access and attribute the principal are read; request/response parameters and
// any payload are deliberately ignored (minimal-data).
type ctEvent struct {
	EventSource   string         `json:"eventSource"`
	EventName     string         `json:"eventName"`
	EventTime     string         `json:"eventTime"`
	ReadOnly      *bool          `json:"readOnly"`
	EventCategory string         `json:"eventCategory"`
	UserIdentity  ctUserIdentity `json:"userIdentity"`
}

// ctUserIdentity carries only the principal-attribution fields.
type ctUserIdentity struct {
	Type           string `json:"type"`
	ARN            string `json:"arn"`
	SessionContext struct {
		SessionIssuer struct {
			ARN string `json:"arn"`
		} `json:"sessionIssuer"`
	} `json:"sessionContext"`
}

// gatherCloudTrail runs the CloudTrail pass: LookupEvents over the lookback
// window, paginating by NextToken up to max_events, classifying each management
// event into one control-plane activity edge. Data-category records are skipped
// defensively (owns the data plane). It returns the first error so the caller
// can record a single health finding. ctx is honored between pages.
func (s *Source) gatherCloudTrail(ctx context.Context, sink sdk.Sink, at time.Time) error {
	start := at.Add(-s.cfg.lookback)
	edges, err := s.collectCloudTrailEdges(ctx, start, at)
	if err != nil {
		return err
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
	return nil
}

// collectCloudTrailEdges pages LookupEvents and maps each event to an edge,
// stopping at max_events or when no NextToken remains.
func (s *Source) collectCloudTrailEdges(ctx context.Context, start, end time.Time) ([]model.EdgeObservation, error) {
	var edges []model.EdgeObservation
	next := ""
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		remaining := s.cfg.maxEvents - len(edges)
		if remaining <= 0 {
			return edges, nil
		}
		page := cloudTrailPageSize
		if remaining < page {
			page = remaining
		}

		resp, err := s.lookupEvents(ctx, lookupRequest{
			StartTime:  start.Unix(),
			EndTime:    end.Unix(),
			MaxResults: page,
			NextToken:  next,
		})
		if err != nil {
			return nil, err
		}

		for _, ev := range resp.Events {
			edge, ok := mapCloudTrailEvent(ev, end)
			if !ok {
				continue // Data-category or unparsable ⇒ skip (boundary/defensive).
			}
			edges = append(edges, edge)
			if len(edges) >= s.cfg.maxEvents {
				return edges, nil
			}
		}
		if resp.NextToken == "" {
			return edges, nil
		}
		next = resp.NextToken
	}
}

// mapCloudTrailEvent parses one event and maps it to an activity edge. It returns
// ok=false (skip) for an unparsable CloudTrailEvent, an empty source/name, or a
// Data-category record (owns the S3 data plane; never double-ingest).
func mapCloudTrailEvent(ev lookupEvent, fallback time.Time) (model.EdgeObservation, bool) {
	var ct ctEvent
	if err := json.Unmarshal([]byte(ev.CloudTrailEvent), &ct); err != nil {
		return model.EdgeObservation{}, false
	}
	if strings.EqualFold(ct.EventCategory, "Data") {
		return model.EdgeObservation{}, false
	}
	if ct.EventSource == "" || ct.EventName == "" {
		return model.EdgeObservation{}, false
	}

	principal, conf := attributePrincipal(ct.UserIdentity)
	// readOnly drives the control-plane R/W classification. When CloudTrail omits
	// the field, report ModeUnknown rather than guess — honest classification over
	// a fabricated one is a project invariant (sdk/model enums: "Unknown is
	// explicit ... never guessed").
	mode := model.ModeUnknown
	if ct.ReadOnly != nil {
		if *ct.ReadOnly {
			mode = model.ModeRead
		} else {
			mode = model.ModeWrite
		}
	}
	resRef := ct.EventSource + ":" + ct.EventName
	at := parseEventTime(ct.EventTime, ev.EventTime, fallback)

	return activityEdge(principal, resRef, ct.EventSource, mode, conf, at), true
}

// attributePrincipal resolves the acting principal reference and its confidence.
// A distinct user/role ARN is firmly attributed; an assumed-role/shared session
// (only the session-issuer ARN available) or an absent ARN is approximate.
func attributePrincipal(ui ctUserIdentity) (string, model.Confidence) {
	if ui.ARN != "" {
		// An assumed-role principal ARN is itself a shared session, so it is
		// approximate even though an arn is present.
		if strings.EqualFold(ui.Type, "AssumedRole") {
			return ui.ARN, model.ConfidenceApproximate
		}
		return ui.ARN, model.ConfidenceAttributed
	}
	if issuer := ui.SessionContext.SessionIssuer.ARN; issuer != "" {
		return issuer, model.ConfidenceApproximate
	}
	if ui.Type != "" {
		return ui.Type, model.ConfidenceApproximate
	}
	return "unknown", model.ConfidenceApproximate
}

// parseEventTime returns the event's own eventTime (RFC3339) when present, else
// the envelope EventTime (Unix seconds, possibly fractional), else the per-pass
// fallback. All times are normalized to UTC.
func parseEventTime(rfc string, unix float64, fallback time.Time) time.Time {
	if rfc != "" {
		if t, err := time.Parse(time.RFC3339, rfc); err == nil {
			return t.UTC()
		}
	}
	if unix > 0 {
		sec := int64(unix)
		nsec := int64((unix - float64(sec)) * 1e9)
		return time.Unix(sec, nsec).UTC()
	}
	return fallback
}

// lookupEvents issues one SigV4-signed LookupEvents POST and decodes the page.
//
// NOTE ON READ-ONLY: CloudTrail's JSON protocol mandates POST even for this
// read-only lookup (the request body carries the time window and pagination
// token). This is the single documented exception to "reads are GET" in this
// connector; LookupEvents performs NO mutation — it only reads the trail.
func (s *Source) lookupEvents(ctx context.Context, body lookupRequest) (lookupResponse, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return lookupResponse{}, err
	}
	endpoint := strings.TrimRight(s.cfg.cloudTrailEndpoint, "/") + "/"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return lookupResponse{}, err
	}
	req.Header.Set("Content-Type", cloudTrailContentType)
	req.Header.Set("X-Amz-Target", cloudTrailTarget)
	sign(req, raw, cloudTrailSigningService, s.cfg.region, s.cfg.creds, time.Now())

	resp, err := s.httpClient().Do(req)
	if err != nil {
		return lookupResponse{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return lookupResponse{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return lookupResponse{}, fmt.Errorf("cloudtrail: LookupEvents returned status %d", resp.StatusCode)
	}
	var out lookupResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return lookupResponse{}, fmt.Errorf("cloudtrail: decode LookupEvents response: %w", err)
	}
	return out, nil
}
