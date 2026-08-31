// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The `olivares superadmin` command: offline lifecycle for the INTERNAL superadmin
// account(s) — list their status and enable/disable one (NEVER delete). Some
// regulations require that a standing built-in administrator be disable-able; this
// is the offline / break-glass counterpart to the console surface (and the way to
// revive a superadmin if the console path is itself locked out). It opens the same
// store the running engine uses. On SQLite the engine is single-writer, so run these
// against a STOPPED engine (or use the console while it runs); on Postgres they are
// safe alongside a running engine. Disabling is deny-closed against total lockout:
// the last ACTIVE superadmin cannot be disabled.
func newSuperadminCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "superadmin",
		Short: "Enable/disable internal superadmin accounts (never deletes)",
		Example: "  olivares superadmin status --data-dir /var/lib/olivares\n" +
			"  olivares superadmin disable --email admin@example.com\n" +
			"  olivares superadmin enable --email admin@example.com",
		Long: "List the internal superadmin accounts and their active/inactive status, and enable\n" +
			"or disable one. Disabling is non-destructive and reversible: the account is marked\n" +
			"inactive and its live sessions/tokens are revoked, but it is never deleted — re-enable\n" +
			"it later with `superadmin enable`. The last ACTIVE superadmin cannot be disabled\n" +
			"(deny-closed against total lockout); provision another active superadmin first.",
	}
	addTextJSONFormatFlag(root)
	root.AddCommand(superadminStatusCmd(), superadminDisableCmd(), superadminEnableCmd())
	return root
}

func superadminStatusCmd() *cobra.Command {
	var dataDir, engine, dsn string
	cmd := &cobra.Command{
		Use:     "status",
		Short:   "List internal superadmin accounts and their active/inactive status",
		Long:    "status lists internal superadmin IDs, email addresses and lifecycle states, including the number that remain active.",
		Example: "  olivares superadmin status --data-dir /var/lib/olivares",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			eng, err := auditBootRO(cmd, dataDir, engine, dsn)
			if err != nil {
				return err
			}
			defer func() { _ = eng.Close() }()
			admins, err := eng.authr.ListSuperadmins(cmd.Context())
			if err != nil {
				return err
			}
			active := 0
			items := make([]superadminStatusItem, 0, len(admins))
			for _, u := range admins {
				if u.Status == model.StatusActive {
					active++
				}
				items = append(items, superadminStatusItem{ID: u.ID.String(), Email: u.Email, Status: string(u.Status)})
			}
			result := superadminStatusResult{Items: items, Active: active}
			return renderOut(cmd, func(out io.Writer) error {
				if len(admins) == 0 {
					_, err := fmt.Fprintln(out, "no superadmin accounts")
					return err
				}
				tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
				fmt.Fprintln(tw, "ID\tEMAIL\tSTATUS")
				for _, u := range admins {
					fmt.Fprintf(tw, "%s\t%s\t%s\n", u.ID, u.Email, u.Status)
				}
				if err := tw.Flush(); err != nil {
					return err
				}
				_, err := fmt.Fprintf(out,
					"\n%d active superadmin(s); the last active one cannot be disabled.\n", active)
				return err
			}, result)
		},
	}
	addStoreFlags(cmd, &dataDir, &engine, &dsn)
	return cmd
}

type superadminStatusItem struct {
	ID     string `json:"id"`
	Email  string `json:"email"`
	Status string `json:"status"`
}

type superadminStatusResult struct {
	Items  []superadminStatusItem `json:"items"`
	Active int                    `json:"active"`
}

// superadminSetActiveResult is what `superadmin enable` and `superadmin disable`
// report. The keys are `status`'s own keys for the same two facts, so a script
// that already reads a row of `superadmin status -o json` reads this unchanged.
//
// IT DELIBERATELY CARRIES NO EMAIL, and that is not an omission to tidy up
// later. core/model/auth.go:28 records the address as PII that is "never logged
// or exported; audit references the user by id, not email", and the TEXT pane of
// these two verbs prints the id and the status and no address. Reusing
// superadminStatusItem here — the tempting move, since one enable result IS one
// status row — would therefore have exported, into the machine-readable document
// an operator redirects into a file or a CI log, a field the human pane withholds.
// `superadmin status` prints emails because enumerating the accounts is its whole
// subject; a mutation receipt's subject is the mutation.
type superadminSetActiveResult struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

