// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// `olivares governance rbac` — WHO CAN DO WHAT, and with what vocabulary.
//
// Eight read routes, none of which paged or filtered before this: every one returns its whole
// set. That is not an omission to fix here — it is what the handlers do (`loadScopedGrants` reads
// them all and sorts), so declaring --limit/--cursor would be a wrong answer rather than a
// missing one.
//
// ⭐ AND THE PAIRING WORTH KNOWING, because it is the question an operator actually has:
// `governance pdp active` reports `grants_expired` — this process is past its offline-staleness
// bound, so POSITIVE grants abstain while forbid rules stay enforced. `rbac grants` is what says
// WHICH grants those were. One says the permits stopped counting; the other says what stopped
// counting. Neither is readable without the other, so each one's help points at its half.
func governanceRBACCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rbac",
		Short: "Who can do what: the grant vocabulary, the custom roles and the scoped grants",
		Long: "rbac reads the authorization model: the vocabulary a grant may be built from\n" +
			"(`catalog`), what you yourself may delegate (`delegation-authority`), the custom roles\n" +
			"and permission groups, and the scoped grants in force.\n\n" +
			"Pairs with `governance pdp active`: that one says whether positive grants have expired,\n" +
			"this one says which grants they were.",
		Example: "  olivares governance rbac grants ls\n" +
			"  olivares governance rbac catalog -o json",
	}
	cmd.AddCommand(
		rbacCatalogCmd(flags), rbacDelegationCmd(flags),
		rbacRolesCmd(flags), rbacPermGroupsCmd(flags), rbacGrantsCmd(flags),
	)
	return cmd
}

type cliRBACCatalog struct {
	Kinds        []string `json:"kinds"`
	TreeKinds    []string `json:"tree_kinds"`
	Permissions  []string `json:"permissions"`
	Verbs        []string `json:"verbs"`
	BuiltinRoles []string `json:"builtin_roles"`
	ScopeTrees   []string `json:"scope_trees"`
	SubjectKinds []string `json:"subject_kinds"`
}

// rbacCatalogCmd — the vocabulary, and the ONE distinction the module warns about.
//
// `kinds` and `tree_kinds` are not the same list and the difference is not cosmetic: a module
// permission is grantable WITHOUT being a scope-tree node, so a picker that reads only `kinds`
// offers combinations validateScopeRefs then rejects. The text view therefore prints them as
// separate rows and says which one a scope picker must filter on — printing them merged would
// reproduce the exact bug the module documents.
func rbacCatalogCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "catalog",
		Short: "The vocabulary a grant can be built from",
		Example: "  olivares governance rbac catalog\n" +
			"  olivares governance rbac catalog -o json",
		Long: "Every value a scoped grant may use: grantable kinds, the subset that lives in the\n" +
			"scope tree, the module permissions, the verbs, the built-in roles and the subject kinds.\n\n" +
			"KINDS and TREE-KINDS are different on purpose. A module permission is grantable without\n" +
			"being a scope-tree node, so a scope picker must filter on TREE-KINDS: filtering on KINDS\n" +
			"offers combinations the engine will reject.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := observeCall{
				flags: flags, ns: governanceNS, method: http.MethodGet, path: "/rbac/catalog",
			}.do(cmd)
			if err != nil {
				return err
			}
			var c cliRBACCatalog
			if err := res.decode(&c); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
				for _, row := range []struct {
					label string
					vals  []string
				}{
					{"kinds (grantable)", c.Kinds},
					{"tree-kinds (scope picker)", c.TreeKinds},
					{"module permissions", c.Permissions},
					{"verbs", c.Verbs},
					{"built-in roles", c.BuiltinRoles},
					{"scope trees", c.ScopeTrees},
					{"subject kinds", c.SubjectKinds},
				} {
					if _, err := fmt.Fprintf(tw, "%s\t%s\n", row.label,
						observeCell(strings.Join(row.vals, " "))); err != nil {
						return err
					}
				}
				return tw.Flush()
			}, observeJSON(res.raw))
		},
	}
}

type cliDelegationDomain struct {
	ScopeTree   string   `json:"scope_tree"`
	ScopeRef    string   `json:"scope_ref,omitempty"`
	ScopeClass  string   `json:"scope_class,omitempty"`
	Permissions []string `json:"permissions"`
}

