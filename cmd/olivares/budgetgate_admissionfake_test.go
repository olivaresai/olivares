// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import "github.com/olivaresai/olivares/modules/finops"

// fakeAdmissionReserve maps a CheckBudget stub onto Reserve, so the gate tests that program
// a BudgetCheck keep exercising the admission interface. It answers as the module does: a
// check that could not be made is a deny-closed refusal with a nil error, and only the
// explicit allow posture admits, with no hold and the cause beside it.
//
// It lives in a _test.go file on purpose: a fake has no place in the shipped binary.
func fakeAdmissionReserve(chk finops.BudgetCheck, checkErr error, req finops.AdmissionRequest) (finops.Reservation, error) {
	if checkErr != nil {
		if req.Unreachable == finops.UnreachableAllow {
			return finops.Reservation{Allowed: true, EstimateMicroUSD: req.EstimateMicroUSD}, checkErr
		}
		return finops.Reservation{
			Allowed: false, Action: "block", Reason: finops.ReasonStoreUnreachable,
			EstimateMicroUSD: req.EstimateMicroUSD,
		}, nil
	}
	return finops.Reservation{
		Allowed: chk.Allowed, Action: chk.Action, BudgetID: chk.BudgetID,
		BudgetName: chk.BudgetName, Reason: chk.Reason,
		EstimateMicroUSD: req.EstimateMicroUSD,
	}, nil
}
