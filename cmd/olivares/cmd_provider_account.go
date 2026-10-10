// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
	"github.com/olivaresai/olivares/core/model"
)

// cmd_provider_account.go is the CLI half of the provider-account plane: creating
// an account with a home of its own, listing the named accounts, reading one, and
// adopting an existing provider profile as one.
//
// Every verb is a thin HTTP client against /v1/m/sessions/provider-accounts*. The
// server owns the naming rule and the uniqueness of a name, so nothing here
// validates or proposes a name: a CLI copy of the rule would be a second answer to
// the same question, and the two would drift.

const providerAccountsPath = "/v1/m/sessions/provider-accounts"

func newProviderAccountCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "account",
		Short: "Create, list, read, edit and adopt the named provider accounts sessions launch under",
		Long: "account is where a provider profile gets a name. An account IS a provider profile —\n" +
			"same reference, same homes, same launch — that carries a name unique in its\n" +
			"execution environment across every driver, archived accounts included.\n\n" +
			"`account add` is the one command that CREATES a home: the engine builds it on the\n" +
			"node and owns it. A home the operator already had stays theirs, and `account adopt`\n" +
			"is the only way that profile becomes an account. A profile nobody has named is not\n" +
			"an account and `account ls` does not list it.",
		Example: "  olivares provider account add --driver claude\n" +
			"  olivares provider account ls\n" +
			"  olivares provider account adopt ppf_01J8... --name work\n" +
			"  olivares provider account get ppf_01J8... -o json",
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(newProviderAccountAddCmd(), newProviderAccountListCmd(),
		newProviderAccountGetCmd(), newProviderAccountEditCmd(), newProviderAccountAdoptCmd())
	return cmd
}

func newProviderAccountAddCmd() *cobra.Command {
	var (
		cfg                    agentClientConfig
		driver, name, retryKey string
	)
	cmd := &cobra.Command{
		Use: "add",
		Short: "Create a provider account and the home it launches under — exit 5 when the name is taken, " +
			"6 when this node builds no homes",
		Long: "add is the one command that makes a provider home. The engine creates it on the node\n" +
			"that serves this execution environment, under a directory it owns, and registers the\n" +
			"account that launches under it. Nothing outside that directory is created or edited,\n" +
			"and no configuration file of the vendor's is ever written.\n\n" +
			"With --name the account gets exactly that name or the command fails: a name another\n" +
			"account already holds in the environment is refused with exit 5 and is never swapped\n" +
			"for a different one. Without --name the server generates one: the bare driver name\n" +
			"first (claude), then claude-b, claude-c and so on. A name is lowercase ASCII, starts\n" +
			"with a letter, uses letters, digits and '-', and is at most 32 characters; the server\n" +
			"refuses any other shape (exit 1).\n\n" +
			"The home is the engine's own, so the account is recorded as shared isolation: the\n" +
			"child runs as the engine's service user and the home is private by file mode. The\n" +
			"path is never printed in full — the account shows it relative to the accounts root,\n" +
			"and the audit ledger is the one reader given the absolute path.",
		Example: "  olivares provider account add --driver claude\n" +
			"  olivares provider account add --driver claude --name work\n" +
			"  olivares provider account add --driver codex -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			if retryKey == "" {
				retryKey = string(model.NewID())
			}
			if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "Account retry key: %q (reuse with --idempotency-key)\n", retryKey); err != nil {
				return err
			}
			body := map[string]any{"driver": driver, "idempotency_key": retryKey}
			if name != "" {
				body["name"] = name
			}
			return providerAccountCreateCall(cmd, &cfg, body)
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().StringVar(&driver, "driver", "", "the provider driver this account's sessions launch")
	cmd.Flags().StringVar(&name, "name", "", "the account's name; omit it and the server generates one")
	cmd.Flags().StringVar(&retryKey, "idempotency-key", "", "stable create intention; reuse this key after an uncertain result")
	_ = cmd.MarkFlagRequired("driver")
	addDeprecatedJSONFlag(cmd)
	return cmd
}

func newProviderAccountListCmd() *cobra.Command {
	var (
		cfg                        agentClientConfig
		environment, driver, state string
	)
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		// The exit contract is in the summary because the published reference renders
		// a command's summary and never its long help.
		Short: "List the named provider accounts — exit 3 when the caller may not read them",
		Long: "ls shows each account's name, driver, environment, isolation level and state. Only\n" +
			"named rows are accounts, so a provider profile nobody has adopted is not listed here;\n" +
			"`olivares agent profile ls` lists every profile.",
		Example: "  olivares provider account ls\n  olivares provider account ls --driver claude -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			path := providerAccountsPath
			if q := providerAccountListQuery(environment, driver, state); q != "" {
				path += "?" + q
			}
			status, b, err := cfg.do(cmd.Context(), "GET", path, nil)
			if err != nil {
				return err
			}
			if status != 200 {
				return httpErr(status, b)
			}
			var page struct {
				Items []map[string]any `json:"items"`
			}
			if uerr := json.Unmarshal(b, &page); uerr != nil {
				return uerr
			}
			return renderOut(cmd, func(w io.Writer) error {
				return printProviderAccountTable(w, page.Items)
			}, page.Items)
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().StringVar(&environment, "environment", "", "only accounts of this execution environment")
	cmd.Flags().StringVar(&driver, "driver", "", "only accounts of this driver")
	cmd.Flags().StringVar(&state, "state", "", "only accounts in this state (active, disabled or retired)")
	addDeprecatedJSONFlag(cmd)
	return cmd
}

