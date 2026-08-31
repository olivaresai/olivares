// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

type authClientFlags struct {
	server string
	token  string
	// tokenFile is the bearer's file/stdin form. The bearer is the ONE secret this
	// CLI used to accept only in argv, where every process on the host can read it
	// out of /proc and the shell writes it to history — while the password and the
	// setup token next to it already had a --*-file form.
	tokenFile string
	// tokenFileValue caches the file read. resolveCLIConfig runs more than once in
	// a single command (`tokens issue` resolves the tenant, then the client
	// resolves again), and `--token-file -` can only be read ONCE: the second read
	// of a consumed stdin returns empty, which would look exactly like "no
	// credential" and refuse a caller who supplied one.
	tokenFileValue string
	tokenFileRead  bool
	// warnedTokenArgv keeps the --token exposure warning to ONE line per run.
	// resolutionOptions is called more than once in a single command (`tokens
	// issue` resolves the tenant, then the client resolves again), and a warning
	// printed twice reads like two different events.
	warnedTokenArgv bool
	// allowCleartext is the explicit, dangerous opt-in for sending a credential to
	// a non-loopback host over plain HTTP (clitransport.go).
	allowCleartext bool
	tenant         string
	caCert         string
	pins           []string
	insecure       bool
	timeout        time.Duration
}

type authWhoamiResponse struct {
	Kind        string            `json:"kind"`
	UserID      string            `json:"user_id"`
	Actor       string            `json:"actor"`
	DisplayName string            `json:"display_name"`
	Superadmin  bool              `json:"superadmin"`
	Grants      []authWhoamiGrant `json:"grants"`
}

type authWhoamiGrant struct {
	Tenant string `json:"tenant"`
	Role   string `json:"role"`
}

// The three CLI-STATE DTOs (VER-06 lot L3). auth login/logout/use-context change
// one thing — which client context this machine will use next — and reported it
// only as a sentence. Every field below is a value the command already had in
// hand; none of them is re-derived, and every string goes through safeCLIValue so
// the credential redaction and the control-character stripping are properties of
// the VALUE, not of the text formatter (the same rule `auth status` follows).

// authLoginResult is what `auth login -o json` reports.
//
// Server, Tenant and Actor are in here although the text line names NONE of them,
// and Tenant is the reason this DTO is worth having: when the caller passed no
// --tenant and the token carries exactly one grant, login SELECTS that tenant and
// writes it into the context (see the RunE below). That inference decides which
// tenant every later command talks to, and until now the only way to learn it was
// to read the config file the command had just written.
type authLoginResult struct {
	// Validated is always true: the field exists because a script's first
	// question is not "which context" but "did the server accept the token", and
	// a document that answers it explicitly does not have to be inspected for
	// absent keys. login never emits this DTO on a rejected credential — the
	// whoami call fails first and nothing is written.
	Validated bool   `json:"validated"`
	Context   string `json:"context"`
	Server    string `json:"server"`
	Tenant    string `json:"tenant"`
	Actor     string `json:"actor"`
	// InsecureNotPersisted mirrors the stderr NOTE below: the context that was
	// just written carries no CA certificate and no pin, so --insecure was applied
	// to THIS command only and the next command will fail TLS verification. A
	// script that provisions a context needs to branch on that, and grepping a
	// human note for it is not a contract.
	InsecureNotPersisted bool `json:"insecure_not_persisted"`
}

// authLogoutResult is what `auth logout -o json` reports. Purged carries exactly
// the fact the text line's verb carries: false is "token removed from", true is
// "purged" (the whole context deleted).
type authLogoutResult struct {
	Context string `json:"context"`
	Purged  bool   `json:"purged"`
}

// authUseContextResult is what `auth use-context -o json` reports.
//
// THE KEY IS `context`, NOT `current_context`, AND THAT WAS A CORRECTION. The fact
// this command reports — which client context this machine will use next — already
// has a spelling in this CLI: `auth status -o json` has emitted it as `context`
// since (the lowercased CONTEXT row of its payload), and the two sibling DTOs
// above use `context` too. `current_context` was the only place in cmd/olivares
// naming it differently, which meant `login → use-context → status` handed a script
// `.context`, then `.current_context`, then `.context` for one value — a parser per
// command, inside one command family, which is the defect VER-06 exists to close.
// The Go field keeps the config file's name (cliConfig.CurrentContext) because that
// is what it is assigned from.
type authUseContextResult struct {
	CurrentContext string `json:"context"`
}

func newAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage CLI authentication and named client contexts",
		Long: "Manage credentials and kubeconfig-style client contexts for remote control planes.\n\n" +
			"Client values resolve in this order: explicit flag > OLIVARES_SERVER_URL,\n" +
			"OLIVARES_TOKEN, or OLIVARES_TENANT environment variable > current context in\n" +
			"~/.config/olivares/config.yaml. The use-context command lives under auth because\n" +
			"the existing top-level config command manages engine configuration, not CLI state.",
		Example: `  olivares auth login --server https://plane.example.com --token "$OLIVARES_TOKEN"
  olivares auth status
  olivares auth use-context production`,
	}
	cmd.AddCommand(newAuthBootstrapCmd(), newAuthLoginCmd(), newAuthLogoutCmd(), newAuthStatusCmd(), newAuthUseContextCmd())
	return cmd
}

// authSetupPath is the ONE route that works before any account exists: it is
// exempt from the setup gate (core/api/middleware.go:197) and its gate is the
// one-time setup token in the body, verified in constant time.
const authSetupPath = "/v1/setup"

