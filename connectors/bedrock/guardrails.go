// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// This file reads the Amazon Bedrock GUARDRAILS configuration and the model-invocation-
// logging (decision-auditability) posture, and emits them as read-only
// FindingReport{Kind:"safety_posture"} on the contract defined (SubjectKind
// bedrock.guardrail / bedrock.logging; DetailHash over the config STATE so an unchanged
// posture dedups in modules/security). It is the same posture pattern the aws connector
// uses — this connector consolidates Bedrock observability and additionally reads
// the Automated Reasoning policy that the aws connector does not.
//
// Two honesty boundaries: it is READ-FIRST — it never calls the paid
// bedrock-runtime ApplyGuardrail POST; and Bedrock exposes NO list API for past
// guardrail DECISIONS, so instead of fabricating a decision feed it reports the
// AUDITABILITY posture (is model-invocation logging on?).
//
// NOTE: enable Guardrails posture on EXACTLY ONE Bedrock connector (this `bedrock`
// connector OR the `aws` connector's enable_bedrock). Running both against the same
// account double-reads the same guardrails; the findings carry the same SubjectKind/
// SubjectRef so the consumer treats them uniformly, but the reads are redundant.
package bedrock

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// bedrockListPageSize is the ListGuardrails page size (the API caps maxResults at 1000);
// bedrockMaxListPages bounds pagination defensively.
const (
	bedrockListPageSize = 100
	bedrockMaxListPages = 50
)

// loggingConfigPath is GetModelInvocationLoggingConfiguration (control plane). It is
// account+region scoped and takes no parameters.
const loggingConfigPath = "/logging/modelinvocations"

// --- Bedrock control-plane wire shapes (restJson1; only the fields we read) -------

type listGuardrailsResponse struct {
	Guardrails []guardrailSummary `json:"guardrails"`
	NextToken  string             `json:"nextToken"`
}

// guardrailSummary is one ListGuardrails row (the summary uses `id`/`arn`, distinct from
// GetGuardrail's `guardrailId`/`guardrailArn`).
type guardrailSummary struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Status  string `json:"status"`
}

// getGuardrailResponse maps only the STATUS and the policy PRESENCE we reason over —
// never the topic definitions, custom words or regex patterns (those can carry an
// operator's own sensitive material; minimal-data keeps them out). The Automated
// Reasoning policy is read as presence + count of attached policy ARNs (the ARNs are
// resource refs, not content).
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
	// AutomatedReasoningPolicy is present only when AR is configured; policies is a
	// non-empty list of automated-reasoning-policy ARNs (1–2). This is the field the aws
	// connector does not read.
	AutomatedReasoningPolicy *struct {
		Policies []string `json:"policies"`
	} `json:"automatedReasoningPolicy"`
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

// gatherGuardrails runs the Bedrock guardrails posture pass: list guardrails, read each
// guardrail's config posture (bounded), then read the decision-logging posture. A
// list/read failure is fatal to the pass so the caller records ONE health finding (a gap
// is a signal, not silence).
func (s *Source) gatherGuardrails(ctx context.Context, sink sdk.Sink, at time.Time) error {
	guardrails, listTruncated, err := s.listGuardrails(ctx)
	if err != nil {
		return err
	}
	scope := s.cfg.accountScope()

	if len(guardrails) == 0 {
		// No provider-native guardrail governs this account's Bedrock traffic in this
		// region — a posture gap a regulated estate should see (absence IS the posture).
		if err := emit(ctx, sink, postureFinding(model.SeverityMedium, subjectBedrockGuardrail, scope,
			"No Bedrock guardrails configured in region "+s.cfg.region,
			"bedrock.guardrail account="+scope+" guardrails=0; no provider-native guardrail governs Bedrock model traffic in this region", at)); err != nil {
			return err
		}
	}

	// Honest "no silent caps": if enumeration stopped at the page bound, or the per-
	// guardrail reads are bounded below the discovered count, say so (docs/SECURITY-HARDENING.md).
	if listTruncated || len(guardrails) > s.cfg.maxGuardrails {
		read := len(guardrails)
		if read > s.cfg.maxGuardrails {
			read = s.cfg.maxGuardrails
		}
		if err := emit(ctx, sink, postureFinding(model.SeverityLow, subjectBedrockGuardrail, scope,
			"Bedrock guardrail posture is PARTIAL — enumeration truncated; raise max_guardrails for full coverage",
			fmt.Sprintf("bedrock.guardrail account=%s coverage=partial read=%d list_truncated=%t; raise max_guardrails", scope, read, listTruncated), at)); err != nil {
			return err
		}
	}

	for i, g := range guardrails {
		if i >= s.cfg.maxGuardrails {
			break
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.gatherGuardrailConfig(ctx, sink, g, at); err != nil {
			return err
		}
	}

	return s.gatherGuardrailLogging(ctx, sink, scope, at)
}

// listGuardrails lists the guardrail summaries, following nextToken pagination up to the
// page bound. It returns truncated=true when it stopped at the page bound with a cursor
// still pending, so the caller can surface an honest partial-coverage finding.
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
	return out, true, nil
}

