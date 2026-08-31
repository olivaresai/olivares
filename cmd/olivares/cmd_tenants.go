// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

const orgsPath = "/v1/system/orgs"

// `olivares tenants` is the ORGANIZATION half of the browser-free first run. First
// boot creates ONE tenant (the one the first superadmin owns); every later tenant
// came from the console until this existed.
//
// It is a thin client over /v1/system/orgs (core/api/server.go:849), superadmin-only
// end to end (system:admin, handlers_core.go:567,740). Provisioning goes through the
// engine's single tenant path — the one first-boot setup itself uses — which
// allocates the tenant id, seeds the tenant's "Default" workspace and starts its
// audit chain in ONE transaction (handlers_core.go:592).
//
// THE RESIDENCY PIN IS HERE NOW, AND THE REASON IT WAS NOT IS WORTH KEEPING.
// PUT /v1/system/orgs/{tenant}/region is gated on an AAL3 step-up
// (handlers_core.go:625), and this file used to conclude that "no CLI credential
// can carry" one and leave the verb to the console. That premise is false for the
// credential the CLI is most likely to be holding: a step-up elevates the SESSION
// (core/auth/assurance.go:57) that `auth login --token` accepts, so a superadmin
// who ran the ceremony in the console can pin residency from here for the next 15
// minutes. The gate is untouched — a caller below AAL3 is refused by the engine,
// with a message that names the ceremony. Setting a tenant's SERVICE status is
// deliberately not step-up gated in the engine — the safe door must not be harder
// to reach than the destructive one (handlers_core.go:689) — so `set-status` never
// needed any of this.
func newTenantsCmd() *cobra.Command {
	flags := &authClientFlags{}
	root := &cobra.Command{
		Use:     "tenants",
		Aliases: []string{"orgs"},
		Short:   "Create, list, suspend and delete tenants (superadmin)",
		Long: "Manage the organizations this installation serves. Creating a tenant allocates its id,\n" +
			"seeds its default workspace and starts its audit chain in one transaction — the same path\n" +
			"first-boot setup takes. Suspending withdraws SERVICE without deleting anything; deleting\n" +
			"is an unrecoverable purge. Pinning a tenant's data region is gated on an AAL3 step-up:\n" +
			"no API token can carry one, an elevated user session can (see `set-region`).",
		Example: `  olivares tenants ls
  olivares tenants create --name "Acme GmbH"
  olivares tenants set-status t_01hq --status suspended
  olivares tenants set-region t_01hq --region eu --yes
  olivares tenants rm t_01hq --yes`,
	}
	flags.addPersistent(root)
	client := bootstrapClient{flags: flags, surface: "tenants"}
	root.AddCommand(
		tenantsListCmd(client),
		tenantsCreateCmd(client),
		tenantsSetStatusCmd(client),
		tenantsSetRegionCmd(client),
		tenantsRemoveCmd(client),
	)
	return root
}

// cliOrgRow mirrors core/api OrgDTO (dto.go:132).
type cliOrgRow struct {
	ID         string `json:"id"`
	TenantID   string `json:"tenant_id"`
	Name       string `json:"name"`
	Slug       string `json:"slug"`
	Status     string `json:"status"`
	DataRegion string `json:"data_region,omitempty"`
	CreatedAt  string `json:"created_at"`
}

type cliOrgList struct {
	Items []cliOrgRow `json:"items"`
}

