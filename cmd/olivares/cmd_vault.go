// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
	"github.com/olivaresai/olivares/core/auth"
)

// The vault rides the organization's secrets routes in their tenant scope: list, put and
// delete are the secret store's own, reveal and the authenticator code are the vault's.
const (
	vaultSecretsPath = "/v1/console/secrets?scope=tenant"
	vaultRevealPath  = "/v1/console/secrets/reveal?scope=tenant"
	vaultCodePath    = "/v1/console/secrets/totp?scope=tenant"
)

const vaultExitCodes = "Exit codes: 0 done, 2 wrong use, 3 refused (not your item, no step-up, or an API token), 4 no such item or part of it, 5 the item has no authenticator secret, 6 the engine is down or answered badly, 1 any other failure (rate limited, an invalid item, a file that cannot be read)."

func newVaultCmd() *cobra.Command {
	flags := &authClientFlags{}
	cmd := &cobra.Command{
		Annotations: map[string]string{"changes_data": "read"},
		Use:         "vault",
		Short:       "Your personal vault: logins, notes and authenticator codes only you can read back",
		Long: "The personal vault holds your logins, notes and authenticator keys under your own\n" +
			"vault/<your-user-id>/ name in this organization's sealed secrets. Only you can read one\n" +
			"back, with the sign-in step-up this deployment asks for; every read is recorded in the\n" +
			"audit ledger without the value. Values are sealed by the engine's key, not end-to-end\n" +
			"encrypted: see docs/VAULT-TRUST-MODEL.md.\n\n" +
			"Secrets are given in files or on stdin, never on the command line. Every command prints\n" +
			"JSON with -o json.\n" + vaultExitCodes,
		Example: "  olivares vault put bank --username ann --generate\n" +
			"  olivares vault get bank | wl-copy\n" +
			"  olivares vault totp bank",
		Args: cobra.NoArgs,
	}
	flags.addPersistent(cmd)
	cmd.AddCommand(newVaultListCmd(flags), newVaultPutCmd(flags), newVaultGetCmd(flags),
		newVaultRemoveCmd(flags), newVaultTOTPCmd(flags), newVaultGenerateCmd(),
		newVaultImportCmd(flags), newVaultExportCmd(flags))
	return cmd
}

// vaultPrefix is the caller's own vault/<user-id>/ prefix, asked of the engine.
func vaultPrefix(cmd *cobra.Command, cfg agentClientConfig, flags *authClientFlags) (string, error) {
	who, err := fetchAuthWhoami(cmd.Context(), cfg.resolved, flags, cmd.ErrOrStderr())
	if err != nil {
		return "", err
	}
	if who.Kind != "user" || who.UserID == "" {
		return "", sentence(exitcode.Auth, "This credential is not a person's session, so it has no vault. Sign in with `olivares auth login`.")
	}
	return auth.PersonalVaultPrefix + who.UserID + "/", nil
}

// vaultItemName is the full entry name of a label: the person's own vault/<user-id>/ prefix,
// unless the caller already spelled it out.
func vaultItemName(cmd *cobra.Command, cfg agentClientConfig, flags *authClientFlags, arg string) (string, error) {
	if strings.HasPrefix(arg, auth.PersonalVaultPrefix) {
		return arg, nil
	}
	if msg := auth.ValidateSecretName(arg); msg != "" {
		return "", sentence(exitcode.Usage, "%s. Name the item by its label, e.g. bank.", msg)
	}
	prefix, err := vaultPrefix(cmd, cfg, flags)
	if err != nil {
		return "", err
	}
	return prefix + arg, nil
}

// vaultLabel is the label of an entry name: what follows vault/<user-id>/.
func vaultLabel(name string) string {
	parts := strings.SplitN(name, "/", 3)
	if len(parts) == 3 {
		return parts[2]
	}
	return name
}

// vaultKindOf splits an entry's hint into the item's kind and whether it carries an
// authenticator secret.
func vaultKindOf(hint string) (kind string, hasCode bool) {
	kind, rest, _ := strings.Cut(hint, "+")
	if kind != auth.VaultLogin && kind != auth.VaultNote && kind != auth.VaultTOTP {
		return "other", false // a value stored without a shape: its hint is a fingerprint
	}
	return kind, kind == auth.VaultTOTP || rest == auth.VaultTOTP
}