// gatherGuardrailConfig reads one guardrail's config and emits a single posture finding:
// Medium when it has a safety-config gap (status not READY, no content filters, or no
// PROMPT_ATTACK filter — the prompt-injection defense), else an Info summary of which
// policy families are present (including whether Automated Reasoning is attached). One
// finding per guardrail keeps the volume bounded and the DetailHash stable across re-
// pulls (it hashes the config STATE, never a timestamp), so an unchanged guardrail
// dedups.
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
	autoReasoning := guardrailHasAutomatedReasoning(gr)

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
	// Automated Reasoning is an advanced, optional control — its absence is NOT a gap;
	// its presence is recorded so a regulated estate can evidence it.
	detail := fmt.Sprintf("bedrock.guardrail id=%s version=%s status=%s content=%d topic=%d word=%d pii=%d grounding=%d prompt_attack=%t automated_reasoning=%t gaps=%s",
		g.ID, version, gr.Status, content, topics, words, pii, grounding, hasPromptAttack, autoReasoning, strings.Join(gaps, "|"))
	// postureFinding cleans the subject ref, so pass g.ID raw (no double-clean).
	return emit(ctx, sink, postureFinding(sev, subjectBedrockGuardrail, g.ID, title, detail, at))
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

// guardrailHasPromptAttack reports whether the content policy includes the PROMPT_ATTACK
// filter (Bedrock's prompt-injection / jailbreak defense).
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

// guardrailHasAutomatedReasoning reports whether an Automated Reasoning policy is
// attached (present AND with at least one policy ARN).
func guardrailHasAutomatedReasoning(gr getGuardrailResponse) bool {
	return gr.AutomatedReasoningPolicy != nil && len(gr.AutomatedReasoningPolicy.Policies) > 0
}

// gatherGuardrailLogging reads the model-invocation-logging configuration and emits the
// decision-AUDITABILITY posture: Info when logging is on (guardrail decisions are
// auditable, given trace), Medium when off. It is honest that Bedrock has no decision
// LIST api — historical ApplyGuardrail decisions are only auditable via these logs (plus
// caller-side trace=ENABLED), and ingesting them is a follow-up.
func (s *Source) gatherGuardrailLogging(ctx context.Context, sink sdk.Sink, scope string, at time.Time) error {
	var lc loggingConfigResponse
	if err := s.bedrockGet(ctx, loggingConfigPath, &lc); err != nil {
		return err
	}
	enabled := lc.LoggingConfig != nil &&
		(lc.LoggingConfig.CloudWatchConfig != nil || lc.LoggingConfig.S3Config != nil) &&
		(lc.LoggingConfig.TextDataDeliveryEnabled || lc.LoggingConfig.ImageDataDeliveryEnabled)

	if enabled {
		return emit(ctx, sink, postureFinding(model.SeverityInfo, subjectBedrockLogging, scope,
			"Bedrock model-invocation logging enabled (guardrail decisions auditable when trace is on)",
			"bedrock.logging account="+scope+" enabled=true; ApplyGuardrail/invocation decisions are captured to CloudWatch/S3 when the caller sends trace=ENABLED (Bedrock has no decision-list API; historical-decision ingestion is a follow-up)", at))
	}
	return emit(ctx, sink, postureFinding(model.SeverityMedium, subjectBedrockLogging, scope,
		"Bedrock model-invocation logging disabled — guardrail decisions are not auditable",
		"bedrock.logging account="+scope+" enabled=false; ApplyGuardrail/invocation guardrail decisions are NOT captured (Bedrock exposes no decision-list API; enabling model-invocation logging + caller trace=ENABLED is the only audit path — historical-decision ingestion is a follow-up)", at))
}
