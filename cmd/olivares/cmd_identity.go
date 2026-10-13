// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"io"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// The identity namespace from a terminal: four read-only posture views —
// federation graph, SSO connection state, customer-managed keys, and workspace
// data residency.
//
// ═══ THE ONE DESIGN DECISION IN THIS FILE, AND WHY IT IS NOT DECORATION ═══
//
// Three of these four routes answer with THREE states, not two, and the engine
// spent real effort making that so. From modules/governance/identitysso.go:28-33,
// verbatim:
//
//	configured=false + no reason  — SSO is genuinely OFF (a real, known state)
//	configured=false + a reason   — we could NOT determine it. "Reporting this as
//	                                'off' would tell an operator SSO is disabled
//	                                when the truth is that we did not look."
//
// modules/governance/identityposture.go:31-35 says the same for the CMEK and
// residency inventories: "No customer-managed keys" and "we could not look" are
// different facts and must not render alike.
//
// A CLI that exited 0 for both would destroy that distinction at the exact place
// it matters most — a fleet sweep. So the third state exits INDETERMINATE (8),
// which the contract already defines for precisely this: "the answer could not be
// established … A fleet sweep must treat this as 'not yet answered', never as
// 'clean'" (exitcode.go:31-37).
//
// This ADDS a use of an existing code; it redefines nothing. A caller that today
// treats non-zero as failure keeps working. A caller that branches learns the
// difference between "no CMEK is configured" and "I could not see whether one is".
// The report is on stdout in both cases, so the exit code is the only thing that
// changes — and `--strict=false` turns it off for a caller that does not want it.

const identityNS = "identity"

func newIdentityCmd() *cobra.Command {
	var flags authClientFlags
	root := &cobra.Command{
		Use:   "identity",
		Short: "Read identity connection state",
		Long: "Read SSO connection state without changing its configuration. " +
			"An unmeasured state exits 8; --strict=false keeps the report and exits 0. " +
			"Business Identity & Scale adds managed identity posture views.",
		Example: "  olivares identity sso\n  olivares identity sso -o json\n  olivares identity sso --strict=false",
	}
	flags.addPersistent(root)
	root.AddCommand(newIdentitySsoCmd(&flags))
	root.AddCommand(editionIdentityCommands(&flags)...)
	return root
}

// addIdentityStrictFlag declares the opt-out from the indeterminate exit.
//
// It defaults TRUE. The safe default for a posture read is to make "I could not
// look" loud; an operator who has decided they do not care can say so, but they
// have to say it.
func addIdentityStrictFlag(cmd *cobra.Command, strict *bool) {
	cmd.Flags().BoolVar(strict, "strict", true,
		"exit 8 (indeterminate) when the engine reports it could not establish this posture; --strict=false exits 0 instead")
}

// identityIndeterminate is the shared decision. The report is ALREADY on stdout
// when this is called, so the returned error carries the code and no message —
// exitcode.Silent's contract (exitcode.go:99).
func identityIndeterminate(strict bool, reason string) error {
	if !strict || reason == "" {
		return nil
	}
	return exitcode.New(exitcode.Indeterminate, nil)
}

// ---- sso ---------------------------------------------------------------------------

type identitySso struct {
	Protocol    string `json:"protocol"`
	Configured  bool   `json:"configured"`
	RedirectURI string `json:"redirect_uri,omitempty"`
	PKCEMethod  string `json:"pkce_method,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

func newIdentitySsoCmd(flags *authClientFlags) *cobra.Command {
	var strict bool
	cmd := &cobra.Command{
		Use:   "sso",
		Short: "Report the SSO connection state",
		Long: "sso reports whether an identity provider currently backs sign-in, which protocol\n" +
			"it speaks, and the redirect URI to register with that provider. The redirect URI\n" +
			"is derived through the engine's own rule, so what this prints is what the login\n" +
			"leg will actually send — including behind a trusted reverse proxy.\n\n" +
			"It REPORTS and never configures. And it distinguishes \"SSO is off\" from \"the\n" +
			"engine could not determine the state\": the second exits 8, because telling a\n" +
			"sweep that SSO is disabled when nobody looked is the failure this split exists\n" +
			"to prevent.",
		Example: "  olivares identity sso\n" +
			"  olivares identity sso -o json\n" +
			"  olivares identity sso --strict=false",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := observeCall{flags: flags, ns: identityNS, method: http.MethodGet, path: "/sso"}.do(cmd)
			if err != nil {
				return err
			}
			var s identitySso
			if err := res.decode(&s); err != nil {
				return err
			}
			if err := renderOut(cmd, func(w io.Writer) error {
				tw := newTabWriter(w)
				switch {
				case s.Reason != "":
					fmt.Fprintf(tw, "sso\tUNDETERMINED\n")
					fmt.Fprintf(tw, "reason\t%s\n", observeCell(s.Reason))
				case s.Configured:
					fmt.Fprintf(tw, "sso\tconnected\n")
					fmt.Fprintf(tw, "protocol\t%s\n", observeCell(s.Protocol))
				default:
					fmt.Fprintf(tw, "sso\tnot configured\n")
				}
				fmt.Fprintf(tw, "redirect uri\t%s\n", observeCell(s.RedirectURI))
				fmt.Fprintf(tw, "pkce method\t%s\n", observeCell(s.PKCEMethod))
				if err := tw.Flush(); err != nil {
					return err
				}
				if s.Reason != "" {
					_, werr := fmt.Fprintln(w,
						"this is NOT a report that SSO is disabled: the engine could not establish the state")
					return werr
				}
				return nil
			}, observeJSON(res.raw)); err != nil {
				return err
			}
			return identityIndeterminate(strict, s.Reason)
		},
	}
	addIdentityStrictFlag(cmd, &strict)
	return cmd
}