type cliDelegationAuthority struct {
	Superadmin bool                  `json:"superadmin"`
	Domains    []cliDelegationDomain `json:"domains"`
}

// rbacDelegationCmd — what YOU may hand to someone else, which is not what you may do.
func rbacDelegationCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "delegation-authority",
		Short:   "What the calling principal may delegate, and where",
		Example: "  olivares governance rbac delegation-authority",
		Long: "The domains in which you may create grants for others. This is a different question\n" +
			"from what you may DO: holding a permission does not imply being able to hand it on.\n\n" +
			"A superadmin answer means the domain list is not the bound.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := observeCall{
				flags: flags, ns: governanceNS, method: http.MethodGet,
				path: "/rbac/delegation-authority",
			}.do(cmd)
			if err != nil {
				return err
			}
			var d cliDelegationAuthority
			if err := res.decode(&d); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				if d.Superadmin {
					if _, err := fmt.Fprintln(out,
						"superadmin: yes — the domains below do not bound what you may delegate"); err != nil {
						return err
					}
				}
				if len(d.Domains) == 0 {
					_, err := fmt.Fprintln(out, "no delegation domain")
					return err
				}
				tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
				if _, err := fmt.Fprintln(tw, "SCOPE-TREE\tREF\tCLASS\tPERMISSIONS"); err != nil {
					return err
				}
				for _, x := range d.Domains {
					if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
						observeCell(x.ScopeTree), observeCell(x.ScopeRef),
						observeCell(x.ScopeClass), observeCell(strings.Join(x.Permissions, " "))); err != nil {
						return err
					}
				}
				return tw.Flush()
			}, observeJSON(res.raw))
		},
	}
}

type cliCustomRole struct {
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name,omitempty"`
	Description string   `json:"description,omitempty"`
	BaseRole    string   `json:"base_role,omitempty"`
	Permissions []string `json:"permissions"`
	Groups      []string `json:"groups,omitempty"`
	Excludes    []string `json:"excludes,omitempty"`
	CreatedBy   string   `json:"created_by,omitempty"`
}

type cliCustomRoleList struct {
	Items []cliCustomRole `json:"items"`
}

func rbacRolesCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "roles",
		Short: "Custom roles: what each one grants, and what it takes away",
		Long: "The tenant's own roles, as opposed to the built-in ones. A custom role is a permission\n" +
			"set plus an EXCLUDES list, and the exclusions are the half that surprises: a role can\n" +
			"subtract a permission its group grants, so reading the grants alone tells you less than\n" +
			"you think.",
		Example: "  olivares governance rbac roles ls\n" +
			"  olivares governance rbac roles get incident-responder",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "ls",
		Short: "List the custom roles",
		Example: "  olivares governance rbac roles ls\n" +
			"  olivares governance rbac roles ls -o json",
		Long: "The tenant's custom roles. EXCLUDES is the column to read twice: a role can subtract\n" +
			"from its base, so the permission list alone does not tell you what it grants.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := observeCall{
				flags: flags, ns: governanceNS, method: http.MethodGet, path: "/rbac/roles",
			}.do(cmd)
			if err != nil {
				return err
			}
			var list cliCustomRoleList
			if err := res.decode(&list); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				if len(list.Items) == 0 {
					_, err := fmt.Fprintln(out, "no custom role")
					return err
				}
				tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
				if _, err := fmt.Fprintln(tw, "NAME\tBASE\tPERMISSIONS\tGROUPS\tEXCLUDES\tCREATED-BY"); err != nil {
					return err
				}
				for _, r := range list.Items {
					if _, err := fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%s\n",
						observeCell(r.Name), observeCell(r.BaseRole),
						len(r.Permissions), len(r.Groups), len(r.Excludes),
						observeCell(r.CreatedBy)); err != nil {
						return err
					}
				}
				return tw.Flush()
			}, observeJSON(res.raw))
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "get <name>",
		Short: "One custom role, with its full permission set",
		Long: "One role in full. `excludes` is printed even when empty, so an absent exclusion list is\n" +
			"visible rather than inferred from a missing line: a role that subtracts nothing and a\n" +
			"role whose exclusions you failed to notice look identical otherwise.",
		Example: "  olivares governance rbac roles get incident-responder",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := observeCall{
				flags: flags, ns: governanceNS, method: http.MethodGet,
				path: "/rbac/roles/" + url.PathEscape(args[0]),
			}.do(cmd)
			if err != nil {
				return err
			}
			var r cliCustomRole
			if err := res.decode(&r); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				return rbacRenderRole(out, r)
			}, observeJSON(res.raw))
		},
	})
	return cmd
}

