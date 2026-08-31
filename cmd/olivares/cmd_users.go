// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

const (
	usersPath            = "/v1/users"
	usersSuperadminsPath = "/v1/users/superadmins"
)

// `olivares users` is the ACCOUNT half of the browser-free first run: the global
// principals a membership is later granted to. It is a thin client over /v1/users
// (core/api/server.go:798), which is superadmin-gated end to end (user:read for the
// listings, user:write to create — handlers_core.go:234,257).
//
// THE TWO VERBS THIS FILE USED TO WITHHOLD, AND WHY WITHHOLDING THEM WAS WRONG.
// POST /v1/users/{id}/disable and /enable are gated on an AAL3 step-up
// (handlers_core.go:306) and this comment used to conclude that no credential the
// CLI can hold reaches AAL3, so a command for them "could only ever exit 3".
//
// Half of that is right and half of it is a legitimate caller shut out. An API
// token genuinely can never pass: its principal sets no assurance level at all
// (core/auth/authenticator.go:220), and a password session is minted at AAL1. But
// a step-up does not mint a new credential — it ELEVATES THE SESSION ROW the
// caller already holds (core/auth/assurance.go:57), for 15 minutes
// (assurance.go:31), and `auth login --token` exists precisely to carry "a session
// you already hold". So a superadmin who ran the WebAuthn/PIV ceremony in the
// console CAN drive these from the CLI, and the only thing stopping them was the
// absence of the command. Withholding it did not protect the gate — requireAAL3
// (core/api/middleware.go:298) protects the gate, and it is untouched here; it
// just denied the operator the surface.
//
// So the verbs exist, and they carry the caller's credential like every other verb
// in this tree. A caller below AAL3 gets the engine's refusal, translated into a
// message that names the ceremony and how to bring an elevated session
// (bootstrapclient.go:138) instead of a bare 403.
func newUsersCmd() *cobra.Command {
	flags := &authClientFlags{}
	root := &cobra.Command{
		Use:   "users",
		Short: "List, create, disable and re-enable the global user accounts (superadmin)",
		Long: "Manage the global user accounts of this installation. A user is an identity; what it\n" +
			"may DO comes from the tenant memberships granted to it with `olivares members grant`.\n" +
			"Every verb here is superadmin-gated by the engine.\n\n" +
			"disable/enable are additionally gated on an AAL3 step-up: an API token can never carry\n" +
			"one, but a user session elevated by a WebAuthn/PIV ceremony can, for 15 minutes — run\n" +
			"the ceremony in the console and pass that session with `auth login --token-file`.",
		Example: `  olivares users ls
  olivares users create --email ops@example.com --display-name "Ops" --password-file /run/secrets/pw
  olivares users superadmins
  olivares users disable 018f2c2e-0000-7000-8000-000000000002 --yes`,
	}
	flags.addPersistent(root)
	client := bootstrapClient{flags: flags, surface: "users"}
	root.AddCommand(usersListCmd(client), usersCreateCmd(client), usersSuperadminsCmd(client),
		usersDisableCmd(client), usersEnableCmd(client))
	return root
}

// usersDisableCmd and usersEnableCmd drive the superadmin lifecycle: a
// non-destructive, reversible withdrawal of a global principal's access. The
// engine keeps every decision — user:write, the AAL3 step-up, and the deny-closed
// refusal to disable the last active superadmin (auth.ErrLastSuperadmin → 409,
// which this maps to exit 5 through the shared classifier).
func usersDisableCmd(client bootstrapClient) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "disable <user-id>",
		Short: "Disable a superadmin account (reversible; requires an AAL3 session)",
		Long: "Withdraw a superadmin account's access without deleting anything: sessions and tokens\n" +
			"stop authenticating and `users enable` restores it exactly. The engine refuses to\n" +
			"disable the LAST active superadmin — check how many are left with `users superadmins`\n" +
			"first — and gates the action on a verified hardware step-up (AAL3), which an API token\n" +
			"can never carry and an elevated user session can.",
		Example: "  olivares users disable 018f2c2e-0000-7000-8000-000000000002 --yes",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := confirmDestructive(cmd, yes, fmt.Sprintf(
				"disable account %q (its sessions and tokens stop authenticating; this is reversible)",
				safeCLIValue(args[0], ""))); err != nil {
				return err
			}
			return setUserActive(cmd, client, args[0], "disable")
		},
	}
	addYesFlag(cmd, &yes)
	return cmd
}

