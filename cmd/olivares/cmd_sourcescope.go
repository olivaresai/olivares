// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// ---------------------------------------------------------------------------
// Request bodies, mirroring modules/sourcescope exactly (its decoder runs with
// DisallowUnknownFields, so an invented field is a 400, not a no-op).
// ---------------------------------------------------------------------------

type sourcescopeBindingBody struct {
	SourceType  string `json:"source_type"`
	SourceRef   string `json:"source_ref"`
	ScopeTree   string `json:"scope_tree"`
	ScopeRef    string `json:"scope_ref,omitempty"`
	Effect      string `json:"effect,omitempty"`
	FolderPath  string `json:"folder_path,omitempty"`
	CredName    string `json:"cred_name,omitempty"`
	CredRefKind string `json:"cred_ref_kind,omitempty"`
	CredRef     string `json:"cred_ref,omitempty"`
	CredHint    string `json:"cred_hint,omitempty"`
	Enabled     bool   `json:"enabled"`
	Note        string `json:"note,omitempty"`
}

type sourcescopeDisableScopingBody struct {
	SourceType string `json:"source_type"`
	SourceRef  string `json:"source_ref"`
}

type sourcescopeGuardPostureBody struct {
	SourceType string `json:"source_type"`
	SourceRef  string `json:"source_ref"`
	Profile    string `json:"profile"`
	Reason     string `json:"reason,omitempty"`
}

type sourcescopeAssignmentBody struct {
	ConnectorName string `json:"connector_name"`
	WorkspaceRef  string `json:"workspace_ref"`
	Mode          string `json:"mode,omitempty"`
	Enabled       bool   `json:"enabled"`
	Note          string `json:"note,omitempty"`
}

type sourcescopeWsConnectorBody struct {
	Name         string            `json:"name"`
	Kind         string            `json:"kind"`
	WorkspaceRef string            `json:"workspace_ref"`
	Config       map[string]string `json:"config,omitempty"`
	Secrets      map[string]string `json:"secrets,omitempty"`
	PollSeconds  int               `json:"poll_seconds,omitempty"`
	Enabled      bool              `json:"enabled"`
	Note         string            `json:"note,omitempty"`
}

// ---------------------------------------------------------------------------

func newSourceScopeCmd() *cobra.Command {
	flags := &authClientFlags{}
	cmd := &cobra.Command{
		Use:     "sourcescope",
		Aliases: []string{"source-scope"},
		Short:   "Decide which sources a workspace or agent may reach",
		Long: "Govern source scoping: the bindings that confine a source to a workspace or agent\n" +
			"group, the connector assignments, the workspace-scoped connectors, the retrieval\n" +
			"guard postures and the dual-controlled queue that relaxations pass through.\n\n" +
			"TWO OUTCOMES ARE SUCCESSES HERE AND THEY ARE NOT THE SAME. A change that\n" +
			"TIGHTENS confinement applies immediately (200/201). A change that RELAXES an\n" +
			"existing confinement is answered 202: it is recorded as a proposal for a second\n" +
			"approver and is NOT in effect. Every verb that can be answered 202 says so on\n" +
			"stderr, and the response body carries the pending request.\n\n" +
			"Connection, credential and TLS values use the same resolution order and trust\n" +
			"controls as `auth`.",
		Example: `  olivares sourcescope bindings ls
  olivares sourcescope --server https://plane.example.com --tenant tenant-a bindings ls`,
		Args: cobra.NoArgs,
	}
	flags.addPersistent(cmd)
	client := datalaneClient{flags: flags, base: sourcescopeAPIBase, what: "sourcescope"}
	cmd.AddCommand(
		newSourceScopeBindingsCmd(client),
		newSourceScopeResourcesCmd(client),
		newSourceScopeSourcesCmd(client),
		newSourceScopeGuardPosturesCmd(client),
		newSourceScopePostureRequestsCmd(client),
		newSourceScopeResolveCmd(client),
		newSourceScopeAssignmentsCmd(client),
		newSourceScopeWsConnectorsCmd(client),
	)
	return cmd
}

// --- bindings --------------------------------------------------------------

type sourcescopeBindingFlags struct {
	sourceType  string
	sourceRef   string
	scopeTree   string
	scopeRef    string
	effect      string
	folderPath  string
	credName    string
	credRefKind string
	credRef     string
	credHint    string
	enabled     bool
	note        string
}

func (f *sourcescopeBindingFlags) add(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.sourceType, "source-type", "", "mcp, model, provider, knowledge or data")
	cmd.Flags().StringVar(&f.sourceRef, "source-ref", "", "reference of the source being confined")
	cmd.Flags().StringVar(&f.scopeTree, "scope-tree", "", "scope tree the binding attaches to (e.g. workspace, agent_group)")
	cmd.Flags().StringVar(&f.scopeRef, "scope-ref", "", "reference within the scope tree")
	cmd.Flags().StringVar(&f.effect, "effect", "", "allow or forbid")
	cmd.Flags().StringVar(&f.folderPath, "folder-path", "", "folder or subtree the binding is anchored to")
	cmd.Flags().StringVar(&f.credName, "cred-name", "", "name of the scoped credential the binding carries")
	cmd.Flags().StringVar(&f.credRefKind, "cred-ref-kind", "", "kind of the credential reference")
	cmd.Flags().StringVar(&f.credRef, "cred-ref", "", "credential reference (a locator, never a secret)")
	cmd.Flags().StringVar(&f.credHint, "cred-hint", "", "non-secret hint shown to operators")
	cmd.Flags().BoolVar(&f.enabled, "enabled", false, "whether the binding is in force")
	cmd.Flags().StringVar(&f.note, "note", "", "note recorded with the binding")
}

