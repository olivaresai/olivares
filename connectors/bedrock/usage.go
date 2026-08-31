// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// This file turns Amazon Bedrock MODEL INVOCATION LOGS into per-model token-usage
// CostSamples — the usage surface CloudTrail does not carry (CloudTrail sees the
// Bedrock InvokeModel/Converse ACCESS edge but no tokens; the s3-cloudtrail connector
// owns that Claude-access path). It reads the same record shape from both delivery
// destinations: S3-delivered gzipped JSON files (a local path, like s3-cloudtrail) and
// CloudWatch Logs (FilterLogEvents). It is provider-agnostic — Titan/Nova/Llama/
// Mistral/Cohere/… are classified the same as Claude.
//
// MINIMAL DATA (docs/SECURITY-HARDENING.md): a model-invocation-log record also carries the full
// request/response bodies (input.inputBodyJson / output.outputBodyJson — raw prompt and
// completion, up to 100 KB). Those fields are DELIBERATELY ABSENT from the struct below,
// so they are never deserialized, held, hashed or emitted. The connector reads only the
// token COUNTS, the model id, the timestamp and the caller principal (an attribution
// ref, the same accepted exception the cost path makes for Actor).
//
// COST is NOT in these logs: a usage sample carries the real token counts with
// CostMicroUSD=0 and Provenance=estimated (cost not reported here, never zero). Billed
// cost comes only from Cost Explorer (cost.go). The two streams never double-count: the
// usage stream has cost=0, the cost stream has tokens=0.
package bedrock

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/bedrockid"
	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// invocationLog is the minimal-data subset of a Bedrock model-invocation-log record we
// read (verified field names + nesting, jun-2026). The bodies (inputBodyJson /
// outputBodyJson) are intentionally not declared — see the file header. Token counts are
// nested camelCase under input/output (the PascalCase InputTokenCount/OutputTokenCount
// are CloudWatch metric names, a different surface — not these JSON fields).
type invocationLog struct {
	// SchemaType ("ModelInvocationLog") and Operation document the wire shape; parsing
	// does NOT gate on them — a record is usable iff it names a modelId (see usable).
	SchemaType string `json:"schemaType"`
	Timestamp  string `json:"timestamp"` // ISO 8601
	Operation  string `json:"operation"` // InvokeModel|Converse|…
	ModelID    string `json:"modelId"`
	Identity   struct {
		ARN string `json:"arn"`
	} `json:"identity"`
	Input struct {
		InputTokenCount int64 `json:"inputTokenCount"`
	} `json:"input"`
	Output struct {
		OutputTokenCount int64 `json:"outputTokenCount"`
	} `json:"output"`
}

// usable reports whether the record names a model we can attribute usage to. A record
// without a modelId is skipped (we never fabricate a model ref).
func (l invocationLog) usable() bool { return strings.TrimSpace(l.ModelID) != "" }

// parseInvocationLogs extracts invocation-log records from a blob, tolerating the three
// real shapes: a JSON array of records (an S3 batch file), newline-delimited records
// (NDJSON), or a single record object (one CloudWatch log-event message). The S3
// intra-file delimiter is not contractually documented, so all three are accepted
// rather than assume one.
func parseInvocationLogs(data []byte) []invocationLog {
	var arr []invocationLog
	if err := json.Unmarshal(data, &arr); err == nil && len(arr) > 0 {
		return arr
	}

	var recs []invocationLog
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // tolerate long lines (bodies up to 100 KB)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var r invocationLog
		if err := json.Unmarshal(line, &r); err == nil && r.usable() {
			recs = append(recs, r)
		}
	}
	if len(recs) > 0 {
		return recs
	}

	var one invocationLog
	if err := json.Unmarshal(data, &one); err == nil && one.usable() {
		return []invocationLog{one}
	}
	return nil
}

