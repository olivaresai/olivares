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
	"strings"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
)

const authSessionsPath = "/v1/auth/sessions"

type cliAuthSession struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	CreatedAt  string  `json:"created_at"`
	LastUsedAt *string `json:"last_used_at"`
	Kind       string  `json:"kind"`
	Current    bool    `json:"current"`
	ExpiresAt  string  `json:"expires_at"`
}

func newAuthSessionsCmd() *cobra.Command {
	flags := &authClientFlags{}
	root := &cobra.Command{
		Annotations: map[string]string{"changes_data": "read"},
		Use:         "sessions",
		Short:       "List your sign-ins and end another one",
		Long: "Manage the live sign-ins for your own account. List them, end another sign-in,\n" +
			"or create a private, one-time pairing link for a device you control.",
		Example: `  olivares auth sessions ls
  olivares auth sessions end 018f2c2e-0000-7000-8000-000000000002 --yes`,
	}
	flags.addPersistent(root)
	client := bootstrapClient{flags: flags, surface: "auth sessions"}
	root.AddCommand(authSessionsListCmd(client), authSessionsEndCmd(client), authSessionsPairCmd(client), authSessionsAcceptCmd(client))
	return root
}

func authSessionsListCmd(client bootstrapClient) *cobra.Command {
	return &cobra.Command{
		Annotations: map[string]string{"changes_data": "read"},
		Use:         "ls",
		Aliases:     []string{"list"},
		Short:       "List your live sign-ins",
		Long: "List your live sign-ins, including the current one, with their device labels,\n" +
			"last observed client kinds and sign-in times. No credentials are returned.",
		Example: "  olivares auth sessions ls",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			raw, err := client.expect(cmd, http.MethodGet, authSessionsPath, nil, http.StatusOK)
			if err != nil {
				return err
			}
			var list struct {
				Items []cliAuthSession `json:"items"`
			}
			if err := decodeBootstrapJSON("auth sessions", raw, &list); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				if len(list.Items) == 0 {
					_, err := fmt.Fprintln(out, "no live sign-ins")
					return err
				}
				table := termrender.Table{Header: []string{"id", "name", "kind", "current", "last used", "created", "expires"}}
				for _, session := range list.Items {
					table.Rows = append(table.Rows, []string{
						safeCLIValue(session.ID, ""), orDash(safeCLIValue(session.Name, "")), safeCLIValue(session.Kind, ""), flagCell(session.Current),
						orDash(safeCLIValue(derefOrEmpty(session.LastUsedAt), "")), safeCLIValue(session.CreatedAt, ""),
						safeCLIValue(session.ExpiresAt, ""),
					})
				}
				renderTo(out).Table(table)
				return nil
			}, json.RawMessage(raw))
		},
	}
}

func authSessionsEndCmd(client bootstrapClient) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Annotations: map[string]string{"changes_data": "delete"},
		Use:         "end <session-id>",
		Short:       "End one of your other sign-ins",
		Long: "Revoke one of your other sign-ins by its session ID. Use auth sessions ls to find\n" +
			"the ID. To end the current sign-in, use auth logout instead.",
		Example: "  olivares auth sessions end 018f2c2e-0000-7000-8000-000000000002 --yes",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := confirmDestructive(cmd, yes, fmt.Sprintf("end sign-in %q", safeCLIValue(args[0], ""))); err != nil {
				return err
			}
			path := authSessionsPath + "/" + bootstrapPathID(args[0])
			if _, err := client.expect(cmd, http.MethodDelete, path, nil, http.StatusNoContent); err != nil {
				return err
			}
			result := map[string]any{"id": args[0], "ended": true}
			return renderOut(cmd, func(out io.Writer) error {
				_, err := fmt.Fprintf(out, "Ended sign-in %s.\n", safeCLIValue(args[0], ""))
				return err
			}, result)
		},
	}
	addYesFlag(cmd, &yes)
	return cmd
}