func (f *sourcescopeBindingFlags) body() sourcescopeBindingBody {
	return sourcescopeBindingBody{
		SourceType: f.sourceType, SourceRef: f.sourceRef, ScopeTree: f.scopeTree,
		ScopeRef: f.scopeRef, Effect: f.effect, FolderPath: f.folderPath,
		CredName: f.credName, CredRefKind: f.credRefKind, CredRef: f.credRef,
		CredHint: f.credHint, Enabled: f.enabled, Note: f.note,
	}
}

func newSourceScopeBindingsCmd(client datalaneClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bindings",
		Short: "Confine a source to a workspace or agent group",
		Long: "A binding is the rule that takes a source out of tenant-wide visibility and\n" +
			"confines it to a scope. The FIRST allow for a source is what brings it under\n" +
			"governance and applies immediately; a later allow WIDENS reach and is proposed.",
		Example: "  olivares sourcescope bindings ls --source-type knowledge",
		Args:    cobra.NoArgs,
	}

	var (
		page       datalanePageFlags
		sourceType string
		sourceRef  string
		scopeTree  string
	)
	list := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List source-to-scope bindings",
		Long: datalaneListLong("List the tenant's bindings, optionally narrowed to a source type, a source\n" +
			"reference or a scope tree."),
		Example: `  olivares sourcescope bindings ls
  olivares sourcescope bindings ls --source-type knowledge --source-ref kb_123`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			query := datalaneFilter(nil, "source_type", sourceType)
			query = datalaneFilter(query, "source_ref", sourceRef)
			query = datalaneFilter(query, "scope_tree", scopeTree)
			query, err := page.values(cmd, query)
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodGet, "/bindings", query, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneRenderList(cmd, client.what, raw,
				"no bindings declared (unconfined sources stay tenant-wide)",
				datalaneCols("ID", "id", "SOURCE_TYPE", "source_type", "SOURCE_REF", "source_ref",
					"SCOPE_TREE", "scope_tree", "SCOPE_REF", "scope_ref", "EFFECT", "effect",
					"ENABLED", "enabled", "CRED", "cred_name"))
		},
	}
	page.add(list)
	list.Flags().StringVar(&sourceType, "source-type", "", "only bindings of this source type")
	list.Flags().StringVar(&sourceRef, "source-ref", "", "only bindings of this source reference")
	list.Flags().StringVar(&scopeTree, "scope-tree", "", "only bindings in this scope tree")

	get := &cobra.Command{
		Use:     "get <binding-id>",
		Short:   "Show one binding",
		Long:    "Show one binding: its source, its scope, its effect and the credential it carries.",
		Example: "  olivares sourcescope bindings get bnd_123",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := datalanePath("bindings", args[0])
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodGet, path, nil, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}

	var createFields sourcescopeBindingFlags
	create := &cobra.Command{
		Use:   "create",
		Short: "Bind a source to a scope",
		Long: "Create a binding. An allow added to an ALREADY confined source widens who can\n" +
			"reach it, so the control plane answers 202 and records a dual-controlled proposal\n" +
			"instead of applying it. The first allow for a source, and every forbid, apply\n" +
			"immediately.",
		Example: `  olivares sourcescope bindings create --source-type knowledge --source-ref kb_123 --scope-tree workspace --scope-ref ws-1 --effect allow --enabled
  olivares sourcescope bindings create --source-type mcp --source-ref github --scope-tree workspace --scope-ref ws-2 --effect forbid --enabled`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if createFields.sourceType == "" || createFields.sourceRef == "" || createFields.scopeTree == "" {
				return exitcode.New(exitcode.Usage,
					fmt.Errorf("--source-type, --source-ref and --scope-tree are required"))
			}
			raw, code, err := client.do(cmd, http.MethodPost, "/bindings", nil, createFields.body())
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}
	createFields.add(create)

	var (
		setFields sourcescopeBindingFlags
		replace   bool
	)
	set := &cobra.Command{
		Use:   "set <binding-id>",
		Short: "Replace a binding",
		Long: "Replace a binding.\n\n" +
			"THIS REPLACES, IT DOES NOT PATCH. The control plane re-resolves the scope from\n" +
			"the payload and rewrites the stored row, so a field left out is reset. The\n" +
			"command therefore refuses a partial invocation unless --replace states that the\n" +
			"reset is intended. A relaxing update is answered 202 and applies only after a\n" +
			"second approver.\n\n" +
			"The SOURCE IDENTITY is the exception: --source-type and --source-ref are the\n" +
			"immutable natural key and the control plane forces them back to the stored row,\n" +
			"so passing them here changes nothing. They are therefore not part of what a\n" +
			"complete replace has to name.",
		Example: `  olivares sourcescope bindings set bnd_123 --source-type knowledge --source-ref kb_123 --scope-tree workspace --scope-ref ws-1 --effect forbid --enabled --note tightened
  olivares sourcescope bindings set bnd_123 --enabled=false --replace`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Measured against modules/sourcescope/binding.go handleUpdateBinding:
			// source_type/source_ref are forced back from the stored row, folder_path
			// is derived by resolveScope, and everything else in bindingDTO.fields is
			// written straight from the payload.
			if err := datalaneRequireCompleteReplace(cmd, replace, []string{
				"scope-tree", "scope-ref", "effect", "enabled", "note",
				"cred-name", "cred-ref-kind", "cred-ref", "cred-hint",
			}); err != nil {
				return err
			}
			path, err := datalanePath("bindings", args[0])
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodPut, path, nil, setFields.body())
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}
	setFields.add(set)
	addDatalaneReplaceFlag(set, &replace)

	var yes bool
	remove := &cobra.Command{
		Use:     "rm <binding-id>",
		Aliases: []string{"delete"},
		Short:   "Delete a binding",
		Long: "Delete a binding. Removing the LAST binding of a source unconfines it, which\n" +
			"WIDENS who can reach it, so the control plane treats a relaxing delete as a\n" +
			"dual-controlled proposal (202) rather than applying it.\n\n" +
			"An endpoint answering 204 has no body: JSON output is then the CLI's own\n" +
			"{\"ok\":true,\"http_status\":204}.",
		Example: "  olivares sourcescope bindings rm bnd_123 --yes",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := confirmDestructive(cmd, yes, fmt.Sprintf(
				"delete binding %s (removing the last binding of a source unconfines it)",
				safeCLIValue(args[0], ""))); err != nil {
				return err
			}
			path, err := datalanePath("bindings", args[0])
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodDelete, path, nil, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code,
				fmt.Sprintf("deleted binding %s", safeCLIValue(args[0], "")))
		},
	}
	addYesFlag(remove, &yes)

	cmd.AddCommand(list, get, create, set, remove)
	return cmd
}

