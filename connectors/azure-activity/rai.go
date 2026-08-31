// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// This file adds the read-only Azure AI Foundry / Azure OpenAI Responsible-AI (RAI)
// content-filter SAFETY-POSTURE surface, reusing this connector's ARM token,
// getJSON helper and subscription enumeration. It reads, per Cognitive Services
// account, the RAI POLICIES (the configurable content filters: Hate/Sexual/Violence/
// Selfharm per Prompt/Completion, plus jailbreak/protected-material) and the model
// DEPLOYMENTS' bindings to them, and emits minimal-data FindingReport{Kind:
// "safety_posture"} — the posture-findings pattern of claude-api/governance.go.
//
// Honesty boundaries, designed in:
//   - A model deployment with NO raiPolicyName is NOT "unfiltered": Azure applies the
//     platform default (Microsoft.Default, which blocks the four harm categories at
//     Medium). We report it as "uses the default policy, not customer-managed", never
//     as a gap (the research-verified semantics; reporting it as "off" would be false).
//   - The per-request content_filter_results / prompt_filter_results are INFERENCE-TIME
//     response annotations — there is NO management API to query past filter decisions.
//     So we do not fabricate a decision feed; we emit one honest note that historical
//     content-filter decision audit is a follow-up (the inference-proxy path).
//   - A read that fails on a credentialed account degrades HONESTLY to an "unreadable"
//     posture finding, never a fabricated green.
package azureactivity

