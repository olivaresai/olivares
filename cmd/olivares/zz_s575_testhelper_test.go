// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import "github.com/olivaresai/olivares/core/auth"

// mustTestOperator builds an ATTRIBUTABLE privileged principal for a fixture.
// The anonymous auth.Principal{Kind: KindUser, Superadmin: true, DisplayName: …}
// these tests used to build is now refused by the ledger (B-12): it produced
// the audit subject "user:" — a prefix and nothing else — which is precisely the
// shape a fixture should not have been able to stand in for.
func mustTestOperator(subject string) auth.Principal {
	p, err := auth.NewLocalOperator(auth.LocalOperator{Subject: subject, Via: "test", Reason: "fixture"})
	if err != nil {
		panic(err)
	}
	return p
}