// vaultReveal reads one item back. An item that is not a document (a value stored before
// items had a shape) is returned as raw, with a nil item.
func vaultReveal(cmd *cobra.Command, cfg agentClientConfig, name string) (item *auth.VaultItem, raw string, err error) {
	_, body, err := cfg.do(cmd.Context(), http.MethodPost, vaultRevealPath, map[string]string{"name": name})
	if err != nil {
		return nil, "", redactCoded(err, cfg.token)
	}
	var got struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		return nil, "", exitcode.New(exitcode.Server, fmt.Errorf("decode the vault item: %w", err))
	}
	if got.Value == "" {
		return nil, "", exitcode.New(exitcode.Server, errors.New("the engine answered without a value"))
	}
	// Only a known kind is an item: another JSON document (a service-account key stored
	// before items had a shape) is a raw value, printed as it is.
	var it auth.VaultItem
	if json.Unmarshal([]byte(got.Value), &it) == nil &&
		(it.Type == auth.VaultLogin || it.Type == auth.VaultNote || it.Type == auth.VaultTOTP) {
		return &it, got.Value, nil
	}
	return nil, got.Value, nil
}

// vaultEntry is one row of the secrets list: what a list shows, never a value.
type vaultEntry struct {
	Name        string `json:"name"`
	Hint        string `json:"hint"`
	Description string `json:"description"`
	UpdatedAt   string `json:"updated_at"`
}

