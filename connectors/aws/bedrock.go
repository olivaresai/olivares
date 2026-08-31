// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// This file adds the read-only AWS Bedrock Guardrails SAFETY-POSTURE surface.
// It reads the guardrail CONFIGURATION (the content/topic/word/PII/grounding policies
// a regulated buyer must evidence) and the model-invocation-logging posture, and emits
// minimal-data FindingReport{Kind:"safety_posture"} — exactly the posture-findings
// pattern of claude-api/governance.go, never a payload.
//
// Two honesty boundaries are designed in:
//   - It is READ-FIRST. It never calls ApplyGuardrail: that is a paid bedrock-runtime
//     POST that inspects content (an enforcement action), out of scope here.
//   - Bedrock exposes NO list API for past guardrail DECISIONS. ApplyGuardrail returns
//     its assessment only synchronously; historical decisions are auditable only when
//     model-invocation logging is enabled AND the caller sends trace=ENABLED — and even
//     then they ride inside the invocation log, not a decision stream. So instead of
//     fabricating a decision feed, this connector reports the AUDITABILITY posture: is
//     model-invocation logging on? (Ingesting the actual decisions from CloudWatch/S3
//     logs is an explicit follow-up, the inference-proxy path.)
package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// bedrockListPageSize is the ListGuardrails page size (the API caps maxResults at
// 1000); bedrockMaxListPages bounds pagination defensively.
const (
	bedrockListPageSize = 100
	bedrockMaxListPages = 50
)

// loggingConfigPath is GetModelInvocationLoggingConfiguration (control plane). It is
// account+region scoped and takes no parameters.
const loggingConfigPath = "/logging/modelinvocations"

// --- Bedrock control-plane wire shapes (restJson1; only the fields we read) -------

// listGuardrailsResponse is GET /guardrails. With no identifier it lists the DRAFT
// of every guardrail; nextToken cursors the pages.
type listGuardrailsResponse struct {
	Guardrails []guardrailSummary `json:"guardrails"`
	NextToken  string             `json:"nextToken"`
}

// guardrailSummary is one ListGuardrails row. We read the id (the GetGuardrail key),
// the name (display) and the version (DRAFT for the un-versioned listing).
type guardrailSummary struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
}

// getGuardrailResponse is GET /guardrails/{id}. We map only the STATUS and the policy
// PRESENCE we reason over — never the topic definitions, custom words or regex
// patterns (those can carry an operator's own sensitive material; minimal-data keeps
// them out). The identity fields (id/name/version) come from the list summary, so they
// are not re-mapped here.
type getGuardrailResponse struct {
	Status string `json:"status"`

	ContentPolicy *struct {
		Filters []struct {
			Type string `json:"type"` // SEXUAL|VIOLENCE|HATE|INSULTS|MISCONDUCT|PROMPT_ATTACK
		} `json:"filters"`
	} `json:"contentPolicy"`
	TopicPolicy *struct {
		Topics []struct {
			Name string `json:"name"`
		} `json:"topics"`
	} `json:"topicPolicy"`
	WordPolicy *struct {
		Words            []struct{} `json:"words"`
		ManagedWordLists []struct{} `json:"managedWordLists"`
	} `json:"wordPolicy"`
	SensitiveInformationPolicy *struct {
		PiiEntities []struct{} `json:"piiEntities"`
		Regexes     []struct{} `json:"regexes"`
	} `json:"sensitiveInformationPolicy"`
	ContextualGroundingPolicy *struct {
		Filters []struct{} `json:"filters"`
	} `json:"contextualGroundingPolicy"`
}

// loggingConfigResponse is GET /logging/modelinvocations. A nil loggingConfig means
// logging was never configured (OFF).
type loggingConfigResponse struct {
	LoggingConfig *struct {
		CloudWatchConfig *struct {
			LogGroupName string `json:"logGroupName"`
		} `json:"cloudWatchConfig"`
		S3Config *struct {
			BucketName string `json:"bucketName"`
		} `json:"s3Config"`
		TextDataDeliveryEnabled  bool `json:"textDataDeliveryEnabled"`
		ImageDataDeliveryEnabled bool `json:"imageDataDeliveryEnabled"`
	} `json:"loggingConfig"`
}