// authLoginPath exchanges email+password for an opaque session credential.
const authLoginPath = "/v1/auth/login"

// authSetupResult is the reply of POST /v1/setup: the created superadmin (the flat
// historical user shape) plus the organization created with it. The tenant id
// matters — every tenant-scoped route resolves a tenant, so an install whose
// operator does not know it has nothing to select (core/api/handlers_auth.go:48).
type authSetupResult struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	IsSuperadmin bool      `json:"is_superadmin"`
	Organization cliOrgRow `json:"organization"`
}

type authSessionResult struct {
	Token     string `json:"token"`
	SessionID string `json:"session_id"`
	ExpiresAt string `json:"expires_at"`
}

// newAuthBootstrapCmd redeems the one-time first-boot token WITHOUT a browser.
//
// This is the step the product had no CLI verb for. `olivares serve` prints the
// token and says "open the console and create the first administrator"
// (cmd_serve.go:522); until this command existed, an install with no browser could
// only proceed by hand-writing the POST. Everything after it — tenants, users,
// memberships, API tokens — was already unreachable for want of a credential.
//
// It is deliberately NOT called `setup`: the top-level `olivares setup` is the
// env-file wizard that runs BEFORE the engine starts, and the two must never be
// confused. This one talks to a running engine.
func newAuthBootstrapCmd() *cobra.Command {
	var (
		flags                                           authClientFlags
		setupToken, setupTokenFile                      string
		email, password, passwordFile, organizationName string
		contextName                                     string
		save                                            bool
	)
	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Redeem the one-time first-boot token: create the first organization and superadmin",
		Long: "Complete first-boot setup against a running engine, without opening the console. It\n" +
			"redeems the single-use token `olivares serve` prints on a fresh data directory and\n" +
			"creates BOTH halves an install needs to be usable: the first organization and the\n" +
			"superadmin that owns it. The token is consumed on success and setup closes.\n\n" +
			"Read both secrets from files (or - for stdin) so they never enter the process table.\n" +
			"With --save-context it then logs in and stores the session in a client context, so the\n" +
			"very next command is already authenticated.",
		Example: `  olivares auth bootstrap --server https://127.0.0.1:8443 --ca-cert ./engine-ca.pem \
    --setup-token-file ./setup.token --email admin@example.com --password-file ./admin.pw
  olivares auth bootstrap --server https://127.0.0.1:8443 --setup-token-file - \
    --email admin@example.com --password-file ./admin.pw --organization "Acme GmbH" --save-context`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Both secrets can be read from stdin in principle, but not BOTH at once:
			// one stream cannot be split into two values without inventing a
			// framing convention the rest of this CLI does not have.
			if setupTokenFile == "-" && passwordFile == "-" {
				return exitcode.New(exitcode.Usage, errors.New(
					"--setup-token-file and --password-file cannot both read stdin; put one in a file"))
			}
			token, err := readSecretValue(cmd, setupToken, setupTokenFile)
			if err != nil {
				return err
			}
			pw, err := readSecretValue(cmd, password, passwordFile)
			if err != nil {
				return err
			}
			if setupToken != "" || password != "" {
				fmt.Fprintln(cmd.ErrOrStderr(),
					"WARNING: a secret passed as a flag is visible in this host's process table; prefer the --*-file forms (or - for stdin)")
			}
			if strings.TrimSpace(token) == "" {
				return exitcode.New(exitcode.Usage, errors.New(
					"the one-time setup token is required: pass --setup-token-file <file> (or - for stdin). "+
						"`olivares serve` prints it once, under \"=== FIRST-BOOT SETUP ===\""))
			}
			if strings.TrimSpace(email) == "" {
				return exitcode.New(exitcode.Usage, errors.New("--email is required: it identifies the first superadmin"))
			}
			if pw == "" {
				return exitcode.New(exitcode.Usage, errors.New(
					"a password is required: pass --password-file <file> (or - for stdin)"))
			}
			client := bootstrapClient{flags: &flags, surface: "setup", anonymous: true, carriesSecret: true}
			body := map[string]any{"token": token, "email": email, "password": pw}
			if org := strings.TrimSpace(organizationName); org != "" {
				body["organization"] = org
			}
			raw, err := client.expect(cmd, http.MethodPost, authSetupPath, body, http.StatusCreated)
			if err != nil {
				// The setup token and the password both traveled in that body.
				return redactCoded(err, token, pw)
			}
			var result authSetupResult
			if err := decodeBootstrapJSON("setup", raw, &result); err != nil {
				return err
			}
			if err := renderOut(cmd, func(out io.Writer) error {
				_, werr := fmt.Fprintf(out,
					"setup complete — organization %q (tenant %s) and superadmin %s (id %s)\n",
					safeCLIValue(result.Organization.Name, token),
					safeCLIValue(result.Organization.TenantID, token),
					safeCLIValue(result.Email, token), safeCLIValue(result.ID, token))
				return werr
			}, json.RawMessage(raw)); err != nil {
				return err
			}
			if !save {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"NEXT: authenticate with the account you just created —\n"+
						"      olivares auth login --server %s --email %s --password-file <file>\n",
					safeCLIValue(flags.server, token), safeCLIValue(email, token))
				return nil
			}
			return saveSessionContext(cmd, &flags, contextName, email, pw, result.Organization.TenantID)
		},
	}
	flags.add(cmd)
	cmd.Flags().StringVar(&setupToken, "setup-token", "", "the one-time first-boot token (prefer --setup-token-file: this form is visible in the process table)")
	cmd.Flags().StringVar(&setupTokenFile, "setup-token-file", "", "read the one-time first-boot token from a file, or - for stdin")
	cmd.Flags().StringVar(&email, "email", "", "email address of the first superadmin (required)")
	cmd.Flags().StringVar(&password, "password", "", "password of the first superadmin (prefer --password-file)")
	cmd.Flags().StringVar(&passwordFile, "password-file", "", "read the first superadmin's password from a file, or - for stdin")
	cmd.Flags().StringVar(&organizationName, "organization", "", "name of the first organization (default: \"Default Organization\")")
	cmd.Flags().BoolVar(&save, "save-context", false, "log in as the new superadmin and save the session in a client context")
	cmd.Flags().StringVar(&contextName, "context", "", "context name to create or update with --save-context (default: server hostname)")
	return cmd
}

