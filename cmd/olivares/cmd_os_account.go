// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/nativepam"
	"github.com/spf13/cobra"
)

const osAccountBindingsPath = "/v1/auth/os-account-bindings"

func newAuthOSAccountCmd() *cobra.Command {
	var flags authClientFlags
	cmd := &cobra.Command{Use: "os-account", Short: "Bind a product account to an immutable native OS account", Long: "An administrator begins the binding, then the product user proves control of the native account from their own sign-in. Use get to inspect the binding.", Example: "  olivares auth os-account begin <user-id> <os-account>"}
	flags.addPersistent(cmd)
	client := bootstrapClient{flags: &flags, surface: "auth os-account"}
	begin := &cobra.Command{Use: "begin <user-id> <os-account>", Short: "Start as an administrator; the subject completes its own proof", Long: "Begin a native account binding as an administrator. Give the returned ceremony ID to the product user so they can run complete from their own sign-in.", Example: "  olivares auth os-account begin <user-id> <os-account>", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		resolved, err := flags.resolve(cmd)
		if err != nil {
			return err
		}
		raw, err := client.expect(cmd, http.MethodPost, osAccountBindingsPath, map[string]any{"tenant": resolved.Tenant, "user_id": args[0], "account": args[1]}, http.StatusOK)
		if err != nil {
			return err
		}
		var out auth.OSAccountCeremony
		if err = decodeBootstrapJSON(client.surface, raw, &out); err != nil {
			return err
		}
		if out.ID.IsZero() {
			return errors.New("engine returned no binding ceremony")
		}
		return renderOut(cmd, func(w io.Writer) error { _, e := fmt.Fprintln(w, out.ID); return e }, out)
	}}
	var passwordFile string
	complete := &cobra.Command{Use: "complete <ceremony-id>", Short: "Prove native account control from the subject's own sign-in", Long: "Complete a binding from the product user's own sign-in over HTTPS, reading the native password from a file or stdin. An administrator can then inspect the binding with get.", Example: "  olivares auth os-account complete <ceremony-id> --password-file -", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		// Refuse cleartext before reading any credential. The server also enforces TLS.
		resolved, err := flags.resolve(cmd)
		if err != nil {
			return err
		}
		endpoint, err := url.Parse(resolved.Server)
		if err != nil || endpoint.Scheme != "https" {
			return exitcode.New(exitcode.Usage, errors.New("native account proof requires an HTTPS engine"))
		}
		if passwordFile == "-" && flags.tokenFile == "-" {
			return exitcode.New(exitcode.Usage, errors.New("token and password cannot both read stdin"))
		}
		password, err := osAccountPassword(cmd, passwordFile)
		if err != nil {
			return err
		}
		defer clear(password)
		proofClient := client
		proofClient.carriesSecret = true
		raw, err := proofClient.expect(cmd, http.MethodPost, osAccountBindingsPath+"/complete", map[string]any{"ceremony_id": args[0], "password": password}, http.StatusOK)
		if err != nil {
			return osAccountProofError(err, password)
		}
		defer clear(raw)
		var mapping auth.OSAccountMapping
		if err := decodeBootstrapJSON(client.surface, raw, &mapping); err != nil {
			return osAccountProofError(err, password)
		}
		// Render accepted completion as success, scrubbing only reflected values.
		// A password matching a fixed metadata key is not a failed transaction.
		for _, proof := range []string{string(password), base64.StdEncoding.EncodeToString(password)} {
			mapping.Account = strings.ReplaceAll(mapping.Account, proof, "<redacted>")
			mapping.Digest = strings.ReplaceAll(mapping.Digest, proof, "<redacted>")
			mapping.User = model.ID(strings.ReplaceAll(mapping.User.String(), proof, "<redacted>"))
			mapping.Tenant = model.TenantID(strings.ReplaceAll(string(mapping.Tenant), proof, "<redacted>"))
		}
		return renderOSAccountMapping(cmd, mapping)
	}}
	complete.Flags().StringVar(&passwordFile, "password-file", "", "read the native password from a file, or - for stdin")
	read := &cobra.Command{Use: "get <user-id>", Short: "Read administrative mapping metadata", Long: "Read a product user's native account binding as an administrator. Use revoke to remove its authority while retaining the immutable account reservation.", Example: "  olivares auth os-account get <user-id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		raw, err := client.expect(cmd, http.MethodGet, osAccountBindingsPath+"/"+bootstrapPathID(args[0]), nil, http.StatusOK)
		if err != nil {
			return err
		}
		return renderOSAccount(cmd, raw)
	}}
	var yes bool
	revoke := &cobra.Command{Use: "revoke <user-id>", Short: "Revoke authority and retain the immutable account reservation", Long: "Revoke a native account binding's authority as an administrator; its account reservation remains permanent. Use get to inspect the retained metadata.", Example: "  olivares auth os-account revoke <user-id> --yes", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := confirmDestructive(cmd, yes, "revoke this OS-account binding (its account reservation remains permanent)"); err != nil {
			return err
		}
		if _, err := client.expect(cmd, http.MethodDelete, osAccountBindingsPath+"/"+bootstrapPathID(args[0]), nil, http.StatusOK); err != nil {
			return err
		}
		return renderOut(cmd, func(w io.Writer) error { _, e := fmt.Fprintln(w, "OS-account authority revoked"); return e }, map[string]bool{"ok": true})
	}}
	addYesFlag(revoke, &yes)
	cmd.AddCommand(begin, complete, read, revoke)
	return cmd
}

func osAccountPassword(cmd *cobra.Command, path string) ([]byte, error) {
	if path == "" {
		return nil, exitcode.New(exitcode.Usage, errors.New("use --password-file <file>, or - for stdin"))
	}
	var input io.Reader = cmd.InOrStdin()
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		input = f
	}
	password, err := io.ReadAll(io.LimitReader(input, nativepam.MaxPassword+3))
	if err != nil {
		clear(password)
		return nil, err
	}
	if bytes.HasSuffix(password, []byte{'\n'}) {
		password = password[:len(password)-1]
		if bytes.HasSuffix(password, []byte{'\r'}) {
			password = password[:len(password)-1]
		}
	}
	if !nativepam.ValidRequest("native", password) {
		clear(password)
		return nil, exitcode.New(exitcode.Usage, errors.New("native password must contain 1 to 4096 bytes and no NUL"))
	}
	return password, nil
}

func renderOSAccount(cmd *cobra.Command, raw []byte) error {
	var mapping auth.OSAccountMapping
	if err := decodeBootstrapJSON("auth os-account", raw, &mapping); err != nil {
		return err
	}
	return renderOSAccountMapping(cmd, mapping)
}

func renderOSAccountMapping(cmd *cobra.Command, mapping auth.OSAccountMapping) error {
	return renderOut(cmd, func(w io.Writer) error {
		_, e := fmt.Fprintf(w, "%s (UID %d) -> %s\n", safeCLIValue(mapping.Account, ""), mapping.UID, safeCLIValue(mapping.User.String(), ""))
		return e
	}, mapping)
}

// Keep public classification and cancellation identity without retaining a
// server error that may contain the submitted native proof in its cause chain.
func osAccountProofError(err error, password []byte) error {
	redacted := redactCodedServer(err, string(password), base64.StdEncoding.EncodeToString(password))
	if redacted == nil {
		return nil
	}
	safe := errors.New(redacted.Error())
	for _, sentinel := range []error{context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, sentinel) {
			safe = fmt.Errorf("%s: %w", redacted.Error(), sentinel)
			break
		}
	}
	return exitcode.New(exitcode.From(redacted), safe)
}