func tenantsListCmd(client bootstrapClient) *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List the tenants this installation serves",
		Long: "List every customer tenant with its id, handle, service status and residency pin. The\n" +
			"reserved system tenant is not a customer organization and the engine never lists it.",
		Example: `  olivares tenants ls
  olivares tenants ls -o json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			raw, err := client.expect(cmd, http.MethodGet, orgsPath, nil, http.StatusOK)
			if err != nil {
				return err
			}
			var list cliOrgList
			if err := decodeBootstrapJSON("tenants", raw, &list); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				if len(list.Items) == 0 {
					_, err := fmt.Fprintln(out, "no tenants exist yet")
					return err
				}
				tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
				fmt.Fprintln(tw, "TENANT ID\tNAME\tSLUG\tSTATUS\tREGION\tCREATED")
				for _, o := range list.Items {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
						safeCLIValue(o.TenantID, ""), safeCLIValue(o.Name, ""),
						safeCLIValue(o.Slug, ""), safeCLIValue(o.Status, ""),
						orDash(safeCLIValue(o.DataRegion, "")), safeCLIValue(o.CreatedAt, ""))
				}
				return tw.Flush()
			}, json.RawMessage(raw))
		},
	}
}

func tenantsCreateCmd(client bootstrapClient) *cobra.Command {
	var name, slug, region string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a tenant",
		Long: "Create a tenant. --slug is its short, URL-safe handle and must be unique; leave it out\n" +
			"and the engine derives one from the name. --region pins the tenant's control-plane data\n" +
			"to a residency region and is validated against this instance's registry — an unknown\n" +
			"region is refused and the tenant is never created half-pinned.",
		Example: `  olivares tenants create --name "Acme GmbH"
  olivares tenants create --name "Acme GmbH" --slug acme --region eu`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			name = strings.TrimSpace(name)
			if name == "" {
				return exitcode.New(exitcode.Usage, fmt.Errorf("--name is required"))
			}
			body := map[string]any{"name": name, "slug": strings.TrimSpace(slug), "data_region": strings.TrimSpace(region)}
			raw, err := client.expect(cmd, http.MethodPost, orgsPath, body, http.StatusCreated)
			if err != nil {
				return err
			}
			var org cliOrgRow
			if err := decodeBootstrapJSON("tenants", raw, &org); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				_, err := fmt.Fprintf(out, "created tenant %s (%q, slug %q, status %s)\n",
					safeCLIValue(org.TenantID, ""), safeCLIValue(org.Name, ""),
					safeCLIValue(org.Slug, ""), safeCLIValue(org.Status, ""))
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(out,
					"→ give somebody a role in it: olivares members grant --user <id> --tenant "+
						safeCLIValue(org.TenantID, "")+" --role admin")
				return err
			}, json.RawMessage(raw))
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "human name of the organization (required)")
	cmd.Flags().StringVar(&slug, "slug", "", "unique URL-safe handle (default: derived from the name)")
	cmd.Flags().StringVar(&region, "region", "", "residency region to pin the tenant to (default: unpinned)")
	return cmd
}

func tenantsSetStatusCmd(client bootstrapClient) *cobra.Command {
	var status string
	var yes bool
	cmd := &cobra.Command{
		Use:   "set-status <tenant-id>",
		Short: "Withdraw or restore a tenant's service without deleting anything",
		Long: "Set a tenant's service status. \"suspended\" withdraws service — mutations and interactive\n" +
			"use are refused — while authentication, the operator's system routes, EXPORT of the\n" +
			"tenant's own data and custodial checkpointing deliberately continue, so withdrawing\n" +
			"service never holds a customer's data hostage. \"active\" restores it losslessly. This is\n" +
			"the reversible door; the destructive one is `tenants rm`.",
		Example: `  olivares tenants set-status t_01hq --status suspended --yes
  olivares tenants set-status t_01hq --status active`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			status = strings.TrimSpace(status)
			if status != "active" && status != "suspended" {
				return exitcode.New(exitcode.Usage, fmt.Errorf(
					"--status must be active or suspended (got %q)", status))
			}
			// Only the withdrawal asks. Restoring service is the safe direction and
			// gating it behind a prompt would train operators to pass --yes always,
			// which is how the prompt on the dangerous direction stops being read.
			if status == "suspended" {
				if err := confirmDestructive(cmd, yes, fmt.Sprintf(
					"withdraw service from tenant %q (its users can no longer work; nothing is deleted)",
					safeCLIValue(args[0], ""))); err != nil {
					return err
				}
			}
			path := orgsPath + "/" + bootstrapPathID(args[0]) + "/status"
			raw, err := client.expect(cmd, http.MethodPut, path, map[string]any{"status": status}, http.StatusOK)
			if err != nil {
				return err
			}
			var org cliOrgRow
			if err := decodeBootstrapJSON("tenants", raw, &org); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				_, err := fmt.Fprintf(out, "tenant %s is now %s\n",
					safeCLIValue(org.TenantID, ""), safeCLIValue(org.Status, ""))
				return err
			}, json.RawMessage(raw))
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "active or suspended (required)")
	_ = cmd.RegisterFlagCompletionFunc("status", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"active", "suspended"}, cobra.ShellCompDirectiveNoFileComp
	})
	addYesFlag(cmd, &yes)
	return cmd
}

// tenantsSetRegionCmd pins or clears a tenant's data residency. The engine
// normalizes and validates the region against this instance's registry, refuses an
// unknown one, and gates the whole route on system:admin PLUS an AAL3 step-up
// (handlers_core.go:625) — none of which this command second-guesses.
//
// Clearing is a separate flag rather than `--region ""`, because "unpin this
// tenant's residency" is a decision with legal weight and an empty string is what
// a shell produces from an unset variable.
func tenantsSetRegionCmd(client bootstrapClient) *cobra.Command {
	var region string
	var clearPin, yes bool
	cmd := &cobra.Command{
		Use:   "set-region <tenant-id>",
		Short: "Pin or clear a tenant's data-residency region (requires an AAL3 session)",
		Long: "Pin the tenant's control-plane data to a residency region, or clear the pin with\n" +
			"--clear. The region is validated against this instance's residency registry: an unknown\n" +
			"one is refused. Moving an already-pinned tenant to another region is a DATA MIGRATION\n" +
			"and is out of this endpoint's scope — the engine decides what it will accept.\n\n" +
			"The route is gated on a verified hardware step-up (AAL3). An API token can never carry\n" +
			"one; a user session elevated by the WebAuthn/PIV ceremony can, for 15 minutes — run the\n" +
			"ceremony in the console, then bring that session with `auth login --token-file`.",
		Example: `  olivares tenants set-region t_01hq --region eu --yes
  olivares tenants set-region t_01hq --clear --yes`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			region = strings.TrimSpace(region)
			if clearPin == (region != "") {
				return exitcode.New(exitcode.Usage, fmt.Errorf(
					"pass exactly one of --region <region> or --clear"))
			}
			what := fmt.Sprintf("pin tenant %q to residency region %q", safeCLIValue(args[0], ""), safeCLIValue(region, ""))
			if clearPin {
				what = fmt.Sprintf("CLEAR the residency pin of tenant %q (its data is no longer promised to a region)",
					safeCLIValue(args[0], ""))
			}
			if err := confirmDestructive(cmd, yes, what); err != nil {
				return err
			}
			path := orgsPath + "/" + bootstrapPathID(args[0]) + "/region"
			raw, err := client.expect(cmd, http.MethodPut, path,
				map[string]any{"data_region": region}, http.StatusOK)
			if err != nil {
				return err
			}
			var org cliOrgRow
			if err := decodeBootstrapJSON("tenants", raw, &org); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				if org.DataRegion == "" {
					_, werr := fmt.Fprintf(out, "tenant %s is no longer pinned to a region\n",
						safeCLIValue(org.TenantID, ""))
					return werr
				}
				_, werr := fmt.Fprintf(out, "tenant %s is pinned to region %s\n",
					safeCLIValue(org.TenantID, ""), safeCLIValue(org.DataRegion, ""))
				return werr
			}, json.RawMessage(raw))
		},
	}
	cmd.Flags().StringVar(&region, "region", "", "residency region to pin the tenant to")
	cmd.Flags().BoolVar(&clearPin, "clear", false, "remove the tenant's residency pin instead of setting one")
	addYesFlag(cmd, &yes)
	return cmd
}

func tenantsRemoveCmd(client bootstrapClient) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "rm <tenant-id>",
		Aliases: []string{"remove", "delete"},
		Short:   "Delete a tenant and everything in it — unrecoverable",
		Long: "Hard-delete a tenant: its data, its audit chain and its memberships. There is no undo\n" +
			"and no grace period at this layer. If the intent is to stop serving a customer while\n" +
			"keeping the ability to explain, export or restore, use `tenants set-status --status\n" +
			"suspended` instead — that is the door this one is not.",
		Example: "  olivares tenants rm t_01hq --yes",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := confirmDestructive(cmd, yes, fmt.Sprintf(
				"PERMANENTLY delete tenant %q and all of its data, including its audit chain",
				safeCLIValue(args[0], ""))); err != nil {
				return err
			}
			if _, err := client.expect(cmd, http.MethodDelete,
				orgsPath+"/"+bootstrapPathID(args[0]), nil, http.StatusNoContent); err != nil {
				return err
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "deleted tenant %s\n", safeCLIValue(args[0], ""))
			return err
		},
	}
	addYesFlag(cmd, &yes)
	return cmd
}