func newAuthLoginCmd() *cobra.Command {
	var (
		flags                         authClientFlags
		contextName                   string
		email, password, passwordFile string
	)
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Validate a credential and save it in a client context",
		Long: "Validate the effective credential with GET /v1/auth/whoami, then create or\n" +
			"update a named client context and select it. No credential is saved unless the\n" +
			"server accepts it. The default context name is the server hostname.\n\n" +
			"Two credentials are accepted, and they are different things. --token is an existing\n" +
			"bearer (an API token, or a session you already hold). --email with --password-file\n" +
			"exchanges a password for a fresh SESSION — the browser-free equivalent of signing in,\n" +
			"and the only way to use the account `olivares auth bootstrap` just created.",
		Example: `  olivares auth login --server https://plane.example.com --token "$OLIVARES_TOKEN" --tenant tenant-a
  olivares auth login --server https://plane.example.com --email admin@example.com --password-file ./admin.pw
  olivares auth login --server https://lab.example.com --token "$LAB_TOKEN" --context lab --ca-cert ./lab-ca.pem`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolved, err := flags.resolve(cmd)
			if err != nil {
				return redactCoded(err, flags.effectiveToken())
			}
			if resolved.Server == "" {
				return errors.New("no server: set --server, OLIVARES_SERVER_URL, or an active client context")
			}
			// The password leg. It runs only when the operator asked for it, so the
			// bearer-token path above and below is byte-for-byte the one it always
			// was — this ADDS a way in, it does not reroute the existing one.
			password, pwErr := resolveLoginPassword(cmd, password, passwordFile)
			if pwErr != nil {
				return pwErr
			}
			identityExchanged := false
			if strings.TrimSpace(email) != "" || password != "" {
				sessionToken, lerr := passwordLogin(cmd, &flags, email, password)
				if lerr != nil {
					return lerr
				}
				resolved.Token = sessionToken
				identityExchanged = true
				// THE TENANT RESOLVED A MOMENT AGO BELONGS TO WHOEVER WAS CONFIGURED
				// BEFORE THIS EXCHANGE. Keeping it would bind the NEW identity's
				// context to the OLD identity's tenant — a value the new account may
				// hold no membership in — and every later command would come back 403
				// or "tenant required" with nothing on screen connecting it to the
				// login. Drop it and re-derive from whoami below; an explicit
				// --tenant survives and is validated against what this identity holds.
				if !cmd.Flags().Changed("tenant") {
					resolved.Tenant = ""
				}
			}
			if resolved.Token == "" {
				return errors.New("no credential: set --token, OLIVARES_TOKEN, a token in the active client context, " +
					"or sign in with --email and --password-file")
			}
			whoami, err := fetchAuthWhoami(cmd.Context(), resolved, &flags, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			name := strings.TrimSpace(contextName)
			if name == "" {
				u, parseErr := url.Parse(resolved.Server)
				if parseErr != nil || u.Hostname() == "" {
					return errors.New("cannot derive a context name from the server; pass --context")
				}
				name = u.Hostname()
			}
			if strings.ContainsAny(name, "\r\n") {
				return errors.New("context name must not contain newline characters")
			}
			if identityExchanged && resolved.Tenant != "" {
				if err := whoamiHoldsTenant(whoami, resolved.Tenant); err != nil {
					return err
				}
			}
			if resolved.Tenant == "" && len(whoami.Grants) == 1 {
				resolved.Tenant = whoami.Grants[0].Tenant
			}
			cfg, path, err := loadCLIConfig()
			if err != nil {
				return redactCoded(err, resolved.Token)
			}
			cfg.upsertContext(cliContext{
				Name:      name,
				Server:    resolved.Server,
				Token:     resolved.Token,
				Tenant:    resolved.Tenant,
				CACert:    resolved.CACert,
				PinSHA256: append([]string(nil), resolved.PinSHA256...),
			})
			cfg.CurrentContext = name
			if err := writeCLIConfig(path, cfg); err != nil {
				return redactCoded(err, resolved.Token)
			}
			insecureNotPersisted := flags.insecure && resolved.CACert == "" && len(resolved.PinSHA256) == 0
			if err = renderOut(cmd, func(out io.Writer) error {
				_, werr := fmt.Fprintf(out, "login validated; current context set to %q\n", safeCLIValue(name, resolved.Token))
				return werr
			}, authLoginResult{
				Validated:            true,
				Context:              safeCLIValue(name, resolved.Token),
				Server:               safeCLIValue(resolved.Server, resolved.Token),
				Tenant:               safeCLIValue(resolved.Tenant, resolved.Token),
				Actor:                safeCLIValue(firstNonEmptyCLI(whoami.Actor, whoami.DisplayName, whoami.UserID, whoami.Kind), resolved.Token),
				InsecureNotPersisted: insecureNotPersisted,
			}); err != nil {
				return err
			}
			// A CONTEXT THAT CANNOT BE USED MUST NOT REPORT ONLY SUCCESS.
			//
			// --insecure is deliberately NOT a field of cliContext: durably storing
			// "skip TLS verification" in a file is worse than typing it. But the
			// consequence was silent, and it lands on the DEFAULT first-run path —
			// `olivares quickstart` mints a self-signed certificate and says so, so
			// the first `auth login` an evaluator runs is very likely this one.
			//
			// Measured 2026-08-09 against a real quickstart engine:
			//   $ olivares auth login --server https://127.0.0.1:8473 --token ... --insecure
			//   login validated; current context set to "127.0.0.1"      <- exit 0
			//   $ olivares status
			//   Error: Get "https://127.0.0.1:8473/status": tls: failed to verify
			//   certificate: x509: certificate signed by unknown authority   <- exit 6
			//
			// Every later command failed the same way, and nothing in either message
			// connected the second to the first: the x509 error is about a
			// certificate, and the operator's question is "what happened to my
			// login". Saying it here, once, at the moment the context is written, is
			// the only place that can name both halves.
			//
			// ONE condition drives BOTH the note and the DTO's
			// insecure_not_persisted field (computed above): a second copy of this
			// predicate is how the human note and the machine document would come to
			// disagree about whether the context is usable.
			if insecureNotPersisted {
				w := cmd.ErrOrStderr()
				fmt.Fprintf(w, "NOTE: --insecure applied to THIS command only — it is not saved in the context.\n")
				fmt.Fprintf(w, "      The context carries no CA certificate and no pin, so the next command will\n")
				fmt.Fprintf(w, "      fail TLS verification (exit 6, \"certificate signed by unknown authority\").\n")
				fmt.Fprintf(w, "      Either pass --insecure to each command, or re-run with --ca-cert <pem>\n")
				fmt.Fprintf(w, "      so the trust is stored once and every later command works.\n")
			}
			return nil
		},
	}
	flags.add(cmd)
	cmd.Flags().StringVar(&contextName, "context", "", "context name to create or update (default: server hostname)")
	cmd.Flags().StringVar(&email, "email", "", "sign in with this account's password instead of a bearer token")
	cmd.Flags().StringVar(&password, "password", "", "password for --email (prefer --password-file: this form is visible in the process table)")
	cmd.Flags().StringVar(&passwordFile, "password-file", "", "read the password for --email from a file, or - for stdin")
	return cmd
}

