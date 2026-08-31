// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package model

// SETSeenJTI records a SET jti that the receiver has already processed, for
// RFC 8417 duplicate suppression. It is append-only and expires after the
// publisher's MaxAgeSeconds (cleaned up periodically). It lives in the
// system tenant alongside other auth-partition entities.
type SETSeenJTI struct {
	BaseFields
	// JTI is the unique SET identifier (RFC 8417 jti claim).
	JTI string
	// PublisherID identifies the SET publisher that issued this event.
	PublisherID string
	// ExpiresAt is when this record may be garbage-collected.
	ExpiresAt Timestamp
}
