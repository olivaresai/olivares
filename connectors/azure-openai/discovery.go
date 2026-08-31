// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// This file is the shared ARM enumeration both the catalog (Snapshot) and the usage pass
// build on: subscriptions → Cognitive Services accounts (filtered to the LLM-hosting kinds)
// → per-account deployments and the deployable-model catalog. Every read is a GET that
// follows the Azure nextLink (a full URL) up to the page bound. It reads only inventory
// METADATA — names, model format/version, sku/capacity, lifecycle — never a key value.
package azureopenai

import (
	"context"
	"net/url"
	"strings"
)

// resolveSubscriptions returns the subscription ids to operate on: the explicit config
// list, or every enabled subscription the principal can see (auto-listed via the
// management API, following nextLink up to max_pages).
func (s *Source) resolveSubscriptions(ctx context.Context) ([]string, error) {
	if len(s.cfg.subscriptions) > 0 {
		return s.cfg.subscriptions, nil
	}
	var out []string
	q := url.Values{"api-version": {s.cfg.subsAPIVersion}}
	full := s.armURL("/subscriptions", q)
	for page := 0; page < s.cfg.maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var resp subscriptionsResponse
		if err := s.getURL(ctx, full, &resp); err != nil {
			return nil, err
		}
		for _, sub := range resp.Value {
			if sub.SubscriptionID != "" && subscriptionEnabled(sub.State) {
				out = append(out, sub.SubscriptionID)
			}
		}
		if resp.NextLink == "" {
			break
		}
		full = resp.NextLink
	}
	return out, nil
}

// subscriptionEnabled reports whether a subscription state is usable. An empty state is
// treated as enabled (some responses omit it).
func subscriptionEnabled(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "", "enabled", "active", "warned":
		return true
	default:
		return false
	}
}

// listAccounts lists the Cognitive Services accounts in a subscription, filtered to the
// configured LLM-hosting kinds (OpenAI / AIServices), following nextLink pagination.
func (s *Source) listAccounts(ctx context.Context, sub string) ([]account, error) {
	q := url.Values{"api-version": {s.cfg.armAPIVersion}}
	full := s.armURL("/subscriptions/"+url.PathEscape(sub)+"/providers/Microsoft.CognitiveServices/accounts", q)
	var out []account
	for page := 0; page < s.cfg.maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var resp accountsResponse
		if err := s.getURL(ctx, full, &resp); err != nil {
			return nil, err
		}
		for _, a := range resp.Value {
			if s.accountKindEnabled(a.Kind) {
				out = append(out, a)
			}
		}
		if resp.NextLink == "" {
			break
		}
		full = resp.NextLink
	}
	return out, nil
}

// accountKindEnabled reports whether a Cognitive Services account kind hosts LLM
// deployments per the configured allow-list (kind is a free-form string filtered
// client-side).
func (s *Source) accountKindEnabled(kind string) bool {
	k := strings.ToLower(strings.TrimSpace(kind))
	for _, want := range s.cfg.accountKinds {
		if k == want {
			return true
		}
	}
	return false
}

// listDeployments lists one account's model deployments (accountID is the full ARM
// resource id), following nextLink pagination.
func (s *Source) listDeployments(ctx context.Context, accountID string) ([]deployment, error) {
	q := url.Values{"api-version": {s.cfg.armAPIVersion}}
	full := s.armURL(accountID+"/deployments", q)
	var out []deployment
	for page := 0; page < s.cfg.maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var resp deploymentsResponse
		if err := s.getURL(ctx, full, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Value...)
		if resp.NextLink == "" {
			break
		}
		full = resp.NextLink
	}
	return out, nil
}

// listAccountModels lists one account's deployable-model catalog (lifecycle + deprecation),
// following nextLink pagination. A read failure is non-fatal to the caller (the deployment
// inventory already stands); it returns the error so the caller can decide.
func (s *Source) listAccountModels(ctx context.Context, accountID string) ([]accountModel, error) {
	q := url.Values{"api-version": {s.cfg.armAPIVersion}}
	full := s.armURL(accountID+"/models", q)
	var out []accountModel
	for page := 0; page < s.cfg.maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var resp accountModelsResponse
		if err := s.getURL(ctx, full, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Value...)
		if resp.NextLink == "" {
			break
		}
		full = resp.NextLink
	}
	return out, nil
}
