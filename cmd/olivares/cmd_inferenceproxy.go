// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// newInferenceProxyCmd wires the `inferenceproxy` module
// (modules/inferenceproxy/inferenceproxy.go:135-140) to the CLI: the gateway
// that sits in front of the models `olivares models` governs.
//
// It is six routes, and every one of them is a governance surface: the gate
// configuration that decides what the proxy enforces, the DLP rules it enforces
// on egress, and the approval of a device grant.
func newInferenceProxyCmd() *cobra.Command {
	flags := &authClientFlags{}
	cmd := &cobra.Command{
		Use:     "inference-proxy",
		Aliases: []string{"inferenceproxy"},
		Short:   "Govern the inference gateway: gates, DLP rules and device grants",
		Long: "Govern the inference proxy that fronts the model estate: which gates it enforces\n" +
			"(model access, budget, residency, context window, request and response DLP), the\n" +
			"per-class DLP rules applied to egress, and the approval of pending device grants.\n\n" +
			"Connection, credential and TLS values use the same resolution order and trust controls\n" +
			"as `auth`. These verbs configure the gateway; they do not proxy traffic.",
		Example: `  olivares inference-proxy config get
  olivares inference-proxy dlp ls -o json`,
		Args: cobra.NoArgs,
	}
	flags.addPersistent(cmd)
	c := modelstackClient{flags: flags, base: inferenceProxyAPIBase, family: "inference-proxy"}
	cmd.AddCommand(
		newInferenceProxyConfigCmd(c),
		newInferenceProxyDLPCmd(c),
		newInferenceProxyDeviceCmd(c),
	)
	return cmd
}

func newInferenceProxyConfigCmd(c modelstackClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Read and replace the gateway's gate configuration",
		Long: "Read and replace the singleton configuration that decides which gates the proxy\n" +
			"enforces, whether it fails open, its response-DLP mode and its per-request ceilings.\n\n" +
			"An unconfigured tenant reads the secure DEFAULT with configured=false: that is not the\n" +
			"same fact as an empty configuration, and the response says which one it is.",
		Example: `  olivares inference-proxy config get
  olivares inference-proxy config set --data @proxy-config.json`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(
		newModelstackGetCmd(c, modelstackGetSpec{
			Use:   "get",
			Short: "Show the gateway's effective gate configuration",
			Long: "Show the effective configuration: each gate's state, the fail-open setting, the\n" +
				"response-DLP mode, the request ceilings, and whether any of it has been configured.",
			Example: `  olivares inference-proxy config get -o json`,
			Target:  modelstackTarget{Collection: "/config"},
		}),
		newModelstackWriteCmd(c, modelstackWriteSpec{
			Use:   "set",
			Short: "Replace the gateway's gate configuration",
			Long: "Replace the singleton configuration with the supplied document. This is admin-tier\n" +
				"and self-audited: turning a gate off changes what the gateway will let through, and\n" +
				"the change is recorded against the caller.",
			Example: `  olivares inference-proxy config set --data @proxy-config.json
  cat proxy-config.json | olivares inference-proxy config set --data -`,
			Method: http.MethodPut,
			Target: modelstackTarget{Collection: "/config"},
			Body:   modelstackBodyRequired,
		}),
	)
	return cmd
}