// --- resources -------------------------------------------------------------

func newSourceScopeResourcesCmd(client datalaneClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "resources",
		Short:   "Navigate the tenant's resource tree",
		Long:    "The navigable resource tree a folder or subtree binding is anchored to.",
		Example: "  olivares sourcescope resources ls",
		Args:    cobra.NoArgs,
	}
	var (
		page        datalanePageFlags
		kind        string
		workspaceID string
		parent      string
		subtree     string
	)
	list := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List resources, by children or by subtree",
		Long: datalaneListLong("List the tenant's resources. --parent lists the direct children of one node,\n" +
			"--subtree lists everything beneath it; both take a real resource id, which is\n" +
			"what a folder binding must be anchored to."),
		Example: `  olivares sourcescope resources ls --kind folder
  olivares sourcescope resources ls --parent res_123 --limit 100`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			query := datalaneFilter(nil, "kind", kind)
			query = datalaneFilter(query, "workspace_id", workspaceID)
			query = datalaneFilter(query, "parent", parent)
			query = datalaneFilter(query, "subtree", subtree)
			query, err := page.values(cmd, query)
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodGet, "/resources", query, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneRenderList(cmd, client.what, raw, "no resources match",
				datalaneCols("ID", "id", "KIND", "kind", "NAME", "name",
					"PARENT", "parent_id", "WORKSPACE", "workspace_id", "PATH", "path"))
		},
	}
	page.add(list)
	list.Flags().StringVar(&kind, "kind", "", "only resources of this kind")
	list.Flags().StringVar(&workspaceID, "workspace-id", "", "only resources of this workspace")
	list.Flags().StringVar(&parent, "parent", "", "list the direct children of this resource id")
	list.Flags().StringVar(&subtree, "subtree", "", "list everything beneath this resource id")
	cmd.AddCommand(list)
	return cmd
}

// --- sources (disable-scoping) --------------------------------------------