import (
	"context"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// --- ARM wire shapes (only the fields we read) ------------------------------------

// accountsResponse is the Cognitive Services accounts list at subscription scope.
type accountsResponse struct {
	Value    []cognitiveAccount `json:"value"`
	NextLink string             `json:"nextLink"`
}

// cognitiveAccount is one Cognitive Services account. ID is the full ARM resource id
// (it embeds the resource group), which we use verbatim as the base path for the
// account's raiPolicies/deployments sub-resources.
type cognitiveAccount struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// raiPoliciesResponse is .../accounts/{a}/raiPolicies.
type raiPoliciesResponse struct {
	Value    []raiPolicy `json:"value"`
	NextLink string      `json:"nextLink"`
}

// raiPolicy is one Responsible-AI content-filter policy.
type raiPolicy struct {
	Name       string `json:"name"`
	Properties struct {
		Type           string             `json:"type"` // UserManaged|SystemManaged
		Mode           string             `json:"mode"`
		BasePolicyName string             `json:"basePolicyName"`
		ContentFilters []raiContentFilter `json:"contentFilters"`
	} `json:"properties"`
}

// raiContentFilter is one filter row (a harm category at one source/threshold).
type raiContentFilter struct {
	Name              string `json:"name"`
	Enabled           bool   `json:"enabled"`
	Blocking          bool   `json:"blocking"`
	SeverityThreshold string `json:"severityThreshold"`
	Source            string `json:"source"` // Prompt|Completion
}

// deploymentsResponse is .../accounts/{a}/deployments.
type deploymentsResponse struct {
	Value    []raiDeployment `json:"value"`
	NextLink string          `json:"nextLink"`
}

// raiDeployment is one model deployment; raiPolicyName is its RAI-policy binding
// (empty ⇒ the platform default Microsoft.Default).
type raiDeployment struct {
	Name       string `json:"name"`
	Properties struct {
		RaiPolicyName string `json:"raiPolicyName"`
	} `json:"properties"`
}

// gatherRAI runs the Azure RAI posture pass across the subscription set. It first
// emits the honest content_filter_results note, then, per subscription, lists the
// Cognitive Services accounts and reads each AI account's RAI policies + deployment
// bindings. A subscription-level account-listing failure is fatal to the pass (one
// Gather health finding); a per-account read failure degrades to an "unreadable"
// posture finding and the pass continues.
func (s *Source) gatherRAI(ctx context.Context, sink sdk.Sink, subs []string, at time.Time) error {
	if err := emit(ctx, sink, s.contentFilterHonestyFinding(at)); err != nil {
		return err
	}
	truncated := false
	for _, sub := range subs {
		if err := ctx.Err(); err != nil {
			return err
		}
		accounts, accTrunc, err := s.listCognitiveAccounts(ctx, sub)
		if err != nil {
			return err
		}
		truncated = truncated || accTrunc
		for _, acct := range accounts {
			if !raiCapableKind(acct.Kind) {
				continue // only OpenAI/AIServices accounts carry RAI policies
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			acctTrunc, err := s.gatherAccountRAI(ctx, sink, acct, at)
			if err != nil {
				return err
			}
			truncated = truncated || acctTrunc
		}
	}
	// Honest "no silent caps" signal (docs/SECURITY-HARDENING.md): if any RAI list stopped at the page
	// bound, say the posture is partial rather than presenting it as complete.
	if truncated {
		return emit(ctx, sink, raiPostureFinding(model.SeverityLow, subjectRAIPolicy, s.tenantRef(),
			"Azure RAI posture is PARTIAL — a list was truncated at max_pages; raise max_pages for full coverage",
			"azure.rai_policy coverage=partial; an accounts/policies/deployments list stopped at the max_pages bound", at))
	}
	return nil
}

// gatherAccountRAI reads one account's RAI policies + deployment bindings and emits a
// posture finding per policy and per deployment. A policy-read failure degrades to one
// "unreadable" Medium finding (honest, never a false green); a deployment-read failure
// after policies succeeded is non-fatal (the policy posture already stands).
func (s *Source) gatherAccountRAI(ctx context.Context, sink sdk.Sink, acct cognitiveAccount, at time.Time) (bool, error) {
	policies, polTrunc, err := s.listRAIPolicies(ctx, acct.ID)
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, emit(ctx, sink, raiPostureFinding(model.SeverityMedium, subjectRAIPolicy, acct.Name,
			"Azure RAI posture unreadable for account "+redact.Clean(acct.Name),
			"azure.rai_policy account="+acct.Name+" unreadable (permission/availability); posture not asserted", at))
	}
	for _, p := range policies {
		if err := emit(ctx, sink, raiPolicyFinding(acct, p, at)); err != nil {
			return false, err
		}
	}

	deployments, depTrunc, err := s.listDeployments(ctx, acct.ID)
	if err != nil {
		if ctx.Err() != nil {
			return polTrunc, ctx.Err()
		}
		return polTrunc, nil // policies already emitted; an unreadable deployment list is non-fatal
	}
	for _, d := range deployments {
		if err := emit(ctx, sink, deploymentFinding(acct, d, at)); err != nil {
			return polTrunc || depTrunc, err
		}
	}
	return polTrunc || depTrunc, nil
}

// raiPolicyFinding builds the posture finding for one RAI policy: Medium when a harm
// category is explicitly DISABLED (enabled:false — filtering off), Low when a harm
// category is enabled but annotate-only (not blocking), else an Info summary. A harm
// category with NO row is treated as inheriting the base policy (the 2024-10-01 API
// returns the full per-category/source set with explicit enabled/blocking flags — a
// category is toggled off, not omitted — so absence is not a gap). The DetailHash is
// over the policy's config STATE (no timestamp) so an unchanged policy dedups in
// modules/security and a real weakening surfaces a fresh finding.
func raiPolicyFinding(acct cognitiveAccount, p raiPolicy, at time.Time) model.FindingReport {
	subjectRef := acct.Name + "/" + p.Name
	var disabled, annotateOnly []string
	for _, cf := range p.Properties.ContentFilters {
		if !isHarmCategory(cf.Name) {
			continue
		}
		switch {
		case !cf.Enabled:
			disabled = append(disabled, cf.Name+"/"+cf.Source)
		case !cf.Blocking:
			annotateOnly = append(annotateOnly, cf.Name+"/"+cf.Source)
		}
	}
	sort.Strings(disabled)
	sort.Strings(annotateOnly)

	sev := model.SeverityInfo
	title := "Azure RAI policy " + subjectRef + " active (" + policyType(p) + ")"
	switch {
	case len(disabled) > 0:
		sev = model.SeverityMedium
		title = "Azure RAI policy " + subjectRef + " disables harm filtering: " + strings.Join(disabled, ", ")
	case len(annotateOnly) > 0:
		sev = model.SeverityLow
		title = "Azure RAI policy " + subjectRef + " is annotate-only (not blocking) for: " + strings.Join(annotateOnly, ", ")
	}
	detail := "azure.rai_policy account=" + acct.Name + " name=" + p.Name + " type=" + policyType(p) +
		" mode=" + p.Properties.Mode + " base=" + p.Properties.BasePolicyName +
		" disabled=[" + strings.Join(disabled, "|") + "] annotate_only=[" + strings.Join(annotateOnly, "|") + "]"
	return raiPostureFinding(sev, subjectRAIPolicy, subjectRef, title, detail, at)
}

// deploymentFinding records a deployment's RAI-policy binding. An absent raiPolicyName
// is the PLATFORM DEFAULT (Microsoft.Default), not "no filtering" — so it is Info, not
// a gap (honesty, per the verified Azure semantics).
func deploymentFinding(acct cognitiveAccount, d raiDeployment, at time.Time) model.FindingReport {
	subjectRef := acct.Name + "/" + d.Name
	policy := strings.TrimSpace(d.Properties.RaiPolicyName)
	if policy == "" {
		return raiPostureFinding(model.SeverityInfo, subjectDeployment, subjectRef,
			"Azure deployment "+subjectRef+" uses the default RAI policy (Microsoft.Default)",
			"azure.deployment account="+acct.Name+" name="+d.Name+" rai_policy=Microsoft.Default(default)", at)
	}
	return raiPostureFinding(model.SeverityInfo, subjectDeployment, subjectRef,
		"Azure deployment "+subjectRef+" bound to RAI policy "+policy,
		"azure.deployment account="+acct.Name+" name="+d.Name+" rai_policy="+policy, at)
}

// contentFilterHonestyFinding records, once per pass, that per-request content-filter
// decisions are inference-time annotations and not management-API readable.
func (s *Source) contentFilterHonestyFinding(at time.Time) model.FindingReport {
	detail := "azure.content_filter: per-request content_filter_results/prompt_filter_results are inference-time response annotations, " +
		"NOT retrievable via the Azure management API; historical content-filter decision audit is a follow-up (the inference-proxy path), not this read-only posture connector"
	return raiPostureFinding(model.SeverityInfo, subjectContentFilter, s.tenantRef(),
		"Azure content-filter decisions are inference-time annotations, not management-API readable", detail, at)
}

// raiPostureFinding builds a safety-posture FindingReport with a state-deterministic
// DetailHash (no timestamp), so an unchanged posture dedups in modules/security.
func raiPostureFinding(sev model.Severity, subjectKind, subjectRef, title, detail string, at time.Time) model.FindingReport {
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

// listCognitiveAccounts lists the Cognitive Services accounts in a subscription,
// following nextLink pagination up to the page bound. truncated is true when the loop
// stopped at the bound with a nextLink still pending (a partial result).
func (s *Source) listCognitiveAccounts(ctx context.Context, sub string) ([]cognitiveAccount, bool, error) {
	q := url.Values{"api-version": {s.cfg.raiAPIVersion}}
	full := strings.TrimRight(s.cfg.managementEndpoint, "/") +
		"/subscriptions/" + sub + "/providers/Microsoft.CognitiveServices/accounts?" + q.Encode()
	var out []cognitiveAccount
	for page := 0; page < s.cfg.maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		var resp accountsResponse
		if err := s.getURL(ctx, full, &resp); err != nil {
			return nil, false, err
		}
		out = append(out, resp.Value...)
		if resp.NextLink == "" {
			return out, false, nil
		}
		full = resp.NextLink
	}
	return out, true, nil // stopped at the page bound with a nextLink still pending
}

// listRAIPolicies lists an account's RAI policies (accountID is a full ARM resource
// path; raiPolicies is its sub-resource), following nextLink pagination. truncated is
// true when it stopped at the page bound with more pending.
func (s *Source) listRAIPolicies(ctx context.Context, accountID string) ([]raiPolicy, bool, error) {
	q := url.Values{"api-version": {s.cfg.raiAPIVersion}}
	full := strings.TrimRight(s.cfg.managementEndpoint, "/") + accountID + "/raiPolicies?" + q.Encode()
	var out []raiPolicy
	for page := 0; page < s.cfg.maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		var resp raiPoliciesResponse
		if err := s.getURL(ctx, full, &resp); err != nil {
			return nil, false, err
		}
		out = append(out, resp.Value...)
		if resp.NextLink == "" {
			return out, false, nil
		}
		full = resp.NextLink
	}
	return out, true, nil
}

