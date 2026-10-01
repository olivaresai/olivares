// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/spf13/cobra"
)

// TOTP second-factor administration from the CLI . The account holder does
// enrolment in the console (a QR cannot be scanned from a terminal usefully);
// these verbs are the OPERATOR side: inspect a factor, reset a lost one, and
// read/set the require-for-administrators policy. A thin client over
// /v1/users/{id}/totp and /v1/auth/totp/policy — the engine holds every
// decision (membership:read / membership:write / system:admin), and the two
// write verbs additionally carry the AAL3 step-up, which an API token can
// never hold but a console-elevated session can (cmd_users.go documents the
// same reach for enable/disable).

// cliTOTPStatus mirrors the non-secret core/auth.TOTPStatus wire shape.
type cliTOTPStatus struct {
	Enrolled               bool   `json:"enrolled"`
	Algorithm              string `json:"algorithm"`
	Digits                 int    `json:"digits"`
	Period                 int    `json:"period"`
	SeedHint               string `json:"seed_hint"`
	ActivatedAt            string `json:"activated_at"`
	RecoveryCodesRemaining int    `json:"recovery_codes_remaining"`
}

func usersTOTPCmd(client bootstrapClient) *cobra.Command {
	return &cobra.Command{
		Use:   "totp <user-id>",
		Short: "Show an account's TOTP second factor (non-secret)",
		Long: "Report whether a local account holds a confirmed TOTP factor: algorithm, activation\n" +
			"time and how many recovery codes remain. Never key material. Tenant-scoped read\n" +
			"(membership:read) resolved from --tenant like every tenant verb.",
		Example: "  olivares users totp 018f2c2e-0000-7000-8000-000000000002",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			raw, err := client.expect(cmd, http.MethodGet, usersPath+"/"+bootstrapPathID(args[0])+"/totp", nil, http.StatusOK)
			if err != nil {
				return err
			}
			var status cliTOTPStatus
			if err := decodeBootstrapJSON("users totp", raw, &status); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				if !status.Enrolled {
					_, werr := fmt.Fprintf(out, "no TOTP factor enrolled\n")
					return werr
				}
				_, werr := fmt.Fprintf(out,
					"enrolled %s digits=%d period=%ds algorithm=%s\nactivated %s\nrecovery codes remaining: %d\n",
					status.SeedHint, status.Digits, status.Period, status.Algorithm,
					safeCLIValue(status.ActivatedAt, "unknown"), status.RecoveryCodesRemaining)
				return werr
			}, json.RawMessage(raw))
		},
	}
}

func usersTOTPResetCmd(client bootstrapClient) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "totp-reset <user-id>",
		Short: "Reset an account's TOTP factor (destructive; requires an AAL3 session)",
		Long: "Delete an account's TOTP factor and every recovery code — the lost-device path.\n" +
			"The account enroles again at next login when the require-for-administrators policy\n" +
			"demands one, or whenever it chooses to. Tenant-scoped membership:write plus the\n" +
			"AAL3 step-up: an API token can never carry one, but a session elevated by a\n" +
			"WebAuthn/PIV ceremony in the console can, for 15 minutes.",
		Example: "  olivares users totp-reset 018f2c2e-0000-7000-8000-000000000002 --yes",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := confirmDestructive(cmd, yes, fmt.Sprintf(
				"reset the TOTP factor of account %q (the factor and its recovery codes are deleted)",
				safeCLIValue(args[0], ""))); err != nil {
				return err
			}
			path := usersPath + "/" + bootstrapPathID(args[0]) + "/totp/reset"
			if _, err := client.expect(cmd, http.MethodPost, path, map[string]any{}, http.StatusOK); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				_, werr := fmt.Fprintf(out, "TOTP factor reset; the account enroles again at next login\n")
				return werr
			}, nil)
		},
	}
	addYesFlag(cmd, &yes)
	return cmd
}

func newAuthTOTPPolicyCmd() *cobra.Command {
	var flags authClientFlags
	cmd := authTOTPPolicyCmd(&flags)
	flags.addPersistent(cmd)
	return cmd
}

func authTOTPPolicyCmd(flags *authClientFlags) *cobra.Command {
	client := bootstrapClient{flags: flags, surface: "auth totp-policy"}
	var requireForAdmins bool
	cmd := &cobra.Command{
		Use:   "totp-policy",
		Short: "Read or set the require-TOTP-for-administrators policy (system:admin)",
		Long: "The deployment-wide local-account policy: when on, administrators (superadmins and\n" +
			"any tenant admin/owner) must enrol a TOTP factor before their password login\n" +
			"completes. Reading needs system:admin; setting it also carries the AAL3 step-up,\n" +
			"which an API token can never hold but a console-elevated session can.",
		Example: `  olivares auth totp-policy
  olivares auth totp-policy set --require-for-admins`,
	}
	cmd.AddCommand(&cobra.Command{
		Use:     "get",
		Short:   "Read the policy",
		Long:    "Read whether local administrators must enrol a TOTP factor before password login completes. Requires system:admin; returns no factor or recovery-code material.",
		Example: "  olivares auth totp-policy get",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return totpPolicyRender(cmd, client, http.MethodGet, nil)
		},
	})
	set := &cobra.Command{
		Use:     "set",
		Short:   "Set the policy (requires an AAL3 session)",
		Long:    "Set the deployment-wide TOTP requirement for local administrators. Requires system:admin and a session with AAL3 step-up. Set --require-for-admins=false to turn the requirement off; existing factors remain enrolled.",
		Example: "  olivares auth totp-policy set --require-for-admins",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return totpPolicyRender(cmd, client, http.MethodPut, map[string]any{"require_for_admins": requireForAdmins})
		},
	}
	set.Flags().BoolVar(&requireForAdmins, "require-for-admins", false,
		"administrators must hold a TOTP factor to finish a password login")
	cmd.AddCommand(set)
	return cmd
}

func totpPolicyRender(cmd *cobra.Command, client bootstrapClient, method string, body any) error {
	raw, err := client.expect(cmd, method, "/v1/auth/totp/policy", body, http.StatusOK)
	if err != nil {
		return err
	}
	var policy struct {
		RequireForAdmins bool `json:"require_for_admins"`
	}
	if err := decodeBootstrapJSON("auth totp-policy", raw, &policy); err != nil {
		return err
	}
	return renderOut(cmd, func(out io.Writer) error {
		state := "off"
		if policy.RequireForAdmins {
			state = "on"
		}
		_, werr := fmt.Fprintf(out, "require TOTP for administrators: %s\n", state)
		return werr
	}, json.RawMessage(raw))
}