func newSourceScopeSourcesCmd(client datalaneClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "sources",
		Short:   "Source-wide posture operations",
		Long:    "Operations that act on a whole source rather than on one binding.",
		Example: "  olivares sourcescope sources disable-scoping --source-type knowledge --source-ref kb_123 --yes",
		Args:    cobra.NoArgs,
	}
	var (
		sourceType string
		sourceRef  string
		yes        bool
	)
	disable := &cobra.Command{
		Use:   "disable-scoping",
		Short: "Propose removing ALL scoping from a source",
		Long: "Propose disabling every binding of one source — the flagship one-way relaxation.\n\n" +
			"THIS IS THE MOST WIDENING OPERATION IN THIS FAMILY, and it is destructive in the\n" +
			"sense that matters here even though it deletes nothing: it removes the\n" +
			"confinement that decided who could reach the source. It therefore asks for\n" +
			"confirmation and refuses an unattended session without --yes.\n\n" +
			"It is ALWAYS dual-controlled: the control plane answers 202 and records a pending\n" +
			"request. Nothing changes until a second approver approves it, and what the source\n" +
			"becomes on approval depends on whether its reference carries connector assignment\n" +
			"rows — the recorded reason says which.",
		Example: "  olivares sourcescope sources disable-scoping --source-type knowledge --source-ref kb_123 --yes",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if sourceType == "" || sourceRef == "" {
				return exitcode.New(exitcode.Usage,
					fmt.Errorf("--source-type and --source-ref are required"))
			}
			if err := confirmDestructive(cmd, yes, fmt.Sprintf(
				"propose disabling ALL scoping for %s source %s, which widens who can reach it",
				safeCLIValue(sourceType, ""), safeCLIValue(sourceRef, ""))); err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodPost, "/sources/disable-scoping", nil,
				sourcescopeDisableScopingBody{SourceType: sourceType, SourceRef: sourceRef})
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}
	disable.Flags().StringVar(&sourceType, "source-type", "", "mcp, model, provider, knowledge or data")
	disable.Flags().StringVar(&sourceRef, "source-ref", "", "reference of the source to unconfine")
	addYesFlag(disable, &yes)
	cmd.AddCommand(disable)
	return cmd
}

// --- guard postures --------------------------------------------------------

func newSourceScopeGuardPosturesCmd(client datalaneClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "guard-postures",
		Short: "Read and set the retrieval guard posture",
		Long: "The guard posture is a separate axis from source scoping: it decides whether\n" +
			"governed retrieval stays ACL/clearance/region aware, or drops to public content\n" +
			"only. With no row, a knowledge base is ACL-aware by default.",
		Example: "  olivares sourcescope guard-postures ls",
		Args:    cobra.NoArgs,
	}
	var (
		page       datalanePageFlags
		sourceType string
		sourceRef  string
		profile    string
	)
	list := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List explicit guard-posture overrides",
		Long: datalaneListLong("List the explicit guard-posture overrides. Only overrides are listed: a source\n" +
			"with no row is ACL-aware, which is the deny-closed default."),
		Example: `  olivares sourcescope guard-postures ls
  olivares sourcescope guard-postures ls --profile public_only`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			query := datalaneFilter(nil, "source_type", sourceType)
			query = datalaneFilter(query, "source_ref", sourceRef)
			query = datalaneFilter(query, "profile", profile)
			query, err := page.values(cmd, query)
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodGet, "/guard-postures", query, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneRenderList(cmd, client.what, raw,
				"no guard-posture overrides (every source stays ACL-aware)",
				datalaneCols("ID", "id", "SOURCE_TYPE", "source_type", "SOURCE_REF", "source_ref",
					"PROFILE", "profile", "REASON", "reason"))
		},
	}
	page.add(list)
	list.Flags().StringVar(&sourceType, "source-type", "", "only postures of this source type")
	list.Flags().StringVar(&sourceRef, "source-ref", "", "only postures of this source reference")
	list.Flags().StringVar(&profile, "profile", "", "only postures with this profile")

	var (
		setSourceType string
		setSourceRef  string
		setProfile    string
		reason        string
	)
	set := &cobra.Command{
		Use:   "set",
		Short: "Set the guard posture of one source",
		Long: "Set the retrieval guard posture of one source. The two directions are NOT\n" +
			"symmetric and the control plane says which happened: acl_aware TIGHTENS and\n" +
			"applies at once, while public_only RELAXES and is answered 202 — recorded for a\n" +
			"second approver and not in effect until approved.",
		Example: `  olivares sourcescope guard-postures set --source-ref kb_123 --profile acl_aware
  olivares sourcescope guard-postures set --source-ref kb_123 --profile public_only --reason "public FAQ corpus"`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if setSourceRef == "" || setProfile == "" {
				return exitcode.New(exitcode.Usage, fmt.Errorf("--source-ref and --profile are required"))
			}
			raw, code, err := client.do(cmd, http.MethodPut, "/guard-postures", nil,
				sourcescopeGuardPostureBody{
					SourceType: setSourceType, SourceRef: setSourceRef,
					Profile: setProfile, Reason: reason,
				})
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}
	set.Flags().StringVar(&setSourceType, "source-type", "", "source type (the control plane requires knowledge here)")
	set.Flags().StringVar(&setSourceRef, "source-ref", "", "reference of the source the posture applies to")
	set.Flags().StringVar(&setProfile, "profile", "", "acl_aware (tightens) or public_only (relaxes, dual-controlled)")
	set.Flags().StringVar(&reason, "reason", "", "reason an approver will read")

	cmd.AddCommand(list, set)
	return cmd
}

// --- posture requests ------------------------------------------------------

