// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
	"github.com/olivaresai/olivares/core/api"
)

// `olivares notify destinations ls|add|test|rm`: the destinations the console's
// Notifications page edits (/v1/console/notify-destinations). A secret setting is read
// from a file or stdin (--secret-file), never from the command line.

const notifyDestinationsPath = "/v1/console/notify-destinations"

func newNotifyDestinationVerbs(flags *authClientFlags) []*cobra.Command {
	read := bootstrapClient{flags: flags, surface: "notify destinations"}
	write := bootstrapClient{flags: flags, surface: "notify destinations", carriesSecret: true}
	return []*cobra.Command{
		newNotifyDestinationListCmd(read),
		newNotifyDestinationAddCmd(write),
		newNotifyDestinationTestCmd(write),
		newNotifyDestinationRemoveCmd(read),
	}
}

func newNotifyDestinationListCmd(client bootstrapClient) *cobra.Command {
	return &cobra.Command{
		Annotations: map[string]string{"changes_data": "read"},
		Use:         "ls",
		Aliases:     []string{"list"},
		Short:       "List every destination, its kind, who owns it and whether it is open",
		Long: "ls lists every notification destination: the ones added here and the ones in the\n" +
			"operator file (OLIVARES_NOTIFY_CONFIG), which are read-only. -o json also lists the\n" +
			"kinds that can be added and their settings. Requires system administration.",
		Example: "  olivares notify destinations ls\n  olivares notify destinations ls -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			raw, err := client.expect(cmd, http.MethodGet, notifyDestinationsPath, nil, http.StatusOK)
			if err != nil {
				return err
			}
			var list api.NotifyDestinationList
			if err := decodeBootstrapJSON("notify destinations", raw, &list); err != nil {
				return err
			}
			table := termrender.Table{
				Header: []string{"name", "kind", "source", "state", "host"},
				Empty:  "No destination yet. Add one: olivares notify destinations add <name> --kind webhook --set url=https://...",
			}
			for _, d := range list.Destinations {
				state, role := "open", termrender.RoleOK
				if !d.Live {
					state, role = "not open", termrender.RoleWarn
				}
				table.Rows = append(table.Rows, []string{termSafe(d.Name), termSafe(d.Kind), d.Source, state, termSafe(d.WebhookHost)})
				table.Roles = append(table.Roles, []termrender.Role{termrender.RoleNone, termrender.RoleNone, termrender.RoleNone, role})
			}
			return renderOut(cmd, func(w io.Writer) error {
				renderTo(w).Table(table)
				return nil
			}, list)
		},
	}
}

// notifyDestinationSettings reads --set setting=value and --secret-file setting=path
// (- for stdin) into the settings of one request. A secret setting of the kind (of any
// kind when it is blank) is refused in --set: argv is readable in /proc and the history.
func notifyDestinationSettings(cmd *cobra.Command, kind string, sets, secretFiles []string) (map[string]string, error) {
	out := map[string]string{}
	for _, kv := range sets {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, exitcode.New(exitcode.Usage, fmt.Errorf("--set takes setting=value, got %q", kv))
		}
		if notifySecretSetting(kind, k) {
			return nil, exitcode.New(exitcode.Usage, fmt.Errorf("%s is secret: give it with --secret-file %s=path, or %s=- for stdin, never on the command line", k, k, k))
		}
		out[k] = v
	}
	stdinUsed := false
	for _, kp := range secretFiles {
		k, path, ok := strings.Cut(kp, "=")
		if !ok || k == "" || path == "" {
			return nil, exitcode.New(exitcode.Usage, fmt.Errorf("--secret-file takes setting=path (- for stdin)"))
		}
		var b []byte
		var err error
		if path == "-" {
			if stdinUsed {
				return nil, exitcode.New(exitcode.Usage, fmt.Errorf("only one --secret-file can read stdin"))
			}
			stdinUsed = true
			b, err = io.ReadAll(cmd.InOrStdin())
		} else {
			b, err = os.ReadFile(path)
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", k, err)
		}
		out[k] = strings.TrimRight(string(b), "\r\n")
	}
	return out, nil
}

// notifySecretSetting reports whether key is a secret setting of kind, or of any kind
// when kind is blank.
func notifySecretSetting(kind, key string) bool {
	for name, k := range notifyConsoleKinds {
		if kind != "" && name != kind {
			continue
		}
		for _, f := range notifyKindFields(name, k) {
			if f.Key == key && f.Secret {
				return true
			}
		}
	}
	return false
}

// addNotifySettingFlags declares the --set and --secret-file flags of add and test.
func addNotifySettingFlags(cmd *cobra.Command, sets, secretFiles *[]string) {
	cmd.Flags().StringArrayVar(sets, "set", nil, "a setting that is not secret, setting=value (repeatable)")
	cmd.Flags().StringArrayVar(secretFiles, "secret-file", nil, "a secret setting read from a file, setting=path or setting=- for stdin (repeatable)")
}

