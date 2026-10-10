// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
)

const tokensPath = "/v1/tokens"

// Token authorization and tenant filtering stay server-side. Out-of-scope IDs
// return not found to avoid a cross-tenant existence oracle.
func newTokensCmd() *cobra.Command {
	flags := &authClientFlags{}
	root := &cobra.Command{
		Use:   "tokens",
		Short: "API tokens for scripts: issue, list, rotate, revoke",
		Long: "Manage the API tokens that authenticate non-interactive callers: CI jobs, collectors,\n" +
			"and this CLI itself. A token is either BOUND to one tenant with one role, or (superadmin\n" +
			"only) cross-tenant. The secret is shown ONCE, at issue and at rotate; the engine stores\n" +
			"only its hash, so a lost token is rotated, never recovered.",
		Example: `  olivares tokens ls
  olivares tokens issue --name ci --tenant tenant-a --role admin
  olivares tokens rotate 018f2c2e-0000-7000-8000-000000000001
  olivares tokens revoke 018f2c2e-0000-7000-8000-000000000001 --yes`,
	}
	flags.addPersistent(root)
	client := bootstrapClient{flags: flags, surface: "tokens"}
	root.AddCommand(
		tokensListCmd(client),
		tokensIssueCmd(client),
		tokensRotateCmd(client),
		tokensRevokeCmd(client),
	)
	return root
}

// cliTokenRow mirrors core/api TokenDTO. It carries no secret and no hash: the
// engine never returns either on a listing, and neither does this.
type cliTokenRow struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	UserID        string  `json:"user_id,omitempty"`
	BoundTenantID string  `json:"bound_tenant_id,omitempty"`
	Role          string  `json:"role,omitempty"`
	IsSuperadmin  bool    `json:"is_superadmin,omitempty"`
	ExpiresAt     *string `json:"expires_at,omitempty"`
	Revoked       bool    `json:"revoked"`
	LastUsedAt    *string `json:"last_used_at,omitempty"`
	CreatedAt     string  `json:"created_at"`
}

type cliTokenList struct {
	Items   []cliTokenRow `json:"items"`
	Cursor  string        `json:"cursor,omitempty"`
	HasMore bool          `json:"has_more,omitempty"`
}

