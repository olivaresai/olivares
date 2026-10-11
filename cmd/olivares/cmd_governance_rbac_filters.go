// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
)

// `olivares governance rbac filters` shows and sets inheritance filters
// (modules/governance/inheritance_filter.go). The engine validates the node, the class and
// that the caller is an admin of the node; this verb sends what it was given and prints the
// engine's answer or refusal.

const inheritanceFiltersPath = "/rbac/inheritance-filters"

type cliInheritanceFilter struct {
	ID         string `json:"id,omitempty"`
	ScopeTree  string `json:"scope_tree"`
	ScopeRef   string `json:"scope_ref,omitempty"`
	ScopeClass string `json:"scope_class"`
	CreatedBy  string `json:"created_by,omitempty"`
}

// Items is a pointer so a body without the list is told apart from an empty one: an empty
// list means every node inherits everything, and an answer that is not a list must not say so.
type cliInheritanceFilterList struct {
	Items *[]cliInheritanceFilter `json:"items"`
}

// decodeInheritanceFilter reads one stored filter. The engine always sends its id; a body
// without one is not a filter, and printing it would read as a record that was stored.
func decodeInheritanceFilter(res observeResult) (cliInheritanceFilter, error) {
	var f cliInheritanceFilter
	if err := res.decode(&f); err != nil {
		return f, err
	}
	if f.ID == "" {
		return f, exitcode.New(exitcode.Server, errors.New("the engine answered without an inheritance filter"))
	}
	return f, nil
}

func rbacFiltersCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "filters",
		Short: "Inheritance filters: where rights from above a node stop applying",
		Long: "An inheritance filter sits on a workspace, agent group or folder and names one\n" +
			"resource class. Rights that reach the node from above it (a tenant-wide role, a grant\n" +
			"anchored higher) no longer apply to that class there; grants at or below the node\n" +
			"still do. It is not a forbid, which stays absolute.\n\n" +
			"A filter applies to every principal, tenant admins included. Only an admin of the\n" +
			"node may set or remove one. `governance rbac rights` shows its effect on one subject.",
		Example: "  olivares governance rbac filters ls\n" +
			"  olivares governance rbac filters set workspace payments --class session\n" +
			"  olivares governance rbac filters rm <filter-id> --yes",
	}
	cmd.AddCommand(rbacFiltersLsCmd(flags), rbacFiltersGetCmd(flags), rbacFiltersSetCmd(flags), rbacFiltersRmCmd(flags))
	return cmd
}

func rbacFiltersLsCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List every inheritance filter",
		Long: "ls lists the inheritance filters of the tenant: the id get and rm take, the node, its\n" +
			"ref, the class each one stops and who set it. No filter means every node inherits\n" +
			"every right from above it.",
		Example: "  olivares governance rbac filters ls -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := observeCall{
				flags: flags, ns: governanceNS, method: http.MethodGet, path: inheritanceFiltersPath,
			}.do(cmd)
			if err != nil {
				return err
			}
			var list cliInheritanceFilterList
			if err := res.decode(&list); err != nil {
				return err
			}
			if list.Items == nil {
				return exitcode.New(exitcode.Server, errors.New("the engine answered without a filter list"))
			}
			items := *list.Items
			return renderOut(cmd, func(out io.Writer) error {
				t := termrender.Table{
					Header: []string{"id", "node", "ref", "class", "created-by"},
					Empty:  "no inheritance filter",
				}
				for _, f := range items {
					t.Rows = append(t.Rows, []string{
						observeCell(f.ID), observeCell(f.ScopeTree), observeCell(f.ScopeRef),
						observeCell(f.ScopeClass), observeCell(f.CreatedBy),
					})
				}
				renderTo(out).Table(t)
				if len(items) == 0 {
					return nil
				}
				_, err := fmt.Fprintf(out, "%d filter(s)\n", len(items))
				return err
			}, observeJSON(res.raw))
		},
	}
}

func rbacFiltersGetCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "get <filter-id>",
		Short: "One inheritance filter",
		Long: "get shows one inheritance filter by the id `filters ls` prints. An unknown id\n" +
			"exits 4 (not found).",
		Example: "  olivares governance rbac filters get <filter-id>",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := observeCall{
				flags: flags, ns: governanceNS, method: http.MethodGet,
				path: inheritanceFiltersPath + observeIDPath(args[0]),
			}.do(cmd)
			if err != nil {
				return err
			}
			f, err := decodeInheritanceFilter(res)
			if err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				renderTo(out).Fields(inheritanceFilterFields(f))
				return nil
			}, observeJSON(res.raw))
		},
	}
}

func rbacFiltersSetCmd(flags *authClientFlags) *cobra.Command {
	var class string
	cmd := &cobra.Command{
		Use:   "set <workspace|agent_group|folder> <ref> --class <kind>",
		Short: "Stop rights from above a node for one resource class",
		Long: "set stores a filter on a node: a workspace slug, an agent-group slug or a folder\n" +
			"resource id. --class is a resource kind of the scope tree (`governance rbac catalog`\n" +
			"lists them as tree-kinds); an agent group takes only `agent`.\n\n" +
			"A node has at most one filter per class: a second set for the same node and class\n" +
			"exits 5 (conflict).",
		Example: "  olivares governance rbac filters set workspace payments --class session\n" +
			"  olivares governance rbac filters set agent_group finance-ops --class agent",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := observeCall{
				flags: flags, ns: governanceNS, method: http.MethodPost, path: inheritanceFiltersPath,
				body: cliInheritanceFilter{ScopeTree: args[0], ScopeRef: args[1], ScopeClass: class},
			}.do(cmd)
			if err != nil {
				return err
			}
			f, err := decodeInheritanceFilter(res)
			if err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				renderTo(out).Fields(inheritanceFilterFields(f))
				return nil
			}, observeJSON(res.raw))
		},
	}
	cmd.Flags().StringVar(&class, "class", "", "the resource class the filter stops (a scope-tree kind)")
	_ = cmd.MarkFlagRequired("class")
	return cmd
}

func rbacFiltersRmCmd(flags *authClientFlags) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "rm <filter-id>",
		Aliases: []string{"remove", "delete"},
		Short:   "Remove an inheritance filter",
		Long: "rm removes a filter, so rights from above its node apply there again. That widens\n" +
			"access, so it asks for confirmation and needs --yes in a non-interactive session.",
		Example: "  olivares governance rbac filters rm <filter-id> --yes",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := confirmDestructive(cmd, yes, fmt.Sprintf(
				"remove inheritance filter %s (rights from above its node apply there again)",
				safeCLIValue(args[0], ""))); err != nil {
				return err
			}
			res, err := observeCall{
				flags: flags, ns: governanceNS, method: http.MethodDelete,
				path: inheritanceFiltersPath + observeIDPath(args[0]),
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				_, werr := fmt.Fprintf(w, "removed inheritance filter %s\n", observeCell(args[0]))
				return werr
			}, observeJSON(res.raw))
		},
	}
	addYesFlag(cmd, &yes)
	return cmd
}

func inheritanceFilterFields(f cliInheritanceFilter) []termrender.Field {
	return []termrender.Field{
		{Key: "id", Value: observeCell(f.ID)},
		{Key: "node", Value: observeCell(f.ScopeTree) + " " + observeCell(f.ScopeRef)},
		{Key: "class", Value: observeCell(f.ScopeClass)},
		{Key: "created by", Value: observeCell(f.CreatedBy)},
	}
}