func newInferenceProxyDLPCmd(c modelstackClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dlp",
		Short: "Govern the per-class DLP rules applied to inference egress",
		Long: "Govern the DLP rules the gateway applies to inference egress: one action per detected\n" +
			"class. An exact tenant rule overrides the seeded default for its class; removing the\n" +
			"override restores that secure default rather than removing the rule.",
		Example: `  olivares inference-proxy dlp ls
  olivares inference-proxy dlp set --data '{"class":"secret","action":"block"}'`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(
		newModelstackListCmd(c, modelstackListSpec{
			Use:       "ls",
			Aliases:   []string{"list"},
			Short:     "List the effective DLP rules",
			Long:      "List this tenant's DLP rules with the action each class carries and who set it.",
			Example:   `  olivares inference-proxy dlp ls -o json`,
			Target:    modelstackTarget{Collection: "/dlp/rules"},
			EmptyNote: "no tenant DLP overrides; the seeded secure defaults apply",
			Paginated: true,
			Columns: []modelstackColumn{
				{Header: "ID", Key: "id"},
				{Header: "CLASS", Key: "class"},
				{Header: "ACTION", Key: "action"},
				{Header: "BY", Key: "created_by"},
				{Header: "NOTE", Key: "note"},
			},
		}),
		newModelstackWriteCmd(c, modelstackWriteSpec{
			Use:   "set",
			Short: "Set the action for one DLP class",
			Long: "Upsert the rule for one detected class from a JSON document carrying its class and\n" +
				"action. Authorizing egress is privileged: the change is admin-tier and self-audited.",
			Example: `  olivares inference-proxy dlp set --data '{"class":"secret","action":"block"}'`,
			Method:  http.MethodPut,
			Target:  modelstackTarget{Collection: "/dlp/rules"},
			Body:    modelstackBodyRequired,
		}),
		newModelstackDeleteCmd(c, modelstackDeleteSpec{
			Use:   "rm <rule-id>",
			Short: "Remove a DLP override and restore its secure default",
			Long: "Remove one tenant override. The class does NOT become unfiltered: the seeded secure\n" +
				"default for that class applies again. The confirmation says so, because \"delete the\n" +
				"rule\" reads like \"stop filtering\" and it is not.",
			Example: `  olivares inference-proxy dlp rm 018f2a10-0000-7000-8000-00000000000e --yes`,
			Target:  modelstackTarget{Collection: "/dlp/rules", IDs: 1},
			Noun:    "DLP override",
			Blast:   "the seeded secure default for that class applies again; egress is not left unfiltered",
		}),
	)
	return cmd
}

// newInferenceProxyDeviceCmd is the one verb in this lot with TYPED flags
// instead of a --data document, and deliberately so: its request body is two
// fields (modules/inferenceproxy/devicegrant.go:220-225), which is the shape the
// house pattern already handles with named flags (`mcp pins approve`). A JSON
// document for two fields would be ceremony, and it would hide the one decision
// that matters — approve or deny — inside a string.
func newInferenceProxyDeviceCmd(c modelstackClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "device",
		Short: "Approve or deny a pending device grant",
		Long: "Resolve a pending device-authorization grant by its user code. Approving it mints the\n" +
			"credential the waiting device is polling for; denying it closes the grant.",
		Example: `  olivares inference-proxy device approve --user-code ABCD-EFGH
  olivares inference-proxy device approve --user-code ABCD-EFGH --deny`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(newInferenceProxyDeviceApproveCmd(c))
	return cmd
}

func newInferenceProxyDeviceApproveCmd(c modelstackClient) *cobra.Command {
	var (
		userCode string
		deny     bool
		yes      bool
	)
	cmd := &cobra.Command{
		Use:   "approve",
		Short: "Resolve a pending device grant by its user code",
		Long: "Resolve one pending device grant. Without --deny it APPROVES the grant, which mints a\n" +
			"credential for the waiting device; with --deny it refuses it.\n\n" +
			"Approving is destructive in the sense that matters — it hands out a credential — so it\n" +
			"asks for confirmation unless --yes is given. Denying does not: refusing a grant takes\n" +
			"nothing away that the device already had.\n\n" +
			"A code that has expired or was already used answers 410 and exits 5: that is state, not\n" +
			"a missing grant, and a script must not retry it as if the code were merely unknown.",
		Example: `  olivares inference-proxy device approve --user-code ABCD-EFGH --yes
  olivares inference-proxy device approve --user-code ABCD-EFGH --deny`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			code := strings.TrimSpace(userCode)
			if code == "" {
				return exitcode.New(exitcode.Usage,
					fmt.Errorf("--user-code is required: it is the code the waiting device displayed"))
			}
			if !deny {
				if err := confirmDestructive(cmd, yes,
					fmt.Sprintf("approve device grant %s (this mints a credential for the waiting device)",
						safeCLIValue(code, ""))); err != nil {
					return err
				}
			}
			body, err := json.Marshal(map[string]any{"user_code": code, "deny": deny})
			if err != nil {
				return exitcode.New(exitcode.Err, fmt.Errorf("encode device approval: %w", err))
			}
			res, err := c.do(cmd, http.MethodPost, "/device/approve", "", body)
			if err != nil {
				return err
			}
			return modelstackRenderWriteResult(cmd, res)
		},
	}
	cmd.Flags().StringVar(&userCode, "user-code", "", "the user code the waiting device displayed")
	cmd.Flags().BoolVar(&deny, "deny", false, "refuse the grant instead of approving it")
	addYesFlag(cmd, &yes)
	return cmd
}
