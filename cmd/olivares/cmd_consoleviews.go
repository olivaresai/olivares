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

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// The consoleviews namespace from a terminal: the saved filter/parameter sets a
// console feature keeps for its users.
//
// WHY A CLI FOR THIS AT ALL. These are the definitions behind a team's saved
// dashboards, and until now they existed only where a browser could reach them.
// Scripting them is what lets a saved view be provisioned with an environment
// rather than recreated by hand in every new tenant.
//
// TWO ENGINE RULES THIS FILE MIRRORS RATHER THAN REINVENTS.
//
//   - VISIBILITY IS NOT OWNERSHIP. A caller sees their own views plus the
//     tenant's shared ones, but may only UPDATE or DELETE their own. A view that
//     is visible-but-not-yours refuses with 403; one you cannot see at all is a
//     404, deliberately, so visibility does not leak existence
//     (consoleviews.go:277-279). The CLI passes both through unchanged.
//   - feature_id IS IMMUTABLE. A view's params only make sense against its
//     feature, so `update` does not offer to change it.
//
// PAGINATION: none. This namespace reads no cursor and no limit — it sorts the
// whole (capped) set newest-touched first and returns it. --cursor is absent
// because there is nothing to hang it on.

const consoleViewsNS = "consoleviews"

func newConsoleViewsCmd() *cobra.Command {
	var flags authClientFlags
	root := &cobra.Command{
		Use:     "consoleviews",
		Aliases: []string{"views"},
		Short:   "Manage saved console views (filter and parameter sets)",
		Long: "consoleviews manages the saved views a console feature keeps: a named set of\n" +
			"parameters for one feature, private to you or shared with the tenant.\n\n" +
			"You see your own views plus the tenant's shared ones, and you may change only\n" +
			"your own. A view you can see but do not own refuses to be modified; one you\n" +
			"cannot see is reported as absent, which is deliberate — visibility must not\n" +
			"leak existence.",
		Example: "  olivares consoleviews ls\n" +
			"  olivares consoleviews ls --feature-id findings -o json\n" +
			"  olivares consoleviews create --feature-id findings --name \"open criticals\" --params '{\"severity\":\"critical\"}'\n" +
			"  olivares consoleviews rm sv-1 --yes",
	}
	flags.addPersistent(root)
	root.AddCommand(
		newConsoleViewsListCmd(&flags),
		newConsoleViewsGetCmd(&flags),
		newConsoleViewsCreateCmd(&flags),
		newConsoleViewsUpdateCmd(&flags),
		newConsoleViewsRemoveCmd(&flags),
	)
	return root
}

type consoleView struct {
	ID          string          `json:"id"`
	FeatureID   string          `json:"feature_id"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Params      json.RawMessage `json:"params"`
	Owner       string          `json:"owner"`
	Shared      bool            `json:"shared"`
	Mine        bool            `json:"mine"`
	CreatedAt   string          `json:"created_at"`
	UpdatedAt   string          `json:"updated_at"`
}

type consoleViewList struct {
	Items []consoleView `json:"items"`
}

// consoleViewInput is the writable subset. It is the same shape for create and
// update; the engine ignores feature_id on update because a view cannot change
// feature.
type consoleViewInput struct {
	FeatureID   string          `json:"feature_id"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Params      json.RawMessage `json:"params"`
	Shared      bool            `json:"shared"`
}

// consoleViewParamsFlags collects the two mutually exclusive ways of supplying
// the params document.
type consoleViewParamsFlags struct {
	params     string
	paramsFile string
}

func addConsoleViewParamsFlags(cmd *cobra.Command, f *consoleViewParamsFlags) {
	cmd.Flags().StringVar(&f.params, "params", "", "the view's parameters as a JSON object")
	cmd.Flags().StringVar(&f.paramsFile, "params-file", "",
		"read the parameters JSON from a file; `-` reads stdin")
}