// vaultEntries lists the organization's secret entries without opening any of them.
func vaultEntries(cmd *cobra.Command, cfg agentClientConfig) ([]vaultEntry, error) {
	_, body, err := cfg.do(cmd.Context(), http.MethodGet, vaultSecretsPath, nil)
	if err != nil {
		return nil, redactCoded(err, cfg.token)
	}
	var list struct {
		Secrets []vaultEntry `json:"secrets"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, exitcode.New(exitcode.Server, fmt.Errorf("decode the vault list: %w", err))
	}
	return list.Secrets, nil
}

// vaultDescription is the description the engine keeps for an entry, "" when there is none.
func vaultDescription(cmd *cobra.Command, cfg agentClientConfig, name string) (string, error) {
	entries, err := vaultEntries(cmd, cfg)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.Name == name {
			return e.Description, nil
		}
	}
	return "", nil
}

// vaultStore writes one item: the single Put that `vault put` and `vault import` share.
func vaultStore(cmd *cobra.Command, cfg agentClientConfig, name string, item auth.VaultItem, description string) error {
	doc, err := json.Marshal(item)
	if err != nil {
		return err
	}
	if _, _, err = cfg.do(cmd.Context(), http.MethodPut, vaultSecretsPath,
		map[string]string{"name": name, "value": string(doc), "description": description}); err != nil {
		// A proxy in front of the engine may echo the request body in its error.
		return redactCoded(err, cfg.token, string(doc), item.Password, item.TOTP, item.Note)
	}
	return nil
}

// vaultItemReason is why an item was refused, without the package's error prefix.
func vaultItemReason(err error) string {
	return strings.TrimPrefix(err.Error(), auth.ErrVaultItem.Error()+": ")
}

// --- ls ---------------------------------------------------------------------------

type vaultListRow struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Type        string `json:"type"`
	TOTP        bool   `json:"totp"`
	Description string `json:"description"`
	UpdatedAt   string `json:"updated_at"`
}

func newVaultListCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Annotations: map[string]string{"changes_data": "read"},
		Use:         "ls",
		Aliases:     []string{"list"},
		Short:       "List your vault items (names and kinds; never a value)",
		Long: "ls prints one row per item of your vault: its label, its kind (login, note or totp),\n" +
			"whether it carries an authenticator secret, its description and when it changed. It opens\n" +
			"no item and writes nothing to the audit ledger. -o json prints the rows as an array.\n" + vaultExitCodes,
		Example: "  olivares vault ls\n  olivares vault ls -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := mcpClient(cmd, flags)
			if err != nil {
				return err
			}
			entries, err := vaultEntries(cmd, cfg)
			if err != nil {
				return err
			}
			rows := make([]vaultListRow, 0, len(entries))
			for _, s := range entries {
				if !strings.HasPrefix(s.Name, auth.PersonalVaultPrefix) {
					continue
				}
				kind, hasCode := vaultKindOf(s.Hint)
				rows = append(rows, vaultListRow{
					Name: s.Name, Label: vaultLabel(s.Name), Type: kind, TOTP: hasCode,
					Description: s.Description, UpdatedAt: s.UpdatedAt,
				})
			}
			table := termrender.Table{
				Header: []string{"item", "type", "code", "description", "updated"},
				Empty:  "Your vault is empty. Add an item: olivares vault put <name> --password-file -",
			}
			for _, r := range rows {
				code := "-"
				if r.TOTP {
					code = "yes"
				}
				table.Rows = append(table.Rows, []string{termSafe(r.Label), r.Type, code, termSafe(r.Description), r.UpdatedAt})
			}
			return renderOut(cmd, func(w io.Writer) error {
				renderTo(w).Table(table)
				return nil
			}, rows)
		},
	}
}

// --- put --------------------------------------------------------------------------

type vaultPutResult struct {
	Name string `json:"name"`
	Type string `json:"type"`
	TOTP bool   `json:"totp"`
}

// passwordFlags are the generator's switches, shared by `put --generate` and `generate`.
type passwordFlags struct {
	length                                int
	noLower, noUpper, noDigits, noSymbols bool
}

func (p *passwordFlags) add(cmd *cobra.Command) {
	cmd.Flags().IntVar(&p.length, "length", auth.DefaultPasswordLength,
		fmt.Sprintf("password length, %d to %d", auth.MinPasswordLength, auth.MaxPasswordLength))
	cmd.Flags().BoolVar(&p.noLower, "no-lower", false, "leave out lowercase letters")
	cmd.Flags().BoolVar(&p.noUpper, "no-upper", false, "leave out uppercase letters")
	cmd.Flags().BoolVar(&p.noDigits, "no-digits", false, "leave out digits")
	cmd.Flags().BoolVar(&p.noSymbols, "no-symbols", false, "leave out symbols")
}

// generate makes the password with the engine's own generator, in this process.
func (p *passwordFlags) generate() (string, error) {
	pw, err := auth.GeneratePassword(auth.PasswordOptions{
		Length: p.length, Lower: !p.noLower, Upper: !p.noUpper, Digits: !p.noDigits, Symbols: !p.noSymbols,
	})
	if err != nil {
		return "", sentence(exitcode.Usage, "%s.", strings.TrimPrefix(err.Error(), auth.ErrPasswordOptions.Error()+": "))
	}
	return pw, nil
}

func newVaultPutCmd(flags *authClientFlags) *cobra.Command {
	var (
		kind, username, url, description string
		passwordFile, totpFile, noteFile string
		generate                         bool
		pw                               passwordFlags
	)
	cmd := &cobra.Command{
		Annotations: map[string]string{"changes_data": "write"},
		Use:         "put <item>",
		Short:       "Add or replace an item of your vault",
		Long: "put stores one item under the label <item>, replacing the item of that name if there is one.\n" +
			"The kind is login (a username and/or a password, optionally an authenticator key), note\n" +
			"(text) or totp (an authenticator key); it is inferred from what you give. Secrets come from a\n" +
			"file, or - for stdin (only one of them can use stdin), never from the command line:\n" +
			"--password-file, --totp-file (a base32 key or an otpauth:// link) and --note-file.\n" +
			"--generate makes a password for a login (see `olivares vault generate`) and stores it without\n" +
			"printing it. The result lists the label, the kind and whether there is a key, never a value.\n" + vaultExitCodes,
		Example: "  olivares vault put bank --username ann --url https://bank.example --generate\n" +
			"  pass show bank | olivares vault put bank --username ann --password-file -\n" +
			"  olivares vault put github-2fa --totp-file ./github-key\n" +
			"  olivares vault put safe --note-file ./safe-code",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if msg := auth.ValidateSecretName(strings.TrimPrefix(args[0], auth.PersonalVaultPrefix)); msg != "" {
				return sentence(exitcode.Usage, "%s. Name the item by its label, e.g. bank.", msg)
			}
			item, err := vaultPutItem(cmd, kind, username, url, passwordFile, totpFile, noteFile, generate, &pw)
			if err != nil {
				return err
			}
			cfg, err := mcpClient(cmd, flags)
			if err != nil {
				return err
			}
			name, err := vaultItemName(cmd, cfg, flags, args[0])
			if err != nil {
				return err
			}
			if !cmd.Flags().Changed("description") {
				// Replacing an item keeps the line its list shows, unless a new one is given.
				if description, err = vaultDescription(cmd, cfg, name); err != nil {
					return err
				}
			}
			if err := vaultStore(cmd, cfg, name, item, description); err != nil {
				return err
			}
			res := vaultPutResult{Name: name, Type: item.Type, TOTP: item.TOTP != ""}
			return renderOut(cmd, func(w io.Writer) error {
				_, err := fmt.Fprintf(w, "Stored %s (%s). Read it back: olivares vault get %s\n", termSafe(vaultLabel(name)), item.Type, shellWord(vaultLabel(name)))
				return err
			}, res)
		},
	}
	cmd.Flags().StringVar(&kind, "type", "", "login, note or totp (default: inferred from what you give)")
	cmd.Flags().StringVar(&username, "username", "", "the login's username")
	cmd.Flags().StringVar(&url, "url", "", "the login's website")
	cmd.Flags().StringVar(&passwordFile, "password-file", "", "read the password from a file, or - for stdin")
	cmd.Flags().BoolVar(&generate, "generate", false, "generate the password instead of reading it")
	pw.add(cmd)
	cmd.Flags().StringVar(&totpFile, "totp-file", "", "read the authenticator key (base32 or otpauth:// link) from a file, or - for stdin")
	cmd.Flags().StringVar(&noteFile, "note-file", "", "read the note from a file, or - for stdin")
	cmd.Flags().StringVar(&description, "description", "", "a non-secret description shown in the list")
	return cmd
}

// vaultPutItem builds the item `put` stores from its flags, refusing every invocation that
// cannot make one before anything is sent.
func vaultPutItem(cmd *cobra.Command, kind, username, url, passwordFile, totpFile, noteFile string, generate bool, pw *passwordFlags) (auth.VaultItem, error) {
	usage := func(format string, args ...any) (auth.VaultItem, error) {
		return auth.VaultItem{}, sentence(exitcode.Usage, format, args...)
	}
	stdin := 0
	for _, f := range []string{passwordFile, totpFile, noteFile} {
		if f == "-" {
			stdin++
		}
	}
	if stdin > 1 {
		return usage("Only one value can come from stdin. Put the others in files.")
	}
	if generate && passwordFile != "" {
		return usage("Give --generate or --password-file, not both.")
	}
	if kind == "" {
		switch {
		case generate || passwordFile != "" || username != "" || url != "":
			kind = auth.VaultLogin
		case noteFile != "":
			kind = auth.VaultNote
		case totpFile != "":
			kind = auth.VaultTOTP
		default:
			return usage("Nothing to store. Give a password (--password-file or --generate), a note (--note-file) or an authenticator key (--totp-file).")
		}
	}
	switch kind {
	case auth.VaultLogin:
	case auth.VaultNote:
		if generate || passwordFile != "" || username != "" || url != "" || totpFile != "" {
			return usage("A note holds only text. Give --note-file alone, or use --type login.")
		}
	case auth.VaultTOTP:
		if generate || passwordFile != "" || username != "" || url != "" {
			return usage("An authenticator item holds only its key (and a note). Use --type login for a login with a key.")
		}
	default:
		return usage("--type must be login, note or totp.")
	}
	item := auth.VaultItem{Type: kind, Username: username, URL: url}
	read := func(file, what string) (string, error) {
		if file == "" {
			return "", nil
		}
		v, err := readSecretValue(cmd, "", file)
		if err != nil {
			return "", err
		}
		if v == "" {
			return "", sentence(exitcode.Usage, "The %s is empty.", what)
		}
		return v, nil
	}
	var err error
	if generate {
		if item.Password, err = pw.generate(); err != nil {
			return auth.VaultItem{}, err
		}
	} else if item.Password, err = read(passwordFile, "password file"); err != nil {
		return auth.VaultItem{}, err
	}
	if item.TOTP, err = read(totpFile, "authenticator key file"); err != nil {
		return auth.VaultItem{}, err
	}
	if item.Note, err = read(noteFile, "note file"); err != nil {
		return auth.VaultItem{}, err
	}
	if err := item.Validate(); err != nil {
		// Validate names the field at fault and never repeats a value.
		return usage("%s.", vaultItemReason(err))
	}
	return item, nil
}

// --- get --------------------------------------------------------------------------

// vaultGetResult is `get -o json`: the whole item, with `value` being what text mode prints.
type vaultGetResult struct {
	Name     string `json:"name"`
	Type     string `json:"type,omitempty"`
	Value    string `json:"value"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	URL      string `json:"url,omitempty"`
	Note     string `json:"note,omitempty"`
	TOTP     string `json:"totp,omitempty"`
}

func newVaultGetCmd(flags *authClientFlags) *cobra.Command {
	var field string
	cmd := &cobra.Command{
		Annotations: map[string]string{"changes_data": "read"},
		Use:         "get <item>",
		Short:       "Print one part of an item of your vault",
		Long: "get prints one part of one of your own items, and nothing else, so it can be piped: the\n" +
			"password of a login, the text of a note, the key of an authenticator item. --field picks\n" +
			"another part: username, password, url, note or totp (the key itself; `olivares vault totp`\n" +
			"prints the current code). The item is the label you stored it under, or its full\n" +
			"vault/<user-id>/<label> name.\n" +
			"It needs a signed-in session (`olivares auth login --token-file <file>`) that has done the\n" +
			"step-up this deployment asks for; an API token is refused. The read is audited and rate\n" +
			"limited. -o json prints the whole item; its value is what text mode prints, empty when the item\n" +
			"lacks the part.\n" + vaultExitCodes,
		Example: "  olivares vault get bank\n  olivares vault get bank --field username\n  olivares vault get bank | wl-copy",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch field {
			case "", "username", "password", "url", "note", "totp":
			default:
				return sentence(exitcode.Usage, "--field must be username, password, url, note or totp.")
			}
			cfg, err := mcpClient(cmd, flags)
			if err != nil {
				return err
			}
			name, err := vaultItemName(cmd, cfg, flags, args[0])
			if err != nil {
				return err
			}
			item, raw, err := vaultReveal(cmd, cfg, name)
			if err != nil {
				return err
			}
			res := vaultGetResult{Name: name, Value: raw}
			if item != nil {
				res = vaultGetResult{
					Name: name, Type: item.Type, Username: item.Username, Password: item.Password,
					URL: item.URL, Note: item.Note, TOTP: item.TOTP,
				}
				res.Value = vaultPrimary(*item)
				if field != "" {
					res.Value = map[string]string{"username": item.Username, "password": item.Password,
						"url": item.URL, "note": item.Note, "totp": item.TOTP}[field]
				}
			}
			return renderOut(cmd, func(w io.Writer) error {
				// Text mode prints one part, so a part the item lacks has nothing to print (a
				// login with only a username is never printed as if it were the password);
				// -o json prints the item as it is, with an empty value.
				switch {
				case field == "totp" && res.Value == "":
					return sentence(exitcode.Conflict, "This item has no authenticator secret.")
				case field != "" && (item == nil || res.Value == ""):
					return sentence(exitcode.NotFound, "This item has no %s.", field)
				case item != nil && res.Value == "":
					return sentence(exitcode.NotFound, "This item has no password. Pick a part with --field (username, url, note or totp).")
				}
				_, err := fmt.Fprintln(w, res.Value)
				return err
			}, res)
		},
	}
	cmd.Flags().StringVar(&field, "field", "", "the part to print: username, password, url, note or totp (default: the password, note or key)")
	return cmd
}

