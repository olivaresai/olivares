//go:build !enterprise || !addon_ids

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package spiffe

import (
	"context"
	"errors"
)

var errAnthropicFederationUnavailable = errors.New("Anthropic Workload Identity Federation requires Business")

func AssertNoStaticKeyShadowing(func(string) (string, bool)) error {
	return errAnthropicFederationUnavailable
}
func FetchAnthropicAssertion(context.Context, jwtFetcher, func(string) (string, bool)) (string, error) {
	return "", errAnthropicFederationUnavailable
}
func InspectAnthropicAssertion(string) (SVIDClaims, error) {
	return SVIDClaims{}, errAnthropicFederationUnavailable
}