// resolve returns the params document, refusing locally what the engine would
// refuse remotely.
//
// The engine requires a JSON OBJECT, non-empty, at most 4096 bytes
// (consoleviews.go:213-219). Checking here means a malformed document costs
// exit 2 and no request; and the refusals below can only REJECT — a document
// that passes is forwarded byte for byte, never reformatted, so what is stored
// is what the operator wrote.
func (f consoleViewParamsFlags) resolve(cmd *cobra.Command) (json.RawMessage, error) {
	if f.params != "" && f.paramsFile != "" {
		return nil, exitcode.New(exitcode.Usage, fmt.Errorf(
			"--params and --params-file are mutually exclusive: pass exactly one"))
	}
	raw := []byte(f.params)
	if f.paramsFile != "" {
		var err error
		raw, err = readObserveDocument(cmd, f.paramsFile)
		if err != nil {
			return nil, err
		}
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, exitcode.New(exitcode.Usage, fmt.Errorf(
			"params is required: pass --params '<json object>' or --params-file <path>"))
	}
	if !strings.HasPrefix(trimmed, "{") || !json.Valid([]byte(trimmed)) {
		return nil, exitcode.New(exitcode.Usage, fmt.Errorf(
			"params must be a JSON object (it must start with '{' and parse)"))
	}
	if len(trimmed) > consoleViewMaxParamsBytes {
		return nil, exitcode.New(exitcode.Usage, fmt.Errorf(
			"params is %d bytes, over the engine's %d-byte limit", len(trimmed), consoleViewMaxParamsBytes))
	}
	return json.RawMessage(trimmed), nil
}

// consoleViewMaxParamsBytes mirrors maxParamsBytes (consoleviews.go:213). It is
// duplicated rather than imported because cmd/olivares does not link the module,
// and it is named here so a change on either side is one grep apart.
const consoleViewMaxParamsBytes = 4096

func renderConsoleView(cmd *cobra.Command, raw []byte, v consoleView, headline string) error {
	return renderOut(cmd, func(w io.Writer) error {
		if headline != "" {
			if _, err := fmt.Fprintln(w, headline); err != nil {
				return err
			}
		}
		tw := newTabWriter(w)
		fmt.Fprintf(tw, "id\t%s\n", observeCell(v.ID))
		fmt.Fprintf(tw, "feature\t%s\n", observeCell(v.FeatureID))
		fmt.Fprintf(tw, "name\t%s\n", observeCell(v.Name))
		fmt.Fprintf(tw, "description\t%s\n", observeCell(v.Description))
		fmt.Fprintf(tw, "owner\t%s%s\n", observeCell(v.Owner), observeBool(v.Mine, " (you)", ""))
		fmt.Fprintf(tw, "shared\t%s\n", observeBool(v.Shared, "yes — visible to the whole tenant", "no — private to the owner"))
		fmt.Fprintf(tw, "created\t%s\n", observeCell(v.CreatedAt))
		fmt.Fprintf(tw, "updated\t%s\n", observeCell(v.UpdatedAt))
		if err := tw.Flush(); err != nil {
			return err
		}
		_, err := fmt.Fprintf(w, "params: %s\n", observeCell(string(v.Params)))
		return err
	}, observeJSON(raw))
}

