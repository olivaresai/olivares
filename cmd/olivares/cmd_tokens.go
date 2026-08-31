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
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

const tokensPath = "/v1/tokens"

// `olivares tokens` is the CREDENTIAL half of the browser-free first run. Until it
// existed, `olivares auth login --token` consumed something only the console or a
// hand-written POST /v1/setup could produce: an install with no browser could
// authenticate nothing, and every other command in this binary was unreachable.
//
// It is a thin client over /v1/tokens (core/api/server.go:809). Every decision
// stays server-side: who may mint a cross-tenant token (superadmin only,
// handlers_core.go:342), who may mint a bound one (token:write in that tenant,
// :355), and who may see or revoke one (a token outside the caller's authority is
// reported NOT FOUND, not forbidden, so this surface cannot be used as a
// cross-tenant existence oracle — :387). The CLI adds no check of its own and
// removes none.
func newTokensCmd() *cobra.Command {
	flags := &authClientFlags{}
	root := &cobra.Command{
		Use:   "tokens",
		Short: "Issue, list, rotate and revoke API tokens (the credential a script authenticates with)",
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
				tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
				fmt.Fprintln(tw, "ID\tNAME\tTENANT\tROLE\tSUPERADMIN\tREVOKED\tLAST USED\tCREATED")
				for _, t := range list.Items {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%t\t%t\t%s\t%s\n",
						safeCLIValue(t.ID, ""), safeCLIValue(t.Name, ""),
						orDash(safeCLIValue(t.BoundTenantID, "")), orDash(safeCLIValue(t.Role, "")),
						t.IsSuperadmin, t.Revoked,
						orDash(safeCLIValue(derefOrEmpty(t.LastUsedAt), "")), safeCLIValue(t.CreatedAt, ""))
				}
				if err := tw.Flush(); err != nil {
					return err
				}
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
			"command). The secret is printed once and never stored by the CLI — pass it to\n" +
			"`olivares auth login --token` to save it in a client context.",
		Example: `  olivares tokens issue --name ci --tenant tenant-a --role admin
  olivares tokens issue --name platform --superadmin -o json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			name = strings.TrimSpace(name)
			if name == "" {
				return exitcode.New(exitcode.Usage, fmt.Errorf("--name is required: a token without a name cannot be told apart in `tokens ls`"))
			}
			// Decided from the ARGUMENTS ALONE, before any request: the two shapes
			// are exclusive in the engine (handlers_core.go:342-360) and saying so
			// here costs no round trip and no ambiguity. This REFUSES a request; it
			// never approves one — a caller passing --superadmin still has to be a
			// superadmin as far as the engine is concerned.
			if superadmin && (cmd.Flags().Changed("role") || cmd.Flags().Changed("tenant")) {
				return exitcode.New(exitcode.Usage, fmt.Errorf(
					"--superadmin mints a CROSS-TENANT token, so it takes neither --tenant nor --role"))
			}
			// NORMALIZE ONCE, THEN USE THAT VALUE EVERYWHERE. isKnownTokenRole
			// validated strings.TrimSpace(role) while the body carried the raw one,
			// so `--role " admin"` passed the local check and was then refused by the
			// engine's IsRole, which compares exactly (core/auth/permission.go:39) —
			// a legitimate value rejected for an accidental space, with an error from
			// the far end that never mentions whitespace.
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
	cmd.Flags().StringVar(&name, "name", "", "human label for the token (required; shown in `tokens ls`)")
	cmd.Flags().StringVar(&role, "role", "viewer", "role the bound token carries: viewer, editor, admin or owner")
	cmd.Flags().BoolVar(&superadmin, "superadmin", false, "mint a CROSS-TENANT superadmin token instead of a tenant-bound one (superadmin callers only)")
	_ = cmd.RegisterFlagCompletionFunc("role", completeTokenRole)
	return cmd
}

// tokensRotateCmd is the one verb in this family whose PROMISE was bigger than
// the engine behind it.
//
// The route is two transactions, not one: handleRotateToken calls IssueToken,
// which commits its own AuthMutate (core/auth/accounts.go:325), and only then
// RevokeToken, which opens another (…:389). A failure between them — store,
// audit, a canceled context — leaves the replacement PERSISTED and the old
// secret STILL VALID, and the handler answers with an error before handing over
// the new secret. That is a partial mutation whose credential-closing half fails
// open, and it is a defect of the ENDPOINT: the CLI cannot fix it from here, and
// widening the API for the CLI's convenience is not this branch's call to make.
// It is written up for the endpoint's owner (a service-level RotateToken doing
// both writes and both audit events inside ONE AuthMutate).
//
// What the CLI owns is the promise it prints. Both halves of this command used to
// state a guarantee the engine does not provide — "then revokes the old one",
// "every holder stops working immediately" — so an operator reading a failure had
// every reason to believe nothing had happened. Now the text says what the engine
// really does, and a failure ASKS THE PLANE what state it left behind and reports
// it, instead of leaving the operator to assume the safe answer.
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

// reportRotationAftermath answers the question a failed rotation leaves open and
// the operator cannot answer from the error alone: IS THE OLD SECRET STILL LIVE?
//
// It writes to stderr and never changes the exit code — the command has already
// failed and keeps the classification the plane earned (401 → 3, 5xx → 6). What it
// adds is the state, measured rather than assumed, because the two outcomes need
// opposite actions: a still-active token must be revoked by hand, and a revoked one
// means a replacement secret exists that nobody was shown and that must be found
// and revoked in `tokens ls`.
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

// findTokenByID looks one token up through the listing the engine already serves.
// There is no GET /v1/tokens/{id} (core/api/server.go:809) and this branch does not
// add one; the listing is authority-filtered server-side, so a token outside the
// caller's authority is simply absent — which is reported as "cannot tell", never
// as "it is gone".
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
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "revoked API token %s\n", safeCLIValue(args[0], ""))
			return err
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
			fmt.Errorf("the control plane %s a token but returned no secret", verb))
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
	// The recommendation used to be `auth login --token …`, which puts the secret
	// this command just minted into argv — readable by every process on the host
	// through /proc, and written to the shell history file. The file/stdin form is
	// the same command without that exposure.
	_, err := fmt.Fprintln(cmd.ErrOrStderr(),
		"NOTE: the secret above is shown ONCE — the engine stores only its hash. Save it now and "+
			"pass it by file, never in argv (`olivares auth login --token-file <file>`, or - for "+
			"stdin); a lost token is rotated, never recovered.")
	return err
}

// isKnownTokenRole mirrors core/auth IsRole (permission.go:40). It is a USAGE
// check, not an authorization one: it turns a typo into exit 2 with the list of
// valid roles instead of a 400 from the plane. The engine still validates the role
// itself and still enforces the role ceiling — a caller can never mint above its
// own rank, and nothing here can change that.
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

// addListPageFlags exposes the engine's OWN ?limit/?cursor parameters on a
// listing. It is deliberately a pass-through and not a pagination policy: the
// policy for the whole CLI is a separate, still-open decision, and inventing one
// here would be a second answer to that question.
func addListPageFlags(cmd *cobra.Command, limit *int, cursor *string) {
	cmd.Flags().IntVar(limit, "limit", 0, "server-side page size (0 = the engine's default)")
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
