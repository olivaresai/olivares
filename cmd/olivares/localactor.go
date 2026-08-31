// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/core/auth"
)

// The CLI half of B-12: a privileged offline mutation must say WHO is doing
// it and WHY, and the command supplies HOW.
//
// Until now these commands built auth.Principal{Kind: KindUser, Superadmin: true,
// DisplayName: "cli:secrets"} — and Principal.Actor(), which is what the ledger
// records, does not read DisplayName. Every one of them appended events whose
// actor was the bare string "user:". Five authorities, one anonymous subject.
//
// --actor and --reason are REQUIRED, not defaulted. There is no environment
// fallback and no "unknown" placeholder: a default would be a value nobody chose,
// recorded as though somebody had. Nothing runs in production yet, so making them
// required is a free incompatible change today and only gets more expensive.

// addLocalActorFlags declares the required attribution flags on a privileged
// offline command. via names the local path and is set by the command, never by
// the operator, so a caller cannot claim a path it is not on.
func addLocalActorFlags(cmd *cobra.Command, actor, reason *string) {
	cmd.Flags().StringVar(actor, "actor", "",
		"REQUIRED: who is performing this privileged operation (an operator or service identity; recorded in the audit ledger)")
	cmd.Flags().StringVar(reason, "reason", "",
		"REQUIRED: why this privileged operation is being performed (recorded in the audit ledger)")
}

// requireLocalActor validates the attribution INSIDE RunE. It is not registered
// with cobra's MarkFlagRequired on purpose: cobra checks required flags before
// RunE runs, so a destructive command missing BOTH consent and attribution would
// report the attribution and never mention --yes -- telling the operator about a
// flag they had not reached instead of the one they had omitted. Validating here
// keeps the refusals in the order the operator experiences them, and it is
// equally fail-closed: no principal, no operation.
func requireLocalActor(via, actor, reason string) (auth.Principal, error) {
	p, err := localOperator(via, actor, reason)
	if err != nil {
		return auth.Principal{}, fmt.Errorf(
			"%w: a privileged offline operation must record who and why (--actor, --reason)", err)
	}
	return p, nil
}

// localOperator builds the attributed principal for a privileged offline
// mutation. It fails closed: an empty or malformed attribution yields no
// principal, so the operation does not happen.
func localOperator(via, actor, reason string) (auth.Principal, error) {
	return auth.NewLocalOperator(auth.LocalOperator{Subject: actor, Via: via, Reason: reason})
}

// The local paths. They are constants rather than literals at each call site so
// the set is enumerable — the shape test walks it and asserts every one of them
// produces a distinguishable audit subject.
const (
	viaCLISecrets    = "cli:secrets"
	viaCLISources    = "cli:sources"
	viaCLISuperadmin = "cli:superadmin"
	viaCLIEventing   = "cli:eventing"
	viaBootSeed      = "boot/seed"
	viaHostSIGHUP    = "sighup/host-operator"
)

// localPaths is every declared local privileged path. A new one added without
// being listed here is caught by the shape test, which requires each to be
// attributable and to carry its provenance.
var localPaths = []string{
	viaCLISecrets, viaCLISources, viaCLISuperadmin,
	viaCLIEventing, viaBootSeed, viaHostSIGHUP,
}