// vaultPrimary is what `get` prints without --field.
func vaultPrimary(it auth.VaultItem) string {
	switch it.Type {
	case auth.VaultNote:
		return it.Note
	case auth.VaultTOTP:
		return it.TOTP
	}
	return it.Password
}

// --- rm ---------------------------------------------------------------------------

func newVaultRemoveCmd(flags *authClientFlags) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Annotations: map[string]string{"changes_data": "delete"},
		Use:         "rm <item>",
		Aliases:     []string{"remove", "delete"},
		Short:       "Delete an item of your vault",
		Long: "rm deletes one of your own items for good. It asks for confirmation on a terminal; in a\n" +
			"script pass --yes to state the intent. It needs the same sign-in step-up as put.\n" + vaultExitCodes,
		Example: "  olivares vault rm bank\n  olivares vault rm bank --yes -o json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := confirmDestructive(cmd, yes, fmt.Sprintf("delete the vault item %q", args[0])); err != nil {
				return err
			}
			cfg, err := mcpClient(cmd, flags)
			if err != nil {
				return err
			}
			name, err := vaultItemName(cmd, cfg, flags, args[0])
			if err != nil {
				return err
			}
			if _, _, err := cfg.do(cmd.Context(), http.MethodDelete, vaultSecretsPath,
				map[string]string{"name": name}, http.StatusNoContent); err != nil {
				return redactCoded(err, cfg.token)
			}
			return renderOut(cmd, func(w io.Writer) error {
				_, err := fmt.Fprintf(w, "Deleted %s.\n", termSafe(vaultLabel(name)))
				return err
			}, map[string]any{"name": name, "deleted": true})
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

