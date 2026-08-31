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
// Request bodies, mirroring modules/catalog exactly (DisallowUnknownFields).
// ---------------------------------------------------------------------------

type catalogEntryBody struct {
	Kind     string          `json:"kind"`
	Name     string          `json:"name"`
	Slug     string          `json:"slug"`
	Version  string          `json:"version"`
	Summary  string          `json:"summary,omitempty"`
	Spec     json.RawMessage `json:"spec,omitempty"`
	OwnerRef string          `json:"owner_ref,omitempty"`
}

type catalogAdmitBody struct {
	Bundle         json.RawMessage `json:"bundle"`
	PredicateTypes []string        `json:"predicate_types,omitempty"`
	ExpectedDigest string          `json:"expected_digest,omitempty"`
	Note           string          `json:"note,omitempty"`
}

type catalogAdmissionPolicyBody struct {
	RequireSigned        bool     `json:"require_signed"`
	RequireSubjectDigest bool     `json:"require_subject_digest"`
	AllowedIdentities    []string `json:"allowed_identities,omitempty"`
	AllowedIssuers       []string `json:"allowed_issuers,omitempty"`
	TrustedKeys          []string `json:"trusted_keys,omitempty"`
	TrustedRoots         []string `json:"trusted_roots,omitempty"`
	AllowedPredicates    []string `json:"allowed_predicates,omitempty"`
	Note                 string   `json:"note,omitempty"`
}

type catalogInstantiateBody struct {
	Name      string `json:"name"`
	TargetRef string `json:"target_ref,omitempty"`
	Note      string `json:"note,omitempty"`
}

type catalogTransitionBody struct {
	Status string `json:"status"`
	Note   string `json:"note,omitempty"`
}

// ---------------------------------------------------------------------------

// newCatalogCmd is a NEW command group and deliberately does not extend `mcp`.
// The two surfaces are adjacent — `catalog mcp-admission policy` decides which
// MCP servers may be admitted at all, `mcp pins` decides which tool definitions
// stay approved once one is — but they are different modules, different
// permissions and different lifecycles, and folding admission into `mcp` would
// put a catalog-wide policy behind a tool-level noun.
func newCatalogCmd() *cobra.Command {
	flags := &authClientFlags{}
	cmd := &cobra.Command{
		Use:   "catalog",
		Short: "Admit and govern catalog entries, connectors and MCP servers",
		Long: "The admission gate of the data lane: catalog entries and their signed lifecycle\n" +
			"(draft, submitted, approved, deprecated), the supply-chain admission policies for\n" +
			"connectors and MCP servers, and the instances self-service provisions from an\n" +
			"approved entry.\n\n" +
			"Connection, credential and TLS values use the same resolution order and trust\n" +
			"controls as `auth`. These routes are served by the catalog module: an engine\n" +
			"built without it answers 404 for the whole namespace, and this command says so.",
		Example: `  olivares catalog entries ls
  olivares catalog --server https://plane.example.com --tenant tenant-a entries ls`,
		Args: cobra.NoArgs,
	}
	flags.addPersistent(cmd)
	client := datalaneClient{flags: flags, base: catalogAPIBase, what: "catalog"}
	cmd.AddCommand(
		newCatalogEntriesCmd(client),
		newCatalogPubkeyCmd(client),
		newCatalogAdmissionCmd(client, "mcp-admission", "mcp-admissions", "MCP server"),
		newCatalogAdmissionCmd(client, "connector-admission", "connector-admissions", "connector"),
		newCatalogInstancesCmd(client),
	)
	return cmd
}

// --- entries ---------------------------------------------------------------

type catalogEntryFlags struct {
	kind     string
	name     string
	slug     string
	version  string
	summary  string
	spec     string
	specFile string
	ownerRef string
}

func (f *catalogEntryFlags) add(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.kind, "kind", "", "agent, mcp, skill, template, model or connector")
	cmd.Flags().StringVar(&f.name, "name", "", "entry name")
	cmd.Flags().StringVar(&f.slug, "slug", "", "lowercase identifier (a-z, 0-9, - and _)")
	cmd.Flags().StringVar(&f.version, "version", "", "semantic version, e.g. 1.2.3")
	cmd.Flags().StringVar(&f.summary, "summary", "", "one-line summary")
	cmd.Flags().StringVar(&f.spec, "spec", "", "entry specification as a JSON object")
	cmd.Flags().StringVar(&f.specFile, "spec-file", "", "file holding the JSON specification (- for stdin)")
	cmd.Flags().StringVar(&f.ownerRef, "owner-ref", "", "owning team or principal")
}