// cliIssuedToken is the show-once reply of issue and rotate.
type cliIssuedToken struct {
	Token     string `json:"token"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	RevokedID string `json:"revoked_id,omitempty"`
}

// Revocation is a completed lifecycle step; mute the row rather than mark an error.
func revokedRole(revoked bool) termrender.Role {
	if revoked {
		return termrender.RoleMuted
	}
	return termrender.RoleNone
}

func tokensListCmd(client bootstrapClient) *cobra.Command {
	var includeRevoked bool
	var limit int
	var cursor string
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List the API tokens the caller may see",
		Long: "List API tokens. A superadmin sees every token; a tenant admin sees the tokens bound\n" +
			"to the tenant they hold token:read in — the engine applies that filter, not this client.\n" +
			"Revoked tokens are hidden unless --include-revoked. No secret is ever returned.",
		Example: `  olivares tokens ls
  olivares tokens ls --include-revoked -o json
  olivares tokens ls --tenant tenant-a --limit 50`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var extra []string
			if includeRevoked {
				extra = append(extra, "include_revoked=true")
			}
			path := tokensPath + listQuerySuffix(limit, cursor, extra...)
			raw, err := client.expect(cmd, http.MethodGet, path, nil, http.StatusOK)
			if err != nil {
				return err
			}
			var list cliTokenList
			if err := decodeBootstrapJSON("tokens", raw, &list); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				if len(list.Items) == 0 {
					_, err := fmt.Fprintln(out, "no API tokens visible to this caller")
					return err
				}
				tbl := termrender.Table{
					Header: []string{"id", "name", "tenant", "role", "superadmin", "revoked", "last used", "created"},
				}
				for _, t := range list.Items {
					tbl.Rows = append(tbl.Rows, []string{
						safeCLIValue(t.ID, ""), safeCLIValue(t.Name, ""),
						orDash(safeCLIValue(t.BoundTenantID, "")), orDash(safeCLIValue(t.Role, "")),
						flagCell(t.IsSuperadmin), flagCell(t.Revoked),
						orDash(safeCLIValue(derefOrEmpty(t.LastUsedAt), "")), safeCLIValue(t.CreatedAt, ""),
					})
					tbl.Roles = append(tbl.Roles, []termrender.Role{0, 0, 0, 0, 0, revokedRole(t.Revoked)})
				}
				renderTo(out).Table(tbl)
				return writeMorePages(out, list.HasMore, list.Cursor)
			}, json.RawMessage(raw))
		},
	}
	cmd.Flags().BoolVar(&includeRevoked, "include-revoked", false, "also list tokens that have been revoked")
	addListPageFlags(cmd, &limit, &cursor)
	return cmd
}

func tokensIssueCmd(client bootstrapClient) *cobra.Command {
	var name, role string
	var superadmin bool
	cmd := &cobra.Command{
		Use:   "issue",
		Short: "Issue an API token and print its secret ONCE",
		Long: "Issue an API token. By default it is BOUND to the resolved tenant with --role, which is\n" +
			"what a CI job or a collector should hold; --superadmin mints a cross-tenant token and is\n" +
			"accepted only from a caller who is already a superadmin (the engine decides, not this\n" +
			"command). The secret is printed once and never stored by the CLI — write it to a file and pass\n" +
			"that to `olivares auth login --token-file <file>` (or - for stdin) to save it in a client context.",
		Example: `  olivares tokens issue --name ci --tenant tenant-a --role admin
  olivares tokens issue --name platform --superadmin -o json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			name = strings.TrimSpace(name)
			if name == "" {
				return exitcode.New(exitcode.Usage, fmt.Errorf("--name is required: a token without a name cannot be told apart in `tokens ls`"))
			}
			// These shapes are exclusive. The engine still authorizes the caller.
			if superadmin && (cmd.Flags().Changed("role") || cmd.Flags().Changed("tenant")) {
				return exitcode.New(exitcode.Usage, fmt.Errorf(
					"--superadmin mints a CROSS-TENANT token, so it takes neither --tenant nor --role"))
			}
			// Validate and send the same normalized role.
			role = strings.TrimSpace(role)
			if !superadmin && !isKnownTokenRole(role) {
				return exitcode.New(exitcode.Usage, fmt.Errorf(
					"--role must be one of viewer, editor, admin, owner (got %q)", role))
			}
			body := map[string]any{"name": name}
			if superadmin {
				body["superadmin"] = true
			} else {
				body["role"] = role
				// The engine reads the bound tenant from the BODY, not the header, so
				// the resolved tenant has to travel in both. Resolution itself is not
				// re-implemented: resolveCLIConfig already applied flag > env > context.
				resolved, err := client.flags.resolve(cmd)
				if err != nil {
					return redactCoded(err, client.flags.effectiveToken())
				}
				if strings.TrimSpace(resolved.Tenant) == "" {
					return missingCLIValueError("tenant", "--tenant", "OLIVARES_TENANT", resolved)
				}
				body["tenant"] = resolved.Tenant
			}
			raw, err := client.expect(cmd, http.MethodPost, tokensPath, body, http.StatusCreated)
			if err != nil {
				return err
			}
			return renderIssuedToken(cmd, raw, "issued")
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "human label for the token (required; shown in 'tokens ls')")
	cmd.Flags().StringVar(&role, "role", "viewer", "role the bound token carries: viewer, editor, admin or owner")
	cmd.Flags().BoolVar(&superadmin, "superadmin", false, "mint a CROSS-TENANT superadmin token instead of a tenant-bound one (superadmin callers only)")
	_ = cmd.RegisterFlagCompletionFunc("role", completeTokenRole)
	return cmd
}

