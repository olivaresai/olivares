// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package release

import "testing"

// Business X.Y is cut from the same version as Community X.Y: the edition is part of
// "up to date", but only for the move the licensed route performs. An installed edition that
// was not read ("unknown") cannot be told from Community, so it is moved too. An older release
// stays a rollback whatever its edition, so anti-rollback and min_version keep reading versions.
func TestUpgradePlanEditionMove(t *testing.T) {
	const business = "enterprise"
	cases := []struct {
		name         string
		known        bool
		direction    int
		from, to     string
		wantUpToDate bool
		wantMove     bool
		wantRollback bool
	}{
		{name: "Community to Business at the same version", known: true, from: EditionCommunity, to: business, wantMove: true},
		{name: "Business to Business at the same version", known: true, from: business, to: business, wantUpToDate: true},
		{name: "Community to Community at the same version", known: true, from: EditionCommunity, to: EditionCommunity, wantUpToDate: true},
		{name: "no edition claim (public channel, bundle) keeps the version-only answer", known: true, wantUpToDate: true},
		{name: "an edition that was not read is moved like Community", known: true, from: EditionUnknown, to: business, wantMove: true},
		{name: "an unread edition under a newer release is an upgrade", known: true, direction: 1, from: EditionUnknown, to: business, wantMove: true},
		{name: "an unread edition under an older release is a rollback", known: true, direction: -1, from: EditionUnknown, to: business, wantMove: true, wantRollback: true},
		{name: "Business over Community is not a move to install", known: true, from: business, to: EditionCommunity, wantUpToDate: true},
		{name: "an older Business release is a rollback, not a move", known: true, direction: -1, from: EditionCommunity, to: business, wantMove: true, wantRollback: true},
		{name: "a newer Business release is an upgrade", known: true, direction: 1, from: EditionCommunity, to: business, wantMove: true},
		{name: "an unknown current version makes no claim", from: EditionCommunity, to: business, wantMove: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := UpgradePlan{CurrentKnown: tc.known, Direction: tc.direction, FromEdition: tc.from, ToEdition: tc.to}
			if got := p.EditionMove(); got != tc.wantMove {
				t.Errorf("EditionMove() = %v, want %v", got, tc.wantMove)
			}
			if got := p.IsUpToDate(); got != tc.wantUpToDate {
				t.Errorf("IsUpToDate() = %v, want %v", got, tc.wantUpToDate)
			}
			if got := p.IsRollback(); got != tc.wantRollback {
				t.Errorf("IsRollback() = %v, want %v", got, tc.wantRollback)
			}
		})
	}
}