// resolveLoginPassword reads the password through the house helper and warns about
// the flag form, which every process on the host can read out of /proc.
func resolveLoginPassword(cmd *cobra.Command, password, passwordFile string) (string, error) {
	pw, err := readSecretValue(cmd, password, passwordFile)
	if err != nil {
		return "", err
	}
	if password != "" {
		fmt.Fprintln(cmd.ErrOrStderr(),
			"WARNING: --password is visible in this host's process table; prefer --password-file (or - for stdin)")
	}
	return pw, nil
}

// passwordLogin exchanges email+password for an opaque SESSION credential.
//
// It refuses to run alongside an explicit --token for a reason that is not
// pedantry: the two are different credentials with different lifetimes and
// different authority, and silently preferring one would save a context the
// operator did not ask for. Say which one you mean.
func passwordLogin(cmd *cobra.Command, flags *authClientFlags, email, password string) (string, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return "", exitcode.New(exitcode.Usage, errors.New("--password/--password-file needs --email: whose password is it?"))
	}
	if password == "" {
		return "", exitcode.New(exitcode.Usage, errors.New(
			"--email needs a password: pass --password-file <file> (or - for stdin)"))
	}
	// AN EXPLICITLY EMPTY --token IS NOT A SECOND CREDENTIAL. The refusal is about
	// ambiguity — two credentials, one context to save — and `--token ""` states
	// the opposite: "use none of the inherited ones". The resolver documents that
	// exact meaning for an explicit empty value (cliconfig.go:38), so refusing it
	// here denied the one spelling an operator has for "ignore my environment",
	// which is precisely what somebody with a stale OLIVARES_TOKEN reaches for.
	if cmd.Flags().Changed("token") && strings.TrimSpace(flags.token) != "" {
		return "", exitcode.New(exitcode.Usage, errors.New(
			"--token and --email are two different credentials; pass one of them"))
	}
	if flags.tokenFile != "" {
		return "", exitcode.New(exitcode.Usage, errors.New(
			"--token-file and --email are two different credentials; pass one of them"))
	}
	client := bootstrapClient{flags: flags, surface: "login", anonymous: true, carriesSecret: true}
	raw, err := client.expect(cmd, http.MethodPost, authLoginPath,
		map[string]any{"email": email, "password": password}, http.StatusOK)
	if err != nil {
		return "", redactCoded(err, password)
	}
	var session authSessionResult
	if err := decodeBootstrapJSON("login", raw, &session); err != nil {
		return "", err
	}
	if session.Token == "" {
		return "", exitcode.New(exitcode.Server, errors.New("the control plane accepted the password but returned no session"))
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "signed in as %s; the session expires at %s\n",
		safeCLIValue(email, password), safeCLIValue(session.ExpiresAt, password))
	return session.Token, nil
}