func usersEnableCmd(client bootstrapClient) *cobra.Command {
	return &cobra.Command{
		Use:   "enable <user-id>",
		Short: "Re-enable a disabled superadmin account (requires an AAL3 session)",
		Long: "Restore a superadmin account that `users disable` withdrew. It is the safe direction\n" +
			"and asks for no confirmation; the engine still requires the same AAL3 step-up, because\n" +
			"granting access back is as privileged as withdrawing it.",
		Example: "  olivares users enable 018f2c2e-0000-7000-8000-000000000002",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return setUserActive(cmd, client, args[0], "enable")
		},
	}
}

func setUserActive(cmd *cobra.Command, client bootstrapClient, id, verb string) error {
	path := usersPath + "/" + bootstrapPathID(id) + "/" + verb
	raw, err := client.expect(cmd, http.MethodPost, path, nil, http.StatusOK)
	if err != nil {
		return err
	}
	var user cliUserRow
	if err := decodeBootstrapJSON("users", raw, &user); err != nil {
		return err
	}
	return renderOut(cmd, func(out io.Writer) error {
		_, werr := fmt.Fprintf(out, "account %s (%s) is now %s\n",
			safeCLIValue(user.ID, ""), safeCLIValue(user.Email, ""), safeCLIValue(user.Status, ""))
		return werr
	}, json.RawMessage(raw))
}

// cliUserRow mirrors core/api UserDTO — never a password hash, by construction of
// the server DTO (dto.go:150).
type cliUserRow struct {
	ID           string `json:"id"`
	Email        string `json:"email"`
	DisplayName  string `json:"display_name,omitempty"`
	Status       string `json:"status"`
	IsSuperadmin bool   `json:"is_superadmin"`
	CreatedAt    string `json:"created_at"`
}

type cliUserList struct {
	Items   []cliUserRow `json:"items"`
	Cursor  string       `json:"cursor,omitempty"`
	HasMore bool         `json:"has_more,omitempty"`
}

func usersListCmd(client bootstrapClient) *cobra.Command {
	var limit int
	var cursor string
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List the global user accounts",
		Long: "List every user account known to this installation, with its status and whether it is\n" +
			"a superadmin. The engine authorizes this as a system read (user:read); a caller without\n" +
			"it is refused there, not here.",
		Example: `  olivares users ls
  olivares users ls -o json
  olivares users ls --limit 100`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			raw, err := client.expect(cmd, http.MethodGet,
				usersPath+listQuerySuffix(limit, cursor), nil, http.StatusOK)
			if err != nil {
				return err
			}
			return renderUserList(cmd, raw, "no user accounts exist yet")
		},
	}
	addListPageFlags(cmd, &limit, &cursor)
	return cmd
}

func usersSuperadminsCmd(client bootstrapClient) *cobra.Command {
	return &cobra.Command{
		Use:   "superadmins",
		Short: "List the superadmin accounts and whether each is active",
		Long: "List every superadmin account with its active/inactive status — the read side of the\n" +
			"superadmin lifecycle. Use it before `users disable`: the engine refuses to disable the\n" +
			"last active superadmin, and this is how you see how many are left.",
		Example: "  olivares users superadmins",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			raw, err := client.expect(cmd, http.MethodGet, usersSuperadminsPath, nil, http.StatusOK)
			if err != nil {
				return err
			}
			return renderUserList(cmd, raw, "no superadmin accounts exist yet")
		},
	}
}