// gatherBedrock runs the Bedrock safety-posture pass: list guardrails, read each
// guardrail's config posture (bounded), then read the decision-logging posture. A
// list/read failure is fatal to the pass so the caller records ONE health finding
// (a gap is a signal, not silence) and the other services still run.
func (s *Source) gatherBedrock(ctx context.Context, sink sdk.Sink, at time.Time) error {
	guardrails, listTruncated, err := s.listGuardrails(ctx)
	if err != nil {
		return err
	}
	scope := s.cfg.bedrockAccountScope()

	if len(guardrails) == 0 {
		// No provider-native guardrail governs this account's Bedrock model traffic in
		// this region — a posture gap a regulated estate should see.
		if err := emit(ctx, sink, bedrockPostureFinding(model.SeverityMedium, subjectBedrockGuardrail, scope,
			"No Bedrock guardrails configured in region "+s.cfg.region,
			"bedrock.guardrail account="+scope+" guardrails=0; no provider-native guardrail governs Bedrock model traffic in this region", at)); err != nil {
			return err
		}
	}

	// Honest "no silent caps" signal: if enumeration stopped at the page bound, or the
	// per-guardrail config reads are bounded below the discovered count, say so rather
	// than present a truncated posture as complete (docs/SECURITY-HARDENING.md).
	if listTruncated || len(guardrails) > s.cfg.maxGuardrails {
		read := len(guardrails)
		if read > s.cfg.maxGuardrails {
			read = s.cfg.maxGuardrails
		}
		if err := emit(ctx, sink, bedrockPostureFinding(model.SeverityLow, subjectBedrockGuardrail, scope,
			"Bedrock guardrail posture is PARTIAL — enumeration truncated; raise max_guardrails for full coverage",
			fmt.Sprintf("bedrock.guardrail account=%s coverage=partial read=%d list_truncated=%t; raise max_guardrails", scope, read, listTruncated), at)); err != nil {
			return err
		}
	}

	for i, g := range guardrails {
		if i >= s.cfg.maxGuardrails {
			break // bound the per-guardrail config reads
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.gatherGuardrailConfig(ctx, sink, g, at); err != nil {
			return err
		}
	}

	return s.gatherBedrockLogging(ctx, sink, scope, at)
}

// listGuardrails lists the guardrail summaries, following nextToken pagination up to
// the page bound. It returns truncated=true when it stopped at the page bound with a
// cursor still pending, so the caller can surface an honest partial-coverage finding.
func (s *Source) listGuardrails(ctx context.Context) ([]guardrailSummary, bool, error) {
	var out []guardrailSummary
	token := ""
	for page := 0; page < bedrockMaxListPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		q := url.Values{"maxResults": {strconv.Itoa(bedrockListPageSize)}}
		if token != "" {
			q.Set("nextToken", token)
		}
		var resp listGuardrailsResponse
		if err := s.bedrockGet(ctx, "/guardrails?"+q.Encode(), &resp); err != nil {
			return nil, false, err
		}
		out = append(out, resp.Guardrails...)
		if resp.NextToken == "" {
			return out, false, nil
		}
		token = resp.NextToken
	}
	return out, true, nil // stopped at the page bound with a cursor still pending
}

// gatherGuardrailConfig reads one guardrail's config and emits a single posture
// finding for it: Medium when it has a safety-config gap (status not READY, no content
// filters at all, or no PROMPT_ATTACK filter — the prompt-injection defense), else an
// Info summary of which policy families are present. One finding per guardrail keeps
// the volume bounded and the DetailHash stable across re-pulls (it hashes the config
// STATE, never a timestamp), so modules/security dedups an unchanged guardrail.
func (s *Source) gatherGuardrailConfig(ctx context.Context, sink sdk.Sink, g guardrailSummary, at time.Time) error {
	version := strings.TrimSpace(g.Version)
	if version == "" {
		version = "DRAFT"
	}
	var gr getGuardrailResponse
	path := "/guardrails/" + url.PathEscape(g.ID) + "?guardrailVersion=" + url.QueryEscape(version)
	if err := s.bedrockGet(ctx, path, &gr); err != nil {
		return err
	}

	content, topics, words, pii, grounding := guardrailPolicyCounts(gr)
	hasPromptAttack := guardrailHasPromptAttack(gr)

	var gaps []string
	sev := model.SeverityInfo
	if gr.Status != "" && !strings.EqualFold(gr.Status, "READY") {
		gaps = append(gaps, "status="+gr.Status+" (not READY)")
		sev = model.SeverityMedium
	}
	switch {
	case content == 0:
		gaps = append(gaps, "no content filters")
		sev = model.SeverityMedium
	case !hasPromptAttack:
		gaps = append(gaps, "no PROMPT_ATTACK (prompt-injection) content filter")
		sev = model.SeverityMedium
	}

	name := redact.Clean(g.Name)
	title := "Bedrock guardrail " + name + " active"
	if len(gaps) > 0 {
		title = "Bedrock guardrail " + name + " has safety-config gaps: " + strings.Join(gaps, ", ")
	}
	detail := fmt.Sprintf("bedrock.guardrail id=%s version=%s status=%s content=%d topic=%d word=%d pii=%d grounding=%d prompt_attack=%t gaps=%s",
		g.ID, version, gr.Status, content, topics, words, pii, grounding, hasPromptAttack, strings.Join(gaps, "|"))
	// bedrockPostureFinding cleans the subject ref, so pass g.ID raw (no double-clean).
	return emit(ctx, sink, bedrockPostureFinding(sev, subjectBedrockGuardrail, g.ID, title, detail, at))
}