// Rotation commits issuance before revocation; a failed request may leave both
// tokens live. Probe the old token after failure rather than assume rollback.
func tokensRotateCmd(client bootstrapClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rotate <token-id>",
		Short: "Rotate an API token: issue a replacement with the same spec and revoke the old one",
		Long: "Rotate an API token. The engine issues a replacement carrying the SAME name, tenant,\n" +
			"role and expiry, then revokes the old one; the new secret is printed once. On success\n" +
			"both halves happened and every holder of the old secret stops working.\n\n" +
			"THE TWO HALVES ARE NOT ONE TRANSACTION. The engine commits the replacement first and\n" +
			"revokes second (core/api/handlers_core.go:474), so a failure in between leaves the old\n" +
			"secret VALID and a replacement you were never shown. This command does not report that\n" +
			"as a clean failure: it asks the plane what survived, prints it, and exits non-zero.",
		Example: "  olivares tokens rotate 018f2c2e-0000-7000-8000-000000000001",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := tokensPath + "/" + bootstrapPathID(args[0]) + "/rotate"
			raw, err := client.expect(cmd, http.MethodPost, path, nil, http.StatusCreated)
			if err != nil {
				// Exit 2 is decided from the arguments alone, before any request is
				// built (no credential, no server, unsafe transport). Nothing was
				// asked of the plane, so there is no aftermath to report and probing
				// would only produce a second copy of the same refusal.
				if exitcode.From(err) != exitcode.Usage {
					reportRotationAftermath(cmd, client, args[0])
				}
				return err
			}
			return renderIssuedToken(cmd, raw, "rotated")
		},
	}
	return cmd
}

// maxRotationProbePages bounds the aftermath probe. The engine pages `tokens ls`,
// and an installation can hold many tokens; this is a diagnostic on an error path,
// not a search, so it walks a few pages and then says it could not tell.
const maxRotationProbePages = 20

// Report the old token's observed state on stderr, preserving the original exit
// code. A replacement may exist even when the operator was not shown its secret.
func reportRotationAftermath(cmd *cobra.Command, client bootstrapClient, id string) {
	w := cmd.ErrOrStderr()
	safeID := safeCLIValue(id, "")
	row, err := findTokenByID(cmd, client, id)
	switch {
	case err != nil:
		fmt.Fprintf(w, "ROTATION FAILED, and the state of token %s could not be verified: %v\n"+
			"      The rotation is NOT atomic in the engine, so the old secret may still be valid and a\n"+
			"      replacement may exist. Check with `olivares tokens ls --include-revoked`.\n", safeID, err)
	case row == nil:
		fmt.Fprintf(w, "ROTATION FAILED, and token %s is not visible to this caller, so nothing here can\n"+
			"      say whether a replacement was created. Check with a credential that can see it.\n", safeID)
	case !row.Revoked:
		fmt.Fprintf(w, "ROTATION FAILED and THE PREVIOUS TOKEN %s IS STILL ACTIVE — every holder of that\n"+
			"      secret still authenticates. If a replacement was created before the failure it is in\n"+
			"      `olivares tokens ls`, and its secret was never shown to anyone. Revoke whichever you\n"+
			"      do not want with `olivares tokens revoke <id> --yes`.\n", safeID)
	default:
		fmt.Fprintf(w, "ROTATION FAILED after the previous token %s was already revoked. A replacement may\n"+
			"      exist whose secret was never shown; find it in `olivares tokens ls` and revoke it, then\n"+
			"      issue a new token with `olivares tokens issue`.\n", safeID)
	}
}

// The listing is authority-filtered: absence means "cannot tell", not "revoked".
func findTokenByID(cmd *cobra.Command, client bootstrapClient, id string) (*cliTokenRow, error) {
	cursor := ""
	for page := 0; page < maxRotationProbePages; page++ {
		raw, err := client.expect(cmd, http.MethodGet,
			tokensPath+listQuerySuffix(0, cursor, "include_revoked=true"), nil, http.StatusOK)
		if err != nil {
			return nil, err
		}
		var list cliTokenList
		if err := decodeBootstrapJSON("tokens", raw, &list); err != nil {
			return nil, err
		}
		for i := range list.Items {
			if list.Items[i].ID == id {
				return &list.Items[i], nil
			}
		}
		if !list.HasMore || strings.TrimSpace(list.Cursor) == "" {
			return nil, nil
		}
		cursor = list.Cursor
	}
	return nil, fmt.Errorf("token %s was not on the first %d pages of the listing",
		safeCLIValue(id, ""), maxRotationProbePages)
}

