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
	membersPath     = "/v1/members"
	membershipsPath = "/v1/memberships"
	invitesPath     = "/v1/invites"
)

// `olivares members` is the JOIN between a global account (`olivares users`) and a
// tenant (`olivares tenants`): the membership that gives an account a role inside
// one tenant. Without it a freshly created user can authenticate and reach nothing,
// which is precisely the state a browser-free install used to be stuck in.
//
// It is a thin client over three routes the engine already serves: the tenant
// roster (GET /v1/members, user:read, core/api/server.go:825), the grant
// (POST /v1/memberships, membership:write in the TARGET tenant,
// handlers_core.go:503) and the pending invitations (/v1/invites, :832).
//
// ONE VERB THE ENGINE HAS AND THIS DOES NOT, and the reason is NOT the one that
// was written here. POST /v1/onboard — the console's "invite a person" — is gated
// on an AAL3 step-up (handlers_onboarding.go:59), and this comment used to call
// that "unreachable with any CLI credential". It is not: a step-up elevates the
// session (core/auth/assurance.go:57), and `auth login --token` carries a session.
// Where the premise was false and a caller was locked out — the superadmin
// lifecycle and the residency pin — the verbs now exist (cmd_users.go,
// cmd_tenants.go). Here it is not, because onboarding has a browser-free
// equivalent this tree already exposes and which needs NO step-up: `users create`
// then `members grant`. Two ways to do one thing, one of them harder to reach, is
// worse than one.
func newMembersCmd() *cobra.Command {
	flags := &authClientFlags{}
	root := &cobra.Command{
		Use:   "members",
		Short: "List a tenant's member roster and grant accounts a role in it",
		Long: "Manage who belongs to a tenant and with which role. A grant is what turns a global\n" +
			"account into somebody who can do work in one tenant; the roster is the resolved view of\n" +
			"that tenant's members, their effective role, workspace confinement and directory groups.\n" +
			"The tenant is the one your credential resolves to — pass --tenant to name it explicitly.",
		Example: `  olivares members ls --tenant tenant-a
  olivares members grant --user 018f2c2e-0000-7000-8000-000000000002 --tenant tenant-a --role editor
  olivares members invites ls --tenant tenant-a`,
	}
	flags.addPersistent(root)
	client := bootstrapClient{flags: flags, surface: "members"}
	root.AddCommand(membersListCmd(client), membersGrantCmd(client), membersInvitesCmd(client))
	return root
}

// cliRosterMember mirrors core/api rosterMemberDTO (handlers_members.go:21).
type cliRosterMember struct {
	UserID       string   `json:"user_id"`
	Email        string   `json:"email"`
	DisplayName  string   `json:"display_name,omitempty"`
	Status       string   `json:"status"`
	SSOOnly      bool     `json:"sso_only"`
	Role         string   `json:"role"`
	WorkspaceIDs []string `json:"workspace_ids,omitempty"`
	Groups       []string `json:"groups,omitempty"`
}

type cliRosterList struct {
	Items []cliRosterMember `json:"items"`
}

type cliGrantedMembership struct {
	ID          string `json:"id"`
	UserID      string `json:"user_id"`
	Tenant      string `json:"tenant"`
	Role        string `json:"role"`
	WorkspaceID string `json:"workspace_id,omitempty"`
}

type cliInviteRow struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Tenant    string `json:"tenant"`
	Role      string `json:"role"`
	ExpiresAt string `json:"expires_at"`
	CreatedAt string `json:"created_at"`
}

type cliInviteList struct {
	Items []cliInviteRow `json:"items"`
}