// whoamiHoldsTenant checks an EXPLICIT --tenant against the identity that just
// signed in, and it is deliberately not an authorization decision — the engine
// makes those, and this cannot grant anything. It refuses to WRITE a context that
// is already known to be unusable, and it refuses only on positive evidence:
//
//   - a superadmin is cross-tenant, so any tenant is legitimate for it;
//   - an identity whose whoami reports no grants at all proves nothing, so the
//     value is kept and the engine gets to answer;
//   - only when whoami lists grants and the requested tenant is not among them is
//     the mismatch certain, and then saying so here beats a 403 three commands
//     later with nothing on screen linking the two.
func whoamiHoldsTenant(whoami authWhoamiResponse, tenant string) error {
	if whoami.Superadmin || len(whoami.Grants) == 0 {
		return nil
	}
	held := make([]string, 0, len(whoami.Grants))
	for _, grant := range whoami.Grants {
		if grant.Tenant == tenant {
			return nil
		}
		held = append(held, grant.Tenant)
	}
	return exitcode.New(exitcode.Usage, fmt.Errorf(
		"--tenant %s: this account holds no membership there (it holds: %s). "+
			"Saving that context would fail every later command with 403",
		safeCLIValue(tenant, ""), strings.Join(held, ", ")))
}

// saveSessionContext is the --save-context tail of `auth bootstrap`: sign in with
// the account just created and store the session, so the next command in a script
// is already authenticated. It goes through the SAME `auth login` code path rather
// than writing the config itself — a second writer of the client context is a
// second set of rules for it.
func saveSessionContext(cmd *cobra.Command, flags *authClientFlags, contextName, email, password, tenant string) error {
	resolved, err := flags.resolve(cmd)
	if err != nil {
		return redactCoded(err, password)
	}
	token, err := passwordLogin(cmd, flags, email, password)
	if err != nil {
		return err
	}
	resolved.Token = token
	// THE TENANT IS THE ONE SETUP JUST CREATED, not the one the environment or a
	// previous context happened to name. This used to prefer the ambient value and
	// fall back to setup's only when it was empty, so a machine with OLIVARES_TENANT
	// still set from an earlier install saved the brand-new superadmin against a
	// tenant it has no membership in. An explicit --tenant still wins: that is the
	// operator saying it on purpose.
	if !cmd.Flags().Changed("tenant") {
		resolved.Tenant = tenant
	}
	// AND THE SESSION IS VALIDATED BEFORE IT IS STORED. The comment above says this
	// goes through the same path as `auth login`; it did not — it wrote the config
	// directly, skipping the whoami check that is the whole reason `auth login`
	// exists ("No credential is saved unless the server accepts it"). Two writers,
	// two sets of rules. This one now runs the same check.
	if _, err := fetchAuthWhoami(cmd.Context(), resolved, flags, cmd.ErrOrStderr()); err != nil {
		return redactCoded(err, password, token)
	}
	name := strings.TrimSpace(contextName)
	if name == "" {
		u, perr := url.Parse(resolved.Server)
		if perr != nil || u.Hostname() == "" {
			return errors.New("cannot derive a context name from the server; pass --context")
		}
		name = u.Hostname()
	}
	if strings.ContainsAny(name, "\r\n") {
		return errors.New("context name must not contain newline characters")
	}
	cfg, path, err := loadCLIConfig()
	if err != nil {
		return redactCoded(err, resolved.Token)
	}
	cfg.upsertContext(cliContext{
		Name:      name,
		Server:    resolved.Server,
		Token:     resolved.Token,
		Tenant:    resolved.Tenant,
		CACert:    resolved.CACert,
		PinSHA256: append([]string(nil), resolved.PinSHA256...),
	})
	cfg.CurrentContext = name
	if err := writeCLIConfig(path, cfg); err != nil {
		return redactCoded(err, resolved.Token)
	}
	_, err = fmt.Fprintf(cmd.ErrOrStderr(), "current context set to %q (tenant %s)\n",
		safeCLIValue(name, resolved.Token), safeCLIValue(resolved.Tenant, resolved.Token))
	return err
}

func newAuthLogoutCmd() *cobra.Command {
	var (
		contextName string
		purge       bool
	)
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Remove a saved token from a client context",
		Long: "Remove the token from the selected or named client context while preserving its\n" +
			"server, tenant and TLS trust settings. Pass --purge to delete the whole context;\n" +
			"purging the current context leaves no context selected.",
		Example: `  olivares auth logout
  olivares auth logout --context lab
  olivares auth logout --context retired --purge`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, path, err := loadCLIConfig()
			if err != nil {
				return err
			}
			name := strings.TrimSpace(contextName)
			if name == "" {
				name = cfg.CurrentContext
			}
			if name == "" {
				return errors.New("no client context selected; pass --context")
			}
			if purge {
				if !cfg.removeContext(name) {
					return exitcode.New(exitcode.NotFound, fmt.Errorf("client context %q does not exist", name))
				}
			} else {
				found := false
				for i := range cfg.Contexts {
					if cfg.Contexts[i].Name == name {
						cfg.Contexts[i].Token = ""
						found = true
						break
					}
				}
				if !found {
					return exitcode.New(exitcode.NotFound, fmt.Errorf("client context %q does not exist", name))
				}
			}
			if err := writeCLIConfig(path, cfg); err != nil {
				return err
			}
			action := "token removed from"
			if purge {
				action = "purged"
			}
			return renderOut(cmd, func(out io.Writer) error {
				_, werr := fmt.Fprintf(out, "%s client context %q\n", action, safeCLIValue(name, ""))
				return werr
			}, authLogoutResult{Context: safeCLIValue(name, ""), Purged: purge})
		},
	}
	cmd.Flags().StringVar(&contextName, "context", "", "context to log out (default: current context)")
	cmd.Flags().BoolVar(&purge, "purge", false, "delete the entire context instead of only its token")
	return cmd
}