// guardrailPolicyCounts returns the number of configured entries per policy family.
func guardrailPolicyCounts(gr getGuardrailResponse) (content, topics, words, pii, grounding int) {
	if gr.ContentPolicy != nil {
		content = len(gr.ContentPolicy.Filters)
	}
	if gr.TopicPolicy != nil {
		topics = len(gr.TopicPolicy.Topics)
	}
	if gr.WordPolicy != nil {
		words = len(gr.WordPolicy.Words) + len(gr.WordPolicy.ManagedWordLists)
	}
	if gr.SensitiveInformationPolicy != nil {
		pii = len(gr.SensitiveInformationPolicy.PiiEntities) + len(gr.SensitiveInformationPolicy.Regexes)
	}
	if gr.ContextualGroundingPolicy != nil {
		grounding = len(gr.ContextualGroundingPolicy.Filters)
	}
	return
}

// guardrailHasPromptAttack reports whether the content policy includes the
// PROMPT_ATTACK filter (Bedrock's prompt-injection / jailbreak defense).
func guardrailHasPromptAttack(gr getGuardrailResponse) bool {
	if gr.ContentPolicy == nil {
		return false
	}
	for _, f := range gr.ContentPolicy.Filters {
		if strings.EqualFold(f.Type, "PROMPT_ATTACK") {
			return true
		}
	}
	return false
}

// gatherBedrockLogging reads the model-invocation-logging configuration and emits the
// decision-AUDITABILITY posture: Info when logging is on (guardrail decisions are
// auditable, given trace), Medium when off. It is honest that Bedrock has no decision
// LIST api — historical ApplyGuardrail decisions are only auditable via these logs
// (plus caller-side trace=ENABLED), and ingesting them is a follow-up.
func (s *Source) gatherBedrockLogging(ctx context.Context, sink sdk.Sink, scope string, at time.Time) error {
	var lc loggingConfigResponse
	if err := s.bedrockGet(ctx, loggingConfigPath, &lc); err != nil {
		return err
	}
	enabled := lc.LoggingConfig != nil &&
		(lc.LoggingConfig.CloudWatchConfig != nil || lc.LoggingConfig.S3Config != nil) &&
		(lc.LoggingConfig.TextDataDeliveryEnabled || lc.LoggingConfig.ImageDataDeliveryEnabled)

	if enabled {
		return emit(ctx, sink, bedrockPostureFinding(model.SeverityInfo, subjectBedrockLogging, scope,
			"Bedrock model-invocation logging enabled (guardrail decisions auditable when trace is on)",
			"bedrock.logging account="+scope+" enabled=true; ApplyGuardrail/invocation decisions are captured to CloudWatch/S3 when the caller sends trace=ENABLED (Bedrock has no decision-list API; historical-decision ingestion is a follow-up)", at))
	}
	return emit(ctx, sink, bedrockPostureFinding(model.SeverityMedium, subjectBedrockLogging, scope,
		"Bedrock model-invocation logging disabled — guardrail decisions are not auditable",
		"bedrock.logging account="+scope+" enabled=false; ApplyGuardrail/invocation guardrail decisions are NOT captured (Bedrock exposes no decision-list API; enabling model-invocation logging + caller trace=ENABLED is the only audit path — historical-decision ingestion is a follow-up)", at))
}

// bedrockPostureFinding builds a safety-posture FindingReport. The DetailHash is over
// a state-deterministic fingerprint (no timestamp) so an unchanged posture dedups in
// modules/security while a real config change surfaces a fresh finding.
func bedrockPostureFinding(sev model.Severity, subjectKind, subjectRef, title, detail string, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        safetyPostureKind,
		Severity:    sev,
		SubjectKind: subjectKind,
		SubjectRef:  redact.Clean(subjectRef),
		Title:       title,
		DetailHash:  redact.Hash(detail),
		OccurredAt:  at,
	}
}

// bedrockGet issues one SigV4-signed GET to the Bedrock control plane and decodes the
// JSON response. The path may carry a query string (signed as part of the canonical
// request). It is read-only: a GET with no body, signed for the "bedrock" service in
// the operating region.
func (s *Source) bedrockGet(ctx context.Context, path string, out any) error {
	endpoint := strings.TrimRight(s.cfg.bedrockEndpoint, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	sign(req, nil, bedrockSigningService, s.cfg.region, s.cfg.creds, time.Now())

	resp, err := s.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bedrock: GET %s returned status %d", reqPathForError(path), resp.StatusCode)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("bedrock: decode %s response: %w", reqPathForError(path), err)
	}
	return nil
}

// reqPathForError returns the path without its query string, so an error never echoes
// a (potentially token-bearing) nextToken cursor.
func reqPathForError(path string) string {
	if i := strings.IndexByte(path, '?'); i >= 0 {
		return path[:i]
	}
	return path
}
