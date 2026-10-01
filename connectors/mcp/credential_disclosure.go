// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import "errors"

// ErrUpstreamCredentialDisclosure marks a refused upstream response. It carries
// no upstream bytes or credential and lets the gateway audit even scope-free
// discovery methods without releasing the refused content.
var ErrUpstreamCredentialDisclosure = errors.New("mcp: upstream credential disclosure refused")

// Credential admission failures are proven before any upstream transmission.
// Their fixed messages are configuration reasons, never credential values.
var (
	ErrUpstreamCredentialTooShort = errors.New("mcp: upstream authentication secret must contain at least 8 bytes")
	ErrUpstreamCredentialInvalid  = errors.New("mcp: upstream Basic authentication must encode a valid user:password pair")
)

func upstreamCredentialRefusalReason(err error) string {
	if errors.Is(err, ErrUpstreamCredentialTooShort) {
		return "upstream credential shorter than 8 bytes; request not sent"
	}
	if errors.Is(err, ErrUpstreamCredentialInvalid) {
		return "upstream Basic credential is invalid; request not sent"
	}
	return "upstream credential disclosure refused"
}