// listDeployments lists an account's model deployments, following nextLink pagination.
// truncated is true when it stopped at the page bound with more pending.
func (s *Source) listDeployments(ctx context.Context, accountID string) ([]raiDeployment, bool, error) {
	q := url.Values{"api-version": {s.cfg.raiAPIVersion}}
	full := strings.TrimRight(s.cfg.managementEndpoint, "/") + accountID + "/deployments?" + q.Encode()
	var out []raiDeployment
	for page := 0; page < s.cfg.maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		var resp deploymentsResponse
		if err := s.getURL(ctx, full, &resp); err != nil {
			return nil, false, err
		}
		out = append(out, resp.Value...)
		if resp.NextLink == "" {
			return out, false, nil
		}
		full = resp.NextLink
	}
	return out, true, nil
}

// raiCapableKind reports whether a Cognitive Services account kind hosts RAI policies
// (Azure OpenAI / multi-service AI). Other kinds (Speech, Vision, ContentSafety, …)
// have no raiPolicies sub-resource, so we skip them rather than 404 per account.
func raiCapableKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "openai", "aiservices":
		return true
	default:
		return false
	}
}

// policyType returns the RAI policy's management type, defaulting to "unknown" when
// the read-only output field is omitted (some api-versions omit it on the list shape).
func policyType(p raiPolicy) string {
	if t := strings.TrimSpace(p.Properties.Type); t != "" {
		return t
	}
	return "unknown"
}

// isHarmCategory reports whether an RAI content-filter name is one of the four severity
// harm categories (Hate/Sexual/Violence/Selfharm). It normalizes spelling (spaces,
// hyphens, case) because the wire spelling varies ("Selfharm" vs "Self-harm"). The
// binary classifiers (Jailbreak, Protected Material, Profanity) are intentionally NOT
// harm categories here — they have no severity threshold and a different default.
func isHarmCategory(name string) bool {
	n := strings.ToLower(strings.NewReplacer(" ", "", "-", "", "_", "").Replace(strings.TrimSpace(name)))
	switch n {
	case "hate", "sexual", "violence", "selfharm":
		return true
	default:
		return false
	}
}