func usersCreateCmd(client bootstrapClient) *cobra.Command {
	var email, displayName, password, passwordFile string
	var superadmin bool
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a global user account (superadmin)",
		Long: "Create a user account. Give it a password with --password-file (a file, or - to read\n" +
			"stdin) so the secret never appears in the process table or the shell history; --password\n" +
			"exists for parity with the rest of the CLI and warns when used. A new account can DO\n" +
			"nothing until it is granted a tenant membership with `olivares members grant`.",
		Example: `  olivares users create --email ops@example.com --display-name "Ops" --password-file /run/secrets/pw
  printf '%s' "$PW" | olivares users create --email ops@example.com --password-file -`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			email = strings.TrimSpace(email)
			if email == "" {
				return exitcode.New(exitcode.Usage, fmt.Errorf("--email is required"))
			}
			pw, err := readSecretValue(cmd, password, passwordFile)
			if err != nil {
				return err
			}
			// The house helper (cmd_secrets.go:253) prefers the FILE when both are
			// given; warn on the flag form either way, because the value is visible
			// in `ps` for as long as the process lives.
			if password != "" {
				fmt.Fprintln(cmd.ErrOrStderr(),
					"WARNING: --password puts the secret in this host's process table; prefer --password-file (or - for stdin)")
			}
			if pw == "" {
				return exitcode.New(exitcode.Usage, fmt.Errorf(
					"a password is required: pass --password-file <file> (or - for stdin)"))
			}
			body := map[string]any{"email": email, "password": pw}
			if displayName != "" {
				body["display_name"] = displayName
			}
			if superadmin {
				body["superadmin"] = true
			}
			raw, err := client.expect(cmd, http.MethodPost, usersPath, body, http.StatusCreated)
			if err != nil {
				// The password traveled in this request body. Nothing in the engine's
				// error envelope echoes it, but an intermediary's error page could, so
				// scrub it from anything that reaches the operator's terminal or logs.
				return redactCoded(err, pw)
			}
			var user cliUserRow
			if err := decodeBootstrapJSON("users", raw, &user); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				_, err := fmt.Fprintf(out, "created user %s (id %s, superadmin %t)\n",
					safeCLIValue(user.Email, pw), safeCLIValue(user.ID, pw), user.IsSuperadmin)
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(out,
					"→ it can do nothing until it holds a membership: olivares members grant --user "+
						safeCLIValue(user.ID, pw)+" --tenant <tenant> --role viewer")
				return err
			}, json.RawMessage(raw))
		},
	}
	cmd.Flags().StringVar(&email, "email", "", "email address that identifies the account (required)")
	cmd.Flags().StringVar(&displayName, "display-name", "", "human name shown in the console and audit ledger")
	cmd.Flags().StringVar(&password, "password", "", "initial password (prefer --password-file: this form is visible in the process table)")
	cmd.Flags().StringVar(&passwordFile, "password-file", "", "read the initial password from a file, or - for stdin")
	cmd.Flags().BoolVar(&superadmin, "superadmin", false, "create the account as a cross-tenant superadmin (the engine accepts this only from a superadmin)")
	return cmd
}

func renderUserList(cmd *cobra.Command, raw []byte, emptyNote string) error {
	var list cliUserList
	if err := decodeBootstrapJSON("users", raw, &list); err != nil {
		return err
	}
	return renderOut(cmd, func(out io.Writer) error {
		if len(list.Items) == 0 {
			_, err := fmt.Fprintln(out, emptyNote)
			return err
		}
		tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tEMAIL\tDISPLAY NAME\tSTATUS\tSUPERADMIN\tCREATED")
		for _, u := range list.Items {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%t\t%s\n",
				safeCLIValue(u.ID, ""), safeCLIValue(u.Email, ""),
				orDash(safeCLIValue(u.DisplayName, "")), safeCLIValue(u.Status, ""),
				u.IsSuperadmin, safeCLIValue(u.CreatedAt, ""))
		}
		if err := tw.Flush(); err != nil {
			return err
		}
		return writeMorePages(out, list.HasMore, list.Cursor)
	}, json.RawMessage(raw))
}