func newAuthStatusCmd() *cobra.Command {
	var flags authClientFlags
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the effective CLI identity and authentication context",
		Long: "Resolve the effective server, token and tenant, call GET /v1/auth/whoami, and\n" +
			"print the actor, effective tenant, role, server and active context. The bearer\n" +
			"token is always redacted and is never written in full to command output.",
		Example: `  olivares auth status
  olivares auth status --server https://plane.example.com --token "$OLIVARES_TOKEN" --tenant tenant-a`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolved, err := flags.resolve(cmd)
			if err != nil {
				return redactCoded(err, flags.effectiveToken())
			}
			if resolved.Server == "" {
				return errors.New("no server: set --server, OLIVARES_SERVER_URL, or an active client context")
			}
			if resolved.Token == "" {
				return errors.New("no token: set --token, OLIVARES_TOKEN, or a token in the active client context")
			}
			whoami, err := fetchAuthWhoami(cmd.Context(), resolved, &flags, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			tenant, role := effectiveWhoamiGrant(whoami, resolved.Tenant)
			actor := whoami.Actor
			if actor == "" {
				actor = firstNonEmptyCLI(whoami.DisplayName, whoami.UserID, whoami.Kind)
			}
			if role == "" && whoami.Superadmin {
				role = "superadmin"
			}
			contextDisplay := resolved.ContextName
			if contextDisplay == "" {
				contextDisplay = "<none>"
			}
			if tenant == "" {
				tenant = "<none>"
			}
			if role == "" {
				role = "<none>"
			}
			// THE GLOBAL -o/--output FLAG IS Honored HERE (2026-08-05). This RunE
			// used to write its six KEY<TAB>value lines with fmt.Fprintf and return,
			// ignoring -o entirely: `olivares auth status -o json` emitted output
			// byte-identical to the text form and exited 0, so a caller piping it to
			// jq got a parse error from a command that reported success. It was the
			// only surface in the sweep where the JSON form was not JSON.
			//
			// The token stays redacted in BOTH forms: the redaction happens here, on
			// the value, not in the text formatter.
			lines := [][2]string{
				{"ACTOR", actor},
				{"TENANT", tenant},
				{"ROLE", role},
				{"SERVER", resolved.Server},
				{"CONTEXT", contextDisplay},
				{"TOKEN", redactCLIToken(resolved.Token)},
			}
			payload := make(map[string]string, len(lines))
			for _, line := range lines {
				payload[strings.ToLower(line[0])] = safeCLIValue(line[1], resolved.Token)
			}
			return renderOut(cmd, func(out io.Writer) error {
				for _, line := range lines {
					if _, err := fmt.Fprintf(out, "%s\t%s\n", line[0], safeCLIValue(line[1], resolved.Token)); err != nil {
						return err
					}
				}
				return nil
			}, payload)
		},
	}
	flags.add(cmd)
	return cmd
}

func newAuthUseContextCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use-context <name>",
		Short: "Select the current CLI client context",
		Long: "Select a named client context for subsequent commands. This command is intentionally\n" +
			"under auth: the top-level `olivares config` tree owns engine configuration and\n" +
			"cannot also represent kubeconfig-style client state without a command collision.",
		Example: "  olivares auth use-context production",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, path, err := loadCLIConfig()
			if err != nil {
				return err
			}
			name := args[0]
			if _, ok := cfg.context(name); !ok {
				return exitcode.New(exitcode.NotFound, fmt.Errorf("client context %q does not exist", name))
			}
			cfg.CurrentContext = name
			if err := writeCLIConfig(path, cfg); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				_, werr := fmt.Fprintf(out, "current context set to %q\n", safeCLIValue(name, ""))
				return werr
			}, authUseContextResult{CurrentContext: safeCLIValue(name, "")})
		},
	}
}

func (f *authClientFlags) add(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.server, "server", "", "control-plane base URL (default $OLIVARES_SERVER_URL, then current context)")
	cmd.Flags().StringVar(&f.token, "token", "", "API bearer token (prefer --token-file: this form is visible in the process table and in shell history; default $OLIVARES_TOKEN, then current context)")
	cmd.Flags().StringVar(&f.tokenFile, "token-file", "", "read the API bearer token from a file, or - for stdin")
	cmd.Flags().BoolVar(&f.allowCleartext, "allow-cleartext", false, "allow sending the credential to a non-loopback host over plain HTTP (DANGEROUS: it travels readable)")
	cmd.Flags().StringVar(&f.tenant, "tenant", "", "tenant id (default $OLIVARES_TENANT, then current context)")
	cmd.Flags().StringVar(&f.caCert, "ca-cert", "", "PEM file containing an additional trusted root CA (default: current context)")
	cmd.Flags().StringArrayVar(&f.pins, "pin-sha256", nil, "trusted leaf SPKI SHA-256 pin, base64 or hex, repeatable — the engine prints it as pin_sha256 on the line reporting its certificate (default: current context)")
	cmd.Flags().BoolVar(&f.insecure, "insecure", false, "skip TLS certificate verification (DANGEROUS; development only)")
	cmd.Flags().DurationVar(&f.timeout, "timeout", defaultCLIRequestTimeout, "request timeout")
}