func providerAccountListQuery(environment, driver, state string) string {
	v := url.Values{}
	for key, value := range map[string]string{"environment": environment, "driver": driver, "state": state} {
		if s := strings.TrimSpace(value); s != "" {
			v.Set(key, s)
		}
	}
	return v.Encode()
}

func newProviderAccountGetCmd() *cobra.Command {
	var cfg agentClientConfig
	cmd := &cobra.Command{
		Use:   "get <account-ref>",
		Short: "Show one provider account — exit 4 when no account has that reference",
		Long: "get prints one account's name, driver, environment, home mode and isolation level.\n" +
			"It never prints a home path. A provider profile nobody has named answers as not\n" +
			"found, exactly like an unknown reference.",
		Example: "  olivares provider account get ppf_01J8ABCDEF -o json",
		Args:    exactRef("account-ref"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			return providerAccountPointCall(cmd, &cfg, "GET", "/"+args[0], nil)
		},
	}
	cfg.addFlags(cmd)
	addDeprecatedJSONFlag(cmd)
	return cmd
}

func newProviderAccountEditCmd() *cobra.Command {
	var cfg agentClientConfig
	var name, displayName, accent string
	cmd := &cobra.Command{
		Use:   "edit <account-ref|name>",
		Short: "Rename an account or edit its label and color without changing its home",
		Long: "Set the account's --name, its display label and/or its accent color. An explicit empty\n" +
			"label or color clears that field. The account is named by its reference or by its name.\n" +
			"A rename changes only the name: the reference, home and launch configuration stay as they\n" +
			"were. A name another account holds in the environment is refused with exit 5 and is never\n" +
			"swapped for a different one; a name of the wrong shape is refused with exit 1.\n" +
			"A lost response can be retried with the same fields; a retired account refuses edits.",
		Example: "  olivares provider account edit work --name research\n" +
			"  olivares provider account edit ppf_01J8ABCDEF --display-name 'Research account'\n" +
			"  olivares provider account edit ppf_01J8ABCDEF --display-name ''",
		Args: exactRef("account-ref"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cmd.Flags().Changed("name") && !cmd.Flags().Changed("display-name") && !cmd.Flags().Changed("accent") {
				return exitcode.New(exitcode.Usage, fmt.Errorf("provide --name, --display-name and/or --accent"))
			}
			if err := cfg.resolve(); err != nil {
				return err
			}
			ref := args[0]
			if !strings.HasPrefix(ref, "ppf_") {
				account, err := cfg.findToolAccount(cmd.Context(), ref)
				if err != nil {
					return err
				}
				ref = account.Ref
			}
			body := map[string]any{}
			if cmd.Flags().Changed("name") {
				body["name"] = name
			}
			if cmd.Flags().Changed("display-name") {
				body["display_name"] = displayName
			}
			if cmd.Flags().Changed("accent") {
				body["accent"] = accent
			}
			return providerAccountPointCall(cmd, &cfg, "PATCH", "/"+ref, body)
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().StringVar(&name, "name", "", "the account's new name: lowercase letters, digits and '-', starting with a letter, at most 32 characters")
	cmd.Flags().StringVar(&displayName, "display-name", "", "display label; an explicit empty string clears it")
	cmd.Flags().StringVar(&accent, "accent", "", "accent: orange, green, amber, red or blue; empty clears it")
	addDeprecatedJSONFlag(cmd)
	return cmd
}

func newProviderAccountAdoptCmd() *cobra.Command {
	var (
		cfg  agentClientConfig
		name string
	)
	cmd := &cobra.Command{
		Use: "adopt <profile-ref>",
		Short: "Make an existing provider profile a named account — exit 4 for an unknown profile, " +
			"5 when it is already an account or the name is taken",
		Long: "adopt names an existing provider profile, which is the only way a profile becomes an\n" +
			"account. With --name the account gets exactly that name or the command fails: a name\n" +
			"another account already holds in the environment is refused with exit 5 and is never\n" +
			"swapped for a different one. Without --name the server generates one: the bare driver\n" +
			"name first (claude), then claude-b, claude-c and so on.\n\n" +
			"A name is lowercase ASCII, starts with a letter, uses letters, digits and '-', and is at\n" +
			"most 32 characters; the server refuses any other shape (exit 1).\n\n" +
			"The home is the operator's own, so the account is recorded as shared isolation: the\n" +
			"child runs as the engine's service user. Nothing on disk is moved, created or chowned.",
		Example: "  olivares provider account adopt ppf_01J8ABCDEF --name work\n" +
			"  olivares provider account adopt ppf_01J8ABCDEF",
		Args: exactRef("profile-ref"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			body := map[string]any{}
			if name != "" {
				body["name"] = name
			}
			return providerAccountPointCall(cmd, &cfg, "POST", "/"+args[0]+"/adopt", body)
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().StringVar(&name, "name", "", "the account's name; omit it and the server generates one")
	addDeprecatedJSONFlag(cmd)
	return cmd
}

// providerAccountCreateCall creates an account and renders the one the server
// built. It accepts 201 and nothing else: this verb makes a directory and a row,
// and a plain 200 would mean the engine did something other than create.
func providerAccountCreateCall(cmd *cobra.Command, cfg *agentClientConfig, body any) error {
	status, b, err := cfg.do(cmd.Context(), "POST", providerAccountsPath, body, 201)
	if err != nil {
		return err
	}
	if status != 201 {
		return httpErr(status, b)
	}
	var rec map[string]any
	if uerr := json.Unmarshal(b, &rec); uerr != nil {
		return uerr
	}
	return renderOut(cmd, func(w io.Writer) error {
		return printProviderAccount(w, rec)
	}, rec)
}

// providerAccountPointCall performs one request against a single account and
// renders the account it answered with.
func providerAccountPointCall(cmd *cobra.Command, cfg *agentClientConfig, method, suffix string, body any) error {
	status, b, err := cfg.do(cmd.Context(), method, providerAccountsPath+suffix, body)
	if err != nil {
		return err
	}
	if status != 200 {
		return httpErr(status, b)
	}
	var rec map[string]any
	if uerr := json.Unmarshal(b, &rec); uerr != nil {
		return uerr
	}
	return renderOut(cmd, func(w io.Writer) error {
		return printProviderAccount(w, rec)
	}, rec)
}

// accountIsolationSentence states the boundary an account's child runs behind. It
// is never inferred: an account that does not state one says so.
func accountIsolationSentence(rec map[string]any) string {
	switch str(rec, "isolation_level") {
	case "shared":
		return "shared — the engine's own service user; any process of that user can read this home"
	case "dedicated":
		if user := str(rec, "os_user"); user != "" {
			return "dedicated — the locked system user " + user
		}
		return "dedicated — a locked system user of its own"
	default:
		return "not stated"
	}
}

// accountHomeSentence says who made the account's home and, when the engine made
// it, which directory it is. The directory is named RELATIVE to the accounts
// root, because that is the only form the control plane discloses: it says which
// home this is without saying where on the node the root sits.
func accountHomeSentence(rec map[string]any) string {
	mode := str(rec, "home_mode")
	if rel := str(rec, "home_relative"); rel != "" {
		return mode + " — " + rel
	}
	return mode
}

func providerAccountFields(rec map[string]any) []termrender.Field {
	fields := []termrender.Field{
		{Key: "account", Value: str(rec, "account_ref")},
		{Key: "name", Value: str(rec, "name")},
		{Key: "driver", Value: str(rec, "driver")},
		{Key: "environment", Value: str(rec, "environment_ref")},
		{Key: "state", Value: str(rec, "state")},
		{Key: "home", Value: accountHomeSentence(rec)},
		{Key: "isolation", Value: accountIsolationSentence(rec)},
	}
	if label := str(rec, "display_name"); label != "" {
		fields = append(fields, termrender.Field{Key: "display name", Value: label})
	}
	if accent := str(rec, "accent"); accent != "" {
		fields = append(fields, termrender.Field{Key: "color", Value: accent})
	}
	if ref := str(rec, "provider_record_ref"); ref != "" {
		fields = append(fields, termrender.Field{Key: "provider", Value: ref})
	}
	return fields
}

func printProviderAccount(w io.Writer, rec map[string]any) error {
	renderTo(w).Fields(providerAccountFields(rec))
	return nil
}

func providerAccountTable(items []map[string]any) termrender.Table {
	t := termrender.Table{
		Header: []string{"account", "name", "display name", "driver", "environment", "isolation", "state"},
		Empty:  "No provider accounts.",
	}
	for _, rec := range items {
		t.Rows = append(t.Rows, []string{
			str(rec, "account_ref"), str(rec, "name"), str(rec, "display_name"), str(rec, "driver"),
			str(rec, "environment_ref"), str(rec, "isolation_level"), str(rec, "state"),
		})
	}
	return t
}

func printProviderAccountTable(w io.Writer, items []map[string]any) error {
	r := renderTo(w)
	r.Table(providerAccountTable(items))
	if len(items) == 0 {
		r.Next("olivares provider account adopt <provider-profile-ref> --name <name>")
	}
	return nil
}