// rbacRenderRole prints EXCLUDES even when empty. A role that subtracts nothing and a role whose
// subtractions were not reported look identical if the line is omitted, and that is the field
// that decides what the role actually grants.
func rbacRenderRole(out io.Writer, r cliCustomRole) error {
	if _, err := fmt.Fprintf(out, "name:        %s\ndisplay:     %s\nbase role:   %s\n",
		observeCell(r.Name), observeCell(r.DisplayName), observeCell(r.BaseRole)); err != nil {
		return err
	}
	if r.Description != "" {
		if _, err := fmt.Fprintf(out, "description: %s\n", r.Description); err != nil {
			return err
		}
	}
	for _, row := range []struct {
		label string
		vals  []string
	}{{"permissions", r.Permissions}, {"groups", r.Groups}, {"excludes", r.Excludes}} {
		v := strings.Join(row.vals, " ")
		if v == "" {
			v = "(none)"
		}
		if _, err := fmt.Fprintf(out, "%-12s %s\n", row.label+":", v); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(out, "created by:  %s\n", observeCell(r.CreatedBy))
	return err
}

type cliPermGroup struct {
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name,omitempty"`
	Description string   `json:"description,omitempty"`
	Permissions []string `json:"permissions"`
	CreatedBy   string   `json:"created_by,omitempty"`
}

type cliPermGroupList struct {
	Items []cliPermGroup `json:"items"`
}

func rbacPermGroupsCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "permission-groups",
		Short: "Named bundles of permissions that roles reuse",
		Long: "A permission group is a named set of permissions that roles include by reference, so a\n" +
			"change to the group reaches every role that uses it. Read this before editing a role:\n" +
			"the permission you are looking for may come from a group rather than from the role.",
		Example: "  olivares governance rbac permission-groups ls\n" +
			"  olivares governance rbac permission-groups get on-call",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "ls",
		Short: "List the permission groups",
		Long: "Every permission group with the count of permissions it carries. Use `get` for the\n" +
			"members: this listing answers which groups exist, not what is in them.",
		Example: "  olivares governance rbac permission-groups ls",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := observeCall{
				flags: flags, ns: governanceNS, method: http.MethodGet, path: "/rbac/permission-groups",
			}.do(cmd)
			if err != nil {
				return err
			}
			var list cliPermGroupList
			if err := res.decode(&list); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				if len(list.Items) == 0 {
					_, err := fmt.Fprintln(out, "no permission group")
					return err
				}
				tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
				if _, err := fmt.Fprintln(tw, "NAME\tDISPLAY\tPERMISSIONS\tCREATED-BY"); err != nil {
					return err
				}
				for _, g := range list.Items {
					if _, err := fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n",
						observeCell(g.Name), observeCell(g.DisplayName),
						len(g.Permissions), observeCell(g.CreatedBy)); err != nil {
						return err
					}
				}
				return tw.Flush()
			}, observeJSON(res.raw))
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "get <name>",
		Short: "One permission group, with its members",
		Long: "One group with its full permission list and its description. The description is printed\n" +
			"as `-` when absent rather than omitted, so a group WITH a description and one without\n" +
			"are not identical on screen.",
		Example: "  olivares governance rbac permission-groups get on-call",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := observeCall{
				flags: flags, ns: governanceNS, method: http.MethodGet,
				path: "/rbac/permission-groups/" + url.PathEscape(args[0]),
			}.do(cmd)
			if err != nil {
				return err
			}
			var g cliPermGroup
			if err := res.decode(&g); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				v := strings.Join(g.Permissions, " ")
				if v == "" {
					v = "(none)"
				}
				// `description` is decoded and the engine populates it, so leaving it out of the
				// text view made a group WITH a description and one without look identical — the
				// same defect the empty `excludes` line above exists to avoid. Printed through
				// observeCell so an absent one shows as "-" instead of a blank the eye skips.
				_, err := fmt.Fprintf(out, "name:        %s\ndisplay:     %s\ndescription: %s\npermissions: %s\ncreated by:  %s\n",
					observeCell(g.Name), observeCell(g.DisplayName), observeCell(g.Description), v, observeCell(g.CreatedBy))
				return err
			}, observeJSON(res.raw))
		},
	})
	return cmd
}

