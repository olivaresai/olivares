// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/spf13/cobra"
)

func newAccountCmd() *cobra.Command {
	flags := &authClientFlags{}
	cmd := &cobra.Command{
		Use: "account", Short: "Manage your own account",
		Long:    "Manage your signed-in account. Change your password and end your other sign-ins with account password.",
		Example: "  olivares account password",
	}
	flags.addPersistent(cmd)
	cmd.AddCommand(newAccountPasswordCmd(bootstrapClient{flags: flags, surface: "account", carriesSecret: true}))
	return cmd
}

func newAccountPasswordCmd(client bootstrapClient) *cobra.Command {
	var currentFile, passwordFile string
	cmd := &cobra.Command{
		Use: "password", Short: "Change your own password and sign out your other sessions",
		Long: "Change the password of the signed-in account. Your current password is required.\n" +
			"At a terminal, passwords are prompted with echo off; confirm the new password.\n" +
			"Scripts supply --current-password-file and --password-file (one may be - for stdin).\n" +
			"This sign-in stays active; your other sign-ins are ended. Identity-provider passwords\n" +
			"are changed at that provider. Sign in first with olivares login.",
		Example: "  olivares account password\n  olivares account password --current-password-file ./current.pw --password-file ./new.pw",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if client.flags.tokenFile == "-" && (currentFile == "" || currentFile == "-" || passwordFile == "" || passwordFile == "-") {
				return sentence(exitcode.Usage, "The sign-in token and a password cannot both read stdin. Put them in separate files.")
			}
			current, next, err := accountPasswords(cmd, currentFile, passwordFile)
			if err != nil {
				return err
			}
			_, _, _, err = client.do(cmd, http.MethodPost, "/v1/account/password", map[string]string{"current_password": current, "new_password": next}, http.StatusNoContent)
			if err != nil {
				return redactCoded(err, current, next)
			}
			return renderOut(cmd, func(out io.Writer) error {
				_, err := fmt.Fprintln(out, "Password changed. Your other sign-ins have ended.")
				return err
			}, map[string]bool{"changed": true})
		},
	}
	cmd.Flags().StringVar(&currentFile, "current-password-file", "", "read your current password from a file, or - for stdin")
	cmd.Flags().StringVar(&passwordFile, "password-file", "", "read your new password from a file, or - for stdin")
	return cmd
}

func accountPasswords(cmd *cobra.Command, currentFile, passwordFile string) (string, string, error) {
	usage := func(message string) (string, string, error) {
		return "", "", exitcode.New(exitcode.Usage, errors.New(message))
	}
	if currentFile == "-" && passwordFile == "-" {
		return usage("Both passwords cannot read stdin. Put one in a file.")
	}
	in := cmd.InOrStdin()
	if (currentFile == "" || passwordFile == "") && !interactiveStdin(in) {
		return usage("Use --current-password-file and --password-file, or run this command at a terminal.")
	}
	current, err := readSecretValue(cmd, "", currentFile)
	if err != nil {
		return "", "", err
	}
	next, err := readSecretValue(cmd, "", passwordFile)
	if err != nil {
		return "", "", err
	}
	reader := bufio.NewReader(in)
	read := func(label string) (string, error) {
		_, _ = fmt.Fprint(cmd.ErrOrStderr(), label+": ")
		value, err := readHiddenInput(in, reader)
		_, _ = fmt.Fprintln(cmd.ErrOrStderr())
		if err != nil {
			return "", sentence(exitcode.Usage, "Could not read the password from this terminal. Use --current-password-file and --password-file.")
		}
		return value, nil
	}
	if currentFile == "" {
		current, err = read("Current password")
		if err != nil {
			return "", "", err
		}
	}
	if passwordFile == "" {
		next, err = read("New password")
		if err != nil {
			return "", "", err
		}
		confirm, err := read("Confirm new password")
		if err != nil {
			return "", "", err
		}
		if next != confirm {
			return usage("The passwords do not match.")
		}
	}
	if current == "" || next == "" {
		return usage("Enter your current and new passwords.")
	}
	return current, next, nil
}