// resolutionOptions builds the resolution inputs for one command invocation. It
// returns an error because --token-file has to be READ, and a file that does not
// exist (or an empty one) is a usage failure the caller must not paper over by
// falling back to the environment: that would answer "the file you named is
// unreadable" by silently using a different credential.
func (f *authClientFlags) resolutionOptions(cmd *cobra.Command) (cliResolutionOptions, error) {
	token, tokenExplicit := f.token, cmd.Flags().Changed("token")
	if f.tokenFile != "" {
		if tokenExplicit && f.token != "" {
			return cliResolutionOptions{}, exitcode.New(exitcode.Usage, errors.New(
				"--token and --token-file are two spellings of one credential; pass one of them"))
		}
		value, err := f.readTokenFile(cmd)
		if err != nil {
			return cliResolutionOptions{}, err
		}
		token, tokenExplicit = value, true
	} else if tokenExplicit && f.token != "" {
		f.warnTokenInArgv(cmd)
	}
	return cliResolutionOptions{
		Server:         f.server,
		Token:          token,
		Tenant:         f.tenant,
		CACert:         f.caCert,
		PinSHA256:      append([]string(nil), f.pins...),
		ServerExplicit: cmd.Flags().Changed("server"),
		TokenExplicit:  tokenExplicit,
		TenantExplicit: cmd.Flags().Changed("tenant"),
		CACertExplicit: cmd.Flags().Changed("ca-cert"),
		PinsExplicit:   cmd.Flags().Changed("pin-sha256"),
	}, nil
}

// resolve is resolutionOptions + resolveCLIConfig, the pair every caller needs.
// It exists so that a new fallible step in the resolution (the token file, today)
// is added in ONE place instead of at nine call sites that could each forget it.
func (f *authClientFlags) resolve(cmd *cobra.Command) (cliResolvedConfig, error) {
	opts, err := f.resolutionOptions(cmd)
	if err != nil {
		return cliResolvedConfig{}, err
	}
	return resolveCLIConfig(opts)
}

func (f *authClientFlags) readTokenFile(cmd *cobra.Command) (string, error) {
	if f.tokenFileRead {
		return f.tokenFileValue, nil
	}
	raw, err := readSecretValue(cmd, "", f.tokenFile)
	if err != nil {
		return "", exitcode.New(exitcode.Usage, fmt.Errorf("read --token-file %s: %w", f.tokenFile, err))
	}
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", exitcode.New(exitcode.Usage, fmt.Errorf(
			"--token-file %s supplied no token; a bearer is required", f.tokenFile))
	}
	f.tokenFileValue, f.tokenFileRead = value, true
	return value, nil
}

// warnTokenInArgv says out loud what `--token <secret>` costs, exactly as
// --password and --setup-token already do (cmd_auth.go:157, :365, cmd_users.go:226).
//
// THE BEARER WAS THE ONE SECRET THIS CLI TOOK IN ARGV WITHOUT SAYING SO. The two
// next to it warned; the longest-lived credential of the three did not, and it is
// the one an operator pastes most often — into a shell that writes it to history,
// in a process whose command line every other process on the host can read out of
// /proc. Adding --token-file gave that operator somewhere to go; the flag stays
// (scripts and the docs use it, and removing a working spelling is not a fix), so
// the warning is what turns the file form from an option nobody notices into the
// one the CLI actually asks for.
//
// It is a warning and not a refusal because a bearer in argv is a real, sometimes
// unavoidable workflow, and because stderr is where this belongs: stdout stays
// machine-readable and the exit code is untouched.
func (f *authClientFlags) warnTokenInArgv(cmd *cobra.Command) {
	if f.warnedTokenArgv {
		return
	}
	f.warnedTokenArgv = true
	fmt.Fprintln(cmd.ErrOrStderr(), cliTokenArgvWarning)
}

// cliTokenArgvWarning is a constant because a test asserts stderr EXACTLY. The
// SARIF export contract is "stdout is the document, stderr is empty unless
// something is wrong" (cmd_findings_test.go), and this line is the one thing that
// may now legitimately appear there. Naming it keeps that test an exact-match
// assertion — strictly stronger than the "want empty" it replaced for that input —
// instead of a substring check that any rewording would quietly loosen.
const cliTokenArgvWarning = "WARNING: --token puts the bearer in this host's process table " +
	"and in your shell history; prefer --token-file <file> (or - for stdin)"

// effectiveToken is the bearer this invocation will actually send, as far as the
// FLAGS know it. Redaction helpers take it so that a token read from a file is
// scrubbed out of an error exactly like one typed on the command line.
func (f *authClientFlags) effectiveToken() string {
	if f.tokenFileRead {
		return f.tokenFileValue
	}
	return f.token
}

func fetchAuthWhoami(ctx context.Context, resolved cliResolvedConfig, flags *authClientFlags, stderr io.Writer) (authWhoamiResponse, error) {
	client, headers, err := cliTransport(cliTransportOptions{
		Resolved: resolved, Insecure: flags.insecure, Timeout: flags.timeout, Stderr: stderr,
		AllowCleartext: flags.allowCleartext,
	})
	if err != nil {
		return authWhoamiResponse{}, redactCodedServer(err, resolved.Token)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, resolved.Server+"/v1/auth/whoami", nil)
	if err != nil {
		return authWhoamiResponse{}, redactCoded(err, resolved.Token)
	}
	req.Header = headers.Clone()
	resp, err := cliDo(client, req)
	if err != nil {
		return authWhoamiResponse{}, redactCodedServer(err, resolved.Token)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return authWhoamiResponse{}, exitcode.New(exitcode.Auth, fmt.Errorf("authentication rejected by server (HTTP %d)", resp.StatusCode))
	}
	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("whoami request failed: HTTP %d", resp.StatusCode)
		if resp.StatusCode >= 500 {
			return authWhoamiResponse{}, exitcode.New(exitcode.Server, err)
		}
		return authWhoamiResponse{}, err
	}
	var out authWhoamiResponse
	dec := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
	if err := dec.Decode(&out); err != nil {
		return authWhoamiResponse{}, exitcode.New(exitcode.Server, fmt.Errorf("decode whoami response: %w", err))
	}
	return out, nil
}