func newSourceScopePostureRequestsCmd(client datalaneClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "posture-requests",
		Short: "Review the dual-control queue of proposed relaxations",
		Long: "Every relaxation of an existing confinement lands here as a pending request. It\n" +
			"is the second half of the dual control: the proposer rides the editor-tier write\n" +
			"path, the decision is admin-tier, and one actor can never do both.",
		Example: "  olivares sourcescope posture-requests ls --status pending",
		Args:    cobra.NoArgs,
	}
	var (
		page       datalanePageFlags
		status     string
		sourceType string
		sourceRef  string
	)
	list := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List posture-change requests",
		Long: datalaneListLong("List posture-change requests, optionally narrowed by status, source type or\n" +
			"source reference. This is the reviewer's queue."),
		Example: `  olivares sourcescope posture-requests ls --status pending
  olivares sourcescope posture-requests ls --source-ref kb_123 -o json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			query := datalaneFilter(nil, "status", status)
			query = datalaneFilter(query, "source_type", sourceType)
			query = datalaneFilter(query, "source_ref", sourceRef)
			query, err := page.values(cmd, query)
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodGet, "/posture-requests", query, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneRenderList(cmd, client.what, raw, "no posture-change requests",
				datalaneCols("ID", "id", "OP", "op", "SOURCE_TYPE", "source_type",
					"SOURCE_REF", "source_ref", "STATUS", "status", "PROPOSER", "proposer",
					"DECIDED_BY", "decided_by"))
		},
	}
	page.add(list)
	list.Flags().StringVar(&status, "status", "", "only requests in this status")
	list.Flags().StringVar(&sourceType, "source-type", "", "only requests for this source type")
	list.Flags().StringVar(&sourceRef, "source-ref", "", "only requests for this source reference")

	get := &cobra.Command{
		Use:   "get <request-id>",
		Short: "Show one posture-change request",
		Long: "Show one posture-change request in full, including the reason recorded for the\n" +
			"approver and the binding it targets.",
		Example: "  olivares sourcescope posture-requests get pr_123",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := datalanePath("posture-requests", args[0])
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodGet, path, nil, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}

	cmd.AddCommand(list, get,
		newSourceScopePostureDecisionCmd(client, "approve",
			"Approve a pending relaxation and apply it",
			"Approve a pending posture change. THIS APPLIES THE RELAXATION: from here the\nsource is reachable by whoever the proposal admitted. It is admin-tier and\nseparated from the proposer by design, so it asks for confirmation.",
			true),
		newSourceScopePostureDecisionCmd(client, "reject",
			"Reject a pending relaxation, changing nothing",
			"Reject a pending posture change. Nothing is applied and the confinement stays as\nit is; the request keeps its record of who decided.",
			false),
	)
	return cmd
}

func newSourceScopePostureDecisionCmd(client datalaneClient, verb, short, long string, destructive bool) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     verb + " <request-id>",
		Short:   short,
		Long:    long,
		Example: fmt.Sprintf("  olivares sourcescope posture-requests %s pr_123", verb),
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if destructive {
				if err := confirmDestructive(cmd, yes, fmt.Sprintf(
					"approve posture request %s and APPLY the relaxation it proposes",
					safeCLIValue(args[0], ""))); err != nil {
					return err
				}
			}
			path, err := datalanePath("posture-requests", args[0], verb)
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodPost, path, nil, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}
	if destructive {
		cmd.Example = fmt.Sprintf("  olivares sourcescope posture-requests %s pr_123 --yes", verb)
		addYesFlag(cmd, &yes)
	}
	return cmd
}

// --- resolve ---------------------------------------------------------------

func newSourceScopeResolveCmd(client datalaneClient) *cobra.Command {
	var (
		sourceType string
		sourceRef  string
		actorKind  string
		actorRef   string
	)
	cmd := &cobra.Command{
		Use:   "resolve",
		Short: "Preview what one actor would resolve for one source",
		Long: "Ask the resolver what a session or agent WOULD get for a source, without\n" +
			"performing the access. It is the read-only preview an operator uses to check a\n" +
			"binding change before making it, and it reports the decision and the rule that\n" +
			"produced it.",
		Example: `  olivares sourcescope resolve --source-type knowledge --source-ref kb_123 --actor-kind agent --actor-ref agent-1
  olivares sourcescope resolve --source-type mcp --source-ref github --actor-kind session --actor-ref sess-9 -o json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if sourceType == "" || sourceRef == "" || actorKind == "" || actorRef == "" {
				return exitcode.New(exitcode.Usage, fmt.Errorf(
					"--source-type, --source-ref, --actor-kind and --actor-ref are all required"))
			}
			query := datalaneFilter(nil, "source_type", sourceType)
			query = datalaneFilter(query, "source_ref", sourceRef)
			query = datalaneFilter(query, "actor_kind", actorKind)
			query = datalaneFilter(query, "actor_ref", actorRef)
			raw, code, err := client.do(cmd, http.MethodGet, "/resolve", query, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}
	cmd.Flags().StringVar(&sourceType, "source-type", "", "mcp, model, provider, knowledge or data")
	cmd.Flags().StringVar(&sourceRef, "source-ref", "", "reference of the source to resolve")
	cmd.Flags().StringVar(&actorKind, "actor-kind", "", "session or agent")
	cmd.Flags().StringVar(&actorRef, "actor-ref", "", "reference of the actor to resolve for")
	return cmd
}

// --- assignments -----------------------------------------------------------