func newConsoleViewsListCmd(flags *authClientFlags) *cobra.Command {
	var featureID string
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List the views you can see",
		Long: "ls lists your own saved views plus the tenant's shared ones, newest-touched\n" +
			"first. The MINE column separates the two, because it decides which of them you\n" +
			"can change.\n\n" +
			"This route has no cursor: the set is capped per tenant and per user, and the\n" +
			"engine returns all of it.",
		Example: "  olivares consoleviews ls\n" +
			"  olivares consoleviews ls --feature-id findings\n" +
			"  olivares consoleviews ls -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if featureID != "" {
				q.Set("feature_id", featureID)
			}
			res, err := observeCall{
				flags: flags, ns: consoleViewsNS, method: http.MethodGet, path: "/views", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			var list consoleViewList
			if err := res.decode(&list); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				if len(list.Items) == 0 {
					_, err := fmt.Fprintln(w, "no saved views are visible to you")
					return err
				}
				tw := newTabWriter(w)
				if _, err := fmt.Fprintln(tw, "ID\tFEATURE\tNAME\tOWNER\tMINE\tSHARED\tUPDATED"); err != nil {
					return err
				}
				for _, v := range list.Items {
					if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
						observeCell(v.ID), observeCell(v.FeatureID), observeCell(v.Name),
						observeCell(v.Owner), observeBool(v.Mine, "yes", "no"),
						observeBool(v.Shared, "yes", "no"), observeCell(v.UpdatedAt)); err != nil {
						return err
					}
				}
				return tw.Flush()
			}, observeJSON(res.raw))
		},
	}
	cmd.Flags().StringVar(&featureID, "feature-id", "", "only views belonging to this console feature")
	return cmd
}

func newConsoleViewsGetCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "get <view-id>",
		Short: "Show one saved view in full",
		Long: "get shows one view with its parameter document.\n\n" +
			"A view you cannot see is reported as NOT FOUND rather than as forbidden. That\n" +
			"is the engine's decision and it is deliberate: a 403 would confirm the view\n" +
			"exists, which is exactly what a private view must not do.",
		Example: "  olivares consoleviews get sv-1\n  olivares consoleviews get sv-1 -o json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := observeCall{
				flags: flags, ns: consoleViewsNS, method: http.MethodGet,
				path: "/views" + observeIDPath(args[0]),
			}.do(cmd)
			if err != nil {
				return err
			}
			var v consoleView
			if err := res.decode(&v); err != nil {
				return err
			}
			return renderConsoleView(cmd, res.raw, v, "")
		},
	}
}

func newConsoleViewsCreateCmd(flags *authClientFlags) *cobra.Command {
	var featureID, name, description string
	var shared bool
	var params consoleViewParamsFlags
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Save a new view",
		Long: "create saves a new view owned by you. The (feature, owner, name) triple is the\n" +
			"natural key, so re-running create with the same name refuses with a conflict\n" +
			"rather than silently replacing your view — use `update` to change one.\n\n" +
			"--shared makes it visible to the whole tenant. Everyone can then SEE it; only\n" +
			"you can change it.",
		Example: "  olivares consoleviews create --feature-id findings --name \"open criticals\" --params '{\"severity\":\"critical\"}'\n" +
			"  olivares consoleviews create --feature-id finops --name \"team spend\" --params-file spend.json --shared",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(featureID) == "" || strings.TrimSpace(name) == "" {
				return exitcode.New(exitcode.Usage,
					fmt.Errorf("--feature-id and --name are both required"))
			}
			body, err := params.resolve(cmd)
			if err != nil {
				return err
			}
			res, err := observeCall{
				flags: flags, ns: consoleViewsNS, method: http.MethodPost, path: "/views",
				body: consoleViewInput{
					FeatureID: featureID, Name: name, Description: description,
					Params: body, Shared: shared,
				},
			}.do(cmd)
			if err != nil {
				return err
			}
			var v consoleView
			if err := res.decode(&v); err != nil {
				return err
			}
			// A create that returns no id created nothing this command can name.
			// Saying "created" over an empty id is the failure mode
			// confirmedCreate exists for one file over (cmd_compliance.go:376).
			if strings.TrimSpace(v.ID) == "" {
				return exitcode.New(exitcode.Server, fmt.Errorf(
					"the control plane answered HTTP %d but returned no view id, so nothing can be confirmed as created",
					res.status))
			}
			return renderConsoleView(cmd, res.raw, v, "created saved view "+observeCell(v.ID))
		},
	}
	cmd.Flags().StringVar(&featureID, "feature-id", "", "the console feature this view belongs to (lowercase slug, required)")
	cmd.Flags().StringVar(&name, "name", "", "the view's name, unique per feature and owner (required)")
	cmd.Flags().StringVar(&description, "description", "", "an optional description")
	cmd.Flags().BoolVar(&shared, "shared", false, "make the view visible to the whole tenant (only you can still change it)")
	addConsoleViewParamsFlags(cmd, &params)
	return cmd
}