// usageSample maps one invocation-log record to a token-usage CostSample, or ok=false
// when there is nothing to measure (no modelId, or zero input AND zero output tokens —
// e.g. an errored or non-token call). fallback supplies OccurredAt when the record's
// own timestamp is missing/unparsable. The cost is left at 0 with Provenance=estimated:
// the tokens are real, the money is not reported here (ARCHITECTURE.md).
func usageSample(rec invocationLog, fallback time.Time) (model.CostSample, bool) {
	if !rec.usable() {
		return model.CostSample{}, false
	}
	in, out := rec.Input.InputTokenCount, rec.Output.OutputTokenCount
	if in <= 0 && out <= 0 {
		return model.CostSample{}, false
	}
	ts := fallback
	if t, ok := parseLogTime(rec.Timestamp); ok {
		ts = t
	}
	cs := model.CostSample{
		ProviderRef:  ProviderBedrock,
		ModelRef:     bedrockid.BaseModelID(rec.ModelID),
		InputTokens:  in,
		OutputTokens: out,
		CostMicroUSD: 0, // cost is not in the invocation log — never fabricated
		OccurredAt:   ts,
		Gateway:      bedrockid.Gateway(rec.ModelID),
		Provenance:   model.ProvenanceEstimated,
	}
	// The caller principal is an attribution ref (the "who" dimension for per-team /
	// per-developer chargeback) — the same accepted exception the cost path makes for
	// Actor. It is an ARN, never a credential; clean it defensively anyway.
	if arn := strings.TrimSpace(rec.Identity.ARN); arn != "" {
		cs.Actor = redact.Clean(arn)
	}
	return cs, true
}

// gatherUsageFiles reads S3-delivered model-invocation-log files from the configured
// local path (a file, or every *.json/*.json.gz in a directory, in name order) and
// emits a usage CostSample per record. It is local I/O — no AWS credentials. ctx is
// honored between files and records.
func (s *Source) gatherUsageFiles(ctx context.Context, sink sdk.Sink, at time.Time) error {
	files, err := listLogFiles(s.cfg.usageLogPath)
	if err != nil {
		return err
	}
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.gatherUsageFile(ctx, sink, f, at); err != nil {
			return err
		}
	}
	return nil
}

// gatherUsageFile reads one log file (gunzipping a .gz) and emits its usage samples.
func (s *Source) gatherUsageFile(ctx context.Context, sink sdk.Sink, path string, at time.Time) error {
	f, err := os.Open(path) //nolint:gosec // operator-configured log path, read-only
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer func() { _ = gz.Close() }()
		r = gz
	}
	data, err := io.ReadAll(io.LimitReader(r, maxResponseBytes))
	if err != nil {
		return err
	}
	for _, rec := range parseInvocationLogs(data) {
		if err := ctx.Err(); err != nil {
			return err
		}
		cs, ok := usageSample(rec, at)
		if !ok {
			continue
		}
		if err := emit(ctx, sink, cs); err != nil {
			return err
		}
	}
	return nil
}

// listLogFiles resolves the configured path to a sorted list of files. A directory
// contributes its *.json and *.json.gz entries; a file contributes itself.
func listLogFiles(path string) ([]string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return []string{path}, nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if n := e.Name(); strings.HasSuffix(n, ".json") || strings.HasSuffix(n, ".json.gz") {
			files = append(files, filepath.Join(path, n))
		}
	}
	sort.Strings(files)
	return files, nil
}

// --- CloudWatch Logs (FilterLogEvents) ------------------------------------------

// cwLogsTarget is the X-Amz-Target for CloudWatch Logs FilterLogEvents.
const cwLogsTarget = "Logs_20140328.FilterLogEvents"

// filterLogEventsRequest is the FilterLogEvents request. startTime/endTime are epoch
// MILLISECONDS (not seconds). nextToken paginates; limit bounds a page (<=10000).
type filterLogEventsRequest struct {
	LogGroupName string `json:"logGroupName"`
	StartTime    int64  `json:"startTime"`
	EndTime      int64  `json:"endTime"`
	Limit        int    `json:"limit,omitempty"`
	NextToken    string `json:"nextToken,omitempty"`
}

// filterLogEventsResponse is one FilterLogEvents page. Pagination is finished ONLY when
// nextToken is absent — an empty/partial events page may still carry a nextToken.
type filterLogEventsResponse struct {
	Events    []filteredLogEvent `json:"events"`
	NextToken string             `json:"nextToken"`
}

// filteredLogEvent carries the model-invocation-log record as a JSON string in message;
// timestamp is epoch milliseconds (the event time, used as the OccurredAt fallback).
type filteredLogEvent struct {
	Timestamp int64  `json:"timestamp"`
	Message   string `json:"message"`
}

// cloudWatchPageSize is the per-page event cap requested from FilterLogEvents (the API
// hard-caps a page at 10000 events / 1 MB).
const cloudWatchPageSize = 10000