// --- totp -------------------------------------------------------------------------

type vaultCodeResult struct {
	Name        string `json:"name"`
	Code        string `json:"code"`
	SecondsLeft int    `json:"seconds_left"`
	Period      int    `json:"period"`
	Digits      int    `json:"digits"`
}

func newVaultTOTPCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Annotations: map[string]string{"changes_data": "read"},
		Use:         "totp <item>",
		Short:       "Print the current authenticator code of an item",
		Long: "totp prints the current one-time code (RFC 6238) of an item that carries an authenticator key,\n" +
			"and nothing else on stdout, so it can be piped; how many seconds it stays valid goes to stderr.\n" +
			"The key itself never leaves the engine. Like get it needs a signed-in session with the\n" +
			"deployment's step-up, is rate limited, and is recorded in the audit ledger without the code.\n" +
			"-o json prints the code, the seconds left, the period and the digits.\n" + vaultExitCodes,
		Example: "  olivares vault totp github-2fa\n  olivares vault totp bank -o json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := mcpClient(cmd, flags)
			if err != nil {
				return err
			}
			name, err := vaultItemName(cmd, cfg, flags, args[0])
			if err != nil {
				return err
			}
			_, body, err := cfg.do(cmd.Context(), http.MethodPost, vaultCodePath, map[string]string{"name": name})
			if err != nil {
				return redactCoded(err, cfg.token)
			}
			res := vaultCodeResult{Name: name}
			if err := json.Unmarshal(body, &res); err != nil || res.Code == "" {
				return exitcode.New(exitcode.Server, errors.New("the engine answered without a code"))
			}
			return renderOut(cmd, func(w io.Writer) error {
				if _, err := fmt.Fprintln(w, res.Code); err != nil {
					return err
				}
				_, err := fmt.Fprintf(cmd.ErrOrStderr(), "valid for %d s\n", res.SecondsLeft)
				return err
			}, res)
		},
	}
}

// --- generate ---------------------------------------------------------------------

func newVaultGenerateCmd() *cobra.Command {
	var pw passwordFlags
	cmd := &cobra.Command{
		Annotations: map[string]string{"changes_data": "read"},
		Use:         "generate",
		Short:       "Print a random password",
		Long: "generate prints a random password and nothing else, so it can be piped. It runs in this\n" +
			"process with the engine's own generator (crypto/rand, at least one character of every chosen\n" +
			"kind) and needs no server or sign-in; nothing is stored. The default is 20 characters of\n" +
			"lowercase and uppercase letters, digits and symbols. -o json prints {\"password\": ...}.\n" +
			"Exit codes: 0 done, 2 wrong use (a length outside the range, or every kind left out).",
		Example: "  olivares vault generate\n  olivares vault generate --length 32 --no-symbols",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			password, err := pw.generate()
			if err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				_, err := fmt.Fprintln(w, password)
				return err
			}, map[string]string{"password": password})
		},
	}
	pw.add(cmd)
	return cmd
}