func effectiveWhoamiGrant(whoami authWhoamiResponse, requested string) (string, string) {
	if requested != "" {
		for _, grant := range whoami.Grants {
			if grant.Tenant == requested {
				return requested, grant.Role
			}
		}
		return requested, ""
	}
	if len(whoami.Grants) == 1 {
		return whoami.Grants[0].Tenant, whoami.Grants[0].Role
	}
	return "", ""
}

func redactCLIToken(token string) string {
	if token == "" {
		return "<none>"
	}
	if len(token) <= 4 {
		return "<redacted>"
	}
	prefix := ""
	if i := strings.IndexByte(token, '_'); i >= 0 && i < 8 {
		prefix = token[:i+1]
	}
	return prefix + "…" + token[len(token)-4:]
}

// redactCLISecrets scrubs one or more secrets out of a MESSAGE and hands back a
// STRING. That return type is the point of this function, not a matter of taste.
//
// It used to be `redactCoded(err error, token string) error`, and in that
// shape 19 of its 20 callers destroyed the classification of the error they were
// redacting: errors.New rebuilds a plain error, so the exitcode.coded that
// cliTransport or httpErr attached does not survive it (exitcode.From walks the
// chain with errors.As), and the caller either fell back to the generic 1 or
// pinned whatever constant it had copied from its neighbor. Four of those
// losses were reachable on this tree — the plain-HTTP credential refusal
// (clitransport.go:201) is exit 2, "your invocation is unsafe", and it reached
// the operator's script as exit 6, "the plane is down".
//
// Nothing about the redaction changed; what changed is that a string cannot be
// returned where an error is expected, so the defective form does not compile.
// The only redactors that hand back an error are redactCoded and
// redactCodedServer (bootstrapclient.go), and both read the exit code BEFORE
// they rebuild anything.
//
// An empty secret is skipped, not treated as a match: a caller whose bearer came
// from a client context rather than a flag passes "" here, and replacing every
// empty string in the message would redact the message itself.
// minRedactableCredential es el suelo por debajo del cual sustituir DESTRUYE el texto en vez de
// protegerlo. Medido: con un token de dos caracteres, «no server: set --server, OLIVARES_SERVER_URL,
// or an active client context» sale como
//
//	no server: se<redacted> --server, OLIVARES_SERVER_URL, or an ac<redacted>ive clien<redacted> con<redacted>ex<redacted>
//
// — ilegible, y encima delata la longitud del secreto en cada aparición. El hallazgo y su prueba
// son de otro carril (#824); aquí se portan al scrubber COMPARTIDO en vez de dejar dos helpers
// con semánticas distintas, que es como se fabrica que dos rutas del mismo CLI protejan distinto.
//
// EL SUELO ES 12 PORQUE ES EL SUYO, no un número que yo eligiera: lo justifica midiendo lo que el
// plano EMITE — `core/auth/credential.go:22-41,58-74` acuña `<prefijo>_<selector>_<secreto>` con
// 4 + 26 + 52 = 84 caracteres, así que ninguna credencial real se acerca a 12 y jamás cae por la
// rama de retención. Un suelo inventado por mí habría podido tragarse una credencial corta de
// verdad; éste está atado a lo que el producto emite.
const minRedactableCredential = 12

// ⛔ Y EL PELIGRO QUE otro carril FIJÓ EN #1129 SIGUE EN PIE, porque NO es este código.
// Su aviso es contra `if len(secret) < N { continue }`: saltarse la sustitución imprimiría el
// secreto VERBATIM, y por :184, :408, :458 y :480 llegan contraseñas aquí. Esta rama no hace
// `continue` — RETIENE el mensaje entero. El secreto no se imprime nunca y su longitud tampoco
// se filtra por el número de `<redacted>`. Las dos lecturas coinciden en lo que importa: por
// debajo del suelo no se puede sustituir sin daño, y la respuesta segura es no emitir el texto,
// jamás emitirlo sin tocar.
func redactCLISecrets(msg string, secrets ...string) string {
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		if !strings.Contains(msg, secret) {
			continue
		}
		// RETENER, no triturar: un secreto demasiado corto para sustituirlo sin destrozar el
		// mensaje se responde diciendo POR QUÉ no se puede leer, no dejando ruido.
		if len(secret) < minRedactableCredential {
			return fmt.Sprintf("message withheld: it contains the %d-character value supplied as "+
				"the credential, which is too short to remove without destroying the text; re-run "+
				"with the real credential to read it", len(secret))
		}
		msg = strings.ReplaceAll(msg, secret, "<redacted>")
	}
	return msg
}

func safeCLIValue(value, token string) string {
	if token != "" {
		value = strings.ReplaceAll(value, token, redactCLIToken(token))
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
}

func firstNonEmptyCLI(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