func tokensRevokeCmd(client bootstrapClient) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "revoke <token-id>",
		Aliases: []string{"rm", "delete"},
		Short:   "Revoke an API token",
		Long: "Revoke an API token. Everything holding that secret stops authenticating at once and\n" +
			"there is no undo — the replacement is a new token, so prefer `tokens rotate` when the\n" +
			"holder must keep working. A token outside the caller's authority is reported as not\n" +
			"found, which is the engine's deliberate refusal to confirm that it exists.",
		Example: "  olivares tokens revoke 018f2c2e-0000-7000-8000-000000000001 --yes",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := confirmDestructive(cmd, yes, fmt.Sprintf(
				"revoke API token %q (every holder of its secret stops authenticating immediately)",
				safeCLIValue(args[0], ""))); err != nil {
				return err
			}
			if _, err := client.expect(cmd, http.MethodDelete,
				tokensPath+"/"+bootstrapPathID(args[0]), nil, http.StatusNoContent); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				_, err := fmt.Fprintf(out, "revoked API token %s\n", safeCLIValue(args[0], ""))
				return err
			}, map[string]any{"id": args[0], "revoked": true})
		},
	}
	addYesFlag(cmd, &yes)
	return cmd
}

// renderIssuedToken prints a show-once secret.
//
// The secret goes to STDOUT because it is the command's product — a script does
// `TOKEN=$(olivares tokens issue … -o json | jq -r .token)`. The warning that it
// will never be shown again goes to STDERR, so it cannot end up inside the
// captured value.
func renderIssuedToken(cmd *cobra.Command, raw []byte, verb string) error {
	var issued cliIssuedToken
	if err := decodeBootstrapJSON("tokens", raw, &issued); err != nil {
		return err
	}
	if issued.Token == "" {
		return exitcode.New(exitcode.Server,
			fmt.Errorf("the engine %s a token but returned no secret", verb))
	}
	if err := renderOut(cmd, func(out io.Writer) error {
		_, err := fmt.Fprintf(out, "%s API token %s (id %s)\n%s\n",
			verb, safeCLIValue(issued.Name, ""), safeCLIValue(issued.ID, ""), issued.Token)
		return err
	}, json.RawMessage(raw)); err != nil {
		return err
	}
	// State the OTHER half of a rotation as fact, from the reply, rather than
	// leaving the operator to infer it from the word "rotated".
	if issued.RevokedID != "" {
		if _, err := fmt.Fprintf(cmd.ErrOrStderr(),
			"the previous token %s is revoked; every holder of its secret stops authenticating\n",
			safeCLIValue(issued.RevokedID, "")); err != nil {
			return err
		}
	}
	// Keep secrets out of process arguments and shell history.
	_, err := fmt.Fprintln(cmd.ErrOrStderr(),
		"NOTE: the secret above is shown ONCE — the engine stores only its hash. Save it now and "+
			"pass it by file, never in argv (`olivares auth login --token-file <file>`, or - for "+
			"stdin); a lost token is rotated, never recovered.")
	return err
}

// Role spelling is a usage check; authorization and role ceilings stay server-side.
func isKnownTokenRole(role string) bool {
	switch strings.TrimSpace(role) {
	case "viewer", "editor", "admin", "owner":
		return true
	default:
		return false
	}
}

func completeTokenRole(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	return []string{"viewer", "editor", "admin", "owner"}, cobra.ShellCompDirectiveNoFileComp
}

// Pass through the engine's page size and cursor.
func addListPageFlags(cmd *cobra.Command, limit *int, cursor *string) {
	cmd.Flags().IntVar(limit, "limit", 0, "server-side page size (left out: the engine's default)")
	cmd.Flags().StringVar(cursor, "cursor", "", "continue from the cursor a previous page reported")
}

// listQuerySuffix renders the query string of a paged listing. extra carries the
// parameters that belong to ONE family (tokens has ?include_revoked; users does
// not), so the shared page parameters stay in one place.
func listQuerySuffix(limit int, cursor string, extra ...string) string {
	parts := append([]string{}, extra...)
	if limit > 0 {
		parts = append(parts, "limit="+strconv.Itoa(limit))
	}
	if c := strings.TrimSpace(cursor); c != "" {
		parts = append(parts, "cursor="+url.QueryEscape(c))
	}
	if len(parts) == 0 {
		return ""
	}
	return "?" + strings.Join(parts, "&")
}

// writeMorePages says the listing was cut short by the server page and names the
// cursor to continue from. Silence here would be the defect: a truncated `ls` that
// looks complete is how an operator concludes a token does not exist.
func writeMorePages(out io.Writer, hasMore bool, cursor string) error {
	if !hasMore {
		return nil
	}
	_, err := fmt.Fprintf(out,
		"… more rows remain; continue with --cursor %s\n", safeCLIValue(cursor, ""))
	return err
}

func derefOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