func superadminDisableCmd() *cobra.Command {
	return superadminSetActiveCmd("disable", false,
		"Disable an internal superadmin (marks it inactive and revokes its sessions/tokens; never deletes)")
}

func superadminEnableCmd() *cobra.Command {
	return superadminSetActiveCmd("enable", true,
		"Re-enable a previously disabled internal superadmin")
}

// superadminSetActiveCmd builds the enable/disable subcommand (active selects which).
// The target is named by --id or --email.
func superadminSetActiveCmd(use string, active bool, short string) *cobra.Command {
	var dataDir, engine, dsn, id, email string
	var actorFlag, reasonFlag string
	cmd := &cobra.Command{
		Use:     use,
		Short:   short,
		Long:    short + ". The mutation is audited and targets exactly one account selected by --id or --email.",
		Example: "  olivares superadmin " + use + " --email admin@example.com",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			eng, err := auditBoot(cmd, dataDir, engine, dsn)
			if err != nil {
				return err
			}
			defer func() { _ = eng.Close() }()
			// Attribution is checked here — after the consent gate and after the store
			// opens — because three refusals compete and the operator must hear them in
			// the order they apply: do you mean it, is there a store, and only then who
			// are you. Putting this first made `secrets rm` answer "--actor" to somebody
			// who had not stated intent, and made an uninitialised store report an actor
			// problem. It is no less fail-closed here: nothing has been mutated yet.
			op, err := requireLocalActor(viaCLISuperadmin, actorFlag, reasonFlag)
			if err != nil {
				return err
			}
			uid, err := resolveUserID(cmd.Context(), eng, id, email)
			if err != nil {
				return err
			}
			u, err := eng.authr.SetSuperadminActive(cmd.Context(), op, uid, active)
			if err != nil {
				return err
			}
			// ONE renderOut for BOTH verbs, because this is one constructor: `enable`
			// and `disable` differ only in `active`, so a JSON pane added here is
			// added to both at once and neither can drift from the other.
			return renderOut(cmd, func(out io.Writer) error {
				_, werr := fmt.Fprintf(out, "superadmin %s is now %s\n", u.ID, u.Status)
				return werr
			}, superadminSetActiveResult{ID: u.ID.String(), Status: string(u.Status)})
		},
	}
	addStoreFlags(cmd, &dataDir, &engine, &dsn)
	addLocalActorFlags(cmd, &actorFlag, &reasonFlag)
	cmd.Flags().StringVar(&id, "id", "", "superadmin user id (see `superadmin status`)")
	cmd.Flags().StringVar(&email, "email", "", "superadmin email (alternative to --id)")
	return cmd
}

// resolveUserID resolves the target user id from --id, or by looking up --email in
// the store. Exactly one of the two must be provided.
func resolveUserID(ctx context.Context, eng *engine, id, email string) (model.ID, error) {
	id = strings.TrimSpace(id)
	email = strings.ToLower(strings.TrimSpace(email))
	switch {
	case id != "" && email != "":
		return "", fmt.Errorf("provide --id OR --email, not both")
	case id != "":
		return model.ID(id), nil
	case email != "":
		var uid model.ID
		err := eng.store.AuthView(ctx, func(as store.AuthScope) error {
			us, _, e := as.Users().List(ctx, model.Query{
				Filters: []model.Filter{{Column: "email", Op: model.OpEq, Value: email}},
				Limit:   1,
			})
			if e != nil {
				return e
			}
			if len(us) == 0 {
				return fmt.Errorf("no user found with email %q", email)
			}
			uid = us[0].ID
			return nil
		})
		return uid, err
	default:
		return "", fmt.Errorf("provide --id or --email")
	}
}
