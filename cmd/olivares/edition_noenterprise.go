// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package main

// enterpriseAddOnsLinked reports whether THIS binary links the commercial
// add-ons. It is false in the community artifact, which is what lets the root
// help stop advertising command groups that can only ever fail here (E6).
//
// The commands themselves are still registered and still invocable: hiding a
// group from the help is not removing it, so a script that already calls
// `olivares threatintel status` keeps getting the same honest refusal instead
// of an "unknown command". What changes is that a first-time reader of
// `olivares --help` is no longer offered seven verbs — hooks attest/conform and
// the five threatintel verbs — that every single invocation of this artifact
// answers with "not available".
const enterpriseAddOnsLinked = false

// enterpriseEditionHint is the sentence every add-on's unavailable-here error
// ends with. It exists because the sentence those errors USED to end with named
// a build that does not exist:
//
//	"it requires an enterprise build (build with -tags enterprise)"
//
// Measured on this repository: `go build -tags enterprise ./cmd/olivares/`
// fails with 47 undefined symbols over 43 unique symbols in 17 files (re-measured it; the figure here used to say 10, which is what plain `go build`
// prints before it gives up with "too many errors" — you need `-gcflags=-e` to
// see them all), because the commercial tree moved to its own
// private distribution in and the tag no longer has anything to select.
// An operator following that instruction got a compile error, not a product.
const enterpriseEditionHint = "it is part of the Olivares AI enterprise edition, which is a " +
	"separate signed distribution — not a build tag of this repository"