type sourcescopeAssignmentFlags struct {
	connectorName string
	workspaceRef  string
	mode          string
	enabled       bool
	note          string
}

func (f *sourcescopeAssignmentFlags) add(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.connectorName, "connector-name", "", "name of the global connector being assigned")
	cmd.Flags().StringVar(&f.workspaceRef, "workspace-ref", "", "workspace the connector is assigned to")
	cmd.Flags().StringVar(&f.mode, "mode", "", "rw (default) or r")
	cmd.Flags().BoolVar(&f.enabled, "enabled", false, "whether the assignment is in force")
	cmd.Flags().StringVar(&f.note, "note", "", "note recorded with the assignment")
}

func (f *sourcescopeAssignmentFlags) body() sourcescopeAssignmentBody {
	return sourcescopeAssignmentBody{
		ConnectorName: f.connectorName, WorkspaceRef: f.workspaceRef,
		Mode: f.mode, Enabled: f.enabled, Note: f.note,
	}
}

func newSourceScopeAssignmentsCmd(client datalaneClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "assignments",
		Short: "Assign global connectors to workspaces",
		Long: "An assignment confines a global connector to the workspaces it names. With NO\n" +
			"assignment rows a connector is visible tenant-wide, so the first assignment\n" +
			"tightens and the last deletion widens.",
		Example: "  olivares sourcescope assignments ls",
		Args:    cobra.NoArgs,
	}
	var (
		page          datalanePageFlags
		connectorName string
		workspaceRef  string
	)
	list := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List connector-to-workspace assignments",
		Long: datalaneListLong("List assignments, optionally narrowed to one connector or one workspace.\n" +
			"An empty list does NOT mean no connector is reachable: with no rows at all,\n" +
			"connectors are globally visible."),
		Example: `  olivares sourcescope assignments ls
  olivares sourcescope assignments ls --connector-name github --workspace-ref ws-1`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			query := datalaneFilter(nil, "connector_name", connectorName)
			query = datalaneFilter(query, "workspace_ref", workspaceRef)
			query, err := page.values(cmd, query)
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodGet, "/assignments", query, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneRenderList(cmd, client.what, raw,
				"no assignments (unassigned connectors are visible tenant-wide)",
				datalaneCols("ID", "id", "CONNECTOR", "connector_name", "WORKSPACE", "workspace_ref",
					"MODE", "mode", "ENABLED", "enabled", "NOTE", "note"))
		},
	}
	page.add(list)
	list.Flags().StringVar(&connectorName, "connector-name", "", "only assignments of this connector")
	list.Flags().StringVar(&workspaceRef, "workspace-ref", "", "only assignments to this workspace")

	get := &cobra.Command{
		Use:     "get <assignment-id>",
		Short:   "Show one assignment",
		Long:    "Show one connector-to-workspace assignment with its mode and enabled state.",
		Example: "  olivares sourcescope assignments get asg_123",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := datalanePath("assignments", args[0])
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodGet, path, nil, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}

	var createFields sourcescopeAssignmentFlags
	create := &cobra.Command{
		Use:   "create",
		Short: "Assign a connector to a workspace",
		Long: "Assign a global connector to a workspace. An ENABLED assignment added to an\n" +
			"already-assigned connector admits a workspace that could not reach it a moment\n" +
			"earlier, so it is answered 202 and proposed. The FIRST assignment — the one that\n" +
			"takes the connector from globally visible to confined — applies immediately.",
		Example: `  olivares sourcescope assignments create --connector-name github --workspace-ref ws-1 --enabled
  olivares sourcescope assignments create --connector-name jira --workspace-ref ws-2 --mode r --enabled`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if createFields.connectorName == "" || createFields.workspaceRef == "" {
				return exitcode.New(exitcode.Usage,
					fmt.Errorf("--connector-name and --workspace-ref are required"))
			}
			raw, code, err := client.do(cmd, http.MethodPost, "/assignments", nil, createFields.body())
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}
	createFields.add(create)

	var (
		setFields sourcescopeAssignmentFlags
		replace   bool
	)
	set := &cobra.Command{
		Use:   "set <assignment-id>",
		Short: "Replace an assignment",
		Long: "Replace an assignment.\n\n" +
			"THIS REPLACES, IT DOES NOT PATCH: --enabled and --note are written from the\n" +
			"request, so leaving one out resets it. The command refuses a partial invocation\n" +
			"unless --replace states that the reset is intended.\n\n" +
			"--connector-name and --workspace-ref are the immutable unique key and are forced\n" +
			"back from the stored row; an omitted --mode falls back to the stored value. None\n" +
			"of the three can be lost, so a complete replace does not have to name them.\n\n" +
			"Enabling a parked assignment ADMITS a workspace, so it is answered 202 and\n" +
			"applies only after a second approver.",
		Example: `  olivares sourcescope assignments set asg_123 --enabled --note "approved by platform"
  olivares sourcescope assignments set asg_123 --enabled=false --replace`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Measured against modules/sourcescope/assignment.go
			// handleUpdateAssignment: connector_name/workspace_ref are overwritten
			// from the record and a blank mode falls back to it; only enabled and
			// note come from the payload unconditionally.
			if err := datalaneRequireCompleteReplace(cmd, replace,
				[]string{"enabled", "note"}); err != nil {
				return err
			}
			path, err := datalanePath("assignments", args[0])
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodPut, path, nil, setFields.body())
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}
	setFields.add(set)
	addDatalaneReplaceFlag(set, &replace)

	var yes bool
	remove := &cobra.Command{
		Use:     "rm <assignment-id>",
		Aliases: []string{"delete"},
		Short:   "Delete an assignment",
		Long: "Delete an assignment. Deleting the LAST row for a connector makes it visible in\n" +
			"EVERY workspace again — unassigned connectors are globally visible — so this is\n" +
			"the widening direction and it asks for confirmation.\n\n" +
			"An endpoint answering 204 has no body: JSON output is then the CLI's own\n" +
			"{\"ok\":true,\"http_status\":204}.",
		Example: "  olivares sourcescope assignments rm asg_123 --yes",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := confirmDestructive(cmd, yes, fmt.Sprintf(
				"delete assignment %s (deleting the last one makes the connector visible tenant-wide)",
				safeCLIValue(args[0], ""))); err != nil {
				return err
			}
			path, err := datalanePath("assignments", args[0])
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodDelete, path, nil, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code,
				fmt.Sprintf("deleted assignment %s", safeCLIValue(args[0], "")))
		},
	}
	addYesFlag(remove, &yes)

	cmd.AddCommand(list, get, create, set, remove)
	return cmd
}