type cliScopedGrant struct {
	ID          string `json:"id,omitempty"`
	SubjectKind string `json:"subject_kind"`
	SubjectRef  string `json:"subject_ref"`
	Role        string `json:"role"`
	RoleCustom  bool   `json:"role_custom,omitempty"`
	ScopeTree   string `json:"scope_tree"`
	ScopeRef    string `json:"scope_ref,omitempty"`
	ScopeClass  string `json:"scope_class,omitempty"`
	Note        string `json:"note,omitempty"`
	CreatedBy   string `json:"created_by,omitempty"`
}

type cliScopedGrantList struct {
	Items []cliScopedGrant `json:"items"`
}

func rbacGrantsCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "grants",
		Short: "The scoped grants in force: who holds what, where",
		Example: "  olivares governance rbac grants ls\n" +
			"  olivares governance rbac grants get g-4711",
		Long: "Read with `governance pdp active`: that verb says whether positive grants have\n" +
			"expired past the offline-staleness bound, and this one says which grants those are.",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "ls",
		Short: "List every scoped grant",
		Example: "  olivares governance rbac grants ls\n" +
			"  olivares governance rbac grants ls -o json",
		Long: "The whole set, sorted by the engine. The route takes no filter and no cursor, so the\n" +
			"columns are the filter: SUBJECT is who, ROLE is what, and SCOPE is where it applies.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := observeCall{
				flags: flags, ns: governanceNS, method: http.MethodGet, path: "/rbac/grants",
			}.do(cmd)
			if err != nil {
				return err
			}
			var list cliScopedGrantList
			if err := res.decode(&list); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				if len(list.Items) == 0 {
					_, err := fmt.Fprintln(out, "no scoped grant")
					return err
				}
				tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
				if _, err := fmt.Fprintln(tw, "ID\tSUBJECT\tKIND\tROLE\tCUSTOM\tSCOPE-TREE\tSCOPE-REF\tCLASS"); err != nil {
					return err
				}
				for _, g := range list.Items {
					custom := "no"
					if g.RoleCustom {
						custom = "yes"
					}
					if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
						observeCell(g.ID), observeCell(g.SubjectRef), observeCell(g.SubjectKind),
						observeCell(g.Role), custom, observeCell(g.ScopeTree),
						observeCell(g.ScopeRef), observeCell(g.ScopeClass)); err != nil {
						return err
					}
				}
				if err := tw.Flush(); err != nil {
					return err
				}
				_, err := fmt.Fprintf(out, "%d grant(s)\n", len(list.Items))
				return err
			}, observeJSON(res.raw))
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "get <id>",
		Short: "One scoped grant",
		Long: "One grant: its holder, the scope it applies in, what it grants and who issued it. Read\n" +
			"it beside `governance pdp active`, which says whether positive grants are in force at\n" +
			"all — a grant that exists and a grant that is being honoured are different facts.",
		Example: "  olivares governance rbac grants get g-4711",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := observeCall{
				flags: flags, ns: governanceNS, method: http.MethodGet,
				path: "/rbac/grants/" + url.PathEscape(args[0]),
			}.do(cmd)
			if err != nil {
				return err
			}
			var g cliScopedGrant
			if err := res.decode(&g); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				custom := "no"
				if g.RoleCustom {
					custom = "YES — see `governance rbac roles get`"
				}
				if _, err := fmt.Fprintf(out,
					"id:          %s\nsubject:     %s (%s)\nrole:        %s\ncustom role: %s\nscope:       %s %s %s\ncreated by:  %s\n",
					observeCell(g.ID), observeCell(g.SubjectRef), observeCell(g.SubjectKind),
					observeCell(g.Role), custom, observeCell(g.ScopeTree),
					observeCell(g.ScopeRef), observeCell(g.ScopeClass), observeCell(g.CreatedBy)); err != nil {
					return err
				}
				if g.Note != "" {
					_, err := fmt.Fprintf(out, "note:        %s\n", g.Note)
					return err
				}
				return nil
			}, observeJSON(res.raw))
		},
	})
	return cmd
}