// cloudWatchMaxEmptyPages bounds CONSECUTIVE no-progress pages. FilterLogEvents may
// return an empty (or sub-page) events page that STILL carries a nextToken mid-scan;
// pagination ends only on an ABSENT nextToken. Without a bound a run of such pages would
// loop forever (the per-request HTTP timeout bounds each call, not the loop, and Gather
// runs under the runtime-lifetime ctx, not a per-pass deadline). We bound non-progress
// rather than total pages so legitimate full-page traffic (capped separately by
// max_events) is never falsely truncated; on the bound we emit an honest partial-coverage
// finding (no silent caps). The counter resets on any page that advances the event count.
const cloudWatchMaxEmptyPages = 50

// gatherUsageCloudWatch pulls model-invocation logs from the configured CloudWatch Logs
// group over the usage_lookback window via FilterLogEvents, parses each event's message
// as a record, and emits a usage CostSample. It paginates by nextToken — stopping ONLY
// when the response carries no nextToken (an empty/partial page is not the end) — and
// bounds total events at max_events AND consecutive no-progress pages, emitting an honest
// partial-coverage finding if it stops with a cursor still pending (no silent caps).
func (s *Source) gatherUsageCloudWatch(ctx context.Context, sink sdk.Sink, at time.Time) error {
	startMS := at.Add(-s.cfg.usageLookback).UnixMilli()
	endMS := at.UnixMilli()

	token := ""
	seen := 0
	emptyPages := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		limit := cloudWatchPageSize
		if remaining := s.cfg.maxEvents - seen; remaining < limit {
			limit = remaining
		}
		if limit <= 0 {
			// Hit the max_events bound.
			return s.cloudWatchPartial(ctx, sink, token, seen, "max_events reached; raise max_events", at)
		}

		var resp filterLogEventsResponse
		req := filterLogEventsRequest{
			LogGroupName: s.cfg.usageLogGroup,
			StartTime:    startMS,
			EndTime:      endMS,
			Limit:        limit,
			NextToken:    token,
		}
		if err := s.awsJSONPost(ctx, s.cfg.cwLogsEndpoint, cwLogsTarget, cwLogsSigningService, s.cfg.region, req, &resp); err != nil {
			return err
		}

		before := seen
		for _, ev := range resp.Events {
			fallback := at
			if ev.Timestamp > 0 {
				fallback = time.UnixMilli(ev.Timestamp).UTC()
			}
			for _, rec := range parseInvocationLogs([]byte(ev.Message)) {
				cs, ok := usageSample(rec, fallback)
				if !ok {
					continue
				}
				if err := emit(ctx, sink, cs); err != nil {
					return err
				}
			}
			seen++
		}

		if resp.NextToken == "" {
			return nil // pagination finished ONLY on absent nextToken
		}
		// Bound CONSECUTIVE no-progress pages (empty events + pending cursor) so a sparse
		// or pathological stream cannot loop forever; reset on any forward progress.
		if seen == before {
			if emptyPages++; emptyPages >= cloudWatchMaxEmptyPages {
				return s.cloudWatchPartial(ctx, sink, token, seen, "too many empty pages with a pending cursor", at)
			}
		} else {
			emptyPages = 0
		}
		token = resp.NextToken
	}
}

// cloudWatchPartial emits the honest Low partial-coverage posture finding when the
// CloudWatch pull stops with a cursor (token) still pending; with no pending cursor the
// window was fully read, so it emits nothing.
func (s *Source) cloudWatchPartial(ctx context.Context, sink sdk.Sink, token string, seen int, why string, at time.Time) error {
	if token == "" {
		return nil
	}
	return emit(ctx, sink, postureFinding(model.SeverityLow, subjectUsage, s.cfg.usageLogGroup,
		"Bedrock CloudWatch usage is PARTIAL — "+why,
		fmt.Sprintf("bedrock.usage log_group=%s coverage=partial read=%d cursor_pending=true; %s", s.cfg.usageLogGroup, seen, why), at))
}

// logTimeLayouts are the timestamp formats a model-invocation-log record uses (ISO 8601).
var logTimeLayouts = []string{time.RFC3339, time.RFC3339Nano}

// parseLogTime parses a record timestamp to UTC, reporting ok=false on an unparsable
// value so the caller falls back to the event/pass time rather than guess.
func parseLogTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, l := range logTimeLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