func newConsoleViewsUpdateCmd(flags *authClientFlags) *cobra.Command {
	var name, description string
	var shared bool
	var params consoleViewParamsFlags
	cmd := &cobra.Command{
		Use:   "update <view-id>",
		Short: "Replace the writable fields of your own view",
		Long: "update replaces the name, description, parameters and shared flag of a view YOU\n" +
			"OWN. It is a replace, not a patch: every writable field is sent, so an omitted\n" +
			"--description clears the stored one.\n\n" +
			"feature_id is NOT changeable — a view's parameters only make sense against the\n" +
			"feature it was written for, so moving one would produce a view that renders\n" +
			"nothing.\n\n" +
			"A view you can see but do not own refuses with a permission error; one you\n" +
			"cannot see is reported as absent.",
		Example: "  olivares consoleviews update sv-1 --name \"open criticals\" --params '{\"severity\":\"critical\",\"status\":\"open\"}'\n" +
			"  olivares consoleviews update sv-1 --name keep --params-file params.json --shared",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(name) == "" {
				return exitcode.New(exitcode.Usage, fmt.Errorf(
					"--name is required: update REPLACES the writable fields, so the name must be stated even when it is unchanged"))
			}
			body, err := params.resolve(cmd)
			if err != nil {
				return err
			}
			res, err := observeCall{
				flags: flags, ns: consoleViewsNS, method: http.MethodPut,
				path: "/views" + observeIDPath(args[0]),
				body: consoleViewInput{
					Name: name, Description: description, Params: body, Shared: shared,
				},
			}.do(cmd)
			if err != nil {
				return err
			}
			var v consoleView
			if err := res.decode(&v); err != nil {
				return err
			}
			return renderConsoleView(cmd, res.raw, v, "updated saved view "+observeCell(v.ID))
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "the view's name (required: this is a replace, not a patch)")
	cmd.Flags().StringVar(&description, "description", "", "the description; omitting it CLEARS the stored one")
	cmd.Flags().BoolVar(&shared, "shared", false, "share with the tenant; omitting it makes the view private again")
	addConsoleViewParamsFlags(cmd, &params)
	return cmd
}

func newConsoleViewsRemoveCmd(flags *authClientFlags) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "rm <view-id>",
		Aliases: []string{"delete", "remove"},
		Short:   "Delete your own saved view",
		Long: "rm deletes a view you own. There is no undo and no revision history on this\n" +
			"namespace: the parameter document is gone with the row.\n\n" +
			"It requires --yes in any non-interactive session. A shared view may be relied\n" +
			"on by the whole tenant even though only you can remove it, which is exactly the\n" +
			"case where an unattended delete is worth stopping.",
		Example: "  olivares consoleviews rm sv-1 --yes\n  olivares consoleviews rm sv-1",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Confirmation FIRST: an unconfirmed delete must cost zero requests,
			// so the engine never sees an intent the operator did not state.
			if err := confirmDestructive(cmd, yes,
				fmt.Sprintf("delete saved view %s, including its parameter document", args[0])); err != nil {
				return err
			}
			res, err := observeCall{
				flags: flags, ns: consoleViewsNS, method: http.MethodDelete,
				path: "/views" + observeIDPath(args[0]),
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				_, werr := fmt.Fprintf(w, "deleted saved view %s\n", observeCell(args[0]))
				return werr
			}, observeJSON(res.raw))
		},
	}
	addYesFlag(cmd, &yes)
	return cmd
}