// --- workspace connectors --------------------------------------------------

type sourcescopeWsConnectorFlags struct {
	name         string
	kind         string
	workspaceRef string
	config       []string
	secretsFile  string
	pollSeconds  int
	enabled      bool
	note         string
}

func (f *sourcescopeWsConnectorFlags) add(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.name, "name", "", "workspace connector name")
	cmd.Flags().StringVar(&f.kind, "kind", "", "connector kind")
	cmd.Flags().StringVar(&f.workspaceRef, "workspace-ref", "", "workspace the connector belongs to")
	cmd.Flags().StringArrayVar(&f.config, "config", nil, "config entry as key=value, repeatable")
	cmd.Flags().StringVar(&f.secretsFile, "secrets-file", "",
		"file holding the secrets as a JSON object of string values (- for stdin)")
	cmd.Flags().IntVar(&f.pollSeconds, "poll-seconds", 0, "polling interval in seconds")
	cmd.Flags().BoolVar(&f.enabled, "enabled", false, "whether the connector is in force")
	cmd.Flags().StringVar(&f.note, "note", "", "note recorded with the connector")
}

// body reads the secrets from a FILE and never from a flag value. A credential
// passed as an argument is visible in the process table and in shell history to
// every user on the box, and this endpoint takes real connector credentials.
func (f *sourcescopeWsConnectorFlags) body(cmd *cobra.Command) (sourcescopeWsConnectorBody, error) {
	config, err := datalaneKeyValues("config", f.config)
	if err != nil {
		return sourcescopeWsConnectorBody{}, err
	}
	body := sourcescopeWsConnectorBody{
		Name: f.name, Kind: f.kind, WorkspaceRef: f.workspaceRef, Config: config,
		PollSeconds: f.pollSeconds, Enabled: f.enabled, Note: f.note,
	}
	if f.pollSeconds < 0 {
		return sourcescopeWsConnectorBody{}, exitcode.New(exitcode.Usage,
			fmt.Errorf("--poll-seconds cannot be negative"))
	}
	if !cmd.Flags().Changed("secrets-file") {
		return body, nil
	}
	raw, err := datalaneJSONArg(cmd, "secrets", "", f.secretsFile)
	if err != nil {
		return sourcescopeWsConnectorBody{}, err
	}
	secrets := map[string]string{}
	if err := json.Unmarshal(raw, &secrets); err != nil {
		return sourcescopeWsConnectorBody{}, exitcode.New(exitcode.Usage,
			fmt.Errorf("--secrets-file must be a JSON object whose values are strings: %w", err))
	}
	body.Secrets = secrets
	return body, nil
}