func newNotifyDestinationAddCmd(client bootstrapClient) *cobra.Command {
	var kind string
	var sets, secretFiles []string
	cmd := &cobra.Command{
		Annotations: map[string]string{"changes_data": "write"},
		Use:         "add <name> --kind <kind> [--set setting=value]... [--secret-file setting=path]...",
		Short:       "Add or update a destination; it opens at once, without a restart",
		Long: "add creates a destination, or updates one added before, and opens it in the running\n" +
			"engine. Kinds: slack, teams (setting webhook_url), webhook (url, signing_secret: each\n" +
			"delivery is HMAC-SHA256 signed when it is set), email (smtp_host, smtp_port,\n" +
			"smtp_username, smtp_password, from, from_name, to) and twilio (account_sid,\n" +
			"auth_token, from or messaging_service_sid, to).\n\n" +
			"Give a secret setting (webhook_url, signing_secret, smtp_username, smtp_password,\n" +
			"auth_token) with --secret-file setting=path, or setting=- for stdin: it is sealed in\n" +
			"the secret store and never shown again. On an update a secret you leave out keeps\n" +
			"its value; when url or smtp_host changes, give every stored secret again. Any\n" +
			"other setting you leave out is cleared.\n\n" +
			"A webhook or SMTP server on a private or reserved address is refused: provision an\n" +
			"internal destination in the operator file (OLIVARES_NOTIFY_CONFIG). Requires system\n" +
			"administration with the deployment's step-up.",
		Example: "  olivares notify destinations add ops-hook --kind webhook --set url=https://hooks.example.com/olivares --secret-file signing_secret=/run/secrets/hook\n" +
			"  printf %s \"$SMTP_PASSWORD\" | olivares notify destinations add ops-mail --kind email --set smtp_host=smtp.example.com --set from=olivares@example.com --set to=oncall@example.com --secret-file smtp_username=/run/secrets/smtp-user --secret-file smtp_password=-\n" +
			"  olivares notify destinations add ops-slack --kind slack --secret-file webhook_url=/run/secrets/slack-hook",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			settings, err := notifyDestinationSettings(cmd, kind, sets, secretFiles)
			if err != nil {
				return err
			}
			in := api.NotifyDestinationInput{Name: args[0], Kind: kind, Settings: settings}
			raw, err := client.expect(cmd, http.MethodPut, notifyDestinationsPath, in, http.StatusOK)
			if err != nil {
				return err
			}
			var d api.NotifyDestination
			if err := decodeBootstrapJSON("notify destinations", raw, &d); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				if !d.Live {
					_, err := fmt.Fprintf(w, "saved %s, but it did not open: check its settings with `olivares notify destinations test %s`\n", termSafe(d.Name), termSafe(d.Name))
					return err
				}
				_, err := fmt.Fprintf(w, "added %s (%s); route to it with `olivares notify routes create --destination %s`\n", termSafe(d.Name), termSafe(d.Kind), termSafe(d.Name))
				return err
			}, d)
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "slack, teams, webhook, email or twilio")
	addNotifySettingFlags(cmd, &sets, &secretFiles)
	_ = cmd.MarkFlagRequired("kind")
	return cmd
}

func newNotifyDestinationTestCmd(client bootstrapClient) *cobra.Command {
	var kind string
	var sets, secretFiles []string
	cmd := &cobra.Command{
		Annotations: map[string]string{"changes_data": "read"},
		Use:         "test <name> [--kind <kind>] [--set setting=value]... [--secret-file setting=path]...",
		Short:       "Send one test message through a destination, saving nothing",
		Long: "test sends one test message through the stored destination of that name. Settings\n" +
			"given here are tried over the stored ones without saving them; with --kind and every\n" +
			"required setting it tests a destination before you add it. Requires system\n" +
			"administration with the deployment's step-up.",
		Example: "  olivares notify destinations test ops-hook\n" +
			"  olivares notify destinations test candidate --kind webhook --set url=https://hooks.example.com/olivares",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			settings, err := notifyDestinationSettings(cmd, kind, sets, secretFiles)
			if err != nil {
				return err
			}
			in := api.NotifyDestinationInput{Name: args[0], Kind: kind, Settings: settings}
			raw, err := client.expect(cmd, http.MethodPost, notifyDestinationsPath+"/test", in, http.StatusOK)
			if err != nil {
				return err
			}
			res := map[string]any{}
			if err := decodeBootstrapJSON("notify destinations", raw, &res); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				_, err := fmt.Fprintf(w, "%s accepted the test message\n", termSafe(args[0]))
				return err
			}, res)
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "the kind, when the destination is not stored yet")
	addNotifySettingFlags(cmd, &sets, &secretFiles)
	return cmd
}

func newNotifyDestinationRemoveCmd(client bootstrapClient) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Annotations: map[string]string{"changes_data": "delete"},
		Use:         "rm <name>",
		Aliases:     []string{"remove", "delete"},
		Short:       "Remove an added destination; delivery to it stops at once",
		Long: "rm closes the destination, deletes its sealed settings and removes it. A route that\n" +
			"names it then records unknown_destination. A destination in the operator file is\n" +
			"removed there. Requires system administration with the deployment's step-up.",
		Example: "  olivares notify destinations rm ops-hook --yes",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := confirmDestructive(cmd, yes, fmt.Sprintf("remove notification destination %q (routes to it stop delivering)", args[0])); err != nil {
				return err
			}
			raw, err := client.expect(cmd, http.MethodDelete, notifyDestinationsPath, map[string]string{"name": args[0]}, http.StatusOK)
			if err != nil {
				return err
			}
			res := map[string]any{}
			if err := decodeBootstrapJSON("notify destinations", raw, &res); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				_, err := fmt.Fprintf(w, "removed %s\n", termSafe(args[0]))
				return err
			}, res)
		},
	}
	addYesFlag(cmd, &yes)
	return cmd
}