func membersListCmd(client bootstrapClient) *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List the resolved tenant's member roster",
		Long: "List every account with a membership in the resolved tenant, with its effective role,\n" +
			"workspace confinement and directory groups. The set is tenant-scoped by the engine — a\n" +
			"workspace-confined caller sees only its own workspace's members — and is returned\n" +
			"complete, without paging.",
		Example: `  olivares members ls --tenant tenant-a
  olivares members ls --tenant tenant-a -o json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			raw, err := client.expect(cmd, http.MethodGet, membersPath, nil, http.StatusOK)
			if err != nil {
				return err
			}
			var list cliRosterList
			if err := decodeBootstrapJSON("members", raw, &list); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				if len(list.Items) == 0 {
					_, err := fmt.Fprintln(out, "no members in this tenant")
					return err
				}
				tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
				fmt.Fprintln(tw, "USER ID\tEMAIL\tROLE\tSTATUS\tSSO ONLY\tWORKSPACES\tGROUPS")
				for _, m := range list.Items {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%t\t%s\t%s\n",
						safeCLIValue(m.UserID, ""), safeCLIValue(m.Email, ""),
						orDash(safeCLIValue(m.Role, "")), safeCLIValue(m.Status, ""), m.SSOOnly,
						orDash(safeCLIValue(strings.Join(m.WorkspaceIDs, ","), "")),
						orDash(safeCLIValue(strings.Join(m.Groups, ","), "")))
				}
				return tw.Flush()
			}, json.RawMessage(raw))
		},
	}
}

func membersGrantCmd(client bootstrapClient) *cobra.Command {
	var userID, role, workspaceID string
	cmd := &cobra.Command{
		Use:   "grant",
		Short: "Grant an existing account a role in a tenant",
		Long: "Grant a global account a role in one tenant, optionally confined to a single workspace\n" +
			"of that tenant. The engine authorizes the grant in the TARGET tenant (membership:write)\n" +
			"and enforces the role ceiling — a caller can never grant a role above its own. Create the\n" +
			"account first with `olivares users create`.",
		Example: `  olivares members grant --user 018f2c2e-0000-7000-8000-000000000002 --tenant tenant-a --role editor
  olivares members grant --user 018f2c2e-0000-7000-8000-000000000002 --tenant tenant-a --role viewer --workspace 018f2c2e-0000-7000-8000-000000000009`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			userID = strings.TrimSpace(userID)
			if userID == "" {
				return exitcode.New(exitcode.Usage, fmt.Errorf("--user is required (the id `olivares users ls` prints)"))
			}
			// Same normalization rule as `tokens issue`: the value that was checked
			// is the value that travels. Checking the trimmed one and sending the raw
			// one turns an accidental space into a refusal from the engine.
			role = strings.TrimSpace(role)
			if !isKnownTokenRole(role) {
				return exitcode.New(exitcode.Usage, fmt.Errorf(
					"--role must be one of viewer, editor, admin, owner (got %q)", role))
			}
			// The engine reads the target tenant from the BODY here, so the resolved
			// tenant has to travel in it as well as in the header. Resolution is the
			// shared one (flag > env > client context); this does not re-implement it.
			resolved, err := client.flags.resolve(cmd)
			if err != nil {
				return redactCoded(err, client.flags.effectiveToken())
			}
			if strings.TrimSpace(resolved.Tenant) == "" {
				return missingCLIValueError("tenant", "--tenant", "OLIVARES_TENANT", resolved)
			}
			body := map[string]any{"user_id": userID, "tenant": resolved.Tenant, "role": role}
			if ws := strings.TrimSpace(workspaceID); ws != "" {
				body["workspace_id"] = ws
			}
			raw, err := client.expect(cmd, http.MethodPost, membershipsPath, body, http.StatusCreated)
			if err != nil {
				return err
			}
			var granted cliGrantedMembership
			if err := decodeBootstrapJSON("members", raw, &granted); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				scope := ""
				if granted.WorkspaceID != "" {
					scope = ", confined to workspace " + safeCLIValue(granted.WorkspaceID, "")
				}
				_, err := fmt.Fprintf(out, "granted %s the role %q in tenant %s%s (membership %s)\n",
					safeCLIValue(granted.UserID, ""), safeCLIValue(granted.Role, ""),
					safeCLIValue(granted.Tenant, ""), scope, safeCLIValue(granted.ID, ""))
				return err
			}, json.RawMessage(raw))
		},
	}
	cmd.Flags().StringVar(&userID, "user", "", "id of the account to grant (required)")
	cmd.Flags().StringVar(&role, "role", "viewer", "role to grant: viewer, editor, admin or owner")
	cmd.Flags().StringVar(&workspaceID, "workspace", "", "confine the membership to one workspace of the tenant (default: tenant-wide)")
	_ = cmd.RegisterFlagCompletionFunc("role", completeTokenRole)
	return cmd
}

func membersInvitesCmd(client bootstrapClient) *cobra.Command {
	root := &cobra.Command{
		Use:   "invites",
		Short: "List and revoke the tenant's pending invitations",
		Long: "Inspect the invitations issued from the console that nobody has redeemed yet, and\n" +
			"revoke one. Issuing an invitation is a console operation — the engine gates it on a\n" +
			"hardware step-up (AAL3) no CLI credential can carry; the browser-free equivalent is\n" +
			"`users create` followed by `members grant`.",
		Example: `  olivares members invites ls --tenant tenant-a
  olivares members invites revoke 018f2c2e-0000-7000-8000-000000000003 --yes`,
	}
	root.AddCommand(invitesListCmd(client), invitesRevokeCmd(client))
	return root
}

func invitesListCmd(client bootstrapClient) *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List the tenant's pending, unexpired invitations",
		Long: "List invitations that have been issued for this tenant and not yet redeemed. No token\n" +
			"material is returned — the engine's invitation DTO carries none, by construction.",
		Example: "  olivares members invites ls --tenant tenant-a",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			raw, err := client.expect(cmd, http.MethodGet, invitesPath, nil, http.StatusOK)
			if err != nil {
				return err
			}
			var list cliInviteList
			if err := decodeBootstrapJSON("members", raw, &list); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				if len(list.Items) == 0 {
					_, err := fmt.Fprintln(out, "no pending invitations in this tenant")
					return err
				}
				tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
				fmt.Fprintln(tw, "ID\tEMAIL\tROLE\tEXPIRES\tCREATED")
				for _, inv := range list.Items {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
						safeCLIValue(inv.ID, ""), safeCLIValue(inv.Email, ""),
						orDash(safeCLIValue(inv.Role, "")), safeCLIValue(inv.ExpiresAt, ""),
						safeCLIValue(inv.CreatedAt, ""))
				}
				return tw.Flush()
			}, json.RawMessage(raw))
		},
	}
}

func invitesRevokeCmd(client bootstrapClient) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "revoke <invite-id>",
		Aliases: []string{"rm", "delete"},
		Short:   "Revoke a pending invitation",
		Long: "Revoke a pending invitation so its single-use token can no longer be redeemed. Use it\n" +
			"when an invitation was sent to the wrong address: the link stops working at once.",
		Example: "  olivares members invites revoke 018f2c2e-0000-7000-8000-000000000003 --yes",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := confirmDestructive(cmd, yes, fmt.Sprintf(
				"revoke invitation %q (its link stops working immediately)",
				safeCLIValue(args[0], ""))); err != nil {
				return err
			}
			if _, err := client.expect(cmd, http.MethodDelete,
				invitesPath+"/"+bootstrapPathID(args[0]), nil, http.StatusNoContent); err != nil {
				return err
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "revoked invitation %s\n", safeCLIValue(args[0], ""))
			return err
		},
	}
	addYesFlag(cmd, &yes)
	return cmd
}
