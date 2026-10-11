// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
)

// The `olivares admin` command: host-local repairs an administrator cannot make
// through the console because the console is what they lost. Like
// `superadmin`, it opens the store the engine uses with the host's own access; there
// is no API route, token or network. On SQLite run it against a STOPPED engine.
func newAdminCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "admin",
		Short: "Host-local repairs: give a person who lost their only passkey a way back",
		Long: "Repairs that run on the engine's host with its access to the database, for the cases\n" +
			"the console cannot reach. On SQLite run them against a stopped engine; on Postgres\n" +
			"they are safe alongside a running one. Every repair is audited.",
		Example: "  olivares admin recover --email ops@example.com --actor ops-oncall --reason \"lost passkey\" --yes",
	}
	addTextJSONFormatFlag(root)
	root.AddCommand(adminRecoverCmd())
	return root
}

// adminRecoverResult is what `admin recover` reports. Like the superadmin receipts it
// carries the account id and no email (core/model/auth.go: email is PII).
type adminRecoverResult struct {
	UserID          string   `json:"user_id"`
	PasskeysRemoved int      `json:"passkeys_removed"`
	SessionsEnded   int      `json:"sessions_ended"`
	ActiveTokens    []string `json:"active_tokens"`
}

func adminRecoverCmd() *cobra.Command {
	var dataDir, engine, dsn, email, actorFlag, reasonFlag string
	var yes bool
	cmd := &cobra.Command{
		Use:   "recover",
		Short: "Remove a person's passkeys and end their sessions so they can add a new passkey",
		Long: "recover is for someone who lost their only passkey on a deployment whose step-up\n" +
			"policy asks for passkeys. In one audited transaction it removes that person's\n" +
			"passkeys and ends their sessions. Nothing else changes: not the deployment's policy,\n" +
			"other people, their password or their API tokens. The person then signs in with\n" +
			"their password, registers a new passkey and steps up with it.",
		Example: "  olivares admin recover --email ops@example.com --actor ops-oncall --reason \"lost passkey\" --yes",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(email) == "" {
				return sessionCLIUsage("provide --email")
			}
			if !yes {
				return sessionCLIUsage("this removes every passkey of " + email + " and ends their sessions: pass --yes")
			}
			eng, err := auditBoot(cmd, dataDir, engine, dsn)
			if err != nil {
				return err
			}
			defer func() { _ = eng.Close() }()
			op, err := requireLocalActor(viaCLIAdminRecover, actorFlag, reasonFlag)
			if err != nil {
				return err
			}
			uid, err := resolveUserID(cmd.Context(), eng, "", email)
			if err != nil {
				return err
			}
			got, err := eng.authr.RecoverPasskeys(cmd.Context(), op, uid)
			if err != nil {
				return err
			}
			result := adminRecoverResult{UserID: uid.String(), PasskeysRemoved: got.Passkeys, SessionsEnded: got.Sessions,
				ActiveTokens: []string{}}
			for _, t := range got.Tokens {
				result.ActiveTokens = append(result.ActiveTokens, t.String())
			}
			return renderOut(cmd, func(out io.Writer) error {
				tokens := "owns none."
				if n := len(result.ActiveTokens); n > 0 {
					tokens = fmt.Sprintf("owns %d active (%s); revoke one with olivares tokens revoke <token-id>.",
						n, strings.Join(result.ActiveTokens, ", "))
				}
				_, err := fmt.Fprintf(out, "Removed %d passkeys of %s and ended %d sessions.\n"+
					"Next: %s signs in with their password, registers a new passkey and steps up with it.\n"+
					"API tokens are unchanged: %s %s\n",
					result.PasskeysRemoved, email, result.SessionsEnded, email, email, tokens)
				return err
			}, result)
		},
	}
	addStoreFlags(cmd, &dataDir, &engine, &dsn)
	addLocalActorFlags(cmd, &actorFlag, &reasonFlag)
	cmd.Flags().StringVar(&email, "email", "", "REQUIRED: the email of the person who lost their passkey")
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm removing that person's passkeys and ending their sessions")
	return cmd
}
