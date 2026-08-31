// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package contentsource

// CompletenessReporter is an OPTIONAL capability a Source implements when its listing
// (List / ListPage) can be INCOMPLETE — e.g. a filesystem source whose tree walk was
// cut short by an I/O budget or a transient read error on part of the tree.
//
// It exists to protect orphan-based deletion. A full reconciliation deletes any stored
// document whose DocID the source no longer lists; that is only safe when "absent from
// the listing" genuinely means "removed from the source". If the listing is a partial
// view, an absent doc may simply be one the source failed to enumerate this round, and
// deleting it would destroy data on a transient blip — the "a source outage must never
// be mistaken for every-document-deleted" invariant. A consumer that reconciles
// deletions MUST skip them when ListingComplete() reports false.
//
// A Source that does NOT implement this capability is treated as always-complete: its
// listing is taken as authoritative (the historical behavior for API-backed sources
// that either return a full page set or fail the call outright).
type CompletenessReporter interface {
	// ListingComplete reports whether the most recent listing enumerated the whole
	// source. False means the view is known-partial and orphan deletion must be
	// withheld until a complete listing is available.
	ListingComplete() bool
}