func (f *catalogEntryFlags) body(cmd *cobra.Command) (catalogEntryBody, error) {
	spec, err := datalaneJSONArg(cmd, "spec", f.spec, f.specFile)
	if err != nil {
		return catalogEntryBody{}, err
	}
	return catalogEntryBody{
		Kind: f.kind, Name: f.name, Slug: f.slug, Version: f.version,
		Summary: f.summary, Spec: spec, OwnerRef: f.ownerRef,
	}, nil
}

func newCatalogEntriesCmd(client datalaneClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "entries",
		Short: "Author, review and admit catalog entries",
		Long: "A catalog entry moves draft to submitted to approved, and approval is what signs\n" +
			"it: the content hash and signature are computed then, and `verify` recomputes\n" +
			"them afterwards. A deprecated entry keeps both, so it stays verifiable — it is\n" +
			"retired, not erased.",
		Example: "  olivares catalog entries ls --status approved",
		Args:    cobra.NoArgs,
	}
	var (
		page   datalanePageFlags
		kind   string
		status string
		slug   string
	)
	list := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List catalog entries",
		Long:    datalaneListLong("List catalog entries, optionally narrowed by kind, status or slug."),
		Example: `  olivares catalog entries ls
  olivares catalog entries ls --kind mcp --status approved`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			query := datalaneFilter(nil, "kind", kind)
			query = datalaneFilter(query, "status", status)
			query = datalaneFilter(query, "slug", slug)
			query, err := page.values(cmd, query)
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodGet, "/entries", query, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneRenderList(cmd, client.what, raw, "no catalog entries",
				datalaneCols("ID", "id", "KIND", "kind", "SLUG", "slug", "VERSION", "version",
					"STATUS", "status", "SIGNED", "signed", "OWNER", "owner_ref"))
		},
	}
	page.add(list)
	list.Flags().StringVar(&kind, "kind", "", "only entries of this kind")
	list.Flags().StringVar(&status, "status", "", "only entries in this status")
	list.Flags().StringVar(&slug, "slug", "", "only entries with this slug")

	get := &cobra.Command{
		Use:     "get <entry-id>",
		Short:   "Show one catalog entry",
		Long:    "Show one entry: its kind, version, status, specification and signature metadata.",
		Example: "  olivares catalog entries get ce_123",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := datalanePath("entries", args[0])
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

	var createFields catalogEntryFlags
	create := &cobra.Command{
		Use:   "create",
		Short: "Author a draft catalog entry",
		Long: "Author a draft entry. It is not admissible yet: it must be submitted and then\n" +
			"approved, and approval is what computes its content hash and signature. The\n" +
			"control plane refuses a specification carrying an inline credential — reference\n" +
			"secrets by name instead.",
		Example: `  olivares catalog entries create --kind mcp --name "GitHub MCP" --slug github-mcp --version 1.0.0
  olivares catalog entries create --kind agent --name Triage --slug triage --version 0.1.0 --spec-file ./agent.json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			body, err := createFields.body(cmd)
			if err != nil {
				return err
			}
			if body.Kind == "" || body.Name == "" || body.Slug == "" || body.Version == "" {
				return exitcode.New(exitcode.Usage,
					fmt.Errorf("--kind, --name, --slug and --version are required"))
			}
			raw, code, err := client.do(cmd, http.MethodPost, "/entries", nil, body)
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
		setFields catalogEntryFlags
		replace   bool
	)
	set := &cobra.Command{
		Use:   "set <entry-id>",
		Short: "Replace a draft entry's authored fields",
		Long: "Replace the authored fields of an entry. Only a DRAFT can be edited: an entry\n" +
			"that has been approved carries a signature over its content, and rewriting it in\n" +
			"place would break that.\n\n" +
			"THIS REPLACES, IT DOES NOT PATCH: the control plane rewrites kind, name, slug,\n" +
			"version, summary, specification and owner from the request, so a field left out\n" +
			"is cleared — including the whole --spec. The command refuses a partial invocation\n" +
			"unless --replace states that the reset is intended.",
		Example: `  olivares catalog entries set ce_123 --kind mcp --name "GitHub MCP" --slug github-mcp --version 1.1.0 --summary "read-only" --owner-ref team-platform --spec-file ./mcp.json
  olivares catalog entries set ce_123 --summary "read-only" --replace`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Measured against modules/catalog/entries.go handleUpdateEntry:
			// entryDTO.entryFields writes kind/name/slug/version/summary/spec/
			// owner_ref straight from the payload, so an omitted --spec ERASES the
			// specification of a draft.
			if err := datalaneRequireCompleteReplace(cmd, replace,
				[]string{"kind", "name", "slug", "version", "summary", "owner-ref", "spec|spec-file"}); err != nil {
				return err
			}
			body, err := setFields.body(cmd)
			if err != nil {
				return err
			}
			path, err := datalanePath("entries", args[0])
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
		Use:     "rm <entry-id>",
		Aliases: []string{"delete"},
		Short:   "Delete a catalog entry",
		Long: "Delete a catalog entry. Deprecation is usually what you want: it retires the\n" +
			"entry while keeping its hash and signature, so what was once admitted stays\n" +
			"verifiable. Deleting removes that record.\n\n" +
			"An endpoint answering 204 has no body: JSON output is then the CLI's own\n" +
			"{\"ok\":true,\"http_status\":204}.",
		Example: "  olivares catalog entries rm ce_123 --yes",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := confirmDestructive(cmd, yes, fmt.Sprintf(
				"delete catalog entry %s (deprecate keeps it verifiable; delete does not)",
				safeCLIValue(args[0], ""))); err != nil {
				return err
			}
			path, err := datalanePath("entries", args[0])
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
				fmt.Sprintf("deleted catalog entry %s", safeCLIValue(args[0], "")))
		},
	}
	addYesFlag(remove, &yes)

	verify := &cobra.Command{
		Use:   "verify <entry-id>",
		Short: "Recompute an entry's hash and check its signature",
		Long: "Recompute the entry's content hash from its stored fields and verify the\n" +
			"signature made at approval. It reports hash_ok, signature_ok and a combined\n" +
			"verdict separately: a hash that no longer matches and a signature that does not\n" +
			"verify are different failures and must not be flattened into one.",
		Example: "  olivares catalog entries verify ce_123",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := datalanePath("entries", args[0], "verify")
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

	cmd.AddCommand(list, get, create, set, remove, verify,
		newCatalogEntryLifecycleCmd(client, "submit",
			"Submit a draft entry for review",
			"Move a draft entry into review. Only a draft can be submitted.",
			false, ""),
		newCatalogEntryLifecycleCmd(client, "approve",
			"Approve a submitted entry, hashing and signing it",
			"Approve an entry under review. Approval computes the content hash and signs it,\nwhich is what makes the entry admissible and later verifiable.",
			false, ""),
		newCatalogEntryLifecycleCmd(client, "deprecate",
			"Retire an approved entry",
			"Retire an approved entry. Its hash and signature are preserved, so it remains\nverifiable; what changes is that it stops being offered.",
			true, "deprecate catalog entry %s, withdrawing it from the catalog"),
		newCatalogEntryAdmitCmd(client),
		newCatalogEntryInstantiateCmd(client),
	)
	return cmd
}

func newCatalogEntryLifecycleCmd(client datalaneClient, verb, short, long string, destructive bool, confirmFmt string) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     verb + " <entry-id>",
		Short:   short,
		Long:    long,
		Example: fmt.Sprintf("  olivares catalog entries %s ce_123", verb),
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if destructive {
				if err := confirmDestructive(cmd, yes,
					fmt.Sprintf(confirmFmt, safeCLIValue(args[0], ""))); err != nil {
					return err
				}
			}
			path, err := datalanePath("entries", args[0], verb)
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
		cmd.Example = fmt.Sprintf("  olivares catalog entries %s ce_123 --yes", verb)
		cmd.Long += "\n\nIt withdraws something consumers may be using, so it asks for confirmation and\n" +
			"refuses an unattended session without --yes."
		addYesFlag(cmd, &yes)
	}
	return cmd
}

func newCatalogEntryAdmitCmd(client datalaneClient) *cobra.Command {
	var (
		bundle     string
		bundleFile string
		predicates []string
		digest     string
		note       string
	)
	cmd := &cobra.Command{
		Use:   "admit <entry-id>",
		Short: "Verify a supply-chain attestation for an entry",
		Long: "Submit a Sigstore attestation bundle for a catalog entry and record the verdict\n" +
			"under the active admission policy. The three outcomes are distinct: a malformed\n" +
			"bundle is refused (400), a well-formed bundle that fails to verify is a RECORDED\n" +
			"verdict (200 with admitted=false), and a verifying bundle admits the entry. Read\n" +
			"the admitted field, not just the exit code.",
		Example: `  olivares catalog entries admit ce_123 --bundle-file ./bundle.json
  olivares catalog entries admit ce_123 --bundle-file ./bundle.json --expected-digest sha256:abc123`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			raw, err := datalaneJSONArg(cmd, "bundle", bundle, bundleFile)
			if err != nil {
				return err
			}
			if raw == nil {
				return exitcode.New(exitcode.Usage,
					fmt.Errorf("supply the attestation with --bundle or --bundle-file"))
			}
			path, err := datalanePath("entries", args[0], "admit")
			if err != nil {
				return err
			}
			body, code, err := client.do(cmd, http.MethodPost, path, nil, catalogAdmitBody{
				Bundle: raw, PredicateTypes: predicates, ExpectedDigest: digest, Note: note,
			})
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, body)
			}
			return datalaneResult(cmd, body, code, "")
		},
	}
	cmd.Flags().StringVar(&bundle, "bundle", "", "attestation bundle as JSON")
	cmd.Flags().StringVar(&bundleFile, "bundle-file", "", "file holding the JSON attestation bundle (- for stdin)")
	cmd.Flags().StringArrayVar(&predicates, "predicate-type", nil, "predicate type to accept, repeatable")
	cmd.Flags().StringVar(&digest, "expected-digest", "", "subject digest the attestation must cover")
	cmd.Flags().StringVar(&note, "note", "", "note recorded with the verdict")
	return cmd
}

func newCatalogEntryInstantiateCmd(client datalaneClient) *cobra.Command {
	var (
		name      string
		targetRef string
		note      string
	)
	cmd := &cobra.Command{
		Use:   "instantiate <entry-id>",
		Short: "Request an instance from an approved entry",
		Long: "Request an instance of an approved catalog entry. This RECORDS a request with the\n" +
			"exact entry version as its provenance; it does not provision anything. The\n" +
			"approval decision and the provisioning are separate, governed steps — see\n" +
			"`catalog instances transition`.",
		Example: `  olivares catalog entries instantiate ce_123 --name support-bot
  olivares catalog entries instantiate ce_123 --name support-bot --target-ref ws-1`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return exitcode.New(exitcode.Usage, fmt.Errorf("--name is required"))
			}
			path, err := datalanePath("entries", args[0], "instantiate")
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodPost, path, nil,
				catalogInstantiateBody{Name: name, TargetRef: targetRef, Note: note})
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "name for the requested instance")
	cmd.Flags().StringVar(&targetRef, "target-ref", "", "where the instance is meant to land")
	cmd.Flags().StringVar(&note, "note", "", "note recorded with the request")
	return cmd
}

// --- pubkey ----------------------------------------------------------------

func newCatalogPubkeyCmd(client datalaneClient) *cobra.Command {
	return &cobra.Command{
		Use:   "pubkey",
		Short: "Show the public key catalog approvals are signed with",
		Long: "Show the public half of the key this control plane signs catalog approvals with,\n" +
			"so a third party can verify an entry's signature without trusting the API that\n" +
			"produced it.",
		Example: "  olivares catalog pubkey -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			raw, code, err := client.do(cmd, http.MethodGet, "/pubkey", nil, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}
}

// --- admission policies ----------------------------------------------------

// newCatalogAdmissionCmd builds the connector and MCP admission surfaces, which
// are the same policy document under two namespaces. They are separate groups
// rather than one flagged command because they are separate policies: relaxing
// one must never be a keystroke away from relaxing the other.
func newCatalogAdmissionCmd(client datalaneClient, group, listRoute, subject string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   group,
		Short: fmt.Sprintf("Read and set the %s supply-chain admission policy", subject),
		Long: fmt.Sprintf("The %s admission policy decides which attestations are accepted: whether a\n"+
			"signature is required at all, whether the subject digest must be covered, and\n"+
			"which identities, issuers, keys, roots and predicate types are trusted.\n\n"+
			"It is deny-closed by construction: an ENFORCING policy with no trust anchor is\n"+
			"refused by the control plane rather than silently admitting everything.", subject),
		Example: fmt.Sprintf("  olivares catalog %s policy get", group),
		Args:    cobra.NoArgs,
	}

	policy := &cobra.Command{
		Use:     "policy",
		Short:   fmt.Sprintf("Read or replace the %s admission policy", subject),
		Long:    fmt.Sprintf("Read or replace the %s admission policy document.", subject),
		Example: fmt.Sprintf("  olivares catalog %s policy get", group),
		Args:    cobra.NoArgs,
	}

	get := &cobra.Command{
		Use:     "get",
		Short:   fmt.Sprintf("Show the %s admission policy", subject),
		Long:    fmt.Sprintf("Show the %s admission policy currently in force, including who attested it and when.", subject),
		Example: fmt.Sprintf("  olivares catalog %s policy get -o json", group),
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := datalanePath(group, "policy")
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

	var (
		requireSigned bool
		requireDigest bool
		identities    []string
		issuers       []string
		keys          []string
		roots         []string
		predicates    []string
		note          string
		replace       bool
		yes           bool
	)
	set := &cobra.Command{
		Use:   "set",
		Short: fmt.Sprintf("Replace the %s admission policy", subject),
		Long: fmt.Sprintf("Replace the %s admission policy.\n\n"+
			"THIS REPLACES THE WHOLE DOCUMENT, IT DOES NOT PATCH. A trust anchor left out is\n"+
			"REMOVED, and dropping --require-signed opens admission to unsigned artifacts, so\n"+
			"the command refuses a partial invocation unless --replace states that the reset\n"+
			"is intended, and asks for confirmation because the effect is a change to what\n"+
			"this control plane will accept.\n\n"+
			"Only PUBLIC material belongs here: identities, issuers, public keys and roots.\n"+
			"The control plane refuses private key material.", subject),
		Example: fmt.Sprintf("  olivares catalog %s policy set --require-signed --require-subject-digest --allowed-issuer https://token.actions.githubusercontent.com --trusted-root ./root.pem --yes\n"+
			"  olivares catalog %s policy set --require-signed --replace --yes", group, group),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := datalaneRequireCompleteReplace(cmd, replace, []string{
				"require-signed", "require-subject-digest", "allowed-identity", "allowed-issuer",
				"trusted-key", "trusted-root", "allowed-predicate",
			}); err != nil {
				return err
			}
			if err := confirmDestructive(cmd, yes, fmt.Sprintf(
				"replace the %s admission policy, changing what this control plane will accept",
				subject)); err != nil {
				return err
			}
			path, err := datalanePath(group, "policy")
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodPut, path, nil, catalogAdmissionPolicyBody{
				RequireSigned: requireSigned, RequireSubjectDigest: requireDigest,
				AllowedIdentities: identities, AllowedIssuers: issuers,
				TrustedKeys: keys, TrustedRoots: roots, AllowedPredicates: predicates, Note: note,
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
	set.Flags().BoolVar(&requireSigned, "require-signed", false, "refuse artifacts without a verifying signature")
	set.Flags().BoolVar(&requireDigest, "require-subject-digest", false, "require the attestation to cover the subject digest")
	set.Flags().StringArrayVar(&identities, "allowed-identity", nil, "trusted keyless identity, repeatable")
	set.Flags().StringArrayVar(&issuers, "allowed-issuer", nil, "trusted OIDC issuer, repeatable")
	set.Flags().StringArrayVar(&keys, "trusted-key", nil, "trusted PUBLIC key, repeatable")
	set.Flags().StringArrayVar(&roots, "trusted-root", nil, "trusted root certificate, repeatable")
	set.Flags().StringArrayVar(&predicates, "allowed-predicate", nil, "accepted attestation predicate type, repeatable")
	set.Flags().StringVar(&note, "note", "", "note recorded with the policy")
	addDatalaneReplaceFlag(set, &replace)
	addYesFlag(set, &yes)

	policy.AddCommand(get, set)

	var (
		page      datalanePageFlags
		entryRef  string
		onlyValid bool
	)
	admissions := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   fmt.Sprintf("List recorded %s admission verdicts", subject),
		Long: datalaneListLong(fmt.Sprintf("List the %s admission verdicts recorded so far: what was claimed, what verified\n"+
			"and which entry it was for. --verified narrows to the ones that verified.", subject)),
		Example: fmt.Sprintf("  olivares catalog %s ls\n  olivares catalog %s ls --verified --limit 50", group, group),
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			query := datalaneFilter(nil, "entry_ref", entryRef)
			if onlyValid {
				query = datalaneFilter(query, "verified", "true")
			}
			query, err := page.values(cmd, query)
			if err != nil {
				return err
			}
			path, err := datalanePath(listRoute)
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodGet, path, query, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneRenderList(cmd, client.what, raw, "no admission verdicts recorded",
				datalaneCols("ID", "id", "ENTRY", "entry_ref", "ADMITTED", "admitted",
					"VERIFIED", "verified", "IDENTITY", "identity", "ISSUER", "issuer",
					"DIGEST", "subject_digest", "REASON", "reason"))
		},
	}
	page.add(admissions)
	admissions.Flags().StringVar(&entryRef, "entry-ref", "", "only verdicts for this entry")
	admissions.Flags().BoolVar(&onlyValid, "verified", false, "only verdicts that verified")

	cmd.AddCommand(policy, admissions)
	return cmd
}

// --- instances -------------------------------------------------------------

func newCatalogInstancesCmd(client datalaneClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "instances",
		Short: "Review and decide self-service instantiation requests",
		Long: "An instance is a request to provision something from an approved entry. It moves\n" +
			"requested to approved to active, or requested/approved to rejected; the module\n" +
			"enforces that flow and records every decision.",
		Example: "  olivares catalog instances ls --status requested",
		Args:    cobra.NoArgs,
	}
	var (
		page    datalanePageFlags
		status  string
		entryID string
	)
	list := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List instantiation requests",
		Long:    datalaneListLong("List instances, optionally narrowed by status or by the entry they came from."),
		Example: `  olivares catalog instances ls
  olivares catalog instances ls --status requested --entry-id ce_123`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			query := datalaneFilter(nil, "status", status)
			query = datalaneFilter(query, "entry_id", entryID)
			query, err := page.values(cmd, query)
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodGet, "/instances", query, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneRenderList(cmd, client.what, raw, "no instantiation requests",
				datalaneCols("ID", "id", "NAME", "name", "ENTRY", "entry_ref",
					"VERSION", "entry_version", "STATUS", "status", "TARGET", "target_ref"))
		},
	}
	page.add(list)
	list.Flags().StringVar(&status, "status", "", "only instances in this status")
	list.Flags().StringVar(&entryID, "entry-id", "", "only instances of this catalog entry")

	get := &cobra.Command{
		Use:   "get <instance-id>",
		Short: "Show one instantiation request",
		Long: "Show one instance, including the exact entry version it was requested from —\n" +
			"its provenance, which does not move when the entry is later updated.",
		Example: "  olivares catalog instances get ci_123",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := datalanePath("instances", args[0])
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

	var (
		newStatus string
		note      string
		yes       bool
	)
	transition := &cobra.Command{
		Use:   "transition <instance-id>",
		Short: "Record a governance decision on an instance",
		Long: "Record an admin-tier decision on an instantiation request: approved, rejected or\n" +
			"active. Only the transitions the module allows are accepted — requested to\n" +
			"approved or rejected, approved to active or rejected — and each is audited.\n\n" +
			"Approving or activating provisions capability for whoever asked, so this asks for\n" +
			"confirmation and refuses an unattended session without --yes.",
		Example: `  olivares catalog instances transition ci_123 --status approved --yes
  olivares catalog instances transition ci_123 --status rejected --note "no budget" --yes`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if newStatus == "" {
				return exitcode.New(exitcode.Usage,
					fmt.Errorf("--status is required (approved, rejected or active)"))
			}
			if err := confirmDestructive(cmd, yes, fmt.Sprintf(
				"transition instance %s to %s", safeCLIValue(args[0], ""), safeCLIValue(newStatus, ""))); err != nil {
				return err
			}
			path, err := datalanePath("instances", args[0], "transition")
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodPost, path, nil,
				catalogTransitionBody{Status: newStatus, Note: note})
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}
	transition.Flags().StringVar(&newStatus, "status", "", "approved, rejected or active")
	transition.Flags().StringVar(&note, "note", "", "note recorded with the decision")
	addYesFlag(transition, &yes)

	cmd.AddCommand(list, get, transition)
	return cmd
}