func newSourceScopeWsConnectorsCmd(client datalaneClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "workspace-connectors",
		Aliases: []string{"ws-connectors"},
		Short:   "Manage connectors that belong to one workspace",
		Long: "A workspace connector is owned by a single workspace rather than by the tenant.\n" +
			"Its secrets are written, never read back: the API returns configuration and\n" +
			"status, never the credential.",
		Example: "  olivares sourcescope workspace-connectors ls",
		Args:    cobra.NoArgs,
	}
	var (
		page         datalanePageFlags
		workspaceRef string
		kind         string
	)
	list := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List workspace connectors",
		Long:    datalaneListLong("List workspace-scoped connectors, optionally narrowed to one workspace or kind."),
		Example: `  olivares sourcescope workspace-connectors ls
  olivares sourcescope workspace-connectors ls --workspace-ref ws-1 --kind github`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			query := datalaneFilter(nil, "workspace_ref", workspaceRef)
			query = datalaneFilter(query, "kind", kind)
			query, err := page.values(cmd, query)
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodGet, "/workspace-connectors", query, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneRenderList(cmd, client.what, raw, "no workspace connectors declared",
				datalaneCols("ID", "id", "NAME", "name", "KIND", "kind",
					"WORKSPACE", "workspace_ref", "ENABLED", "enabled",
					"POLL", "poll_seconds", "STATUS", "status"))
		},
	}
	page.add(list)
	list.Flags().StringVar(&workspaceRef, "workspace-ref", "", "only connectors of this workspace")
	list.Flags().StringVar(&kind, "kind", "", "only connectors of this kind")

	get := &cobra.Command{
		Use:   "get <connector-id>",
		Short: "Show one workspace connector",
		Long: "Show one workspace connector: its kind, workspace, configuration and status. The\n" +
			"secrets are never returned.",
		Example: "  olivares sourcescope workspace-connectors get wc_123",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := datalanePath("workspace-connectors", args[0])
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodGet, path, nil, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}

	var createFields sourcescopeWsConnectorFlags
	create := &cobra.Command{
		Use:   "create",
		Short: "Declare a workspace connector",
		Long: "Declare a connector owned by one workspace.\n\n" +
			"Secrets are supplied ONLY through --secrets-file (a JSON object of string\n" +
			"values, - for stdin). There is no --secret flag on purpose: a credential passed\n" +
			"as an argument is readable in the process table by every user on the machine.",
		Example: `  olivares sourcescope workspace-connectors create --name docs --kind confluence --workspace-ref ws-1 --enabled
  olivares sourcescope workspace-connectors create --name docs --kind confluence --workspace-ref ws-1 --secrets-file ./secrets.json --config base_url=https://wiki.example.com`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			body, err := createFields.body(cmd)
			if err != nil {
				return err
			}
			if body.Name == "" || body.Kind == "" || body.WorkspaceRef == "" {
				return exitcode.New(exitcode.Usage,
					fmt.Errorf("--name, --kind and --workspace-ref are required"))
			}
			raw, code, err := client.do(cmd, http.MethodPost, "/workspace-connectors", nil, body)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}
	createFields.add(create)

	var (
		setFields sourcescopeWsConnectorFlags
		replace   bool
	)
	set := &cobra.Command{
		Use:   "set <connector-id>",
		Short: "Replace a workspace connector",
		Long: "Replace a workspace connector.\n\n" +
			"THIS REPLACES, IT DOES NOT PATCH: --config, --poll-seconds, --enabled and\n" +
			"--note are written from the request, so leaving one out resets it — an omitted\n" +
			"--config empties the whole configuration map. The command refuses a partial\n" +
			"invocation unless --replace states that the reset is intended.\n\n" +
			"Two things CANNOT be lost here and are therefore not part of a complete replace:\n" +
			"--name, --kind and --workspace-ref are the immutable natural key and are forced\n" +
			"back from the stored row, and supplying no secrets KEEPS the sealed ones (that\n" +
			"is the control plane's rule, not this command's). Secrets still come from\n" +
			"--secrets-file only.",
		Example: `  olivares sourcescope workspace-connectors set wc_123 --enabled --poll-seconds 300 --note nightly --config base_url=https://wiki.example.com
  olivares sourcescope workspace-connectors set wc_123 --enabled=false --replace`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Measured against modules/sourcescope/wsconnector.go
			// handleUpdateWsConnector: name/kind/workspace_ref are overwritten from
			// the record, sealWsSecrets keeps the existing secrets when none are
			// supplied, and wsConnectorDTO.fields writes the rest from the payload.
			if err := datalaneRequireCompleteReplace(cmd, replace,
				[]string{"config", "poll-seconds", "enabled", "note"}); err != nil {
				return err
			}
			body, err := setFields.body(cmd)
			if err != nil {
				return err
			}
			path, err := datalanePath("workspace-connectors", args[0])
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodPut, path, nil, body)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}
	setFields.add(set)
	addDatalaneReplaceFlag(set, &replace)

	var yes bool
	remove := &cobra.Command{
		Use:     "rm <connector-id>",
		Aliases: []string{"delete"},
		Short:   "Delete a workspace connector",
		Long: "Delete a workspace connector and the stored secrets that belong to it. Anything\n" +
			"ingesting through it stops.\n\n" +
			"An endpoint answering 204 has no body: JSON output is then the CLI's own\n" +
			"{\"ok\":true,\"http_status\":204}.",
		Example: "  olivares sourcescope workspace-connectors rm wc_123 --yes",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := confirmDestructive(cmd, yes, fmt.Sprintf(
				"delete workspace connector %s and the credentials stored with it",
				safeCLIValue(args[0], ""))); err != nil {
				return err
			}
			path, err := datalanePath("workspace-connectors", args[0])
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodDelete, path, nil, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code,
				fmt.Sprintf("deleted workspace connector %s", safeCLIValue(args[0], "")))
		},
	}
	addYesFlag(remove, &yes)

	cmd.AddCommand(list, get, create, set, remove)
	return cmd
}