// Pairing links carry credentials in their fragment, never in a query or argv.
func authSessionsPairCmd(client bootstrapClient) *cobra.Command {
	return &cobra.Command{
		Annotations: map[string]string{"changes_data": "write"},
		Use:         "pair <device-name>",
		Short:       "Create a one-time sign-in link for another device",
		Long: "Create a private, one-time sign-in link for another device you control.\n" +
			"The link signs in as your account; keep it private and accept it before it expires.",
		Example: "  olivares auth sessions pair \"Work phone\"",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			raw, err := client.expect(cmd, http.MethodPost, authSessionsPath+"/pairing", map[string]string{"name": args[0]}, http.StatusOK)
			if err != nil {
				return err
			}
			var result struct {
				Token     string `json:"token"`
				ExpiresAt string `json:"expires_at"`
			}
			if err := decodeBootstrapJSON("pair device", raw, &result); err != nil {
				return err
			}
			if result.Token == "" {
				return fmt.Errorf("pair device: missing one-time token")
			}
			resolved, err := client.flags.resolve(cmd)
			if err != nil {
				return err
			}
			link := strings.TrimRight(resolved.Server, "/") + "/pair#pair=" + url.QueryEscape(result.Token)
			return renderOut(cmd, func(out io.Writer) error {
				_, err := fmt.Fprintf(out, "Open this private, one-time link on the device, or pass it to auth sessions accept on stdin:\n%s\nExpires: %s\n", link, safeCLIValue(result.ExpiresAt, ""))
				return err
			}, map[string]string{"link": link, "expires_at": result.ExpiresAt})
		},
	}
}

func authSessionsAcceptCmd(client bootstrapClient) *cobra.Command {
	client.anonymous = true
	client.carriesSecret = true
	return &cobra.Command{
		Annotations: map[string]string{"changes_data": "write"},
		Use:         "accept",
		Short:       "Read a one-time pairing link from stdin and save this device's sign-in",
		Long:        "Read a private pairing link from stdin and save the resulting sign-in.\nOnly accept a link you created in your own account: this signs in as its owner.",
		Example:     "  olivares auth sessions accept < pairing-link.txt",
		Args:        cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			input, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 4097))
			if err != nil {
				return fmt.Errorf("read pairing link: %w", err)
			}
			if len(input) > 4096 {
				return fmt.Errorf("pairing link is too long")
			}
			token := strings.TrimSpace(string(input))
			clear(input)
			resolved, err := client.flags.resolve(cmd)
			if err != nil {
				return err
			}
			if strings.Contains(token, "://") {
				link, parseErr := url.Parse(token)
				server, serverErr := url.Parse(resolved.Server)
				if parseErr != nil || serverErr != nil || link.Scheme != server.Scheme || link.Host != server.Host || link.Path != "/pair" || link.RawQuery != "" || link.User != nil {
					return fmt.Errorf("pairing link must belong to the selected server")
				}
				fragment, parseErr := url.ParseQuery(link.Fragment)
				if parseErr != nil {
					return fmt.Errorf("invalid pairing link")
				}
				token = fragment.Get("pair")
			}
			if token == "" {
				return fmt.Errorf("read a pairing link from stdin")
			}
			raw, err := client.expect(cmd, http.MethodPost, authSessionsPath+"/pairing/accept", map[string]string{"token": token}, http.StatusOK)
			if err != nil {
				return redactCoded(err, token)
			}
			var session struct {
				Token string `json:"token"`
			}
			if err := decodeBootstrapJSON("accept pairing", raw, &session); err != nil {
				return fmt.Errorf("invalid pairing response")
			}
			if session.Token == "" {
				return fmt.Errorf("accept pairing: missing session credential")
			}
			resolved.Token = session.Token
			resolved.Tenant = ""
			whoami, err := fetchAuthWhoami(cmd.Context(), resolved, client.flags, cmd.ErrOrStderr())
			if err != nil {
				return redactCoded(err, session.Token)
			}
			if len(whoami.Grants) == 1 {
				resolved.Tenant = whoami.Grants[0].Tenant
			}
			server, err := url.Parse(resolved.Server)
			if err != nil || server.Hostname() == "" {
				return fmt.Errorf("invalid server address")
			}
			cfg, path, err := loadCLIConfig()
			if err != nil {
				return err
			}
			name := server.Hostname()
			cfg.upsertContext(cliContext{Name: name, Server: resolved.Server, Token: session.Token, Tenant: resolved.Tenant, CACert: resolved.CACert, PinSHA256: append([]string(nil), resolved.PinSHA256...)})
			cfg.CurrentContext = name
			if err := writeCLIConfig(path, cfg); err != nil {
				return redactCoded(err, session.Token)
			}
			if client.flags.insecure && resolved.CACert == "" && len(resolved.PinSHA256) == 0 {
				fmt.Fprintln(cmd.ErrOrStderr(), "NOTE: --insecure is not saved. The next command needs a trusted CA or --pin-sha256.")
			}
			return renderOut(cmd, func(out io.Writer) error {
				_, err := fmt.Fprintf(out, "Device paired. Saved as context %q.\n", safeCLIValue(name, ""))
				return err
			}, map[string]any{"paired": true, "context": name})
		},
	}
}
